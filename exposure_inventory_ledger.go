// Package task keeps the external exposure inventory: item identity,
// evidence ageing, owner inheritance and merge bookkeeping.
package task

import (
	"errors"
	"time"
)

// errNotImplemented is returned by behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Observation states.
const (
	StateOpen       = "open"
	StateClosed     = "closed"
	StateHistorical = "historical"
)

// Transport protocols.
const (
	ProtoTCP = "tcp"
	ProtoUDP = "udp"
)

// Owner provenance values.
const (
	OwnerInherited = "inherited"
	OwnerManual    = "manual"
)

// Change kinds appended to the audit log.
const (
	ChangeNew        = "new"
	ChangeOwner      = "owner_changed"
	ChangeDowngraded = "downgraded"
	ChangeMerged     = "merged"
	ChangeClosed     = "closed"
)

// Errors returned by the ledger.
var (
	ErrBadObservation = errors.New("observation is invalid")
	ErrBlankOwner     = errors.New("owner is required")
	ErrBadNetblock    = errors.New("netblock must be a valid CIDR")
	ErrNotFound       = errors.New("exposure item not found")
	ErrBadSweep       = errors.New("sweep instant is required")
	ErrBadMerge       = errors.New("merge needs at least two item ids")
	ErrMergeMismatch  = errors.New("items do not share address and protocol")
	ErrItemLimit      = errors.New("exposure item limit reached")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// Observation is one probe result for one exposed endpoint.
type Observation struct {
	Addr        string
	Port        int
	Proto       string
	CertSubject string
	Owner       string
	State       string
	Seen        time.Time
}

// Item is one exposure inventory entry.
type Item struct {
	ID          string
	Addr        string
	Port        int
	Proto       string
	CertSubject string
	Owner       string
	OwnerSource string
	State       string
	FirstSeen   time.Time
	LastSeen    time.Time
	Evidence    int
	Misses      int
	Seq         int64
	MergedFrom  []string
}

// Change is one lifecycle event of an item.
type Change struct {
	ItemID string
	Kind   string
	From   string
	To     string
	Seq    int64
	At     time.Time
}

// Limits bounds the inventory.
type Limits struct {
	TTL      time.Duration
	MaxItems int
}

// Stats is the counter block of the ledger.
type Stats struct {
	Live       int
	Historical int
	Evidence   int
	Merged     int
}

// ExposureLedger owns the live items, the historical items, the inheritance
// table and the change log.
type ExposureLedger struct {
	limits Limits
}

// NewExposureLedger builds an empty ledger.
func NewExposureLedger(limits Limits) *ExposureLedger {
	return &ExposureLedger{limits: limits}
}

// Observe records one probe result and returns the item identity.
func (l *ExposureLedger) Observe(obs Observation) (string, error) {
	return "", errNotImplemented
}

// Attribute overrides the owner of one item.
func (l *ExposureLedger) Attribute(itemID string, owner string) error {
	return errNotImplemented
}

// Inherit registers an owner for a netblock.
func (l *ExposureLedger) Inherit(netblock string, owner string) error {
	return errNotImplemented
}

// Sweep downgrades items that outlived their evidence and returns the changes.
func (l *ExposureLedger) Sweep(now time.Time) ([]Change, error) {
	return nil, errNotImplemented
}

// Merge folds duplicate items of one endpoint into the oldest one.
func (l *ExposureLedger) Merge(ids []string) error {
	return errNotImplemented
}

// Get reads one item, including historical ones.
func (l *ExposureLedger) Get(id string) (Item, bool) {
	return Item{}, false
}

// Items lists the live items ordered by id.
func (l *ExposureLedger) Items() []Item {
	return nil
}

// Changes lists the audit log in append order.
func (l *ExposureLedger) Changes() []Change {
	return nil
}

// Stats reads the counter block.
func (l *ExposureLedger) Stats() Stats {
	return Stats{}
}

// Snapshot serialises items, the inheritance table, changes and counters.
func (l *ExposureLedger) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole ledger state with a snapshot, all or nothing.
func (l *ExposureLedger) Restore(data []byte) error {
	return errNotImplemented
}
