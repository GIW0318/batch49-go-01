// Package task collects signed tree heads from several log and witness sources
// and detects two contradictory views of the same certificate transparency log.
package task

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/bits"
	"sort"
	"strings"
	"sync"
	"time"
)

// errNotImplemented marks behaviour that still has to be supplied.
var errNotImplemented = errors.New("not implemented")

// Offer verdict statuses.
const (
	StatusAccepted    = "accepted"
	StatusPending     = "pending"
	StatusStale       = "stale"
	StatusSplitView   = "split_view"
	StatusQuarantined = "quarantined"
)

// Defaults applied to zero valued Config fields.
const (
	DefaultQuorum = 2
)

// Errors returned by the watch.
var (
	ErrBadSTH      = errors.New("signed tree head is incomplete")
	ErrUnknownLog  = errors.New("log id is unknown")
	ErrNoConsensus = errors.New("no quorum reached for this log")
	ErrQuarantined = errors.New("log is quarantined")
	ErrBadReason   = errors.New("quarantine reason is required")
	ErrUnknownHead = errors.New("tree head size is unknown")
	ErrBadRange    = errors.New("tree size range is invalid")
	ErrBadSnapshot = errors.New("snapshot payload is invalid")
)

// Clock supplies the current time for bookkeeping.
type Clock interface {
	Now() time.Time
}

// SignedTreeHead is one signed view submitted by a log or witness source.
type SignedTreeHead struct {
	LogID     string    `json:"log_id"`
	Source    string    `json:"source"`
	TreeSize  uint64    `json:"tree_size"`
	RootHash  []byte    `json:"root_hash"`
	Timestamp time.Time `json:"timestamp"`
	Signature []byte    `json:"signature"`
}

// TreeHead is the agreed view of one log.
type TreeHead struct {
	LogID     string    `json:"log_id"`
	TreeSize  uint64    `json:"tree_size"`
	RootHash  []byte    `json:"root_hash"`
	Sources   int       `json:"sources"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Verdict is the answer of one Offer call.
type Verdict struct {
	LogID    string `json:"log_id"`
	Status   string `json:"status"`
	Reason   string `json:"reason"`
	TreeSize uint64 `json:"tree_size"`
	Sources  int    `json:"sources"`
}

// Evidence is one contradictory or isolated head kept for later review.
type Evidence struct {
	LogID     string    `json:"log_id"`
	TreeSize  uint64    `json:"tree_size"`
	RootHash  []byte    `json:"root_hash"`
	Source    string    `json:"source"`
	Timestamp time.Time `json:"timestamp"`
	Reason    string    `json:"reason"`
}

// Config bounds the watch.
type Config struct {
	Quorum int   `json:"quorum"`
	Clock  Clock `json:"-"`
}

// Stats aggregates the counters of the watch.
type Stats struct {
	Offers      int64 `json:"offers"`
	Accepted    int64 `json:"accepted"`
	Pending     int64 `json:"pending"`
	Stale       int64 `json:"stale"`
	SplitViews  int64 `json:"split_views"`
	Quarantined int   `json:"quarantined"`
	Logs        int   `json:"logs"`
}

// logState holds everything that belongs to one log. All fields are protected
// by SplitViewWatch.mu, which makes an Offer decision and its state changes
// atomic.
type logState struct {
	Sources          map[string]SignedTreeHead `json:"sources"`
	Accepted         *TreeHead                 `json:"accepted,omitempty"`
	Sizes            map[uint64][]byte         `json:"sizes,omitempty"`
	Evidence         []Evidence                `json:"evidence,omitempty"`
	Quarantined      bool                      `json:"quarantined,omitempty"`
	QuarantineReason string                    `json:"quarantine_reason,omitempty"`
}

// SplitViewWatch owns the per log source table, the accepted head and the
// evidence set.
type SplitViewWatch struct {
	mu    sync.Mutex
	cfg   Config
	logs  map[string]*logState
	stats Stats
}

// NewSplitViewWatch builds a watch with the given configuration.
func NewSplitViewWatch(cfg Config) *SplitViewWatch {
	return &SplitViewWatch{
		cfg:  cfg,
		logs: make(map[string]*logState),
	}
}

// Offer submits one signed head for its log.
func (w *SplitViewWatch) Offer(sth SignedTreeHead) (Verdict, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if strings.TrimSpace(sth.LogID) == "" ||
		strings.TrimSpace(sth.Source) == "" ||
		len(sth.RootHash) == 0 {
		return Verdict{}, ErrBadSTH
	}
	sth = cloneSTH(sth)
	w.stats.Offers++

	ls := w.logs[sth.LogID]
	if ls == nil {
		ls = &logState{
			Sources: make(map[string]SignedTreeHead),
			Sizes:   make(map[uint64][]byte),
		}
		w.logs[sth.LogID] = ls
	}

	// Once a log is isolated, observations can only add evidence. They may not
	// update the accepted watermark, source table or consensus state.
	if ls.Quarantined {
		if !w.isReplayLocked(ls, sth) && w.shouldRetainWhileQuarantined(ls, sth) {
			w.addEvidence(ls, sth, "quarantined")
		}
		return Verdict{
			LogID:    sth.LogID,
			Status:   StatusQuarantined,
			Reason:   ls.QuarantineReason,
			TreeSize: sth.TreeSize,
			Sources:  w.agreeingSources(ls, sth.TreeSize, sth.RootHash),
		}, nil
	}

	// A tree below the accepted watermark is stale, even when its root differs.
	// It cannot move the watermark, add a size index entry or isolate the log.
	if ls.Accepted != nil && sth.TreeSize < ls.Accepted.TreeSize {
		w.stats.Stale++
		return Verdict{
			LogID:    sth.LogID,
			Status:   StatusStale,
			TreeSize: sth.TreeSize,
			Sources:  w.agreeingSources(ls, sth.TreeSize, sth.RootHash),
		}, nil
	}

	prev, hadPrev := ls.Sources[sth.Source]
	// A backwards timestamp for an identical head is a replay. Ignore it
	// without changing counters, source state, evidence or the watermark. Heads
	// already retained as evidence are consulted too, since quarantine
	// observations deliberately do not update the source table.
	if w.isReplayLocked(ls, sth) {
		if ls.Accepted != nil && sameTreeHead(ls.Accepted, sth) {
			return Verdict{
				LogID:    sth.LogID,
				Status:   StatusAccepted,
				TreeSize: sth.TreeSize,
				Sources:  w.agreeingSources(ls, sth.TreeSize, sth.RootHash),
			}, nil
		}
		return Verdict{
			LogID:    sth.LogID,
			Status:   StatusPending,
			TreeSize: sth.TreeSize,
			Sources:  w.agreeingSources(ls, sth.TreeSize, sth.RootHash),
		}, nil
	}

	ls.Sources[sth.Source] = sth
	agreeing := w.agreeingSources(ls, sth.TreeSize, sth.RootHash)

	// After a log has reached consensus, two sources reporting the same larger
	// size with different roots are two mutually exclusive views as well.
	if ls.Accepted != nil && sth.TreeSize > ls.Accepted.TreeSize {
		conflicts := w.sameSizeConflicts(ls, sth, prev, hadPrev)
		if len(conflicts) > 0 {
			w.stats.SplitViews++
			ls.Quarantined = true
			ls.QuarantineReason = fmt.Sprintf(
				"split view: tree size %d has conflicting roots", sth.TreeSize,
			)
			for _, conflicting := range conflicts {
				w.addEvidence(ls, conflicting, StatusSplitView)
			}
			w.addEvidence(ls, sth, StatusSplitView)
			return Verdict{
				LogID:    sth.LogID,
				Status:   StatusSplitView,
				Reason:   ls.QuarantineReason,
				TreeSize: sth.TreeSize,
				Sources:  agreeing,
			}, nil
		}
	}

	// Same size as the accepted head.
	if ls.Accepted != nil && sth.TreeSize == ls.Accepted.TreeSize {
		if bytes.Equal(sth.RootHash, ls.Accepted.RootHash) {
			// A matching head confirms an existing source. It is accepted, but
			// does not count as a new acceptance or alter the accepted head.
			return Verdict{
				LogID:    sth.LogID,
				Status:   StatusAccepted,
				TreeSize: sth.TreeSize,
				Sources:  agreeing,
			}, nil
		}

		// A second mutually exclusive view for the already agreed size is a
		// split view. Isolate the log and keep the conflicting head.
		w.stats.SplitViews++
		ls.Quarantined = true
		ls.QuarantineReason = fmt.Sprintf(
			"split view: tree size %d has conflicting roots", sth.TreeSize,
		)
		w.addEvidence(ls, sth, StatusSplitView)
		return Verdict{
			LogID:    sth.LogID,
			Status:   StatusSplitView,
			Reason:   ls.QuarantineReason,
			TreeSize: sth.TreeSize,
			Sources:  agreeing,
		}, nil
	}

	if agreeing >= w.quorum() {
		w.acceptLocked(ls, sth, agreeing)
		// Evidence gathered before consensus is deliberately retained.
		return Verdict{
			LogID:    sth.LogID,
			Status:   StatusAccepted,
			TreeSize: sth.TreeSize,
			Sources:  agreeing,
		}, nil
	}

	w.stats.Pending++
	w.recordDisagreements(ls, sth, prev, hadPrev)
	return Verdict{
		LogID:    sth.LogID,
		Status:   StatusPending,
		TreeSize: sth.TreeSize,
		Sources:  agreeing,
	}, nil
}

// VerifyConsistency checks a Merkle consistency proof between two accepted
// tree sizes.
func (w *SplitViewWatch) VerifyConsistency(from, to uint64, proof [][]byte) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if from == 0 || from > to {
		return false, ErrBadRange
	}

	logs := w.logsWithSizes(from, to)
	if len(logs) == 0 {
		return false, ErrUnknownHead
	}

	if len(proof) != consistencyPathLen(from, to) {
		return false, nil
	}

	for _, ls := range logs {
		current := cloneBytes(ls.Sizes[from])
		for _, node := range proof {
			buf := make([]byte, 0, len(current)+len(node))
			buf = append(buf, current...)
			buf = append(buf, node...)
			sum := sha256.Sum256(buf)
			current = sum[:]
		}
		if bytes.Equal(current, ls.Sizes[to]) {
			return true, nil
		}
	}
	return false, nil
}

// Quarantine takes one log out of consensus and keeps the reason.
func (w *SplitViewWatch) Quarantine(logID, reason string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if strings.TrimSpace(reason) == "" {
		return ErrBadReason
	}
	ls := w.logs[logID]
	if ls == nil {
		return ErrUnknownLog
	}
	if ls.Quarantined {
		// Idempotent: retain the first reason and the existing evidence.
		return nil
	}
	ls.Quarantined = true
	ls.QuarantineReason = reason
	return nil
}

// Consensus returns the agreed head of one log.
func (w *SplitViewWatch) Consensus(logID string) (TreeHead, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	ls := w.logs[logID]
	if ls == nil {
		return TreeHead{}, ErrUnknownLog
	}
	if ls.Quarantined {
		return TreeHead{}, ErrQuarantined
	}
	if ls.Accepted == nil {
		return TreeHead{}, ErrNoConsensus
	}
	return cloneTreeHead(*ls.Accepted), nil
}

// Quarantined reports whether one log is isolated.
func (w *SplitViewWatch) Quarantined(logID string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	ls := w.logs[logID]
	return ls != nil && ls.Quarantined
}

// Evidence returns the retained evidence of one log.
func (w *SplitViewWatch) Evidence(logID string) []Evidence {
	w.mu.Lock()
	defer w.mu.Unlock()

	ls := w.logs[logID]
	if ls == nil {
		return nil
	}
	return cloneEvidenceSlice(ls.Evidence)
}

// Logs lists every log the watch has seen, in ascending order.
func (w *SplitViewWatch) Logs() []string {
	w.mu.Lock()
	defer w.mu.Unlock()

	out := make([]string, 0, len(w.logs))
	for logID := range w.logs {
		out = append(out, logID)
	}
	sort.Strings(out)
	return out
}

// Stats reads the aggregate counters.
func (w *SplitViewWatch) Stats() Stats {
	w.mu.Lock()
	defer w.mu.Unlock()

	out := w.stats
	out.Logs = len(w.logs)
	out.Quarantined = 0
	for _, ls := range w.logs {
		if ls.Quarantined {
			out.Quarantined++
		}
	}
	return out
}

const snapshotVersion = 1

type snapshotSize struct {
	Size uint64 `json:"size"`
	Root []byte `json:"root"`
}

type snapshotLog struct {
	Sources          map[string]SignedTreeHead `json:"sources"`
	Accepted         *TreeHead                 `json:"accepted,omitempty"`
	Sizes            []snapshotSize            `json:"sizes,omitempty"`
	Evidence         []Evidence                `json:"evidence,omitempty"`
	Quarantined      bool                      `json:"quarantined,omitempty"`
	QuarantineReason string                    `json:"quarantine_reason,omitempty"`
}

type snapshotCounters struct {
	Offers     int64 `json:"offers"`
	Accepted   int64 `json:"accepted"`
	Pending    int64 `json:"pending"`
	Stale      int64 `json:"stale"`
	SplitViews int64 `json:"split_views"`
}

type snapshotState struct {
	Version  int                     `json:"version"`
	Logs     map[string]*snapshotLog `json:"logs"`
	Counters snapshotCounters        `json:"counters"`
}

type snapshotEnvelope struct {
	Version  int    `json:"version"`
	Checksum string `json:"checksum"`
	Payload  []byte `json:"payload"`
}

// Snapshot serialises the accepted heads, size indexes, source tables,
// evidence sets, quarantine state and counters.
func (w *SplitViewWatch) Snapshot() ([]byte, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	state := snapshotState{
		Version: snapshotVersion,
		Logs:    make(map[string]*snapshotLog, len(w.logs)),
		Counters: snapshotCounters{
			Offers:     w.stats.Offers,
			Accepted:   w.stats.Accepted,
			Pending:    w.stats.Pending,
			Stale:      w.stats.Stale,
			SplitViews: w.stats.SplitViews,
		},
	}

	for logID, ls := range w.logs {
		sl := &snapshotLog{
			Sources:          make(map[string]SignedTreeHead, len(ls.Sources)),
			Quarantined:      ls.Quarantined,
			QuarantineReason: ls.QuarantineReason,
		}
		for source, sth := range ls.Sources {
			sl.Sources[source] = cloneSTH(sth)
		}
		if ls.Accepted != nil {
			head := cloneTreeHead(*ls.Accepted)
			sl.Accepted = &head
		}
		sl.Sizes = make([]snapshotSize, 0, len(ls.Sizes))
		for size, root := range ls.Sizes {
			sl.Sizes = append(sl.Sizes, snapshotSize{Size: size, Root: cloneBytes(root)})
		}
		sort.Slice(sl.Sizes, func(i, j int) bool { return sl.Sizes[i].Size < sl.Sizes[j].Size })
		sl.Evidence = cloneEvidenceSlice(ls.Evidence)
		state.Logs[logID] = sl
	}

	payload, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(payload)
	envelope := snapshotEnvelope{
		Version:  snapshotVersion,
		Checksum: hex.EncodeToString(sum[:]),
		Payload:  payload,
	}
	return json.Marshal(envelope)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (w *SplitViewWatch) Restore(data []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	var envelope snapshotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return fmt.Errorf("%w: malformed envelope", ErrBadSnapshot)
	}
	if envelope.Version != snapshotVersion || len(envelope.Payload) == 0 {
		return fmt.Errorf("%w: unsupported snapshot", ErrBadSnapshot)
	}
	sum := sha256.Sum256(envelope.Payload)
	if envelope.Checksum != hex.EncodeToString(sum[:]) {
		return fmt.Errorf("%w: checksum mismatch", ErrBadSnapshot)
	}

	var state snapshotState
	if err := json.Unmarshal(envelope.Payload, &state); err != nil {
		return fmt.Errorf("%w: malformed state", ErrBadSnapshot)
	}
	if state.Version != snapshotVersion {
		return fmt.Errorf("%w: unsupported state version", ErrBadSnapshot)
	}
	if state.Counters.Offers < 0 || state.Counters.Accepted < 0 ||
		state.Counters.Pending < 0 || state.Counters.Stale < 0 ||
		state.Counters.SplitViews < 0 {
		return fmt.Errorf("%w: negative counters", ErrBadSnapshot)
	}

	logs := make(map[string]*logState, len(state.Logs))
	for logID, sl := range state.Logs {
		if logID == "" || sl == nil {
			return fmt.Errorf("%w: invalid log entry", ErrBadSnapshot)
		}
		ls := &logState{
			Sources:          make(map[string]SignedTreeHead, len(sl.Sources)),
			Sizes:            make(map[uint64][]byte, len(sl.Sizes)),
			Quarantined:      sl.Quarantined,
			QuarantineReason: sl.QuarantineReason,
		}
		for source, sth := range sl.Sources {
			if source == "" || sth.LogID != logID || len(sth.RootHash) == 0 {
				return fmt.Errorf("%w: invalid source head", ErrBadSnapshot)
			}
			ls.Sources[source] = cloneSTH(sth)
		}
		if sl.Accepted != nil {
			if sl.Accepted.LogID != logID || len(sl.Accepted.RootHash) == 0 {
				return fmt.Errorf("%w: invalid accepted head", ErrBadSnapshot)
			}
			head := cloneTreeHead(*sl.Accepted)
			ls.Accepted = &head
		}
		for _, entry := range sl.Sizes {
			if len(entry.Root) == 0 {
				return fmt.Errorf("%w: invalid size index", ErrBadSnapshot)
			}
			if _, exists := ls.Sizes[entry.Size]; exists {
				return fmt.Errorf("%w: duplicate size index", ErrBadSnapshot)
			}
			ls.Sizes[entry.Size] = cloneBytes(entry.Root)
		}
		if ls.Accepted != nil {
			root, ok := ls.Sizes[ls.Accepted.TreeSize]
			if !ok || !bytes.Equal(root, ls.Accepted.RootHash) {
				return fmt.Errorf("%w: accepted head is not indexed", ErrBadSnapshot)
			}
		}
		ls.Evidence = cloneEvidenceSlice(sl.Evidence)
		logs[logID] = ls
	}

	w.logs = logs
	w.stats = Stats{
		Offers:     state.Counters.Offers,
		Accepted:   state.Counters.Accepted,
		Pending:    state.Counters.Pending,
		Stale:      state.Counters.Stale,
		SplitViews: state.Counters.SplitViews,
	}
	return nil
}

func (w *SplitViewWatch) quorum() int {
	if w.cfg.Quorum <= 0 {
		return DefaultQuorum
	}
	return w.cfg.Quorum
}

func (w *SplitViewWatch) now() time.Time {
	if w.cfg.Clock != nil {
		return w.cfg.Clock.Now()
	}
	return time.Now()
}

func (w *SplitViewWatch) agreeingSources(ls *logState, size uint64, root []byte) int {
	count := 0
	for _, sth := range ls.Sources {
		if sth.TreeSize == size && bytes.Equal(sth.RootHash, root) {
			count++
		}
	}
	return count
}

func (w *SplitViewWatch) acceptLocked(ls *logState, sth SignedTreeHead, sources int) {
	ls.Accepted = &TreeHead{
		LogID:     sth.LogID,
		TreeSize:  sth.TreeSize,
		RootHash:  cloneBytes(sth.RootHash),
		Sources:   sources,
		UpdatedAt: w.now(),
	}
	ls.Sizes[sth.TreeSize] = cloneBytes(sth.RootHash)
	w.stats.Accepted++
}

func (w *SplitViewWatch) recordDisagreements(
	ls *logState,
	sth SignedTreeHead,
	prev SignedTreeHead,
	hadPrev bool,
) {
	if hadPrev && prev.TreeSize == sth.TreeSize &&
		!bytes.Equal(prev.RootHash, sth.RootHash) {
		w.addEvidence(ls, prev, "disagreement")
		w.addEvidence(ls, sth, "disagreement")
	}

	for source, other := range ls.Sources {
		if source == sth.Source {
			continue
		}
		if other.TreeSize == sth.TreeSize &&
			!bytes.Equal(other.RootHash, sth.RootHash) {
			w.addEvidence(ls, other, "disagreement")
			w.addEvidence(ls, sth, "disagreement")
			continue
		}
		// Before any consensus exists, different sizes are also retained:
		// there is no accepted watermark that can classify one as stale.
		if ls.Accepted == nil && other.TreeSize != sth.TreeSize {
			w.addEvidence(ls, other, "disagreement")
			w.addEvidence(ls, sth, "disagreement")
		}
	}
}

func (w *SplitViewWatch) shouldRetainWhileQuarantined(ls *logState, sth SignedTreeHead) bool {
	if ls.Accepted == nil {
		return true
	}
	if sth.TreeSize > ls.Accepted.TreeSize {
		return true
	}
	return sth.TreeSize == ls.Accepted.TreeSize &&
		!bytes.Equal(sth.RootHash, ls.Accepted.RootHash)
}

func (w *SplitViewWatch) sameSizeConflicts(
	ls *logState,
	sth SignedTreeHead,
	prev SignedTreeHead,
	hadPrev bool,
) []SignedTreeHead {
	var conflicts []SignedTreeHead
	if hadPrev && prev.TreeSize == sth.TreeSize &&
		!bytes.Equal(prev.RootHash, sth.RootHash) {
		conflicts = append(conflicts, prev)
	}
	for source, other := range ls.Sources {
		if source == sth.Source {
			continue
		}
		if other.TreeSize == sth.TreeSize &&
			!bytes.Equal(other.RootHash, sth.RootHash) {
			conflicts = append(conflicts, other)
		}
	}
	return conflicts
}

func (w *SplitViewWatch) addEvidence(ls *logState, sth SignedTreeHead, reason string) {
	ev := Evidence{
		LogID:     sth.LogID,
		TreeSize:  sth.TreeSize,
		RootHash:  cloneBytes(sth.RootHash),
		Source:    sth.Source,
		Timestamp: sth.Timestamp,
		Reason:    reason,
	}
	for _, existing := range ls.Evidence {
		if sameEvidenceHead(existing, ev) {
			return
		}
	}
	ls.Evidence = append(ls.Evidence, ev)
}

func (w *SplitViewWatch) isReplayLocked(ls *logState, sth SignedTreeHead) bool {
	if prev, ok := ls.Sources[sth.Source]; ok &&
		sameHead(prev, sth) && sth.Timestamp.Before(prev.Timestamp) {
		return true
	}
	for _, ev := range ls.Evidence {
		if ev.Source == sth.Source &&
			ev.TreeSize == sth.TreeSize &&
			bytes.Equal(ev.RootHash, sth.RootHash) &&
			sth.Timestamp.Before(ev.Timestamp) {
			return true
		}
	}
	return false
}

func (w *SplitViewWatch) logsWithSizes(from, to uint64) []*logState {
	ids := make([]string, 0, len(w.logs))
	for logID := range w.logs {
		ids = append(ids, logID)
	}
	sort.Strings(ids)
	out := make([]*logState, 0, len(ids))
	for _, logID := range ids {
		ls := w.logs[logID]
		if _, ok := ls.Sizes[from]; !ok {
			continue
		}
		if _, ok := ls.Sizes[to]; ok {
			out = append(out, ls)
		}
	}
	return out
}

// consistencyPathLen returns the minimum number of aligned binary blocks that
// cover the interval [from, to). Those blocks are exactly the proof nodes
// appended to the from root on the way to the to root.
func consistencyPathLen(from, to uint64) int {
	if from == 0 || from > to {
		return -1
	}
	levels := 0
	cur := from
	for cur < to {
		remaining := to - cur
		maxByRemaining := uint64(1) << (bits.Len64(remaining) - 1)
		alignment := cur & (^cur + 1)
		step := maxByRemaining
		if alignment < step {
			step = alignment
		}
		cur += step
		levels++
	}
	return levels
}

func sameHead(a, b SignedTreeHead) bool {
	return a.LogID == b.LogID &&
		a.Source == b.Source &&
		a.TreeSize == b.TreeSize &&
		bytes.Equal(a.RootHash, b.RootHash)
}

func sameTreeHead(head *TreeHead, sth SignedTreeHead) bool {
	return head != nil &&
		head.LogID == sth.LogID &&
		head.TreeSize == sth.TreeSize &&
		bytes.Equal(head.RootHash, sth.RootHash)
}

func sameEvidenceHead(a, b Evidence) bool {
	return a.LogID == b.LogID &&
		a.TreeSize == b.TreeSize &&
		bytes.Equal(a.RootHash, b.RootHash) &&
		a.Source == b.Source &&
		a.Timestamp.Equal(b.Timestamp)
}

func cloneBytes(in []byte) []byte {
	if in == nil {
		return nil
	}
	out := make([]byte, len(in))
	copy(out, in)
	return out
}

func cloneSTH(in SignedTreeHead) SignedTreeHead {
	in.RootHash = cloneBytes(in.RootHash)
	in.Signature = cloneBytes(in.Signature)
	return in
}

func cloneTreeHead(in TreeHead) TreeHead {
	in.RootHash = cloneBytes(in.RootHash)
	return in
}

func cloneEvidenceSlice(in []Evidence) []Evidence {
	if in == nil {
		return nil
	}
	out := make([]Evidence, len(in))
	for i, ev := range in {
		ev.RootHash = cloneBytes(ev.RootHash)
		out[i] = ev
	}
	return out
}
