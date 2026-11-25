// Package task scores candidate behaviors with integer utility curves and
// picks the behavior an agent should run next.
package task

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"sync"
)

// Supported curve kinds.
const (
	CurveLinear   = "linear"
	CurveStep     = "step"
	CurveInverseS = "inverse_s"
)

// Score bounds used by every curve.
const (
	ScoreMin = 0
	ScoreMax = 1000
)

// Reasons attached to a selection.
const (
	ReasonInitial  = "initial"
	ReasonSwitch   = "switch"
	ReasonHold     = "hold"
	ReasonCooldown = "cooldown"
)

const snapshotVersion = 1

// Errors reported by the selector.
var (
	ErrBlankAgent        = errors.New("agent id is required")
	ErrBadBehavior       = errors.New("behavior definition is invalid")
	ErrDuplicateBehavior = errors.New("behavior id already registered")
	ErrUnknownBehavior   = errors.New("behavior id is not registered")
	ErrBadAmount         = errors.New("penalty amount must be positive")
	ErrNoBehaviors       = errors.New("no behaviors are registered")
	ErrNoCandidate       = errors.New("no behavior is eligible")
	ErrBadPayload        = errors.New("snapshot payload is invalid")
)

// World carries the numeric facts a curve reads.
type World struct {
	Vars map[string]int
}

// Behavior maps one world variable through one curve.
type Behavior struct {
	ID    string
	Var   string
	Curve string
	InMin int
	InMax int
}

// Options tunes scoring, cooldown and switching.
type Options struct {
	// CooldownMs is how long a committed behavior stays out of selection.
	CooldownMs int64
	// HysteresisPercent is how much better a rival must score before the
	// current behavior is replaced.
	HysteresisPercent int
	// PenaltyCap is the hard ceiling of accumulated penalty points.
	PenaltyCap int
	// PenaltyStep is added on every repeated commit of the same behavior.
	PenaltyStep int
}

// Selection is the outcome of one Select call.
type Selection struct {
	Agent    string
	Behavior string
	Score    int
	Switch   bool
	Reason   string
}

// ScoreTrace explains how a final score was produced.
type ScoreTrace struct {
	Agent        string `json:"agent"`
	Behavior     string `json:"behavior"`
	Variable     string `json:"variable"`
	Curve        string `json:"curve"`
	Input        int    `json:"input"`
	ClampedInput int    `json:"clamped_input"`
	InMin        int    `json:"in_min"`
	InMax        int    `json:"in_max"`
	Percent      int    `json:"percent"`
	BaseScore    int    `json:"base_score"`
	Penalty      int    `json:"penalty"`
	Score        int    `json:"score"`
}

// String renders the score source in a stable log-friendly key=value form.
func (t ScoreTrace) String() string {
	return fmt.Sprintf(
		"agent=%s behavior=%s var=%s curve=%s input=%d clamped=%d percent=%d base=%d penalty=%d score=%d",
		t.Agent, t.Behavior, t.Variable, t.Curve, t.Input, t.ClampedInput,
		t.Percent, t.BaseScore, t.Penalty, t.Score,
	)
}

// UtilitySelector owns behavior definitions and the per agent runtime state.
type UtilitySelector struct {
	mu         sync.RWMutex
	agentLocks map[string]*sync.Mutex
	opts       Options
	behaviors  map[string]Behavior
	order      []string
	agents     map[string]*agentState
}

type agentState struct {
	current   string
	cooldowns map[string]int64
	penalties map[string]int
	streak    int
}

type snapshotData struct {
	Version   int                      `json:"version"`
	Behaviors []Behavior               `json:"behaviors"`
	Agents    map[string]agentSnapshot `json:"agents"`
}

type agentSnapshot struct {
	Current   string           `json:"current"`
	Cooldowns map[string]int64 `json:"cooldowns"`
	Penalties map[string]int   `json:"penalties"`
	Streak    int              `json:"streak"`
}

// NewUtilitySelector builds an empty selector.
func NewUtilitySelector(opts Options) *UtilitySelector {
	return &UtilitySelector{
		opts:       opts,
		agentLocks: make(map[string]*sync.Mutex),
		behaviors:  make(map[string]Behavior),
		agents:     make(map[string]*agentState),
	}
}

func (s *UtilitySelector) lockAgentOperations(agent string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.agentLocks == nil {
		s.agentLocks = make(map[string]*sync.Mutex)
	}
	lock := s.agentLocks[agent]
	if lock == nil {
		lock = &sync.Mutex{}
		s.agentLocks[agent] = lock
	}
	return lock
}

// AddBehavior registers one behavior definition.
func (s *UtilitySelector) AddBehavior(b Behavior) error {
	if err := validateBehavior(b); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.behaviors == nil {
		s.behaviors = make(map[string]Behavior)
	}
	if _, exists := s.behaviors[b.ID]; exists {
		return ErrDuplicateBehavior
	}

	s.behaviors[b.ID] = b
	s.order = append(s.order, b.ID)
	sort.Strings(s.order)
	return nil
}

// Behaviors returns a copy of the registered behaviors ordered by id.
func (s *UtilitySelector) Behaviors() []Behavior {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]Behavior, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, s.behaviors[id])
	}
	return out
}

// Score computes the current integer score of one behavior for one agent.
func (s *UtilitySelector) Score(agent, behavior string, world World) (int, error) {
	trace, err := s.ExplainScore(agent, behavior, world)
	return trace.Score, err
}

// ExplainScore returns the curve and penalty inputs behind one score.
func (s *UtilitySelector) ExplainScore(agent, behavior string, world World) (ScoreTrace, error) {
	if isBlank(agent) {
		return ScoreTrace{}, ErrBlankAgent
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	b, ok := s.behaviors[behavior]
	if !ok {
		return ScoreTrace{}, ErrUnknownBehavior
	}
	return s.explainLocked(agent, b, world), nil
}

// ExplainScores returns score sources for every registered behavior in ID order.
func (s *UtilitySelector) ExplainScores(agent string, world World) ([]ScoreTrace, error) {
	if isBlank(agent) {
		return nil, ErrBlankAgent
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]ScoreTrace, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, s.explainLocked(agent, s.behaviors[id], world))
	}
	return out, nil
}

// Select picks the behavior the agent should run at the given time.
func (s *UtilitySelector) Select(agent string, world World, at int64) (Selection, error) {
	if isBlank(agent) {
		return Selection{}, ErrBlankAgent
	}

	operationLock := s.lockAgentOperations(agent)
	operationLock.Lock()
	defer operationLock.Unlock()

	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.order) == 0 {
		return Selection{}, ErrNoBehaviors
	}

	state := s.agents[agent]
	currentID := ""
	if state != nil {
		currentID = state.current
	}

	var bestID string
	bestScore := 0
	haveCandidate := false
	var bestAlternativeID string
	bestAlternativeScore := 0
	haveAlternative := false

	for _, id := range s.order {
		if state != nil && state.cooldowns[id] > at {
			continue
		}

		score := s.scoreLocked(agent, s.behaviors[id], world)
		if !haveCandidate || score > bestScore {
			bestID = id
			bestScore = score
			haveCandidate = true
		}
		if currentID != "" && id != currentID {
			if !haveAlternative || score > bestAlternativeScore {
				bestAlternativeID = id
				bestAlternativeScore = score
				haveAlternative = true
			}
		}
	}

	if !haveCandidate {
		return Selection{}, ErrNoCandidate
	}

	selection := Selection{
		Agent:    agent,
		Behavior: bestID,
		Score:    bestScore,
	}

	if currentID == "" {
		selection.Switch = true
		selection.Reason = ReasonInitial
		return selection, nil
	}

	if state.cooldowns[currentID] > at {
		selection.Switch = true
		selection.Reason = ReasonCooldown
		return selection, nil
	}

	currentScore := s.scoreLocked(agent, s.behaviors[currentID], world)
	threshold := currentScore * (100 - s.opts.HysteresisPercent) / 100
	if haveAlternative && bestAlternativeScore > threshold {
		selection.Switch = true
		selection.Behavior = bestAlternativeID
		selection.Score = bestAlternativeScore
		selection.Reason = ReasonSwitch
		return selection, nil
	}

	selection.Behavior = currentID
	selection.Score = currentScore
	selection.Switch = false
	selection.Reason = ReasonHold
	return selection, nil
}

// Commit records that the agent started the given behavior.
func (s *UtilitySelector) Commit(agent, behavior string, at int64) error {
	if isBlank(agent) {
		return ErrBlankAgent
	}

	operationLock := s.lockAgentOperations(agent)
	operationLock.Lock()
	defer operationLock.Unlock()

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.behaviors[behavior]; !ok {
		return ErrUnknownBehavior
	}

	state := s.ensureAgentLocked(agent)
	if state.current == behavior {
		state.streak++
		if s.opts.PenaltyStep > 0 {
			state.penalties[behavior] = addPenalty(
				state.penalties[behavior], s.opts.PenaltyStep, s.opts.PenaltyCap,
			)
		}
	} else {
		state.current = behavior
		state.streak = 1
	}

	if state.cooldowns == nil {
		state.cooldowns = make(map[string]int64)
	}
	state.cooldowns[behavior] = addMs(at, s.opts.CooldownMs)
	return nil
}

// Penalize adds manual penalty points to one behavior of one agent.
func (s *UtilitySelector) Penalize(agent, behavior string, amount int) error {
	if isBlank(agent) {
		return ErrBlankAgent
	}
	if amount <= 0 {
		return ErrBadAmount
	}

	operationLock := s.lockAgentOperations(agent)
	operationLock.Lock()
	defer operationLock.Unlock()

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.behaviors[behavior]; !ok {
		return ErrUnknownBehavior
	}

	state := s.ensureAgentLocked(agent)
	state.penalties[behavior] = addPenalty(
		state.penalties[behavior], amount, s.opts.PenaltyCap,
	)
	return nil
}

// Current reports the behavior the agent currently runs.
func (s *UtilitySelector) Current(agent string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	state := s.agents[agent]
	if state == nil || state.current == "" {
		return "", false
	}
	return state.current, true
}

// CooldownUntil reports when a behavior becomes selectable again.
func (s *UtilitySelector) CooldownUntil(agent, behavior string) int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	state := s.agents[agent]
	if state == nil || state.cooldowns == nil {
		return 0
	}
	return state.cooldowns[behavior]
}

// Reset clears per agent runtime state.
func (s *UtilitySelector) Reset(agent string) error {
	if isBlank(agent) {
		return ErrBlankAgent
	}

	operationLock := s.lockAgentOperations(agent)
	operationLock.Lock()
	defer operationLock.Unlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.agents, agent)
	return nil
}

// Snapshot serialises behaviors, cooldowns, penalties and current behavior.
func (s *UtilitySelector) Snapshot() ([]byte, error) {
	s.mu.RLock()
	data := snapshotData{
		Version:   snapshotVersion,
		Behaviors: make([]Behavior, 0, len(s.order)),
		Agents:    make(map[string]agentSnapshot, len(s.agents)),
	}

	for _, id := range s.order {
		data.Behaviors = append(data.Behaviors, s.behaviors[id])
	}
	for agent, state := range s.agents {
		if state == nil {
			continue
		}

		snapshot := agentSnapshot{
			Current:   state.current,
			Streak:    state.streak,
			Cooldowns: make(map[string]int64, len(state.cooldowns)),
			Penalties: make(map[string]int, len(state.penalties)),
		}
		for behavior, until := range state.cooldowns {
			snapshot.Cooldowns[behavior] = until
		}
		for behavior, penalty := range state.penalties {
			snapshot.Penalties[behavior] = penalty
		}
		data.Agents[agent] = snapshot
	}
	s.mu.RUnlock()

	payload, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadPayload, err)
	}
	return payload, nil
}

// Restore replaces the whole selector state with a snapshot.
func (s *UtilitySelector) Restore(payload []byte) error {
	next, err := decodeSnapshot(payload)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.behaviors = next.behaviors
	s.order = next.order
	s.agents = next.agents
	s.mu.Unlock()
	return nil
}

type restoredState struct {
	behaviors map[string]Behavior
	order     []string
	agents    map[string]*agentState
}

func decodeSnapshot(payload []byte) (restoredState, error) {
	var data snapshotData
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&data); err != nil {
		return restoredState{}, fmt.Errorf("%w: decode snapshot: %v", ErrBadPayload, err)
	}
	if err := decoder.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		if err == nil {
			return restoredState{}, fmt.Errorf("%w: trailing snapshot data", ErrBadPayload)
		}
		return restoredState{}, fmt.Errorf("%w: decode trailing data: %v", ErrBadPayload, err)
	}
	if data.Version != snapshotVersion {
		return restoredState{}, fmt.Errorf("%w: unsupported version %d", ErrBadPayload, data.Version)
	}

	restored := restoredState{
		behaviors: make(map[string]Behavior, len(data.Behaviors)),
		order:     make([]string, 0, len(data.Behaviors)),
		agents:    make(map[string]*agentState, len(data.Agents)),
	}

	for _, b := range data.Behaviors {
		if err := validateBehavior(b); err != nil {
			return restoredState{}, fmt.Errorf("%w: behavior %q: %v", ErrBadPayload, b.ID, err)
		}
		if _, exists := restored.behaviors[b.ID]; exists {
			return restoredState{}, fmt.Errorf("%w: duplicate behavior %q", ErrBadPayload, b.ID)
		}
		restored.behaviors[b.ID] = b
		restored.order = append(restored.order, b.ID)
	}
	sort.Strings(restored.order)

	for agent, snapshot := range data.Agents {
		if isBlank(agent) {
			return restoredState{}, fmt.Errorf("%w: blank agent key", ErrBadPayload)
		}
		if snapshot.Current != "" {
			if _, ok := restored.behaviors[snapshot.Current]; !ok {
				return restoredState{}, fmt.Errorf("%w: unknown current behavior %q", ErrBadPayload, snapshot.Current)
			}
		}
		if snapshot.Streak < 0 {
			return restoredState{}, fmt.Errorf("%w: negative streak for agent %q", ErrBadPayload, agent)
		}
		if snapshot.Current != "" && snapshot.Streak == 0 {
			snapshot.Streak = 1
		}
		if snapshot.Current == "" {
			snapshot.Streak = 0
		}

		state := &agentState{
			current:   snapshot.Current,
			streak:    snapshot.Streak,
			cooldowns: make(map[string]int64, len(snapshot.Cooldowns)),
			penalties: make(map[string]int, len(snapshot.Penalties)),
		}

		for behavior, until := range snapshot.Cooldowns {
			if _, ok := restored.behaviors[behavior]; !ok {
				return restoredState{}, fmt.Errorf("%w: unknown cooldown behavior %q", ErrBadPayload, behavior)
			}
			state.cooldowns[behavior] = until
		}
		for behavior, penalty := range snapshot.Penalties {
			if _, ok := restored.behaviors[behavior]; !ok {
				return restoredState{}, fmt.Errorf("%w: unknown penalty behavior %q", ErrBadPayload, behavior)
			}
			if penalty < 0 {
				return restoredState{}, fmt.Errorf("%w: negative penalty for %q/%q", ErrBadPayload, agent, behavior)
			}
			state.penalties[behavior] = penalty
		}

		restored.agents[agent] = state
	}

	return restored, nil
}

func (s *UtilitySelector) ensureAgentLocked(agent string) *agentState {
	if s.agents == nil {
		s.agents = make(map[string]*agentState)
	}
	state := s.agents[agent]
	if state == nil {
		state = &agentState{
			cooldowns: make(map[string]int64),
			penalties: make(map[string]int),
		}
		s.agents[agent] = state
	}
	if state.cooldowns == nil {
		state.cooldowns = make(map[string]int64)
	}
	if state.penalties == nil {
		state.penalties = make(map[string]int)
	}
	return state
}

func (s *UtilitySelector) scoreLocked(agent string, b Behavior, world World) int {
	return s.explainLocked(agent, b, world).Score
}

func (s *UtilitySelector) explainLocked(agent string, b Behavior, world World) ScoreTrace {
	input := 0
	if world.Vars != nil {
		input = world.Vars[b.Var]
	}

	clamped := input
	percent := 0
	hasSpan := b.InMax > b.InMin
	if hasSpan {
		clamped = clampInt(input, b.InMin, b.InMax)
		percent = (clamped - b.InMin) * 100 / (b.InMax - b.InMin)
	}

	base := 0
	switch b.Curve {
	case CurveLinear:
		base = percent * 10
	case CurveStep:
		if input >= b.InMin {
			base = ScoreMax
			percent = 100
			if !hasSpan {
				clamped = b.InMax
			}
		}
	case CurveInverseS:
		distance := 2*percent - 100
		if distance < 0 {
			distance = -distance
		}
		base = (100 - distance) * 10
	}

	penalty := 0
	if state := s.agents[agent]; state != nil {
		penalty = state.penalties[b.ID]
	}

	score := clampInt(base-penalty, ScoreMin, ScoreMax)
	return ScoreTrace{
		Agent:        agent,
		Behavior:     b.ID,
		Variable:     b.Var,
		Curve:        b.Curve,
		Input:        input,
		ClampedInput: clamped,
		InMin:        b.InMin,
		InMax:        b.InMax,
		Percent:      percent,
		BaseScore:    base,
		Penalty:      penalty,
		Score:        score,
	}
}

func validateBehavior(b Behavior) error {
	if isBlank(b.ID) || isBlank(b.Var) {
		return ErrBadBehavior
	}
	switch b.Curve {
	case CurveLinear, CurveStep, CurveInverseS:
	default:
		return ErrBadBehavior
	}
	if b.InMax <= b.InMin {
		if b.Curve != CurveStep || b.InMax != 0 {
			return ErrBadBehavior
		}
	}
	return nil
}

func isBlank(value string) bool {
	return strings.TrimSpace(value) == ""
}

func clampInt(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

func addPenalty(current, amount, cap int) int {
	if amount <= 0 {
		return max(current, 0)
	}
	if cap < 0 {
		cap = 0
	}

	total := current + amount
	if total < 0 {
		return 0
	}
	if total > cap {
		return cap
	}
	return total
}

func addMs(at int64, delay int64) int64 {
	if delay <= 0 {
		return at
	}
	if at > math.MaxInt64-delay {
		return math.MaxInt64
	}
	return at + delay
}
