// Package task scans raw ADTS streams, cuts complete audio frames and
// recovers the sync point after damaged data.
package task

import (
	"encoding/binary"
	"errors"
	"sync"
)

// ADTS header sizes without and with the optional CRC word.
const (
	ADTSHeaderSize    = 7
	ADTSHeaderCRCSize = 9
	DefaultMaxResync  = 4096
	DefaultMaxFrame   = 8191
	MinMaxFrameBytes  = 64
	MaxMaxFrameBytes  = 8191
	MinMaxResync      = 1
	MaxMaxResync      = 1048576
)

// Errors returned by the scanner.
var (
	ErrBadMaxResync = errors.New("max resync bytes out of range")
	ErrBadMaxFrame  = errors.New("max frame size out of range")
	ErrBadSnapshot  = errors.New("snapshot payload is invalid")
)

// sampleRates maps the 4-bit sampling frequency index to Hz. Indices 13,
// 14 and 15 are reserved or explicit and never index this table.
var sampleRates = [13]int{
	96000, 88200, 64000, 48000, 44100, 32000, 24000,
	22050, 16000, 12000, 11025, 8000, 7350,
}

// snapshotMagic identifies and versions the serialised scanner state.
var snapshotMagic = []byte{'A', 'D', 'T', 'S', 0x01}

// FrameInfo describes one complete ADTS frame found in the stream.
type FrameInfo struct {
	Index         int
	Offset        int64
	Length        int
	SampleRate    int
	Channels      int
	HeaderLen     int
	FormatChanged bool
}

// ScanStats is a snapshot of the scanner counters.
type ScanStats struct {
	Frames       int
	SkippedBytes int64
	Resyncs      int
	Buffered     int
}

// AdtsFrameScanner owns the pending bytes, the frame counter and the limits.
type AdtsFrameScanner struct {
	mu            sync.Mutex
	buf           []byte
	consumed      int64
	nextIndex     int
	frames        int
	skipped       int64
	resyncs       int
	garbageRun    int
	resyncCounted bool
	skipRemaining int
	maxResync     int
	maxFrame      int
	hasFormat     bool
	lastRate      int
	lastChannels  int
}

// NewAdtsFrameScanner builds a scanner with the default limits.
func NewAdtsFrameScanner() *AdtsFrameScanner {
	return &AdtsFrameScanner{
		maxResync: DefaultMaxResync,
		maxFrame:  DefaultMaxFrame,
		nextIndex: 1,
	}
}

// Feed appends bytes to the pending buffer and returns every complete frame.
func (s *AdtsFrameScanner) Feed(buf []byte) ([]FrameInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf = append(s.buf, buf...)
	s.enforceCap()
	var frames []FrameInfo
	s.scan(&frames)
	return frames, nil
}

// Resync throws away the pending buffer and restarts scanning.
func (s *AdtsFrameScanner) Resync() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.skipped += int64(len(s.buf))
	s.buf = nil
	s.resyncs++
	s.garbageRun = 0
	s.resyncCounted = false
	s.skipRemaining = 0
	return nil
}

// SetMaxResync bounds how many unusable bytes are tolerated before a resync.
func (s *AdtsFrameScanner) SetMaxResync(n int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n < MinMaxResync || n > MaxMaxResync {
		return ErrBadMaxResync
	}
	s.maxResync = n
	return nil
}

// SetMaxFrameBytes bounds the largest frame the scanner will emit.
func (s *AdtsFrameScanner) SetMaxFrameBytes(n int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n < MinMaxFrameBytes || n > MaxMaxFrameBytes {
		return ErrBadMaxFrame
	}
	s.maxFrame = n
	return nil
}

// Stats returns a copy of the running counters.
func (s *AdtsFrameScanner) Stats() ScanStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return ScanStats{
		Frames:       s.frames,
		SkippedBytes: s.skipped,
		Resyncs:      s.resyncs,
		Buffered:     len(s.buf),
	}
}

// Pending reports how many bytes are buffered but not yet consumed.
func (s *AdtsFrameScanner) Pending() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.buf)
}

// FrameCount reports how many frames were emitted since the last reset.
func (s *AdtsFrameScanner) FrameCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.frames
}

// Reset clears the buffer, the frame counter and the statistics.
func (s *AdtsFrameScanner) Reset() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf = nil
	s.consumed = 0
	s.nextIndex = 1
	s.frames = 0
	s.skipped = 0
	s.resyncs = 0
	s.garbageRun = 0
	s.resyncCounted = false
	s.skipRemaining = 0
	s.hasFormat = false
	s.lastRate = 0
	s.lastChannels = 0
	return nil
}

// Snapshot serialises the pending buffer, counters and format state.
func (s *AdtsFrameScanner) Snapshot() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]byte{}, snapshotMagic...)
	out = binary.LittleEndian.AppendUint64(out, uint64(s.nextIndex))
	out = binary.LittleEndian.AppendUint64(out, uint64(s.frames))
	out = binary.LittleEndian.AppendUint64(out, uint64(s.skipped))
	out = binary.LittleEndian.AppendUint64(out, uint64(s.resyncs))
	out = binary.LittleEndian.AppendUint64(out, uint64(s.consumed))
	out = binary.LittleEndian.AppendUint64(out, uint64(s.maxResync))
	out = binary.LittleEndian.AppendUint64(out, uint64(s.maxFrame))
	out = binary.LittleEndian.AppendUint64(out, uint64(s.garbageRun))
	out = binary.LittleEndian.AppendUint64(out, uint64(s.skipRemaining))
	out = binary.LittleEndian.AppendUint64(out, uint64(s.lastRate))
	out = binary.LittleEndian.AppendUint64(out, uint64(s.lastChannels))
	var flags uint64
	if s.hasFormat {
		flags |= 1
	}
	if s.resyncCounted {
		flags |= 2
	}
	out = binary.LittleEndian.AppendUint64(out, flags)
	out = binary.LittleEndian.AppendUint64(out, uint64(len(s.buf)))
	out = append(out, s.buf...)
	return out, nil
}

// Restore replaces the whole scanner state from a snapshot, all or nothing.
func (s *AdtsFrameScanner) Restore(data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	const wordCount = 13
	headerLen := len(snapshotMagic) + wordCount*8
	if len(data) < headerLen {
		return ErrBadSnapshot
	}
	if string(data[:len(snapshotMagic)]) != string(snapshotMagic) {
		return ErrBadSnapshot
	}
	words := make([]uint64, wordCount)
	for i := range words {
		off := len(snapshotMagic) + i*8
		words[i] = binary.LittleEndian.Uint64(data[off : off+8])
	}
	bufLen := words[12]
	if uint64(len(data)-headerLen) != bufLen {
		return ErrBadSnapshot
	}
	nextIndex := int(words[0])
	maxResync := int(words[5])
	maxFrame := int(words[6])
	if nextIndex < 1 ||
		maxResync < MinMaxResync || maxResync > MaxMaxResync ||
		maxFrame < MinMaxFrameBytes || maxFrame > MaxMaxFrameBytes {
		return ErrBadSnapshot
	}
	pending := make([]byte, bufLen)
	copy(pending, data[headerLen:])
	s.nextIndex = nextIndex
	s.frames = int(words[1])
	s.skipped = int64(words[2])
	s.resyncs = int(words[3])
	s.consumed = int64(words[4])
	s.maxResync = maxResync
	s.maxFrame = maxFrame
	s.garbageRun = int(words[7])
	s.skipRemaining = int(words[8])
	s.lastRate = int(words[9])
	s.lastChannels = int(words[10])
	s.hasFormat = words[11]&1 != 0
	s.resyncCounted = words[11]&2 != 0
	s.buf = pending
	return nil
}

// enforceCap keeps the pending buffer within maxFrame+maxResync bytes so a
// long stretch of damaged data can never grow the buffer without bound.
func (s *AdtsFrameScanner) enforceCap() {
	limit := s.maxFrame + s.maxResync
	if len(s.buf) <= limit {
		return
	}
	drop := len(s.buf) - limit
	if s.skipRemaining > 0 {
		s.skipRemaining -= min(drop, s.skipRemaining)
	}
	s.skipped += int64(drop)
	s.resyncs++
	s.garbageRun = 0
	s.dropBytes(drop)
}

// scan consumes every complete frame currently available in the buffer.
func (s *AdtsFrameScanner) scan(frames *[]FrameInfo) {
	for {
		if s.skipRemaining > 0 {
			n := min(s.skipRemaining, len(s.buf))
			s.skipped += int64(n)
			s.dropBytes(n)
			s.skipRemaining -= n
			if s.skipRemaining > 0 {
				return
			}
			continue
		}
		idx := findSync(s.buf)
		if idx < 0 {
			keep := 0
			if n := len(s.buf); n > 0 && s.buf[n-1] == 0xFF {
				keep = 1
			}
			if garbage := len(s.buf) - keep; garbage > 0 {
				s.noteGarbage(garbage)
				s.dropBytes(garbage)
			}
			return
		}
		if idx > 0 {
			s.noteGarbage(idx)
			s.dropBytes(idx)
		}
		if len(s.buf) < ADTSHeaderSize {
			return
		}
		h := s.buf
		headerLen := ADTSHeaderSize
		if h[1]&0x01 == 0 {
			headerLen = ADTSHeaderCRCSize
		}
		sfi := int(h[2]>>2) & 0x0F
		if sfi == 13 || sfi == 14 {
			s.noteGarbage(1)
			s.dropBytes(1)
			continue
		}
		frameLen := int(h[3]&0x03)<<11 | int(h[4])<<3 | int(h[5])>>5
		if frameLen < headerLen {
			s.noteGarbage(1)
			s.dropBytes(1)
			continue
		}
		if sfi == 15 || frameLen > s.maxFrame {
			n := min(frameLen, len(s.buf))
			s.skipped += int64(n)
			s.dropBytes(n)
			s.skipRemaining = frameLen - n
			continue
		}
		if len(s.buf) < frameLen {
			return
		}
		rate := sampleRates[sfi]
		channels := int(h[2]&0x01)<<2 | int(h[3]>>6)&0x03
		changed := s.hasFormat && (rate != s.lastRate || channels != s.lastChannels)
		s.hasFormat = true
		s.lastRate = rate
		s.lastChannels = channels
		*frames = append(*frames, FrameInfo{
			Index:         s.nextIndex,
			Offset:        s.consumed,
			Length:        frameLen,
			SampleRate:    rate,
			Channels:      channels,
			HeaderLen:     headerLen,
			FormatChanged: changed,
		})
		s.nextIndex++
		s.frames++
		s.garbageRun = 0
		s.resyncCounted = false
		s.dropBytes(frameLen)
	}
}

// findSync returns the offset of the first byte pair that can start an ADTS
// header: syncword 0xFFF with layer bits cleared.
func findSync(buf []byte) int {
	for i := 0; i+1 < len(buf); i++ {
		if buf[i] != 0xFF {
			continue
		}
		b1 := buf[i+1]
		if b1&0xF0 != 0xF0 || (b1>>1)&0x03 != 0 {
			continue
		}
		return i
	}
	return -1
}

// noteGarbage counts unalignable bytes and trips a resync once the run of
// consecutive unusable bytes grows past the configured limit.
func (s *AdtsFrameScanner) noteGarbage(n int) {
	s.skipped += int64(n)
	s.garbageRun += n
	if s.garbageRun > s.maxResync && !s.resyncCounted {
		s.resyncs++
		s.resyncCounted = true
	}
}

// dropBytes removes n bytes from the front of the pending buffer.
func (s *AdtsFrameScanner) dropBytes(n int) {
	s.buf = s.buf[n:]
	s.consumed += int64(n)
	if len(s.buf) == 0 {
		s.buf = nil
	}
}
