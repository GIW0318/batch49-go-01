// Package task keeps the most recent encoded frames of a live stream so that
// highlight clips can be exported at any moment.
package task

import (
	"errors"
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
	opts Options
}

// NewReplayRingBuffer builds a buffer with the given budgets.
func NewReplayRingBuffer(opts Options) *ReplayRingBuffer {
	return &ReplayRingBuffer{opts: opts}
}

// Push appends one encoded frame and drops whole image groups when needed.
func (r *ReplayRingBuffer) Push(f EncodedFrame) error {
	return errNotImplemented
}

// Export returns the playable clip that covers the requested range.
func (r *ReplayRingBuffer) Export(from, to time.Duration) (Clip, error) {
	return Clip{}, errNotImplemented
}

// Pause freezes overwriting while capture keeps running.
func (r *ReplayRingBuffer) Pause() error {
	return errNotImplemented
}

// Resume lets overwriting run again.
func (r *ReplayRingBuffer) Resume() error {
	return errNotImplemented
}

// Paused reports whether overwriting is frozen.
func (r *ReplayRingBuffer) Paused() bool {
	return false
}

// Available returns the buffered window.
func (r *ReplayRingBuffer) Available() Span {
	return Span{}
}

// FrameCount returns the number of buffered frames.
func (r *ReplayRingBuffer) FrameCount() int {
	return 0
}

// ByteCount returns the number of buffered payload bytes.
func (r *ReplayRingBuffer) ByteCount() int {
	return 0
}

// DroppedGOPs returns how many image groups were overwritten so far.
func (r *ReplayRingBuffer) DroppedGOPs() int {
	return 0
}

// KeyframeAtOrBefore returns the newest keyframe timestamp at or before d.
func (r *ReplayRingBuffer) KeyframeAtOrBefore(d time.Duration) (time.Duration, bool) {
	return 0, false
}

// Snapshot serialises the buffered frames and the pause flag.
func (r *ReplayRingBuffer) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole buffer with a snapshot, all or nothing.
func (r *ReplayRingBuffer) Restore(data []byte) error {
	return errNotImplemented
}
