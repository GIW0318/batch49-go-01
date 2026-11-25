package task

import (
	"errors"
	"testing"

	impl "gsb/batch49-go-game-a-01"
)

func newBag(stack, slots int) *impl.StackDefragmenter {
	return impl.NewStackDefragmenter(impl.Options{StackSize: stack, MaxSlots: slots})
}

func mustPut(t *testing.T, bag *impl.StackDefragmenter, owner string, lot impl.Lot) {
	t.Helper()
	if err := bag.Put(owner, lot); err != nil {
		t.Fatalf("put lot %s of %s: %v", lot.UID, lot.ItemID, err)
	}
}

func lotUIDs(lots []impl.Lot) []string {
	out := make([]string, 0, len(lots))
	for _, lot := range lots {
		out = append(out, lot.UID)
	}
	return out
}

func TestPutStoresLotWithSequence(t *testing.T) {
	bag := newBag(10, 8)
	if err := bag.Put("hero", impl.Lot{UID: "lot-a", ItemID: "potion", Qty: 4, Bind: impl.BindFree, ExpireAt: 100}); err != nil {
		t.Fatalf("put single lot: %v", err)
	}
	lots := bag.Lots("hero", "potion")
	if len(lots) != 1 {
		t.Fatalf("lots = %d, want 1", len(lots))
	}
	if lots[0].UID != "lot-a" || lots[0].Qty != 4 || lots[0].ExpireAt != 100 {
		t.Fatalf("stored lot mismatch: %+v", lots[0])
	}
	if lots[0].Seq != 1 {
		t.Fatalf("first inserted lot seq = %d, want 1", lots[0].Seq)
	}
	if got := bag.Slots("hero"); got != 1 {
		t.Fatalf("slots = %d, want 1", got)
	}
	if got := bag.Total("hero", "potion"); got != 4 {
		t.Fatalf("total = %d, want 4", got)
	}
	mustPut(t, bag, "hero", impl.Lot{UID: "lot-b", ItemID: "potion", Qty: 1, Bind: impl.BindFree, ExpireAt: 100})
	second := bag.Lots("hero", "potion")
	if len(second) != 2 || second[1].Seq != 2 {
		t.Fatalf("second lot must carry seq 2, got %+v", second)
	}
}

func TestPutRejectsBlankOwnerAndItem(t *testing.T) {
	bag := newBag(10, 8)
	good := impl.Lot{UID: "lot-a", ItemID: "potion", Qty: 1, Bind: impl.BindFree, ExpireAt: 1}
	if err := bag.Put("  ", good); !errors.Is(err, impl.ErrBlankOwner) {
		t.Fatalf("blank owner must be rejected with ErrBlankOwner, got %v", err)
	}
	if err := bag.Put("hero", good); err != nil {
		t.Fatalf("well formed lot must be accepted, got %v", err)
	}
	bad := good
	bad.ItemID = " "
	if err := bag.Put("hero", bad); !errors.Is(err, impl.ErrBadLot) {
		t.Fatalf("blank item id must be rejected with ErrBadLot, got %v", err)
	}
	bad = good
	bad.UID = ""
	if err := bag.Put("hero", bad); !errors.Is(err, impl.ErrBadLot) {
		t.Fatalf("blank lot uid must be rejected with ErrBadLot, got %v", err)
	}
}

func TestPutRejectsBadQuantityAndBind(t *testing.T) {
	bag := newBag(10, 8)
	for _, lot := range []impl.Lot{
		{UID: "zero", ItemID: "potion", Qty: 0, Bind: impl.BindFree},
		{UID: "negative", ItemID: "potion", Qty: -3, Bind: impl.BindFree},
		{UID: "overflow", ItemID: "potion", Qty: 11, Bind: impl.BindFree},
		{UID: "soulbound", ItemID: "potion", Qty: 1, Bind: "soul"},
		{UID: "nobind", ItemID: "potion", Qty: 1},
	} {
		if err := bag.Put("hero", lot); !errors.Is(err, impl.ErrBadLot) {
			t.Fatalf("lot %q must be rejected with ErrBadLot, got %v", lot.UID, err)
		}
	}
	if got := bag.Slots("hero"); got != 0 {
		t.Fatalf("rejected lots must not occupy slots, got %d", got)
	}
}

func TestPutEnforcesSlotCap(t *testing.T) {
	bag := newBag(10, 2)
	mustPut(t, bag, "hero", impl.Lot{UID: "lot-1", ItemID: "potion", Qty: 1, Bind: impl.BindFree})
	mustPut(t, bag, "hero", impl.Lot{UID: "lot-2", ItemID: "ore", Qty: 1, Bind: impl.BindFree})
	err := bag.Put("hero", impl.Lot{UID: "lot-3", ItemID: "potion", Qty: 1, Bind: impl.BindFree})
	if !errors.Is(err, impl.ErrBagFull) {
		t.Fatalf("third lot must hit the slot cap with ErrBagFull, got %v", err)
	}
	if got := bag.Slots("hero"); got != 2 {
		t.Fatalf("slots = %d after a full bag rejection, want 2", got)
	}
	mustPut(t, bag, "mage", impl.Lot{UID: "lot-9", ItemID: "potion", Qty: 1, Bind: impl.BindFree})
	if got := bag.Slots("mage"); got != 1 {
		t.Fatalf("slot cap must be per owner, mage slots = %d", got)
	}
}

func TestLotsOrderedByExpiryThenSequence(t *testing.T) {
	bag := newBag(10, 8)
	mustPut(t, bag, "hero", impl.Lot{UID: "late", ItemID: "potion", Qty: 1, Bind: impl.BindFree, ExpireAt: 300})
	mustPut(t, bag, "hero", impl.Lot{UID: "early", ItemID: "potion", Qty: 1, Bind: impl.BindFree, ExpireAt: 100})
	mustPut(t, bag, "hero", impl.Lot{UID: "mid", ItemID: "potion", Qty: 1, Bind: impl.BindFree, ExpireAt: 200})
	mustPut(t, bag, "hero", impl.Lot{UID: "tie", ItemID: "potion", Qty: 1, Bind: impl.BindFree, ExpireAt: 200})
	want := []string{"early", "mid", "tie", "late"}
	got := lotUIDs(bag.Lots("hero", "potion"))
	if len(got) != len(want) {
		t.Fatalf("lots = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("lots = %v, want %v", got, want)
		}
	}
}

func TestItemsListsDistinctSortedIDs(t *testing.T) {
	bag := newBag(10, 8)
	mustPut(t, bag, "hero", impl.Lot{UID: "l1", ItemID: "ore", Qty: 1, Bind: impl.BindFree})
	mustPut(t, bag, "hero", impl.Lot{UID: "l2", ItemID: "axe", Qty: 1, Bind: impl.BindFree})
	mustPut(t, bag, "hero", impl.Lot{UID: "l3", ItemID: "ore", Qty: 1, Bind: impl.BindFree})
	got := bag.Items("hero")
	want := []string{"axe", "ore"}
	if len(got) != len(want) {
		t.Fatalf("items = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("items = %v, want %v", got, want)
		}
	}
	if got := bag.Items("nobody"); len(got) != 0 {
		t.Fatalf("unknown owner must list no items, got %v", got)
	}
}

func TestTotalSumsQuantityAcrossLots(t *testing.T) {
	bag := newBag(10, 8)
	mustPut(t, bag, "hero", impl.Lot{UID: "p1", ItemID: "potion", Qty: 3, Bind: impl.BindFree})
	mustPut(t, bag, "hero", impl.Lot{UID: "p2", ItemID: "potion", Qty: 4, Bind: impl.BindBound})
	mustPut(t, bag, "hero", impl.Lot{UID: "o1", ItemID: "ore", Qty: 2, Bind: impl.BindFree})
	if got := bag.Total("hero", "potion"); got != 7 {
		t.Fatalf("potion total = %d, want 7", got)
	}
	if got := bag.Total("hero", "ore"); got != 2 {
		t.Fatalf("ore total = %d, want 2", got)
	}
	if got := bag.Total("hero", "missing"); got != 0 {
		t.Fatalf("missing item total = %d, want 0", got)
	}
}

func TestPlanIsReadOnly(t *testing.T) {
	bag := newBag(10, 8)
	mustPut(t, bag, "hero", impl.Lot{UID: "p1", ItemID: "potion", Qty: 2, Bind: impl.BindFree, ExpireAt: 100})
	mustPut(t, bag, "hero", impl.Lot{UID: "p2", ItemID: "potion", Qty: 3, Bind: impl.BindFree, ExpireAt: 200})
	mustPut(t, bag, "hero", impl.Lot{UID: "o1", ItemID: "ore", Qty: 4, Bind: impl.BindFree, ExpireAt: 300})
	beforeSlots := bag.Slots("hero")
	beforeTotal := bag.Total("hero", "potion")
	plan, err := bag.Plan("hero")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.Moves != 1 {
		t.Fatalf("plan moves = %d, want 1", plan.Moves)
	}
	if bag.Slots("hero") != beforeSlots {
		t.Fatalf("plan must not change slot count: %d -> %d", beforeSlots, bag.Slots("hero"))
	}
	if bag.Total("hero", "potion") != beforeTotal {
		t.Fatalf("plan must not change quantity: %d -> %d", beforeTotal, bag.Total("hero", "potion"))
	}
	lots := bag.Lots("hero", "potion")
	if len(lots) != 2 || lots[0].Qty != 2 || lots[1].Qty != 3 {
		t.Fatalf("plan must not rewrite lots, got %+v", lots)
	}
}

func TestPlanMergesFragmentsOfOneItem(t *testing.T) {
	bag := newBag(10, 8)
	mustPut(t, bag, "hero", impl.Lot{UID: "lot-a", ItemID: "potion", Qty: 2, Bind: impl.BindFree, ExpireAt: 100})
	mustPut(t, bag, "hero", impl.Lot{UID: "lot-b", ItemID: "potion", Qty: 3, Bind: impl.BindFree, ExpireAt: 300})
	plan, err := bag.Plan("hero")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Merges) != 1 {
		t.Fatalf("plan merges = %d, want 1", len(plan.Merges))
	}
	m := plan.Merges[0]
	if m.ItemID != "potion" || m.Bind != impl.BindFree {
		t.Fatalf("merge group mismatch: %+v", m)
	}
	if m.Carrier != "lot-a" {
		t.Fatalf("carrier = %q, want the earliest expiring lot-a", m.Carrier)
	}
	if len(m.Absorb) != 1 || m.Absorb[0] != "lot-b" {
		t.Fatalf("absorbed lots = %v, want [lot-b]", m.Absorb)
	}
	if m.Qty != 5 {
		t.Fatalf("merged quantity = %d, want 5", m.Qty)
	}
	if plan.Fragments != 2 {
		t.Fatalf("fragments = %d, want 2", plan.Fragments)
	}
}

func TestCompactFreesSlotAndKeepsTotal(t *testing.T) {
	bag := newBag(10, 8)
	mustPut(t, bag, "hero", impl.Lot{UID: "lot-a", ItemID: "potion", Qty: 2, Bind: impl.BindFree, ExpireAt: 100})
	mustPut(t, bag, "hero", impl.Lot{UID: "lot-b", ItemID: "potion", Qty: 3, Bind: impl.BindFree, ExpireAt: 300})
	result, err := bag.Compact("hero", 8)
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	if result.Moves != 1 || result.Merges != 1 || result.Freed != 1 {
		t.Fatalf("compact result = %+v, want moves 1 merges 1 freed 1", result)
	}
	if got := bag.Slots("hero"); got != 1 {
		t.Fatalf("slots after compact = %d, want 1", got)
	}
	if got := bag.Total("hero", "potion"); got != 5 {
		t.Fatalf("total after compact = %d, want 5", got)
	}
	lots := bag.Lots("hero", "potion")
	if len(lots) != 1 || lots[0].UID != "lot-a" || lots[0].Qty != 5 {
		t.Fatalf("compacted lots = %+v, want lot-a with 5", lots)
	}
}

func TestCompactSkipsFullStacksAndChecksBudget(t *testing.T) {
	bag := newBag(10, 8)
	mustPut(t, bag, "hero", impl.Lot{UID: "full", ItemID: "potion", Qty: 10, Bind: impl.BindFree})
	plan, err := bag.Plan("hero")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.Moves != 0 || len(plan.Merges) != 0 {
		t.Fatalf("full stack must not be planned, got %+v", plan)
	}
	if _, err := bag.Compact("hero", -1); !errors.Is(err, impl.ErrBadBudget) {
		t.Fatalf("negative budget must be rejected with ErrBadBudget, got %v", err)
	}
	result, err := bag.Compact("hero", 8)
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	if result.Freed != 0 || result.Moves != 0 {
		t.Fatalf("full stack compact result = %+v, want no moves", result)
	}
	if got := bag.Slots("hero"); got != 1 {
		t.Fatalf("slots = %d, want 1", got)
	}
}

func TestConsumeTakesEarliestExpiryFirst(t *testing.T) {
	bag := newBag(10, 8)
	mustPut(t, bag, "hero", impl.Lot{UID: "lot-late", ItemID: "potion", Qty: 5, Bind: impl.BindFree, ExpireAt: 900})
	mustPut(t, bag, "hero", impl.Lot{UID: "lot-early", ItemID: "potion", Qty: 5, Bind: impl.BindFree, ExpireAt: 100})
	uses, err := bag.Consume("hero", "potion", 2)
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if len(uses) != 1 {
		t.Fatalf("uses = %+v, want a single debit", uses)
	}
	if uses[0].LotUID != "lot-early" || uses[0].Qty != 2 || uses[0].ExpireAt != 100 {
		t.Fatalf("first debit = %+v, want lot-early x2 expiring at 100", uses[0])
	}
	lots := bag.Lots("hero", "potion")
	if len(lots) != 2 || lots[0].UID != "lot-early" || lots[0].Qty != 3 {
		t.Fatalf("lots after consume = %+v, want lot-early with 3", lots)
	}
	if got := bag.Total("hero", "potion"); got != 8 {
		t.Fatalf("total after consume = %d, want 8", got)
	}
}

func TestConsumePartialKeepsLotIdentity(t *testing.T) {
	bag := newBag(10, 8)
	mustPut(t, bag, "hero", impl.Lot{UID: "lot-x", ItemID: "potion", Qty: 6, Bind: impl.BindFree, ExpireAt: 400})
	uses, err := bag.Consume("hero", "potion", 4)
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if len(uses) != 1 || uses[0].Qty != 4 || uses[0].LotUID != "lot-x" {
		t.Fatalf("uses = %+v, want lot-x x4", uses)
	}
	lots := bag.Lots("hero", "potion")
	if len(lots) != 1 {
		t.Fatalf("lots = %d, want the lot to survive", len(lots))
	}
	if lots[0].UID != "lot-x" || lots[0].Qty != 2 || lots[0].ExpireAt != 400 {
		t.Fatalf("remaining lot = %+v, want lot-x with 2 and expiry 400", lots[0])
	}
}

func TestConsumeRejectsShortageAndBadQuantity(t *testing.T) {
	bag := newBag(10, 8)
	mustPut(t, bag, "hero", impl.Lot{UID: "lot-a", ItemID: "potion", Qty: 3, Bind: impl.BindFree, ExpireAt: 100})
	if _, err := bag.Consume("hero", "potion", 5); !errors.Is(err, impl.ErrShortage) {
		t.Fatalf("shortage must return ErrShortage, got %v", err)
	}
	if got := bag.Total("hero", "potion"); got != 3 {
		t.Fatalf("failed consume changed total to %d, want 3", got)
	}
	if _, err := bag.Consume("hero", "potion", 0); !errors.Is(err, impl.ErrBadQty) {
		t.Fatalf("zero quantity must return ErrBadQty, got %v", err)
	}
	if _, err := bag.Consume("hero", "potion", -2); !errors.Is(err, impl.ErrBadQty) {
		t.Fatalf("negative quantity must return ErrBadQty, got %v", err)
	}
}

func TestSnapshotRestoreKeepsLotsAndSequence(t *testing.T) {
	bag := newBag(10, 8)
	mustPut(t, bag, "hero", impl.Lot{UID: "lot-a", ItemID: "potion", Qty: 2, Bind: impl.BindFree, ExpireAt: 100})
	mustPut(t, bag, "hero", impl.Lot{UID: "lot-b", ItemID: "ore", Qty: 3, Bind: impl.BindBound, ExpireAt: 200})
	data, err := bag.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := newBag(10, 8)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	lots := restored.Lots("hero", "potion")
	if len(lots) != 1 || lots[0].UID != "lot-a" || lots[0].Qty != 2 {
		t.Fatalf("restored potion lots = %+v", lots)
	}
	if got := restored.Total("hero", "ore"); got != 3 {
		t.Fatalf("restored ore total = %d, want 3", got)
	}
	mustPut(t, restored, "hero", impl.Lot{UID: "lot-c", ItemID: "potion", Qty: 1, Bind: impl.BindFree})
	third := restored.Lots("hero", "potion")
	if len(third) != 2 {
		t.Fatalf("restored bag lost lots: %+v", third)
	}
	if third[1].Seq <= 2 {
		t.Fatalf("new lot seq = %d, want a number beyond the restored sequence", third[1].Seq)
	}
	if err := restored.Restore([]byte("not a snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("garbage restore must return ErrBadSnapshot, got %v", err)
	}
}
