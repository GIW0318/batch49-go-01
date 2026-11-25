// Package task keeps shared limited stock, per account purchase limits,
// fixed restock anchors and reversible receipts for a shop.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

const snapshotVersion = 1

// Options bounds the catalogue kept by the replenisher.
type Options struct {
	MaxOffers  int
	MaxAnchors int
}

// Errors returned by the replenisher.
var (
	ErrInvalidOffer    = errors.New("offer definition is invalid")
	ErrDuplicateOffer  = errors.New("offer already defined")
	ErrUnknownOffer    = errors.New("unknown offer")
	ErrInvalidAnchor   = errors.New("restock anchors must be strictly ascending")
	ErrOutOfStock      = errors.New("not enough shared stock")
	ErrLimitExceeded   = errors.New("account purchase limit exceeded")
	ErrBlankAccount    = errors.New("account id is required")
	ErrInvalidQuantity = errors.New("purchase quantity must be positive")
	ErrReceiptNotFound = errors.New("receipt not found")
	ErrReceiptOwner    = errors.New("receipt belongs to another account")
	ErrAlreadyReversed = errors.New("receipt already reversed")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")

	// ErrTooManyOffers is reported when the configured catalogue bound is
	// reached. It wraps ErrInvalidOffer so generic definition validation
	// checks continue to work.
	ErrTooManyOffers = fmt.Errorf("%w: too many offers", ErrInvalidOffer)
)

// Offer is one limited shop item.
type Offer struct {
	ID         string  `json:"id"`
	Cap        int     `json:"cap"`
	Limit      int     `json:"limit"`
	WindowSecs int64   `json:"window_secs"`
	Anchors    []int64 `json:"anchors"`
}

// Receipt records one successful purchase.
type Receipt struct {
	ID       string `json:"id"`
	Account  string `json:"account"`
	OfferID  string `json:"offer_id"`
	Qty      int    `json:"qty"`
	At       int64  `json:"at"`
	Reversed bool   `json:"reversed"`
}

// RestockResult reports the anchors applied by one restock call.
type RestockResult struct {
	OfferID string  `json:"offer_id"`
	Anchors []int64 `json:"anchors"`
	Added   int     `json:"added"`
	Stock   int     `json:"stock"`
}

// OverflowRecord preserves the amount discarded when a reversal would restore
// more units than the offer cap allows.
type OverflowRecord struct {
	OfferID   string `json:"offer_id"`
	ReceiptID string `json:"receipt_id"`
	Account   string `json:"account"`
	Quantity  int    `json:"quantity"`
	Restored  int    `json:"restored"`
	Discarded int    `json:"discarded"`
	Stock     int    `json:"stock"`
	At        int64  `json:"at"`
}

// limitCounter is the fixed-window purchase counter for one account/offer
// pair. Reversals deliberately do not decrement Used.
type limitCounter struct {
	Used        int   `json:"used"`
	WindowStart int64 `json:"window_start"`
	Active      bool  `json:"active"`
}

type offerState struct {
	offer     Offer
	stock     int
	applied   map[int64]struct{}
	lastAt    int64
	hasLastAt bool
	limits    map[string]*limitCounter
}

// StockReplenisher owns offers, stock, limit counters and receipts.
//
// A single mutex serialises all catalogue mutations and every operation on an
// offer. This is intentionally coarser than per-offer locking: snapshots and
// restores also need a consistent view across accounts and receipts.
type StockReplenisher struct {
	mu              sync.Mutex
	opts            Options
	offers          map[string]*offerState
	receipts        map[string]*Receipt
	receiptOrder    []string
	accountReceipts map[string][]string
	overflows       []OverflowRecord
	nextReceiptSeq  uint64
}

// NewStockReplenisher builds an empty replenisher.
func NewStockReplenisher(opts Options) *StockReplenisher {
	return &StockReplenisher{
		opts:            opts,
		offers:          make(map[string]*offerState),
		receipts:        make(map[string]*Receipt),
		accountReceipts: make(map[string][]string),
		nextReceiptSeq:  1,
	}
}

func isBlank(value string) bool {
	return strings.TrimSpace(value) == ""
}

func cloneOffer(offer Offer) Offer {
	cloned := offer
	if offer.Anchors != nil {
		cloned.Anchors = append([]int64(nil), offer.Anchors...)
	}
	return cloned
}

func validateOffer(offer Offer, opts Options) error {
	if isBlank(offer.ID) || offer.Cap <= 0 || offer.Limit <= 0 || offer.WindowSecs <= 0 {
		return fmt.Errorf("%w: id, cap, limit and window are required", ErrInvalidOffer)
	}
	if opts.MaxAnchors > 0 && len(offer.Anchors) > opts.MaxAnchors {
		return fmt.Errorf("%w: too many anchors", ErrInvalidAnchor)
	}
	for i := 1; i < len(offer.Anchors); i++ {
		if offer.Anchors[i] <= offer.Anchors[i-1] {
			return fmt.Errorf("%w: anchors must be unique and ascending", ErrInvalidAnchor)
		}
	}
	return nil
}

func windowExpired(start, now, windowSecs int64) bool {
	if now < start {
		return false
	}
	return now-start >= windowSecs
}

func (counter *limitCounter) expire(now, windowSecs int64) {
	if counter != nil && counter.Active && windowExpired(counter.WindowStart, now, windowSecs) {
		counter.Used = 0
		counter.Active = false
	}
}

func (counter *limitCounter) remaining(limit int) int {
	if counter == nil || counter.Used <= 0 {
		return limit
	}
	if counter.Used >= limit {
		return 0
	}
	return limit - counter.Used
}

func (counter *limitCounter) commit(qty int, at, windowSecs int64) {
	if !counter.Active || windowExpired(counter.WindowStart, at, windowSecs) {
		counter.Used = qty
		counter.WindowStart = at
		counter.Active = true
		return
	}
	counter.Used += qty
}

func (offer *offerState) touch(at int64) {
	if !offer.hasLastAt || at > offer.lastAt {
		offer.lastAt = at
		offer.hasLastAt = true
	}
}

// settleAnchorsLocked applies every unapplied anchor at or before at. Anchors
// are fixed and sorted, so an already-full offer still marks each due anchor
// as applied and reports it once.
func (offer *offerState) settleAnchorsLocked(at int64) ([]int64, int) {
	var anchors []int64
	added := 0

	for _, anchor := range offer.offer.Anchors {
		if anchor > at {
			break
		}
		if _, exists := offer.applied[anchor]; exists {
			continue
		}

		offer.applied[anchor] = struct{}{}
		anchors = append(anchors, anchor)

		restock := offer.offer.Cap - offer.stock
		added += restock
		offer.stock = offer.offer.Cap
	}

	return anchors, added
}

func (s *StockReplenisher) newReceiptIDLocked() string {
	for {
		id := fmt.Sprintf("r-%d", s.nextReceiptSeq)
		s.nextReceiptSeq++
		if _, exists := s.receipts[id]; !exists {
			return id
		}
	}
}

// DefineOffer validates and stores one offer.
func (s *StockReplenisher) DefineOffer(o Offer) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := validateOffer(o, s.opts); err != nil {
		return err
	}
	if _, exists := s.offers[o.ID]; exists {
		return ErrDuplicateOffer
	}
	if s.opts.MaxOffers > 0 && len(s.offers) >= s.opts.MaxOffers {
		return ErrTooManyOffers
	}

	definition := cloneOffer(o)
	s.offers[definition.ID] = &offerState{
		offer:   definition,
		stock:   definition.Cap,
		applied: make(map[int64]struct{}),
		limits:  make(map[string]*limitCounter),
	}
	return nil
}

// Purchase buys from the shared stock inside the per account limit.
func (s *StockReplenisher) Purchase(account, offerID string, qty int, at int64) (Receipt, error) {
	if isBlank(account) {
		return Receipt{}, ErrBlankAccount
	}
	if qty <= 0 {
		return Receipt{}, ErrInvalidQuantity
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	offer, exists := s.offers[offerID]
	if !exists {
		return Receipt{}, ErrUnknownOffer
	}

	offer.touch(at)
	offer.settleAnchorsLocked(at)

	counter := offer.limits[account]
	if counter != nil {
		counter.expire(at, offer.offer.WindowSecs)
	}
	if qty > counter.remaining(offer.offer.Limit) {
		return Receipt{}, ErrLimitExceeded
	}
	if qty > offer.stock {
		return Receipt{}, ErrOutOfStock
	}

	if counter == nil {
		counter = &limitCounter{}
		offer.limits[account] = counter
	}

	// Shared stock, account window count and receipt commit together under
	// the replenisher mutex.
	offer.stock -= qty
	counter.commit(qty, at, offer.offer.WindowSecs)

	receipt := Receipt{
		ID:       s.newReceiptIDLocked(),
		Account:  account,
		OfferID:  offerID,
		Qty:      qty,
		At:       at,
		Reversed: false,
	}
	receiptCopy := receipt
	s.receipts[receipt.ID] = &receiptCopy
	s.receiptOrder = append(s.receiptOrder, receipt.ID)
	s.accountReceipts[account] = append(s.accountReceipts[account], receipt.ID)

	return receipt, nil
}

// Stock reads the shared stock after applying the anchors due by at.
func (s *StockReplenisher) Stock(offerID string, at int64) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	offer, exists := s.offers[offerID]
	if !exists {
		return 0
	}
	offer.touch(at)
	offer.settleAnchorsLocked(at)
	return offer.stock
}

// Restock applies every anchor due by at exactly once.
func (s *StockReplenisher) Restock(offerID string, at int64) (RestockResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	offer, exists := s.offers[offerID]
	if !exists {
		return RestockResult{OfferID: offerID, Anchors: []int64{}}, ErrUnknownOffer
	}

	offer.touch(at)
	anchors, added := offer.settleAnchorsLocked(at)
	if anchors == nil {
		anchors = []int64{}
	}

	return RestockResult{
		OfferID: offerID,
		Anchors: anchors,
		Added:   added,
		Stock:   offer.stock,
	}, nil
}

// Remaining reports how many units the account may still buy in the window.
// Because this call carries no timestamp, the offer's highest observed
// interaction time is used to settle the window.
func (s *StockReplenisher) Remaining(account, offerID string) int {
	if isBlank(account) {
		return 0
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	offer, exists := s.offers[offerID]
	if !exists {
		return 0
	}

	counter := offer.limits[account]
	if counter != nil {
		counter.expire(offer.lastAt, offer.offer.WindowSecs)
	}
	return counter.remaining(offer.offer.Limit)
}

// Reverse refunds one receipt of the account. Limit usage is intentionally
// left unchanged.
func (s *StockReplenisher) Reverse(account, receiptID string, at int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	receipt, exists := s.receipts[receiptID]
	if !exists {
		return ErrReceiptNotFound
	}
	if receipt.Account != account {
		return ErrReceiptOwner
	}
	if receipt.Reversed {
		return ErrAlreadyReversed
	}

	offer, exists := s.offers[receipt.OfferID]
	if !exists {
		return ErrUnknownOffer
	}

	offer.touch(at)
	offer.settleAnchorsLocked(at)

	room := offer.offer.Cap - offer.stock
	restored := receipt.Qty
	if restored > room {
		restored = room
	}
	discarded := receipt.Qty - restored

	offer.stock += restored
	receipt.Reversed = true

	if discarded > 0 {
		s.overflows = append(s.overflows, OverflowRecord{
			OfferID:   receipt.OfferID,
			ReceiptID: receipt.ID,
			Account:   account,
			Quantity:  receipt.Qty,
			Restored:  restored,
			Discarded: discarded,
			Stock:     offer.stock,
			At:        at,
		})
	}

	return nil
}

// Receipts lists the receipts of one account in creation order.
func (s *StockReplenisher) Receipts(account string) []Receipt {
	s.mu.Lock()
	defer s.mu.Unlock()

	ids := s.accountReceipts[account]
	if len(ids) == 0 {
		return nil
	}

	receipts := make([]Receipt, 0, len(ids))
	for _, id := range ids {
		if receipt, exists := s.receipts[id]; exists {
			receipts = append(receipts, *receipt)
		}
	}
	return receipts
}

// OverflowRecords returns reversal quantities discarded by the cap. When
// offerID is empty, records for every offer are returned.
func (s *StockReplenisher) OverflowRecords(offerID string) []OverflowRecord {
	s.mu.Lock()
	defer s.mu.Unlock()

	var records []OverflowRecord
	for _, record := range s.overflows {
		if offerID == "" || record.OfferID == offerID {
			records = append(records, record)
		}
	}
	return records
}

type offerSnapshot struct {
	Offer                Offer            `json:"offer"`
	Stock                int              `json:"stock"`
	AppliedAnchors       []int64          `json:"applied_anchors,omitempty"`
	LastInteractionAt    int64            `json:"last_interaction_at"`
	HasLastInteractionAt bool             `json:"has_last_interaction_at"`
	Limits               map[string]int   `json:"limits,omitempty"`
	LimitWindows         map[string]int64 `json:"limit_windows,omitempty"`
	LimitActive          map[string]bool  `json:"limit_active,omitempty"`
}

type snapshotData struct {
	Version         int                 `json:"version"`
	Options         Options             `json:"options"`
	NextReceiptSeq  uint64              `json:"next_receipt_seq"`
	Offers          []offerSnapshot     `json:"offers"`
	Receipts        []Receipt           `json:"receipts"`
	AccountReceipts map[string][]string `json:"account_receipts,omitempty"`
	Overflows       []OverflowRecord    `json:"overflows,omitempty"`
}

// Snapshot serialises offers, stock, limit counters, applied anchors and
// receipts.
func (s *StockReplenisher) Snapshot() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data := snapshotData{
		Version:        snapshotVersion,
		Options:        s.opts,
		NextReceiptSeq: s.nextReceiptSeq,
		Receipts:       make([]Receipt, 0, len(s.receiptOrder)),
		Overflows:      append([]OverflowRecord(nil), s.overflows...),
	}

	offerIDs := make([]string, 0, len(s.offers))
	for id := range s.offers {
		offerIDs = append(offerIDs, id)
	}
	sort.Strings(offerIDs)

	for _, id := range offerIDs {
		offer := s.offers[id]
		item := offerSnapshot{
			Offer:                cloneOffer(offer.offer),
			Stock:                offer.stock,
			LastInteractionAt:    offer.lastAt,
			HasLastInteractionAt: offer.hasLastAt,
			Limits:               make(map[string]int, len(offer.limits)),
			LimitWindows:         make(map[string]int64, len(offer.limits)),
			LimitActive:          make(map[string]bool, len(offer.limits)),
		}

		for _, anchor := range offer.offer.Anchors {
			if _, applied := offer.applied[anchor]; applied {
				item.AppliedAnchors = append(item.AppliedAnchors, anchor)
			}
		}
		for account, counter := range offer.limits {
			item.Limits[account] = counter.Used
			item.LimitWindows[account] = counter.WindowStart
			item.LimitActive[account] = counter.Active
		}

		data.Offers = append(data.Offers, item)
	}

	for _, id := range s.receiptOrder {
		if receipt, exists := s.receipts[id]; exists {
			data.Receipts = append(data.Receipts, *receipt)
		}
	}
	data.AccountReceipts = make(map[string][]string, len(s.accountReceipts))
	for account, ids := range s.accountReceipts {
		data.AccountReceipts[account] = append([]string(nil), ids...)
	}

	payload, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	return payload, nil
}

// Restore replaces the whole replenisher state with a snapshot. Invalid
// payloads are rejected before any live state is changed.
func (s *StockReplenisher) Restore(payload []byte) error {
	var data snapshotData
	if err := json.Unmarshal(payload, &data); err != nil {
		return fmt.Errorf("%w: decode snapshot: %v", ErrBadSnapshot, err)
	}
	if data.Version != snapshotVersion {
		return fmt.Errorf("%w: unsupported version %d", ErrBadSnapshot, data.Version)
	}

	offers := make(map[string]*offerState, len(data.Offers))
	for _, item := range data.Offers {
		definition := cloneOffer(item.Offer)
		if err := validateOffer(definition, data.Options); err != nil {
			return fmt.Errorf("%w: invalid offer in snapshot: %v", ErrBadSnapshot, err)
		}
		if _, duplicate := offers[definition.ID]; duplicate {
			return fmt.Errorf("%w: duplicate offer in snapshot", ErrBadSnapshot)
		}
		if item.Stock < 0 || item.Stock > definition.Cap {
			return fmt.Errorf("%w: stock for %q is outside its cap", ErrBadSnapshot, definition.ID)
		}

		applied := make(map[int64]struct{}, len(item.AppliedAnchors))
		if len(item.AppliedAnchors) > len(definition.Anchors) {
			return fmt.Errorf("%w: too many applied anchors", ErrBadSnapshot)
		}
		for index, anchor := range item.AppliedAnchors {
			if anchor != definition.Anchors[index] {
				return fmt.Errorf("%w: applied anchors must be an ordered prefix", ErrBadSnapshot)
			}
			if _, duplicate := applied[anchor]; duplicate {
				return fmt.Errorf("%w: applied anchor %d is duplicated", ErrBadSnapshot, anchor)
			}
			applied[anchor] = struct{}{}
		}

		limits := make(map[string]*limitCounter, len(item.Limits))
		for account, used := range item.Limits {
			if isBlank(account) || used < 0 || used > definition.Limit {
				return fmt.Errorf("%w: invalid limit counter", ErrBadSnapshot)
			}
			limits[account] = &limitCounter{
				Used:        used,
				WindowStart: item.LimitWindows[account],
				Active:      item.LimitActive[account],
			}
		}

		offers[definition.ID] = &offerState{
			offer:     definition,
			stock:     item.Stock,
			applied:   applied,
			lastAt:    item.LastInteractionAt,
			hasLastAt: item.HasLastInteractionAt || item.LastInteractionAt != 0,
			limits:    limits,
		}
	}

	receipts := make(map[string]*Receipt, len(data.Receipts))
	order := make([]string, 0, len(data.Receipts))
	accountReceipts := make(map[string][]string)
	seenAccountReceipt := make(map[string]struct{})

	for _, receiptValue := range data.Receipts {
		receipt := receiptValue
		if isBlank(receipt.ID) {
			return fmt.Errorf("%w: receipt id is blank", ErrBadSnapshot)
		}
		if _, duplicate := receipts[receipt.ID]; duplicate {
			return fmt.Errorf("%w: duplicate receipt %q", ErrBadSnapshot, receipt.ID)
		}
		if isBlank(receipt.Account) || receipt.Qty <= 0 {
			return fmt.Errorf("%w: receipt %q is invalid", ErrBadSnapshot, receipt.ID)
		}
		if _, exists := offers[receipt.OfferID]; !exists {
			return fmt.Errorf("%w: receipt %q references unknown offer", ErrBadSnapshot, receipt.ID)
		}

		receipts[receipt.ID] = &receipt
		order = append(order, receipt.ID)
		accountReceipts[receipt.Account] = append(accountReceipts[receipt.Account], receipt.ID)
		seenAccountReceipt[receipt.Account+"\x00"+receipt.ID] = struct{}{}
	}

	// Rebuild the account index from receipts. When an optional index is
	// present, reject snapshots where it disagrees with the receipt list.
	for account, ids := range data.AccountReceipts {
		if isBlank(account) || len(ids) == 0 {
			return fmt.Errorf("%w: invalid account receipt index", ErrBadSnapshot)
		}
		for _, id := range ids {
			if _, ok := seenAccountReceipt[account+"\x00"+id]; !ok {
				return fmt.Errorf("%w: account receipt index does not match receipts", ErrBadSnapshot)
			}
		}
	}

	nextSeq := data.NextReceiptSeq
	if nextSeq == 0 {
		nextSeq = 1
	}
	overflows := append([]OverflowRecord(nil), data.Overflows...)

	s.mu.Lock()
	s.opts = data.Options
	s.offers = offers
	s.receipts = receipts
	s.receiptOrder = order
	s.accountReceipts = accountReceipts
	s.overflows = overflows
	s.nextReceiptSeq = nextSeq
	s.mu.Unlock()

	return nil
}
