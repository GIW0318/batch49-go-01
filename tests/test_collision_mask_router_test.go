package task

import (
	"errors"
	"testing"

	impl "gsb/batch49-go-game-a-14"
)

func routerOptions() impl.Options {
	return impl.Options{MaxObjects: 32, MaxEvents: 16}
}

func addMask(t *testing.T, r *impl.CollisionMaskRouter, id string, layer, mask uint32, kind string) {
	t.Helper()
	if err := r.SetLayer(id, layer, mask, kind); err != nil {
		t.Fatalf("SetLayer(%s) failed: %v", id, err)
	}
}

func TestSetLayerStoresSpec(t *testing.T) {
	r := impl.NewCollisionMaskRouter(routerOptions())
	addMask(t, r, "player", 1, 6, impl.KindSolid)
	spec, ok := r.Layer("player")
	if !ok {
		t.Fatalf("stored layer spec must be readable")
	}
	if spec.ID != "player" || spec.Layer != 1 || spec.Mask != 6 || spec.Kind != impl.KindSolid {
		t.Fatalf("spec = %+v, want layer 1 mask 6 solid", spec)
	}
}

func TestSetLayerRejectsBadKind(t *testing.T) {
	r := impl.NewCollisionMaskRouter(routerOptions())
	if err := r.SetLayer("wall", 1, 1, "ghost"); !errors.Is(err, impl.ErrBadKind) {
		t.Fatalf("unknown kind must be rejected, got %v", err)
	}
	if _, ok := r.Layer("wall"); ok {
		t.Fatalf("rejected objects must not enter the layer table")
	}
	if err := r.SetLayer("  ", 1, 1, impl.KindSolid); !errors.Is(err, impl.ErrBlankID) {
		t.Fatalf("blank id must be rejected, got %v", err)
	}
}

func TestSetLayerEnforcesObjectLimit(t *testing.T) {
	r := impl.NewCollisionMaskRouter(impl.Options{MaxObjects: 2, MaxEvents: 8})
	addMask(t, r, "a", 1, 1, impl.KindSolid)
	addMask(t, r, "b", 1, 1, impl.KindSolid)
	if err := r.SetLayer("c", 1, 1, impl.KindSolid); !errors.Is(err, impl.ErrObjectLimit) {
		t.Fatalf("third object must exceed the limit, got %v", err)
	}
}

func TestPairsRequiresBidirectionalMask(t *testing.T) {
	r := impl.NewCollisionMaskRouter(routerOptions())
	addMask(t, r, "a", 1, 2, impl.KindSolid)
	addMask(t, r, "b", 2, 1, impl.KindSolid)
	addMask(t, r, "c", 4, 2, impl.KindSolid)
	if !r.Pairs("a", "b") {
		t.Fatalf("a and b accept each other and must pair")
	}
	if r.Pairs("b", "c") {
		t.Fatalf("one sided mask must not pair b and c")
	}
}

func TestPairsUnknownOrSameIsFalse(t *testing.T) {
	r := impl.NewCollisionMaskRouter(routerOptions())
	addMask(t, r, "a", 1, 1, impl.KindSolid)
	if r.Pairs("a", "a") {
		t.Fatalf("an object must not pair with itself")
	}
	if r.Pairs("a", "ghost") {
		t.Fatalf("unknown objects must not pair")
	}
}

func TestUpdateEmitsEnterOnce(t *testing.T) {
	r := impl.NewCollisionMaskRouter(routerOptions())
	addMask(t, r, "a", 1, 2, impl.KindSolid)
	addMask(t, r, "b", 2, 1, impl.KindSolid)
	events, err := r.Update(impl.Step{At: 1, Overlaps: []impl.Pair{{A: "a", B: "b"}}})
	if err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %+v, want exactly one enter", events)
	}
	ev := events[0]
	if ev.Kind != impl.EventEnter || ev.A != "a" || ev.B != "b" {
		t.Fatalf("event = %+v, want enter a b", ev)
	}
	if ev.Trigger {
		t.Fatalf("two solid objects must not emit a trigger event")
	}
	if ev.Ticks != 1 {
		t.Fatalf("first enter ticks = %d, want 1", ev.Ticks)
	}
}

func TestUpdateNormalizesPair(t *testing.T) {
	r := impl.NewCollisionMaskRouter(routerOptions())
	addMask(t, r, "a", 1, 2, impl.KindSolid)
	addMask(t, r, "b", 2, 1, impl.KindSolid)
	events, err := r.Update(impl.Step{At: 1, Overlaps: []impl.Pair{{A: "b", B: "a"}}})
	if err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if len(events) != 1 || events[0].A != "a" || events[0].B != "b" {
		t.Fatalf("events = %+v, want normalized a b", events)
	}
}

func TestUpdateDedupesRepeatedOverlap(t *testing.T) {
	r := impl.NewCollisionMaskRouter(routerOptions())
	addMask(t, r, "a", 1, 2, impl.KindSolid)
	addMask(t, r, "b", 2, 1, impl.KindSolid)
	events, err := r.Update(impl.Step{At: 1, Overlaps: []impl.Pair{{A: "a", B: "b"}, {A: "b", B: "a"}, {A: "a", B: "b"}}})
	if err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if len(events) != 1 || events[0].Kind != impl.EventEnter {
		t.Fatalf("events = %+v, want a single enter", events)
	}
}

func TestUpdateEmitsLeave(t *testing.T) {
	r := impl.NewCollisionMaskRouter(routerOptions())
	addMask(t, r, "a", 1, 2, impl.KindSolid)
	addMask(t, r, "b", 2, 1, impl.KindSolid)
	if _, err := r.Update(impl.Step{At: 1, Overlaps: []impl.Pair{{A: "a", B: "b"}}}); err != nil {
		t.Fatalf("first update failed: %v", err)
	}
	events, err := r.Update(impl.Step{At: 2})
	if err != nil {
		t.Fatalf("second update failed: %v", err)
	}
	if len(events) != 1 || events[0].Kind != impl.EventLeave || events[0].A != "a" || events[0].B != "b" {
		t.Fatalf("events = %+v, want one leave for a b", events)
	}
	if r.Entered("a", "b") {
		t.Fatalf("pair must not stay entered after leave")
	}
}

func TestUpdateStayIncrementsTicks(t *testing.T) {
	r := impl.NewCollisionMaskRouter(routerOptions())
	addMask(t, r, "a", 1, 2, impl.KindSolid)
	addMask(t, r, "b", 2, 1, impl.KindSolid)
	if _, err := r.Update(impl.Step{At: 1, Overlaps: []impl.Pair{{A: "a", B: "b"}}}); err != nil {
		t.Fatalf("first update failed: %v", err)
	}
	events, err := r.Update(impl.Step{At: 2, Overlaps: []impl.Pair{{A: "a", B: "b"}}})
	if err != nil {
		t.Fatalf("second update failed: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %+v, want one stay", events)
	}
	if events[0].Kind != impl.EventStay {
		t.Fatalf("event kind = %q, want stay", events[0].Kind)
	}
	if events[0].Ticks != 2 {
		t.Fatalf("stay ticks = %d, want 2", events[0].Ticks)
	}
}

func TestUpdateSortsEventsByPair(t *testing.T) {
	r := impl.NewCollisionMaskRouter(routerOptions())
	addMask(t, r, "a", 1, 7, impl.KindSolid)
	addMask(t, r, "b", 1, 7, impl.KindSolid)
	addMask(t, r, "c", 1, 7, impl.KindSolid)
	events, err := r.Update(impl.Step{At: 1, Overlaps: []impl.Pair{{A: "c", B: "b"}, {A: "a", B: "c"}, {A: "b", B: "a"}}})
	if err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("events = %+v, want three enters", events)
	}
	want := []impl.Pair{{A: "a", B: "b"}, {A: "a", B: "c"}, {A: "b", B: "c"}}
	for i, pair := range want {
		if events[i].A != pair.A || events[i].B != pair.B {
			t.Fatalf("event %d = %s-%s, want %s-%s", i, events[i].A, events[i].B, pair.A, pair.B)
		}
	}
}

func TestUpdateMarksTriggerPairs(t *testing.T) {
	r := impl.NewCollisionMaskRouter(routerOptions())
	addMask(t, r, "player", 1, 2, impl.KindSolid)
	addMask(t, r, "portal", 2, 1, impl.KindTrigger)
	events, err := r.Update(impl.Step{At: 1, Overlaps: []impl.Pair{{A: "player", B: "portal"}}})
	if err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %+v, want one event", events)
	}
	if !events[0].Trigger {
		t.Fatalf("pair with a trigger object must be marked as trigger")
	}
}

func TestEnteredAndActivePairs(t *testing.T) {
	r := impl.NewCollisionMaskRouter(routerOptions())
	addMask(t, r, "a", 1, 2, impl.KindSolid)
	addMask(t, r, "b", 2, 1, impl.KindSolid)
	if _, err := r.Update(impl.Step{At: 1, Overlaps: []impl.Pair{{A: "b", B: "a"}}}); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if !r.Entered("b", "a") {
		t.Fatalf("pair must be entered regardless of argument order")
	}
	active := r.ActivePairs()
	if len(active) != 1 || active[0].A != "a" || active[0].B != "b" {
		t.Fatalf("active pairs = %+v, want normalized a b", active)
	}
	if _, err := r.Update(impl.Step{At: 2}); err != nil {
		t.Fatalf("leave update failed: %v", err)
	}
	if r.Entered("a", "b") {
		t.Fatalf("pair must leave the active set")
	}
	if active := r.ActivePairs(); len(active) != 0 {
		t.Fatalf("active pairs = %+v, want none", active)
	}
}

func TestRemoveObjectDropsPair(t *testing.T) {
	r := impl.NewCollisionMaskRouter(routerOptions())
	addMask(t, r, "a", 1, 2, impl.KindSolid)
	addMask(t, r, "b", 2, 1, impl.KindSolid)
	addMask(t, r, "c", 2, 1, impl.KindSolid)
	if err := r.RemoveObject("b"); err != nil {
		t.Fatalf("remove failed: %v", err)
	}
	if _, ok := r.Layer("b"); ok {
		t.Fatalf("removed object must leave the layer table")
	}
	if err := r.RemoveObject("b"); !errors.Is(err, impl.ErrUnknownObject) {
		t.Fatalf("removing a missing object must fail, got %v", err)
	}
	ids := r.ObjectIDs()
	if len(ids) != 2 || ids[0] != "a" || ids[1] != "c" {
		t.Fatalf("object ids = %v, want [a c]", ids)
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	r := impl.NewCollisionMaskRouter(routerOptions())
	addMask(t, r, "a", 1, 2, impl.KindSolid)
	addMask(t, r, "b", 2, 1, impl.KindTrigger)
	if _, err := r.Update(impl.Step{At: 1, Overlaps: []impl.Pair{{A: "a", B: "b"}}}); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	snap, err := r.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	restored := impl.NewCollisionMaskRouter(routerOptions())
	if err := restored.Restore(snap); err != nil {
		t.Fatalf("restore failed: %v", err)
	}
	if !restored.Entered("a", "b") {
		t.Fatalf("restored router must keep the active pair")
	}
	if active := restored.ActivePairs(); len(active) != 1 {
		t.Fatalf("restored active pairs = %+v, want one", active)
	}
	events, err := restored.Update(impl.Step{At: 2, Overlaps: []impl.Pair{{A: "a", B: "b"}}})
	if err != nil {
		t.Fatalf("update after restore failed: %v", err)
	}
	if len(events) != 1 || events[0].Kind != impl.EventStay || events[0].Ticks != 2 {
		t.Fatalf("events after restore = %+v, want stay with ticks 2", events)
	}
	if err := restored.Restore([]byte("<<broken>>")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("broken snapshot must be rejected, got %v", err)
	}
}
