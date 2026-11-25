package task

import (
	"errors"
	"testing"

	impl "gsb/batch49-go-game-a-15"
)

func cube(min, max float64) impl.Box {
	return impl.Box{Min: impl.Vec3{X: min, Y: min, Z: min}, Max: impl.Vec3{X: max, Y: max, Z: max}}
}

func TestNewHashGridRejectsBadCellSize(t *testing.T) {
	if _, err := impl.NewHashGrid(0); !errors.Is(err, impl.ErrBadCellSize) {
		t.Fatalf("zero cell size must be rejected, got %v", err)
	}
	if _, err := impl.NewHashGrid(-4); !errors.Is(err, impl.ErrBadCellSize) {
		t.Fatalf("negative cell size must be rejected, got %v", err)
	}
	if _, err := impl.NewHashGrid(4); err != nil {
		t.Fatalf("valid cell size must be accepted, got %v", err)
	}
}

func TestInsertRejectsBadInput(t *testing.T) {
	g, err := impl.NewHashGrid(4)
	if err != nil {
		t.Fatalf("grid construction failed: %v", err)
	}
	if err := g.Insert("  ", cube(0, 1)); !errors.Is(err, impl.ErrBlankID) {
		t.Fatalf("blank id must be rejected, got %v", err)
	}
	bad := impl.Box{Min: impl.Vec3{X: 2}, Max: impl.Vec3{X: 1}}
	if err := g.Insert("bad", bad); !errors.Is(err, impl.ErrBadBox) {
		t.Fatalf("inverted box must be rejected, got %v", err)
	}
}

func TestInsertStoresBox(t *testing.T) {
	g, err := impl.NewHashGrid(4)
	if err != nil {
		t.Fatalf("grid construction failed: %v", err)
	}
	if err := g.Insert("a", cube(0, 1)); err != nil {
		t.Fatalf("insert failed: %v", err)
	}
	if got := g.Len(); got != 1 {
		t.Fatalf("len = %d, want 1", got)
	}
	box, ok := g.Box("a")
	if !ok {
		t.Fatalf("inserted box must be readable")
	}
	if box.Min.X != 0 || box.Max.X != 1 {
		t.Fatalf("box = %+v, want [0,1]", box)
	}
}

func TestInsertRejectsDuplicateID(t *testing.T) {
	g, err := impl.NewHashGrid(4)
	if err != nil {
		t.Fatalf("grid construction failed: %v", err)
	}
	if err := g.Insert("a", cube(0, 1)); err != nil {
		t.Fatalf("first insert failed: %v", err)
	}
	if err := g.Insert("a", cube(2, 3)); !errors.Is(err, impl.ErrDuplicateID) {
		t.Fatalf("duplicate id must be rejected, got %v", err)
	}
}

func TestMoveRelocatesObject(t *testing.T) {
	g, err := impl.NewHashGrid(4)
	if err != nil {
		t.Fatalf("grid construction failed: %v", err)
	}
	if err := g.Insert("a", cube(0, 1)); err != nil {
		t.Fatalf("insert failed: %v", err)
	}
	if err := g.Move("a", cube(8, 9)); err != nil {
		t.Fatalf("move failed: %v", err)
	}
	old, _, err := g.Query(cube(0.2, 0.8), 8)
	if err != nil {
		t.Fatalf("old query failed: %v", err)
	}
	if len(old) != 0 {
		t.Fatalf("old cell query = %v, want none after move", old)
	}
	fresh, _, err := g.Query(cube(8.2, 8.8), 8)
	if err != nil {
		t.Fatalf("new query failed: %v", err)
	}
	if len(fresh) != 1 || fresh[0] != "a" {
		t.Fatalf("new cell query = %v, want [a]", fresh)
	}
}

func TestMoveRejectsUnknownID(t *testing.T) {
	g, err := impl.NewHashGrid(4)
	if err != nil {
		t.Fatalf("grid construction failed: %v", err)
	}
	if err := g.Move("ghost", cube(0, 1)); !errors.Is(err, impl.ErrUnknownID) {
		t.Fatalf("moving an unknown object must fail, got %v", err)
	}
}

func TestRemoveDropsObject(t *testing.T) {
	g, err := impl.NewHashGrid(4)
	if err != nil {
		t.Fatalf("grid construction failed: %v", err)
	}
	if err := g.Insert("a", cube(0, 1)); err != nil {
		t.Fatalf("insert a failed: %v", err)
	}
	if err := g.Insert("b", cube(0, 1)); err != nil {
		t.Fatalf("insert b failed: %v", err)
	}
	if err := g.Remove("a"); err != nil {
		t.Fatalf("remove failed: %v", err)
	}
	if got := g.Len(); got != 1 {
		t.Fatalf("len after remove = %d, want 1", got)
	}
	ids, _, err := g.Query(cube(0.2, 0.8), 8)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(ids) != 1 || ids[0] != "b" {
		t.Fatalf("query after remove = %v, want [b]", ids)
	}
	if err := g.Remove("a"); !errors.Is(err, impl.ErrUnknownID) {
		t.Fatalf("removing a missing object must fail, got %v", err)
	}
}

func TestPairsFindsNeighborInSameCell(t *testing.T) {
	g, err := impl.NewHashGrid(4)
	if err != nil {
		t.Fatalf("grid construction failed: %v", err)
	}
	if err := g.Insert("a", cube(0, 1)); err != nil {
		t.Fatalf("insert a failed: %v", err)
	}
	if err := g.Insert("b", cube(1, 2)); err != nil {
		t.Fatalf("insert b failed: %v", err)
	}
	pairs, _, err := g.Pairs(8)
	if err != nil {
		t.Fatalf("pairs failed: %v", err)
	}
	if len(pairs) != 1 {
		t.Fatalf("pairs = %v, want exactly one", pairs)
	}
}

func TestPairsDedupesAcrossCells(t *testing.T) {
	g, err := impl.NewHashGrid(4)
	if err != nil {
		t.Fatalf("grid construction failed: %v", err)
	}
	if err := g.Insert("a", cube(0, 6)); err != nil {
		t.Fatalf("insert a failed: %v", err)
	}
	if err := g.Insert("b", cube(1, 5)); err != nil {
		t.Fatalf("insert b failed: %v", err)
	}
	pairs, _, err := g.Pairs(8)
	if err != nil {
		t.Fatalf("pairs failed: %v", err)
	}
	if len(pairs) != 1 {
		t.Fatalf("pairs = %v, want one deduplicated pair across shared cells", pairs)
	}
}

func TestPairsNoSharedCell(t *testing.T) {
	g, err := impl.NewHashGrid(4)
	if err != nil {
		t.Fatalf("grid construction failed: %v", err)
	}
	if err := g.Insert("a", cube(0, 1)); err != nil {
		t.Fatalf("insert a failed: %v", err)
	}
	if err := g.Insert("b", cube(8, 9)); err != nil {
		t.Fatalf("insert b failed: %v", err)
	}
	pairs, _, err := g.Pairs(8)
	if err != nil {
		t.Fatalf("pairs failed: %v", err)
	}
	if len(pairs) != 0 {
		t.Fatalf("pairs = %v, want none for distant objects", pairs)
	}
}

func TestPairsRespectsBudget(t *testing.T) {
	g, err := impl.NewHashGrid(4)
	if err != nil {
		t.Fatalf("grid construction failed: %v", err)
	}
	for _, id := range []string{"a", "b", "c"} {
		if err := g.Insert(id, cube(0, 4)); err != nil {
			t.Fatalf("insert %s failed: %v", id, err)
		}
	}
	limited, _, err := g.Pairs(2)
	if err != nil {
		t.Fatalf("limited pairs failed: %v", err)
	}
	if len(limited) != 2 {
		t.Fatalf("limited pairs = %v, want exactly the budget of 2", limited)
	}
	full, _, err := g.Pairs(8)
	if err != nil {
		t.Fatalf("full pairs failed: %v", err)
	}
	if len(full) != 3 {
		t.Fatalf("full pairs = %v, want three pairs", full)
	}
}

func TestQueryReturnsCandidates(t *testing.T) {
	g, err := impl.NewHashGrid(4)
	if err != nil {
		t.Fatalf("grid construction failed: %v", err)
	}
	if err := g.Insert("a", cube(0, 1)); err != nil {
		t.Fatalf("insert a failed: %v", err)
	}
	if err := g.Insert("b", cube(1, 2)); err != nil {
		t.Fatalf("insert b failed: %v", err)
	}
	ids, _, err := g.Query(cube(0.5, 1.5), 8)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("query = %v, want [a b]", ids)
	}
	far, _, err := g.Query(cube(100, 101), 8)
	if err != nil {
		t.Fatalf("far query failed: %v", err)
	}
	if len(far) != 0 {
		t.Fatalf("far query = %v, want none", far)
	}
}

func TestQueryRespectsBudget(t *testing.T) {
	g, err := impl.NewHashGrid(4)
	if err != nil {
		t.Fatalf("grid construction failed: %v", err)
	}
	for _, id := range []string{"a", "b", "c"} {
		if err := g.Insert(id, cube(0, 1)); err != nil {
			t.Fatalf("insert %s failed: %v", id, err)
		}
	}
	ids, _, err := g.Query(cube(0.2, 0.8), 2)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("query = %v, want exactly the budget of 2", ids)
	}
}

func TestCellsCountsOccupiedCells(t *testing.T) {
	g, err := impl.NewHashGrid(4)
	if err != nil {
		t.Fatalf("grid construction failed: %v", err)
	}
	wide := impl.Box{Min: impl.Vec3{X: 0, Y: 0, Z: 0}, Max: impl.Vec3{X: 9, Y: 1, Z: 1}}
	if err := g.Insert("wide", wide); err != nil {
		t.Fatalf("insert failed: %v", err)
	}
	if got := g.Cells(); got != 3 {
		t.Fatalf("cells = %d, want 3 cells along x", got)
	}
	if err := g.Insert("wide2", wide); err != nil {
		t.Fatalf("second insert failed: %v", err)
	}
	if got := g.Cells(); got != 3 {
		t.Fatalf("cells = %d, want the same 3 shared cells", got)
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	g, err := impl.NewHashGrid(4)
	if err != nil {
		t.Fatalf("grid construction failed: %v", err)
	}
	if err := g.Insert("a", cube(0, 1)); err != nil {
		t.Fatalf("insert a failed: %v", err)
	}
	if err := g.Insert("b", cube(1, 2)); err != nil {
		t.Fatalf("insert b failed: %v", err)
	}
	snap, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	restored, err := impl.NewHashGrid(4)
	if err != nil {
		t.Fatalf("grid construction failed: %v", err)
	}
	if err := restored.Restore(snap); err != nil {
		t.Fatalf("restore failed: %v", err)
	}
	if got := restored.Len(); got != 2 {
		t.Fatalf("restored len = %d, want 2", got)
	}
	pairs, _, err := restored.Pairs(8)
	if err != nil {
		t.Fatalf("restored pairs failed: %v", err)
	}
	if len(pairs) != 1 {
		t.Fatalf("restored pairs = %v, want one", pairs)
	}
	ids, _, err := restored.Query(cube(0.2, 0.8), 8)
	if err != nil {
		t.Fatalf("restored query failed: %v", err)
	}
	if len(ids) == 0 {
		t.Fatalf("restored query must find objects")
	}
	if err := restored.Restore([]byte("}}not-json{{")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("broken snapshot must be rejected, got %v", err)
	}
	if got := restored.Len(); got != 2 {
		t.Fatalf("failed restore must keep the live state, len = %d", got)
	}
}
