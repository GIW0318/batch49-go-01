// Package task guards bag item locks coming from several sources and previews
// batch discards before executing them.
package task

import "errors"

// errNotImplemented marks behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Lock sources recorded for one item.
const (
	LockPlayer   = "player"
	LockTimed    = "timed"
	LockEquipped = "equipped"
	LockEscrow   = "escrow"
	LockQuest    = "quest"
)

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
type ItemLockGuard struct {
	options Options
}

// NewItemLockGuard builds a guard with the given caps.
func NewItemLockGuard(options Options) *ItemLockGuard {
	return &ItemLockGuard{options: options}
}

// AddItem puts one item into an owner bag.
func (g *ItemLockGuard) AddItem(owner string, item BagItem) error {
	return errNotImplemented
}

// Items lists the bag of one owner ordered by uid.
func (g *ItemLockGuard) Items(owner string) []BagItem {
	return nil
}

// Lock records one lock source for an item.
func (g *ItemLockGuard) Lock(owner, itemUID, reason string, until int64) error {
	return errNotImplemented
}

// Unlock removes one lock source from an item.
func (g *ItemLockGuard) Unlock(owner, itemUID, reason string) error {
	return errNotImplemented
}

// EffectiveLock reports the lock state of one item at a given moment.
func (g *ItemLockGuard) EffectiveLock(owner, itemUID string, at int64) LockState {
	return LockState{}
}

// Locks lists the recorded sources of one item, oldest first.
func (g *ItemLockGuard) Locks(owner, itemUID string) []LockEntry {
	return nil
}

// LockCount counts the recorded lock sources of one owner.
func (g *ItemLockGuard) LockCount(owner string) int {
	return 0
}

// DryRunDiscard previews a batch discard without touching any item.
func (g *ItemLockGuard) DryRunDiscard(owner string, uids []string) ([]Rejection, error) {
	return nil, errNotImplemented
}

// Discard removes a whole batch when every item of it is discardable.
func (g *ItemLockGuard) Discard(owner string, uids []string, at int64) (DiscardResult, error) {
	return DiscardResult{}, errNotImplemented
}

// Snapshot serialises the bag, the lock records and the sequence counter.
func (g *ItemLockGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *ItemLockGuard) Restore(data []byte) error {
	return errNotImplemented
}
