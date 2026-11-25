// Package task keeps vulnerability findings identical across scan rounds:
// drift tolerant identity resolution, a fix/reopen state machine and
// reversible merging.
package task

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Finding states.
const (
	StateNew      = "new"
	StateOpen     = "open"
	StateFixed    = "fixed"
	StateReopened = "reopened"
	StateIgnored  = "ignored"
)

// Event kinds appended to the audit log.
const (
	KindNew      = "new"
	KindSeen     = "seen"
	KindFixed    = "fixed"
	KindReopened = "reopened"
	KindMerged   = "merged"
	KindSplit    = "split"
	KindIgnored  = "ignored"
)

// Errors returned by the ledger.
var (
	ErrBlankScanID = errors.New("scan id is required")
	ErrBadRaw      = errors.New("raw finding is invalid")
	ErrNoMatch     = errors.New("no finding matches the raw finding")
	ErrNotFound    = errors.New("finding not found")
	ErrBadSplit    = errors.New("split request is invalid")
	ErrBadSnapshot = errors.New("snapshot payload is invalid")
)

// snapshotVersion tags the serialised payload so unrelated JSON is rejected.
const snapshotVersion = 1

// RawFinding is one finding as reported by a scanner.
type RawFinding struct {
	Source      string
	RuleID      string
	Path        string
	Line        int
	Fingerprint string
	Message     string
	Severity    string
}

// Finding is one identity that survives across scans.
type Finding struct {
	ID          string
	RuleID      string
	Path        string
	Line        int
	Fingerprint string
	State       string
	Origins     []string
	Seen        int
	ReopenCount int
	Seq         int64
	FirstScan   string
	LastScan    string
}

// Event is one state transition of one finding.
type Event struct {
	ScanID    string
	FindingID string
	Kind      string
	From      string
	To        string
	Source    string
	Seq       int64
}

// Config tunes identity resolution.
type Config struct {
	LineDrift        int
	FingerprintDrift int
}

// Stats is the counter block of the ledger.
type Stats struct {
	New     int
	Open    int
	Fixed   int
	Ignored int
}

// record is the stored Finding plus per-source report counters.
type record struct {
	Finding
	SourceCounts map[string]int `json:"source_counts,omitempty"`
}

// snapshotData is the wholesale serialised state of a ledger.
type snapshotData struct {
	Version int       `json:"version"`
	Rounds  int       `json:"rounds"`
	Seq     int64     `json:"seq"`
	Events  []Event   `json:"events"`
	Records []*record `json:"records"`
}

// FindingIdentityLedger owns findings, their sources and the event log.
type FindingIdentityLedger struct {
	cfg     Config
	mu      sync.RWMutex
	seq     int64
	rounds  int
	records map[string]*record
	events  []Event
}

// NewFindingIdentityLedger builds an empty ledger.
func NewFindingIdentityLedger(cfg Config) *FindingIdentityLedger {
	return &FindingIdentityLedger{
		cfg:     cfg,
		records: make(map[string]*record),
	}
}

// Identity joins rule, path, line and semantic fingerprint into the raw key.
// The scanner source deliberately does not participate.
func (l *FindingIdentityLedger) Identity(raw RawFinding) string {
	return strings.Join([]string{
		raw.RuleID,
		raw.Path,
		strconv.Itoa(raw.Line),
		raw.Fingerprint,
	}, "|")
}

// Resolve maps a raw finding onto a stored finding without mutating state.
func (l *FindingIdentityLedger) Resolve(raw RawFinding) (Finding, error) {
	if !validRaw(raw) {
		return Finding{}, ErrBadRaw
	}
	l.mu.RLock()
	defer l.mu.RUnlock()

	rec := l.matchLocked(raw)
	if rec == nil {
		return Finding{}, ErrNoMatch
	}
	return cloneFinding(rec.Finding), nil
}

// Ingest applies one whole scan round and returns the events it produced.
func (l *FindingIdentityLedger) Ingest(scanID string, raw []RawFinding) ([]Event, error) {
	if strings.TrimSpace(scanID) == "" {
		return nil, ErrBlankScanID
	}
	for _, item := range raw {
		if !validRaw(item) {
			return nil, ErrBadRaw
		}
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	produced := make([]Event, 0, len(raw))
	seenInRound := make(map[string]bool)
	sourcesInRound := make(map[string]map[string]struct{})

	for _, item := range raw {
		rec := l.matchLocked(item)

		if rec == nil {
			rec = l.newRecordLocked(item, scanID)
			l.records[rec.ID] = rec
			seenInRound[rec.ID] = true
			sourcesInRound[rec.ID] = map[string]struct{}{item.Source: {}}
			l.trackSourceLocked(rec, item.Source)
			ev := l.appendLocked(Event{
				ScanID:    scanID,
				FindingID: rec.ID,
				Kind:      KindNew,
				To:        StateNew,
				Source:    item.Source,
			})
			produced = append(produced, ev)
			continue
		}

		// Ignored entries stay completely untouched and emit nothing.
		if rec.State == StateIgnored {
			continue
		}

		batchSources := sourcesInRound[rec.ID]
		if batchSources == nil {
			batchSources = make(map[string]struct{})
			sourcesInRound[rec.ID] = batchSources
		}
		_, sourceAlreadyInBatch := batchSources[item.Source]
		batchSources[item.Source] = struct{}{}
		l.trackSourceLocked(rec, item.Source)

		if !seenInRound[rec.ID] {
			// First active report for this identity this round owns the
			// new/seen/reopened lifecycle transition.
			seenInRound[rec.ID] = true
			rec.Seen++
			rec.LastScan = scanID
			// Rebinding follows parser drift: remember where the defect sits.
			rec.Line = item.Line
			rec.Fingerprint = item.Fingerprint

			var ev Event
			switch rec.State {
			case StateFixed:
				from := StateFixed
				rec.State = StateReopened
				rec.ReopenCount++
				ev = Event{
					ScanID:    scanID,
					FindingID: rec.ID,
					Kind:      KindReopened,
					From:      from,
					To:        StateReopened,
					Source:    item.Source,
				}
			case StateNew:
				rec.State = StateOpen
				ev = Event{
					ScanID:    scanID,
					FindingID: rec.ID,
					Kind:      KindSeen,
					From:      StateNew,
					To:        StateOpen,
					Source:    item.Source,
				}
			default: // StateOpen or StateReopened
				from := rec.State
				ev = Event{
					ScanID:    scanID,
					FindingID: rec.ID,
					Kind:      KindSeen,
					From:      from,
					To:        from,
					Source:    item.Source,
				}
			}
			produced = append(produced, l.appendLocked(ev))
			continue
		}

		// A later report for the same identity from a scanner source not yet
		// represented in this round is a source convergence.
		if !sourceAlreadyInBatch {
			ev := l.appendLocked(Event{
				ScanID:    scanID,
				FindingID: rec.ID,
				Kind:      KindMerged,
				From:      rec.State,
				To:        rec.State,
				Source:    item.Source,
			})
			produced = append(produced, ev)
		}
	}

	// End of round: active entries that were not reported become fixed.
	for _, id := range l.sortedIDsLocked() {
		rec := l.records[id]
		switch rec.State {
		case StateNew, StateOpen, StateReopened:
		default:
			continue
		}
		if seenInRound[id] {
			continue
		}
		from := rec.State
		rec.State = StateFixed
		ev := l.appendLocked(Event{
			ScanID:    scanID,
			FindingID: id,
			Kind:      KindFixed,
			From:      from,
			To:        StateFixed,
		})
		produced = append(produced, ev)
	}

	l.rounds++
	return produced, nil
}

// Split peels a subset of sources off one finding. The keep set names the
// sources carried by the new split instance; the original keeps the rest.
func (l *FindingIdentityLedger) Split(findingID string, keep []string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	parent, ok := l.records[findingID]
	if !ok {
		return ErrNotFound
	}

	keepSet := make(map[string]struct{})
	for _, source := range keep {
		if !hasOrigin(parent.Origins, source) {
			return ErrBadSplit
		}
		keepSet[source] = struct{}{}
	}
	if len(keepSet) == 0 || len(keepSet) >= len(parent.Origins) {
		return ErrBadSplit
	}

	peeled := make([]string, 0, len(keepSet))
	for source := range keepSet {
		peeled = append(peeled, source)
	}
	sort.Strings(peeled)

	remaining := make([]string, 0, len(parent.Origins)-len(peeled))
	for _, source := range parent.Origins {
		if _, peel := keepSet[source]; !peel {
			remaining = append(remaining, source)
		}
	}

	childID := l.splitIDLocked(findingID, peeled)
	child := &record{
		Finding: Finding{
			ID:          childID,
			RuleID:      parent.RuleID,
			Path:        parent.Path,
			Line:        parent.Line,
			Fingerprint: parent.Fingerprint,
			State:       parent.State,
			Origins:     append([]string(nil), peeled...),
			Seen:        parent.Seen,
			ReopenCount: parent.ReopenCount,
			FirstScan:   parent.FirstScan,
			LastScan:    parent.LastScan,
		},
		SourceCounts: make(map[string]int, len(peeled)),
	}
	for _, source := range peeled {
		if count, ok := parent.SourceCounts[source]; ok {
			child.SourceCounts[source] = count
			delete(parent.SourceCounts, source)
		}
	}
	parent.Origins = remaining

	// Move source-attributable history onto the matching instance instead of
	// copying the whole log onto both. Source-agnostic events stay put.
	for i := range l.events {
		ev := &l.events[i]
		if ev.FindingID != findingID || ev.Source == "" {
			continue
		}
		if _, peel := keepSet[ev.Source]; peel {
			ev.FindingID = childID
		}
	}

	l.records[childID] = child
	l.appendLocked(Event{
		FindingID: findingID,
		Kind:      KindSplit,
		From:      parent.State,
		To:        parent.State,
	})
	return nil
}

// Ignore silences one finding for the following rounds.
func (l *FindingIdentityLedger) Ignore(findingID string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	rec, ok := l.records[findingID]
	if !ok {
		return ErrNotFound
	}
	if rec.State == StateIgnored {
		return nil
	}
	from := rec.State
	rec.State = StateIgnored
	l.appendLocked(Event{
		FindingID: findingID,
		Kind:      KindIgnored,
		From:      from,
		To:        StateIgnored,
	})
	return nil
}

// Get reads one finding.
func (l *FindingIdentityLedger) Get(id string) (Finding, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	rec, ok := l.records[id]
	if !ok {
		return Finding{}, false
	}
	return cloneFinding(rec.Finding), true
}

// Findings lists every finding ordered by id.
func (l *FindingIdentityLedger) Findings() []Finding {
	l.mu.RLock()
	defer l.mu.RUnlock()

	ids := l.sortedIDsLocked()
	out := make([]Finding, 0, len(ids))
	for _, id := range ids {
		out = append(out, cloneFinding(l.records[id].Finding))
	}
	return out
}

// Events lists the audit log in append order.
func (l *FindingIdentityLedger) Events() []Event {
	l.mu.RLock()
	defer l.mu.RUnlock()

	out := make([]Event, len(l.events))
	copy(out, l.events)
	return out
}

// Stats reads the counter block. Reopened work is still open work.
func (l *FindingIdentityLedger) Stats() Stats {
	l.mu.RLock()
	defer l.mu.RUnlock()

	var stats Stats
	for _, rec := range l.records {
		switch rec.State {
		case StateNew:
			stats.New++
		case StateOpen, StateReopened:
			stats.Open++
		case StateFixed:
			stats.Fixed++
		case StateIgnored:
			stats.Ignored++
		}
	}
	return stats
}

// Rounds reports how many scan rounds the ledger has applied.
func (l *FindingIdentityLedger) Rounds() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.rounds
}

// Snapshot serialises findings, origins, events and the round counter.
func (l *FindingIdentityLedger) Snapshot() ([]byte, error) {
	l.mu.RLock()
	snap := snapshotData{
		Version: snapshotVersion,
		Rounds:  l.rounds,
		Seq:     l.seq,
		Events:  make([]Event, len(l.events)),
		Records: make([]*record, 0, len(l.records)),
	}
	copy(snap.Events, l.events)
	for _, id := range l.sortedIDsLocked() {
		rec := l.records[id]
		cp := *rec
		cp.Origins = append([]string(nil), rec.Origins...)
		if rec.SourceCounts != nil {
			cp.SourceCounts = make(map[string]int, len(rec.SourceCounts))
			for source, count := range rec.SourceCounts {
				cp.SourceCounts[source] = count
			}
		}
		snap.Records = append(snap.Records, &cp)
	}
	l.mu.RUnlock()

	data, err := json.Marshal(snap)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Restore replaces the whole ledger state with a snapshot, all or nothing.
func (l *FindingIdentityLedger) Restore(data []byte) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return ErrBadSnapshot
	}

	var snap snapshotData
	if err := json.Unmarshal(data, &snap); err != nil {
		return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	if err := validateSnapshot(&snap); err != nil {
		return err
	}

	records := make(map[string]*record, len(snap.Records))
	for _, saved := range snap.Records {
		cp := *saved
		// A brand-new announcement has been durably checkpointed; after a
		// restart it is known active work rather than a transient "new" badge.
		if cp.State == StateNew {
			cp.State = StateOpen
		}
		cp.Origins = normalizeOrigins(cp.Origins)
		if saved.SourceCounts != nil {
			cp.SourceCounts = make(map[string]int, len(saved.SourceCounts))
			for source, count := range saved.SourceCounts {
				cp.SourceCounts[source] = count
			}
		} else {
			cp.SourceCounts = make(map[string]int)
		}
		records[cp.ID] = &cp
	}
	events := make([]Event, len(snap.Events))
	copy(events, snap.Events)

	l.mu.Lock()
	l.records = records
	l.events = events
	l.seq = snap.Seq
	l.rounds = snap.Rounds
	l.mu.Unlock()
	return nil
}

// matchLocked resolves a raw finding by priority: exact tuple, same
// rule/path/fingerprint within line drift, then a conservative fingerprint
// edit-distance rebind. Callers must hold the read or write lock.
func (l *FindingIdentityLedger) matchLocked(raw RawFinding) *record {
	ids := l.sortedIDsLocked()

	var exact, exactOwned *record
	for _, id := range ids {
		rec := l.records[id]
		if rec.RuleID == raw.RuleID && rec.Path == raw.Path &&
			rec.Fingerprint == raw.Fingerprint && rec.Line == raw.Line {
			if exact == nil {
				exact = rec
			}
			if exactOwned == nil && hasOrigin(rec.Origins, raw.Source) {
				exactOwned = rec
			}
		}
	}
	if exactOwned != nil {
		return exactOwned
	}
	if exact != nil {
		return exact
	}

	lineTolerance := l.cfg.LineDrift

	var drift *record
	driftDist := 0
	driftOwned := false
	for _, id := range ids {
		rec := l.records[id]
		if rec.RuleID != raw.RuleID || rec.Path != raw.Path ||
			rec.Fingerprint != raw.Fingerprint {
			continue
		}
		dist := absInt(rec.Line - raw.Line)
		if dist > lineTolerance {
			continue
		}
		owned := hasOrigin(rec.Origins, raw.Source)
		if drift == nil ||
			dist < driftDist ||
			(dist == driftDist && owned && !driftOwned) ||
			(dist == driftDist && owned == driftOwned && rec.ID < drift.ID) {
			drift = rec
			driftDist = dist
			driftOwned = owned
		}
	}
	if drift != nil {
		return drift
	}

	fpTolerance := l.cfg.FingerprintDrift
	var fuzzy *record
	fuzzyLine, fuzzyFP := 0, 0
	fuzzyOwned := false
	for _, id := range ids {
		rec := l.records[id]
		// Fingerprint closeness alone never merges: same rule, same path and
		// close lines are all required before the fallback rebind.
		if rec.RuleID != raw.RuleID || rec.Path != raw.Path {
			continue
		}
		lineDist := absInt(rec.Line - raw.Line)
		if lineDist > lineTolerance {
			continue
		}
		fpDist := editDistance(rec.Fingerprint, raw.Fingerprint)
		if fpDist > fpTolerance {
			continue
		}
		owned := hasOrigin(rec.Origins, raw.Source)
		better := fuzzy == nil ||
			lineDist < fuzzyLine ||
			(lineDist == fuzzyLine && fpDist < fuzzyFP) ||
			(lineDist == fuzzyLine && fpDist == fuzzyFP && owned && !fuzzyOwned) ||
			(lineDist == fuzzyLine && fpDist == fuzzyFP && owned == fuzzyOwned && rec.ID < fuzzy.ID)
		if better {
			fuzzy = rec
			fuzzyLine = lineDist
			fuzzyFP = fpDist
			fuzzyOwned = owned
		}
	}
	return fuzzy
}

// newRecordLocked builds an entry whose id is derived only from the raw
// identity, so repeating the same raw record always reaches the same id.
func (l *FindingIdentityLedger) newRecordLocked(raw RawFinding, scanID string) *record {
	identity := l.Identity(raw)
	id := findingIDForIdentity(identity)
	if _, exists := l.records[id]; exists {
		for nonce := 1; ; nonce++ {
			h := fnv.New32a()
			_, _ = h.Write([]byte(fmt.Sprintf("%s#%d", identity, nonce)))
			candidate := fmt.Sprintf("fi-%08x", h.Sum32())
			if _, taken := l.records[candidate]; !taken {
				id = candidate
				break
			}
		}
	}
	return &record{
		Finding: Finding{
			ID:          id,
			RuleID:      raw.RuleID,
			Path:        raw.Path,
			Line:        raw.Line,
			Fingerprint: raw.Fingerprint,
			State:       StateNew,
			Origins:     []string{},
			Seen:        1,
			FirstScan:   scanID,
			LastScan:    scanID,
		},
		SourceCounts: make(map[string]int),
	}
}

// appendLocked assigns the next unique sequence number and records the event.
func (l *FindingIdentityLedger) appendLocked(ev Event) Event {
	l.seq++
	ev.Seq = l.seq
	l.events = append(l.events, ev)
	if rec, ok := l.records[ev.FindingID]; ok {
		rec.Seq = ev.Seq
	}
	return ev
}

// trackSourceLocked records one per-source report and keeps origins sorted
// and deduplicated.
func (l *FindingIdentityLedger) trackSourceLocked(rec *record, source string) {
	if rec.SourceCounts == nil {
		rec.SourceCounts = make(map[string]int)
	}
	rec.SourceCounts[source]++

	pos := sort.SearchStrings(rec.Origins, source)
	if pos < len(rec.Origins) && rec.Origins[pos] == source {
		return
	}
	rec.Origins = append(rec.Origins, "")
	copy(rec.Origins[pos+1:], rec.Origins[pos:])
	rec.Origins[pos] = source
}

func (l *FindingIdentityLedger) splitIDLocked(parentID string, peeled []string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.Join(peeled, ",")))
	base := int(h.Sum32() & 0xffff)
	for nonce := 0; nonce < 0x10000; nonce++ {
		candidate := fmt.Sprintf("%s-%04x", parentID, (base+nonce)&0xffff)
		if _, exists := l.records[candidate]; !exists {
			return candidate
		}
	}
	return fmt.Sprintf("%s-%04x", parentID, int(l.seq)&0xffff)
}

func (l *FindingIdentityLedger) sortedIDsLocked() []string {
	ids := make([]string, 0, len(l.records))
	for id := range l.records {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func validRaw(raw RawFinding) bool {
	return strings.TrimSpace(raw.Source) != "" &&
		strings.TrimSpace(raw.RuleID) != "" &&
		strings.TrimSpace(raw.Path) != "" &&
		strings.TrimSpace(raw.Fingerprint) != "" &&
		raw.Line >= 1
}

func findingIDForIdentity(identity string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(identity))
	return fmt.Sprintf("fi-%08x", h.Sum32())
}

func hasOrigin(origins []string, source string) bool {
	pos := sort.SearchStrings(origins, source)
	return pos < len(origins) && origins[pos] == source
}

func normalizeOrigins(in []string) []string {
	if len(in) == 0 {
		return []string{}
	}
	out := append([]string(nil), in...)
	sort.Strings(out)
	write := 1
	for read := 1; read < len(out); read++ {
		if out[read] != out[write-1] {
			out[write] = out[read]
			write++
		}
	}
	return out[:write]
}

func cloneFinding(f Finding) Finding {
	if f.Origins != nil {
		f.Origins = append([]string(nil), f.Origins...)
	}
	return f
}

func validateSnapshot(snap *snapshotData) error {
	if snap.Version != snapshotVersion {
		return fmt.Errorf("%w: unsupported version", ErrBadSnapshot)
	}
	if snap.Rounds < 0 || snap.Seq < 0 {
		return fmt.Errorf("%w: negative counters", ErrBadSnapshot)
	}

	ids := make(map[string]struct{}, len(snap.Records))
	for _, rec := range snap.Records {
		if rec == nil || rec.ID == "" || rec.RuleID == "" ||
			rec.Path == "" || rec.Fingerprint == "" {
			return fmt.Errorf("%w: malformed record", ErrBadSnapshot)
		}
		switch rec.State {
		case StateNew, StateOpen, StateFixed, StateReopened, StateIgnored:
		default:
			return fmt.Errorf("%w: unknown state %q", ErrBadSnapshot, rec.State)
		}
		if _, dup := ids[rec.ID]; dup {
			return fmt.Errorf("%w: duplicate finding id", ErrBadSnapshot)
		}
		ids[rec.ID] = struct{}{}
	}

	seqs := make(map[int64]struct{}, len(snap.Events))
	var maxSeq int64
	for _, ev := range snap.Events {
		if ev.Seq <= 0 {
			return fmt.Errorf("%w: event without sequence", ErrBadSnapshot)
		}
		if _, dup := seqs[ev.Seq]; dup {
			return fmt.Errorf("%w: duplicate event sequence", ErrBadSnapshot)
		}
		seqs[ev.Seq] = struct{}{}
		if ev.Seq > maxSeq {
			maxSeq = ev.Seq
		}
	}
	if maxSeq > snap.Seq {
		return fmt.Errorf("%w: sequence counter is behind events", ErrBadSnapshot)
	}
	return nil
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

// editDistance is the Levenshtein distance over runes.
func editDistance(a, b string) int {
	left := []rune(a)
	right := []rune(b)
	if len(left) == 0 {
		return len(right)
	}
	if len(right) == 0 {
		return len(left)
	}
	previous := make([]int, len(right)+1)
	current := make([]int, len(right)+1)
	for j := range previous {
		previous[j] = j
	}
	for i := 1; i <= len(left); i++ {
		current[0] = i
		for j := 1; j <= len(right); j++ {
			cost := 1
			if left[i-1] == right[j-1] {
				cost = 0
			}
			current[j] = min(previous[j]+1, current[j-1]+1, previous[j-1]+cost)
		}
		previous, current = current, previous
	}
	return previous[len(right)]
}
