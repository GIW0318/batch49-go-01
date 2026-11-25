package task

import (
	"errors"
	"testing"

	impl "gsb/batch49-go-fix-a-11"
)

const nowMs = int64(1000)

func testConfig() impl.Config {
	return impl.Config{
		MaxLag:               1,
		MaxRevoked:           8,
		MaxSessionsPerSerial: 2,
		Now:                  func() int64 { return nowMs },
	}
}

func newGate(t *testing.T, cfg impl.Config) *impl.MtlsRevocationGate {
	t.Helper()
	g, err := impl.NewMtlsRevocationGate(cfg)
	if err != nil {
		t.Fatalf("new revocation gate: %v", err)
	}
	return g
}

func revoke(t *testing.T, g *impl.MtlsRevocationGate, serial string) uint64 {
	t.Helper()
	epoch, err := g.PublishRevocation(impl.Revocation{Serial: serial, Issuer: "ca-a", At: nowMs})
	if err != nil {
		t.Fatalf("revoke %q: %v", serial, err)
	}
	return epoch
}

func syncWorker(t *testing.T, g *impl.MtlsRevocationGate, worker string, epoch uint64) {
	t.Helper()
	if err := g.SyncWorker(worker, epoch); err != nil {
		t.Fatalf("sync worker %q to epoch %d: %v", worker, epoch, err)
	}
}

func handshake(serial, worker string) impl.Handshake {
	return impl.Handshake{Serial: serial, Issuer: "ca-b", WorkerID: worker, NotAfter: nowMs + 60000}
}

func TestNewGateRejectsBadConfig(t *testing.T) {
	if _, err := impl.NewMtlsRevocationGate(impl.Config{MaxRevoked: 0, MaxSessionsPerSerial: 4}); !errors.Is(err, impl.ErrBadConfig) {
		t.Fatalf("zero revocation capacity must fail with ErrBadConfig, got %v", err)
	}
	if _, err := impl.NewMtlsRevocationGate(impl.Config{MaxRevoked: 4, MaxSessionsPerSerial: 0}); !errors.Is(err, impl.ErrBadConfig) {
		t.Fatalf("zero session capacity must fail with ErrBadConfig, got %v", err)
	}
}

func TestPublishRevocationBumpsEpochOnce(t *testing.T) {
	g := newGate(t, testConfig())
	if epoch := revoke(t, g, "s-1"); epoch != 1 {
		t.Fatalf("first revocation epoch = %d, want 1", epoch)
	}
	if epoch := revoke(t, g, "s-1"); epoch != 1 {
		t.Fatalf("a repeated revocation must not bump the epoch, got %d", epoch)
	}
	if got := g.Epoch(); got != 1 {
		t.Fatalf("epoch = %d, want 1", got)
	}
}

func TestPublishRejectsBlankFields(t *testing.T) {
	g := newGate(t, testConfig())
	if _, err := g.PublishRevocation(impl.Revocation{Serial: "  ", Issuer: "ca-a"}); !errors.Is(err, impl.ErrBadRevocation) {
		t.Fatalf("blank serial must fail with ErrBadRevocation, got %v", err)
	}
	if _, err := g.PublishRevocation(impl.Revocation{Serial: "s-1", Issuer: " "}); !errors.Is(err, impl.ErrBadRevocation) {
		t.Fatalf("blank issuer must fail with ErrBadRevocation, got %v", err)
	}
}

func TestAdmitRejectsRevokedSerial(t *testing.T) {
	g := newGate(t, testConfig())
	revoke(t, g, "s-1")
	syncWorker(t, g, "w-1", 1)
	ok, err := g.Admit(handshake("s-9", "w-1"))
	if err != nil {
		t.Fatalf("admit clean serial: %v", err)
	}
	if !ok.Allow || ok.Reason != impl.ReasonOK || ok.Epoch != 1 {
		t.Fatalf("clean decision = %+v, want allow at epoch 1", ok)
	}
	denied, err := g.Admit(handshake("s-1", "w-1"))
	if err != nil {
		t.Fatalf("admit revoked serial: %v", err)
	}
	if denied.Allow || denied.Reason != impl.ReasonRevoked {
		t.Fatalf("revoked decision = %+v, want deny with %s", denied, impl.ReasonRevoked)
	}
}

func TestAdmitRejectsRevokedIssuer(t *testing.T) {
	g := newGate(t, testConfig())
	epoch, err := g.RevokeIssuer("ca-a")
	if err != nil {
		t.Fatalf("revoke issuer: %v", err)
	}
	if epoch != 1 {
		t.Fatalf("issuer revocation epoch = %d, want 1", epoch)
	}
	syncWorker(t, g, "w-1", 1)
	hs := handshake("s-any", "w-1")
	hs.Issuer = "ca-a"
	got, err := g.Admit(hs)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if got.Allow || got.Reason != impl.ReasonRevoked {
		t.Fatalf("decision = %+v, want deny for a revoked issuer", got)
	}
	if g.Revoked("s-any") {
		t.Fatal("issuer revocation must not name individual serials")
	}
}

func TestAdmitRejectsExpiredCert(t *testing.T) {
	g := newGate(t, testConfig())
	hs := handshake("s-1", "w-0")
	hs.NotAfter = nowMs - 1
	got, err := g.Admit(hs)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if got.Allow || got.Reason != impl.ReasonExpired {
		t.Fatalf("decision = %+v, want deny with %s", got, impl.ReasonExpired)
	}
}

func TestAdmitStaleWorkerFailsClosed(t *testing.T) {
	g := newGate(t, testConfig())
	revoke(t, g, "s-1")
	revoke(t, g, "s-2")
	got, err := g.Admit(handshake("s-9", "w-lazy"))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if got.Allow || got.Reason != impl.ReasonStaleView {
		t.Fatalf("decision = %+v, want fail closed with %s", got, impl.ReasonStaleView)
	}
	if got.Epoch != 2 {
		t.Fatalf("decision epoch = %d, want 2", got.Epoch)
	}
}

func TestSyncWorkerRejectsRegression(t *testing.T) {
	g := newGate(t, testConfig())
	revoke(t, g, "s-1")
	revoke(t, g, "s-2")
	revoke(t, g, "s-3")
	syncWorker(t, g, "w-1", 3)
	if err := g.SyncWorker("w-1", 2); !errors.Is(err, impl.ErrEpochRegression) {
		t.Fatalf("downgrade must fail with ErrEpochRegression, got %v", err)
	}
	if got := g.AppliedEpoch("w-1"); got != 3 {
		t.Fatalf("applied epoch = %d, want 3", got)
	}
}

func TestSyncWorkerRejectsFuture(t *testing.T) {
	g := newGate(t, testConfig())
	revoke(t, g, "s-1")
	if err := g.SyncWorker("w-1", 2); !errors.Is(err, impl.ErrEpochFuture) {
		t.Fatalf("an epoch ahead of the gate must fail with ErrEpochFuture, got %v", err)
	}
	if got := g.AppliedEpoch("w-1"); got != 0 {
		t.Fatalf("rejected sync must not move the view, applied = %d", got)
	}
}

func TestRegisterSessionAndSessionsSorted(t *testing.T) {
	g := newGate(t, testConfig())
	for _, id := range []string{"sess-b", "sess-a"} {
		if err := g.RegisterSession(impl.Session{ID: id, Serial: "s-1", WorkerID: "w-1", At: nowMs}); err != nil {
			t.Fatalf("register %q: %v", id, err)
		}
	}
	got := g.SessionsFor("s-1")
	if len(got) != 2 || got[0] != "sess-a" || got[1] != "sess-b" {
		t.Fatalf("sessions = %v, want [sess-a sess-b]", got)
	}
}

func TestSessionLimitEnforced(t *testing.T) {
	cfg := testConfig()
	cfg.MaxSessionsPerSerial = 1
	g := newGate(t, cfg)
	if err := g.RegisterSession(impl.Session{ID: "sess-a", Serial: "s-1", WorkerID: "w-1", At: nowMs}); err != nil {
		t.Fatalf("register first session: %v", err)
	}
	if err := g.RegisterSession(impl.Session{ID: "sess-b", Serial: "s-1", WorkerID: "w-1", At: nowMs}); !errors.Is(err, impl.ErrSessionLimit) {
		t.Fatalf("second session must fail with ErrSessionLimit, got %v", err)
	}
	if got := g.SessionsFor("s-1"); len(got) != 1 {
		t.Fatalf("sessions = %v, want exactly one", got)
	}
}

func TestDrainSessionsReturnsAndRemoves(t *testing.T) {
	g := newGate(t, testConfig())
	for _, id := range []string{"sess-b", "sess-a"} {
		if err := g.RegisterSession(impl.Session{ID: id, Serial: "s-1", WorkerID: "w-1", At: nowMs}); err != nil {
			t.Fatalf("register %q: %v", id, err)
		}
	}
	drained, err := g.DrainSessions("s-1")
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(drained) != 2 || drained[0] != "sess-a" || drained[1] != "sess-b" {
		t.Fatalf("drained = %v, want [sess-a sess-b]", drained)
	}
	again, err := g.DrainSessions("s-1")
	if err != nil {
		t.Fatalf("second drain: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("second drain must be empty, got %v", again)
	}
}

func TestRevocationLimitEnforced(t *testing.T) {
	cfg := testConfig()
	cfg.MaxRevoked = 1
	g := newGate(t, cfg)
	revoke(t, g, "s-1")
	if _, err := g.PublishRevocation(impl.Revocation{Serial: "s-2", Issuer: "ca-a", At: nowMs}); !errors.Is(err, impl.ErrRevocationLimit) {
		t.Fatalf("second serial must fail with ErrRevocationLimit, got %v", err)
	}
	if !g.Revoked("s-1") || g.Revoked("s-2") {
		t.Fatalf("revoked set wrong: s-1=%v s-2=%v", g.Revoked("s-1"), g.Revoked("s-2"))
	}
	if got := g.Epoch(); got != 1 {
		t.Fatalf("a rejected revocation must not bump the epoch, got %d", got)
	}
}

func TestSnapshotRestoreKeepsEpochAndRevocations(t *testing.T) {
	g := newGate(t, testConfig())
	revoke(t, g, "s-1")
	revoke(t, g, "s-2")
	if err := g.RegisterSession(impl.Session{ID: "sess-a", Serial: "s-1", WorkerID: "w-1", At: nowMs}); err != nil {
		t.Fatalf("register session: %v", err)
	}
	syncWorker(t, g, "w-1", 1)
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := newGate(t, testConfig())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.Epoch(); got != 2 {
		t.Fatalf("restored epoch = %d, want 2", got)
	}
	if !restored.Revoked("s-1") || !restored.Revoked("s-2") {
		t.Fatal("the revoked serials must survive a restore")
	}
	if got := restored.AppliedEpoch("w-1"); got != 1 {
		t.Fatalf("restored worker view = %d, want 1", got)
	}
	if got := len(restored.SessionsFor("s-1")); got != 1 {
		t.Fatalf("restored sessions = %d, want 1", got)
	}
	if epoch := revoke(t, restored, "s-3"); epoch != 3 {
		t.Fatalf("the epoch must continue after restore, got %d", epoch)
	}
}

func TestRestoreRejectsGarbage(t *testing.T) {
	g := newGate(t, testConfig())
	if err := g.Restore([]byte("\"nope\"")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("garbage snapshot must fail with ErrBadSnapshot, got %v", err)
	}
}
