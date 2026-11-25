// Package task indexes byte ranges of media segments for seeking by time.
package task

import (
	"errors"
	"sync"
	"time"
)

var errNotImplemented = errors.New("not implemented")

// Errors returned by the indexer.
var (
	ErrMalformedSegment = errors.New("segment payload is malformed")
	ErrDuplicateSegment = errors.New("segment is already indexed")
	ErrUnknownSegment   = errors.New("segment is unknown")
	ErrSegmentInvalid   = errors.New("segment is invalidated")
	ErrOutOfRange       = errors.New("time is outside the indexed range")
	ErrIndexFull        = errors.New("index entry limit reached")
	ErrInvalidConfig    = errors.New("index configuration is invalid")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

// Indexer defaults.
const (
	DefaultTimescale  = 90000
	DefaultMaxEntries = 4096
)

// IndexConfig tunes the tick rate and the entry cap.
type IndexConfig struct {
	Timescale  uint32
	MaxEntries int
}

// ByteRange is one indexed subsegment.
type ByteRange struct {
	Segment string
	Index   int
	Offset  int64
	Size    int64
	Start   time.Duration
	End     time.Duration
	SAP     bool
}

// IndexStats reports the live counters.
type IndexStats struct {
	Segments    int
	Ranges      int
	Bytes       int64
	Invalidated int
}

// ByteRangeIndexer owns the accumulated byte range index.
type ByteRangeIndexer struct {
	mu      sync.Mutex
	cfg     IndexConfig
	order   []string
	bySeg   map[string][]ByteRange
	dead    map[string]bool
	ranges  int
	bytes   int64
	invalid int
	base    time.Duration
}

// NewByteRangeIndexer builds an indexer with the given limits.
func NewByteRangeIndexer(cfg IndexConfig) *ByteRangeIndexer {
	return &ByteRangeIndexer{cfg: cfg, bySeg: map[string][]ByteRange{}, dead: map[string]bool{}}
}

// IndexSegment parses one segment payload and appends its ranges.
func (b *ByteRangeIndexer) IndexSegment(segID string, data []byte) error {
	return errNotImplemented
}

// Locate maps a global playback time to a byte range.
func (b *ByteRangeIndexer) Locate(t time.Duration, requireSAP bool) (ByteRange, error) {
	return ByteRange{}, errNotImplemented
}

// Invalidate drops one segment from future lookups.
func (b *ByteRangeIndexer) Invalidate(segID string) error {
	return errNotImplemented
}

// Entries returns how many byte ranges are indexed.
func (b *ByteRangeIndexer) Entries() int {
	return 0
}

// RangeAt reads one range by segment and local index.
func (b *ByteRangeIndexer) RangeAt(segID string, index int) (ByteRange, error) {
	return ByteRange{}, errNotImplemented
}

// Segments lists the live segment ids in indexing order.
func (b *ByteRangeIndexer) Segments() []string {
	return nil
}

// TotalBytes returns the sum of all indexed range sizes.
func (b *ByteRangeIndexer) TotalBytes() int64 {
	return 0
}

// Stats returns a copy of the counters.
func (b *ByteRangeIndexer) Stats() IndexStats {
	return IndexStats{}
}

// Reset clears the whole index.
func (b *ByteRangeIndexer) Reset() error {
	return errNotImplemented
}

// Snapshot serialises the index and the counters.
func (b *ByteRangeIndexer) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the state with a snapshot, all or nothing.
func (b *ByteRangeIndexer) Restore(data []byte) error {
	return errNotImplemented
}
