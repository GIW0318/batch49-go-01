// Package task plans goal oriented action sequences for game agents and
// replays the chosen sequence one step at a time.
package task

import (
	"container/heap"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultMaxDepth caps how many actions one plan may contain when the
// options do not ask for a different depth.
const DefaultMaxDepth = 6

// Errors reported by the planner.
var (
	ErrBlankAgent      = errors.New("agent id is required")
	ErrBadAction       = errors.New("action definition is invalid")
	ErrDuplicateAction = errors.New("action id already registered")
	ErrUnknownAction   = errors.New("action id is not registered")
	ErrNoGoal          = errors.New("agent has no goal")
	ErrNoPlan          = errors.New("no action sequence reaches the goal")
	ErrPlanDone        = errors.New("planned sequence is already exhausted")
	ErrBadTime         = errors.New("logical time must not go backwards")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// State is a set of world facts that are true. A fact that is absent or set
// to false is considered false.
type State map[string]bool

// Action is one authored step an agent may take. Pre lists the facts that
// must hold before the action can run, Effect lists the facts it turns true.
type Action struct {
	ID     string
	Cost   int
	Pre    State
	Effect State
}

// Options tunes the planner.
type Options struct {
	// MaxDepth is the longest action sequence the search may return.
	// Values below one fall back to DefaultMaxDepth.
	MaxDepth int
}

// Plan is the ordered sequence chosen for one agent and one goal.
type Plan struct {
	Agent   string
	Goal    string
	Actions []string
	Cost    int
	Steps   int
}

// ActionResult describes the single step that Execute just performed.
type ActionResult struct {
	Agent     string
	Action    string
	Index     int
	Remaining int
	Done      bool
}

type goalRecord struct {
	Name    string `json:"name"`
	Desired State  `json:"desired"`
}

type runtimeState struct {
	Plan     Plan
	HasPlan  bool
	Cursor   int
	LastTime int64
	HasTime  bool
}

type execOutcome struct {
	Result ActionResult
	Err    error
}

type execTicket struct {
	at  int64
	out chan execOutcome
}

type execQueue struct {
	mu         sync.Mutex
	pending    []*execTicket
	processing bool
}

type snapshotGoal struct {
	Name    string `json:"name"`
	Desired State  `json:"desired"`
}

type snapshotRuntime struct {
	Plan     Plan  `json:"plan"`
	HasPlan  bool  `json:"has_plan"`
	Cursor   int   `json:"cursor"`
	LastTime int64 `json:"last_time"`
	HasTime  bool  `json:"has_time"`
}

type snapshotData struct {
	Version int                        `json:"version"`
	Actions []Action                   `json:"actions"`
	Goals   map[string]snapshotGoal    `json:"goals"`
	Cache   map[string]map[string]Plan `json:"cache"`
	Agents  map[string]snapshotRuntime `json:"agents"`
	Hits    int                        `json:"hits"`
	Misses  int                        `json:"misses"`
}

const snapshotVersion = 1

// GoapPlanner owns action definitions, per agent goals, cached plans and
// per agent execution cursors.
type GoapPlanner struct {
	mu      sync.Mutex
	opts    Options
	actions map[string]Action
	goals   map[string]goalRecord
	cache   map[string]map[string]Plan
	agents  map[string]*runtimeState
	queues  sync.Map
	hits    int
	misses  int
}

// NewGoapPlanner builds an empty planner.
func NewGoapPlanner(opts Options) *GoapPlanner {
	return &GoapPlanner{
		opts:    opts,
		actions: make(map[string]Action),
		goals:   make(map[string]goalRecord),
		cache:   make(map[string]map[string]Plan),
		agents:  make(map[string]*runtimeState),
	}
}

func cloneState(s State) State {
	if s == nil {
		return nil
	}
	cp := make(State, len(s))
	for k, v := range s {
		cp[k] = v
	}
	return cp
}

func clonePlan(plan Plan) Plan {
	plan.Actions = append([]string(nil), plan.Actions...)
	return plan
}

func cloneAction(a Action) Action {
	a.Pre = cloneState(a.Pre)
	a.Effect = cloneState(a.Effect)
	return a
}

func (p *GoapPlanner) maxDepth() int {
	if p.opts.MaxDepth < 1 {
		return DefaultMaxDepth
	}
	return p.opts.MaxDepth
}

func (p *GoapPlanner) getRuntime(agent string) *runtimeState {
	rt := p.agents[agent]
	if rt == nil {
		rt = &runtimeState{}
		p.agents[agent] = rt
	}
	return rt
}

func (p *GoapPlanner) execQueue(agent string) *execQueue {
	if value, ok := p.queues.Load(agent); ok {
		return value.(*execQueue)
	}
	queue := &execQueue{}
	actual, _ := p.queues.LoadOrStore(agent, queue)
	return actual.(*execQueue)
}

func (p *GoapPlanner) dropPlan(agent string) {
	rt := p.agents[agent]
	if rt == nil {
		return
	}
	rt.Plan = Plan{}
	rt.HasPlan = false
	rt.Cursor = 0
}

// AddAction registers one action definition.
func (p *GoapPlanner) AddAction(a Action) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if strings.TrimSpace(a.ID) == "" || a.Cost < 0 || len(a.Effect) == 0 {
		return ErrBadAction
	}
	if _, ok := p.actions[a.ID]; ok {
		return ErrDuplicateAction
	}
	p.actions[a.ID] = cloneAction(a)
	p.cache = make(map[string]map[string]Plan)
	return nil
}

// RemoveAction drops one action definition.
func (p *GoapPlanner) RemoveAction(id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, ok := p.actions[id]; !ok {
		return ErrUnknownAction
	}
	delete(p.actions, id)
	p.cache = make(map[string]map[string]Plan)
	for agent, rt := range p.agents {
		if !rt.HasPlan {
			continue
		}
		for _, actionID := range rt.Plan.Actions {
			if actionID == id {
				p.dropPlan(agent)
				break
			}
		}
	}
	return nil
}

func (p *GoapPlanner) orderedActionsLocked() []Action {
	actions := make([]Action, 0, len(p.actions))
	for _, a := range p.actions {
		actions = append(actions, cloneAction(a))
	}
	sort.Slice(actions, func(i, j int) bool {
		return actions[i].ID < actions[j].ID
	})
	return actions
}

// Actions returns a copy of the registered actions ordered by id.
func (p *GoapPlanner) Actions() []Action {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.orderedActionsLocked()
}

// SetGoal stores the desired facts for one agent.
func (p *GoapPlanner) SetGoal(agent, goal string, desired State) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if strings.TrimSpace(agent) == "" {
		return ErrBlankAgent
	}
	p.goals[agent] = goalRecord{Name: goal, Desired: cloneState(desired)}
	delete(p.cache, agent)
	p.dropPlan(agent)
	return nil
}

// Goal returns a copy of the stored goal of one agent.
func (p *GoapPlanner) Goal(agent string) (State, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	g, ok := p.goals[agent]
	if !ok {
		return nil, false
	}
	return cloneState(g.Desired), true
}

func stateKey(state State) string {
	facts := make([]string, 0, len(state))
	for name, value := range state {
		if value {
			facts = append(facts, name)
		}
	}
	sort.Strings(facts)
	data, _ := json.Marshal(facts)
	return string(data)
}

func searchKey(state State, depth int) string {
	return fmt.Sprintf("%d:%s", depth, stateKey(state))
}

func applyAction(state State, a Action) State {
	next := cloneState(state)
	for fact := range a.Effect {
		next[fact] = true
	}
	return next
}

func preconditionsHold(state State, a Action) bool {
	for fact := range a.Pre {
		if !state[fact] {
			return false
		}
	}
	return true
}

func goalMet(state State, desired State) bool {
	if len(desired) == 0 {
		return true
	}
	for fact, want := range desired {
		if state[fact] != want {
			return false
		}
	}
	return true
}

type searchRank struct {
	cost int
	path []string
}

func pathLess(a, b []string) bool {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return len(a) < len(b)
}

func rankLess(a, b searchRank) bool {
	if a.cost != b.cost {
		return a.cost < b.cost
	}
	if len(a.path) != len(b.path) {
		return len(a.path) < len(b.path)
	}
	return pathLess(a.path, b.path)
}

func rankEqual(a, b searchRank) bool {
	if a.cost != b.cost || len(a.path) != len(b.path) {
		return false
	}
	for i := range a.path {
		if a.path[i] != b.path[i] {
			return false
		}
	}
	return true
}

type searchNode struct {
	state State
	path  []string
	cost  int
}

func nodeRank(n *searchNode) searchRank {
	return searchRank{cost: n.cost, path: n.path}
}

type searchQueue []*searchNode

func (q searchQueue) Len() int { return len(q) }
func (q searchQueue) Less(i, j int) bool {
	return rankLess(nodeRank(q[i]), nodeRank(q[j]))
}
func (q searchQueue) Swap(i, j int) { q[i], q[j] = q[j], q[i] }
func (q *searchQueue) Push(x any) {
	*q = append(*q, x.(*searchNode))
}
func (q *searchQueue) Pop() any {
	old := *q
	n := len(old)
	item := old[n-1]
	old[n-1] = nil
	*q = old[:n-1]
	return item
}

func appendPath(path []string, id string) []string {
	next := make([]string, len(path)+1)
	copy(next, path)
	next[len(path)] = id
	return next
}

func (p *GoapPlanner) relevantFactsLocked(desired State) map[string]struct{} {
	facts := make(map[string]struct{})
	for _, a := range p.actions {
		for fact := range a.Pre {
			facts[fact] = struct{}{}
		}
		for fact := range a.Effect {
			facts[fact] = struct{}{}
		}
	}
	for fact := range desired {
		facts[fact] = struct{}{}
	}
	return facts
}

func initialSearchState(world State, relevant map[string]struct{}) State {
	state := make(State, len(relevant))
	for fact := range relevant {
		if world[fact] {
			state[fact] = true
		}
	}
	return state
}

func (p *GoapPlanner) searchLocked(start State, desired State) ([]string, int, bool) {
	actions := p.orderedActionsLocked()
	maxDepth := p.maxDepth()

	startNode := &searchNode{state: start}
	queue := searchQueue{startNode}
	heap.Init(&queue)

	best := map[string]searchRank{
		searchKey(start, 0): {cost: 0, path: nil},
	}

	for queue.Len() > 0 {
		node := heap.Pop(&queue).(*searchNode)
		stateID := stateKey(node.state)
		depth := len(node.path)
		bucket := searchKey(node.state, depth)
		if current, ok := best[bucket]; !ok || !rankEqual(current, nodeRank(node)) {
			continue
		}

		if goalMet(node.state, desired) {
			return node.path, node.cost, true
		}
		if depth >= maxDepth {
			continue
		}

		for _, action := range actions {
			if !preconditionsHold(node.state, action) {
				continue
			}
			next := applyAction(node.state, action)
			if stateKey(next) == stateID {
				continue
			}

			nextPath := appendPath(node.path, action.ID)
			nextCost := node.cost + action.Cost
			nextBucket := searchKey(next, depth+1)
			nextRank := searchRank{cost: nextCost, path: nextPath}

			if current, exists := best[nextBucket]; exists && !rankLess(nextRank, current) {
				continue
			}
			best[nextBucket] = nextRank
			heap.Push(&queue, &searchNode{
				state: next,
				path:  nextPath,
				cost:  nextCost,
			})
		}
	}
	return nil, 0, false
}

// Plan searches the cheapest applicable sequence for one agent and world.
func (p *GoapPlanner) Plan(agent string, world State) (Plan, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	goal, ok := p.goals[agent]
	if !ok {
		return Plan{}, ErrNoGoal
	}

	relevant := p.relevantFactsLocked(goal.Desired)
	start := initialSearchState(world, relevant)
	key := stateKey(start)

	if cached, ok := p.cache[agent][key]; ok {
		p.hits++
		plan := clonePlan(cached)
		rt := p.getRuntime(agent)
		rt.Plan = clonePlan(plan)
		rt.HasPlan = true
		rt.Cursor = 0
		return plan, nil
	}

	p.misses++
	ids, cost, found := p.searchLocked(start, goal.Desired)
	if !found {
		return Plan{}, ErrNoPlan
	}

	plan := Plan{
		Agent:   agent,
		Goal:    goal.Name,
		Actions: append([]string(nil), ids...),
		Cost:    cost,
		Steps:   len(ids),
	}
	if p.cache[agent] == nil {
		p.cache[agent] = make(map[string]Plan)
	}
	p.cache[agent][key] = clonePlan(plan)

	rt := p.getRuntime(agent)
	rt.Plan = clonePlan(plan)
	rt.HasPlan = true
	rt.Cursor = 0
	return clonePlan(plan), nil
}

func (p *GoapPlanner) executeStep(agent string, at int64) (ActionResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	rt := p.agents[agent]
	if rt == nil || !rt.HasPlan {
		return ActionResult{}, ErrNoPlan
	}
	if rt.HasTime && at < rt.LastTime {
		return ActionResult{}, ErrBadTime
	}
	if rt.Cursor >= len(rt.Plan.Actions) {
		return ActionResult{}, ErrPlanDone
	}

	rt.Cursor++
	if !rt.HasTime || at > rt.LastTime {
		rt.LastTime = at
	}
	rt.HasTime = true

	result := ActionResult{
		Agent:     agent,
		Action:    rt.Plan.Actions[rt.Cursor-1],
		Index:     rt.Cursor,
		Remaining: len(rt.Plan.Actions) - rt.Cursor,
		Done:      rt.Cursor == len(rt.Plan.Actions),
	}
	return result, nil
}

func (q *execQueue) waitForBatch() {
	const coalesceWindow = 500 * time.Microsecond
	time.Sleep(coalesceWindow)
}

func (p *GoapPlanner) runExecQueue(agent string, q *execQueue) {
	for {
		q.waitForBatch()

		q.mu.Lock()
		batch := q.pending
		q.pending = nil
		q.mu.Unlock()

		if len(batch) == 0 {
			q.mu.Lock()
			if len(q.pending) == 0 {
				q.processing = false
				q.mu.Unlock()
				return
			}
			q.mu.Unlock()
			continue
		}

		sort.SliceStable(batch, func(i, j int) bool {
			return batch[i].at < batch[j].at
		})
		for _, ticket := range batch {
			result, err := p.executeStep(agent, ticket.at)
			ticket.out <- execOutcome{Result: result, Err: err}
		}

		q.mu.Lock()
		empty := len(q.pending) == 0
		if empty {
			q.processing = false
		}
		q.mu.Unlock()
		if empty {
			return
		}
	}
}

// Execute performs the next stored step of one agent.
func (p *GoapPlanner) Execute(agent string, at int64) (ActionResult, error) {
	q := p.execQueue(agent)
	ticket := &execTicket{at: at, out: make(chan execOutcome, 1)}

	q.mu.Lock()
	q.pending = append(q.pending, ticket)
	processor := !q.processing
	if processor {
		q.processing = true
	}
	q.mu.Unlock()

	if processor {
		p.runExecQueue(agent, q)
	}
	outcome := <-ticket.out
	return outcome.Result, outcome.Err
}

// Cursor reports how many steps of the stored plan already ran.
func (p *GoapPlanner) Cursor(agent string) int {
	p.mu.Lock()
	defer p.mu.Unlock()

	rt := p.agents[agent]
	if rt == nil {
		return 0
	}
	return rt.Cursor
}

// Invalidate drops the cached and stored plan of one agent.
func (p *GoapPlanner) Invalidate(agent string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	delete(p.cache, agent)
	p.dropPlan(agent)
	return nil
}

// CacheStats reports cache hits and misses since the planner was created.
func (p *GoapPlanner) CacheStats() (hits, misses int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.hits, p.misses
}

// Snapshot serialises actions, goals, cached plans and cursors.
func (p *GoapPlanner) Snapshot() ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	data := snapshotData{
		Version: snapshotVersion,
		Actions: p.orderedActionsLocked(),
		Goals:   make(map[string]snapshotGoal, len(p.goals)),
		Cache:   make(map[string]map[string]Plan, len(p.cache)),
		Agents:  make(map[string]snapshotRuntime, len(p.agents)),
		Hits:    p.hits,
		Misses:  p.misses,
	}
	for agent, goal := range p.goals {
		data.Goals[agent] = snapshotGoal{Name: goal.Name, Desired: cloneState(goal.Desired)}
	}
	for agent, plans := range p.cache {
		copied := make(map[string]Plan, len(plans))
		for key, plan := range plans {
			copied[key] = clonePlan(plan)
		}
		data.Cache[agent] = copied
	}
	for agent, rt := range p.agents {
		data.Agents[agent] = snapshotRuntime{
			Plan:     clonePlan(rt.Plan),
			HasPlan:  rt.HasPlan,
			Cursor:   rt.Cursor,
			LastTime: rt.LastTime,
			HasTime:  rt.HasTime,
		}
	}

	payload, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	return payload, nil
}

func validatePlan(plan Plan, agent string, actions map[string]Action) error {
	if plan.Agent != agent {
		return fmt.Errorf("plan agent %q does not match key %q", plan.Agent, agent)
	}
	if plan.Steps != len(plan.Actions) || plan.Steps < 0 || plan.Cost < 0 {
		return fmt.Errorf("plan has inconsistent step or cost fields")
	}
	expectedCost := 0
	for _, id := range plan.Actions {
		a, ok := actions[id]
		if !ok {
			return fmt.Errorf("plan refers to unknown action %q", id)
		}
		expectedCost += a.Cost
	}
	if expectedCost != plan.Cost {
		return fmt.Errorf("plan cost %d does not match actions cost %d", plan.Cost, expectedCost)
	}
	return nil
}

func buildSnapshotState(data snapshotData) (
	map[string]Action,
	map[string]goalRecord,
	map[string]map[string]Plan,
	map[string]*runtimeState,
	error,
) {
	if data.Version != snapshotVersion {
		return nil, nil, nil, nil, fmt.Errorf("unsupported snapshot version %d", data.Version)
	}

	actions := make(map[string]Action, len(data.Actions))
	for _, a := range data.Actions {
		if strings.TrimSpace(a.ID) == "" || a.Cost < 0 || len(a.Effect) == 0 {
			return nil, nil, nil, nil, fmt.Errorf("snapshot contains an invalid action")
		}
		if _, exists := actions[a.ID]; exists {
			return nil, nil, nil, nil, fmt.Errorf("snapshot contains duplicate action %q", a.ID)
		}
		actions[a.ID] = cloneAction(a)
	}

	goals := make(map[string]goalRecord, len(data.Goals))
	for agent, goal := range data.Goals {
		if strings.TrimSpace(agent) == "" {
			return nil, nil, nil, nil, fmt.Errorf("snapshot contains a blank agent goal")
		}
		goals[agent] = goalRecord{Name: goal.Name, Desired: cloneState(goal.Desired)}
	}

	cache := make(map[string]map[string]Plan, len(data.Cache))
	for agent, plans := range data.Cache {
		if _, hasGoal := goals[agent]; !hasGoal {
			return nil, nil, nil, nil, fmt.Errorf("snapshot cache exists for agent %q without a goal", agent)
		}
		copied := make(map[string]Plan, len(plans))
		for key, plan := range plans {
			if err := validatePlan(plan, agent, actions); err != nil {
				return nil, nil, nil, nil, err
			}
			copied[key] = clonePlan(plan)
		}
		cache[agent] = copied
	}

	agents := make(map[string]*runtimeState, len(data.Agents))
	for agent, saved := range data.Agents {
		rt := &runtimeState{
			Cursor:   saved.Cursor,
			LastTime: saved.LastTime,
			HasTime:  saved.HasTime,
		}
		if saved.HasPlan {
			if _, hasGoal := goals[agent]; !hasGoal {
				return nil, nil, nil, nil, fmt.Errorf("snapshot plan exists for agent %q without a goal", agent)
			}
			if err := validatePlan(saved.Plan, agent, actions); err != nil {
				return nil, nil, nil, nil, err
			}
			if saved.Cursor < 0 || saved.Cursor > len(saved.Plan.Actions) {
				return nil, nil, nil, nil, fmt.Errorf("snapshot cursor is out of range")
			}
			rt.Plan = clonePlan(saved.Plan)
			rt.HasPlan = true
		} else if saved.Cursor != 0 {
			return nil, nil, nil, nil, fmt.Errorf("snapshot cursor exists without a plan")
		}
		agents[agent] = rt
	}

	return actions, goals, cache, agents, nil
}

// Restore replaces the whole planner state with a snapshot.
func (p *GoapPlanner) Restore(data []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	var decoded snapshotData
	if err := json.Unmarshal(data, &decoded); err != nil {
		return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	actions, goals, cache, agents, err := buildSnapshotState(decoded)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}

	p.actions = actions
	p.goals = goals
	p.cache = cache
	p.agents = agents
	p.hits = decoded.Hits
	p.misses = decoded.Misses
	return nil
}
