package task

import (
	"errors"
	"math"
	"testing"

	impl "gsb/batch49-go-game-b-14"
)

func mustSelector(t *testing.T) *impl.TerrainQuadSelector {
	t.Helper()
	s, err := impl.NewTerrainQuadSelector(impl.SelectorOptions{
		MaxDepth:       2,
		WorldSize:      64,
		ViewportHeight: 720,
		FovDeg:         60,
		TolerancePx:    40,
	})
	if err != nil {
		t.Fatalf("new selector: %v", err)
	}
	if s == nil {
		t.Fatalf("selector must not be nil")
	}
	return s
}

func errorTable() []impl.GeomError {
	return []impl.GeomError{
		{Level: 0, Error: 4},
		{Level: 1, Error: 16},
		{Level: 2, Error: 64},
	}
}

func mustTable(t *testing.T, s *impl.TerrainQuadSelector) {
	t.Helper()
	if err := s.SetErrorTable(errorTable()); err != nil {
		t.Fatalf("set error table: %v", err)
	}
}

func nearView() impl.View {
	return impl.View{Pos: impl.Vec2{X: 32, Y: 32}, ViewportHeight: 720, FovDeg: 60, TolerancePx: 40}
}

func quadArea(level int) float64 {
	side := 64 / math.Pow(2, float64(level))
	return side * side
}

func TestNewRejectsInvalidOptions(t *testing.T) {
	if _, err := impl.NewTerrainQuadSelector(impl.SelectorOptions{MaxDepth: 0, WorldSize: 64, ViewportHeight: 720, FovDeg: 60, TolerancePx: 40}); !errors.Is(err, impl.ErrInvalidOptions) {
		t.Fatalf("zero max depth must report ErrInvalidOptions, got %v", err)
	}
	if _, err := impl.NewTerrainQuadSelector(impl.SelectorOptions{MaxDepth: 2, WorldSize: 0, ViewportHeight: 720, FovDeg: 60, TolerancePx: 40}); !errors.Is(err, impl.ErrInvalidOptions) {
		t.Fatalf("zero world size must report ErrInvalidOptions, got %v", err)
	}
}

func TestSetErrorTableRejectsEmpty(t *testing.T) {
	s := mustSelector(t)
	if err := s.SetErrorTable(nil); !errors.Is(err, impl.ErrEmptyErrorTable) {
		t.Fatalf("empty table must report ErrEmptyErrorTable, got %v", err)
	}
}

func TestSetErrorTableRejectsLevelMismatch(t *testing.T) {
	s := mustSelector(t)
	short := []impl.GeomError{{Level: 0, Error: 4}, {Level: 1, Error: 16}}
	if err := s.SetErrorTable(short); !errors.Is(err, impl.ErrErrorLevelMismatch) {
		t.Fatalf("table shorter than the tree must report ErrErrorLevelMismatch, got %v", err)
	}
	shifted := []impl.GeomError{{Level: 0, Error: 4}, {Level: 2, Error: 16}, {Level: 3, Error: 64}}
	if err := s.SetErrorTable(shifted); !errors.Is(err, impl.ErrErrorLevelMismatch) {
		t.Fatalf("shifted levels must report ErrErrorLevelMismatch, got %v", err)
	}
}

func TestSetErrorTableRejectsNegativeError(t *testing.T) {
	s := mustSelector(t)
	bad := []impl.GeomError{{Level: 0, Error: 4}, {Level: 1, Error: -1}, {Level: 2, Error: 64}}
	if err := s.SetErrorTable(bad); !errors.Is(err, impl.ErrNegativeError) {
		t.Fatalf("negative error must report ErrNegativeError, got %v", err)
	}
}

func TestSetErrorTableRejectsNonMonotonicTable(t *testing.T) {
	s := mustSelector(t)
	bad := []impl.GeomError{{Level: 0, Error: 16}, {Level: 1, Error: 4}, {Level: 2, Error: 64}}
	if err := s.SetErrorTable(bad); !errors.Is(err, impl.ErrNonMonotonicError) {
		t.Fatalf("decreasing error must report ErrNonMonotonicError, got %v", err)
	}
}

func TestSelectRejectsNonPositiveBudget(t *testing.T) {
	s := mustSelector(t)
	mustTable(t, s)
	if _, err := s.Select(nearView(), 0); !errors.Is(err, impl.ErrInvalidBudget) {
		t.Fatalf("zero budget must report ErrInvalidBudget, got %v", err)
	}
	if _, err := s.Select(nearView(), -3); !errors.Is(err, impl.ErrInvalidBudget) {
		t.Fatalf("negative budget must report ErrInvalidBudget, got %v", err)
	}
}

func TestSelectRejectsInvalidView(t *testing.T) {
	s := mustSelector(t)
	mustTable(t, s)
	bad := impl.View{Pos: impl.Vec2{X: 1, Y: 1}, ViewportHeight: 720, FovDeg: 0, TolerancePx: 40}
	if _, err := s.Select(bad, 8); !errors.Is(err, impl.ErrInvalidView) {
		t.Fatalf("zero fov must report ErrInvalidView, got %v", err)
	}
}

func TestSelectFarViewKeepsRootOnly(t *testing.T) {
	s := mustSelector(t)
	mustTable(t, s)
	view := impl.View{Pos: impl.Vec2{X: -5000, Y: -5000}, ViewportHeight: 720, FovDeg: 60, TolerancePx: 40}
	sel, err := s.Select(view, 100)
	if err != nil {
		t.Fatalf("select far view: %v", err)
	}
	if len(sel.Quads) != 1 {
		t.Fatalf("far view selected %d quads, want the root only", len(sel.Quads))
	}
	if sel.Quads[0] != (impl.QuadID{Level: 0, X: 0, Y: 0}) {
		t.Fatalf("far view selected %+v, want the root quad", sel.Quads[0])
	}
	if sel.Root != (impl.QuadID{Level: 0, X: 0, Y: 0}) {
		t.Fatalf("selection root = %+v, want level 0 at 0,0", sel.Root)
	}
}

func TestSelectNearViewRefinesToMaxDepth(t *testing.T) {
	s := mustSelector(t)
	mustTable(t, s)
	sel, err := s.Select(nearView(), 100)
	if err != nil {
		t.Fatalf("select near view: %v", err)
	}
	if len(sel.Quads) != 16 {
		t.Fatalf("near view selected %d quads, want 16 leaves", len(sel.Quads))
	}
	for _, q := range sel.Quads {
		if q.Level != 2 {
			t.Fatalf("near view selected %+v, want only level 2 leaves", q)
		}
	}
	for i := 1; i < len(sel.Quads); i++ {
		prev, cur := sel.Quads[i-1], sel.Quads[i]
		if cur.Level < prev.Level || (cur.Level == prev.Level && cur.X < prev.X) || (cur.Level == prev.Level && cur.X == prev.X && cur.Y < prev.Y) {
			t.Fatalf("selection is not ordered at index %d: %+v after %+v", i, cur, prev)
		}
	}
}

func TestSelectBudgetListsMissingBlocks(t *testing.T) {
	s := mustSelector(t)
	mustTable(t, s)
	sel, err := s.Select(nearView(), 6)
	if err != nil {
		t.Fatalf("select with tight budget: %v", err)
	}
	if sel.Budget != 6 {
		t.Fatalf("selection budget = %d, want 6", sel.Budget)
	}
	if len(sel.Quads) != 6 {
		t.Fatalf("selected %d quads under a budget of 6", len(sel.Quads))
	}
	if len(sel.Missing) != 10 {
		t.Fatalf("missing blocks = %d, want the 10 dropped leaves", len(sel.Missing))
	}
	last := s.LastSelection()
	if len(last.Quads) != 6 || len(last.Missing) != 10 {
		t.Fatalf("last selection = %d quads / %d missing, want 6/10", len(last.Quads), len(last.Missing))
	}
	if stats := s.Stats(); stats.Missing != 10 || stats.Nodes != 6 {
		t.Fatalf("stats = %+v, want 6 nodes and 10 missing", stats)
	}
}

func TestSelectionCoversWholeTerrain(t *testing.T) {
	s := mustSelector(t)
	mustTable(t, s)
	sel, err := s.Select(nearView(), 100)
	if err != nil {
		t.Fatalf("select near view: %v", err)
	}
	if len(sel.Quads) == 0 {
		t.Fatalf("selection must not be empty")
	}
	total := 0.0
	for _, q := range sel.Quads {
		total += quadArea(q.Level)
	}
	if math.Abs(total-4096) > 1e-9 {
		t.Fatalf("selected area = %v, want the full 64x64 terrain", total)
	}
}

func TestNeighbourLevelsDifferByAtMostOne(t *testing.T) {
	s := mustSelector(t)
	mustTable(t, s)
	sel, err := s.Select(nearView(), 100)
	if err != nil {
		t.Fatalf("select near view: %v", err)
	}
	if len(sel.Quads) != 16 {
		t.Fatalf("selected %d quads, want 16 leaves to check adjacency", len(sel.Quads))
	}
	type rect struct{ x0, y0, x1, y1 float64 }
	rectOf := func(q impl.QuadID) rect {
		side := 64 / math.Pow(2, float64(q.Level))
		return rect{x0: float64(q.X) * side, y0: float64(q.Y) * side, x1: float64(q.X+1) * side, y1: float64(q.Y+1) * side}
	}
	for i := 0; i < len(sel.Quads); i++ {
		for j := i + 1; j < len(sel.Quads); j++ {
			a, b := sel.Quads[i], sel.Quads[j]
			ra, rb := rectOf(a), rectOf(b)
			touching := ra.x0 <= rb.x1 && ra.x1 >= rb.x0 && ra.y0 <= rb.y1 && ra.y1 >= rb.y0
			if !touching {
				continue
			}
			diff := a.Level - b.Level
			if diff < 0 {
				diff = -diff
			}
			if diff > 1 {
				t.Fatalf("touching quads %+v and %+v differ by %d levels", a, b, diff)
			}
		}
	}
}

func TestPinAndUnpinValidateQuads(t *testing.T) {
	s := mustSelector(t)
	mustTable(t, s)
	if err := s.Pin(impl.QuadID{Level: 3, X: 0, Y: 0}); !errors.Is(err, impl.ErrInvalidQuad) {
		t.Fatalf("pin beyond max depth must report ErrInvalidQuad, got %v", err)
	}
	if err := s.Pin(impl.QuadID{Level: 1, X: 2, Y: 0}); !errors.Is(err, impl.ErrInvalidQuad) {
		t.Fatalf("pin outside the level grid must report ErrInvalidQuad, got %v", err)
	}
	if err := s.Unpin(impl.QuadID{Level: 1, X: 1, Y: 1}); !errors.Is(err, impl.ErrNotPinned) {
		t.Fatalf("unpin of a free quad must report ErrNotPinned, got %v", err)
	}
}

func TestSnapshotRestoreKeepsPinsAndTable(t *testing.T) {
	s := mustSelector(t)
	mustTable(t, s)
	pin := impl.QuadID{Level: 1, X: 0, Y: 0}
	if err := s.Pin(pin); err != nil {
		t.Fatalf("pin quad: %v", err)
	}
	if _, err := s.Select(nearView(), 100); err != nil {
		t.Fatalf("select: %v", err)
	}
	snap, err := s.Snapshot()
	if err != nil || len(snap) == 0 {
		t.Fatalf("snapshot err=%v len=%d, want a non empty payload", err, len(snap))
	}
	if err := s.SetErrorTable([]impl.GeomError{{Level: 0, Error: 8}, {Level: 1, Error: 32}, {Level: 2, Error: 128}}); err != nil {
		t.Fatalf("replace table: %v", err)
	}
	if err := s.Restore(snap); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got, err := s.ErrorAt(1); err != nil || got != 16 {
		t.Fatalf("restored error at level 1 = %v/%v, want 16/nil", got, err)
	}
	pins := s.Pinned()
	if len(pins) != 1 || pins[0] != pin {
		t.Fatalf("restored pins = %+v, want only %+v", pins, pin)
	}
}

func TestRestoreRejectsCorruptPayload(t *testing.T) {
	s := mustSelector(t)
	if err := s.Restore([]byte("[]")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must report ErrBadSnapshot, got %v", err)
	}
}
