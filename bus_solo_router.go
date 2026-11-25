// Package task maintains a mix bus tree with mute, solo, bypass and latency
// alignment compensation.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
)

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
	mu       sync.RWMutex
	maxDepth int
	buses    map[BusID]*BusInfo
	children map[BusID][]BusID
	nextID   int
}

const snapshotVersion = 1

type snapshotBus struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Parent    string `json:"parent"`
	Mute      bool   `json:"mute"`
	Solo      bool   `json:"solo"`
	Bypass    bool   `json:"bypass"`
	LatencyMs int    `json:"latencyMs"`
}

type snapshotData struct {
	Version  int           `json:"version"`
	MaxDepth int           `json:"maxDepth"`
	NextID   int           `json:"nextID"`
	Buses    []snapshotBus `json:"buses"`
}

// NewBusSoloRouter builds a router whose tree may grow to maxDepth levels.
func NewBusSoloRouter(maxDepth int) *BusSoloRouter {
	if maxDepth < 0 {
		maxDepth = 0
	}
	r := &BusSoloRouter{
		maxDepth: maxDepth,
		buses:    make(map[BusID]*BusInfo),
		children: make(map[BusID][]BusID),
		nextID:   1,
	}
	r.buses[RootBusID] = &BusInfo{
		ID:     RootBusID,
		Name:   "master",
		Parent: "",
		Depth:  0,
	}
	r.children[RootBusID] = []BusID{}
	return r
}

// CreateBus attaches a child bus below parentID and returns its identifier.
func (r *BusSoloRouter) CreateBus(parentID, name string) (BusID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	name = strings.TrimSpace(name)
	if name == "" {
		return "", ErrBadBusName
	}

	parent, ok := r.buses[BusID(parentID)]
	if !ok {
		return "", ErrBusNotFound
	}
	if parent.Depth+1 > r.maxDepth {
		return "", ErrDepthExceeded
	}
	for _, childID := range r.children[parent.ID] {
		if r.buses[childID].Name == name {
			return "", ErrDuplicateBusName
		}
	}

	id := r.allocateIDLocked()
	info := &BusInfo{
		ID:     id,
		Name:   name,
		Parent: parent.ID,
		Depth:  parent.Depth + 1,
	}
	r.buses[id] = info
	r.children[parent.ID] = append(r.children[parent.ID], id)
	r.children[id] = []BusID{}
	return id, nil
}

func (r *BusSoloRouter) allocateIDLocked() BusID {
	for {
		id := BusID(fmt.Sprintf("bus-%d", r.nextID))
		r.nextID++
		if _, exists := r.buses[id]; !exists {
			return id
		}
	}
}

// DeleteBus removes one bus using the declared policy for its children.
func (r *BusSoloRouter) DeleteBus(busID string, policy DeletePolicy) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	id := BusID(busID)
	info, ok := r.buses[id]
	if !ok {
		return ErrBusNotFound
	}
	if id == RootBusID {
		return ErrRootBus
	}
	switch policy {
	case DeleteReject:
		if len(r.children[id]) != 0 {
			return ErrHasChildren
		}
		r.removeChildLocked(info.Parent, id)
		delete(r.buses, id)
		delete(r.children, id)
		return nil
	case DeleteDetach:
		r.detachAndRemoveLocked(info)
		return nil
	case DeleteCascade:
		r.removeSubtreeLocked(info)
		return nil
	default:
		return ErrBadPolicy
	}
}

func (r *BusSoloRouter) detachAndRemoveLocked(info *BusInfo) {
	parentID := info.Parent
	kids := append([]BusID(nil), r.children[info.ID]...)

	index := r.removeChildLocked(parentID, info.ID)
	if index >= 0 && len(kids) != 0 {
		old := r.children[parentID]
		merged := make([]BusID, 0, len(old)+len(kids))
		merged = append(merged, old...)
		merged = append(merged, kids...)
		sort.SliceStable(merged, func(i, j int) bool {
			return busIDLess(merged[i], merged[j])
		})
		r.children[parentID] = merged
	}
	for _, kid := range kids {
		child := r.buses[kid]
		child.Parent = parentID
		r.updateDepthLocked(kid, info.Depth)
	}

	delete(r.buses, info.ID)
	delete(r.children, info.ID)
}

func (r *BusSoloRouter) removeSubtreeLocked(info *BusInfo) {
	toDelete := make([]BusID, 0)
	var collect func(BusID)
	collect = func(id BusID) {
		for _, child := range r.children[id] {
			collect(child)
		}
		toDelete = append(toDelete, id)
	}
	collect(info.ID)

	r.removeChildLocked(info.Parent, info.ID)
	for _, id := range toDelete {
		delete(r.buses, id)
		delete(r.children, id)
	}
}

func (r *BusSoloRouter) removeChildLocked(parentID, childID BusID) int {
	list := r.children[parentID]
	for i, id := range list {
		if id != childID {
			continue
		}
		r.children[parentID] = append(list[:i], list[i+1:]...)
		return i
	}
	return -1
}

func (r *BusSoloRouter) updateDepthLocked(id BusID, depth int) {
	info := r.buses[id]
	info.Depth = depth
	for _, child := range r.children[id] {
		r.updateDepthLocked(child, depth+1)
	}
}

func busIDLess(a, b BusID) bool {
	na, errA := strconv.Atoi(strings.TrimPrefix(string(a), "bus-"))
	nb, errB := strconv.Atoi(strings.TrimPrefix(string(b), "bus-"))
	switch {
	case errA == nil && errB == nil:
		return na < nb
	case errA == nil:
		return true
	case errB == nil:
		return false
	default:
		return string(a) < string(b)
	}
}

// SetMute stores the own mute flag of one bus.
func (r *BusSoloRouter) SetMute(busID string, v bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	info, ok := r.buses[BusID(busID)]
	if !ok {
		return ErrBusNotFound
	}
	info.Mute = v
	return nil
}

// SetSolo stores the own solo flag of one bus.
func (r *BusSoloRouter) SetSolo(busID string, v bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	info, ok := r.buses[BusID(busID)]
	if !ok {
		return ErrBusNotFound
	}
	info.Solo = v
	return nil
}

// SetBypass skips the effect chain of one bus.
func (r *BusSoloRouter) SetBypass(busID string, v bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	info, ok := r.buses[BusID(busID)]
	if !ok {
		return ErrBusNotFound
	}
	if info.ID == RootBusID {
		return ErrRootBus
	}
	info.Bypass = v
	return nil
}

// SetLatency stores the own processing latency of one bus in milliseconds.
func (r *BusSoloRouter) SetLatency(busID string, ms int) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	info, ok := r.buses[BusID(busID)]
	if !ok {
		return ErrBusNotFound
	}
	if ms < 0 || ms > MaxOwnLatencyMs {
		return ErrLatencyRange
	}
	info.LatencyMs = ms
	return nil
}

// Muted reports the own mute flag of one bus, ignoring inheritance.
func (r *BusSoloRouter) Muted(busID string) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	info, ok := r.buses[BusID(busID)]
	if !ok {
		return false, ErrBusNotFound
	}
	return info.Mute, nil
}

// Audible reports whether the bus reaches the master output.
func (r *BusSoloRouter) Audible(busID string) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	id := BusID(busID)
	if _, ok := r.buses[id]; !ok {
		return false, ErrBusNotFound
	}
	return r.audibleLocked(id), nil
}

func (r *BusSoloRouter) audibleLocked(id BusID) bool {
	// Mute is inherited by the whole subtree and always wins over solo.
	for cur := id; ; {
		info := r.buses[cur]
		if info.Mute {
			return false
		}
		if info.Parent == "" {
			break
		}
		cur = info.Parent
	}

	// Solo only affects the direct siblings at the layer where it is set.
	for cur := id; ; {
		info := r.buses[cur]
		if info.Parent == "" {
			break
		}
		if !info.Solo {
			for _, sibling := range r.children[info.Parent] {
				if sibling != cur && r.buses[sibling].Solo {
					return false
				}
			}
		}
		cur = info.Parent
	}
	return true
}

// Latency returns the alignment compensation of one bus in milliseconds.
func (r *BusSoloRouter) Latency(busID string) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	id := BusID(busID)
	if _, ok := r.buses[id]; !ok {
		return 0, ErrBusNotFound
	}

	paths := make(map[BusID]int, len(r.buses))
	longest := 0
	var walk func(BusID, int)
	walk = func(current BusID, accumulated int) {
		info := r.buses[current]
		if !info.Bypass {
			accumulated += info.LatencyMs
		}
		paths[current] = accumulated
		if accumulated > longest {
			longest = accumulated
		}
		for _, child := range r.children[current] {
			walk(child, accumulated)
		}
	}
	walk(RootBusID, 0)
	return longest - paths[id], nil
}

// Path returns the bus names from the root down to busID.
func (r *BusSoloRouter) Path(busID string) ([]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	id := BusID(busID)
	if _, ok := r.buses[id]; !ok {
		return nil, ErrBusNotFound
	}

	var reversed []string
	for cur := id; ; {
		info := r.buses[cur]
		reversed = append(reversed, info.Name)
		if info.Parent == "" {
			break
		}
		cur = info.Parent
	}
	path := make([]string, len(reversed))
	for i := range reversed {
		path[len(reversed)-1-i] = reversed[i]
	}
	return path, nil
}

// Children returns the children of one bus in creation order.
func (r *BusSoloRouter) Children(busID string) ([]BusID, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	id := BusID(busID)
	if _, ok := r.buses[id]; !ok {
		return nil, ErrBusNotFound
	}
	children := make([]BusID, len(r.children[id]))
	copy(children, r.children[id])
	return children, nil
}

// Bus reads one bus record.
func (r *BusSoloRouter) Bus(busID string) (BusInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	info, ok := r.buses[BusID(busID)]
	if !ok {
		return BusInfo{}, false
	}
	return *info, true
}

// Count returns the number of buses in the tree, root included.
func (r *BusSoloRouter) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.buses)
}

// Snapshot serialises the whole tree and every switch.
func (r *BusSoloRouter) Snapshot() ([]byte, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	snap := snapshotData{
		Version:  snapshotVersion,
		MaxDepth: r.maxDepth,
		NextID:   r.nextID,
		Buses:    make([]snapshotBus, 0, len(r.buses)),
	}
	var walk func(BusID)
	walk = func(id BusID) {
		info := r.buses[id]
		snap.Buses = append(snap.Buses, snapshotBus{
			ID:        string(info.ID),
			Name:      info.Name,
			Parent:    string(info.Parent),
			Mute:      info.Mute,
			Solo:      info.Solo,
			Bypass:    info.Bypass,
			LatencyMs: info.LatencyMs,
		})
		for _, child := range r.children[id] {
			walk(child)
		}
	}
	walk(RootBusID)
	return json.Marshal(snap)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (r *BusSoloRouter) Restore(data []byte) error {
	var snap snapshotData
	if err := json.Unmarshal(data, &snap); err != nil {
		return ErrBadSnapshot
	}
	if snap.Version != snapshotVersion || snap.MaxDepth < 0 || snap.NextID < 1 || len(snap.Buses) == 0 {
		return ErrBadSnapshot
	}

	infos := make(map[BusID]*BusInfo, len(snap.Buses))
	for _, saved := range snap.Buses {
		id := BusID(saved.ID)
		if id == "" {
			return ErrBadSnapshot
		}
		if _, duplicate := infos[id]; duplicate {
			return ErrBadSnapshot
		}
		if saved.Name == "" || strings.TrimSpace(saved.Name) != saved.Name {
			return ErrBadSnapshot
		}
		if saved.LatencyMs < 0 || saved.LatencyMs > MaxOwnLatencyMs {
			return ErrBadSnapshot
		}
		if id == RootBusID {
			if saved.Parent != "" || saved.Name != "master" || saved.Bypass {
				return ErrBadSnapshot
			}
		} else if saved.Parent == "" || BusID(saved.Parent) == id {
			return ErrBadSnapshot
		}
		infos[id] = &BusInfo{
			ID:        id,
			Name:      saved.Name,
			Parent:    BusID(saved.Parent),
			Mute:      saved.Mute,
			Solo:      saved.Solo,
			Bypass:    saved.Bypass,
			LatencyMs: saved.LatencyMs,
		}
	}

	if _, ok := infos[RootBusID]; !ok {
		return ErrBadSnapshot
	}
	for id, info := range infos {
		if id == RootBusID {
			continue
		}
		if _, ok := infos[info.Parent]; !ok {
			return ErrBadSnapshot
		}
	}

	children := make(map[BusID][]BusID, len(infos))
	for _, saved := range snap.Buses {
		id := BusID(saved.ID)
		if id == RootBusID {
			continue
		}
		parentID := BusID(saved.Parent)
		children[parentID] = append(children[parentID], id)
	}
	for _, list := range children {
		sort.SliceStable(list, func(i, j int) bool {
			return busIDLess(list[i], list[j])
		})
	}

	visited := make(map[BusID]bool, len(infos))
	actualDepth := 0
	bad := false
	var walk func(BusID, int)
	walk = func(id BusID, depth int) {
		if visited[id] {
			bad = true
			return
		}
		visited[id] = true
		infos[id].Depth = depth
		if depth > actualDepth {
			actualDepth = depth
		}
		for _, child := range children[id] {
			walk(child, depth+1)
		}
	}
	walk(RootBusID, 0)
	if bad || len(visited) != len(infos) {
		return ErrBadSnapshot
	}

	restoredMaxDepth := snap.MaxDepth
	if actualDepth > restoredMaxDepth {
		restoredMaxDepth = actualDepth
	}
	nextID := snap.NextID
	maxNumericID := 0
	for id := range infos {
		text := string(id)
		if !strings.HasPrefix(text, "bus-") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimPrefix(text, "bus-"))
		if err == nil && n > maxNumericID {
			maxNumericID = n
		}
	}
	if maxNumericID+1 > nextID {
		nextID = maxNumericID + 1
	}
	if nextID < 1 {
		nextID = 1
	}
	for {
		candidate := BusID(fmt.Sprintf("bus-%d", nextID))
		if _, exists := infos[candidate]; !exists {
			break
		}
		nextID++
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.maxDepth = restoredMaxDepth
	r.buses = infos
	r.children = children
	r.nextID = nextID
	return nil
}
