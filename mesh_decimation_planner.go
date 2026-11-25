// Package task plans half edge collapse order for a LOD chain, locks the
// boundary rings and keeps the collapse history resumable across restarts.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"sync"
)

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
	ID         int         `json:"id"`
	Verts      [3]VertexID `json:"verts"`
	MaterialID string      `json:"material_id"`
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

const snapshotVersion = 1

// DecimationPlanner owns the planned mesh, the collapse cursor and the locks.
type DecimationPlanner struct {
	mu sync.Mutex

	options PlannerOptions

	hasPlan        bool
	mesh           Mesh
	plan           Plan
	executed       int
	faceCounts     []int
	collapsedTotal int
	manual         map[EdgeID]struct{}
	locked         map[EdgeID]struct{}
	knownEdges     map[EdgeID]struct{}
	costOrder      []EdgeID
	planDirty      bool
}

// NewDecimationPlanner builds a planner with the given budget.
func NewDecimationPlanner(options PlannerOptions) *DecimationPlanner {
	return &DecimationPlanner{options: options}
}

// Plan validates the mesh and plans one collapse sequence per level.
func (d *DecimationPlanner) Plan(mesh Mesh, levels []LevelSpec) (Plan, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	vertices, edgeSet, err := validateMesh(mesh)
	if err != nil {
		return Plan{}, err
	}
	if err := validateLevels(len(mesh.Faces), levels); err != nil {
		return Plan{}, err
	}

	baseFaces := len(mesh.Faces)
	requiredTotal := 0
	previousFaces := baseFaces
	for _, level := range levels {
		requiredTotal += previousFaces - level.TargetFaces
		previousFaces = level.TargetFaces
	}

	budget := d.options.MaxCollapseBudget
	if budget < 0 {
		budget = 0
	}
	if requiredTotal > budget {
		return Plan{}, ErrCollapseBudget
	}

	manual := make(map[EdgeID]struct{}, len(d.manual))
	locked := make(map[EdgeID]struct{}, len(mesh.Edges))
	for _, edge := range mesh.Edges {
		if edge.Boundary || edge.MaterialSeam || edge.UVSeam {
			locked[edge.ID] = struct{}{}
		}
	}
	for id := range d.manual {
		if _, ok := edgeSet[id]; ok {
			manual[id] = struct{}{}
			locked[id] = struct{}{}
		}
	}

	costOrder := buildCostOrder(mesh, vertices)
	remaining := make([]EdgeID, 0, len(costOrder))
	for _, id := range costOrder {
		if _, isLocked := locked[id]; !isLocked {
			remaining = append(remaining, id)
		}
	}

	levelPlans := make([]LevelPlan, 0, len(levels))
	previousFaces = baseFaces
	for index, level := range levels {
		required := previousFaces - level.TargetFaces
		if len(remaining) < required {
			return Plan{}, ErrInsufficientEdges
		}
		var collapses []EdgeID
		if required > 0 {
			collapses = append([]EdgeID(nil), remaining[:required]...)
			remaining = remaining[required:]
		}
		levelPlans = append(levelPlans, LevelPlan{
			Level:       index,
			TargetFaces: level.TargetFaces,
			Collapses:   collapses,
			Locked:      len(locked),
		})
		previousFaces = level.TargetFaces
	}

	plan := Plan{BaseFaces: baseFaces, Levels: levelPlans}

	d.hasPlan = true
	d.mesh = cloneMesh(mesh)
	d.plan = clonePlan(plan)
	d.executed = 0
	d.faceCounts = []int{baseFaces}
	d.collapsedTotal = 0
	d.manual = manual
	d.locked = locked
	d.knownEdges = edgeSet
	d.costOrder = costOrder
	d.planDirty = false

	return clonePlan(plan), nil
}

// Collapse executes the planned collapses of one level.
func (d *DecimationPlanner) Collapse(level int) (LevelResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.hasPlan || level != d.executed || level < 0 || level >= len(d.plan.Levels) {
		return LevelResult{}, ErrLevelOutOfOrder
	}

	d.reconcileLocked()
	levelPlan := d.plan.Levels[level]

	collapsed := make([]EdgeID, 0, len(levelPlan.Collapses))
	for _, id := range levelPlan.Collapses {
		if _, isLocked := d.locked[id]; isLocked {
			continue
		}
		collapsed = append(collapsed, id)
	}

	budget := d.options.MaxCollapseBudget
	if budget < 0 {
		budget = 0
	}
	if d.collapsedTotal+len(collapsed) > budget {
		return LevelResult{}, ErrCollapseBudget
	}

	faces := d.faceCounts[len(d.faceCounts)-1] - len(collapsed)
	d.faceCounts = append(d.faceCounts, faces)
	d.executed++
	d.collapsedTotal += len(collapsed)

	return LevelResult{
		Level:         level,
		Faces:         faces,
		Collapses:     len(collapsed),
		ReachedTarget: faces == levelPlan.TargetFaces,
	}, nil
}

// LockBoundary locks one more edge so it never collapses.
func (d *DecimationPlanner) LockBoundary(edge EdgeID) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.hasPlan {
		return ErrUnknownEdge
	}
	if _, ok := d.knownEdges[edge]; !ok {
		return ErrUnknownEdge
	}
	if _, ok := d.manual[edge]; ok {
		return nil
	}
	if d.manual == nil {
		d.manual = make(map[EdgeID]struct{})
	}
	d.manual[edge] = struct{}{}
	if _, alreadyLocked := d.locked[edge]; !alreadyLocked {
		d.locked[edge] = struct{}{}
		d.planDirty = true
	}
	return nil
}

// Report summarises the plan and the executed levels.
func (d *DecimationPlanner) Report() DecimateReport {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.hasPlan {
		return DecimateReport{}
	}
	d.reconcileLocked()

	planned := 0
	for _, level := range d.plan.Levels {
		planned += len(level.Collapses)
	}
	return DecimateReport{
		Levels:    len(d.plan.Levels),
		Planned:   planned,
		Collapsed: d.collapsedTotal,
		Locked:    len(d.locked),
	}
}

// Faces returns the face count of one level state, or -1 if not reached.
func (d *DecimationPlanner) Faces(level int) int {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.hasPlan || level < 0 {
		return -1
	}
	if level == 0 {
		return d.faceCounts[0]
	}
	if level <= d.executed && level < len(d.faceCounts) {
		return d.faceCounts[level]
	}
	return -1
}

// PendingLevel returns the next level to collapse.
func (d *DecimationPlanner) PendingLevel() int {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.hasPlan {
		return 0
	}
	return d.executed
}

// LockedEdges lists every locked edge in ascending order.
func (d *DecimationPlanner) LockedEdges() []EdgeID {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.hasPlan {
		return nil
	}
	return sortedEdgeIDs(d.locked)
}

// CollapseOrder returns the planned collapse order of one level.
func (d *DecimationPlanner) CollapseOrder(level int) []EdgeID {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.hasPlan || level < 0 || level >= len(d.plan.Levels) {
		return nil
	}
	d.reconcileLocked()
	return append([]EdgeID(nil), d.plan.Levels[level].Collapses...)
}

// Snapshot serialises the whole planner state.
func (d *DecimationPlanner) Snapshot() ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.reconcileLocked()

	state := snapshotState{
		Planned: d.hasPlan,
		Options: d.options,
	}
	if d.hasPlan {
		state.Mesh = cloneMesh(d.mesh)
		state.Plan = clonePlan(d.plan)
		state.Executed = d.executed
		state.CollapsedTotal = d.collapsedTotal
		state.FaceCounts = append([]int(nil), d.faceCounts...)
		state.ManualLocks = sortedEdgeIDs(d.manual)
		state.Locked = sortedEdgeIDs(d.locked)
		state.CostOrder = append([]EdgeID(nil), d.costOrder...)
	}

	stateBytes, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(stateBytes)
	envelope := snapshotEnvelope{
		Version:  snapshotVersion,
		Checksum: hex.EncodeToString(sum[:]),
		State:    stateBytes,
	}
	return json.Marshal(envelope)
}

// Restore replaces the whole planner state from a snapshot.
func (d *DecimationPlanner) Restore(data []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	var envelope snapshotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return ErrBadSnapshot
	}
	if envelope.Version != snapshotVersion || len(envelope.State) == 0 {
		return ErrBadSnapshot
	}
	sum := sha256.Sum256(envelope.State)
	if hex.EncodeToString(sum[:]) != envelope.Checksum {
		return ErrBadSnapshot
	}

	var state snapshotState
	if err := json.Unmarshal(envelope.State, &state); err != nil {
		return ErrBadSnapshot
	}
	restored, err := restoreState(state)
	if err != nil {
		return ErrBadSnapshot
	}

	d.options = state.Options
	d.hasPlan = restored.hasPlan
	d.mesh = restored.mesh
	d.plan = restored.plan
	d.executed = restored.executed
	d.faceCounts = restored.faceCounts
	d.collapsedTotal = restored.collapsedTotal
	d.manual = restored.manual
	d.locked = restored.locked
	d.knownEdges = restored.knownEdges
	d.costOrder = restored.costOrder
	d.planDirty = false
	return nil
}

// validateMesh checks mesh references before any planning state is touched.
func validateMesh(mesh Mesh) (map[VertexID]Vertex, map[EdgeID]struct{}, error) {
	if len(mesh.Verts) == 0 || len(mesh.Faces) == 0 {
		return nil, nil, ErrEmptyMesh
	}

	vertices := make(map[VertexID]Vertex, len(mesh.Verts))
	for _, vertex := range mesh.Verts {
		vertices[vertex.ID] = vertex
	}

	for _, face := range mesh.Faces {
		a, b, c := face.Verts[0], face.Verts[1], face.Verts[2]
		if a == b || a == c || b == c {
			return nil, nil, ErrBadFace
		}
		for _, id := range face.Verts {
			if _, ok := vertices[id]; !ok {
				return nil, nil, ErrBadFace
			}
		}
	}

	edges := make(map[EdgeID]struct{}, len(mesh.Edges))
	for _, edge := range mesh.Edges {
		if _, duplicate := edges[edge.ID]; duplicate {
			return nil, nil, ErrBadEdge
		}
		if edge.A == edge.B {
			return nil, nil, ErrBadEdge
		}
		if _, ok := vertices[edge.A]; !ok {
			return nil, nil, ErrBadEdge
		}
		if _, ok := vertices[edge.B]; !ok {
			return nil, nil, ErrBadEdge
		}
		edges[edge.ID] = struct{}{}
	}
	return vertices, edges, nil
}

func validateLevels(baseFaces int, levels []LevelSpec) error {
	if len(levels) == 0 {
		return ErrBadTarget
	}

	previousFaces := baseFaces
	for index, level := range levels {
		if level.TargetFaces <= 0 {
			return ErrBadTarget
		}
		if index == 0 {
			if level.TargetFaces > baseFaces {
				return ErrLevelOrder
			}
		} else if level.TargetFaces >= previousFaces {
			return ErrLevelOrder
		}
		previousFaces = level.TargetFaces
	}
	return nil
}

type edgeCost struct {
	id       EdgeID
	distance float64
}

func buildCostOrder(mesh Mesh, vertices map[VertexID]Vertex) []EdgeID {
	costs := make([]edgeCost, 0, len(mesh.Edges))
	for _, edge := range mesh.Edges {
		a := vertices[edge.A].Pos
		b := vertices[edge.B].Pos
		distance := math.Hypot(math.Hypot(a.X-b.X, a.Y-b.Y), a.Z-b.Z)
		if math.IsNaN(distance) {
			distance = math.Inf(1)
		}
		costs = append(costs, edgeCost{id: edge.ID, distance: distance})
	}

	sort.Slice(costs, func(i, j int) bool {
		if costs[i].distance != costs[j].distance {
			return costs[i].distance < costs[j].distance
		}
		return costs[i].id < costs[j].id
	})

	order := make([]EdgeID, len(costs))
	for index, cost := range costs {
		order[index] = cost.id
	}
	return order
}

// reconcileLocked is called under the planner lock. It keeps pending level
// orders consistent with locks added after Plan was called.
func (d *DecimationPlanner) reconcileLocked() {
	if !d.hasPlan || !d.planDirty {
		return
	}
	d.planDirty = false

	consumed := make(map[EdgeID]struct{})
	for index := 0; index < d.executed && index < len(d.plan.Levels); index++ {
		for _, id := range d.plan.Levels[index].Collapses {
			consumed[id] = struct{}{}
		}
	}

	currentFaces := d.faceCounts[len(d.faceCounts)-1]
	for index := d.executed; index < len(d.plan.Levels); index++ {
		level := &d.plan.Levels[index]
		required := currentFaces - level.TargetFaces
		if required < 0 {
			required = 0
		}

		var collapses []EdgeID
		if required > 0 {
			collapses = make([]EdgeID, 0, required)
			for _, id := range d.costOrder {
				if _, isLocked := d.locked[id]; isLocked {
					continue
				}
				if _, used := consumed[id]; used {
					continue
				}
				collapses = append(collapses, id)
				consumed[id] = struct{}{}
				if len(collapses) == required {
					break
				}
			}
		}

		level.Collapses = collapses
		currentFaces -= len(collapses)
	}
	for index := range d.plan.Levels {
		d.plan.Levels[index].Locked = len(d.locked)
	}
}

func cloneMesh(mesh Mesh) Mesh {
	return Mesh{
		Verts: append([]Vertex(nil), mesh.Verts...),
		Faces: append([]Face(nil), mesh.Faces...),
		Edges: append([]Edge(nil), mesh.Edges...),
	}
}

func clonePlan(plan Plan) Plan {
	out := Plan{BaseFaces: plan.BaseFaces}
	if plan.Levels == nil {
		return out
	}
	out.Levels = make([]LevelPlan, len(plan.Levels))
	for index, level := range plan.Levels {
		out.Levels[index] = LevelPlan{
			Level:       level.Level,
			TargetFaces: level.TargetFaces,
			Collapses:   append([]EdgeID(nil), level.Collapses...),
			Locked:      level.Locked,
		}
	}
	return out
}

func sortedEdgeIDs(ids map[EdgeID]struct{}) []EdgeID {
	if len(ids) == 0 {
		return nil
	}
	out := make([]EdgeID, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

type snapshotEnvelope struct {
	Version  int             `json:"version"`
	Checksum string          `json:"checksum"`
	State    json.RawMessage `json:"state"`
}

type snapshotState struct {
	Planned        bool           `json:"planned"`
	Options        PlannerOptions `json:"options"`
	Mesh           Mesh           `json:"mesh"`
	Plan           Plan           `json:"plan"`
	Executed       int            `json:"executed"`
	CollapsedTotal int            `json:"collapsed_total"`
	FaceCounts     []int          `json:"face_counts"`
	ManualLocks    []EdgeID       `json:"manual_locks"`
	Locked         []EdgeID       `json:"locked"`
	CostOrder      []EdgeID       `json:"cost_order"`
}

// restoreState validates a decoded snapshot completely before the caller
// commits it to its live planner.
func restoreState(state snapshotState) (*DecimationPlanner, error) {
	if !state.Planned {
		if state.Executed != 0 || len(state.FaceCounts) != 0 ||
			len(state.Plan.Levels) != 0 || len(state.ManualLocks) != 0 ||
			len(state.Locked) != 0 || len(state.CostOrder) != 0 {
			return nil, ErrBadSnapshot
		}
		return &DecimationPlanner{options: state.Options}, nil
	}

	_, edgeSet, err := validateMesh(state.Mesh)
	if err != nil {
		return nil, ErrBadSnapshot
	}
	baseFaces := len(state.Mesh.Faces)
	if state.Plan.BaseFaces != baseFaces || len(state.Plan.Levels) == 0 {
		return nil, ErrBadSnapshot
	}

	previousFaces := baseFaces
	for index, level := range state.Plan.Levels {
		if level.Level != index || level.TargetFaces <= 0 {
			return nil, ErrBadSnapshot
		}
		if index == 0 {
			if level.TargetFaces > baseFaces {
				return nil, ErrBadSnapshot
			}
		} else if level.TargetFaces >= previousFaces {
			return nil, ErrBadSnapshot
		}
		previousFaces = level.TargetFaces
	}

	if state.Executed < 0 || state.Executed > len(state.Plan.Levels) {
		return nil, ErrBadSnapshot
	}
	if len(state.FaceCounts) != state.Executed+1 || state.FaceCounts[0] != baseFaces {
		return nil, ErrBadSnapshot
	}

	locked := make(map[EdgeID]struct{}, len(state.Locked))
	for index, id := range state.Locked {
		if _, ok := edgeSet[id]; !ok {
			return nil, ErrBadSnapshot
		}
		if index > 0 && id <= state.Locked[index-1] {
			return nil, ErrBadSnapshot
		}
		locked[id] = struct{}{}
	}
	for _, edge := range state.Mesh.Edges {
		if edge.Boundary || edge.MaterialSeam || edge.UVSeam {
			if _, ok := locked[edge.ID]; !ok {
				return nil, ErrBadSnapshot
			}
		}
	}

	manual := make(map[EdgeID]struct{}, len(state.ManualLocks))
	for index, id := range state.ManualLocks {
		if _, ok := locked[id]; !ok {
			return nil, ErrBadSnapshot
		}
		if index > 0 && id <= state.ManualLocks[index-1] {
			return nil, ErrBadSnapshot
		}
		manual[id] = struct{}{}
	}

	used := make(map[EdgeID]struct{})
	for levelIndex, level := range state.Plan.Levels {
		for _, id := range level.Collapses {
			if _, ok := edgeSet[id]; !ok {
				return nil, ErrBadSnapshot
			}
			if _, isLocked := locked[id]; isLocked {
				// Locks added after a level was executed are kept as history,
				// but they must never appear in a pending level.
				if levelIndex >= state.Executed {
					return nil, ErrBadSnapshot
				}
			}
			if _, duplicate := used[id]; duplicate {
				return nil, ErrBadSnapshot
			}
			used[id] = struct{}{}
		}
	}

	collapsedTotal := 0
	for index := 0; index < state.Executed; index++ {
		collapsedTotal += len(state.Plan.Levels[index].Collapses)
		if state.FaceCounts[index+1] != state.FaceCounts[index]-len(state.Plan.Levels[index].Collapses) {
			return nil, ErrBadSnapshot
		}
	}
	if state.CollapsedTotal != collapsedTotal {
		return nil, ErrBadSnapshot
	}

	costOrder := append([]EdgeID(nil), state.CostOrder...)
	if len(costOrder) != len(state.Mesh.Edges) {
		return nil, ErrBadSnapshot
	}
	costSeen := make(map[EdgeID]struct{}, len(costOrder))
	for _, id := range costOrder {
		if _, ok := edgeSet[id]; !ok {
			return nil, ErrBadSnapshot
		}
		if _, duplicate := costSeen[id]; duplicate {
			return nil, ErrBadSnapshot
		}
		costSeen[id] = struct{}{}
	}

	return &DecimationPlanner{
		options:        state.Options,
		hasPlan:        true,
		mesh:           cloneMesh(state.Mesh),
		plan:           clonePlan(state.Plan),
		executed:       state.Executed,
		faceCounts:     append([]int(nil), state.FaceCounts...),
		collapsedTotal: state.CollapsedTotal,
		manual:         manual,
		locked:         locked,
		knownEdges:     edgeSet,
		costOrder:      costOrder,
		planDirty:      false,
	}, nil
}
