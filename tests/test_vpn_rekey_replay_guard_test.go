package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch49-go-fix-a-07"
)

func testConfig() impl.GuardConfig {
	return impl.GuardConfig{
		MaxEpochs:        2,
		MaxFutureEpochs:  0,
		MaxBytesPerEpoch: 1 << 20,
	}
}

func newGuard(t *testing.T, cfg impl.GuardConfig) *impl.RekeyReplayGuard {
	t.Helper()
	g, err := impl.NewRekeyReplayGuard(cfg)
	if err != nil {
		t.Fatalf("new rekey guard: %v", err)
	}
	return g
}

func installEpoch(t *testing.T, g *impl.RekeyReplayGuard, epoch uint64, keyID string) {
	t.Helper()
	if err := g.InstallEpoch(epoch, keyID); err != nil {
		t.Fatalf("install epoch %d: %v", epoch, err)
	}
}

func acceptPacket(t *testing.T, g *impl.RekeyReplayGuard, epoch, counter uint64, keyID string) impl.Verdict {
	t.Helper()
	v, err := g.AcceptPacket(impl.Packet{Epoch: epoch, Counter: counter, Bytes: 128, KeyID: keyID})
	if err != nil {
		t.Fatalf("accept packet epoch=%d counter=%d: %v", epoch, counter, err)
	}
	return v
}

func TestNewGuardRejectsInvalidConfig(t *testing.T) {
	if _, err := impl.NewRekeyReplayGuard(impl.GuardConfig{MaxEpochs: 0}); !errors.Is(err, impl.ErrBadConfig) {
		t.Fatalf("zero epoch capacity must fail with ErrBadConfig, got %v", err)
	}
	if _, err := impl.NewRekeyReplayGuard(impl.GuardConfig{MaxEpochs: 2, MaxBytesPerEpoch: -1}); !errors.Is(err, impl.ErrBadConfig) {
		t.Fatalf("negative byte budget must fail with ErrBadConfig, got %v", err)
	}
}

func TestInstallEpochRejectsStaleAndZero(t *testing.T) {
	g := newGuard(t, testConfig())
	if err := g.InstallEpoch(0, "key-zero"); !errors.Is(err, impl.ErrBadEpoch) {
		t.Fatalf("epoch zero must fail with ErrBadEpoch, got %v", err)
	}
	installEpoch(t, g, 3, "key-3")
	if err := g.InstallEpoch(3, "key-3b"); !errors.Is(err, impl.ErrEpochStale) {
		t.Fatalf("reinstalling the same epoch must fail with ErrEpochStale, got %v", err)
	}
	if err := g.InstallEpoch(2, "key-2"); !errors.Is(err, impl.ErrEpochStale) {
		t.Fatalf("going backwards must fail with ErrEpochStale, got %v", err)
	}
}

func TestInstallEpochRejectsBlankKey(t *testing.T) {
	g := newGuard(t, testConfig())
	if err := g.InstallEpoch(1, "   "); !errors.Is(err, impl.ErrBadKeyID) {
		t.Fatalf("blank key id must fail with ErrBadKeyID, got %v", err)
	}
	if got := g.ActiveEpoch(); got != 0 {
		t.Fatalf("rejected install must leave the guard empty, active epoch = %d", got)
	}
}

func TestActiveEpochTracksInstall(t *testing.T) {
	g := newGuard(t, testConfig())
	installEpoch(t, g, 1, "key-1")
	installEpoch(t, g, 2, "key-2")
	if got := g.ActiveEpoch(); got != 2 {
		t.Fatalf("active epoch = %d, want 2", got)
	}
}

func TestAcceptInOrderPackets(t *testing.T) {
	g := newGuard(t, testConfig())
	installEpoch(t, g, 1, "key-1")
	for c := uint64(1); c <= 3; c++ {
		v := acceptPacket(t, g, 1, c, "key-1")
		if v.Result != impl.VerdictAccept || v.Counter != c {
			t.Fatalf("counter %d: got %+v, want accept", c, v)
		}
	}
	if top, ok := g.WindowTop(1); !ok || top != 3 {
		t.Fatalf("window top = %d ok=%v, want 3", top, ok)
	}
	if got := g.AcceptedCount(1); got != 3 {
		t.Fatalf("accepted count = %d, want 3", got)
	}
}

func TestReplayDetectedInWindow(t *testing.T) {
	g := newGuard(t, testConfig())
	installEpoch(t, g, 1, "key-1")
	for c := uint64(1); c <= 5; c++ {
		acceptPacket(t, g, 1, c, "key-1")
	}
	v := acceptPacket(t, g, 1, 3, "key-1")
	if v.Result != impl.VerdictReplay {
		t.Fatalf("duplicate counter must be a replay, got %+v", v)
	}
	if top, _ := g.WindowTop(1); top != 5 {
		t.Fatalf("replay must not move the window, top = %d", top)
	}
	if got := g.ReplayCount("key-1"); got != 1 {
		t.Fatalf("replay count = %d, want 1", got)
	}
	if got := g.AcceptedCount(1); got != 5 {
		t.Fatalf("replay must not count as accepted, accepted = %d", got)
	}
}

func TestWindowSlidesAndOldCounterDropped(t *testing.T) {
	g := newGuard(t, testConfig())
	installEpoch(t, g, 1, "key-1")
	for c := uint64(1); c <= 70; c++ {
		acceptPacket(t, g, 1, c, "key-1")
	}
	v := acceptPacket(t, g, 1, 6, "key-1")
	if v.Result != impl.VerdictDrop || v.Reason != impl.ReasonCounterOld {
		t.Fatalf("counter below the window must drop as %q, got %+v", impl.ReasonCounterOld, v)
	}
	if top, _ := g.WindowTop(1); top != 70 {
		t.Fatalf("a dropped old counter must not move the window, top = %d", top)
	}
}

func TestLateCounterInsideWindowAccepted(t *testing.T) {
	g := newGuard(t, testConfig())
	installEpoch(t, g, 1, "key-1")
	for _, c := range []uint64{1, 2, 3, 5, 6, 7} {
		acceptPacket(t, g, 1, c, "key-1")
	}
	v := acceptPacket(t, g, 1, 4, "key-1")
	if v.Result != impl.VerdictAccept {
		t.Fatalf("a gap filling counter must be accepted, got %+v", v)
	}
	if top, _ := g.WindowTop(1); top != 7 {
		t.Fatalf("late accept must keep the window top, top = %d", top)
	}
}

func TestUnknownEpochDrop(t *testing.T) {
	g := newGuard(t, testConfig())
	installEpoch(t, g, 1, "key-1")
	v, err := g.AcceptPacket(impl.Packet{Epoch: 9, Counter: 1, Bytes: 64, KeyID: "key-9"})
	if err != nil {
		t.Fatalf("unknown epoch must be a verdict, not an error: %v", err)
	}
	if v.Result != impl.VerdictDrop || v.Reason != impl.ReasonEpochUnknown {
		t.Fatalf("unknown epoch verdict = %+v, want drop/%s", v, impl.ReasonEpochUnknown)
	}
}

func TestRetireEpochBlocksPackets(t *testing.T) {
	g := newGuard(t, testConfig())
	installEpoch(t, g, 1, "key-1")
	installEpoch(t, g, 2, "key-2")
	if err := g.RetireEpoch(1); err != nil {
		t.Fatalf("retire epoch 1: %v", err)
	}
	v := acceptPacket(t, g, 1, 1, "key-1")
	if v.Result != impl.VerdictDrop || v.Reason != impl.ReasonEpochRetired {
		t.Fatalf("retired epoch verdict = %+v, want drop/%s", v, impl.ReasonEpochRetired)
	}
	if err := g.RetireEpoch(2); !errors.Is(err, impl.ErrActiveEpoch) {
		t.Fatalf("retiring the active epoch must fail with ErrActiveEpoch, got %v", err)
	}
	if err := g.RetireEpoch(1); err != nil {
		t.Fatalf("retiring an already retired epoch must be a no-op, got %v", err)
	}
}

func TestNextCounterPerEpoch(t *testing.T) {
	g := newGuard(t, testConfig())
	installEpoch(t, g, 1, "key-1")
	for want := uint64(1); want <= 2; want++ {
		got, err := g.NextCounter(1)
		if err != nil {
			t.Fatalf("next counter: %v", err)
		}
		if got != want {
			t.Fatalf("next counter = %d, want %d", got, want)
		}
	}
	installEpoch(t, g, 2, "key-2")
	got, err := g.NextCounter(2)
	if err != nil {
		t.Fatalf("next counter of the new epoch: %v", err)
	}
	if got != 1 {
		t.Fatalf("a new epoch must start its send counter at 1, got %d", got)
	}
	if _, err := g.NextCounter(9); !errors.Is(err, impl.ErrEpochUnknown) {
		t.Fatalf("unknown epoch must fail with ErrEpochUnknown, got %v", err)
	}
}

func TestSnapshotRestoreKeepsWindow(t *testing.T) {
	g := newGuard(t, testConfig())
	installEpoch(t, g, 1, "key-1")
	for c := uint64(1); c <= 4; c++ {
		acceptPacket(t, g, 1, c, "key-1")
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := newGuard(t, testConfig())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	v := acceptPacket(t, restored, 1, 2, "key-1")
	if v.Result != impl.VerdictReplay {
		t.Fatalf("after restore the old counter must stay a replay, got %+v", v)
	}
	if top, ok := restored.WindowTop(1); !ok || top != 4 {
		t.Fatalf("restored window top = %d ok=%v, want 4", top, ok)
	}
	if err := restored.InstallEpoch(1, "key-1"); !errors.Is(err, impl.ErrEpochStale) {
		t.Fatalf("restored active epoch must be preserved, got %v", err)
	}
}

func TestRestoreRejectsGarbage(t *testing.T) {
	g := newGuard(t, testConfig())
	if err := g.Restore([]byte("{not json")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("garbage snapshot must fail with ErrBadSnapshot, got %v", err)
	}
}

func TestEpochTrimRetiresOldest(t *testing.T) {
	g := newGuard(t, testConfig())
	installEpoch(t, g, 1, "key-1")
	installEpoch(t, g, 2, "key-2")
	installEpoch(t, g, 3, "key-3")
	states := g.Epochs()
	if len(states) != 3 {
		t.Fatalf("retained epochs = %d, want 3 (one tombstone plus two active)", len(states))
	}
	if states[0].Epoch != 1 || !states[0].Retired {
		t.Fatalf("oldest epoch must become a retired tombstone, got %+v", states[0])
	}
	v := acceptPacket(t, g, 1, 1, "key-1")
	if v.Result != impl.VerdictDrop || v.Reason != impl.ReasonEpochRetired {
		t.Fatalf("trimmed epoch verdict = %+v, want drop/%s", v, impl.ReasonEpochRetired)
	}
	if got := g.ActiveEpoch(); got != 3 {
		t.Fatalf("active epoch = %d, want 3", got)
	}
}

func TestConcurrentSameCounterSingleWinner(t *testing.T) {
	g := newGuard(t, testConfig())
	installEpoch(t, g, 1, "key-1")
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepts, replays := 0, 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := g.AcceptPacket(impl.Packet{Epoch: 1, Counter: 1, Bytes: 32, KeyID: "key-1"})
			if err != nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			switch v.Result {
			case impl.VerdictAccept:
				accepts++
			case impl.VerdictReplay:
				replays++
			}
		}()
	}
	wg.Wait()
	if accepts != 1 || replays != 7 {
		t.Fatalf("want exactly one accept and seven replays, got accepts=%d replays=%d", accepts, replays)
	}
}
