//go:build stress

package task

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStressConcurrentCaptionLifecycle(t *testing.T) {
	const segmentCount = 24
	cfg := CaptionConfig{
		CorrectionWindow: 2 * time.Millisecond,
		MaxLines:         5,
		MaxSegments:      segmentCount * 2,
	}
	stabilizer := NewLiveCaptionStabilizer(cfg)

	var unexpectedMu sync.Mutex
	var unexpected []error
	record := func(op string, err error) {
		if err == nil || errors.Is(err, ErrUnknownSegment) || errors.Is(err, ErrSegmentLimit) {
			return
		}
		unexpectedMu.Lock()
		unexpected = append(unexpected, fmt.Errorf("%s: %w", op, err))
		unexpectedMu.Unlock()
	}

	var sequence [segmentCount]int64
	var wg sync.WaitGroup

	for segmentIndex := 0; segmentIndex < segmentCount; segmentIndex++ {
		segID := fmt.Sprintf("seg-%02d", segmentIndex)
		for worker := 0; worker < 2; worker++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 200; i++ {
					seq := int(atomic.AddInt64(&sequence[segmentIndex], 1))
					now := time.Duration(seq) * time.Millisecond
					err := stabilizer.Update(Partial{
						SegID: segID,
						Seq:   seq,
						Text:  fmt.Sprintf("%s-v%d", segID, seq),
						Start: time.Duration(segmentIndex) * time.Millisecond,
						Now:   now,
					})
					record("update", err)

					if i%7 == 0 && seq > 1 {
						err := stabilizer.Update(Partial{
							SegID: segID,
							Seq:   seq - 1,
							Text:  "stale",
							Start: time.Duration(segmentIndex) * time.Millisecond,
							Now:   now,
						})
						record("stale update", err)
					}
				}
			}()
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				if i%3 == 0 {
					err := stabilizer.Finalize(segID)
					record("finalize", err)
				}
				_ = stabilizer.Pending()
				_ = stabilizer.Rolling()
				_ = stabilizer.Stats()
			}
		}()
	}

	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(worker + 1)))
			for i := 0; i < 100; i++ {
				cutoff := time.Duration(rng.Intn(500)) * time.Millisecond
				if err := stabilizer.ForceFinalize(cutoff); err != nil {
					record("force finalize", err)
				}

				window := time.Duration(rng.Intn(10)) * time.Millisecond
				if err := stabilizer.SetCorrectionWindow(window); err != nil {
					record("set window", err)
				}
				if err := stabilizer.SetCorrectionWindow(-time.Duration(i + 1)); !errors.Is(err, ErrInvalidWindow) {
					record("negative window", fmt.Errorf("got %v", err))
				}
			}
		}(worker)
	}

	for worker := 0; worker < 2; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				data, err := stabilizer.Snapshot()
				if err != nil {
					record("snapshot", err)
					continue
				}
				if err := stabilizer.Restore(data); err != nil {
					record("restore own snapshot", err)
				}
				if i%17 == 0 {
					if err := stabilizer.Reset(); err != nil {
						record("reset", err)
					}
				}
			}
		}()
	}

	wg.Wait()

	if len(unexpected) != 0 {
		t.Fatalf("unexpected concurrent errors: %v", unexpected)
	}

	stabilizer.mu.RLock()

	if len(stabilizer.segments) != stabilizer.stats.Segments {
		t.Fatalf("map size %d != stats segments %d", len(stabilizer.segments), stabilizer.stats.Segments)
	}
	if len(stabilizer.order) != len(stabilizer.segments) {
		t.Fatalf("order length %d != map size %d", len(stabilizer.order), len(stabilizer.segments))
	}

	frozen := 0
	seen := make(map[string]struct{}, len(stabilizer.order))
	for _, segID := range stabilizer.order {
		if _, duplicated := seen[segID]; duplicated {
			t.Fatalf("segment %q duplicated in order", segID)
		}
		seen[segID] = struct{}{}

		segment, ok := stabilizer.segments[segID]
		if !ok {
			t.Fatalf("ordered segment %q missing from map", segID)
		}
		if segment.line.SegID != segID || segment.line.Seq < 1 {
			t.Fatalf("invalid segment state: %+v", segment.line)
		}
		if segment.line.Frozen {
			frozen++
		}
		if segment.finalized && !segment.line.Frozen {
			t.Fatalf("finalized segment %q is not frozen", segID)
		}
	}
	if frozen != stabilizer.stats.Frozen {
		t.Fatalf("frozen count %d != stats frozen %d", frozen, stabilizer.stats.Frozen)
	}

	expectedEvicted := stabilizer.stats.Segments - stabilizer.cfg.MaxLines
	if expectedEvicted < 0 {
		expectedEvicted = 0
	}
	if stabilizer.stats.Evicted != expectedEvicted {
		t.Fatalf("evicted %d, want %d", stabilizer.stats.Evicted, expectedEvicted)
	}
	maxLines := stabilizer.cfg.MaxLines
	stabilizer.mu.RUnlock()

	rolling := stabilizer.Rolling()
	if len(rolling) > maxLines {
		t.Fatalf("rolling length %d exceeds %d", len(rolling), maxLines)
	}
	rollingIDs := make(map[string]struct{}, len(rolling))
	for _, line := range rolling {
		if _, duplicated := rollingIDs[line.SegID]; duplicated {
			t.Fatalf("rolling contains duplicate segment %q", line.SegID)
		}
		rollingIDs[line.SegID] = struct{}{}
	}
}

func TestStressSnapshotContinuation(t *testing.T) {
	rng := rand.New(rand.NewSource(20261126))
	cfg := CaptionConfig{CorrectionWindow: 250 * time.Millisecond, MaxLines: 4, MaxSegments: 32}
	original := NewLiveCaptionStabilizer(cfg)
	restored := NewLiveCaptionStabilizer(cfg)

	randomPartial := func() Partial {
		segment := rng.Intn(10)
		return Partial{
			SegID: fmt.Sprintf("seg-%02d", segment),
			Seq:   1 + rng.Intn(12),
			Text:  fmt.Sprintf("text-%d-%d", segment, rng.Intn(1000)),
			Start: time.Duration(rng.Intn(200)) * time.Millisecond,
			Now:   time.Duration(rng.Intn(900)) * time.Millisecond,
		}
	}

	for step := 0; step < 300; step++ {
		p := randomPartial()
		errOriginal := original.Update(p)
		errRestored := restored.Update(p)
		if !errors.Is(errRestored, errOriginal) && (errOriginal == nil || errRestored == nil) {
			t.Fatalf("step %d update errors differ: original=%v restored=%v", step, errOriginal, errRestored)
		}

		switch rng.Intn(6) {
		case 0:
			segID := fmt.Sprintf("seg-%02d", rng.Intn(12))
			errA := original.Finalize(segID)
			errB := restored.Finalize(segID)
			if errors.Is(errA, ErrUnknownSegment) != errors.Is(errB, ErrUnknownSegment) {
				t.Fatalf("step %d finalize errors differ: %v / %v", step, errA, errB)
			}
		case 1:
			cutoff := time.Duration(rng.Intn(900)) * time.Millisecond
			if err := original.ForceFinalize(cutoff); err != nil {
				t.Fatal(err)
			}
			if err := restored.ForceFinalize(cutoff); err != nil {
				t.Fatal(err)
			}
		case 2:
			window := time.Duration(rng.Intn(500)) * time.Millisecond
			if err := original.SetCorrectionWindow(window); err != nil {
				t.Fatal(err)
			}
			if err := restored.SetCorrectionWindow(window); err != nil {
				t.Fatal(err)
			}
		}

		if step == 99 || step == 199 {
			data, err := original.Snapshot()
			if err != nil {
				t.Fatalf("snapshot step %d: %v", step, err)
			}
			if err := restored.Restore(data); err != nil {
				t.Fatalf("restore step %d: %v", step, err)
			}
			restoredData, err := restored.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(data, restoredData) {
				t.Fatalf("step %d restored snapshot does not round-trip exactly", step)
			}
		}
	}

	originalData, err := original.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	finalRestored := NewLiveCaptionStabilizer(cfg)
	if err := finalRestored.Restore(originalData); err != nil {
		t.Fatal(err)
	}
	finalData, err := finalRestored.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(originalData, finalData) {
		t.Fatal("final snapshot did not round-trip exactly")
	}

	for seq := 20; seq < 40; seq++ {
		p := Partial{SegID: "seg-00", Seq: seq, Text: fmt.Sprintf("continuation-%d", seq), Now: time.Duration(seq*20) * time.Millisecond}
		if err := original.Update(p); err != nil {
			t.Fatal(err)
		}
		if err := finalRestored.Update(p); err != nil {
			t.Fatal(err)
		}
	}

	afterOriginal, err := original.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	afterRestored, err := finalRestored.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterOriginal, afterRestored) {
		t.Fatal("state diverged after continuing from restored snapshot")
	}
}

func TestStressCorruptSnapshotLeavesStateUntouched(t *testing.T) {
	stabilizer := NewLiveCaptionStabilizer(CaptionConfig{MaxLines: 2, MaxSegments: 8})
	for i := 1; i <= 4; i++ {
		if err := stabilizer.Update(Partial{
			SegID: fmt.Sprintf("seg-%d", i),
			Seq:   i,
			Text:  fmt.Sprintf("text-%d", i),
			Now:   time.Duration(i*100) * time.Millisecond,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := stabilizer.ForceFinalize(250 * time.Millisecond); err != nil {
		t.Fatal(err)
	}

	good, err := stabilizer.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	before, err := stabilizer.Snapshot()
	if err != nil {
		t.Fatal(err)
	}

	corruptions := [][]byte{
		nil,
		{},
		[]byte("{broken"),
		[]byte("not-json"),
		[]byte(`{"magic":"wrong","payload":{},"checksum_sha256":""}`),
	}
	for i := range good {
		corrupted := append([]byte(nil), good...)
		corrupted[i] ^= 0x01
		corruptions = append(corruptions, corrupted)
	}

	for index, data := range corruptions {
		if err := stabilizer.Restore(data); !errors.Is(err, ErrBadSnapshot) {
			t.Fatalf("corruption %d: Restore error = %v, want ErrBadSnapshot", index, err)
		}
		after, err := stabilizer.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Fatalf("corruption %d changed live state", index)
		}
	}
}

func TestStressExplicitBoundaryBehaviors(t *testing.T) {
	t.Run("finalized updates are dropped without changing the line", func(t *testing.T) {
		stabilizer := NewLiveCaptionStabilizer(CaptionConfig{MaxLines: 4, MaxSegments: 4})
		first := Partial{SegID: "seg-a", Seq: 1, Text: "draft", Now: 100 * time.Millisecond}
		if err := stabilizer.Update(first); err != nil {
			t.Fatal(err)
		}
		if err := stabilizer.Finalize("seg-a"); err != nil {
			t.Fatal(err)
		}
		if err := stabilizer.Update(Partial{SegID: "seg-a", Seq: 2, Text: "late", Now: 120 * time.Millisecond}); err != nil {
			t.Fatalf("dropped final update should not return an API error: %v", err)
		}

		line := stabilizer.Rolling()[0]
		if line.Text != "draft" || line.Seq != 1 || !line.Frozen {
			t.Fatalf("finalized line changed: %+v", line)
		}
		stats := stabilizer.Stats()
		if stats.DroppedFinal != 1 || stats.Applied != 1 || stats.Frozen != 1 {
			t.Fatalf("stats = %+v", stats)
		}
	})

	t.Run("segment limit is enforced", func(t *testing.T) {
		stabilizer := NewLiveCaptionStabilizer(CaptionConfig{MaxLines: 8, MaxSegments: 2})
		for _, segID := range []string{"seg-a", "seg-b"} {
			if err := stabilizer.Update(Partial{SegID: segID, Seq: 1, Text: segID}); err != nil {
				t.Fatal(err)
			}
		}
		err := stabilizer.Update(Partial{SegID: "seg-c", Seq: 1, Text: "overflow"})
		if !errors.Is(err, ErrSegmentLimit) {
			t.Fatalf("third segment error = %v, want ErrSegmentLimit", err)
		}
		if stats := stabilizer.Stats(); stats.Segments != 2 {
			t.Fatalf("segments = %d, want 2", stats.Segments)
		}
	})

	t.Run("force finalizes every eligible segment", func(t *testing.T) {
		stabilizer := NewLiveCaptionStabilizer(CaptionConfig{
			CorrectionWindow: time.Hour,
			MaxLines:         10,
			MaxSegments:      10,
		})
		for i := 0; i < 6; i++ {
			now := time.Duration(100+i*50) * time.Millisecond
			if err := stabilizer.Update(Partial{
				SegID: fmt.Sprintf("seg-%d", i),
				Seq:   1,
				Text:  "text",
				Now:   now,
			}); err != nil {
				t.Fatal(err)
			}
		}

		if err := stabilizer.ForceFinalize(300 * time.Millisecond); err != nil {
			t.Fatal(err)
		}
		lines := stabilizer.Rolling()
		for i, line := range lines {
			wantFrozen := 100+i*50 <= 300
			if line.Frozen != wantFrozen {
				t.Fatalf("line %d (%s) frozen=%v, want %v", i, line.SegID, line.Frozen, wantFrozen)
			}
		}
		if stats := stabilizer.Stats(); stats.Frozen != 5 {
			t.Fatalf("frozen = %d, want 5", stats.Frozen)
		}
		if pending := stabilizer.Pending(); len(pending) != 1 {
			t.Fatalf("pending = %d, want 1", len(pending))
		}
	})
}
