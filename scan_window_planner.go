// Package task schedules scan tasks inside declared maintenance windows.
package task

import (
	"errors"
	"time"
)

// errNotImplemented marks behaviour that is not built yet.
var errNotImplemented = errors.New("not implemented")

// Task states tracked by the planner.
const (
	StateQueued  = "queued"
	StateRunning = "running"
	StateBackoff = "backoff"
	StateDone    = "done"
	StateParked  = "parked"
)

// Outcome kinds reported by the executor.
const (
	OutcomeDone      = "done"
	OutcomeThrottled = "throttled"
	OutcomeFailed    = "failed"
)

// Errors returned by the planner.
var (
	ErrBadWindow      = errors.New("maintenance window is invalid")
	ErrDuplicateGroup = errors.New("asset group is already declared")
	ErrUnknownGroup   = errors.New("asset group is not declared")
	ErrBadTask        = errors.New("scan task is invalid")
	ErrDuplicateTask  = errors.New("scan task id is already submitted")
	ErrUnknownTask    = errors.New("scan task is unknown or already settled")
	ErrBadOutcome     = errors.New("scan outcome kind is invalid")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// Window is the maintenance window declared for one asset group. Zone is an
// IANA time zone name, Days lists the allowed weekdays and Start/End are the
// offsets from local midnight.
type Window struct {
	Zone  string         `json:"zone"`
	Days  []time.Weekday `json:"days"`
	Start time.Duration  `json:"start"`
	End   time.Duration  `json:"end"`
}

// Task is one unit of scan work.
type Task struct {
	ID       string `json:"id"`
	Group    string `json:"group"`
	Target   string `json:"target"`
	Progress int    `json:"progress"`
}

// Outcome is one executor report. At is the moment the executor observed the
// outcome, RetryAfter is the delay requested by the target.
type Outcome struct {
	Kind       string        `json:"kind"`
	RetryAfter time.Duration `json:"retry_after"`
	Progress   int           `json:"progress"`
	At         time.Time     `json:"at"`
}

// TaskState is the inspectable state of one submitted task.
type TaskState struct {
	ID       string `json:"id"`
	Group    string `json:"group"`
	Target   string `json:"target"`
	State    string `json:"state"`
	Attempts int    `json:"attempts"`
	Progress int    `json:"progress"`
}

// Limits bounds dispatch.
type Limits struct {
	PerTargetConcurrency int
	MaxThrottles         int
	MaxBackoff           time.Duration
}

// ScanWindowPlanner owns the window declarations, the queue and the per target
// concurrency gates.
type ScanWindowPlanner struct {
	limits Limits
}

// NewScanWindowPlanner builds a planner with the given caps.
func NewScanWindowPlanner(limits Limits) *ScanWindowPlanner {
	return &ScanWindowPlanner{limits: limits}
}

// Declare registers the maintenance window of one asset group.
func (p *ScanWindowPlanner) Declare(group string, w Window) error {
	return errNotImplemented
}

// Submit enqueues one scan task and returns its id.
func (p *ScanWindowPlanner) Submit(t Task) (string, error) {
	return "", errNotImplemented
}

// Next hands out the next dispatchable task whose window is open at now.
func (p *ScanWindowPlanner) Next(now time.Time) (Task, bool) {
	return Task{}, false
}

// Report records the executor outcome of one dispatched task.
func (p *ScanWindowPlanner) Report(id string, r Outcome) error {
	return errNotImplemented
}

// InWindow reports whether the group window is open at the given instant.
func (p *ScanWindowPlanner) InWindow(group string, at time.Time) (bool, error) {
	return false, errNotImplemented
}

// Pending counts the unsettled tasks of one group.
func (p *ScanWindowPlanner) Pending(group string) int {
	return 0
}

// Groups lists the declared asset groups in lexicographic order.
func (p *ScanWindowPlanner) Groups() []string {
	return nil
}

// State returns the current state of one task.
func (p *ScanWindowPlanner) State(id string) (TaskState, error) {
	return TaskState{}, errNotImplemented
}

// Queue lists the unsettled task ids of one group in queue order.
func (p *ScanWindowPlanner) Queue(group string) []string {
	return nil
}

// Snapshot serialises declarations, queue, gates and counters.
func (p *ScanWindowPlanner) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (p *ScanWindowPlanner) Restore(data []byte) error {
	return errNotImplemented
}
