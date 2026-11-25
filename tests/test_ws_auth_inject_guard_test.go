package task

import (
	"errors"
	"testing"

	impl "gsb/batch49-go-fix-a-12"
)

const nowMs = int64(1000)

func testConfig() impl.Config {
	return impl.Config{
		AllowedOrigins:  []string{"https://im.app.example"},
		MaxMessageBytes: 1024,
		MaxDepth:        3,
		MaxKeyLen:       16,
		AuthTimeoutMs:   30000,
		TicketTTL:       60000,
		MaxConns:        8,
		RateLimit:       100,
		RateWindowMs:    1000,
		Now:             func() int64 { return nowMs },
	}
}

func newGuard(t *testing.T, cfg impl.Config) *impl.WsAuthInjectGuard {
	t.Helper()
	g, err := impl.NewWsAuthInjectGuard(cfg)
	if err != nil {
		t.Fatalf("new websocket guard: %v", err)
	}
	return g
}

func openConn(t *testing.T, g *impl.WsAuthInjectGuard, connID, origin string) string {
	t.Helper()
	nonce, err := g.Open(connID, origin)
	if err != nil {
		t.Fatalf("open %q: %v", connID, err)
	}
	return nonce
}

func makeTicket(connID, nonce, subject string) impl.Ticket {
	return impl.Ticket{
		Value:     "tk-" + connID,
		ConnID:    connID,
		Nonce:     nonce,
		Subject:   subject,
		IssuedAt:  nowMs,
		ExpiresAt: nowMs + 10000,
	}
}

func authConn(t *testing.T, g *impl.WsAuthInjectGuard, connID, subject string) impl.Ticket {
	t.Helper()
	nonce := openConn(t, g, connID, "https://im.app.example")
	ticket := makeTicket(connID, nonce, subject)
	if _, err := g.Authenticate(connID, ticket); err != nil {
		t.Fatalf("authenticate %q: %v", connID, err)
	}
	return ticket
}

func TestNewGuardRejectsBadConfig(t *testing.T) {
	bad := testConfig()
	bad.MaxMessageBytes = 0
	if _, err := impl.NewWsAuthInjectGuard(bad); !errors.Is(err, impl.ErrBadConfig) {
		t.Fatalf("zero message budget must fail with ErrBadConfig, got %v", err)
	}
	noOrigins := testConfig()
	noOrigins.AllowedOrigins = nil
	if _, err := impl.NewWsAuthInjectGuard(noOrigins); !errors.Is(err, impl.ErrBadConfig) {
		t.Fatalf("empty origin list must fail with ErrBadConfig, got %v", err)
	}
}

func TestOpenRejectsOriginNotAllowed(t *testing.T) {
	g := newGuard(t, testConfig())
	if _, err := g.Open("c1", "https://evil.example"); !errors.Is(err, impl.ErrOriginDenied) {
		t.Fatalf("foreign origin must fail with ErrOriginDenied, got %v", err)
	}
	if _, err := g.State("c1"); !errors.Is(err, impl.ErrUnknownConn) {
		t.Fatalf("a rejected open must not register the connection, got %v", err)
	}
}

func TestOpenNormalizesOriginAndRejectsDuplicate(t *testing.T) {
	cfg := testConfig()
	cfg.AllowedOrigins = []string{"https://IM.App.Example:443/"}
	g := newGuard(t, cfg)
	nonce := openConn(t, g, "c1", "https://im.app.example")
	if nonce == "" {
		t.Fatal("open must return a non-empty nonce")
	}
	if _, err := g.Open("c1", "https://im.app.example"); !errors.Is(err, impl.ErrConnOpen) {
		t.Fatalf("duplicate open must fail with ErrConnOpen, got %v", err)
	}
	if st, err := g.State("c1"); err != nil || st != impl.StateOpen {
		t.Fatalf("state = %q err=%v, want open", st, err)
	}
}

func TestAuthenticateRequiresMatchingNonce(t *testing.T) {
	g := newGuard(t, testConfig())
	nonce := openConn(t, g, "c1", "https://im.app.example")
	ticket := makeTicket("c1", nonce, "alice")
	ticket.Nonce = "wrong-nonce"
	if _, err := g.Authenticate("c1", ticket); !errors.Is(err, impl.ErrTicketMismatch) {
		t.Fatalf("a foreign nonce must fail with ErrTicketMismatch, got %v", err)
	}
}

func TestAuthenticateRejectsExpiredTicket(t *testing.T) {
	g := newGuard(t, testConfig())
	nonce := openConn(t, g, "c1", "https://im.app.example")
	ticket := makeTicket("c1", nonce, "alice")
	ticket.ExpiresAt = nowMs - 1
	if _, err := g.Authenticate("c1", ticket); !errors.Is(err, impl.ErrTicketExpired) {
		t.Fatalf("an expired ticket must fail with ErrTicketExpired, got %v", err)
	}
}

func TestTicketSingleUse(t *testing.T) {
	g := newGuard(t, testConfig())
	ticket := authConn(t, g, "c1", "alice")
	if !g.UsedTicket(ticket.Value) {
		t.Fatal("a consumed ticket must be reported as used")
	}
	if _, err := g.Authenticate("c1", ticket); !errors.Is(err, impl.ErrTicketReused) {
		t.Fatalf("reusing a ticket must fail with ErrTicketReused, got %v", err)
	}
}

func TestInboundBeforeAuthDenied(t *testing.T) {
	g := newGuard(t, testConfig())
	openConn(t, g, "c1", "https://im.app.example")
	if _, err := g.Inbound("c1", 1, []byte(`{"k":"v"}`)); !errors.Is(err, impl.ErrNotAuthenticated) {
		t.Fatalf("inbound before auth must fail with ErrNotAuthenticated, got %v", err)
	}
}

func TestInboundAfterAuthAccepted(t *testing.T) {
	g := newGuard(t, testConfig())
	authConn(t, g, "c1", "alice")
	msg, err := g.Inbound("c1", 1, []byte(`{"k":"v"}`))
	if err != nil {
		t.Fatalf("inbound after auth: %v", err)
	}
	if msg.Seq != 1 || msg.Kind != impl.KindText || msg.Text != `{"k":"v"}` {
		t.Fatalf("message = %+v, want seq 1 text payload", msg)
	}
	if st, err := g.State("c1"); err != nil || st != impl.StateAuthenticated {
		t.Fatalf("state = %q err=%v, want authenticated", st, err)
	}
}

func TestInboundRejectsHeaderInjection(t *testing.T) {
	g := newGuard(t, testConfig())
	authConn(t, g, "c1", "alice")
	payload := []byte("{\"k\":\"a\r\nX-Injected: 1\"}")
	if _, err := g.Inbound("c1", 1, payload); !errors.Is(err, impl.ErrHeaderInjection) {
		t.Fatalf("crlf in a payload must fail with ErrHeaderInjection, got %v", err)
	}
}

func TestInboundRejectsTooDeep(t *testing.T) {
	g := newGuard(t, testConfig())
	authConn(t, g, "c1", "alice")
	payload := []byte(`{"a":{"b":{"c":{"d":1}}}}`)
	if _, err := g.Inbound("c1", 1, payload); !errors.Is(err, impl.ErrTooDeep) {
		t.Fatalf("nesting deeper than the limit must fail with ErrTooDeep, got %v", err)
	}
}

func TestInboundRejectsReservedField(t *testing.T) {
	g := newGuard(t, testConfig())
	authConn(t, g, "c1", "alice")
	if _, err := g.Inbound("c1", 1, []byte(`{"__proto__":"x"}`)); !errors.Is(err, impl.ErrReservedField) {
		t.Fatalf("a reserved field must fail with ErrReservedField, got %v", err)
	}
}

func TestInboundRejectsBadSequence(t *testing.T) {
	g := newGuard(t, testConfig())
	authConn(t, g, "c1", "alice")
	if _, err := g.Inbound("c1", 2, []byte(`{"k":"v"}`)); !errors.Is(err, impl.ErrBadSequence) {
		t.Fatalf("skipping a sequence number must fail with ErrBadSequence, got %v", err)
	}
	if st, err := g.State("c1"); err != nil || st != impl.StateClosed {
		t.Fatalf("state after a bad sequence = %q err=%v, want closed", st, err)
	}
	if _, err := g.Inbound("c1", 3, []byte(`{"k":"v"}`)); !errors.Is(err, impl.ErrConnClosed) {
		t.Fatalf("inbound on a closed connection must fail with ErrConnClosed, got %v", err)
	}
}

func TestOutboundEscapesMarkup(t *testing.T) {
	g := newGuard(t, testConfig())
	authConn(t, g, "c1", "alice")
	out, err := g.Outbound("c1", impl.Message{Seq: 1, Kind: impl.KindText, Text: `<script>&"x"`})
	if err != nil {
		t.Fatalf("outbound: %v", err)
	}
	want := `&lt;script&gt;&amp;&quot;x&quot;`
	if string(out) != want {
		t.Fatalf("outbound = %q, want %q", string(out), want)
	}
}

func TestRevokeSubjectClosesConnections(t *testing.T) {
	g := newGuard(t, testConfig())
	authConn(t, g, "c1", "alice")
	nonce := openConn(t, g, "c2", "https://im.app.example")
	if _, err := g.Authenticate("c2", makeTicket("c2", nonce, "alice")); err != nil {
		t.Fatalf("authenticate c2: %v", err)
	}
	if n := g.RevokeSubject("alice"); n != 2 {
		t.Fatalf("revoked connections = %d, want 2", n)
	}
	if _, err := g.Inbound("c1", 1, []byte(`{"k":"v"}`)); !errors.Is(err, impl.ErrRevoked) {
		t.Fatalf("inbound after revocation must fail with ErrRevoked, got %v", err)
	}
	if n := g.RevokeSubject("alice"); n != 0 {
		t.Fatalf("a second revocation must affect nothing, got %d", n)
	}
}

func TestSnapshotRestoreKeepsUsedTickets(t *testing.T) {
	g := newGuard(t, testConfig())
	ticket := authConn(t, g, "c1", "alice")
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := newGuard(t, testConfig())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if st, err := restored.State("c1"); err != nil || st != impl.StateAuthenticated {
		t.Fatalf("restored state = %q err=%v, want authenticated", st, err)
	}
	if !restored.UsedTicket(ticket.Value) {
		t.Fatal("the used ticket set must survive a restore")
	}
	if _, err := restored.Authenticate("c1", ticket); !errors.Is(err, impl.ErrTicketReused) {
		t.Fatalf("a restored used ticket must fail with ErrTicketReused, got %v", err)
	}
}
