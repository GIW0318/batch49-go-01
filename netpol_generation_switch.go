// Package task stages network policy generations and switches the live rule
// set atomically while evaluations already in flight finish on their own
// generation.
package task

import (
	"errors"
)

// errNotImplemented is returned by behaviour that is not wired up yet.
var errNotImplemented = errors.New("not implemented")

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

// Generation is one staged rule set. The resolved workload assignment is
// captured when the generation is staged, so later label edits only affect
// generations staged afterwards.
type Generation struct {
	ID       uint64   `json:"id"`
	Policies []Policy `json:"policies"`
	Digest   string   `json:"digest"`
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

// Limits bounds the switcher tables.
type Limits struct {
	MaxGenerations int
	MaxPolicies    int
	MaxWorkloads   int
}

// PolicyGeneration owns workload labels, staged generations, the active pointer
// and the one step rollback slot.
type PolicyGeneration struct {
	limits Limits
}

// NewPolicyGeneration builds an empty switcher. Generation zero is the implicit
// empty generation that denies everything.
func NewPolicyGeneration(limits Limits) *PolicyGeneration {
	return &PolicyGeneration{limits: limits}
}

// SetWorkload installs or replaces the labels of one workload.
func (p *PolicyGeneration) SetWorkload(workload string, labels map[string]string) error {
	return errNotImplemented
}

// RemoveWorkload drops one workload from the label table.
func (p *PolicyGeneration) RemoveWorkload(workload string) bool {
	return false
}

// Stage compiles policies against the current workload labels and returns a new
// monotonically numbered generation.
func (p *PolicyGeneration) Stage(policies []Policy) (Generation, error) {
	return Generation{}, errNotImplemented
}

// Diff compares the resolved workload assignments of two generations.
func (p *PolicyGeneration) Diff(cur, next Generation) (Diff, error) {
	return Diff{}, errNotImplemented
}

// Commit makes one staged generation active.
func (p *PolicyGeneration) Commit(gen Generation) error {
	return errNotImplemented
}

// Rollback returns to the generation that was active before the last commit.
func (p *PolicyGeneration) Rollback() error {
	return errNotImplemented
}

// Active returns the generation currently serving new evaluations.
func (p *PolicyGeneration) Active() Generation {
	return Generation{}
}

// History lists staged generations in ascending id order.
func (p *PolicyGeneration) History() []Generation {
	return nil
}

// BeginEvaluation pins a new evaluation to the active generation.
func (p *PolicyGeneration) BeginEvaluation(workload string, port int) (EvalToken, error) {
	return EvalToken{}, errNotImplemented
}

// FinishEvaluation resolves a pinned evaluation with its own generation.
func (p *PolicyGeneration) FinishEvaluation(tok EvalToken) (Decision, error) {
	return Decision{}, errNotImplemented
}

// Snapshot serialises labels, generations, the active pointer and rollback state.
func (p *PolicyGeneration) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (p *PolicyGeneration) Restore(data []byte) error {
	return errNotImplemented
}
