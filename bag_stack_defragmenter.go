// Package task consolidates fragmented bag stacks, spends the earliest
// expiring lot first and keeps compaction resumable across restarts.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
)

// snapshotVersion tags the on-wire snapshot layout.
const snapshotVersion = 1

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

// ownerState keeps the lots of one owner in insertion order plus the durable
// compaction checkpoint. The checkpoint advances as absorbed lots are removed
// and is part of every snapshot, so a restart resumes with the same surviving
// fragment order instead of rebuilding it from scratch.
type ownerState struct {
	lots       []Lot
	breakpoint int64
}

// StackDefragmenter owns the per owner lots and the compaction cursor.
//
// A single reader/writer lock gives every owner serialized Put, Compact and
// Consume calls, while Plan and the read-only queries can run concurrently and
// observe either the state before or the state after a whole mutation.
type StackDefragmenter struct {
	mu      sync.RWMutex
	options Options
	nextSeq int64
	owners  map[string]*ownerState
}

// snapshotOwner is one owner entry in the snapshot payload.
type snapshotOwner struct {
	Lots       []Lot `json:"lots"`
	Breakpoint int64 `json:"breakpoint"`
}

// snapshotPayload is the complete persisted state.
type snapshotPayload struct {
	Version int                      `json:"version"`
	NextSeq int64                    `json:"next_seq"`
	Owners  map[string]snapshotOwner `json:"owners"`
}

// planLot is a lot together with its position in the owner insertion slice.
type planLot struct {
	index int
	lot   Lot
}

// internalMerge is a merge expressed with slice indexes while holding the
// write lock.
type internalMerge struct {
	carrier int
	absorb  []int
	qty     int
}

// internalPlan is the index based form of a compaction plan.
type internalPlan struct {
	merges    []internalMerge
	moves     int
	fragments int
}

// NewStackDefragmenter builds a defragmenter with the given caps.
func NewStackDefragmenter(options Options) *StackDefragmenter {
	return &StackDefragmenter{
		options: options,
		nextSeq: 1,
		owners:  make(map[string]*ownerState),
	}
}

// validBind reports whether bind is one of the two supported binding modes.
func validBind(bind string) bool {
	return bind == BindFree || bind == BindBound
}

// expiryRank maps the zero expiry sentinel to the end of the ordering. A zero
// ExpireAt means the lot has no expiry, so it must be spent after every dated
// lot even though zero is numerically smaller.
func expiryRank(expireAt int64) int64 {
	if expireAt == 0 {
		return math.MaxInt64
	}
	return expireAt
}

// lotLess orders lots by expiry and then by insertion sequence.
func lotLess(a, b Lot) bool {
	ar, br := expiryRank(a.ExpireAt), expiryRank(b.ExpireAt)
	if ar != br {
		return ar < br
	}
	return a.Seq < b.Seq
}

// validateLot checks a lot against the configured stack size.
func (s *StackDefragmenter) validateLot(lot Lot) error {
	if strings.TrimSpace(lot.UID) == "" || strings.TrimSpace(lot.ItemID) == "" {
		return ErrBadLot
	}
	if lot.Qty <= 0 || lot.Qty > s.options.StackSize {
		return ErrBadLot
	}
	if !validBind(lot.Bind) {
		return ErrBadLot
	}
	return nil
}

// cloneLots returns an independent copy of lots.
func cloneLots(lots []Lot) []Lot {
	if len(lots) == 0 {
		return nil
	}
	out := make([]Lot, len(lots))
	copy(out, lots)
	return out
}

// Put appends one batch to an owner bag and assigns its insertion number.
func (s *StackDefragmenter) Put(owner string, lot Lot) error {
	if strings.TrimSpace(owner) == "" {
		return ErrBlankOwner
	}
	if err := s.validateLot(lot); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	st := s.owners[owner]
	held := 0
	if st != nil {
		held = len(st.lots)
	}
	if held >= s.options.MaxSlots {
		return ErrBagFull
	}
	if st == nil {
		st = &ownerState{}
		s.owners[owner] = st
	}

	lot.Seq = s.nextSeq
	s.nextSeq++
	st.lots = append(st.lots, lot)
	return nil
}

// Lots lists the batches of one item, earliest expiry first.
func (s *StackDefragmenter) Lots(owner, itemID string) []Lot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	st := s.owners[owner]
	if st == nil {
		return nil
	}
	out := make([]Lot, 0, len(st.lots))
	for _, lot := range st.lots {
		if lot.ItemID == itemID {
			out = append(out, lot)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return lotLess(out[i], out[j])
	})
	return out
}

// Items lists the distinct item ids of one owner in ascending order.
func (s *StackDefragmenter) Items(owner string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	st := s.owners[owner]
	if st == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(st.lots))
	for _, lot := range st.lots {
		seen[lot.ItemID] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for itemID := range seen {
		out = append(out, itemID)
	}
	sort.Strings(out)
	return out
}

// Total sums the quantity of one item across all its batches.
func (s *StackDefragmenter) Total(owner, itemID string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	st := s.owners[owner]
	if st == nil {
		return 0
	}
	total := 0
	for _, lot := range st.lots {
		if lot.ItemID == itemID {
			total += lot.Qty
		}
	}
	return total
}

// Slots counts the batches currently held by one owner.
func (s *StackDefragmenter) Slots(owner string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if st := s.owners[owner]; st != nil {
		return len(st.lots)
	}
	return 0
}

// buildPlan computes the index based plan without changing any lot.
func (s *StackDefragmenter) buildPlan(st *ownerState) internalPlan {
	var result internalPlan
	if st == nil || s.options.StackSize <= 0 {
		return result
	}

	type groupKey struct {
		itemID string
		bind   string
	}
	groups := make(map[groupKey][]planLot)
	order := make([]groupKey, 0)
	for index, lot := range st.lots {
		if lot.Qty >= s.options.StackSize {
			continue
		}
		key := groupKey{itemID: lot.ItemID, bind: lot.Bind}
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], planLot{index: index, lot: lot})
	}

	for _, key := range order {
		result.merges = append(result.merges, s.planGroup(groups[key])...)
	}
	for _, merge := range result.merges {
		result.moves += len(merge.absorb)
		result.fragments += 1 + len(merge.absorb)
	}
	return result
}

// planGroup packs one item/binding group. The carrier of each merge is the
// earliest expiring unprocessed fragment, while absorbed fragments are taken
// in insertion (Seq) order so the result is independent of expiry ties.
func (s *StackDefragmenter) planGroup(lots []planLot) []internalMerge {
	if len(lots) < 2 {
		return nil
	}
	fragments := append([]planLot(nil), lots...)
	sort.SliceStable(fragments, func(i, j int) bool {
		return lotLess(fragments[i].lot, fragments[j].lot)
	})

	used := make([]bool, len(fragments))
	merges := make([]internalMerge, 0)
	for carrierIndex := 0; carrierIndex < len(fragments); carrierIndex++ {
		if used[carrierIndex] {
			continue
		}
		carrier := fragments[carrierIndex]
		used[carrierIndex] = true

		candidates := make([]int, 0, len(fragments)-carrierIndex-1)
		for index := 0; index < len(fragments); index++ {
			if !used[index] {
				candidates = append(candidates, index)
			}
		}
		sort.SliceStable(candidates, func(i, j int) bool {
			left := fragments[candidates[i]].lot
			right := fragments[candidates[j]].lot
			if left.Seq != right.Seq {
				return left.Seq < right.Seq
			}
			return fragments[candidates[i]].index < fragments[candidates[j]].index
		})

		qty := carrier.lot.Qty
		absorbed := make([]int, 0, len(candidates))
		for _, candidateIndex := range candidates {
			candidate := fragments[candidateIndex]
			if qty+candidate.lot.Qty > s.options.StackSize {
				continue
			}
			qty += candidate.lot.Qty
			absorbed = append(absorbed, candidate.index)
			used[candidateIndex] = true
		}
		if len(absorbed) == 0 {
			// The carrier cannot take any remaining lot and, because every
			// other lot has an equal or larger size, none can take it either.
			continue
		}
		merges = append(merges, internalMerge{
			carrier: carrier.index,
			absorb:  absorbed,
			qty:     qty,
		})
	}
	return merges
}

// Plan builds the merge plan of one owner without touching any batch.
func (s *StackDefragmenter) Plan(owner string) (CompactPlan, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	plan := CompactPlan{Owner: owner, Merges: make([]Merge, 0)}
	st := s.owners[owner]
	if st == nil {
		return plan, nil
	}

	internal := s.buildPlan(st)
	for _, merge := range internal.merges {
		carrier := st.lots[merge.carrier]
		absorb := make([]string, 0, len(merge.absorb))
		for _, index := range merge.absorb {
			absorb = append(absorb, st.lots[index].UID)
		}
		plan.Merges = append(plan.Merges, Merge{
			ItemID:  carrier.ItemID,
			Bind:    carrier.Bind,
			Carrier: carrier.UID,
			Absorb:  absorb,
			Qty:     merge.qty,
		})
	}
	plan.Moves = internal.moves
	plan.Fragments = internal.fragments
	return plan, nil
}

// Compact performs at most budget merges and reports what changed.
func (s *StackDefragmenter) Compact(owner string, budget int) (CompactResult, error) {
	if budget < 0 {
		return CompactResult{}, ErrBadBudget
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	result := CompactResult{Owner: owner}
	st := s.owners[owner]
	if st == nil || budget == 0 {
		return result, nil
	}

	plan := s.buildPlan(st)
	if len(plan.merges) == 0 {
		return result, nil
	}

	removed := make([]bool, len(st.lots))
	carrierQty := make(map[int]int)
	var highestSeq int64
	for _, merge := range plan.merges {
		if result.Moves >= budget {
			break
		}
		if removed[merge.carrier] {
			continue
		}
		carrierQty[merge.carrier] = st.lots[merge.carrier].Qty
		touched := false
		for _, index := range merge.absorb {
			if result.Moves >= budget {
				break
			}
			if removed[index] {
				continue
			}
			removed[index] = true
			carrierQty[merge.carrier] += st.lots[index].Qty
			if st.lots[index].Seq > highestSeq {
				highestSeq = st.lots[index].Seq
			}
			result.Moves++
			touched = true
		}
		if touched {
			result.Merges++
		}
	}
	if result.Moves == 0 {
		return result, nil
	}

	compacted := make([]Lot, 0, len(st.lots)-result.Moves)
	for index, lot := range st.lots {
		if removed[index] {
			continue
		}
		if qty, ok := carrierQty[index]; ok {
			lot.Qty = qty
		}
		compacted = append(compacted, lot)
	}
	st.lots = compacted
	if highestSeq > st.breakpoint {
		st.breakpoint = highestSeq
	}
	result.Freed = result.Moves
	return result, nil
}

// Consume debits quantity from the earliest expiring batches of one item.
func (s *StackDefragmenter) Consume(owner, itemID string, qty int) ([]LotUse, error) {
	if qty <= 0 {
		return nil, ErrBadQty
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	st := s.owners[owner]
	if st == nil {
		return nil, ErrShortage
	}

	total := int64(0)
	for _, lot := range st.lots {
		if lot.ItemID == itemID {
			total += int64(lot.Qty)
		}
	}
	if total < int64(qty) {
		return nil, ErrShortage
	}

	order := make([]int, 0, len(st.lots))
	for index, lot := range st.lots {
		if lot.ItemID == itemID {
			order = append(order, index)
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		return lotLess(st.lots[order[i]], st.lots[order[j]])
	})

	remaining := qty
	uses := make([]LotUse, 0)
	removed := make([]bool, len(st.lots))
	for _, index := range order {
		if remaining == 0 {
			break
		}
		lot := st.lots[index]
		take := lot.Qty
		if take > remaining {
			take = remaining
		}
		uses = append(uses, LotUse{
			LotUID:   lot.UID,
			Qty:      take,
			ExpireAt: lot.ExpireAt,
		})
		remaining -= take
		if take == lot.Qty {
			removed[index] = true
		} else {
			st.lots[index].Qty -= take
		}
	}

	pruned := make([]Lot, 0, len(st.lots))
	for index, lot := range st.lots {
		if !removed[index] {
			pruned = append(pruned, lot)
		}
	}
	st.lots = pruned
	return uses, nil
}

// Snapshot serialises the batches, the sequence counter and the cursor.
func (s *StackDefragmenter) Snapshot() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	payload := snapshotPayload{
		Version: snapshotVersion,
		NextSeq: s.nextSeq,
		Owners:  make(map[string]snapshotOwner, len(s.owners)),
	}
	for owner, st := range s.owners {
		payload.Owners[owner] = snapshotOwner{
			Lots:       cloneLots(st.lots),
			Breakpoint: st.breakpoint,
		}
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	return data, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (s *StackDefragmenter) Restore(data []byte) error {
	var payload snapshotPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	if payload.Version != snapshotVersion || payload.Owners == nil {
		return ErrBadSnapshot
	}

	owners := make(map[string]*ownerState, len(payload.Owners))
	seenSeq := make(map[int64]struct{})
	maxSeq := int64(0)
	for owner, snap := range payload.Owners {
		if strings.TrimSpace(owner) == "" || snap.Breakpoint < 0 {
			return ErrBadSnapshot
		}
		if len(snap.Lots) > s.options.MaxSlots {
			return ErrBadSnapshot
		}
		st := &ownerState{
			lots:       make([]Lot, 0, len(snap.Lots)),
			breakpoint: snap.Breakpoint,
		}
		for _, lot := range snap.Lots {
			if err := s.validateLot(lot); err != nil {
				return ErrBadSnapshot
			}
			if lot.Seq <= 0 {
				return ErrBadSnapshot
			}
			if _, duplicate := seenSeq[lot.Seq]; duplicate {
				return ErrBadSnapshot
			}
			seenSeq[lot.Seq] = struct{}{}
			if lot.Seq > maxSeq {
				maxSeq = lot.Seq
			}
			st.lots = append(st.lots, lot)
		}
		// Insertion order is defined by Seq; canonicalising keeps a restored
		// bag equivalent for later plans.
		sort.SliceStable(st.lots, func(i, j int) bool {
			return st.lots[i].Seq < st.lots[j].Seq
		})
		owners[owner] = st
	}

	nextSeq := payload.NextSeq
	if nextSeq <= maxSeq {
		nextSeq = maxSeq + 1
	}
	if nextSeq < 1 {
		nextSeq = 1
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.owners = owners
	s.nextSeq = nextSeq
	return nil
}
