package task

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	impl "gsb/batch49-go-safety-a-14"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 4, 6, 10, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func testLimits(clock *fakeClock) impl.Limits {
	return impl.Limits{
		LeaseTTL:          30 * time.Minute,
		ConsentTTL:        10 * time.Minute,
		MaxLeasesPerUser:  4,
		MaxLeasesPerAdmin: 4,
		MaxEnvelopes:      16,
		Now:               clock.Now,
	}
}

func mustConsent(t *testing.T, m *impl.ImpersonationSessionLease, adminID, userID, scope string, mode impl.Mode) impl.Consent {
	t.Helper()
	consent, err := m.RequestConsent(adminID, userID, scope, mode)
	if err != nil {
		t.Fatalf("request consent: %v", err)
	}
	return consent
}

func mustLease(t *testing.T, m *impl.ImpersonationSessionLease, adminID, userID, scope string, mode impl.Mode) impl.Lease {
	t.Helper()
	consent := mustConsent(t, m, adminID, userID, scope, mode)
	lease, err := m.Grant(consent.ID, userID)
	if err != nil {
		t.Fatalf("grant lease: %v", err)
	}
	return lease
}

func mustSession(t *testing.T, m *impl.ImpersonationSessionLease, leaseID string) impl.Session {
	t.Helper()
	session, err := m.Begin(leaseID)
	if err != nil {
		t.Fatalf("begin session: %v", err)
	}
	return session
}

func TestRequestConsentCreatesPendingConsent(t *testing.T) {
	clock := newClock()
	m := impl.NewImpersonationSessionLease(testLimits(clock))
	consent := mustConsent(t, m, "admin-1", "user-1", "profile,orders", impl.ModeReadWrite)
	if consent.ID == "" {
		t.Fatalf("consent id must not be empty")
	}
	if consent.AdminID != "admin-1" || consent.UserID != "user-1" || consent.Scope != "profile,orders" {
		t.Fatalf("consent = %+v, want the requested identity and scope", consent)
	}
	if consent.Mode != impl.ModeReadWrite || consent.State != impl.ConsentPending {
		t.Fatalf("consent = %+v, want a pending read write approval", consent)
	}
	wantExpiry := clock.Now().Add(10 * time.Minute)
	if !consent.ExpiresAt.Equal(wantExpiry) {
		t.Fatalf("consent expiry = %s, want %s", consent.ExpiresAt, wantExpiry)
	}
	stored, err := m.ConsentState(consent.ID)
	if err != nil {
		t.Fatalf("consent state: %v", err)
	}
	if stored.State != impl.ConsentPending || stored.UserID != "user-1" {
		t.Fatalf("stored consent = %+v, want the pending approval", stored)
	}
}

func TestRequestConsentValidation(t *testing.T) {
	clock := newClock()
	m := impl.NewImpersonationSessionLease(testLimits(clock))
	if _, err := m.RequestConsent("  ", "user-1", "profile", impl.ModeReadOnly); !errors.Is(err, impl.ErrBlankAdmin) {
		t.Fatalf("blank admin must fail with ErrBlankAdmin, got %v", err)
	}
	if _, err := m.RequestConsent("admin-1", " ", "profile", impl.ModeReadOnly); !errors.Is(err, impl.ErrBlankUser) {
		t.Fatalf("blank user must fail with ErrBlankUser, got %v", err)
	}
	if _, err := m.RequestConsent("admin-1", "user-1", "  ", impl.ModeReadOnly); !errors.Is(err, impl.ErrBlankScope) {
		t.Fatalf("blank scope must fail with ErrBlankScope, got %v", err)
	}
	if _, err := m.RequestConsent("admin-1", "user-1", "profile", impl.Mode("root")); !errors.Is(err, impl.ErrBadMode) {
		t.Fatalf("an unknown mode must fail with ErrBadMode, got %v", err)
	}
}

func TestGrantRejectsUnknownMismatchedAndUsed(t *testing.T) {
	clock := newClock()
	m := impl.NewImpersonationSessionLease(testLimits(clock))
	if _, err := m.Grant("consent-404", "user-1"); !errors.Is(err, impl.ErrUnknownConsent) {
		t.Fatalf("an unknown consent must fail with ErrUnknownConsent, got %v", err)
	}
	consent := mustConsent(t, m, "admin-1", "user-1", "profile", impl.ModeReadOnly)
	if _, err := m.Grant(consent.ID, "user-2"); !errors.Is(err, impl.ErrConsentMismatch) {
		t.Fatalf("a consent granted to another user must fail with ErrConsentMismatch, got %v", err)
	}
	if _, err := m.Grant(consent.ID, "user-1"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if _, err := m.Grant(consent.ID, "user-1"); !errors.Is(err, impl.ErrConsentUsed) {
		t.Fatalf("granting the same consent twice must fail with ErrConsentUsed, got %v", err)
	}
}

func TestGrantIssuesActiveLease(t *testing.T) {
	clock := newClock()
	limits := testLimits(clock)
	m := impl.NewImpersonationSessionLease(limits)
	lease := mustLease(t, m, "admin-1", "user-1", "profile", impl.ModeReadWrite)
	if lease.ID == "" || lease.AdminID != "admin-1" || lease.UserID != "user-1" {
		t.Fatalf("lease = %+v, want an issued lease for admin-1 on user-1", lease)
	}
	if lease.State != impl.LeaseActive || lease.Mode != impl.ModeReadWrite {
		t.Fatalf("lease = %+v, want an active read write lease", lease)
	}
	if !lease.ExpiresAt.Equal(clock.Now().Add(limits.LeaseTTL)) {
		t.Fatalf("lease expiry = %s, want one lease ttl later", lease.ExpiresAt)
	}
	if lease.Remaining != limits.LeaseTTL {
		t.Fatalf("lease remaining = %s, want %s", lease.Remaining, limits.LeaseTTL)
	}
	active := m.Active("user-1")
	if len(active) != 1 || active[0].ID != lease.ID {
		t.Fatalf("active leases = %+v, want the issued lease", active)
	}
	stored, err := m.LeaseState(lease.ID)
	if err != nil {
		t.Fatalf("lease state: %v", err)
	}
	if stored.ID != lease.ID || stored.State != impl.LeaseActive || stored.Remaining != limits.LeaseTTL {
		t.Fatalf("stored lease = %+v, want the active lease with full remaining time", stored)
	}
}

func TestLeaseCaps(t *testing.T) {
	clock := newClock()
	userLimits := testLimits(clock)
	userLimits.MaxLeasesPerUser = 2
	userLimits.MaxLeasesPerAdmin = 10
	userMgr := impl.NewImpersonationSessionLease(userLimits)
	mustLease(t, userMgr, "admin-1", "user-1", "profile", impl.ModeReadOnly)
	mustLease(t, userMgr, "admin-2", "user-1", "orders", impl.ModeReadOnly)
	consent := mustConsent(t, userMgr, "admin-3", "user-1", "billing", impl.ModeReadOnly)
	if _, err := userMgr.Grant(consent.ID, "user-1"); !errors.Is(err, impl.ErrUserLeaseLimit) {
		t.Fatalf("a third live lease for one user must fail with ErrUserLeaseLimit, got %v", err)
	}

	adminLimits := testLimits(clock)
	adminLimits.MaxLeasesPerUser = 10
	adminLimits.MaxLeasesPerAdmin = 2
	adminMgr := impl.NewImpersonationSessionLease(adminLimits)
	mustLease(t, adminMgr, "admin-1", "user-1", "profile", impl.ModeReadOnly)
	mustLease(t, adminMgr, "admin-1", "user-2", "profile", impl.ModeReadOnly)
	consent = mustConsent(t, adminMgr, "admin-1", "user-3", "profile", impl.ModeReadOnly)
	if _, err := adminMgr.Grant(consent.ID, "user-3"); !errors.Is(err, impl.ErrAdminLeaseLimit) {
		t.Fatalf("a third live lease for one admin must fail with ErrAdminLeaseLimit, got %v", err)
	}
}

func TestBeginRejectsUnknownAndBusyAndEndSessionFrees(t *testing.T) {
	clock := newClock()
	m := impl.NewImpersonationSessionLease(testLimits(clock))
	if _, err := m.Begin("lease-404"); !errors.Is(err, impl.ErrUnknownLease) {
		t.Fatalf("begin on an unknown lease must fail with ErrUnknownLease, got %v", err)
	}
	lease := mustLease(t, m, "admin-1", "user-1", "profile", impl.ModeReadWrite)
	session := mustSession(t, m, lease.ID)
	if session.ID == "" || session.LeaseID != lease.ID || session.AdminID != "admin-1" || session.UserID != "user-1" {
		t.Fatalf("session = %+v, want a session bound to the lease", session)
	}
	if session.Mode != impl.ModeReadWrite {
		t.Fatalf("session mode = %q, want the lease mode", session.Mode)
	}
	if _, err := m.Begin(lease.ID); !errors.Is(err, impl.ErrLeaseBusy) {
		t.Fatalf("a second session on one lease must fail with ErrLeaseBusy, got %v", err)
	}
	if err := m.EndSession(session.ID); err != nil {
		t.Fatalf("end session: %v", err)
	}
	if _, err := m.Begin(lease.ID); err != nil {
		t.Fatalf("the lease slot must be free after the session ends, got %v", err)
	}
	if err := m.EndSession("sess-404"); !errors.Is(err, impl.ErrUnknownSession) {
		t.Fatalf("ending an unknown session must fail with ErrUnknownSession, got %v", err)
	}
}

func TestBeginRejectsRevokedLease(t *testing.T) {
	clock := newClock()
	m := impl.NewImpersonationSessionLease(testLimits(clock))
	lease := mustLease(t, m, "admin-1", "user-1", "profile", impl.ModeReadWrite)
	if err := m.Revoke(lease.ID, "user-1", "user opted out"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := m.Begin(lease.ID); !errors.Is(err, impl.ErrLeaseRevoked) {
		t.Fatalf("begin on a revoked lease must fail with ErrLeaseRevoked, got %v", err)
	}
}

func TestAuthorizeRejectsUnknownSessionAndBlankAction(t *testing.T) {
	clock := newClock()
	m := impl.NewImpersonationSessionLease(testLimits(clock))
	lease := mustLease(t, m, "admin-1", "user-1", "profile", impl.ModeReadWrite)
	session := mustSession(t, m, lease.ID)
	if err := m.Authorize("sess-404", "read:profile"); !errors.Is(err, impl.ErrUnknownSession) {
		t.Fatalf("an unknown session must fail with ErrUnknownSession, got %v", err)
	}
	if err := m.Authorize(session.ID, "   "); !errors.Is(err, impl.ErrBadAction) {
		t.Fatalf("a blank action must fail with ErrBadAction, got %v", err)
	}
}

func TestAuthorizeReadRecordsEnvelope(t *testing.T) {
	clock := newClock()
	m := impl.NewImpersonationSessionLease(testLimits(clock))
	lease := mustLease(t, m, "admin-1", "user-1", "profile", impl.ModeReadWrite)
	session := mustSession(t, m, lease.ID)
	if err := m.Authorize(session.ID, "read:profile"); err != nil {
		t.Fatalf("authorize read: %v", err)
	}
	envelopes := m.Envelopes(lease.ID)
	if len(envelopes) != 1 {
		t.Fatalf("envelopes = %d, want 1", len(envelopes))
	}
	env := envelopes[0]
	if env.Action != "read:profile" || !env.Allowed || env.Reason != impl.ReasonAuthorized {
		t.Fatalf("envelope = %+v, want an allowed read", env)
	}
	if env.SessionID != session.ID || env.LeaseID != lease.ID || env.Mode != impl.ModeReadWrite {
		t.Fatalf("envelope identity = %+v, want the session, lease and mode", env)
	}
	if env.Seq != 1 {
		t.Fatalf("envelope seq = %d, want 1", env.Seq)
	}
}

func TestAuthorizeWriteOnWritableLease(t *testing.T) {
	clock := newClock()
	m := impl.NewImpersonationSessionLease(testLimits(clock))
	lease := mustLease(t, m, "admin-2", "user-2", "orders", impl.ModeReadWrite)
	session := mustSession(t, m, lease.ID)
	if err := m.Authorize(session.ID, "read:orders"); err != nil {
		t.Fatalf("authorize read: %v", err)
	}
	if err := m.Authorize(session.ID, "write:orders"); err != nil {
		t.Fatalf("a write on a read write lease must be allowed, got %v", err)
	}
	envelopes := m.Envelopes(lease.ID)
	if len(envelopes) != 2 {
		t.Fatalf("envelopes = %d, want 2", len(envelopes))
	}
	write := envelopes[1]
	if write.Action != "write:orders" || !write.Allowed || write.Reason != impl.ReasonAuthorized {
		t.Fatalf("write envelope = %+v, want an allowed write", write)
	}
	if write.Seq != 2 {
		t.Fatalf("write envelope seq = %d, want 2", write.Seq)
	}
}

func TestRevokeIsImmediateForLease(t *testing.T) {
	clock := newClock()
	m := impl.NewImpersonationSessionLease(testLimits(clock))
	lease := mustLease(t, m, "admin-1", "user-1", "profile", impl.ModeReadWrite)
	session := mustSession(t, m, lease.ID)
	if err := m.Authorize(session.ID, "read:profile"); err != nil {
		t.Fatalf("authorize before revoke: %v", err)
	}
	if err := m.Revoke(lease.ID, "user-1", "user opted out"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	stored, err := m.LeaseState(lease.ID)
	if err != nil {
		t.Fatalf("lease state: %v", err)
	}
	if stored.State != impl.LeaseRevoked {
		t.Fatalf("lease state = %q, want revoked", stored.State)
	}
	if err := m.Authorize(session.ID, "read:profile"); !errors.Is(err, impl.ErrLeaseRevoked) {
		t.Fatalf("authorize after revoke must fail with ErrLeaseRevoked, got %v", err)
	}
	envelopes := m.Envelopes(lease.ID)
	if len(envelopes) != 2 {
		t.Fatalf("envelopes = %d, want the read plus the revocation", len(envelopes))
	}
	last := envelopes[1]
	if last.Action != impl.ActionRevoke || last.Allowed {
		t.Fatalf("revocation envelope = %+v, want a refused revoke record", last)
	}
	if err := m.Revoke(lease.ID, "user-1", "again"); !errors.Is(err, impl.ErrLeaseRevoked) {
		t.Fatalf("revoking twice must fail with ErrLeaseRevoked, got %v", err)
	}
	if err := m.Revoke("lease-404", "user-1", "reason"); !errors.Is(err, impl.ErrUnknownLease) {
		t.Fatalf("revoking an unknown lease must fail with ErrUnknownLease, got %v", err)
	}
	if err := m.Revoke(lease.ID, "user-1", "  "); !errors.Is(err, impl.ErrBadReason) {
		t.Fatalf("a blank revoke reason must fail with ErrBadReason, got %v", err)
	}
	if err := m.Revoke(lease.ID, " ", "reason"); !errors.Is(err, impl.ErrBadActor) {
		t.Fatalf("a blank actor must fail with ErrBadActor, got %v", err)
	}
}

func TestEnvelopesRetainNewest(t *testing.T) {
	clock := newClock()
	limits := testLimits(clock)
	limits.MaxEnvelopes = 3
	m := impl.NewImpersonationSessionLease(limits)
	lease := mustLease(t, m, "admin-1", "user-1", "profile", impl.ModeReadWrite)
	session := mustSession(t, m, lease.ID)
	for i := 0; i < 5; i++ {
		if err := m.Authorize(session.ID, fmt.Sprintf("read:item-%d", i)); err != nil {
			t.Fatalf("authorize %d: %v", i, err)
		}
	}
	envelopes := m.Envelopes(lease.ID)
	if len(envelopes) != 3 {
		t.Fatalf("retained envelopes = %d, want the cap 3", len(envelopes))
	}
	for i := 1; i < len(envelopes); i++ {
		if envelopes[i].Seq <= envelopes[i-1].Seq {
			t.Fatalf("envelope seq must strictly increase, got %d then %d", envelopes[i-1].Seq, envelopes[i].Seq)
		}
	}
	if envelopes[0].Seq != 3 || envelopes[2].Seq != 5 {
		t.Fatalf("retained seqs = [%d %d %d], want the newest window [3 4 5]", envelopes[0].Seq, envelopes[1].Seq, envelopes[2].Seq)
	}
	if envelopes[2].Action != "read:item-4" {
		t.Fatalf("newest envelope = %+v, want read:item-4", envelopes[2])
	}
}

func TestConcurrentAuthorizeSerialized(t *testing.T) {
	clock := newClock()
	limits := testLimits(clock)
	limits.MaxEnvelopes = 64
	m := impl.NewImpersonationSessionLease(limits)
	lease := mustLease(t, m, "admin-1", "user-1", "profile", impl.ModeReadWrite)
	session := mustSession(t, m, lease.ID)
	var wg sync.WaitGroup
	var mu sync.Mutex
	failures := 0
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := m.Authorize(session.ID, fmt.Sprintf("read:item-%d", i)); err != nil {
				mu.Lock()
				failures++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if failures != 0 {
		t.Fatalf("%d concurrent authorizations failed, want all to succeed", failures)
	}
	envelopes := m.Envelopes(lease.ID)
	if len(envelopes) != 24 {
		t.Fatalf("envelopes = %d, want 24", len(envelopes))
	}
	seen := make(map[int64]bool, 24)
	for _, env := range envelopes {
		if env.Seq < 1 || env.Seq > 24 || seen[env.Seq] {
			t.Fatalf("envelope seq = %d, want each of 1..24 exactly once", env.Seq)
		}
		seen[env.Seq] = true
	}
}

func TestSnapshotRestorePreservesLeaseAndEnvelopes(t *testing.T) {
	clock := newClock()
	limits := testLimits(clock)
	m := impl.NewImpersonationSessionLease(limits)
	lease := mustLease(t, m, "admin-1", "user-1", "profile", impl.ModeReadWrite)
	session := mustSession(t, m, lease.ID)
	if err := m.Authorize(session.ID, "read:profile"); err != nil {
		t.Fatalf("authorize: %v", err)
	}
	data, err := m.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload must not be empty")
	}
	clock.Advance(5 * time.Minute)
	restored := impl.NewImpersonationSessionLease(limits)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	stored, err := restored.LeaseState(lease.ID)
	if err != nil {
		t.Fatalf("restored lease state: %v", err)
	}
	if stored.State != impl.LeaseActive {
		t.Fatalf("restored lease state = %q, want active", stored.State)
	}
	if stored.Remaining != 25*time.Minute {
		t.Fatalf("restored remaining = %s, want 25m after a five minute gap", stored.Remaining)
	}
	active := restored.Active("user-1")
	if len(active) != 1 || active[0].ID != lease.ID {
		t.Fatalf("restored active leases = %+v, want the saved lease", active)
	}
	envelopes := restored.Envelopes(lease.ID)
	if len(envelopes) != 1 || envelopes[0].Action != "read:profile" {
		t.Fatalf("restored envelopes = %+v, want the saved read", envelopes)
	}
	if err := restored.Authorize(session.ID, "write:profile"); err != nil {
		t.Fatalf("the restored session must stay usable, got %v", err)
	}
	if err := restored.Restore([]byte("nope")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("a corrupt payload must fail with ErrBadSnapshot, got %v", err)
	}
	stored2, err := restored.LeaseState(lease.ID)
	if err != nil {
		t.Fatalf("lease state after rejected restore: %v", err)
	}
	if stored2.State != impl.LeaseActive {
		t.Fatalf("a rejected restore must keep the live lease, state = %q", stored2.State)
	}
}

func TestActiveListsOnlyLiveLeases(t *testing.T) {
	clock := newClock()
	m := impl.NewImpersonationSessionLease(testLimits(clock))
	first := mustLease(t, m, "admin-1", "user-1", "profile", impl.ModeReadOnly)
	second := mustLease(t, m, "admin-1", "user-1", "orders", impl.ModeReadOnly)
	third := mustLease(t, m, "admin-2", "user-1", "billing", impl.ModeReadOnly)
	if err := m.Revoke(second.ID, "user-1", "user opted out"); err != nil {
		t.Fatalf("revoke second lease: %v", err)
	}
	active := m.Active("user-1")
	if len(active) != 2 {
		t.Fatalf("active leases = %d, want 2 after one revocation", len(active))
	}
	if active[0].ID != first.ID || active[1].ID != third.ID {
		t.Fatalf("active leases = [%s %s], want the two live leases ordered by id", active[0].ID, active[1].ID)
	}
	if got := len(m.Active("user-404")); got != 0 {
		t.Fatalf("a user without leases must have no active leases, got %d", got)
	}
}
