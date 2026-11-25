package task

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	impl "gsb/batch49-go-safety-a-16"
)

var ledgerBase = time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)

func ledgerLimits() impl.Limits {
	return impl.Limits{MaxGrantsPerUser: 8, MaxRecipientsPerObject: 4}
}

func mustGrant(t *testing.T, l *impl.DataSharingConsentLedger, user, recipient, object, purpose string, ttl time.Duration, now time.Time) impl.Grant {
	t.Helper()
	g, err := l.GrantAt(user, recipient, object, purpose, ttl, now)
	if err != nil {
		t.Fatalf("grant %s/%s/%s: %v", user, object, recipient, err)
	}
	return g
}

func TestGrantWritesLedgerEntry(t *testing.T) {
	l := impl.NewDataSharingConsentLedger(ledgerLimits())
	g, err := l.GrantAt("user-1", "receiver-1", "object-1", "claims_review", time.Hour, ledgerBase)
	if err != nil {
		t.Fatalf("grant must succeed, got %v", err)
	}
	if g.UserID != "user-1" || g.RecipientID != "receiver-1" || g.ObjectID != "object-1" || g.Purpose != "claims_review" {
		t.Fatalf("grant identity mismatch: %+v", g)
	}
	if !g.GrantedAt.Equal(ledgerBase) {
		t.Fatalf("grant time = %v, want %v", g.GrantedAt, ledgerBase)
	}
	if !g.ExpiresAt.Equal(ledgerBase.Add(time.Hour)) {
		t.Fatalf("grant expiry = %v, want %v", g.ExpiresAt, ledgerBase.Add(time.Hour))
	}
	if g.Seq <= 0 {
		t.Fatalf("grant sequence = %d, want a positive number", g.Seq)
	}
	if g.Revoked {
		t.Fatalf("fresh grant must not be revoked")
	}
	byObject := l.ListByObject("user-1", "object-1")
	if len(byObject) != 1 || byObject[0].RecipientID != "receiver-1" {
		t.Fatalf("list by object = %+v, want exactly receiver-1", byObject)
	}
	byRecipient := l.ListByRecipient("receiver-1")
	if len(byRecipient) != 1 || byRecipient[0].ObjectID != "object-1" {
		t.Fatalf("list by recipient = %+v, want exactly object-1", byRecipient)
	}
	if got := len(l.ListByObject("user-2", "object-1")); got != 0 {
		t.Fatalf("another user must not see the record, got %d entries", got)
	}
}

func TestGrantRejectsBlankArguments(t *testing.T) {
	l := impl.NewDataSharingConsentLedger(ledgerLimits())
	cases := []struct{ user, recipient, object, purpose string }{
		{"   ", "receiver-1", "object-1", "claims_review"},
		{"user-1", "", "object-1", "claims_review"},
		{"user-1", "receiver-1", "  ", "claims_review"},
		{"user-1", "receiver-1", "object-1", "\t"},
	}
	for i, c := range cases {
		if _, err := l.GrantAt(c.user, c.recipient, c.object, c.purpose, time.Hour, ledgerBase); !errors.Is(err, impl.ErrBlankArgument) {
			t.Fatalf("case %d must fail with ErrBlankArgument, got %v", i, err)
		}
	}
	if got := len(l.ListByObject("user-1", "object-1")); got != 0 {
		t.Fatalf("rejected grants must not be stored, got %d entries", got)
	}
}

func TestGrantRejectsNonPositiveTTL(t *testing.T) {
	l := impl.NewDataSharingConsentLedger(ledgerLimits())
	if _, err := l.GrantAt("user-1", "receiver-1", "object-1", "claims_review", 0, ledgerBase); !errors.Is(err, impl.ErrBadTTL) {
		t.Fatalf("zero ttl must fail with ErrBadTTL, got %v", err)
	}
	if _, err := l.GrantAt("user-1", "receiver-1", "object-1", "claims_review", -time.Minute, ledgerBase); !errors.Is(err, impl.ErrBadTTL) {
		t.Fatalf("negative ttl must fail with ErrBadTTL, got %v", err)
	}
	if got := len(l.ListByObject("user-1", "object-1")); got != 0 {
		t.Fatalf("rejected grants must not be stored, got %d entries", got)
	}
}

func TestGrantRenewsSameTupleInPlace(t *testing.T) {
	l := impl.NewDataSharingConsentLedger(ledgerLimits())
	first := mustGrant(t, l, "user-1", "receiver-1", "object-1", "claims_review", time.Hour, ledgerBase)
	second := mustGrant(t, l, "user-1", "receiver-1", "object-1", "claims_review", 2*time.Hour, ledgerBase.Add(30*time.Minute))
	if second.Seq <= first.Seq {
		t.Fatalf("renewal sequence = %d, must be greater than %d", second.Seq, first.Seq)
	}
	wantExpiry := ledgerBase.Add(30 * time.Minute).Add(2 * time.Hour)
	if !second.ExpiresAt.Equal(wantExpiry) {
		t.Fatalf("renewed expiry = %v, want %v", second.ExpiresAt, wantExpiry)
	}
	records := l.ListByObject("user-1", "object-1")
	if len(records) != 1 {
		t.Fatalf("renewal must keep one record, got %d", len(records))
	}
	if !records[0].ExpiresAt.Equal(wantExpiry) {
		t.Fatalf("stored expiry = %v, want %v", records[0].ExpiresAt, wantExpiry)
	}
}

func TestGrantEnforcesPerUserLimit(t *testing.T) {
	l := impl.NewDataSharingConsentLedger(impl.Limits{MaxGrantsPerUser: 2, MaxRecipientsPerObject: 4})
	mustGrant(t, l, "user-1", "receiver-1", "object-1", "claims_review", time.Hour, ledgerBase)
	mustGrant(t, l, "user-1", "receiver-2", "object-2", "claims_review", time.Hour, ledgerBase)
	if _, err := l.GrantAt("user-1", "receiver-3", "object-3", "claims_review", time.Hour, ledgerBase); !errors.Is(err, impl.ErrGrantLimit) {
		t.Fatalf("third grant must hit ErrGrantLimit, got %v", err)
	}
	if got := len(l.ListByObject("user-1", "object-3")); got != 0 {
		t.Fatalf("rejected grant must not be stored, got %d entries", got)
	}
	total := len(l.ListByRecipient("receiver-1")) + len(l.ListByRecipient("receiver-2"))
	if total != 2 {
		t.Fatalf("stored grants = %d, want the configured cap 2", total)
	}
}

func TestGrantEnforcesRecipientCap(t *testing.T) {
	l := impl.NewDataSharingConsentLedger(impl.Limits{MaxGrantsPerUser: 8, MaxRecipientsPerObject: 2})
	mustGrant(t, l, "user-1", "receiver-1", "object-1", "claims_review", time.Hour, ledgerBase)
	mustGrant(t, l, "user-1", "receiver-2", "object-1", "claims_review", time.Hour, ledgerBase)
	if _, err := l.GrantAt("user-1", "receiver-3", "object-1", "claims_review", time.Hour, ledgerBase); !errors.Is(err, impl.ErrRecipientLimit) {
		t.Fatalf("third recipient must hit ErrRecipientLimit, got %v", err)
	}
	records := l.ListByObject("user-1", "object-1")
	if len(records) != 2 {
		t.Fatalf("object consents = %d, want the configured cap 2", len(records))
	}
	for _, g := range records {
		if g.RecipientID == "receiver-3" {
			t.Fatalf("rejected recipient must not appear in the ledger")
		}
	}
}

func TestConcurrentGrantRespectsRecipientCap(t *testing.T) {
	l := impl.NewDataSharingConsentLedger(impl.Limits{MaxGrantsPerUser: 8, MaxRecipientsPerObject: 4})
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, limited := 0, 0
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := l.GrantAt("user-1", fmt.Sprintf("receiver-%02d", i), "object-1", "claims_review", time.Hour, ledgerBase)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, impl.ErrRecipientLimit):
				limited++
			}
		}(i)
	}
	wg.Wait()
	if ok != 4 {
		t.Fatalf("concurrent grants stored %d records, want exactly the cap 4", ok)
	}
	if limited != 12 {
		t.Fatalf("concurrent grants reported %d recipient limit errors, want 12", limited)
	}
	if got := len(l.ListByObject("user-1", "object-1")); got != 4 {
		t.Fatalf("object consents after the race = %d, want 4", got)
	}
}

func TestGrantSequenceIsMonotonic(t *testing.T) {
	l := impl.NewDataSharingConsentLedger(ledgerLimits())
	a := mustGrant(t, l, "user-1", "receiver-1", "object-1", "claims_review", time.Hour, ledgerBase)
	b := mustGrant(t, l, "user-1", "receiver-2", "object-2", "claims_review", time.Hour, ledgerBase)
	c := mustGrant(t, l, "user-2", "receiver-1", "object-3", "claims_review", time.Hour, ledgerBase)
	if !(a.Seq < b.Seq && b.Seq < c.Seq) {
		t.Fatalf("grant sequences %d, %d, %d must be strictly increasing", a.Seq, b.Seq, c.Seq)
	}
	if got := len(l.ListByRecipient("receiver-1")); got != 2 {
		t.Fatalf("recipient consents = %d, want 2", got)
	}
}

func TestCheckAllowsLiveGrant(t *testing.T) {
	l := impl.NewDataSharingConsentLedger(ledgerLimits())
	mustGrant(t, l, "user-1", "receiver-1", "object-1", "claims_review", time.Hour, ledgerBase)
	d, err := l.CheckAt("receiver-1", "object-1", "claims_review", ledgerBase.Add(10*time.Minute))
	if err != nil {
		t.Fatalf("check must not fail, got %v", err)
	}
	if !d.Allowed {
		t.Fatalf("live grant must allow the read, got reason %q", d.Reason)
	}
	if d.Reason != impl.ReasonAllowed {
		t.Fatalf("decision reason = %q, want %q", d.Reason, impl.ReasonAllowed)
	}
	if !d.ExpiresAt.Equal(ledgerBase.Add(time.Hour)) {
		t.Fatalf("decision expiry = %v, want %v", d.ExpiresAt, ledgerBase.Add(time.Hour))
	}
}

func TestCheckDeniesWithoutGrant(t *testing.T) {
	l := impl.NewDataSharingConsentLedger(ledgerLimits())
	mustGrant(t, l, "user-1", "receiver-1", "object-1", "claims_review", time.Hour, ledgerBase)
	d, err := l.CheckAt("receiver-9", "object-1", "claims_review", ledgerBase)
	if err != nil {
		t.Fatalf("a missing grant is a denial, not an error, got %v", err)
	}
	if d.Allowed {
		t.Fatalf("unknown recipient must be denied")
	}
	if d.Reason != impl.ReasonNotGranted {
		t.Fatalf("decision reason = %q, want %q", d.Reason, impl.ReasonNotGranted)
	}
	if !d.ExpiresAt.IsZero() {
		t.Fatalf("denied decision must not carry an expiry, got %v", d.ExpiresAt)
	}
	if d2, err := l.CheckAt("receiver-1", "object-404", "claims_review", ledgerBase); err != nil || d2.Allowed {
		t.Fatalf("unknown object must be denied, got allowed=%v err=%v", d2.Allowed, err)
	}
}

func TestCheckRejectsBlankQuery(t *testing.T) {
	l := impl.NewDataSharingConsentLedger(ledgerLimits())
	mustGrant(t, l, "user-1", "receiver-1", "object-1", "claims_review", time.Hour, ledgerBase)
	if _, err := l.CheckAt(" ", "object-1", "claims_review", ledgerBase); !errors.Is(err, impl.ErrBlankArgument) {
		t.Fatalf("blank recipient must fail with ErrBlankArgument, got %v", err)
	}
	if _, err := l.CheckAt("receiver-1", "", "claims_review", ledgerBase); !errors.Is(err, impl.ErrBlankArgument) {
		t.Fatalf("blank object must fail with ErrBlankArgument, got %v", err)
	}
	if _, err := l.CheckAt("receiver-1", "object-1", "  ", ledgerBase); !errors.Is(err, impl.ErrBlankArgument) {
		t.Fatalf("blank purpose must fail with ErrBlankArgument, got %v", err)
	}
	if d, err := l.CheckAt("receiver-1", "object-1", "claims_review", ledgerBase); err != nil || !d.Allowed {
		t.Fatalf("a valid query must still allow the read, got allowed=%v err=%v", d.Allowed, err)
	}
}

func TestRevokeMarksGrantRevoked(t *testing.T) {
	l := impl.NewDataSharingConsentLedger(ledgerLimits())
	mustGrant(t, l, "user-1", "receiver-1", "object-1", "claims_review", time.Hour, ledgerBase)
	if err := l.Revoke("user-1", "object-1", "receiver-1"); err != nil {
		t.Fatalf("revoke must succeed, got %v", err)
	}
	d, err := l.CheckAt("receiver-1", "object-1", "claims_review", ledgerBase.Add(time.Minute))
	if err != nil {
		t.Fatalf("check after revoke must not fail, got %v", err)
	}
	if d.Allowed || d.Reason != impl.ReasonRevoked {
		t.Fatalf("revoked consent got allowed=%v reason=%q, want denial with %q", d.Allowed, d.Reason, impl.ReasonRevoked)
	}
	records := l.ListByObject("user-1", "object-1")
	if len(records) != 1 {
		t.Fatalf("revoke must keep the audit record, got %d entries", len(records))
	}
	if !records[0].Revoked {
		t.Fatalf("stored record must be flagged revoked")
	}
	if err := l.Revoke("user-1", "object-1", "receiver-1"); err != nil {
		t.Fatalf("revoking twice must be idempotent, got %v", err)
	}
	if err := l.Revoke("user-1", "object-1", "receiver-9"); !errors.Is(err, impl.ErrUnknownGrant) {
		t.Fatalf("revoking an unknown pair must fail with ErrUnknownGrant, got %v", err)
	}
}

func TestSweepExpiredRemovesOnlyExpiredGrants(t *testing.T) {
	l := impl.NewDataSharingConsentLedger(ledgerLimits())
	mustGrant(t, l, "user-1", "receiver-1", "object-1", "claims_review", time.Hour, ledgerBase)
	mustGrant(t, l, "user-1", "receiver-2", "object-2", "claims_review", 5*time.Hour, ledgerBase)
	removed, err := l.SweepExpired(ledgerBase.Add(2 * time.Hour))
	if err != nil {
		t.Fatalf("sweep must succeed, got %v", err)
	}
	if removed != 1 {
		t.Fatalf("sweep removed %d records, want 1", removed)
	}
	if got := len(l.ListByObject("user-1", "object-1")); got != 0 {
		t.Fatalf("expired record must be gone, got %d entries", got)
	}
	if got := len(l.ListByObject("user-1", "object-2")); got != 1 {
		t.Fatalf("live record must survive the sweep, got %d entries", got)
	}
	if d, err := l.CheckAt("receiver-1", "object-1", "claims_review", ledgerBase.Add(2*time.Hour)); err != nil || d.Allowed {
		t.Fatalf("expired consent must be denied after the sweep, got allowed=%v err=%v", d.Allowed, err)
	}
	if d, err := l.CheckAt("receiver-2", "object-2", "claims_review", ledgerBase.Add(2*time.Hour)); err != nil || !d.Allowed {
		t.Fatalf("live consent must still allow, got allowed=%v err=%v", d.Allowed, err)
	}
	again, err := l.SweepExpired(ledgerBase.Add(2 * time.Hour))
	if err != nil || again != 0 {
		t.Fatalf("second sweep removed %d records (err=%v), want 0", again, err)
	}
}

func TestSnapshotRestoreKeepsExpiryAndRevocation(t *testing.T) {
	limits := impl.Limits{MaxGrantsPerUser: 8, MaxRecipientsPerObject: 4}
	l := impl.NewDataSharingConsentLedger(limits)
	live := mustGrant(t, l, "user-1", "receiver-1", "object-1", "claims_review", 2*time.Hour, ledgerBase)
	gone := mustGrant(t, l, "user-1", "receiver-2", "object-2", "claims_review", 4*time.Hour, ledgerBase)
	if err := l.Revoke("user-1", "object-2", "receiver-2"); err != nil {
		t.Fatalf("revoke before the snapshot: %v", err)
	}
	data, err := l.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload must not be empty")
	}
	restored := impl.NewDataSharingConsentLedger(limits)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	liveRecords := restored.ListByObject("user-1", "object-1")
	if len(liveRecords) != 1 {
		t.Fatalf("restored live consents = %d, want 1", len(liveRecords))
	}
	if !liveRecords[0].ExpiresAt.Equal(live.ExpiresAt) {
		t.Fatalf("restored expiry = %v, want %v", liveRecords[0].ExpiresAt, live.ExpiresAt)
	}
	goneRecords := restored.ListByObject("user-1", "object-2")
	if len(goneRecords) != 1 || !goneRecords[0].Revoked {
		t.Fatalf("restored record must keep the revocation flag, got %+v", goneRecords)
	}
	if d, err := restored.CheckAt("receiver-1", "object-1", "claims_review", ledgerBase.Add(time.Hour)); err != nil || !d.Allowed {
		t.Fatalf("restored live consent must still allow, got allowed=%v err=%v", d.Allowed, err)
	}
	if d, err := restored.CheckAt("receiver-2", "object-2", "claims_review", ledgerBase.Add(time.Hour)); err != nil || d.Allowed {
		t.Fatalf("restored revoked consent must stay denied, got allowed=%v err=%v", d.Allowed, err)
	}
	next := mustGrant(t, restored, "user-1", "receiver-3", "object-3", "claims_review", time.Hour, ledgerBase.Add(2*time.Hour))
	if next.Seq <= gone.Seq {
		t.Fatalf("sequence after restore = %d, must continue past %d", next.Seq, gone.Seq)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	l := impl.NewDataSharingConsentLedger(ledgerLimits())
	mustGrant(t, l, "user-1", "receiver-1", "object-1", "claims_review", time.Hour, ledgerBase)
	if err := l.Restore([]byte("{not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := len(l.ListByObject("user-1", "object-1")); got != 1 {
		t.Fatalf("rejected restore must not touch live state, got %d entries", got)
	}
	if d, err := l.CheckAt("receiver-1", "object-1", "claims_review", ledgerBase); err != nil || !d.Allowed {
		t.Fatalf("live consent must keep working after a rejected restore, got allowed=%v err=%v", d.Allowed, err)
	}
}
