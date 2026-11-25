// Package task keeps a fuzz corpus under a byte quota: entries are admitted
// only when they extend coverage, repeated content is deduplicated, and
// eviction is driven by contribution.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Verdict reasons reported for an Add call.
const (
	ReasonAccepted      = "accepted"
	ReasonDuplicate     = "duplicate_content"
	ReasonNoNewCoverage = "no_new_coverage"
	ReasonNoCoverage    = "no_coverage"
	ReasonQuotaFull     = "quota_full"
)

// Errors returned by the vault.
var (
	ErrBlankOrigin = errors.New("origin is required")
	ErrNilReducer  = errors.New("cover reducer is required")
	ErrNotFound    = errors.New("corpus entry not found")
	ErrNotCrash    = errors.New("entry is not a crash case")
	ErrBadSnapshot = errors.New("snapshot payload is invalid")
)

// Reducer derives the coverage of a corpus input. The vault calls it while
// minimising an entry.
type Reducer func(input []byte) CoverSet

// CoverSet is the coverage bitmap of one input: an opaque bitmap hash plus
// the sorted edge identifiers it reached.
type CoverSet struct {
	Hash  string
	Edges []uint64
}

// Limits bounds the corpus.
type Limits struct {
	MaxBytes   int
	MaxEntries int
}

// Entry is one stored corpus input.
type Entry struct {
	ID        string
	Hash      string
	CoverHash string
	Bytes     int
	NewEdges  int
	Origins   []string
	Crash     bool
	Confirmed bool
	Pinned    bool
	Seq       int64
}

// Verdict reports what happened to one Add call.
type Verdict struct {
	ID        string
	Accepted  bool
	Duplicate bool
	NewEdges  int
	Reason    string
	Bytes     int
	Evicted   []string
}

// VaultStats is the running counter block of the vault.
type VaultStats struct {
	Entries    int
	Bytes      int
	Invalid    int
	Duplicates int
	Redundant  int
	Evicted    int
	Crash      int
}

type storedEntry struct {
	Entry
	input      []byte
	coverEdges []uint64
}

type vaultCounters struct {
	invalid    int
	duplicates int
	redundant  int
	evicted    int
}

// CorpusVault owns the corpus entries, the coverage watermark and counters.
type CorpusVault struct {
	mu sync.RWMutex

	limits Limits
	reduce Reducer

	entries map[string]*storedEntry
	order   []*storedEntry
	content map[string]string
	seen    map[uint64]struct{}

	nextSeq    int64
	crashCount int
	counters   vaultCounters
}

// NewCorpusVault builds an empty vault with the given caps and reducer.
func NewCorpusVault(limits Limits, reduce Reducer) *CorpusVault {
	return &CorpusVault{
		limits:  limits,
		reduce:  reduce,
		entries: make(map[string]*storedEntry),
		content: make(map[string]string),
		seen:    make(map[uint64]struct{}),
		nextSeq: 1,
	}
}

// Add admits one corpus input when it brings unseen coverage.
func (v *CorpusVault) Add(input []byte, cov CoverSet, origin string) (Verdict, error) {
	if strings.TrimSpace(origin) == "" {
		return Verdict{}, ErrBlankOrigin
	}
	if v.reduce == nil {
		return Verdict{}, ErrNilReducer
	}

	v.mu.Lock()
	defer v.mu.Unlock()

	hash := contentHash(input)
	if id, ok := v.content[hash]; ok {
		entry := v.entries[id]
		entry.Origins = append(entry.Origins, origin)
		v.counters.duplicates++
		return Verdict{
			ID:        id,
			Duplicate: true,
			Reason:    ReasonDuplicate,
			Bytes:     len(input),
		}, nil
	}

	edges := normalizeEdges(cov.Edges)
	if len(edges) == 0 {
		v.counters.invalid++
		return Verdict{
			Reason: ReasonNoCoverage,
			Bytes:  len(input),
		}, nil
	}

	newEdges := 0
	for _, edge := range edges {
		if _, ok := v.seen[edge]; !ok {
			newEdges++
		}
	}
	if newEdges == 0 {
		v.counters.redundant++
		return Verdict{
			Reason: ReasonNoNewCoverage,
			Bytes:  len(input),
		}, nil
	}

	if v.limits.MaxEntries <= 0 || len(v.entries) >= v.limits.MaxEntries {
		return Verdict{
			Reason: ReasonQuotaFull,
			Bytes:  len(input),
		}, nil
	}

	victims, ok := v.planEvictions(newEdges, len(input))
	if !ok {
		return Verdict{
			Reason: ReasonQuotaFull,
			Bytes:  len(input),
		}, nil
	}

	for _, victim := range victims {
		v.removeLocked(victim)
		v.counters.evicted++
	}

	seq := v.nextSeq
	v.nextSeq++
	id := corpusID(seq)
	entry := &storedEntry{
		Entry: Entry{
			ID:        id,
			Hash:      hash,
			CoverHash: cov.Hash,
			Bytes:     len(input),
			NewEdges:  newEdges,
			Origins:   []string{origin},
			Seq:       seq,
		},
		input:      cloneBytes(input),
		coverEdges: cloneEdges(edges),
	}
	v.entries[id] = entry
	v.order = append(v.order, entry)
	v.content[hash] = id
	for _, edge := range edges {
		v.seen[edge] = struct{}{}
	}

	return Verdict{
		ID:       id,
		Accepted: true,
		NewEdges: newEdges,
		Reason:   ReasonAccepted,
		Bytes:    len(input),
		Evicted:  append([]string(nil), victims...),
	}, nil
}

// Get reads one entry.
func (v *CorpusVault) Get(id string) (Entry, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()

	entry, ok := v.entries[id]
	if !ok {
		return Entry{}, false
	}
	return cloneEntry(entry), true
}

// Entries lists the live entries in admission order.
func (v *CorpusVault) Entries() []Entry {
	v.mu.RLock()
	defer v.mu.RUnlock()

	out := make([]Entry, 0, len(v.order))
	for _, entry := range v.order {
		out = append(out, cloneEntry(entry))
	}
	return out
}

// Minimize shrinks one entry to the shortest prefix that reproduces its
// coverage.
func (v *CorpusVault) Minimize(id string) ([]byte, error) {
	v.mu.RLock()
	entry, ok := v.entries[id]
	if !ok {
		v.mu.RUnlock()
		return nil, ErrNotFound
	}
	reduce := v.reduce
	input := cloneBytes(entry.input)
	targetHash := entry.CoverHash
	targetEdges := cloneEdges(entry.coverEdges)
	v.mu.RUnlock()

	if reduce == nil {
		return nil, ErrNilReducer
	}

	best := cloneBytes(input)
	for length := len(input) - 1; length >= 0; length-- {
		candidate := input[:length]
		got := reduce(candidate)
		if sameCoverage(got, targetHash, targetEdges) {
			best = cloneBytes(candidate)
		}
	}
	return best, nil
}

// Promote pins an entry so eviction skips it.
func (v *CorpusVault) Promote(id string) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	entry, ok := v.entries[id]
	if !ok {
		return ErrNotFound
	}
	entry.Pinned = true
	return nil
}

// MarkCrash flags an entry as a crash case and keeps its origins.
func (v *CorpusVault) MarkCrash(id string) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	entry, ok := v.entries[id]
	if !ok {
		return ErrNotFound
	}
	if !entry.Crash {
		entry.Crash = true
		v.crashCount++
	}
	return nil
}

// ConfirmCrash records the manual confirmation of a crash case.
func (v *CorpusVault) ConfirmCrash(id string) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	entry, ok := v.entries[id]
	if !ok {
		return ErrNotFound
	}
	if !entry.Crash {
		return ErrNotCrash
	}
	entry.Confirmed = true
	return nil
}

// Reap evicts entries until the byte quota is met and returns their ids.
func (v *CorpusVault) Reap() []string {
	v.mu.Lock()
	defer v.mu.Unlock()

	var evicted []string
	for v.totalBytesLocked() > v.limits.MaxBytes {
		victim := v.weakestEvictableLocked()
		if victim == nil {
			break
		}
		evicted = append(evicted, victim.ID)
		v.removeLocked(victim.ID)
		v.counters.evicted++
	}
	return evicted
}

// Stats reads the counter block.
func (v *CorpusVault) Stats() VaultStats {
	v.mu.RLock()
	defer v.mu.RUnlock()

	return VaultStats{
		Entries:    len(v.entries),
		Bytes:      v.totalBytesLocked(),
		Invalid:    v.counters.invalid,
		Duplicates: v.counters.duplicates,
		Redundant:  v.counters.redundant,
		Evicted:    v.counters.evicted,
		Crash:      v.crashCount,
	}
}

const (
	snapshotMagic   = "gsb-corpus-vault"
	snapshotVersion = 1
)

type snapshotEnvelope struct {
	Magic    string          `json:"magic"`
	Version  int             `json:"version"`
	Checksum string          `json:"checksum"`
	Payload  json.RawMessage `json:"payload"`
}

type snapshotPayload struct {
	Version   int              `json:"version"`
	NextSeq   int64            `json:"next_seq"`
	SeenEdges []uint64         `json:"seen_edges"`
	Entries   []snapshotEntry  `json:"entries"`
	Counters  snapshotCounters `json:"counters"`
}

type snapshotEntry struct {
	ID         string   `json:"id"`
	Hash       string   `json:"hash"`
	CoverHash  string   `json:"cover_hash"`
	Bytes      int      `json:"bytes"`
	NewEdges   int      `json:"new_edges"`
	Origins    []string `json:"origins"`
	Crash      bool     `json:"crash"`
	Confirmed  bool     `json:"confirmed"`
	Pinned     bool     `json:"pinned"`
	Seq        int64    `json:"seq"`
	Input      []byte   `json:"input"`
	CoverEdges []uint64 `json:"cover_edges"`
}

type snapshotCounters struct {
	Entries    int `json:"entries"`
	Bytes      int `json:"bytes"`
	Invalid    int `json:"invalid"`
	Duplicates int `json:"duplicates"`
	Redundant  int `json:"redundant"`
	Evicted    int `json:"evicted"`
	Crash      int `json:"crash"`
}

// Snapshot serialises entries, origins, the coverage watermark and counters.
func (v *CorpusVault) Snapshot() ([]byte, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	seen := make([]uint64, 0, len(v.seen))
	for edge := range v.seen {
		seen = append(seen, edge)
	}
	sort.Slice(seen, func(i, j int) bool { return seen[i] < seen[j] })

	entries := make([]snapshotEntry, 0, len(v.order))
	for _, entry := range v.order {
		entries = append(entries, snapshotEntry{
			ID:         entry.ID,
			Hash:       entry.Hash,
			CoverHash:  entry.CoverHash,
			Bytes:      entry.Bytes,
			NewEdges:   entry.NewEdges,
			Origins:    append([]string(nil), entry.Origins...),
			Crash:      entry.Crash,
			Confirmed:  entry.Confirmed,
			Pinned:     entry.Pinned,
			Seq:        entry.Seq,
			Input:      cloneBytes(entry.input),
			CoverEdges: cloneEdges(entry.coverEdges),
		})
	}

	payload := snapshotPayload{
		Version:   snapshotVersion,
		NextSeq:   v.nextSeq,
		SeenEdges: seen,
		Entries:   entries,
		Counters: snapshotCounters{
			Entries:    len(v.entries),
			Bytes:      v.totalBytesLocked(),
			Invalid:    v.counters.invalid,
			Duplicates: v.counters.duplicates,
			Redundant:  v.counters.redundant,
			Evicted:    v.counters.evicted,
			Crash:      v.crashCount,
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	return json.Marshal(snapshotEnvelope{
		Magic:    snapshotMagic,
		Version:  snapshotVersion,
		Checksum: hex.EncodeToString(sum[:]),
		Payload:  body,
	})
}

// Restore replaces the whole vault state with a snapshot, all or nothing.
func (v *CorpusVault) Restore(data []byte) error {
	if len(data) == 0 {
		return ErrBadSnapshot
	}

	var envelope snapshotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return ErrBadSnapshot
	}
	if envelope.Magic != snapshotMagic || envelope.Version != snapshotVersion {
		return ErrBadSnapshot
	}
	sum := sha256.Sum256(envelope.Payload)
	if envelope.Checksum != hex.EncodeToString(sum[:]) {
		return ErrBadSnapshot
	}

	var payload snapshotPayload
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return ErrBadSnapshot
	}
	if payload.Version != snapshotVersion || payload.NextSeq < 1 {
		return ErrBadSnapshot
	}
	if payload.Counters.Invalid < 0 || payload.Counters.Duplicates < 0 ||
		payload.Counters.Redundant < 0 || payload.Counters.Evicted < 0 ||
		payload.Counters.Crash < 0 {
		return ErrBadSnapshot
	}
	if !sort.SliceIsSorted(payload.SeenEdges, func(i, j int) bool {
		return payload.SeenEdges[i] < payload.SeenEdges[j]
	}) || hasDuplicateEdges(payload.SeenEdges) {
		return ErrBadSnapshot
	}

	entries := make(map[string]*storedEntry, len(payload.Entries))
	content := make(map[string]string, len(payload.Entries))
	seen := make(map[uint64]struct{}, len(payload.SeenEdges))
	for _, edge := range payload.SeenEdges {
		seen[edge] = struct{}{}
	}

	var (
		totalBytes int
		crashCount int
		maxSeq     int64
	)
	for _, saved := range payload.Entries {
		if saved.Seq < 1 || saved.Bytes != len(saved.Input) ||
			saved.NewEdges < 1 || saved.NewEdges > len(saved.CoverEdges) ||
			saved.ID != corpusID(saved.Seq) || len(saved.Origins) == 0 ||
			saved.Confirmed && !saved.Crash {
			return ErrBadSnapshot
		}
		if contentHash(saved.Input) != saved.Hash {
			return ErrBadSnapshot
		}
		if _, exists := entries[saved.ID]; exists {
			return ErrBadSnapshot
		}
		if _, exists := content[saved.Hash]; exists {
			return ErrBadSnapshot
		}
		edges := normalizeEdges(saved.CoverEdges)
		if !sameEdges(edges, saved.CoverEdges) || len(edges) == 0 {
			return ErrBadSnapshot
		}
		for _, edge := range edges {
			if _, ok := seen[edge]; !ok {
				return ErrBadSnapshot
			}
		}
		for _, origin := range saved.Origins {
			if strings.TrimSpace(origin) == "" {
				return ErrBadSnapshot
			}
		}

		entry := &storedEntry{
			Entry: Entry{
				ID:        saved.ID,
				Hash:      saved.Hash,
				CoverHash: saved.CoverHash,
				Bytes:     saved.Bytes,
				NewEdges:  saved.NewEdges,
				Origins:   append([]string(nil), saved.Origins...),
				Crash:     saved.Crash,
				Confirmed: saved.Confirmed,
				Pinned:    saved.Pinned,
				Seq:       saved.Seq,
			},
			input:      cloneBytes(saved.Input),
			coverEdges: cloneEdges(edges),
		}
		entries[entry.ID] = entry
		content[entry.Hash] = entry.ID
		totalBytes += entry.Bytes
		if entry.Crash {
			crashCount++
		}
		if entry.Seq > maxSeq {
			maxSeq = entry.Seq
		}
	}

	if payload.Counters.Entries != len(entries) ||
		payload.Counters.Bytes != totalBytes ||
		payload.Counters.Crash != crashCount ||
		payload.NextSeq <= maxSeq {
		return ErrBadSnapshot
	}

	order := make([]*storedEntry, 0, len(entries))
	for _, entry := range entries {
		order = append(order, entry)
	}
	sort.Slice(order, func(i, j int) bool { return order[i].Seq < order[j].Seq })

	v.mu.Lock()
	defer v.mu.Unlock()

	nextSeq := payload.NextSeq
	if nextSeq < v.nextSeq {
		nextSeq = v.nextSeq
	}
	if nextSeq <= maxSeq {
		nextSeq = maxSeq + 1
	}

	v.entries = entries
	v.order = order
	v.content = content
	v.seen = seen
	v.nextSeq = nextSeq
	v.crashCount = crashCount
	v.counters = vaultCounters{
		invalid:    payload.Counters.Invalid,
		duplicates: payload.Counters.Duplicates,
		redundant:  payload.Counters.Redundant,
		evicted:    payload.Counters.Evicted,
	}
	return nil
}

func (v *CorpusVault) planEvictions(newEdges, newBytes int) ([]string, bool) {
	excess := v.totalBytesLocked() + newBytes - v.limits.MaxBytes
	if excess <= 0 {
		return nil, true
	}

	candidates := v.evictableLocked()
	sort.Slice(candidates, func(i, j int) bool {
		return contributionLess(candidates[i], candidates[j])
	})

	var (
		victims []string
		freed   int
	)
	for _, candidate := range candidates {
		if compareContribution(newEdges, newBytes, candidate.NewEdges, candidate.Bytes) < 0 {
			return nil, false
		}
		victims = append(victims, candidate.ID)
		freed += candidate.Bytes
		if freed >= excess {
			return victims, true
		}
	}
	return nil, false
}

func (v *CorpusVault) evictableLocked() []*storedEntry {
	out := make([]*storedEntry, 0, len(v.order))
	for _, entry := range v.order {
		if entry.Pinned || entry.Crash && !entry.Confirmed {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func (v *CorpusVault) weakestEvictableLocked() *storedEntry {
	var weakest *storedEntry
	for _, entry := range v.order {
		if entry.Pinned || entry.Crash && !entry.Confirmed {
			continue
		}
		if weakest == nil || contributionLess(entry, weakest) {
			weakest = entry
		}
	}
	return weakest
}

func (v *CorpusVault) removeLocked(id string) {
	entry, ok := v.entries[id]
	if !ok {
		return
	}
	delete(v.entries, id)
	delete(v.content, entry.Hash)
	if entry.Crash {
		v.crashCount--
	}
	for i, candidate := range v.order {
		if candidate.ID == id {
			v.order = append(v.order[:i], v.order[i+1:]...)
			break
		}
	}
}

func (v *CorpusVault) totalBytesLocked() int {
	total := 0
	for _, entry := range v.entries {
		total += entry.Bytes
	}
	return total
}

func contributionLess(a, b *storedEntry) bool {
	comparison := compareContribution(a.NewEdges, a.Bytes, b.NewEdges, b.Bytes)
	if comparison != 0 {
		return comparison < 0
	}
	return a.Seq < b.Seq
}

func compareContribution(aEdges, aBytes, bEdges, bBytes int) int {
	switch {
	case aBytes == 0 && bBytes == 0:
		return compareInt(aEdges, bEdges)
	case aBytes == 0:
		return 1
	case bBytes == 0:
		return -1
	}

	left := int64(aEdges) * int64(bBytes)
	right := int64(bEdges) * int64(aBytes)
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}

func compareInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func corpusID(seq int64) string {
	return fmt.Sprintf("corpus-%04d", seq)
}

func contentHash(input []byte) string {
	sum := sha256.Sum256(input)
	return hex.EncodeToString(sum[:])
}

func normalizeEdges(edges []uint64) []uint64 {
	out := cloneEdges(edges)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	if len(out) < 2 {
		return out
	}
	write := 1
	for read := 1; read < len(out); read++ {
		if out[read] == out[write-1] {
			continue
		}
		out[write] = out[read]
		write++
	}
	return out[:write]
}

func sameCoverage(got CoverSet, targetHash string, targetEdges []uint64) bool {
	return got.Hash == targetHash && sameEdges(normalizeEdges(got.Edges), targetEdges)
}

func sameEdges(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func hasDuplicateEdges(edges []uint64) bool {
	for i := 1; i < len(edges); i++ {
		if edges[i] == edges[i-1] {
			return true
		}
	}
	return false
}

func cloneEntry(entry *storedEntry) Entry {
	out := entry.Entry
	out.Origins = append([]string(nil), entry.Origins...)
	return out
}

func cloneBytes(input []byte) []byte {
	return append([]byte(nil), input...)
}

func cloneEdges(edges []uint64) []uint64 {
	return append([]uint64(nil), edges...)
}
