package task

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	impl "gsb/batch49-go-fix-a-06"
)

var gateBase = time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)

func gateConfig() impl.GateConfig {
	return impl.GateConfig{
		DifficultyBase:  2,
		DifficultyMax:   4,
		ChallengeTTL:    5 * time.Minute,
		PassTTL:         10 * time.Minute,
		MaxFailures:     3,
		BlockDuration:   2 * time.Minute,
		PenaltyDecay:    30 * time.Minute,
		RefillPerSecond: 1,
		Burst:           2,
		MaxSources:      8,
		IPv6PrefixBits:  64,
	}
}

func newGate() *impl.ChallengeRateGate {
	g := impl.NewChallengeRateGate(gateConfig())
	g.SetNow(gateBase)
	return g
}

func findProof(t *testing.T, challengeID string, difficulty int) (string, string) {
	t.Helper()
	prefix := strings.Repeat("0", difficulty)
	for i := 0; i < 2000000; i++ {
		nonce := fmt.Sprintf("nonce-%d", i)
		sum := sha256.Sum256([]byte(challengeID + ":" + nonce))
		digest := hex.EncodeToString(sum[:])
		if strings.HasPrefix(digest, prefix) {
			return nonce, digest
		}
	}
	t.Fatalf("could not find a proof for difficulty %d", difficulty)
	return "", ""
}

func issueChallenge(t *testing.T, g *impl.ChallengeRateGate, source string) impl.Challenge {
	t.Helper()
	decision, err := g.Admit(source)
	if err != nil {
		t.Fatalf("admit %s: %v", source, err)
	}
	if decision.Action != impl.ActionChallenge {
		t.Fatalf("admit %s action = %q, want challenge", source, decision.Action)
	}
	ch, ok := g.Challenge(decision.ChallengeID)
	if !ok {
		t.Fatalf("challenge %q was not stored", decision.ChallengeID)
	}
	return ch
}

func passSource(t *testing.T, g *impl.ChallengeRateGate, source string) impl.Pass {
	t.Helper()
	ch := issueChallenge(t, g, source)
	nonce, digest := findProof(t, ch.ID, ch.Difficulty)
	pass, err := g.Verify(ch.ID, nonce, digest)
	if err != nil {
		t.Fatalf("verify solved challenge: %v", err)
	}
	return pass
}

func TestNewSourceGetsChallenge(t *testing.T) {
	g := newGate()
	decision, err := g.Admit("203.0.113.7")
	if err != nil {
		t.Fatalf("first contact must succeed, got %v", err)
	}
	if decision.Action != impl.ActionChallenge {
		t.Fatalf("action = %q, want challenge", decision.Action)
	}
	if decision.Difficulty != 2 || decision.ChallengeID == "" {
		t.Fatalf("decision = %+v, want difficulty 2 and a challenge id", decision)
	}
	ch, ok := g.Challenge(decision.ChallengeID)
	if !ok {
		t.Fatalf("issued challenge must be retrievable")
	}
	if ch.Prefix != "203.0.113.7" || ch.Difficulty != 2 || ch.Used {
		t.Fatalf("challenge = %+v", ch)
	}
	if !ch.ExpiresAt.Equal(gateBase.Add(5 * time.Minute)) {
		t.Fatalf("challenge expiry = %v, want %v", ch.ExpiresAt, gateBase.Add(5*time.Minute))
	}
	stats := g.Stats()
	if stats.Sources != 1 || stats.Challenged != 1 {
		t.Fatalf("stats = %+v, want one source and one challenge", stats)
	}
	if _, ok := g.Bucket("203.0.113.7"); !ok {
		t.Fatalf("source bucket must exist after the challenge")
	}
}

func TestAdmitRejectsBlankAndBadSources(t *testing.T) {
	g := newGate()
	if _, err := g.Admit("   "); !errors.Is(err, impl.ErrBlankSource) {
		t.Fatalf("blank source must be rejected, got %v", err)
	}
	if _, err := g.Admit("not-an-ip"); !errors.Is(err, impl.ErrBadSource) {
		t.Fatalf("invalid source must be rejected, got %v", err)
	}
	if _, err := g.Admit("2001:db8::zz"); !errors.Is(err, impl.ErrBadSource) {
		t.Fatalf("invalid ipv6 source must be rejected, got %v", err)
	}
}

func TestIPv6SourcesAggregatePerPrefix(t *testing.T) {
	g := newGate()
	first := issueChallenge(t, g, "2001:db8:1:2:aaaa::1")
	second := issueChallenge(t, g, "2001:db8:1:2:bbbb::2")
	if first.ID != second.ID {
		t.Fatalf("addresses in one /64 must share a challenge: %q vs %q", first.ID, second.ID)
	}
	if first.Prefix != "2001:db8:1:2::/64" {
		t.Fatalf("prefix = %q, want the /64 aggregate", first.Prefix)
	}
	if _, ok := g.Bucket("2001:db8:1:2:cccc::3"); !ok {
		t.Fatalf("bucket lookup must use the same /64 aggregate")
	}
	if got := g.Stats().Sources; got != 1 {
		t.Fatalf("tracked sources = %d, want 1 for one /64", got)
	}
}

func TestSolvedChallengeGrantsPassAndAdmission(t *testing.T) {
	g := newGate()
	pass := passSource(t, g, "203.0.113.20")
	if pass.Token == "" {
		t.Fatalf("pass token must not be empty")
	}
	if pass.Prefix != "203.0.113.20" {
		t.Fatalf("pass prefix = %q", pass.Prefix)
	}
	if !pass.ExpiresAt.Equal(gateBase.Add(10 * time.Minute)) {
		t.Fatalf("pass expiry = %v, want %v", pass.ExpiresAt, gateBase.Add(10*time.Minute))
	}
	decision, err := g.Admit("203.0.113.20")
	if err != nil {
		t.Fatalf("admission with a pass must succeed, got %v", err)
	}
	if decision.Action != impl.ActionAllow {
		t.Fatalf("action = %q, want allow", decision.Action)
	}
	bucket, ok := g.Bucket("203.0.113.20")
	if !ok {
		t.Fatalf("bucket must exist")
	}
	if bucket.Tokens >= float64(gateConfig().Burst) {
		t.Fatalf("tokens = %v, want one consumed from the burst", bucket.Tokens)
	}
}

func TestWrongSolutionLeavesChallengeUsable(t *testing.T) {
	g := newGate()
	ch := issueChallenge(t, g, "203.0.113.30")
	if _, err := g.Verify(ch.ID, "nonce-bad", strings.Repeat("f", 64)); !errors.Is(err, impl.ErrBadSolution) {
		t.Fatalf("wrong proof must be rejected, got %v", err)
	}
	if _, err := g.Verify(ch.ID, "nonce-bad", "short"); !errors.Is(err, impl.ErrBadSolution) {
		t.Fatalf("malformed digest must be rejected, got %v", err)
	}
	if got := g.Stats().Failures; got != 1 {
		t.Fatalf("failure counter = %d, want 1", got)
	}
	nonce, digest := findProof(t, ch.ID, ch.Difficulty)
	if _, err := g.Verify(ch.ID, nonce, digest); err != nil {
		t.Fatalf("challenge must stay usable after a wrong attempt, got %v", err)
	}
}

func TestExpiredChallengeIsRejected(t *testing.T) {
	g := newGate()
	ch := issueChallenge(t, g, "203.0.113.40")
	nonce, digest := findProof(t, ch.ID, ch.Difficulty)
	g.SetNow(gateBase.Add(6 * time.Minute))
	if _, err := g.Verify(ch.ID, nonce, digest); !errors.Is(err, impl.ErrChallengeExpired) {
		t.Fatalf("expired challenge must be rejected, got %v", err)
	}
}

func TestSolvedChallengeCannotBeReplayed(t *testing.T) {
	g := newGate()
	ch := issueChallenge(t, g, "203.0.113.50")
	nonce, digest := findProof(t, ch.ID, ch.Difficulty)
	if _, err := g.Verify(ch.ID, nonce, digest); err != nil {
		t.Fatalf("first solve: %v", err)
	}
	if _, err := g.Verify(ch.ID, nonce, digest); !errors.Is(err, impl.ErrReplay) {
		t.Fatalf("second solve must be a replay, got %v", err)
	}
}

func TestRepeatedFailuresBlockTheSource(t *testing.T) {
	cfg := gateConfig()
	cfg.MaxFailures = 2
	g := impl.NewChallengeRateGate(cfg)
	g.SetNow(gateBase)
	ch := issueChallenge(t, g, "203.0.113.60")
	for i := 0; i < 2; i++ {
		if _, err := g.Verify(ch.ID, "nonce-bad", strings.Repeat("f", 64)); !errors.Is(err, impl.ErrBadSolution) {
			t.Fatalf("failure %d must report ErrBadSolution, got %v", i, err)
		}
	}
	if _, err := g.Admit("203.0.113.60"); !errors.Is(err, impl.ErrBlocked) {
		t.Fatalf("blocked source must be rejected, got %v", err)
	}
	stats := g.Stats()
	if stats.Blocked != 1 {
		t.Fatalf("blocked counter = %d, want 1", stats.Blocked)
	}
	g.SetNow(gateBase.Add(2*time.Minute + time.Second))
	decision, err := g.Admit("203.0.113.60")
	if err != nil {
		t.Fatalf("source must be unblocked after the block window, got %v", err)
	}
	if decision.Action != impl.ActionChallenge {
		t.Fatalf("action after the block = %q, want a new challenge", decision.Action)
	}
}

func TestTokenBucketRateLimitsAfterPass(t *testing.T) {
	g := newGate()
	passSource(t, g, "203.0.113.70")
	for i := 0; i < 2; i++ {
		if _, err := g.Admit("203.0.113.70"); err != nil {
			t.Fatalf("admission %d inside the burst: %v", i, err)
		}
	}
	decision, err := g.Admit("203.0.113.70")
	if !errors.Is(err, impl.ErrRateLimited) {
		t.Fatalf("third admission must be rate limited, got %v", err)
	}
	if decision.Action != impl.ActionReject || decision.RetryAfter <= 0 {
		t.Fatalf("rate limited decision = %+v, want reject with a positive retry", decision)
	}
	g.SetNow(gateBase.Add(time.Second))
	if _, err := g.Admit("203.0.113.70"); err != nil {
		t.Fatalf("one refill tick must allow one more request, got %v", err)
	}
}

func TestAllowListBypassesChallenge(t *testing.T) {
	g := newGate()
	if err := g.AllowPrefix("198.51.100.0/24"); err != nil {
		t.Fatalf("allow prefix: %v", err)
	}
	decision, err := g.Admit("198.51.100.9")
	if err != nil {
		t.Fatalf("allow listed source must pass, got %v", err)
	}
	if decision.Action != impl.ActionAllow {
		t.Fatalf("action = %q, want allow without a challenge", decision.Action)
	}
	if got := g.Stats().Challenged; got != 0 {
		t.Fatalf("allow listed source was challenged: %d", got)
	}
}

func TestLongestPrefixWinsBetweenDenyAndAllow(t *testing.T) {
	g := newGate()
	if err := g.DenyPrefix("203.0.113.0/24"); err != nil {
		t.Fatalf("deny prefix: %v", err)
	}
	if err := g.AllowPrefix("203.0.113.128/25"); err != nil {
		t.Fatalf("allow prefix: %v", err)
	}
	decision, err := g.Admit("203.0.113.200")
	if err != nil {
		t.Fatalf("longer allow prefix must win, got %v", err)
	}
	if decision.Action != impl.ActionAllow {
		t.Fatalf("action = %q, want allow", decision.Action)
	}
	if _, err := g.Admit("203.0.113.7"); !errors.Is(err, impl.ErrBlocked) {
		t.Fatalf("address under the deny prefix must be blocked, got %v", err)
	}
}

func TestPrefixValidation(t *testing.T) {
	g := newGate()
	if err := g.AllowPrefix("not-a-cidr"); !errors.Is(err, impl.ErrBadCIDR) {
		t.Fatalf("invalid allow prefix must be rejected, got %v", err)
	}
	if err := g.DenyPrefix("10.0.0.0/33"); !errors.Is(err, impl.ErrBadCIDR) {
		t.Fatalf("invalid deny prefix must be rejected, got %v", err)
	}
}

func TestSweepDropsExpiredChallenges(t *testing.T) {
	g := newGate()
	ch := issueChallenge(t, g, "203.0.113.80")
	g.SetNow(gateBase.Add(6 * time.Minute))
	if removed := g.Sweep(); removed < 1 {
		t.Fatalf("sweep removed %d entries, want at least the expired challenge", removed)
	}
	if _, ok := g.Challenge(ch.ID); ok {
		t.Fatalf("expired challenge must be gone after the sweep")
	}
}

func TestSourceCapacityEvictsIdleSource(t *testing.T) {
	cfg := gateConfig()
	cfg.MaxSources = 2
	g := impl.NewChallengeRateGate(cfg)
	g.SetNow(gateBase)
	first := issueChallenge(t, g, "203.0.113.90")
	issueChallenge(t, g, "203.0.113.91")
	if _, err := g.Admit("203.0.113.92"); err != nil {
		t.Fatalf("new source must evict an idle one, got %v", err)
	}
	if got := g.Stats().Evictions; got != 1 {
		t.Fatalf("eviction counter = %d, want 1", got)
	}
	if _, ok := g.Challenge(first.ID); ok {
		t.Fatalf("challenge of the evicted source must be dropped")
	}
	decision, err := g.Admit("203.0.113.90")
	if err != nil {
		t.Fatalf("evicted source must be able to come back, got %v", err)
	}
	if decision.ChallengeID == first.ID {
		t.Fatalf("returning source must receive a fresh challenge")
	}
}

func TestSnapshotRestoreKeepsPassAndCounters(t *testing.T) {
	g := newGate()
	passSource(t, g, "203.0.113.100")
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := impl.NewChallengeRateGate(gateConfig())
	restored.SetNow(gateBase)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restored.Stats() != g.Stats() {
		t.Fatalf("restored stats = %+v, want %+v", restored.Stats(), g.Stats())
	}
	decision, err := restored.Admit("203.0.113.100")
	if err != nil {
		t.Fatalf("restored pass must admit, got %v", err)
	}
	if decision.Action != impl.ActionAllow {
		t.Fatalf("action after restore = %q, want allow", decision.Action)
	}
	before := restored.Stats()
	if err := restored.Restore([]byte("garbage")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must report ErrBadSnapshot, got %v", err)
	}
	if restored.Stats() != before {
		t.Fatalf("failed restore mutated the gate: %+v -> %+v", before, restored.Stats())
	}
}
