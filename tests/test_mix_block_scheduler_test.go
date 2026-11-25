package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch49-go-game-c-02"
)

func newScheduler(t *testing.T) *impl.MixBlockScheduler {
	t.Helper()
	s, err := impl.NewMixBlockScheduler(48000, 8)
	if err != nil {
		t.Fatalf("new scheduler must succeed, got %v", err)
	}
	return s
}

func dcFrame(n int, v float32) impl.Frame {
	l := make([]float32, n)
	r := make([]float32, n)
	for i := 0; i < n; i++ {
		l[i] = v
		r[i] = v
	}
	return impl.Frame{Left: l, Right: r}
}

func TestNewRejectsBadSampleRate(t *testing.T) {
	if _, err := impl.NewMixBlockScheduler(4000, 64); !errors.Is(err, impl.ErrBadSampleRate) {
		t.Fatalf("rate 4000 must report ErrBadSampleRate, got %v", err)
	}
	if _, err := impl.NewMixBlockScheduler(400000, 64); !errors.Is(err, impl.ErrBadSampleRate) {
		t.Fatalf("rate 400000 must report ErrBadSampleRate, got %v", err)
	}
	s, err := impl.NewMixBlockScheduler(48000, 64)
	if err != nil {
		t.Fatalf("rate 48000 must be accepted, got %v", err)
	}
	if got := s.Stats().SampleRate; got != 48000 {
		t.Fatalf("reported rate = %d, want 48000", got)
	}
}

func TestNewRejectsBadBlockSize(t *testing.T) {
	if _, err := impl.NewMixBlockScheduler(48000, 0); !errors.Is(err, impl.ErrBadBlockSize) {
		t.Fatalf("block 0 must report ErrBadBlockSize, got %v", err)
	}
	if _, err := impl.NewMixBlockScheduler(48000, 5000); !errors.Is(err, impl.ErrBadBlockSize) {
		t.Fatalf("block 5000 must report ErrBadBlockSize, got %v", err)
	}
}

func TestSubmitRejectsChannelMismatch(t *testing.T) {
	s := newScheduler(t)
	if _, err := s.SubmitBlock(impl.Frame{}); !errors.Is(err, impl.ErrBadFrame) {
		t.Fatalf("empty frame must report ErrBadFrame, got %v", err)
	}
	bad := impl.Frame{Left: make([]float32, 4), Right: make([]float32, 5)}
	if _, err := s.SubmitBlock(bad); !errors.Is(err, impl.ErrBadFrame) {
		t.Fatalf("mismatched channels must report ErrBadFrame, got %v", err)
	}
}

func TestSubmitPadsShortBlock(t *testing.T) {
	s := newScheduler(t)
	out, err := s.SubmitBlock(dcFrame(3, 0.5))
	if err != nil {
		t.Fatalf("short block must be padded, got %v", err)
	}
	if len(out.Left) != 8 || len(out.Right) != 8 {
		t.Fatalf("output lengths = %d/%d, want 8/8", len(out.Left), len(out.Right))
	}
	if out.Left[0] <= 0 || out.Left[0] >= 0.5 {
		t.Fatalf("filtered first sample = %v, want value inside (0, 0.5)", out.Left[0])
	}
	for i := 3; i < 8; i++ {
		if out.Left[i] != 0 || out.Right[i] != 0 {
			t.Fatalf("pad sample %d = %v/%v, want silence", i, out.Left[i], out.Right[i])
		}
	}
	st := s.Stats()
	if st.Underruns != 1 || st.PadFrames != 5 || st.Blocks != 1 {
		t.Fatalf("stats = %+v, want underruns 1 pad 5 blocks 1", st)
	}
}

func TestSubmitExactBlockDoesNotPad(t *testing.T) {
	s := newScheduler(t)
	out, err := s.SubmitBlock(dcFrame(8, 0.25))
	if err != nil {
		t.Fatalf("exact block: %v", err)
	}
	if len(out.Left) != 8 {
		t.Fatalf("output length = %d, want 8", len(out.Left))
	}
	st := s.Stats()
	if st.Underruns != 0 || st.PadFrames != 0 {
		t.Fatalf("stats = %+v, want no padding", st)
	}
	if st.Blocks != 1 || st.OutputFrames != 8 {
		t.Fatalf("stats = %+v, want blocks 1 output 8", st)
	}
}

func TestFilterStateCarriesAcrossBlocks(t *testing.T) {
	s := newScheduler(t)
	in := dcFrame(8, 0.5)
	first, err := s.SubmitBlock(in)
	if err != nil {
		t.Fatalf("first block: %v", err)
	}
	second, err := s.SubmitBlock(in)
	if err != nil {
		t.Fatalf("second block: %v", err)
	}
	if first.Left[0] <= 0 || first.Left[0] >= 0.5 {
		t.Fatalf("first sample = %v, want inside (0, 0.5)", first.Left[0])
	}
	if first.Left[7] <= first.Left[0] {
		t.Fatalf("filter must rise inside the block: first=%v last=%v", first.Left[0], first.Left[7])
	}
	if second.Left[0] <= first.Left[7] {
		t.Fatalf("second block must continue from the first state: first last=%v second first=%v", first.Left[7], second.Left[0])
	}
	if second.Right[0] != second.Left[0] {
		t.Fatalf("channels must be filtered identically: left=%v right=%v", second.Left[0], second.Right[0])
	}
}

func TestSetBlockSizeValidatesRange(t *testing.T) {
	s := newScheduler(t)
	if err := s.SetBlockSize(0); !errors.Is(err, impl.ErrBadBlockSize) {
		t.Fatalf("block 0 must report ErrBadBlockSize, got %v", err)
	}
	if err := s.SetBlockSize(impl.MaxBlockSize + 1); !errors.Is(err, impl.ErrBadBlockSize) {
		t.Fatalf("oversized block must report ErrBadBlockSize, got %v", err)
	}
	if err := s.SetBlockSize(128); err != nil {
		t.Fatalf("block 128 must be accepted, got %v", err)
	}
	if got := s.Stats().BlockSize; got != 128 {
		t.Fatalf("reported block size = %d, want 128", got)
	}
	out, err := s.SubmitBlock(dcFrame(10, 0.1))
	if err != nil {
		t.Fatalf("submit after resize: %v", err)
	}
	if len(out.Left) != 128 {
		t.Fatalf("output length = %d, want 128", len(out.Left))
	}
}

func TestSetSampleRateValidatesRange(t *testing.T) {
	s := newScheduler(t)
	if err := s.SetSampleRate(4000); !errors.Is(err, impl.ErrBadSampleRate) {
		t.Fatalf("rate 4000 must report ErrBadSampleRate, got %v", err)
	}
	if err := s.SetSampleRate(96000); err != nil {
		t.Fatalf("rate 96000 must be accepted, got %v", err)
	}
	if got := s.Stats().SampleRate; got != 96000 {
		t.Fatalf("reported rate = %d, want 96000", got)
	}
}

func TestSetParamValidatesNameAndRange(t *testing.T) {
	s := newScheduler(t)
	if err := s.SetParam("drive", 0.5); !errors.Is(err, impl.ErrUnknownParam) {
		t.Fatalf("unknown parameter must report ErrUnknownParam, got %v", err)
	}
	if err := s.SetParam(impl.ParamGain, 5); !errors.Is(err, impl.ErrOutOfRange) {
		t.Fatalf("gain 5 must report ErrOutOfRange, got %v", err)
	}
	if err := s.SetParam(impl.ParamCutoff, 50); !errors.Is(err, impl.ErrOutOfRange) {
		t.Fatalf("cutoff 50 must report ErrOutOfRange, got %v", err)
	}
	if err := s.SetParam(impl.ParamGain, 1.5); err != nil {
		t.Fatalf("gain 1.5 must be accepted, got %v", err)
	}
	if got := s.Param(impl.ParamGain); got != 1.5 {
		t.Fatalf("gain = %v, want 1.5", got)
	}
}

func TestBypassPassesInputThrough(t *testing.T) {
	s := newScheduler(t)
	s.SetBypass(true)
	in := impl.Frame{Left: []float32{0.25, -0.5, 0.75, 1}, Right: []float32{0.1, -0.2, 0.3, 0.4}}
	out, err := s.SubmitBlock(in)
	if err != nil {
		t.Fatalf("bypassed submit: %v", err)
	}
	if len(out.Left) != 8 {
		t.Fatalf("output length = %d, want 8", len(out.Left))
	}
	for i := 0; i < 4; i++ {
		if out.Left[i] != in.Left[i] || out.Right[i] != in.Right[i] {
			t.Fatalf("sample %d = %v/%v, want %v/%v", i, out.Left[i], out.Right[i], in.Left[i], in.Right[i])
		}
	}
	for i := 4; i < 8; i++ {
		if out.Left[i] != 0 || out.Right[i] != 0 {
			t.Fatalf("pad sample %d must be silent, got %v/%v", i, out.Left[i], out.Right[i])
		}
	}
	if st := s.Stats(); st.Blocks != 1 || st.PadFrames != 4 {
		t.Fatalf("stats = %+v, want blocks 1 pad 4", st)
	}
}

func TestStatsCountsBlocksAndFrames(t *testing.T) {
	s := newScheduler(t)
	for i := 0; i < 3; i++ {
		if _, err := s.SubmitBlock(dcFrame(8, 0.2)); err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
	}
	st := s.Stats()
	if st.Blocks != 3 || st.InputFrames != 24 || st.OutputFrames != 24 || st.PadFrames != 0 {
		t.Fatalf("stats = %+v, want blocks 3 input 24 output 24 pad 0", st)
	}
}

func TestConcurrentSubmitCountsEveryBlock(t *testing.T) {
	s, err := impl.NewMixBlockScheduler(48000, 16)
	if err != nil {
		t.Fatalf("new scheduler: %v", err)
	}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 5; i++ {
				if _, err := s.SubmitBlock(dcFrame(16, 0.1)); err != nil {
					return
				}
			}
		}()
	}
	wg.Wait()
	st := s.Stats()
	if st.Blocks != 40 || st.OutputFrames != 640 || st.InputFrames != 640 {
		t.Fatalf("stats = %+v, want blocks 40 input 640 output 640", st)
	}
}

func TestSnapshotRestoreKeepsCounters(t *testing.T) {
	s := newScheduler(t)
	for i := 0; i < 2; i++ {
		if _, err := s.SubmitBlock(dcFrame(8, 0.3)); err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
	}
	data, err := s.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	fresh, err := impl.NewMixBlockScheduler(48000, 8)
	if err != nil {
		t.Fatalf("new scheduler: %v", err)
	}
	if err := fresh.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	st := fresh.Stats()
	if st.Blocks != 2 || st.OutputFrames != 16 || st.SampleRate != 48000 || st.BlockSize != 8 {
		t.Fatalf("restored stats = %+v, want blocks 2 output 16 rate 48000 block 8", st)
	}
}

func TestRestoreRejectsCorruptPayload(t *testing.T) {
	s := newScheduler(t)
	if err := s.Restore([]byte{0x00, 0x01, 0x02}); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt payload must report ErrBadSnapshot, got %v", err)
	}
}

func TestResetFilterRestartsResponse(t *testing.T) {
	s := newScheduler(t)
	in := dcFrame(8, 0.5)
	first, err := s.SubmitBlock(in)
	if err != nil {
		t.Fatalf("first block: %v", err)
	}
	settled := first.Left[7]
	if err := s.ResetFilter(); err != nil {
		t.Fatalf("reset filter: %v", err)
	}
	second, err := s.SubmitBlock(in)
	if err != nil {
		t.Fatalf("second block: %v", err)
	}
	if second.Left[0] != first.Left[0] {
		t.Fatalf("after reset first sample = %v, want fresh response %v", second.Left[0], first.Left[0])
	}
	if second.Left[0] >= settled {
		t.Fatalf("after reset sample %v must be below settled value %v", second.Left[0], settled)
	}
	if st := s.Stats(); st.Blocks != 2 {
		t.Fatalf("reset must not clear counters, blocks = %d", st.Blocks)
	}
}
