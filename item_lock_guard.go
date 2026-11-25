// Package task guards bag item locks coming from several sources and previews
// batch discards before executing them.
package task

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
)

// Lock sources recorded for one item.
const (
	LockPlayer   = "player"
	LockTimed    = "timed"
	LockEquipped = "equipped"
	LockEscrow   = "escrow"
	LockQuest    = "quest"
)

// snapshotVersion labels the snapshot schema produced by Snapshot.
const snapshotVersion = 1

// Errors returned by the lock guard.
var (
	ErrBlankArg    = errors.New("argument is required")
	ErrUnknownItem = errors.New("item is not in the bag")
	ErrBadItem     = errors.New("item is invalid or already present")
	ErrBadReason   = errors.New("lock source is not supported")
	ErrBadDeadline = errors.New("lock deadline does not match the source")
	ErrLockLimit   = errors.New("lock source limit reached for this item")
	ErrNotLocked   = errors.New("lock source is not present")
	ErrBadBatch    = errors.New("discard batch is empty, duplicated or too large")
	ErrBadSnapshot = errors.New("snapshot payload is invalid")
)

// Options bounds the lock tables.
type Options struct {
	MaxLocksPerItem int
	MaxBatch        int
}

// BagItem is one item held by an owner.
type BagItem struct {
	UID    string `json:"uid"`
	ItemID string `json:"item_id"`
	Qty    int    `json:"qty"`
}

// LockEntry is one recorded lock source of an item.
type LockEntry struct {
	UID    string `json:"uid"`
	Reason string `json:"reason"`
	Until  int64  `json:"until"`
	Seq    int64  `json:"seq"`
}

// LockState is the effective lock view of one item at a given moment.
type LockState struct {
	UID     string   `json:"uid"`
	Locked  bool     `json:"locked"`
	Reasons []string `json:"reasons"`
}

// Rejection explains why one item of a discard batch is blocked.
type Rejection struct {
	UID    string `json:"uid"`
	Reason string `json:"reason"`
}

// DiscardResult reports the outcome of one batch discard.
type DiscardResult struct {
	Removed  []string    `json:"removed"`
	Rejected []Rejection `json:"rejected"`
}

// ItemLockGuard owns the bag, the lock records and the sequence counter.
// A single mutex serialises every operation, which is stricter than the
// required per-owner serialisation and keeps multi-map updates atomic.
type ItemLockGuard struct {
	mu      sync.Mutex
	options Options
	seq     int64
	owners  map[string]*ownerState
}

// ownerState is one owner bag together with its per-item lock record lists.
type ownerState struct {
	items map[string]BagItem
	locks map[string][]LockEntry
}

// NewItemLockGuard builds a guard with the given caps.
func NewItemLockGuard(options Options) *ItemLockGuard {
	return &ItemLockGuard{
		options: options,
		owners:  make(map[string]*ownerState),
	}
}

func isBlank(value string) bool {
	return strings.TrimSpace(value) == ""
}

func isKnownReason(reason string) bool {
	switch reason {
	case LockPlayer, LockTimed, LockEquipped, LockEscrow, LockQuest:
		return true
	default:
		return false
	}
}

// deadlineMatchesSource enforces positive deadlines for timed locks and a
// zero deadline for the four permanent/system sources.
func deadlineMatchesSource(reason string, until int64) bool {
	if reason == LockTimed {
		return until > 0
	}
	return until == 0
}

func (g *ItemLockGuard) ensureOwnerLocked(owner string) *ownerState {
	state := g.owners[owner]
	if state == nil {
		state = &ownerState{
			items: make(map[string]BagItem),
			locks: make(map[string][]LockEntry),
		}
		g.owners[owner] = state
	}
	return state
}

// AddItem puts one item into an owner bag.
func (g *ItemLockGuard) AddItem(owner string, item BagItem) error {
	if isBlank(owner) || isBlank(item.UID) {
		return ErrBlankArg
	}
	if item.Qty <= 0 {
		return ErrBadItem
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	state := g.ensureOwnerLocked(owner)
	if _, exists := state.items[item.UID]; exists {
		return ErrBadItem
	}
	state.items[item.UID] = item
	return nil
}

// Items lists the bag of one owner ordered by uid.
func (g *ItemLockGuard) Items(owner string) []BagItem {
	g.mu.Lock()
	defer g.mu.Unlock()

	items := make([]BagItem, 0)
	state := g.owners[owner]
	if state == nil {
		return items
	}
	for _, item := range state.items {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].UID < items[j].UID
	})
	return items
}

// Lock records one lock source for an item.
func (g *ItemLockGuard) Lock(owner, itemUID, reason string, until int64) error {
	if isBlank(owner) || isBlank(itemUID) {
		return ErrBlankArg
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	state := g.owners[owner]
	if state == nil {
		return ErrUnknownItem
	}
	if _, ok := state.items[itemUID]; !ok {
		return ErrUnknownItem
	}
	if !isKnownReason(reason) {
		return ErrBadReason
	}
	if !deadlineMatchesSource(reason, until) {
		return ErrBadDeadline
	}

	entries := state.locks[itemUID]
	for i := range entries {
		if entries[i].Reason == reason {
			entries[i].Until = until
			state.locks[itemUID] = entries
			return nil
		}
	}

	if len(entries) >= g.options.MaxLocksPerItem {
		return ErrLockLimit
	}

	g.seq++
	state.locks[itemUID] = append(entries, LockEntry{
		UID:    itemUID,
		Reason: reason,
		Until:  until,
		Seq:    g.seq,
	})
	return nil
}

// Unlock removes one lock source from an item.
func (g *ItemLockGuard) Unlock(owner, itemUID, reason string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	state := g.owners[owner]
	if state == nil {
		return ErrNotLocked
	}
	entries := state.locks[itemUID]
	index := -1
	for i := range entries {
		if entries[i].Reason == reason {
			index = i
			break
		}
	}
	if index < 0 {
		return ErrNotLocked
	}

	entries = append(entries[:index], entries[index+1:]...)
	if len(entries) == 0 {
		delete(state.locks, itemUID)
	} else {
		state.locks[itemUID] = entries
	}
	return nil
}

// EffectiveLock reports the lock state of one item at a given moment.
func (g *ItemLockGuard) EffectiveLock(owner, itemUID string, at int64) LockState {
	g.mu.Lock()
	defer g.mu.Unlock()

	result := LockState{UID: itemUID, Reasons: []string{}}
	state := g.owners[owner]
	if state == nil {
		return result
	}
	if _, ok := state.items[itemUID]; !ok {
		return result
	}

	reasons := make([]string, 0)
	for _, entry := range state.locks[itemUID] {
		if entry.Reason == LockTimed && at >= entry.Until {
			continue
		}
		reasons = append(reasons, entry.Reason)
	}
	sort.Strings(reasons)
	result.Reasons = reasons
	result.Locked = len(reasons) > 0
	return result
}

// Locks lists the recorded sources of one item, oldest first.
func (g *ItemLockGuard) Locks(owner, itemUID string) []LockEntry {
	g.mu.Lock()
	defer g.mu.Unlock()

	entries := make([]LockEntry, 0)
	state := g.owners[owner]
	if state == nil {
		return entries
	}
	if recorded := state.locks[itemUID]; len(recorded) > 0 {
		entries = append(entries, recorded...)
	}
	return entries
}

// LockCount counts the recorded lock sources of one owner.
func (g *ItemLockGuard) LockCount(owner string) int {
	g.mu.Lock()
	defer g.mu.Unlock()

	state := g.owners[owner]
	if state == nil {
		return 0
	}
	total := 0
	for _, entries := range state.locks {
		total += len(entries)
	}
	return total
}

// DryRunDiscard previews a batch discard without touching any item.
func (g *ItemLockGuard) DryRunDiscard(owner string, uids []string) ([]Rejection, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if err := validateBatchShape(uids, g.options.MaxBatch); err != nil {
		return nil, err
	}
	state := g.owners[owner]
	if err := checkBatchItems(state, uids); err != nil {
		return nil, err
	}
	return previewRejections(state, uids), nil
}

// Discard removes a whole batch when every item of it is discardable. It is
// record-conservative: expired timed records still block until Unlock, so at
// never relaxes the verdict and Rejected matches DryRunDiscard exactly.
func (g *ItemLockGuard) Discard(owner string, uids []string, at int64) (DiscardResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if err := validateBatchShape(uids, g.options.MaxBatch); err != nil {
		return DiscardResult{}, err
	}
	state := g.owners[owner]
	if err := checkBatchItems(state, uids); err != nil {
		return DiscardResult{}, err
	}

	rejected := previewRejections(state, uids)
	if len(rejected) > 0 {
		return DiscardResult{Removed: []string{}, Rejected: rejected}, nil
	}

	removed := make([]string, 0, len(uids))
	for _, uid := range uids {
		delete(state.items, uid)
		delete(state.locks, uid)
		removed = append(removed, uid)
	}
	return DiscardResult{Removed: removed, Rejected: []Rejection{}}, nil
}

// validateBatchShape rejects empty, duplicated or oversized batches.
func validateBatchShape(uids []string, maxBatch int) error {
	if len(uids) == 0 || len(uids) > maxBatch {
		return ErrBadBatch
	}
	seen := make(map[string]struct{}, len(uids))
	for _, uid := range uids {
		if _, duplicated := seen[uid]; duplicated {
			return ErrBadBatch
		}
		seen[uid] = struct{}{}
	}
	return nil
}

// checkBatchItems ensures every batch uid is present in the owner bag.
func checkBatchItems(state *ownerState, uids []string) error {
	if state == nil {
		return ErrUnknownItem
	}
	for _, uid := range uids {
		if _, ok := state.items[uid]; !ok {
			return ErrUnknownItem
		}
	}
	return nil
}

// previewRejections builds one rejection per blocked uid in batch order.
// Any recorded source blocks; the oldest recorded source names the verdict.
func previewRejections(state *ownerState, uids []string) []Rejection {
	rejected := make([]Rejection, 0)
	for _, uid := range uids {
		entries := state.locks[uid]
		if len(entries) == 0 {
			continue
		}
		rejected = append(rejected, Rejection{
			UID:    uid,
			Reason: entries[0].Reason,
		})
	}
	return rejected
}

// Snapshot serialises the bag, the lock records and the sequence counter.
func (g *ItemLockGuard) Snapshot() ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	dump := snapshotFile{
		Version: snapshotVersion,
		Seq:     g.seq,
		Owners:  make(map[string]snapshotOwner, len(g.owners)),
	}
	for owner, state := range g.owners {
		ownerDump := snapshotOwner{
			Items: make(map[string]BagItem, len(state.items)),
			Locks: make(map[string][]LockEntry, len(state.locks)),
		}
		for uid, item := range state.items {
			ownerDump.Items[uid] = item
		}
		for uid, entries := range state.locks {
			copied := make([]LockEntry, len(entries))
			copy(copied, entries)
			ownerDump.Locks[uid] = copied
		}
		dump.Owners[owner] = ownerDump
	}
	return json.Marshal(&dump)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *ItemLockGuard) Restore(data []byte) error {
	var dump snapshotFile
	if err := json.Unmarshal(data, &dump); err != nil {
		return ErrBadSnapshot
	}

	owners, seq, err := buildSnapshotState(&dump)
	if err != nil {
		return ErrBadSnapshot
	}

	g.mu.Lock()
	g.owners = owners
	g.seq = seq
	g.mu.Unlock()
	return nil
}

// snapshotFile is the on-disk shape of a guard snapshot.
type snapshotFile struct {
	Version int                      `json:"version"`
	Seq     int64                    `json:"seq"`
	Owners  map[string]snapshotOwner `json:"owners"`
}

// snapshotOwner holds one owner bag and its per-item lock records.
type snapshotOwner struct {
	Items map[string]BagItem     `json:"items"`
	Locks map[string][]LockEntry `json:"locks"`
}

// buildSnapshotState fully validates the payload into a fresh state without
// touching the live guard, so a failure leaves the current state intact.
func buildSnapshotState(dump *snapshotFile) (map[string]*ownerState, int64, error) {
	if dump == nil || dump.Version != snapshotVersion || dump.Seq < 0 {
		return nil, 0, ErrBadSnapshot
	}

	owners := make(map[string]*ownerState, len(dump.Owners))
	seq := dump.Seq

	for ownerID, ownerDump := range dump.Owners {
		if isBlank(ownerID) {
			return nil, 0, ErrBadSnapshot
		}
		state := &ownerState{
			items: make(map[string]BagItem, len(ownerDump.Items)),
			locks: make(map[string][]LockEntry, len(ownerDump.Locks)),
		}

		for uid, item := range ownerDump.Items {
			if isBlank(uid) || item.UID != uid || item.Qty <= 0 {
				return nil, 0, ErrBadSnapshot
			}
			state.items[uid] = item
		}

		for uid, entries := range ownerDump.Locks {
			if isBlank(uid) {
				return nil, 0, ErrBadSnapshot
			}
			if _, ok := state.items[uid]; !ok {
				return nil, 0, ErrBadSnapshot
			}

			restored := make([]LockEntry, 0, len(entries))
			seenReasons := make(map[string]struct{}, len(entries))
			for _, entry := range entries {
				if entry.UID != uid || entry.Seq < 0 ||
					!isKnownReason(entry.Reason) ||
					!deadlineMatchesSource(entry.Reason, entry.Until) {
					return nil, 0, ErrBadSnapshot
				}
				if _, duplicated := seenReasons[entry.Reason]; duplicated {
					return nil, 0, ErrBadSnapshot
				}
				seenReasons[entry.Reason] = struct{}{}
				if entry.Seq > seq {
					seq = entry.Seq
				}
				restored = append(restored, entry)
			}
			state.locks[uid] = restored
		}

		owners[ownerID] = state
	}

	return owners, seq, nil
}
