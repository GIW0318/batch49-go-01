package task

import (
	"errors"
	"testing"

	impl "gsb/batch49-go-game-a-02"
)

func newGuard(maxLocks, maxBatch int) *impl.ItemLockGuard {
	return impl.NewItemLockGuard(impl.Options{MaxLocksPerItem: maxLocks, MaxBatch: maxBatch})
}

func mustAdd(t *testing.T, guard *impl.ItemLockGuard, owner string, item impl.BagItem) {
	t.Helper()
	if err := guard.AddItem(owner, item); err != nil {
		t.Fatalf("add item %s: %v", item.UID, err)
	}
}

func mustLock(t *testing.T, guard *impl.ItemLockGuard, owner, uid, reason string, until int64) {
	t.Helper()
	if err := guard.Lock(owner, uid, reason, until); err != nil {
		t.Fatalf("lock %s with %s: %v", uid, reason, err)
	}
}

func TestAddItemRegistersItem(t *testing.T) {
	guard := newGuard(4, 8)
	if err := guard.AddItem("hero", impl.BagItem{UID: "sword", ItemID: "sword-1", Qty: 1}); err != nil {
		t.Fatalf("add item: %v", err)
	}
	if err := guard.AddItem("hero", impl.BagItem{UID: "apple", ItemID: "food-1", Qty: 5}); err != nil {
		t.Fatalf("add second item: %v", err)
	}
	items := guard.Items("hero")
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	if items[0].UID != "apple" || items[1].UID != "sword" {
		t.Fatalf("items must be ordered by uid, got %+v", items)
	}
	if items[0].Qty != 5 {
		t.Fatalf("apple quantity = %d, want 5", items[0].Qty)
	}
}

func TestAddItemRejectsBadInput(t *testing.T) {
	guard := newGuard(4, 8)
	if err := guard.AddItem("", impl.BagItem{UID: "sword", ItemID: "sword-1", Qty: 1}); !errors.Is(err, impl.ErrBlankArg) {
		t.Fatalf("blank owner must be rejected with ErrBlankArg, got %v", err)
	}
	if err := guard.AddItem("hero", impl.BagItem{UID: " ", ItemID: "sword-1", Qty: 1}); !errors.Is(err, impl.ErrBlankArg) {
		t.Fatalf("blank uid must be rejected with ErrBlankArg, got %v", err)
	}
	if err := guard.AddItem("hero", impl.BagItem{UID: "sword", ItemID: "sword-1", Qty: 0}); !errors.Is(err, impl.ErrBadItem) {
		t.Fatalf("zero quantity must be rejected with ErrBadItem, got %v", err)
	}
	if err := guard.AddItem("hero", impl.BagItem{UID: "sword", ItemID: "sword-1", Qty: 1}); err != nil {
		t.Fatalf("first registration must succeed, got %v", err)
	}
	if err := guard.AddItem("hero", impl.BagItem{UID: "sword", ItemID: "sword-2", Qty: 1}); !errors.Is(err, impl.ErrBadItem) {
		t.Fatalf("duplicate uid must be rejected with ErrBadItem, got %v", err)
	}
}

func TestLockUnknownItemRejected(t *testing.T) {
	guard := newGuard(4, 8)
	if err := guard.Lock("hero", "ghost", impl.LockPlayer, 0); !errors.Is(err, impl.ErrUnknownItem) {
		t.Fatalf("locking an unknown item must return ErrUnknownItem, got %v", err)
	}
	mustAdd(t, guard, "hero", impl.BagItem{UID: "sword", ItemID: "sword-1", Qty: 1})
	if err := guard.Lock("hero", "", impl.LockPlayer, 0); !errors.Is(err, impl.ErrBlankArg) {
		t.Fatalf("blank uid must return ErrBlankArg, got %v", err)
	}
}

func TestLockRejectsUnsupportedSource(t *testing.T) {
	guard := newGuard(4, 8)
	mustAdd(t, guard, "hero", impl.BagItem{UID: "sword", ItemID: "sword-1", Qty: 1})
	if err := guard.Lock("hero", "sword", "gm", 0); !errors.Is(err, impl.ErrBadReason) {
		t.Fatalf("unknown source must return ErrBadReason, got %v", err)
	}
	if err := guard.Lock("hero", "sword", "", 0); !errors.Is(err, impl.ErrBadReason) {
		t.Fatalf("empty source must return ErrBadReason, got %v", err)
	}
	if got := guard.LockCount("hero"); got != 0 {
		t.Fatalf("rejected locks must not be recorded, count = %d", got)
	}
}

func TestLockRejectsBadDeadline(t *testing.T) {
	guard := newGuard(4, 8)
	mustAdd(t, guard, "hero", impl.BagItem{UID: "sword", ItemID: "sword-1", Qty: 1})
	if err := guard.Lock("hero", "sword", impl.LockTimed, 0); !errors.Is(err, impl.ErrBadDeadline) {
		t.Fatalf("timed lock without deadline must return ErrBadDeadline, got %v", err)
	}
	if err := guard.Lock("hero", "sword", impl.LockTimed, -5); !errors.Is(err, impl.ErrBadDeadline) {
		t.Fatalf("timed lock with past deadline must return ErrBadDeadline, got %v", err)
	}
	if err := guard.Lock("hero", "sword", impl.LockPlayer, 500); !errors.Is(err, impl.ErrBadDeadline) {
		t.Fatalf("permanent source with a deadline must return ErrBadDeadline, got %v", err)
	}
	if err := guard.Lock("hero", "sword", impl.LockEquipped, 9); !errors.Is(err, impl.ErrBadDeadline) {
		t.Fatalf("system source with a deadline must return ErrBadDeadline, got %v", err)
	}
}

func TestTimedLockStopsCountingAfterDeadline(t *testing.T) {
	guard := newGuard(4, 8)
	mustAdd(t, guard, "hero", impl.BagItem{UID: "sword", ItemID: "sword-1", Qty: 1})
	mustLock(t, guard, "hero", "sword", impl.LockTimed, 500)
	before := guard.EffectiveLock("hero", "sword", 499)
	if !before.Locked {
		t.Fatalf("item must stay locked before the deadline, got %+v", before)
	}
	if len(before.Reasons) != 1 || before.Reasons[0] != impl.LockTimed {
		t.Fatalf("reasons before deadline = %v, want [timed]", before.Reasons)
	}
	after := guard.EffectiveLock("hero", "sword", 500)
	if after.Locked {
		t.Fatalf("timed lock must stop counting at the deadline, got %+v", after)
	}
	if len(after.Reasons) != 0 {
		t.Fatalf("expired reasons = %v, want none", after.Reasons)
	}
	if after.UID != "sword" {
		t.Fatalf("effective state uid = %q, want sword", after.UID)
	}
}

func TestUnlockRemovesSource(t *testing.T) {
	guard := newGuard(4, 8)
	mustAdd(t, guard, "hero", impl.BagItem{UID: "sword", ItemID: "sword-1", Qty: 1})
	mustLock(t, guard, "hero", "sword", impl.LockPlayer, 0)
	if err := guard.Unlock("hero", "sword", impl.LockPlayer); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	state := guard.EffectiveLock("hero", "sword", 100)
	if state.Locked || len(state.Reasons) != 0 {
		t.Fatalf("item must be unlocked after removing the only source, got %+v", state)
	}
	if err := guard.Unlock("hero", "sword", impl.LockPlayer); !errors.Is(err, impl.ErrNotLocked) {
		t.Fatalf("second unlock must return ErrNotLocked, got %v", err)
	}
	if got := guard.LockCount("hero"); got != 0 {
		t.Fatalf("lock count after unlock = %d, want 0", got)
	}
}

func TestEffectiveLockIsPerItem(t *testing.T) {
	guard := newGuard(4, 8)
	mustAdd(t, guard, "hero", impl.BagItem{UID: "sword", ItemID: "sword-1", Qty: 1})
	mustAdd(t, guard, "hero", impl.BagItem{UID: "apple", ItemID: "food-1", Qty: 5})
	mustLock(t, guard, "hero", "sword", impl.LockQuest, 0)
	if state := guard.EffectiveLock("hero", "sword", 10); !state.Locked || state.Reasons[0] != impl.LockQuest {
		t.Fatalf("sword must be locked by quest, got %+v", state)
	}
	if state := guard.EffectiveLock("hero", "apple", 10); state.Locked || len(state.Reasons) != 0 {
		t.Fatalf("apple must be unlocked, got %+v", state)
	}
	if state := guard.EffectiveLock("hero", "missing", 10); state.Locked {
		t.Fatalf("unknown item must report unlocked, got %+v", state)
	}
	if got := guard.LockCount("hero"); got != 1 {
		t.Fatalf("lock count = %d, want 1", got)
	}
}

func TestDryRunReportsBlockedItems(t *testing.T) {
	guard := newGuard(4, 8)
	mustAdd(t, guard, "hero", impl.BagItem{UID: "sword", ItemID: "sword-1", Qty: 1})
	mustAdd(t, guard, "hero", impl.BagItem{UID: "apple", ItemID: "food-1", Qty: 5})
	mustLock(t, guard, "hero", "sword", impl.LockPlayer, 0)
	rejections, err := guard.DryRunDiscard("hero", []string{"apple", "sword"})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if len(rejections) != 1 {
		t.Fatalf("rejections = %+v, want one blocked item", rejections)
	}
	if rejections[0].UID != "sword" || rejections[0].Reason != impl.LockPlayer {
		t.Fatalf("rejection = %+v, want sword blocked by player", rejections[0])
	}
	if got := len(guard.Items("hero")); got != 2 {
		t.Fatalf("dry run must not remove items, items = %d", got)
	}
	clean, err := guard.DryRunDiscard("hero", []string{"apple"})
	if err != nil {
		t.Fatalf("dry run on unlocked item: %v", err)
	}
	if len(clean) != 0 {
		t.Fatalf("unlocked item must produce no rejection, got %+v", clean)
	}
}

func TestDryRunRejectsBadBatch(t *testing.T) {
	guard := newGuard(4, 8)
	mustAdd(t, guard, "hero", impl.BagItem{UID: "sword", ItemID: "sword-1", Qty: 1})
	if _, err := guard.DryRunDiscard("hero", nil); !errors.Is(err, impl.ErrBadBatch) {
		t.Fatalf("empty batch must return ErrBadBatch, got %v", err)
	}
	if _, err := guard.DryRunDiscard("hero", []string{"sword", "sword"}); !errors.Is(err, impl.ErrBadBatch) {
		t.Fatalf("duplicated batch must return ErrBadBatch, got %v", err)
	}
	small := newGuard(4, 1)
	mustAdd(t, small, "hero", impl.BagItem{UID: "a", ItemID: "x", Qty: 1})
	mustAdd(t, small, "hero", impl.BagItem{UID: "b", ItemID: "x", Qty: 1})
	if _, err := small.DryRunDiscard("hero", []string{"a", "b"}); !errors.Is(err, impl.ErrBadBatch) {
		t.Fatalf("batch over the cap must return ErrBadBatch, got %v", err)
	}
}

func TestDryRunRejectsUnknownItem(t *testing.T) {
	guard := newGuard(4, 8)
	mustAdd(t, guard, "hero", impl.BagItem{UID: "sword", ItemID: "sword-1", Qty: 1})
	if _, err := guard.DryRunDiscard("hero", []string{"sword", "ghost"}); !errors.Is(err, impl.ErrUnknownItem) {
		t.Fatalf("unknown uid must return ErrUnknownItem, got %v", err)
	}
	if _, err := guard.DryRunDiscard("nobody", []string{"sword"}); !errors.Is(err, impl.ErrUnknownItem) {
		t.Fatalf("unknown owner must return ErrUnknownItem, got %v", err)
	}
}

func TestDiscardRemovesUnlockedBatch(t *testing.T) {
	guard := newGuard(4, 8)
	mustAdd(t, guard, "hero", impl.BagItem{UID: "a", ItemID: "x", Qty: 1})
	mustAdd(t, guard, "hero", impl.BagItem{UID: "b", ItemID: "x", Qty: 2})
	mustAdd(t, guard, "hero", impl.BagItem{UID: "c", ItemID: "x", Qty: 3})
	result, err := guard.Discard("hero", []string{"b", "a"}, 100)
	if err != nil {
		t.Fatalf("discard: %v", err)
	}
	if len(result.Removed) != 2 || result.Removed[0] != "b" || result.Removed[1] != "a" {
		t.Fatalf("removed = %v, want [b a] in input order", result.Removed)
	}
	if len(result.Rejected) != 0 {
		t.Fatalf("rejected = %+v, want none", result.Rejected)
	}
	items := guard.Items("hero")
	if len(items) != 1 || items[0].UID != "c" {
		t.Fatalf("remaining items = %+v, want only c", items)
	}
}

func TestDiscardMatchesDryRunOnBlockedBatch(t *testing.T) {
	guard := newGuard(4, 8)
	mustAdd(t, guard, "hero", impl.BagItem{UID: "a", ItemID: "x", Qty: 1})
	mustAdd(t, guard, "hero", impl.BagItem{UID: "b", ItemID: "x", Qty: 2})
	mustLock(t, guard, "hero", "a", impl.LockPlayer, 0)
	mustLock(t, guard, "hero", "b", impl.LockEscrow, 0)
	uids := []string{"a", "b"}
	preview, err := guard.DryRunDiscard("hero", uids)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	result, err := guard.Discard("hero", uids, 100)
	if err != nil {
		t.Fatalf("discard: %v", err)
	}
	if len(result.Removed) != 0 {
		t.Fatalf("blocked batch must remove nothing, got %v", result.Removed)
	}
	if len(result.Rejected) != len(preview) {
		t.Fatalf("discard rejected %d, preview rejected %d", len(result.Rejected), len(preview))
	}
	for i := range preview {
		if result.Rejected[i].UID != preview[i].UID || result.Rejected[i].Reason != preview[i].Reason {
			t.Fatalf("rejection %d = %+v, preview = %+v", i, result.Rejected[i], preview[i])
		}
	}
	if got := len(guard.Items("hero")); got != 2 {
		t.Fatalf("blocked discard removed items, count = %d", got)
	}
}

func TestDiscardRejectsBadBatch(t *testing.T) {
	guard := newGuard(4, 8)
	mustAdd(t, guard, "hero", impl.BagItem{UID: "a", ItemID: "x", Qty: 1})
	if _, err := guard.Discard("hero", nil, 10); !errors.Is(err, impl.ErrBadBatch) {
		t.Fatalf("empty discard batch must return ErrBadBatch, got %v", err)
	}
	if _, err := guard.Discard("hero", []string{"a", "a"}, 10); !errors.Is(err, impl.ErrBadBatch) {
		t.Fatalf("duplicated discard batch must return ErrBadBatch, got %v", err)
	}
	if _, err := guard.Discard("hero", []string{"ghost"}, 10); !errors.Is(err, impl.ErrUnknownItem) {
		t.Fatalf("unknown discard uid must return ErrUnknownItem, got %v", err)
	}
	if got := len(guard.Items("hero")); got != 1 {
		t.Fatalf("failed discards must keep the bag, count = %d", got)
	}
}

func TestSnapshotRestoreKeepsBagAndLocks(t *testing.T) {
	guard := newGuard(4, 8)
	mustAdd(t, guard, "hero", impl.BagItem{UID: "sword", ItemID: "sword-1", Qty: 1})
	mustAdd(t, guard, "hero", impl.BagItem{UID: "apple", ItemID: "food-1", Qty: 5})
	mustLock(t, guard, "hero", "sword", impl.LockTimed, 900)
	data, err := guard.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := newGuard(4, 8)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := len(restored.Items("hero")); got != 2 {
		t.Fatalf("restored items = %d, want 2", got)
	}
	if state := restored.EffectiveLock("hero", "sword", 800); !state.Locked || state.Reasons[0] != impl.LockTimed {
		t.Fatalf("restored lock state = %+v, want a timed lock", state)
	}
	if got := restored.LockCount("hero"); got != 1 {
		t.Fatalf("restored lock count = %d, want 1", got)
	}
	if err := restored.Restore([]byte("garbage")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("garbage restore must return ErrBadSnapshot, got %v", err)
	}
	if got := len(restored.Items("hero")); got != 2 {
		t.Fatalf("failed restore must not wipe the bag, count = %d", got)
	}
}
