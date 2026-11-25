// Package task keeps the most recent encoded frames of a live stream so that
// highlight clips can be exported at any moment.
package task

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"sync"
	"time"
)

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Default budgets used when the matching option is not set.
const (
	DefaultMaxBytes    = 4 << 20
	DefaultMaxDuration = 30 * time.Second
	DefaultMaxGOPs     = 8
)

// EncodedFrame is one encoded frame as handed over by the encoder.
type EncodedFrame struct {
	ID       int
	PTS      time.Duration
	Dur      time.Duration
	Keyframe bool
	Bytes    int
}

// Clip is one exported range of frames.
type Clip struct {
	Frames    []EncodedFrame
	Start     time.Duration
	End       time.Duration
	Truncated bool
}

// Span describes the currently buffered window.
type Span struct {
	From   time.Duration
	To     time.Duration
	Frames int
	Bytes  int
}

// Options bounds the ring buffer.
type Options struct {
	MaxBytes    int
	MaxDuration time.Duration
	MaxGOPs     int
}

// Errors reported by the ring buffer.
var (
	ErrBadFrame    = errors.New("frame is not acceptable")
	ErrNoKeyframe  = errors.New("buffer must start with a keyframe")
	ErrTooLarge    = errors.New("frame does not fit the buffer budget")
	ErrBadRange    = errors.New("export range is invalid")
	ErrEmptyBuffer = errors.New("buffer is empty")
	ErrOverwritten = errors.New("a needed group was overwritten during export")
	ErrBadSnapshot = errors.New("snapshot payload is invalid")
)

// ReplayRingBuffer stores recent frames grouped by image group.
type ReplayRingBuffer struct {
	mu sync.Mutex

	opts Options

	gops    []*frameGroup
	frames  int
	bytes   int
	paused  bool
	dropped int
	lastPTS time.Duration
	hasLast bool
}

type frameGroup struct {
	frames []EncodedFrame
	bytes  int
}

// NewReplayRingBuffer builds a buffer with the given budgets.
func NewReplayRingBuffer(opts Options) *ReplayRingBuffer {
	opts = normalizeOptions(opts)
	return &ReplayRingBuffer{opts: opts}
}

func normalizeOptions(opts Options) Options {
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = DefaultMaxBytes
	}
	if opts.MaxDuration <= 0 {
		opts.MaxDuration = DefaultMaxDuration
	}
	if opts.MaxGOPs <= 0 {
		opts.MaxGOPs = DefaultMaxGOPs
	}
	return opts
}

// Push appends one encoded frame and drops whole image groups when needed.
func (r *ReplayRingBuffer) Push(f EncodedFrame) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if f.Bytes <= 0 || f.PTS < 0 {
		return ErrBadFrame
	}
	if r.hasLast && f.PTS <= r.lastPTS {
		return ErrBadFrame
	}
	if f.Bytes > r.opts.MaxBytes {
		return ErrTooLarge
	}
	if r.frames == 0 && !f.Keyframe {
		return ErrNoKeyframe
	}

	if f.Keyframe || len(r.gops) == 0 {
		r.gops = append(r.gops, &frameGroup{})
	}
	g := r.gops[len(r.gops)-1]
	g.frames = append(g.frames, f)
	g.bytes += f.Bytes
	r.frames++
	r.bytes += f.Bytes
	r.lastPTS = f.PTS
	r.hasLast = true

	r.evictLocked()
	return nil
}

// Export returns the playable clip that covers the requested range.
func (r *ReplayRingBuffer) Export(from, to time.Duration) (Clip, error) {
	if from < 0 || from >= to {
		return Clip{}, ErrBadRange
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.frames == 0 {
		return Clip{}, ErrEmptyBuffer
	}

	spanFrom := r.gops[0].frames[0].PTS
	spanTo := r.gops[len(r.gops)-1].frames[len(r.gops[len(r.gops)-1].frames)-1].PTS
	if to < spanFrom {
		return Clip{}, ErrEmptyBuffer
	}

	startGroup := 0
	if from >= spanFrom {
		for i, g := range r.gops {
			if g.frames[0].PTS <= from {
				startGroup = i
			} else {
				break
			}
		}
	}

	endGroup, endFrame := -1, -1
	foundEnd := false
	for gi := startGroup; gi < len(r.gops) && !foundEnd; gi++ {
		for fi, f := range r.gops[gi].frames {
			if f.PTS > to {
				foundEnd = true
				break
			}
			endGroup, endFrame = gi, fi
		}
	}
	if endGroup < 0 {
		return Clip{}, ErrEmptyBuffer
	}

	clip := Clip{
		Truncated: from < spanFrom || to > spanTo || from > spanTo,
	}
	for gi := startGroup; gi <= endGroup; gi++ {
		g := r.gops[gi]
		last := len(g.frames) - 1
		if gi == endGroup {
			last = endFrame
		}
		clip.Frames = append(clip.Frames, g.frames[:last+1]...)
	}
	if len(clip.Frames) == 0 {
		return Clip{}, ErrEmptyBuffer
	}
	clip.Start = clip.Frames[0].PTS
	clip.End = clip.Frames[len(clip.Frames)-1].PTS
	return clip, nil
}

// Pause freezes overwriting while capture keeps running.
func (r *ReplayRingBuffer) Pause() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.paused = true
	r.evictLocked()
	return nil
}

// Resume lets overwriting run again.
func (r *ReplayRingBuffer) Resume() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.paused = false
	r.evictLocked()
	return nil
}

// Paused reports whether overwriting is frozen.
func (r *ReplayRingBuffer) Paused() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.paused
}

// Available returns the buffered window.
func (r *ReplayRingBuffer) Available() Span {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.frames == 0 {
		return Span{}
	}
	first := r.gops[0].frames[0]
	lastGroup := r.gops[len(r.gops)-1]
	last := lastGroup.frames[len(lastGroup.frames)-1]
	return Span{
		From:   first.PTS,
		To:     last.PTS,
		Frames: r.frames,
		Bytes:  r.bytes,
	}
}

// FrameCount returns the number of buffered frames.
func (r *ReplayRingBuffer) FrameCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.frames
}

// ByteCount returns the number of buffered payload bytes.
func (r *ReplayRingBuffer) ByteCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bytes
}

// DroppedGOPs returns how many image groups were overwritten so far.
func (r *ReplayRingBuffer) DroppedGOPs() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dropped
}

// KeyframeAtOrBefore returns the newest keyframe timestamp at or before d.
func (r *ReplayRingBuffer) KeyframeAtOrBefore(d time.Duration) (time.Duration, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.frames == 0 || d < r.gops[0].frames[0].PTS {
		return 0, false
	}
	var found time.Duration
	ok := false
	for _, g := range r.gops {
		keyPTS := g.frames[0].PTS
		if keyPTS > d {
			break
		}
		found = keyPTS
		ok = true
	}
	return found, ok
}

// Snapshot serialises the buffered frames and the pause flag.
func (r *ReplayRingBuffer) Snapshot() ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	size := snapshotHeaderSize + r.frames*snapshotFrameSize + snapshotCRCSize
	data := make([]byte, size)
	copy(data[:8], snapshotMagic)
	binary.LittleEndian.PutUint32(data[8:12], snapshotVersion)
	if r.paused {
		data[12] = 1
	}
	binary.LittleEndian.PutUint64(data[16:24], uint64(r.opts.MaxBytes))
	binary.LittleEndian.PutUint64(data[24:32], uint64(r.opts.MaxDuration))
	binary.LittleEndian.PutUint64(data[32:40], uint64(r.opts.MaxGOPs))
	binary.LittleEndian.PutUint64(data[40:48], uint64(r.dropped))
	binary.LittleEndian.PutUint64(data[48:56], uint64(r.frames))

	off := snapshotHeaderSize
	for _, g := range r.gops {
		for _, f := range g.frames {
			binary.LittleEndian.PutUint64(data[off:off+8], uint64(int64(f.ID)))
			binary.LittleEndian.PutUint64(data[off+8:off+16], uint64(f.PTS))
			binary.LittleEndian.PutUint64(data[off+16:off+24], uint64(f.Dur))
			binary.LittleEndian.PutUint64(data[off+24:off+32], uint64(int64(f.Bytes)))
			if f.Keyframe {
				data[off+32] = 1
			}
			off += snapshotFrameSize
		}
	}
	binary.LittleEndian.PutUint32(data[off:off+4], crc32.ChecksumIEEE(data[:off]))
	return data, nil
}

// Restore replaces the whole buffer with a snapshot, all or nothing.
func (r *ReplayRingBuffer) Restore(data []byte) error {
	restored, err := decodeSnapshot(data)
	if err != nil {
		return ErrBadSnapshot
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.opts = restored.opts
	r.gops = restored.gops
	r.frames = restored.frames
	r.bytes = restored.bytes
	r.paused = restored.paused
	r.dropped = restored.dropped
	r.lastPTS = restored.lastPTS
	r.hasLast = restored.hasLast
	r.evictLocked()
	return nil
}

func (r *ReplayRingBuffer) evictLocked() {
	for len(r.gops) > 0 {
		overBudget := r.bytes > r.opts.MaxBytes
		if !r.paused {
			overBudget = overBudget ||
				len(r.gops) > r.opts.MaxGOPs ||
				r.windowDurationLocked() > r.opts.MaxDuration
		}
		if !overBudget {
			return
		}

		dropped := r.gops[0]
		copy(r.gops, r.gops[1:])
		r.gops[len(r.gops)-1] = nil
		r.gops = r.gops[:len(r.gops)-1]
		r.frames -= len(dropped.frames)
		r.bytes -= dropped.bytes
		r.dropped++
	}
}

func (r *ReplayRingBuffer) windowDurationLocked() time.Duration {
	if len(r.gops) == 0 {
		return 0
	}
	first := r.gops[0].frames[0].PTS
	lastGroup := r.gops[len(r.gops)-1]
	last := lastGroup.frames[len(lastGroup.frames)-1].PTS
	return last - first
}

const (
	snapshotHeaderSize = 56
	snapshotFrameSize  = 33
	snapshotCRCSize    = 4
	snapshotVersion    = 1
)

const snapshotMagic = "RRBF0001"

func decodeSnapshot(data []byte) (*ReplayRingBuffer, error) {
	if len(data) < snapshotHeaderSize+snapshotCRCSize {
		return nil, ErrBadSnapshot
	}
	if string(data[:8]) != snapshotMagic {
		return nil, ErrBadSnapshot
	}
	if binary.LittleEndian.Uint32(data[8:12]) != snapshotVersion {
		return nil, ErrBadSnapshot
	}
	if data[12]&^byte(1) != 0 || data[13] != 0 || data[14] != 0 || data[15] != 0 {
		return nil, ErrBadSnapshot
	}

	payloadEnd := len(data) - snapshotCRCSize
	wantCRC := binary.LittleEndian.Uint32(data[payloadEnd:])
	if crc32.ChecksumIEEE(data[:payloadEnd]) != wantCRC {
		return nil, ErrBadSnapshot
	}

	maxInt := int64(^uint(0) >> 1)
	maxBytes64 := int64(binary.LittleEndian.Uint64(data[16:24]))
	maxDuration64 := int64(binary.LittleEndian.Uint64(data[24:32]))
	maxGOPs64 := int64(binary.LittleEndian.Uint64(data[32:40]))
	dropped64 := int64(binary.LittleEndian.Uint64(data[40:48]))
	frameCount64 := uint64(binary.LittleEndian.Uint64(data[48:56]))
	if maxBytes64 <= 0 || maxBytes64 > maxInt ||
		maxDuration64 <= 0 ||
		maxGOPs64 <= 0 || maxGOPs64 > maxInt ||
		dropped64 < 0 || dropped64 > maxInt ||
		frameCount64 > uint64((payloadEnd-snapshotHeaderSize)/snapshotFrameSize) {
		return nil, ErrBadSnapshot
	}
	if int(frameCount64)*snapshotFrameSize != payloadEnd-snapshotHeaderSize {
		return nil, ErrBadSnapshot
	}

	out := &ReplayRingBuffer{
		opts: Options{
			MaxBytes:    int(maxBytes64),
			MaxDuration: time.Duration(maxDuration64),
			MaxGOPs:     int(maxGOPs64),
		},
		paused:  data[12] == 1,
		dropped: int(dropped64),
	}

	off := snapshotHeaderSize
	var totalBytes int64
	for i := uint64(0); i < frameCount64; i++ {
		id := int64(binary.LittleEndian.Uint64(data[off : off+8]))
		pts := int64(binary.LittleEndian.Uint64(data[off+8 : off+16]))
		dur := int64(binary.LittleEndian.Uint64(data[off+16 : off+24]))
		size := int64(binary.LittleEndian.Uint64(data[off+24 : off+32]))
		keyframe := data[off+32]
		if id < -maxInt-1 || id > maxInt ||
			pts < 0 ||
			size <= 0 || size > maxBytes64 || size > maxInt ||
			keyframe > 1 {
			return nil, ErrBadSnapshot
		}
		if i == 0 && keyframe != 1 {
			return nil, ErrBadSnapshot
		}
		if out.hasLast && time.Duration(pts) <= out.lastPTS {
			return nil, ErrBadSnapshot
		}
		totalBytes += size
		if totalBytes > maxInt {
			return nil, ErrBadSnapshot
		}

		f := EncodedFrame{
			ID:       int(id),
			PTS:      time.Duration(pts),
			Dur:      time.Duration(dur),
			Keyframe: keyframe == 1,
			Bytes:    int(size),
		}
		if f.Keyframe || len(out.gops) == 0 {
			out.gops = append(out.gops, &frameGroup{})
		}
		g := out.gops[len(out.gops)-1]
		g.frames = append(g.frames, f)
		g.bytes += f.Bytes
		out.frames++
		out.bytes += f.Bytes
		out.lastPTS = f.PTS
		out.hasLast = true
		off += snapshotFrameSize
	}
	return out, nil
}
