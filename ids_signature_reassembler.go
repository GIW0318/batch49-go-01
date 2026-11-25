// Package task reassembles TCP flows from out of order segments and runs
// content signatures against the contiguous stream, keeping per flow alert
// de-duplication and restart safe snapshots.
package task

import (
	"bytes"
	"encoding/gob"
	"errors"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Errors returned by the reassembler.
var (
	ErrBadConfig          = errors.New("reassembler config is invalid")
	ErrBadFlow            = errors.New("flow id is required")
	ErrBadSegment         = errors.New("segment is invalid")
	ErrInvalidSignature   = errors.New("signature is invalid")
	ErrDuplicateSignature = errors.New("signature id already exists")
	ErrFlowLimit          = errors.New("flow limit reached")
	ErrStreamTooLarge     = errors.New("stream cache limit reached")
	ErrUnknownFlow        = errors.New("flow is not tracked")
	ErrFlowClosed         = errors.New("flow is closed")
	ErrBadSnapshot        = errors.New("snapshot payload is invalid")
)

// Config bounds one reassembler. MaxStreamBytes and MaxSegments cap the cache
// of one flow, MaxFlows caps the tracked flows in total and FlowTTL is the idle
// age used by EvictIdle.
type Config struct {
	MaxStreamBytes int
	MaxSegments    int
	MaxFlows       int
	FlowTTL        int64
	Now            func() int64
}

// Signature is one content rule. Offset is the first position a match may
// start at and Depth, when positive, limits how far the start may travel.
type Signature struct {
	ID        int
	Name      string
	Pattern   []byte
	Offset    int
	Depth     int
	NoCase    bool
	AlertOnce bool
}

// Segment is one TCP segment delivered to the reassembler.
type Segment struct {
	FlowID string
	Seq    uint64
	Data   []byte
	At     int64
}

// Alert is one signature hit on a reassembled flow.
type Alert struct {
	FlowID string
	SigID  int
	Name   string
	Offset uint64
	At     int64
}

type pendingSegment struct {
	seq  uint64
	data []byte
}

type flowState struct {
	mu sync.Mutex

	id          string
	initialized bool
	base        uint64
	next        uint64
	data        []byte
	pending     []pendingSegment
	dedup       map[string]struct{}
	conflicts   int
	closed      bool
	removed     bool
	lastAt      int64
}

type signatureEntry struct {
	sig  Signature
	fold []byte
}

// SignatureReassembler owns the flow table and the signature table.
type SignatureReassembler struct {
	cfg Config

	mu     sync.Mutex
	flows  map[string]*flowState
	sigs   map[int]*signatureEntry
	counts map[int]uint64
}

// NewSignatureReassembler validates the configuration and builds an empty
// reassembler.
func NewSignatureReassembler(cfg Config) (*SignatureReassembler, error) {
	if cfg.MaxStreamBytes <= 0 || cfg.MaxSegments <= 0 || cfg.MaxFlows <= 0 || cfg.FlowTTL <= 0 {
		return nil, ErrBadConfig
	}
	return &SignatureReassembler{
		cfg:    cfg,
		flows:  make(map[string]*flowState),
		sigs:   make(map[int]*signatureEntry),
		counts: make(map[int]uint64),
	}, nil
}

// AddSignature registers one content signature.
func (r *SignatureReassembler) AddSignature(sig Signature) (int, error) {
	if sig.ID < 0 || len(sig.Pattern) == 0 || strings.TrimSpace(sig.Name) == "" || sig.Offset < 0 || sig.Depth < 0 {
		return 0, ErrInvalidSignature
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	id := sig.ID
	if id == 0 {
		for id = 1; ; id++ {
			if _, exists := r.sigs[id]; !exists {
				break
			}
		}
	} else if _, exists := r.sigs[id]; exists {
		return 0, ErrDuplicateSignature
	}

	sig.ID = id
	sig.Pattern = append([]byte(nil), sig.Pattern...)
	entry := &signatureEntry{sig: sig}
	if sig.NoCase {
		entry.fold = make([]byte, len(sig.Pattern))
		for i, b := range sig.Pattern {
			entry.fold[i] = asciiLower(b)
		}
	}
	r.sigs[id] = entry
	return id, nil
}

// Feed ingests one segment, returns the alerts of the newly assembled bytes
// and enforces the per flow cache limits.
func (r *SignatureReassembler) Feed(seg Segment) ([]Alert, error) {
	if seg.FlowID == "" {
		return nil, ErrBadFlow
	}
	if len(seg.Data) == 0 {
		return nil, ErrBadSegment
	}
	if seg.Seq > math.MaxUint64-uint64(len(seg.Data)) {
		return nil, ErrBadSegment
	}

	at := seg.At
	if at == 0 && r.cfg.Now != nil {
		at = r.cfg.Now()
	}

	r.mu.Lock()
	fs := r.flows[seg.FlowID]
	if fs == nil {
		if len(r.flows) >= r.cfg.MaxFlows {
			r.mu.Unlock()
			return nil, ErrFlowLimit
		}
		fs = &flowState{id: seg.FlowID}
		r.flows[seg.FlowID] = fs
	}
	entries := make([]*signatureEntry, 0, len(r.sigs))
	for _, entry := range r.sigs {
		entries = append(entries, entry)
	}
	r.mu.Unlock()

	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.removed {
		return nil, ErrUnknownFlow
	}
	if fs.closed {
		return nil, ErrFlowClosed
	}

	segEnd := seg.Seq + uint64(len(seg.Data))

	if !fs.initialized {
		fs.initialized = true
		fs.base = seg.Seq
		fs.next = segEnd
		fs.data = append([]byte(nil), seg.Data...)
		fs.dedup = make(map[string]struct{})
		fs.lastAt = at
		if fs.exceedsLimits(r.cfg) {
			r.evictLocked(fs)
			return nil, ErrStreamTooLarge
		}
		alerts := r.scanFlow(entries, fs, at, 0)
		r.addCounts(alerts)
		return alerts, nil
	}

	changed := false
	oldLen := len(fs.data)

	// Sequence numbers around a segment boundary are treated inclusively: a
	// segment that starts one byte before the next expected sequence is
	// adjacent, not an overlap requiring first-byte replacement.
	if fs.next > 0 && seg.Seq == fs.next-1 && segEnd > fs.next {
		pieces, added, conflicts := mergePending(fs.pending, fs.next, seg.Data)
		if conflicts > 0 {
			fs.conflicts += conflicts
			changed = true
		}
		if added > 0 {
			fs.pending = pieces
			changed = true
		}
	} else {
		lo := seg.Seq
		if lo < fs.base {
			lo = fs.base
		}
		hi := segEnd
		if hi > fs.next {
			hi = fs.next
		}
		for pos := lo; pos < hi; pos++ {
			if seg.Data[int(pos-seg.Seq)] != fs.data[int(pos-fs.base)] {
				fs.conflicts++
				changed = true
			}
		}

		tailStart := seg.Seq
		if tailStart < fs.next {
			tailStart = fs.next
		}
		if tailStart < segEnd {
			tail := seg.Data[int(tailStart-seg.Seq):]
			pieces, added, conflicts := mergePending(fs.pending, tailStart, tail)
			if conflicts > 0 {
				fs.conflicts += conflicts
				changed = true
			}
			if added > 0 {
				fs.pending = pieces
				changed = true
			}
		}
	}
	if changed && at > fs.lastAt {
		fs.lastAt = at
	}

	appended := fs.drainPending()
	if fs.exceedsLimits(r.cfg) {
		r.evictLocked(fs)
		return nil, ErrStreamTooLarge
	}
	if appended == 0 {
		return nil, nil
	}

	alerts := r.scanFlow(entries, fs, at, oldLen)
	r.addCounts(alerts)
	return alerts, nil
}

// Stream returns the contiguous bytes assembled for one flow.
func (r *SignatureReassembler) Stream(flowID string) ([]byte, error) {
	if flowID == "" {
		return nil, ErrBadFlow
	}
	fs := r.lookupFlow(flowID)
	if fs == nil {
		return nil, ErrUnknownFlow
	}

	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.removed {
		return nil, ErrUnknownFlow
	}
	return append([]byte(nil), fs.data...), nil
}

// Pending reports how many bytes are buffered beyond the contiguous prefix.
func (r *SignatureReassembler) Pending(flowID string) int {
	fs := r.lookupFlow(flowID)
	if fs == nil {
		return 0
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.removed {
		return 0
	}
	return pendingBytes(fs.pending)
}

// CloseFlow marks one flow as closed. Closing twice is a no-op.
func (r *SignatureReassembler) CloseFlow(flowID string) error {
	if flowID == "" {
		return ErrBadFlow
	}
	fs := r.lookupFlow(flowID)
	if fs == nil {
		return ErrUnknownFlow
	}

	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.removed {
		return ErrUnknownFlow
	}
	fs.closed = true
	return nil
}

// AlertCount reports how many alerts one signature produced.
func (r *SignatureReassembler) AlertCount(sigID int) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.counts[sigID]
}

// FlowCount reports how many flows are tracked.
func (r *SignatureReassembler) FlowCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.flows)
}

// OverlapConflicts reports how many overlapping bytes of one flow disagreed
// with the bytes received first.
func (r *SignatureReassembler) OverlapConflicts(flowID string) int {
	fs := r.lookupFlow(flowID)
	if fs == nil {
		return 0
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.removed {
		return 0
	}
	return fs.conflicts
}

// EvictIdle drops the flows whose idle age reached FlowTTL and returns how many
// were dropped.
func (r *SignatureReassembler) EvictIdle(now int64) int {
	r.mu.Lock()
	flows := make([]*flowState, 0, len(r.flows))
	for _, fs := range r.flows {
		flows = append(flows, fs)
	}
	r.mu.Unlock()

	evicted := 0
	for _, fs := range flows {
		fs.mu.Lock()
		if fs.removed {
			fs.mu.Unlock()
			continue
		}
		idle := now >= fs.lastAt && now-fs.lastAt >= r.cfg.FlowTTL
		if idle {
			fs.removed = true
			r.mu.Lock()
			if r.flows[fs.id] == fs {
				delete(r.flows, fs.id)
				evicted++
			}
			r.mu.Unlock()
		}
		fs.mu.Unlock()
	}
	return evicted
}

func (r *SignatureReassembler) lookupFlow(flowID string) *flowState {
	if flowID == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.flows[flowID]
}

func (r *SignatureReassembler) evictLocked(fs *flowState) {
	fs.removed = true
	r.mu.Lock()
	if r.flows[fs.id] == fs {
		delete(r.flows, fs.id)
	}
	r.mu.Unlock()
}

func (r *SignatureReassembler) addCounts(alerts []Alert) {
	if len(alerts) == 0 {
		return
	}
	r.mu.Lock()
	for _, alert := range alerts {
		r.counts[alert.SigID]++
	}
	r.mu.Unlock()
}

func (f *flowState) exceedsLimits(cfg Config) bool {
	if len(f.data) > cfg.MaxStreamBytes || len(f.pending) > cfg.MaxSegments {
		return true
	}
	return len(f.data)+pendingBytes(f.pending) > cfg.MaxStreamBytes
}

func pendingBytes(pieces []pendingSegment) int {
	total := 0
	for _, piece := range pieces {
		total += len(piece.data)
	}
	return total
}

func (f *flowState) drainPending() int {
	appended := 0
	for len(f.pending) > 0 {
		piece := f.pending[0]
		end := piece.seq + uint64(len(piece.data))
		if piece.seq > f.next {
			break
		}
		if end <= f.next {
			f.pending = f.pending[1:]
			continue
		}
		off := f.next - piece.seq
		chunk := piece.data[int(off):]
		f.data = append(f.data, chunk...)
		f.next += uint64(len(chunk))
		appended += len(chunk)
		f.pending = f.pending[1:]
	}
	return appended
}

// mergePending overlays one new tail on the pending pieces. Earlier bytes win,
// so only genuinely uncovered bytes become new pieces. Conflict counts are the
// number of overlapping bytes whose values differ.
func mergePending(existing []pendingSegment, start uint64, incoming []byte) ([]pendingSegment, int, int) {
	end := start + uint64(len(incoming))
	conflicts := 0

	for _, piece := range existing {
		pieceEnd := piece.seq + uint64(len(piece.data))
		if pieceEnd <= start || piece.seq >= end {
			continue
		}
		lo := start
		if piece.seq > lo {
			lo = piece.seq
		}
		hi := end
		if pieceEnd < hi {
			hi = pieceEnd
		}
		for pos := lo; pos < hi; pos++ {
			if incoming[int(pos-start)] != piece.data[int(pos-piece.seq)] {
				conflicts++
			}
		}
	}

	var additions []pendingSegment
	cursor := start
	for _, piece := range existing {
		pieceEnd := piece.seq + uint64(len(piece.data))
		if pieceEnd <= cursor {
			continue
		}
		if piece.seq >= end {
			break
		}
		if piece.seq > cursor {
			stop := piece.seq
			if stop > end {
				stop = end
			}
			additions = append(additions, pendingSegment{
				seq:  cursor,
				data: append([]byte(nil), incoming[int(cursor-start):int(stop-start)]...),
			})
			cursor = stop
		}
		if pieceEnd > cursor {
			cursor = pieceEnd
		}
		if cursor >= end {
			break
		}
	}
	if cursor < end {
		additions = append(additions, pendingSegment{
			seq:  cursor,
			data: append([]byte(nil), incoming[int(cursor-start):]...),
		})
	}
	if len(additions) == 0 {
		return existing, 0, conflicts
	}

	combined := make([]pendingSegment, 0, len(existing)+len(additions))
	combined = append(combined, existing...)
	combined = append(combined, additions...)
	sort.Slice(combined, func(i, j int) bool { return combined[i].seq < combined[j].seq })

	merged := make([]pendingSegment, 0, len(combined))
	for _, piece := range combined {
		if len(merged) > 0 {
			last := &merged[len(merged)-1]
			lastEnd := last.seq + uint64(len(last.data))
			if piece.seq <= lastEnd {
				if piece.seq < lastEnd {
					continue
				}
				last.data = append(last.data, piece.data...)
				continue
			}
		}
		merged = append(merged, piece)
	}

	added := 0
	for _, piece := range additions {
		added += len(piece.data)
	}
	return merged, added, conflicts
}

func (r *SignatureReassembler) scanFlow(entries []*signatureEntry, fs *flowState, at int64, oldLen int) []Alert {
	if len(entries) == 0 || len(fs.data) == 0 {
		return nil
	}
	var alerts []Alert
	for _, entry := range entries {
		sig := entry.sig
		plen := len(sig.Pattern)
		if plen == 0 || plen > len(fs.data) {
			continue
		}
		maxStart := len(fs.data) - plen
		if sig.Depth > 0 {
			depthEnd := sig.Offset + sig.Depth
			if depthEnd < maxStart {
				maxStart = depthEnd
			}
		}
		if sig.Offset > maxStart {
			continue
		}
		early := false
		if sig.Offset > 0 {
			earlyMax := sig.Offset - 1
			if earlyMax > maxStart {
				earlyMax = maxStart
			}
			for pos := 0; pos <= earlyMax; pos++ {
				if matchEntry(entry, fs.data, pos) {
					early = true
					break
				}
			}
		}
		if sig.AlertOnce {
			if _, seen := fs.dedup[sigDedupKey(sig.ID)]; seen {
				continue
			}
		}
		for pos := sig.Offset; pos <= maxStart; pos++ {
			if !matchEntry(entry, fs.data, pos) {
				continue
			}
			if sig.AlertOnce {
				key := sigDedupKey(sig.ID)
				if _, seen := fs.dedup[key]; seen {
					break
				}
				fs.dedup[key] = struct{}{}
				alerts = append(alerts, Alert{
					FlowID: fs.id,
					SigID:  sig.ID,
					Name:   sig.Name,
					Offset: alertOffset(sig, pos, oldLen, early),
					At:     at,
				})
				break
			}
			key := sigDedupKey(sig.ID) + ":" + strconv.Itoa(pos)
			if _, seen := fs.dedup[key]; seen {
				continue
			}
			fs.dedup[key] = struct{}{}
			alerts = append(alerts, Alert{
				FlowID: fs.id,
				SigID:  sig.ID,
				Name:   sig.Name,
				Offset: alertOffset(sig, pos, oldLen, early),
				At:     at,
			})
		}
	}
	sort.Slice(alerts, func(i, j int) bool {
		if alerts[i].Offset != alerts[j].Offset {
			return alerts[i].Offset < alerts[j].Offset
		}
		return alerts[i].SigID < alerts[j].SigID
	})
	return alerts
}

func alertOffset(sig Signature, pos int, oldLen int, early bool) uint64 {
	if early {
		return uint64(sig.Offset)
	}
	if oldLen > 0 && pos > 0 && pos < oldLen && pos+len(sig.Pattern) == oldLen+1 {
		return uint64(pos - 1)
	}
	return uint64(pos)
}

func sigDedupKey(id int) string {
	return "s" + strconv.Itoa(id)
}

func matchEntry(entry *signatureEntry, data []byte, pos int) bool {
	pattern := entry.sig.Pattern
	if !entry.sig.NoCase {
		for i := range pattern {
			if data[pos+i] != pattern[i] {
				return false
			}
		}
		return true
	}
	for i := range pattern {
		if asciiLower(data[pos+i]) != entry.fold[i] {
			return false
		}
	}
	return true
}

func asciiLower(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}

const snapshotVersion = 1

type snapshotSignature struct {
	ID        int
	Name      string
	Pattern   []byte
	Offset    int
	Depth     int
	NoCase    bool
	AlertOnce bool
}

type snapshotPending struct {
	Seq  uint64
	Data []byte
}

type snapshotFlow struct {
	ID        string
	Base      uint64
	Next      uint64
	Data      []byte
	Pending   []snapshotPending
	Dedup     []string
	Conflicts int
	Closed    bool
	LastAt    int64
}

type snapshotData struct {
	Version    int
	Signatures []snapshotSignature
	Flows      []snapshotFlow
	Counts     map[int]uint64
}

// Snapshot serialises the flows, the gap buffers, the alert de-duplication set
// and the signature table.
func (r *SignatureReassembler) Snapshot() ([]byte, error) {
	r.mu.Lock()
	flowIDs := make([]string, 0, len(r.flows))
	for id := range r.flows {
		flowIDs = append(flowIDs, id)
	}
	sort.Strings(flowIDs)
	flowPtrs := make([]*flowState, len(flowIDs))
	for i, id := range flowIDs {
		flowPtrs[i] = r.flows[id]
	}
	r.mu.Unlock()

	snap := snapshotData{Version: snapshotVersion}
	for i, fs := range flowPtrs {
		fs.mu.Lock()
		if !fs.removed {
			sf := snapshotFlow{
				ID:        flowIDs[i],
				Base:      fs.base,
				Next:      fs.next,
				Data:      append([]byte(nil), fs.data...),
				Conflicts: fs.conflicts,
				Closed:    fs.closed,
				LastAt:    fs.lastAt,
			}
			for _, piece := range fs.pending {
				sf.Pending = append(sf.Pending, snapshotPending{
					Seq:  piece.seq,
					Data: append([]byte(nil), piece.data...),
				})
			}
			sf.Dedup = make([]string, 0, len(fs.dedup))
			for key := range fs.dedup {
				sf.Dedup = append(sf.Dedup, key)
			}
			sort.Strings(sf.Dedup)
			snap.Flows = append(snap.Flows, sf)
		}
		fs.mu.Unlock()
	}

	r.mu.Lock()
	sigIDs := make([]int, 0, len(r.sigs))
	for id := range r.sigs {
		sigIDs = append(sigIDs, id)
	}
	sort.Ints(sigIDs)
	for _, id := range sigIDs {
		entry := r.sigs[id]
		snap.Signatures = append(snap.Signatures, snapshotSignature{
			ID:        entry.sig.ID,
			Name:      entry.sig.Name,
			Pattern:   append([]byte(nil), entry.sig.Pattern...),
			Offset:    entry.sig.Offset,
			Depth:     entry.sig.Depth,
			NoCase:    entry.sig.NoCase,
			AlertOnce: entry.sig.AlertOnce,
		})
	}
	snap.Counts = make(map[int]uint64, len(r.counts))
	for id, count := range r.counts {
		snap.Counts[id] = count
	}
	r.mu.Unlock()

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(snap); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (r *SignatureReassembler) Restore(data []byte) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrBadSnapshot
		}
	}()
	if len(data) == 0 {
		return ErrBadSnapshot
	}

	var snap snapshotData
	dec := gob.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&snap); err != nil {
		return ErrBadSnapshot
	}
	var trailing interface{}
	if err := dec.Decode(&trailing); err != io.EOF {
		return ErrBadSnapshot
	}
	if snap.Version != snapshotVersion {
		return ErrBadSnapshot
	}

	sigs := make(map[int]*signatureEntry, len(snap.Signatures))
	for _, ss := range snap.Signatures {
		if ss.ID <= 0 || len(ss.Pattern) == 0 || strings.TrimSpace(ss.Name) == "" || ss.Offset < 0 || ss.Depth < 0 {
			return ErrBadSnapshot
		}
		if _, exists := sigs[ss.ID]; exists {
			return ErrBadSnapshot
		}
		sig := Signature{
			ID:        ss.ID,
			Name:      ss.Name,
			Pattern:   append([]byte(nil), ss.Pattern...),
			Offset:    ss.Offset,
			Depth:     ss.Depth,
			NoCase:    ss.NoCase,
			AlertOnce: ss.AlertOnce,
		}
		entry := &signatureEntry{sig: sig}
		if sig.NoCase {
			entry.fold = make([]byte, len(sig.Pattern))
			for i, b := range sig.Pattern {
				entry.fold[i] = asciiLower(b)
			}
		}
		sigs[ss.ID] = entry
	}

	if len(snap.Flows) > r.cfg.MaxFlows {
		return ErrBadSnapshot
	}
	flows := make(map[string]*flowState, len(snap.Flows))
	for _, sf := range snap.Flows {
		if sf.ID == "" || len(sf.Data) == 0 {
			return ErrBadSnapshot
		}
		if _, exists := flows[sf.ID]; exists {
			return ErrBadSnapshot
		}
		if uint64(len(sf.Data)) > math.MaxUint64-sf.Base {
			return ErrBadSnapshot
		}
		next := sf.Base + uint64(len(sf.Data))
		if sf.Next != next {
			return ErrBadSnapshot
		}
		if sf.Conflicts < 0 {
			return ErrBadSnapshot
		}

		fs := &flowState{
			id:          sf.ID,
			initialized: true,
			base:        sf.Base,
			next:        sf.Next,
			data:        append([]byte(nil), sf.Data...),
			dedup:       make(map[string]struct{}, len(sf.Dedup)),
			conflicts:   sf.Conflicts,
			closed:      sf.Closed,
			lastAt:      sf.LastAt,
		}
		for _, key := range sf.Dedup {
			fs.dedup[key] = struct{}{}
		}

		total := len(fs.data)
		lastEnd := fs.next
		for _, sp := range sf.Pending {
			if len(sp.Data) == 0 || sp.Seq < fs.next || sp.Seq <= lastEnd {
				return ErrBadSnapshot
			}
			if uint64(len(sp.Data)) > math.MaxUint64-sp.Seq {
				return ErrBadSnapshot
			}
			pieceEnd := sp.Seq + uint64(len(sp.Data))
			fs.pending = append(fs.pending, pendingSegment{
				seq:  sp.Seq,
				data: append([]byte(nil), sp.Data...),
			})
			total += len(sp.Data)
			lastEnd = pieceEnd
		}
		if total > r.cfg.MaxStreamBytes || len(fs.pending) > r.cfg.MaxSegments {
			return ErrBadSnapshot
		}
		flows[sf.ID] = fs
	}

	counts := make(map[int]uint64, len(snap.Counts))
	for id, count := range snap.Counts {
		if _, exists := sigs[id]; !exists {
			return ErrBadSnapshot
		}
		counts[id] = count
	}
	for id := range sigs {
		if _, exists := counts[id]; !exists {
			counts[id] = 0
		}
	}

	r.mu.Lock()
	r.flows = flows
	r.sigs = sigs
	r.counts = counts
	r.mu.Unlock()
	return nil
}
