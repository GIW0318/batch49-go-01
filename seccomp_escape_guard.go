// Package task narrows seccomp allow lists from observed syscall usage and
// blocks attempts to escape the enforced profile.
package task

import "errors"

// errNotImplemented marks behaviour that is still missing.
var errNotImplemented = errors.New("not implemented")

// Workload lifecycle states.
const (
	StateAudit       = "audit"
	StateEnforced    = "enforced"
	StateQuarantined = "quarantined"
)

// Decisions returned by Check.
const (
	ActionAllow = "allow"
	ActionDeny  = "deny"
	ActionKill  = "kill"
)

// Reason codes attached to a decision and to a recorded attempt.
const (
	ReasonAuditMode   = "audit_mode"
	ReasonAllowed     = "allowed"
	ReasonBlocked     = "blocked_syscall"
	ReasonEscape      = "escape_dangerous_syscall"
	ReasonQuarantined = "quarantined"
)

// Call is one syscall invocation. Arg0 is the only argument that profiles may
// constrain.
type Call struct {
	Name string `json:"name"`
	Arg0 uint64 `json:"arg0"`
}

// Rule allows one syscall name. A non zero mask constrains Arg0 so that
// Arg0&Arg0Mask must equal Arg0Value.
type Rule struct {
	Name      string `json:"name"`
	Arg0Mask  uint64 `json:"arg0_mask"`
	Arg0Value uint64 `json:"arg0_value"`
}

// Profile is an immutable generation of allowed rules.
type Profile struct {
	Generation int    `json:"generation"`
	Rules      []Rule `json:"rules"`
}

// WorkloadSpec is the registration input for one container workload.
type WorkloadSpec struct {
	ID      string `json:"id"`
	Allowed []Rule `json:"allowed"`
}

// Workload is the externally visible state of one container.
type Workload struct {
	ID      string  `json:"id"`
	State   string  `json:"state"`
	Profile Profile `json:"profile"`
	Strikes int     `json:"strikes"`
	Seq     int64   `json:"seq"`
}

// Observation is the aggregated audit window entry for one syscall name.
type Observation struct {
	Name     string `json:"name"`
	Count    int    `json:"count"`
	FirstSeq int64  `json:"first_seq"`
}

// Draft is a proposed narrower profile that has not been enforced yet.
type Draft struct {
	WorkloadID string   `json:"workload_id"`
	Allowed    []Rule   `json:"allowed"`
	Removed    []string `json:"removed"`
	Generation int      `json:"generation"`
	Digest     string   `json:"digest"`
}

// Decision is the outcome of one runtime check.
type Decision struct {
	Action      string `json:"action"`
	Reason      string `json:"reason"`
	Strikes     int    `json:"strikes"`
	Quarantined bool   `json:"quarantined"`
}

// Attempt records one blocked syscall for later review.
type Attempt struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
	Seq    int64  `json:"seq"`
}

// Limits bounds the guard tables.
type Limits struct {
	MaxWorkloads int
	MaxRules     int
	MaxObserved  int
	MaxStrikes   int
}

// Errors returned by the guard.
var (
	ErrBlankWorkload      = errors.New("workload id is required")
	ErrDuplicateWorkload  = errors.New("workload already enrolled")
	ErrUnknownWorkload    = errors.New("unknown workload")
	ErrEmptyProfile       = errors.New("profile must allow at least one syscall")
	ErrInvalidCall        = errors.New("syscall call or rule is invalid")
	ErrNotAuditing        = errors.New("workload is not in the audit window")
	ErrNoObservation      = errors.New("audit window has no observation")
	ErrNoDraft            = errors.New("no profile draft for this workload")
	ErrWidenRejected      = errors.New("narrowed profile would widen the allow list")
	ErrAlreadyQuarantined = errors.New("workload is already quarantined")
	ErrNotQuarantined     = errors.New("workload is not quarantined")
	ErrReasonRequired     = errors.New("quarantine reason is required")
	ErrLimitExceeded      = errors.New("limit exceeded")
	ErrBadSnapshot        = errors.New("snapshot payload is invalid")
)

// SeccompEscapeGuard owns workload state, audit windows and quarantine tickets.
type SeccompEscapeGuard struct {
	limits Limits
}

// NewSeccompEscapeGuard builds a guard with the given caps.
func NewSeccompEscapeGuard(limits Limits) *SeccompEscapeGuard {
	return &SeccompEscapeGuard{limits: limits}
}

// Enroll registers a workload in the audit state with its first profile.
func (g *SeccompEscapeGuard) Enroll(spec WorkloadSpec) error {
	return errNotImplemented
}

// Observe records one audit window syscall for a workload.
func (g *SeccompEscapeGuard) Observe(workloadID string, c Call) error {
	return errNotImplemented
}

// Observations returns the audit window in first seen order.
func (g *SeccompEscapeGuard) Observations(workloadID string) ([]Observation, error) {
	return nil, errNotImplemented
}

// Propose derives a narrower profile from the current audit window.
func (g *SeccompEscapeGuard) Propose(workloadID string) (Draft, error) {
	return Draft{}, errNotImplemented
}

// Promote turns the pending draft into the enforced profile.
func (g *SeccompEscapeGuard) Promote(workloadID string) (Profile, error) {
	return Profile{}, errNotImplemented
}

// Check decides one runtime syscall.
func (g *SeccompEscapeGuard) Check(workloadID string, c Call) (Decision, error) {
	return Decision{}, errNotImplemented
}

// Strikes returns the accumulated blocked call count.
func (g *SeccompEscapeGuard) Strikes(workloadID string) (int, error) {
	return 0, errNotImplemented
}

// Quarantine moves a workload out of service with a reason.
func (g *SeccompEscapeGuard) Quarantine(workloadID, reason string) error {
	return errNotImplemented
}

// Release returns a quarantined workload to a fresh audit window.
func (g *SeccompEscapeGuard) Release(workloadID string) error {
	return errNotImplemented
}

// State returns the lifecycle state of a workload.
func (g *SeccompEscapeGuard) State(workloadID string) (string, error) {
	return "", errNotImplemented
}

// Workload returns a copy of the workload record.
func (g *SeccompEscapeGuard) Workload(workloadID string) (Workload, bool) {
	return Workload{}, false
}

// Attempts returns blocked syscalls for a workload in order.
func (g *SeccompEscapeGuard) Attempts(workloadID string) ([]Attempt, error) {
	return nil, errNotImplemented
}

// Snapshot serialises workloads, profiles, windows and attempt logs.
func (g *SeccompEscapeGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state from a snapshot.
func (g *SeccompEscapeGuard) Restore(data []byte) error {
	return errNotImplemented
}
