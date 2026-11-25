// Package task hands out short lived capability leases and keeps the
// readback view equal to the union of the leases that are still alive.
package task

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

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

// defaultHolder is the holder recorded for Acquire, which takes no holder.
const defaultHolder = "default"

// snapshotVersion is the only snapshot encoding understood by Restore.
const snapshotVersion = 1

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
//
// A single mutex serialises every transition of the lease table, the two
// capability sets and the audit trail. The readback view is derived from
// the same map that backs Active and LeaseByID, so a reclamation is visible
// everywhere atomically.
type AmbientCapLease struct {
	mu sync.Mutex

	maxTTL    time.Duration
	maxLeases int
	maxCaps   int

	bounding  map[Cap]struct{}
	permitted map[Cap]struct{}

	leases map[string]Lease
	audit  []LeaseAudit
	nextID uint64
}

// NewAmbientCapLease returns an empty ledger.
func NewAmbientCapLease(cfg LeaseConfig) *AmbientCapLease {
	if cfg.MaxTTL <= 0 {
		cfg.MaxTTL = DefaultMaxTTL
	}
	if cfg.MaxLeases <= 0 {
		cfg.MaxLeases = DefaultMaxLeases
	}
	if cfg.MaxCapsPerLease <= 0 {
		cfg.MaxCapsPerLease = DefaultMaxCapsPerLease
	}
	return &AmbientCapLease{
		maxTTL:    cfg.MaxTTL,
		maxLeases: cfg.MaxLeases,
		maxCaps:   cfg.MaxCapsPerLease,
		bounding:  newCapSet(cfg.Bounding),
		permitted: newCapSet(cfg.Permitted),
		leases:    make(map[string]Lease),
	}
}

// Acquire grants a lease to the default holder.
func (l *AmbientCapLease) Acquire(op string, caps []Cap, ttl time.Duration) (Lease, error) {
	return l.AcquireFor(defaultHolder, op, caps, ttl)
}

// AcquireFor grants a lease on behalf of one holder.
func (l *AmbientCapLease) AcquireFor(holder, op string, caps []Cap, ttl time.Duration) (Lease, error) {
	if op == "" {
		return Lease{}, ErrBadOp
	}
	if len(caps) == 0 {
		return Lease{}, ErrNoCaps
	}
	if len(caps) > l.maxCaps {
		return Lease{}, ErrTooManyCaps
	}
	if ttl <= 0 || ttl > l.maxTTL {
		return Lease{}, ErrBadTTL
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	// Permitted is checked before bounding: a capability missing from both
	// reports ErrCapNotPermitted, and the whole order is rejected before any
	// state changes.
	for _, cap := range caps {
		if _, ok := l.permitted[cap]; !ok {
			return Lease{}, ErrCapNotPermitted
		}
	}
	for _, cap := range caps {
		if _, ok := l.bounding[cap]; !ok {
			return Lease{}, ErrCapOutOfBounds
		}
	}
	if len(l.leases) >= l.maxLeases {
		return Lease{}, ErrLeaseLimit
	}

	l.nextID++
	granted := Lease{
		ID:      formatLeaseID(l.nextID),
		Op:      op,
		Holder:  holder,
		Caps:    cloneCaps(caps),
		Expires: canonicalNow().Add(ttl),
		State:   LeaseStateActive,
	}
	l.leases[granted.ID] = granted
	return cloneLease(granted), nil
}

// Release returns a lease before it expires.
func (l *AmbientCapLease) Release(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	lease, ok := l.leases[id]
	if !ok {
		return ErrUnknownLease
	}
	l.reclaimLocked(lease, ReasonReleased, canonicalNow())
	return nil
}

// Active returns the leases that are currently alive.
func (l *AmbientCapLease) Active() []Lease {
	l.mu.Lock()
	defer l.mu.Unlock()

	out := make([]Lease, 0, len(l.leases))
	for _, lease := range l.leases {
		out = append(out, cloneLease(lease))
	}
	sortLeasesByID(out)
	return out
}

// Sweep reclaims leases that expired at or before now.
func (l *AmbientCapLease) Sweep(now time.Time) []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	due := make([]Lease, 0)
	for _, lease := range l.leases {
		if !lease.Expires.After(now) {
			due = append(due, lease)
		}
	}
	sortLeasesByID(due)

	ids := make([]string, 0, len(due))
	stamp := canonicalNow()
	for _, lease := range due {
		l.reclaimLocked(lease, ReasonExpired, stamp)
		ids = append(ids, lease.ID)
	}
	return ids
}

// Readback returns the union of the capabilities the live leases hold.
func (l *AmbientCapLease) Readback() []Cap {
	l.mu.Lock()
	defer l.mu.Unlock()

	union := make(map[Cap]struct{})
	for _, lease := range l.leases {
		for _, cap := range lease.Caps {
			union[cap] = struct{}{}
		}
	}
	return sortedCapSet(union)
}

// SetBounding replaces the bounding set.
//
// Leases holding a capability that the new set removes are reclaimed first
// (with ReasonBoundingShrink); only then is the set swapped, so no live
// lease can ever read back outside the bounding set.
func (l *AmbientCapLease) SetBounding(caps []Cap) error {
	if len(caps) == 0 {
		return ErrEmptyBounding
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	for _, cap := range caps {
		if _, ok := l.permitted[cap]; !ok {
			return ErrCapNotPermitted
		}
	}

	next := newCapSet(caps)
	victims := make([]Lease, 0)
	for _, lease := range l.leases {
		for _, cap := range lease.Caps {
			if _, ok := next[cap]; !ok {
				victims = append(victims, lease)
				break
			}
		}
	}
	sortLeasesByID(victims)

	stamp := canonicalNow()
	for _, lease := range victims {
		l.reclaimLocked(lease, ReasonBoundingShrink, stamp)
	}
	l.bounding = next
	return nil
}

// SetPermitted replaces the permitted set.
func (l *AmbientCapLease) SetPermitted(caps []Cap) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.permitted = newCapSet(caps)
	return nil
}

// Reap reclaims leases whose holder is no longer alive.
//
// holders is the set of holders known to be alive. A lease whose Holder is
// absent from that set is reclaimed with ReasonHolderGone.
func (l *AmbientCapLease) Reap(holders []string) []string {
	alive := make(map[string]struct{}, len(holders))
	for _, holder := range holders {
		alive[holder] = struct{}{}
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	gone := make([]Lease, 0)
	for _, lease := range l.leases {
		if _, ok := alive[lease.Holder]; !ok {
			gone = append(gone, lease)
		}
	}
	sortLeasesByID(gone)

	ids := make([]string, 0, len(gone))
	stamp := canonicalNow()
	for _, lease := range gone {
		l.reclaimLocked(lease, ReasonHolderGone, stamp)
		ids = append(ids, lease.ID)
	}
	return ids
}

// Audit returns the reclamation records in the order they were written.
func (l *AmbientCapLease) Audit() []LeaseAudit {
	l.mu.Lock()
	defer l.mu.Unlock()

	out := make([]LeaseAudit, 0, len(l.audit))
	for _, record := range l.audit {
		out = append(out, cloneAudit(record))
	}
	return out
}

// LeaseByID looks one active lease up.
func (l *AmbientCapLease) LeaseByID(id string) (Lease, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	lease, ok := l.leases[id]
	if !ok {
		return Lease{}, false
	}
	return cloneLease(lease), true
}

// Snapshot serialises live leases together with both capability sets.
func (l *AmbientCapLease) Snapshot() ([]byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	active := make([]Lease, 0, len(l.leases))
	for _, lease := range l.leases {
		active = append(active, lease)
	}
	sortLeasesByID(active)

	leases := make([]leaseSnapshot, 0, len(active))
	for _, lease := range active {
		leases = append(leases, toLeaseSnapshot(lease))
	}

	records := make([]auditSnapshot, 0, len(l.audit))
	for _, record := range l.audit {
		records = append(records, auditSnapshot{
			ID:     record.ID,
			Op:     record.Op,
			Reason: record.Reason,
			Caps:   cloneCaps(record.Caps),
			AtNano: record.At.UnixNano(),
		})
	}

	payload := snapshotEnvelope{
		Version:   snapshotVersion,
		NextID:    l.nextID,
		Bounding:  sortedCapSet(l.bounding),
		Permitted: sortedCapSet(l.permitted),
		Leases:    leases,
		Audit:     records,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// Restore rebuilds the ledger from a snapshot produced by Snapshot.
// The replacement is validated in full before any live state is touched, so
// an invalid payload leaves the current ledger untouched.
func (l *AmbientCapLease) Restore(payload []byte) error {
	if len(bytes.TrimSpace(payload)) == 0 {
		return ErrBadSnapshot
	}

	var envelope snapshotEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	if envelope.Version != snapshotVersion {
		return fmt.Errorf("%w: unsupported version %d", ErrBadSnapshot, envelope.Version)
	}
	if envelope.Bounding == nil || envelope.Permitted == nil {
		return fmt.Errorf("%w: missing capability sets", ErrBadSnapshot)
	}

	restored := make(map[string]Lease, len(envelope.Leases))
	var highestID uint64
	for _, item := range envelope.Leases {
		lease := fromLeaseSnapshot(item)
		if lease.ID == "" {
			return fmt.Errorf("%w: lease with empty id", ErrBadSnapshot)
		}
		if len(lease.Caps) == 0 {
			return fmt.Errorf("%w: lease %s has no capabilities", ErrBadSnapshot, lease.ID)
		}
		if _, exists := restored[lease.ID]; exists {
			return fmt.Errorf("%w: duplicate lease id %s", ErrBadSnapshot, lease.ID)
		}
		lease.State = LeaseStateActive
		restored[lease.ID] = lease
		if number := parseLeaseIDNumber(lease.ID); number > highestID {
			highestID = number
		}
	}

	nextID := envelope.NextID
	if highestID > nextID {
		nextID = highestID
	}

	audit := make([]LeaseAudit, 0, len(envelope.Audit))
	for _, item := range envelope.Audit {
		audit = append(audit, LeaseAudit{
			ID:     item.ID,
			Op:     item.Op,
			Reason: item.Reason,
			Caps:   cloneCaps(item.Caps),
			At:     time.Unix(0, item.AtNano),
		})
	}

	l.mu.Lock()
	l.leases = restored
	l.bounding = newCapSet(envelope.Bounding)
	l.permitted = newCapSet(envelope.Permitted)
	l.audit = audit
	l.nextID = nextID
	l.mu.Unlock()
	return nil
}

// reclaimLocked removes one lease and appends exactly one audit record.
// Callers must hold l.mu. It is the single convergence point for Release,
// Sweep, Reap and bounding-shrink reclamation, which makes competing paths
// linearise on the map delete.
func (l *AmbientCapLease) reclaimLocked(lease Lease, reason string, at time.Time) {
	delete(l.leases, lease.ID)
	l.audit = append(l.audit, LeaseAudit{
		ID:     lease.ID,
		Op:     lease.Op,
		Reason: reason,
		Caps:   cloneCaps(lease.Caps),
		At:     at,
	})
}

type leaseSnapshot struct {
	ID        string `json:"id"`
	Op        string `json:"op"`
	Holder    string `json:"holder"`
	Caps      []Cap  `json:"caps"`
	ExpiresAt int64  `json:"expires_unix_nano"`
	State     string `json:"state"`
}

type auditSnapshot struct {
	ID     string `json:"id"`
	Op     string `json:"op"`
	Reason string `json:"reason"`
	Caps   []Cap  `json:"caps"`
	AtNano int64  `json:"at_unix_nano"`
}

type snapshotEnvelope struct {
	Version   int             `json:"version"`
	NextID    uint64          `json:"next_id"`
	Bounding  []Cap           `json:"bounding"`
	Permitted []Cap           `json:"permitted"`
	Leases    []leaseSnapshot `json:"leases"`
	Audit     []auditSnapshot `json:"audit"`
}

func toLeaseSnapshot(lease Lease) leaseSnapshot {
	return leaseSnapshot{
		ID:        lease.ID,
		Op:        lease.Op,
		Holder:    lease.Holder,
		Caps:      cloneCaps(lease.Caps),
		ExpiresAt: lease.Expires.UnixNano(),
		State:     lease.State,
	}
}

func fromLeaseSnapshot(item leaseSnapshot) Lease {
	return Lease{
		ID:      item.ID,
		Op:      item.Op,
		Holder:  item.Holder,
		Caps:    cloneCaps(item.Caps),
		Expires: time.Unix(0, item.ExpiresAt),
		State:   item.State,
	}
}

func newCapSet(caps []Cap) map[Cap]struct{} {
	set := make(map[Cap]struct{}, len(caps))
	for _, cap := range caps {
		set[cap] = struct{}{}
	}
	return set
}

func sortedCapSet(set map[Cap]struct{}) []Cap {
	out := make([]Cap, 0, len(set))
	for cap := range set {
		out = append(out, cap)
	}
	sort.Slice(out, func(i, j int) bool {
		return string(out[i]) < string(out[j])
	})
	return out
}

func cloneCaps(caps []Cap) []Cap {
	return append([]Cap(nil), caps...)
}

func cloneLease(lease Lease) Lease {
	lease.Caps = cloneCaps(lease.Caps)
	return lease
}

func cloneAudit(record LeaseAudit) LeaseAudit {
	record.Caps = cloneCaps(record.Caps)
	return record
}

func sortLeasesByID(leases []Lease) {
	sort.Slice(leases, func(i, j int) bool {
		return leases[i].ID < leases[j].ID
	})
}

func formatLeaseID(number uint64) string {
	return "lease-" + strconv.FormatUint(number, 10)
}

func parseLeaseIDNumber(id string) uint64 {
	raw := strings.TrimPrefix(id, "lease-")
	if raw == id {
		return 0
	}
	number, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0
	}
	return number
}

// canonicalNow returns a wall-clock timestamp stripped of its monotonic
// reading, in the same representation used after a snapshot round trip.
func canonicalNow() time.Time {
	return time.Unix(0, time.Now().UnixNano())
}
