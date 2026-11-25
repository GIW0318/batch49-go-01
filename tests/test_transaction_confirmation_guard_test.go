package task

import (
	"errors"
	"sync"
	"testing"
	"time"

	impl "gsb/batch49-go-safety-a-08"
)

type guardClock struct {
	mu sync.Mutex
	at time.Time
}

func newGuardClock() *guardClock {
	return &guardClock{at: time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)}
}

func (c *guardClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *guardClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

func guardOptions(clock *guardClock) impl.Options {
	return impl.Options{
		SmallLimitCents:      1000,
		LargeLimitCents:      100000,
		ChallengeTTL:         10 * time.Minute,
		TicketTTL:            5 * time.Minute,
		MaxPendingChallenges: 3,
		MaxConfirmAttempts:   3,
		Now:                  clock.Now,
	}
}

func transfer(user string, cents int64) impl.Transaction {
	return impl.Transaction{
		UserID:       user,
		AmountCents:  cents,
		Currency:     "CNY",
		Payee:        "Shanghai Supplier Ltd",
		PayeeAccount: "6222-0000-1111-2222",
		Channel:      "mobile-banking",
	}
}

func goodProof(id string) impl.Proof {
	return impl.Proof{Signature: "sig-" + id, DeviceID: "device-" + id}
}

func TestPrepareBuildsChallengeTierAndDigest(t *testing.T) {
	clock := newGuardClock()
	g := impl.NewTransactionConfirmationGuard(guardOptions(clock))
	ch, err := g.Prepare("op-1", transfer("user-1", 5000))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if ch.ID == "" || ch.OpID != "op-1" || ch.UserID != "user-1" {
		t.Fatalf("challenge identity = %+v, want op-1 under user-1", ch)
	}
	if ch.Tier != impl.TierSingle || ch.Required != 1 {
		t.Fatalf("tier=%q required=%d, want single tier with one principal", ch.Tier, ch.Required)
	}
	if ch.State != impl.ChallengeOpen {
		t.Fatalf("challenge state = %q, want %q", ch.State, impl.ChallengeOpen)
	}
	if len(ch.Digest) != 64 {
		t.Fatalf("digest = %q, want a 64 character hex digest", ch.Digest)
	}
	if ch.TicketID != "" {
		t.Fatalf("single tier must not issue a ticket before confirmation, got %q", ch.TicketID)
	}
	want := clock.Now().Add(guardOptions(clock).ChallengeTTL)
	if !ch.ExpiresAt.Equal(want) {
		t.Fatalf("challenge expiry = %v, want %v", ch.ExpiresAt, want)
	}
	if ch.Seq <= 0 {
		t.Fatalf("challenge sequence = %d, want a positive number", ch.Seq)
	}
	pending := g.Pending("user-1")
	if len(pending) != 1 || pending[0].ID != ch.ID {
		t.Fatalf("pending = %+v, want exactly the new challenge", pending)
	}
	if _, ok := g.LookupChallenge(ch.ID); !ok {
		t.Fatalf("challenge must be readable through LookupChallenge")
	}
}

func TestPrepareTierBoundaries(t *testing.T) {
	clock := newGuardClock()
	g := impl.NewTransactionConfirmationGuard(guardOptions(clock))
	exempt, err := g.Prepare("op-small", transfer("user-small", 999))
	if err != nil {
		t.Fatalf("prepare small amount: %v", err)
	}
	if exempt.Tier != impl.TierExempt || exempt.Required != 0 || exempt.TicketID == "" {
		t.Fatalf("small amount = %+v, want exempt tier with a ready ticket", exempt)
	}
	single, err := g.Prepare("op-mid", transfer("user-mid", 1000))
	if err != nil {
		t.Fatalf("prepare mid amount: %v", err)
	}
	if single.Tier != impl.TierSingle || single.Required != 1 {
		t.Fatalf("mid amount = %+v, want single tier", single)
	}
	stillSingle, err := g.Prepare("op-high", transfer("user-high", 99999))
	if err != nil {
		t.Fatalf("prepare high amount: %v", err)
	}
	if stillSingle.Tier != impl.TierSingle {
		t.Fatalf("99999 cents tier = %q, want single", stillSingle.Tier)
	}
	dual, err := g.Prepare("op-dual", transfer("user-dual", 100000))
	if err != nil {
		t.Fatalf("prepare dual amount: %v", err)
	}
	if dual.Tier != impl.TierDual || dual.Required != 2 {
		t.Fatalf("large amount = %+v, want dual tier with two principals", dual)
	}
}

func TestPrepareRejectsBadInput(t *testing.T) {
	clock := newGuardClock()
	g := impl.NewTransactionConfirmationGuard(guardOptions(clock))
	if _, err := g.Prepare("   ", transfer("user-1", 5000)); !errors.Is(err, impl.ErrBlankID) {
		t.Fatalf("blank operation id must fail with ErrBlankID, got %v", err)
	}
	if _, err := g.Prepare("op-1", transfer("  ", 5000)); !errors.Is(err, impl.ErrBlankID) {
		t.Fatalf("blank user id must fail with ErrBlankID, got %v", err)
	}
	if _, err := g.Prepare("op-2", transfer("user-1", 0)); !errors.Is(err, impl.ErrBadAmount) {
		t.Fatalf("zero amount must fail with ErrBadAmount, got %v", err)
	}
	noPayee := transfer("user-1", 5000)
	noPayee.Payee = " "
	if _, err := g.Prepare("op-3", noPayee); !errors.Is(err, impl.ErrBadTransaction) {
		t.Fatalf("blank payee must fail with ErrBadTransaction, got %v", err)
	}
	noChannel := transfer("user-1", 5000)
	noChannel.Channel = ""
	if _, err := g.Prepare("op-4", noChannel); !errors.Is(err, impl.ErrBadTransaction) {
		t.Fatalf("blank channel must fail with ErrBadTransaction, got %v", err)
	}
	if got := len(g.Pending("user-1")); got != 0 {
		t.Fatalf("rejected preparations must not leave pending challenges, got %d", got)
	}
}

func TestPrepareSameOpReplacesChallenge(t *testing.T) {
	clock := newGuardClock()
	g := impl.NewTransactionConfirmationGuard(guardOptions(clock))
	first, err := g.Prepare("op-1", transfer("user-1", 5000))
	if err != nil {
		t.Fatalf("first prepare: %v", err)
	}
	second, err := g.Prepare("op-1", transfer("user-1", 7000))
	if err != nil {
		t.Fatalf("second prepare: %v", err)
	}
	if first.Digest == second.Digest {
		t.Fatalf("changed amount must change the digest, both were %q", first.Digest)
	}
	pending := g.Pending("user-1")
	if len(pending) != 1 || pending[0].ID != second.ID {
		t.Fatalf("pending = %+v, want only the newest challenge", pending)
	}
	if _, err := g.Confirm(first.ID, "principal-1", goodProof("1")); !errors.Is(err, impl.ErrChallengeStale) {
		t.Fatalf("superseded challenge must fail with ErrChallengeStale, got %v", err)
	}
}

func TestPrepareEnforcesPendingCap(t *testing.T) {
	clock := newGuardClock()
	opts := guardOptions(clock)
	opts.MaxPendingChallenges = 2
	g := impl.NewTransactionConfirmationGuard(opts)
	first, err := g.Prepare("op-1", transfer("user-1", 5000))
	if err != nil {
		t.Fatalf("prepare op-1: %v", err)
	}
	if _, err := g.Prepare("op-2", transfer("user-1", 5000)); err != nil {
		t.Fatalf("prepare op-2: %v", err)
	}
	if _, err := g.Prepare("op-3", transfer("user-1", 5000)); !errors.Is(err, impl.ErrChallengeLimit) {
		t.Fatalf("third pending challenge must fail with ErrChallengeLimit, got %v", err)
	}
	if err := g.Cancel(first.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := g.Prepare("op-3", transfer("user-1", 5000)); err != nil {
		t.Fatalf("preparing after a cancel must succeed, got %v", err)
	}
	if got := len(g.Pending("user-1")); got != 2 {
		t.Fatalf("pending count = %d, want the cap 2", got)
	}
}

func TestConfirmSinglePrincipalIssuesTicket(t *testing.T) {
	clock := newGuardClock()
	g := impl.NewTransactionConfirmationGuard(guardOptions(clock))
	ch, err := g.Prepare("op-1", transfer("user-1", 5000))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	ticket, err := g.Confirm(ch.ID, "principal-1", goodProof("1"))
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if ticket.ID == "" || ticket.OpID != "op-1" || ticket.Digest != ch.Digest {
		t.Fatalf("ticket = %+v, want it bound to op-1 and the challenge digest", ticket)
	}
	if ticket.Channel != "mobile-banking" {
		t.Fatalf("ticket channel = %q, want the prepared channel", ticket.Channel)
	}
	if ticket.State != impl.TicketReady {
		t.Fatalf("ticket state = %q, want %q", ticket.State, impl.TicketReady)
	}
	if ticket.Seq <= 0 {
		t.Fatalf("ticket sequence = %d, want a positive number", ticket.Seq)
	}
	if _, ok := g.LookupTicket(ticket.ID); !ok {
		t.Fatalf("ticket must be readable through LookupTicket")
	}
	closed, ok := g.LookupChallenge(ch.ID)
	if !ok || closed.State != impl.ChallengeClosed || closed.TicketID != ticket.ID {
		t.Fatalf("challenge after confirmation = %+v ok=%v, want closed with the ticket id", closed, ok)
	}
	if len(g.Pending("user-1")) != 0 {
		t.Fatalf("confirmed challenge must leave the pending list")
	}
	if _, err := g.Confirm(ch.ID, "principal-2", goodProof("2")); !errors.Is(err, impl.ErrChallengeClosed) {
		t.Fatalf("confirming a closed challenge must fail with ErrChallengeClosed, got %v", err)
	}
}

func TestConfirmDualRequiresTwoDistinctPrincipals(t *testing.T) {
	clock := newGuardClock()
	g := impl.NewTransactionConfirmationGuard(guardOptions(clock))
	ch, err := g.Prepare("op-1", transfer("user-1", 250000))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if ch.Tier != impl.TierDual || ch.Required != 2 {
		t.Fatalf("challenge = %+v, want the dual tier", ch)
	}
	if ticket, err := g.Confirm(ch.ID, "principal-1", goodProof("1")); !errors.Is(err, impl.ErrNotConfirmed) {
		t.Fatalf("first principal must not complete a dual challenge, got ticket %+v err %v", ticket, err)
	}
	ticket, err := g.Confirm(ch.ID, "principal-2", goodProof("2"))
	if err != nil {
		t.Fatalf("second principal confirm: %v", err)
	}
	if ticket.ID == "" {
		t.Fatalf("dual challenge must issue a ticket after two principals")
	}
	got, ok := g.LookupChallenge(ch.ID)
	if !ok || got.Confirmed != 2 || len(got.Principals) != 2 {
		t.Fatalf("dual challenge record = %+v ok=%v, want two recorded principals", got, ok)
	}
	if got.Principals[0] != "principal-1" || got.Principals[1] != "principal-2" {
		t.Fatalf("principals = %v, want them recorded in submission order", got.Principals)
	}
}

func TestConfirmRejectsBadProofAndUnknownChallenge(t *testing.T) {
	clock := newGuardClock()
	g := impl.NewTransactionConfirmationGuard(guardOptions(clock))
	ch, err := g.Prepare("op-1", transfer("user-1", 5000))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, err := g.Confirm(ch.ID, "principal-1", impl.Proof{DeviceID: "device-1"}); !errors.Is(err, impl.ErrBadProof) {
		t.Fatalf("missing signature must fail with ErrBadProof, got %v", err)
	}
	if _, err := g.Confirm(ch.ID, "principal-1", impl.Proof{Signature: "sig-1"}); !errors.Is(err, impl.ErrBadProof) {
		t.Fatalf("missing device must fail with ErrBadProof, got %v", err)
	}
	if _, err := g.Confirm("ch-404", "principal-1", goodProof("1")); !errors.Is(err, impl.ErrChallengeNotFound) {
		t.Fatalf("unknown challenge must fail with ErrChallengeNotFound, got %v", err)
	}
	if _, err := g.Confirm(ch.ID, "   ", goodProof("1")); !errors.Is(err, impl.ErrBlankID) {
		t.Fatalf("blank principal must fail with ErrBlankID, got %v", err)
	}
	got, _ := g.LookupChallenge(ch.ID)
	if got.Attempts != 2 {
		t.Fatalf("attempt counter = %d, want the two bad proofs recorded", got.Attempts)
	}
}

func TestConfirmEnforcesAttemptLimit(t *testing.T) {
	clock := newGuardClock()
	opts := guardOptions(clock)
	opts.MaxConfirmAttempts = 2
	g := impl.NewTransactionConfirmationGuard(opts)
	ch, err := g.Prepare("op-1", transfer("user-1", 5000))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := g.Confirm(ch.ID, "principal-1", impl.Proof{Signature: "", DeviceID: "device-1"}); !errors.Is(err, impl.ErrBadProof) {
			t.Fatalf("bad proof %d must fail with ErrBadProof, got %v", i, err)
		}
	}
	if _, err := g.Confirm(ch.ID, "principal-1", goodProof("1")); !errors.Is(err, impl.ErrAttemptLimit) {
		t.Fatalf("confirmation after the attempt cap must fail with ErrAttemptLimit, got %v", err)
	}
	got, _ := g.LookupChallenge(ch.ID)
	if got.TicketID != "" {
		t.Fatalf("exhausted challenge must not carry a ticket, got %+v", got)
	}
}

func TestConfirmRejectsExpiredChallenge(t *testing.T) {
	clock := newGuardClock()
	g := impl.NewTransactionConfirmationGuard(guardOptions(clock))
	ch, err := g.Prepare("op-1", transfer("user-1", 5000))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	clock.Advance(11 * time.Minute)
	if _, err := g.Confirm(ch.ID, "principal-1", goodProof("1")); !errors.Is(err, impl.ErrChallengeExpired) {
		t.Fatalf("expired challenge must fail with ErrChallengeExpired, got %v", err)
	}
	if _, ok := g.LookupTicket(ch.TicketID); ok {
		t.Fatalf("expired challenge must not issue a ticket")
	}
}

func TestCancelClosesChallenge(t *testing.T) {
	clock := newGuardClock()
	g := impl.NewTransactionConfirmationGuard(guardOptions(clock))
	ch, err := g.Prepare("op-1", transfer("user-1", 5000))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if err := g.Cancel(ch.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if err := g.Cancel(ch.ID); !errors.Is(err, impl.ErrChallengeClosed) {
		t.Fatalf("second cancel must fail with ErrChallengeClosed, got %v", err)
	}
	if err := g.Cancel("ch-404"); !errors.Is(err, impl.ErrChallengeNotFound) {
		t.Fatalf("cancel of an unknown challenge must fail with ErrChallengeNotFound, got %v", err)
	}
	if _, err := g.Confirm(ch.ID, "principal-1", goodProof("1")); !errors.Is(err, impl.ErrChallengeClosed) {
		t.Fatalf("confirm after cancel must fail with ErrChallengeClosed, got %v", err)
	}
	if len(g.Pending("user-1")) != 0 {
		t.Fatalf("cancelled challenge must leave the pending list")
	}
}

func TestRedeemTicketOnce(t *testing.T) {
	clock := newGuardClock()
	g := impl.NewTransactionConfirmationGuard(guardOptions(clock))
	ch, err := g.Prepare("op-1", transfer("user-1", 5000))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	ticket, err := g.Confirm(ch.ID, "principal-1", goodProof("1"))
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	decision, err := g.Redeem(ticket.ID, "op-1")
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if !decision.Approved || decision.OpID != "op-1" || decision.TicketID != ticket.ID {
		t.Fatalf("decision = %+v, want an approval for op-1 with the ticket id", decision)
	}
	if decision.Reason != impl.ReasonConfirmed {
		t.Fatalf("decision reason = %q, want %q", decision.Reason, impl.ReasonConfirmed)
	}
	if _, err := g.Redeem(ticket.ID, "op-1"); !errors.Is(err, impl.ErrTicketUsed) {
		t.Fatalf("second redemption must fail with ErrTicketUsed, got %v", err)
	}
	if got, ok := g.LookupTicket(ticket.ID); !ok || got.State != impl.TicketUsed {
		t.Fatalf("ticket after redemption = %+v ok=%v, want state used", got, ok)
	}
	other, err := g.Prepare("op-2", transfer("user-1", 5000))
	if err != nil {
		t.Fatalf("prepare op-2: %v", err)
	}
	second, err := g.Confirm(other.ID, "principal-2", goodProof("2"))
	if err != nil {
		t.Fatalf("confirm op-2: %v", err)
	}
	if _, err := g.Redeem(second.ID, "op-9"); !errors.Is(err, impl.ErrOpMismatch) {
		t.Fatalf("redeem on another operation must fail with ErrOpMismatch, got %v", err)
	}
	if _, err := g.Redeem(second.ID, "op-2"); err != nil {
		t.Fatalf("mismatched redemption must not consume the ticket, got %v", err)
	}
}

func TestRedeemRejectsExpiredTicket(t *testing.T) {
	clock := newGuardClock()
	g := impl.NewTransactionConfirmationGuard(guardOptions(clock))
	ch, err := g.Prepare("op-1", transfer("user-1", 5000))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	ticket, err := g.Confirm(ch.ID, "principal-1", goodProof("1"))
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	clock.Advance(6 * time.Minute)
	if _, err := g.Redeem(ticket.ID, "op-1"); !errors.Is(err, impl.ErrTicketExpired) {
		t.Fatalf("expired ticket must fail with ErrTicketExpired, got %v", err)
	}
}

func TestSnapshotRestoreKeepsChallengesAndTickets(t *testing.T) {
	clock := newGuardClock()
	opts := guardOptions(clock)
	g := impl.NewTransactionConfirmationGuard(opts)
	dual, err := g.Prepare("op-dual", transfer("user-1", 250000))
	if err != nil {
		t.Fatalf("prepare dual: %v", err)
	}
	if _, err := g.Confirm(dual.ID, "principal-1", goodProof("1")); !errors.Is(err, impl.ErrNotConfirmed) {
		t.Fatalf("first principal on dual: %v", err)
	}
	single, err := g.Prepare("op-single", transfer("user-1", 5000))
	if err != nil {
		t.Fatalf("prepare single: %v", err)
	}
	used, err := g.Confirm(single.ID, "principal-1", goodProof("1"))
	if err != nil {
		t.Fatalf("confirm single: %v", err)
	}
	if _, err := g.Redeem(used.ID, "op-single"); err != nil {
		t.Fatalf("redeem single: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	if err := g.Restore([]byte("not a snapshot at all")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if _, ok := g.LookupChallenge(dual.ID); !ok {
		t.Fatalf("rejected restore must leave the live challenge table intact")
	}
	restored := impl.NewTransactionConfirmationGuard(opts)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	got, ok := restored.LookupChallenge(dual.ID)
	if !ok || got.Confirmed != 1 || got.Required != 2 || got.State != impl.ChallengeOpen {
		t.Fatalf("restored challenge = %+v ok=%v, want still open with one principal", got, ok)
	}
	if !got.ExpiresAt.Equal(dual.ExpiresAt) {
		t.Fatalf("restored expiry = %v, want the original %v", got.ExpiresAt, dual.ExpiresAt)
	}
	ticket, err := restored.Confirm(dual.ID, "principal-2", goodProof("2"))
	if err != nil {
		t.Fatalf("confirm on the restored challenge: %v", err)
	}
	if _, err := restored.Redeem(ticket.ID, "op-dual"); err != nil {
		t.Fatalf("redeem the ticket issued after restore: %v", err)
	}
	if _, err := restored.Redeem(used.ID, "op-single"); !errors.Is(err, impl.ErrTicketUsed) {
		t.Fatalf("the redeemed ticket must stay used after restore, got %v", err)
	}
	if got := len(restored.Pending("user-1")); got != 0 {
		t.Fatalf("pending after restore and confirm = %d, want 0", got)
	}
}

func TestConcurrentDualConfirmIssuesOneTicket(t *testing.T) {
	clock := newGuardClock()
	g := impl.NewTransactionConfirmationGuard(guardOptions(clock))
	ch, err := g.Prepare("op-1", transfer("user-1", 250000))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	issued, closed := 0, 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := g.Confirm(ch.ID, "principal-"+string(rune('a'+i)), goodProof(string(rune('a'+i))))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				issued++
			case errors.Is(err, impl.ErrChallengeClosed), errors.Is(err, impl.ErrNotConfirmed):
				closed++
			}
		}(i)
	}
	wg.Wait()
	if issued != 1 {
		t.Fatalf("concurrent confirms issued %d tickets, want exactly 1", issued)
	}
	if closed != 7 {
		t.Fatalf("concurrent confirms rejected %d submissions, want 7", closed)
	}
	got, _ := g.LookupChallenge(ch.ID)
	if got.TicketID == "" || got.Confirmed != 2 {
		t.Fatalf("final challenge = %+v, want a ticket and exactly 2 counted principals", got)
	}
	if _, err := g.Redeem(got.TicketID, "op-1"); err != nil {
		t.Fatalf("redeem the single issued ticket: %v", err)
	}
}
