package task

import (
	"errors"
	"sync"
	"testing"
	"time"

	impl "gsb/batch49-go-safety-a-15"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 5, 11, 8, 30, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func testLimits(clock *fakeClock) impl.Limits {
	return impl.Limits{
		MaxGrantsPerAdmin:    5,
		MaxAdminsPerIncident: 3,
		MaxDuration:          30 * time.Minute,
		ReviewWindow:         4 * time.Hour,
		Cooldown:             time.Hour,
		MaxEvents:            16,
		Now:                  clock.Now,
	}
}

func mustTrigger(t *testing.T, l *impl.AdminBreakGlassLedger, adminID, incidentID string) impl.Grant {
	t.Helper()
	grant, err := l.Trigger(adminID, incidentID, "sso outage during rotation")
	if err != nil {
		t.Fatalf("trigger %s on %s: %v", adminID, incidentID, err)
	}
	return grant
}

func TestTriggerCreatesActiveGrant(t *testing.T) {
	clock := newClock()
	limits := testLimits(clock)
	l := impl.NewAdminBreakGlassLedger(limits)
	grant := mustTrigger(t, l, "admin-1", "incident-1")
	if grant.ID == "" || grant.AdminID != "admin-1" || grant.IncidentID != "incident-1" {
		t.Fatalf("grant = %+v, want an issued grant for admin-1 on incident-1", grant)
	}
	if grant.State != impl.GrantActive {
		t.Fatalf("grant state = %q, want %q", grant.State, impl.GrantActive)
	}
	if grant.Reason != "sso outage during rotation" {
		t.Fatalf("grant reason = %q, want the operator note", grant.Reason)
	}
	if !grant.ExpiresAt.Equal(clock.Now().Add(limits.MaxDuration)) {
		t.Fatalf("grant expiry = %s, want one max duration later", grant.ExpiresAt)
	}
	if grant.Remaining != limits.MaxDuration {
		t.Fatalf("grant remaining = %s, want %s", grant.Remaining, limits.MaxDuration)
	}
	if grant.ReviewerID != "" {
		t.Fatalf("fresh grant reviewer = %q, want empty", grant.ReviewerID)
	}
	events := l.Events("incident-1")
	if len(events) != 1 {
		t.Fatalf("events = %d, want the activation notice", len(events))
	}
	notice := events[0]
	if notice.Kind != impl.EventNotify || notice.State != impl.GrantActive || notice.Seq != 1 {
		t.Fatalf("notice = %+v, want a notify event in the active state with seq 1", notice)
	}
	if notice.AdminID != "admin-1" || notice.GrantID != grant.ID {
		t.Fatalf("notice identity = %+v, want the grant and admin", notice)
	}
}

func TestTriggerValidation(t *testing.T) {
	clock := newClock()
	l := impl.NewAdminBreakGlassLedger(testLimits(clock))
	if _, err := l.Trigger("  ", "incident-1", "reason"); !errors.Is(err, impl.ErrBlankAdmin) {
		t.Fatalf("blank admin must fail with ErrBlankAdmin, got %v", err)
	}
	if _, err := l.Trigger("admin-1", " ", "reason"); !errors.Is(err, impl.ErrBlankIncident) {
		t.Fatalf("blank incident must fail with ErrBlankIncident, got %v", err)
	}
	if _, err := l.Trigger("admin-1", "incident-1", "   "); !errors.Is(err, impl.ErrBadReason) {
		t.Fatalf("blank reason must fail with ErrBadReason, got %v", err)
	}
	if _, err := l.Quota("  "); !errors.Is(err, impl.ErrBlankAdmin) {
		t.Fatalf("blank admin quota must fail with ErrBlankAdmin, got %v", err)
	}
}

func TestTriggerDuplicateIsRejected(t *testing.T) {
	clock := newClock()
	l := impl.NewAdminBreakGlassLedger(testLimits(clock))
	grant := mustTrigger(t, l, "admin-1", "incident-1")
	if _, err := l.Trigger("admin-1", "incident-1", "second attempt"); !errors.Is(err, impl.ErrAlreadyGranted) {
		t.Fatalf("a second open grant for the same admin and incident must fail with ErrAlreadyGranted, got %v", err)
	}
	active := l.Active("admin-1")
	if len(active) != 1 || active[0].ID != grant.ID {
		t.Fatalf("active grants = %+v, want exactly the first grant", active)
	}
	if got := len(l.Events("incident-1")); got != 1 {
		t.Fatalf("events = %d, want the rejected attempt to leave no notice", got)
	}
}

func TestTriggerAfterRejectionEntersCooldown(t *testing.T) {
	clock := newClock()
	limits := testLimits(clock)
	l := impl.NewAdminBreakGlassLedger(limits)
	grant := mustTrigger(t, l, "admin-1", "incident-1")
	if err := l.Expire(grant.ID); err != nil {
		t.Fatalf("expire: %v", err)
	}
	if _, err := l.Review(grant.ID, "sec-1", false); err != nil {
		t.Fatalf("review: %v", err)
	}
	if _, err := l.Trigger("admin-1", "incident-2", "new outage"); !errors.Is(err, impl.ErrCooldown) {
		t.Fatalf("a trigger during cooldown must fail with ErrCooldown, got %v", err)
	}
	until, err := l.CooldownUntil("admin-1")
	if err != nil {
		t.Fatalf("cooldown until: %v", err)
	}
	if !until.Equal(clock.Now().Add(limits.Cooldown)) {
		t.Fatalf("cooldown until = %s, want %s", until, clock.Now().Add(limits.Cooldown))
	}
}

func TestQuotaCountsDistinctIncidents(t *testing.T) {
	clock := newClock()
	limits := testLimits(clock)
	limits.MaxGrantsPerAdmin = 2
	l := impl.NewAdminBreakGlassLedger(limits)
	mustTrigger(t, l, "admin-1", "incident-1")
	mustTrigger(t, l, "admin-1", "incident-2")
	remaining, err := l.Quota("admin-1")
	if err != nil {
		t.Fatalf("quota: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("remaining quota = %d, want 0 after two distinct incidents", remaining)
	}
	if _, err := l.Trigger("admin-1", "incident-3", "third outage"); !errors.Is(err, impl.ErrQuotaExceeded) {
		t.Fatalf("a third distinct incident over a quota of two must fail with ErrQuotaExceeded, got %v", err)
	}
	other, err := l.Quota("admin-2")
	if err != nil {
		t.Fatalf("quota of an unseen admin: %v", err)
	}
	if other != 2 {
		t.Fatalf("remaining quota of an unseen admin = %d, want the full 2", other)
	}
}

func TestUseValidationAndMarksInUse(t *testing.T) {
	clock := newClock()
	l := impl.NewAdminBreakGlassLedger(testLimits(clock))
	if err := l.Use("grant-404", "read:logs"); !errors.Is(err, impl.ErrGrantNotFound) {
		t.Fatalf("use of an unknown grant must fail with ErrGrantNotFound, got %v", err)
	}
	grant := mustTrigger(t, l, "admin-1", "incident-1")
	if err := l.Use(grant.ID, "   "); !errors.Is(err, impl.ErrBadAction) {
		t.Fatalf("a blank action must fail with ErrBadAction, got %v", err)
	}
	if err := l.Use(grant.ID, "read:logs"); err != nil {
		t.Fatalf("use read: %v", err)
	}
	stored, err := l.GrantState(grant.ID)
	if err != nil {
		t.Fatalf("grant state: %v", err)
	}
	if stored.State != impl.GrantInUse {
		t.Fatalf("grant state = %q, want %q", stored.State, impl.GrantInUse)
	}
	events := l.Events("incident-1")
	if len(events) != 2 {
		t.Fatalf("events = %d, want the notice plus one use", len(events))
	}
	last := events[1]
	if last.Kind != impl.EventUse || last.Action != "read:logs" || last.State != impl.GrantInUse {
		t.Fatalf("use event = %+v, want a use record for read:logs", last)
	}
	if err := l.Use(grant.ID, "write:config"); err != nil {
		t.Fatalf("use write on an active grant: %v", err)
	}
	if got := len(l.Events("incident-1")); got != 3 {
		t.Fatalf("events = %d, want the second use appended", got)
	}
}

func TestUseOnRejectedGrantFails(t *testing.T) {
	clock := newClock()
	l := impl.NewAdminBreakGlassLedger(testLimits(clock))
	grant := mustTrigger(t, l, "admin-1", "incident-1")
	if err := l.Expire(grant.ID); err != nil {
		t.Fatalf("expire: %v", err)
	}
	if _, err := l.Review(grant.ID, "sec-1", false); err != nil {
		t.Fatalf("review: %v", err)
	}
	if err := l.Use(grant.ID, "read:logs"); !errors.Is(err, impl.ErrGrantRejected) {
		t.Fatalf("use of a rejected grant must fail with ErrGrantRejected, got %v", err)
	}
}

func TestUseOnUpheldGrantFails(t *testing.T) {
	clock := newClock()
	l := impl.NewAdminBreakGlassLedger(testLimits(clock))
	grant := mustTrigger(t, l, "admin-1", "incident-1")
	if err := l.Expire(grant.ID); err != nil {
		t.Fatalf("expire: %v", err)
	}
	if _, err := l.Review(grant.ID, "sec-1", true); err != nil {
		t.Fatalf("review: %v", err)
	}
	if err := l.Use(grant.ID, "read:logs"); !errors.Is(err, impl.ErrGrantClosed) {
		t.Fatalf("use of a closed grant must fail with ErrGrantClosed, got %v", err)
	}
}

func TestExpireTransitions(t *testing.T) {
	clock := newClock()
	l := impl.NewAdminBreakGlassLedger(testLimits(clock))
	if err := l.Expire("grant-404"); !errors.Is(err, impl.ErrGrantNotFound) {
		t.Fatalf("expiring an unknown grant must fail with ErrGrantNotFound, got %v", err)
	}
	grant := mustTrigger(t, l, "admin-1", "incident-1")
	if err := l.Expire(grant.ID); err != nil {
		t.Fatalf("expire: %v", err)
	}
	stored, err := l.GrantState(grant.ID)
	if err != nil {
		t.Fatalf("grant state: %v", err)
	}
	if stored.State != impl.GrantPendingReview {
		t.Fatalf("grant state = %q, want %q", stored.State, impl.GrantPendingReview)
	}
	if err := l.Expire(grant.ID); !errors.Is(err, impl.ErrNotGrantActive) {
		t.Fatalf("expiring twice must fail with ErrNotGrantActive, got %v", err)
	}
}

func TestReviewValidation(t *testing.T) {
	clock := newClock()
	l := impl.NewAdminBreakGlassLedger(testLimits(clock))
	if _, err := l.Review("grant-404", "sec-1", true); !errors.Is(err, impl.ErrGrantNotFound) {
		t.Fatalf("review of an unknown grant must fail with ErrGrantNotFound, got %v", err)
	}
	open := mustTrigger(t, l, "admin-1", "incident-1")
	if _, err := l.Review(open.ID, "sec-1", true); !errors.Is(err, impl.ErrNotPendingReview) {
		t.Fatalf("review of an active grant must fail with ErrNotPendingReview, got %v", err)
	}
	if err := l.Expire(open.ID); err != nil {
		t.Fatalf("expire: %v", err)
	}
	if _, err := l.Review(open.ID, "  ", true); !errors.Is(err, impl.ErrBlankReviewer) {
		t.Fatalf("a blank reviewer must fail with ErrBlankReviewer, got %v", err)
	}
	if _, err := l.Review(open.ID, "admin-1", true); !errors.Is(err, impl.ErrSelfReview) {
		t.Fatalf("the granting admin must not review the grant, got %v", err)
	}
}

func TestReviewUphold(t *testing.T) {
	clock := newClock()
	l := impl.NewAdminBreakGlassLedger(testLimits(clock))
	grant := mustTrigger(t, l, "admin-1", "incident-1")
	if err := l.Expire(grant.ID); err != nil {
		t.Fatalf("expire: %v", err)
	}
	verdict, err := l.Review(grant.ID, "sec-1", true)
	if err != nil {
		t.Fatalf("review uphold: %v", err)
	}
	if verdict.Verdict != impl.VerdictUpheld || verdict.ReviewerID != "sec-1" || verdict.GrantID != grant.ID {
		t.Fatalf("verdict = %+v, want an upheld verdict by sec-1", verdict)
	}
	if !verdict.At.Equal(clock.Now()) {
		t.Fatalf("verdict time = %s, want the review instant", verdict.At)
	}
	stored, err := l.GrantState(grant.ID)
	if err != nil {
		t.Fatalf("grant state: %v", err)
	}
	if stored.State != impl.GrantUpheld || stored.ReviewerID != "sec-1" {
		t.Fatalf("grant = %+v, want upheld and reviewed by sec-1", stored)
	}
	if stored.Remaining != 0 {
		t.Fatalf("remaining after review = %s, want 0", stored.Remaining)
	}
}

func TestReviewRejectStartsCooldown(t *testing.T) {
	clock := newClock()
	limits := testLimits(clock)
	l := impl.NewAdminBreakGlassLedger(limits)
	grant := mustTrigger(t, l, "admin-1", "incident-1")
	if err := l.Expire(grant.ID); err != nil {
		t.Fatalf("expire: %v", err)
	}
	verdict, err := l.Review(grant.ID, "sec-2", false)
	if err != nil {
		t.Fatalf("review reject: %v", err)
	}
	if verdict.Verdict != impl.VerdictRejected {
		t.Fatalf("verdict = %+v, want rejected", verdict)
	}
	stored, err := l.GrantState(grant.ID)
	if err != nil {
		t.Fatalf("grant state: %v", err)
	}
	if stored.State != impl.GrantRejected || stored.ReviewerID != "sec-2" {
		t.Fatalf("grant = %+v, want rejected and reviewed by sec-2", stored)
	}
	until, err := l.CooldownUntil("admin-1")
	if err != nil {
		t.Fatalf("cooldown until: %v", err)
	}
	if !until.Equal(clock.Now().Add(limits.Cooldown)) {
		t.Fatalf("cooldown until = %s, want one cooldown after the rejection", until)
	}
	if _, err := l.Trigger("admin-1", "incident-2", "unrelated outage"); !errors.Is(err, impl.ErrCooldown) {
		t.Fatalf("the rejected admin must be blocked during cooldown, got %v", err)
	}
}

func TestEventsCapRetainsNewest(t *testing.T) {
	clock := newClock()
	limits := testLimits(clock)
	limits.MaxEvents = 4
	l := impl.NewAdminBreakGlassLedger(limits)
	grant := mustTrigger(t, l, "admin-1", "incident-1")
	for i := 0; i < 3; i++ {
		if err := l.Use(grant.ID, "write:config"); err != nil {
			t.Fatalf("use %d: %v", i, err)
		}
	}
	if err := l.Expire(grant.ID); err != nil {
		t.Fatalf("expire: %v", err)
	}
	events := l.Events("incident-1")
	if len(events) != 4 {
		t.Fatalf("retained events = %d, want the cap 4", len(events))
	}
	for i := 1; i < len(events); i++ {
		if events[i].Seq <= events[i-1].Seq {
			t.Fatalf("event seq must strictly increase, got %d then %d", events[i-1].Seq, events[i].Seq)
		}
	}
	if events[0].Seq != 2 || events[3].Seq != 5 {
		t.Fatalf("retained seqs = [%d %d %d %d], want the newest window [2 3 4 5]", events[0].Seq, events[1].Seq, events[2].Seq, events[3].Seq)
	}
	if events[0].Kind != impl.EventUse || events[3].Kind != impl.EventExpire {
		t.Fatalf("retained kinds = [%s ... %s], want use first and expire last", events[0].Kind, events[3].Kind)
	}
}

func TestSnapshotRestorePreservesGrants(t *testing.T) {
	clock := newClock()
	limits := testLimits(clock)
	l := impl.NewAdminBreakGlassLedger(limits)
	grant := mustTrigger(t, l, "admin-1", "incident-1")
	if err := l.Use(grant.ID, "read:logs"); err != nil {
		t.Fatalf("use: %v", err)
	}
	if err := l.Expire(grant.ID); err != nil {
		t.Fatalf("expire: %v", err)
	}
	data, err := l.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload must not be empty")
	}
	restored := impl.NewAdminBreakGlassLedger(limits)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	stored, err := restored.GrantState(grant.ID)
	if err != nil {
		t.Fatalf("restored grant state: %v", err)
	}
	if stored.State != impl.GrantPendingReview || stored.AdminID != "admin-1" {
		t.Fatalf("restored grant = %+v, want the pending review grant of admin-1", stored)
	}
	quota, err := restored.Quota("admin-1")
	if err != nil {
		t.Fatalf("restored quota: %v", err)
	}
	if quota != limits.MaxGrantsPerAdmin-1 {
		t.Fatalf("restored quota = %d, want %d", quota, limits.MaxGrantsPerAdmin-1)
	}
	if got := len(restored.Events("incident-1")); got != 3 {
		t.Fatalf("restored events = %d, want the notice, use and expire", got)
	}
	if err := restored.Restore([]byte("[]")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("a corrupt payload must fail with ErrBadSnapshot, got %v", err)
	}
	stored2, err := restored.GrantState(grant.ID)
	if err != nil {
		t.Fatalf("grant state after rejected restore: %v", err)
	}
	if stored2.State != impl.GrantPendingReview {
		t.Fatalf("a rejected restore must keep the live grant, state = %q", stored2.State)
	}
	if _, err := restored.Review(grant.ID, "sec-1", true); err != nil {
		t.Fatalf("review after restore: %v", err)
	}
}

func TestConcurrentTriggerSingleWinner(t *testing.T) {
	clock := newClock()
	l := impl.NewAdminBreakGlassLedger(testLimits(clock))
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners, duplicated, other := 0, 0, 0
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := l.Trigger("admin-1", "incident-1", "sso outage")
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				winners++
			case errors.Is(err, impl.ErrAlreadyGranted):
				duplicated++
			default:
				other++
			}
		}()
	}
	wg.Wait()
	if winners != 1 || duplicated != 15 || other != 0 {
		t.Fatalf("winners=%d duplicated=%d other=%d, want 1/15/0", winners, duplicated, other)
	}
	if got := len(l.Active("admin-1")); got != 1 {
		t.Fatalf("active grants = %d, want 1", got)
	}
	if got := len(l.Events("incident-1")); got != 1 {
		t.Fatalf("events = %d, want a single activation notice", got)
	}
	quota, err := l.Quota("admin-1")
	if err != nil {
		t.Fatalf("quota: %v", err)
	}
	if quota != 4 {
		t.Fatalf("quota = %d, want 4 after one distinct incident", quota)
	}
}
