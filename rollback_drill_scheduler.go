// Package task schedules rollback drills on a sample of hosts and keeps a
// coverage ledger of simulated versus expected steps.
package task

import (
	"errors"
	"time"
)

// errNotImplemented marks behaviour that still has to be supplied.
var errNotImplemented = errors.New("not implemented")

// Drill outcomes.
const (
	DrillPassed     = "passed"
	DrillFailed     = "failed"
	DrillIncomplete = "incomplete"
)

// Mismatch kinds produced by step comparison.
const (
	MismatchMissing = "missing"
	MismatchOrder   = "order"
	MismatchResult  = "result"
)

// Errors surfaced by the scheduler.
var (
	ErrBlankField     = errors.New("host, fleet, batch, step and image are required")
	ErrDuplicateHost  = errors.New("host is already enrolled")
	ErrDuplicateBatch = errors.New("batch is already registered")
	ErrUnknownHost    = errors.New("host is not enrolled")
	ErrUnknownBatch   = errors.New("batch is not registered")
	ErrUnknownFleet   = errors.New("fleet has no enrolled hosts")
	ErrUnknownImage   = errors.New("image is not part of the batch")
	ErrBadLimit       = errors.New("sample size must be positive")
	ErrHostOffBatch   = errors.New("host is no longer on the requested batch")
	ErrNoSimulation   = errors.New("no simulation recorded for this host and batch")
	ErrDrillTimeout   = errors.New("simulated drill exceeded the time budget")
)

// BatchSpec is the expected rollback step list of one patch batch.
type BatchSpec struct {
	ID     string   `json:"id"`
	Steps  []string `json:"steps"`
	Images []string `json:"images"`
}

// Host is one enrolled machine.
type Host struct {
	ID    string `json:"id"`
	Fleet string `json:"fleet"`
	Batch string `json:"batch"`
	Image string `json:"image"`
}

// Mismatch is one difference between expected and simulated steps.
type Mismatch struct {
	Kind string `json:"kind"`
	Step string `json:"step"`
	Want string `json:"want"`
	Got  string `json:"got"`
}

// DrillResult is the outcome of one simulated drill.
type DrillResult struct {
	Host       string        `json:"host"`
	Batch      string        `json:"batch"`
	Image      string        `json:"image"`
	Status     string        `json:"status"`
	Mismatches []Mismatch    `json:"mismatches"`
	Elapsed    time.Duration `json:"elapsed"`
	Input      []string      `json:"input"`
	At         time.Time     `json:"at"`
}

// Coverage is the per fleet drill ledger.
type Coverage struct {
	Fleet            string   `json:"fleet"`
	TotalHosts       int      `json:"total_hosts"`
	DrilledHosts     int      `json:"drilled_hosts"`
	UncoveredBatches []string `json:"uncovered_batches"`
	Gaps             int      `json:"gaps"`
}

// DrillFailure is one failed or incomplete drill kept for triage.
type DrillFailure struct {
	Host       string     `json:"host"`
	Batch      string     `json:"batch"`
	Image      string     `json:"image"`
	Category   string     `json:"category"`
	Mismatches []Mismatch `json:"mismatches"`
}

// Options injects the clock and the drill time budget.
type Options struct {
	Now         func() time.Time
	DrillBudget time.Duration
}

// RollbackDrillScheduler owns hosts, batch specs, simulations and results.
type RollbackDrillScheduler struct {
}

// NewRollbackDrillScheduler builds a scheduler; a nil clock means time.Now.
func NewRollbackDrillScheduler(opts Options) *RollbackDrillScheduler {
	return &RollbackDrillScheduler{}
}

// Enroll adds a host to a fleet.
func (s *RollbackDrillScheduler) Enroll(host, fleet string) error {
	return errNotImplemented
}

// RegisterBatch stores the expected rollback steps of a batch.
func (s *RollbackDrillScheduler) RegisterBatch(spec BatchSpec) error {
	return errNotImplemented
}

// Assign pins a host to one batch and image.
func (s *RollbackDrillScheduler) Assign(host, batch, image string) error {
	return errNotImplemented
}

// Sample picks n hosts from a fleet, stratified by batch and image.
func (s *RollbackDrillScheduler) Sample(fleet string, n int) ([]string, error) {
	return nil, errNotImplemented
}

// Drill compares a recorded simulation with the expected batch steps.
func (s *RollbackDrillScheduler) Drill(host string, batch string) (DrillResult, error) {
	return DrillResult{}, errNotImplemented
}

// Coverage reports drilled hosts, uncovered batches and gaps.
func (s *RollbackDrillScheduler) Coverage(fleet string) (Coverage, error) {
	return Coverage{}, errNotImplemented
}

// Failures lists the failed or incomplete drills of a fleet.
func (s *RollbackDrillScheduler) Failures(fleet string) []DrillFailure {
	return nil
}

// RecordSimulation stores what a sandboxed rollback would do.
func (s *RollbackDrillScheduler) RecordSimulation(host, batch string, steps []string, elapsed time.Duration) error {
	return errNotImplemented
}

// Hosts lists the enrolled hosts of a fleet, sorted by id.
func (s *RollbackDrillScheduler) Hosts(fleet string) []Host {
	return nil
}
