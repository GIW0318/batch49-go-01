// Package task adapts scan pressure per target from a running health score.
package task

import (
	"errors"
	"time"
)

// errNotImplemented marks behaviour that is not built yet.
var errNotImplemented = errors.New("not implemented")

// Outcome kinds reported by the scanner.
const (
	OutcomeOK        = "ok"
	OutcomeThrottled = "throttled"
	OutcomeError     = "error"
	OutcomeProbe     = "probe"
)

// Health anchors.
const (
	HealthMax = 100
	HealthMin = 0
)

// Errors returned by the governor.
var (
	ErrBadTarget   = errors.New("target is blank")
	ErrBadOutcome  = errors.New("outcome kind is invalid")
	ErrUnknownTarget = errors.New("target is not tracked")
	ErrBadSnapshot = errors.New("snapshot payload is invalid")
	ErrBadAlias    = errors.New("target alias is invalid")
)

// Config tunes the governor.
type Config struct {
	MaxConcurrency   int
	BaseDelay        time.Duration
	MaxDelay         time.Duration
	PenaltyThreshold int
	RecoveryStreak   int
	MaxTargets       int
}

// Outcome is one scan result reported for a target.
type Outcome struct {
	Kind     string        `json:"kind"`
	Bytes    int64         `json:"bytes"`
	Duration time.Duration `json:"duration"`
	At       time.Time     `json:"at"`
}

// TargetState is the inspectable posture of one target.
type TargetState struct {
	Target      string        `json:"target"`
	Health      int           `json:"health"`
	Penalized   bool          `json:"penalized"`
	Concurrency int           `json:"concurrency"`
	Delay       time.Duration `json:"delay"`
	InFlight    int           `json:"in_flight"`
}

// TargetPressureGovernor owns per target health, permits and penalties.
type TargetPressureGovernor struct {
	cfg Config
}

// NewTargetPressureGovernor builds a governor with the given configuration.
func NewTargetPressureGovernor(cfg Config) *TargetPressureGovernor {
	return &TargetPressureGovernor{cfg: cfg}
}

// Report folds one scan outcome into the health score of the target.
func (g *TargetPressureGovernor) Report(target string, r Outcome) error {
	return errNotImplemented
}

// Permit asks for one concurrency slot of the target.
func (g *TargetPressureGovernor) Permit(target string) (bool, error) {
	return false, errNotImplemented
}

// Release returns one concurrency slot of the target.
func (g *TargetPressureGovernor) Release(target string) error {
	return errNotImplemented
}

// Health returns the current health score of the target.
func (g *TargetPressureGovernor) Health(target string) int {
	return 0
}

// Concurrency returns the permit cap of the target.
func (g *TargetPressureGovernor) Concurrency(target string) int {
	return 0
}

// Delay returns the pause the scanner must keep between requests.
func (g *TargetPressureGovernor) Delay(target string) time.Duration {
	return 0
}

// Penalized reports whether the target sits in the penalty zone.
func (g *TargetPressureGovernor) Penalized(target string) bool {
	return false
}

// State returns the full posture of one target.
func (g *TargetPressureGovernor) State(target string) (TargetState, error) {
	return TargetState{}, errNotImplemented
}

// Targets lists the tracked targets in lexicographic order.
func (g *TargetPressureGovernor) Targets() []string {
	return nil
}

// AddAlias makes alias share the health score of target.
func (g *TargetPressureGovernor) AddAlias(target, alias string) error {
	return errNotImplemented
}

// Snapshot serialises every tracked target and its counters.
func (g *TargetPressureGovernor) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *TargetPressureGovernor) Restore(data []byte) error {
	return errNotImplemented
}
