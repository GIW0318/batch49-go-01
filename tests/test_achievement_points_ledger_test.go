package task

import (
	"errors"
	"strings"
	"sync"
	"testing"

	impl "gsb/batch49-go-game-a-21"
)

func ledgerOptions() impl.Options {
	return impl.Options{
		CategoryCaps: map[string]int64{"combat": 100, "explore": 40},
		SeriesBonus:  25,
	}
}

func mustDefine(t *testing.T, l *impl.PointsLedger, a impl.Achievement) {
	t.Helper()
	if err := l.DefineAchievement(a); err != nil {
		t.Fatalf("define achievement %q: %v", a.ID, err)
	}
}

func TestDefineAchievementRejectsInvalidAndDuplicate(t *testing.T) {
	l := impl.NewPointsLedger(ledgerOptions())
	bad := []impl.Achievement{
		{ID: "", Category: "combat", Points: 10},
		{ID: "no-category", Category: "   ", Points: 10},
		{ID: "zero-points", Category: "combat", Points: 0},
		{ID: "negative-points", Category: "combat", Points: -5},
	}
	for _, a := range bad {
		if err := l.DefineAchievement(a); !errors.Is(err, impl.ErrInvalidAchievement) {
			t.Fatalf("definition %+v must be rejected with ErrInvalidAchievement, got %v", a, err)
		}
	}
	ok := impl.Achievement{ID: "first-blood", Category: "combat", Points: 10}
	mustDefine(t, l, ok)
	if err := l.DefineAchievement(ok); !errors.Is(err, impl.ErrDuplicateAchievement) {
		t.Fatalf("second definition of the same id must fail with ErrDuplicateAchievement, got %v", err)
	}
}

func TestUnlockAwardsCategoryPoints(t *testing.T) {
	l := impl.NewPointsLedger(ledgerOptions())
	mustDefine(t, l, impl.Achievement{ID: "a1", Category: "combat", Points: 30})
	mustDefine(t, l, impl.Achievement{ID: "a2", Category: "explore", Points: 12})

	delta, err := l.Unlock("acc-1", "a1", 100)
	if err != nil {
		t.Fatalf("unlock a1: %v", err)
	}
	if delta.Added != 30 || delta.Total != 30 {
		t.Fatalf("first unlock delta = %+v, want added 30 total 30", delta)
	}
	delta, err = l.Unlock("acc-1", "a2", 200)
	if err != nil {
		t.Fatalf("unlock a2: %v", err)
	}
	if delta.Added != 12 || delta.Total != 42 {
		t.Fatalf("second unlock delta = %+v, want added 12 total 42", delta)
	}
	if got := l.CategoryPoints("acc-1", "combat"); got != 30 {
		t.Fatalf("combat category points = %d, want 30", got)
	}
	if got := l.CategoryPoints("acc-1", "explore"); got != 12 {
		t.Fatalf("explore category points = %d, want 12", got)
	}
}

func TestUnlockBeforeDefinitionIsPending(t *testing.T) {
	l := impl.NewPointsLedger(impl.Options{CategoryCaps: map[string]int64{}})
	delta, err := l.Unlock("acc-1", "future", 10)
	if err != nil {
		t.Fatalf("unlocking a not yet defined achievement must be kept pending, got %v", err)
	}
	if delta.Added != 0 || delta.Total != 0 {
		t.Fatalf("pending unlock delta = %+v, want zero points", delta)
	}
	if ids := l.Unlocked("acc-1"); len(ids) != 1 || ids[0] != "future" {
		t.Fatalf("pending unlock must stay in the unlock set, got %v", ids)
	}
	mustDefine(t, l, impl.Achievement{ID: "future", Category: "combat", Points: 15})
	rec, err := l.Recalc("acc-1")
	if err != nil {
		t.Fatalf("recalc after the definition arrives: %v", err)
	}
	if rec.Added != 15 || rec.Total != 15 {
		t.Fatalf("recalc delta = %+v, want added 15 total 15", rec)
	}
	again, err := l.Recalc("acc-1")
	if err != nil {
		t.Fatalf("second recalc: %v", err)
	}
	if again.Added != 0 || again.Total != 15 {
		t.Fatalf("repeated recalc = %+v, want added 0 total 15", again)
	}
}

func TestUnlockIsIdempotent(t *testing.T) {
	l := impl.NewPointsLedger(ledgerOptions())
	mustDefine(t, l, impl.Achievement{ID: "a1", Category: "combat", Points: 10})

	if _, err := l.Unlock("acc-1", "a1", 100); err != nil {
		t.Fatalf("first unlock: %v", err)
	}
	delta, err := l.Unlock("acc-1", "a1", 300)
	if err != nil {
		t.Fatalf("duplicate unlock must stay a success, got %v", err)
	}
	if delta.Added != 0 || delta.Total != 10 {
		t.Fatalf("duplicate unlock delta = %+v, want added 0 total 10", delta)
	}
	if ids := l.Unlocked("acc-1"); len(ids) != 1 || ids[0] != "a1" {
		t.Fatalf("unlocked ids = %v, want exactly [a1]", ids)
	}
}

func TestCategoryCapStopsAward(t *testing.T) {
	opts := impl.Options{CategoryCaps: map[string]int64{"combat": 50}}
	l := impl.NewPointsLedger(opts)
	mustDefine(t, l, impl.Achievement{ID: "a1", Category: "combat", Points: 40})
	mustDefine(t, l, impl.Achievement{ID: "a2", Category: "combat", Points: 30})

	if _, err := l.Unlock("acc-1", "a1", 100); err != nil {
		t.Fatalf("unlock a1: %v", err)
	}
	delta, err := l.Unlock("acc-1", "a2", 200)
	if err != nil {
		t.Fatalf("unlock a2: %v", err)
	}
	if delta.Added != 10 {
		t.Fatalf("capped unlock added %d points, want the remaining 10", delta.Added)
	}
	if delta.Total != 50 {
		t.Fatalf("capped total = %d, want the hard cap 50", delta.Total)
	}
	if got := l.CategoryPoints("acc-1", "combat"); got != 50 {
		t.Fatalf("category points = %d, want 50", got)
	}
}

func TestSeriesBonusGrantedOnce(t *testing.T) {
	opts := impl.Options{
		CategoryCaps: map[string]int64{"combat": 100, "explore": 100},
		SeriesBonus:  25,
	}
	l := impl.NewPointsLedger(opts)
	mustDefine(t, l, impl.Achievement{ID: "s1", Category: "combat", Points: 10, Series: "starter"})
	mustDefine(t, l, impl.Achievement{ID: "s2", Category: "explore", Points: 10, Series: "starter"})

	delta, err := l.Unlock("acc-1", "s1", 100)
	if err != nil {
		t.Fatalf("unlock s1: %v", err)
	}
	if len(delta.Bonuses) != 0 || delta.Total != 10 {
		t.Fatalf("incomplete series must not pay a bonus, got %+v", delta)
	}
	delta, err = l.Unlock("acc-1", "s2", 200)
	if err != nil {
		t.Fatalf("unlock s2: %v", err)
	}
	if len(delta.Bonuses) != 1 || delta.Bonuses[0] != "starter" {
		t.Fatalf("completing the series must pay starter once, got bonuses %v", delta.Bonuses)
	}
	if delta.Total != 45 {
		t.Fatalf("total after series bonus = %d, want 10+10+25", delta.Total)
	}
	if got := l.GrantedBonus("acc-1"); len(got) != 1 || got[0] != "starter" {
		t.Fatalf("granted bonuses = %v, want [starter]", got)
	}
	delta, err = l.Unlock("acc-1", "s2", 300)
	if err != nil {
		t.Fatalf("duplicate unlock: %v", err)
	}
	if delta.Total != 45 || len(delta.Bonuses) != 0 {
		t.Fatalf("duplicate unlock must not pay the bonus again, got %+v", delta)
	}
}

func TestGrantedBonusIsPerAccount(t *testing.T) {
	l := impl.NewPointsLedger(impl.Options{
		CategoryCaps: map[string]int64{},
		SeriesBonus:  25,
	})
	mustDefine(t, l, impl.Achievement{ID: "s1", Category: "combat", Points: 10, Series: "starter"})
	mustDefine(t, l, impl.Achievement{ID: "s2", Category: "combat", Points: 10, Series: "starter"})
	if _, err := l.Unlock("acc-a", "s1", 1); err != nil {
		t.Fatalf("unlock s1 for acc-a: %v", err)
	}
	if _, err := l.Unlock("acc-a", "s2", 2); err != nil {
		t.Fatalf("unlock s2 for acc-a: %v", err)
	}
	if got := l.GrantedBonus("acc-a"); len(got) != 1 {
		t.Fatalf("acc-a granted bonuses = %v, want one entry", got)
	}
	if got := l.GrantedBonus("acc-b"); len(got) != 0 {
		t.Fatalf("acc-b must not see acc-a bonuses, got %v", got)
	}
	if got := l.Points("acc-b").Total; got != 0 {
		t.Fatalf("acc-b points = %d, want 0", got)
	}
}

func TestRecalcSkipsNeverUnlockedDefinition(t *testing.T) {
	l := impl.NewPointsLedger(impl.Options{CategoryCaps: map[string]int64{}})
	mustDefine(t, l, impl.Achievement{ID: "a1", Category: "combat", Points: 10})
	if _, err := l.Unlock("acc-1", "a1", 100); err != nil {
		t.Fatalf("unlock a1: %v", err)
	}
	mustDefine(t, l, impl.Achievement{ID: "a2", Category: "social", Points: 15})

	rec, err := l.Recalc("acc-1")
	if err != nil {
		t.Fatalf("recalc: %v", err)
	}
	if rec.Added != 0 || rec.Total != 10 {
		t.Fatalf("recalc for a never unlocked definition = %+v, want added 0 total 10", rec)
	}
	delta, err := l.Unlock("acc-1", "a2", 200)
	if err != nil {
		t.Fatalf("unlock a2: %v", err)
	}
	if delta.Added != 15 || delta.Total != 25 {
		t.Fatalf("unlock a2 delta = %+v, want added 15 total 25", delta)
	}
}

func TestPointsUnknownAndKnownAccount(t *testing.T) {
	l := impl.NewPointsLedger(ledgerOptions())
	mustDefine(t, l, impl.Achievement{ID: "a1", Category: "combat", Points: 30})
	mustDefine(t, l, impl.Achievement{ID: "a2", Category: "explore", Points: 12})
	if _, err := l.Unlock("acc-1", "a1", 1); err != nil {
		t.Fatalf("unlock a1: %v", err)
	}
	if _, err := l.Unlock("acc-1", "a2", 2); err != nil {
		t.Fatalf("unlock a2: %v", err)
	}

	ghost := l.Points("ghost")
	if ghost.Total != 0 || len(ghost.Categories) != 0 {
		t.Fatalf("unknown account must have an empty breakdown, got %+v", ghost)
	}
	p := l.Points("acc-1")
	if p.Total != 42 {
		t.Fatalf("known account total = %d, want 42", p.Total)
	}
	if p.Categories["combat"] != 30 || p.Categories["explore"] != 12 {
		t.Fatalf("known account categories = %v, want combat 30 explore 12", p.Categories)
	}
}

func TestConcurrentUnlockSameAccount(t *testing.T) {
	l := impl.NewPointsLedger(impl.Options{CategoryCaps: map[string]int64{}})
	const n = 40
	for i := 0; i < n; i++ {
		mustDefine(t, l, impl.Achievement{ID: "a" + string(rune('A'+i)), Category: "combat", Points: 1})
	}
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := l.Unlock("acc-1", "a"+string(rune('A'+i)), int64(i))
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent unlock failed: %v", err)
		}
	}
	if got := l.Points("acc-1").Total; got != n {
		t.Fatalf("concurrent unlocks lost points: total %d, want %d", got, n)
	}
	if ids := l.Unlocked("acc-1"); len(ids) != n {
		t.Fatalf("unlocked ids = %d, want %d", len(ids), n)
	}
}

func TestConcurrentUnlockDifferentAccounts(t *testing.T) {
	l := impl.NewPointsLedger(impl.Options{CategoryCaps: map[string]int64{}})
	const n = 10
	for i := 0; i < n; i++ {
		mustDefine(t, l, impl.Achievement{ID: "a" + string(rune('A'+i)), Category: "combat", Points: int64(i + 1)})
	}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			account := "acc-a"
			if i%2 == 1 {
				account = "acc-b"
			}
			if _, err := l.Unlock(account, "a"+string(rune('A'+i)), int64(i)); err != nil {
				t.Errorf("unlock %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	if got := l.Points("acc-a").Total; got != 25 {
		t.Fatalf("acc-a total = %d, want 25", got)
	}
	if got := l.Points("acc-b").Total; got != 30 {
		t.Fatalf("acc-b total = %d, want 30", got)
	}
}

func TestSnapshotRestorePreservesLedger(t *testing.T) {
	opts := impl.Options{
		CategoryCaps: map[string]int64{"combat": 100, "explore": 100},
		SeriesBonus:  25,
	}
	l := impl.NewPointsLedger(opts)
	mustDefine(t, l, impl.Achievement{ID: "s1", Category: "combat", Points: 10, Series: "starter"})
	mustDefine(t, l, impl.Achievement{ID: "s2", Category: "explore", Points: 10, Series: "starter"})
	if _, err := l.Unlock("acc-1", "s1", 1); err != nil {
		t.Fatalf("unlock s1: %v", err)
	}
	if _, err := l.Unlock("acc-1", "s2", 2); err != nil {
		t.Fatalf("unlock s2: %v", err)
	}
	data, err := l.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	restored := impl.NewPointsLedger(opts)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.Points("acc-1").Total; got != 45 {
		t.Fatalf("restored total = %d, want 45", got)
	}
	if got := restored.GrantedBonus("acc-1"); len(got) != 1 || got[0] != "starter" {
		t.Fatalf("restored bonuses = %v, want [starter]", got)
	}
	ids := restored.Unlocked("acc-1")
	if len(ids) != 2 || ids[0] != "s1" || ids[1] != "s2" {
		t.Fatalf("restored unlocked ids = %v, want [s1 s2]", ids)
	}
	delta, err := restored.Unlock("acc-1", "s1", 3)
	if err != nil {
		t.Fatalf("unlock after restore: %v", err)
	}
	if delta.Added != 0 || delta.Total != 45 {
		t.Fatalf("unlock after restore must stay idempotent, got %+v", delta)
	}
}

func TestRestoreRejectsGarbage(t *testing.T) {
	l := impl.NewPointsLedger(ledgerOptions())
	mustDefine(t, l, impl.Achievement{ID: "a1", Category: "combat", Points: 10})
	if _, err := l.Unlock("acc-1", "a1", 1); err != nil {
		t.Fatalf("unlock a1: %v", err)
	}
	if err := l.Restore([]byte("{\"accounts\": [broken")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("garbage snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := l.Points("acc-1").Total; got != 10 {
		t.Fatalf("failed restore must keep the live ledger, total = %d want 10", got)
	}
	if ids := l.Unlocked("acc-1"); len(ids) != 1 {
		t.Fatalf("failed restore must keep unlocks, got %v", ids)
	}
}

func TestUnlockedInIDOrder(t *testing.T) {
	l := impl.NewPointsLedger(impl.Options{CategoryCaps: map[string]int64{}})
	for _, id := range []string{"gamma", "alpha", "beta"} {
		mustDefine(t, l, impl.Achievement{ID: id, Category: "combat", Points: 1})
	}
	if _, err := l.Unlock("acc-1", "gamma", 1); err != nil {
		t.Fatalf("unlock gamma: %v", err)
	}
	if _, err := l.Unlock("acc-1", "alpha", 2); err != nil {
		t.Fatalf("unlock alpha: %v", err)
	}
	if _, err := l.Unlock("acc-1", "beta", 3); err != nil {
		t.Fatalf("unlock beta: %v", err)
	}
	got := strings.Join(l.Unlocked("acc-1"), ",")
	if got != "alpha,beta,gamma" {
		t.Fatalf("unlocked order = %q, want alpha,beta,gamma", got)
	}
}

func TestCategoryPointsAgreeWithBreakdown(t *testing.T) {
	l := impl.NewPointsLedger(ledgerOptions())
	mustDefine(t, l, impl.Achievement{ID: "a1", Category: "combat", Points: 30})
	mustDefine(t, l, impl.Achievement{ID: "a2", Category: "explore", Points: 12})
	mustDefine(t, l, impl.Achievement{ID: "a3", Category: "explore", Points: 5})
	for i, id := range []string{"a1", "a2", "a3"} {
		if _, err := l.Unlock("acc-1", id, int64(i)); err != nil {
			t.Fatalf("unlock %s: %v", id, err)
		}
	}
	p := l.Points("acc-1")
	if p.Total != 47 {
		t.Fatalf("total = %d, want 47", p.Total)
	}
	for category, want := range map[string]int64{"combat": 30, "explore": 17} {
		if got := l.CategoryPoints("acc-1", category); got != want {
			t.Fatalf("CategoryPoints(%q) = %d, want %d", category, got, want)
		}
		if got := p.Categories[category]; got != want {
			t.Fatalf("breakdown[%q] = %d, want %d", category, got, want)
		}
	}
	if got := l.CategoryPoints("acc-1", "missing"); got != 0 {
		t.Fatalf("missing category points = %d, want 0", got)
	}
}
