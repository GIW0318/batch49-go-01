// Package task runs player stalls: slot bounded listings, an escrowed item
// per listing, a periodically refreshed browse snapshot and an offline
// settlement ledger.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

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
	mu       sync.Mutex
	opts     Options
	stalls   map[string]Stall
	listings map[string]Listing
	nextSeq  int64
	browse   []BrowseEntry
	version  int64
	ledger   map[string][]LedgerEntry
	balances map[string]int64
}

// NewStallGuard builds a guard with the given caps.
func NewStallGuard(opts Options) *StallGuard {
	if opts.MaxSlots == 0 {
		opts.MaxSlots = DefaultMaxSlots
	}
	if opts.MaxPageSize == 0 {
		opts.MaxPageSize = DefaultMaxPageSize
	}
	return &StallGuard{
		opts:     opts,
		stalls:   make(map[string]Stall),
		listings: make(map[string]Listing),
		browse:   []BrowseEntry{},
		ledger:   make(map[string][]LedgerEntry),
		balances: make(map[string]int64),
	}
}

// Open creates the storefront of one owner.
func (g *StallGuard) Open(owner string, slots int) (Stall, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if strings.TrimSpace(owner) == "" {
		return Stall{}, ErrBadOwner
	}
	if slots < 1 || slots > g.opts.MaxSlots {
		return Stall{}, ErrBadSlots
	}
	if _, exists := g.stalls[owner]; exists {
		return Stall{}, ErrStallOpen
	}

	stall := Stall{Owner: owner, Slots: slots}
	g.stalls[owner] = stall
	return stall, nil
}

// Stall reads one storefront.
func (g *StallGuard) Stall(owner string) (Stall, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	stall, ok := g.stalls[owner]
	return stall, ok
}

// List moves one item into escrow and offers it at the given price.
func (g *StallGuard) List(owner string, item Item, price int64) (Listing, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	stall, ok := g.stalls[owner]
	if !ok {
		return Listing{}, ErrNoStall
	}
	if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.Name) == "" || item.Count <= 0 {
		return Listing{}, ErrBadItem
	}
	if price <= 0 {
		return Listing{}, ErrBadPrice
	}
	if stall.Used >= effectiveCapacity(stall.Slots) {
		return Listing{}, ErrSlotLimit
	}

	g.nextSeq++
	listing := Listing{
		ID:    fmt.Sprintf("L%020d", g.nextSeq),
		Owner: owner,
		Item:  item,
		Price: price,
		State: ListingOnSale,
		Seq:   g.nextSeq,
	}
	g.listings[listing.ID] = listing
	stall.Used++
	g.stalls[owner] = stall
	return listing, nil
}

// ListingOf reads one listing by id.
func (g *StallGuard) ListingOf(id string) (Listing, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	listing, ok := g.listings[id]
	return listing, ok
}

// Browse reads one page of the last refreshed snapshot.
func (g *StallGuard) Browse(page, pageSize int) (BrowsePage, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if page < 1 || pageSize < 1 || pageSize > g.opts.MaxPageSize {
		return BrowsePage{}, ErrBadPage
	}

	total := len(g.browse)
	start := 0
	if page > 1 {
		previous := page - 1
		maxInt := int(^uint(0) >> 1)
		if previous > maxInt/pageSize {
			start = total
		} else {
			start = previous * pageSize
		}
	}
	if start > total {
		start = total
	}
	end := start + pageSize
	if end < start || end > total {
		end = total
	}

	entries := make([]BrowseEntry, 0, end-start)
	for _, entry := range g.browse[start:end] {
		listing, live := g.listings[entry.ListingID]
		if live && listing.State == ListingOnSale {
			_, open := g.stalls[listing.Owner]
			entry.Available = open
		} else {
			entry.Available = false
		}
		entries = append(entries, entry)
	}

	return BrowsePage{
		Entries:  entries,
		Page:     page,
		PageSize: pageSize,
		Total:    total,
		Version:  g.version,
	}, nil
}

// Refresh rebuilds the browse snapshot from the live on sale listings.
func (g *StallGuard) Refresh() error {
	g.mu.Lock()
	defer g.mu.Unlock()

	listings := make([]Listing, 0, len(g.listings))
	for _, listing := range g.listings {
		if listing.State == ListingOnSale {
			listings = append(listings, listing)
		}
	}
	sort.Slice(listings, func(i, j int) bool {
		if listings[i].Price != listings[j].Price {
			return listings[i].Price < listings[j].Price
		}
		return listings[i].ID < listings[j].ID
	})

	entries := make([]BrowseEntry, 0, len(listings))
	for _, listing := range listings {
		entries = append(entries, BrowseEntry{
			ListingID: listing.ID,
			Owner:     listing.Owner,
			Item:      listing.Item,
			Price:     listing.Price,
			Available: true,
		})
	}
	g.browse = entries
	g.version++
	return nil
}

// SnapshotVersion is the revision of the current browse snapshot.
func (g *StallGuard) SnapshotVersion() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.version
}

// Buy executes one purchase against live listing state.
func (g *StallGuard) Buy(buyer, listingID string, at int64) (Purchase, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if strings.TrimSpace(buyer) == "" {
		return Purchase{}, ErrBadAccount
	}

	listing, ok := g.listings[listingID]
	if !ok || listing.State != ListingOnSale {
		return Purchase{}, ErrListingGone
	}
	stall, ok := g.stalls[listing.Owner]
	if !ok {
		return Purchase{}, ErrListingGone
	}
	if buyer == listing.Owner {
		return Purchase{}, ErrSelfTrade
	}
	if g.balances[buyer] < listing.Price {
		return Purchase{}, ErrInsufficientFunds
	}

	fee := listing.Price * int64(g.opts.FeePermille) / 1000
	net := listing.Price - fee

	g.balances[buyer] -= listing.Price
	g.ledger[listing.Owner] = append(g.ledger[listing.Owner], LedgerEntry{
		ListingID: listing.ID,
		Buyer:     buyer,
		Gross:     listing.Price,
		Fee:       fee,
		Net:       net,
		At:        at,
	})

	listing.State = ListingSold
	g.listings[listing.ID] = listing
	stall.Used--
	if stall.Used < 0 {
		stall.Used = 0
	}
	g.stalls[listing.Owner] = stall

	return Purchase{
		ListingID: listing.ID,
		Buyer:     buyer,
		Seller:    listing.Owner,
		Price:     listing.Price,
		Fee:       fee,
		Net:       net,
		At:        at,
	}, nil
}

// Close cancels the remaining listings and returns the escrowed items.
func (g *StallGuard) Close(owner string) ([]Listing, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	stall, ok := g.stalls[owner]
	if !ok {
		return nil, ErrNoStall
	}

	remaining := make([]Listing, 0)
	for _, listing := range g.listings {
		if listing.Owner == owner && listing.State == ListingOnSale {
			remaining = append(remaining, listing)
		}
	}
	sort.Slice(remaining, func(i, j int) bool {
		if remaining[i].Seq != remaining[j].Seq {
			return remaining[i].Seq < remaining[j].Seq
		}
		return remaining[i].ID < remaining[j].ID
	})
	for i := range remaining {
		remaining[i].State = ListingCancelled
		g.listings[remaining[i].ID] = remaining[i]
	}

	stall.Used = 0
	delete(g.stalls, owner)
	return remaining, nil
}

// Settle releases unsettled proceeds up to the given time.
func (g *StallGuard) Settle(owner string, at int64) (Settlement, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	type candidate struct {
		index int
		entry LedgerEntry
	}
	candidates := make([]candidate, 0)
	for i, entry := range g.ledger[owner] {
		if !entry.Settled && entry.At <= at {
			candidates = append(candidates, candidate{index: i, entry: entry})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].entry.At != candidates[j].entry.At {
			return candidates[i].entry.At < candidates[j].entry.At
		}
		return candidates[i].entry.ListingID < candidates[j].entry.ListingID
	})

	released := make([]LedgerEntry, 0, len(candidates))
	var total int64
	if len(candidates) > 0 {
		entries := g.ledger[owner]
		for _, candidate := range candidates {
			entries[candidate.index].Settled = true
			candidate.entry.Settled = true
			released = append(released, candidate.entry)
			total += candidate.entry.Net
		}
		g.ledger[owner] = entries
		g.balances[owner] += total
	}

	return Settlement{
		Owner:   owner,
		Entries: released,
		Total:   total,
		At:      at,
	}, nil
}

// Ledger returns a copy of the seller ledger.
func (g *StallGuard) Ledger(owner string) []LedgerEntry {
	g.mu.Lock()
	defer g.mu.Unlock()

	entries := g.ledger[owner]
	out := make([]LedgerEntry, len(entries))
	copy(out, entries)
	return out
}

// Deposit funds one account.
func (g *StallGuard) Deposit(account string, amount int64) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if strings.TrimSpace(account) == "" {
		return ErrBadAccount
	}
	if amount <= 0 {
		return ErrBadAmount
	}
	g.balances[account] += amount
	return nil
}

// Balance reads one account balance.
func (g *StallGuard) Balance(account string) int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.balances[account]
}

// Snapshot serialises stalls, listings, balances, the ledger and the snapshot.
func (g *StallGuard) Snapshot() ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	stalls := make([]Stall, 0, len(g.stalls))
	for _, stall := range g.stalls {
		stalls = append(stalls, stall)
	}
	sort.Slice(stalls, func(i, j int) bool {
		return stalls[i].Owner < stalls[j].Owner
	})

	listings := make([]Listing, 0, len(g.listings))
	for _, listing := range g.listings {
		listings = append(listings, listing)
	}
	sort.Slice(listings, func(i, j int) bool {
		if listings[i].Seq != listings[j].Seq {
			return listings[i].Seq < listings[j].Seq
		}
		return listings[i].ID < listings[j].ID
	})

	browse := make([]BrowseEntry, len(g.browse))
	copy(browse, g.browse)

	ledger := make(map[string][]LedgerEntry, len(g.ledger))
	for owner, entries := range g.ledger {
		copied := make([]LedgerEntry, len(entries))
		copy(copied, entries)
		ledger[owner] = copied
	}
	balances := make(map[string]int64, len(g.balances))
	for account, balance := range g.balances {
		balances[account] = balance
	}

	data, err := json.Marshal(guardSnapshot{
		Version:  g.version,
		NextSeq:  g.nextSeq,
		Stalls:   stalls,
		Listings: listings,
		Browse:   browse,
		Ledger:   ledger,
		Balances: balances,
	})
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *StallGuard) Restore(data []byte) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	var snapshot *guardSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil || snapshot == nil {
		return ErrBadSnapshot
	}
	if snapshot.Version < 0 || snapshot.NextSeq < 0 {
		return ErrBadSnapshot
	}

	stalls := make(map[string]Stall, len(snapshot.Stalls))
	for _, stall := range snapshot.Stalls {
		if strings.TrimSpace(stall.Owner) == "" || stall.Slots < 1 ||
			stall.Used < 0 || stall.Used > effectiveCapacity(stall.Slots) {
			return ErrBadSnapshot
		}
		if _, exists := stalls[stall.Owner]; exists {
			return ErrBadSnapshot
		}
		stalls[stall.Owner] = stall
	}

	listings := make(map[string]Listing, len(snapshot.Listings))
	onSaleCounts := make(map[string]int)
	var maxSeq int64
	for _, listing := range snapshot.Listings {
		if strings.TrimSpace(listing.ID) == "" ||
			strings.TrimSpace(listing.Owner) == "" ||
			strings.TrimSpace(listing.Item.ID) == "" ||
			strings.TrimSpace(listing.Item.Name) == "" ||
			listing.Item.Count <= 0 ||
			listing.Price <= 0 ||
			listing.Seq <= 0 {
			return ErrBadSnapshot
		}
		switch listing.State {
		case ListingOnSale, ListingSold, ListingCancelled:
		default:
			return ErrBadSnapshot
		}
		if _, exists := listings[listing.ID]; exists {
			return ErrBadSnapshot
		}
		listings[listing.ID] = listing
		if listing.Seq > maxSeq {
			maxSeq = listing.Seq
		}
		if listing.State == ListingOnSale {
			onSaleCounts[listing.Owner]++
		}
	}
	for owner, stall := range stalls {
		if onSaleCounts[owner] != stall.Used {
			return ErrBadSnapshot
		}
	}
	for _, listing := range listings {
		if listing.State == ListingOnSale {
			if _, open := stalls[listing.Owner]; !open {
				return ErrBadSnapshot
			}
		}
	}

	browse := make([]BrowseEntry, len(snapshot.Browse))
	browseIDs := make(map[string]struct{}, len(snapshot.Browse))
	for i, entry := range snapshot.Browse {
		if strings.TrimSpace(entry.ListingID) == "" {
			return ErrBadSnapshot
		}
		if _, exists := listings[entry.ListingID]; !exists {
			return ErrBadSnapshot
		}
		if _, duplicate := browseIDs[entry.ListingID]; duplicate {
			return ErrBadSnapshot
		}
		browseIDs[entry.ListingID] = struct{}{}
		browse[i] = entry
	}

	ledger := make(map[string][]LedgerEntry, len(snapshot.Ledger))
	for owner, entries := range snapshot.Ledger {
		if strings.TrimSpace(owner) == "" {
			return ErrBadSnapshot
		}
		copied := make([]LedgerEntry, len(entries))
		copy(copied, entries)
		for _, entry := range entries {
			if strings.TrimSpace(entry.ListingID) == "" ||
				entry.Gross <= 0 || entry.Fee < 0 || entry.Net < 0 ||
				entry.Fee+entry.Net != entry.Gross {
				return ErrBadSnapshot
			}
			if _, exists := listings[entry.ListingID]; !exists {
				return ErrBadSnapshot
			}
		}
		ledger[owner] = copied
	}

	balances := make(map[string]int64, len(snapshot.Balances))
	for account, balance := range snapshot.Balances {
		if strings.TrimSpace(account) == "" || balance < 0 {
			return ErrBadSnapshot
		}
		balances[account] = balance
	}

	nextSeq := snapshot.NextSeq
	if nextSeq < maxSeq {
		nextSeq = maxSeq
	}

	g.stalls = stalls
	g.listings = listings
	g.nextSeq = nextSeq
	g.browse = browse
	g.version = snapshot.Version
	g.ledger = ledger
	g.balances = balances
	return nil
}

type guardSnapshot struct {
	Version  int64                    `json:"version"`
	NextSeq  int64                    `json:"next_seq"`
	Stalls   []Stall                  `json:"stalls"`
	Listings []Listing                `json:"listings"`
	Browse   []BrowseEntry            `json:"browse"`
	Ledger   map[string][]LedgerEntry `json:"ledger"`
	Balances map[string]int64         `json:"balances"`
}

func effectiveCapacity(slots int) int {
	return slots + slots/4
}
