// Package task hands out short lived capability leases and keeps the
// readback view equal to the union of the leases that are still alive.
package task

import (
	"errors"
	"time"
)

// errNotImplemented marks behaviour that still has to be implemented.
var errNotImplemented = errors.New("not implemented")

// Cap is one ambient capability.
type Cap string

const (
	CapNetConnect Cap = "net.connect"
	CapFSRead     Cap = "fs.read"
	CapFSWrite    Cap = "fs.write"
	CapProcSignal Cap = "proc.signal"
	CapSysAdmin   Cap = "sys.admin"
)

// Lease states and audit reasons.
const (
	LeaseStateActive = "active"

	ReasonReleased       = "released"
	ReasonExpired        = "expired"
	ReasonBoundingShrink = "bounding_shrink"
	ReasonHolderGone     = "holder_gone"
)

// Defaults applied by the constructor when a configuration field is unset.
const (
	DefaultMaxTTL          = time.Hour
	DefaultMaxLeases       = 16
	DefaultMaxCapsPerLease = 4
)

// LeaseConfig carries the hard limits of one ledger.
type LeaseConfig struct {
	MaxTTL          time.Duration
	MaxLeases       int
	MaxCapsPerLease int
	Bounding        []Cap
	Permitted       []Cap
}

// Lease is one temporary capability grant.
type Lease struct {
	ID      string
	Op      string
	Holder  string
	Caps    []Cap
	Expires time.Time
	State   string
}

// LeaseAudit is one reclamation record kept for the operator.
type LeaseAudit struct {
	ID     string
	Op     string
	Reason string
	Caps   []Cap
	At     time.Time
}

var (
	ErrBadOp           = errors.New("operation name is required")
	ErrNoCaps          = errors.New("lease needs at least one capability")
	ErrTooManyCaps     = errors.New("lease exceeds the per lease capability limit")
	ErrBadTTL          = errors.New("lease ttl is outside the allowed range")
	ErrCapNotPermitted = errors.New("capability is not in the permitted set")
	ErrCapOutOfBounds  = errors.New("capability is not in the bounding set")
	ErrLeaseLimit      = errors.New("lease limit reached")
	ErrUnknownLease    = errors.New("lease is not active")
	ErrEmptyBounding   = errors.New("bounding set must not be empty")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// AmbientCapLease is the ledger of temporary capability grants.
type AmbientCapLease struct {
	config LeaseConfig
}

// NewAmbientCapLease returns an empty ledger.
func NewAmbientCapLease(cfg LeaseConfig) *AmbientCapLease {
	return &AmbientCapLease{config: cfg}
}

// Acquire grants a lease to the default holder.
func (l *AmbientCapLease) Acquire(op string, caps []Cap, ttl time.Duration) (Lease, error) {
	return Lease{}, errNotImplemented
}

// AcquireFor grants a lease on behalf of one holder.
func (l *AmbientCapLease) AcquireFor(holder, op string, caps []Cap, ttl time.Duration) (Lease, error) {
	return Lease{}, errNotImplemented
}

// Release returns a lease before it expires.
func (l *AmbientCapLease) Release(id string) error {
	return errNotImplemented
}

// Active returns the leases that are currently alive.
func (l *AmbientCapLease) Active() []Lease {
	return nil
}

// Sweep reclaims leases that expired at or before now.
func (l *AmbientCapLease) Sweep(now time.Time) []string {
	return nil
}

// Readback returns the union of the capabilities the live leases hold.
func (l *AmbientCapLease) Readback() []Cap {
	return nil
}

// SetBounding replaces the bounding set.
func (l *AmbientCapLease) SetBounding(caps []Cap) error {
	return errNotImplemented
}

// SetPermitted replaces the permitted set.
func (l *AmbientCapLease) SetPermitted(caps []Cap) error {
	return errNotImplemented
}

// Reap reclaims leases whose holder is no longer alive.
func (l *AmbientCapLease) Reap(holders []string) []string {
	return nil
}

// Audit returns the reclamation records in the order they were written.
func (l *AmbientCapLease) Audit() []LeaseAudit {
	return nil
}

// LeaseByID looks one active lease up.
func (l *AmbientCapLease) LeaseByID(id string) (Lease, bool) {
	return Lease{}, false
}

// Snapshot serialises live leases together with both capability sets.
func (l *AmbientCapLease) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore rebuilds the ledger from a snapshot produced by Snapshot.
func (l *AmbientCapLease) Restore(payload []byte) error {
	return errNotImplemented
}
