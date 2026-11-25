package task

import (
	"errors"
	"testing"

	impl "gsb/batch49-go-game-b-06"
)

func newAtlas() *impl.ShadowAtlasDefragger {
	return impl.NewShadowAtlasDefragger(2, 8, 2)
}

// fragmentedAtlas builds a page holding tiles in slots 0, 2, 4 and 6, so the
// free slots 1, 3, 5 and 7 form four isolated runs.
func fragmentedAtlas(t *testing.T) (*impl.ShadowAtlasDefragger, map[int]impl.TileHandle) {
	t.Helper()
	a := newAtlas()
	handles := make(map[int]impl.TileHandle, 7)
	for i := 0; i < 7; i++ {
		h, err := a.Alloc(0)
		if err != nil {
			t.Fatalf("alloc %d on an empty page must succeed, got %v", i, err)
		}
		if h.Slot != i {
			t.Fatalf("alloc %d landed on slot %d, want %d", i, h.Slot, i)
		}
		handles[i] = h
	}
	for _, slot := range []int{1, 3, 5} {
		if err := a.Free(handles[slot]); err != nil {
			t.Fatalf("free slot %d: %v", slot, err)
		}
		delete(handles, slot)
	}
	return a, handles
}

func drainPlan(t *testing.T, a *impl.ShadowAtlasDefragger, planID string) {
	t.Helper()
	for i := 0; i < 32; i++ {
		_, err := a.ApplyStep(planID)
		if err != nil {
			if errors.Is(err, impl.ErrPlanDone) {
				return
			}
			t.Fatalf("apply step %d: %v", i, err)
		}
	}
	t.Fatalf("plan %q never finished after 32 steps", planID)
}

func TestAllocReturnsLowestFreeSlot(t *testing.T) {
	a := newAtlas()
	h0, err := a.Alloc(0)
	if err != nil {
		t.Fatalf("first alloc: %v", err)
	}
	h1, err := a.Alloc(0)
	if err != nil {
		t.Fatalf("second alloc: %v", err)
	}
	if h0.Page != 0 || h0.Slot != 0 || h1.Slot != 1 {
		t.Fatalf("slots = (%d,%d), want (0,1) on page 0", h0.Slot, h1.Slot)
	}
	if h0.Gen != 1 || h1.Gen != 1 {
		t.Fatalf("generations = (%d,%d), want both 1", h0.Gen, h1.Gen)
	}
	if !a.HandleValid(h0) || !a.HandleValid(h1) {
		t.Fatalf("fresh handles must stay valid")
	}
	slot, err := a.Slot(h0)
	if err != nil {
		t.Fatalf("slot read of a fresh handle: %v", err)
	}
	if slot.Slot != 0 || slot.Handle.Gen != 1 || slot.Pinned {
		t.Fatalf("slot state = %+v, want slot 0 gen 1 unpinned", slot)
	}
}

func TestAllocRejectsUnknownPage(t *testing.T) {
	a := newAtlas()
	for _, page := range []int{-1, 2, 9} {
		if _, err := a.Alloc(page); !errors.Is(err, impl.ErrNoSuchPage) {
			t.Fatalf("alloc on page %d must fail with ErrNoSuchPage, got %v", page, err)
		}
	}
	if h, err := a.Alloc(1); err != nil || h.Page != 1 || h.Slot != 0 {
		t.Fatalf("valid page must still allocate, got handle %+v err %v", h, err)
	}
}

func TestAllocRejectsRetiredPage(t *testing.T) {
	a := newAtlas()
	if a.Retired(1) {
		t.Fatalf("page 1 must not start retired")
	}
	if err := a.RetirePage(1); err != nil {
		t.Fatalf("retire page 1: %v", err)
	}
	if !a.Retired(1) || a.Retired(0) {
		t.Fatalf("retirement table wrong: page1=%v page0=%v", a.Retired(1), a.Retired(0))
	}
	if _, err := a.Alloc(1); !errors.Is(err, impl.ErrPageRetired) {
		t.Fatalf("alloc on a retired page must fail with ErrPageRetired, got %v", err)
	}
	if _, err := a.Alloc(0); err != nil {
		t.Fatalf("live page must keep allocating, got %v", err)
	}
}

func TestFreeInvalidatesOldHandle(t *testing.T) {
	a := newAtlas()
	h, err := a.Alloc(0)
	if err != nil {
		t.Fatalf("alloc: %v", err)
	}
	if err := a.Free(h); err != nil {
		t.Fatalf("free: %v", err)
	}
	if a.HandleValid(h) {
		t.Fatalf("freed handle must report invalid")
	}
	if _, err := a.Slot(h); !errors.Is(err, impl.ErrStaleHandle) {
		t.Fatalf("slot read of a freed handle must fail with ErrStaleHandle, got %v", err)
	}
	again, err := a.Alloc(0)
	if err != nil {
		t.Fatalf("realloc after free: %v", err)
	}
	if again.Slot != 0 || again.Gen != 2 {
		t.Fatalf("realloc = slot %d gen %d, want slot 0 gen 2", again.Slot, again.Gen)
	}
	if !a.HandleValid(again) {
		t.Fatalf("reallocated handle must be valid")
	}
}

func TestRequestDefragCompactsTowardFront(t *testing.T) {
	a, handles := fragmentedAtlas(t)
	if got := a.Fragmentation(0); got != 1.0 {
		t.Fatalf("fragmentation before defrag = %v, want 1", got)
	}
	plan, err := a.RequestDefrag(0)
	if err != nil {
		t.Fatalf("request defrag: %v", err)
	}
	if plan.ID == "" || plan.Page != 0 {
		t.Fatalf("plan identity = %+v, want non empty id on page 0", plan)
	}
	if len(plan.Moves) != 3 {
		t.Fatalf("plan moves = %d, want 3 (%+v)", len(plan.Moves), plan.Moves)
	}
	want := []struct{ from, to int }{{2, 1}, {4, 2}, {6, 3}}
	for i, w := range want {
		if plan.Moves[i].From.Slot != w.from || plan.Moves[i].To != w.to {
			t.Fatalf("move %d = %d->%d, want %d->%d", i, plan.Moves[i].From.Slot, plan.Moves[i].To, w.from, w.to)
		}
		if plan.Moves[i].From.Page != 0 || plan.Moves[i].From.Gen != handles[w.from].Gen {
			t.Fatalf("move %d carries a foreign source handle: %+v", i, plan.Moves[i].From)
		}
	}
	if !a.PlanActive(0) || a.PlanActive(1) {
		t.Fatalf("plan activity wrong: page0=%v page1=%v", a.PlanActive(0), a.PlanActive(1))
	}
}

func TestRequestDefragSkipsPinnedTiles(t *testing.T) {
	a, handles := fragmentedAtlas(t)
	if err := a.Pin(handles[2]); err != nil {
		t.Fatalf("pin slot 2: %v", err)
	}
	plan, err := a.RequestDefrag(0)
	if err != nil {
		t.Fatalf("request defrag: %v", err)
	}
	if len(plan.Moves) != 2 {
		t.Fatalf("plan moves = %d (%+v), want 2 while slot 2 is pinned", len(plan.Moves), plan.Moves)
	}
	if plan.Moves[0].From.Slot != 4 || plan.Moves[0].To != 1 {
		t.Fatalf("first move = %d->%d, want 4->1", plan.Moves[0].From.Slot, plan.Moves[0].To)
	}
	if plan.Moves[1].From.Slot != 6 || plan.Moves[1].To != 3 {
		t.Fatalf("second move = %d->%d, want 6->3", plan.Moves[1].From.Slot, plan.Moves[1].To)
	}
	for _, mv := range plan.Moves {
		if mv.From.Slot == 2 || mv.To == 2 {
			t.Fatalf("pinned tile must not appear in the plan: %+v", mv)
		}
	}
	if err := a.Unpin(handles[2]); err != nil {
		t.Fatalf("unpin slot 2: %v", err)
	}
}

func TestRequestDefragRejectsCompactPage(t *testing.T) {
	a := newAtlas()
	for i := 0; i < 3; i++ {
		if _, err := a.Alloc(0); err != nil {
			t.Fatalf("alloc %d: %v", i, err)
		}
	}
	if got := a.Fragmentation(0); got != 0.25 {
		t.Fatalf("fragmentation of a packed front = %v, want 0.25", got)
	}
	if _, err := a.RequestDefrag(0); !errors.Is(err, impl.ErrNotFragmented) {
		t.Fatalf("defrag of a compact page must fail with ErrNotFragmented, got %v", err)
	}
}

func TestRequestDefragRejectsBadPage(t *testing.T) {
	a := newAtlas()
	for _, page := range []int{-1, 7} {
		if _, err := a.RequestDefrag(page); !errors.Is(err, impl.ErrNoSuchPage) {
			t.Fatalf("defrag on page %d must fail with ErrNoSuchPage, got %v", page, err)
		}
	}
	if err := a.RetirePage(1); err != nil {
		t.Fatalf("retire page 1: %v", err)
	}
	if _, err := a.RequestDefrag(1); !errors.Is(err, impl.ErrPageRetired) {
		t.Fatalf("defrag on a retired page must fail with ErrPageRetired, got %v", err)
	}
}

func TestAllocBlockedWhilePlanActive(t *testing.T) {
	a, _ := fragmentedAtlas(t)
	plan, err := a.RequestDefrag(0)
	if err != nil {
		t.Fatalf("request defrag: %v", err)
	}
	if _, err := a.Alloc(0); !errors.Is(err, impl.ErrPageBusy) {
		t.Fatalf("alloc on a page under defrag must fail with ErrPageBusy, got %v", err)
	}
	if _, err := a.Alloc(1); err != nil {
		t.Fatalf("other pages must keep allocating, got %v", err)
	}
	drainPlan(t, a, plan.ID)
	next, err := a.Alloc(0)
	if err != nil {
		t.Fatalf("alloc after the plan finished: %v", err)
	}
	if next.Slot != 4 {
		t.Fatalf("alloc after defrag landed on slot %d, want 4", next.Slot)
	}
}

func TestApplyStepHonorsMoveBudget(t *testing.T) {
	a, _ := fragmentedAtlas(t)
	plan, err := a.RequestDefrag(0)
	if err != nil {
		t.Fatalf("request defrag: %v", err)
	}
	first, err := a.ApplyStep(plan.ID)
	if err != nil {
		t.Fatalf("first step: %v", err)
	}
	if first.Count != 2 || first.Remaining != 1 {
		t.Fatalf("first step = count %d remaining %d, want 2 and 1", first.Count, first.Remaining)
	}
	if first.PlanID != plan.ID || first.Page != 0 {
		t.Fatalf("first step identity = %+v", first)
	}
	second, err := a.ApplyStep(plan.ID)
	if err != nil {
		t.Fatalf("second step: %v", err)
	}
	if second.Count != 1 || second.Remaining != 0 {
		t.Fatalf("second step = count %d remaining %d, want 1 and 0", second.Count, second.Remaining)
	}
	if a.PlanActive(0) {
		t.Fatalf("plan must stop being active once every move ran")
	}
}

func TestApplyStepUnknownAndFinishedPlan(t *testing.T) {
	a, _ := fragmentedAtlas(t)
	if _, err := a.ApplyStep("plan-missing"); !errors.Is(err, impl.ErrUnknownPlan) {
		t.Fatalf("unknown plan must fail with ErrUnknownPlan, got %v", err)
	}
	plan, err := a.RequestDefrag(0)
	if err != nil {
		t.Fatalf("request defrag: %v", err)
	}
	drainPlan(t, a, plan.ID)
	if _, err := a.ApplyStep(plan.ID); !errors.Is(err, impl.ErrPlanDone) {
		t.Fatalf("finished plan must fail with ErrPlanDone, got %v", err)
	}
}

func TestMigratedTileHandleGeneration(t *testing.T) {
	a, handles := fragmentedAtlas(t)
	moved := handles[4]
	plan, err := a.RequestDefrag(0)
	if err != nil {
		t.Fatalf("request defrag: %v", err)
	}
	drainPlan(t, a, plan.ID)
	if a.HandleValid(moved) {
		t.Fatalf("handle of a migrated tile must go stale")
	}
	if _, err := a.Slot(moved); !errors.Is(err, impl.ErrStaleHandle) {
		t.Fatalf("slot read of a migrated handle must fail with ErrStaleHandle, got %v", err)
	}
	fresh, ok := a.Lookup(0, 2)
	if !ok {
		t.Fatalf("lookup of the destination slot must resolve")
	}
	if fresh.Slot != 2 || fresh.Gen != moved.Gen+1 {
		t.Fatalf("destination handle = %+v, want slot 2 gen %d", fresh, moved.Gen+1)
	}
	if !a.HandleValid(fresh) {
		t.Fatalf("destination handle must be valid")
	}
	if !a.HandleValid(handles[0]) || handles[0].Gen != 1 {
		t.Fatalf("tile already at the front must keep its handle, got %+v", handles[0])
	}
}

func TestFragmentationDropsAfterDefrag(t *testing.T) {
	a, _ := fragmentedAtlas(t)
	before := a.Fragmentation(0)
	if before != 1.0 {
		t.Fatalf("fragmentation before = %v, want 1", before)
	}
	if got := a.Fragmentation(1); got != 0.125 {
		t.Fatalf("fragmentation of an untouched page = %v, want 0.125", got)
	}
	plan, err := a.RequestDefrag(0)
	if err != nil {
		t.Fatalf("request defrag: %v", err)
	}
	drainPlan(t, a, plan.ID)
	after := a.Fragmentation(0)
	if after != 0.25 {
		t.Fatalf("fragmentation after = %v, want 0.25", after)
	}
	if after >= before {
		t.Fatalf("fragmentation must strictly drop, before %v after %v", before, after)
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	a := newAtlas()
	h0, err := a.Alloc(0)
	if err != nil {
		t.Fatalf("alloc 0: %v", err)
	}
	h1, err := a.Alloc(0)
	if err != nil {
		t.Fatalf("alloc 1: %v", err)
	}
	h2, err := a.Alloc(0)
	if err != nil {
		t.Fatalf("alloc 2: %v", err)
	}
	snap, err := a.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snap) == 0 {
		t.Fatalf("snapshot payload must not be empty")
	}
	if err := a.Free(h2); err != nil {
		t.Fatalf("free before restore: %v", err)
	}
	if err := a.Restore(snap); err != nil {
		t.Fatalf("restore: %v", err)
	}
	for _, h := range []impl.TileHandle{h0, h1, h2} {
		if !a.HandleValid(h) {
			t.Fatalf("handle %+v must survive the restore", h)
		}
	}
	next, err := a.Alloc(0)
	if err != nil {
		t.Fatalf("alloc after restore: %v", err)
	}
	if next.Slot != 3 || next.Gen != 1 {
		t.Fatalf("alloc after restore = slot %d gen %d, want slot 3 gen 1", next.Slot, next.Gen)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	a := newAtlas()
	h, err := a.Alloc(0)
	if err != nil {
		t.Fatalf("alloc: %v", err)
	}
	if err := a.Restore([]byte("{not a snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt payload must fail with ErrBadSnapshot, got %v", err)
	}
	if !a.HandleValid(h) {
		t.Fatalf("a rejected restore must leave the current atlas untouched")
	}
	next, err := a.Alloc(0)
	if err != nil || next.Slot != 1 {
		t.Fatalf("alloc after a rejected restore = slot %d err %v, want slot 1", next.Slot, err)
	}
}
