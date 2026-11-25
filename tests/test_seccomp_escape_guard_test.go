package task

import (
	"errors"
	"testing"

	impl "gsb/batch49-go-fix-a-14"
)

func t2Limits() impl.Limits {
	return impl.Limits{MaxWorkloads: 4, MaxRules: 16, MaxObserved: 8, MaxStrikes: 3}
}

func t2Rules() []impl.Rule {
	return []impl.Rule{
		{Name: "read"},
		{Name: "write"},
		{Name: "openat", Arg0Mask: 0xF, Arg0Value: 0x1},
		{Name: "exit_group"},
		{Name: "rt_sigreturn"},
	}
}

func t2Enroll(t *testing.T, g *impl.SeccompEscapeGuard, id string, rules []impl.Rule) {
	t.Helper()
	if err := g.Enroll(impl.WorkloadSpec{ID: id, Allowed: rules}); err != nil {
		t.Fatalf("enroll %q must succeed, got %v", id, err)
	}
}

func t2Observe(t *testing.T, g *impl.SeccompEscapeGuard, id, name string, arg0 uint64) {
	t.Helper()
	if err := g.Observe(id, impl.Call{Name: name, Arg0: arg0}); err != nil {
		t.Fatalf("observe %q on %q must succeed, got %v", name, id, err)
	}
}

func t2Enforce(t *testing.T, g *impl.SeccompEscapeGuard, id string, rules []impl.Rule, observed ...impl.Call) {
	t.Helper()
	t2Enroll(t, g, id, rules)
	for _, c := range observed {
		t2Observe(t, g, id, c.Name, c.Arg0)
	}
	if _, err := g.Propose(id); err != nil {
		t.Fatalf("propose for %q must succeed, got %v", id, err)
	}
	if _, err := g.Promote(id); err != nil {
		t.Fatalf("promote for %q must succeed, got %v", id, err)
	}
}

func t2Contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func t2HasRule(rules []impl.Rule, want impl.Rule) bool {
	for _, r := range rules {
		if r == want {
			return true
		}
	}
	return false
}

func TestEnrollRejectsBlankDuplicateAndEmptyProfile(t *testing.T) {
	g := impl.NewSeccompEscapeGuard(impl.Limits{MaxWorkloads: 2, MaxRules: 8, MaxObserved: 4, MaxStrikes: 3})
	if err := g.Enroll(impl.WorkloadSpec{ID: " ", Allowed: []impl.Rule{{Name: "read"}}}); !errors.Is(err, impl.ErrBlankWorkload) {
		t.Fatalf("blank workload must report ErrBlankWorkload, got %v", err)
	}
	if err := g.Enroll(impl.WorkloadSpec{ID: "c1"}); !errors.Is(err, impl.ErrEmptyProfile) {
		t.Fatalf("empty allow list must report ErrEmptyProfile, got %v", err)
	}
	t2Enroll(t, g, "c1", []impl.Rule{{Name: "read"}})
	if err := g.Enroll(impl.WorkloadSpec{ID: "c1", Allowed: []impl.Rule{{Name: "write"}}}); !errors.Is(err, impl.ErrDuplicateWorkload) {
		t.Fatalf("duplicate workload must report ErrDuplicateWorkload, got %v", err)
	}
	t2Enroll(t, g, "c2", []impl.Rule{{Name: "write"}})
	if err := g.Enroll(impl.WorkloadSpec{ID: "c3", Allowed: []impl.Rule{{Name: "openat"}}}); !errors.Is(err, impl.ErrLimitExceeded) {
		t.Fatalf("third workload over cap must report ErrLimitExceeded, got %v", err)
	}
}

func TestEnrollRejectsBadRules(t *testing.T) {
	g := impl.NewSeccompEscapeGuard(impl.Limits{MaxWorkloads: 4, MaxRules: 2, MaxObserved: 4, MaxStrikes: 3})
	if err := g.Enroll(impl.WorkloadSpec{ID: "c1", Allowed: []impl.Rule{{Name: "read"}, {Name: "read"}}}); !errors.Is(err, impl.ErrInvalidCall) {
		t.Fatalf("duplicate rule name must report ErrInvalidCall, got %v", err)
	}
	if err := g.Enroll(impl.WorkloadSpec{ID: "c1", Allowed: []impl.Rule{{Name: "  "}}}); !errors.Is(err, impl.ErrInvalidCall) {
		t.Fatalf("blank rule name must report ErrInvalidCall, got %v", err)
	}
	if err := g.Enroll(impl.WorkloadSpec{ID: "c1", Allowed: []impl.Rule{{Name: "read"}, {Name: "write"}, {Name: "openat"}}}); !errors.Is(err, impl.ErrLimitExceeded) {
		t.Fatalf("rule count over cap must report ErrLimitExceeded, got %v", err)
	}
}

func TestObserveCountsDistinctCalls(t *testing.T) {
	g := impl.NewSeccompEscapeGuard(t2Limits())
	t2Enroll(t, g, "c1", t2Rules())
	t2Observe(t, g, "c1", "read", 0)
	t2Observe(t, g, "c1", "read", 0)
	t2Observe(t, g, "c1", "openat", 1)
	obs, err := g.Observations("c1")
	if err != nil {
		t.Fatalf("observations must succeed, got %v", err)
	}
	if len(obs) != 2 {
		t.Fatalf("distinct observation count = %d, want 2: %+v", len(obs), obs)
	}
	if obs[0].Name != "read" || obs[0].Count != 2 || obs[1].Name != "openat" || obs[1].Count != 1 {
		t.Fatalf("observations = %+v, want read=2 then openat=1 in first seen order", obs)
	}
	if obs[0].FirstSeq <= 0 || obs[1].FirstSeq <= obs[0].FirstSeq {
		t.Fatalf("first seen sequences must increase: %+v", obs)
	}
	if err := g.Observe("ghost", impl.Call{Name: "read"}); !errors.Is(err, impl.ErrUnknownWorkload) {
		t.Fatalf("unknown workload must report ErrUnknownWorkload, got %v", err)
	}
	if err := g.Observe("c1", impl.Call{Name: ""}); !errors.Is(err, impl.ErrInvalidCall) {
		t.Fatalf("blank call must report ErrInvalidCall, got %v", err)
	}
}

func TestObserveRejectedOutsideAuditWindow(t *testing.T) {
	g := impl.NewSeccompEscapeGuard(t2Limits())
	t2Enforce(t, g, "c1", t2Rules(), impl.Call{Name: "read"})
	if err := g.Observe("c1", impl.Call{Name: "read"}); !errors.Is(err, impl.ErrNotAuditing) {
		t.Fatalf("observe on an enforced workload must report ErrNotAuditing, got %v", err)
	}
}

func TestProposeNarrowsToObservedCalls(t *testing.T) {
	g := impl.NewSeccompEscapeGuard(t2Limits())
	t2Enroll(t, g, "c1", t2Rules())
	t2Observe(t, g, "c1", "read", 0)
	t2Observe(t, g, "c1", "openat", 1)
	draft, err := g.Propose("c1")
	if err != nil {
		t.Fatalf("propose must succeed, got %v", err)
	}
	if !t2Contains(draft.Removed, "write") {
		t.Fatalf("removed = %v, want the unobserved write rule", draft.Removed)
	}
	if t2Contains(draft.Removed, "read") || t2Contains(draft.Removed, "openat") {
		t.Fatalf("removed = %v, must not drop observed calls", draft.Removed)
	}
	if !t2HasRule(draft.Allowed, impl.Rule{Name: "read"}) {
		t.Fatalf("allowed = %+v, want observed read kept", draft.Allowed)
	}
	if !t2HasRule(draft.Allowed, impl.Rule{Name: "openat", Arg0Mask: 0xF, Arg0Value: 0x1}) {
		t.Fatalf("allowed = %+v, want the original openat argument constraint kept verbatim", draft.Allowed)
	}
	if draft.Generation != 2 || draft.Digest == "" || draft.WorkloadID != "c1" {
		t.Fatalf("draft metadata = %+v, want generation 2, non empty digest and c1", draft)
	}
	again, err := g.Propose("c1")
	if err != nil {
		t.Fatalf("repeated propose must succeed, got %v", err)
	}
	if again.Digest != draft.Digest {
		t.Fatalf("repeated propose digest = %q, want stable %q", again.Digest, draft.Digest)
	}
}

func TestProposeRequiresObservationAndAuditState(t *testing.T) {
	g := impl.NewSeccompEscapeGuard(t2Limits())
	t2Enroll(t, g, "c1", t2Rules())
	if _, err := g.Propose("c1"); !errors.Is(err, impl.ErrNoObservation) {
		t.Fatalf("propose without observation must report ErrNoObservation, got %v", err)
	}
	t2Observe(t, g, "c1", "read", 0)
	if _, err := g.Propose("c1"); err != nil {
		t.Fatalf("propose must succeed, got %v", err)
	}
	if _, err := g.Promote("c1"); err != nil {
		t.Fatalf("promote must succeed, got %v", err)
	}
	if _, err := g.Propose("c1"); !errors.Is(err, impl.ErrNotAuditing) {
		t.Fatalf("propose on enforced workload must report ErrNotAuditing, got %v", err)
	}
}

func TestProposeNeverWidensTheAllowList(t *testing.T) {
	g := impl.NewSeccompEscapeGuard(t2Limits())
	initial := []impl.Rule{{Name: "read"}, {Name: "openat", Arg0Mask: 0xF, Arg0Value: 0x1}}
	t2Enroll(t, g, "c1", initial)
	t2Observe(t, g, "c1", "openat", 0x2)
	t2Observe(t, g, "c1", "bpf", 0)
	draft, err := g.Propose("c1")
	if err != nil {
		t.Fatalf("propose must succeed, got %v", err)
	}
	for _, r := range draft.Allowed {
		if !t2HasRule(initial, r) {
			t.Fatalf("proposed rule %+v is not in the current profile, narrowing must not add rules", r)
		}
		if r.Name == "bpf" {
			t.Fatalf("a syscall outside the current profile must not be added by narrowing")
		}
	}
	if !t2HasRule(draft.Allowed, initial[1]) {
		t.Fatalf("allowed = %+v, want the observed openat rule kept with its original constraint", draft.Allowed)
	}
	if draft.Generation != 2 {
		t.Fatalf("draft generation = %d, want 2", draft.Generation)
	}
}

func TestPromoteRequiresDraftAndLocksAuditWindow(t *testing.T) {
	g := impl.NewSeccompEscapeGuard(t2Limits())
	t2Enroll(t, g, "c1", t2Rules())
	if _, err := g.Promote("c1"); !errors.Is(err, impl.ErrNoDraft) {
		t.Fatalf("promote without draft must report ErrNoDraft, got %v", err)
	}
	t2Observe(t, g, "c1", "read", 0)
	if _, err := g.Propose("c1"); err != nil {
		t.Fatalf("propose must succeed, got %v", err)
	}
	profile, err := g.Promote("c1")
	if err != nil {
		t.Fatalf("promote must succeed, got %v", err)
	}
	if profile.Generation != 2 {
		t.Fatalf("promoted generation = %d, want 2", profile.Generation)
	}
	state, err := g.State("c1")
	if err != nil {
		t.Fatalf("state must succeed, got %v", err)
	}
	if state != impl.StateEnforced {
		t.Fatalf("state = %q, want enforced", state)
	}
	if _, err := g.Promote("c1"); !errors.Is(err, impl.ErrNotAuditing) {
		t.Fatalf("second promote must report ErrNotAuditing, got %v", err)
	}
}

func TestCheckAuditModeAllowsEverything(t *testing.T) {
	g := impl.NewSeccompEscapeGuard(t2Limits())
	t2Enroll(t, g, "c1", []impl.Rule{{Name: "read"}})
	d, err := g.Check("c1", impl.Call{Name: "bpf", Arg0: 9})
	if err != nil {
		t.Fatalf("check must succeed, got %v", err)
	}
	if d.Action != impl.ActionAllow || d.Reason != impl.ReasonAuditMode {
		t.Fatalf("audit decision = %+v, want allow with audit_mode reason", d)
	}
	if d.Strikes != 0 || d.Quarantined {
		t.Fatalf("audit decision must not strike or quarantine: %+v", d)
	}
}

func TestCheckEnforcedDeniesAndQuarantinesAtThreshold(t *testing.T) {
	g := impl.NewSeccompEscapeGuard(t2Limits())
	t2Enforce(t, g, "c1", []impl.Rule{{Name: "read"}}, impl.Call{Name: "read"})
	for i := 1; i <= 2; i++ {
		d, err := g.Check("c1", impl.Call{Name: "write"})
		if err != nil {
			t.Fatalf("check %d must succeed, got %v", i, err)
		}
		if d.Action != impl.ActionDeny || d.Reason != impl.ReasonBlocked || d.Strikes != i || d.Quarantined {
			t.Fatalf("check %d = %+v, want deny, blocked, strike %d, not quarantined", i, d, i)
		}
	}
	d, err := g.Check("c1", impl.Call{Name: "write"})
	if err != nil {
		t.Fatalf("third check must succeed, got %v", err)
	}
	if d.Action != impl.ActionKill || d.Strikes != 3 || !d.Quarantined {
		t.Fatalf("third blocked call = %+v, want kill at strike 3 and quarantine", d)
	}
	state, err := g.State("c1")
	if err != nil {
		t.Fatalf("state must succeed, got %v", err)
	}
	if state != impl.StateQuarantined {
		t.Fatalf("state = %q, want quarantined", state)
	}
}

func TestCheckEnforcedHonorsArgumentConstraint(t *testing.T) {
	g := impl.NewSeccompEscapeGuard(t2Limits())
	rules := []impl.Rule{{Name: "openat", Arg0Mask: 0xF, Arg0Value: 0x1}}
	t2Enforce(t, g, "c1", rules, impl.Call{Name: "openat", Arg0: 0x1})
	ok, err := g.Check("c1", impl.Call{Name: "openat", Arg0: 0x1})
	if err != nil {
		t.Fatalf("check must succeed, got %v", err)
	}
	if ok.Action != impl.ActionAllow || ok.Reason != impl.ReasonAllowed {
		t.Fatalf("matching arg decision = %+v, want allow", ok)
	}
	bad, err := g.Check("c1", impl.Call{Name: "openat", Arg0: 0x2})
	if err != nil {
		t.Fatalf("check must succeed, got %v", err)
	}
	if bad.Action != impl.ActionDeny || bad.Strikes != 1 {
		t.Fatalf("non matching arg decision = %+v, want deny with strike 1", bad)
	}
}

func TestDangerousCallKillsImmediately(t *testing.T) {
	g := impl.NewSeccompEscapeGuard(t2Limits())
	t2Enforce(t, g, "c1", []impl.Rule{{Name: "read"}}, impl.Call{Name: "read"})
	d, err := g.Check("c1", impl.Call{Name: "bpf"})
	if err != nil {
		t.Fatalf("check must succeed, got %v", err)
	}
	if d.Action != impl.ActionKill || d.Reason != impl.ReasonEscape || !d.Quarantined {
		t.Fatalf("dangerous call decision = %+v, want kill with escape reason and quarantine", d)
	}
	if d.Strikes != 1 {
		t.Fatalf("dangerous call strikes = %d, want 1", d.Strikes)
	}
	attempts, err := g.Attempts("c1")
	if err != nil {
		t.Fatalf("attempts must succeed, got %v", err)
	}
	if len(attempts) != 1 || attempts[0].Name != "bpf" || attempts[0].Reason != impl.ReasonEscape {
		t.Fatalf("attempts = %+v, want one bpf escape attempt", attempts)
	}
}

func TestReleaseReturnsToAuditAndClearsWindow(t *testing.T) {
	g := impl.NewSeccompEscapeGuard(t2Limits())
	t2Enforce(t, g, "c1", []impl.Rule{{Name: "write"}}, impl.Call{Name: "write"})
	if err := g.Quarantine("c1", "manual review"); err != nil {
		t.Fatalf("quarantine must succeed, got %v", err)
	}
	if err := g.Release("c1"); err != nil {
		t.Fatalf("release must succeed, got %v", err)
	}
	state, err := g.State("c1")
	if err != nil {
		t.Fatalf("state must succeed, got %v", err)
	}
	if state != impl.StateAudit {
		t.Fatalf("state after release = %q, want audit", state)
	}
	strikes, err := g.Strikes("c1")
	if err != nil {
		t.Fatalf("strikes must succeed, got %v", err)
	}
	if strikes != 0 {
		t.Fatalf("strikes after release = %d, want 0", strikes)
	}
	obs, err := g.Observations("c1")
	if err != nil {
		t.Fatalf("observations must succeed, got %v", err)
	}
	if len(obs) != 0 {
		t.Fatalf("release must open a fresh audit window, got %+v", obs)
	}
	d, err := g.Check("c1", impl.Call{Name: "read"})
	if err != nil {
		t.Fatalf("check after release must succeed, got %v", err)
	}
	if d.Action != impl.ActionAllow || d.Reason != impl.ReasonAuditMode {
		t.Fatalf("check after release = %+v, want audit mode allow", d)
	}
	if err := g.Release("c1"); !errors.Is(err, impl.ErrNotQuarantined) {
		t.Fatalf("release of an auditing workload must report ErrNotQuarantined, got %v", err)
	}
}

func TestQuarantineRequiresReasonAndIsTerminal(t *testing.T) {
	g := impl.NewSeccompEscapeGuard(t2Limits())
	t2Enforce(t, g, "c1", []impl.Rule{{Name: "read"}}, impl.Call{Name: "read"})
	if _, err := g.Check("c1", impl.Call{Name: "write"}); err != nil {
		t.Fatalf("first blocked call must succeed, got %v", err)
	}
	if err := g.Quarantine("c1", "   "); !errors.Is(err, impl.ErrReasonRequired) {
		t.Fatalf("blank reason must report ErrReasonRequired, got %v", err)
	}
	if err := g.Quarantine("c1", "policy"); err != nil {
		t.Fatalf("quarantine must succeed, got %v", err)
	}
	if err := g.Quarantine("c1", "again"); !errors.Is(err, impl.ErrAlreadyQuarantined) {
		t.Fatalf("second quarantine must report ErrAlreadyQuarantined, got %v", err)
	}
	d, err := g.Check("c1", impl.Call{Name: "read"})
	if err != nil {
		t.Fatalf("check must succeed, got %v", err)
	}
	if d.Action != impl.ActionDeny || d.Reason != impl.ReasonQuarantined || !d.Quarantined {
		t.Fatalf("quarantined check = %+v, want deny with quarantined reason", d)
	}
	if d.Strikes != 1 {
		t.Fatalf("quarantined check must not advance strikes, got %d want 1", d.Strikes)
	}
}

func TestSnapshotRestoreKeepsWorkloadState(t *testing.T) {
	g := impl.NewSeccompEscapeGuard(t2Limits())
	t2Enforce(t, g, "c1", t2Rules(), impl.Call{Name: "read"})
	if _, err := g.Check("c1", impl.Call{Name: "write"}); err != nil {
		t.Fatalf("first blocked call must succeed, got %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot must succeed, got %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	if _, err := g.Check("c1", impl.Call{Name: "write"}); err != nil {
		t.Fatalf("second blocked call must succeed, got %v", err)
	}
	if err := g.Restore(data); err != nil {
		t.Fatalf("restore must succeed, got %v", err)
	}
	strikes, err := g.Strikes("c1")
	if err != nil {
		t.Fatalf("strikes must succeed, got %v", err)
	}
	if strikes != 1 {
		t.Fatalf("strikes after restore = %d, want 1", strikes)
	}
	state, err := g.State("c1")
	if err != nil {
		t.Fatalf("state must succeed, got %v", err)
	}
	if state != impl.StateEnforced {
		t.Fatalf("state after restore = %q, want enforced", state)
	}
	obs, err := g.Observations("c1")
	if err != nil {
		t.Fatalf("observations must succeed, got %v", err)
	}
	if len(obs) != 1 {
		t.Fatalf("restore must bring back the audit window, got %+v", obs)
	}
	if err := g.Restore([]byte("broken payload")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must report ErrBadSnapshot, got %v", err)
	}
	strikes, err = g.Strikes("c1")
	if err != nil {
		t.Fatalf("strikes must succeed, got %v", err)
	}
	if strikes != 1 {
		t.Fatalf("failed restore must leave live state untouched, strikes = %d", strikes)
	}
}
