// Package task bakes occluder candidates into per cell runtime data, filters
// unusable candidates and keeps a verifiable version hash per cell.
package task

import "errors"

// errNotImplemented marks behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Vec3 is a three component vector.
type Vec3 struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

// CellID identifies one cell of the bake grid.
type CellID struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// Triangle is one candidate occluder triangle in world space.
type Triangle struct {
	A Vec3 `json:"a"`
	B Vec3 `json:"b"`
	C Vec3 `json:"c"`
}

// MaterialInfo describes how one material takes part in occlusion.
type MaterialInfo struct {
	ID          string `json:"id"`
	Transparent bool   `json:"transparent"`
	Dynamic     bool   `json:"dynamic"`
}

// CellData is the baked result of one cell.
type CellData struct {
	Cell      CellID  `json:"cell"`
	Triangles int     `json:"triangles"`
	Coverage  float64 `json:"coverage"`
	Version   string  `json:"version"`
	Discarded bool    `json:"discarded"`
}

// BakeResult reports what one incremental bake pass produced.
type BakeResult struct {
	Baked     []CellID `json:"baked"`
	Discarded []CellID `json:"discarded"`
	Skipped   int      `json:"skipped"`
}

// BakeOptions bounds the bake grid and the batch sizes.
type BakeOptions struct {
	CellSize             float64 `json:"cell_size"`
	MinCoverage          float64 `json:"min_coverage"`
	MaxTrianglesPerBatch int     `json:"max_triangles_per_batch"`
	MaxCellsPerBake      int     `json:"max_cells_per_bake"`
}

// Errors returned by the planner.
var (
	ErrBlankScene       = errors.New("scene id is required")
	ErrBadMaterial      = errors.New("material id is required")
	ErrEmptyBatch       = errors.New("triangle batch is empty")
	ErrBatchTooLarge    = errors.New("triangle batch exceeds the cap")
	ErrBadTriangle      = errors.New("triangle coordinates are not finite")
	ErrUnknownCell      = errors.New("cell is unknown")
	ErrUnknownMaterial  = errors.New("material was never submitted")
	ErrNotBaked         = errors.New("cell has not been baked")
	ErrCellDiscarded    = errors.New("cell was discarded")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

// OccluderBakePlanner owns the candidate grid and the baked cell table.
type OccluderBakePlanner struct {
	options BakeOptions
}

// NewOccluderBakePlanner builds a planner for the given grid.
func NewOccluderBakePlanner(options BakeOptions) *OccluderBakePlanner {
	return &OccluderBakePlanner{options: options}
}

// Submit queues one triangle batch of a single material.
func (p *OccluderBakePlanner) Submit(sceneID string, tris []Triangle, mat MaterialInfo) error {
	return errNotImplemented
}

// BakeCell bakes one cell from its current candidates.
func (p *OccluderBakePlanner) BakeCell(cell CellID) (CellData, error) {
	return CellData{}, errNotImplemented
}

// BakePending bakes the pending cells within the per pass budget.
func (p *OccluderBakePlanner) BakePending(limit int) (BakeResult, error) {
	return BakeResult{}, errNotImplemented
}

// InvalidateByMaterial marks every cell using one material as dirty.
func (p *OccluderBakePlanner) InvalidateByMaterial(matID string) error {
	return errNotImplemented
}

// Cells lists every cell that has candidates, ordered by X then Y.
func (p *OccluderBakePlanner) Cells() []CellID {
	return nil
}

// DirtyCells lists the cells that need a rebake, ordered by X then Y.
func (p *OccluderBakePlanner) DirtyCells() []CellID {
	return nil
}

// DiscardedCells lists the cells dropped by the coverage gate.
func (p *OccluderBakePlanner) DiscardedCells() []CellID {
	return nil
}

// Coverage returns the projected coverage ratio of one cell.
func (p *OccluderBakePlanner) Coverage(cell CellID) (float64, error) {
	return 0, errNotImplemented
}

// VersionHash returns the verifiable version hash of one baked cell.
func (p *OccluderBakePlanner) VersionHash(cell CellID) (string, error) {
	return "", errNotImplemented
}

// CandidateCount returns how many candidates one cell holds.
func (p *OccluderBakePlanner) CandidateCount(cell CellID) int {
	return 0
}

// Snapshot serialises the whole planner state.
func (p *OccluderBakePlanner) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole planner state from a snapshot.
func (p *OccluderBakePlanner) Restore(data []byte) error {
	return errNotImplemented
}
