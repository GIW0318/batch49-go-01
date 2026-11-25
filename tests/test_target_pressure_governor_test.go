package task

import (
	"errors"
	"sync"
	"testing"
	"time"

	impl "gsb/batch49-go-safety-e-04"
)

func testConfig() impl.Config {
	return impl.Config{
		MaxConcurrency:   10,
		BaseDelay:        100 * time.Millisecond,
		MaxDelay:         2 * time.Second,
		PenaltyThreshold: 20,
		RecoveryStreak:   3,
		MaxTargets:       8,
	}
}

func okOutcome() impl.Outcome {
	return impl.Outcome{Kind: impl.OutcomeOK, Bytes: 2048, Duration: 20 * time.Millisecond, At: time.Date(2026, time.November, 16, 10, 0, 0, 0, time.UTC)}
}

func throttleOutcome() impl.Outcome {
	return impl.Outcome{Kind: impl.OutcomeThrottled, Bytes: 0, At: time.Date(2026, time.November, 16, 10, 1, 0, 0, time.UTC)}
}

func errorOutcome() impl.Outcome {
	return impl.Outcome{Kind: impl.OutcomeError, Bytes: 0, At: time.Date(2026, time.November, 16, 10, 2, 0, 0, time.UTC)}
}

func newGovernor(t *testing.T, cfg impl.Config) *impl.TargetPressureGovernor {
	t.Helper()
	g := impl.NewTargetPressureGovernor(cfg)
	if g == nil {
		t.Fatalf("constructor returned nil")
	}
	return g
}

func TestReportRejectsBlankTarget(t *testing.T) {
	g := newGovernor(t, testConfig())
	if err := g.Report("   ", okOutcome()); !errors.Is(err, impl.ErrBadTarget) {
		t.Fatalf("blank target must report ErrBadTarget, got %v", err)
	}
	if got := len(g.Targets()); got != 0 {
		t.Fatalf("rejected report must not track a target, got %d", got)
	}
}

func TestReportRejectsUnknownKind(t *testing.T) {
	g := newGovernor(t, testConfig())
	bad := impl.Outcome{Kind: "vibes", At: time.Date(2026, time.November, 16, 10, 0, 0, 0, time.UTC)}
	if err := g.Report("edge-1", bad); !errors.Is(err, impl.ErrBadOutcome) {
		t.Fatalf("unknown outcome kind must report ErrBadOutcome, got %v", err)
	}
}

func TestNewTargetStartsHealthy(t *testing.T) {
	g := newGovernor(t, testConfig())
	if got := g.Health("edge-1"); got != impl.HealthMax {
		t.Fatalf("fresh health = %d, want %d", got, impl.HealthMax)
	}
	if got := g.Concurrency("edge-1"); got != 10 {
		t.Fatalf("fresh concurrency = %d, want the configured 10", got)
	}
	if got := g.Delay("edge-1"); got != 100*time.Millisecond {
		t.Fatalf("fresh delay = %v, want BaseDelay", got)
	}
	if g.Penalized("edge-1") {
		t.Fatalf("fresh target must not be penalized")
	}
	ok, err := g.Permit("edge-1")
	if err != nil || !ok {
		t.Fatalf("fresh target must grant a permit, got ok=%v err=%v", ok, err)
	}
}

func TestThrottleDropsHealthAndPenalizes(t *testing.T) {
	g := newGovernor(t, testConfig())
	for i := 0; i < 2; i++ {
		if err := g.Report("edge-2", throttleOutcome()); err != nil {
			t.Fatalf("throttle report %d: %v", i, err)
		}
	}
	if got := g.Health("edge-2"); got != 50 {
		t.Fatalf("health after two throttles = %d, want 50", got)
	}
	if !g.Penalized("edge-2") {
		t.Fatalf("two consecutive throttles must enter the penalty zone")
	}
	if got := g.Concurrency("edge-2"); got != 1 {
		t.Fatalf("penalty concurrency = %d, want 1", got)
	}
}

func TestDelayGrowsWithBadHealth(t *testing.T) {
	g := newGovernor(t, testConfig())
	for i := 0; i < 2; i++ {
		if err := g.Report("edge-3", errorOutcome()); err != nil {
			t.Fatalf("error report %d: %v", i, err)
		}
	}
	if got := g.Health("edge-3"); got != 70 {
		t.Fatalf("health after two errors = %d, want 70", got)
	}
	if got := g.Delay("edge-3"); got != 200*time.Millisecond {
		t.Fatalf("delay at health 70 = %v, want twice BaseDelay", got)
	}
	if got := g.Concurrency("edge-3"); got != 7 {
		t.Fatalf("concurrency at health 70 = %d, want 7", got)
	}
}

func TestStateReportsPosture(t *testing.T) {
	g := newGovernor(t, testConfig())
	if err := g.Report("edge-10", errorOutcome()); err != nil {
		t.Fatalf("error report: %v", err)
	}
	state, err := g.State("edge-10")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if state.Target != "edge-10" {
		t.Fatalf("state target = %q, want edge-10", state.Target)
	}
	if state.Health != 85 {
		t.Fatalf("state health = %d, want 85", state.Health)
	}
	if state.Concurrency != 8 {
		t.Fatalf("state concurrency = %d, want 8", state.Concurrency)
	}
	if state.InFlight != 0 {
		t.Fatalf("state in flight = %d, want 0", state.InFlight)
	}
	if ok, err := g.Permit("edge-10"); err != nil || !ok {
		t.Fatalf("permit = %v/%v, want granted", ok, err)
	}
	state, err = g.State("edge-10")
	if err != nil {
		t.Fatalf("state after permit: %v", err)
	}
	if state.InFlight != 1 {
		t.Fatalf("state in flight after permit = %d, want 1", state.InFlight)
	}
}

func TestHealthFloorAndCeiling(t *testing.T) {
	g := newGovernor(t, testConfig())
	for i := 0; i < 7; i++ {
		if err := g.Report("edge-4", errorOutcome()); err != nil {
			t.Fatalf("error report %d: %v", i, err)
		}
	}
	if got := g.Health("edge-4"); got != impl.HealthMin {
		t.Fatalf("health floor = %d, want %d", got, impl.HealthMin)
	}
	if got := g.Concurrency("edge-4"); got != 1 {
		t.Fatalf("floor concurrency = %d, want 1", got)
	}
	if err := g.Report("edge-5", errorOutcome()); err != nil {
		t.Fatalf("error report: %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := g.Report("edge-5", okOutcome()); err != nil {
			t.Fatalf("ok report %d: %v", i, err)
		}
	}
	if got := g.Health("edge-5"); got != impl.HealthMax {
		t.Fatalf("health ceiling = %d, want %d", got, impl.HealthMax)
	}
}

func TestPermitEnforcesConcurrencyCap(t *testing.T) {
	cfg := testConfig()
	cfg.MaxConcurrency = 3
	g := newGovernor(t, cfg)
	granted := 0
	for i := 0; i < 5; i++ {
		ok, err := g.Permit("edge-6")
		if err != nil {
			t.Fatalf("permit %d: %v", i, err)
		}
		if ok {
			granted++
		}
	}
	if granted != 3 {
		t.Fatalf("granted permits = %d, want the cap 3", granted)
	}
}

func TestPermitAtomicUnderConcurrency(t *testing.T) {
	cfg := testConfig()
	cfg.MaxConcurrency = 4
	g := newGovernor(t, cfg)
	if got := g.Concurrency("edge-7"); got != 4 {
		t.Fatalf("concurrency = %d, want 4", got)
	}
	const workers = 8
	var mu sync.Mutex
	granted := 0
	failures := 0
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := g.Permit("edge-7")
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failures++
				return
			}
			if ok {
				granted++
			}
		}()
	}
	wg.Wait()
	if failures != 0 {
		t.Fatalf("permit errors = %d, want 0", failures)
	}
	if granted != 4 {
		t.Fatalf("concurrent permits granted = %d, want exactly the cap 4", granted)
	}
}

func TestReleaseFreesSlot(t *testing.T) {
	cfg := testConfig()
	cfg.MaxConcurrency = 1
	g := newGovernor(t, cfg)
	if ok, err := g.Permit("edge-8"); err != nil || !ok {
		t.Fatalf("first permit = %v/%v, want granted", ok, err)
	}
	if ok, err := g.Permit("edge-8"); err != nil || ok {
		t.Fatalf("second permit = %v/%v, want refused", ok, err)
	}
	if err := g.Release("edge-8"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if ok, err := g.Permit("edge-8"); err != nil || !ok {
		t.Fatalf("permit after release = %v/%v, want granted", ok, err)
	}
}

func TestIsolationBetweenTargets(t *testing.T) {
	g := newGovernor(t, testConfig())
	for i := 0; i < 2; i++ {
		if err := g.Report("bad-host", throttleOutcome()); err != nil {
			t.Fatalf("throttle bad-host: %v", err)
		}
	}
	if got := g.Concurrency("bad-host"); got != 1 {
		t.Fatalf("bad-host concurrency = %d, want 1", got)
	}
	if got := g.Concurrency("quiet-host"); got != 10 {
		t.Fatalf("quiet-host concurrency = %d, want the untouched cap 10", got)
	}
	if got := g.Health("quiet-host"); got != impl.HealthMax {
		t.Fatalf("quiet-host health = %d, want %d", got, impl.HealthMax)
	}
}

func TestPenaltyExitIsGradual(t *testing.T) {
	g := newGovernor(t, testConfig())
	for i := 0; i < 2; i++ {
		if err := g.Report("edge-9", throttleOutcome()); err != nil {
			t.Fatalf("throttle: %v", err)
		}
	}
	if !g.Penalized("edge-9") {
		t.Fatalf("target must start inside the penalty zone")
	}
	for i := 0; i < 3; i++ {
		if err := g.Report("edge-9", okOutcome()); err != nil {
			t.Fatalf("recovery probe %d: %v", i, err)
		}
	}
	if g.Penalized("edge-9") {
		t.Fatalf("three clean probes must leave the penalty zone")
	}
	got := g.Concurrency("edge-9")
	if got <= 0 || got >= 10 {
		t.Fatalf("recovered concurrency = %d, want a middle tier between 1 and the cap", got)
	}
}

func TestTargetCapEvictsLeastRecent(t *testing.T) {
	cfg := testConfig()
	cfg.MaxTargets = 2
	g := newGovernor(t, cfg)
	for _, target := range []string{"t-1", "t-2", "t-3"} {
		if err := g.Report(target, okOutcome()); err != nil {
			t.Fatalf("report %s: %v", target, err)
		}
	}
	got := g.Targets()
	if len(got) != 2 {
		t.Fatalf("tracked targets = %v, want the cap of 2", got)
	}
	if got[0] != "t-2" || got[1] != "t-3" {
		t.Fatalf("tracked targets = %v, want the two most recent ones", got)
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	g := newGovernor(t, testConfig())
	for i := 0; i < 2; i++ {
		if err := g.Report("p-1", throttleOutcome()); err != nil {
			t.Fatalf("throttle: %v", err)
		}
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	fresh := newGovernor(t, testConfig())
	if err := fresh.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !fresh.Penalized("p-1") {
		t.Fatalf("restored target must still be penalized")
	}
	if got := fresh.Health("p-1"); got != 50 {
		t.Fatalf("restored health = %d, want 50", got)
	}
	if got := fresh.Concurrency("p-1"); got != 1 {
		t.Fatalf("restored concurrency = %d, want 1", got)
	}
	if err := fresh.Restore([]byte("broken")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must report ErrBadSnapshot, got %v", err)
	}
	if got := fresh.Health("p-1"); got != 50 {
		t.Fatalf("rejected restore must not disturb live state, health = %d", got)
	}
}

func TestTargetsAreSorted(t *testing.T) {
	g := newGovernor(t, testConfig())
	for _, target := range []string{"b-host", "a-host", "c-host"} {
		if err := g.Report(target, okOutcome()); err != nil {
			t.Fatalf("report %s: %v", target, err)
		}
	}
	got := g.Targets()
	want := []string{"a-host", "b-host", "c-host"}
	if len(got) != len(want) {
		t.Fatalf("targets = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("targets[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
