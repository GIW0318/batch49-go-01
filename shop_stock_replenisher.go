// Package task keeps shared limited stock, per account purchase limits,
// fixed restock anchors and reversible receipts for a shop.
package task

import "errors"

// errNotImplemented marks behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

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

// StockReplenisher owns offers, stock, limit counters and receipts.
type StockReplenisher struct {
	opts Options
}

// NewStockReplenisher builds an empty replenisher.
func NewStockReplenisher(opts Options) *StockReplenisher {
	return &StockReplenisher{opts: opts}
}

// DefineOffer validates and stores one offer.
func (s *StockReplenisher) DefineOffer(o Offer) error {
	return errNotImplemented
}

// Purchase buys from the shared stock inside the per account limit.
func (s *StockReplenisher) Purchase(account, offerID string, qty int, at int64) (Receipt, error) {
	return Receipt{}, errNotImplemented
}

// Stock reads the shared stock after applying the anchors due by at.
func (s *StockReplenisher) Stock(offerID string, at int64) int {
	return 0
}

// Restock applies every anchor due by at exactly once.
func (s *StockReplenisher) Restock(offerID string, at int64) (RestockResult, error) {
	return RestockResult{}, errNotImplemented
}

// Remaining reports how many units the account may still buy in the window.
func (s *StockReplenisher) Remaining(account, offerID string) int {
	return 0
}

// Reverse refunds one receipt of the account.
func (s *StockReplenisher) Reverse(account, receiptID string, at int64) error {
	return errNotImplemented
}

// Receipts lists the receipts of one account in creation order.
func (s *StockReplenisher) Receipts(account string) []Receipt {
	return nil
}

// Snapshot serialises offers, stock, limit counters and receipts.
func (s *StockReplenisher) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole replenisher state with a snapshot.
func (s *StockReplenisher) Restore(data []byte) error {
	return errNotImplemented
}
