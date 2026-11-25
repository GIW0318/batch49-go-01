// Package task selects the terrain quadtree nodes rendered for one frame from
// screen space error, with neighbour level limits and a node budget.
package task

import "errors"

// errNotImplemented marks behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

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
	opts SelectorOptions
}

// NewTerrainQuadSelector builds a selector for one terrain size.
func NewTerrainQuadSelector(opts SelectorOptions) (*TerrainQuadSelector, error) {
	return &TerrainQuadSelector{opts: opts}, nil
}

// SetErrorTable installs the per level geometric error table.
func (s *TerrainQuadSelector) SetErrorTable(levels []GeomError) error {
	return errNotImplemented
}

// ErrorAt reads the geometric error of one level.
func (s *TerrainQuadSelector) ErrorAt(level int) (float64, error) {
	return 0, errNotImplemented
}

// Select builds the node set for one view inside the node budget.
func (s *TerrainQuadSelector) Select(view View, budget int) (Selection, error) {
	return Selection{}, errNotImplemented
}

// Pin keeps one quad selected regardless of the error threshold.
func (s *TerrainQuadSelector) Pin(quad QuadID) error {
	return errNotImplemented
}

// Unpin releases one pinned quad.
func (s *TerrainQuadSelector) Unpin(quad QuadID) error {
	return errNotImplemented
}

// Pinned lists the pinned quads, ordered by level then coordinates.
func (s *TerrainQuadSelector) Pinned() []QuadID {
	return nil
}

// LastSelection returns the previous result.
func (s *TerrainQuadSelector) LastSelection() Selection {
	return Selection{}
}

// Stats summarises the last selection.
func (s *TerrainQuadSelector) Stats() SelectorStats {
	return SelectorStats{}
}

// Snapshot serialises the error table, the pins and the last selection.
func (s *TerrainQuadSelector) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (s *TerrainQuadSelector) Restore(data []byte) error {
	return errNotImplemented
}
