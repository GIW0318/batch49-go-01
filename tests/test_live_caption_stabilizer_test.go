package task

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	impl "gsb/batch49-go-game-c-06"
)

func captionConfig() impl.CaptionConfig {
	return impl.CaptionConfig{
		CorrectionWindow: 500 * time.Millisecond,
		MaxLines:         4,
		MaxSegments:      16,
	}
}

func partial(seg string, seq int, text string, start, now time.Duration) impl.Partial {
	return impl.Partial{SegID: seg, Seq: seq, Text: text, Start: start, Now: now}
}

func mustUpdate(t *testing.T, s *impl.LiveCaptionStabilizer, p impl.Partial) {
	t.Helper()
	if err := s.Update(p); err != nil {
		t.Fatalf("update %s/%d: %v", p.SegID, p.Seq, err)
	}
}

func TestUpdateCreatesRollingLine(t *testing.T) {
	s := impl.NewLiveCaptionStabilizer(captionConfig())
	mustUpdate(t, s, partial("seg-a", 1, "hello", 0, 100*time.Millisecond))
	lines := s.Rolling()
	if len(lines) != 1 {
		t.Fatalf("rolling lines = %d, want 1", len(lines))
	}
	got := lines[0]
	if got.SegID != "seg-a" || got.Seq != 1 || got.Text != "hello" {
		t.Fatalf("line = %+v, want seg-a/1/hello", got)
	}
	if got.Start != 0 {
		t.Fatalf("line start = %v, want 0", got.Start)
	}
	if got.Frozen {
		t.Fatalf("fresh line must not be frozen")
	}
	if st := s.Stats(); st.Received != 1 || st.Applied != 1 {
		t.Fatalf("stats received=%d applied=%d, want 1/1", st.Received, st.Applied)
	}
}

func TestUpdateSameSegmentReplacesText(t *testing.T) {
	s := impl.NewLiveCaptionStabilizer(captionConfig())
	mustUpdate(t, s, partial("seg-a", 1, "he", 0, 100*time.Millisecond))
	mustUpdate(t, s, partial("seg-a", 2, "hello world", 0, 200*time.Millisecond))
	lines := s.Rolling()
	if len(lines) != 1 {
		t.Fatalf("same segment must stay on one line, got %d lines", len(lines))
	}
	if lines[0].Text != "hello world" {
		t.Fatalf("line text = %q, want the newest partial", lines[0].Text)
	}
	if lines[0].Seq != 2 {
		t.Fatalf("line seq = %d, want 2", lines[0].Seq)
	}
	if st := s.Stats(); st.Applied != 2 {
		t.Fatalf("applied = %d, want 2", st.Applied)
	}
}

func TestUpdateDiscardsStaleSequence(t *testing.T) {
	s := impl.NewLiveCaptionStabilizer(captionConfig())
	mustUpdate(t, s, partial("seg-a", 5, "newer", 0, 100*time.Millisecond))
	mustUpdate(t, s, partial("seg-a", 4, "older", 0, 120*time.Millisecond))
	lines := s.Rolling()
	if len(lines) != 1 || lines[0].Text != "newer" {
		t.Fatalf("stale partial must be dropped, lines = %+v", lines)
	}
	if st := s.Stats(); st.DroppedStale != 1 {
		t.Fatalf("dropped stale = %d, want 1", st.DroppedStale)
	}
}

func TestFinalizeFreezesSegment(t *testing.T) {
	s := impl.NewLiveCaptionStabilizer(captionConfig())
	mustUpdate(t, s, partial("seg-a", 1, "done", 0, 100*time.Millisecond))
	if err := s.Finalize("seg-a"); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	lines := s.Rolling()
	if len(lines) != 1 || !lines[0].Frozen {
		t.Fatalf("finalized line must be frozen, lines = %+v", lines)
	}
	if st := s.Stats(); st.Frozen != 1 {
		t.Fatalf("frozen count = %d, want 1", st.Frozen)
	}
	if pending := s.Pending(); len(pending) != 0 {
		t.Fatalf("pending lines after finalize = %d, want 0", len(pending))
	}
}

func TestFinalizeUnknownSegmentFails(t *testing.T) {
	s := impl.NewLiveCaptionStabilizer(captionConfig())
	if err := s.Finalize("seg-missing"); !errors.Is(err, impl.ErrUnknownSegment) {
		t.Fatalf("finalize unknown segment = %v, want ErrUnknownSegment", err)
	}
	if err := s.Finalize("  "); !errors.Is(err, impl.ErrUnknownSegment) {
		t.Fatalf("finalize blank segment = %v, want ErrUnknownSegment", err)
	}
}

func TestForceFinalizeFreezesEligibleSegment(t *testing.T) {
	s := impl.NewLiveCaptionStabilizer(captionConfig())
	mustUpdate(t, s, partial("seg-a", 1, "first", 0, 100*time.Millisecond))
	mustUpdate(t, s, partial("seg-b", 1, "second", 50*time.Millisecond, 300*time.Millisecond))
	if err := s.ForceFinalize(150 * time.Millisecond); err != nil {
		t.Fatalf("force finalize: %v", err)
	}
	lines := s.Rolling()
	if len(lines) != 2 {
		t.Fatalf("rolling lines = %d, want 2", len(lines))
	}
	if !lines[0].Frozen {
		t.Fatalf("segment shown at 100ms must be frozen by cutoff 150ms")
	}
	if lines[1].Frozen {
		t.Fatalf("segment shown at 300ms must stay live when cutoff is 150ms")
	}
}

func TestRollingRespectsMaxLines(t *testing.T) {
	cfg := captionConfig()
	cfg.MaxLines = 3
	s := impl.NewLiveCaptionStabilizer(cfg)
	for i := 0; i < 5; i++ {
		seg := fmt.Sprintf("seg-%d", i)
		mustUpdate(t, s, partial(seg, 1, seg, time.Duration(i)*100*time.Millisecond, time.Duration(i)*100*time.Millisecond))
	}
	lines := s.Rolling()
	if len(lines) != 3 {
		t.Fatalf("rolling lines = %d, want the cap 3", len(lines))
	}
	if lines[0].SegID != "seg-2" || lines[2].SegID != "seg-4" {
		t.Fatalf("rolling order = %q..%q, want the newest three in display order", lines[0].SegID, lines[2].SegID)
	}
	if lines[0].Start >= lines[1].Start || lines[1].Start >= lines[2].Start {
		t.Fatalf("rolling order must follow reveal order, got %v", []time.Duration{lines[0].Start, lines[1].Start, lines[2].Start})
	}
}

func TestCorrectionWindowExpired(t *testing.T) {
	cfg := captionConfig()
	cfg.CorrectionWindow = 200 * time.Millisecond
	s := impl.NewLiveCaptionStabilizer(cfg)
	mustUpdate(t, s, partial("seg-a", 1, "draft", 0, 100*time.Millisecond))
	mustUpdate(t, s, partial("seg-a", 2, "too late", 0, 400*time.Millisecond))
	lines := s.Rolling()
	if len(lines) != 1 || lines[0].Text != "draft" {
		t.Fatalf("late edit must not change text, lines = %+v", lines)
	}
	if st := s.Stats(); st.LateEdits != 1 {
		t.Fatalf("late edits = %d, want 1", st.LateEdits)
	}
}

func TestSetCorrectionWindowRejectsNegative(t *testing.T) {
	s := impl.NewLiveCaptionStabilizer(captionConfig())
	if err := s.SetCorrectionWindow(-1); !errors.Is(err, impl.ErrInvalidWindow) {
		t.Fatalf("negative window = %v, want ErrInvalidWindow", err)
	}
	if err := s.SetCorrectionWindow(250 * time.Millisecond); err != nil {
		t.Fatalf("valid window must be accepted, got %v", err)
	}
}

func TestUpdateRejectsBlankSegment(t *testing.T) {
	s := impl.NewLiveCaptionStabilizer(captionConfig())
	if err := s.Update(partial("   ", 1, "text", 0, 100*time.Millisecond)); !errors.Is(err, impl.ErrBadPartial) {
		t.Fatalf("blank segment = %v, want ErrBadPartial", err)
	}
	if err := s.Update(partial("seg-a", 0, "text", 0, 100*time.Millisecond)); !errors.Is(err, impl.ErrBadPartial) {
		t.Fatalf("zero seq = %v, want ErrBadPartial", err)
	}
	if got := len(s.Rolling()); got != 0 {
		t.Fatalf("rejected partials must not create lines, got %d", got)
	}
}

func TestStatsTrackReceivedAndApplied(t *testing.T) {
	s := impl.NewLiveCaptionStabilizer(captionConfig())
	for i := 1; i <= 3; i++ {
		mustUpdate(t, s, partial("seg-a", i, fmt.Sprintf("v%d", i), 0, time.Duration(i)*100*time.Millisecond))
	}
	mustUpdate(t, s, partial("seg-b", 1, "other", 200*time.Millisecond, 350*time.Millisecond))
	st := s.Stats()
	if st.Received != 4 || st.Applied != 4 {
		t.Fatalf("stats received=%d applied=%d, want 4/4", st.Received, st.Applied)
	}
	if st.Segments != 2 {
		t.Fatalf("segments = %d, want 2", st.Segments)
	}
}

func TestSnapshotRestoreKeepsLinesAndCounters(t *testing.T) {
	cfg := captionConfig()
	s := impl.NewLiveCaptionStabilizer(cfg)
	mustUpdate(t, s, partial("seg-a", 3, "alpha", 0, 100*time.Millisecond))
	mustUpdate(t, s, partial("seg-b", 1, "beta", 150*time.Millisecond, 200*time.Millisecond))
	if err := s.Finalize("seg-a"); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	data, err := s.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewLiveCaptionStabilizer(cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	lines := restored.Rolling()
	if len(lines) != 2 {
		t.Fatalf("restored lines = %d, want 2", len(lines))
	}
	if lines[0].Text != "alpha" || !lines[0].Frozen || lines[0].Seq != 3 {
		t.Fatalf("restored first line = %+v, want frozen alpha seq 3", lines[0])
	}
	if lines[1].Text != "beta" || lines[1].Frozen {
		t.Fatalf("restored second line = %+v, want live beta", lines[1])
	}
	if st := restored.Stats(); st.Applied != 2 || st.Frozen != 1 || st.Segments != 2 {
		t.Fatalf("restored stats = %+v, want applied 2 frozen 1 segments 2", st)
	}
	mustUpdate(t, restored, partial("seg-b", 2, "beta2", 150*time.Millisecond, 300*time.Millisecond))
	if got := restored.Rolling()[1].Text; got != "beta2" {
		t.Fatalf("update after restore = %q, want beta2", got)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	s := impl.NewLiveCaptionStabilizer(captionConfig())
	mustUpdate(t, s, partial("seg-a", 1, "keep", 0, 100*time.Millisecond))
	if err := s.Restore([]byte("{broken")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot = %v, want ErrBadSnapshot", err)
	}
	lines := s.Rolling()
	if len(lines) != 1 || lines[0].Text != "keep" {
		t.Fatalf("failed restore must not touch live state, lines = %+v", lines)
	}
}

func TestConcurrentUpdatesAcrossSegments(t *testing.T) {
	cfg := captionConfig()
	cfg.MaxLines = 32
	s := impl.NewLiveCaptionStabilizer(cfg)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			seg := fmt.Sprintf("seg-%02d", i)
			for k := 1; k <= 10; k++ {
				p := partial(seg, k, fmt.Sprintf("%s-v%d", seg, k), time.Duration(i)*time.Millisecond, time.Duration(k)*10*time.Millisecond)
				if err := s.Update(p); err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
					return
				}
			}
		}(i)
	}
	wg.Wait()
	if firstErr != nil {
		t.Fatalf("concurrent update failed: %v", firstErr)
	}
	if lines := s.Rolling(); len(lines) != 16 {
		t.Fatalf("rolling lines = %d, want 16", len(lines))
	}
	if st := s.Stats(); st.Applied != 160 || st.Received != 160 {
		t.Fatalf("stats applied=%d received=%d, want 160/160", st.Applied, st.Received)
	}
}

func TestResetClearsState(t *testing.T) {
	s := impl.NewLiveCaptionStabilizer(captionConfig())
	mustUpdate(t, s, partial("seg-a", 1, "text", 0, 100*time.Millisecond))
	if err := s.Reset(); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if got := len(s.Rolling()); got != 0 {
		t.Fatalf("rolling lines after reset = %d, want 0", got)
	}
	if st := s.Stats(); st != (impl.CaptionStats{}) {
		t.Fatalf("stats after reset = %+v, want zero", st)
	}
	if err := s.Finalize("seg-a"); !errors.Is(err, impl.ErrUnknownSegment) {
		t.Fatalf("reset must forget segments, finalize got %v", err)
	}
}
