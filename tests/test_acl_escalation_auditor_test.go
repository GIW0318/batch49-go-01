package task

import (
	"errors"
	"fmt"
	"testing"

	impl "gsb/batch49-go-fix-a-13"
)

func t1Limits() impl.Limits {
	return impl.Limits{MaxPrincipals: 8, MaxNodes: 12, MaxFindings: 32}
}

func t1MustPrincipal(t *testing.T, a *impl.AclEscalationAuditor, p impl.Principal) {
	t.Helper()
	if err := a.RegisterPrincipal(p); err != nil {
		t.Fatalf("register principal %q must succeed, got %v", p.ID, err)
	}
}

func t1MustNode(t *testing.T, a *impl.AclEscalationAuditor, n impl.Node) {
	t.Helper()
	if err := a.AddNode(n); err != nil {
		t.Fatalf("add node %q must succeed, got %v", n.Path, err)
	}
}

func t1Service(t *testing.T, a *impl.AclEscalationAuditor) {
	t.Helper()
	t1MustPrincipal(t, a, impl.Principal{ID: "svc", UID: 2200, GID: 2200, Groups: []uint32{2200}})
}

func t1FindingByPath(t *testing.T, findings []impl.Finding, path string) impl.Finding {
	t.Helper()
	for _, f := range findings {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("no finding for path %q in %+v", path, findings)
	return impl.Finding{}
}

func TestRegisterPrincipalRejectsBlankDuplicateAndOverLimit(t *testing.T) {
	a := impl.NewAclEscalationAuditor(impl.Limits{MaxPrincipals: 2, MaxNodes: 4, MaxFindings: 8})
	if err := a.RegisterPrincipal(impl.Principal{ID: "   "}); !errors.Is(err, impl.ErrBlankPrincipal) {
		t.Fatalf("blank principal id must report ErrBlankPrincipal, got %v", err)
	}
	t1MustPrincipal(t, a, impl.Principal{ID: "alice", UID: 2001, GID: 2001})
	if err := a.RegisterPrincipal(impl.Principal{ID: "alice", UID: 2002, GID: 2002}); !errors.Is(err, impl.ErrDuplicatePrincipal) {
		t.Fatalf("duplicate principal id must report ErrDuplicatePrincipal, got %v", err)
	}
	t1MustPrincipal(t, a, impl.Principal{ID: "bob", UID: 2002, GID: 2002})
	if err := a.RegisterPrincipal(impl.Principal{ID: "carol", UID: 2003, GID: 2003}); !errors.Is(err, impl.ErrLimitExceeded) {
		t.Fatalf("third principal over cap must report ErrLimitExceeded, got %v", err)
	}
}

func TestAddNodeRejectsUncleanPaths(t *testing.T) {
	a := impl.NewAclEscalationAuditor(t1Limits())
	for _, path := range []string{"srv/app", "/srv/../etc", "/srv/app/", ""} {
		n := impl.Node{Path: path, Owner: 0, Group: 0, OwnerBits: impl.AccessRead}
		if err := a.AddNode(n); !errors.Is(err, impl.ErrPathInvalid) {
			t.Fatalf("path %q must report ErrPathInvalid, got %v", path, err)
		}
	}
	if _, ok := a.Node("/srv/app"); ok {
		t.Fatalf("rejected paths must not be registered")
	}
}

func TestAddNodeDuplicateAndCopySemantics(t *testing.T) {
	a := impl.NewAclEscalationAuditor(t1Limits())
	n := impl.Node{
		Path:      "/srv/app",
		Owner:     0,
		Group:     500,
		OwnerBits: impl.AccessFull,
		GroupBits: impl.AccessRead,
		OtherBits: impl.AccessRead,
		Entries:   []impl.Ace{{Kind: impl.AceUser, ID: 2001, Bits: impl.AccessRead | impl.AccessWrite}},
	}
	t1MustNode(t, a, n)
	if err := a.AddNode(n); !errors.Is(err, impl.ErrDuplicateNode) {
		t.Fatalf("duplicate path must report ErrDuplicateNode, got %v", err)
	}
	n.Entries[0].Bits = impl.AccessNone
	got, ok := a.Node("/srv/app")
	if !ok {
		t.Fatalf("registered node must be found")
	}
	if got.Entries[0].Bits != impl.AccessRead|impl.AccessWrite {
		t.Fatalf("caller mutation must not change stored entries, bits = %d", got.Entries[0].Bits)
	}
	got.Entries[0].Bits = impl.AccessNone
	again, _ := a.Node("/srv/app")
	if again.Entries[0].Bits != impl.AccessRead|impl.AccessWrite {
		t.Fatalf("returned node must be a copy, bits = %d", again.Entries[0].Bits)
	}
}

func TestEffectiveAccessOwnerAndMask(t *testing.T) {
	a := impl.NewAclEscalationAuditor(t1Limits())
	t1MustPrincipal(t, a, impl.Principal{ID: "alice", UID: 2001, GID: 2001, Groups: []uint32{500}})
	t1MustNode(t, a, impl.Node{
		Path: "/srv/own", Owner: 2001, Group: 2001,
		OwnerBits: impl.AccessRead | impl.AccessWrite,
		GroupBits: impl.AccessExecute,
	})
	t1MustNode(t, a, impl.Node{
		Path: "/srv/shared", Owner: 0, Group: 500,
		OwnerBits: impl.AccessFull,
		GroupBits: impl.AccessRead | impl.AccessWrite,
		OtherBits: impl.AccessRead,
		Entries: []impl.Ace{
			{Kind: impl.AceMask, ID: 0, Bits: impl.AccessRead},
			{Kind: impl.AceUser, ID: 2001, Bits: impl.AccessWrite | impl.AccessExecute},
			{Kind: impl.AceGroup, ID: 500, Bits: impl.AccessRead | impl.AccessWrite},
		},
	})
	got, err := a.EffectiveAccess("alice", "/srv/own")
	if err != nil {
		t.Fatalf("owner access must resolve, got %v", err)
	}
	if got != impl.AccessRead|impl.AccessWrite {
		t.Fatalf("owner access = %d, want read|write", got)
	}
	got, err = a.EffectiveAccess("alice", "/srv/shared")
	if err != nil {
		t.Fatalf("masked access must resolve, got %v", err)
	}
	if got != impl.AccessRead {
		t.Fatalf("masked access = %d, want read only (mask narrows named user and group entries)", got)
	}
}

func TestEffectiveAccessGroupAndOther(t *testing.T) {
	a := impl.NewAclEscalationAuditor(t1Limits())
	t1MustPrincipal(t, a, impl.Principal{ID: "alice", UID: 2001, GID: 2001, Groups: []uint32{500}})
	t1MustPrincipal(t, a, impl.Principal{ID: "bob", UID: 2002, GID: 600, Groups: []uint32{600}})
	t1MustNode(t, a, impl.Node{
		Path: "/srv/data", Owner: 0, Group: 500,
		OwnerBits: impl.AccessFull,
		GroupBits: impl.AccessRead | impl.AccessExecute,
		OtherBits: impl.AccessRead,
	})
	got, err := a.EffectiveAccess("alice", "/srv/data")
	if err != nil {
		t.Fatalf("group access must resolve, got %v", err)
	}
	if got != impl.AccessRead|impl.AccessExecute {
		t.Fatalf("group access = %d, want read|execute", got)
	}
	got, err = a.EffectiveAccess("bob", "/srv/data")
	if err != nil {
		t.Fatalf("other access must resolve, got %v", err)
	}
	if got != impl.AccessRead {
		t.Fatalf("other access = %d, want read", got)
	}
}

func TestEffectiveAccessUnknownPrincipalAndPath(t *testing.T) {
	a := impl.NewAclEscalationAuditor(t1Limits())
	t1MustPrincipal(t, a, impl.Principal{ID: "alice", UID: 2001, GID: 2001})
	t1MustNode(t, a, impl.Node{Path: "/srv/app", Owner: 0, OwnerBits: impl.AccessRead})
	if _, err := a.EffectiveAccess("ghost", "/srv/app"); !errors.Is(err, impl.ErrUnknownPrincipal) {
		t.Fatalf("unknown principal must report ErrUnknownPrincipal, got %v", err)
	}
	if _, err := a.EffectiveAccess("alice", "/srv/missing"); !errors.Is(err, impl.ErrNodeNotFound) {
		t.Fatalf("unknown path must report ErrNodeNotFound, got %v", err)
	}
}

func TestScanDirectWriteOnPrivilegedFile(t *testing.T) {
	a := impl.NewAclEscalationAuditor(t1Limits())
	t1Service(t, a)
	t1MustNode(t, a, impl.Node{
		Path: "/usr/local/bin/backup", Owner: 0, Group: 0,
		OtherBits: impl.AccessRead | impl.AccessExecute,
		SetUID:    true,
		Entries:   []impl.Ace{{Kind: impl.AceUser, ID: 2200, Bits: impl.AccessWrite}},
	})
	findings, err := a.Scan("svc")
	if err != nil {
		t.Fatalf("scan must succeed, got %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("finding count = %d, want 1: %+v", len(findings), findings)
	}
	f := findings[0]
	if f.Path != "/usr/local/bin/backup" || f.Reason != impl.ReasonDirectWrite {
		t.Fatalf("finding = %+v, want direct_write on /usr/local/bin/backup", f)
	}
	if f.Severity != impl.SeverityCritical {
		t.Fatalf("direct write on setuid file severity = %q, want critical", f.Severity)
	}
	if f.ViaGroup != 0 || f.PrincipalID != "svc" || f.ID == "" {
		t.Fatalf("finding attribution is wrong: %+v", f)
	}
	if len(f.Chain) != 1 || f.Chain[0] != "/usr/local/bin/backup" {
		t.Fatalf("direct finding chain = %v, want only the file itself", f.Chain)
	}
}

func TestScanAncestorDirectoryCarriesChain(t *testing.T) {
	a := impl.NewAclEscalationAuditor(t1Limits())
	t1Service(t, a)
	t1MustNode(t, a, impl.Node{
		Path: "/srv/bin", Owner: 0, Group: 0,
		OtherBits: impl.AccessRead | impl.AccessExecute,
		Entries:   []impl.Ace{{Kind: impl.AceUser, ID: 2200, Bits: impl.AccessRead | impl.AccessWrite | impl.AccessExecute}},
	})
	t1MustNode(t, a, impl.Node{
		Path: "/srv/bin/agent", Owner: 0, Group: 0,
		OtherBits: impl.AccessRead | impl.AccessExecute,
		SetUID:    true,
	})
	findings, err := a.Scan("svc")
	if err != nil {
		t.Fatalf("scan must succeed, got %v", err)
	}
	f := t1FindingByPath(t, findings, "/srv/bin")
	if f.Reason != impl.ReasonAncestorWrite || f.Severity != impl.SeverityHigh {
		t.Fatalf("ancestor finding = %+v, want high ancestor_write", f)
	}
	if len(f.Chain) != 2 || f.Chain[0] != "/srv/bin" || f.Chain[1] != "/srv/bin/agent" {
		t.Fatalf("ancestor chain = %v, want [/srv/bin /srv/bin/agent]", f.Chain)
	}
}

func TestScanGroupAndOtherAttribution(t *testing.T) {
	a := impl.NewAclEscalationAuditor(t1Limits())
	t1Service(t, a)
	t1MustNode(t, a, impl.Node{
		Path: "/srv/data", Owner: 0, Group: 2200,
		OtherBits: impl.AccessRead | impl.AccessExecute,
		Entries:   []impl.Ace{{Kind: impl.AceGroup, ID: 2200, Bits: impl.AccessRead | impl.AccessWrite | impl.AccessExecute}},
	})
	t1MustNode(t, a, impl.Node{
		Path: "/srv/data/tool", Owner: 0, Group: 0,
		OtherBits: impl.AccessRead | impl.AccessExecute,
		SetUID:    true,
	})
	t1MustNode(t, a, impl.Node{
		Path: "/srv/cache", Owner: 0, Group: 0,
		OtherBits: impl.AccessRead | impl.AccessWrite | impl.AccessExecute,
	})
	t1MustNode(t, a, impl.Node{
		Path: "/srv/cache/run", Owner: 0, Group: 0,
		OtherBits: impl.AccessRead | impl.AccessExecute,
		SetGID:    true,
	})
	findings, err := a.Scan("svc")
	if err != nil {
		t.Fatalf("scan must succeed, got %v", err)
	}
	groupFinding := t1FindingByPath(t, findings, "/srv/data")
	if groupFinding.Reason != impl.ReasonGroupWrite || groupFinding.ViaGroup != 2200 {
		t.Fatalf("group finding = %+v, want group_write via group 2200", groupFinding)
	}
	otherFinding := t1FindingByPath(t, findings, "/srv/cache")
	if otherFinding.Reason != impl.ReasonOtherWrite || otherFinding.ViaGroup != 0 {
		t.Fatalf("other finding = %+v, want other_write with no group", otherFinding)
	}
}

func TestScanSkipsOwnerAndPrivilegedPrincipal(t *testing.T) {
	a := impl.NewAclEscalationAuditor(t1Limits())
	t1MustPrincipal(t, a, impl.Principal{ID: "owner1", UID: 2300, GID: 2300})
	t1MustPrincipal(t, a, impl.Principal{ID: "root", UID: 0, GID: 0, Privileged: true})
	t1MustNode(t, a, impl.Node{
		Path: "/opt/tool", Owner: 2300, Group: 2300,
		OwnerBits: impl.AccessRead | impl.AccessWrite | impl.AccessExecute,
		SetUID:    true,
	})
	t1MustNode(t, a, impl.Node{
		Path: "/opt/rootbin", Owner: 0, Group: 0,
		OtherBits: impl.AccessRead | impl.AccessExecute,
		SetUID:    true,
		Entries:   []impl.Ace{{Kind: impl.AceUser, ID: 0, Bits: impl.AccessRead | impl.AccessWrite}},
	})
	ownerFindings, err := a.Scan("owner1")
	if err != nil {
		t.Fatalf("scan owner must succeed, got %v", err)
	}
	if len(ownerFindings) != 0 {
		t.Fatalf("owner writing own setuid file must not be an escalation, got %+v", ownerFindings)
	}
	rootFindings, err := a.Scan("root")
	if err != nil {
		t.Fatalf("scan root must succeed, got %v", err)
	}
	if len(rootFindings) != 0 {
		t.Fatalf("privileged principal must be excluded from findings, got %+v", rootFindings)
	}
}

func TestScanOrderingAndIdempotence(t *testing.T) {
	a := impl.NewAclEscalationAuditor(t1Limits())
	t1MustPrincipal(t, a, impl.Principal{ID: "p1", UID: 2201, GID: 2201})
	t1MustPrincipal(t, a, impl.Principal{ID: "p2", UID: 2202, GID: 2202})
	t1MustNode(t, a, impl.Node{Path: "/srv/a-bin", Owner: 0, OtherBits: impl.AccessRead | impl.AccessExecute, SetUID: true,
		Entries: []impl.Ace{{Kind: impl.AceUser, ID: 2202, Bits: impl.AccessWrite}}})
	t1MustNode(t, a, impl.Node{Path: "/srv/b-bin", Owner: 0, OtherBits: impl.AccessRead | impl.AccessExecute, SetUID: true,
		Entries: []impl.Ace{{Kind: impl.AceUser, ID: 2201, Bits: impl.AccessWrite}}})
	t1MustNode(t, a, impl.Node{Path: "/srv/z-dir", Owner: 0, OtherBits: impl.AccessRead | impl.AccessExecute,
		Entries: []impl.Ace{{Kind: impl.AceUser, ID: 2201, Bits: impl.AccessWrite | impl.AccessExecute}}})
	t1MustNode(t, a, impl.Node{Path: "/srv/z-dir/tool", Owner: 0, OtherBits: impl.AccessRead | impl.AccessExecute, SetUID: true})
	first, err := a.Scan("")
	if err != nil {
		t.Fatalf("scan all must succeed, got %v", err)
	}
	if len(first) != 3 {
		t.Fatalf("finding count = %d, want 3: %+v", len(first), first)
	}
	wantPaths := []string{"/srv/a-bin", "/srv/b-bin", "/srv/z-dir"}
	wantSeverity := []impl.Severity{impl.SeverityCritical, impl.SeverityCritical, impl.SeverityHigh}
	for i := range wantPaths {
		if first[i].Path != wantPaths[i] || first[i].Severity != wantSeverity[i] {
			t.Fatalf("finding %d = %+v, want path %q severity %q", i, first[i], wantPaths[i], wantSeverity[i])
		}
	}
	second, err := a.Scan("")
	if err != nil {
		t.Fatalf("second scan must succeed, got %v", err)
	}
	if fmt.Sprintf("%+v", first) != fmt.Sprintf("%+v", second) {
		t.Fatalf("repeated scan must return identical findings:\nfirst  %+v\nsecond %+v", first, second)
	}
}

func TestPlanRepairNarrowsGrantingEntryOnly(t *testing.T) {
	a := impl.NewAclEscalationAuditor(t1Limits())
	t1Service(t, a)
	t1MustNode(t, a, impl.Node{
		Path: "/usr/local/bin/backup", Owner: 0, Group: 0,
		OtherBits: impl.AccessRead | impl.AccessExecute,
		SetUID:    true,
		Entries: []impl.Ace{
			{Kind: impl.AceUser, ID: 0, Bits: impl.AccessRead | impl.AccessWrite},
			{Kind: impl.AceUser, ID: 2200, Bits: impl.AccessRead | impl.AccessWrite},
			{Kind: impl.AceGroup, ID: 2200, Bits: impl.AccessRead},
		},
	})
	if _, err := a.Scan("svc"); err != nil {
		t.Fatalf("scan must succeed, got %v", err)
	}
	plan, err := a.PlanRepair()
	if err != nil {
		t.Fatalf("plan must succeed, got %v", err)
	}
	if plan.Digest == "" {
		t.Fatalf("repair plan digest must not be empty")
	}
	if len(plan.Changes) != 1 {
		t.Fatalf("repair changes = %+v, want exactly one write removal", plan.Changes)
	}
	change := plan.Changes[0]
	if change.Path != "/usr/local/bin/backup" || change.Kind != impl.AceUser || change.ID != 2200 || change.Remove != impl.AccessWrite {
		t.Fatalf("repair change = %+v, want write removal from user 2200", change)
	}
	for _, c := range plan.Changes {
		if c.Kind == impl.AceUser && c.ID == 0 {
			t.Fatalf("plan must not touch the owner entry: %+v", c)
		}
	}
}

func TestApplyRemovesGrantAndVerifyIsClean(t *testing.T) {
	a := impl.NewAclEscalationAuditor(t1Limits())
	t1Service(t, a)
	t1MustNode(t, a, impl.Node{
		Path: "/usr/local/bin/backup", Owner: 0, Group: 0,
		OtherBits: impl.AccessRead | impl.AccessExecute,
		SetUID:    true,
		Entries:   []impl.Ace{{Kind: impl.AceUser, ID: 2200, Bits: impl.AccessRead | impl.AccessWrite}},
	})
	if _, err := a.Scan("svc"); err != nil {
		t.Fatalf("scan must succeed, got %v", err)
	}
	plan, err := a.PlanRepair()
	if err != nil {
		t.Fatalf("plan must succeed, got %v", err)
	}
	res, err := a.Apply(plan)
	if err != nil {
		t.Fatalf("apply must succeed, got %v", err)
	}
	if res.Applied != 1 || res.Remaining != 0 {
		t.Fatalf("apply result = %+v, want applied 1 remaining 0", res)
	}
	access, err := a.EffectiveAccess("svc", "/usr/local/bin/backup")
	if err != nil {
		t.Fatalf("effective access after apply must resolve, got %v", err)
	}
	if access&impl.AccessWrite != 0 {
		t.Fatalf("write must be removed after apply, access = %d", access)
	}
	if err := a.Verify(); err != nil {
		t.Fatalf("verify after repair must be clean, got %v", err)
	}
	after, err := a.Scan("svc")
	if err != nil {
		t.Fatalf("scan after repair must succeed, got %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("no finding may survive the repair, got %+v", after)
	}
}

func TestApplyRejectsStaleAndRepeatedPlan(t *testing.T) {
	a := impl.NewAclEscalationAuditor(t1Limits())
	t1Service(t, a)
	t1MustNode(t, a, impl.Node{
		Path: "/usr/local/bin/backup", Owner: 0, Group: 0,
		OtherBits: impl.AccessRead | impl.AccessExecute, SetUID: true,
		Entries: []impl.Ace{{Kind: impl.AceUser, ID: 2200, Bits: impl.AccessWrite}},
	})
	if _, err := a.Scan("svc"); err != nil {
		t.Fatalf("scan must succeed, got %v", err)
	}
	stale, err := a.PlanRepair()
	if err != nil {
		t.Fatalf("plan must succeed, got %v", err)
	}
	t1MustNode(t, a, impl.Node{Path: "/srv/extra", Owner: 0, OtherBits: impl.AccessRead})
	if _, err := a.Apply(stale); !errors.Is(err, impl.ErrPlanStale) {
		t.Fatalf("plan built before a tree change must report ErrPlanStale, got %v", err)
	}
	fresh, err := a.PlanRepair()
	if err != nil {
		t.Fatalf("fresh plan must succeed, got %v", err)
	}
	if _, err := a.Apply(fresh); err != nil {
		t.Fatalf("fresh plan must apply, got %v", err)
	}
	if _, err := a.Apply(fresh); !errors.Is(err, impl.ErrPlanStale) {
		t.Fatalf("replaying an applied plan must report ErrPlanStale, got %v", err)
	}
	access, err := a.EffectiveAccess("svc", "/usr/local/bin/backup")
	if err != nil {
		t.Fatalf("effective access must resolve, got %v", err)
	}
	if access&impl.AccessWrite != 0 {
		t.Fatalf("replayed plan must not restore write, access = %d", access)
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	a := impl.NewAclEscalationAuditor(t1Limits())
	t1Service(t, a)
	t1MustNode(t, a, impl.Node{Path: "/srv/app", Owner: 0, OtherBits: impl.AccessRead})
	data, err := a.Snapshot()
	if err != nil {
		t.Fatalf("snapshot must succeed, got %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload must not be empty")
	}
	t1MustNode(t, a, impl.Node{Path: "/srv/late", Owner: 0, OtherBits: impl.AccessRead})
	if err := a.Restore(data); err != nil {
		t.Fatalf("restore must succeed, got %v", err)
	}
	if _, ok := a.Node("/srv/late"); ok {
		t.Fatalf("restore must drop nodes added after the snapshot")
	}
	if _, ok := a.Node("/srv/app"); !ok {
		t.Fatalf("restore must bring back nodes captured in the snapshot")
	}
	if err := a.Restore([]byte("{not a snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must report ErrBadSnapshot, got %v", err)
	}
	if _, ok := a.Node("/srv/app"); !ok {
		t.Fatalf("failed restore must leave the live state untouched")
	}
	if _, ok := a.Node("/srv/late"); ok {
		t.Fatalf("failed restore must not resurrect post snapshot nodes")
	}
}
