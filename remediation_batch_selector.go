// Package task aggregates vulnerability findings with the fix actions that
// remove them, then packs actions into maintenance batches under a hard budget.
package task

import (
	"errors"
	"sync"
	"time"
)

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Finding is one open vulnerability mapped to a component.
type Finding struct {
	ID        string    `json:"id"`
	Component string    `json:"component"`
	Severity  string    `json:"severity"`
	DueAt     time.Time `json:"due_at"`
}

// Action is one executable fix step: one target version of one component.
type Action struct {
	ID            string   `json:"id"`
	Component     string   `json:"component"`
	TargetVersion string   `json:"target_version"`
	Prereq        string   `json:"prereq"`
	Cost          int      `json:"cost"`
	Fixes         []string `json:"fixes"`
}

// Budget is the hard cap of one maintenance window sequence.
type Budget struct {
	MaxCost    int `json:"max_cost"`
	MaxBatches int `json:"max_batches"`
}

// Batch is one maintenance window.
type Batch struct {
	Actions []string `json:"actions"`
	Cost    int      `json:"cost"`
	Covers  []string `json:"covers"`
}

// FixGroup is the per component aggregation of findings and available
// actions.
type FixGroup struct {
	Component      string   `json:"component"`
	Findings       []string `json:"findings"`
	Actions        []string `json:"actions"`
	TargetVersions []string `json:"target_versions"`
}

// BlockedFinding is one finding that Select could not cover, together with the
// constraint that blocked it.
type BlockedFinding struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// Reasons reported in BlockedFinding.
const (
	ReasonBudget     = "budget"
	ReasonBatchLimit = "batch_limit"
)

// Errors returned by the selector.
var (
	ErrBadFinding       = errors.New("finding id and component are required")
	ErrDuplicateFinding = errors.New("finding id is already registered")
	ErrBadAction        = errors.New("action id, component, target version and cost are required")
	ErrActionMismatch   = errors.New("action does not belong to this finding")
	ErrUnknownAction    = errors.New("unknown action")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

// RemediationBatchSelector owns the findings, the fix actions and the applied
// state.
type RemediationBatchSelector struct {
	mu       sync.Mutex
	findings map[string]Finding
	actions  map[string]Action
	fixed    map[string]bool
	order    []string
}

// NewRemediationBatchSelector builds an empty selector.
func NewRemediationBatchSelector() *RemediationBatchSelector {
	return &RemediationBatchSelector{
		findings: make(map[string]Finding),
		actions:  make(map[string]Action),
		fixed:    make(map[string]bool),
	}
}

// Add registers one finding together with the actions that can remove it.
func (s *RemediationBatchSelector) Add(f Finding, actions []Action) error {
	return errNotImplemented
}

// Group aggregates the registered findings and actions per component.
func (s *RemediationBatchSelector) Group() []FixGroup { return nil }

// Select packs actions into batches inside the budget. Findings that do not
// fit are reported through Uncovered instead of producing an error.
func (s *RemediationBatchSelector) Select(budget Budget) ([]Batch, error) {
	return nil, errNotImplemented
}

// Coverage counts the still open findings removed by the given batches.
func (s *RemediationBatchSelector) Coverage(bs []Batch) (int, error) { return 0, errNotImplemented }

// Conflict reports whether two actions may not share one maintenance window.
func (s *RemediationBatchSelector) Conflict(a, b Action) bool { return false }

// Apply marks every finding removed by the batch as fixed. Repeating the same
// batch is a no-op.
func (s *RemediationBatchSelector) Apply(batch Batch) error { return errNotImplemented }

// Uncovered lists the open findings left out by the last Select call together
// with the blocking constraint.
func (s *RemediationBatchSelector) Uncovered() []BlockedFinding { return nil }

// Findings lists the registered findings ordered by ID.
func (s *RemediationBatchSelector) Findings() []Finding { return nil }

// Actions lists the actions registered for one component ordered by ID.
func (s *RemediationBatchSelector) Actions(component string) []Action { return nil }

// Fixed reports whether a finding was already removed by an applied batch.
func (s *RemediationBatchSelector) Fixed(id string) bool { return false }

// Snapshot serialises the findings, actions and applied state.
func (s *RemediationBatchSelector) Snapshot() ([]byte, error) { return nil, errNotImplemented }

// Restore replaces the selector state from a snapshot. A corrupt payload
// leaves the selector untouched.
func (s *RemediationBatchSelector) Restore(data []byte) error { return errNotImplemented }
