// Package task implements a spatial hash broad phase: cross cell
// registration, deduplicated candidate pairs, budgeted queries and the
// overflow bucket for objects outside the grid bounds.
package task

import (
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"sync"
)

// Grid defaults and hard capacities.
const (
	DefaultBoundMin = -1024.0
	DefaultBoundMax = 1024.0

	// MaxObjects is the hard limit on objects registered in one grid.
	MaxObjects = 4096
	// MaxCellsPerBox is the hard limit on cells touched by a single box.
	// A box whose closed cell range exceeds this value is rejected rather
	// than silently clipped or placed in the overflow bucket.
	MaxCellsPerBox = 1 << 16
)

// Errors returned by the grid.
var (
	ErrBadCellSize    = errors.New("cell size must be positive")
	ErrBlankID        = errors.New("object id is required")
	ErrBadBox         = errors.New("box min must not exceed max")
	ErrDuplicateID    = errors.New("object id already exists")
	ErrUnknownID      = errors.New("unknown object")
	ErrBadBudget      = errors.New("budget must be positive")
	ErrBadBounds      = errors.New("grid bounds are invalid")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
	ErrTooManyObjects = errors.New("object limit reached")
	ErrTooManyCells   = errors.New("box spans too many cells")
)

// Vec3 is a point in world space.
type Vec3 struct {
	X float64
	Y float64
	Z float64
}

// Box is an axis aligned bounding box.
type Box struct {
	Min Vec3
	Max Vec3
}

// Pair names two objects with the smaller id first.
type Pair struct {
	A string
	B string
}

// CellKey identifies one grid cell.
type CellKey struct {
	X int64
	Y int64
	Z int64
}

// HashGrid owns the cell map, the overflow bucket and the object table.
type HashGrid struct {
	mu       sync.RWMutex
	cellSize float64
	bounds   Box
	// maxObjects is kept as state so the capacity remains inspectable by
	// same-package callers; NewHashGrid always applies the public hard cap.
	maxObjects int

	boxes map[string]Box
	cells map[CellKey]map[string]struct{}

	// objCells is the reverse of cells. It makes relocation and removal
	// proportional to the number of cells actually occupied by one object.
	objCells map[string]map[CellKey]struct{}
	overflow map[string]struct{}
}

// NewHashGrid builds a grid with the given cell size and default bounds.
func NewHashGrid(cellSize float64) (*HashGrid, error) {
	if err := validateCellSize(cellSize); err != nil {
		return nil, err
	}
	return &HashGrid{
		cellSize: cellSize,
		bounds: Box{
			Min: Vec3{X: DefaultBoundMin, Y: DefaultBoundMin, Z: DefaultBoundMin},
			Max: Vec3{X: DefaultBoundMax, Y: DefaultBoundMax, Z: DefaultBoundMax},
		},
		maxObjects: MaxObjects,
		boxes:      make(map[string]Box),
		cells:      make(map[CellKey]map[string]struct{}),
		objCells:   make(map[string]map[CellKey]struct{}),
		overflow:   make(map[string]struct{}),
	}, nil
}

// Insert registers one object box, splitting it across every touched cell.
func (g *HashGrid) Insert(id string, box Box) error {
	if strings.TrimSpace(id) == "" {
		return ErrBlankID
	}
	if err := validateBox(box); err != nil {
		return err
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if _, exists := g.boxes[id]; exists {
		return ErrDuplicateID
	}
	if len(g.boxes) >= g.maxObjects {
		return ErrTooManyObjects
	}

	keys, outOfBounds, err := boxCells(box, g.bounds, g.cellSize)
	if err != nil {
		return err
	}

	g.boxes[id] = box
	if outOfBounds {
		g.overflow[id] = struct{}{}
		return nil
	}
	g.registerCellsLocked(id, keys)
	return nil
}

// Move relocates one object, clearing every old cell reference first.
func (g *HashGrid) Move(id string, box Box) error {
	if strings.TrimSpace(id) == "" {
		return ErrBlankID
	}
	if err := validateBox(box); err != nil {
		return err
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if _, exists := g.boxes[id]; !exists {
		return ErrUnknownID
	}

	keys, outOfBounds, err := boxCells(box, g.bounds, g.cellSize)
	if err != nil {
		return err
	}

	// The new registration has been validated before touching the old one,
	// so readers see either the complete old index or the complete new one.
	g.removeRegistrationLocked(id)
	g.boxes[id] = box
	if outOfBounds {
		g.overflow[id] = struct{}{}
		return nil
	}
	g.registerCellsLocked(id, keys)
	return nil
}

// Remove drops one object from the grid and from the overflow bucket.
func (g *HashGrid) Remove(id string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if _, exists := g.boxes[id]; !exists {
		return ErrUnknownID
	}
	g.removeRegistrationLocked(id)
	delete(g.boxes, id)
	return nil
}

// Pairs returns deduplicated candidate pairs, stopping at the budget.
//
// The returned remainder is the number of unique candidate pairs that were
// not returned. Pairs already placed in the result are never counted again.
func (g *HashGrid) Pairs(budget int) ([]Pair, int, error) {
	if budget <= 0 {
		return nil, 0, ErrBadBudget
	}

	g.mu.RLock()
	defer g.mu.RUnlock()

	ids := sortedBoxIDs(g.boxes)
	overflowIDs := make([]string, 0, len(g.overflow))
	for _, id := range ids {
		if _, ok := g.overflow[id]; ok {
			overflowIDs = append(overflowIDs, id)
		}
	}

	capHint := budget
	if capHint > 1024 {
		capHint = 1024
	}
	pairs := make([]Pair, 0, capHint)
	remaining := 0

	for _, a := range ids {
		candidates := make(map[string]struct{})
		if _, isOverflow := g.overflow[a]; isOverflow {
			// Overflow objects only pair with other overflow objects.
			for _, b := range overflowIDs {
				if b > a {
					candidates[b] = struct{}{}
				}
			}
		} else {
			// Union all cells of a, then keep only ids greater than a.
			// This normalises the pair and removes cross-cell duplicates.
			for cell := range g.objCells[a] {
				for b := range g.cells[cell] {
					if b <= a {
						continue
					}
					if _, isOverflow := g.overflow[b]; isOverflow {
						continue
					}
					if _, exists := g.boxes[b]; !exists {
						continue
					}
					candidates[b] = struct{}{}
				}
			}
		}

		if len(pairs) >= budget {
			remaining += len(candidates)
			continue
		}

		partners := make([]string, 0, len(candidates))
		for b := range candidates {
			partners = append(partners, b)
		}
		sort.Strings(partners)
		for _, b := range partners {
			if len(pairs) < budget {
				pairs = append(pairs, Pair{A: a, B: b})
			} else {
				remaining++
			}
		}
	}

	return pairs, remaining, nil
}

// Query returns object ids that share a cell with the query box.
func (g *HashGrid) Query(box Box, budget int) ([]string, int, error) {
	if budget <= 0 {
		return nil, 0, ErrBadBudget
	}
	if err := validateBox(box); err != nil {
		return nil, 0, err
	}

	g.mu.RLock()
	defer g.mu.RUnlock()

	loX, loY, loZ, hiX, hiY, hiZ, err := cellRange(box, g.cellSize)
	if err != nil {
		return nil, 0, err
	}

	candidates := make(map[string]struct{})
	dx, dxOK := spanLength(loX, hiX)
	dy, dyOK := spanLength(loY, hiY)
	dz, dzOK := spanLength(loZ, hiZ)
	total, totalOK := multiplySpan(dx, dxOK, dy, dyOK, dz, dzOK)

	if totalOK && total <= uint64(MaxCellsPerBox) {
		for x := loX; ; x++ {
			for y := loY; ; y++ {
				for z := loZ; ; z++ {
					for id := range g.cells[CellKey{X: x, Y: y, Z: z}] {
						if _, exists := g.boxes[id]; exists {
							candidates[id] = struct{}{}
						}
					}
					if z == hiZ {
						break
					}
				}
				if y == hiY {
					break
				}
			}
			if x == hiX {
				break
			}
		}
	} else {
		// A very large query range is cheaper to answer by visiting the
		// occupied cells than by generating every cell in the range.
		for key, ids := range g.cells {
			if key.X < loX || key.X > hiX ||
				key.Y < loY || key.Y > hiY ||
				key.Z < loZ || key.Z > hiZ {
				continue
			}
			for id := range ids {
				if _, exists := g.boxes[id]; exists {
					candidates[id] = struct{}{}
				}
			}
		}
	}

	ids := make([]string, 0, len(candidates))
	for id := range candidates {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	remaining := 0
	if len(ids) > budget {
		remaining = len(ids) - budget
		ids = ids[:budget]
	}
	return ids, remaining, nil
}

// Box returns the registered box of one object.
func (g *HashGrid) Box(id string) (Box, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	box, ok := g.boxes[id]
	return box, ok
}

// Len returns the number of registered objects.
func (g *HashGrid) Len() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.boxes)
}

// Cells returns the number of occupied cells.
func (g *HashGrid) Cells() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.cells)
}

// CellOf returns the cell coordinates that contain p. Coordinates are
// computed in world space and are valid even for a point outside the bounds;
// registration decides separately whether a box belongs to the overflow.
func (g *HashGrid) CellOf(p Vec3) (int64, int64, int64, error) {
	if !finite(p.X) || !finite(p.Y) || !finite(p.Z) {
		return 0, 0, 0, ErrBadBox
	}

	g.mu.RLock()
	cellSize := g.cellSize
	g.mu.RUnlock()

	x, err := cellCoord(p.X, cellSize)
	if err != nil {
		return 0, 0, 0, err
	}
	y, err := cellCoord(p.Y, cellSize)
	if err != nil {
		return 0, 0, 0, err
	}
	z, err := cellCoord(p.Z, cellSize)
	if err != nil {
		return 0, 0, 0, err
	}
	return x, y, z, nil
}

// SetBounds replaces the grid bounds and re-buckets every object.
func (g *HashGrid) SetBounds(b Box) error {
	if err := validateBounds(b); err != nil {
		return err
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	cells, objCells, overflow, err := buildIndex(g.boxes, b, g.cellSize)
	if err != nil {
		return err
	}

	g.bounds = b
	g.cells = cells
	g.objCells = objCells
	g.overflow = overflow
	return nil
}

// OverflowCount returns the number of objects in the overflow bucket.
func (g *HashGrid) OverflowCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.overflow)
}

type snapshotCell struct {
	X   int64    `json:"x"`
	Y   int64    `json:"y"`
	Z   int64    `json:"z"`
	IDs []string `json:"ids"`
}

type snapshotData struct {
	Version        int            `json:"version"`
	CellSize       float64        `json:"cellSize"`
	Bounds         Box            `json:"bounds"`
	MaxObjects     int            `json:"maxObjects"`
	MaxCellsPerBox int            `json:"maxCellsPerBox"`
	Boxes          map[string]Box `json:"boxes"`
	Overflow       []string       `json:"overflow"`
	Cells          []snapshotCell `json:"cells"`
}

// Snapshot serialises the cell map, the overflow bucket and the boxes.
func (g *HashGrid) Snapshot() ([]byte, error) {
	g.mu.RLock()
	data := snapshotData{
		Version:        1,
		CellSize:       g.cellSize,
		Bounds:         g.bounds,
		MaxObjects:     MaxObjects,
		MaxCellsPerBox: MaxCellsPerBox,
		Boxes:          make(map[string]Box, len(g.boxes)),
		Overflow:       make([]string, 0, len(g.overflow)),
		Cells:          make([]snapshotCell, 0, len(g.cells)),
	}
	for id, box := range g.boxes {
		data.Boxes[id] = box
	}
	for _, id := range sortedBoxIDs(g.overflow) {
		data.Overflow = append(data.Overflow, id)
	}

	keys := make([]CellKey, 0, len(g.cells))
	for key := range g.cells {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].X != keys[j].X {
			return keys[i].X < keys[j].X
		}
		if keys[i].Y != keys[j].Y {
			return keys[i].Y < keys[j].Y
		}
		return keys[i].Z < keys[j].Z
	})
	for _, key := range keys {
		ids := make([]string, 0, len(g.cells[key]))
		for id := range g.cells[key] {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		data.Cells = append(data.Cells, snapshotCell{
			X: key.X, Y: key.Y, Z: key.Z, IDs: ids,
		})
	}
	g.mu.RUnlock()

	payload, err := json.Marshal(data)
	if err != nil {
		return nil, ErrBadSnapshot
	}
	return payload, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *HashGrid) Restore(data []byte) error {
	var snap snapshotData
	if err := json.Unmarshal(data, &snap); err != nil {
		return ErrBadSnapshot
	}
	if snap.Version != 1 {
		return ErrBadSnapshot
	}
	if err := validateCellSize(snap.CellSize); err != nil {
		return ErrBadSnapshot
	}
	if err := validateBounds(snap.Bounds); err != nil {
		return ErrBadSnapshot
	}
	if len(snap.Boxes) > MaxObjects {
		return ErrBadSnapshot
	}

	boxes := make(map[string]Box, len(snap.Boxes))
	for id, box := range snap.Boxes {
		if strings.TrimSpace(id) == "" {
			return ErrBadSnapshot
		}
		if err := validateBox(box); err != nil {
			return ErrBadSnapshot
		}
		boxes[id] = box
	}

	cells, objCells, overflow, err := buildIndex(boxes, snap.Bounds, snap.CellSize)
	if err != nil {
		return ErrBadSnapshot
	}
	if !snapshotIndexMatches(snap, boxes, cells, overflow) {
		return ErrBadSnapshot
	}

	g.mu.Lock()
	g.cellSize = snap.CellSize
	g.bounds = snap.Bounds
	g.boxes = boxes
	g.cells = cells
	g.objCells = objCells
	g.overflow = overflow
	g.mu.Unlock()
	return nil
}

func (g *HashGrid) registerCellsLocked(id string, keys []CellKey) {
	set := make(map[CellKey]struct{}, len(keys))
	for _, key := range keys {
		cell := g.cells[key]
		if cell == nil {
			cell = make(map[string]struct{})
			g.cells[key] = cell
		}
		cell[id] = struct{}{}
		set[key] = struct{}{}
	}
	g.objCells[id] = set
}

func (g *HashGrid) removeRegistrationLocked(id string) {
	for key := range g.objCells[id] {
		cell := g.cells[key]
		if cell == nil {
			continue
		}
		delete(cell, id)
		if len(cell) == 0 {
			delete(g.cells, key)
		}
	}
	delete(g.objCells, id)
	delete(g.overflow, id)
}

func buildIndex(boxes map[string]Box, bounds Box, cellSize float64) (
	map[CellKey]map[string]struct{},
	map[string]map[CellKey]struct{},
	map[string]struct{},
	error,
) {
	cells := make(map[CellKey]map[string]struct{})
	objCells := make(map[string]map[CellKey]struct{}, len(boxes))
	overflow := make(map[string]struct{})

	for _, id := range sortedBoxIDs(boxes) {
		box := boxes[id]
		keys, outOfBounds, err := boxCells(box, bounds, cellSize)
		if err != nil {
			return nil, nil, nil, err
		}
		if outOfBounds {
			overflow[id] = struct{}{}
			continue
		}
		set := make(map[CellKey]struct{}, len(keys))
		for _, key := range keys {
			cell := cells[key]
			if cell == nil {
				cell = make(map[string]struct{})
				cells[key] = cell
			}
			cell[id] = struct{}{}
			set[key] = struct{}{}
		}
		objCells[id] = set
	}
	return cells, objCells, overflow, nil
}

func snapshotIndexMatches(
	snap snapshotData,
	boxes map[string]Box,
	cells map[CellKey]map[string]struct{},
	overflow map[string]struct{},
) bool {
	if len(snap.Overflow) != len(overflow) {
		return false
	}
	seenOverflow := make(map[string]struct{}, len(snap.Overflow))
	for _, id := range snap.Overflow {
		if _, exists := boxes[id]; !exists {
			return false
		}
		if _, ok := overflow[id]; !ok {
			return false
		}
		if _, duplicate := seenOverflow[id]; duplicate {
			return false
		}
		seenOverflow[id] = struct{}{}
	}

	if len(snap.Cells) != len(cells) {
		return false
	}
	seenCells := make(map[CellKey]struct{}, len(snap.Cells))
	for _, entry := range snap.Cells {
		key := CellKey{X: entry.X, Y: entry.Y, Z: entry.Z}
		if _, duplicate := seenCells[key]; duplicate {
			return false
		}
		seenCells[key] = struct{}{}

		want, exists := cells[key]
		if !exists || len(entry.IDs) != len(want) {
			return false
		}
		seenIDs := make(map[string]struct{}, len(entry.IDs))
		for _, id := range entry.IDs {
			if _, duplicate := seenIDs[id]; duplicate {
				return false
			}
			seenIDs[id] = struct{}{}
			if _, ok := want[id]; !ok {
				return false
			}
		}
	}
	return true
}

func validateCellSize(cellSize float64) error {
	if math.IsNaN(cellSize) || math.IsInf(cellSize, 0) || cellSize <= 0 {
		return ErrBadCellSize
	}
	return nil
}

func validateBox(box Box) error {
	if !finite(box.Min.X) || !finite(box.Min.Y) || !finite(box.Min.Z) ||
		!finite(box.Max.X) || !finite(box.Max.Y) || !finite(box.Max.Z) {
		return ErrBadBox
	}
	if box.Min.X > box.Max.X || box.Min.Y > box.Max.Y || box.Min.Z > box.Max.Z {
		return ErrBadBox
	}
	return nil
}

func validateBounds(box Box) error {
	if err := validateBox(box); err != nil {
		return ErrBadBounds
	}
	return nil
}

func finite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

// boxCells returns the closed cell range touched by box. The bool reports
// that the box is not fully inside the grid bounds and must use overflow.
func boxCells(box Box, bounds Box, cellSize float64) ([]CellKey, bool, error) {
	if err := validateBox(box); err != nil {
		return nil, false, err
	}

	loX, loY, loZ, hiX, hiY, hiZ, err := cellRange(box, cellSize)
	if err != nil {
		return nil, false, err
	}
	dx, dxOK := spanLength(loX, hiX)
	dy, dyOK := spanLength(loY, hiY)
	dz, dzOK := spanLength(loZ, hiZ)
	total, totalOK := multiplySpan(dx, dxOK, dy, dyOK, dz, dzOK)
	if !totalOK || total > uint64(MaxCellsPerBox) {
		return nil, false, ErrTooManyCells
	}

	keys := make([]CellKey, 0, int(total))
	for x := loX; ; x++ {
		for y := loY; ; y++ {
			for z := loZ; ; z++ {
				keys = append(keys, CellKey{X: x, Y: y, Z: z})
				if z == hiZ {
					break
				}
			}
			if y == hiY {
				break
			}
		}
		if x == hiX {
			break
		}
	}

	outOfBounds := box.Min.X < bounds.Min.X || box.Max.X > bounds.Max.X ||
		box.Min.Y < bounds.Min.Y || box.Max.Y > bounds.Max.Y ||
		box.Min.Z < bounds.Min.Z || box.Max.Z > bounds.Max.Z
	return keys, outOfBounds, nil
}

func cellRange(box Box, cellSize float64) (int64, int64, int64, int64, int64, int64, error) {
	loX, err := cellCoord(box.Min.X, cellSize)
	if err != nil {
		return 0, 0, 0, 0, 0, 0, err
	}
	loY, err := cellCoord(box.Min.Y, cellSize)
	if err != nil {
		return 0, 0, 0, 0, 0, 0, err
	}
	loZ, err := cellCoord(box.Min.Z, cellSize)
	if err != nil {
		return 0, 0, 0, 0, 0, 0, err
	}
	hiX, err := cellCoord(box.Max.X, cellSize)
	if err != nil {
		return 0, 0, 0, 0, 0, 0, err
	}
	hiY, err := cellCoord(box.Max.Y, cellSize)
	if err != nil {
		return 0, 0, 0, 0, 0, 0, err
	}
	hiZ, err := cellCoord(box.Max.Z, cellSize)
	if err != nil {
		return 0, 0, 0, 0, 0, 0, err
	}
	return loX, loY, loZ, hiX, hiY, hiZ, nil
}

func cellCoord(v, cellSize float64) (int64, error) {
	if !finite(v) {
		return 0, ErrBadBox
	}
	q := math.Floor(v / cellSize)
	if math.IsNaN(q) || math.IsInf(q, 0) ||
		q < float64(math.MinInt64) || q > float64(math.MaxInt64) {
		return 0, ErrBadBox
	}
	return int64(q), nil
}

// spanLength returns hi-lo+1 without overflowing signed integers.
func spanLength(lo, hi int64) (uint64, bool) {
	if hi < lo {
		return 0, false
	}
	distance := uint64(hi) - uint64(lo)
	if distance == math.MaxUint64 {
		return 0, false
	}
	return distance + 1, true
}

func multiplySpan(dx uint64, dxOK bool, dy uint64, dyOK bool, dz uint64, dzOK bool) (uint64, bool) {
	if !dxOK || !dyOK || !dzOK {
		return 0, false
	}
	if dx != 0 && dy > math.MaxUint64/dx {
		return 0, false
	}
	xy := dx * dy
	if xy != 0 && dz > math.MaxUint64/xy {
		return 0, false
	}
	return xy * dz, true
}

func sortedBoxIDs[V any](m map[string]V) []string {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
