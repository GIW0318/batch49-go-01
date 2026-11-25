package task

import (
	"errors"
	"reflect"
	"testing"

	impl "gsb/batch49-go-game-b-03"
)

func newPlanner(budget int) *impl.DecimationPlanner {
	return impl.NewDecimationPlanner(impl.PlannerOptions{MaxCollapseBudget: budget})
}

func levels(targets ...int) []impl.LevelSpec {
	out := make([]impl.LevelSpec, 0, len(targets))
	for _, target := range targets {
		out = append(out, impl.LevelSpec{TargetFaces: target})
	}
	return out
}

func meshWithLengths(lengths []float64) impl.Mesh {
	verts := make([]impl.Vertex, len(lengths)+1)
	pos := 0.0
	verts[0] = impl.Vertex{ID: 0, Pos: impl.Vec3{X: 0}}
	for i, l := range lengths {
		pos += l
		verts[i+1] = impl.Vertex{ID: impl.VertexID(i + 1), Pos: impl.Vec3{X: pos}}
	}
	edges := make([]impl.Edge, len(lengths))
	for i := range lengths {
		edges[i] = impl.Edge{ID: impl.EdgeID(i), A: impl.VertexID(i), B: impl.VertexID(i + 1)}
	}
	faces := make([]impl.Face, 8)
	for i := 0; i < 8; i++ {
		faces[i] = impl.Face{
			ID:    i,
			Verts: [3]impl.VertexID{impl.VertexID(i % len(verts)), impl.VertexID((i + 1) % len(verts)), impl.VertexID((i + 2) % len(verts))},
		}
	}
	return impl.Mesh{Verts: verts, Faces: faces, Edges: edges}
}

func mesh8() impl.Mesh {
	return meshWithLengths([]float64{1, 1, 1, 1, 1, 1, 1, 1})
}

func edgeIDs(edges []impl.EdgeID) []int {
	out := make([]int, 0, len(edges))
	for _, e := range edges {
		out = append(out, int(e))
	}
	return out
}

func TestPlanRejectsEmptyMesh(t *testing.T) {
	p := newPlanner(100)
	if _, err := p.Plan(impl.Mesh{}, levels(4)); !errors.Is(err, impl.ErrEmptyMesh) {
		t.Fatalf("empty mesh error = %v, want ErrEmptyMesh", err)
	}
	noFaces := mesh8()
	noFaces.Faces = nil
	if _, err := p.Plan(noFaces, levels(4)); !errors.Is(err, impl.ErrEmptyMesh) {
		t.Fatalf("mesh without faces error = %v, want ErrEmptyMesh", err)
	}
}

func TestPlanRejectsBadFacesAndEdges(t *testing.T) {
	p := newPlanner(100)
	repeated := mesh8()
	repeated.Faces[0].Verts = [3]impl.VertexID{0, 0, 1}
	if _, err := p.Plan(repeated, levels(6)); !errors.Is(err, impl.ErrBadFace) {
		t.Fatalf("repeated vertex error = %v, want ErrBadFace", err)
	}
	dangling := mesh8()
	dangling.Faces[0].Verts = [3]impl.VertexID{0, 1, 99}
	if _, err := p.Plan(dangling, levels(6)); !errors.Is(err, impl.ErrBadFace) {
		t.Fatalf("dangling face error = %v, want ErrBadFace", err)
	}
	badEdge := mesh8()
	badEdge.Edges[0].B = 99
	if _, err := p.Plan(badEdge, levels(6)); !errors.Is(err, impl.ErrBadEdge) {
		t.Fatalf("dangling edge error = %v, want ErrBadEdge", err)
	}
	loop := mesh8()
	loop.Edges[1].A = loop.Edges[1].B
	if _, err := p.Plan(loop, levels(6)); !errors.Is(err, impl.ErrBadEdge) {
		t.Fatalf("self loop error = %v, want ErrBadEdge", err)
	}
}

func TestPlanRejectsBadLevels(t *testing.T) {
	p := newPlanner(100)
	if _, err := p.Plan(mesh8(), nil); !errors.Is(err, impl.ErrBadTarget) {
		t.Fatalf("empty levels error = %v, want ErrBadTarget", err)
	}
	if _, err := p.Plan(mesh8(), levels(0)); !errors.Is(err, impl.ErrBadTarget) {
		t.Fatalf("zero target error = %v, want ErrBadTarget", err)
	}
	if _, err := p.Plan(mesh8(), levels(9)); !errors.Is(err, impl.ErrLevelOrder) {
		t.Fatalf("target above base error = %v, want ErrLevelOrder", err)
	}
	if _, err := p.Plan(mesh8(), levels(6, 6)); !errors.Is(err, impl.ErrLevelOrder) {
		t.Fatalf("flat targets error = %v, want ErrLevelOrder", err)
	}
	if _, err := p.Plan(mesh8(), levels(6, 7)); !errors.Is(err, impl.ErrLevelOrder) {
		t.Fatalf("increasing targets error = %v, want ErrLevelOrder", err)
	}
}

func TestPlanLocksBoundarySeamEdges(t *testing.T) {
	p := newPlanner(100)
	m := mesh8()
	m.Edges[0].Boundary = true
	m.Edges[1].MaterialSeam = true
	m.Edges[2].UVSeam = true
	plan, err := p.Plan(m, levels(6))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Levels) != 1 {
		t.Fatalf("levels = %d, want 1", len(plan.Levels))
	}
	if plan.Levels[0].Locked != 3 {
		t.Fatalf("locked edges = %d, want 3", plan.Levels[0].Locked)
	}
	if got := edgeIDs(p.LockedEdges()); !reflect.DeepEqual(got, []int{0, 1, 2}) {
		t.Fatalf("locked edge list = %v, want [0 1 2]", got)
	}
	if got := edgeIDs(p.CollapseOrder(0)); !reflect.DeepEqual(got, []int{3, 4}) {
		t.Fatalf("collapse order = %v, want [3 4]", got)
	}
}

func TestPlanOrdersCollapsesByCostThenID(t *testing.T) {
	p := newPlanner(100)
	plan, err := p.Plan(meshWithLengths([]float64{10, 1, 1, 5, 2}), levels(6))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if got := edgeIDs(plan.Levels[0].Collapses); !reflect.DeepEqual(got, []int{1, 2}) {
		t.Fatalf("collapses = %v, want [1 2]", got)
	}
	if got := edgeIDs(p.CollapseOrder(0)); !reflect.DeepEqual(got, []int{1, 2}) {
		t.Fatalf("collapse order = %v, want [1 2]", got)
	}
}

func TestPlanRejectsInsufficientEdges(t *testing.T) {
	p := newPlanner(100)
	if _, err := p.Plan(meshWithLengths([]float64{1, 1}), levels(5)); !errors.Is(err, impl.ErrInsufficientEdges) {
		t.Fatalf("insufficient edges error = %v, want ErrInsufficientEdges", err)
	}
}

func TestPlanRejectsCollapseBudget(t *testing.T) {
	p := newPlanner(2)
	if _, err := p.Plan(mesh8(), levels(6, 4, 2)); !errors.Is(err, impl.ErrCollapseBudget) {
		t.Fatalf("budget error = %v, want ErrCollapseBudget", err)
	}
}

func TestPlanIsDeterministic(t *testing.T) {
	p := newPlanner(100)
	m := meshWithLengths([]float64{3, 1, 2, 1, 4})
	first, err := p.Plan(m, levels(6, 4))
	if err != nil {
		t.Fatalf("first plan: %v", err)
	}
	second, err := p.Plan(m, levels(6, 4))
	if err != nil {
		t.Fatalf("second plan: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("plans differ:\n%+v\n%+v", first, second)
	}
}

func TestCollapseRequiresSequentialLevels(t *testing.T) {
	p := newPlanner(100)
	if _, err := p.Plan(mesh8(), levels(6, 4)); err != nil {
		t.Fatalf("plan: %v", err)
	}
	if _, err := p.Collapse(1); !errors.Is(err, impl.ErrLevelOutOfOrder) {
		t.Fatalf("skip level error = %v, want ErrLevelOutOfOrder", err)
	}
	if _, err := p.Collapse(-1); !errors.Is(err, impl.ErrLevelOutOfOrder) {
		t.Fatalf("negative level error = %v, want ErrLevelOutOfOrder", err)
	}
	if _, err := p.Collapse(0); err != nil {
		t.Fatalf("collapse level 0: %v", err)
	}
	if _, err := p.Collapse(0); !errors.Is(err, impl.ErrLevelOutOfOrder) {
		t.Fatalf("repeated level error = %v, want ErrLevelOutOfOrder", err)
	}
	if _, err := p.Collapse(1); err != nil {
		t.Fatalf("collapse level 1: %v", err)
	}
	if _, err := p.Collapse(2); !errors.Is(err, impl.ErrLevelOutOfOrder) {
		t.Fatalf("past last level error = %v, want ErrLevelOutOfOrder", err)
	}
}

func TestCollapseProducesLevelResult(t *testing.T) {
	p := newPlanner(100)
	if _, err := p.Plan(mesh8(), levels(6, 4)); err != nil {
		t.Fatalf("plan: %v", err)
	}
	first, err := p.Collapse(0)
	if err != nil {
		t.Fatalf("collapse 0: %v", err)
	}
	if first.Level != 0 || first.Faces != 6 || first.Collapses != 2 || !first.ReachedTarget {
		t.Fatalf("level 0 result = %+v", first)
	}
	second, err := p.Collapse(1)
	if err != nil {
		t.Fatalf("collapse 1: %v", err)
	}
	if second.Level != 1 || second.Faces != 4 || second.Collapses != 2 || !second.ReachedTarget {
		t.Fatalf("level 1 result = %+v", second)
	}
}

func TestLockBoundaryGuardsAndExcludes(t *testing.T) {
	p := newPlanner(100)
	if err := p.LockBoundary(0); !errors.Is(err, impl.ErrUnknownEdge) {
		t.Fatalf("lock before plan error = %v, want ErrUnknownEdge", err)
	}
	if _, err := p.Plan(mesh8(), levels(6)); err != nil {
		t.Fatalf("plan: %v", err)
	}
	if err := p.LockBoundary(99); !errors.Is(err, impl.ErrUnknownEdge) {
		t.Fatalf("unknown edge error = %v, want ErrUnknownEdge", err)
	}
	if err := p.LockBoundary(0); err != nil {
		t.Fatalf("lock edge 0: %v", err)
	}
	if err := p.LockBoundary(0); err != nil {
		t.Fatalf("repeated lock: %v", err)
	}
	if got := edgeIDs(p.LockedEdges()); !reflect.DeepEqual(got, []int{0}) {
		t.Fatalf("locked edge list = %v, want [0]", got)
	}
	if _, err := p.Plan(mesh8(), levels(6)); err != nil {
		t.Fatalf("replan: %v", err)
	}
	if got := edgeIDs(p.CollapseOrder(0)); !reflect.DeepEqual(got, []int{1, 2}) {
		t.Fatalf("collapse order after lock = %v, want [1 2]", got)
	}
}

func TestReportCountsPlannedAndCollapsed(t *testing.T) {
	p := newPlanner(100)
	if _, err := p.Plan(mesh8(), levels(6, 4, 2)); err != nil {
		t.Fatalf("plan: %v", err)
	}
	report := p.Report()
	if report.Levels != 3 || report.Planned != 6 || report.Collapsed != 0 || report.Locked != 0 {
		t.Fatalf("initial report = %+v", report)
	}
	if _, err := p.Collapse(0); err != nil {
		t.Fatalf("collapse 0: %v", err)
	}
	report = p.Report()
	if report.Collapsed != 2 || report.Planned != 6 {
		t.Fatalf("report after level 0 = %+v", report)
	}
}

func TestFacesReportsCompletedLevels(t *testing.T) {
	p := newPlanner(100)
	if _, err := p.Plan(mesh8(), levels(6, 4)); err != nil {
		t.Fatalf("plan: %v", err)
	}
	if got := p.Faces(0); got != 8 {
		t.Fatalf("faces at base = %d, want 8", got)
	}
	if got := p.Faces(1); got != -1 {
		t.Fatalf("faces before level 0 = %d, want -1", got)
	}
	if _, err := p.Collapse(0); err != nil {
		t.Fatalf("collapse 0: %v", err)
	}
	if got := p.Faces(1); got != 6 {
		t.Fatalf("faces after level 0 = %d, want 6", got)
	}
	if got := p.Faces(2); got != -1 {
		t.Fatalf("faces before level 1 = %d, want -1", got)
	}
	if _, err := p.Collapse(1); err != nil {
		t.Fatalf("collapse 1: %v", err)
	}
	if got := p.Faces(2); got != 4 {
		t.Fatalf("faces after level 1 = %d, want 4", got)
	}
	if got := p.Faces(3); got != -1 {
		t.Fatalf("faces past last level = %d, want -1", got)
	}
}

func TestSnapshotRestoreResumesPendingLevel(t *testing.T) {
	p := newPlanner(100)
	if _, err := p.Plan(mesh8(), levels(6, 4, 2)); err != nil {
		t.Fatalf("plan: %v", err)
	}
	if _, err := p.Collapse(0); err != nil {
		t.Fatalf("collapse 0: %v", err)
	}
	data, err := p.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload is empty")
	}
	restored := newPlanner(100)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.PendingLevel(); got != 1 {
		t.Fatalf("pending level after restore = %d, want 1", got)
	}
	if got := restored.Faces(0); got != 8 {
		t.Fatalf("base faces after restore = %d, want 8", got)
	}
	if got := restored.Faces(1); got != 6 {
		t.Fatalf("level 0 faces after restore = %d, want 6", got)
	}
	result, err := restored.Collapse(1)
	if err != nil {
		t.Fatalf("resume collapse: %v", err)
	}
	if result.Faces != 4 {
		t.Fatalf("resumed level faces = %d, want 4", result.Faces)
	}
}

func TestRestoreRejectsCorruptPayload(t *testing.T) {
	p := newPlanner(100)
	if _, err := p.Plan(mesh8(), levels(6, 4)); err != nil {
		t.Fatalf("plan: %v", err)
	}
	if err := p.Restore([]byte("{broken")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt payload error = %v, want ErrBadSnapshot", err)
	}
	if got := p.PendingLevel(); got != 0 {
		t.Fatalf("pending level after failed restore = %d, want 0", got)
	}
	if got := p.Faces(0); got != 8 {
		t.Fatalf("base faces after failed restore = %d, want 8", got)
	}
}
