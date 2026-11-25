package task

import (
	"errors"
	"math"
	"testing"

	impl "gsb/batch49-go-game-b-04"
)

func newBaker() *impl.OccluderBakePlanner {
	return impl.NewOccluderBakePlanner(impl.BakeOptions{
		CellSize:             10,
		MinCoverage:          0.05,
		MaxTrianglesPerBatch: 1000,
		MaxCellsPerBake:      4,
	})
}

func v(x, y, z float64) impl.Vec3 {
	return impl.Vec3{X: x, Y: y, Z: z}
}

func fullCell(x0, y0 float64) []impl.Triangle {
	return []impl.Triangle{
		{A: v(x0, y0, 0), B: v(x0+10, y0, 0), C: v(x0+10, y0+10, 0)},
		{A: v(x0, y0, 0), B: v(x0+10, y0+10, 0), C: v(x0, y0+10, 0)},
	}
}

func rock() impl.MaterialInfo {
	return impl.MaterialInfo{ID: "mat-rock"}
}

func TestSubmitAssignsTrianglesToCells(t *testing.T) {
	p := newBaker()
	if err := p.Submit("scene", fullCell(0, 0), rock()); err != nil {
		t.Fatalf("submit first cell: %v", err)
	}
	if err := p.Submit("scene", fullCell(10, 0), rock()); err != nil {
		t.Fatalf("submit second cell: %v", err)
	}
	cells := p.Cells()
	if len(cells) != 2 {
		t.Fatalf("cells = %v, want two cells", cells)
	}
	if cells[0] != (impl.CellID{X: 0, Y: 0}) || cells[1] != (impl.CellID{X: 1, Y: 0}) {
		t.Fatalf("cells = %v, want [{0 0} {1 0}]", cells)
	}
	if got := p.CandidateCount(impl.CellID{X: 0, Y: 0}); got != 2 {
		t.Fatalf("candidates = %d, want 2", got)
	}
}

func TestSubmitRejectsBlankSceneAndMaterial(t *testing.T) {
	p := newBaker()
	if err := p.Submit("  ", fullCell(0, 0), rock()); !errors.Is(err, impl.ErrBlankScene) {
		t.Fatalf("blank scene error = %v, want ErrBlankScene", err)
	}
	if err := p.Submit("scene", fullCell(0, 0), impl.MaterialInfo{}); !errors.Is(err, impl.ErrBadMaterial) {
		t.Fatalf("blank material error = %v, want ErrBadMaterial", err)
	}
	if got := len(p.Cells()); got != 0 {
		t.Fatalf("rejected submits created %d cells", got)
	}
}

func TestSubmitRejectsEmptyAndOversizedBatch(t *testing.T) {
	p := newBaker()
	if err := p.Submit("scene", nil, rock()); !errors.Is(err, impl.ErrEmptyBatch) {
		t.Fatalf("empty batch error = %v, want ErrEmptyBatch", err)
	}
	small := impl.NewOccluderBakePlanner(impl.BakeOptions{
		CellSize:             10,
		MinCoverage:          0.05,
		MaxTrianglesPerBatch: 2,
		MaxCellsPerBake:      4,
	})
	if err := small.Submit("scene", fullCell(0, 0), rock()); err != nil {
		t.Fatalf("submit at the cap: %v", err)
	}
	tooMany := append(fullCell(10, 0), impl.Triangle{A: v(10, 0, 0), B: v(20, 0, 0), C: v(20, 10, 0)})
	if err := small.Submit("scene", tooMany, rock()); !errors.Is(err, impl.ErrBatchTooLarge) {
		t.Fatalf("oversized batch error = %v, want ErrBatchTooLarge", err)
	}
}

func TestSubmitDropsTransparentAndDynamic(t *testing.T) {
	p := newBaker()
	if err := p.Submit("scene", fullCell(0, 0), impl.MaterialInfo{ID: "mat-foliage", Transparent: true}); err != nil {
		t.Fatalf("submit transparent: %v", err)
	}
	if err := p.Submit("scene", fullCell(10, 0), impl.MaterialInfo{ID: "mat-flag", Dynamic: true}); err != nil {
		t.Fatalf("submit dynamic: %v", err)
	}
	if got := len(p.Cells()); got != 0 {
		t.Fatalf("filtered materials created %d cells", got)
	}
	if got := p.CandidateCount(impl.CellID{X: 0, Y: 0}); got != 0 {
		t.Fatalf("transparent candidates = %d, want 0", got)
	}
}

func TestSubmitRejectsBadTriangle(t *testing.T) {
	p := newBaker()
	bad := fullCell(0, 0)
	bad[0].A.X = math.NaN()
	if err := p.Submit("scene", bad, rock()); !errors.Is(err, impl.ErrBadTriangle) {
		t.Fatalf("nan triangle error = %v, want ErrBadTriangle", err)
	}
	inf := fullCell(0, 0)
	inf[1].C.Y = math.Inf(1)
	if err := p.Submit("scene", inf, rock()); !errors.Is(err, impl.ErrBadTriangle) {
		t.Fatalf("inf triangle error = %v, want ErrBadTriangle", err)
	}
	if got := len(p.Cells()); got != 0 {
		t.Fatalf("bad triangles created %d cells", got)
	}
}

func TestBakeCellProducesCellData(t *testing.T) {
	p := newBaker()
	if err := p.Submit("scene", fullCell(0, 0), rock()); err != nil {
		t.Fatalf("submit: %v", err)
	}
	data, err := p.BakeCell(impl.CellID{X: 0, Y: 0})
	if err != nil {
		t.Fatalf("bake cell: %v", err)
	}
	if data.Cell != (impl.CellID{X: 0, Y: 0}) {
		t.Fatalf("baked cell = %+v", data.Cell)
	}
	if data.Triangles != 2 {
		t.Fatalf("baked triangles = %d, want 2", data.Triangles)
	}
	if data.Coverage < 0.99 {
		t.Fatalf("coverage = %f, want full coverage", data.Coverage)
	}
	if data.Discarded {
		t.Fatalf("full cell was discarded")
	}
	if data.Version == "" {
		t.Fatalf("baked cell has no version hash")
	}
}

func TestBakeCellRejectsUnknownCell(t *testing.T) {
	p := newBaker()
	if _, err := p.BakeCell(impl.CellID{X: 5, Y: 5}); !errors.Is(err, impl.ErrUnknownCell) {
		t.Fatalf("unknown cell error = %v, want ErrUnknownCell", err)
	}
	if err := p.Submit("scene", fullCell(0, 0), rock()); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if _, err := p.BakeCell(impl.CellID{X: 5, Y: 5}); !errors.Is(err, impl.ErrUnknownCell) {
		t.Fatalf("unknown cell error with candidates present = %v, want ErrUnknownCell", err)
	}
}

func TestBakeCellDiscardsLowCoverage(t *testing.T) {
	p := newBaker()
	tiny := []impl.Triangle{{A: v(0, 0, 0), B: v(0.4, 0, 0), C: v(0, 0.5, 0)}}
	if err := p.Submit("scene", tiny, rock()); err != nil {
		t.Fatalf("submit tiny triangle: %v", err)
	}
	coverage, err := p.Coverage(impl.CellID{X: 0, Y: 0})
	if err != nil {
		t.Fatalf("coverage: %v", err)
	}
	if coverage >= 0.05 {
		t.Fatalf("coverage = %f, want below the gate", coverage)
	}
	data, err := p.BakeCell(impl.CellID{X: 0, Y: 0})
	if err != nil {
		t.Fatalf("bake low coverage cell: %v", err)
	}
	if !data.Discarded {
		t.Fatalf("low coverage cell was not discarded")
	}
	if data.Version != "" {
		t.Fatalf("discarded cell carries version %q", data.Version)
	}
	discarded := p.DiscardedCells()
	if len(discarded) != 1 || discarded[0] != (impl.CellID{X: 0, Y: 0}) {
		t.Fatalf("discarded cells = %v", discarded)
	}
}

func TestBakePendingRespectsCellBudget(t *testing.T) {
	p := impl.NewOccluderBakePlanner(impl.BakeOptions{
		CellSize:             10,
		MinCoverage:          0.05,
		MaxTrianglesPerBatch: 1000,
		MaxCellsPerBake:      2,
	})
	for i := 0; i < 3; i++ {
		if err := p.Submit("scene", fullCell(float64(i)*10, 0), rock()); err != nil {
			t.Fatalf("submit cell %d: %v", i, err)
		}
	}
	first, err := p.BakePending(10)
	if err != nil {
		t.Fatalf("first bake pass: %v", err)
	}
	if len(first.Baked) != 2 || first.Skipped != 1 {
		t.Fatalf("first pass = %+v, want 2 baked and 1 skipped", first)
	}
	second, err := p.BakePending(10)
	if err != nil {
		t.Fatalf("second bake pass: %v", err)
	}
	if len(second.Baked) != 1 || second.Skipped != 0 {
		t.Fatalf("second pass = %+v, want 1 baked", second)
	}
}

func TestInvalidateByMaterialMarksDirty(t *testing.T) {
	p := newBaker()
	if err := p.Submit("scene", fullCell(0, 0), rock()); err != nil {
		t.Fatalf("submit rock: %v", err)
	}
	if err := p.Submit("scene", fullCell(10, 0), impl.MaterialInfo{ID: "mat-wood"}); err != nil {
		t.Fatalf("submit wood: %v", err)
	}
	if _, err := p.BakePending(10); err != nil {
		t.Fatalf("bake pass: %v", err)
	}
	if got := len(p.DirtyCells()); got != 0 {
		t.Fatalf("dirty cells after bake = %d, want 0", got)
	}
	woodBefore, err := p.VersionHash(impl.CellID{X: 1, Y: 0})
	if err != nil {
		t.Fatalf("wood hash: %v", err)
	}
	if err := p.InvalidateByMaterial("mat-rock"); err != nil {
		t.Fatalf("invalidate rock: %v", err)
	}
	dirty := p.DirtyCells()
	if len(dirty) != 1 || dirty[0] != (impl.CellID{X: 0, Y: 0}) {
		t.Fatalf("dirty cells = %v, want [{0 0}]", dirty)
	}
	woodAfter, err := p.VersionHash(impl.CellID{X: 1, Y: 0})
	if err != nil {
		t.Fatalf("wood hash after invalidate: %v", err)
	}
	if woodBefore != woodAfter {
		t.Fatalf("unrelated cell hash changed from %q to %q", woodBefore, woodAfter)
	}
	if err := p.InvalidateByMaterial("mat-glass"); !errors.Is(err, impl.ErrUnknownMaterial) {
		t.Fatalf("unknown material error = %v, want ErrUnknownMaterial", err)
	}
	if _, err := p.BakeCell(impl.CellID{X: 0, Y: 0}); err != nil {
		t.Fatalf("rebake dirty cell: %v", err)
	}
	if got := len(p.DirtyCells()); got != 0 {
		t.Fatalf("dirty cells after rebake = %d, want 0", got)
	}
}

func TestVersionHashStableForSameContent(t *testing.T) {
	p := newBaker()
	if err := p.Submit("scene", fullCell(0, 0), rock()); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if _, err := p.BakeCell(impl.CellID{X: 0, Y: 0}); err != nil {
		t.Fatalf("first bake: %v", err)
	}
	first, err := p.VersionHash(impl.CellID{X: 0, Y: 0})
	if err != nil {
		t.Fatalf("first hash: %v", err)
	}
	if _, err := p.BakeCell(impl.CellID{X: 0, Y: 0}); err != nil {
		t.Fatalf("second bake: %v", err)
	}
	second, err := p.VersionHash(impl.CellID{X: 0, Y: 0})
	if err != nil {
		t.Fatalf("second hash: %v", err)
	}
	if first != second {
		t.Fatalf("same content produced different hashes %q and %q", first, second)
	}
	extra := []impl.Triangle{{A: v(0, 0, 1), B: v(5, 0, 1), C: v(0, 5, 1)}}
	if err := p.Submit("scene", extra, rock()); err != nil {
		t.Fatalf("submit extra: %v", err)
	}
	if _, err := p.BakeCell(impl.CellID{X: 0, Y: 0}); err != nil {
		t.Fatalf("third bake: %v", err)
	}
	third, err := p.VersionHash(impl.CellID{X: 0, Y: 0})
	if err != nil {
		t.Fatalf("third hash: %v", err)
	}
	if third == first {
		t.Fatalf("changed content kept hash %q", first)
	}
}

func TestVersionHashRequiresBake(t *testing.T) {
	p := newBaker()
	if err := p.Submit("scene", fullCell(0, 0), rock()); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if _, err := p.VersionHash(impl.CellID{X: 0, Y: 0}); !errors.Is(err, impl.ErrNotBaked) {
		t.Fatalf("unbaked hash error = %v, want ErrNotBaked", err)
	}
	if _, err := p.BakeCell(impl.CellID{X: 0, Y: 0}); err != nil {
		t.Fatalf("bake: %v", err)
	}
	hash, err := p.VersionHash(impl.CellID{X: 0, Y: 0})
	if err != nil {
		t.Fatalf("hash after bake: %v", err)
	}
	if hash == "" {
		t.Fatalf("hash after bake is empty")
	}
}

func TestCoverageIsExposedPerCell(t *testing.T) {
	p := newBaker()
	half := []impl.Triangle{{A: v(0, 0, 0), B: v(10, 0, 0), C: v(0, 10, 0)}}
	if err := p.Submit("scene", half, rock()); err != nil {
		t.Fatalf("submit half triangle: %v", err)
	}
	coverage, err := p.Coverage(impl.CellID{X: 0, Y: 0})
	if err != nil {
		t.Fatalf("coverage: %v", err)
	}
	if math.Abs(coverage-0.5) > 1e-6 {
		t.Fatalf("coverage = %f, want 0.5", coverage)
	}
	if _, err := p.Coverage(impl.CellID{X: 3, Y: 3}); !errors.Is(err, impl.ErrUnknownCell) {
		t.Fatalf("unknown cell coverage error = %v, want ErrUnknownCell", err)
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	p := newBaker()
	if err := p.Submit("scene", fullCell(0, 0), rock()); err != nil {
		t.Fatalf("submit rock: %v", err)
	}
	if err := p.Submit("scene", fullCell(10, 0), impl.MaterialInfo{ID: "mat-wood"}); err != nil {
		t.Fatalf("submit wood: %v", err)
	}
	if _, err := p.BakePending(10); err != nil {
		t.Fatalf("bake pass: %v", err)
	}
	if err := p.InvalidateByMaterial("mat-rock"); err != nil {
		t.Fatalf("invalidate rock: %v", err)
	}
	wantHash, err := p.VersionHash(impl.CellID{X: 1, Y: 0})
	if err != nil {
		t.Fatalf("wood hash: %v", err)
	}
	wantCoverage, err := p.Coverage(impl.CellID{X: 0, Y: 0})
	if err != nil {
		t.Fatalf("rock coverage: %v", err)
	}
	data, err := p.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload is empty")
	}
	restored := newBaker()
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := len(restored.Cells()); got != 2 {
		t.Fatalf("restored cells = %d, want 2", got)
	}
	gotHash, err := restored.VersionHash(impl.CellID{X: 1, Y: 0})
	if err != nil {
		t.Fatalf("restored hash: %v", err)
	}
	if gotHash != wantHash {
		t.Fatalf("restored hash = %q, want %q", gotHash, wantHash)
	}
	gotCoverage, err := restored.Coverage(impl.CellID{X: 0, Y: 0})
	if err != nil {
		t.Fatalf("restored coverage: %v", err)
	}
	if math.Abs(gotCoverage-wantCoverage) > 1e-9 {
		t.Fatalf("restored coverage = %f, want %f", gotCoverage, wantCoverage)
	}
	dirty := restored.DirtyCells()
	if len(dirty) != 1 || dirty[0] != (impl.CellID{X: 0, Y: 0}) {
		t.Fatalf("restored dirty cells = %v", dirty)
	}
}

func TestRestoreRejectsCorruptPayload(t *testing.T) {
	p := newBaker()
	if err := p.Submit("scene", fullCell(0, 0), rock()); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if err := p.Restore([]byte("not a snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt payload error = %v, want ErrBadSnapshot", err)
	}
	if got := len(p.Cells()); got != 1 {
		t.Fatalf("cells after failed restore = %d, want 1", got)
	}
	if _, err := p.BakeCell(impl.CellID{X: 0, Y: 0}); err != nil {
		t.Fatalf("bake after failed restore: %v", err)
	}
}
