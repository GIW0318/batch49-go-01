// Package task routes overlapping object pairs through collision layers and
// masks, emitting enter, leave and stay events for trigger volumes.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

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

const (
	defaultMaxObjects = 4096
	defaultMaxEvents  = 4096
	snapshotVersion   = 1
)

// Errors returned by the router.
var (
	ErrBadOptions    = errors.New("options are out of range")
	ErrBlankID       = errors.New("object id is required")
	ErrBadKind       = errors.New("object kind must be solid or trigger")
	ErrObjectLimit   = errors.New("object limit reached")
	ErrUnknownObject = errors.New("unknown object")
	ErrBadPair       = errors.New("pair must name two different objects")
	ErrBadSnapshot   = errors.New("snapshot payload is invalid")
)

// Options bounds the object table and the emitted event batch.
type Options struct {
	MaxObjects int
	MaxEvents  int
}

// LayerSpec is the collision layer, mask and kind of one object.
type LayerSpec struct {
	ID    string `json:"id"`
	Layer uint32 `json:"layer"`
	Mask  uint32 `json:"mask"`
	Kind  string `json:"kind"`
}

// Pair names two objects; the router keeps the lexicographically smaller id in A.
type Pair struct {
	A string `json:"a"`
	B string `json:"b"`
}

// Step is one tick of candidate overlaps reported by the broad phase.
type Step struct {
	At       int64  `json:"at"`
	Overlaps []Pair `json:"overlaps,omitempty"`
}

// Event is one router output. Ticks counts consecutive active ticks including
// the enter tick.
type Event struct {
	Kind    string `json:"kind"`
	A       string `json:"a"`
	B       string `json:"b"`
	Trigger bool   `json:"trigger"`
	Ticks   int    `json:"ticks"`
}

type activePair struct {
	A       string `json:"a"`
	B       string `json:"b"`
	Trigger bool   `json:"trigger"`
	Ticks   int    `json:"ticks"`
}

type pairSnapshot struct {
	A       string `json:"a"`
	B       string `json:"b"`
	Trigger bool   `json:"trigger"`
	Ticks   int    `json:"ticks"`
}

type snapshotData struct {
	Version       int            `json:"version"`
	Objects       []LayerSpec    `json:"objects"`
	Pairs         []pairSnapshot `json:"pairs"`
	PendingLeaves []Event        `json:"pending_leaves,omitempty"`
	LastAt        int64          `json:"last_at,omitempty"`
}

// CollisionMaskRouter owns the layer table and the active pair set.
type CollisionMaskRouter struct {
	mu            sync.Mutex
	opts          Options
	objects       map[string]LayerSpec
	pairs         map[Pair]*activePair
	pendingLeaves map[Pair]Event
	lastAt        int64
}

// NewCollisionMaskRouter builds a router, filling defaults for zero options.
func NewCollisionMaskRouter(opts Options) *CollisionMaskRouter {
	if opts.MaxObjects <= 0 {
		opts.MaxObjects = defaultMaxObjects
	}
	if opts.MaxEvents <= 0 {
		opts.MaxEvents = defaultMaxEvents
	}

	return &CollisionMaskRouter{
		opts:          opts,
		objects:       make(map[string]LayerSpec),
		pairs:         make(map[Pair]*activePair),
		pendingLeaves: make(map[Pair]Event),
	}
}

// SetLayer creates or updates one object and its mask.
func (r *CollisionMaskRouter) SetLayer(id string, layer, mask uint32, kind string) error {
	if strings.TrimSpace(id) == "" {
		return ErrBlankID
	}
	if kind != KindSolid && kind != KindTrigger {
		return fmt.Errorf("%w: %q", ErrBadKind, kind)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.objects[id]; !exists && len(r.objects) >= r.opts.MaxObjects {
		return fmt.Errorf("%w: max objects %d", ErrObjectLimit, r.opts.MaxObjects)
	}

	r.objects[id] = LayerSpec{
		ID:    id,
		Layer: layer,
		Mask:  mask,
		Kind:  kind,
	}
	return nil
}

// Layer returns the layer table entry of one object.
func (r *CollisionMaskRouter) Layer(id string) (LayerSpec, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	spec, ok := r.objects[id]
	return spec, ok
}

// RemoveObject drops one object from the layer table and schedules leave
// events for its previously active pairs.
func (r *CollisionMaskRouter) RemoveObject(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	spec, ok := r.objects[id]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownObject, id)
	}
	delete(r.objects, id)

	for key, state := range r.pairs {
		otherID := ""
		switch id {
		case key.A:
			otherID = key.B
		case key.B:
			otherID = key.A
		default:
			continue
		}

		trigger := spec.Kind == KindTrigger
		if other, exists := r.objects[otherID]; exists {
			trigger = trigger || other.Kind == KindTrigger
		} else {
			trigger = trigger || state.Trigger
		}

		r.pendingLeaves[key] = Event{
			Kind:    EventLeave,
			A:       key.A,
			B:       key.B,
			Trigger: trigger,
			Ticks:   state.Ticks,
		}
		delete(r.pairs, key)
	}
	return nil
}

// Pairs reports whether two objects accept each other through masks.
func (r *CollisionMaskRouter) Pairs(a, b string) bool {
	if a == b {
		return false
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	specA, ok := r.objects[a]
	if !ok {
		return false
	}
	specB, ok := r.objects[b]
	if !ok {
		return false
	}
	return masksAccept(specA, specB)
}

// Update consumes one tick of candidate overlaps and returns the diff events.
func (r *CollisionMaskRouter) Update(step Step) ([]Event, error) {
	candidates := make(map[Pair]struct{}, len(step.Overlaps))

	r.mu.Lock()
	defer r.mu.Unlock()

	// Validate the complete candidate batch before changing any state. This
	// keeps a bad tick from leaving behind a half-applied pair set.
	for _, overlap := range step.Overlaps {
		key, err := normalizeCandidate(overlap)
		if err != nil {
			return nil, err
		}
		if _, ok := r.objects[key.A]; !ok {
			return nil, fmt.Errorf("%w: %q", ErrUnknownObject, key.A)
		}
		if _, ok := r.objects[key.B]; !ok {
			return nil, fmt.Errorf("%w: %q", ErrUnknownObject, key.B)
		}
		candidates[key] = struct{}{}
	}

	var events []Event

	// Destruction leaves were committed by RemoveObject, but are emitted by
	// the next valid tick. Clearing them here makes them one-shot even if the
	// output batch is shortened by MaxEvents.
	for _, event := range r.pendingLeaves {
		events = append(events, event)
	}
	r.pendingLeaves = make(map[Pair]Event)

	previous := r.pairs
	next := make(map[Pair]*activePair, len(candidates))

	for key := range candidates {
		specA := r.objects[key.A]
		specB := r.objects[key.B]
		if !masksAccept(specA, specB) {
			continue
		}

		trigger := specA.Kind == KindTrigger || specB.Kind == KindTrigger
		if old, ok := previous[key]; ok {
			state := *old
			state.Trigger = trigger
			state.Ticks++
			next[key] = &state
			events = append(events, Event{
				Kind:    EventStay,
				A:       key.A,
				B:       key.B,
				Trigger: trigger,
				Ticks:   state.Ticks,
			})
			continue
		}

		state := &activePair{
			A:       key.A,
			B:       key.B,
			Trigger: trigger,
			Ticks:   1,
		}
		next[key] = state
		events = append(events, Event{
			Kind:    EventEnter,
			A:       key.A,
			B:       key.B,
			Trigger: trigger,
			Ticks:   1,
		})
	}

	for key, old := range previous {
		if _, remains := next[key]; remains {
			continue
		}

		trigger := old.Trigger
		if specA, ok := r.objects[key.A]; ok {
			trigger = trigger || specA.Kind == KindTrigger
		}
		if specB, ok := r.objects[key.B]; ok {
			trigger = trigger || specB.Kind == KindTrigger
		}

		events = append(events, Event{
			Kind:    EventLeave,
			A:       key.A,
			B:       key.B,
			Trigger: trigger,
			Ticks:   old.Ticks,
		})
	}

	r.pairs = next
	r.lastAt = step.At

	sortEvents(events)
	if r.opts.MaxEvents > 0 && len(events) > r.opts.MaxEvents {
		// Return an independent prefix after all state has been committed.
		events = append([]Event(nil), events[:r.opts.MaxEvents]...)
	}
	return events, nil
}

// Entered reports whether a pair is currently in the active set.
func (r *CollisionMaskRouter) Entered(a, b string) bool {
	key, ok := normalizePair(a, b)
	if !ok {
		return false
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	_, ok = r.pairs[key]
	return ok
}

// ActivePairs returns the active pair set, sorted by ids.
func (r *CollisionMaskRouter) ActivePairs() []Pair {
	r.mu.Lock()
	defer r.mu.Unlock()

	pairs := make([]Pair, 0, len(r.pairs))
	for key := range r.pairs {
		pairs = append(pairs, key)
	}
	sortPairs(pairs)
	return pairs
}

// ObjectIDs returns the registered object ids in sorted order.
func (r *CollisionMaskRouter) ObjectIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	ids := make([]string, 0, len(r.objects))
	for id := range r.objects {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Snapshot serialises the layer table and the active pair set.
func (r *CollisionMaskRouter) Snapshot() ([]byte, error) {
	r.mu.Lock()
	dto := snapshotData{
		Version:       snapshotVersion,
		Objects:       make([]LayerSpec, 0, len(r.objects)),
		Pairs:         make([]pairSnapshot, 0, len(r.pairs)),
		PendingLeaves: make([]Event, 0, len(r.pendingLeaves)),
		LastAt:        r.lastAt,
	}

	ids := make([]string, 0, len(r.objects))
	for id := range r.objects {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		dto.Objects = append(dto.Objects, r.objects[id])
	}

	keys := make([]Pair, 0, len(r.pairs))
	for key := range r.pairs {
		keys = append(keys, key)
	}
	sortPairs(keys)
	for _, key := range keys {
		state := r.pairs[key]
		dto.Pairs = append(dto.Pairs, pairSnapshot{
			A:       state.A,
			B:       state.B,
			Trigger: state.Trigger,
			Ticks:   state.Ticks,
		})
	}

	pendingKeys := make([]Pair, 0, len(r.pendingLeaves))
	for key := range r.pendingLeaves {
		pendingKeys = append(pendingKeys, key)
	}
	sortPairs(pendingKeys)
	for _, key := range pendingKeys {
		dto.PendingLeaves = append(dto.PendingLeaves, r.pendingLeaves[key])
	}
	r.mu.Unlock()

	data, err := json.Marshal(&dto)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	return data, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (r *CollisionMaskRouter) Restore(data []byte) error {
	var dto snapshotData
	if err := json.Unmarshal(data, &dto); err != nil {
		return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	if dto.Version != snapshotVersion {
		return fmt.Errorf("%w: unsupported version %d", ErrBadSnapshot, dto.Version)
	}

	objects := make(map[string]LayerSpec, len(dto.Objects))
	for _, spec := range dto.Objects {
		if strings.TrimSpace(spec.ID) == "" {
			return fmt.Errorf("%w: blank object id", ErrBadSnapshot)
		}
		if spec.Kind != KindSolid && spec.Kind != KindTrigger {
			return fmt.Errorf("%w: bad kind for %q", ErrBadSnapshot, spec.ID)
		}
		if _, duplicate := objects[spec.ID]; duplicate {
			return fmt.Errorf("%w: duplicate object %q", ErrBadSnapshot, spec.ID)
		}
		objects[spec.ID] = spec
	}
	if len(objects) > r.opts.MaxObjects {
		return fmt.Errorf("%w: snapshot has %d objects, limit %d", ErrBadSnapshot, len(objects), r.opts.MaxObjects)
	}

	pairs := make(map[Pair]*activePair, len(dto.Pairs))
	for _, saved := range dto.Pairs {
		key, ok := normalizePair(saved.A, saved.B)
		if !ok {
			return fmt.Errorf("%w: invalid pair %q/%q", ErrBadSnapshot, saved.A, saved.B)
		}
		if _, exists := objects[key.A]; !exists {
			return fmt.Errorf("%w: unknown object %q", ErrBadSnapshot, key.A)
		}
		if _, exists := objects[key.B]; !exists {
			return fmt.Errorf("%w: unknown object %q", ErrBadSnapshot, key.B)
		}
		if saved.Ticks < 1 {
			return fmt.Errorf("%w: bad ticks for %q/%q", ErrBadSnapshot, key.A, key.B)
		}
		if _, duplicate := pairs[key]; duplicate {
			return fmt.Errorf("%w: duplicate pair %q/%q", ErrBadSnapshot, key.A, key.B)
		}

		pairs[key] = &activePair{
			A:       key.A,
			B:       key.B,
			Trigger: saved.Trigger,
			Ticks:   saved.Ticks,
		}
	}

	pendingLeaves := make(map[Pair]Event, len(dto.PendingLeaves))
	for _, event := range dto.PendingLeaves {
		if event.Kind != EventLeave {
			return fmt.Errorf("%w: invalid pending event kind %q", ErrBadSnapshot, event.Kind)
		}
		key, ok := normalizePair(event.A, event.B)
		if !ok {
			return fmt.Errorf("%w: invalid pending pair %q/%q", ErrBadSnapshot, event.A, event.B)
		}
		if event.Ticks < 0 {
			return fmt.Errorf("%w: bad pending ticks for %q/%q", ErrBadSnapshot, key.A, key.B)
		}
		if _, duplicate := pendingLeaves[key]; duplicate {
			return fmt.Errorf("%w: duplicate pending pair %q/%q", ErrBadSnapshot, key.A, key.B)
		}
		event.A = key.A
		event.B = key.B
		event.Kind = EventLeave
		pendingLeaves[key] = event
	}

	r.mu.Lock()
	r.objects = objects
	r.pairs = pairs
	r.pendingLeaves = pendingLeaves
	r.lastAt = dto.LastAt
	r.mu.Unlock()
	return nil
}

func masksAccept(a, b LayerSpec) bool {
	return a.Mask&b.Layer != 0 && b.Mask&a.Layer != 0
}

func normalizeCandidate(pair Pair) (Pair, error) {
	normalized, ok := normalizePair(pair.A, pair.B)
	if !ok {
		if pair.A == pair.B {
			return Pair{}, fmt.Errorf("%w: %q", ErrBadPair, pair.A)
		}
		return Pair{}, fmt.Errorf("%w: %q/%q", ErrBadPair, pair.A, pair.B)
	}
	return normalized, nil
}

func normalizePair(a, b string) (Pair, bool) {
	if a == b {
		return Pair{}, false
	}
	if a > b {
		a, b = b, a
	}
	return Pair{A: a, B: b}, true
}

func sortPairs(pairs []Pair) {
	sort.SliceStable(pairs, func(i, j int) bool {
		if pairs[i].A != pairs[j].A {
			return pairs[i].A < pairs[j].A
		}
		return pairs[i].B < pairs[j].B
	})
}

func sortEvents(events []Event) {
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].A != events[j].A {
			return events[i].A < events[j].A
		}
		return events[i].B < events[j].B
	})
}
