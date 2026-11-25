// Package task routes overlapping object pairs through collision layers and
// masks, emitting enter, leave and stay events for trigger volumes.
package task

import (
	"errors"
	"sync"
)

var errNotImplemented = errors.New("not implemented")

// Object kinds.
const (
	KindSolid   = "solid"
	KindTrigger = "trigger"
)

// Event kinds.
const (
	EventEnter = "enter"
	EventLeave = "leave"
	EventStay  = "stay"
)

// Errors returned by the router.
var (
	ErrBadOptions   = errors.New("options are out of range")
	ErrBlankID      = errors.New("object id is required")
	ErrBadKind      = errors.New("object kind must be solid or trigger")
	ErrObjectLimit  = errors.New("object limit reached")
	ErrUnknownObject = errors.New("unknown object")
	ErrBadPair      = errors.New("pair must name two different objects")
	ErrBadSnapshot  = errors.New("snapshot payload is invalid")
)

// Options bounds the object table and the emitted event batch.
type Options struct {
	MaxObjects int
	MaxEvents  int
}

// LayerSpec is the collision layer, mask and kind of one object.
type LayerSpec struct {
	ID    string
	Layer uint32
	Mask  uint32
	Kind  string
}

// Pair names two objects; the router keeps the lexicographically smaller id in A.
type Pair struct {
	A string
	B string
}

// Step is one tick of candidate overlaps reported by the broad phase.
type Step struct {
	At       int64
	Overlaps []Pair
}

// Event is one router output. Ticks counts consecutive ticks of a stay.
type Event struct {
	Kind    string
	A       string
	B       string
	Trigger bool
	Ticks   int
}

type activePair struct {
	A     string
	B     string
	Trigger bool
	Ticks int
}

// CollisionMaskRouter owns the layer table and the active pair set.
type CollisionMaskRouter struct {
	mu      sync.Mutex
	opts    Options
	objects map[string]LayerSpec
	pairs   map[string]*activePair
	lastAt  int64
}

// NewCollisionMaskRouter builds a router, filling defaults for zero options.
func NewCollisionMaskRouter(opts Options) *CollisionMaskRouter {
	return &CollisionMaskRouter{
		opts:    opts,
		objects: make(map[string]LayerSpec),
		pairs:   make(map[string]*activePair),
	}
}

// SetLayer creates or updates one object and its mask.
func (r *CollisionMaskRouter) SetLayer(id string, layer, mask uint32, kind string) error {
	return errNotImplemented
}

// Layer returns the layer table entry of one object.
func (r *CollisionMaskRouter) Layer(id string) (LayerSpec, bool) {
	return LayerSpec{}, false
}

// RemoveObject drops one object from the layer table.
func (r *CollisionMaskRouter) RemoveObject(id string) error {
	return errNotImplemented
}

// Pairs reports whether two objects accept each other through masks.
func (r *CollisionMaskRouter) Pairs(a, b string) bool {
	return false
}

// Update consumes one tick of candidate overlaps and returns the diff events.
func (r *CollisionMaskRouter) Update(step Step) ([]Event, error) {
	return nil, errNotImplemented
}

// Entered reports whether a pair is currently in the active set.
func (r *CollisionMaskRouter) Entered(a, b string) bool {
	return false
}

// ActivePairs returns the active pair set, sorted by ids.
func (r *CollisionMaskRouter) ActivePairs() []Pair {
	return nil
}

// ObjectIDs returns the registered object ids in sorted order.
func (r *CollisionMaskRouter) ObjectIDs() []string {
	return nil
}

// Snapshot serialises the layer table and the active pair set.
func (r *CollisionMaskRouter) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (r *CollisionMaskRouter) Restore(data []byte) error {
	return errNotImplemented
}
