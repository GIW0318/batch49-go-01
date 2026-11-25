package task

import (
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"

	impl "gsb/batch49-go-game-c-05"
)

func testConfig() impl.Config {
	return impl.Config{MaxStepMs: 40, OutlierThresholdMs: 250, MinAnchors: 3}
}

func submitOffset(t *testing.T, c *impl.CaptionAutoResync, cue string, offset int64) {
	t.Helper()
	a := impl.Anchor{CueID: cue, CueStartMs: 1000, VoiceStartMs: 1000 + offset}
	if err := c.SubmitAnchor(a); err != nil {
		t.Fatalf("submit anchor %q offset %d: %v", cue, offset, err)
	}
}

func TestSubmitAnchorRejectsBlankCue(t *testing.T) {
	c := impl.NewCaptionAutoResync(testConfig())
	if err := c.SubmitAnchor(impl.Anchor{CueID: "   ", CueStartMs: 1000, VoiceStartMs: 1100}); !errors.Is(err, impl.ErrBlankCue) {
		t.Fatalf("blank cue must report ErrBlankCue, got %v", err)
	}
}

func TestSubmitAnchorRejectsDuplicateCue(t *testing.T) {
	c := impl.NewCaptionAutoResync(testConfig())
	a := impl.Anchor{CueID: "cue-a", CueStartMs: 1000, VoiceStartMs: 1100}
	if err := c.SubmitAnchor(a); err != nil {
		t.Fatalf("first anchor: %v", err)
	}
	if err := c.SubmitAnchor(a); !errors.Is(err, impl.ErrDuplicateAnchor) {
		t.Fatalf("duplicate cue must report ErrDuplicateAnchor, got %v", err)
	}
	if got := c.AnchorCount(); got != 1 {
		t.Fatalf("anchor count = %d, want 1", got)
	}
}

func TestSubmitAnchorRejectsNegativeTimes(t *testing.T) {
	c := impl.NewCaptionAutoResync(testConfig())
	if err := c.SubmitAnchor(impl.Anchor{CueID: "cue-a", CueStartMs: -1, VoiceStartMs: 100}); !errors.Is(err, impl.ErrBadAnchor) {
		t.Fatalf("negative cue time must report ErrBadAnchor, got %v", err)
	}
	if err := c.SubmitAnchor(impl.Anchor{CueID: "cue-a", CueStartMs: 100, VoiceStartMs: -2}); !errors.Is(err, impl.ErrBadAnchor) {
		t.Fatalf("negative voice time must report ErrBadAnchor, got %v", err)
	}
}

func TestEstimateMedianOffsetAndSamples(t *testing.T) {
	c := impl.NewCaptionAutoResync(testConfig())
	submitOffset(t, c, "cue-a", 100)
	submitOffset(t, c, "cue-b", 110)
	submitOffset(t, c, "cue-c", 120)
	est, err := c.Estimate()
	if err != nil {
		t.Fatalf("estimate: %v", err)
	}
	if est.OffsetMs != 110 {
		t.Fatalf("offset = %d, want median 110", est.OffsetMs)
	}
	if est.Samples != 3 || est.Outliers != 0 {
		t.Fatalf("samples/outliers = %d/%d, want 3/0", est.Samples, est.Outliers)
	}
}

func TestEstimateRemovesOutlierPairs(t *testing.T) {
	c := impl.NewCaptionAutoResync(testConfig())
	for i, offset := range []int64{100, 100, 100, 100, 5000} {
		submitOffset(t, c, fmt.Sprintf("cue-%d", i), offset)
	}
	est, err := c.Estimate()
	if err != nil {
		t.Fatalf("estimate: %v", err)
	}
	if est.OffsetMs != 100 {
		t.Fatalf("offset = %d, want 100 after dropping the outlier", est.OffsetMs)
	}
	if est.Outliers != 1 {
		t.Fatalf("outliers = %d, want 1", est.Outliers)
	}
	if est.Samples != 4 {
		t.Fatalf("samples = %d, want 4", est.Samples)
	}
}

func TestEstimateConfidencePerfectWhenConsistent(t *testing.T) {
	c := impl.NewCaptionAutoResync(testConfig())
	for i := 0; i < 3; i++ {
		submitOffset(t, c, fmt.Sprintf("cue-%d", i), 100)
	}
	est, err := c.Estimate()
	if err != nil {
		t.Fatalf("estimate: %v", err)
	}
	if est.Confidence != 1 {
		t.Fatalf("confidence = %v, want 1 for identical offsets", est.Confidence)
	}
}

func TestEstimateConfidenceDropsForSpread(t *testing.T) {
	c := impl.NewCaptionAutoResync(testConfig())
	for i, offset := range []int64{100, 100, 200, 300, 300} {
		submitOffset(t, c, fmt.Sprintf("cue-%d", i), offset)
	}
	est, err := c.Estimate()
	if err != nil {
		t.Fatalf("estimate: %v", err)
	}
	if est.OffsetMs != 200 {
		t.Fatalf("offset = %d, want 200", est.OffsetMs)
	}
	if math.Abs(est.Confidence-0.6) > 1e-9 {
		t.Fatalf("confidence = %v, want 0.6", est.Confidence)
	}
}

func TestApplyStepCapsShiftPerCall(t *testing.T) {
	c := impl.NewCaptionAutoResync(testConfig())
	for i := 0; i < 3; i++ {
		submitOffset(t, c, fmt.Sprintf("cue-%d", i), 200)
	}
	shift, err := c.ApplyStep()
	if err != nil {
		t.Fatalf("apply step: %v", err)
	}
	if shift.AppliedMs != 40 || shift.RemainingMs != 160 {
		t.Fatalf("shift = %+v, want applied 40 remaining 160", shift)
	}
	if got, ok := c.CueShift("cue-0"); !ok || got != 40 {
		t.Fatalf("cue shift = %d ok=%v, want 40", got, ok)
	}
}

func TestApplyStepConvergesAndStops(t *testing.T) {
	c := impl.NewCaptionAutoResync(testConfig())
	for i := 0; i < 3; i++ {
		submitOffset(t, c, fmt.Sprintf("cue-%d", i), 200)
	}
	applied := int64(0)
	for i := 0; i < 5; i++ {
		shift, err := c.ApplyStep()
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if shift.AppliedMs != 40 {
			t.Fatalf("step %d applied = %d, want 40", i, shift.AppliedMs)
		}
		applied += shift.AppliedMs
	}
	if applied != 200 {
		t.Fatalf("total applied = %d, want 200", applied)
	}
	shift, err := c.ApplyStep()
	if err != nil {
		t.Fatalf("final step: %v", err)
	}
	if shift.AppliedMs != 0 || shift.RemainingMs != 0 {
		t.Fatalf("final shift = %+v, want no movement", shift)
	}
}

func TestApplyStepNoopWhenAligned(t *testing.T) {
	c := impl.NewCaptionAutoResync(testConfig())
	for i := 0; i < 3; i++ {
		submitOffset(t, c, fmt.Sprintf("cue-%d", i), 0)
	}
	shift, err := c.ApplyStep()
	if err != nil {
		t.Fatalf("apply step: %v", err)
	}
	if shift.AppliedMs != 0 || shift.RemainingMs != 0 {
		t.Fatalf("aligned shift = %+v, want 0/0", shift)
	}
}

func TestLockedCueNotShifted(t *testing.T) {
	c := impl.NewCaptionAutoResync(testConfig())
	submitOffset(t, c, "cue-a", 200)
	submitOffset(t, c, "cue-b", 200)
	submitOffset(t, c, "cue-c", 200)
	if err := c.Lock("cue-b"); err != nil {
		t.Fatalf("lock cue-b: %v", err)
	}
	if !c.Locked("cue-b") {
		t.Fatalf("cue-b must report locked")
	}
	if _, err := c.ApplyStep(); err != nil {
		t.Fatalf("apply step: %v", err)
	}
	if got, ok := c.CueShift("cue-a"); !ok || got != 40 {
		t.Fatalf("unlocked cue shift = %d ok=%v, want 40", got, ok)
	}
	if got, ok := c.CueShift("cue-b"); !ok || got != 0 {
		t.Fatalf("locked cue shift = %d ok=%v, want 0", got, ok)
	}
}

func TestUnlockResumesShift(t *testing.T) {
	c := impl.NewCaptionAutoResync(testConfig())
	submitOffset(t, c, "cue-a", 200)
	submitOffset(t, c, "cue-b", 200)
	submitOffset(t, c, "cue-c", 200)
	if err := c.Lock("cue-b"); err != nil {
		t.Fatalf("lock cue-b: %v", err)
	}
	if _, err := c.ApplyStep(); err != nil {
		t.Fatalf("first step: %v", err)
	}
	if err := c.Unlock("cue-b"); err != nil {
		t.Fatalf("unlock cue-b: %v", err)
	}
	if c.Locked("cue-b") {
		t.Fatalf("cue-b must report unlocked")
	}
	if _, err := c.ApplyStep(); err != nil {
		t.Fatalf("second step: %v", err)
	}
	if got, ok := c.CueShift("cue-b"); !ok || got != 40 {
		t.Fatalf("cue-b shift after unlock = %d ok=%v, want 40", got, ok)
	}
	if got, _ := c.CueShift("cue-a"); got != 80 {
		t.Fatalf("cue-a shift = %d, want 80", got)
	}
}

func TestConcurrentSubmitAnchorsCounted(t *testing.T) {
	c := impl.NewCaptionAutoResync(testConfig())
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			a := impl.Anchor{
				CueID:        fmt.Sprintf("cue-%d", i),
				CueStartMs:   1000,
				VoiceStartMs: 1100,
			}
			if err := c.SubmitAnchor(a); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent submit failed: %v", err)
	}
	if got := c.AnchorCount(); got != 16 {
		t.Fatalf("anchor count = %d, want 16", got)
	}
}

func TestSnapshotRestoreKeepsAnchorsAndShift(t *testing.T) {
	c := impl.NewCaptionAutoResync(testConfig())
	submitOffset(t, c, "cue-a", 200)
	submitOffset(t, c, "cue-b", 200)
	submitOffset(t, c, "cue-c", 200)
	for i := 0; i < 2; i++ {
		if _, err := c.ApplyStep(); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	data, err := c.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	fresh := impl.NewCaptionAutoResync(testConfig())
	if err := fresh.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := fresh.AnchorCount(); got != 3 {
		t.Fatalf("restored anchors = %d, want 3", got)
	}
	if got, ok := fresh.CueShift("cue-a"); !ok || got != 80 {
		t.Fatalf("restored shift = %d ok=%v, want 80", got, ok)
	}
	shift, err := fresh.ApplyStep()
	if err != nil {
		t.Fatalf("step after restore: %v", err)
	}
	if shift.AppliedMs != 40 || shift.RemainingMs != 80 {
		t.Fatalf("shift after restore = %+v, want applied 40 remaining 80", shift)
	}
}

func TestRestoreRejectsCorruptPayload(t *testing.T) {
	c := impl.NewCaptionAutoResync(testConfig())
	if err := c.Restore([]byte("bad-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt payload must report ErrBadSnapshot, got %v", err)
	}
}
