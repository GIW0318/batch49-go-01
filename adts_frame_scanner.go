// Package task scans raw ADTS streams, cuts complete audio frames and
// recovers the sync point after damaged data.
package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// ADTS header sizes without and with the optional CRC word.
const (
	ADTSHeaderSize    = 7
	ADTSHeaderCRCSize = 9
	DefaultMaxResync  = 4096
	DefaultMaxFrame   = 8192
	MinMaxFrameBytes  = 64
	MaxMaxFrameBytes  = 65535
)

// Errors returned by the scanner.
var (
	ErrBadMaxResync = errors.New("max resync bytes out of range")
	ErrBadMaxFrame  = errors.New("max frame size out of range")
	ErrBadSnapshot  = errors.New("snapshot payload is invalid")
)

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
	maxResync int
	maxFrame  int
	nextIndex int
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
	return nil, errNotImplemented
}

// Resync throws away the pending buffer and restarts scanning.
func (s *AdtsFrameScanner) Resync() error {
	return errNotImplemented
}

// SetMaxResync bounds how many unusable bytes are tolerated before a resync.
func (s *AdtsFrameScanner) SetMaxResync(n int) error {
	return errNotImplemented
}

// SetMaxFrameBytes bounds the largest frame the scanner will emit.
func (s *AdtsFrameScanner) SetMaxFrameBytes(n int) error {
	return errNotImplemented
}

// Stats returns a copy of the running counters.
func (s *AdtsFrameScanner) Stats() ScanStats {
	return ScanStats{}
}

// Pending reports how many bytes are buffered but not yet consumed.
func (s *AdtsFrameScanner) Pending() int {
	return 0
}

// FrameCount reports how many frames were emitted since the last reset.
func (s *AdtsFrameScanner) FrameCount() int {
	return 0
}

// Reset clears the buffer, the frame counter and the statistics.
func (s *AdtsFrameScanner) Reset() error {
	return errNotImplemented
}

// Snapshot serialises the pending buffer, counters and format state.
func (s *AdtsFrameScanner) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole scanner state from a snapshot, all or nothing.
func (s *AdtsFrameScanner) Restore(data []byte) error {
	return errNotImplemented
}
