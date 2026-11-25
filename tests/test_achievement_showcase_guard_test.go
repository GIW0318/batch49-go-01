package task

import (
	"errors"
	"math"
	"strings"
	"sync"
	"testing"

	impl "gsb/batch49-go-game-a-22"
)

func guardOptions() impl.Options {
	return impl.Options{MaxSlots: 3}
}

func mustDefineShowcase(t *testing.T, g *impl.ShowcaseGuard, id string) {
	t.Helper()
	if err := g.DefineAchievement(id, false); err != nil {
		t.Fatalf("define achievement %q: %v", id, err)
	}
}

func mustUnlockShowcase(t *testing.T, g *impl.ShowcaseGuard, id, account string) {
	t.Helper()
	if err := g.RecordUnlock(id, account); err != nil {
		t.Fatalf("unlock %q for %q: %v", id, account, err)
	}
}

func TestDefineAchievementRejectsInvalidAndDuplicate(t *testing.T) {
	g := impl.NewShowcaseGuard(guardOptions())
	for _, id := range []string{"", "   "} {
		if err := g.DefineAchievement(id, false); !errors.Is(err, impl.ErrInvalidAchievement) {
			t.Fatalf("definition %q must fail with ErrInvalidAchievement, got %v", id, err)
		}
	}
	mustDefineShowcase(t, g, "a1")
	if err := g.DefineAchievement("a1", true); !errors.Is(err, impl.ErrDuplicateAchievement) {
		t.Fatalf("duplicate definition must fail with ErrDuplicateAchievement, got %v", err)
	}
}

func TestRecordUnlockRejectsUnknownAndBlankAccount(t *testing.T) {
	g := impl.NewShowcaseGuard(guardOptions())
	mustDefineShowcase(t, g, "a1")
	if err := g.RecordUnlock("missing", "u1"); !errors.Is(err, impl.ErrUnknownAchievement) {
		t.Fatalf("unknown achievement must fail with ErrUnknownAchievement, got %v", err)
	}
	if err := g.RecordUnlock("a1", "   "); !errors.Is(err, impl.ErrBlankAccount) {
		t.Fatalf("blank account must fail with ErrBlankAccount, got %v", err)
	}
}

func TestRecordUnlockIsIdempotent(t *testing.T) {
	g := impl.NewShowcaseGuard(guardOptions())
	mustDefineShowcase(t, g, "a1")
	mustUnlockShowcase(t, g, "a1", "u1")
	mustUnlockShowcase(t, g, "a1", "u1")
	r, err := g.Rarity("a1")
	if err != nil {
		t.Fatalf("rarity: %v", err)
	}
	if r.Unlocked != 1 {
		t.Fatalf("duplicate unlock counted %d accounts, want 1", r.Unlocked)
	}
	mustUnlockShowcase(t, g, "a1", "u2")
	r, err = g.Rarity("a1")
	if err != nil {
		t.Fatalf("rarity after second account: %v", err)
	}
	if r.Unlocked != 2 {
		t.Fatalf("distinct accounts counted %d, want 2", r.Unlocked)
	}
}

func TestRarityCountsBeforeRecalc(t *testing.T) {
	g := impl.NewShowcaseGuard(guardOptions())
	mustDefineShowcase(t, g, "a1")
	mustDefineShowcase(t, g, "a2")
	if err := g.SetActivePlayers(50); err != nil {
		t.Fatalf("set active players: %v", err)
	}
	mustUnlockShowcase(t, g, "a1", "u1")
	mustUnlockShowcase(t, g, "a1", "u2")
	mustUnlockShowcase(t, g, "a2", "u1")

	r, err := g.Rarity("a1")
	if err != nil {
		t.Fatalf("rarity a1: %v", err)
	}
	if r.Unlocked != 2 || r.ActivePlayers != 50 {
		t.Fatalf("a1 rarity = %+v, want unlocked 2 of 50", r)
	}
	if r.Version != 0 || r.Percent != 0 {
		t.Fatalf("before any recalc a1 must report version 0 percent 0, got %+v", r)
	}
	r2, err := g.Rarity("a2")
	if err != nil {
		t.Fatalf("rarity a2: %v", err)
	}
	if r2.Unlocked != 1 {
		t.Fatalf("a2 unlocked = %d, want 1", r2.Unlocked)
	}
}

func TestSetActivePlayersRejectsNonPositive(t *testing.T) {
	g := impl.NewShowcaseGuard(guardOptions())
	for _, n := range []int{0, -3} {
		if err := g.SetActivePlayers(n); !errors.Is(err, impl.ErrInvalidActivePlayers) {
			t.Fatalf("active players %d must fail with ErrInvalidActivePlayers, got %v", n, err)
		}
	}
	if err := g.SetActivePlayers(7); err != nil {
		t.Fatalf("positive active players must be accepted, got %v", err)
	}
}

func TestRecalcRarityComputesPercent(t *testing.T) {
	g := impl.NewShowcaseGuard(guardOptions())
	mustDefineShowcase(t, g, "a1")
	if err := g.SetActivePlayers(200); err != nil {
		t.Fatalf("set active players: %v", err)
	}
	for _, account := range []string{"u1", "u2", "u3"} {
		mustUnlockShowcase(t, g, "a1", account)
	}
	res, err := g.RecalcRarity(1000)
	if err != nil {
		t.Fatalf("recalc: %v", err)
	}
	if res.Version != 1 || res.Changed != 1 || res.At != 1000 {
		t.Fatalf("recalc result = %+v, want version 1 changed 1 at 1000", res)
	}
	r, err := g.Rarity("a1")
	if err != nil {
		t.Fatalf("rarity: %v", err)
	}
	if r.Version != 1 || r.Unlocked != 3 || r.ActivePlayers != 200 {
		t.Fatalf("rarity after recalc = %+v, want version 1 unlocked 3 of 200", r)
	}
	if math.Abs(r.Percent-1.5) > 1e-9 {
		t.Fatalf("percent = %v, want 1.5", r.Percent)
	}
}

func TestRecalcRarityBumpsVersionWhenUnchanged(t *testing.T) {
	g := impl.NewShowcaseGuard(guardOptions())
	mustDefineShowcase(t, g, "a1")
	if err := g.SetActivePlayers(100); err != nil {
		t.Fatalf("set active players: %v", err)
	}
	mustUnlockShowcase(t, g, "a1", "u1")
	if _, err := g.RecalcRarity(1000); err != nil {
		t.Fatalf("first recalc: %v", err)
	}
	res, err := g.RecalcRarity(2000)
	if err != nil {
		t.Fatalf("second recalc: %v", err)
	}
	if res.Version != 2 || res.Changed != 0 {
		t.Fatalf("second recalc = %+v, want version 2 changed 0", res)
	}
	r, err := g.Rarity("a1")
	if err != nil {
		t.Fatalf("rarity: %v", err)
	}
	if r.Version != 2 || math.Abs(r.Percent-1.0) > 1e-9 {
		t.Fatalf("rarity after second recalc = %+v, want version 2 percent 1", r)
	}
}

func TestRecalcRarityRejectsStaleTimestamp(t *testing.T) {
	g := impl.NewShowcaseGuard(guardOptions())
	mustDefineShowcase(t, g, "a1")
	if err := g.SetActivePlayers(10); err != nil {
		t.Fatalf("set active players: %v", err)
	}
	mustUnlockShowcase(t, g, "a1", "u1")
	if _, err := g.RecalcRarity(1000); err != nil {
		t.Fatalf("first recalc: %v", err)
	}
	if _, err := g.RecalcRarity(999); !errors.Is(err, impl.ErrStaleRecalc) {
		t.Fatalf("older recalc must fail with ErrStaleRecalc, got %v", err)
	}
	r, err := g.Rarity("a1")
	if err != nil {
		t.Fatalf("rarity: %v", err)
	}
	if r.Version != 1 {
		t.Fatalf("stale recalc changed the version to %d, want 1", r.Version)
	}
}

func TestSetShowcaseValidatesSlots(t *testing.T) {
	g := impl.NewShowcaseGuard(impl.Options{MaxSlots: 3})
	for _, id := range []string{"a1", "a2", "a3", "a4"} {
		mustDefineShowcase(t, g, id)
	}
	for _, id := range []string{"a1", "a2", "a3"} {
		mustUnlockShowcase(t, g, id, "acc-1")
	}
	if err := g.SetShowcase("acc-1", []string{"a1", "a2", "a3", "a4"}); !errors.Is(err, impl.ErrSlotLimit) {
		t.Fatalf("four slots must fail with ErrSlotLimit, got %v", err)
	}
	if err := g.SetShowcase("acc-1", []string{"a1", "missing"}); !errors.Is(err, impl.ErrUnknownAchievement) {
		t.Fatalf("unknown achievement must fail with ErrUnknownAchievement, got %v", err)
	}
	if err := g.SetShowcase("acc-1", []string{"a4"}); !errors.Is(err, impl.ErrNotUnlocked) {
		t.Fatalf("locked achievement must fail with ErrNotUnlocked, got %v", err)
	}
	if err := g.SetShowcase("acc-1", []string{"a1", "a2"}); err != nil {
		t.Fatalf("valid showcase must be accepted, got %v", err)
	}
	if entries := g.View("acc-1", "acc-1"); len(entries) != 2 {
		t.Fatalf("view after rejected writes = %v, want the two valid slots", entries)
	}
}

func TestViewShowsSlotsInOrder(t *testing.T) {
	g := impl.NewShowcaseGuard(guardOptions())
	for _, id := range []string{"a1", "a2", "a3"} {
		mustDefineShowcase(t, g, id)
		mustUnlockShowcase(t, g, id, "acc-1")
	}
	if err := g.SetShowcase("acc-1", []string{"a3", "a1"}); err != nil {
		t.Fatalf("set showcase: %v", err)
	}
	entries := g.View("acc-1", "acc-1")
	if len(entries) != 2 {
		t.Fatalf("view = %v, want 2 entries", entries)
	}
	if entries[0].Slot != 1 || entries[0].AchievementID != "a3" || entries[0].Masked {
		t.Fatalf("first entry = %+v, want slot 1 a3 unmasked", entries[0])
	}
	if entries[1].Slot != 2 || entries[1].AchievementID != "a1" || entries[1].Masked {
		t.Fatalf("second entry = %+v, want slot 2 a1 unmasked", entries[1])
	}
}

func TestViewUnknownAccountIsEmpty(t *testing.T) {
	g := impl.NewShowcaseGuard(guardOptions())
	mustDefineShowcase(t, g, "a1")
	mustUnlockShowcase(t, g, "a1", "acc-1")
	if err := g.SetShowcase("acc-1", []string{"a1"}); err != nil {
		t.Fatalf("set showcase: %v", err)
	}
	if entries := g.View("ghost", "ghost"); len(entries) != 0 {
		t.Fatalf("unknown account cabinet = %v, want empty", entries)
	}
	if entries := g.View("acc-1", "acc-1"); len(entries) != 1 {
		t.Fatalf("known account cabinet = %v, want one entry", entries)
	}
}

func TestConcurrentRecordUnlock(t *testing.T) {
	g := impl.NewShowcaseGuard(guardOptions())
	mustDefineShowcase(t, g, "a1")
	if err := g.SetActivePlayers(400); err != nil {
		t.Fatalf("set active players: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := g.RecordUnlock("a1", "u"+strings.Repeat("x", i%3)+string(rune('A'+i))); err != nil {
				t.Errorf("unlock %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	r, err := g.Rarity("a1")
	if err != nil {
		t.Fatalf("rarity: %v", err)
	}
	if r.Unlocked != 40 {
		t.Fatalf("concurrent unlocks counted %d, want 40", r.Unlocked)
	}
	if _, err := g.RecalcRarity(500); err != nil {
		t.Fatalf("recalc: %v", err)
	}
	r, err = g.Rarity("a1")
	if err != nil {
		t.Fatalf("rarity after recalc: %v", err)
	}
	if math.Abs(r.Percent-10.0) > 1e-9 {
		t.Fatalf("percent = %v, want 10", r.Percent)
	}
}

func TestConcurrentSetShowcaseSameAccount(t *testing.T) {
	g := impl.NewShowcaseGuard(guardOptions())
	for _, id := range []string{"a1", "a2", "a3"} {
		mustDefineShowcase(t, g, id)
		mustUnlockShowcase(t, g, id, "acc-1")
	}
	cabinets := [][]string{{"a1", "a2"}, {"a2", "a3"}, {"a1", "a3"}}
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := g.SetShowcase("acc-1", cabinets[i%len(cabinets)]); err != nil {
				t.Errorf("set showcase %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	entries := g.View("acc-1", "acc-1")
	if len(entries) != 2 {
		t.Fatalf("final cabinet = %v, want 2 complete slots", entries)
	}
	got := entries[0].AchievementID + "," + entries[1].AchievementID
	valid := false
	for _, cabinet := range cabinets {
		if got == cabinet[0]+","+cabinet[1] {
			valid = true
		}
	}
	if !valid {
		t.Fatalf("final cabinet = %q, want one complete writer view", got)
	}
}

func TestSnapshotRestorePreservesShowcaseAndRarity(t *testing.T) {
	opts := impl.Options{MaxSlots: 3}
	g := impl.NewShowcaseGuard(opts)
	for _, id := range []string{"a1", "a2"} {
		mustDefineShowcase(t, g, id)
	}
	if err := g.SetActivePlayers(200); err != nil {
		t.Fatalf("set active players: %v", err)
	}
	for _, account := range []string{"u1", "u2", "u3"} {
		mustUnlockShowcase(t, g, "a1", account)
	}
	mustUnlockShowcase(t, g, "a2", "u1")
	if err := g.SetShowcase("u1", []string{"a1", "a2"}); err != nil {
		t.Fatalf("set showcase: %v", err)
	}
	if _, err := g.RecalcRarity(1000); err != nil {
		t.Fatalf("recalc: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	restored := impl.NewShowcaseGuard(opts)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	r, err := restored.Rarity("a1")
	if err != nil {
		t.Fatalf("restored rarity: %v", err)
	}
	if r.Version != 1 || r.Unlocked != 3 || r.ActivePlayers != 200 {
		t.Fatalf("restored rarity = %+v, want version 1 unlocked 3 of 200", r)
	}
	if math.Abs(r.Percent-1.5) > 1e-9 {
		t.Fatalf("restored percent = %v, want 1.5", r.Percent)
	}
	entries := restored.View("u1", "u1")
	if len(entries) != 2 || entries[0].AchievementID != "a1" || entries[1].AchievementID != "a2" {
		t.Fatalf("restored cabinet = %v, want a1 then a2", entries)
	}
	if err := restored.RecordUnlock("a1", "u1"); err != nil {
		t.Fatalf("idempotent unlock after restore: %v", err)
	}
	r, err = restored.Rarity("a1")
	if err != nil {
		t.Fatalf("rarity after restore unlock: %v", err)
	}
	if r.Unlocked != 3 {
		t.Fatalf("unlock after restore recounted u1, unlocked = %d want 3", r.Unlocked)
	}
}

func TestRestoreRejectsGarbage(t *testing.T) {
	g := impl.NewShowcaseGuard(guardOptions())
	mustDefineShowcase(t, g, "a1")
	mustUnlockShowcase(t, g, "a1", "acc-1")
	if err := g.SetShowcase("acc-1", []string{"a1"}); err != nil {
		t.Fatalf("set showcase: %v", err)
	}
	if err := g.Restore([]byte("this is not a snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("garbage snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if entries := g.View("acc-1", "acc-1"); len(entries) != 1 {
		t.Fatalf("failed restore must keep the live cabinet, got %v", entries)
	}
	r, err := g.Rarity("a1")
	if err != nil {
		t.Fatalf("rarity after failed restore: %v", err)
	}
	if r.Unlocked != 1 {
		t.Fatalf("failed restore changed unlock count to %d, want 1", r.Unlocked)
	}
}
