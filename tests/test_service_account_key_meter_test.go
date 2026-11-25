package task

import (
	"errors"
	"sync"
	"testing"
	"time"

	impl "gsb/batch49-go-safety-a-11"
)

func testLimits() impl.Limits {
	return impl.Limits{
		KeyQuota:         5,
		AccountQuota:     50,
		Window:           10 * time.Minute,
		MaxTargetsPerKey: 4,
		MaxEvents:        32,
	}
}

func baseClock() time.Time {
	return time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)
}

func mustRegister(t *testing.T, m *impl.ServiceAccountKeyMeter, keyID, accountID string, targets ...string) {
	t.Helper()
	if err := m.RegisterKey(keyID, accountID, targets); err != nil {
		t.Fatalf("register %s: %v", keyID, err)
	}
}

func TestRegisterKeyCreatesActiveKey(t *testing.T) {
	m := impl.NewServiceAccountKeyMeter(testLimits())
	if err := m.RegisterKey("key-1", "acct-1", []string{"orders", "billing", "orders"}); err != nil {
		t.Fatalf("register key: %v", err)
	}
	st, err := m.KeyState("key-1")
	if err != nil {
		t.Fatalf("key state: %v", err)
	}
	if st.State != impl.KeyActive {
		t.Fatalf("state = %q, want %q", st.State, impl.KeyActive)
	}
	if st.AccountID != "acct-1" {
		t.Fatalf("account = %q, want acct-1", st.AccountID)
	}
	want := []string{"billing", "orders"}
	if len(st.Targets) != len(want) {
		t.Fatalf("targets = %v, want deduplicated %v", st.Targets, want)
	}
	for i := range want {
		if st.Targets[i] != want[i] {
			t.Fatalf("targets = %v, want sorted %v", st.Targets, want)
		}
	}
	if st.Seq != 0 {
		t.Fatalf("fresh key seq = %d, want 0 before any call", st.Seq)
	}
}

func TestRegisterKeyRejectsBadInput(t *testing.T) {
	limits := testLimits()
	limits.MaxTargetsPerKey = 2
	m := impl.NewServiceAccountKeyMeter(limits)
	if err := m.RegisterKey("  ", "acct-1", []string{"orders"}); !errors.Is(err, impl.ErrEmptyKeyID) {
		t.Fatalf("blank key id must fail with ErrEmptyKeyID, got %v", err)
	}
	if err := m.RegisterKey("key-1", "", []string{"orders"}); !errors.Is(err, impl.ErrEmptyAccountID) {
		t.Fatalf("blank account must fail with ErrEmptyAccountID, got %v", err)
	}
	if err := m.RegisterKey("key-1", "acct-1", nil); !errors.Is(err, impl.ErrBadTargets) {
		t.Fatalf("empty target list must fail with ErrBadTargets, got %v", err)
	}
	if err := m.RegisterKey("key-1", "acct-1", []string{"a", "b", "c"}); !errors.Is(err, impl.ErrTargetLimit) {
		t.Fatalf("three targets over a cap of two must fail with ErrTargetLimit, got %v", err)
	}
	if err := m.RegisterKey("key-1", "acct-1", []string{"orders"}); err != nil {
		t.Fatalf("valid registration must succeed, got %v", err)
	}
	if err := m.RegisterKey("key-1", "acct-2", []string{"orders"}); !errors.Is(err, impl.ErrKeyExists) {
		t.Fatalf("duplicate key must fail with ErrKeyExists, got %v", err)
	}
}

func TestRecordAllowedTargetAccumulatesUsage(t *testing.T) {
	m := impl.NewServiceAccountKeyMeter(testLimits())
	mustRegister(t, m, "key-1", "acct-1", "orders", "billing")
	t0 := baseClock()
	for i := 0; i < 3; i++ {
		if err := m.Record("key-1", "orders", t0.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
	u, err := m.Usage("key-1", 10*time.Minute)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if u.Count != 3 || u.Allowed != 3 || u.Denied != 0 {
		t.Fatalf("usage = %+v, want count/allowed 3 and denied 0", u)
	}
	if !u.To.Equal(t0.Add(2 * time.Minute)) {
		t.Fatalf("usage to = %s, want the clock watermark %s", u.To, t0.Add(2*time.Minute))
	}
	if !u.From.Equal(t0.Add(2*time.Minute - 10*time.Minute)) {
		t.Fatalf("usage from = %s, want watermark minus window", u.From)
	}
	st, err := m.KeyState("key-1")
	if err != nil {
		t.Fatalf("key state: %v", err)
	}
	if st.Allowed != 3 || st.Denied != 0 || st.Seq != 3 {
		t.Fatalf("key stat = %+v, want allowed 3, denied 0, seq 3", st)
	}
	events := m.Events("acct-1")
	if len(events) != 3 {
		t.Fatalf("event count = %d, want 3", len(events))
	}
	last := events[2]
	if last.Decision != impl.DecisionAllow || last.Reason != impl.ReasonCommitted || last.Target != "orders" {
		t.Fatalf("last event = %+v, want an allow commit on orders", last)
	}
	if last.KeyID != "key-1" || last.AccountID != "acct-1" || last.Seq != 3 {
		t.Fatalf("last event identity = %+v, want key-1/acct-1 and seq 3", last)
	}
}

func TestRecordUnauthorizedTargetSuspendsKey(t *testing.T) {
	m := impl.NewServiceAccountKeyMeter(testLimits())
	mustRegister(t, m, "key-1", "acct-1", "orders")
	now := baseClock()
	if err := m.Record("key-1", "billing", now); !errors.Is(err, impl.ErrTargetDenied) {
		t.Fatalf("an unlisted target must fail with ErrTargetDenied, got %v", err)
	}
	st, err := m.KeyState("key-1")
	if err != nil {
		t.Fatalf("key state: %v", err)
	}
	if st.State != impl.KeySuspended || st.Reason != impl.ReasonUnauthorizedTarget {
		t.Fatalf("key stat = %+v, want suspended for %q", st, impl.ReasonUnauthorizedTarget)
	}
	events := m.Events("acct-1")
	if len(events) != 1 {
		t.Fatalf("event count = %d, want exactly 1", len(events))
	}
	ev := events[0]
	if ev.Decision != impl.DecisionDeny || ev.Reason != impl.ReasonUnauthorizedTarget || ev.Target != "billing" {
		t.Fatalf("event = %+v, want a deny for the unlisted target", ev)
	}
	if err := m.Record("key-1", "orders", now.Add(time.Second)); !errors.Is(err, impl.ErrKeySuspended) {
		t.Fatalf("a suspended key must reject calls with ErrKeySuspended, got %v", err)
	}
	st2, err := m.KeyState("key-1")
	if err != nil {
		t.Fatalf("key state after denied retry: %v", err)
	}
	if st2.State != impl.KeySuspended {
		t.Fatalf("state = %q, want the key to stay suspended", st2.State)
	}
}

func TestSuspendedKeyRejectsCalls(t *testing.T) {
	m := impl.NewServiceAccountKeyMeter(testLimits())
	mustRegister(t, m, "key-1", "acct-1", "orders")
	if err := m.Suspend("key-1", "rotate credentials"); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	st, err := m.KeyState("key-1")
	if err != nil {
		t.Fatalf("key state: %v", err)
	}
	if st.State != impl.KeySuspended || st.Reason != "rotate credentials" {
		t.Fatalf("key stat = %+v, want suspended with the operator note", st)
	}
	if err := m.Record("key-1", "orders", baseClock()); !errors.Is(err, impl.ErrKeySuspended) {
		t.Fatalf("call on a suspended key must fail with ErrKeySuspended, got %v", err)
	}
	events := m.Events("acct-1")
	if len(events) != 2 {
		t.Fatalf("event count = %d, want the suspend entry plus the rejected call", len(events))
	}
	if events[0].Reason != impl.ReasonOperatorHold || events[0].Decision != impl.DecisionDeny {
		t.Fatalf("first event = %+v, want a deny with reason %q", events[0], impl.ReasonOperatorHold)
	}
	if events[1].Reason != impl.ReasonKeySuspended || events[1].Decision != impl.DecisionDeny {
		t.Fatalf("second event = %+v, want a deny with reason %q", events[1], impl.ReasonKeySuspended)
	}
}

func TestSuspendRequiresReason(t *testing.T) {
	m := impl.NewServiceAccountKeyMeter(testLimits())
	mustRegister(t, m, "key-1", "acct-1", "orders")
	if err := m.Suspend("key-1", "   "); !errors.Is(err, impl.ErrBadReason) {
		t.Fatalf("blank reason must fail with ErrBadReason, got %v", err)
	}
	st, err := m.KeyState("key-1")
	if err != nil {
		t.Fatalf("key state: %v", err)
	}
	if st.State != impl.KeyActive {
		t.Fatalf("state = %q, want the key to stay active after a rejected suspend", st.State)
	}
	if err := m.Suspend("key-404", "operator hold"); !errors.Is(err, impl.ErrUnknownKey) {
		t.Fatalf("suspending an unknown key must fail with ErrUnknownKey, got %v", err)
	}
}

func TestResumeRequiresApprover(t *testing.T) {
	m := impl.NewServiceAccountKeyMeter(testLimits())
	mustRegister(t, m, "key-1", "acct-1", "orders")
	if err := m.Suspend("key-1", "operator hold"); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if err := m.Resume("key-1", "  "); !errors.Is(err, impl.ErrApproverRequired) {
		t.Fatalf("resume without an approver must fail with ErrApproverRequired, got %v", err)
	}
	st, err := m.KeyState("key-1")
	if err != nil {
		t.Fatalf("key state: %v", err)
	}
	if st.State != impl.KeySuspended {
		t.Fatalf("state = %q, want the key to stay suspended", st.State)
	}
	if err := m.Resume("key-1", "sec-approver-1"); err != nil {
		t.Fatalf("resume with an approver must succeed, got %v", err)
	}
	st2, err := m.KeyState("key-1")
	if err != nil {
		t.Fatalf("key state after resume: %v", err)
	}
	if st2.State != impl.KeyActive || st2.Reason != "" {
		t.Fatalf("key stat = %+v, want active with a cleared reason", st2)
	}
	if err := m.Record("key-1", "orders", baseClock()); err != nil {
		t.Fatalf("a resumed key must accept calls again, got %v", err)
	}
	if err := m.Resume("key-1", "sec-approver-2"); !errors.Is(err, impl.ErrKeyNotSuspended) {
		t.Fatalf("resuming an active key must fail with ErrKeyNotSuspended, got %v", err)
	}
	if err := m.Resume("key-404", "sec-approver-1"); !errors.Is(err, impl.ErrUnknownKey) {
		t.Fatalf("resuming an unknown key must fail with ErrUnknownKey, got %v", err)
	}
}

func TestRevokeIsTerminal(t *testing.T) {
	m := impl.NewServiceAccountKeyMeter(testLimits())
	mustRegister(t, m, "key-1", "acct-1", "orders")
	if err := m.Revoke("key-1", "  "); !errors.Is(err, impl.ErrBadReason) {
		t.Fatalf("blank revoke reason must fail with ErrBadReason, got %v", err)
	}
	if err := m.Revoke("key-1", "leaked in build log"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	st, err := m.KeyState("key-1")
	if err != nil {
		t.Fatalf("key state: %v", err)
	}
	if st.State != impl.KeyRevoked || st.Reason != "leaked in build log" {
		t.Fatalf("key stat = %+v, want revoked with the operator note", st)
	}
	if err := m.Record("key-1", "orders", baseClock()); !errors.Is(err, impl.ErrKeyRevoked) {
		t.Fatalf("calls on a revoked key must fail with ErrKeyRevoked, got %v", err)
	}
	if err := m.Suspend("key-1", "operator hold"); !errors.Is(err, impl.ErrKeyRevoked) {
		t.Fatalf("suspending a revoked key must fail with ErrKeyRevoked, got %v", err)
	}
	if err := m.Resume("key-1", "sec-approver-1"); !errors.Is(err, impl.ErrKeyRevoked) {
		t.Fatalf("resuming a revoked key must fail with ErrKeyRevoked, got %v", err)
	}
	if err := m.Revoke("key-1", "repeated"); err != nil {
		t.Fatalf("repeated revoke must be idempotent, got %v", err)
	}
}

func TestRecordStopsAtKeyQuota(t *testing.T) {
	limits := testLimits()
	limits.KeyQuota = 2
	limits.AccountQuota = 100
	m := impl.NewServiceAccountKeyMeter(limits)
	mustRegister(t, m, "key-1", "acct-1", "orders")
	t0 := baseClock()
	for i := 0; i < 2; i++ {
		if err := m.Record("key-1", "orders", t0.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("record %d within quota: %v", i, err)
		}
	}
	err := m.Record("key-1", "orders", t0.Add(2*time.Second))
	if !errors.Is(err, impl.ErrKeyQuotaExceeded) {
		t.Fatalf("the third call over a key quota of two must fail with ErrKeyQuotaExceeded, got %v", err)
	}
	u, err := m.Usage("key-1", time.Minute)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if u.Allowed != 2 || u.Denied != 1 || u.Count != 2 {
		t.Fatalf("usage = %+v, want allowed 2, denied 1, count 2", u)
	}
	st, err := m.KeyState("key-1")
	if err != nil {
		t.Fatalf("key state: %v", err)
	}
	if st.Denied != 1 || st.Seq != 3 {
		t.Fatalf("key stat = %+v, want denied 1 and seq 3", st)
	}
	events := m.Events("acct-1")
	if len(events) != 3 {
		t.Fatalf("event count = %d, want 3", len(events))
	}
	if events[2].Decision != impl.DecisionDeny || events[2].Reason != impl.ReasonKeyQuota {
		t.Fatalf("last event = %+v, want a deny with reason %q", events[2], impl.ReasonKeyQuota)
	}
}

func TestQuotaReportsAccountUsage(t *testing.T) {
	limits := testLimits()
	limits.KeyQuota = 10
	limits.AccountQuota = 3
	m := impl.NewServiceAccountKeyMeter(limits)
	mustRegister(t, m, "key-1", "acct-1", "orders")
	mustRegister(t, m, "key-2", "acct-1", "orders")
	t0 := baseClock()
	records := []struct {
		key string
		at  time.Time
	}{
		{"key-1", t0},
		{"key-1", t0.Add(time.Second)},
		{"key-2", t0.Add(2 * time.Second)},
	}
	for i, r := range records {
		if err := m.Record(r.key, "orders", r.at); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
	q, err := m.Quota("acct-1")
	if err != nil {
		t.Fatalf("quota: %v", err)
	}
	if q.Used != 3 || q.Limit != 3 || !q.Exhausted {
		t.Fatalf("quota = %+v, want used 3, limit 3, exhausted", q)
	}
	if q.Window != limits.Window {
		t.Fatalf("quota window = %s, want %s", q.Window, limits.Window)
	}
	if _, err := m.Quota("acct-404"); !errors.Is(err, impl.ErrUnknownAccount) {
		t.Fatalf("quota of an unknown account must fail with ErrUnknownAccount, got %v", err)
	}
}

func TestUsageWindowSlides(t *testing.T) {
	m := impl.NewServiceAccountKeyMeter(testLimits())
	mustRegister(t, m, "key-1", "acct-1", "orders")
	t0 := baseClock()
	for i := 0; i < 3; i++ {
		if err := m.Record("key-1", "orders", t0.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
	u, err := m.Usage("key-1", 2*time.Minute)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if u.Count != 2 {
		t.Fatalf("count inside a two minute window = %d, want 2 (left open, right closed)", u.Count)
	}
	if !u.From.Equal(t0) || !u.To.Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("window = [%s, %s], want (%s, %s]", u.From, u.To, t0, t0.Add(2*time.Minute))
	}
	wide, err := m.Usage("key-1", 3*time.Minute)
	if err != nil {
		t.Fatalf("wide usage: %v", err)
	}
	if wide.Count != 3 {
		t.Fatalf("count inside a three minute window = %d, want 3", wide.Count)
	}
	if _, err := m.Usage("key-1", 0); !errors.Is(err, impl.ErrBadWindow) {
		t.Fatalf("a zero window must fail with ErrBadWindow, got %v", err)
	}
	if _, err := m.Usage("key-404", time.Minute); !errors.Is(err, impl.ErrUnknownKey) {
		t.Fatalf("usage of an unknown key must fail with ErrUnknownKey, got %v", err)
	}
}

func TestConcurrentRecordHonoursQuota(t *testing.T) {
	limits := testLimits()
	limits.KeyQuota = 20
	limits.AccountQuota = 500
	limits.MaxEvents = 64
	m := impl.NewServiceAccountKeyMeter(limits)
	mustRegister(t, m, "key-1", "acct-1", "orders")
	at := baseClock()
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed, limited, other := 0, 0, 0
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := m.Record("key-1", "orders", at)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				allowed++
			case errors.Is(err, impl.ErrKeyQuotaExceeded):
				limited++
			default:
				other++
			}
		}()
	}
	wg.Wait()
	if allowed != 20 || limited != 20 || other != 0 {
		t.Fatalf("allowed=%d limited=%d other=%d, want 20/20/0", allowed, limited, other)
	}
	u, err := m.Usage("key-1", time.Minute)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if u.Allowed != 20 || u.Denied != 20 {
		t.Fatalf("usage = %+v, want allowed 20 and denied 20", u)
	}
}

func TestAccountEventsRetainNewest(t *testing.T) {
	limits := testLimits()
	limits.MaxEvents = 3
	limits.KeyQuota = 10
	m := impl.NewServiceAccountKeyMeter(limits)
	mustRegister(t, m, "key-1", "acct-1", "orders")
	t0 := baseClock()
	for i := 0; i < 5; i++ {
		if err := m.Record("key-1", "orders", t0.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
	events := m.Events("acct-1")
	if len(events) != 3 {
		t.Fatalf("retained events = %d, want the cap 3", len(events))
	}
	for i := 1; i < len(events); i++ {
		if events[i].Seq <= events[i-1].Seq {
			t.Fatalf("event seq must strictly increase, got %d then %d", events[i-1].Seq, events[i].Seq)
		}
	}
	if events[0].Seq != 3 || events[2].Seq != 5 {
		t.Fatalf("retained seqs = [%d %d %d], want the newest window [3 4 5]", events[0].Seq, events[1].Seq, events[2].Seq)
	}
	if len(m.Events("acct-404")) != 0 {
		t.Fatalf("an account without calls must have an empty audit log")
	}
}

func TestDecideAndCommitCountsOnce(t *testing.T) {
	m := impl.NewServiceAccountKeyMeter(testLimits())
	mustRegister(t, m, "key-1", "acct-1", "orders")
	t0 := baseClock()
	d, err := m.Decide("key-1", "orders", t0)
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if d.ID == "" || d.Verdict != impl.DecisionAllow || d.KeyID != "key-1" || d.Target != "orders" {
		t.Fatalf("decision = %+v, want an allow verdict for key-1 on orders", d)
	}
	if !d.ExpiresAt.Equal(t0.Add(10 * time.Minute)) {
		t.Fatalf("decision expiry = %s, want the window later %s", d.ExpiresAt, t0.Add(10*time.Minute))
	}
	if err := m.Commit(d.ID, t0.Add(time.Minute)); err != nil {
		t.Fatalf("commit within the window: %v", err)
	}
	u, err := m.Usage("key-1", time.Minute)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if u.Count != 1 || u.Allowed != 1 {
		t.Fatalf("usage after one commit = %+v, want count 1 and allowed 1", u)
	}
	if err := m.Commit(d.ID, t0.Add(2*time.Minute)); !errors.Is(err, impl.ErrDecisionUsed) {
		t.Fatalf("committing the same decision twice must fail with ErrDecisionUsed, got %v", err)
	}
	if err := m.Commit("dec-404", t0); !errors.Is(err, impl.ErrDecisionNotFound) {
		t.Fatalf("committing an unknown decision must fail with ErrDecisionNotFound, got %v", err)
	}
}

func TestSnapshotRestoreKeepsUsageAndState(t *testing.T) {
	limits := testLimits()
	m := impl.NewServiceAccountKeyMeter(limits)
	mustRegister(t, m, "key-1", "acct-1", "orders")
	mustRegister(t, m, "key-2", "acct-1", "orders")
	t0 := baseClock()
	if err := m.Record("key-1", "orders", t0); err != nil {
		t.Fatalf("record first: %v", err)
	}
	if err := m.Record("key-1", "orders", t0.Add(time.Minute)); err != nil {
		t.Fatalf("record second: %v", err)
	}
	if err := m.Suspend("key-2", "manual hold"); err != nil {
		t.Fatalf("suspend key-2: %v", err)
	}
	data, err := m.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload must not be empty")
	}
	restored := impl.NewServiceAccountKeyMeter(limits)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	st, err := restored.KeyState("key-1")
	if err != nil {
		t.Fatalf("restored key state: %v", err)
	}
	if st.State != impl.KeyActive || st.Allowed != 2 || st.Seq != 2 {
		t.Fatalf("restored key stat = %+v, want active, allowed 2, seq 2", st)
	}
	u, err := restored.Usage("key-1", 10*time.Minute)
	if err != nil {
		t.Fatalf("restored usage: %v", err)
	}
	if u.Count != 2 || !u.To.Equal(t0.Add(time.Minute)) {
		t.Fatalf("restored usage = %+v, want count 2 at the saved watermark", u)
	}
	st2, err := restored.KeyState("key-2")
	if err != nil {
		t.Fatalf("restored key-2 state: %v", err)
	}
	if st2.State != impl.KeySuspended || st2.Reason != "manual hold" {
		t.Fatalf("restored key-2 = %+v, want suspended with the saved reason", st2)
	}
	if err := restored.Record("key-1", "orders", t0.Add(2*time.Minute)); err != nil {
		t.Fatalf("record after restore: %v", err)
	}
	st3, err := restored.KeyState("key-1")
	if err != nil {
		t.Fatalf("key state after restore record: %v", err)
	}
	if st3.Seq <= st.Seq {
		t.Fatalf("sequence after restore = %d, must continue past %d", st3.Seq, st.Seq)
	}
	if err := restored.Restore([]byte("{broken")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("a corrupt payload must fail with ErrBadSnapshot, got %v", err)
	}
	if _, err := restored.Usage("key-1", 10*time.Minute); err != nil {
		t.Fatalf("a rejected restore must leave the live state usable, got %v", err)
	}
}
