// Package task maintains a mix bus tree with mute, solo, bypass and latency
// alignment compensation.
package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// BusID identifies one bus in the tree.
type BusID string

// RootBusID is the identifier of the bus created together with the router.
const RootBusID BusID = "bus.root"

// MaxOwnLatencyMs caps the own processing latency of a single bus.
const MaxOwnLatencyMs = 2000

// DeletePolicy decides what happens to children when a bus is removed.
type DeletePolicy int

// Supported delete policies.
const (
	DeleteReject DeletePolicy = iota
	DeleteDetach
	DeleteCascade
)

// Errors reported by the router.
var (
	ErrBusNotFound      = errors.New("bus not found")
	ErrBadBusName       = errors.New("bus name is required")
	ErrDuplicateBusName = errors.New("sibling bus name already exists")
	ErrDepthExceeded    = errors.New("bus tree depth exceeded")
	ErrRootBus          = errors.New("root bus cannot be removed")
	ErrHasChildren      = errors.New("bus still has children")
	ErrBadPolicy        = errors.New("unknown delete policy")
	ErrLatencyRange     = errors.New("bus latency out of range")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

// BusInfo is the readable record of one bus.
type BusInfo struct {
	ID        BusID
	Name      string
	Parent    BusID
	Depth     int
	Mute      bool
	Solo      bool
	Bypass    bool
	LatencyMs int
}

// BusSoloRouter owns the bus tree together with its switches.
type BusSoloRouter struct {
	maxDepth int
	buses    map[BusID]*BusInfo
	children map[BusID][]BusID
	nextID   int
}

// NewBusSoloRouter builds a router whose tree may grow to maxDepth levels.
func NewBusSoloRouter(maxDepth int) *BusSoloRouter {
	return &BusSoloRouter{
		maxDepth: maxDepth,
		buses:    make(map[BusID]*BusInfo),
		children: make(map[BusID][]BusID),
	}
}

// CreateBus attaches a child bus below parentID and returns its identifier.
func (r *BusSoloRouter) CreateBus(parentID, name string) (BusID, error) {
	return "", errNotImplemented
}

// DeleteBus removes one bus using the declared policy for its children.
func (r *BusSoloRouter) DeleteBus(busID string, policy DeletePolicy) error {
	return errNotImplemented
}

// SetMute stores the own mute flag of one bus.
func (r *BusSoloRouter) SetMute(busID string, v bool) error {
	return errNotImplemented
}

// SetSolo stores the own solo flag of one bus.
func (r *BusSoloRouter) SetSolo(busID string, v bool) error {
	return errNotImplemented
}

// SetBypass skips the effect chain of one bus.
func (r *BusSoloRouter) SetBypass(busID string, v bool) error {
	return errNotImplemented
}

// SetLatency stores the own processing latency of one bus in milliseconds.
func (r *BusSoloRouter) SetLatency(busID string, ms int) error {
	return errNotImplemented
}

// Muted reports the own mute flag of one bus, ignoring inheritance.
func (r *BusSoloRouter) Muted(busID string) (bool, error) {
	return false, errNotImplemented
}

// Audible reports whether the bus reaches the master output.
func (r *BusSoloRouter) Audible(busID string) (bool, error) {
	return false, errNotImplemented
}

// Latency returns the alignment compensation of one bus in milliseconds.
func (r *BusSoloRouter) Latency(busID string) (int, error) {
	return 0, errNotImplemented
}

// Path returns the bus names from the root down to busID.
func (r *BusSoloRouter) Path(busID string) ([]string, error) {
	return nil, errNotImplemented
}

// Children returns the children of one bus in creation order.
func (r *BusSoloRouter) Children(busID string) ([]BusID, error) {
	return nil, errNotImplemented
}

// Bus reads one bus record.
func (r *BusSoloRouter) Bus(busID string) (BusInfo, bool) {
	return BusInfo{}, false
}

// Count returns the number of buses in the tree, root included.
func (r *BusSoloRouter) Count() int {
	return 0
}

// Snapshot serialises the whole tree and every switch.
func (r *BusSoloRouter) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (r *BusSoloRouter) Restore(data []byte) error {
	return errNotImplemented
}
