// Package task plans low latency HLS partial segments and their playlist.
package task

import (
	"encoding/json"
	"errors"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

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

const (
	snapshotMagic  = "gsb-batch49-go-game-c-07-llhls-part-planner"
	snapshotFormat = 1
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
	startSeq  uint64
	startSet  bool
	evicted   int
}

type snapshotFile struct {
	Magic     string            `json:"magic"`
	Format    int               `json:"format"`
	Config    PlannerConfig     `json:"config"`
	StartSeq  uint64            `json:"start_seq"`
	HasStart  bool              `json:"has_start"`
	Newest    uint64            `json:"newest"`
	Published bool              `json:"published"`
	Evicted   int               `json:"evicted"`
	Segments  []snapshotSegment `json:"segments"`
}

type snapshotSegment struct {
	Seq    uint64      `json:"seq"`
	Closed bool        `json:"closed"`
	Parts  []PartEntry `json:"parts"`
}

// NewLlhlsPartPlanner builds a planner with the given limits.
func NewLlhlsPartPlanner(cfg PlannerConfig) *LlhlsPartPlanner {
	if cfg.PartTarget <= 0 {
		cfg.PartTarget = DefaultPartTarget
	}
	if cfg.WindowSegments <= 0 {
		cfg.WindowSegments = DefaultWindowSegments
	}
	if cfg.WindowSegments > MaxWindowSegments {
		cfg.WindowSegments = MaxWindowSegments
	}
	if cfg.MaxPartsPerSeg <= 0 {
		cfg.MaxPartsPerSeg = DefaultMaxPartsPerSeg
	}

	return &LlhlsPartPlanner{
		cfg:      cfg,
		segments: make(map[uint64][]PartEntry),
		closed:   make(map[uint64]bool),
	}
}

// PublishPart appends one part to a segment and freezes its shape.
func (p *LlhlsPartPlanner) PublishPart(seg uint64, part PartInfo) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if part.Duration <= 0 || part.Duration > p.cfg.PartTarget {
		return ErrPartDuration
	}
	if p.startSet && seg < p.startSeq {
		return ErrOutOfOrder
	}
	if p.closed[seg] {
		return ErrSegmentClosed
	}

	parts := p.segments[seg]
	nextIndex := len(parts)
	switch {
	case part.Index < 0:
		return ErrPartOrder
	case part.Index < nextIndex:
		return ErrDuplicatePart
	case part.Index > nextIndex:
		return ErrPartOrder
	}

	if part.Index == 0 && !part.Independent {
		return ErrNotIndependent
	}
	if part.Index > 0 && part.Independent {
		return ErrBadIndependent
	}
	if nextIndex >= p.cfg.MaxPartsPerSeg {
		return ErrTooManyParts
	}

	if !p.startSet {
		p.startSet = true
		p.startSeq = seg
	}

	entry := PartEntry{
		Segment:     seg,
		Index:       part.Index,
		Duration:    part.Duration,
		URI:         part.URI,
		Independent: part.Independent,
	}
	p.segments[seg] = append(parts, entry)

	if !p.published || seg > p.newest {
		p.newest = seg
		p.published = true
	}

	p.enforceWindowLocked()
	return nil
}

// CompleteSegment closes a segment so no further part is accepted.
func (p *LlhlsPartPlanner) CompleteSegment(seg uint64) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.startSet && seg < p.startSeq {
		return ErrOutOfOrder
	}
	if p.closed[seg] {
		return ErrSegmentClosed
	}

	parts, ok := p.segments[seg]
	if !ok || len(parts) == 0 {
		return ErrEmptySegment
	}

	p.closed[seg] = true
	return nil
}

// Playlist renders the sliding window as a consistent manifest.
func (p *LlhlsPartPlanner) Playlist() (Manifest, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	segments := p.sortedSegmentsLocked()
	totalParts := 0
	for _, seg := range segments {
		totalParts += len(p.segments[seg])
	}

	rows := make([]PartEntry, 0, totalParts)
	for _, seg := range segments {
		rows = append(rows, p.segments[seg]...)
	}

	startNumber := p.startSeq
	if len(segments) > 0 {
		startNumber = segments[0]
	}

	manifest := Manifest{
		Version:        ManifestVersion,
		TargetDuration: p.cfg.PartTarget,
		StartNumber:    startNumber,
		Parts:          rows,
		Preload:        p.preloadLocked(segments),
	}
	if p.cfg.HoldBack > 0 {
		manifest.Blocking = &BlockingReload{
			PartTarget: p.cfg.PartTarget,
			HoldBack:   p.cfg.HoldBack,
		}
	}

	return manifest, nil
}

// SetWindow changes how many segments stay in the playlist.
func (p *LlhlsPartPlanner) SetWindow(n int) error {
	if n < 1 || n > MaxWindowSegments {
		return ErrInvalidWindow
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	p.cfg.WindowSegments = n
	p.enforceWindowLocked()
	return nil
}

// SetPartTarget changes the hard cap of one part duration.
func (p *LlhlsPartPlanner) SetPartTarget(d time.Duration) error {
	if d <= 0 {
		return ErrInvalidPartTarget
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	p.cfg.PartTarget = d
	return nil
}

// Preload returns the preload hint of the live edge.
func (p *LlhlsPartPlanner) Preload() []PreloadHint {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.preloadLocked(p.sortedSegmentsLocked())
}

// RemoveBefore drops every segment with a sequence number below seg.
func (p *LlhlsPartPlanner) RemoveBefore(seg uint64) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	for current := range p.segments {
		if current < seg {
			delete(p.segments, current)
			delete(p.closed, current)
			p.evicted++
		}
	}
	for current := range p.closed {
		if current < seg {
			delete(p.closed, current)
		}
	}

	if len(p.segments) == 0 {
		p.published = false
		p.newest = 0
		if !p.startSet || seg > p.startSeq {
			p.startSeq = seg
		}
		p.startSet = true
		return nil
	}

	oldest, newest := p.minMaxSegmentsLocked()
	p.startSeq = oldest
	p.startSet = true
	p.newest = newest
	p.published = true
	return nil
}

// Stats returns a copy of the counters.
func (p *LlhlsPartPlanner) Stats() PlannerStats {
	p.mu.Lock()
	defer p.mu.Unlock()

	stats := PlannerStats{
		Segments: len(p.segments),
		Evicted:  p.evicted,
	}
	for _, parts := range p.segments {
		stats.Parts += len(parts)
	}
	return stats
}

// Parts returns the published parts of one segment, oldest first.
func (p *LlhlsPartPlanner) Parts(seg uint64) []PartEntry {
	p.mu.Lock()
	defer p.mu.Unlock()

	source := p.segments[seg]
	if len(source) == 0 {
		return nil
	}

	parts := make([]PartEntry, len(source))
	copy(parts, source)
	return parts
}

// Snapshot serialises the window, the closed set and the counters.
func (p *LlhlsPartPlanner) Snapshot() ([]byte, error) {
	p.mu.Lock()
	payload := snapshotFile{
		Magic:     snapshotMagic,
		Format:    snapshotFormat,
		Config:    p.cfg,
		StartSeq:  p.startSeq,
		HasStart:  p.startSet,
		Newest:    p.newest,
		Published: p.published,
		Evicted:   p.evicted,
		Segments:  make([]snapshotSegment, 0, len(p.segments)),
	}
	for _, seg := range p.sortedSegmentsLocked() {
		source := p.segments[seg]
		parts := make([]PartEntry, len(source))
		copy(parts, source)
		payload.Segments = append(payload.Segments, snapshotSegment{
			Seq:    seg,
			Closed: p.closed[seg],
			Parts:  parts,
		})
	}
	p.mu.Unlock()

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Restore replaces the state with a snapshot, all or nothing.
func (p *LlhlsPartPlanner) Restore(data []byte) error {
	var payload snapshotFile
	if err := json.Unmarshal(data, &payload); err != nil {
		return ErrBadSnapshot
	}
	if err := payload.validate(); err != nil {
		return ErrBadSnapshot
	}

	segments := make(map[uint64][]PartEntry, len(payload.Segments))
	closed := make(map[uint64]bool, len(payload.Segments))
	var oldest, newest uint64
	haveSegment := false

	for _, snapshotSegment := range payload.Segments {
		parts := make([]PartEntry, len(snapshotSegment.Parts))
		copy(parts, snapshotSegment.Parts)
		for index := range parts {
			parts[index].Segment = snapshotSegment.Seq
			parts[index].Index = index
			parts[index].Independent = index == 0
		}

		segments[snapshotSegment.Seq] = parts
		if snapshotSegment.Closed {
			closed[snapshotSegment.Seq] = true
		}

		if !haveSegment || snapshotSegment.Seq < oldest {
			oldest = snapshotSegment.Seq
		}
		if !haveSegment || snapshotSegment.Seq > newest {
			newest = snapshotSegment.Seq
		}
		haveSegment = true
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	p.cfg = payload.Config
	p.segments = segments
	p.closed = closed
	p.evicted = payload.Evicted

	if haveSegment {
		p.startSeq = oldest
		p.startSet = true
		p.newest = newest
		p.published = true
	} else {
		p.startSeq = payload.StartSeq
		p.startSet = payload.HasStart
		p.newest = 0
		p.published = false
	}

	return nil
}

func (s snapshotFile) validate() error {
	if s.Magic != snapshotMagic || s.Format != snapshotFormat {
		return ErrBadSnapshot
	}
	if s.Config.PartTarget <= 0 {
		return ErrBadSnapshot
	}
	if s.Config.WindowSegments < 1 || s.Config.WindowSegments > MaxWindowSegments {
		return ErrBadSnapshot
	}
	if s.Config.MaxPartsPerSeg <= 0 {
		return ErrBadSnapshot
	}
	if s.Evicted < 0 || len(s.Segments) > s.Config.WindowSegments {
		return ErrBadSnapshot
	}

	seen := make(map[uint64]struct{}, len(s.Segments))
	var oldest, newest uint64
	haveSegment := false

	for _, segment := range s.Segments {
		if _, duplicated := seen[segment.Seq]; duplicated {
			return ErrBadSnapshot
		}
		seen[segment.Seq] = struct{}{}

		partCount := len(segment.Parts)
		if partCount == 0 || partCount > s.Config.MaxPartsPerSeg {
			return ErrBadSnapshot
		}

		for index, part := range segment.Parts {
			if part.Index != index || part.Segment != segment.Seq || part.Duration <= 0 {
				return ErrBadSnapshot
			}
			if index == 0 {
				if !part.Independent {
					return ErrBadSnapshot
				}
			} else if part.Independent {
				return ErrBadSnapshot
			}
		}

		if !haveSegment || segment.Seq < oldest {
			oldest = segment.Seq
		}
		if !haveSegment || segment.Seq > newest {
			newest = segment.Seq
		}
		haveSegment = true
	}

	if haveSegment {
		if !s.HasStart || s.StartSeq != oldest || !s.Published || s.Newest != newest {
			return ErrBadSnapshot
		}
	} else if s.Published {
		return ErrBadSnapshot
	}

	return nil
}

func (p *LlhlsPartPlanner) sortedSegmentsLocked() []uint64 {
	segments := make([]uint64, 0, len(p.segments))
	for seg := range p.segments {
		segments = append(segments, seg)
	}
	sort.Slice(segments, func(i, j int) bool {
		return segments[i] < segments[j]
	})
	return segments
}

func (p *LlhlsPartPlanner) minMaxSegmentsLocked() (uint64, uint64) {
	first := true
	var oldest, newest uint64
	for seg := range p.segments {
		if first || seg < oldest {
			oldest = seg
		}
		if first || seg > newest {
			newest = seg
		}
		first = false
	}
	return oldest, newest
}

func (p *LlhlsPartPlanner) enforceWindowLocked() {
	excess := len(p.segments) - p.cfg.WindowSegments
	if excess <= 0 {
		return
	}

	segments := p.sortedSegmentsLocked()
	for i := 0; i < excess; i++ {
		seg := segments[i]
		delete(p.segments, seg)
		delete(p.closed, seg)
		p.evicted++
	}

	p.startSeq = segments[excess]
	p.startSet = true
	p.newest = segments[len(segments)-1]
	p.published = true
}

func (p *LlhlsPartPlanner) preloadLocked(segments []uint64) []PreloadHint {
	if len(segments) == 0 {
		return nil
	}

	newestSegment := segments[len(segments)-1]
	parts := p.segments[newestSegment]
	if len(parts) == 0 {
		return nil
	}

	last := parts[len(parts)-1]
	nextIndex := last.Index + 1
	return []PreloadHint{{
		URI:       nextPartURI(last.URI, nextIndex),
		PartIndex: nextIndex,
		Type:      PreloadTypePart,
	}}
}

// newestSequenceLocked exposes the live edge to package-local verification.
func (p *LlhlsPartPlanner) newestSequenceLocked() (uint64, bool) {
	if len(p.segments) == 0 {
		return 0, false
	}
	_, newest := p.minMaxSegmentsLocked()
	return newest, true
}

func nextPartURI(uri string, nextIndex int) string {
	uriPath := uri
	suffix := ""
	if cut := strings.IndexAny(uri, "?#"); cut >= 0 {
		uriPath = uri[:cut]
		suffix = uri[cut:]
	}

	ext := path.Ext(uriPath)
	if len(ext) > 1 {
		numericExtension := true
		for _, char := range ext[1:] {
			if char < '0' || char > '9' {
				numericExtension = false
				break
			}
		}
		if numericExtension {
			ext = ""
		}
	}
	stem := strings.TrimSuffix(uriPath, ext)

	digitsStart, digitsEnd := trailingDigits(stem)
	if digitsStart < digitsEnd {
		digits := stem[digitsStart:digitsEnd]
		if value, err := strconv.Atoi(digits); err == nil {
			nextValue := strconv.Itoa(value + 1)
			if len(nextValue) < len(digits) {
				nextValue = strings.Repeat("0", len(digits)-len(nextValue)) + nextValue
			}
			return stem[:digitsStart] + nextValue + stem[digitsEnd:] + ext + suffix
		}
	}

	return uriPath + strconv.Itoa(nextIndex) + suffix
}

func trailingDigits(value string) (int, int) {
	end := len(value)
	start := end
	for start > 0 {
		char := value[start-1]
		if char < '0' || char > '9' {
			break
		}
		start--
	}
	return start, end
}
