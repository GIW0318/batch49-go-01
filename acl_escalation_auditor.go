// Package task audits file access control entries for privilege escalation
// paths and computes the smallest repair that removes them.
package task

import "errors"

// errNotImplemented marks behaviour that is still missing.
var errNotImplemented = errors.New("not implemented")

// Access is a set of permission bits used for file and directory access.
type Access uint8

// Permission bits.
const (
	AccessNone    Access = 0
	AccessRead    Access = 1 << 0
	AccessWrite   Access = 1 << 1
	AccessExecute Access = 1 << 2
	AccessFull    Access = AccessRead | AccessWrite | AccessExecute
)

// AceKind identifies the class of an access control entry.
type AceKind string

// Entry classes. The default_* classes describe entries inherited by children
// that are created later, they do not affect the directory itself.
const (
	AceUser         AceKind = "user"
	AceGroup        AceKind = "group"
	AceMask         AceKind = "mask"
	AceOther        AceKind = "other"
	AceDefaultUser  AceKind = "default_user"
	AceDefaultGroup AceKind = "default_group"
	AceDefaultOther AceKind = "default_other"
)

// Severity ranks a finding.
type Severity string

// Finding severities.
const (
	SeverityCritical Severity = "critical"
	SeverityHigh     Severity = "high"
	SeverityMedium   Severity = "medium"
)

// Reason codes attached to a finding.
const (
	ReasonDirectWrite   = "direct_write"
	ReasonGroupWrite    = "group_write"
	ReasonOtherWrite    = "other_write"
	ReasonAncestorWrite = "ancestor_write"
	ReasonDefaultWrite  = "default_write"
)

// Ace is one access control entry. ID is a uid, a gid or zero for other and
// mask entries.
type Ace struct {
	Kind AceKind `json:"kind"`
	ID   uint32  `json:"id"`
	Bits Access  `json:"bits"`
}

// Node is one file or directory in the audited tree.
type Node struct {
	Path           string `json:"path"`
	Owner          uint32 `json:"owner"`
	Group          uint32 `json:"group"`
	OwnerBits      Access `json:"owner_bits"`
	GroupBits      Access `json:"group_bits"`
	OtherBits      Access `json:"other_bits"`
	SetUID         bool   `json:"setuid"`
	SetGID         bool   `json:"setgid"`
	Entries        []Ace  `json:"entries"`
	DefaultEntries []Ace  `json:"default_entries"`
}

// Principal is an account whose effective access is evaluated.
type Principal struct {
	ID         string   `json:"id"`
	UID        uint32   `json:"uid"`
	GID        uint32   `json:"gid"`
	Groups     []uint32 `json:"groups"`
	Privileged bool     `json:"privileged"`
}

// Finding is one escalation path attributed to one principal.
type Finding struct {
	ID          string   `json:"id"`
	PrincipalID string   `json:"principal_id"`
	Path        string   `json:"path"`
	Reason      string   `json:"reason"`
	Severity    Severity `json:"severity"`
	ViaGroup    uint32   `json:"via_group"`
	Chain       []string `json:"chain"`
	Seq         int64    `json:"seq"`
}

// AceChange removes bits from one entry of one node.
type AceChange struct {
	Path   string  `json:"path"`
	Kind   AceKind `json:"kind"`
	ID     uint32  `json:"id"`
	Remove Access  `json:"remove"`
}

// RepairPlan is an all or nothing set of entry changes bound to one state.
type RepairPlan struct {
	Changes []AceChange `json:"changes"`
	Digest  string      `json:"digest"`
}

// ApplyResult reports what a successful repair did.
type ApplyResult struct {
	Applied   int   `json:"applied"`
	Remaining int   `json:"remaining"`
	Seq       int64 `json:"seq"`
}

// Limits bounds the live tables of the auditor.
type Limits struct {
	MaxPrincipals int
	MaxNodes      int
	MaxFindings   int
}

// Errors returned by the auditor.
var (
	ErrBlankPrincipal     = errors.New("principal id is required")
	ErrDuplicatePrincipal = errors.New("principal already registered")
	ErrUnknownPrincipal   = errors.New("unknown principal")
	ErrPathInvalid        = errors.New("node path must be absolute and clean")
	ErrDuplicateNode      = errors.New("node already registered")
	ErrNodeNotFound       = errors.New("node not found")
	ErrLimitExceeded      = errors.New("limit exceeded")
	ErrPlanStale          = errors.New("repair plan no longer matches current state")
	ErrApplyRejected      = errors.New("repair plan cannot be applied")
	ErrResidualEscalation = errors.New("escalation paths remain after repair")
	ErrBadSnapshot        = errors.New("snapshot payload is invalid")
	ErrApplyBusy          = errors.New("another repair is being applied")
)

// AclEscalationAuditor owns the principal table, the node tree and the last
// audit result.
type AclEscalationAuditor struct {
	limits Limits
}

// NewAclEscalationAuditor builds an auditor with the given caps.
func NewAclEscalationAuditor(limits Limits) *AclEscalationAuditor {
	return &AclEscalationAuditor{limits: limits}
}

// RegisterPrincipal adds an account to the evaluated set.
func (a *AclEscalationAuditor) RegisterPrincipal(p Principal) error {
	return errNotImplemented
}

// AddNode registers one file or directory.
func (a *AclEscalationAuditor) AddNode(n Node) error {
	return errNotImplemented
}

// Node returns a copy of one registered node.
func (a *AclEscalationAuditor) Node(path string) (Node, bool) {
	return Node{}, false
}

// EffectiveAccess resolves the permission bits a principal holds on a path.
func (a *AclEscalationAuditor) EffectiveAccess(principalID, path string) (Access, error) {
	return AccessNone, errNotImplemented
}

// Scan evaluates the tree and records findings for one principal, or for every
// non privileged principal when principalID is empty.
func (a *AclEscalationAuditor) Scan(principalID string) ([]Finding, error) {
	return nil, errNotImplemented
}

// Findings returns the findings recorded by the most recent scan.
func (a *AclEscalationAuditor) Findings() []Finding {
	return nil
}

// PlanRepair builds the smallest repair that clears every recorded finding.
func (a *AclEscalationAuditor) PlanRepair() (RepairPlan, error) {
	return RepairPlan{}, errNotImplemented
}

// Apply validates and applies a repair plan atomically.
func (a *AclEscalationAuditor) Apply(plan RepairPlan) (ApplyResult, error) {
	return ApplyResult{}, errNotImplemented
}

// Verify rescans every principal and reports whether escalations remain.
func (a *AclEscalationAuditor) Verify() error {
	return errNotImplemented
}

// Snapshot serialises principals, nodes and the sequence counter.
func (a *AclEscalationAuditor) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state from a snapshot.
func (a *AclEscalationAuditor) Restore(data []byte) error {
	return errNotImplemented
}
