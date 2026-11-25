// Package task indexes byte ranges of media segments for seeking by time.
package task

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

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
	deadRng int
	bytes   int64
	invalid int
	base    time.Duration
	nextOff int64
}

// NewByteRangeIndexer builds an indexer with the given limits.
func NewByteRangeIndexer(cfg IndexConfig) *ByteRangeIndexer {
	if cfg.Timescale == 0 {
		cfg.Timescale = DefaultTimescale
	}
	if cfg.MaxEntries <= 0 {
		cfg.MaxEntries = DefaultMaxEntries
	}
	return &ByteRangeIndexer{cfg: cfg, bySeg: map[string][]ByteRange{}, dead: map[string]bool{}}
}

// durationAccumulator converts ticks to nanoseconds, carrying the
// conversion remainder forward so rounding never accumulates drift.
type durationAccumulator struct {
	ts  uint64
	rem uint64
}

func (d *durationAccumulator) add(ticks uint32) time.Duration {
	total := uint64(ticks)*uint64(time.Second) + d.rem
	dur := total / d.ts
	d.rem = total % d.ts
	if dur == 0 && ticks > 0 {
		dur = 1
		d.rem = 0
	}
	return time.Duration(dur)
}

// parsedRange is one subsegment before global offsets are assigned.
type parsedRange struct {
	offset int64
	size   int64
	dur    time.Duration
	sap    bool
}

// parsedSegment is a fully validated segment ready to be committed.
type parsedSegment struct {
	ranges   []parsedRange
	consumed int64
}

func boxHeader(data []byte, pos int) (length int64, typ string) {
	length = int64(binary.BigEndian.Uint32(data[pos : pos+4]))
	typ = string(data[pos+4 : pos+8])
	return length, typ
}

func parseSegment(timescale uint32, data []byte) (parsedSegment, error) {
	if len(data) == 0 {
		return parsedSegment{}, nil
	}
	if len(data) >= 8 {
		if _, typ := boxHeader(data, 0); typ == "sidx" {
			return parseIndexedSegment(data)
		}
	}
	return parseFragmentedSegment(timescale, data)
}

// parseFragmentedSegment parses a moof/mdat pair stream. Trailing bytes
// that cannot complete a pair are merged into the previous subsegment.
func parseFragmentedSegment(timescale uint32, data []byte) (parsedSegment, error) {
	var out parsedSegment
	acc := durationAccumulator{ts: uint64(timescale)}
	pos := 0
	var localOff int64
	for pos < len(data) {
		rem := len(data) - pos
		if rem < 8 {
			if len(out.ranges) == 0 {
				return parsedSegment{}, ErrMalformedSegment
			}
			out.ranges[len(out.ranges)-1].size += int64(rem)
			pos = len(data)
			break
		}
		moofLen, typ := boxHeader(data, pos)
		if moofLen != 16 || moofLen > int64(rem) || typ != "moof" {
			return parsedSegment{}, ErrMalformedSegment
		}
		payload := data[pos+8 : pos+16]
		ticks := binary.BigEndian.Uint32(payload[0:4])
		sap := payload[4]&1 == 1
		dataPos := pos + 16
		dataRem := len(data) - dataPos
		if dataRem < 8 {
			if len(out.ranges) == 0 {
				return parsedSegment{}, ErrMalformedSegment
			}
			out.ranges[len(out.ranges)-1].size += int64(len(data) - pos)
			pos = len(data)
			break
		}
		mdatLen, mtyp := boxHeader(data, dataPos)
		if mdatLen < 8 || mdatLen > int64(dataRem) || mtyp != "mdat" {
			return parsedSegment{}, ErrMalformedSegment
		}
		size := 16 + mdatLen
		out.ranges = append(out.ranges, parsedRange{
			offset: localOff,
			size:   size,
			dur:    acc.add(ticks),
			sap:    sap,
		})
		localOff += size
		pos = dataPos + int(mdatLen)
	}
	out.consumed = int64(len(data))
	return out, nil
}

// parseIndexedSegment parses a segment led by a sidx index box. Byte
// offsets accumulate from the end of the sidx box.
func parseIndexedSegment(data []byte) (parsedSegment, error) {
	var out parsedSegment
	sidxLen, _ := boxHeader(data, 0)
	if sidxLen < 8 || sidxLen > int64(len(data)) {
		return parsedSegment{}, ErrMalformedSegment
	}
	payload := data[8:sidxLen]
	if len(payload) < 8 {
		return parsedSegment{}, ErrMalformedSegment
	}
	timescale := binary.BigEndian.Uint32(payload[0:4])
	count := binary.BigEndian.Uint32(payload[4:8])
	if timescale == 0 || len(payload) != 8+12*int(count) {
		return parsedSegment{}, ErrMalformedSegment
	}
	acc := durationAccumulator{ts: uint64(timescale)}
	localOff := sidxLen
	for i := 0; i < int(count); i++ {
		entry := payload[8+12*i : 8+12*(i+1)]
		ticks := binary.BigEndian.Uint32(entry[0:4])
		size := int64(binary.BigEndian.Uint32(entry[4:8]))
		flags := binary.BigEndian.Uint32(entry[8:12])
		out.ranges = append(out.ranges, parsedRange{
			offset: localOff,
			size:   size,
			dur:    acc.add(ticks),
			sap:    flags&1 == 1,
		})
		localOff += size
	}
	out.consumed = localOff
	return out, nil
}

// IndexSegment parses one segment payload and appends its ranges.
func (b *ByteRangeIndexer) IndexSegment(segID string, data []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.bySeg[segID]; ok {
		return ErrDuplicateSegment
	}
	parsed, err := parseSegment(b.cfg.Timescale, data)
	if err != nil {
		return err
	}
	if b.ranges+len(parsed.ranges) > b.cfg.MaxEntries {
		return ErrIndexFull
	}
	ranges := make([]ByteRange, len(parsed.ranges))
	start := b.base
	var totalBytes int64
	for i, r := range parsed.ranges {
		ranges[i] = ByteRange{
			Segment: segID,
			Index:   i,
			Offset:  b.nextOff + r.offset,
			Size:    r.size,
			Start:   start,
			End:     start + r.dur,
			SAP:     r.sap,
		}
		start += r.dur
		totalBytes += r.size
	}
	b.bySeg[segID] = ranges
	b.order = append(b.order, segID)
	b.ranges += len(ranges)
	b.bytes += totalBytes
	b.base = start
	b.nextOff += parsed.consumed
	return nil
}

// Locate maps a global playback time to a byte range.
func (b *ByteRangeIndexer) Locate(t time.Duration, requireSAP bool) (ByteRange, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if t < 0 {
		return ByteRange{}, ErrOutOfRange
	}
	if requireSAP {
		for _, id := range b.order {
			for _, r := range b.bySeg[id] {
				if r.SAP && r.End > t {
					if b.dead[id] {
						return ByteRange{}, ErrOutOfRange
					}
					return r, nil
				}
			}
		}
		return ByteRange{}, ErrOutOfRange
	}
	for _, id := range b.order {
		for _, r := range b.bySeg[id] {
			if r.Start <= t && t < r.End {
				if b.dead[id] {
					return ByteRange{}, ErrOutOfRange
				}
				return r, nil
			}
		}
	}
	return ByteRange{}, ErrOutOfRange
}

// Invalidate drops one segment from future lookups.
func (b *ByteRangeIndexer) Invalidate(segID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.bySeg[segID]; !ok || b.dead[segID] {
		return ErrUnknownSegment
	}
	b.dead[segID] = true
	b.deadRng += len(b.bySeg[segID])
	b.invalid++
	return nil
}

// Entries returns how many live byte ranges are indexed.
func (b *ByteRangeIndexer) Entries() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ranges - b.deadRng
}

// RangeAt reads one range by segment and local index.
func (b *ByteRangeIndexer) RangeAt(segID string, index int) (ByteRange, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ranges, ok := b.bySeg[segID]
	if !ok {
		return ByteRange{}, ErrUnknownSegment
	}
	if index < 0 || index >= len(ranges) {
		return ByteRange{}, ErrOutOfRange
	}
	return ranges[index], nil
}

// Segments lists the live segment ids in indexing order.
func (b *ByteRangeIndexer) Segments() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, 0, len(b.order))
	for _, id := range b.order {
		if !b.dead[id] {
			out = append(out, id)
		}
	}
	return out
}

// TotalBytes returns the sum of all indexed range sizes.
func (b *ByteRangeIndexer) TotalBytes() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.bytes
}

// Stats returns a copy of the counters.
func (b *ByteRangeIndexer) Stats() IndexStats {
	b.mu.Lock()
	defer b.mu.Unlock()
	return IndexStats{
		Segments:    len(b.order) - len(b.dead),
		Ranges:      b.ranges - b.deadRng,
		Bytes:       b.bytes,
		Invalidated: b.invalid,
	}
}

// Reset clears the whole index.
func (b *ByteRangeIndexer) Reset() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.order = nil
	b.bySeg = map[string][]ByteRange{}
	b.dead = map[string]bool{}
	b.ranges = 0
	b.deadRng = 0
	b.bytes = 0
	b.invalid = 0
	b.base = 0
	b.nextOff = 0
	return nil
}

const snapshotVersion = 1

type snapshotSegment struct {
	ID     string      `json:"id"`
	Dead   bool        `json:"dead"`
	Ranges []ByteRange `json:"ranges"`
}

type snapshotState struct {
	Version  int               `json:"version"`
	Base     time.Duration     `json:"base"`
	NextOff  int64             `json:"nextOff"`
	Ranges   int               `json:"ranges"`
	Bytes    int64             `json:"bytes"`
	Invalid  int               `json:"invalid"`
	Segments []snapshotSegment `json:"segments"`
}

// Snapshot serialises the index and the counters.
func (b *ByteRangeIndexer) Snapshot() ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	state := snapshotState{
		Version: snapshotVersion,
		Base:    b.base,
		NextOff: b.nextOff,
		Ranges:  b.ranges,
		Bytes:   b.bytes,
		Invalid: b.invalid,
	}
	for _, id := range b.order {
		state.Segments = append(state.Segments, snapshotSegment{
			ID:     id,
			Dead:   b.dead[id],
			Ranges: b.bySeg[id],
		})
	}
	return json.Marshal(state)
}

// Restore replaces the state with a snapshot, all or nothing.
func (b *ByteRangeIndexer) Restore(data []byte) error {
	var state snapshotState
	if err := json.Unmarshal(data, &state); err != nil {
		return ErrBadSnapshot
	}
	if state.Version != snapshotVersion || state.Base < 0 || state.NextOff < 0 {
		return ErrBadSnapshot
	}
	order := make([]string, 0, len(state.Segments))
	bySeg := make(map[string][]ByteRange, len(state.Segments))
	dead := make(map[string]bool)
	seen := make(map[string]bool, len(state.Segments))
	var ranges, deadRng, bytes, invalid int64
	var prevEnd time.Duration
	var prevOff int64
	hasPrev := false
	for _, sg := range state.Segments {
		if seen[sg.ID] {
			return ErrBadSnapshot
		}
		seen[sg.ID] = true
		for i, r := range sg.Ranges {
			if r.Segment != sg.ID || r.Index != i || r.Size < 0 || r.Offset < 0 || r.End < r.Start {
				return ErrBadSnapshot
			}
			if i > 0 {
				prev := sg.Ranges[i-1]
				if r.Offset != prev.Offset+prev.Size || r.Start != prev.End {
					return ErrBadSnapshot
				}
			} else if hasPrev {
				if r.Start != prevEnd || r.Offset < prevOff {
					return ErrBadSnapshot
				}
			}
			ranges++
			bytes += r.Size
		}
		if n := len(sg.Ranges); n > 0 {
			last := sg.Ranges[n-1]
			prevEnd = last.End
			prevOff = last.Offset + last.Size
			hasPrev = true
		}
		if sg.Dead {
			dead[sg.ID] = true
			invalid++
			deadRng += int64(len(sg.Ranges))
		}
		order = append(order, sg.ID)
		cp := make([]ByteRange, len(sg.Ranges))
		copy(cp, sg.Ranges)
		bySeg[sg.ID] = cp
	}
	if hasPrev && state.Base < prevEnd {
		return ErrBadSnapshot
	}
	if int(ranges) != state.Ranges || bytes != state.Bytes || int(invalid) != state.Invalid {
		return ErrBadSnapshot
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.order = order
	b.bySeg = bySeg
	b.dead = dead
	b.ranges = int(ranges)
	b.deadRng = int(deadRng)
	b.bytes = bytes
	b.invalid = int(invalid)
	b.base = state.Base
	b.nextOff = state.NextOff
	return nil
}
