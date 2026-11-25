package task

import (
	"errors"
	"sync"
	"testing"
	"time"

	impl "gsb/batch49-go-safety-a-05"
)

var recoveryClock = time.Date(2026, 5, 11, 10, 0, 0, 0, time.UTC)

func recoveryLimits() impl.Limits {
	return impl.Limits{
		MaxEvidencePerCase: 3,
		MaxAttemptsPerUser: 2,
		WaitPeriod:         72 * time.Hour,
		CoolingPeriod:      48 * time.Hour,
	}
}

func verifiedEvidence(id string) impl.Evidence {
	return impl.Evidence{
		ID:        id,
		Kind:      "identity_document",
		IssuedAt:  recoveryClock.Add(-1 * time.Hour),
		ExpiresAt: recoveryClock.Add(720 * time.Hour),
		Verified:  true,
	}
}

func unverifiedEvidence(id string) impl.Evidence {
	e := verifiedEvidence(id)
	e.Verified = false
	return e
}

// casefileFixture opens one fully verified case, which lands in the waiting
// window immediately.
func casefileFixture(t *testing.T) (*impl.AccountRecoveryCasefile, impl.Case) {
	t.Helper()
	f := impl.NewAccountRecoveryCasefile(recoveryLimits())
	f.SetNow(recoveryClock)
	c, err := f.Open("user-1", impl.ChannelEmail, []impl.Evidence{verifiedEvidence("ev-1")})
	if err != nil {
		t.Fatalf("open must succeed, got %v", err)
	}
	return f, c
}

func TestOpenRejectsBlankUser(t *testing.T) {
	f := impl.NewAccountRecoveryCasefile(recoveryLimits())
	f.SetNow(recoveryClock)
	if _, err := f.Open("", impl.ChannelEmail, []impl.Evidence{verifiedEvidence("ev-1")}); !errors.Is(err, impl.ErrBlankUser) {
		t.Fatalf("open without a user = %v, want ErrBlankUser", err)
	}
	if _, err := f.Open("   ", impl.ChannelSMS, []impl.Evidence{verifiedEvidence("ev-1")}); !errors.Is(err, impl.ErrBlankUser) {
		t.Fatalf("open with a whitespace user = %v, want ErrBlankUser", err)
	}
}

func TestOpenRejectsBadChannel(t *testing.T) {
	f := impl.NewAccountRecoveryCasefile(recoveryLimits())
	f.SetNow(recoveryClock)
	if _, err := f.Open("user-1", impl.Channel("carrier_pigeon"), []impl.Evidence{verifiedEvidence("ev-1")}); !errors.Is(err, impl.ErrBadChannel) {
		t.Fatalf("open on an unknown channel = %v, want ErrBadChannel", err)
	}
	if _, err := f.Open("user-1", impl.Channel(""), []impl.Evidence{verifiedEvidence("ev-1")}); !errors.Is(err, impl.ErrBadChannel) {
		t.Fatalf("open on an empty channel = %v, want ErrBadChannel", err)
	}
}

func TestOpenRejectsEmptyEvidence(t *testing.T) {
	f := impl.NewAccountRecoveryCasefile(recoveryLimits())
	f.SetNow(recoveryClock)
	if _, err := f.Open("user-1", impl.ChannelEmail, nil); !errors.Is(err, impl.ErrNoEvidence) {
		t.Fatalf("open without evidence = %v, want ErrNoEvidence", err)
	}
	if _, err := f.Open("user-1", impl.ChannelEmail, []impl.Evidence{{ID: "  ", Verified: true}}); !errors.Is(err, impl.ErrBadEvidence) {
		t.Fatalf("open with a blank evidence id = %v, want ErrBadEvidence", err)
	}
}

func TestOpenRejectsExpiredEvidence(t *testing.T) {
	f := impl.NewAccountRecoveryCasefile(recoveryLimits())
	f.SetNow(recoveryClock)
	expired := verifiedEvidence("ev-old")
	expired.ExpiresAt = recoveryClock.Add(-1 * time.Minute)
	if _, err := f.Open("user-1", impl.ChannelEmail, []impl.Evidence{expired}); !errors.Is(err, impl.ErrEvidenceExpired) {
		t.Fatalf("open with expired evidence = %v, want ErrEvidenceExpired", err)
	}
}

func TestOpenWithUnverifiedEvidenceStaysPendingThenSubmit(t *testing.T) {
	f := impl.NewAccountRecoveryCasefile(recoveryLimits())
	f.SetNow(recoveryClock)
	c, err := f.Open("user-1", impl.ChannelSupport, []impl.Evidence{unverifiedEvidence("ev-1")})
	if err != nil {
		t.Fatalf("open must succeed, got %v", err)
	}
	if c.State != impl.CaseEvidencePending {
		t.Fatalf("unverified case state = %q, want %q", c.State, impl.CaseEvidencePending)
	}
	if !c.WaitUntil.IsZero() {
		t.Fatalf("pending case must not start the waiting window, got %v", c.WaitUntil)
	}
	if err := f.Submit(c.ID, verifiedEvidence("ev-2")); err != nil {
		t.Fatalf("submit must succeed, got %v", err)
	}
	stored, err := f.Get(c.ID)
	if err != nil {
		t.Fatalf("get must succeed, got %v", err)
	}
	if stored.State != impl.CaseWaiting {
		t.Fatalf("state after full verification = %q, want %q", stored.State, impl.CaseWaiting)
	}
	want := recoveryClock.Add(72 * time.Hour)
	if !stored.WaitUntil.Equal(want) {
		t.Fatalf("wait deadline = %v, want %v", stored.WaitUntil, want)
	}
	if len(stored.Evidence) != 2 {
		t.Fatalf("evidence count = %d, want 2", len(stored.Evidence))
	}
	if stored.Seq <= 0 {
		t.Fatalf("case sequence = %d, want a positive number", stored.Seq)
	}
	if err := f.Submit(c.ID, verifiedEvidence("ev-3")); !errors.Is(err, impl.ErrCaseState) {
		t.Fatalf("submit once the case moved on = %v, want ErrCaseState", err)
	}
}

func TestEvidenceCapPerCase(t *testing.T) {
	f := impl.NewAccountRecoveryCasefile(recoveryLimits())
	f.SetNow(recoveryClock)
	over := []impl.Evidence{
		unverifiedEvidence("ev-1"),
		unverifiedEvidence("ev-2"),
		unverifiedEvidence("ev-3"),
		unverifiedEvidence("ev-4"),
	}
	if _, err := f.Open("user-1", impl.ChannelEmail, over); !errors.Is(err, impl.ErrEvidenceLimit) {
		t.Fatalf("open with four evidence items and a cap of three = %v, want ErrEvidenceLimit", err)
	}
	c, err := f.Open("user-1", impl.ChannelEmail, over[:3])
	if err != nil {
		t.Fatalf("open at the cap must succeed, got %v", err)
	}
	if err := f.Submit(c.ID, verifiedEvidence("ev-5")); !errors.Is(err, impl.ErrEvidenceLimit) {
		t.Fatalf("submit past the cap = %v, want ErrEvidenceLimit", err)
	}
}

func TestAdvanceReleasesWaitingCase(t *testing.T) {
	f, c := casefileFixture(t)
	early, err := f.Advance(recoveryClock.Add(71 * time.Hour))
	if err != nil {
		t.Fatalf("early advance must succeed, got %v", err)
	}
	if len(early) != 0 {
		t.Fatalf("early advance released %v, want none", early)
	}
	released, err := f.Advance(recoveryClock.Add(73 * time.Hour))
	if err != nil {
		t.Fatalf("late advance must succeed, got %v", err)
	}
	if len(released) != 1 || released[0] != c.ID {
		t.Fatalf("late advance released %v, want just %q", released, c.ID)
	}
	stored, err := f.Get(c.ID)
	if err != nil {
		t.Fatalf("get must succeed, got %v", err)
	}
	if stored.State != impl.CaseReviewPending {
		t.Fatalf("state after the wait = %q, want %q", stored.State, impl.CaseReviewPending)
	}
	again, err := f.Advance(recoveryClock.Add(74 * time.Hour))
	if err != nil {
		t.Fatalf("second advance must succeed, got %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("second advance released %v, want none", again)
	}
}

func TestApproveRequiresReviewState(t *testing.T) {
	f, c := casefileFixture(t)
	if _, err := f.Approve(c.ID, "reviewer-7"); !errors.Is(err, impl.ErrCaseState) {
		t.Fatalf("approve inside the waiting window = %v, want ErrCaseState", err)
	}
	stored, err := f.Get(c.ID)
	if err != nil {
		t.Fatalf("get must succeed, got %v", err)
	}
	if stored.State != impl.CaseWaiting {
		t.Fatalf("rejected approve changed the state to %q", stored.State)
	}
}

func TestApproveRejectsBlankReviewer(t *testing.T) {
	f, c := casefileFixture(t)
	if _, err := f.Advance(recoveryClock.Add(73 * time.Hour)); err != nil {
		t.Fatalf("advance must succeed, got %v", err)
	}
	if _, err := f.Approve(c.ID, "   "); !errors.Is(err, impl.ErrBadActor) {
		t.Fatalf("approve without a reviewer = %v, want ErrBadActor", err)
	}
	if _, err := f.Approve("case-missing", "reviewer-7"); !errors.Is(err, impl.ErrCaseNotFound) {
		t.Fatalf("approve of an unknown case = %v, want ErrCaseNotFound", err)
	}
}

func TestApproveIssuesCoolingGrant(t *testing.T) {
	f, c := casefileFixture(t)
	reviewAt := recoveryClock.Add(73 * time.Hour)
	if _, err := f.Advance(reviewAt); err != nil {
		t.Fatalf("advance must succeed, got %v", err)
	}
	g, err := f.Approve(c.ID, "reviewer-7")
	if err != nil {
		t.Fatalf("approve must succeed, got %v", err)
	}
	if g.ID == "" || g.CaseID != c.ID || g.UserID != "user-1" {
		t.Fatalf("grant = %+v, want it bound to case %q and user-1", g, c.ID)
	}
	if g.IssuedAt.IsZero() {
		t.Fatalf("grant must carry its issue time, got zero")
	}
	if g.CoolingUntil.Sub(g.IssuedAt) != 48*time.Hour {
		t.Fatalf("cooling window = %v, want 48h after issue", g.CoolingUntil.Sub(g.IssuedAt))
	}
	stored, err := f.Get(c.ID)
	if err != nil {
		t.Fatalf("get must succeed, got %v", err)
	}
	if stored.State != impl.CaseApproved {
		t.Fatalf("state after approval = %q, want %q", stored.State, impl.CaseApproved)
	}
	read, ok := f.Grant(g.ID)
	if !ok {
		t.Fatalf("grant %q must be readable", g.ID)
	}
	if read.CaseID != c.ID || read.UserID != "user-1" {
		t.Fatalf("stored grant = %+v", read)
	}
	allowed, err := f.Permitted(g.ID, impl.OpChangePassword)
	if err != nil {
		t.Fatalf("permitted must succeed, got %v", err)
	}
	if !allowed {
		t.Fatalf("password change must be allowed inside the cooling window")
	}
	allowed, err = f.Permitted(g.ID, impl.OpReEnrollFactor)
	if err != nil {
		t.Fatalf("permitted must succeed, got %v", err)
	}
	if !allowed {
		t.Fatalf("factor re-enrolment must be allowed inside the cooling window")
	}
}

func TestPermittedAfterCoolingAllowsEmailChange(t *testing.T) {
	f, c := casefileFixture(t)
	if _, err := f.Advance(recoveryClock.Add(73 * time.Hour)); err != nil {
		t.Fatalf("advance must succeed, got %v", err)
	}
	g, err := f.Approve(c.ID, "reviewer-7")
	if err != nil {
		t.Fatalf("approve must succeed, got %v", err)
	}
	f.SetNow(recoveryClock.Add(73*time.Hour + 49*time.Hour))
	allowed, err := f.Permitted(g.ID, impl.OpChangePrimaryEmail)
	if err != nil {
		t.Fatalf("permitted must succeed, got %v", err)
	}
	if !allowed {
		t.Fatalf("primary email change must unlock once the cooling period is over")
	}
	if _, err := f.Permitted("grant-missing", impl.OpChangePassword); !errors.Is(err, impl.ErrUnknownGrant) {
		t.Fatalf("permitted on an unknown grant = %v, want ErrUnknownGrant", err)
	}
	if _, ok := f.Grant("grant-missing"); ok {
		t.Fatalf("unknown grant must not resolve")
	}
}

func TestCancelBlocksApprove(t *testing.T) {
	f, c := casefileFixture(t)
	if err := f.Contest("case-missing", "owner-1", "never asked"); !errors.Is(err, impl.ErrCaseNotFound) {
		t.Fatalf("contest of an unknown case = %v, want ErrCaseNotFound", err)
	}
	if err := f.Contest(c.ID, "  ", "never asked"); !errors.Is(err, impl.ErrBadActor) {
		t.Fatalf("contest without an actor = %v, want ErrBadActor", err)
	}
	if err := f.Cancel(c.ID); err != nil {
		t.Fatalf("cancel must succeed, got %v", err)
	}
	stored, err := f.Get(c.ID)
	if err != nil {
		t.Fatalf("get must succeed, got %v", err)
	}
	if stored.State != impl.CaseCancelled {
		t.Fatalf("state after cancel = %q, want %q", stored.State, impl.CaseCancelled)
	}
	if _, err := f.Advance(recoveryClock.Add(73 * time.Hour)); err != nil {
		t.Fatalf("advance must succeed, got %v", err)
	}
	if _, err := f.Approve(c.ID, "reviewer-7"); !errors.Is(err, impl.ErrCaseState) {
		t.Fatalf("approve after cancel = %v, want ErrCaseState", err)
	}
	if err := f.Cancel(c.ID); !errors.Is(err, impl.ErrCaseState) {
		t.Fatalf("second cancel = %v, want ErrCaseState", err)
	}
}

func TestAttemptsCapPerUser(t *testing.T) {
	f := impl.NewAccountRecoveryCasefile(recoveryLimits())
	f.SetNow(recoveryClock)
	for i := 0; i < 2; i++ {
		c, err := f.Open("user-1", impl.ChannelEmail, []impl.Evidence{verifiedEvidence("ev-1")})
		if err != nil {
			t.Fatalf("open %d must succeed, got %v", i, err)
		}
		if err := f.Cancel(c.ID); err != nil {
			t.Fatalf("cancel %d must succeed, got %v", i, err)
		}
	}
	if _, err := f.Open("user-1", impl.ChannelEmail, []impl.Evidence{verifiedEvidence("ev-9")}); !errors.Is(err, impl.ErrAttemptsExceeded) {
		t.Fatalf("third attempt with a cap of two = %v, want ErrAttemptsExceeded", err)
	}
	if _, err := f.Open("user-2", impl.ChannelSMS, []impl.Evidence{verifiedEvidence("ev-9")}); err != nil {
		t.Fatalf("another user must still be able to open, got %v", err)
	}
}

func TestConcurrentApproveIssuesSingleGrant(t *testing.T) {
	f, c := casefileFixture(t)
	if _, err := f.Advance(recoveryClock.Add(73 * time.Hour)); err != nil {
		t.Fatalf("advance must succeed, got %v", err)
	}
	const workers = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := 0
	rejected := 0
	other := 0
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			defer wg.Done()
			_, err := f.Approve(c.ID, "reviewer-7")
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				winners++
			case errors.Is(err, impl.ErrCaseState):
				rejected++
			default:
				other++
			}
		}(i)
	}
	wg.Wait()
	if winners != 1 || rejected != workers-1 || other != 0 {
		t.Fatalf("concurrent approve winners=%d rejected=%d other=%d, want 1/%d/0", winners, rejected, other, workers-1)
	}
	stored, err := f.Get(c.ID)
	if err != nil {
		t.Fatalf("get must succeed, got %v", err)
	}
	if stored.State != impl.CaseApproved {
		t.Fatalf("state after the race = %q, want %q", stored.State, impl.CaseApproved)
	}
}

func TestSnapshotRestoreKeepsWaitAndRejectsCorrupt(t *testing.T) {
	f, c := casefileFixture(t)
	data, err := f.Snapshot()
	if err != nil {
		t.Fatalf("snapshot must succeed, got %v", err)
	}
	restored := impl.NewAccountRecoveryCasefile(recoveryLimits())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore must succeed, got %v", err)
	}
	early, err := restored.Advance(recoveryClock.Add(71 * time.Hour))
	if err != nil {
		t.Fatalf("early advance must succeed, got %v", err)
	}
	if len(early) != 0 {
		t.Fatalf("restored case left the wall too early: %v", early)
	}
	released, err := restored.Advance(recoveryClock.Add(73 * time.Hour))
	if err != nil {
		t.Fatalf("late advance must succeed, got %v", err)
	}
	if len(released) != 1 || released[0] != c.ID {
		t.Fatalf("restored advance released %v, want %q", released, c.ID)
	}
	if err := restored.Restore([]byte("[] not a case file")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt restore = %v, want ErrBadSnapshot", err)
	}
	stored, err := restored.Get(c.ID)
	if err != nil {
		t.Fatalf("rejected restore must keep the live case, got %v", err)
	}
	if stored.State != impl.CaseReviewPending {
		t.Fatalf("live state after a rejected restore = %q, want %q", stored.State, impl.CaseReviewPending)
	}
}
