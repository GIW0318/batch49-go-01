package task

import (
	"errors"
	"reflect"
	"testing"
	"time"

	impl "gsb/batch49-go-fix-a-02"
)

var tokenBase = time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)

func tokenConfig() impl.ScopeConfig {
	return impl.ScopeConfig{
		AllowedAudiences: map[string][]string{
			"svc-a": {"aud-a", "aud-b", "aud-c", "aud-d"},
			"svc-b": {"aud-a"},
		},
		MaxTTL:                     time.Hour,
		MaxDepth:                   2,
		MaxTokensPerServiceAccount: 8,
		OverlapWindow:              10 * time.Minute,
		ClockSkew:                  30 * time.Second,
	}
}

func newTokenChain() *impl.SaTokenScopeChain {
	c := impl.NewSaTokenScopeChain(tokenConfig())
	c.SetNow(tokenBase)
	return c
}

func mustGrant(t *testing.T, c *impl.SaTokenScopeChain, audiences []string, ttl time.Duration) impl.Token {
	t.Helper()
	tok, err := c.Grant("svc-a", audiences, ttl)
	if err != nil {
		t.Fatalf("grant %v: %v", audiences, err)
	}
	return tok
}

func TestGrantCreatesRootToken(t *testing.T) {
	c := newTokenChain()
	tok, err := c.Grant("svc-a", []string{"aud-c", "aud-a", "aud-b", "aud-a"}, 10*time.Minute)
	if err != nil {
		t.Fatalf("grant must succeed, got %v", err)
	}
	if tok.ID == "" {
		t.Fatalf("token id must not be empty")
	}
	if tok.SAID != "svc-a" || tok.Generation != 1 || tok.Depth != 0 {
		t.Fatalf("root token = %+v, want svc-a generation 1 depth 0", tok)
	}
	if tok.ParentID != "" {
		t.Fatalf("root token parent = %q, want empty", tok.ParentID)
	}
	wantAud := []string{"aud-a", "aud-b", "aud-c"}
	if !reflect.DeepEqual(tok.Audiences, wantAud) {
		t.Fatalf("audiences = %v, want deduplicated and sorted %v", tok.Audiences, wantAud)
	}
	if !tok.IssuedAt.Equal(tokenBase) {
		t.Fatalf("issued at = %v, want %v", tok.IssuedAt, tokenBase)
	}
	if !tok.ExpiresAt.Equal(tokenBase.Add(10 * time.Minute)) {
		t.Fatalf("expires at = %v, want %v", tok.ExpiresAt, tokenBase.Add(10*time.Minute))
	}
	p, err := c.Verify(tok.ID, "aud-b")
	if err != nil {
		t.Fatalf("verify root token: %v", err)
	}
	if p.SAID != "svc-a" || p.TokenID != tok.ID || p.Generation != 1 || p.Depth != 0 {
		t.Fatalf("principal = %+v", p)
	}
	if !reflect.DeepEqual(p.Audiences, wantAud) {
		t.Fatalf("principal audiences = %v, want %v", p.Audiences, wantAud)
	}
}

func TestGrantRejectsBlankFields(t *testing.T) {
	c := newTokenChain()
	if _, err := c.Grant("  ", []string{"aud-a"}, time.Minute); !errors.Is(err, impl.ErrBlankField) {
		t.Fatalf("blank service account must be rejected, got %v", err)
	}
	if _, err := c.Grant("svc-a", nil, time.Minute); !errors.Is(err, impl.ErrBlankField) {
		t.Fatalf("empty audience list must be rejected, got %v", err)
	}
	if _, err := c.Grant("svc-a", []string{"aud-a", "  "}, time.Minute); !errors.Is(err, impl.ErrBlankField) {
		t.Fatalf("blank audience entry must be rejected, got %v", err)
	}
	if got := c.Status("svc-a").LiveTokens; got != 0 {
		t.Fatalf("rejected grants left %d live tokens, want 0", got)
	}
}

func TestGrantRejectsUnknownServiceAccount(t *testing.T) {
	c := newTokenChain()
	if _, err := c.Grant("svc-zzz", []string{"aud-a"}, time.Minute); !errors.Is(err, impl.ErrUnknownServiceAccount) {
		t.Fatalf("unregistered service account must be rejected, got %v", err)
	}
}

func TestGrantRejectsUnknownAudience(t *testing.T) {
	c := newTokenChain()
	if _, err := c.Grant("svc-a", []string{"aud-a", "aud-z"}, time.Minute); !errors.Is(err, impl.ErrUnknownAudience) {
		t.Fatalf("audience outside the registration must be rejected, got %v", err)
	}
	if got := c.Status("svc-a").LiveTokens; got != 0 {
		t.Fatalf("rejected audience left %d live tokens, want 0", got)
	}
}

func TestGrantRejectsTTLOutsideWindow(t *testing.T) {
	c := newTokenChain()
	if _, err := c.Grant("svc-a", []string{"aud-a"}, 2*time.Hour); !errors.Is(err, impl.ErrTTLTooLong) {
		t.Fatalf("ttl beyond the configured maximum must be rejected, got %v", err)
	}
	if _, err := c.Grant("svc-a", []string{"aud-a"}, 0); !errors.Is(err, impl.ErrBadTTL) {
		t.Fatalf("zero ttl must be rejected, got %v", err)
	}
	if _, err := c.Grant("svc-a", []string{"aud-a"}, -time.Minute); !errors.Is(err, impl.ErrBadTTL) {
		t.Fatalf("negative ttl must be rejected, got %v", err)
	}
}

func TestExchangeNarrowsAudiencesAndTTL(t *testing.T) {
	c := newTokenChain()
	root := mustGrant(t, c, []string{"aud-a", "aud-b", "aud-c"}, 10*time.Minute)
	child, err := c.Exchange(root.ID, []string{"aud-c", "aud-a"}, 5*time.Minute)
	if err != nil {
		t.Fatalf("exchange must succeed, got %v", err)
	}
	if child.ParentID != root.ID {
		t.Fatalf("child parent = %q, want %q", child.ParentID, root.ID)
	}
	if child.Depth != 1 || child.Generation != 1 || child.SAID != "svc-a" {
		t.Fatalf("child token = %+v, want depth 1 generation 1 of svc-a", child)
	}
	wantAud := []string{"aud-a", "aud-c"}
	if !reflect.DeepEqual(child.Audiences, wantAud) {
		t.Fatalf("child audiences = %v, want %v", child.Audiences, wantAud)
	}
	if !child.ExpiresAt.Equal(tokenBase.Add(5 * time.Minute)) {
		t.Fatalf("child expiry = %v, want %v", child.ExpiresAt, tokenBase.Add(5*time.Minute))
	}
	if _, err := c.Verify(child.ID, "aud-c"); err != nil {
		t.Fatalf("child must be valid for a narrowed audience, got %v", err)
	}
	if _, err := c.Verify(child.ID, "aud-b"); !errors.Is(err, impl.ErrAudienceMismatch) {
		t.Fatalf("child must not be valid for the dropped audience, got %v", err)
	}
}

func TestExchangeRejectsAudienceWidening(t *testing.T) {
	c := newTokenChain()
	root := mustGrant(t, c, []string{"aud-a", "aud-b", "aud-c"}, 10*time.Minute)
	if _, err := c.Exchange(root.ID, []string{"aud-a", "aud-b", "aud-c"}, time.Minute); !errors.Is(err, impl.ErrAudienceWidening) {
		t.Fatalf("equal audience set must be rejected as widening, got %v", err)
	}
	if _, err := c.Exchange(root.ID, []string{"aud-a", "aud-b", "aud-c", "aud-d"}, time.Minute); !errors.Is(err, impl.ErrAudienceWidening) {
		t.Fatalf("superset must be rejected as widening, got %v", err)
	}
	narrow := mustExchange(t, c, root.ID, []string{"aud-a", "aud-b"}, 5*time.Minute)
	if _, err := c.Exchange(narrow.ID, []string{"aud-a", "aud-c"}, time.Minute); !errors.Is(err, impl.ErrAudienceWidening) {
		t.Fatalf("audience outside the parent must be rejected, got %v", err)
	}
}

func mustExchange(t *testing.T, c *impl.SaTokenScopeChain, parentID string, audiences []string, ttl time.Duration) impl.Token {
	t.Helper()
	tok, err := c.Exchange(parentID, audiences, ttl)
	if err != nil {
		t.Fatalf("exchange %v: %v", audiences, err)
	}
	return tok
}

func TestExchangeRejectsTTLBeyondParent(t *testing.T) {
	c := newTokenChain()
	root := mustGrant(t, c, []string{"aud-a", "aud-b"}, 5*time.Minute)
	if _, err := c.Exchange(root.ID, []string{"aud-a"}, 10*time.Minute); !errors.Is(err, impl.ErrTTLTooLong) {
		t.Fatalf("child ttl beyond the parent remaining time must be rejected, got %v", err)
	}
	if _, err := c.Exchange(root.ID, []string{"aud-a"}, 0); !errors.Is(err, impl.ErrBadTTL) {
		t.Fatalf("zero child ttl must be rejected, got %v", err)
	}
}

func TestExchangeRejectsDepthLimit(t *testing.T) {
	c := newTokenChain()
	root := mustGrant(t, c, []string{"aud-a", "aud-b", "aud-c", "aud-d"}, 30*time.Minute)
	d1 := mustExchange(t, c, root.ID, []string{"aud-a", "aud-b", "aud-c"}, 20*time.Minute)
	d2 := mustExchange(t, c, d1.ID, []string{"aud-a", "aud-b"}, 10*time.Minute)
	if d2.Depth != 2 {
		t.Fatalf("second child depth = %d, want 2", d2.Depth)
	}
	if _, err := c.Exchange(d2.ID, []string{"aud-a"}, 5*time.Minute); !errors.Is(err, impl.ErrDepthLimit) {
		t.Fatalf("exchange beyond the configured depth must be rejected, got %v", err)
	}
}

func TestVerifyRejectsExpiredToken(t *testing.T) {
	c := newTokenChain()
	tok := mustGrant(t, c, []string{"aud-a"}, 10*time.Minute)
	c.SetNow(tokenBase.Add(11 * time.Minute))
	if _, err := c.Verify(tok.ID, "aud-a"); !errors.Is(err, impl.ErrTokenExpired) {
		t.Fatalf("token past its expiry must be rejected, got %v", err)
	}
	if _, err := c.Verify("tok-missing", "aud-a"); !errors.Is(err, impl.ErrUnknownToken) {
		t.Fatalf("unknown token must report ErrUnknownToken, got %v", err)
	}
}

func TestRotateKeepsPreviousGenerationDuringOverlap(t *testing.T) {
	c := newTokenChain()
	old := mustGrant(t, c, []string{"aud-a"}, 10*time.Minute)
	rec, err := c.Rotate("svc-a")
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if rec.SAID != "svc-a" || rec.Generation != 2 || rec.RetiredGeneration != 1 {
		t.Fatalf("rotation record = %+v, want generation 2 retiring 1", rec)
	}
	if !rec.RetireAt.Equal(tokenBase.Add(10 * time.Minute)) {
		t.Fatalf("retire at = %v, want %v", rec.RetireAt, tokenBase.Add(10*time.Minute))
	}
	if rec.LiveTokens != 1 {
		t.Fatalf("live tokens at rotation = %d, want 1", rec.LiveTokens)
	}
	fresh := mustGrant(t, c, []string{"aud-a"}, time.Hour)
	if fresh.Generation != 2 {
		t.Fatalf("new token generation = %d, want 2", fresh.Generation)
	}
	c.SetNow(tokenBase.Add(9 * time.Minute))
	if _, err := c.Verify(old.ID, "aud-a"); err != nil {
		t.Fatalf("old generation must stay valid inside the overlap window, got %v", err)
	}
	c.SetNow(tokenBase.Add(10*time.Minute + time.Nanosecond))
	if _, err := c.Verify(old.ID, "aud-a"); !errors.Is(err, impl.ErrGenerationRetired) {
		t.Fatalf("old generation after the overlap must report ErrGenerationRetired, got %v", err)
	}
	if _, err := c.Verify(fresh.ID, "aud-a"); err != nil {
		t.Fatalf("new generation token must keep working, got %v", err)
	}
}

func TestRevokeGenerationKillsTokensAndIsIdempotent(t *testing.T) {
	c := newTokenChain()
	first := mustGrant(t, c, []string{"aud-a"}, 10*time.Minute)
	second := mustGrant(t, c, []string{"aud-b"}, 10*time.Minute)
	if _, err := c.Rotate("svc-a"); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	third := mustGrant(t, c, []string{"aud-a"}, 10*time.Minute)
	if third.Generation != 2 {
		t.Fatalf("token after rotation has generation %d, want 2", third.Generation)
	}
	revoked, err := c.RevokeGeneration("svc-a", 1)
	if err != nil {
		t.Fatalf("revoke generation: %v", err)
	}
	if revoked != 2 {
		t.Fatalf("revoked %d tokens, want 2", revoked)
	}
	for _, tok := range []impl.Token{first, second} {
		if _, err := c.Verify(tok.ID, "aud-a"); !errors.Is(err, impl.ErrTokenRevoked) {
			t.Fatalf("generation 1 token must be revoked, got %v", err)
		}
	}
	if _, err := c.Verify(third.ID, "aud-a"); err != nil {
		t.Fatalf("generation 2 token must survive, got %v", err)
	}
	if again, err := c.RevokeGeneration("svc-a", 1); err != nil || again != 0 {
		t.Fatalf("repeated revoke generation = (%d, %v), want (0, nil)", again, err)
	}
	if _, err := c.RevokeGeneration("svc-a", 9); !errors.Is(err, impl.ErrUnknownGeneration) {
		t.Fatalf("unknown generation must be rejected, got %v", err)
	}
	if got := c.Status("svc-a").LiveTokens; got != 1 {
		t.Fatalf("live tokens after the generation revoke = %d, want 1", got)
	}
}

func TestRevokeTokenRejectsAndIsIdempotent(t *testing.T) {
	c := newTokenChain()
	root := mustGrant(t, c, []string{"aud-a", "aud-b"}, 10*time.Minute)
	child := mustExchange(t, c, root.ID, []string{"aud-a"}, 5*time.Minute)
	if err := c.RevokeToken(child.ID); err != nil {
		t.Fatalf("revoke token: %v", err)
	}
	if _, err := c.Verify(child.ID, "aud-a"); !errors.Is(err, impl.ErrTokenRevoked) {
		t.Fatalf("revoked child must be rejected, got %v", err)
	}
	if _, err := c.Verify(root.ID, "aud-b"); err != nil {
		t.Fatalf("revoking a child must not revoke its parent, got %v", err)
	}
	if err := c.RevokeToken(child.ID); err != nil {
		t.Fatalf("repeated revoke must be idempotent, got %v", err)
	}
	if err := c.RevokeToken("tok-missing"); !errors.Is(err, impl.ErrUnknownToken) {
		t.Fatalf("revoking an unknown token must report ErrUnknownToken, got %v", err)
	}
}

func TestTokenQuotaIsEnforced(t *testing.T) {
	cfg := tokenConfig()
	cfg.MaxTokensPerServiceAccount = 2
	c := impl.NewSaTokenScopeChain(cfg)
	c.SetNow(tokenBase)
	root := mustGrant(t, c, []string{"aud-a", "aud-b", "aud-c"}, 10*time.Minute)
	mustExchange(t, c, root.ID, []string{"aud-a", "aud-b"}, 5*time.Minute)
	if _, err := c.Exchange(root.ID, []string{"aud-a", "aud-c"}, 5*time.Minute); !errors.Is(err, impl.ErrTokenQuotaExceeded) {
		t.Fatalf("third live token must hit the quota, got %v", err)
	}
	if got := c.Status("svc-a").LiveTokens; got != 2 {
		t.Fatalf("live tokens = %d, want the configured cap 2", got)
	}
}

func TestSnapshotRestoreKeepsLedgerAndRejectsCorrupt(t *testing.T) {
	c := newTokenChain()
	root := mustGrant(t, c, []string{"aud-a", "aud-b", "aud-c"}, 30*time.Minute)
	child := mustExchange(t, c, root.ID, []string{"aud-a", "aud-b"}, 20*time.Minute)
	if _, err := c.Rotate("svc-a"); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	mustGrant(t, c, []string{"aud-a"}, time.Hour)
	data, err := c.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := impl.NewSaTokenScopeChain(tokenConfig())
	restored.SetNow(tokenBase)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !reflect.DeepEqual(restored.Status("svc-a"), c.Status("svc-a")) {
		t.Fatalf("restored status = %+v, want %+v", restored.Status("svc-a"), c.Status("svc-a"))
	}
	if _, err := restored.Verify(child.ID, "aud-a"); err != nil {
		t.Fatalf("restored chain must accept a live child token, got %v", err)
	}
	before := restored.Status("svc-a")
	if err := restored.Restore([]byte("<<not-json>>")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must report ErrBadSnapshot, got %v", err)
	}
	if !reflect.DeepEqual(restored.Status("svc-a"), before) {
		t.Fatalf("failed restore mutated the state: %+v -> %+v", before, restored.Status("svc-a"))
	}
}
