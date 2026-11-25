// Package task compares observed processes with an isolation domain baseline
// and reports namespace escapes and privilege anomalies.
package task

import "errors"

// errNotImplemented marks behaviour that is still missing.
var errNotImplemented = errors.New("not implemented")

// Finding kinds.
const (
	KindNSMismatch     = "ns_mismatch"
	KindMountDrift     = "mount_drift"
	KindOrphan         = "orphan"
	KindPrivilegeShift = "privilege_shift"
	KindCapRetained    = "cap_retained"
	KindPIDReuse       = "pid_reuse"
)

// Finding severities.
const (
	SeverityCritical = "critical"
	SeverityHigh     = "high"
	SeverityMedium   = "medium"
)

// NamespaceSet holds the kernel namespace identifiers of one process or one
// domain baseline.
type NamespaceSet struct {
	PID   uint64 `json:"pid"`
	Mount uint64 `json:"mount"`
	Net   uint64 `json:"net"`
	User  uint64 `json:"user"`
	UTS   uint64 `json:"uts"`
	IPC   uint64 `json:"ipc"`
}

// IDMap is one uid mapping range of a user namespace.
type IDMap struct {
	Inside  uint32 `json:"inside"`
	Outside uint32 `json:"outside"`
	Length  uint32 `json:"length"`
}

// Domain is one isolation domain and its expected baseline.
type Domain struct {
	ID          string       `json:"id"`
	Baseline    NamespaceSet `json:"baseline"`
	UIDMap      []IDMap      `json:"uid_map"`
	InitPID     int          `json:"init_pid"`
	AllowedCaps []string     `json:"allowed_caps"`
}

// ProcessRow is one process observation from the collector.
type ProcessRow struct {
	DomainID  string       `json:"domain_id"`
	PID       int          `json:"pid"`
	PPID      int          `json:"ppid"`
	StartTime uint64       `json:"start_time"`
	NS        NamespaceSet `json:"ns"`
	UID       uint32       `json:"uid"`
	EUID      uint32       `json:"euid"`
	Caps      []string     `json:"caps"`
}

// EscapeFinding is one escape or privilege anomaly.
type EscapeFinding struct {
	ID        string `json:"id"`
	DomainID  string `json:"domain_id"`
	PID       int    `json:"pid"`
	Kind      string `json:"kind"`
	Severity  string `json:"severity"`
	Namespace string `json:"namespace"`
	Detail    string `json:"detail"`
	Seq       int64  `json:"seq"`
}

// QuarantineTicket records an active process quarantine.
type QuarantineTicket struct {
	ID       string `json:"id"`
	DomainID string `json:"domain_id"`
	PID      int    `json:"pid"`
	Seq      int64  `json:"seq"`
}

// Limits bounds the auditor tables.
type Limits struct {
	MaxDomains       int
	MaxRowsPerDomain int
	MaxTickets       int
}

// Errors returned by the auditor.
var (
	ErrBlankDomain        = errors.New("domain id is required")
	ErrBadBaseline        = errors.New("domain baseline is incomplete")
	ErrDuplicateDomain    = errors.New("domain already registered")
	ErrUnknownDomain      = errors.New("unknown domain")
	ErrBadRow             = errors.New("process row is invalid")
	ErrNoFinding          = errors.New("no current finding for this process")
	ErrAlreadyQuarantined = errors.New("process is already quarantined")
	ErrUnknownTicket      = errors.New("unknown quarantine ticket")
	ErrLimitExceeded      = errors.New("limit exceeded")
	ErrBadSnapshot        = errors.New("snapshot payload is invalid")
)

// NamespaceEscapeAuditor owns domains, observed rows, findings and tickets.
type NamespaceEscapeAuditor struct {
	limits Limits
}

// NewNamespaceEscapeAuditor builds an auditor with the given caps.
func NewNamespaceEscapeAuditor(limits Limits) *NamespaceEscapeAuditor {
	return &NamespaceEscapeAuditor{limits: limits}
}

// RegisterDomain adds one isolation domain with its baseline.
func (a *NamespaceEscapeAuditor) RegisterDomain(d Domain) error {
	return errNotImplemented
}

// SetBaseline replaces the expected namespace set of a domain.
func (a *NamespaceEscapeAuditor) SetBaseline(domainID string, ns NamespaceSet) error {
	return errNotImplemented
}

// Domain returns a copy of the domain record.
func (a *NamespaceEscapeAuditor) Domain(domainID string) (Domain, bool) {
	return Domain{}, false
}

// Observe records one process row from the collector.
func (a *NamespaceEscapeAuditor) Observe(row ProcessRow) error {
	return errNotImplemented
}

// Rows returns the current row per pid in ascending pid order.
func (a *NamespaceEscapeAuditor) Rows(domainID string) ([]ProcessRow, error) {
	return nil, errNotImplemented
}

// Audit recomputes findings for one domain.
func (a *NamespaceEscapeAuditor) Audit(domainID string) ([]EscapeFinding, error) {
	return nil, errNotImplemented
}

// Findings returns the findings of the last audit for a domain.
func (a *NamespaceEscapeAuditor) Findings(domainID string) ([]EscapeFinding, error) {
	return nil, errNotImplemented
}

// Count returns the number of findings of the last audit.
func (a *NamespaceEscapeAuditor) Count(domainID string) (int, error) {
	return 0, errNotImplemented
}

// Quarantine stops a process that has a critical or high finding.
func (a *NamespaceEscapeAuditor) Quarantine(domainID string, pid int) (QuarantineTicket, error) {
	return QuarantineTicket{}, errNotImplemented
}

// Release lifts one quarantine ticket.
func (a *NamespaceEscapeAuditor) Release(ticketID string) error {
	return errNotImplemented
}

// Snapshot serialises domains, rows, findings and tickets.
func (a *NamespaceEscapeAuditor) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state from a snapshot.
func (a *NamespaceEscapeAuditor) Restore(data []byte) error {
	return errNotImplemented
}
