package task

import (
	"errors"
	"fmt"
	"testing"

	impl "gsb/batch49-go-fix-a-16"
)

func t4Limits() impl.Limits {
	return impl.Limits{MaxDomains: 4, MaxRowsPerDomain: 16, MaxTickets: 8}
}

func t4NS() impl.NamespaceSet {
	return impl.NamespaceSet{PID: 4001, Mount: 4002, Net: 4003, User: 4004, UTS: 4005, IPC: 4006}
}

func t4Domain(id string) impl.Domain {
	return impl.Domain{
		ID:       id,
		Baseline: t4NS(),
		UIDMap:   []impl.IDMap{{Inside: 0, Outside: 100000, Length: 65536}},
		InitPID:  1,
	}
}

func t4Register(t *testing.T, a *impl.NamespaceEscapeAuditor, id string) {
	t.Helper()
	if err := a.RegisterDomain(t4Domain(id)); err != nil {
		t.Fatalf("register domain %q must succeed, got %v", id, err)
	}
}

func t4Row(domainID string, pid, ppid int, start uint64) impl.ProcessRow {
	return impl.ProcessRow{
		DomainID:  domainID,
		PID:       pid,
		PPID:      ppid,
		StartTime: start,
		NS:        t4NS(),
		UID:       1000,
		EUID:      1000,
	}
}

func t4Observe(t *testing.T, a *impl.NamespaceEscapeAuditor, row impl.ProcessRow) {
	t.Helper()
	if err := a.Observe(row); err != nil {
		t.Fatalf("observe pid %d must succeed, got %v", row.PID, err)
	}
}

func t4Kinds(findings []impl.EscapeFinding) []string {
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		out = append(out, f.Kind)
	}
	return out
}

func TestRegisterDomainValidation(t *testing.T) {
	a := impl.NewNamespaceEscapeAuditor(impl.Limits{MaxDomains: 2, MaxRowsPerDomain: 8, MaxTickets: 4})
	if err := a.RegisterDomain(impl.Domain{ID: "  ", Baseline: t4NS(), InitPID: 1}); !errors.Is(err, impl.ErrBlankDomain) {
		t.Fatalf("blank domain must report ErrBlankDomain, got %v", err)
	}
	if err := a.RegisterDomain(impl.Domain{ID: "d1", Baseline: impl.NamespaceSet{PID: 1}, InitPID: 1}); !errors.Is(err, impl.ErrBadBaseline) {
		t.Fatalf("incomplete baseline must report ErrBadBaseline, got %v", err)
	}
	t4Register(t, a, "d1")
	if err := a.RegisterDomain(t4Domain("d1")); !errors.Is(err, impl.ErrDuplicateDomain) {
		t.Fatalf("duplicate domain must report ErrDuplicateDomain, got %v", err)
	}
	t4Register(t, a, "d2")
	if err := a.RegisterDomain(t4Domain("d3")); !errors.Is(err, impl.ErrLimitExceeded) {
		t.Fatalf("third domain over cap must report ErrLimitExceeded, got %v", err)
	}
}

func TestObserveRejectsBadRows(t *testing.T) {
	a := impl.NewNamespaceEscapeAuditor(t4Limits())
	t4Register(t, a, "d1")
	ghost := t4Row("ghost", 10, 1, 100)
	if err := a.Observe(ghost); !errors.Is(err, impl.ErrUnknownDomain) {
		t.Fatalf("row for unknown domain must report ErrUnknownDomain, got %v", err)
	}
	badPID := t4Row("d1", 0, 1, 100)
	if err := a.Observe(badPID); !errors.Is(err, impl.ErrBadRow) {
		t.Fatalf("pid zero must report ErrBadRow, got %v", err)
	}
	badStart := t4Row("d1", 10, 1, 0)
	if err := a.Observe(badStart); !errors.Is(err, impl.ErrBadRow) {
		t.Fatalf("zero start time must report ErrBadRow, got %v", err)
	}
	foreign := t4Row("other", 10, 1, 100)
	if err := a.Observe(foreign); !errors.Is(err, impl.ErrBadRow) {
		t.Fatalf("row whose domain id does not match must report ErrBadRow, got %v", err)
	}
	rows, err := a.Rows("d1")
	if err != nil {
		t.Fatalf("rows must succeed, got %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("rejected rows must not be stored, got %+v", rows)
	}
}

func TestObserveDuplicateRowIsIdempotent(t *testing.T) {
	a := impl.NewNamespaceEscapeAuditor(t4Limits())
	t4Register(t, a, "d1")
	row := t4Row("d1", 2, 1, 100)
	t4Observe(t, a, row)
	t4Observe(t, a, row)
	rows, err := a.Rows("d1")
	if err != nil {
		t.Fatalf("rows must succeed, got %v", err)
	}
	if len(rows) != 1 || rows[0].PID != 2 || rows[0].StartTime != 100 {
		t.Fatalf("rows = %+v, want one entry for pid 2 start 100", rows)
	}
	findings, err := a.Audit("d1")
	if err != nil {
		t.Fatalf("audit must succeed, got %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("duplicate observation of the same process must not create findings, got %+v", findings)
	}
}

func TestAuditUserNamespaceMismatch(t *testing.T) {
	a := impl.NewNamespaceEscapeAuditor(t4Limits())
	t4Register(t, a, "d1")
	row := t4Row("d1", 10, 1, 100)
	row.NS.User++
	t4Observe(t, a, row)
	findings, err := a.Audit("d1")
	if err != nil {
		t.Fatalf("audit must succeed, got %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("findings = %+v, want exactly one", findings)
	}
	f := findings[0]
	if f.Kind != impl.KindNSMismatch || f.Namespace != "user" || f.Severity != impl.SeverityCritical {
		t.Fatalf("finding = %+v, want critical ns_mismatch on user", f)
	}
	if f.PID != 10 || f.DomainID != "d1" || f.ID == "" {
		t.Fatalf("finding identity is wrong: %+v", f)
	}
}

func TestAuditPIDNamespaceMismatch(t *testing.T) {
	a := impl.NewNamespaceEscapeAuditor(t4Limits())
	t4Register(t, a, "d1")
	row := t4Row("d1", 10, 1, 100)
	row.NS.PID++
	t4Observe(t, a, row)
	findings, err := a.Audit("d1")
	if err != nil {
		t.Fatalf("audit must succeed, got %v", err)
	}
	if len(findings) != 1 || findings[0].Kind != impl.KindNSMismatch ||
		findings[0].Namespace != "pid" || findings[0].Severity != impl.SeverityCritical {
		t.Fatalf("findings = %+v, want critical ns_mismatch on pid", findings)
	}
}

func TestAuditMountOnlyDrift(t *testing.T) {
	a := impl.NewNamespaceEscapeAuditor(t4Limits())
	t4Register(t, a, "d1")
	row := t4Row("d1", 10, 1, 100)
	row.NS.Mount++
	t4Observe(t, a, row)
	findings, err := a.Audit("d1")
	if err != nil {
		t.Fatalf("audit must succeed, got %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("findings = %+v, want exactly one", findings)
	}
	f := findings[0]
	if f.Kind != impl.KindMountDrift || f.Namespace != "mount" || f.Severity != impl.SeverityCritical {
		t.Fatalf("finding = %+v, want critical mount_drift with mount namespace", f)
	}
}

func TestAuditOrphanProcess(t *testing.T) {
	a := impl.NewNamespaceEscapeAuditor(t4Limits())
	t4Register(t, a, "d1")
	t4Observe(t, a, t4Row("d1", 10, 0, 100))
	t4Observe(t, a, t4Row("d1", 20, 999, 200))
	findings, err := a.Audit("d1")
	if err != nil {
		t.Fatalf("audit must succeed, got %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("findings = %+v, want two orphan findings", findings)
	}
	for _, f := range findings {
		if f.Kind != impl.KindOrphan || f.Severity != impl.SeverityHigh {
			t.Fatalf("finding = %+v, want high orphan", f)
		}
	}
}

func TestAuditPrivilegeShift(t *testing.T) {
	a := impl.NewNamespaceEscapeAuditor(t4Limits())
	t4Register(t, a, "d1")
	row := t4Row("d1", 10, 1, 100)
	row.EUID = 0
	t4Observe(t, a, row)
	findings, err := a.Audit("d1")
	if err != nil {
		t.Fatalf("audit must succeed, got %v", err)
	}
	if len(findings) != 1 || findings[0].Kind != impl.KindPrivilegeShift || findings[0].Severity != impl.SeverityCritical {
		t.Fatalf("findings = %+v, want one critical privilege_shift", findings)
	}
}

func TestAuditCapabilityRetainedAfterDrop(t *testing.T) {
	a := impl.NewNamespaceEscapeAuditor(t4Limits())
	t4Register(t, a, "d1")
	row := t4Row("d1", 10, 1, 100)
	row.Caps = []string{"cap_net_admin"}
	t4Observe(t, a, row)
	findings, err := a.Audit("d1")
	if err != nil {
		t.Fatalf("audit must succeed, got %v", err)
	}
	if len(findings) != 1 || findings[0].Kind != impl.KindCapRetained || findings[0].Severity != impl.SeverityHigh {
		t.Fatalf("findings = %+v, want one high cap_retained", findings)
	}
}

func TestAuditPIDReuse(t *testing.T) {
	a := impl.NewNamespaceEscapeAuditor(t4Limits())
	t4Register(t, a, "d1")
	t4Observe(t, a, t4Row("d1", 42, 1, 100))
	t4Observe(t, a, t4Row("d1", 42, 1, 200))
	rows, err := a.Rows("d1")
	if err != nil {
		t.Fatalf("rows must succeed, got %v", err)
	}
	if len(rows) != 1 || rows[0].PID != 42 || rows[0].StartTime != 200 {
		t.Fatalf("rows = %+v, want the latest observation for pid 42", rows)
	}
	findings, err := a.Audit("d1")
	if err != nil {
		t.Fatalf("audit must succeed, got %v", err)
	}
	if len(findings) != 1 || findings[0].Kind != impl.KindPIDReuse || findings[0].Severity != impl.SeverityHigh {
		t.Fatalf("findings = %+v, want one high pid_reuse", findings)
	}
	if findings[0].PID != 42 {
		t.Fatalf("pid_reuse finding pid = %d, want 42", findings[0].PID)
	}
}

func TestAuditOrderingAndIdempotence(t *testing.T) {
	a := impl.NewNamespaceEscapeAuditor(t4Limits())
	t4Register(t, a, "d1")
	init := t4Row("d1", 1, 0, 50)
	init.UID = 0
	init.EUID = 0
	t4Observe(t, a, init)
	shift := t4Row("d1", 10, 1, 100)
	shift.EUID = 0
	t4Observe(t, a, shift)
	drift := t4Row("d1", 20, 1, 200)
	drift.NS.Mount++
	t4Observe(t, a, drift)
	caps := t4Row("d1", 30, 1, 300)
	caps.Caps = []string{"cap_sys_admin"}
	t4Observe(t, a, caps)
	first, err := a.Audit("d1")
	if err != nil {
		t.Fatalf("audit must succeed, got %v", err)
	}
	wantKinds := []string{impl.KindPrivilegeShift, impl.KindMountDrift, impl.KindCapRetained}
	wantPIDs := []int{10, 20, 30}
	if len(first) != len(wantKinds) {
		t.Fatalf("findings = %+v, want %d entries", first, len(wantKinds))
	}
	for i := range wantKinds {
		if first[i].Kind != wantKinds[i] || first[i].PID != wantPIDs[i] {
			t.Fatalf("finding %d = %+v, want kind %q pid %d (severity desc then pid asc)", i, first[i], wantKinds[i], wantPIDs[i])
		}
	}
	second, err := a.Audit("d1")
	if err != nil {
		t.Fatalf("second audit must succeed, got %v", err)
	}
	if fmt.Sprintf("%+v", first) != fmt.Sprintf("%+v", second) {
		t.Fatalf("repeated audit must be identical:\nfirst  %+v\nsecond %+v", first, second)
	}
}

func TestAuditCleanDomainHasNoFindings(t *testing.T) {
	a := impl.NewNamespaceEscapeAuditor(t4Limits())
	t4Register(t, a, "d1")
	init := t4Row("d1", 1, 0, 50)
	init.UID = 0
	init.EUID = 0
	t4Observe(t, a, init)
	t4Observe(t, a, t4Row("d1", 2, 1, 100))
	findings, err := a.Audit("d1")
	if err != nil {
		t.Fatalf("audit must succeed, got %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("clean domain must have no findings, got %+v", findings)
	}
	count, err := a.Count("d1")
	if err != nil {
		t.Fatalf("count must succeed, got %v", err)
	}
	if count != 0 {
		t.Fatalf("count = %d, want 0", count)
	}
}

func TestQuarantineRequiresFinding(t *testing.T) {
	a := impl.NewNamespaceEscapeAuditor(t4Limits())
	t4Register(t, a, "d1")
	t4Observe(t, a, t4Row("d1", 10, 1, 100))
	if _, err := a.Audit("d1"); err != nil {
		t.Fatalf("audit must succeed, got %v", err)
	}
	if _, err := a.Quarantine("d1", 10); !errors.Is(err, impl.ErrNoFinding) {
		t.Fatalf("quarantine without a finding must report ErrNoFinding, got %v", err)
	}
	row := t4Row("d1", 11, 1, 200)
	row.EUID = 0
	t4Observe(t, a, row)
	if _, err := a.Audit("d1"); err != nil {
		t.Fatalf("audit must succeed, got %v", err)
	}
	ticket, err := a.Quarantine("d1", 11)
	if err != nil {
		t.Fatalf("quarantine must succeed, got %v", err)
	}
	if ticket.ID == "" || ticket.DomainID != "d1" || ticket.PID != 11 {
		t.Fatalf("ticket = %+v, want a non empty ticket for d1 pid 11", ticket)
	}
	if _, err := a.Quarantine("d1", 11); !errors.Is(err, impl.ErrAlreadyQuarantined) {
		t.Fatalf("second quarantine must report ErrAlreadyQuarantined, got %v", err)
	}
}

func TestReleaseTicket(t *testing.T) {
	a := impl.NewNamespaceEscapeAuditor(t4Limits())
	t4Register(t, a, "d1")
	row := t4Row("d1", 11, 1, 200)
	row.EUID = 0
	t4Observe(t, a, row)
	if _, err := a.Audit("d1"); err != nil {
		t.Fatalf("audit must succeed, got %v", err)
	}
	ticket, err := a.Quarantine("d1", 11)
	if err != nil {
		t.Fatalf("quarantine must succeed, got %v", err)
	}
	if err := a.Release("q-unknown"); !errors.Is(err, impl.ErrUnknownTicket) {
		t.Fatalf("unknown ticket must report ErrUnknownTicket, got %v", err)
	}
	if err := a.Release(ticket.ID); err != nil {
		t.Fatalf("release must succeed, got %v", err)
	}
	if err := a.Release(ticket.ID); !errors.Is(err, impl.ErrUnknownTicket) {
		t.Fatalf("second release must report ErrUnknownTicket, got %v", err)
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	a := impl.NewNamespaceEscapeAuditor(t4Limits())
	t4Register(t, a, "d1")
	t4Observe(t, a, t4Row("d1", 2, 1, 100))
	row := t4Row("d1", 10, 1, 200)
	row.EUID = 0
	t4Observe(t, a, row)
	if _, err := a.Audit("d1"); err != nil {
		t.Fatalf("audit must succeed, got %v", err)
	}
	ticket, err := a.Quarantine("d1", 10)
	if err != nil {
		t.Fatalf("quarantine must succeed, got %v", err)
	}
	data, err := a.Snapshot()
	if err != nil {
		t.Fatalf("snapshot must succeed, got %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	if err := a.Release(ticket.ID); err != nil {
		t.Fatalf("release before restore must succeed, got %v", err)
	}
	t4Observe(t, a, t4Row("d1", 12, 1, 300))
	if err := a.Restore(data); err != nil {
		t.Fatalf("restore must succeed, got %v", err)
	}
	rows, err := a.Rows("d1")
	if err != nil {
		t.Fatalf("rows must succeed, got %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows after restore = %+v, want the two snapshotted rows", rows)
	}
	findings, err := a.Findings("d1")
	if err != nil {
		t.Fatalf("findings must succeed, got %v", err)
	}
	if len(findings) != 1 || findings[0].Kind != impl.KindPrivilegeShift {
		t.Fatalf("findings after restore = %+v, want the snapshotted privilege_shift", findings)
	}
	if err := a.Release(ticket.ID); err != nil {
		t.Fatalf("snapshotted ticket must be active after restore, got %v", err)
	}
	if err := a.Restore([]byte("broken")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must report ErrBadSnapshot, got %v", err)
	}
	rows, err = a.Rows("d1")
	if err != nil {
		t.Fatalf("rows must succeed, got %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("failed restore must leave live rows untouched, got %+v", rows)
	}
}
