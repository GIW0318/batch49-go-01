// Package task plans half edge collapse order for a LOD chain, locks the
// boundary rings and keeps the collapse history resumable across restarts.
package task

import "errors"

// errNotImplemented marks behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// VertexID identifies one vertex of a mesh.
type VertexID int

// EdgeID identifies one edge of a mesh.
type EdgeID int

// Vec3 is a three component vector.
type Vec3 struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

// Vec2 is a texture coordinate.
type Vec2 struct {
	U float64 `json:"u"`
	V float64 `json:"v"`
}

// Vertex is one mesh vertex with its material and UV island.
type Vertex struct {
	ID         VertexID `json:"id"`
	Pos        Vec3     `json:"pos"`
	MaterialID string   `json:"material_id"`
	UV         Vec2     `json:"uv"`
}

// Face is one triangle of the mesh.
type Face struct {
	ID         int        `json:"id"`
	Verts      [3]VertexID `json:"verts"`
	MaterialID string     `json:"material_id"`
}

// Edge is one collapsible half edge with its lock flags.
type Edge struct {
	ID           EdgeID   `json:"id"`
	A            VertexID `json:"a"`
	B            VertexID `json:"b"`
	Boundary     bool     `json:"boundary"`
	MaterialSeam bool     `json:"material_seam"`
	UVSeam       bool     `json:"uv_seam"`
}

// Mesh is the input of one planning run.
type Mesh struct {
	Verts []Vertex `json:"verts"`
	Faces []Face   `json:"faces"`
	Edges []Edge   `json:"edges"`
}

// LevelSpec is the target face count of one LOD level.
type LevelSpec struct {
	TargetFaces int `json:"target_faces"`
}

// LevelPlan is the collapse sequence planned for one level.
type LevelPlan struct {
	Level       int      `json:"level"`
	TargetFaces int      `json:"target_faces"`
	Collapses   []EdgeID `json:"collapses"`
	Locked      int      `json:"locked"`
}

// Plan is the full collapse plan of one mesh.
type Plan struct {
	BaseFaces int         `json:"base_faces"`
	Levels    []LevelPlan `json:"levels"`
}

// LevelResult reports what one collapsed level produced.
type LevelResult struct {
	Level         int  `json:"level"`
	Faces         int  `json:"faces"`
	Collapses     int  `json:"collapses"`
	ReachedTarget bool `json:"reached_target"`
}

// DecimateReport summarises the planner tables.
type DecimateReport struct {
	Levels    int `json:"levels"`
	Planned   int `json:"planned"`
	Collapsed int `json:"collapsed"`
	Locked    int `json:"locked"`
}

// PlannerOptions bounds one planning run.
type PlannerOptions struct {
	MaxCollapseBudget int `json:"max_collapse_budget"`
}

// Errors returned by the planner.
var (
	ErrEmptyMesh         = errors.New("mesh has no vertices or faces")
	ErrBadFace           = errors.New("face references an invalid vertex set")
	ErrBadEdge           = errors.New("edge references an invalid vertex pair")
	ErrBadTarget         = errors.New("level target must be positive")
	ErrLevelOrder        = errors.New("level targets must decrease strictly")
	ErrInsufficientEdges = errors.New("not enough unlocked edges to reach target")
	ErrCollapseBudget    = errors.New("collapse budget exceeded")
	ErrUnknownEdge       = errors.New("edge is unknown")
	ErrLevelOutOfOrder   = errors.New("level must be collapsed in order")
	ErrBadSnapshot       = errors.New("snapshot payload is invalid")
)

// DecimationPlanner owns the planned mesh, the collapse cursor and the locks.
type DecimationPlanner struct {
	options PlannerOptions
}

// NewDecimationPlanner builds a planner with the given budget.
func NewDecimationPlanner(options PlannerOptions) *DecimationPlanner {
	return &DecimationPlanner{options: options}
}

// Plan validates the mesh and plans one collapse sequence per level.
func (d *DecimationPlanner) Plan(mesh Mesh, levels []LevelSpec) (Plan, error) {
	return Plan{}, errNotImplemented
}

// Collapse executes the planned collapses of one level.
func (d *DecimationPlanner) Collapse(level int) (LevelResult, error) {
	return LevelResult{}, errNotImplemented
}

// LockBoundary locks one more edge so it never collapses.
func (d *DecimationPlanner) LockBoundary(edge EdgeID) error {
	return errNotImplemented
}

// Report summarises the plan and the executed levels.
func (d *DecimationPlanner) Report() DecimateReport {
	return DecimateReport{}
}

// Faces returns the face count of one level state, or -1 if not reached.
func (d *DecimationPlanner) Faces(level int) int {
	return -1
}

// PendingLevel returns the next level to collapse.
func (d *DecimationPlanner) PendingLevel() int {
	return 0
}

// LockedEdges lists every locked edge in ascending order.
func (d *DecimationPlanner) LockedEdges() []EdgeID {
	return nil
}

// CollapseOrder returns the planned collapse order of one level.
func (d *DecimationPlanner) CollapseOrder(level int) []EdgeID {
	return nil
}

// Snapshot serialises the whole planner state.
func (d *DecimationPlanner) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole planner state from a snapshot.
func (d *DecimationPlanner) Restore(data []byte) error {
	return errNotImplemented
}
