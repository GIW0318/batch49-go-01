// Package task compares observed processes with an isolation domain baseline
// and reports namespace escapes and privilege anomalies.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

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

// Limits bounds the auditor tables. A zero limit means "no cap".
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

// snapshotVersion tags the serialised state format.
const snapshotVersion = 1

// NamespaceEscapeAuditor owns domains, observed rows, findings and tickets.
//
// A single RWMutex serialises all state changes. Observation replacement and
// history updates happen in one critical section so an Audit always observes a
// complete row set from before or after any concurrent Observe.
type NamespaceEscapeAuditor struct {
	mu        sync.RWMutex
	limits    Limits
	domains   map[string]*domainState
	tickets   map[string]QuarantineTicket
	ticketSeq int64
}

// domainState is the per-domain mutable tables.
type domainState struct {
	record   Domain
	rows     map[int]ProcessRow
	history  map[int][]uint64
	findings []EscapeFinding
}

// snapshotData is the whole serialisable auditor state.
type snapshotData struct {
	Version   int                           `json:"version"`
	Limits    Limits                        `json:"limits"`
	TicketSeq int64                         `json:"ticket_seq"`
	Domains   map[string]Domain             `json:"domains"`
	Rows      map[string]map[int]ProcessRow `json:"rows"`
	History   map[string]map[int][]uint64   `json:"history"`
	Findings  map[string][]EscapeFinding    `json:"findings"`
	Tickets   map[string]QuarantineTicket   `json:"tickets"`
}

// NewNamespaceEscapeAuditor builds an auditor with the given caps.
func NewNamespaceEscapeAuditor(limits Limits) *NamespaceEscapeAuditor {
	return &NamespaceEscapeAuditor{
		limits:  limits,
		domains: make(map[string]*domainState),
		tickets: make(map[string]QuarantineTicket),
	}
}

func isBlankDomainID(domainID string) bool {
	return strings.TrimSpace(domainID) == ""
}

func baselineComplete(ns NamespaceSet) bool {
	return ns.PID != 0 && ns.Mount != 0 && ns.Net != 0 &&
		ns.User != 0 && ns.UTS != 0 && ns.IPC != 0
}

func cloneRow(row ProcessRow) ProcessRow {
	if row.Caps != nil {
		row.Caps = append([]string(nil), row.Caps...)
	}
	return row
}

func cloneDomain(domain Domain) Domain {
	if domain.UIDMap != nil {
		domain.UIDMap = append([]IDMap(nil), domain.UIDMap...)
	}
	if domain.AllowedCaps != nil {
		domain.AllowedCaps = append([]string(nil), domain.AllowedCaps...)
	}
	return domain
}

func findingID(domainID string, pid int, kind, namespace string) string {
	if namespace != "" {
		return fmt.Sprintf("f:%s:%d:%s:%s", domainID, pid, kind, namespace)
	}
	return fmt.Sprintf("f:%s:%d:%s", domainID, pid, kind)
}

func severityRank(severity string) int {
	switch severity {
	case SeverityCritical:
		return 0
	case SeverityHigh:
		return 1
	case SeverityMedium:
		return 2
	default:
		return 3
	}
}

// RegisterDomain adds one isolation domain with its baseline.
func (a *NamespaceEscapeAuditor) RegisterDomain(d Domain) error {
	if isBlankDomainID(d.ID) {
		return ErrBlankDomain
	}
	if !baselineComplete(d.Baseline) {
		return ErrBadBaseline
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if _, exists := a.domains[d.ID]; exists {
		return ErrDuplicateDomain
	}
	if a.limits.MaxDomains > 0 && len(a.domains) >= a.limits.MaxDomains {
		return ErrLimitExceeded
	}

	a.domains[d.ID] = &domainState{
		record:  cloneDomain(d),
		rows:    make(map[int]ProcessRow),
		history: make(map[int][]uint64),
	}
	return nil
}

// SetBaseline replaces the expected namespace set of a domain. Existing
// findings are intentionally left untouched until the next Audit.
func (a *NamespaceEscapeAuditor) SetBaseline(domainID string, ns NamespaceSet) error {
	if isBlankDomainID(domainID) {
		return ErrBlankDomain
	}
	if !baselineComplete(ns) {
		return ErrBadBaseline
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	ds, ok := a.domains[domainID]
	if !ok {
		return ErrUnknownDomain
	}
	ds.record.Baseline = ns
	return nil
}

// Domain returns a copy of the domain record.
func (a *NamespaceEscapeAuditor) Domain(domainID string) (Domain, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	ds, ok := a.domains[domainID]
	if !ok {
		return Domain{}, false
	}
	return cloneDomain(ds.record), true
}

// Observe records one process row from the collector. Rows key by pid; an
// identical (pid, start time) report is idempotent while a new start time for
// the same pid replaces the current row and preserves the old start time as
// reuse evidence.
func (a *NamespaceEscapeAuditor) Observe(row ProcessRow) error {
	if row.PID <= 0 || row.StartTime == 0 {
		return ErrBadRow
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	ds, ok := a.domains[row.DomainID]
	if !ok {
		// An observation that cannot be reconciled to a registered target is
		// both an unknown-domain error and a row whose domain id does not
		// match, so the returned error matches both sentinels via errors.Is.
		if isBlankDomainID(row.DomainID) {
			return fmt.Errorf("%w: blank domain id in observation: %w (%w)",
				ErrBadRow, ErrBlankDomain, ErrUnknownDomain)
		}
		return fmt.Errorf("%w: domain id %q does not match a registered domain: %w",
			ErrBadRow, row.DomainID, ErrUnknownDomain)
	}

	stored := cloneRow(row)
	if previous, exists := ds.rows[row.PID]; exists {
		if previous.StartTime != row.StartTime {
			// A new start time means the pid was recycled: keep the old
			// start time as reuse evidence. An equal start time is the same
			// process instance, so a redelivery only refreshes the mutable
			// observation without adding history or a second row.
			ds.history[row.PID] = append(ds.history[row.PID], previous.StartTime)
		}
		ds.rows[row.PID] = stored
		return nil
	}

	if a.limits.MaxRowsPerDomain > 0 && len(ds.rows) >= a.limits.MaxRowsPerDomain {
		return ErrLimitExceeded
	}
	ds.rows[row.PID] = stored
	return nil
}

// Rows returns the current row per pid in ascending pid order.
func (a *NamespaceEscapeAuditor) Rows(domainID string) ([]ProcessRow, error) {
	if isBlankDomainID(domainID) {
		return nil, ErrBlankDomain
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	ds, ok := a.domains[domainID]
	if !ok {
		return nil, ErrUnknownDomain
	}

	out := make([]ProcessRow, 0, len(ds.rows))
	for _, row := range ds.rows {
		out = append(out, cloneRow(row))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out, nil
}

// Audit recomputes findings for one domain. The whole computation runs under
// the write lock together with replacing the stored last-audit result.
func (a *NamespaceEscapeAuditor) Audit(domainID string) ([]EscapeFinding, error) {
	if isBlankDomainID(domainID) {
		return nil, ErrBlankDomain
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	ds, ok := a.domains[domainID]
	if !ok {
		return nil, ErrUnknownDomain
	}

	findings := a.auditLocked(ds)
	out := make([]EscapeFinding, len(findings))
	copy(out, findings)
	return out, nil
}

// auditLocked recomputes the finding set from the current rows and stores it.
// Caller must hold a.mu for writing.
func (a *NamespaceEscapeAuditor) auditLocked(ds *domainState) []EscapeFinding {
	domainID := ds.record.ID
	baseline := ds.record.Baseline
	initPID := ds.record.InitPID

	pids := make([]int, 0, len(ds.rows))
	pidSet := make(map[int]struct{}, len(ds.rows))
	for pid := range ds.rows {
		pids = append(pids, pid)
		pidSet[pid] = struct{}{}
	}
	sort.Ints(pids)

	findings := make([]EscapeFinding, 0)
	seen := make(map[string]struct{})
	add := func(pid int, kind, severity, namespace, detail string) {
		dedupKey := fmt.Sprintf("%d|%s|%s", pid, kind, namespace)
		if _, duplicated := seen[dedupKey]; duplicated {
			return
		}
		seen[dedupKey] = struct{}{}
		findings = append(findings, EscapeFinding{
			ID:        findingID(domainID, pid, kind, namespace),
			DomainID:  domainID,
			PID:       pid,
			Kind:      kind,
			Severity:  severity,
			Namespace: namespace,
			Detail:    detail,
		})
	}

	for _, pid := range pids {
		row := ds.rows[pid]

		nsChecks := []struct {
			namespace string
			actual    uint64
			expected  uint64
			severity  string
		}{
			{"pid", row.NS.PID, baseline.PID, SeverityCritical},
			{"user", row.NS.User, baseline.User, SeverityCritical},
			{"net", row.NS.Net, baseline.Net, SeverityHigh},
			{"uts", row.NS.UTS, baseline.UTS, SeverityHigh},
			{"ipc", row.NS.IPC, baseline.IPC, SeverityHigh},
		}
		for _, check := range nsChecks {
			if check.actual != check.expected {
				add(pid, KindNSMismatch, check.severity, check.namespace,
					fmt.Sprintf("%s namespace handle %d differs from baseline %d",
						check.namespace, check.actual, check.expected))
			}
		}

		if row.NS.Mount != baseline.Mount {
			add(pid, KindMountDrift, SeverityCritical, "mount",
				fmt.Sprintf("mount namespace view replaced: actual mount handle=%d baseline mount handle=%d",
					row.NS.Mount, baseline.Mount))
		}

		isInit := initPID != 0 && row.PID == initPID
		if !isInit {
			switch {
			case row.PPID == 0:
				add(pid, KindOrphan, SeverityHigh, "",
					fmt.Sprintf("non-init process has ppid 0 (domain init pid=%d)", initPID))
			default:
				_, parentPresent := pidSet[row.PPID]
				pointsAtInit := initPID != 0 && row.PPID == initPID
				if !pointsAtInit && !parentPresent {
					add(pid, KindOrphan, SeverityHigh, "",
						fmt.Sprintf("ppid %d is neither domain init pid %d nor one of the current rows",
							row.PPID, initPID))
				}
			}
		}

		if row.UID != 0 && row.EUID == 0 {
			add(pid, KindPrivilegeShift, SeverityCritical, "",
				fmt.Sprintf("ordinary uid %d is running with effective uid 0", row.UID))
		}
		if row.EUID != 0 && len(row.Caps) > 0 {
			add(pid, KindCapRetained, SeverityHigh, "",
				fmt.Sprintf("euid %d still holds capabilities [%s]",
					row.EUID, strings.Join(row.Caps, " ")))
		}

		if oldStartTimes := ds.history[pid]; len(oldStartTimes) > 0 {
			add(pid, KindPIDReuse, SeverityHigh, "",
				fmt.Sprintf("pid reused: previous start_time(s)=%v new start_time=%d",
					oldStartTimes, row.StartTime))
		}
	}

	sort.SliceStable(findings, func(i, j int) bool {
		left, right := findings[i], findings[j]
		if ri, rj := severityRank(left.Severity), severityRank(right.Severity); ri != rj {
			return ri < rj
		}
		if left.PID != right.PID {
			return left.PID < right.PID
		}
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		return left.Namespace < right.Namespace
	})

	for i := range findings {
		findings[i].Seq = int64(i + 1)
	}

	ds.findings = findings
	return findings
}

// Findings returns the findings of the last audit for a domain.
func (a *NamespaceEscapeAuditor) Findings(domainID string) ([]EscapeFinding, error) {
	if isBlankDomainID(domainID) {
		return nil, ErrBlankDomain
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	ds, ok := a.domains[domainID]
	if !ok {
		return nil, ErrUnknownDomain
	}

	out := make([]EscapeFinding, len(ds.findings))
	copy(out, ds.findings)
	return out, nil
}

// Count returns the number of findings of the last audit.
func (a *NamespaceEscapeAuditor) Count(domainID string) (int, error) {
	if isBlankDomainID(domainID) {
		return 0, ErrBlankDomain
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	ds, ok := a.domains[domainID]
	if !ok {
		return 0, ErrUnknownDomain
	}
	return len(ds.findings), nil
}

// Quarantine stops a process that has a critical or high finding. An active
// ticket for the same process is reported as ErrAlreadyQuarantined.
func (a *NamespaceEscapeAuditor) Quarantine(domainID string, pid int) (QuarantineTicket, error) {
	if isBlankDomainID(domainID) {
		return QuarantineTicket{}, ErrBlankDomain
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	ds, ok := a.domains[domainID]
	if !ok {
		return QuarantineTicket{}, ErrUnknownDomain
	}

	for _, ticket := range a.tickets {
		if ticket.DomainID == domainID && ticket.PID == pid {
			return QuarantineTicket{}, ErrAlreadyQuarantined
		}
	}

	var matched *EscapeFinding
	for i := range ds.findings {
		finding := ds.findings[i]
		if finding.PID == pid &&
			(finding.Severity == SeverityCritical || finding.Severity == SeverityHigh) {
			matched = &finding
			break
		}
	}
	if matched == nil {
		return QuarantineTicket{}, ErrNoFinding
	}

	if a.limits.MaxTickets > 0 && len(a.tickets) >= a.limits.MaxTickets {
		return QuarantineTicket{}, ErrLimitExceeded
	}

	a.ticketSeq++
	ticket := QuarantineTicket{
		ID:       fmt.Sprintf("q-%d", a.ticketSeq),
		DomainID: domainID,
		PID:      pid,
		Seq:      matched.Seq,
	}
	a.tickets[ticket.ID] = ticket
	return ticket, nil
}

// Release lifts one quarantine ticket. Releasing an unknown or already
// released ticket reports ErrUnknownTicket. Release never suppresses future
// findings for the process.
func (a *NamespaceEscapeAuditor) Release(ticketID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if _, ok := a.tickets[ticketID]; !ok {
		return ErrUnknownTicket
	}
	delete(a.tickets, ticketID)
	return nil
}

// Snapshot serialises domains, baselines, current rows, pid reuse history,
// last-audit findings and active quarantine tickets.
func (a *NamespaceEscapeAuditor) Snapshot() ([]byte, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	dto := snapshotData{
		Version:   snapshotVersion,
		Limits:    a.limits,
		TicketSeq: a.ticketSeq,
		Domains:   make(map[string]Domain, len(a.domains)),
		Rows:      make(map[string]map[int]ProcessRow, len(a.domains)),
		History:   make(map[string]map[int][]uint64, len(a.domains)),
		Findings:  make(map[string][]EscapeFinding, len(a.domains)),
		Tickets:   make(map[string]QuarantineTicket, len(a.tickets)),
	}

	for domainID, ds := range a.domains {
		dto.Domains[domainID] = cloneDomain(ds.record)

		rows := make(map[int]ProcessRow, len(ds.rows))
		for pid, row := range ds.rows {
			rows[pid] = cloneRow(row)
		}
		dto.Rows[domainID] = rows

		history := make(map[int][]uint64, len(ds.history))
		for pid, startTimes := range ds.history {
			history[pid] = append([]uint64(nil), startTimes...)
		}
		dto.History[domainID] = history

		dto.Findings[domainID] = append([]EscapeFinding(nil), ds.findings...)
	}

	for ticketID, ticket := range a.tickets {
		dto.Tickets[ticketID] = ticket
	}

	data, err := json.Marshal(&dto)
	if err != nil {
		return nil, fmt.Errorf("namespace escape snapshot: %w", err)
	}
	return data, nil
}

// Restore replaces the whole auditor state from a snapshot. A malformed or
// inconsistent payload returns ErrBadSnapshot without touching live state.
func (a *NamespaceEscapeAuditor) Restore(data []byte) error {
	var dto snapshotData
	if err := json.Unmarshal(data, &dto); err != nil {
		return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	if err := validateSnapshot(&dto); err != nil {
		return err
	}

	domains := make(map[string]*domainState, len(dto.Domains))
	for domainID, record := range dto.Domains {
		ds := &domainState{
			record:   cloneDomain(record),
			rows:     make(map[int]ProcessRow),
			history:  make(map[int][]uint64),
			findings: append([]EscapeFinding(nil), dto.Findings[domainID]...),
		}
		for pid, row := range dto.Rows[domainID] {
			ds.rows[pid] = cloneRow(row)
		}
		for pid, startTimes := range dto.History[domainID] {
			ds.history[pid] = append([]uint64(nil), startTimes...)
		}
		domains[domainID] = ds
	}

	tickets := make(map[string]QuarantineTicket, len(dto.Tickets))
	for ticketID, ticket := range dto.Tickets {
		tickets[ticketID] = ticket
	}

	a.mu.Lock()
	a.domains = domains
	a.tickets = tickets
	a.ticketSeq = dto.TicketSeq
	a.mu.Unlock()
	return nil
}

// validateSnapshot checks structural integrity before any live state is
// swapped in.
func validateSnapshot(dto *snapshotData) error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", ErrBadSnapshot, fmt.Sprintf(format, args...))
	}

	if dto.Version != snapshotVersion {
		return bad("unsupported snapshot version %d", dto.Version)
	}
	if dto.Domains == nil {
		return bad("missing domains table")
	}

	for domainID, record := range dto.Domains {
		if isBlankDomainID(domainID) || record.ID != domainID || isBlankDomainID(record.ID) {
			return bad("domain id is inconsistent")
		}
		if !baselineComplete(record.Baseline) {
			return bad("incomplete baseline for domain %q", domainID)
		}
	}

	for domainID, rows := range dto.Rows {
		if _, ok := dto.Domains[domainID]; !ok {
			return bad("rows reference unknown domain %q", domainID)
		}
		for pid, row := range rows {
			if pid <= 0 || row.PID != pid || row.StartTime == 0 {
				return bad("invalid row identity in domain %q", domainID)
			}
			if row.DomainID != domainID {
				return bad("row domain id mismatch in domain %q", domainID)
			}
		}
	}

	for domainID, history := range dto.History {
		if _, ok := dto.Domains[domainID]; !ok {
			return bad("history references unknown domain %q", domainID)
		}
		for pid, startTimes := range history {
			if pid <= 0 {
				return bad("invalid history pid in domain %q", domainID)
			}
			for _, startTime := range startTimes {
				if startTime == 0 {
					return bad("zero historical start time for pid %d", pid)
				}
			}
		}
	}

	for domainID, findings := range dto.Findings {
		if _, ok := dto.Domains[domainID]; !ok {
			return bad("findings reference unknown domain %q", domainID)
		}
		for _, finding := range findings {
			if finding.ID == "" || finding.Kind == "" || finding.PID <= 0 {
				return bad("incomplete finding in domain %q", domainID)
			}
		}
	}

	for ticketID, ticket := range dto.Tickets {
		if ticketID == "" || ticket.ID != ticketID {
			return bad("ticket id is inconsistent")
		}
		if _, ok := dto.Domains[ticket.DomainID]; !ok {
			return bad("ticket %q references unknown domain", ticketID)
		}
		if ticket.PID <= 0 {
			return bad("ticket %q has invalid pid", ticketID)
		}
	}

	if dto.TicketSeq < 0 {
		return bad("negative ticket sequence")
	}
	return nil
}
