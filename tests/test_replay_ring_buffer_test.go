package task

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	impl "gsb/batch49-go-game-c-12"
)

func ms(v int) time.Duration { return time.Duration(v) * time.Millisecond }

func testFrame(id, ptsMs int, key bool, bytes int) impl.EncodedFrame {
	return impl.EncodedFrame{
		ID:       id,
		PTS:      ms(ptsMs),
		Dur:      ms(500),
		Keyframe: key,
		Bytes:    bytes,
	}
}

func plainOptions() impl.Options {
	return impl.Options{MaxBytes: 100000, MaxDuration: time.Hour, MaxGOPs: 64}
}

func mustPush(t *testing.T, r *impl.ReplayRingBuffer, f impl.EncodedFrame) {
	t.Helper()
	if err := r.Push(f); err != nil {
		t.Fatalf("Push(frame %d pts %v) failed: %v", f.ID, f.PTS, err)
	}
}

func pushGOP(t *testing.T, r *impl.ReplayRingBuffer, baseMs int, keyBytes, deltaBytes int) {
	t.Helper()
	mustPush(t, r, testFrame(baseMs, baseMs, true, keyBytes))
	mustPush(t, r, testFrame(baseMs+1, baseMs+100, false, deltaBytes))
	mustPush(t, r, testFrame(baseMs+2, baseMs+200, false, deltaBytes))
}

func TestFreshBufferIsEmpty(t *testing.T) {
	r := impl.NewReplayRingBuffer(plainOptions())
	span := r.Available()
	if span.Frames != 0 || span.Bytes != 0 || span.From != 0 || span.To != 0 {
		t.Fatalf("fresh span = %+v, want an empty window", span)
	}
	if got := r.FrameCount(); got != 0 {
		t.Fatalf("frame count = %d, want 0", got)
	}
	if got := r.ByteCount(); got != 0 {
		t.Fatalf("byte count = %d, want 0", got)
	}
	if r.Paused() {
		t.Fatalf("a fresh buffer must not be paused")
	}
	if _, err := r.Export(0, ms(1000)); !errors.Is(err, impl.ErrEmptyBuffer) {
		t.Fatalf("export on an empty buffer must fail with ErrEmptyBuffer, got %v", err)
	}
}

func TestPushStoresFramesAndReportsSpan(t *testing.T) {
	r := impl.NewReplayRingBuffer(plainOptions())
	mustPush(t, r, testFrame(1, 0, true, 100))
	mustPush(t, r, testFrame(2, 500, false, 100))
	mustPush(t, r, testFrame(3, 1000, false, 100))
	if got := r.FrameCount(); got != 3 {
		t.Fatalf("frame count = %d, want 3", got)
	}
	if got := r.ByteCount(); got != 300 {
		t.Fatalf("byte count = %d, want 300", got)
	}
	span := r.Available()
	if span.Frames != 3 || span.Bytes != 300 {
		t.Fatalf("span = %+v, want 3 frames and 300 bytes", span)
	}
	if span.From != 0 || span.To != ms(1000) {
		t.Fatalf("span window = [%v %v], want [0s 1s]", span.From, span.To)
	}
}

func TestPushRejectsBadFrame(t *testing.T) {
	r := impl.NewReplayRingBuffer(plainOptions())
	if err := r.Push(testFrame(1, 0, true, 0)); !errors.Is(err, impl.ErrBadFrame) {
		t.Fatalf("zero sized frame must fail with ErrBadFrame, got %v", err)
	}
	if err := r.Push(testFrame(2, 0, false, 100)); !errors.Is(err, impl.ErrNoKeyframe) {
		t.Fatalf("first frame without a keyframe must fail with ErrNoKeyframe, got %v", err)
	}
	mustPush(t, r, testFrame(3, 500, true, 100))
	if err := r.Push(testFrame(4, 500, false, 100)); !errors.Is(err, impl.ErrBadFrame) {
		t.Fatalf("repeated pts must fail with ErrBadFrame, got %v", err)
	}
	if err := r.Push(testFrame(5, 400, false, 100)); !errors.Is(err, impl.ErrBadFrame) {
		t.Fatalf("backward pts must fail with ErrBadFrame, got %v", err)
	}
	if err := r.Push(testFrame(6, 600, false, -5)); !errors.Is(err, impl.ErrBadFrame) {
		t.Fatalf("negative payload must fail with ErrBadFrame, got %v", err)
	}
	if got := r.FrameCount(); got != 1 {
		t.Fatalf("frame count = %d, want the single accepted frame", got)
	}
}

func TestPushRejectsOversizedFrame(t *testing.T) {
	r := impl.NewReplayRingBuffer(impl.Options{MaxBytes: 1000, MaxDuration: time.Hour, MaxGOPs: 8})
	if err := r.Push(testFrame(1, 0, true, 1500)); !errors.Is(err, impl.ErrTooLarge) {
		t.Fatalf("frame above the byte budget must fail with ErrTooLarge, got %v", err)
	}
	if got := r.FrameCount(); got != 0 {
		t.Fatalf("frame count = %d, want 0 after the rejected frame", got)
	}
	if got := r.ByteCount(); got != 0 {
		t.Fatalf("byte count = %d, want 0 after the rejected frame", got)
	}
	mustPush(t, r, testFrame(2, 0, true, 400))
	if got := r.ByteCount(); got != 400 {
		t.Fatalf("byte count = %d, want the accepted 400 byte frame", got)
	}
}

func TestEvictionDropsWholeImageGroups(t *testing.T) {
	r := impl.NewReplayRingBuffer(impl.Options{MaxBytes: 700, MaxDuration: time.Hour, MaxGOPs: 16})
	pushGOP(t, r, 0, 100, 100)
	pushGOP(t, r, 1000, 100, 100)
	if got := r.FrameCount(); got != 6 {
		t.Fatalf("frame count = %d, want 6 inside the budget", got)
	}
	pushGOP(t, r, 2000, 100, 100)
	if got := r.FrameCount(); got != 6 {
		t.Fatalf("frame count = %d, want 6 after one group is dropped", got)
	}
	if got := r.ByteCount(); got != 600 {
		t.Fatalf("byte count = %d, want 600 after one group is dropped", got)
	}
	if got := r.DroppedGOPs(); got != 1 {
		t.Fatalf("dropped groups = %d, want 1", got)
	}
	span := r.Available()
	if span.From != ms(1000) {
		t.Fatalf("window start = %v, want the second group start 1s", span.From)
	}
	if head, ok := r.KeyframeAtOrBefore(span.From); !ok || head != span.From {
		t.Fatalf("window must start on a keyframe, got %v ok=%v", head, ok)
	}
}

func TestEvictionRespectsDurationBudget(t *testing.T) {
	r := impl.NewReplayRingBuffer(impl.Options{MaxBytes: 1 << 20, MaxDuration: 3 * time.Second, MaxGOPs: 64})
	for i := 0; i < 6; i++ {
		base := i * 1000
		mustPush(t, r, testFrame(base, base, true, 100))
	}
	span := r.Available()
	if span.From != ms(2000) {
		t.Fatalf("window start = %v, want 2s after trimming to three seconds", span.From)
	}
	if span.To != ms(5000) {
		t.Fatalf("window end = %v, want the newest frame at 5s", span.To)
	}
	if got := r.FrameCount(); got != 4 {
		t.Fatalf("frame count = %d, want 4 frames inside three seconds", got)
	}
	if got := r.DroppedGOPs(); got != 2 {
		t.Fatalf("dropped groups = %d, want 2", got)
	}
}

func TestExportAlignsStartToKeyframe(t *testing.T) {
	r := impl.NewReplayRingBuffer(plainOptions())
	mustPush(t, r, testFrame(1, 0, true, 100))
	mustPush(t, r, testFrame(2, 500, false, 100))
	mustPush(t, r, testFrame(3, 900, false, 100))
	mustPush(t, r, testFrame(4, 1000, true, 100))
	mustPush(t, r, testFrame(5, 1500, false, 100))
	mustPush(t, r, testFrame(6, 2000, true, 100))
	mustPush(t, r, testFrame(7, 2500, false, 100))
	clip, err := r.Export(ms(1200), ms(1800))
	if err != nil {
		t.Fatalf("Export failed: %v", err)
	}
	if clip.Start != ms(1000) {
		t.Fatalf("clip start = %v, want the aligned keyframe at 1s", clip.Start)
	}
	if clip.End != ms(1500) {
		t.Fatalf("clip end = %v, want the last frame inside the range at 1.5s", clip.End)
	}
	if clip.Truncated {
		t.Fatalf("an in-range export must not be marked truncated")
	}
	if len(clip.Frames) != 2 {
		t.Fatalf("clip frames = %d, want 2", len(clip.Frames))
	}
	if clip.Frames[0].PTS != ms(1000) || !clip.Frames[0].Keyframe {
		t.Fatalf("clip must begin with the keyframe at 1s, got %+v", clip.Frames[0])
	}
	if clip.Frames[1].PTS != ms(1500) {
		t.Fatalf("clip second frame pts = %v, want 1.5s", clip.Frames[1].PTS)
	}
}

func TestExportTruncatesTail(t *testing.T) {
	r := impl.NewReplayRingBuffer(plainOptions())
	mustPush(t, r, testFrame(1, 0, true, 100))
	mustPush(t, r, testFrame(2, 1000, true, 100))
	mustPush(t, r, testFrame(3, 1500, false, 100))
	clip, err := r.Export(ms(1200), ms(9000))
	if err != nil {
		t.Fatalf("Export failed: %v", err)
	}
	if !clip.Truncated {
		t.Fatalf("a range past the newest frame must be marked truncated")
	}
	if clip.Start != ms(1000) {
		t.Fatalf("clip start = %v, want the keyframe at 1s", clip.Start)
	}
	if clip.End != ms(1500) {
		t.Fatalf("clip end = %v, want the newest buffered frame at 1.5s", clip.End)
	}
	if len(clip.Frames) != 2 {
		t.Fatalf("clip frames = %d, want 2", len(clip.Frames))
	}
	if got := clip.Frames[len(clip.Frames)-1].PTS; got != ms(1500) {
		t.Fatalf("last clip frame pts = %v, want 1.5s", got)
	}
}

func TestExportRejectsBadRange(t *testing.T) {
	r := impl.NewReplayRingBuffer(plainOptions())
	mustPush(t, r, testFrame(1, 0, true, 100))
	mustPush(t, r, testFrame(2, 500, false, 100))
	if _, err := r.Export(ms(1000), ms(1000)); !errors.Is(err, impl.ErrBadRange) {
		t.Fatalf("empty range must fail with ErrBadRange, got %v", err)
	}
	if _, err := r.Export(ms(2000), ms(500)); !errors.Is(err, impl.ErrBadRange) {
		t.Fatalf("reversed range must fail with ErrBadRange, got %v", err)
	}
	if _, err := r.Export(ms(-1), ms(500)); !errors.Is(err, impl.ErrBadRange) {
		t.Fatalf("negative start must fail with ErrBadRange, got %v", err)
	}
}

func TestPauseFreezesOverwritingButKeepsCapture(t *testing.T) {
	r := impl.NewReplayRingBuffer(impl.Options{MaxBytes: 700, MaxDuration: time.Hour, MaxGOPs: 16})
	pushGOP(t, r, 0, 100, 100)
	pushGOP(t, r, 1000, 100, 100)
	if err := r.Pause(); err != nil {
		t.Fatalf("Pause failed: %v", err)
	}
	if !r.Paused() {
		t.Fatalf("Paused must report true after Pause")
	}
	if err := r.Pause(); err != nil {
		t.Fatalf("a second Pause must be accepted, got %v", err)
	}
	mustPush(t, r, testFrame(99, 1500, false, 50))
	if got := r.FrameCount(); got != 7 {
		t.Fatalf("frame count = %d, want 7 while paused capture continues", got)
	}
	if got := r.DroppedGOPs(); got != 0 {
		t.Fatalf("dropped groups = %d, want 0 while overwriting is frozen", got)
	}
	if got := r.Available().To; got != ms(1500) {
		t.Fatalf("window end = %v, want the paused capture frame at 1.5s", got)
	}
}

func TestResumeLetsOverwritingRun(t *testing.T) {
	r := impl.NewReplayRingBuffer(impl.Options{MaxBytes: 700, MaxDuration: time.Hour, MaxGOPs: 16})
	pushGOP(t, r, 0, 100, 100)
	pushGOP(t, r, 1000, 100, 100)
	if err := r.Pause(); err != nil {
		t.Fatalf("Pause failed: %v", err)
	}
	mustPush(t, r, testFrame(90, 1500, false, 50))
	if err := r.Resume(); err != nil {
		t.Fatalf("Resume failed: %v", err)
	}
	if r.Paused() {
		t.Fatalf("Paused must report false after Resume")
	}
	mustPush(t, r, testFrame(91, 1600, false, 100))
	if got := r.DroppedGOPs(); got != 1 {
		t.Fatalf("dropped groups = %d, want 1 after resuming over the budget", got)
	}
	if got := r.Available().From; got != ms(1000) {
		t.Fatalf("window start = %v, want the second group at 1s", got)
	}
	if got := r.FrameCount(); got != 5 {
		t.Fatalf("frame count = %d, want 5 after the oldest group is dropped", got)
	}
}

func TestKeyframeAtOrBefore(t *testing.T) {
	r := impl.NewReplayRingBuffer(plainOptions())
	mustPush(t, r, testFrame(1, 1000, true, 100))
	mustPush(t, r, testFrame(2, 1500, false, 100))
	mustPush(t, r, testFrame(3, 2000, true, 100))
	mustPush(t, r, testFrame(4, 2500, false, 100))
	cases := []struct {
		at   time.Duration
		want time.Duration
		ok   bool
	}{
		{ms(1800), ms(1000), true},
		{ms(2000), ms(2000), true},
		{ms(9000), ms(2000), true},
		{ms(900), 0, false},
	}
	for _, tc := range cases {
		got, ok := r.KeyframeAtOrBefore(tc.at)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("KeyframeAtOrBefore(%v) = %v,%v want %v,%v", tc.at, got, ok, tc.want, tc.ok)
		}
	}
}

func TestSnapshotRestorePreservesBuffer(t *testing.T) {
	r := impl.NewReplayRingBuffer(impl.Options{MaxBytes: 700, MaxDuration: time.Hour, MaxGOPs: 16})
	pushGOP(t, r, 0, 100, 100)
	pushGOP(t, r, 1000, 100, 100)
	if err := r.Pause(); err != nil {
		t.Fatalf("Pause failed: %v", err)
	}
	wantClip, err := r.Export(ms(1100), ms(1900))
	if err != nil {
		t.Fatalf("Export before snapshot failed: %v", err)
	}
	data, err := r.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewReplayRingBuffer(impl.Options{MaxBytes: 100000, MaxDuration: time.Hour, MaxGOPs: 64})
	if err := restored.Restore(data); err != nil {
		t.Fatalf("Restore failed: %v", err)
	}
	if got, want := restored.FrameCount(), r.FrameCount(); got != want {
		t.Fatalf("restored frame count = %d, want %d", got, want)
	}
	if got, want := restored.ByteCount(), r.ByteCount(); got != want {
		t.Fatalf("restored byte count = %d, want %d", got, want)
	}
	if !restored.Paused() {
		t.Fatalf("restored buffer must keep the pause flag")
	}
	if got, want := restored.Available(), r.Available(); got != want {
		t.Fatalf("restored span = %+v, want %+v", got, want)
	}
	gotClip, err := restored.Export(ms(1100), ms(1900))
	if err != nil {
		t.Fatalf("Export after restore failed: %v", err)
	}
	if len(gotClip.Frames) != len(wantClip.Frames) {
		t.Fatalf("restored clip frames = %d, want %d", len(gotClip.Frames), len(wantClip.Frames))
	}
	for i := range wantClip.Frames {
		if gotClip.Frames[i].ID != wantClip.Frames[i].ID || gotClip.Frames[i].PTS != wantClip.Frames[i].PTS {
			t.Fatalf("restored clip frame %d = %+v, want %+v", i, gotClip.Frames[i], wantClip.Frames[i])
		}
	}
	if err := restored.Resume(); err != nil {
		t.Fatalf("Resume after restore failed: %v", err)
	}
	pushGOP(t, restored, 2000, 100, 100)
	if got := restored.DroppedGOPs(); got != 1 {
		t.Fatalf("restored buffer dropped %d groups, want 1 when using the restored budget", got)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	r := impl.NewReplayRingBuffer(plainOptions())
	mustPush(t, r, testFrame(1, 0, true, 100))
	mustPush(t, r, testFrame(2, 500, false, 100))
	if err := r.Restore([]byte("]not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := r.FrameCount(); got != 2 {
		t.Fatalf("frame count = %d, want the live buffer untouched", got)
	}
	if got := r.Available().To; got != ms(500) {
		t.Fatalf("window end = %v, want the live buffer untouched at 0.5s", got)
	}
	clip, err := r.Export(0, ms(500))
	if err != nil {
		t.Fatalf("the live buffer must keep working, got %v", err)
	}
	if len(clip.Frames) != 2 {
		t.Fatalf("live clip frames = %d, want 2", len(clip.Frames))
	}
}

func TestConcurrentPushAndExport(t *testing.T) {
	r := impl.NewReplayRingBuffer(impl.Options{MaxBytes: 1 << 20, MaxDuration: time.Hour, MaxGOPs: 4096})
	var failures int64
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			f := testFrame(i, i*100, i%5 == 0, 100)
			if err := r.Push(f); err != nil {
				atomic.AddInt64(&failures, 1)
				return
			}
		}
	}()
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := 0; k < 150; k++ {
				clip, err := r.Export(0, time.Hour)
				if err != nil {
					if errors.Is(err, impl.ErrEmptyBuffer) || errors.Is(err, impl.ErrOverwritten) {
						continue
					}
					atomic.AddInt64(&failures, 1)
					return
				}
				if len(clip.Frames) == 0 {
					atomic.AddInt64(&failures, 1)
					return
				}
				if !clip.Frames[0].Keyframe {
					atomic.AddInt64(&failures, 1)
					return
				}
				if clip.Start != clip.Frames[0].PTS || clip.End != clip.Frames[len(clip.Frames)-1].PTS {
					atomic.AddInt64(&failures, 1)
					return
				}
				for i := 1; i < len(clip.Frames); i++ {
					if clip.Frames[i].PTS <= clip.Frames[i-1].PTS {
						atomic.AddInt64(&failures, 1)
						return
					}
				}
				if span := r.Available(); span.Frames == 0 {
					atomic.AddInt64(&failures, 1)
					return
				}
			}
		}()
	}
	wg.Wait()
	if failures != 0 {
		t.Fatalf("%d concurrent workers observed an invalid clip or a failing push", failures)
	}
	if got := r.FrameCount(); got != 200 {
		t.Fatalf("frame count = %d, want all 200 frames inside the budget", got)
	}
	span := r.Available()
	if head, ok := r.KeyframeAtOrBefore(span.From); !ok || head != span.From {
		t.Fatalf("buffered window must start on a keyframe, got %v ok=%v", head, ok)
	}
	clip, err := r.Export(ms(1000), ms(3000))
	if err != nil {
		t.Fatalf("final Export failed: %v", err)
	}
	if clip.Start != ms(1000) || clip.End != ms(3000) {
		t.Fatalf("final clip = [%v %v], want [1s 3s]", clip.Start, clip.End)
	}
}
