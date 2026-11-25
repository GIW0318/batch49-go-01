package task

import (
	"errors"
	"sort"
	"sync"
	"testing"

	impl "gsb/batch49-go-game-c-04"
)

// buildADTS assembles one ADTS frame with a zero filled payload.
func buildADTS(payload, sfi, channels int, crc bool) []byte {
	header := impl.ADTSHeaderSize
	protection := 1
	if crc {
		header = impl.ADTSHeaderCRCSize
		protection = 0
	}
	length := header + payload
	buf := make([]byte, length)
	buf[0] = 0xFF
	buf[1] = 0xF0 | byte(protection)
	buf[2] = byte(1<<6 | (sfi << 2) | ((channels >> 2) & 1))
	buf[3] = byte(((channels & 3) << 6) | ((length >> 11) & 0x03))
	buf[4] = byte((length >> 3) & 0xFF)
	buf[5] = byte(((length & 0x07) << 5) | 0x1F)
	buf[6] = 0xFC
	return buf
}

func newScanner(t *testing.T) *impl.AdtsFrameScanner {
	t.Helper()
	return impl.NewAdtsFrameScanner()
}

func TestFeedParsesSingleFrame(t *testing.T) {
	s := newScanner(t)
	frames, err := s.Feed(buildADTS(5, 4, 2, false))
	if err != nil {
		t.Fatalf("feed: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("frames = %d, want 1", len(frames))
	}
	f := frames[0]
	if f.Index != 1 || f.Offset != 0 {
		t.Fatalf("frame index/offset = %d/%d, want 1/0", f.Index, f.Offset)
	}
	if f.Length != 12 || f.HeaderLen != impl.ADTSHeaderSize {
		t.Fatalf("frame length/header = %d/%d, want 12/7", f.Length, f.HeaderLen)
	}
	if f.SampleRate != 44100 || f.Channels != 2 {
		t.Fatalf("format = %d/%d, want 44100/2", f.SampleRate, f.Channels)
	}
	if f.FormatChanged {
		t.Fatalf("first frame must not report a format change")
	}
}

func TestFeedParsesMultipleFramesInOrder(t *testing.T) {
	s := newScanner(t)
	stream := append(buildADTS(3, 4, 2, false), buildADTS(4, 4, 2, false)...)
	stream = append(stream, buildADTS(5, 4, 2, false)...)
	frames, err := s.Feed(stream)
	if err != nil {
		t.Fatalf("feed: %v", err)
	}
	if len(frames) != 3 {
		t.Fatalf("frames = %d, want 3", len(frames))
	}
	wantOffsets := []int64{0, 10, 21}
	for i, f := range frames {
		if f.Index != i+1 {
			t.Fatalf("frame %d index = %d, want %d", i, f.Index, i+1)
		}
		if f.Offset != wantOffsets[i] {
			t.Fatalf("frame %d offset = %d, want %d", i, f.Offset, wantOffsets[i])
		}
	}
}

func TestFeedRetainsPartialFrameUntilComplete(t *testing.T) {
	s := newScanner(t)
	frame := buildADTS(5, 4, 2, false)
	frames, err := s.Feed(frame[:4])
	if err != nil {
		t.Fatalf("partial feed: %v", err)
	}
	if len(frames) != 0 {
		t.Fatalf("partial header must not emit a frame, got %d", len(frames))
	}
	if got := s.Pending(); got != 4 {
		t.Fatalf("pending = %d, want 4", got)
	}
	frames, err = s.Feed(frame[4:])
	if err != nil {
		t.Fatalf("completion feed: %v", err)
	}
	if len(frames) != 1 || frames[0].Index != 1 || frames[0].Length != 12 {
		t.Fatalf("completed frame = %+v, want index 1 length 12", frames)
	}
	if got := s.Pending(); got != 0 {
		t.Fatalf("pending after completion = %d, want 0", got)
	}
}

func TestFeedReportsCRCProtectedHeaderLength(t *testing.T) {
	s := newScanner(t)
	frames, err := s.Feed(buildADTS(4, 3, 1, true))
	if err != nil {
		t.Fatalf("feed: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("frames = %d, want 1", len(frames))
	}
	if frames[0].HeaderLen != impl.ADTSHeaderCRCSize {
		t.Fatalf("header length = %d, want 9", frames[0].HeaderLen)
	}
	if frames[0].Length != 13 {
		t.Fatalf("frame length = %d, want 13", frames[0].Length)
	}
	if frames[0].SampleRate != 48000 || frames[0].Channels != 1 {
		t.Fatalf("format = %d/%d, want 48000/1", frames[0].SampleRate, frames[0].Channels)
	}
}

func TestFeedSkipsGarbageAndCountsBytes(t *testing.T) {
	s := newScanner(t)
	stream := append([]byte{0x00, 0x12, 0x34}, buildADTS(2, 4, 2, false)...)
	frames, err := s.Feed(stream)
	if err != nil {
		t.Fatalf("feed: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("frames = %d, want 1", len(frames))
	}
	if frames[0].Offset != 3 {
		t.Fatalf("frame offset = %d, want 3", frames[0].Offset)
	}
	st := s.Stats()
	if st.SkippedBytes != 3 || st.Resyncs != 0 {
		t.Fatalf("stats = %+v, want skipped 3 resyncs 0", st)
	}
}

func TestSetMaxResyncRejectsNonPositive(t *testing.T) {
	s := newScanner(t)
	if err := s.SetMaxResync(0); !errors.Is(err, impl.ErrBadMaxResync) {
		t.Fatalf("zero must report ErrBadMaxResync, got %v", err)
	}
	if err := s.SetMaxResync(-5); !errors.Is(err, impl.ErrBadMaxResync) {
		t.Fatalf("negative must report ErrBadMaxResync, got %v", err)
	}
	if err := s.SetMaxResync(64); err != nil {
		t.Fatalf("64 must be accepted, got %v", err)
	}
}

func TestResyncGiveUpCountsAndRecovers(t *testing.T) {
	s := newScanner(t)
	if err := s.SetMaxResync(4); err != nil {
		t.Fatalf("set max resync: %v", err)
	}
	garbage := []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	stream := append(garbage, buildADTS(3, 4, 2, false)...)
	frames, err := s.Feed(stream)
	if err != nil {
		t.Fatalf("feed: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("frames = %d, want 1 after recovery", len(frames))
	}
	st := s.Stats()
	if st.Resyncs != 1 {
		t.Fatalf("resyncs = %d, want 1", st.Resyncs)
	}
	if st.SkippedBytes != 6 {
		t.Fatalf("skipped = %d, want 6", st.SkippedBytes)
	}
}

func TestFormatChangeReported(t *testing.T) {
	s := newScanner(t)
	stream := append(buildADTS(2, 4, 2, false), buildADTS(2, 3, 1, false)...)
	frames, err := s.Feed(stream)
	if err != nil {
		t.Fatalf("feed: %v", err)
	}
	if len(frames) != 2 {
		t.Fatalf("frames = %d, want 2", len(frames))
	}
	if frames[0].FormatChanged {
		t.Fatalf("first frame must not report a change")
	}
	if !frames[1].FormatChanged {
		t.Fatalf("second frame must report the rate/channel change")
	}
	if frames[1].SampleRate != 48000 || frames[1].Channels != 1 {
		t.Fatalf("second format = %d/%d, want 48000/1", frames[1].SampleRate, frames[1].Channels)
	}
}

func TestSetMaxFrameBytesValidates(t *testing.T) {
	s := newScanner(t)
	if err := s.SetMaxFrameBytes(0); !errors.Is(err, impl.ErrBadMaxFrame) {
		t.Fatalf("zero must report ErrBadMaxFrame, got %v", err)
	}
	if err := s.SetMaxFrameBytes(20000); !errors.Is(err, impl.ErrBadMaxFrame) {
		t.Fatalf("oversized limit must report ErrBadMaxFrame, got %v", err)
	}
	if err := s.SetMaxFrameBytes(4096); err != nil {
		t.Fatalf("4096 must be accepted, got %v", err)
	}
}

func TestPartialHeaderBufferedInStats(t *testing.T) {
	s := newScanner(t)
	frame := buildADTS(6, 4, 2, false)
	if _, err := s.Feed(frame[:6]); err != nil {
		t.Fatalf("partial header feed: %v", err)
	}
	st := s.Stats()
	if st.Buffered != 6 || st.Frames != 0 {
		t.Fatalf("stats = %+v, want buffered 6 frames 0", st)
	}
	if got := s.Pending(); got != 6 {
		t.Fatalf("pending = %d, want 6", got)
	}
}

func TestSnapshotRestoreKeepsStreamPosition(t *testing.T) {
	s := newScanner(t)
	first := buildADTS(3, 4, 2, false)
	second := buildADTS(4, 4, 2, false)
	if _, err := s.Feed(append(append([]byte{}, first...), second...)); err != nil {
		t.Fatalf("feed: %v", err)
	}
	data, err := s.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	fresh := impl.NewAdtsFrameScanner()
	if err := fresh.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	third := buildADTS(5, 4, 2, false)
	frames, err := fresh.Feed(third)
	if err != nil {
		t.Fatalf("feed after restore: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("frames after restore = %d, want 1", len(frames))
	}
	if frames[0].Index != 3 {
		t.Fatalf("frame index after restore = %d, want 3", frames[0].Index)
	}
	if want := int64(len(first) + len(second)); frames[0].Offset != want {
		t.Fatalf("frame offset after restore = %d, want %d", frames[0].Offset, want)
	}
}

func TestRestoreRejectsCorruptPayload(t *testing.T) {
	s := newScanner(t)
	if err := s.Restore([]byte{0xde, 0xad}); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt payload must report ErrBadSnapshot, got %v", err)
	}
}

func TestResetClearsStream(t *testing.T) {
	s := newScanner(t)
	if _, err := s.Feed(buildADTS(2, 4, 2, false)); err != nil {
		t.Fatalf("first feed: %v", err)
	}
	if _, err := s.Feed(buildADTS(2, 4, 2, false)); err != nil {
		t.Fatalf("second feed: %v", err)
	}
	if err := s.Reset(); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if st := s.Stats(); st.Frames != 0 || st.Buffered != 0 {
		t.Fatalf("stats after reset = %+v, want zeros", st)
	}
	if got := s.FrameCount(); got != 0 {
		t.Fatalf("frame count after reset = %d, want 0", got)
	}
	frames, err := s.Feed(buildADTS(2, 4, 2, false))
	if err != nil {
		t.Fatalf("feed after reset: %v", err)
	}
	if len(frames) != 1 || frames[0].Index != 1 || frames[0].Offset != 0 {
		t.Fatalf("frame after reset = %+v, want index 1 offset 0", frames)
	}
}

func TestFeedByteByByteKeepsOrder(t *testing.T) {
	s := newScanner(t)
	stream := append(buildADTS(2, 4, 2, false), buildADTS(3, 4, 2, false)...)
	var got []impl.FrameInfo
	for _, b := range stream {
		frames, err := s.Feed([]byte{b})
		if err != nil {
			t.Fatalf("byte feed: %v", err)
		}
		got = append(got, frames...)
	}
	if len(got) != 2 {
		t.Fatalf("frames = %d, want 2", len(got))
	}
	if got[0].Index != 1 || got[1].Index != 2 {
		t.Fatalf("indices = %d,%d, want 1,2", got[0].Index, got[1].Index)
	}
	if got[1].Offset != int64(len(buildADTS(2, 4, 2, false))) {
		t.Fatalf("second offset = %d, want 9", got[1].Offset)
	}
}

func TestConcurrentFeedCountsAllFrames(t *testing.T) {
	s := newScanner(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var indices []int
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			frames, err := s.Feed(buildADTS(2, 4, 2, false))
			if err != nil {
				return
			}
			mu.Lock()
			for _, f := range frames {
				indices = append(indices, f.Index)
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	if got := s.FrameCount(); got != 8 {
		t.Fatalf("frame count = %d, want 8", got)
	}
	if len(indices) != 8 {
		t.Fatalf("reported frames = %d, want 8", len(indices))
	}
	sort.Ints(indices)
	for i, idx := range indices {
		if idx != i+1 {
			t.Fatalf("indices = %v, want 1..8", indices)
		}
	}
}
