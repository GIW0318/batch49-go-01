package task

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	impl "gsb/batch49-go-safety-a-20"
)

var linkBase = time.Date(2026, 7, 13, 6, 0, 0, 0, time.UTC)

type linkClock struct {
	now time.Time
}

func (c *linkClock) Now() time.Time {
	return c.now
}

func (c *linkClock) Advance(d time.Duration) {
	c.now = c.now.Add(d)
}

func linkOptions(clk *linkClock) impl.Options {
	return impl.Options{
		Now:                    clk.Now,
		TTL:                    15 * time.Minute,
		Cooldown:               time.Minute,
		MaxActiveLinksPerEmail: 3,
		SessionTTL:             12 * time.Hour,
		AllowedPrefixes:        []string{"/app/", "/billing/"},
	}
}

func newDispenser(clk *linkClock, opts impl.Options) *impl.MagicLinkDispenser {
	return impl.NewMagicLinkDispenser(opts)
}

func TestSendCreatesLink(t *testing.T) {
	clk := &linkClock{now: linkBase}
	d := newDispenser(clk, linkOptions(clk))
	link, err := d.Send("  Alice@Example.TEST ", "/app/orders")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if link.Token == "" {
		t.Fatalf("token must not be empty")
	}
	if link.Email != "alice@example.test" {
		t.Fatalf("link email = %q, want the normalised alice@example.test", link.Email)
	}
	if link.ReturnTo != "/app/orders" {
		t.Fatalf("return path = %q, want /app/orders", link.ReturnTo)
	}
	if link.BindCookie == "" {
		t.Fatalf("link must carry a bind cookie")
	}
	if !link.IssuedAt.Equal(linkBase) {
		t.Fatalf("issued at = %v, want %v", link.IssuedAt, linkBase)
	}
	if !link.ExpiresAt.Equal(linkBase.Add(15 * time.Minute)) {
		t.Fatalf("expires at = %v, want %v", link.ExpiresAt, linkBase.Add(15*time.Minute))
	}
	if link.Used || link.Revoked {
		t.Fatalf("fresh link must be neither used nor revoked: %+v", link)
	}
	if link.ResendCount != 0 {
		t.Fatalf("fresh link resend count = %d, want 0", link.ResendCount)
	}
	if link.Seq <= 0 {
		t.Fatalf("link sequence = %d, want a positive number", link.Seq)
	}
}

func TestSendRejectsBadEmailAndReturnTo(t *testing.T) {
	clk := &linkClock{now: linkBase}
	d := newDispenser(clk, linkOptions(clk))
	if _, err := d.Send("   ", "/app/orders"); !errors.Is(err, impl.ErrBlankEmail) {
		t.Fatalf("blank email must fail with ErrBlankEmail, got %v", err)
	}
	if _, err := d.Send("missing-at-sign", "/app/orders"); !errors.Is(err, impl.ErrBadEmail) {
		t.Fatalf("malformed email must fail with ErrBadEmail, got %v", err)
	}
	badPaths := []string{
		"https://evil.example/app/orders",
		"/app/../admin",
		"//evil.example/app/orders",
		"/other/page",
		"",
	}
	for _, path := range badPaths {
		if _, err := d.Send("alice@example.test", path); !errors.Is(err, impl.ErrReturnTo) {
			t.Fatalf("return path %q must fail with ErrReturnTo, got %v", path, err)
		}
	}
	if got := len(d.Links("alice@example.test")); got != 0 {
		t.Fatalf("rejected sends must not store links, got %d", got)
	}
	if _, err := d.Send("alice@example.test", "/billing/invoices"); err != nil {
		t.Fatalf("a whitelisted return path must be accepted, got %v", err)
	}
}

func TestSendEnforcesActiveLinkCap(t *testing.T) {
	clk := &linkClock{now: linkBase}
	opts := linkOptions(clk)
	opts.Cooldown = 0
	opts.MaxActiveLinksPerEmail = 2
	d := newDispenser(clk, opts)
	if _, err := d.Send("alice@example.test", "/app/one"); err != nil {
		t.Fatalf("first send: %v", err)
	}
	if _, err := d.Send("alice@example.test", "/app/two"); err != nil {
		t.Fatalf("second send: %v", err)
	}
	if _, err := d.Send("alice@example.test", "/app/three"); !errors.Is(err, impl.ErrActiveLinkLimit) {
		t.Fatalf("third link must fail with ErrActiveLinkLimit, got %v", err)
	}
	if got := len(d.Links("alice@example.test")); got != 2 {
		t.Fatalf("active links = %d, want the configured cap 2", got)
	}
	if _, err := d.Send("bob@example.test", "/app/one"); err != nil {
		t.Fatalf("the cap is per email, bob must be accepted, got %v", err)
	}
}

func TestSendCooldownBlocksThenAllows(t *testing.T) {
	clk := &linkClock{now: linkBase}
	d := newDispenser(clk, linkOptions(clk))
	if _, err := d.Send("alice@example.test", "/app/one"); err != nil {
		t.Fatalf("first send: %v", err)
	}
	if _, err := d.Send("alice@example.test", "/app/two"); !errors.Is(err, impl.ErrCooldown) {
		t.Fatalf("a send inside the cooldown must fail with ErrCooldown, got %v", err)
	}
	clk.Advance(61 * time.Second)
	if _, err := d.Send("alice@example.test", "/app/two"); err != nil {
		t.Fatalf("a send after the cooldown must succeed, got %v", err)
	}
	if got := len(d.Links("alice@example.test")); got != 2 {
		t.Fatalf("stored links = %d, want 2", got)
	}
}

func TestPreviewReturnsLanding(t *testing.T) {
	clk := &linkClock{now: linkBase}
	d := newDispenser(clk, linkOptions(clk))
	link, err := d.Send("alice@example.test", "/app/orders")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	landing, err := d.Preview(link.Token)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if landing.Email != "alice@example.test" {
		t.Fatalf("landing email = %q, want alice@example.test", landing.Email)
	}
	if landing.MaskedEmail != "a***e@example.test" {
		t.Fatalf("masked email = %q, want a***e@example.test", landing.MaskedEmail)
	}
	if landing.ReturnTo != "/app/orders" {
		t.Fatalf("landing return path = %q, want /app/orders", landing.ReturnTo)
	}
	if !landing.ExpiresAt.Equal(link.ExpiresAt) {
		t.Fatalf("landing expiry = %v, want %v", landing.ExpiresAt, link.ExpiresAt)
	}
	if landing.ConfirmPath == "" {
		t.Fatalf("landing must expose a confirmation path")
	}
}

func TestPreviewUnknownToken(t *testing.T) {
	clk := &linkClock{now: linkBase}
	d := newDispenser(clk, linkOptions(clk))
	if _, err := d.Preview("token-404"); !errors.Is(err, impl.ErrTokenNotFound) {
		t.Fatalf("unknown token must fail with ErrTokenNotFound, got %v", err)
	}
	if _, err := d.Preview(""); !errors.Is(err, impl.ErrTokenNotFound) {
		t.Fatalf("empty token must fail with ErrTokenNotFound, got %v", err)
	}
}

func TestRedeemIssuesSessionOnce(t *testing.T) {
	clk := &linkClock{now: linkBase}
	d := newDispenser(clk, linkOptions(clk))
	link, err := d.Send("alice@example.test", "/app/orders")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	session, err := d.Redeem(link.Token, link.BindCookie)
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if session.ID == "" {
		t.Fatalf("session id must not be empty")
	}
	if session.Email != "alice@example.test" || session.ReturnTo != "/app/orders" {
		t.Fatalf("session identity mismatch: %+v", session)
	}
	if !session.ExpiresAt.Equal(linkBase.Add(12 * time.Hour)) {
		t.Fatalf("session expiry = %v, want %v", session.ExpiresAt, linkBase.Add(12*time.Hour))
	}
	if _, err := d.Redeem(link.Token, link.BindCookie); !errors.Is(err, impl.ErrTokenUsed) {
		t.Fatalf("second redeem must fail with ErrTokenUsed, got %v", err)
	}
}

func TestRedeemUnknownToken(t *testing.T) {
	clk := &linkClock{now: linkBase}
	d := newDispenser(clk, linkOptions(clk))
	if _, err := d.Redeem("token-404", "cookie-1"); !errors.Is(err, impl.ErrTokenNotFound) {
		t.Fatalf("unknown token must fail with ErrTokenNotFound, got %v", err)
	}
}

func TestRedeemExpiredToken(t *testing.T) {
	clk := &linkClock{now: linkBase}
	d := newDispenser(clk, linkOptions(clk))
	link, err := d.Send("alice@example.test", "/app/orders")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	clk.Advance(16 * time.Minute)
	if _, err := d.Redeem(link.Token, link.BindCookie); !errors.Is(err, impl.ErrTokenExpired) {
		t.Fatalf("redeeming past the ttl must fail with ErrTokenExpired, got %v", err)
	}
}

func TestResendIssuesFreshToken(t *testing.T) {
	clk := &linkClock{now: linkBase}
	d := newDispenser(clk, linkOptions(clk))
	first, err := d.Send("alice@example.test", "/app/orders")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	clk.Advance(61 * time.Second)
	second, err := d.Resend("alice@example.test")
	if err != nil {
		t.Fatalf("resend: %v", err)
	}
	if second.Token == first.Token {
		t.Fatalf("resend must issue a fresh token")
	}
	if second.Email != "alice@example.test" || second.ReturnTo != first.ReturnTo {
		t.Fatalf("resent link = %+v, want the original email and return path", second)
	}
	if second.ResendCount != 1 {
		t.Fatalf("resend count = %d, want 1", second.ResendCount)
	}
	if !second.ExpiresAt.Equal(linkBase.Add(61 * time.Second).Add(15 * time.Minute)) {
		t.Fatalf("resent expiry = %v, want a fresh ttl", second.ExpiresAt)
	}
	if second.Seq <= first.Seq {
		t.Fatalf("resent sequence = %d, must be greater than %d", second.Seq, first.Seq)
	}
}

func TestRevokeInvalidatesLinks(t *testing.T) {
	clk := &linkClock{now: linkBase}
	d := newDispenser(clk, linkOptions(clk))
	link, err := d.Send("alice@example.test", "/app/orders")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if err := d.Revoke("alice@example.test"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := d.Preview(link.Token); !errors.Is(err, impl.ErrTokenRevoked) {
		t.Fatalf("previewing a revoked token must fail with ErrTokenRevoked, got %v", err)
	}
	if _, err := d.Redeem(link.Token, link.BindCookie); !errors.Is(err, impl.ErrTokenRevoked) {
		t.Fatalf("redeeming a revoked token must fail with ErrTokenRevoked, got %v", err)
	}
	if err := d.Revoke("nobody@example.test"); err != nil {
		t.Fatalf("revoking an email without links must be idempotent, got %v", err)
	}
	if _, err := d.Resend("alice@example.test"); !errors.Is(err, impl.ErrNoActiveLink) {
		t.Fatalf("resending without an active link must fail with ErrNoActiveLink, got %v", err)
	}
	if _, err := d.Send("alice@example.test", "/app/again"); err != nil {
		t.Fatalf("a fresh send after revoke must succeed, got %v", err)
	}
}

func TestConcurrentSendRespectsCap(t *testing.T) {
	clk := &linkClock{now: linkBase}
	opts := linkOptions(clk)
	opts.Cooldown = 0
	opts.MaxActiveLinksPerEmail = 3
	d := newDispenser(clk, opts)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, limited := 0, 0
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := d.Send("alice@example.test", fmt.Sprintf("/app/%02d", i))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, impl.ErrActiveLinkLimit):
				limited++
			}
		}(i)
	}
	wg.Wait()
	if ok != 3 {
		t.Fatalf("concurrent sends issued %d links, want exactly the cap 3", ok)
	}
	if limited != 9 {
		t.Fatalf("concurrent sends reported %d limit errors, want 9", limited)
	}
	if got := len(d.Links("alice@example.test")); got != 3 {
		t.Fatalf("active links after the race = %d, want 3", got)
	}
}

func TestNormalizeAndMaskEmail(t *testing.T) {
	clk := &linkClock{now: linkBase}
	d := newDispenser(clk, linkOptions(clk))
	value, err := d.NormalizeEmail("  Alice@Example.TEST ")
	if err != nil {
		t.Fatalf("normalize email: %v", err)
	}
	if value != "alice@example.test" {
		t.Fatalf("normalized email = %q, want alice@example.test", value)
	}
	if _, err := d.NormalizeEmail("missing-at-sign"); !errors.Is(err, impl.ErrBadEmail) {
		t.Fatalf("malformed email must fail with ErrBadEmail, got %v", err)
	}
	if _, err := d.NormalizeEmail("a@@b"); !errors.Is(err, impl.ErrBadEmail) {
		t.Fatalf("email with two @ must fail with ErrBadEmail, got %v", err)
	}
	cases := map[string]string{
		"alice@example.test": "a***e@example.test",
		"ab@example.test":    "a***b@example.test",
		"a@example.test":     "a***@example.test",
	}
	for input, want := range cases {
		if got := d.MaskEmail(input); got != want {
			t.Fatalf("masked %q = %q, want %q", input, got, want)
		}
	}
}

func TestSnapshotRestoreKeepsLiveToken(t *testing.T) {
	clk := &linkClock{now: linkBase}
	opts := linkOptions(clk)
	opts.Cooldown = 0
	d := newDispenser(clk, opts)
	first, err := d.Send("alice@example.test", "/app/orders")
	if err != nil {
		t.Fatalf("send first: %v", err)
	}
	if _, err := d.Send("bob@example.test", "/billing/invoices"); err != nil {
		t.Fatalf("send second: %v", err)
	}
	data, err := d.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload must not be empty")
	}
	restored := impl.NewMagicLinkDispenser(opts)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	links := restored.Links("alice@example.test")
	if len(links) != 1 {
		t.Fatalf("restored links = %d, want 1", len(links))
	}
	if !links[0].ExpiresAt.Equal(first.ExpiresAt) {
		t.Fatalf("restored expiry = %v, want the original %v", links[0].ExpiresAt, first.ExpiresAt)
	}
	clk.Advance(5 * time.Minute)
	session, err := restored.Redeem(first.Token, first.BindCookie)
	if err != nil {
		t.Fatalf("redeeming a restored live token: %v", err)
	}
	if session.Email != "alice@example.test" {
		t.Fatalf("restored session email = %q, want alice@example.test", session.Email)
	}
	if !session.ExpiresAt.Equal(linkBase.Add(5 * time.Minute).Add(12 * time.Hour)) {
		t.Fatalf("restored session expiry = %v, want a fresh session ttl", session.ExpiresAt)
	}
	if got := len(restored.Links("bob@example.test")); got != 1 {
		t.Fatalf("restored links for bob = %d, want 1", got)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	clk := &linkClock{now: linkBase}
	d := newDispenser(clk, linkOptions(clk))
	link, err := d.Send("alice@example.test", "/app/orders")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if err := d.Restore([]byte("}{not json at all")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := len(d.Links("alice@example.test")); got != 1 {
		t.Fatalf("rejected restore must not touch live links, got %d", got)
	}
	if _, err := d.Redeem(link.Token, link.BindCookie); err != nil {
		t.Fatalf("live link must keep working after a rejected restore, got %v", err)
	}
}
