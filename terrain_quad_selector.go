// Package task selects the terrain quadtree nodes rendered for one frame from
// screen space error, with neighbour level limits and a node budget.
package task

import (
	"encoding/json"
	"errors"
	"math"
	"sort"
)

// RootQuad is the level zero node covering the whole terrain.
var RootQuad = QuadID{Level: 0, X: 0, Y: 0}

// Errors returned by the selector.
var (
	ErrInvalidOptions     = errors.New("selector options are invalid")
	ErrEmptyErrorTable    = errors.New("geometry error table is empty")
	ErrNonMonotonicError  = errors.New("geometry errors must not decrease with the level")
	ErrNegativeError      = errors.New("geometry error must not be negative")
	ErrErrorLevelMismatch = errors.New("geometry error table does not match the tree depth")
	ErrInvalidBudget      = errors.New("node budget must be positive")
	ErrInvalidView        = errors.New("view is invalid")
	ErrInvalidQuad        = errors.New("quad identifier is out of range")
	ErrNotPinned          = errors.New("quad is not pinned")
	ErrBadSnapshot        = errors.New("snapshot payload is invalid")
)

// Vec2 is a horizontal world position.
type Vec2 struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// GeomError is the geometric error of one level, in world units.
type GeomError struct {
	Level int     `json:"level"`
	Error float64 `json:"error"`
}

// View is the camera state used for one selection.
type View struct {
	Pos            Vec2    `json:"pos"`
	ViewportHeight int     `json:"viewport_height"`
	FovDeg         float64 `json:"fov_deg"`
	TolerancePx    float64 `json:"tolerance_px"`
}

// QuadID addresses one quadtree node.
type QuadID struct {
	Level int `json:"level"`
	X     int `json:"x"`
	Y     int `json:"y"`
}

// Selection is the result of one Select call.
type Selection struct {
	Root    QuadID   `json:"root"`
	Budget  int      `json:"budget"`
	Quads   []QuadID `json:"quads"`
	Missing []QuadID `json:"missing"`
}

// SelectorOptions fixes the tree shape.
type SelectorOptions struct {
	MaxDepth       int     `json:"max_depth"`
	WorldSize      float64 `json:"world_size"`
	ViewportHeight int     `json:"viewport_height"`
	FovDeg         float64 `json:"fov_deg"`
	TolerancePx    float64 `json:"tolerance_px"`
}

// SelectorStats summarises the last selection.
type SelectorStats struct {
	Nodes    int `json:"nodes"`
	Missing  int `json:"missing"`
	MaxLevel int `json:"max_level"`
	Budget   int `json:"budget"`
	Pinned   int `json:"pinned"`
}

// TerrainQuadSelector owns the error table, the pins and the last selection.
type TerrainQuadSelector struct {
	opts   SelectorOptions
	table  []float64
	pins   map[QuadID]bool
	last   Selection
	stats  SelectorStats
	hasSel bool
}

// NewTerrainQuadSelector builds a selector for one terrain size.
func NewTerrainQuadSelector(opts SelectorOptions) (*TerrainQuadSelector, error) {
	if opts.MaxDepth <= 0 || opts.WorldSize <= 0 || opts.ViewportHeight <= 0 ||
		opts.FovDeg <= 0 || opts.FovDeg >= 180 || opts.TolerancePx <= 0 {
		return nil, ErrInvalidOptions
	}
	return &TerrainQuadSelector{opts: opts, pins: map[QuadID]bool{}}, nil
}

func (s *TerrainQuadSelector) validQuad(q QuadID) bool {
	if q.Level < 0 || q.Level > s.opts.MaxDepth || q.X < 0 || q.Y < 0 {
		return false
	}
	n := 1 << uint(q.Level)
	return q.X < n && q.Y < n
}

func validateTable(levels []GeomError, maxDepth int) ([]float64, error) {
	if len(levels) == 0 {
		return nil, ErrEmptyErrorTable
	}
	if len(levels) != maxDepth+1 {
		return nil, ErrErrorLevelMismatch
	}
	table := make([]float64, len(levels))
	for i, ge := range levels {
		if ge.Level != i {
			return nil, ErrErrorLevelMismatch
		}
		if ge.Error < 0 {
			return nil, ErrNegativeError
		}
		if i > 0 && ge.Error < table[i-1] {
			return nil, ErrNonMonotonicError
		}
		table[i] = ge.Error
	}
	return table, nil
}

// SetErrorTable installs the per level geometric error table.
func (s *TerrainQuadSelector) SetErrorTable(levels []GeomError) error {
	table, err := validateTable(levels, s.opts.MaxDepth)
	if err != nil {
		return err
	}
	s.table = table
	return nil
}

// ErrorAt reads the geometric error of one level.
func (s *TerrainQuadSelector) ErrorAt(level int) (float64, error) {
	if s.table == nil {
		return 0, ErrEmptyErrorTable
	}
	if level < 0 || level >= len(s.table) {
		return 0, ErrErrorLevelMismatch
	}
	return s.table[level], nil
}

func lessQuad(a, b QuadID) bool {
	if a.Level != b.Level {
		return a.Level < b.Level
	}
	if a.X != b.X {
		return a.X < b.X
	}
	return a.Y < b.Y
}

func (s *TerrainQuadSelector) screenError(q QuadID, view View, tanHalf float64) float64 {
	side := s.opts.WorldSize / math.Pow(2, float64(q.Level))
	x0, y0 := float64(q.X)*side, float64(q.Y)*side
	x1, y1 := x0+side, y0+side
	dx := math.Max(0, math.Max(x0-view.Pos.X, view.Pos.X-x1))
	dy := math.Max(0, math.Max(y0-view.Pos.Y, view.Pos.Y-y1))
	dist := math.Max(1, math.Hypot(dx, dy))
	return s.table[q.Level] * float64(view.ViewportHeight) / (2 * dist * tanHalf)
}

func childrenOf(q QuadID) [4]QuadID {
	return [4]QuadID{
		{Level: q.Level + 1, X: 2 * q.X, Y: 2 * q.Y},
		{Level: q.Level + 1, X: 2*q.X + 1, Y: 2 * q.Y},
		{Level: q.Level + 1, X: 2 * q.X, Y: 2*q.Y + 1},
		{Level: q.Level + 1, X: 2*q.X + 1, Y: 2*q.Y + 1},
	}
}

// Select builds the node set for one view inside the node budget.
func (s *TerrainQuadSelector) Select(view View, budget int) (Selection, error) {
	if budget <= 0 {
		return Selection{}, ErrInvalidBudget
	}
	if view.ViewportHeight <= 0 || view.FovDeg <= 0 || view.FovDeg >= 180 || view.TolerancePx <= 0 {
		return Selection{}, ErrInvalidView
	}
	if s.table == nil {
		return Selection{}, ErrEmptyErrorTable
	}

	forced := map[QuadID]bool{}
	for p := range s.pins {
		for l := p.Level - 1; l >= 0; l-- {
			shift := uint(p.Level - l)
			forced[QuadID{Level: l, X: p.X >> shift, Y: p.Y >> shift}] = true
		}
	}

	tanHalf := math.Tan(view.FovDeg * math.Pi / 360)
	leaves := []QuadID{}
	var walk func(q QuadID)
	walk = func(q QuadID) {
		switch {
		case s.pins[q]:
			leaves = append(leaves, q)
		case q.Level == s.opts.MaxDepth:
			leaves = append(leaves, q)
		case forced[q] || s.screenError(q, view, tanHalf) > view.TolerancePx:
			for _, c := range childrenOf(q) {
				walk(c)
			}
		default:
			leaves = append(leaves, q)
		}
	}
	walk(RootQuad)

	leaves = s.enforceNeighbours(leaves)
	sort.Slice(leaves, func(i, j int) bool { return lessQuad(leaves[i], leaves[j]) })

	kept := make([]QuadID, 0, len(leaves))
	missing := []QuadID{}
	free := budget
	for _, q := range leaves {
		if s.pins[q] {
			kept = append(kept, q)
		} else if free > 0 {
			free--
			kept = append(kept, q)
		} else {
			missing = append(missing, q)
		}
	}
	sort.Slice(kept, func(i, j int) bool { return lessQuad(kept[i], kept[j]) })

	sel := Selection{Root: RootQuad, Budget: budget, Quads: kept, Missing: missing}
	stats := SelectorStats{
		Nodes:   len(kept),
		Missing: len(missing),
		Budget:  budget,
	}
	for _, q := range kept {
		if q.Level > stats.MaxLevel {
			stats.MaxLevel = q.Level
		}
		if s.pins[q] {
			stats.Pinned++
		}
	}
	s.last = sel
	s.stats = stats
	s.hasSel = true
	return sel, nil
}

// enforceNeighbours splits coarse leaves until no two touching leaves differ
// by more than one level. Pinned leaves keep their level.
func (s *TerrainQuadSelector) enforceNeighbours(leaves []QuadID) []QuadID {
	set := map[QuadID]bool{}
	for _, q := range leaves {
		set[q] = true
	}
	for {
		byLevel := map[int]map[QuadID]bool{}
		for q := range set {
			if byLevel[q.Level] == nil {
				byLevel[q.Level] = map[QuadID]bool{}
			}
			byLevel[q.Level][q] = true
		}
		ordered := make([]QuadID, 0, len(set))
		for q := range set {
			ordered = append(ordered, q)
		}
		sort.Slice(ordered, func(i, j int) bool { return lessQuad(ordered[i], ordered[j]) })

		var target QuadID
		found := false
	outer:
		for _, q := range ordered {
			if s.pins[q] || q.Level >= s.opts.MaxDepth {
				continue
			}
			for lp := q.Level + 2; lp <= s.opts.MaxDepth; lp++ {
				deep := byLevel[lp]
				if len(deep) == 0 {
					continue
				}
				shift := uint(lp - q.Level)
				x0, x1 := q.X<<shift, (q.X+1)<<shift
				y0, y1 := q.Y<<shift, (q.Y+1)<<shift
				for cx := x0 - 1; cx <= x1; cx++ {
					for cy := y0 - 1; cy <= y1; cy++ {
						if deep[QuadID{Level: lp, X: cx, Y: cy}] {
							target, found = q, true
							break outer
						}
					}
				}
			}
		}
		if !found {
			break
		}
		delete(set, target)
		for _, c := range childrenOf(target) {
			set[c] = true
		}
	}
	out := make([]QuadID, 0, len(set))
	for q := range set {
		out = append(out, q)
	}
	return out
}

// Pin keeps one quad selected regardless of the error threshold.
func (s *TerrainQuadSelector) Pin(quad QuadID) error {
	if !s.validQuad(quad) {
		return ErrInvalidQuad
	}
	s.pins[quad] = true
	return nil
}

// Unpin releases one pinned quad.
func (s *TerrainQuadSelector) Unpin(quad QuadID) error {
	if !s.validQuad(quad) {
		return ErrInvalidQuad
	}
	if !s.pins[quad] {
		return ErrNotPinned
	}
	delete(s.pins, quad)
	return nil
}

// Pinned lists the pinned quads, ordered by level then coordinates.
func (s *TerrainQuadSelector) Pinned() []QuadID {
	out := make([]QuadID, 0, len(s.pins))
	for q := range s.pins {
		out = append(out, q)
	}
	sort.Slice(out, func(i, j int) bool { return lessQuad(out[i], out[j]) })
	return out
}

// LastSelection returns the previous result.
func (s *TerrainQuadSelector) LastSelection() Selection {
	return s.last
}

// Stats summarises the last selection.
func (s *TerrainQuadSelector) Stats() SelectorStats {
	return s.stats
}

type snapshotPayload struct {
	Table  []GeomError   `json:"table"`
	Pins   []QuadID      `json:"pins"`
	Last   Selection     `json:"last"`
	Stats  SelectorStats `json:"stats"`
	HasSel bool          `json:"has_sel"`
}

// Snapshot serialises the error table, the pins and the last selection.
func (s *TerrainQuadSelector) Snapshot() ([]byte, error) {
	p := snapshotPayload{
		Pins:   s.Pinned(),
		Last:   s.last,
		Stats:  s.stats,
		HasSel: s.hasSel,
	}
	if s.table != nil {
		p.Table = make([]GeomError, len(s.table))
		for i, e := range s.table {
			p.Table[i] = GeomError{Level: i, Error: e}
		}
	}
	return json.Marshal(p)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (s *TerrainQuadSelector) Restore(data []byte) error {
	var p snapshotPayload
	if err := json.Unmarshal(data, &p); err != nil {
		return ErrBadSnapshot
	}
	var table []float64
	if p.Table != nil {
		t, err := validateTable(p.Table, s.opts.MaxDepth)
		if err != nil {
			return ErrBadSnapshot
		}
		table = t
	}
	pins := map[QuadID]bool{}
	for _, q := range p.Pins {
		if !s.validQuad(q) {
			return ErrBadSnapshot
		}
		pins[q] = true
	}
	s.table = table
	s.pins = pins
	s.last = p.Last
	s.stats = p.Stats
	s.hasSel = p.HasSel
	return nil
}
