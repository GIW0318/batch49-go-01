package task

import (
	"errors"
	"testing"

	impl "gsb/batch49-go-safety-d-03"
)

func d03Ref(pid int, start, domain string) impl.ProcessRef {
	return impl.ProcessRef{PID: pid, StartID: start, Domain: domain, Comm: "worker"}
}

func d03Limits() impl.LedgerLimits {
	return impl.LedgerLimits{MaxDomains: 4, MaxMembersPerDomain: 8, MaxScan: 8}
}

func d03Domain(t *testing.T, g *impl.ProcessDomainLedger, domain string) {
	t.Helper()
	if err := g.Create(domain); err != nil {
		t.Fatalf("Create(%q) must succeed, got %v", domain, err)
	}
}

func TestCreateRejectsBlankAndDuplicateDomain(t *testing.T) {
	g := impl.NewProcessDomainLedger(d03Limits())
	if err := g.Create("   "); !errors.Is(err, impl.ErrBlankDomain) {
		t.Fatalf("blank domain must get ErrBlankDomain, got %v", err)
	}
	d03Domain(t, g, "web")
	if err := g.Create("web"); !errors.Is(err, impl.ErrDomainExists) {
		t.Fatalf("duplicate domain must get ErrDomainExists, got %v", err)
	}
	domains := g.Domains()
	if len(domains) != 1 || domains[0] != "web" {
		t.Fatalf("domains = %v, want [web]", domains)
	}
}

func TestCreateEnforcesDomainLimit(t *testing.T) {
	g := impl.NewProcessDomainLedger(impl.LedgerLimits{MaxDomains: 2, MaxMembersPerDomain: 8, MaxScan: 8})
	d03Domain(t, g, "web")
	d03Domain(t, g, "db")
	if err := g.Create("cache"); !errors.Is(err, impl.ErrDomainLimit) {
		t.Fatalf("third domain must get ErrDomainLimit, got %v", err)
	}
	if got := len(g.Domains()); got != 2 {
		t.Fatalf("domain count = %d, want 2", got)
	}
}

func TestJoinRegistersSortedMembers(t *testing.T) {
	g := impl.NewProcessDomainLedger(d03Limits())
	d03Domain(t, g, "web")
	if err := g.Join("web", d03Ref(20, "s-20", "")); err != nil {
		t.Fatalf("join pid 20 must succeed, got %v", err)
	}
	if err := g.Join("web", d03Ref(10, "s-10", "")); err != nil {
		t.Fatalf("join pid 10 must succeed, got %v", err)
	}
	members := g.Members("web")
	if len(members) != 2 {
		t.Fatalf("member count = %d, want 2", len(members))
	}
	if members[0].PID != 10 || members[1].PID != 20 {
		t.Fatalf("members must be sorted by pid, got %d then %d", members[0].PID, members[1].PID)
	}
	if members[0].Domain != "web" {
		t.Fatalf("member domain = %q, want web", members[0].Domain)
	}
	if members[0].StartID != "s-10" {
		t.Fatalf("member start id = %q, want s-10", members[0].StartID)
	}
	stats := g.Stats()
	if stats.Domains != 1 || stats.Members != 2 {
		t.Fatalf("stats = %+v, want 1 domain and 2 members", stats)
	}
}

func TestJoinRejectsUnknownDomain(t *testing.T) {
	g := impl.NewProcessDomainLedger(d03Limits())
	if err := g.Join("web", d03Ref(10, "s-10", "")); !errors.Is(err, impl.ErrUnknownDomain) {
		t.Fatalf("join into unknown domain must get ErrUnknownDomain, got %v", err)
	}
	if got := len(g.Members("web")); got != 0 {
		t.Fatalf("rejected join must not create members, got %d", got)
	}
}

func TestJoinRejectsCrossDomainRef(t *testing.T) {
	g := impl.NewProcessDomainLedger(d03Limits())
	d03Domain(t, g, "web")
	d03Domain(t, g, "db")
	if err := g.Join("web", d03Ref(10, "s-10", "db")); !errors.Is(err, impl.ErrCrossDomain) {
		t.Fatalf("ref from another domain must get ErrCrossDomain, got %v", err)
	}
	if got := len(g.Members("web")); got != 0 {
		t.Fatalf("cross domain ref must not be registered, got %d members", got)
	}
}

func TestJoinRejectsDuplicateIdentity(t *testing.T) {
	g := impl.NewProcessDomainLedger(d03Limits())
	d03Domain(t, g, "web")
	if err := g.Join("web", d03Ref(10, "s-10", "")); err != nil {
		t.Fatalf("first join must succeed, got %v", err)
	}
	if err := g.Join("web", d03Ref(10, "s-10", "")); !errors.Is(err, impl.ErrAlreadyMember) {
		t.Fatalf("same identity must get ErrAlreadyMember, got %v", err)
	}
	if got := len(g.Members("web")); got != 1 {
		t.Fatalf("duplicate join must not add a member, got %d", got)
	}
}

func TestJoinAllowsReusedPIDWithNewStartID(t *testing.T) {
	g := impl.NewProcessDomainLedger(d03Limits())
	d03Domain(t, g, "web")
	if err := g.Join("web", d03Ref(10, "boot-a", "")); err != nil {
		t.Fatalf("first join must succeed, got %v", err)
	}
	if err := g.Join("web", d03Ref(10, "boot-b", "")); err != nil {
		t.Fatalf("reused pid with a new start id is a different process, got %v", err)
	}
	if got := len(g.Members("web")); got != 2 {
		t.Fatalf("member count = %d, want 2 for two start ids on the same pid", got)
	}
}

func TestMemberLimitEnforced(t *testing.T) {
	g := impl.NewProcessDomainLedger(impl.LedgerLimits{MaxDomains: 4, MaxMembersPerDomain: 2, MaxScan: 8})
	d03Domain(t, g, "web")
	if err := g.Join("web", d03Ref(10, "s-10", "")); err != nil {
		t.Fatalf("first join must succeed, got %v", err)
	}
	if err := g.Join("web", d03Ref(11, "s-11", "")); err != nil {
		t.Fatalf("second join must succeed, got %v", err)
	}
	if err := g.Join("web", d03Ref(12, "s-12", "")); !errors.Is(err, impl.ErrMemberLimit) {
		t.Fatalf("third join must get ErrMemberLimit, got %v", err)
	}
	if got := len(g.Members("web")); got != 2 {
		t.Fatalf("member count = %d, want 2", got)
	}
}

func TestLeaveRemovesMember(t *testing.T) {
	g := impl.NewProcessDomainLedger(d03Limits())
	d03Domain(t, g, "web")
	if err := g.Join("web", d03Ref(10, "s-10", "")); err != nil {
		t.Fatalf("join must succeed, got %v", err)
	}
	if err := g.Join("web", d03Ref(20, "s-20", "")); err != nil {
		t.Fatalf("join must succeed, got %v", err)
	}
	if err := g.Leave("web", 10); err != nil {
		t.Fatalf("leave must succeed, got %v", err)
	}
	members := g.Members("web")
	if len(members) != 1 || members[0].PID != 20 {
		t.Fatalf("members after leave = %+v, want only pid 20", members)
	}
	if err := g.Leave("web", 10); !errors.Is(err, impl.ErrUnknownProcess) {
		t.Fatalf("leaving twice must get ErrUnknownProcess, got %v", err)
	}
}

func TestLeaveAmbiguousPIDFails(t *testing.T) {
	g := impl.NewProcessDomainLedger(d03Limits())
	d03Domain(t, g, "web")
	if err := g.Join("web", d03Ref(10, "boot-a", "")); err != nil {
		t.Fatalf("join must succeed, got %v", err)
	}
	if err := g.Join("web", d03Ref(10, "boot-b", "")); err != nil {
		t.Fatalf("join must succeed, got %v", err)
	}
	if err := g.Leave("web", 10); !errors.Is(err, impl.ErrAmbiguousPID) {
		t.Fatalf("pid matching two members must get ErrAmbiguousPID, got %v", err)
	}
	if got := len(g.Members("web")); got != 2 {
		t.Fatalf("ambiguous leave must not remove anything, got %d members", got)
	}
}

func TestDestroyReturnsMembersAndDropsDomain(t *testing.T) {
	g := impl.NewProcessDomainLedger(d03Limits())
	d03Domain(t, g, "web")
	if err := g.Join("web", d03Ref(20, "s-20", "")); err != nil {
		t.Fatalf("join must succeed, got %v", err)
	}
	if err := g.Join("web", d03Ref(10, "s-10", "")); err != nil {
		t.Fatalf("join must succeed, got %v", err)
	}
	members, err := g.Destroy("web")
	if err != nil {
		t.Fatalf("destroy must succeed, got %v", err)
	}
	if len(members) != 2 || members[0].PID != 10 || members[1].PID != 20 {
		t.Fatalf("destroyed members = %+v, want sorted 10 and 20", members)
	}
	if got := len(g.Domains()); got != 0 {
		t.Fatalf("domains after destroy = %d, want 0", got)
	}
	if err := g.Join("web", d03Ref(10, "s-10", "")); !errors.Is(err, impl.ErrUnknownDomain) {
		t.Fatalf("join after destroy must get ErrUnknownDomain, got %v", err)
	}
	if _, err := g.Destroy("web"); !errors.Is(err, impl.ErrUnknownDomain) {
		t.Fatalf("destroying twice must get ErrUnknownDomain, got %v", err)
	}
}

func TestReconcileReportsUnregistered(t *testing.T) {
	g := impl.NewProcessDomainLedger(d03Limits())
	d03Domain(t, g, "web")
	if err := g.Join("web", d03Ref(10, "s-10", "")); err != nil {
		t.Fatalf("join must succeed, got %v", err)
	}
	rec := g.Reconcile([]impl.ProcessRef{
		d03Ref(10, "s-10", "web"),
		d03Ref(11, "s-11", "web"),
	})
	if len(rec.Unregistered) != 1 {
		t.Fatalf("unregistered = %+v, want one process", rec.Unregistered)
	}
	if rec.Unregistered[0].PID != 11 || rec.Unregistered[0].StartID != "s-11" {
		t.Fatalf("unregistered entry = %+v, want pid 11 start s-11", rec.Unregistered[0])
	}
	if len(rec.Exited) != 0 {
		t.Fatalf("exited = %d, want 0", len(rec.Exited))
	}
	if rec.Scanned != 2 {
		t.Fatalf("scanned count = %d, want 2", rec.Scanned)
	}
}

func TestReconcileReportsExitedWithoutMutation(t *testing.T) {
	g := impl.NewProcessDomainLedger(d03Limits())
	d03Domain(t, g, "web")
	if err := g.Join("web", d03Ref(20, "s-20", "")); err != nil {
		t.Fatalf("join must succeed, got %v", err)
	}
	if err := g.Join("web", d03Ref(10, "s-10", "")); err != nil {
		t.Fatalf("join must succeed, got %v", err)
	}
	rec := g.Reconcile(nil)
	if len(rec.Exited) != 2 {
		t.Fatalf("exited = %+v, want both members", rec.Exited)
	}
	if rec.Exited[0].PID != 10 || rec.Exited[1].PID != 20 {
		t.Fatalf("exited order = %d,%d, want ascending 10,20", rec.Exited[0].PID, rec.Exited[1].PID)
	}
	if got := len(g.Members("web")); got != 2 {
		t.Fatalf("reconcile must not mutate the ledger, members = %d", got)
	}
	if rec.Generation != g.Generation() {
		t.Fatalf("reconciliation generation = %d, want ledger generation %d", rec.Generation, g.Generation())
	}
}

func TestReconcileReportsDuplicated(t *testing.T) {
	g := impl.NewProcessDomainLedger(d03Limits())
	d03Domain(t, g, "web")
	if err := g.Join("web", d03Ref(10, "s-10", "")); err != nil {
		t.Fatalf("join must succeed, got %v", err)
	}
	rec := g.Reconcile([]impl.ProcessRef{
		d03Ref(10, "s-10", "web"),
		d03Ref(10, "s-10", "web"),
	})
	if len(rec.Duplicated) != 1 {
		t.Fatalf("duplicated = %+v, want one entry for the repeated identity", rec.Duplicated)
	}
	if rec.Duplicated[0].PID != 10 || rec.Duplicated[0].StartID != "s-10" {
		t.Fatalf("duplicated entry = %+v, want pid 10 start s-10", rec.Duplicated[0])
	}
	if len(rec.Exited) != 0 {
		t.Fatalf("exited = %d, want 0 because the member is still in the scan", len(rec.Exited))
	}
}

func TestSnapshotRestorePreservesLedger(t *testing.T) {
	g := impl.NewProcessDomainLedger(d03Limits())
	d03Domain(t, g, "web")
	d03Domain(t, g, "db")
	if err := g.Join("web", d03Ref(10, "s-10", "")); err != nil {
		t.Fatalf("join must succeed, got %v", err)
	}
	if err := g.Join("db", d03Ref(20, "s-20", "")); err != nil {
		t.Fatalf("join must succeed, got %v", err)
	}
	blob, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot must succeed, got %v", err)
	}
	if len(blob) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	h := impl.NewProcessDomainLedger(d03Limits())
	if err := h.Restore(blob); err != nil {
		t.Fatalf("restore must succeed, got %v", err)
	}
	domains := h.Domains()
	if len(domains) != 2 || domains[0] != "db" || domains[1] != "web" {
		t.Fatalf("domains after restore = %v, want [db web]", domains)
	}
	web := h.Members("web")
	if len(web) != 1 || web[0].PID != 10 || web[0].StartID != "s-10" {
		t.Fatalf("web members after restore = %+v, want pid 10 start s-10", web)
	}
	stats := h.Stats()
	if stats.Domains != 2 || stats.Members != 2 {
		t.Fatalf("stats after restore = %+v, want 2 domains and 2 members", stats)
	}
	if err := h.Join("web", d03Ref(10, "s-10", "")); !errors.Is(err, impl.ErrAlreadyMember) {
		t.Fatalf("restored membership must be enforced, got %v", err)
	}
}
