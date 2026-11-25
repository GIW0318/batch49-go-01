// Package task stabilises live caption partial results into rolling lines.
package task

import (
	"errors"
	"sync"
	"time"
)

var errNotImplemented = errors.New("not implemented")

// Errors returned by the stabiliser.
var (
	ErrBadPartial     = errors.New("partial result is invalid")
	ErrUnknownSegment = errors.New("caption segment is unknown")
	ErrInvalidWindow  = errors.New("correction window is invalid")
	ErrSegmentLimit   = errors.New("caption segment limit reached")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// Defaults used when the configuration leaves a field at zero.
const (
	DefaultCorrectionWindow = 400 * time.Millisecond
	DefaultMaxLines         = 3
	DefaultMaxSegments      = 64
)

// Partial is one partial recognition result pushed by the engine.
type Partial struct {
	SegID string
	Seq   int
	Text  string
	Start time.Duration
	Now   time.Duration
}

// Line is one on-screen caption line.
type Line struct {
	SegID  string
	Seq    int
	Text   string
	Start  time.Duration
	Shown  time.Duration
	Frozen bool
}

// CaptionConfig bounds the rolling window and the correction window.
type CaptionConfig struct {
	CorrectionWindow time.Duration
	MaxLines         int
	MaxSegments      int
}

// CaptionStats reports counters of the stabiliser.
type CaptionStats struct {
	Received     int
	Applied      int
	DroppedStale int
	DroppedFinal int
	LateEdits    int
	Segments     int
	Frozen       int
}

// LiveCaptionStabilizer keeps partial results stable on screen.
type LiveCaptionStabilizer struct {
	mu    sync.Mutex
	cfg   CaptionConfig
	lines []Line
	index map[string]int
	stats CaptionStats
}

// NewLiveCaptionStabilizer builds a stabiliser with the given limits.
func NewLiveCaptionStabilizer(cfg CaptionConfig) *LiveCaptionStabilizer {
	return &LiveCaptionStabilizer{cfg: cfg, index: map[string]int{}}
}

// Update applies one partial result to its segment.
func (s *LiveCaptionStabilizer) Update(p Partial) error {
	return errNotImplemented
}

// Finalize freezes one segment so later partial results cannot touch it.
func (s *LiveCaptionStabilizer) Finalize(segID string) error {
	return errNotImplemented
}

// ForceFinalize freezes every unfrozen segment first shown at or before cutoff.
func (s *LiveCaptionStabilizer) ForceFinalize(cutoff time.Duration) error {
	return errNotImplemented
}

// Rolling returns the live lines in display order.
func (s *LiveCaptionStabilizer) Rolling() []Line {
	return nil
}

// SetCorrectionWindow changes how long a revealed line may still be edited.
func (s *LiveCaptionStabilizer) SetCorrectionWindow(d time.Duration) error {
	return errNotImplemented
}

// Pending returns the lines that are not frozen yet.
func (s *LiveCaptionStabilizer) Pending() []Line {
	return nil
}

// Stats returns a copy of the counters.
func (s *LiveCaptionStabilizer) Stats() CaptionStats {
	return CaptionStats{}
}

// Reset drops every line and counter.
func (s *LiveCaptionStabilizer) Reset() error {
	return errNotImplemented
}

// Snapshot serialises the rolling window and counters.
func (s *LiveCaptionStabilizer) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the state with a snapshot, all or nothing.
func (s *LiveCaptionStabilizer) Restore(data []byte) error {
	return errNotImplemented
}
