// Package task keeps the member process ledger of isolated domains and
// reconciles it with an external process scan.
package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// ProcessRef identifies one process. PID alone is not unique because the
// kernel reuses it, so StartID always travels with it.
type ProcessRef struct {
	PID     int    `json:"pid"`
	StartID string `json:"start_id"`
	Domain  string `json:"domain"`
	Comm    string `json:"comm"`
}

// Reconciliation is the difference report between ledger and scan.
type Reconciliation struct {
	Unregistered []ProcessRef `json:"unregistered"`
	Exited       []ProcessRef `json:"exited"`
	Duplicated   []ProcessRef `json:"duplicated"`
	Scanned      int          `json:"scanned"`
	Generation   uint64       `json:"generation"`
}

// LedgerLimits caps the ledger size.
type LedgerLimits struct {
	MaxDomains          int `json:"max_domains"`
	MaxMembersPerDomain int `json:"max_members_per_domain"`
	MaxScan             int `json:"max_scan"`
}

// LedgerStats summarises the ledger state.
type LedgerStats struct {
	Domains    int    `json:"domains"`
	Members    int    `json:"members"`
	Generation uint64 `json:"generation"`
}

// Errors reported by the ledger.
var (
	ErrBlankDomain      = errors.New("domain name is required")
	ErrDomainExists     = errors.New("domain already exists")
	ErrDomainLimit      = errors.New("domain limit reached")
	ErrDomainDestroying = errors.New("domain is being destroyed")
	ErrUnknownDomain    = errors.New("unknown domain")
	ErrBlankProcess     = errors.New("process pid and start id are required")
	ErrCrossDomain      = errors.New("process belongs to another domain")
	ErrAlreadyMember    = errors.New("process is already a member of the domain")
	ErrMemberLimit      = errors.New("member limit reached for the domain")
	ErrUnknownProcess   = errors.New("process is not a member of the domain")
	ErrAmbiguousPID     = errors.New("pid matches more than one member")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

// ProcessDomainLedger records which process belongs to which isolated domain.
type ProcessDomainLedger struct {
	limits LedgerLimits
}

// NewProcessDomainLedger builds a ledger with the given limits.
func NewProcessDomainLedger(l LedgerLimits) *ProcessDomainLedger {
	return &ProcessDomainLedger{limits: l}
}

// Create adds an empty domain.
func (g *ProcessDomainLedger) Create(domain string) error {
	return errNotImplemented
}

// Join registers one process in a domain.
func (g *ProcessDomainLedger) Join(domain string, p ProcessRef) error {
	return errNotImplemented
}

// Leave removes one member by pid.
func (g *ProcessDomainLedger) Leave(domain string, pid int) error {
	return errNotImplemented
}

// Members returns a sorted copy of one domain's members.
func (g *ProcessDomainLedger) Members(domain string) []ProcessRef {
	return nil
}

// Destroy closes a domain and returns its remaining members.
func (g *ProcessDomainLedger) Destroy(domain string) ([]ProcessRef, error) {
	return nil, errNotImplemented
}

// Reconcile compares the ledger with an external scan without mutating it.
func (g *ProcessDomainLedger) Reconcile(scan []ProcessRef) Reconciliation {
	return Reconciliation{}
}

// Snapshot serialises the whole ledger.
func (g *ProcessDomainLedger) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the ledger with a previous snapshot.
func (g *ProcessDomainLedger) Restore(data []byte) error {
	return errNotImplemented
}

// Domains returns the domain names in ascending order.
func (g *ProcessDomainLedger) Domains() []string {
	return nil
}

// Stats summarises the ledger.
func (g *ProcessDomainLedger) Stats() LedgerStats {
	return LedgerStats{}
}

// Generation returns the monotonic change counter of the ledger.
func (g *ProcessDomainLedger) Generation() uint64 {
	return 0
}
