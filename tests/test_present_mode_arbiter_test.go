package task

import (
	"errors"
	"sync"
	"testing"
	"time"

	impl "gsb/batch49-go-game-c-09"
)

func arbiterConfig() impl.ArbiterConfig {
	return impl.ArbiterConfig{
		RefreshInterval: 16 * time.Millisecond,
		VRRMin:          8 * time.Millisecond,
		VRRMax:          34 * time.Millisecond,
		Window:          4,
		Percentile:      95,
		Hysteresis:      1,
	}
}

func frame(d time.Duration) impl.FrameTiming {
	return impl.FrameTiming{Frame: d, Focused: true}
}

func report(t *testing.T, a *impl.PresentModeArbiter, d impl.FrameTiming) {
	t.Helper()
	if err := a.ReportFrame(d); err != nil {
		t.Fatalf("report frame %v: %v", d.Frame, err)
	}
}

func TestNewArbiterStartsInSync(t *testing.T) {
	a := impl.NewPresentModeArbiter(arbiterConfig())
	if got := a.Current(); got != impl.ModeSync {
		t.Fatalf("initial mode = %q, want sync", got)
	}
	modes := a.SupportedModes()
	want := []impl.PresentMode{impl.ModeSync, impl.ModeMailbox, impl.ModeImmediate}
	if len(modes) != len(want) {
		t.Fatalf("supported modes = %v, want %v", modes, want)
	}
	for i := range want {
		if modes[i] != want[i] {
			t.Fatalf("supported modes = %v, want %v", modes, want)
		}
	}
	if st := a.Stats(); st != (impl.PresentStats{}) {
		t.Fatalf("fresh stats = %+v, want zero", st)
	}
}

func TestReportFrameRejectsBadTiming(t *testing.T) {
	a := impl.NewPresentModeArbiter(arbiterConfig())
	if err := a.ReportFrame(impl.FrameTiming{Frame: 0, Focused: true}); !errors.Is(err, impl.ErrBadFrame) {
		t.Fatalf("zero frame = %v, want ErrBadFrame", err)
	}
	if err := a.ReportFrame(impl.FrameTiming{Frame: -time.Millisecond, Focused: true}); !errors.Is(err, impl.ErrBadFrame) {
		t.Fatalf("negative frame = %v, want ErrBadFrame", err)
	}
	if st := a.Stats(); st.Frames != 0 {
		t.Fatalf("rejected frames must not be counted, got %d", st.Frames)
	}
}

func TestSteadyFramesSwitchToMailbox(t *testing.T) {
	a := impl.NewPresentModeArbiter(arbiterConfig())
	for i := 0; i < 3; i++ {
		report(t, a, frame(16*time.Millisecond))
	}
	if got := a.Current(); got != impl.ModeMailbox {
		t.Fatalf("mode after steady frames = %q, want mailbox", got)
	}
}

func TestHysteresisRequiresConsecutiveFrames(t *testing.T) {
	cfg := arbiterConfig()
	cfg.Hysteresis = 3
	a := impl.NewPresentModeArbiter(cfg)
	report(t, a, frame(16*time.Millisecond))
	report(t, a, frame(16*time.Millisecond))
	if got := a.Current(); got != impl.ModeSync {
		t.Fatalf("mode after two candidate frames = %q, want sync", got)
	}
	if st := a.Stats(); st.Hysteresis != 2 {
		t.Fatalf("hysteresis counter = %d, want 2", st.Hysteresis)
	}
	report(t, a, frame(16*time.Millisecond))
	if got := a.Current(); got != impl.ModeMailbox {
		t.Fatalf("mode after three candidate frames = %q, want mailbox", got)
	}
}

func TestUpperVRRBoundForcesSync(t *testing.T) {
	a := impl.NewPresentModeArbiter(arbiterConfig())
	for i := 0; i < 3; i++ {
		report(t, a, frame(16*time.Millisecond))
	}
	if got := a.Current(); got != impl.ModeMailbox {
		t.Fatalf("mode before the slow frames = %q, want mailbox", got)
	}
	for i := 0; i < 5; i++ {
		report(t, a, frame(40*time.Millisecond))
	}
	if got := a.Current(); got != impl.ModeSync {
		t.Fatalf("mode above the vrr upper bound = %q, want sync", got)
	}
}

func TestPercentileIgnoresSingleSpike(t *testing.T) {
	cfg := arbiterConfig()
	cfg.Window = 9
	cfg.Percentile = 50
	a := impl.NewPresentModeArbiter(cfg)
	for i := 0; i < 8; i++ {
		report(t, a, frame(16*time.Millisecond))
	}
	if got := a.Current(); got != impl.ModeMailbox {
		t.Fatalf("mode before the spike = %q, want mailbox", got)
	}
	report(t, a, frame(40*time.Millisecond))
	if got := a.Current(); got != impl.ModeMailbox {
		t.Fatalf("one slow frame moved the median mode to %q, want mailbox", got)
	}
	if st := a.Stats(); st.Percentile != 16*time.Millisecond {
		t.Fatalf("window percentile = %v, want the median 16ms", st.Percentile)
	}
}

func TestUnfocusedFrameForcesSync(t *testing.T) {
	a := impl.NewPresentModeArbiter(arbiterConfig())
	for i := 0; i < 2; i++ {
		report(t, a, frame(16*time.Millisecond))
	}
	if got := a.Current(); got != impl.ModeMailbox {
		t.Fatalf("mode before losing focus = %q, want mailbox", got)
	}
	report(t, a, impl.FrameTiming{Frame: 16 * time.Millisecond, Focused: false})
	if got := a.Current(); got != impl.ModeSync {
		t.Fatalf("mode after losing focus = %q, want sync", got)
	}
}

func TestUnfocusedFramesDoNotAdvanceStats(t *testing.T) {
	cfg := arbiterConfig()
	cfg.Hysteresis = 1000
	a := impl.NewPresentModeArbiter(cfg)
	for i := 0; i < 3; i++ {
		report(t, a, frame(16*time.Millisecond))
	}
	if st := a.Stats(); st.Frames != 3 {
		t.Fatalf("frames after three focused reports = %d, want 3", st.Frames)
	}
	for i := 0; i < 3; i++ {
		report(t, a, impl.FrameTiming{Frame: 16 * time.Millisecond, Focused: false})
	}
	if st := a.Stats(); st.Frames != 3 {
		t.Fatalf("unfocused frames advanced the counter to %d, want 3", st.Frames)
	}
	if got := a.Current(); got != impl.ModeSync {
		t.Fatalf("mode during unfocused frames = %q, want sync", got)
	}
}

func TestRequestModeSwitchesSupportedMode(t *testing.T) {
	a := impl.NewPresentModeArbiter(arbiterConfig())
	if err := a.RequestMode(impl.ModeMailbox); err != nil {
		t.Fatalf("request mailbox: %v", err)
	}
	if got := a.Current(); got != impl.ModeMailbox {
		t.Fatalf("mode after request = %q, want mailbox", got)
	}
	if err := a.RequestMode(impl.ModeSync); err != nil {
		t.Fatalf("request sync: %v", err)
	}
	if got := a.Current(); got != impl.ModeSync {
		t.Fatalf("mode after request = %q, want sync", got)
	}
}

func TestRequestModeRejectsUnknownMode(t *testing.T) {
	a := impl.NewPresentModeArbiter(arbiterConfig())
	if err := a.RequestMode(impl.PresentMode("triple")); !errors.Is(err, impl.ErrUnknownMode) {
		t.Fatalf("unknown mode = %v, want ErrUnknownMode", err)
	}
	if got := a.Current(); got != impl.ModeSync {
		t.Fatalf("rejected request changed the mode to %q, want sync", got)
	}
}

func TestSetVRRRangeRejectsInverted(t *testing.T) {
	a := impl.NewPresentModeArbiter(arbiterConfig())
	if err := a.SetVRRRange(34*time.Millisecond, 8*time.Millisecond); !errors.Is(err, impl.ErrBadVRRRange) {
		t.Fatalf("inverted range = %v, want ErrBadVRRRange", err)
	}
	if err := a.SetVRRRange(-time.Millisecond, 8*time.Millisecond); !errors.Is(err, impl.ErrBadVRRRange) {
		t.Fatalf("negative lower bound = %v, want ErrBadVRRRange", err)
	}
	if err := a.SetVRRRange(8*time.Millisecond, 34*time.Millisecond); err != nil {
		t.Fatalf("valid range must be accepted, got %v", err)
	}
}

func TestStatsCountTears(t *testing.T) {
	a := impl.NewPresentModeArbiter(arbiterConfig())
	if err := a.RequestMode(impl.ModeMailbox); err != nil {
		t.Fatalf("request mailbox: %v", err)
	}
	for i := 0; i < 4; i++ {
		report(t, a, frame(20*time.Millisecond))
	}
	st := a.Stats()
	if st.Frames != 4 {
		t.Fatalf("frames = %d, want 4", st.Frames)
	}
	if st.Tears != 4 {
		t.Fatalf("tears = %d, want one per late mailbox frame", st.Tears)
	}
	if got := a.Current(); got != impl.ModeMailbox {
		t.Fatalf("mode drifted to %q, want mailbox", got)
	}
}

func TestSnapshotRestoreKeepsModeAndWindow(t *testing.T) {
	cfg := arbiterConfig()
	a := impl.NewPresentModeArbiter(cfg)
	for i := 0; i < 3; i++ {
		report(t, a, frame(16*time.Millisecond))
	}
	data, err := a.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewPresentModeArbiter(cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.Current(); got != impl.ModeMailbox {
		t.Fatalf("restored mode = %q, want mailbox", got)
	}
	if st := restored.Stats(); st.Percentile != 16*time.Millisecond {
		t.Fatalf("restored percentile = %v, want the window median 16ms", st.Percentile)
	}
	report(t, restored, frame(20*time.Millisecond))
	if got := restored.Current(); got != impl.ModeMailbox {
		t.Fatalf("mode after restore and a frame = %q, want mailbox", got)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	a := impl.NewPresentModeArbiter(arbiterConfig())
	for i := 0; i < 3; i++ {
		report(t, a, frame(16*time.Millisecond))
	}
	if err := a.Restore([]byte("not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot = %v, want ErrBadSnapshot", err)
	}
	if got := a.Current(); got != impl.ModeMailbox {
		t.Fatalf("failed restore changed the mode to %q, want mailbox", got)
	}
	if st := a.Stats(); st.Percentile != 16*time.Millisecond {
		t.Fatalf("failed restore damaged the window, percentile = %v", st.Percentile)
	}
}

func TestConcurrentReportFrameAndCurrent(t *testing.T) {
	cfg := arbiterConfig()
	cfg.Hysteresis = 1000
	a := impl.NewPresentModeArbiter(cfg)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				if err := a.ReportFrame(frame(16 * time.Millisecond)); err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
					return
				}
				if got := a.Current(); got != impl.ModeSync && got != impl.ModeMailbox && got != impl.ModeImmediate {
					mu.Lock()
					if firstErr == nil {
						firstErr = errors.New("current mode became invalid")
					}
					mu.Unlock()
					return
				}
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		t.Fatalf("concurrent reports failed: %v", firstErr)
	}
	st := a.Stats()
	if st.Frames != 200 {
		t.Fatalf("frames = %d, want 200", st.Frames)
	}
	if st.Percentile != 16*time.Millisecond {
		t.Fatalf("percentile = %v, want 16ms", st.Percentile)
	}
	if got := a.Current(); got != impl.ModeSync {
		t.Fatalf("mode = %q, want sync because the hysteresis was never reached", got)
	}
}
