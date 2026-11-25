package task

import (
	"errors"
	"math"
	"testing"

	impl "gsb/batch49-go-game-b-02"
)

func newMerger(maxVerts int) *impl.StaticBatchMerger {
	return impl.NewStaticBatchMerger(impl.MergerOptions{MaxVerticesPerBatch: maxVerts})
}

func identity() impl.Transform {
	return impl.Transform{M: [3][3]float64{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}}}
}

func layout32() impl.VertexLayout {
	return impl.VertexLayout{Stride: 32}
}

func quadMesh(id, material string) impl.SubMesh {
	return impl.SubMesh{
		ID:         id,
		MaterialID: material,
		Layout:     layout32(),
		Positions: []impl.Vec3{
			{X: 0, Y: 0, Z: 0},
			{X: 1, Y: 0, Z: 0},
			{X: 1, Y: 1, Z: 0},
			{X: 0, Y: 1, Z: 0},
		},
		Normals: []impl.Vec3{{Z: 1}, {Z: 1}, {Z: 1}, {Z: 1}},
		Indices: []uint32{0, 1, 2, 0, 2, 3},
	}
}

func triMesh(id, material string) impl.SubMesh {
	return impl.SubMesh{
		ID:         id,
		MaterialID: material,
		Layout:     layout32(),
		Positions:  []impl.Vec3{{X: 1}, {Y: 1}, {Z: 1}},
		Normals:    []impl.Vec3{{X: 1}, {Y: 1}, {Z: 1}},
		Indices:    []uint32{0, 1, 2},
	}
}

func mustAdd(t *testing.T, m *impl.StaticBatchMerger, group string, mesh impl.SubMesh) {
	t.Helper()
	if err := m.AddSubMesh(group, mesh, identity()); err != nil {
		t.Fatalf("add %s to %s: %v", mesh.ID, group, err)
	}
}

func mustBuild(t *testing.T, m *impl.StaticBatchMerger, group string) impl.Batch {
	t.Helper()
	batch, err := m.Build(group)
	if err != nil {
		t.Fatalf("build %s: %v", group, err)
	}
	return batch
}

func closeTo(got, want float64) bool {
	return math.Abs(got-want) < 1e-9
}

func TestAddSubMeshStoresGeometry(t *testing.T) {
	m := newMerger(1 << 20)
	mustAdd(t, m, "terrain", quadMesh("ground", "mat-rock"))
	if got := m.SubMeshCount("terrain"); got != 1 {
		t.Fatalf("sub mesh count = %d, want 1", got)
	}
	if got := m.TotalVertices("terrain"); got != 4 {
		t.Fatalf("total vertices = %d, want 4", got)
	}
	groups := m.Groups()
	if len(groups) != 1 || groups[0] != "terrain" {
		t.Fatalf("groups = %v, want [terrain]", groups)
	}
}

func TestAddRejectsInvalidSubMesh(t *testing.T) {
	m := newMerger(1 << 20)
	if err := m.AddSubMesh("  ", quadMesh("ground", "mat-rock"), identity()); !errors.Is(err, impl.ErrBlankGroup) {
		t.Fatalf("blank group error = %v, want ErrBlankGroup", err)
	}
	blank := quadMesh("ground", "mat-rock")
	blank.ID = " "
	if err := m.AddSubMesh("terrain", blank, identity()); !errors.Is(err, impl.ErrBadSubMesh) {
		t.Fatalf("blank sub mesh error = %v, want ErrBadSubMesh", err)
	}
	short := quadMesh("short", "mat-rock")
	short.Normals = short.Normals[:2]
	if err := m.AddSubMesh("terrain", short, identity()); !errors.Is(err, impl.ErrBadGeometry) {
		t.Fatalf("normal mismatch error = %v, want ErrBadGeometry", err)
	}
	odd := quadMesh("odd", "mat-rock")
	odd.Indices = []uint32{0, 1, 2, 0}
	if err := m.AddSubMesh("terrain", odd, identity()); !errors.Is(err, impl.ErrBadGeometry) {
		t.Fatalf("index triple error = %v, want ErrBadGeometry", err)
	}
	if got := m.SubMeshCount("terrain"); got != 0 {
		t.Fatalf("rejected sub meshes were stored: %d", got)
	}
}

func TestAddRejectsDuplicateAndCrossGroupBinds(t *testing.T) {
	m := newMerger(1 << 20)
	mustAdd(t, m, "terrain", quadMesh("ground", "mat-rock"))
	if err := m.AddSubMesh("terrain", quadMesh("ground", "mat-rock"), identity()); !errors.Is(err, impl.ErrDuplicateSubMesh) {
		t.Fatalf("duplicate error = %v, want ErrDuplicateSubMesh", err)
	}
	if err := m.AddSubMesh("props", quadMesh("ground", "mat-rock"), identity()); !errors.Is(err, impl.ErrSubMeshBound) {
		t.Fatalf("cross group error = %v, want ErrSubMeshBound", err)
	}
}

func TestAddRejectsMixedMaterialOrLayout(t *testing.T) {
	m := newMerger(1 << 20)
	mustAdd(t, m, "terrain", quadMesh("ground", "mat-rock"))
	if err := m.AddSubMesh("terrain", quadMesh("rock2", "mat-wood"), identity()); !errors.Is(err, impl.ErrMixedGroup) {
		t.Fatalf("mixed material error = %v, want ErrMixedGroup", err)
	}
	wide := quadMesh("wide", "mat-rock")
	wide.Layout = impl.VertexLayout{Stride: 44}
	if err := m.AddSubMesh("terrain", wide, identity()); !errors.Is(err, impl.ErrMixedGroup) {
		t.Fatalf("mixed layout error = %v, want ErrMixedGroup", err)
	}
	if got := m.SubMeshCount("terrain"); got != 1 {
		t.Fatalf("mixed sub meshes were stored: %d", got)
	}
}

func TestBuildPacksRangesContiguously(t *testing.T) {
	m := newMerger(1 << 20)
	mustAdd(t, m, "terrain", quadMesh("a", "mat-rock"))
	mustAdd(t, m, "terrain", quadMesh("b", "mat-rock"))
	mustAdd(t, m, "terrain", quadMesh("c", "mat-rock"))
	batch := mustBuild(t, m, "terrain")
	if batch.VertexCount != 12 || len(batch.Vertices) != 12 {
		t.Fatalf("batch vertices = %d/%d, want 12", batch.VertexCount, len(batch.Vertices))
	}
	if batch.IndexCount != 18 || len(batch.Indices) != 18 {
		t.Fatalf("batch indices = %d/%d, want 18", batch.IndexCount, len(batch.Indices))
	}
	if batch.IndexWidth != 16 {
		t.Fatalf("index width = %d, want 16", batch.IndexWidth)
	}
	if batch.GroupKey != "terrain" || batch.MaterialID != "mat-rock" {
		t.Fatalf("batch identity = %q/%q", batch.GroupKey, batch.MaterialID)
	}
	wantStarts := []int{0, 4, 8}
	wantIndexStarts := []int{0, 6, 12}
	if len(batch.Ranges) != 3 {
		t.Fatalf("ranges = %d, want 3", len(batch.Ranges))
	}
	for i, r := range batch.Ranges {
		if r.VertexStart != wantStarts[i] || r.VertexCount != 4 {
			t.Fatalf("range %d vertex slot = %+v", i, r)
		}
		if r.IndexStart != wantIndexStarts[i] || r.IndexCount != 6 {
			t.Fatalf("range %d index slot = %+v", i, r)
		}
	}
}

func TestBuildBakesWorldTransform(t *testing.T) {
	m := newMerger(1 << 20)
	world := impl.Transform{
		M: [3][3]float64{{0, -1, 0}, {1, 0, 0}, {0, 0, 1}},
		T: impl.Vec3{X: 10},
	}
	mesh := triMesh("tri", "mat-rock")
	if err := m.AddSubMesh("terrain", mesh, world); err != nil {
		t.Fatalf("add triangle: %v", err)
	}
	batch := mustBuild(t, m, "terrain")
	if len(batch.Vertices) != 3 {
		t.Fatalf("batch vertices = %d, want 3", len(batch.Vertices))
	}
	first := batch.Vertices[0]
	if !closeTo(first.X, 10) || !closeTo(first.Y, 1) || !closeTo(first.Z, 0) {
		t.Fatalf("baked position = %+v, want (10,1,0)", first)
	}
	if !closeTo(batch.Normals[0].X, 0) || !closeTo(batch.Normals[0].Y, 1) || !closeTo(batch.Normals[0].Z, 0) {
		t.Fatalf("baked normal = %+v, want (0,1,0)", batch.Normals[0])
	}
}

func TestBuildSwitchesIndexWidthAboveUint16(t *testing.T) {
	m := newMerger(70000)
	big := impl.SubMesh{
		ID:         "big",
		MaterialID: "mat-rock",
		Layout:     layout32(),
		Positions:  make([]impl.Vec3, 70000),
		Normals:    make([]impl.Vec3, 70000),
		Indices:    []uint32{0, 1, 2},
	}
	if err := m.AddSubMesh("terrain", big, identity()); err != nil {
		t.Fatalf("add big sub mesh: %v", err)
	}
	batch := mustBuild(t, m, "terrain")
	if batch.IndexWidth != 32 {
		t.Fatalf("index width = %d, want 32", batch.IndexWidth)
	}
	if got := m.IndexWidth("terrain"); got != 32 {
		t.Fatalf("reported index width = %d, want 32", got)
	}
	if batch.VertexCount != 70000 {
		t.Fatalf("batch vertices = %d, want 70000", batch.VertexCount)
	}
	if len(batch.Indices) != 3 {
		t.Fatalf("index count = %d, want 3", len(batch.Indices))
	}
}

func TestBuildRejectsBatchOverVertexCap(t *testing.T) {
	m := newMerger(4)
	mustAdd(t, m, "terrain", quadMesh("a", "mat-rock"))
	mustAdd(t, m, "terrain", quadMesh("b", "mat-rock"))
	if _, err := m.Build("terrain"); !errors.Is(err, impl.ErrBatchTooLarge) {
		t.Fatalf("over cap error = %v, want ErrBatchTooLarge", err)
	}
	if got := m.IndexWidth("terrain"); got != 0 {
		t.Fatalf("failed build left index width %d", got)
	}
}

func TestBuildRejectsUnknownGroup(t *testing.T) {
	m := newMerger(1 << 20)
	if _, err := m.Build("missing"); !errors.Is(err, impl.ErrUnknownGroup) {
		t.Fatalf("unknown group error = %v, want ErrUnknownGroup", err)
	}
	mustAdd(t, m, "terrain", quadMesh("a", "mat-rock"))
	if _, err := m.Build("missing"); !errors.Is(err, impl.ErrUnknownGroup) {
		t.Fatalf("unknown group error with other groups present = %v, want ErrUnknownGroup", err)
	}
	if _, err := m.Build("terrain"); err != nil {
		t.Fatalf("known group build: %v", err)
	}
}

func TestRemoveCompactsSurvivingRanges(t *testing.T) {
	m := newMerger(1 << 20)
	mustAdd(t, m, "terrain", quadMesh("a", "mat-rock"))
	mustAdd(t, m, "terrain", quadMesh("b", "mat-rock"))
	mustAdd(t, m, "terrain", quadMesh("c", "mat-rock"))
	mustBuild(t, m, "terrain")
	if err := m.RemoveSubMesh("terrain", "a"); err != nil {
		t.Fatalf("remove a: %v", err)
	}
	if got := m.SubMeshCount("terrain"); got != 2 {
		t.Fatalf("sub mesh count after remove = %d, want 2", got)
	}
	if got := m.TotalVertices("terrain"); got != 8 {
		t.Fatalf("total vertices after remove = %d, want 8", got)
	}
	batch := mustBuild(t, m, "terrain")
	if batch.VertexCount != 8 || batch.IndexCount != 12 {
		t.Fatalf("rebuilt batch = %d verts %d indices, want 8/12", batch.VertexCount, batch.IndexCount)
	}
	bRef, err := m.BatchOf("b")
	if err != nil {
		t.Fatalf("batch of b: %v", err)
	}
	if bRef.VertexStart != 0 || bRef.VertexCount != 4 || bRef.IndexStart != 0 || bRef.IndexCount != 6 {
		t.Fatalf("b range after compaction = %+v", bRef)
	}
	cRef, err := m.BatchOf("c")
	if err != nil {
		t.Fatalf("batch of c: %v", err)
	}
	if cRef.VertexStart != 4 || cRef.IndexStart != 6 {
		t.Fatalf("c range after compaction = %+v", cRef)
	}
	if _, err := m.BatchOf("a"); !errors.Is(err, impl.ErrUnknownSubMesh) {
		t.Fatalf("removed sub mesh error = %v, want ErrUnknownSubMesh", err)
	}
}

func TestRemoveRejectsUnknownSubMesh(t *testing.T) {
	m := newMerger(1 << 20)
	if err := m.RemoveSubMesh("missing", "a"); !errors.Is(err, impl.ErrUnknownGroup) {
		t.Fatalf("unknown group error = %v, want ErrUnknownGroup", err)
	}
	mustAdd(t, m, "terrain", quadMesh("a", "mat-rock"))
	if err := m.RemoveSubMesh("terrain", "ghost"); !errors.Is(err, impl.ErrUnknownSubMesh) {
		t.Fatalf("unknown sub mesh error = %v, want ErrUnknownSubMesh", err)
	}
}

func TestBatchOfRequiresBuild(t *testing.T) {
	m := newMerger(1 << 20)
	mustAdd(t, m, "terrain", quadMesh("a", "mat-rock"))
	if _, err := m.BatchOf("a"); !errors.Is(err, impl.ErrNotBuilt) {
		t.Fatalf("unbuilt lookup error = %v, want ErrNotBuilt", err)
	}
	mustBuild(t, m, "terrain")
	ref, err := m.BatchOf("a")
	if err != nil {
		t.Fatalf("batch of a: %v", err)
	}
	if ref.GroupKey != "terrain" || ref.MaterialID != "mat-rock" || ref.BatchID == "" {
		t.Fatalf("batch ref = %+v", ref)
	}
}

func TestReportAggregatesBuilds(t *testing.T) {
	m := newMerger(1 << 20)
	mustAdd(t, m, "terrain", quadMesh("ground", "mat-rock"))
	mustAdd(t, m, "props", quadMesh("crate", "mat-wood"))
	mustAdd(t, m, "props", quadMesh("barrel", "mat-wood"))
	mustBuild(t, m, "terrain")
	mustBuild(t, m, "props")
	report := m.Report()
	if report.Groups != 2 || report.Batches != 2 || report.SubMeshes != 3 {
		t.Fatalf("report groups/batches/sub meshes = %+v", report)
	}
	if report.Vertices != 12 || report.Indices != 18 {
		t.Fatalf("report vertices/indices = %d/%d, want 12/18", report.Vertices, report.Indices)
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	m := newMerger(1 << 20)
	mustAdd(t, m, "terrain", quadMesh("a", "mat-rock"))
	mustAdd(t, m, "terrain", quadMesh("b", "mat-rock"))
	mustBuild(t, m, "terrain")
	want, err := m.BatchOf("b")
	if err != nil {
		t.Fatalf("batch of b: %v", err)
	}
	data, err := m.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload is empty")
	}
	restored := newMerger(1 << 20)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.SubMeshCount("terrain"); got != 2 {
		t.Fatalf("restored sub mesh count = %d, want 2", got)
	}
	got, err := restored.BatchOf("b")
	if err != nil {
		t.Fatalf("restored batch of b: %v", err)
	}
	if got != want {
		t.Fatalf("restored range = %+v, want %+v", got, want)
	}
	batch := mustBuild(t, restored, "terrain")
	if batch.VertexCount != 8 || batch.IndexWidth != 16 {
		t.Fatalf("rebuilt restored batch = %+v", batch)
	}
}

func TestRestoreRejectsCorruptPayload(t *testing.T) {
	m := newMerger(1 << 20)
	mustAdd(t, m, "terrain", quadMesh("a", "mat-rock"))
	if err := m.Restore([]byte("not a snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt payload error = %v, want ErrBadSnapshot", err)
	}
	if got := m.SubMeshCount("terrain"); got != 1 {
		t.Fatalf("sub mesh count after failed restore = %d, want 1", got)
	}
	if _, err := m.Build("terrain"); err != nil {
		t.Fatalf("build after failed restore: %v", err)
	}
}
