// Package task plans low latency HLS partial segments and their playlist.
package task

import (
	"errors"
	"sync"
	"time"
)

var errNotImplemented = errors.New("not implemented")

// Errors returned by the planner.
var (
	ErrPartDuration      = errors.New("part duration is out of range")
	ErrPartOrder         = errors.New("part index is not the next one")
	ErrDuplicatePart     = errors.New("part index already published")
	ErrTooManyParts      = errors.New("part count exceeds the per segment cap")
	ErrSegmentClosed     = errors.New("segment is already closed")
	ErrEmptySegment      = errors.New("segment has no published part")
	ErrUnknownSegment    = errors.New("segment is unknown")
	ErrNotIndependent    = errors.New("first part must be marked independent")
	ErrBadIndependent    = errors.New("only the first part may be independent")
	ErrInvalidWindow     = errors.New("window size is out of range")
	ErrInvalidPartTarget = errors.New("part target is invalid")
	ErrOutOfOrder        = errors.New("segment sequence must move forward")
	ErrBadSnapshot       = errors.New("snapshot payload is invalid")
)

// ManifestVersion is the playlist protocol version.
const ManifestVersion = 6

// PreloadTypePart marks a part level preload hint.
const PreloadTypePart = "PART"

// Planner limits.
const (
	DefaultPartTarget     = 200 * time.Millisecond
	DefaultWindowSegments = 6
	DefaultMaxPartsPerSeg = 16
	MaxWindowSegments     = 16
)

// PartInfo is one partial segment produced by the packager.
type PartInfo struct {
	Index       int
	Duration    time.Duration
	URI         string
	Independent bool
}

// PartEntry is one playlist row.
type PartEntry struct {
	Segment     uint64
	Index       int
	Duration    time.Duration
	URI         string
	Independent bool
}

// PreloadHint tells the client which part to fetch next.
type PreloadHint struct {
	URI       string
	PartIndex int
	Type      string
}

// BlockingReload carries the server controlled reload parameters.
type BlockingReload struct {
	PartTarget time.Duration
	HoldBack   time.Duration
}

// Manifest is the playlist snapshot served to clients.
type Manifest struct {
	Version        int
	TargetDuration time.Duration
	StartNumber    uint64
	Parts          []PartEntry
	Preload        []PreloadHint
	Blocking       *BlockingReload
}

// PlannerConfig bounds the window and the part shape.
type PlannerConfig struct {
	PartTarget     time.Duration
	WindowSegments int
	MaxPartsPerSeg int
	HoldBack       time.Duration
}

// PlannerStats reports the live counters.
type PlannerStats struct {
	Segments int
	Parts    int
	Evicted  int
}

// LlhlsPartPlanner owns the sliding playlist window.
type LlhlsPartPlanner struct {
	mu        sync.Mutex
	cfg       PlannerConfig
	segments  map[uint64][]PartEntry
	closed    map[uint64]bool
	newest    uint64
	published bool
	evicted   int
}

// NewLlhlsPartPlanner builds a planner with the given limits.
func NewLlhlsPartPlanner(cfg PlannerConfig) *LlhlsPartPlanner {
	return &LlhlsPartPlanner{cfg: cfg, segments: map[uint64][]PartEntry{}, closed: map[uint64]bool{}}
}

// PublishPart appends one part to a segment and freezes its shape.
func (p *LlhlsPartPlanner) PublishPart(seg uint64, part PartInfo) error {
	return errNotImplemented
}

// CompleteSegment closes a segment so no further part is accepted.
func (p *LlhlsPartPlanner) CompleteSegment(seg uint64) error {
	return errNotImplemented
}

// Playlist renders the sliding window as a consistent manifest.
func (p *LlhlsPartPlanner) Playlist() (Manifest, error) {
	return Manifest{}, errNotImplemented
}

// SetWindow changes how many segments stay in the playlist.
func (p *LlhlsPartPlanner) SetWindow(n int) error {
	return errNotImplemented
}

// SetPartTarget changes the hard cap of one part duration.
func (p *LlhlsPartPlanner) SetPartTarget(d time.Duration) error {
	return errNotImplemented
}

// Preload returns the preload hint of the live edge.
func (p *LlhlsPartPlanner) Preload() []PreloadHint {
	return nil
}

// RemoveBefore drops every segment with a sequence number below seg.
func (p *LlhlsPartPlanner) RemoveBefore(seg uint64) error {
	return errNotImplemented
}

// Stats returns a copy of the counters.
func (p *LlhlsPartPlanner) Stats() PlannerStats {
	return PlannerStats{}
}

// Parts returns the published parts of one segment, oldest first.
func (p *LlhlsPartPlanner) Parts(seg uint64) []PartEntry {
	return nil
}

// Snapshot serialises the window, the closed set and the counters.
func (p *LlhlsPartPlanner) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the state with a snapshot, all or nothing.
func (p *LlhlsPartPlanner) Restore(data []byte) error {
	return errNotImplemented
}
