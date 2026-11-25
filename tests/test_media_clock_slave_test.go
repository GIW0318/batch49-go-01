package task

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	impl "gsb/batch49-go-game-c-13"
)

func durMs(v int) time.Duration { return time.Duration(v) * time.Millisecond }

func mediaMs(v int) impl.MediaTime { return impl.MediaTime(int64(v) * int64(time.Millisecond)) }

// sampleAt builds a measurement whose midpoint estimate yields the given
// offset: anchor = local - rtt/2, master = anchor + offset.
func sampleAt(localMs, offsetMs, rttMs int) impl.SyncSample {
	anchor := durMs(localMs) - durMs(rttMs)/2
	master := anchor + durMs(offsetMs)
	return impl.SyncSample{
		MasterTime: impl.MediaTime(int64(master)),
		LocalRecv:  durMs(localMs),
		RTT:        durMs(rttMs),
	}
}

func windowOneConfig() impl.Config {
	return impl.Config{
		WindowSize:    1,
		MaxRTT:        durMs(500),
		StepThreshold: durMs(50),
		SlewLimitPPM:  500,
		HoldoverAfter: durMs(2000),
	}
}

func mustSample(t *testing.T, m *impl.MediaClockSlave, s impl.SyncSample) {
	t.Helper()
	if err := m.PushSample(s); err != nil {
		t.Fatalf("PushSample(master %v local %v rtt %v) failed: %v", s.MasterTime, s.LocalRecv, s.RTT, err)
	}
}

func mustNow(t *testing.T, m *impl.MediaClockSlave, want impl.MediaTime) {
	t.Helper()
	if got := m.Now(); got != want {
		t.Fatalf("Now() = %v, want %v", got, want)
	}
}

func TestFreshClockHasNoOffset(t *testing.T) {
	m := impl.NewMediaClockSlave(windowOneConfig())
	if got := m.Now(); got != 0 {
		t.Fatalf("Now() = %v, want 0 before any sample", got)
	}
	if off, ok := m.Offset(); ok || off != 0 {
		t.Fatalf("Offset() = %v,%v, want 0,false before any sample", off, ok)
	}
	if m.Holdover() || m.Degraded() {
		t.Fatalf("a fresh clock must not report holdover or degradation")
	}
	if m.SampleCount() != 0 || m.Rejected() != 0 || m.Steps() != 0 || m.Slew() != 0 {
		t.Fatalf("fresh counters are not zeroed")
	}
	if err := m.Advance(durMs(500)); err != nil {
		t.Fatalf("Advance failed: %v", err)
	}
	mustNow(t, m, mediaMs(500))
}

func TestFirstSampleSetsOffsetDirectly(t *testing.T) {
	m := impl.NewMediaClockSlave(windowOneConfig())
	mustSample(t, m, sampleAt(1000, 10, 20))
	off, ok := m.Offset()
	if !ok {
		t.Fatalf("Offset() must be known after one sample")
	}
	if off != durMs(10) {
		t.Fatalf("Offset() = %v, want 10ms", off)
	}
	if got := m.SampleCount(); got != 1 {
		t.Fatalf("sample count = %d, want 1", got)
	}
	if m.Steps() != 0 || m.Slew() != 0 {
		t.Fatalf("the first sample must not step or slew, got steps=%d slew=%d", m.Steps(), m.Slew())
	}
	mustNow(t, m, mediaMs(10))
}

func TestPushSampleRejectsBadMeasurement(t *testing.T) {
	m := impl.NewMediaClockSlave(windowOneConfig())
	bad := []impl.SyncSample{
		{MasterTime: mediaMs(1000), LocalRecv: durMs(1000), RTT: durMs(-1)},
		{MasterTime: mediaMs(1000), LocalRecv: durMs(1000), RTT: durMs(501)},
		{MasterTime: mediaMs(1000), LocalRecv: durMs(-1), RTT: durMs(10)},
		{MasterTime: 0, LocalRecv: durMs(1000), RTT: durMs(10)},
	}
	for i, s := range bad {
		if err := m.PushSample(s); !errors.Is(err, impl.ErrBadSample) {
			t.Fatalf("bad sample %d must fail with ErrBadSample, got %v", i, err)
		}
	}
	if got := m.SampleCount(); got != 0 {
		t.Fatalf("sample count = %d, want 0 after rejected samples", got)
	}
	if _, ok := m.Offset(); ok {
		t.Fatalf("rejected samples must not establish an offset")
	}
}

func TestRTTMedianOutlierIsRejected(t *testing.T) {
	cfg := windowOneConfig()
	cfg.WindowSize = 5
	cfg.StepThreshold = time.Hour
	m := impl.NewMediaClockSlave(cfg)
	for i := 1; i <= 4; i++ {
		mustSample(t, m, sampleAt(i*1000, 10, 10))
	}
	if got := m.SampleCount(); got != 4 {
		t.Fatalf("sample count = %d, want 4", got)
	}
	outlier := sampleAt(5000, 10, 100)
	if err := m.PushSample(outlier); !errors.Is(err, impl.ErrOutlier) {
		t.Fatalf("a round trip far above the median must fail with ErrOutlier, got %v", err)
	}
	if got := m.Rejected(); got != 1 {
		t.Fatalf("rejected samples = %d, want 1", got)
	}
	if got := m.SampleCount(); got != 4 {
		t.Fatalf("outliers must not enter the window, sample count = %d", got)
	}
	off, ok := m.Offset()
	if !ok || off != durMs(10) {
		t.Fatalf("Offset() = %v,%v, want the unchanged 10ms estimate", off, ok)
	}
}

func TestOffsetWindowUsesMedian(t *testing.T) {
	cfg := windowOneConfig()
	cfg.WindowSize = 4
	cfg.StepThreshold = time.Hour
	m := impl.NewMediaClockSlave(cfg)
	for i, offset := range []int{10, 20, 30, 40, 50, 60} {
		mustSample(t, m, sampleAt((i+1)*1000, offset, 10))
	}
	if got := m.SampleCount(); got != 4 {
		t.Fatalf("sample count = %d, want the window size 4", got)
	}
	if err := m.Advance(durMs(120000)); err != nil {
		t.Fatalf("Advance failed: %v", err)
	}
	off, ok := m.Offset()
	if !ok {
		t.Fatalf("Offset() must stay known")
	}
	if off != durMs(45) {
		t.Fatalf("Offset() = %v, want the median 45ms of the last four samples", off)
	}
	if m.Steps() != 0 {
		t.Fatalf("steps = %d, want 0 because every delta stays below the step threshold", m.Steps())
	}
	if m.Slew() != 0 {
		t.Fatalf("slew = %d, want 0 after convergence", m.Slew())
	}
}

func TestSmallOffsetSlewsWithinLimit(t *testing.T) {
	m := impl.NewMediaClockSlave(windowOneConfig())
	mustSample(t, m, sampleAt(1000, 0, 20))
	mustSample(t, m, sampleAt(2000, 20, 20))
	if m.Steps() != 0 {
		t.Fatalf("a 20ms offset must not step, got %d steps", m.Steps())
	}
	if got := m.Slew(); got != 500 {
		t.Fatalf("slew = %d, want the configured 500ppm", got)
	}
	if off, _ := m.Offset(); off != 0 {
		t.Fatalf("offset = %v before any advance, want 0", off)
	}
	if err := m.Advance(durMs(10000)); err != nil {
		t.Fatalf("Advance failed: %v", err)
	}
	if off, _ := m.Offset(); off != durMs(5) {
		t.Fatalf("offset after 10s at 500ppm = %v, want 5ms", off)
	}
	mustNow(t, m, mediaMs(10005))
	if err := m.Advance(durMs(30000)); err != nil {
		t.Fatalf("Advance failed: %v", err)
	}
	if off, _ := m.Offset(); off != durMs(20) {
		t.Fatalf("offset = %v, want the converged 20ms", off)
	}
	if got := m.Slew(); got != 0 {
		t.Fatalf("slew = %d, want 0 once the target is reached", got)
	}
	mustNow(t, m, mediaMs(40020))
	if err := m.Advance(durMs(1000)); err != nil {
		t.Fatalf("Advance failed: %v", err)
	}
	mustNow(t, m, mediaMs(41020))
}

func TestLargeOffsetStepsForward(t *testing.T) {
	m := impl.NewMediaClockSlave(windowOneConfig())
	mustSample(t, m, sampleAt(1000, 0, 20))
	mustSample(t, m, sampleAt(2000, 200, 20))
	if got := m.Steps(); got != 1 {
		t.Fatalf("steps = %d, want 1 for a 200ms correction", got)
	}
	if off, _ := m.Offset(); off != durMs(200) {
		t.Fatalf("offset = %v, want the stepped 200ms", off)
	}
	if got := m.Slew(); got != 0 {
		t.Fatalf("slew = %d, want 0 right after a step", got)
	}
	mustNow(t, m, mediaMs(200))
}

func TestBackwardCorrectionNeverRewindsTimeline(t *testing.T) {
	m := impl.NewMediaClockSlave(windowOneConfig())
	mustSample(t, m, sampleAt(1000, 10, 20))
	mustNow(t, m, mediaMs(10))
	// The reference jumps 999ms backwards while the master timestamp still
	// moves forward, so the sample itself is not stale.
	mustSample(t, m, impl.SyncSample{
		MasterTime: impl.MediaTime(int64(durMs(1001))),
		LocalRecv:  durMs(2000),
		RTT:        durMs(20),
	})
	if got := m.Steps(); got != 0 {
		t.Fatalf("steps = %d, want 0 because a backward correction must not step", got)
	}
	if got := m.Slew(); got >= 0 {
		t.Fatalf("slew = %d, want a negative rate correction", got)
	}
	mustNow(t, m, mediaMs(10))
	if err := m.Advance(durMs(1000)); err != nil {
		t.Fatalf("Advance failed: %v", err)
	}
	mustNow(t, m, impl.MediaTime(int64(durMs(1009))+int64(durMs(1))/2))
}

func TestSetSlewLimitBoundsAndClamps(t *testing.T) {
	m := impl.NewMediaClockSlave(windowOneConfig())
	mustSample(t, m, sampleAt(1000, 0, 20))
	mustSample(t, m, sampleAt(2000, 20, 20))
	if got := m.Slew(); got != 500 {
		t.Fatalf("slew = %d, want 500 before tightening the limit", got)
	}
	if err := m.SetSlewLimit(0); !errors.Is(err, impl.ErrSlewRange) {
		t.Fatalf("zero slew limit must fail with ErrSlewRange, got %v", err)
	}
	if err := m.SetSlewLimit(-5); !errors.Is(err, impl.ErrSlewRange) {
		t.Fatalf("negative slew limit must fail with ErrSlewRange, got %v", err)
	}
	if err := m.SetSlewLimit(impl.MaxSlewLimitPPM + 1); !errors.Is(err, impl.ErrSlewRange) {
		t.Fatalf("slew limit above the cap must fail with ErrSlewRange, got %v", err)
	}
	if got := m.Slew(); got != 500 {
		t.Fatalf("slew = %d, rejected limits must not change it", got)
	}
	if err := m.SetSlewLimit(100); err != nil {
		t.Fatalf("SetSlewLimit(100) failed: %v", err)
	}
	if got := m.Slew(); got != 100 {
		t.Fatalf("slew = %d, want the running correction clamped to 100", got)
	}
	if err := m.Advance(durMs(10000)); err != nil {
		t.Fatalf("Advance failed: %v", err)
	}
	if off, _ := m.Offset(); off != durMs(1) {
		t.Fatalf("offset after 10s at 100ppm = %v, want 1ms", off)
	}
}

func TestHoldoverAfterSilence(t *testing.T) {
	m := impl.NewMediaClockSlave(windowOneConfig())
	mustSample(t, m, sampleAt(1000, 0, 20))
	if err := m.Advance(durMs(1000)); err != nil {
		t.Fatalf("Advance failed: %v", err)
	}
	if m.Holdover() || m.Degraded() {
		t.Fatalf("one second of silence must stay inside the holdover window")
	}
	if err := m.Advance(durMs(1500)); err != nil {
		t.Fatalf("Advance failed: %v", err)
	}
	if !m.Holdover() {
		t.Fatalf("2.5s of silence must enter holdover")
	}
	if !m.Degraded() {
		t.Fatalf("holdover must be reported as degraded")
	}
	mustNow(t, m, mediaMs(3500))
	if err := m.Advance(durMs(1000)); err != nil {
		t.Fatalf("Advance failed: %v", err)
	}
	mustNow(t, m, mediaMs(4500))
	if !m.Holdover() {
		t.Fatalf("the clock must stay in holdover until a new sample arrives")
	}
}

func TestSampleEndsHoldover(t *testing.T) {
	m := impl.NewMediaClockSlave(windowOneConfig())
	mustSample(t, m, sampleAt(1000, 0, 20))
	if err := m.Advance(durMs(3000)); err != nil {
		t.Fatalf("Advance failed: %v", err)
	}
	if !m.Holdover() {
		t.Fatalf("three seconds of silence must enter holdover")
	}
	mustSample(t, m, sampleAt(3000, 20, 20))
	if m.Holdover() || m.Degraded() {
		t.Fatalf("a fresh sample must clear holdover and degradation")
	}
	if err := m.Advance(durMs(1500)); err != nil {
		t.Fatalf("Advance failed: %v", err)
	}
	if m.Holdover() {
		t.Fatalf("holdover must be measured from the newest sample, not the oldest")
	}
}

func TestAdvanceRejectsNonPositive(t *testing.T) {
	m := impl.NewMediaClockSlave(windowOneConfig())
	mustSample(t, m, sampleAt(1000, 10, 20))
	if err := m.Advance(0); !errors.Is(err, impl.ErrBadAdvance) {
		t.Fatalf("zero advance must fail with ErrBadAdvance, got %v", err)
	}
	if err := m.Advance(durMs(-1)); !errors.Is(err, impl.ErrBadAdvance) {
		t.Fatalf("negative advance must fail with ErrBadAdvance, got %v", err)
	}
	mustNow(t, m, mediaMs(10))
}

func TestSnapshotRestoreServoState(t *testing.T) {
	cfg := windowOneConfig()
	cfg.WindowSize = 2
	m := impl.NewMediaClockSlave(cfg)
	mustSample(t, m, sampleAt(1000, 0, 20))
	mustSample(t, m, sampleAt(2000, 20, 20))
	if err := m.Advance(durMs(4000)); err != nil {
		t.Fatalf("Advance failed: %v", err)
	}
	data, err := m.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewMediaClockSlave(impl.Config{WindowSize: 1, StepThreshold: durMs(1), SlewLimitPPM: 2000})
	if err := restored.Restore(data); err != nil {
		t.Fatalf("Restore failed: %v", err)
	}
	if got, want := restored.Now(), m.Now(); got != want {
		t.Fatalf("restored Now() = %v, want %v", got, want)
	}
	if off, ok := restored.Offset(); !ok || off != durMs(2) {
		t.Fatalf("restored Offset() = %v,%v, want 2ms,true", off, ok)
	}
	if got := restored.Slew(); got != 500 {
		t.Fatalf("restored slew = %d, want 500", got)
	}
	if got := restored.SampleCount(); got != 2 {
		t.Fatalf("restored sample count = %d, want 2", got)
	}
	if err := m.Advance(durMs(10000)); err != nil {
		t.Fatalf("Advance on the original failed: %v", err)
	}
	if err := restored.Advance(durMs(10000)); err != nil {
		t.Fatalf("Advance on the restored clock failed: %v", err)
	}
	if got, want := restored.Now(), m.Now(); got != want {
		t.Fatalf("restored clock advanced to %v, want %v", got, want)
	}
	if off, _ := restored.Offset(); off != durMs(7) {
		t.Fatalf("restored offset after 10s = %v, want 7ms", off)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	m := impl.NewMediaClockSlave(windowOneConfig())
	mustSample(t, m, sampleAt(1000, 10, 20))
	if err := m.Restore([]byte("<not-a-snapshot>")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	mustNow(t, m, mediaMs(10))
	if got := m.SampleCount(); got != 1 {
		t.Fatalf("sample count = %d, want the live window untouched", got)
	}
	if err := m.Advance(durMs(1000)); err != nil {
		t.Fatalf("the live clock must keep working, got %v", err)
	}
	mustNow(t, m, mediaMs(1010))
}

func TestConcurrentAdvanceAndReaders(t *testing.T) {
	m := impl.NewMediaClockSlave(windowOneConfig())
	var failures int64
	var wg sync.WaitGroup
	for w := 0; w < 3; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				if err := m.Advance(durMs(1)); err != nil {
					atomic.AddInt64(&failures, 1)
					return
				}
			}
		}()
	}
	for r := 0; r < 3; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var last impl.MediaTime
			for i := 0; i < 1000; i++ {
				now := m.Now()
				if now < last {
					atomic.AddInt64(&failures, 1)
					return
				}
				last = now
				m.Offset()
				m.Holdover()
			}
		}()
	}
	wg.Wait()
	if failures != 0 {
		t.Fatalf("%d workers observed a rewinding clock or a failing advance", failures)
	}
	mustNow(t, m, mediaMs(1500))
	if _, ok := m.Offset(); ok {
		t.Fatalf("advancing without samples must not establish an offset")
	}
}
