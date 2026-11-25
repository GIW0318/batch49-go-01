// Package task runs player stalls: slot bounded listings, an escrowed item
// per listing, a periodically refreshed browse snapshot and an offline
// settlement ledger.
package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Listing states.
const (
	ListingOnSale    = "onsale"
	ListingSold      = "sold"
	ListingCancelled = "cancelled"
)

// Defaults used when Options leaves a field unset.
const (
	DefaultMaxSlots    = 8
	DefaultMaxPageSize = 50
)

// Errors returned by the guard.
var (
	ErrBadOptions        = errors.New("invalid stall guard options")
	ErrBadOwner          = errors.New("owner is required")
	ErrBadAccount        = errors.New("account is required")
	ErrBadSlots          = errors.New("slot count is out of range")
	ErrStallOpen         = errors.New("stall is already open")
	ErrNoStall           = errors.New("stall is not open")
	ErrSlotLimit         = errors.New("all stall slots are occupied")
	ErrBadItem           = errors.New("item is invalid")
	ErrBadPrice          = errors.New("price must be positive")
	ErrBadAmount         = errors.New("amount must be positive")
	ErrListingGone       = errors.New("listing is no longer available")
	ErrSelfTrade         = errors.New("owner cannot buy an own listing")
	ErrInsufficientFunds = errors.New("buyer balance is insufficient")
	ErrBadPage           = errors.New("browse paging is out of range")
	ErrBadSnapshot       = errors.New("snapshot payload is invalid")
)

// Options bounds one guard.
type Options struct {
	MaxSlots    int
	MaxPageSize int
	FeePermille int
}

// Item is one escrowed stack.
type Item struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// Stall is one player storefront.
type Stall struct {
	Owner string `json:"owner"`
	Slots int    `json:"slots"`
	Used  int    `json:"used"`
}

// Listing is one item offered for sale.
type Listing struct {
	ID    string `json:"id"`
	Owner string `json:"owner"`
	Item  Item   `json:"item"`
	Price int64  `json:"price"`
	State string `json:"state"`
	Seq   int64  `json:"seq"`
}

// BrowseEntry is one row of the browse snapshot.
type BrowseEntry struct {
	ListingID string `json:"listing_id"`
	Owner     string `json:"owner"`
	Item      Item   `json:"item"`
	Price     int64  `json:"price"`
	Available bool   `json:"available"`
}

// BrowsePage is one page of the browse snapshot.
type BrowsePage struct {
	Entries  []BrowseEntry `json:"entries"`
	Page     int           `json:"page"`
	PageSize int           `json:"page_size"`
	Total    int           `json:"total"`
	Version  int64         `json:"version"`
}

// Purchase is one executed sale.
type Purchase struct {
	ListingID string `json:"listing_id"`
	Buyer     string `json:"buyer"`
	Seller    string `json:"seller"`
	Price     int64  `json:"price"`
	Fee       int64  `json:"fee"`
	Net       int64  `json:"net"`
	At        int64  `json:"at"`
}

// LedgerEntry is one sale credited to a seller.
type LedgerEntry struct {
	ListingID string `json:"listing_id"`
	Buyer     string `json:"buyer"`
	Gross     int64  `json:"gross"`
	Fee       int64  `json:"fee"`
	Net       int64  `json:"net"`
	At        int64  `json:"at"`
	Settled   bool   `json:"settled"`
}

// Settlement is the set of ledger entries released by one Settle call.
type Settlement struct {
	Owner   string        `json:"owner"`
	Entries []LedgerEntry `json:"entries"`
	Total   int64         `json:"total"`
	At      int64         `json:"at"`
}

// StallGuard owns stalls, listings, the browse snapshot and the ledger.
type StallGuard struct {
	opts Options
}

// NewStallGuard builds a guard with the given caps.
func NewStallGuard(opts Options) *StallGuard {
	return &StallGuard{opts: opts}
}

// Open creates the storefront of one owner.
func (g *StallGuard) Open(owner string, slots int) (Stall, error) {
	return Stall{}, errNotImplemented
}

// Stall reads one storefront.
func (g *StallGuard) Stall(owner string) (Stall, bool) {
	return Stall{}, false
}

// List moves one item into escrow and offers it at the given price.
func (g *StallGuard) List(owner string, item Item, price int64) (Listing, error) {
	return Listing{}, errNotImplemented
}

// ListingOf reads one listing by id.
func (g *StallGuard) ListingOf(id string) (Listing, bool) {
	return Listing{}, false
}

// Browse reads one page of the last refreshed snapshot.
func (g *StallGuard) Browse(page, pageSize int) (BrowsePage, error) {
	return BrowsePage{}, errNotImplemented
}

// Refresh rebuilds the browse snapshot from the live on sale listings.
func (g *StallGuard) Refresh() error {
	return errNotImplemented
}

// SnapshotVersion is the revision of the current browse snapshot.
func (g *StallGuard) SnapshotVersion() int64 {
	return 0
}

// Buy executes one purchase against live listing state.
func (g *StallGuard) Buy(buyer, listingID string, at int64) (Purchase, error) {
	return Purchase{}, errNotImplemented
}

// Close cancels the remaining listings and returns the escrowed items.
func (g *StallGuard) Close(owner string) ([]Listing, error) {
	return nil, errNotImplemented
}

// Settle releases unsettled proceeds up to the given time.
func (g *StallGuard) Settle(owner string, at int64) (Settlement, error) {
	return Settlement{}, errNotImplemented
}

// Ledger returns a copy of the seller ledger.
func (g *StallGuard) Ledger(owner string) []LedgerEntry {
	return nil
}

// Deposit funds one account.
func (g *StallGuard) Deposit(account string, amount int64) error {
	return errNotImplemented
}

// Balance reads one account balance.
func (g *StallGuard) Balance(account string) int64 {
	return 0
}

// Snapshot serialises stalls, listings, balances, the ledger and the snapshot.
func (g *StallGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *StallGuard) Restore(data []byte) error {
	return errNotImplemented
}
