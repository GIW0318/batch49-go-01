package task

import (
	"errors"
	"sync"
	"testing"
	"time"

	impl "gsb/batch49-go-safety-a-04"
)

var reconcileClock = time.Date(2026, 4, 6, 8, 0, 0, 0, time.UTC)

func reconcileLimits() impl.Limits {
	return impl.Limits{MaxAccountsPerRun: 2, GracePeriod: 24 * time.Hour}
}

func downstreamAccount(id, userName string, sessions int) impl.Account {
	return impl.Account{ID: id, UserName: userName, Sessions: sessions}
}

// reconcileFixture seeds a roster of alice and bob and opens one run.
func reconcileFixture(t *testing.T, accounts []impl.Account) (*impl.ScimDeprovisionReconciler, impl.RunReport) {
	t.Helper()
	r := impl.NewScimDeprovisionReconciler(reconcileLimits())
	r.SetNow(reconcileClock)
	if err := r.SetUpstream("tenant-1", []string{"alice", "bob"}); err != nil {
		t.Fatalf("upstream roster must be accepted, got %v", err)
	}
	if err := r.SetDownstream("tenant-1", "app-crm", accounts); err != nil {
		t.Fatalf("downstream roster must be accepted, got %v", err)
	}
	report, err := r.Run("tenant-1", "app-crm")
	if err != nil {
		t.Fatalf("run must open, got %v", err)
	}
	return r, report
}

func accountByID(t *testing.T, r *impl.ScimDeprovisionReconciler, tenantID, appID, accountID string) impl.Account {
	t.Helper()
	accounts, err := r.Accounts(tenantID, appID)
	if err != nil {
		t.Fatalf("accounts must be readable, got %v", err)
	}
	for _, a := range accounts {
		if a.ID == accountID {
			return a
		}
	}
	t.Fatalf("account %q not found in %+v", accountID, accounts)
	return impl.Account{}
}

func TestRunRejectsBlankIdentity(t *testing.T) {
	r := impl.NewScimDeprovisionReconciler(reconcileLimits())
	r.SetNow(reconcileClock)
	if err := r.SetUpstream("", []string{"alice"}); !errors.Is(err, impl.ErrBlankTenant) {
		t.Fatalf("roster without a tenant = %v, want ErrBlankTenant", err)
	}
	if err := r.SetDownstream("tenant-1", "  ", nil); !errors.Is(err, impl.ErrBlankApp) {
		t.Fatalf("roster without an application = %v, want ErrBlankApp", err)
	}
	if _, err := r.Run("", "app-crm"); !errors.Is(err, impl.ErrBlankTenant) {
		t.Fatalf("run without a tenant = %v, want ErrBlankTenant", err)
	}
	if _, err := r.Run("tenant-1", ""); !errors.Is(err, impl.ErrBlankApp) {
		t.Fatalf("run without an application = %v, want ErrBlankApp", err)
	}
}

func TestRunPlacesOrphansInGrace(t *testing.T) {
	r, report := reconcileFixture(t, []impl.Account{
		downstreamAccount("acct-1", "alice", 1),
		downstreamAccount("acct-3", "carol", 2),
	})
	if report.Upstream != 2 || report.Downstream != 2 || report.Orphans != 1 {
		t.Fatalf("report = %+v, want 2 upstream, 2 downstream, 1 orphan", report)
	}
	if report.RunID == "" {
		t.Fatalf("run must carry an id")
	}
	if report.Completed {
		t.Fatalf("a freshly opened run must not be completed")
	}
	orphans, err := r.Orphans("tenant-1", "app-crm")
	if err != nil {
		t.Fatalf("orphans must be readable, got %v", err)
	}
	if len(orphans) != 1 || orphans[0].ID != "acct-3" {
		t.Fatalf("orphans = %+v, want exactly acct-3", orphans)
	}
	if orphans[0].State != impl.AccountGrace {
		t.Fatalf("orphan state = %q, want %q", orphans[0].State, impl.AccountGrace)
	}
	wantUntil := reconcileClock.Add(24 * time.Hour)
	if !orphans[0].GraceUntil.Equal(wantUntil) {
		t.Fatalf("grace deadline = %v, want %v", orphans[0].GraceUntil, wantUntil)
	}
	matched := accountByID(t, r, "tenant-1", "app-crm", "acct-1")
	if matched.State != impl.AccountActive || matched.Sessions != 1 {
		t.Fatalf("matched account = %+v, want it untouched and active", matched)
	}
}

func TestOrphansSortedPerApplication(t *testing.T) {
	r := impl.NewScimDeprovisionReconciler(reconcileLimits())
	r.SetNow(reconcileClock)
	if err := r.SetUpstream("tenant-1", []string{"alice"}); err != nil {
		t.Fatalf("upstream roster must be accepted, got %v", err)
	}
	if err := r.SetDownstream("tenant-1", "app-a", []impl.Account{
		downstreamAccount("acct-a2", "bob", 1),
		downstreamAccount("acct-a1", "carol", 1),
	}); err != nil {
		t.Fatalf("app-a roster must be accepted, got %v", err)
	}
	if err := r.SetDownstream("tenant-1", "app-b", []impl.Account{
		downstreamAccount("acct-b1", "dave", 1),
	}); err != nil {
		t.Fatalf("app-b roster must be accepted, got %v", err)
	}
	if _, err := r.Run("tenant-1", "app-a"); err != nil {
		t.Fatalf("app-a run must open, got %v", err)
	}
	if _, err := r.Run("tenant-1", "app-b"); err != nil {
		t.Fatalf("app-b run must open, got %v", err)
	}
	orphansA, err := r.Orphans("tenant-1", "app-a")
	if err != nil {
		t.Fatalf("app-a orphans must be readable, got %v", err)
	}
	if len(orphansA) != 2 || orphansA[0].ID != "acct-a1" || orphansA[1].ID != "acct-a2" {
		t.Fatalf("app-a orphans = %+v, want acct-a1 then acct-a2", orphansA)
	}
	orphansB, err := r.Orphans("tenant-1", "app-b")
	if err != nil {
		t.Fatalf("app-b orphans must be readable, got %v", err)
	}
	if len(orphansB) != 1 || orphansB[0].ID != "acct-b1" {
		t.Fatalf("app-b orphans = %+v, want exactly acct-b1", orphansB)
	}
}

func TestSecondRunInFlightRejected(t *testing.T) {
	r, _ := reconcileFixture(t, []impl.Account{
		downstreamAccount("acct-1", "alice", 1),
	})
	if _, err := r.Run("tenant-1", "app-crm"); !errors.Is(err, impl.ErrRunInProgress) {
		t.Fatalf("second run on a busy app = %v, want ErrRunInProgress", err)
	}
	r.SetNow(reconcileClock)
	if err := r.SetDownstream("tenant-1", "app-hr", []impl.Account{
		downstreamAccount("acct-h1", "eve", 1),
	}); err != nil {
		t.Fatalf("second app roster must be accepted, got %v", err)
	}
	if _, err := r.Run("tenant-1", "app-hr"); err != nil {
		t.Fatalf("a run on another app must open, got %v", err)
	}
}

func TestConfirmValidatesRunAndAccount(t *testing.T) {
	r, report := reconcileFixture(t, []impl.Account{
		downstreamAccount("acct-1", "alice", 1),
		downstreamAccount("acct-3", "carol", 2),
	})
	if err := r.Confirm("run-missing", "acct-3"); !errors.Is(err, impl.ErrUnknownRun) {
		t.Fatalf("confirm on an unknown run = %v, want ErrUnknownRun", err)
	}
	if err := r.Confirm(report.RunID, "acct-1"); !errors.Is(err, impl.ErrUnknownAccount) {
		t.Fatalf("confirm of a matched account = %v, want ErrUnknownAccount", err)
	}
	if err := r.Confirm(report.RunID, "acct-missing"); !errors.Is(err, impl.ErrUnknownAccount) {
		t.Fatalf("confirm of an unknown account = %v, want ErrUnknownAccount", err)
	}
}

func TestConfirmBeforeGraceRejected(t *testing.T) {
	r, report := reconcileFixture(t, []impl.Account{
		downstreamAccount("acct-3", "carol", 2),
	})
	if err := r.Confirm(report.RunID, "acct-3"); !errors.Is(err, impl.ErrGraceNotElapsed) {
		t.Fatalf("confirm inside the grace window = %v, want ErrGraceNotElapsed", err)
	}
	if orphans, err := r.Orphans("tenant-1", "app-crm"); err != nil || len(orphans) != 1 {
		t.Fatalf("orphan must still wait, got %+v err=%v", orphans, err)
	}
	r.SetNow(reconcileClock.Add(25 * time.Hour))
	if err := r.Confirm(report.RunID, "acct-3"); err != nil {
		t.Fatalf("confirm after the grace window must succeed, got %v", err)
	}
}

func TestExecuteRequiresConfirmation(t *testing.T) {
	r, report := reconcileFixture(t, []impl.Account{
		downstreamAccount("acct-3", "carol", 2),
	})
	r.SetNow(reconcileClock.Add(25 * time.Hour))
	if _, err := r.Execute(report.RunID); !errors.Is(err, impl.ErrNotConfirmed) {
		t.Fatalf("execute without a confirmation = %v, want ErrNotConfirmed", err)
	}
	if _, err := r.Execute("run-missing"); !errors.Is(err, impl.ErrUnknownRun) {
		t.Fatalf("execute on an unknown run = %v, want ErrUnknownRun", err)
	}
}

func TestExecuteRevokesThenDisables(t *testing.T) {
	r, report := reconcileFixture(t, []impl.Account{
		downstreamAccount("acct-1", "alice", 1),
		downstreamAccount("acct-3", "carol", 2),
	})
	r.SetNow(reconcileClock.Add(25 * time.Hour))
	if err := r.Confirm(report.RunID, "acct-3"); err != nil {
		t.Fatalf("confirm must succeed, got %v", err)
	}
	res, err := r.Execute(report.RunID)
	if err != nil {
		t.Fatalf("execute must succeed, got %v", err)
	}
	if res.Disabled != 1 || res.Revoked != 1 || res.Failed != 0 {
		t.Fatalf("result = %+v, want 1 disabled and 1 revoked", res)
	}
	if !res.Completed || res.Cursor != 1 {
		t.Fatalf("result = %+v, want completed at cursor 1", res)
	}
	ops := r.Ops(report.RunID)
	want := []string{"revoke_sessions:acct-3", "disable:acct-3"}
	if len(ops) != len(want) {
		t.Fatalf("ops = %v, want %v", ops, want)
	}
	for i := range want {
		if ops[i] != want[i] {
			t.Fatalf("ops[%d] = %q, want %q (full log %v)", i, ops[i], want[i], ops)
		}
	}
	orphan := accountByID(t, r, "tenant-1", "app-crm", "acct-3")
	if orphan.State != impl.AccountDisabled || orphan.Sessions != 0 {
		t.Fatalf("disabled account = %+v, want disabled with no sessions", orphan)
	}
	matched := accountByID(t, r, "tenant-1", "app-crm", "acct-1")
	if matched.State != impl.AccountActive || matched.Sessions != 1 {
		t.Fatalf("matched account = %+v, want it untouched", matched)
	}
}

func TestExecuteIsIdempotentOnCompletedRun(t *testing.T) {
	r, report := reconcileFixture(t, []impl.Account{
		downstreamAccount("acct-3", "carol", 2),
	})
	r.SetNow(reconcileClock.Add(25 * time.Hour))
	if err := r.Confirm(report.RunID, "acct-3"); err != nil {
		t.Fatalf("confirm must succeed, got %v", err)
	}
	first, err := r.Execute(report.RunID)
	if err != nil {
		t.Fatalf("first execute must succeed, got %v", err)
	}
	firstOps := r.Ops(report.RunID)
	second, err := r.Execute(report.RunID)
	if err != nil {
		t.Fatalf("second execute must succeed, got %v", err)
	}
	if second.Disabled != first.Disabled || second.Completed != first.Completed {
		t.Fatalf("second result = %+v, want the same as %+v", second, first)
	}
	if len(r.Ops(report.RunID)) != len(firstOps) {
		t.Fatalf("second execute appended operations: %v then %v", firstOps, r.Ops(report.RunID))
	}
}

func TestExecuteRespectsPerCallCap(t *testing.T) {
	r, report := reconcileFixture(t, []impl.Account{
		downstreamAccount("acct-1", "alice", 1),
		downstreamAccount("acct-2", "bob", 1),
		downstreamAccount("acct-3", "carol", 1),
	})
	r.SetNow(reconcileClock.Add(25 * time.Hour))
	for _, id := range []string{"acct-2", "acct-3"} {
		if err := r.Confirm(report.RunID, id); err != nil {
			t.Fatalf("confirm %q must succeed, got %v", id, err)
		}
	}
	first, err := r.Execute(report.RunID)
	if err != nil {
		t.Fatalf("first execute must succeed, got %v", err)
	}
	if first.Disabled != 2 || first.Completed || first.Cursor != 2 {
		t.Fatalf("first call = %+v, want 2 disabled, cursor 2, unfinished", first)
	}
	second, err := r.Execute(report.RunID)
	if err != nil {
		t.Fatalf("second execute must succeed, got %v", err)
	}
	if second.Disabled != 1 || !second.Completed || second.Cursor != 3 {
		t.Fatalf("second call = %+v, want 1 disabled, cursor 3, completed", second)
	}
	if got := len(r.Ops(report.RunID)); got != 4 {
		t.Fatalf("operation log length = %d, want 4 (two accounts times revoke and disable)", got)
	}
}

func TestSnapshotRestoreResumesRun(t *testing.T) {
	r, report := reconcileFixture(t, []impl.Account{
		downstreamAccount("acct-1", "alice", 1),
		downstreamAccount("acct-2", "bob", 1),
		downstreamAccount("acct-3", "carol", 1),
	})
	r.SetNow(reconcileClock.Add(25 * time.Hour))
	for _, id := range []string{"acct-2", "acct-3"} {
		if err := r.Confirm(report.RunID, id); err != nil {
			t.Fatalf("confirm %q must succeed, got %v", id, err)
		}
	}
	if _, err := r.Execute(report.RunID); err != nil {
		t.Fatalf("first execute must succeed, got %v", err)
	}
	data, err := r.Snapshot()
	if err != nil {
		t.Fatalf("snapshot must succeed, got %v", err)
	}
	restored := impl.NewScimDeprovisionReconciler(reconcileLimits())
	restored.SetNow(reconcileClock.Add(30 * time.Hour))
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore must succeed, got %v", err)
	}
	res, err := restored.Execute(report.RunID)
	if err != nil {
		t.Fatalf("execute after restore must succeed, got %v", err)
	}
	if res.Disabled != 1 || !res.Completed {
		t.Fatalf("resumed result = %+v, want the last orphan disabled", res)
	}
	if got := len(restored.Ops(report.RunID)); got != 4 {
		t.Fatalf("restored operation log length = %d, want 4", got)
	}
	orphan := accountByID(t, restored, "tenant-1", "app-crm", "acct-3")
	if orphan.State != impl.AccountDisabled {
		t.Fatalf("restored orphan state = %q, want disabled", orphan.State)
	}
	if err := restored.Restore([]byte("null garbage")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt restore = %v, want ErrBadSnapshot", err)
	}
	if got := len(restored.Ops(report.RunID)); got != 4 {
		t.Fatalf("rejected restore must keep the log, length = %d", got)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	r, report := reconcileFixture(t, []impl.Account{
		downstreamAccount("acct-2", "bob", 1),
	})
	if err := r.Restore([]byte("{\"runs\": \"not a map\"}")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt restore = %v, want ErrBadSnapshot", err)
	}
	if orphans, err := r.Orphans("tenant-1", "app-crm"); err != nil || len(orphans) != 1 {
		t.Fatalf("live orphans must survive a rejected restore, got %+v err=%v", orphans, err)
	}
	r.SetNow(reconcileClock.Add(25 * time.Hour))
	if err := r.Confirm(report.RunID, "acct-2"); err != nil {
		t.Fatalf("live run must stay usable, got %v", err)
	}
	res, err := r.Execute(report.RunID)
	if err != nil {
		t.Fatalf("execute after a rejected restore must succeed, got %v", err)
	}
	if res.Disabled != 1 || !res.Completed {
		t.Fatalf("result = %+v, want one account disabled", res)
	}
}

func TestPlanOrdersRevokeBeforeDisable(t *testing.T) {
	r, _ := reconcileFixture(t, []impl.Account{
		downstreamAccount("acct-1", "alice", 1),
		downstreamAccount("acct-2", "bob", 3),
		downstreamAccount("acct-3", "carol", 2),
	})
	actions, err := r.Plan("tenant-1", "app-crm")
	if err != nil {
		t.Fatalf("plan must succeed, got %v", err)
	}
	wantIDs := []string{"acct-2", "acct-2", "acct-3", "acct-3"}
	wantKinds := []string{impl.ActionRevokeSessions, impl.ActionDisable, impl.ActionRevokeSessions, impl.ActionDisable}
	if len(actions) != len(wantIDs) {
		t.Fatalf("planned %d actions, want %d: %+v", len(actions), len(wantIDs), actions)
	}
	for i := range wantIDs {
		if actions[i].AccountID != wantIDs[i] || actions[i].Kind != wantKinds[i] {
			t.Fatalf("action %d = %+v, want %s on %s", i, actions[i], wantKinds[i], wantIDs[i])
		}
	}
}

func TestMatchedAccountsStayActive(t *testing.T) {
	r, report := reconcileFixture(t, []impl.Account{
		downstreamAccount("acct-1", "alice", 3),
	})
	orphans, err := r.Orphans("tenant-1", "app-crm")
	if err != nil {
		t.Fatalf("orphans must be readable, got %v", err)
	}
	if len(orphans) != 0 {
		t.Fatalf("orphans = %+v, want none", orphans)
	}
	r.SetNow(reconcileClock.Add(48 * time.Hour))
	res, err := r.Execute(report.RunID)
	if err != nil {
		t.Fatalf("execute must succeed, got %v", err)
	}
	if res.Disabled != 0 || !res.Completed {
		t.Fatalf("result = %+v, want nothing disabled and completed", res)
	}
	if ops := r.Ops(report.RunID); len(ops) != 0 {
		t.Fatalf("operation log = %v, want empty", ops)
	}
	matched := accountByID(t, r, "tenant-1", "app-crm", "acct-1")
	if matched.State != impl.AccountActive || matched.Sessions != 3 {
		t.Fatalf("matched account = %+v, want active with 3 sessions", matched)
	}
}

func TestConcurrentRunSingleWinner(t *testing.T) {
	r := impl.NewScimDeprovisionReconciler(reconcileLimits())
	r.SetNow(reconcileClock)
	if err := r.SetUpstream("tenant-1", []string{"alice"}); err != nil {
		t.Fatalf("upstream roster must be accepted, got %v", err)
	}
	if err := r.SetDownstream("tenant-1", "app-crm", []impl.Account{
		downstreamAccount("acct-2", "bob", 1),
	}); err != nil {
		t.Fatalf("downstream roster must be accepted, got %v", err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := 0
	rejected := 0
	other := 0
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			_, err := r.Run("tenant-1", "app-crm")
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				winners++
			case errors.Is(err, impl.ErrRunInProgress):
				rejected++
			default:
				other++
			}
		}()
	}
	wg.Wait()
	if winners != 1 || rejected != 1 || other != 0 {
		t.Fatalf("concurrent run winners=%d rejected=%d other=%d, want 1/1/0", winners, rejected, other)
	}
}
