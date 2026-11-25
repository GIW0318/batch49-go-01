package task

import (
	"errors"
	"sync"
	"testing"
	"time"

	impl "gsb/batch49-go-safety-a-09"
)

type ladderClock struct {
	mu sync.Mutex
	at time.Time
}

func newLadderClock() *ladderClock {
	return &ladderClock{at: time.Date(2026, 11, 22, 9, 30, 0, 0, time.UTC)}
}

func (c *ladderClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *ladderClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

func ladderOptions(clock *ladderClock) impl.Options {
	return impl.Options{
		MaxChallengesPerUser: 3,
		MaxAttemptsPerMethod: 3,
		ChallengeTTL:         10 * time.Minute,
		ResultTTL:            30 * time.Minute,
		Now:                  clock.Now,
	}
}

func codeProof() impl.Proof {
	return impl.Proof{Code: "418902", DeviceID: "device-1"}
}

func TestEnrollAndAvailableOrder(t *testing.T) {
	clock := newLadderClock()
	l := impl.NewStepUpMethodLadder(ladderOptions(clock))
	for _, m := range []string{impl.MethodTOTP, impl.MethodSMS, impl.MethodSecurityKey} {
		if err := l.Enroll("user-1", m); err != nil {
			t.Fatalf("enroll %s: %v", m, err)
		}
	}
	available := l.Available("user-1")
	if len(available) != 3 {
		t.Fatalf("available count = %d, want 3", len(available))
	}
	want := []impl.Method{
		{Kind: impl.MethodSecurityKey, Strength: impl.StrengthSecurityKey},
		{Kind: impl.MethodTOTP, Strength: impl.StrengthTOTP},
		{Kind: impl.MethodSMS, Strength: impl.StrengthSMS},
	}
	for i, m := range want {
		if available[i].Kind != m.Kind || available[i].Strength != m.Strength {
			t.Fatalf("available[%d] = %+v, want %+v ordered strongest first", i, available[i], m)
		}
	}
	if err := l.Enroll("user-1", impl.MethodTOTP); !errors.Is(err, impl.ErrMethodEnrolled) {
		t.Fatalf("duplicate enrol must fail with ErrMethodEnrolled, got %v", err)
	}
	if err := l.Enroll("user-1", "carrier_pigeon"); !errors.Is(err, impl.ErrUnknownMethod) {
		t.Fatalf("unknown method must fail with ErrUnknownMethod, got %v", err)
	}
	if err := l.Enroll("   ", impl.MethodTOTP); !errors.Is(err, impl.ErrBlankID) {
		t.Fatalf("blank user must fail with ErrBlankID, got %v", err)
	}
	if got := len(l.Available("user-404")); got != 0 {
		t.Fatalf("unknown user has %d methods, want 0", got)
	}
}

func TestBeginSelectsStrongestAvailable(t *testing.T) {
	clock := newLadderClock()
	l := impl.NewStepUpMethodLadder(ladderOptions(clock))
	for _, m := range []string{impl.MethodTOTP, impl.MethodSecurityKey} {
		if err := l.Enroll("user-1", m); err != nil {
			t.Fatalf("enroll %s: %v", m, err)
		}
	}
	ch, err := l.Begin("user-1", "wire-transfer")
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if ch.Method != impl.MethodSecurityKey || ch.Strength != impl.StrengthSecurityKey {
		t.Fatalf("challenge method = %q strength = %d, want the strongest rung", ch.Method, ch.Strength)
	}
	if ch.Operation != "wire-transfer" || ch.UserID != "user-1" || ch.ID == "" {
		t.Fatalf("challenge identity = %+v, want user-1 / wire-transfer", ch)
	}
	if ch.State != impl.ChallengeOpen || ch.Attempts != 0 {
		t.Fatalf("new challenge state=%q attempts=%d, want open with no attempts", ch.State, ch.Attempts)
	}
	if ch.Seq <= 0 {
		t.Fatalf("challenge sequence = %d, want a positive number", ch.Seq)
	}
	want := clock.Now().Add(ladderOptions(clock).ChallengeTTL)
	if !ch.ExpiresAt.Equal(want) {
		t.Fatalf("challenge expiry = %v, want %v", ch.ExpiresAt, want)
	}
	if _, ok := l.LookupChallenge(ch.ID); !ok {
		t.Fatalf("challenge must be readable through LookupChallenge")
	}
}

func TestBeginSkipsDisabledMethod(t *testing.T) {
	clock := newLadderClock()
	l := impl.NewStepUpMethodLadder(ladderOptions(clock))
	for _, m := range []string{impl.MethodSecurityKey, impl.MethodTOTP, impl.MethodSMS} {
		if err := l.Enroll("user-1", m); err != nil {
			t.Fatalf("enroll %s: %v", m, err)
		}
	}
	if err := l.Disable("user-1", impl.MethodSecurityKey, "key left at home"); err != nil {
		t.Fatalf("disable security key: %v", err)
	}
	first, err := l.Begin("user-1", "change-password")
	if err != nil {
		t.Fatalf("begin after disable: %v", err)
	}
	if first.Method != impl.MethodTOTP {
		t.Fatalf("challenge method = %q, want the next usable rung totp", first.Method)
	}
	if err := l.Revoke("user-1", impl.MethodTOTP); err != nil {
		t.Fatalf("revoke totp: %v", err)
	}
	second, err := l.Begin("user-1", "rotate-key")
	if err != nil {
		t.Fatalf("begin after revoke: %v", err)
	}
	if second.Method != impl.MethodSMS {
		t.Fatalf("challenge method = %q, want the only usable rung sms", second.Method)
	}
}

func TestBeginRequiresAvailableMethod(t *testing.T) {
	clock := newLadderClock()
	l := impl.NewStepUpMethodLadder(ladderOptions(clock))
	if _, err := l.Begin("user-1", "wire-transfer"); !errors.Is(err, impl.ErrNoMethod) {
		t.Fatalf("user without methods must fail with ErrNoMethod, got %v", err)
	}
	if _, err := l.Begin("   ", "wire-transfer"); !errors.Is(err, impl.ErrBlankID) {
		t.Fatalf("blank user must fail with ErrBlankID, got %v", err)
	}
	if _, err := l.Begin("user-1", "  "); !errors.Is(err, impl.ErrBlankID) {
		t.Fatalf("blank operation must fail with ErrBlankID, got %v", err)
	}
	if err := l.Enroll("user-2", impl.MethodSMS); err != nil {
		t.Fatalf("enroll sms: %v", err)
	}
	if err := l.Disable("user-2", impl.MethodSMS, "no signal"); err != nil {
		t.Fatalf("disable sms: %v", err)
	}
	if _, err := l.Begin("user-2", "wire-transfer"); !errors.Is(err, impl.ErrNoMethod) {
		t.Fatalf("only disabled method must fail with ErrNoMethod, got %v", err)
	}
}

func TestBeginEnforcesChallengeLimit(t *testing.T) {
	clock := newLadderClock()
	opts := ladderOptions(clock)
	opts.MaxChallengesPerUser = 2
	l := impl.NewStepUpMethodLadder(opts)
	if err := l.Enroll("user-1", impl.MethodTOTP); err != nil {
		t.Fatalf("enroll totp: %v", err)
	}
	first, err := l.Begin("user-1", "op-1")
	if err != nil {
		t.Fatalf("begin op-1: %v", err)
	}
	if _, err := l.Begin("user-1", "op-2"); err != nil {
		t.Fatalf("begin op-2: %v", err)
	}
	if _, err := l.Begin("user-1", "op-3"); !errors.Is(err, impl.ErrChallengeLimit) {
		t.Fatalf("third concurrent challenge must fail with ErrChallengeLimit, got %v", err)
	}
	if err := l.Abandon(first.ID); err != nil {
		t.Fatalf("abandon first challenge: %v", err)
	}
	if _, err := l.Begin("user-1", "op-3"); err != nil {
		t.Fatalf("begin after abandoning a slot must succeed, got %v", err)
	}
}

func TestSubmitValidProofCompletesChallenge(t *testing.T) {
	clock := newLadderClock()
	l := impl.NewStepUpMethodLadder(ladderOptions(clock))
	for _, m := range []string{impl.MethodTOTP, impl.MethodPlatformAuth} {
		if err := l.Enroll("user-1", m); err != nil {
			t.Fatalf("enroll %s: %v", m, err)
		}
	}
	ch, err := l.Begin("user-1", "wire-transfer")
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if ch.Method != impl.MethodPlatformAuth {
		t.Fatalf("challenge method = %q, want the platform authenticator", ch.Method)
	}
	res, err := l.Submit(ch.ID, ch.Method, codeProof())
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if !res.Verified || res.Method != impl.MethodPlatformAuth || res.Strength != impl.StrengthPlatformAuth {
		t.Fatalf("result = %+v, want a verified platform authenticator result", res)
	}
	if res.Reason != impl.ReasonVerified || res.ChallengeID != ch.ID {
		t.Fatalf("result reason=%q challenge=%q, want verified on the challenge", res.Reason, res.ChallengeID)
	}
	if res.Seq <= 0 {
		t.Fatalf("result sequence = %d, want a positive number", res.Seq)
	}
	want := clock.Now().Add(ladderOptions(clock).ResultTTL)
	if !res.ExpiresAt.Equal(want) {
		t.Fatalf("result expiry = %v, want %v", res.ExpiresAt, want)
	}
	done, ok := l.LookupChallenge(ch.ID)
	if !ok || done.State != impl.ChallengeCompleted {
		t.Fatalf("challenge after submit = %+v ok=%v, want completed", done, ok)
	}
	reused, ok := l.Reuse("user-1", "wire-transfer")
	if !ok {
		t.Fatalf("completed result must be reusable inside its window")
	}
	if reused.ChallengeID != ch.ID || reused.Strength != impl.StrengthPlatformAuth {
		t.Fatalf("reused result = %+v, want the completed platform result", reused)
	}
}

func TestSubmitRejectsWrongMethod(t *testing.T) {
	clock := newLadderClock()
	l := impl.NewStepUpMethodLadder(ladderOptions(clock))
	for _, m := range []string{impl.MethodSMS, impl.MethodSecurityKey} {
		if err := l.Enroll("user-1", m); err != nil {
			t.Fatalf("enroll %s: %v", m, err)
		}
	}
	ch, err := l.Begin("user-1", "wire-transfer")
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if ch.Method != impl.MethodSecurityKey {
		t.Fatalf("challenge method = %q, want security key", ch.Method)
	}
	if _, err := l.Submit(ch.ID, impl.MethodSMS, codeProof()); !errors.Is(err, impl.ErrMethodNotAllowed) {
		t.Fatalf("submit on another rung must fail with ErrMethodNotAllowed, got %v", err)
	}
	got, _ := l.LookupChallenge(ch.ID)
	if got.State != impl.ChallengeOpen || got.Attempts != 0 {
		t.Fatalf("challenge after a wrong rung = %+v, want open with no attempts", got)
	}
}

func TestSubmitRejectsBadProof(t *testing.T) {
	clock := newLadderClock()
	l := impl.NewStepUpMethodLadder(ladderOptions(clock))
	if err := l.Enroll("user-1", impl.MethodTOTP); err != nil {
		t.Fatalf("enroll totp: %v", err)
	}
	ch, err := l.Begin("user-1", "wire-transfer")
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := l.Submit(ch.ID, ch.Method, impl.Proof{}); !errors.Is(err, impl.ErrBadProof) {
		t.Fatalf("empty proof must fail with ErrBadProof, got %v", err)
	}
	got, _ := l.LookupChallenge(ch.ID)
	if got.Attempts != 1 || got.State != impl.ChallengeOpen {
		t.Fatalf("challenge after a bad proof = %+v, want one attempt and still open", got)
	}
}

func TestRevokeAndEnableMethods(t *testing.T) {
	clock := newLadderClock()
	l := impl.NewStepUpMethodLadder(ladderOptions(clock))
	if err := l.Enroll("user-1", impl.MethodTOTP); err != nil {
		t.Fatalf("enroll totp: %v", err)
	}
	if err := l.Enroll("user-1", impl.MethodSMS); err != nil {
		t.Fatalf("enroll sms: %v", err)
	}
	if err := l.Disable("user-1", impl.MethodSMS, "   "); !errors.Is(err, impl.ErrBadReason) {
		t.Fatalf("blank disable reason must fail with ErrBadReason, got %v", err)
	}
	if err := l.Disable("user-1", impl.MethodSMS, "carrier outage"); err != nil {
		t.Fatalf("disable sms: %v", err)
	}
	if got := len(l.Available("user-1")); got != 1 {
		t.Fatalf("available after disable = %d, want 1", got)
	}
	if err := l.Enable("user-1", impl.MethodSMS); err != nil {
		t.Fatalf("enable sms: %v", err)
	}
	if got := len(l.Available("user-1")); got != 2 {
		t.Fatalf("available after enable = %d, want 2", got)
	}
	if err := l.Revoke("user-1", impl.MethodTOTP); err != nil {
		t.Fatalf("revoke totp: %v", err)
	}
	available := l.Available("user-1")
	if len(available) != 1 || available[0].Kind != impl.MethodSMS {
		t.Fatalf("available after revoke = %+v, want only sms", available)
	}
	if err := l.Revoke("user-1", impl.MethodTOTP); !errors.Is(err, impl.ErrMethodNotEnrolled) {
		t.Fatalf("second revoke must fail with ErrMethodNotEnrolled, got %v", err)
	}
	if err := l.Enable("user-1", impl.MethodTOTP); !errors.Is(err, impl.ErrMethodNotEnrolled) {
		t.Fatalf("enabling a revoked method must fail with ErrMethodNotEnrolled, got %v", err)
	}
	if err := l.Disable("user-1", impl.MethodTOTP, "lost"); !errors.Is(err, impl.ErrMethodNotEnrolled) {
		t.Fatalf("disabling a revoked method must fail with ErrMethodNotEnrolled, got %v", err)
	}
}

func TestSubmitRejectsUnknownAndExpired(t *testing.T) {
	clock := newLadderClock()
	l := impl.NewStepUpMethodLadder(ladderOptions(clock))
	if err := l.Enroll("user-1", impl.MethodTOTP); err != nil {
		t.Fatalf("enroll totp: %v", err)
	}
	if _, err := l.Submit("ch-404", impl.MethodTOTP, codeProof()); !errors.Is(err, impl.ErrChallengeNotFound) {
		t.Fatalf("unknown challenge must fail with ErrChallengeNotFound, got %v", err)
	}
	ch, err := l.Begin("user-1", "wire-transfer")
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	clock.Advance(11 * time.Minute)
	if _, err := l.Submit(ch.ID, ch.Method, codeProof()); !errors.Is(err, impl.ErrChallengeExpired) {
		t.Fatalf("expired challenge must fail with ErrChallengeExpired, got %v", err)
	}
}

func TestEscalateMovesToStrongerMethod(t *testing.T) {
	clock := newLadderClock()
	l := impl.NewStepUpMethodLadder(ladderOptions(clock))
	for _, m := range []string{impl.MethodSecurityKey, impl.MethodTOTP} {
		if err := l.Enroll("user-1", m); err != nil {
			t.Fatalf("enroll %s: %v", m, err)
		}
	}
	if err := l.Disable("user-1", impl.MethodSecurityKey, "key not present"); err != nil {
		t.Fatalf("disable security key: %v", err)
	}
	ch, err := l.Begin("user-1", "wire-transfer")
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if ch.Method != impl.MethodTOTP {
		t.Fatalf("challenge method = %q, want totp", ch.Method)
	}
	if _, err := l.Submit(ch.ID, ch.Method, impl.Proof{}); !errors.Is(err, impl.ErrBadProof) {
		t.Fatalf("bad proof before escalate: %v", err)
	}
	if err := l.Enable("user-1", impl.MethodSecurityKey); err != nil {
		t.Fatalf("enable security key: %v", err)
	}
	escalated, err := l.Escalate(ch.ID)
	if err != nil {
		t.Fatalf("escalate: %v", err)
	}
	if escalated.Method != impl.MethodSecurityKey || escalated.Strength != impl.StrengthSecurityKey {
		t.Fatalf("escalated challenge = %+v, want the security key rung", escalated)
	}
	if escalated.Attempts != 0 || escalated.State != impl.ChallengeOpen {
		t.Fatalf("escalated challenge attempts=%d state=%q, want a fresh open attempt", escalated.Attempts, escalated.State)
	}
	if _, err := l.Escalate(ch.ID); !errors.Is(err, impl.ErrNoStrongerMethod) {
		t.Fatalf("escalating past the top must fail with ErrNoStrongerMethod, got %v", err)
	}
	res, err := l.Submit(ch.ID, impl.MethodSecurityKey, codeProof())
	if err != nil {
		t.Fatalf("submit after escalate: %v", err)
	}
	if !res.Verified || res.Strength != impl.StrengthSecurityKey {
		t.Fatalf("result after escalate = %+v, want a verified security key result", res)
	}
}

func TestAbandonClosesChallenge(t *testing.T) {
	clock := newLadderClock()
	l := impl.NewStepUpMethodLadder(ladderOptions(clock))
	if err := l.Enroll("user-1", impl.MethodTOTP); err != nil {
		t.Fatalf("enroll totp: %v", err)
	}
	ch, err := l.Begin("user-1", "wire-transfer")
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := l.Abandon(ch.ID); err != nil {
		t.Fatalf("abandon: %v", err)
	}
	if _, err := l.Submit(ch.ID, ch.Method, codeProof()); !errors.Is(err, impl.ErrChallengeClosed) {
		t.Fatalf("submit after abandon must fail with ErrChallengeClosed, got %v", err)
	}
	if err := l.Abandon(ch.ID); !errors.Is(err, impl.ErrChallengeClosed) {
		t.Fatalf("second abandon must fail with ErrChallengeClosed, got %v", err)
	}
	if err := l.Abandon("ch-404"); !errors.Is(err, impl.ErrChallengeNotFound) {
		t.Fatalf("abandon of an unknown challenge must fail with ErrChallengeNotFound, got %v", err)
	}
	got, _ := l.LookupChallenge(ch.ID)
	if got.State != impl.ChallengeAbandoned {
		t.Fatalf("challenge state = %q, want %q", got.State, impl.ChallengeAbandoned)
	}
}

func TestReuseResultWithinWindow(t *testing.T) {
	clock := newLadderClock()
	l := impl.NewStepUpMethodLadder(ladderOptions(clock))
	if err := l.Enroll("user-1", impl.MethodTOTP); err != nil {
		t.Fatalf("enroll totp: %v", err)
	}
	ch, err := l.Begin("user-1", "wire-transfer")
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := l.Submit(ch.ID, ch.Method, codeProof()); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if _, ok := l.Reuse("user-1", "wire-transfer"); !ok {
		t.Fatalf("fresh result must be reusable")
	}
	if _, ok := l.Reuse("user-1", "other-operation"); ok {
		t.Fatalf("a result must not be reusable for another operation")
	}
	if _, ok := l.Reuse("user-2", "wire-transfer"); ok {
		t.Fatalf("a result must not be reusable by another user")
	}
	clock.Advance(31 * time.Minute)
	if _, ok := l.Reuse("user-1", "wire-transfer"); ok {
		t.Fatalf("result must expire after the reuse window")
	}
}

func TestSnapshotRestoreKeepsChallengesAndResults(t *testing.T) {
	clock := newLadderClock()
	opts := ladderOptions(clock)
	l := impl.NewStepUpMethodLadder(opts)
	for _, m := range []string{impl.MethodTOTP, impl.MethodPlatformAuth} {
		if err := l.Enroll("user-1", m); err != nil {
			t.Fatalf("enroll %s: %v", m, err)
		}
	}
	open, err := l.Begin("user-1", "op-open")
	if err != nil {
		t.Fatalf("begin op-open: %v", err)
	}
	if _, err := l.Submit(open.ID, open.Method, impl.Proof{}); !errors.Is(err, impl.ErrBadProof) {
		t.Fatalf("bad proof on op-open: %v", err)
	}
	done, err := l.Begin("user-1", "op-done")
	if err != nil {
		t.Fatalf("begin op-done: %v", err)
	}
	if _, err := l.Submit(done.ID, done.Method, codeProof()); err != nil {
		t.Fatalf("submit op-done: %v", err)
	}
	data, err := l.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	if err := l.Restore([]byte("garbage snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if _, ok := l.LookupChallenge(open.ID); !ok {
		t.Fatalf("rejected restore must leave the live challenge table intact")
	}
	restored := impl.NewStepUpMethodLadder(opts)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	got, ok := restored.LookupChallenge(open.ID)
	if !ok || got.Attempts != 1 || got.Method != impl.MethodPlatformAuth || got.State != impl.ChallengeOpen {
		t.Fatalf("restored open challenge = %+v ok=%v, want one attempt on the platform rung", got, ok)
	}
	if !got.ExpiresAt.Equal(open.ExpiresAt) {
		t.Fatalf("restored expiry = %v, want the original %v", got.ExpiresAt, open.ExpiresAt)
	}
	res, err := restored.Submit(open.ID, got.Method, codeProof())
	if err != nil {
		t.Fatalf("submit after restore: %v", err)
	}
	if !res.Verified {
		t.Fatalf("submit after restore must verify, got %+v", res)
	}
	if _, err := restored.Submit(done.ID, done.Method, codeProof()); !errors.Is(err, impl.ErrChallengeClosed) {
		t.Fatalf("completed challenge must stay closed after restore, got %v", err)
	}
	if _, ok := restored.Reuse("user-1", "op-done"); !ok {
		t.Fatalf("completed result must survive a restore")
	}
	if _, ok := restored.Reuse("user-1", "op-open"); !ok {
		t.Fatalf("result produced after restore must be reusable")
	}
}

func TestConcurrentSubmitsProduceOneResult(t *testing.T) {
	clock := newLadderClock()
	l := impl.NewStepUpMethodLadder(ladderOptions(clock))
	if err := l.Enroll("user-1", impl.MethodTOTP); err != nil {
		t.Fatalf("enroll totp: %v", err)
	}
	ch, err := l.Begin("user-1", "wire-transfer")
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	verified, closed := 0, 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := l.Submit(ch.ID, ch.Method, codeProof())
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil && res.Verified:
				verified++
			case errors.Is(err, impl.ErrChallengeClosed):
				closed++
			}
		}()
	}
	wg.Wait()
	if verified != 1 {
		t.Fatalf("concurrent submits produced %d verified results, want exactly 1", verified)
	}
	if closed != 7 {
		t.Fatalf("concurrent submits closed %d times, want 7", closed)
	}
	if _, ok := l.Reuse("user-1", "wire-transfer"); !ok {
		t.Fatalf("the single verified result must be reusable")
	}
}
