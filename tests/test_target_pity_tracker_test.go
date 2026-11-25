package task

import (
	"errors"
	"reflect"
	"sync"
	"testing"

	impl "gsb/batch49-go-game-a-09"
)

func newTracker(t *testing.T, opts impl.Options) *impl.TargetPityTracker {
	t.Helper()
	tracker := impl.NewTargetPityTracker(opts)
	if tracker == nil {
		t.Fatal("NewTargetPityTracker returned nil")
	}
	return tracker
}

func mustSelect(t *testing.T, tracker *impl.TargetPityTracker, player, pool, target string) {
	t.Helper()
	if err := tracker.SelectTarget(player, pool, target); err != nil {
		t.Fatalf("SelectTarget(%q, %q, %q) return error: %v", player, pool, target, err)
	}
}

func mustRoll(t *testing.T, tracker *impl.TargetPityTracker, player, pool, hit string) impl.PityState {
	t.Helper()
	state, err := tracker.OnRoll(player, pool, hit)
	if err != nil {
		t.Fatalf("OnRoll(%q, %q, %q) return error: %v", player, pool, hit, err)
	}
	return state
}

func TestNewTrackerDefaults(t *testing.T) {
	tracker := newTracker(t, impl.Options{})
	if got := tracker.Threshold(); got != 80 {
		t.Fatalf("default threshold = %d, want 80", got)
	}
	num, den := tracker.Ratio()
	if num != 1 || den != 2 {
		t.Fatalf("default conversion ratio = %d/%d, want 1/2", num, den)
	}
}

func TestSelectTargetStoresTarget(t *testing.T) {
	tracker := newTracker(t, impl.Options{})
	mustSelect(t, tracker, "alice", "pool-a", "sword")
	if got := tracker.Target("alice", "pool-a"); got != "sword" {
		t.Fatalf("target = %q, want sword", got)
	}
}

func TestSelectTargetRejectsEmptyTarget(t *testing.T) {
	tracker := newTracker(t, impl.Options{})
	if err := tracker.SelectTarget("alice", "pool-a", ""); !errors.Is(err, impl.ErrInvalidTarget) {
		t.Fatalf("empty target error = %v, want ErrInvalidTarget", err)
	}
	if err := tracker.SelectTarget("", "pool-a", "sword"); !errors.Is(err, impl.ErrInvalidTarget) {
		t.Fatalf("empty player error = %v, want ErrInvalidTarget", err)
	}
}

func TestOnRollBeforeSelectRejected(t *testing.T) {
	tracker := newTracker(t, impl.Options{})
	if _, err := tracker.OnRoll("alice", "pool-a", "sword"); !errors.Is(err, impl.ErrNoTarget) {
		t.Fatalf("roll without target error = %v, want ErrNoTarget", err)
	}
}

func TestMissIncrementsWatermark(t *testing.T) {
	tracker := newTracker(t, impl.Options{Threshold: 10})
	mustSelect(t, tracker, "alice", "pool-a", "sword")
	for i := 0; i < 3; i++ {
		mustRoll(t, tracker, "alice", "pool-a", "shield")
	}
	if got := tracker.Watermark("alice", "pool-a"); got != 3 {
		t.Fatalf("watermark = %d, want 3", got)
	}
}

func TestHitResetsWatermark(t *testing.T) {
	tracker := newTracker(t, impl.Options{Threshold: 10})
	mustSelect(t, tracker, "alice", "pool-a", "sword")
	for i := 0; i < 3; i++ {
		mustRoll(t, tracker, "alice", "pool-a", "shield")
	}
	state := mustRoll(t, tracker, "alice", "pool-a", "sword")
	if state.Watermark != 0 {
		t.Fatalf("watermark after target hit = %d, want 0", state.Watermark)
	}
	if got := tracker.Watermark("alice", "pool-a"); got != 0 {
		t.Fatalf("stored watermark after target hit = %d, want 0", got)
	}
}

func TestThresholdForcesTarget(t *testing.T) {
	tracker := newTracker(t, impl.Options{Threshold: 3})
	mustSelect(t, tracker, "alice", "pool-a", "sword")
	mustRoll(t, tracker, "alice", "pool-a", "shield")
	mustRoll(t, tracker, "alice", "pool-a", "shield")
	state := mustRoll(t, tracker, "alice", "pool-a", "shield")
	if !state.Forced {
		t.Fatalf("third miss state = %+v, want forced", state)
	}
	if state.Watermark != 0 {
		t.Fatalf("watermark after force = %d, want 0", state.Watermark)
	}
	if !tracker.Forced("alice", "pool-a") {
		t.Fatal("Forced() = false, want true after forced roll")
	}
}

func TestAfterForceWatermarkRestarts(t *testing.T) {
	tracker := newTracker(t, impl.Options{Threshold: 3})
	mustSelect(t, tracker, "alice", "pool-a", "sword")
	for i := 0; i < 3; i++ {
		mustRoll(t, tracker, "alice", "pool-a", "shield")
	}
	state := mustRoll(t, tracker, "alice", "pool-a", "shield")
	if state.Watermark != 1 {
		t.Fatalf("watermark after forced cycle = %d, want 1", state.Watermark)
	}
	if state.Forced {
		t.Fatalf("state after forced cycle = %+v, want not forced", state)
	}
	if tracker.Forced("alice", "pool-a") {
		t.Fatal("Forced() = true, want false after a normal miss")
	}
}

func TestNormalPityCountsMisses(t *testing.T) {
	tracker := newTracker(t, impl.Options{Threshold: 10})
	mustSelect(t, tracker, "alice", "pool-a", "sword")
	for i := 0; i < 3; i++ {
		mustRoll(t, tracker, "alice", "pool-a", "shield")
	}
	if got := tracker.NormalPity("alice", "pool-a"); got != 3 {
		t.Fatalf("normal pity = %d, want 3", got)
	}
}

func TestNormalPityResetsOnTargetHit(t *testing.T) {
	tracker := newTracker(t, impl.Options{Threshold: 10})
	mustSelect(t, tracker, "alice", "pool-a", "sword")
	for i := 0; i < 3; i++ {
		mustRoll(t, tracker, "alice", "pool-a", "shield")
	}
	mustRoll(t, tracker, "alice", "pool-a", "sword")
	if got := tracker.NormalPity("alice", "pool-a"); got != 0 {
		t.Fatalf("normal pity after target hit = %d, want 0", got)
	}
}

func TestSwitchTargetConvertsWatermark(t *testing.T) {
	tracker := newTracker(t, impl.Options{})
	mustSelect(t, tracker, "alice", "pool-a", "sword")
	for i := 0; i < 10; i++ {
		mustRoll(t, tracker, "alice", "pool-a", "shield")
	}
	state, err := tracker.SwitchTarget("alice", "pool-a", "axe")
	if err != nil {
		t.Fatalf("SwitchTarget return error: %v", err)
	}
	if state.Target != "axe" {
		t.Fatalf("switched target = %q, want axe", state.Target)
	}
	if state.Watermark != 5 {
		t.Fatalf("converted watermark = %d, want 5", state.Watermark)
	}
	if got := tracker.Target("alice", "pool-a"); got != "axe" {
		t.Fatalf("stored target = %q, want axe", got)
	}
}

func TestSwitchTargetRecordsConversion(t *testing.T) {
	tracker := newTracker(t, impl.Options{})
	mustSelect(t, tracker, "alice", "pool-a", "sword")
	for i := 0; i < 10; i++ {
		mustRoll(t, tracker, "alice", "pool-a", "shield")
	}
	if _, err := tracker.SwitchTarget("alice", "pool-a", "axe"); err != nil {
		t.Fatalf("first SwitchTarget return error: %v", err)
	}
	if got := tracker.Conversions("alice", "pool-a"); got != 1 {
		t.Fatalf("conversion count = %d, want 1", got)
	}
	if _, err := tracker.SwitchTarget("alice", "pool-a", "bow"); err != nil {
		t.Fatalf("second SwitchTarget return error: %v", err)
	}
	if got := tracker.Conversions("alice", "pool-a"); got != 2 {
		t.Fatalf("conversion count after second switch = %d, want 2", got)
	}
}

func TestUnknownPlayerAndPoolListing(t *testing.T) {
	tracker := newTracker(t, impl.Options{})
	if got := tracker.Watermark("nobody", "pool-a"); got != 0 {
		t.Fatalf("unknown player watermark = %d, want 0", got)
	}
	mustSelect(t, tracker, "alice", "pool-b", "sword")
	mustSelect(t, tracker, "alice", "pool-a", "axe")
	if got := tracker.Pools("alice"); !reflect.DeepEqual(got, []string{"pool-a", "pool-b"}) {
		t.Fatalf("pools = %v, want [pool-a pool-b]", got)
	}
}

func TestSnapshotRestoreKeepsWatermark(t *testing.T) {
	tracker := newTracker(t, impl.Options{})
	mustSelect(t, tracker, "alice", "pool-a", "sword")
	for i := 0; i < 7; i++ {
		mustRoll(t, tracker, "alice", "pool-a", "shield")
	}
	data, err := tracker.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot return error: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("Snapshot returned empty payload")
	}
	restored := impl.NewTargetPityTracker(impl.Options{})
	if err := restored.Restore(data); err != nil {
		t.Fatalf("Restore return error: %v", err)
	}
	if got := restored.Watermark("alice", "pool-a"); got != 7 {
		t.Fatalf("restored watermark = %d, want 7", got)
	}
	if got := restored.Target("alice", "pool-a"); got != "sword" {
		t.Fatalf("restored target = %q, want sword", got)
	}
}

func TestConcurrentMissesSerialize(t *testing.T) {
	tracker := newTracker(t, impl.Options{Threshold: 1000})
	mustSelect(t, tracker, "alice", "pool-a", "sword")
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := tracker.OnRoll("alice", "pool-a", "shield"); err != nil {
				t.Errorf("concurrent OnRoll return error: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := tracker.Watermark("alice", "pool-a"); got != 100 {
		t.Fatalf("watermark after 100 concurrent misses = %d, want 100", got)
	}
}
