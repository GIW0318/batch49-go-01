package task

import (
	"errors"
	"strings"
	"sync"
	"testing"

	impl "gsb/batch49-go-game-a-25"
)

func masteryOptions() impl.Options {
	return impl.Options{}
}

func mustRecipe(t *testing.T, g *impl.RecipeMasteryGuard, r impl.Recipe) {
	t.Helper()
	if err := g.DefineRecipe(r); err != nil {
		t.Fatalf("define recipe %q: %v", r.ID, err)
	}
}

func baseRecipe(id, discipline string, points int) impl.Recipe {
	return impl.Recipe{ID: id, Discipline: discipline, Points: points}
}

func TestDefineRecipeRejectsInvalidAndDuplicate(t *testing.T) {
	g := impl.NewRecipeMasteryGuard(masteryOptions())
	bad := []impl.Recipe{
		{ID: "", Discipline: "smithing", Points: 10},
		{ID: "no-discipline", Discipline: "   ", Points: 10},
		{ID: "zero-points", Discipline: "smithing", Points: 0},
		{ID: "negative-points", Discipline: "smithing", Points: -3},
		{ID: "negative-skill", Discipline: "smithing", Points: 10, SkillReq: -1},
	}
	for _, r := range bad {
		if err := g.DefineRecipe(r); !errors.Is(err, impl.ErrInvalidRecipe) {
			t.Fatalf("recipe %+v must fail with ErrInvalidRecipe, got %v", r, err)
		}
	}
	ok := baseRecipe("dagger", "smithing", 10)
	mustRecipe(t, g, ok)
	if err := g.DefineRecipe(ok); !errors.Is(err, impl.ErrDuplicateRecipe) {
		t.Fatalf("duplicate recipe must fail with ErrDuplicateRecipe, got %v", err)
	}
}

func TestDefineRecipeDetectsPrereqCycle(t *testing.T) {
	g := impl.NewRecipeMasteryGuard(masteryOptions())
	forward := baseRecipe("blade", "smithing", 10)
	forward.Prereqs = []string{"ingot"}
	mustRecipe(t, g, forward)

	back := baseRecipe("ingot", "smithing", 10)
	back.Prereqs = []string{"blade"}
	if err := g.DefineRecipe(back); !errors.Is(err, impl.ErrPrereqCycle) {
		t.Fatalf("two recipe cycle must fail with ErrPrereqCycle, got %v", err)
	}

	self := baseRecipe("loop", "smithing", 10)
	self.Prereqs = []string{"loop"}
	if err := g.DefineRecipe(self); !errors.Is(err, impl.ErrPrereqCycle) {
		t.Fatalf("self reference must fail with ErrPrereqCycle, got %v", err)
	}
}

func TestDiscoverRejectsUnknownAndBlank(t *testing.T) {
	g := impl.NewRecipeMasteryGuard(masteryOptions())
	mustRecipe(t, g, baseRecipe("dagger", "smithing", 10))
	if _, err := g.Discover("acc-1", "missing"); !errors.Is(err, impl.ErrUnknownRecipe) {
		t.Fatalf("unknown recipe must fail with ErrUnknownRecipe, got %v", err)
	}
	if _, err := g.Discover("   ", "dagger"); !errors.Is(err, impl.ErrBlankAccount) {
		t.Fatalf("blank account must fail with ErrBlankAccount, got %v", err)
	}
}

func TestDiscoverRejectsLowSkill(t *testing.T) {
	g := impl.NewRecipeMasteryGuard(masteryOptions())
	advanced := baseRecipe("greatsword", "smithing", 20)
	advanced.SkillReq = 1
	mustRecipe(t, g, advanced)
	discovered, err := g.Discover("acc-1", "greatsword")
	if !errors.Is(err, impl.ErrSkillTooLow) {
		t.Fatalf("low skill must fail with ErrSkillTooLow, got %v", err)
	}
	if discovered {
		t.Fatalf("failed discover must report false")
	}
	if g.IsUnlocked("acc-1", "greatsword") {
		t.Fatalf("failed discover must not unlock the recipe")
	}
}

func TestDiscoverRejectsMissingPrereq(t *testing.T) {
	g := impl.NewRecipeMasteryGuard(masteryOptions())
	mustRecipe(t, g, baseRecipe("ingot", "smithing", 10))
	advanced := baseRecipe("blade", "smithing", 10)
	advanced.Prereqs = []string{"ingot"}
	mustRecipe(t, g, advanced)

	if _, err := g.Discover("acc-1", "blade"); !errors.Is(err, impl.ErrPrereqMissing) {
		t.Fatalf("missing prerequisite must fail with ErrPrereqMissing, got %v", err)
	}
	if _, err := g.Discover("acc-1", "ingot"); err != nil {
		t.Fatalf("discover base recipe: %v", err)
	}
	ok, err := g.Discover("acc-1", "blade")
	if err != nil {
		t.Fatalf("discover after prerequisite: %v", err)
	}
	if !ok {
		t.Fatalf("discover after prerequisite must report true")
	}
}

func TestDiscoverIsIdempotent(t *testing.T) {
	g := impl.NewRecipeMasteryGuard(masteryOptions())
	mustRecipe(t, g, baseRecipe("dagger", "smithing", 10))
	ok, err := g.Discover("acc-1", "dagger")
	if err != nil || !ok {
		t.Fatalf("first discover = (%v, %v), want (true, nil)", ok, err)
	}
	ok, err = g.Discover("acc-1", "dagger")
	if err != nil {
		t.Fatalf("second discover must not fail, got %v", err)
	}
	if ok {
		t.Fatalf("second discover must report false")
	}
	if got := strings.Join(g.Unlocked("acc-1"), ","); got != "dagger" {
		t.Fatalf("unlocked = %q, want dagger", got)
	}
}

func TestCraftRejectsUndiscovered(t *testing.T) {
	g := impl.NewRecipeMasteryGuard(masteryOptions())
	mustRecipe(t, g, baseRecipe("dagger", "smithing", 10))
	if _, err := g.Craft("acc-1", "dagger", 100); !errors.Is(err, impl.ErrNotDiscovered) {
		t.Fatalf("craft without discover must fail with ErrNotDiscovered, got %v", err)
	}
	if got := g.Mastery("acc-1", "smithing").Points; got != 0 {
		t.Fatalf("failed craft awarded %d points", got)
	}
}

func TestCraftAddsMasteryAndFirstBonus(t *testing.T) {
	g := impl.NewRecipeMasteryGuard(masteryOptions())
	mustRecipe(t, g, baseRecipe("dagger", "smithing", 20))
	if _, err := g.Discover("acc-1", "dagger"); err != nil {
		t.Fatalf("discover: %v", err)
	}
	first, err := g.Craft("acc-1", "dagger", 100)
	if err != nil {
		t.Fatalf("first craft: %v", err)
	}
	if !first.FirstCraft || first.Bonus != impl.FirstCraftBonus || first.Points != 70 {
		t.Fatalf("first craft = %+v, want first craft bonus 50 and 70 points", first)
	}
	second, err := g.Craft("acc-1", "dagger", 200)
	if err != nil {
		t.Fatalf("second craft: %v", err)
	}
	if second.FirstCraft || second.Bonus != 0 || second.Points != 90 {
		t.Fatalf("second craft = %+v, want no bonus and 90 points", second)
	}
	if got := g.Mastery("acc-1", "smithing").Points; got != 90 {
		t.Fatalf("mastery points = %d, want 90", got)
	}
}

func TestCraftLevelsUpAtThreshold(t *testing.T) {
	g := impl.NewRecipeMasteryGuard(masteryOptions())
	mustRecipe(t, g, baseRecipe("ingot", "smithing", impl.MasteryPerLevel))
	if _, err := g.Discover("acc-1", "ingot"); err != nil {
		t.Fatalf("discover: %v", err)
	}
	res, err := g.Craft("acc-1", "ingot", 100)
	if err != nil {
		t.Fatalf("craft: %v", err)
	}
	if res.Level != 1 {
		t.Fatalf("craft level = %d, want 1", res.Level)
	}
	info := g.Mastery("acc-1", "smithing")
	if info.Level != 1 || info.MaxLevel != impl.DefaultMaxMasteryLevel {
		t.Fatalf("mastery = %+v, want level 1 of max 10", info)
	}
}

func TestLevelGateUnlocksNextRecipe(t *testing.T) {
	g := impl.NewRecipeMasteryGuard(masteryOptions())
	mustRecipe(t, g, baseRecipe("training", "smithing", impl.MasteryPerLevel))
	advanced := baseRecipe("greatsword", "smithing", 20)
	advanced.SkillReq = 1
	mustRecipe(t, g, advanced)

	if _, err := g.Discover("acc-1", "greatsword"); !errors.Is(err, impl.ErrSkillTooLow) {
		t.Fatalf("recipe must be gated before level 1, got %v", err)
	}
	if _, err := g.Discover("acc-1", "training"); err != nil {
		t.Fatalf("discover training: %v", err)
	}
	if _, err := g.Craft("acc-1", "training", 100); err != nil {
		t.Fatalf("craft training: %v", err)
	}
	ok, err := g.Discover("acc-1", "greatsword")
	if err != nil {
		t.Fatalf("discover after level up: %v", err)
	}
	if !ok {
		t.Fatalf("discover after level up must report true")
	}
}

func TestCraftRejectsStaleTimestamp(t *testing.T) {
	g := impl.NewRecipeMasteryGuard(masteryOptions())
	mustRecipe(t, g, baseRecipe("dagger", "smithing", 10))
	if _, err := g.Discover("acc-1", "dagger"); err != nil {
		t.Fatalf("discover: %v", err)
	}
	if _, err := g.Craft("acc-1", "dagger", 500); err != nil {
		t.Fatalf("craft at 500: %v", err)
	}
	if _, err := g.Craft("acc-1", "dagger", 400); !errors.Is(err, impl.ErrStaleCraft) {
		t.Fatalf("older craft must fail with ErrStaleCraft, got %v", err)
	}
	if got := g.Mastery("acc-1", "smithing").Points; got != 60 {
		t.Fatalf("stale craft changed mastery to %d, want 60", got)
	}
}

func TestConcurrentCraftsSameAccount(t *testing.T) {
	g := impl.NewRecipeMasteryGuard(masteryOptions())
	mustRecipe(t, g, baseRecipe("dagger", "smithing", 1))
	if _, err := g.Discover("acc-1", "dagger"); err != nil {
		t.Fatalf("discover: %v", err)
	}
	const workers = 40
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := g.Craft("acc-1", "dagger", 1000); err != nil {
				t.Errorf("concurrent craft: %v", err)
				return
			}
			mu.Lock()
			ok++
			mu.Unlock()
		}()
	}
	wg.Wait()
	if ok != workers {
		t.Fatalf("successful crafts = %d, want %d", ok, workers)
	}
	if got := g.Mastery("acc-1", "smithing").Points; got != workers+impl.FirstCraftBonus {
		t.Fatalf("mastery points = %d, want %d", got, workers+impl.FirstCraftBonus)
	}
}

func TestConcurrentDiscoverDifferentAccounts(t *testing.T) {
	g := impl.NewRecipeMasteryGuard(masteryOptions())
	mustRecipe(t, g, baseRecipe("dagger", "smithing", 10))
	const workers = 20
	var wg sync.WaitGroup
	newly := make(chan bool, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ok, err := g.Discover(string(rune('a'+i))+"-acc", "dagger")
			if err != nil {
				t.Errorf("discover account %d: %v", i, err)
				return
			}
			newly <- ok
		}(i)
	}
	wg.Wait()
	close(newly)
	for ok := range newly {
		if !ok {
			t.Fatalf("discover for a fresh account must report true")
		}
	}
	for i := 0; i < workers; i++ {
		if !g.IsUnlocked(string(rune('a'+i))+"-acc", "dagger") {
			t.Fatalf("account %d must be unlocked", i)
		}
	}
}

func TestSnapshotRestorePreservesState(t *testing.T) {
	opts := masteryOptions()
	g := impl.NewRecipeMasteryGuard(opts)
	mustRecipe(t, g, baseRecipe("dagger", "smithing", 5))
	if _, err := g.Discover("acc-1", "dagger"); err != nil {
		t.Fatalf("discover: %v", err)
	}
	for _, at := range []int64{100, 200} {
		if _, err := g.Craft("acc-1", "dagger", at); err != nil {
			t.Fatalf("craft at %d: %v", at, err)
		}
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	restored := impl.NewRecipeMasteryGuard(opts)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !restored.IsUnlocked("acc-1", "dagger") {
		t.Fatalf("restore lost the unlock set")
	}
	if got := restored.Mastery("acc-1", "smithing").Points; got != 60 {
		t.Fatalf("restored mastery points = %d, want 60", got)
	}
	res, err := restored.Craft("acc-1", "dagger", 300)
	if err != nil {
		t.Fatalf("craft after restore: %v", err)
	}
	if res.FirstCraft || res.Bonus != 0 || res.Points != 65 {
		t.Fatalf("craft after restore = %+v, want no first craft bonus and 65 points", res)
	}
}

func TestRestoreRejectsGarbage(t *testing.T) {
	g := impl.NewRecipeMasteryGuard(masteryOptions())
	mustRecipe(t, g, baseRecipe("dagger", "smithing", 10))
	if _, err := g.Discover("acc-1", "dagger"); err != nil {
		t.Fatalf("discover: %v", err)
	}
	if err := g.Restore([]byte("{broken snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("garbage snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if !g.IsUnlocked("acc-1", "dagger") {
		t.Fatalf("failed restore lost the live unlock set")
	}
}
