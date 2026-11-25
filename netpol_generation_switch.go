// Package task stages network policy generations and switches the live rule
// set atomically while evaluations already in flight finish on their own
// generation.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
)

// Policy actions and the synthetic rule reported for unmatched traffic.
const (
	ActionAllow     = "allow"
	ActionDeny      = "deny"
	RuleDefaultDeny = "default_deny"
)

// Errors reported by the switcher.
var (
	ErrBlankWorkload     = errors.New("workload and labels are required")
	ErrBadLabels         = errors.New("workload labels are invalid")
	ErrBlankPolicyName   = errors.New("policy name is required")
	ErrBadPort           = errors.New("policy port is out of range")
	ErrBadAction         = errors.New("policy action is unknown")
	ErrTooManyPolicies   = errors.New("policy limit reached for one generation")
	ErrUnknownWorkload   = errors.New("unknown workload")
	ErrUnknownGeneration = errors.New("unknown policy generation")
	ErrAlreadyActive     = errors.New("generation is already active")
	ErrNoRollback        = errors.New("no committed generation is available for rollback")
	ErrGenerationLimit   = errors.New("generation history limit reached")
	ErrUnknownToken      = errors.New("unknown evaluation token")
	ErrBadSnapshot       = errors.New("snapshot payload is invalid")
)

// Policy is one selector based rule. An empty Ports slice means every port.
type Policy struct {
	Name     string            `json:"name"`
	Selector map[string]string `json:"selector"`
	Ports    []int             `json:"ports"`
	Action   string            `json:"action"`
	Priority int               `json:"priority"`
}

type policySet map[string]struct{}

// Generation is one staged rule set. The resolved workload assignment is
// captured when the generation is staged, so later label edits only affect
// generations staged afterwards.
type Generation struct {
	ID       uint64   `json:"id"`
	Policies []Policy `json:"policies"`
	Digest   string   `json:"digest"`

	// assignment freezes workload -> matching policy names at stage time. It is
	// deliberately unexported: callers see immutable snapshots through Stage,
	// Active and History while Diff can still use the frozen table.
	assignment map[string]policySet
}

// Diff buckets workloads by how their resolved assignment moved between two
// generations.
type Diff struct {
	From    uint64   `json:"from"`
	To      uint64   `json:"to"`
	Added   []string `json:"added"`
	Removed []string `json:"removed"`
	Changed []string `json:"changed"`
}

// Decision is the result of one evaluation.
type Decision struct {
	Workload string `json:"workload"`
	Port     int    `json:"port"`
	Gen      uint64 `json:"gen"`
	Action   string `json:"action"`
	Rule     string `json:"rule"`
}

// EvalToken pins one started evaluation to the generation active at begin time.
type EvalToken struct {
	ID       uint64 `json:"id"`
	Workload string `json:"workload"`
	Port     int    `json:"port"`
	Gen      uint64 `json:"gen"`
}

type evaluation struct {
	workload string
	port     int
	gen      Generation
}

// Limits bounds the switcher tables.
type Limits struct {
	MaxGenerations int
	MaxPolicies    int
	MaxWorkloads   int
}

// PolicyGeneration owns workload labels, staged generations, the active pointer
// and the one step rollback slot.
type PolicyGeneration struct {
	mu     sync.Mutex
	limits Limits

	workloads map[string]map[string]string
	history   []Generation
	active    Generation

	rollback     Generation
	hasRollback  bool
	rollbackUsed bool

	nextID    uint64
	committed map[uint64]bool

	nextEvalID  uint64
	evaluations map[uint64]evaluation
}

// NewPolicyGeneration builds an empty switcher. Generation zero is the implicit
// empty generation that denies everything.
func NewPolicyGeneration(limits Limits) *PolicyGeneration {
	return &PolicyGeneration{
		limits:      limits,
		workloads:   make(map[string]map[string]string),
		history:     make([]Generation, 0),
		active:      emptyGeneration(0),
		nextID:      1,
		committed:   make(map[uint64]bool),
		nextEvalID:  1,
		evaluations: make(map[uint64]evaluation),
	}
}

// SetWorkload installs or replaces the labels of one workload.
func (p *PolicyGeneration) SetWorkload(workload string, labels map[string]string) error {
	if strings.TrimSpace(workload) == "" || labels == nil {
		return ErrBlankWorkload
	}
	if len(labels) == 0 {
		return ErrBadLabels
	}
	for key := range labels {
		if strings.TrimSpace(key) == "" {
			return ErrBadLabels
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if _, exists := p.workloads[workload]; !exists &&
		p.limits.MaxWorkloads > 0 && len(p.workloads) >= p.limits.MaxWorkloads {
		return ErrGenerationLimit
	}

	copied := make(map[string]string, len(labels))
	for key, value := range labels {
		copied[key] = value
	}
	p.workloads[workload] = copied
	return nil
}

// RemoveWorkload drops one workload from the label table.
func (p *PolicyGeneration) RemoveWorkload(workload string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.workloads[workload]; !ok {
		return false
	}
	delete(p.workloads, workload)
	return true
}

// Stage compiles policies against the current workload labels and returns a new
// monotonically numbered generation.
func (p *PolicyGeneration) Stage(policies []Policy) (Generation, error) {
	if err := validatePolicies(policies, p.limits.MaxPolicies); err != nil {
		return Generation{}, err
	}
	copied := clonePolicies(policies)

	p.mu.Lock()
	defer p.mu.Unlock()

	assignment := resolveAssignments(copied, p.workloads)
	id := p.nextID
	p.nextID++

	g := Generation{
		ID:         id,
		Policies:   copied,
		assignment: assignment,
	}
	g.Digest = computeDigest(g.Policies, g.assignment)

	p.history = append(p.history, g)
	if p.limits.MaxGenerations > 0 && len(p.history) > p.limits.MaxGenerations {
		keep := p.limits.MaxGenerations
		trimmed := make([]Generation, keep)
		copy(trimmed, p.history[len(p.history)-keep:])
		p.history = trimmed
	}
	return cloneGeneration(g), nil
}

// Diff compares the resolved workload assignments of two generations.
func (p *PolicyGeneration) Diff(cur, next Generation) (Diff, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	curAssignment, err := p.assignmentFor(cur)
	if err != nil {
		return Diff{}, err
	}
	nextAssignment, err := p.assignmentFor(next)
	if err != nil {
		return Diff{}, err
	}

	diff := Diff{From: cur.ID, To: next.ID}
	seen := make(map[string]struct{}, len(curAssignment)+len(nextAssignment))
	for workload := range curAssignment {
		seen[workload] = struct{}{}
	}
	for workload := range nextAssignment {
		seen[workload] = struct{}{}
	}

	for workload := range seen {
		before := curAssignment[workload]
		after := nextAssignment[workload]
		beforeEmpty := len(before) == 0
		afterEmpty := len(after) == 0

		switch {
		case beforeEmpty && afterEmpty:
			continue
		case beforeEmpty:
			diff.Added = append(diff.Added, workload)
		case afterEmpty:
			diff.Removed = append(diff.Removed, workload)
		case !samePolicySet(before, after):
			diff.Changed = append(diff.Changed, workload)
		}
	}

	sort.Strings(diff.Added)
	sort.Strings(diff.Removed)
	sort.Strings(diff.Changed)
	return diff, nil
}

// Commit makes one staged generation active.
func (p *PolicyGeneration) Commit(gen Generation) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	var target *Generation
	for i := range p.history {
		if p.history[i].ID == gen.ID {
			target = &p.history[i]
			break
		}
	}
	if target == nil {
		return ErrUnknownGeneration
	}
	if p.active.ID == gen.ID {
		return ErrAlreadyActive
	}

	previous := cloneGeneration(p.active)
	p.rollback = previous
	p.hasRollback = true
	p.rollbackUsed = false
	p.active = cloneGeneration(*target)
	p.committed[gen.ID] = true
	return nil
}

// Rollback returns to the generation that was active before the last commit.
func (p *PolicyGeneration) Rollback() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.rollbackUsed || !p.hasRollback {
		return ErrNoRollback
	}
	p.active = cloneGeneration(p.rollback)
	p.hasRollback = false
	p.rollbackUsed = true
	return nil
}

// Active returns the generation currently serving new evaluations.
func (p *PolicyGeneration) Active() Generation {
	p.mu.Lock()
	defer p.mu.Unlock()
	return cloneGeneration(p.active)
}

// History lists staged generations in ascending id order.
func (p *PolicyGeneration) History() []Generation {
	p.mu.Lock()
	defer p.mu.Unlock()

	out := make([]Generation, len(p.history))
	for i := range p.history {
		out[i] = cloneGeneration(p.history[i])
	}
	return out
}

// BeginEvaluation pins a new evaluation to the active generation.
func (p *PolicyGeneration) BeginEvaluation(workload string, port int) (EvalToken, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, ok := p.workloads[workload]; !ok {
		return EvalToken{}, ErrUnknownWorkload
	}
	if port < 1 || port > 65535 {
		return EvalToken{}, ErrBadPort
	}

	id := p.nextEvalID
	p.nextEvalID++
	p.evaluations[id] = evaluation{
		workload: workload,
		port:     port,
		gen:      cloneGeneration(p.active),
	}
	return EvalToken{ID: id, Workload: workload, Port: port, Gen: p.active.ID}, nil
}

// FinishEvaluation resolves a pinned evaluation with its own generation.
func (p *PolicyGeneration) FinishEvaluation(tok EvalToken) (Decision, error) {
	p.mu.Lock()
	eval, ok := p.evaluations[tok.ID]
	if ok {
		delete(p.evaluations, tok.ID)
	}
	p.mu.Unlock()

	if !ok {
		return Decision{}, ErrUnknownToken
	}
	return evaluate(eval.gen, eval.workload, eval.port), nil
}

const snapshotVersion = 1

type snapshotGeneration struct {
	ID         uint64              `json:"id"`
	Policies   []Policy            `json:"policies"`
	Digest     string              `json:"digest"`
	Assignment map[string][]string `json:"assignment"`
}

type snapshotData struct {
	Version      int                          `json:"version"`
	Workloads    map[string]map[string]string `json:"workloads"`
	Generations  []snapshotGeneration         `json:"generations"`
	Active       snapshotGeneration           `json:"active"`
	Rollback     *snapshotGeneration          `json:"rollback,omitempty"`
	RollbackUsed bool                         `json:"rollback_used"`
	NextID       uint64                       `json:"next_id"`
	Committed    []uint64                     `json:"committed"`
}

type restoredState struct {
	workloads    map[string]map[string]string
	history      []Generation
	active       Generation
	rollback     Generation
	hasRollback  bool
	rollbackUsed bool
	nextID       uint64
	committed    map[uint64]bool
}

// Snapshot serialises labels, generations, the active pointer and rollback state.
func (p *PolicyGeneration) Snapshot() ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	snap := snapshotData{
		Version:      snapshotVersion,
		Workloads:    cloneWorkloads(p.workloads),
		Generations:  make([]snapshotGeneration, 0, len(p.history)),
		Active:       generationToSnapshot(p.active),
		RollbackUsed: p.rollbackUsed,
		NextID:       p.nextID,
		Committed:    make([]uint64, 0, len(p.committed)),
	}
	for i := range p.history {
		snap.Generations = append(snap.Generations, generationToSnapshot(p.history[i]))
	}
	if p.hasRollback {
		r := generationToSnapshot(p.rollback)
		snap.Rollback = &r
	}
	for id := range p.committed {
		snap.Committed = append(snap.Committed, id)
	}
	sort.Slice(snap.Committed, func(i, j int) bool {
		return snap.Committed[i] < snap.Committed[j]
	})

	return json.Marshal(snap)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (p *PolicyGeneration) Restore(data []byte) error {
	state, err := parseSnapshot(data)
	if err != nil {
		return ErrBadSnapshot
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	p.workloads = state.workloads
	p.history = state.history
	p.active = state.active
	p.rollback = state.rollback
	p.hasRollback = state.hasRollback
	p.rollbackUsed = state.rollbackUsed
	p.nextID = state.nextID
	p.committed = state.committed

	// Evaluations belong to live requests, not to the restored process.
	p.nextEvalID = 1
	p.evaluations = make(map[uint64]evaluation)
	return nil
}

func emptyGeneration(id uint64) Generation {
	return Generation{ID: id}
}

func validatePolicies(policies []Policy, max int) error {
	if max > 0 && len(policies) > max {
		return ErrTooManyPolicies
	}
	for _, policy := range policies {
		if strings.TrimSpace(policy.Name) == "" {
			return ErrBlankPolicyName
		}
		switch policy.Action {
		case ActionAllow, ActionDeny:
		default:
			return ErrBadAction
		}
		for _, port := range policy.Ports {
			if port < 1 || port > 65535 {
				return ErrBadPort
			}
		}
	}
	return nil
}

func clonePolicies(policies []Policy) []Policy {
	if policies == nil {
		return nil
	}
	out := make([]Policy, len(policies))
	for i, policy := range policies {
		out[i] = Policy{
			Name:     policy.Name,
			Action:   policy.Action,
			Priority: policy.Priority,
		}
		if policy.Selector != nil {
			out[i].Selector = make(map[string]string, len(policy.Selector))
			for key, value := range policy.Selector {
				out[i].Selector[key] = value
			}
		}
		if policy.Ports != nil {
			out[i].Ports = append([]int(nil), policy.Ports...)
		}
	}
	return out
}

func cloneGeneration(g Generation) Generation {
	out := Generation{
		ID:       g.ID,
		Policies: clonePolicies(g.Policies),
		Digest:   g.Digest,
	}
	if g.assignment != nil {
		out.assignment = make(map[string]policySet, len(g.assignment))
		for workload, policies := range g.assignment {
			set := make(policySet, len(policies))
			for name := range policies {
				set[name] = struct{}{}
			}
			out.assignment[workload] = set
		}
	}
	return out
}

func cloneWorkloads(workloads map[string]map[string]string) map[string]map[string]string {
	out := make(map[string]map[string]string, len(workloads))
	for workload, labels := range workloads {
		copied := make(map[string]string, len(labels))
		for key, value := range labels {
			copied[key] = value
		}
		out[workload] = copied
	}
	return out
}

func resolveAssignments(policies []Policy, workloads map[string]map[string]string) map[string]policySet {
	assignment := make(map[string]policySet, len(workloads))
	for workload, labels := range workloads {
		set := make(policySet)
		for _, policy := range policies {
			if selectorMatches(policy.Selector, labels) {
				set[policy.Name] = struct{}{}
			}
		}
		assignment[workload] = set
	}
	return assignment
}

func selectorMatches(selector, labels map[string]string) bool {
	for key, want := range selector {
		got, ok := labels[key]
		if !ok || got != want {
			return false
		}
	}
	return true
}

func (p *PolicyGeneration) assignmentFor(g Generation) (map[string]policySet, error) {
	if g.assignment != nil {
		return g.assignment, nil
	}
	if g.ID == 0 {
		return nil, nil
	}
	for i := range p.history {
		if p.history[i].ID == g.ID {
			return p.history[i].assignment, nil
		}
	}
	return nil, ErrUnknownGeneration
}

func samePolicySet(a, b policySet) bool {
	if len(a) != len(b) {
		return false
	}
	for name := range a {
		if _, ok := b[name]; !ok {
			return false
		}
	}
	return true
}

func evaluate(gen Generation, workload string, port int) Decision {
	decision := Decision{
		Workload: workload,
		Port:     port,
		Gen:      gen.ID,
		Action:   ActionDeny,
		Rule:     RuleDefaultDeny,
	}

	matched := gen.assignment[workload]
	if len(matched) == 0 {
		return decision
	}

	var best *Policy
	for i := range gen.Policies {
		policy := &gen.Policies[i]
		if _, ok := matched[policy.Name]; !ok {
			continue
		}
		if !portMatches(policy.Ports, port) {
			continue
		}
		if best == nil ||
			policy.Priority > best.Priority ||
			(policy.Priority == best.Priority && policy.Name < best.Name) {
			best = policy
		}
	}
	if best == nil {
		return decision
	}
	decision.Action = best.Action
	decision.Rule = best.Name
	return decision
}

func portMatches(ports []int, port int) bool {
	if len(ports) == 0 {
		return true
	}
	for _, candidate := range ports {
		if candidate == port {
			return true
		}
	}
	return false
}

func computeDigest(policies []Policy, assignment map[string]policySet) string {
	type workloadEntry struct {
		Workload string   `json:"workload"`
		Policies []string `json:"policies"`
	}
	workloads := make([]string, 0, len(assignment))
	for workload := range assignment {
		workloads = append(workloads, workload)
	}
	sort.Strings(workloads)

	entries := make([]workloadEntry, 0, len(workloads))
	for _, workload := range workloads {
		names := make([]string, 0, len(assignment[workload]))
		for name := range assignment[workload] {
			names = append(names, name)
		}
		sort.Strings(names)
		entries = append(entries, workloadEntry{Workload: workload, Policies: names})
	}

	payload := struct {
		Policies   []Policy        `json:"policies"`
		Assignment []workloadEntry `json:"assignment"`
	}{
		Policies:   policies,
		Assignment: entries,
	}
	encoded, _ := json.Marshal(payload)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func generationToSnapshot(g Generation) snapshotGeneration {
	out := snapshotGeneration{
		ID:       g.ID,
		Policies: clonePolicies(g.Policies),
		Digest:   g.Digest,
	}
	if g.assignment != nil {
		out.Assignment = make(map[string][]string, len(g.assignment))
		for workload, set := range g.assignment {
			names := make([]string, 0, len(set))
			for name := range set {
				names = append(names, name)
			}
			sort.Strings(names)
			out.Assignment[workload] = names
		}
	}
	return out
}

func parseSnapshot(data []byte) (*restoredState, error) {
	var snap snapshotData
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, ErrBadSnapshot
	}
	if snap.Version != snapshotVersion {
		return nil, ErrBadSnapshot
	}

	state := &restoredState{
		workloads: make(map[string]map[string]string, len(snap.Workloads)),
		history:   make([]Generation, 0, len(snap.Generations)),
		committed: make(map[uint64]bool, len(snap.Committed)),
	}
	for workload, labels := range snap.Workloads {
		if strings.TrimSpace(workload) == "" || labels == nil {
			return nil, ErrBadSnapshot
		}
		copied := make(map[string]string, len(labels))
		for key, value := range labels {
			if strings.TrimSpace(key) == "" {
				return nil, ErrBadSnapshot
			}
			copied[key] = value
		}
		state.workloads[workload] = copied
	}

	maxID := uint64(0)
	var previousID uint64
	for i, snapGen := range snap.Generations {
		if snapGen.ID == 0 || (i > 0 && snapGen.ID <= previousID) {
			return nil, ErrBadSnapshot
		}
		gen, err := snapshotToGeneration(snapGen)
		if err != nil {
			return nil, err
		}
		state.history = append(state.history, gen)
		previousID = snapGen.ID
		if snapGen.ID > maxID {
			maxID = snapGen.ID
		}
	}

	for _, id := range snap.Committed {
		if id == 0 {
			return nil, ErrBadSnapshot
		}
		state.committed[id] = true
		if id > maxID {
			maxID = id
		}
	}

	active, err := snapshotToGeneration(snap.Active)
	if err != nil {
		return nil, err
	}
	if active.ID == 0 {
		if len(active.Policies) != 0 || len(active.assignment) != 0 {
			return nil, ErrBadSnapshot
		}
		active = emptyGeneration(0)
	} else {
		if !state.committed[active.ID] || !matchesHistory(state.history, active) {
			return nil, ErrBadSnapshot
		}
		if active.ID > maxID {
			maxID = active.ID
		}
	}
	state.active = active

	if snap.Rollback != nil {
		rollback, err := snapshotToGeneration(*snap.Rollback)
		if err != nil {
			return nil, err
		}
		if rollback.ID == 0 {
			if len(rollback.Policies) != 0 || len(rollback.assignment) != 0 {
				return nil, ErrBadSnapshot
			}
			rollback = emptyGeneration(0)
		} else {
			if !state.committed[rollback.ID] || !matchesHistory(state.history, rollback) {
				return nil, ErrBadSnapshot
			}
			if rollback.ID > maxID {
				maxID = rollback.ID
			}
		}
		state.rollback = rollback
		state.hasRollback = true
	}
	state.rollbackUsed = snap.RollbackUsed

	if snap.NextID == 0 || snap.NextID <= maxID {
		return nil, ErrBadSnapshot
	}
	state.nextID = snap.NextID
	return state, nil
}

func snapshotToGeneration(snap snapshotGeneration) (Generation, error) {
	if err := validatePolicies(snap.Policies, 0); err != nil {
		return Generation{}, ErrBadSnapshot
	}

	gen := Generation{
		ID:       snap.ID,
		Policies: clonePolicies(snap.Policies),
		Digest:   snap.Digest,
	}
	if snap.Assignment != nil {
		gen.assignment = make(map[string]policySet, len(snap.Assignment))
		for workload, names := range snap.Assignment {
			if strings.TrimSpace(workload) == "" {
				return Generation{}, ErrBadSnapshot
			}
			set := make(policySet, len(names))
			for _, name := range names {
				set[name] = struct{}{}
			}
			gen.assignment[workload] = set
		}
	}
	if gen.ID != 0 && gen.Digest == "" {
		gen.Digest = computeDigest(gen.Policies, gen.assignment)
	}
	return gen, nil
}

func matchesHistory(history []Generation, gen Generation) bool {
	for i := range history {
		if history[i].ID != gen.ID {
			continue
		}
		return sameGenerationContent(history[i], gen)
	}
	// A committed generation may legitimately have been trimmed from history.
	return true
}

func sameGenerationContent(a, b Generation) bool {
	if a.ID != b.ID || len(a.Policies) != len(b.Policies) {
		return false
	}
	for i := range a.Policies {
		if a.Policies[i].Name != b.Policies[i].Name ||
			a.Policies[i].Action != b.Policies[i].Action ||
			a.Policies[i].Priority != b.Policies[i].Priority ||
			!sameIntSlice(a.Policies[i].Ports, b.Policies[i].Ports) ||
			!sameStringMap(a.Policies[i].Selector, b.Policies[i].Selector) {
			return false
		}
	}
	if len(a.assignment) != len(b.assignment) {
		return false
	}
	for workload, aSet := range a.assignment {
		bSet, ok := b.assignment[workload]
		if !ok || !samePolicySet(aSet, bSet) {
			return false
		}
	}
	return true
}

func sameIntSlice(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameStringMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		other, ok := b[key]
		if !ok || other != value {
			return false
		}
	}
	return true
}
