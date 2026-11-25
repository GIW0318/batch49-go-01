// Package task scores candidate behaviors with integer utility curves and
// picks the behavior an agent should run next.
package task

import "errors"

// errNotImplemented marks behaviour that still has to be written.
var errNotImplemented = errors.New("not implemented")

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

// UtilitySelector owns behavior definitions and the per agent runtime state.
type UtilitySelector struct {
	opts Options
}

// NewUtilitySelector builds an empty selector.
func NewUtilitySelector(opts Options) *UtilitySelector {
	return &UtilitySelector{opts: opts}
}

// AddBehavior registers one behavior definition.
func (s *UtilitySelector) AddBehavior(b Behavior) error {
	return errNotImplemented
}

// Behaviors returns a copy of the registered behaviors ordered by id.
func (s *UtilitySelector) Behaviors() []Behavior {
	return nil
}

// Score computes the current integer score of one behavior for one agent.
func (s *UtilitySelector) Score(agent, behavior string, world World) (int, error) {
	return 0, errNotImplemented
}

// Select picks the behavior the agent should run at the given time.
func (s *UtilitySelector) Select(agent string, world World, at int64) (Selection, error) {
	return Selection{}, errNotImplemented
}

// Commit records that the agent started the given behavior.
func (s *UtilitySelector) Commit(agent, behavior string, at int64) error {
	return errNotImplemented
}

// Penalize adds manual penalty points to one behavior of one agent.
func (s *UtilitySelector) Penalize(agent, behavior string, amount int) error {
	return errNotImplemented
}

// Current reports the behavior the agent currently runs.
func (s *UtilitySelector) Current(agent string) (string, bool) {
	return "", false
}

// CooldownUntil reports when a behavior becomes selectable again.
func (s *UtilitySelector) CooldownUntil(agent, behavior string) int64 {
	return 0
}

// Reset clears per agent runtime state.
func (s *UtilitySelector) Reset(agent string) error {
	return errNotImplemented
}

// Snapshot serialises behaviors, cooldowns, penalties and current behavior.
func (s *UtilitySelector) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole selector state with a snapshot.
func (s *UtilitySelector) Restore(data []byte) error {
	return errNotImplemented
}
