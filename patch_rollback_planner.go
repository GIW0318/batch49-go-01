// Package task plans and executes dependency aware rollbacks of patch batches.
package task

import (
	"errors"
	"time"
)

// errNotImplemented marks behaviour that still has to be supplied.
var errNotImplemented = errors.New("not implemented")

// Rollback result states.
const (
	StatusPlanned      = "planned"
	StatusExecuted     = "executed"
	StatusPartial      = "partial"
	StatusWindowClosed = "window_closed"
)

// Blocker reasons reported by feasibility checks.
const (
	BlockerIrreversible      = "irreversible"
	BlockerMissingDependency = "missing_dependency"
	BlockerCycle             = "cycle"
)

// Errors surfaced by the planner.
var (
	ErrBlankField        = errors.New("batch id, step id and reason are required")
	ErrDuplicateBatch    = errors.New("batch already registered")
	ErrDuplicateStep     = errors.New("step id is repeated inside the batch")
	ErrUnknownBatch      = errors.New("batch is not registered")
	ErrUnknownStep       = errors.New("step is not registered in any batch")
	ErrDependencyMissing = errors.New("step depends on an unknown step")
	ErrDependencyCycle   = errors.New("step dependencies form a cycle")
	ErrBadUpto           = errors.New("rollback count is out of range")
	ErrIrreversible      = errors.New("rollback would cross an irreversible step")
	ErrWindowClosed      = errors.New("rollback window has closed")
	ErrAlreadyExecuted   = errors.New("batch rollback was already started")
	ErrStepFailed        = errors.New("rollback step failed")
)

// Step is one unit of patch application.
type Step struct {
	ID            string   `json:"id"`
	Host          string   `json:"host"`
	DependsOn     []string `json:"depends_on"`
	ApplyOrder    int      `json:"apply_order"`
	Irreversible  bool     `json:"irreversible"`
	ReverseScript string   `json:"reverse_script"`
	FailRollback  bool     `json:"fail_rollback"`
}

// Batch is the applied patch bundle to roll back.
type Batch struct {
	ID        string        `json:"id"`
	Steps     []Step        `json:"steps"`
	AppliedAt time.Time     `json:"applied_at"`
	Window    time.Duration `json:"window"`
}

// Blocker explains why a plan cannot be fully executed.
type Blocker struct {
	Step   string `json:"step"`
	Reason string `json:"reason"`
}

// RollbackPlan is the reverse ordered rollback schedule.
type RollbackPlan struct {
	Batch      string     `json:"batch"`
	Order      []string   `json:"order"`
	Layers     [][]string `json:"layers"`
	Reversible int        `json:"reversible"`
	Blockers   []Blocker  `json:"blockers"`
	Deadline   time.Time  `json:"deadline"`
}

// Result reports what a rollback execution actually did.
type Result struct {
	Batch     string   `json:"batch"`
	Completed []string `json:"completed"`
	Failed    string   `json:"failed"`
	Remaining []string `json:"remaining"`
	Status    string   `json:"status"`
}

// Options injects the clock used for window checks.
type Options struct {
	Now func() time.Time
}

// PatchRollbackPlanner owns registered batches and irreversibility flags.
type PatchRollbackPlanner struct {
}

// NewPatchRollbackPlanner builds a planner; a nil clock means time.Now.
func NewPatchRollbackPlanner(opts Options) *PatchRollbackPlanner {
	return &PatchRollbackPlanner{}
}

// RegisterBatch stores a batch and its rollback window.
func (p *PatchRollbackPlanner) RegisterBatch(b Batch) error {
	return errNotImplemented
}

// Plan builds the dependency aware reverse order for a batch.
func (p *PatchRollbackPlanner) Plan(batch Batch) (RollbackPlan, error) {
	return RollbackPlan{}, errNotImplemented
}

// Feasible tells whether the whole plan can be executed.
func (p *PatchRollbackPlanner) Feasible(plan RollbackPlan) (bool, []Blocker) {
	return false, nil
}

// Execute rolls back the first upto steps of a plan.
func (p *PatchRollbackPlanner) Execute(plan RollbackPlan, upto int) (Result, error) {
	return Result{}, errNotImplemented
}

// Window returns the rollback deadline of a registered batch.
func (p *PatchRollbackPlanner) Window(batch string) (time.Time, error) {
	return time.Time{}, errNotImplemented
}

// MarkIrreversible flags a step as impossible to undo.
func (p *PatchRollbackPlanner) MarkIrreversible(step string, reason string) error {
	return errNotImplemented
}

// Steps returns a copy of the registered steps of a batch.
func (p *PatchRollbackPlanner) Steps(batch string) ([]Step, error) {
	return nil, errNotImplemented
}

// Status returns the last execution result of a batch.
func (p *PatchRollbackPlanner) Status(batch string) (Result, bool) {
	return Result{}, false
}

// Reversible tells whether a step may be rolled back at all.
func (p *PatchRollbackPlanner) Reversible(step string) bool {
	return false
}
