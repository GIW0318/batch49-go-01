// Package task plans and drives patch rollout batches across services that
// declare dependencies on each other.
package task

import (
	"errors"
	"sync"
)

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Host lifecycle states.
const (
	HostPending     = "pending"
	HostActive      = "active"
	HostOK          = "ok"
	HostFailed      = "failed"
	HostQuarantined = "quarantined"
	HostAborted     = "aborted"
)

// Gate reasons.
const (
	GatePassed     = "passed"
	GateWaiting    = "waiting_canary"
	GateIncomplete = "waiting_hosts"
	GateFailed     = "canary_failed"
)

// Errors returned by the rollout.
var (
	ErrNoPlan          = errors.New("no rollout plan")
	ErrEmptyPlan       = errors.New("no hosts to patch")
	ErrBadPlan         = errors.New("rollout plan is invalid")
	ErrDependencyCycle = errors.New("service dependency cycle")
	ErrUnknownBatch    = errors.New("unknown batch")
	ErrUnknownHost     = errors.New("unknown host")
	ErrBatchStarted    = errors.New("batch already started")
	ErrDepsPending     = errors.New("dependency batches are not finished")
	ErrBatchNotActive  = errors.New("batch is not active")
	ErrBatchAborted    = errors.New("batch was aborted")
	ErrBatchClosed     = errors.New("batch is already finished")
	ErrHostReported    = errors.New("host was already reported")
	ErrCanaryPending   = errors.New("canary hosts must report first")
	ErrOutsideWindow   = errors.New("outside the maintenance window")
	ErrOverBudget      = errors.New("host downtime budget exceeded")
	ErrBadResult       = errors.New("host result is invalid")
	ErrBadReason       = errors.New("abort reason is required")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// Window is a maintenance window in local minutes since midnight. When
// StartMinute is greater than EndMinute the window wraps past midnight.
type Window struct {
	StartMinute int `json:"start_minute"`
	EndMinute   int `json:"end_minute"`
}

// Host is one machine that needs a patch.
type Host struct {
	ID            string `json:"id"`
	Service       string `json:"service"`
	Canary        bool   `json:"canary"`
	Window        Window `json:"window"`
	BudgetMinutes int    `json:"budget_minutes"`
}

// DepEdge declares that Service depends on DependsOn, so DependsOn must be
// patched in an earlier batch.
type DepEdge struct {
	Service   string `json:"service"`
	DependsOn string `json:"depends_on"`
}

// Batch is one dependency layer of the rollout plan.
type Batch struct {
	ID        string   `json:"id"`
	Services  []string `json:"services"`
	Hosts     []string `json:"hosts"`
	DependsOn []string `json:"depends_on"`
}

// HostResult is the outcome reported for one host.
type HostResult struct {
	OK              bool   `json:"ok"`
	DurationMinutes int    `json:"duration_minutes"`
	Note            string `json:"note,omitempty"`
}

// GateResult is the canary gate decision of one batch.
type GateResult struct {
	BatchID       string   `json:"batch_id"`
	Passed        bool     `json:"passed"`
	Reason        string   `json:"reason"`
	Reported      int      `json:"reported"`
	Total         int      `json:"total"`
	WaitingCanary int      `json:"waiting_canary"`
	Quarantined   []string `json:"quarantined,omitempty"`
}

// RolloutPolicy bounds how much canary failure a batch may tolerate.
type RolloutPolicy struct {
	MaxCanaryFailureRate float64 `json:"max_canary_failure_rate"`
}

// PatchRollout owns the plan, the batch state machine and the quarantine.
type PatchRollout struct {
	mu sync.Mutex
}

// NewPatchRollout builds a rollout with the given policy.
func NewPatchRollout(policy RolloutPolicy) *PatchRollout {
	return &PatchRollout{}
}

// Plan layers hosts by service dependency and replaces any previous plan.
func (p *PatchRollout) Plan(hosts []Host, deps []DepEdge) ([]Batch, error) {
	return nil, errNotImplemented
}

// Batches returns a copy of the current plan in batch order.
func (p *PatchRollout) Batches() []Batch {
	return nil
}

// Batch reads one planned batch by id.
func (p *PatchRollout) Batch(batchID string) (Batch, bool) {
	return Batch{}, false
}

// Start opens one batch after its dependency batches finished.
func (p *PatchRollout) Start(batchID string) error {
	return errNotImplemented
}

// BeginHost checks the maintenance window before one host may be patched.
func (p *PatchRollout) BeginHost(hostID string, minuteOfDay int) error {
	return errNotImplemented
}

// ReportHost records the outcome of one host in the active batch.
func (p *PatchRollout) ReportHost(hostID string, result HostResult) error {
	return errNotImplemented
}

// Gate evaluates the canary subset and either releases or blocks the batch.
func (p *PatchRollout) Gate(batchID string) (GateResult, error) {
	return GateResult{}, errNotImplemented
}

// Abort closes one batch with an operator reason.
func (p *PatchRollout) Abort(batchID, reason string) error {
	return errNotImplemented
}

// HostStatus reports the lifecycle state of one host.
func (p *PatchRollout) HostStatus(hostID string) (string, bool) {
	return "", false
}

// Quarantined lists the failed hosts in ascending order.
func (p *PatchRollout) Quarantined() []string {
	return nil
}

// Snapshot serialises the plan, batch states, host results and quarantine.
func (p *PatchRollout) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole rollout state with a snapshot, all or nothing.
func (p *PatchRollout) Restore(data []byte) error {
	return errNotImplemented
}
