// Package task consolidates fragmented bag stacks, spends the earliest
// expiring lot first and keeps compaction resumable across restarts.
package task

import "errors"

// errNotImplemented marks behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Binding modes of a bag lot.
const (
	BindFree  = "free"
	BindBound = "bound"
)

// Errors returned by the defragmenter.
var (
	ErrBlankOwner  = errors.New("owner is required")
	ErrBadLot      = errors.New("lot is invalid")
	ErrBagFull     = errors.New("bag slot limit reached")
	ErrBadBudget   = errors.New("compact budget must not be negative")
	ErrBadQty      = errors.New("quantity must be positive")
	ErrShortage    = errors.New("not enough quantity to consume")
	ErrBadSnapshot = errors.New("snapshot payload is invalid")
)

// Options bounds the bag tables.
type Options struct {
	StackSize int
	MaxSlots  int
}

// Lot is one batch of an item with its own expiry and insertion order.
type Lot struct {
	UID      string `json:"uid"`
	ItemID   string `json:"item_id"`
	Qty      int    `json:"qty"`
	Bind     string `json:"bind"`
	ExpireAt int64  `json:"expire_at"`
	Seq      int64  `json:"seq"`
}

// Merge describes one consolidation inside a compaction plan.
type Merge struct {
	ItemID  string   `json:"item_id"`
	Bind    string   `json:"bind"`
	Carrier string   `json:"carrier"`
	Absorb  []string `json:"absorb"`
	Qty     int      `json:"qty"`
}

// CompactPlan is the read only result of Plan.
type CompactPlan struct {
	Owner     string  `json:"owner"`
	Merges    []Merge `json:"merges"`
	Moves     int     `json:"moves"`
	Fragments int     `json:"fragments"`
}

// CompactResult reports what Compact actually changed.
type CompactResult struct {
	Owner  string `json:"owner"`
	Merges int    `json:"merges"`
	Moves  int    `json:"moves"`
	Freed  int    `json:"freed"`
}

// LotUse is one lot debit returned by Consume.
type LotUse struct {
	LotUID   string `json:"lot_uid"`
	Qty      int    `json:"qty"`
	ExpireAt int64  `json:"expire_at"`
}

// StackDefragmenter owns the per owner lots and the compaction cursor.
type StackDefragmenter struct {
	options Options
}

// NewStackDefragmenter builds a defragmenter with the given caps.
func NewStackDefragmenter(options Options) *StackDefragmenter {
	return &StackDefragmenter{options: options}
}

// Put appends one batch to an owner bag and assigns its insertion number.
func (s *StackDefragmenter) Put(owner string, lot Lot) error {
	return errNotImplemented
}

// Lots lists the batches of one item, earliest expiry first.
func (s *StackDefragmenter) Lots(owner, itemID string) []Lot {
	return nil
}

// Items lists the distinct item ids of one owner in ascending order.
func (s *StackDefragmenter) Items(owner string) []string {
	return nil
}

// Total sums the quantity of one item across all its batches.
func (s *StackDefragmenter) Total(owner, itemID string) int {
	return 0
}

// Slots counts the batches currently held by one owner.
func (s *StackDefragmenter) Slots(owner string) int {
	return 0
}

// Plan builds the merge plan of one owner without touching any batch.
func (s *StackDefragmenter) Plan(owner string) (CompactPlan, error) {
	return CompactPlan{}, errNotImplemented
}

// Compact performs at most budget merges and reports what changed.
func (s *StackDefragmenter) Compact(owner string, budget int) (CompactResult, error) {
	return CompactResult{}, errNotImplemented
}

// Consume debits quantity from the earliest expiring batches of one item.
func (s *StackDefragmenter) Consume(owner, itemID string, qty int) ([]LotUse, error) {
	return nil, errNotImplemented
}

// Snapshot serialises the batches, the sequence counter and the cursor.
func (s *StackDefragmenter) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (s *StackDefragmenter) Restore(data []byte) error {
	return errNotImplemented
}
