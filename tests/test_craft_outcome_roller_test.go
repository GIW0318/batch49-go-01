package task

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	impl "gsb/batch49-go-game-a-26"
)

func craftOptions() impl.Options {
	return impl.Options{MaxSuccessPermille: 950, MaxHistory: 64}
}

func basicRecipe(id string, base, perLevel int) impl.RecipeSpec {
	return impl.RecipeSpec{
		ID:                  id,
		BasePermille:        base,
		ProficiencyPerLevel: perLevel,
		Materials: []impl.Material{
			{ID: "ore", Count: 4},
			{ID: "cloth", Count: 6},
		},
		RefundPermille: 1000,
	}
}

func craftSeed(i int) uint64 {
	return uint64(i+1) * 0x9E3779B97F4A7C15
}

func tierFor(tiers []impl.QualityTier, draw int) string {
	name := ""
	for _, tier := range tiers {
		if draw >= tier.MinPermille {
			name = tier.Name
		}
	}
	return name
}

func TestConstructorInstallsDefaultTiers(t *testing.T) {
	r := impl.NewCraftOutcomeRoller(impl.Options{})
	tiers := r.QualityTiers()
	if len(tiers) < 3 {
		t.Fatalf("default ladder has %d tiers, want at least 3", len(tiers))
	}
	if tiers[0].MinPermille != 0 {
		t.Fatalf("lowest tier starts at %d, want 0", tiers[0].MinPermille)
	}
	for i := 1; i < len(tiers); i++ {
		if tiers[i].MinPermille <= tiers[i-1].MinPermille {
			t.Fatalf("tier ladder not ascending at %d: %+v", i, tiers)
		}
		if tiers[i].Name == "" {
			t.Fatalf("tier %d has a blank name", i)
		}
	}
	tiers[0].Name = "mutated"
	if r.QualityTiers()[0].Name == "mutated" {
		t.Fatalf("QualityTiers must return a defensive copy")
	}
}

func TestSetQualityTiersValidation(t *testing.T) {
	r := impl.NewCraftOutcomeRoller(craftOptions())
	bad := [][]impl.QualityTier{
		{{Name: "a", MinPermille: 10}, {Name: "b", MinPermille: 500}},
		{{Name: "a", MinPermille: 0}, {Name: "b", MinPermille: 500}, {Name: "c", MinPermille: 400}},
		{{Name: "a", MinPermille: 0}, {Name: "a", MinPermille: 500}},
		{{Name: "", MinPermille: 0}, {Name: "b", MinPermille: 500}},
	}
	for i, tiers := range bad {
		if err := r.SetQualityTiers(tiers); !errors.Is(err, impl.ErrBadTier) {
			t.Fatalf("bad ladder %d must be rejected with ErrBadTier, got %v", i, err)
		}
	}
	good := []impl.QualityTier{
		{Name: "普通", MinPermille: 0},
		{Name: "精良", MinPermille: 500},
		{Name: "史诗", MinPermille: 900},
	}
	if err := r.SetQualityTiers(good); err != nil {
		t.Fatalf("valid ladder rejected: %v", err)
	}
	got := r.QualityTiers()
	if len(got) != len(good) {
		t.Fatalf("ladder length = %d, want %d", len(got), len(good))
	}
	for i := range got {
		if got[i] != good[i] {
			t.Fatalf("tier %d = %+v, want %+v", i, got[i], good[i])
		}
	}
}

func TestSetRecipeValidation(t *testing.T) {
	r := impl.NewCraftOutcomeRoller(craftOptions())
	good := basicRecipe("forged-blade", 400, 25)
	bad := []impl.RecipeSpec{
		{ID: "", BasePermille: 400, ProficiencyPerLevel: 10, Materials: good.Materials, RefundPermille: 500},
		{ID: "r1", BasePermille: 400, ProficiencyPerLevel: 10, RefundPermille: 500},
		{ID: "r1", BasePermille: 400, ProficiencyPerLevel: 10, Materials: []impl.Material{{ID: "", Count: 2}}, RefundPermille: 500},
		{ID: "r1", BasePermille: 400, ProficiencyPerLevel: 10, Materials: []impl.Material{{ID: "ore", Count: 0}}, RefundPermille: 500},
		{ID: "r1", BasePermille: 400, ProficiencyPerLevel: 10, Materials: []impl.Material{{ID: "ore", Count: 2}, {ID: "ore", Count: 3}}, RefundPermille: 500},
		{ID: "r1", BasePermille: -1, ProficiencyPerLevel: 10, Materials: good.Materials, RefundPermille: 500},
		{ID: "r1", BasePermille: 1001, ProficiencyPerLevel: 10, Materials: good.Materials, RefundPermille: 500},
		{ID: "r1", BasePermille: 400, ProficiencyPerLevel: -2, Materials: good.Materials, RefundPermille: 500},
		{ID: "r1", BasePermille: 400, ProficiencyPerLevel: 10, Materials: good.Materials, RefundPermille: 1001},
		{ID: "r1", BasePermille: 400, ProficiencyPerLevel: 10, Materials: good.Materials, RefundPermille: -1},
	}
	for i, spec := range bad {
		if err := r.SetRecipe(spec); !errors.Is(err, impl.ErrBadRecipe) {
			t.Fatalf("bad recipe %d must be rejected with ErrBadRecipe, got %v", i, err)
		}
	}
	if _, ok := r.Recipe("forged-blade"); ok {
		t.Fatalf("rejected recipes must not be stored")
	}
	if err := r.SetRecipe(good); err != nil {
		t.Fatalf("valid recipe rejected: %v", err)
	}
	stored, ok := r.Recipe("forged-blade")
	if !ok {
		t.Fatalf("valid recipe is missing after SetRecipe")
	}
	if stored.BasePermille != 400 || stored.ProficiencyPerLevel != 25 || stored.RefundPermille != 1000 {
		t.Fatalf("stored recipe mismatch: %+v", stored)
	}
	if len(stored.Materials) != 2 || stored.Materials[0].ID != "ore" || stored.Materials[1].Count != 6 {
		t.Fatalf("stored materials mismatch: %+v", stored.Materials)
	}
}

func TestRecipeLookupAndOrdering(t *testing.T) {
	r := impl.NewCraftOutcomeRoller(craftOptions())
	if err := r.SetRecipe(basicRecipe("zeta", 300, 10)); err != nil {
		t.Fatalf("load zeta: %v", err)
	}
	if err := r.SetRecipe(basicRecipe("alpha", 600, 5)); err != nil {
		t.Fatalf("load alpha: %v", err)
	}
	list := r.Recipes()
	if len(list) != 2 || list[0].ID != "alpha" || list[1].ID != "zeta" {
		t.Fatalf("Recipes order = %+v, want alpha before zeta", list)
	}
	stored, ok := r.Recipe("alpha")
	if !ok {
		t.Fatalf("alpha must be found")
	}
	stored.Materials[0].Count = 99
	again, _ := r.Recipe("alpha")
	if again.Materials[0].Count != 4 {
		t.Fatalf("Recipe must return a defensive copy, stored count = %d", again.Materials[0].Count)
	}
	if _, ok := r.Recipe("missing"); ok {
		t.Fatalf("unknown recipe must not be reported as loaded")
	}
}

func TestSuccessPermilleCapping(t *testing.T) {
	r := impl.NewCraftOutcomeRoller(impl.Options{MaxSuccessPermille: 950})
	if err := r.SetRecipe(basicRecipe("r", 500, 60)); err != nil {
		t.Fatalf("load recipe: %v", err)
	}
	got, err := r.SuccessPermille(impl.CraftRequest{RecipeID: "r", PlayerID: "p1", Proficiency: 10})
	if err != nil {
		t.Fatalf("SuccessPermille: %v", err)
	}
	if got != 950 {
		t.Fatalf("capped rate = %d, want 950", got)
	}
	got, err = r.SuccessPermille(impl.CraftRequest{RecipeID: "r", PlayerID: "p1", Proficiency: 2})
	if err != nil {
		t.Fatalf("SuccessPermille: %v", err)
	}
	if got != 620 {
		t.Fatalf("base plus bonus = %d, want 620", got)
	}
	low := impl.NewCraftOutcomeRoller(impl.Options{MaxSuccessPermille: 900})
	if err := low.SetRecipe(basicRecipe("r", 950, 10)); err != nil {
		t.Fatalf("load recipe: %v", err)
	}
	got, err = low.SuccessPermille(impl.CraftRequest{RecipeID: "r", PlayerID: "p1"})
	if err != nil || got != 900 {
		t.Fatalf("option cap must win, got rate=%d err=%v", got, err)
	}
	deflt := impl.NewCraftOutcomeRoller(impl.Options{})
	if err := deflt.SetRecipe(basicRecipe("r", 1000, 0)); err != nil {
		t.Fatalf("load recipe: %v", err)
	}
	got, err = deflt.SuccessPermille(impl.CraftRequest{RecipeID: "r", PlayerID: "p1"})
	if err != nil || got != impl.DefaultMaxSuccessPermille {
		t.Fatalf("default cap must apply, got rate=%d err=%v", got, err)
	}
	if _, err := r.SuccessPermille(impl.CraftRequest{RecipeID: "nope", PlayerID: "p1"}); !errors.Is(err, impl.ErrUnknownRecipe) {
		t.Fatalf("unknown recipe must fail with ErrUnknownRecipe, got %v", err)
	}
}

func TestRollRejectsUnknownRecipeAndBadRequest(t *testing.T) {
	r := impl.NewCraftOutcomeRoller(craftOptions())
	if err := r.SetRecipe(basicRecipe("r", 500, 0)); err != nil {
		t.Fatalf("load recipe: %v", err)
	}
	if _, err := r.Roll(impl.CraftRequest{RecipeID: "nope", PlayerID: "p1"}, 1); !errors.Is(err, impl.ErrUnknownRecipe) {
		t.Fatalf("unknown recipe roll = %v, want ErrUnknownRecipe", err)
	}
	cases := []impl.CraftRequest{
		{RecipeID: "", PlayerID: "p1"},
		{RecipeID: "r", PlayerID: "  "},
		{RecipeID: "r", PlayerID: "p1", Proficiency: -1},
	}
	for i, req := range cases {
		if _, err := r.Roll(req, 2); !errors.Is(err, impl.ErrBadRequest) {
			t.Fatalf("request %d = %v, want ErrBadRequest", i, err)
		}
	}
	_, err := r.Batch([]impl.CraftRequest{
		{RecipeID: "r", PlayerID: "p1"},
		{RecipeID: "nope", PlayerID: "p2"},
	}, 3)
	if !errors.Is(err, impl.ErrUnknownRecipe) {
		t.Fatalf("batch with unknown recipe = %v, want ErrUnknownRecipe", err)
	}
	if got := len(r.History()); got != 0 {
		t.Fatalf("failed batch must not append history, got %d", got)
	}
}

func TestRollDeterministicForSameSeed(t *testing.T) {
	left := impl.NewCraftOutcomeRoller(craftOptions())
	right := impl.NewCraftOutcomeRoller(craftOptions())
	if err := left.SetRecipe(basicRecipe("r", 500, 30)); err != nil {
		t.Fatalf("load left: %v", err)
	}
	if err := right.SetRecipe(basicRecipe("r", 500, 30)); err != nil {
		t.Fatalf("load right: %v", err)
	}
	req := impl.CraftRequest{RecipeID: "r", PlayerID: "p1", Proficiency: 4}
	for i := 0; i < 6; i++ {
		seed := craftSeed(i)
		a, err := left.Roll(req, seed)
		if err != nil {
			t.Fatalf("left roll %d: %v", i, err)
		}
		b, err := right.Roll(req, seed)
		if err != nil {
			t.Fatalf("right roll %d: %v", i, err)
		}
		if a.Success != b.Success || a.SuccessPermille != b.SuccessPermille ||
			a.SuccessDraw != b.SuccessDraw || a.QualityDraw != b.QualityDraw || a.Quality != b.Quality {
			t.Fatalf("same seed %d diverged: %+v vs %+v", seed, a, b)
		}
		again, err := left.Roll(req, seed)
		if err != nil {
			t.Fatalf("repeat roll %d: %v", i, err)
		}
		if again.Success != a.Success || again.SuccessDraw != a.SuccessDraw ||
			again.QualityDraw != a.QualityDraw || again.Quality != a.Quality {
			t.Fatalf("repeat on the same roller diverged for seed %d", seed)
		}
	}
}

func TestRollDrawConsistency(t *testing.T) {
	r := impl.NewCraftOutcomeRoller(craftOptions())
	rec := basicRecipe("r", 400, 0)
	rec.RefundPermille = 1000
	if err := r.SetRecipe(rec); err != nil {
		t.Fatalf("load recipe: %v", err)
	}
	tiers := r.QualityTiers()
	req := impl.CraftRequest{RecipeID: "r", PlayerID: "p1"}
	successes, failures := 0, 0
	for i := 0; i < 200; i++ {
		o, err := r.Roll(req, craftSeed(i))
		if err != nil {
			t.Fatalf("roll %d: %v", i, err)
		}
		if o.SuccessPermille != 400 {
			t.Fatalf("roll %d rate = %d, want 400", i, o.SuccessPermille)
		}
		if o.Success != (o.SuccessDraw < 400) {
			t.Fatalf("roll %d success=%v draw=%d inconsistent", i, o.Success, o.SuccessDraw)
		}
		if o.SuccessDraw < 0 || o.SuccessDraw >= impl.DrawRange {
			t.Fatalf("roll %d success draw %d outside range", i, o.SuccessDraw)
		}
		if o.QualityDraw < 0 || o.QualityDraw >= impl.DrawRange {
			t.Fatalf("roll %d quality draw %d outside range", i, o.QualityDraw)
		}
		if o.Success {
			successes++
			if want := tierFor(tiers, o.QualityDraw); o.Quality != want {
				t.Fatalf("roll %d quality = %q, want %q for draw %d", i, o.Quality, want, o.QualityDraw)
			}
			if len(o.Refund) != 0 {
				t.Fatalf("roll %d succeeded but refund has %d entries", i, len(o.Refund))
			}
		} else {
			failures++
			if o.Quality != "" {
				t.Fatalf("roll %d failed but carries quality %q", i, o.Quality)
			}
			if len(o.Refund) != 2 || o.Refund["ore"] != 4 || o.Refund["cloth"] != 6 {
				t.Fatalf("roll %d refund = %+v, want full material table", i, o.Refund)
			}
		}
	}
	if successes == 0 || failures == 0 {
		t.Fatalf("rate 400 produced successes=%d failures=%d, want both", successes, failures)
	}
}

func TestSuccessRateBoundaries(t *testing.T) {
	r := impl.NewCraftOutcomeRoller(impl.Options{MaxSuccessPermille: 1000, MaxHistory: 128})
	certain := basicRecipe("certain", 1000, 0)
	if err := r.SetRecipe(certain); err != nil {
		t.Fatalf("load certain recipe: %v", err)
	}
	for i := 0; i < 50; i++ {
		o, err := r.Roll(impl.CraftRequest{RecipeID: "certain", PlayerID: "p1"}, craftSeed(i))
		if err != nil {
			t.Fatalf("certain roll %d: %v", i, err)
		}
		if !o.Success {
			t.Fatalf("rate 1000 roll %d failed at draw %d", i, o.SuccessDraw)
		}
		if o.SuccessPermille != 1000 {
			t.Fatalf("certain rate = %d, want 1000", o.SuccessPermille)
		}
	}
	hopeless := impl.RecipeSpec{
		ID:                  "hopeless",
		BasePermille:        0,
		ProficiencyPerLevel: 50,
		Materials:           []impl.Material{{ID: "ore", Count: 4}, {ID: "cloth", Count: 8}},
		RefundPermille:      500,
	}
	if err := r.SetRecipe(hopeless); err != nil {
		t.Fatalf("load hopeless recipe: %v", err)
	}
	for i := 0; i < 50; i++ {
		o, err := r.Roll(impl.CraftRequest{RecipeID: "hopeless", PlayerID: "p2"}, craftSeed(i))
		if err != nil {
			t.Fatalf("hopeless roll %d: %v", i, err)
		}
		if o.Success {
			t.Fatalf("rate 0 roll %d succeeded at draw %d", i, o.SuccessDraw)
		}
		if len(o.Refund) != 2 || o.Refund["ore"] != 2 || o.Refund["cloth"] != 4 {
			t.Fatalf("hopeless roll %d refund = %+v, want ore:2 cloth:4", i, o.Refund)
		}
	}
}

func TestQualityTierMapping(t *testing.T) {
	r := impl.NewCraftOutcomeRoller(craftOptions())
	tiers := []impl.QualityTier{
		{Name: "coarse", MinPermille: 0},
		{Name: "fine", MinPermille: 500},
		{Name: "relic", MinPermille: 900},
	}
	if err := r.SetQualityTiers(tiers); err != nil {
		t.Fatalf("set tiers: %v", err)
	}
	rec := basicRecipe("r", 500, 0)
	rec.RefundPermille = 0
	if err := r.SetRecipe(rec); err != nil {
		t.Fatalf("load recipe: %v", err)
	}
	seen := map[string]int{}
	for i := 0; i < 200; i++ {
		o, err := r.Roll(impl.CraftRequest{RecipeID: "r", PlayerID: "p3"}, craftSeed(i))
		if err != nil {
			t.Fatalf("roll %d: %v", i, err)
		}
		if !o.Success {
			continue
		}
		if o.Quality != tierFor(tiers, o.QualityDraw) {
			t.Fatalf("roll %d tier %q does not match draw %d", i, o.Quality, o.QualityDraw)
		}
		seen[o.Quality]++
	}
	if len(seen) < 2 {
		t.Fatalf("quality tiers seen = %v, want at least two distinct tiers", seen)
	}
}

func TestBatchMatchesIndividualRolls(t *testing.T) {
	left := impl.NewCraftOutcomeRoller(craftOptions())
	right := impl.NewCraftOutcomeRoller(craftOptions())
	for _, spec := range []impl.RecipeSpec{
		basicRecipe("a", 300, 20),
		basicRecipe("b", 700, 10),
		basicRecipe("c", 500, 40),
	} {
		if err := left.SetRecipe(spec); err != nil {
			t.Fatalf("load %s: %v", spec.ID, err)
		}
		if err := right.SetRecipe(spec); err != nil {
			t.Fatalf("load %s: %v", spec.ID, err)
		}
	}
	reqs := []impl.CraftRequest{
		{RecipeID: "a", PlayerID: "p1", Proficiency: 3},
		{RecipeID: "b", PlayerID: "p2", Proficiency: 1},
		{RecipeID: "c", PlayerID: "p3", Proficiency: 8},
	}
	batch, err := left.Batch(reqs, craftSeed(11))
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	if len(batch) != len(reqs) {
		t.Fatalf("batch len = %d, want %d", len(batch), len(reqs))
	}
	for i, req := range reqs {
		want, err := right.Roll(req, craftSeed(11))
		if err != nil {
			t.Fatalf("individual roll %d: %v", i, err)
		}
		if batch[i].RecipeID != req.RecipeID || batch[i].PlayerID != req.PlayerID {
			t.Fatalf("batch %d identity = %+v, want %s/%s", i, batch[i], req.RecipeID, req.PlayerID)
		}
		if batch[i].Success != want.Success || batch[i].SuccessDraw != want.SuccessDraw ||
			batch[i].QualityDraw != want.QualityDraw || batch[i].Quality != want.Quality {
			t.Fatalf("batch %d differs from individual roll: %+v vs %+v", i, batch[i], want)
		}
	}
	if got := len(left.History()); got != 3 {
		t.Fatalf("history after batch = %d, want 3", got)
	}
	empty, err := left.Batch(nil, 5)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty batch = %v err=%v, want empty non-nil slice", empty, err)
	}
}

func TestHistoryCapAndOrder(t *testing.T) {
	r := impl.NewCraftOutcomeRoller(impl.Options{MaxSuccessPermille: 950, MaxHistory: 3})
	for _, id := range []string{"r1", "r2", "r3", "r4", "r5", "r6"} {
		if err := r.SetRecipe(basicRecipe(id, 500, 0)); err != nil {
			t.Fatalf("load %s: %v", id, err)
		}
		if _, err := r.Roll(impl.CraftRequest{RecipeID: id, PlayerID: "p1"}, craftSeed(2)); err != nil {
			t.Fatalf("roll %s: %v", id, err)
		}
	}
	history := r.History()
	if len(history) != 3 {
		t.Fatalf("history len = %d, want 3", len(history))
	}
	for i, want := range []string{"r4", "r5", "r6"} {
		if history[i].RecipeID != want {
			t.Fatalf("history[%d] = %s, want %s", i, history[i].RecipeID, want)
		}
	}
	history[0].PlayerID = "tampered"
	if r.History()[0].PlayerID == "tampered" {
		t.Fatalf("History must return a defensive copy")
	}
}

func TestRefundIsPerMaterialCopy(t *testing.T) {
	r := impl.NewCraftOutcomeRoller(craftOptions())
	rec := basicRecipe("r", 500, 0)
	rec.RefundPermille = 500
	rec.Materials = []impl.Material{{ID: "ore", Count: 4}, {ID: "cloth", Count: 6}}
	if err := r.SetRecipe(rec); err != nil {
		t.Fatalf("load recipe: %v", err)
	}
	var failed, succeeded impl.Outcome
	for i := 0; i < 50 && (!failed.Success || succeeded.Success); i++ {
		o, err := r.Roll(impl.CraftRequest{RecipeID: "r", PlayerID: "p1"}, craftSeed(i))
		if err != nil {
			t.Fatalf("roll %d: %v", i, err)
		}
		if o.Success {
			if succeeded.RecipeID == "" {
				succeeded = o
			}
		} else if failed.RecipeID == "" {
			failed = o
		}
	}
	if failed.RecipeID == "" || succeeded.RecipeID == "" {
		t.Fatalf("sample did not contain both outcomes: failed=%+v succeeded=%+v", failed, succeeded)
	}
	refund := r.Refund(failed)
	if len(refund) != 2 || refund["ore"] != 2 || refund["cloth"] != 3 {
		t.Fatalf("refund = %+v, want ore:2 cloth:3", refund)
	}
	refund["ore"] = 99
	if again := r.Refund(failed); again["ore"] != 2 {
		t.Fatalf("Refund must return a copy, second read = %d", again["ore"])
	}
	if got := r.Refund(succeeded); len(got) != 0 {
		t.Fatalf("successful outcome refund = %+v, want empty", got)
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	r := impl.NewCraftOutcomeRoller(craftOptions())
	if err := r.SetQualityTiers([]impl.QualityTier{
		{Name: "coarse", MinPermille: 0},
		{Name: "fine", MinPermille: 600},
		{Name: "relic", MinPermille: 950},
	}); err != nil {
		t.Fatalf("set tiers: %v", err)
	}
	for _, spec := range []impl.RecipeSpec{basicRecipe("a", 400, 10), basicRecipe("b", 900, 0)} {
		if err := r.SetRecipe(spec); err != nil {
			t.Fatalf("load %s: %v", spec.ID, err)
		}
	}
	for i := 0; i < 3; i++ {
		if _, err := r.Roll(impl.CraftRequest{RecipeID: "a", PlayerID: "p1"}, craftSeed(i)); err != nil {
			t.Fatalf("roll %d: %v", i, err)
		}
	}
	data, err := r.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := impl.NewCraftOutcomeRoller(impl.Options{})
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.QualityTiers(); len(got) != 3 || got[2].Name != "relic" {
		t.Fatalf("restored tiers = %+v", got)
	}
	if got := restored.Recipes(); len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" {
		t.Fatalf("restored recipes = %+v", got)
	}
	if got := restored.History(); len(got) != 3 {
		t.Fatalf("restored history len = %d, want 3", len(got))
	}
	if err := restored.Restore([]byte("{not json")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("bad payload = %v, want ErrBadSnapshot", err)
	}
	if got := len(restored.Recipes()); got != 2 {
		t.Fatalf("failed restore must keep the old state, recipes = %d", got)
	}
	if err := restored.Restore(nil); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("nil payload = %v, want ErrBadSnapshot", err)
	}
}

func TestConcurrentRollAndSnapshot(t *testing.T) {
	r := impl.NewCraftOutcomeRoller(impl.Options{MaxSuccessPermille: 950, MaxHistory: 500})
	if err := r.SetRecipe(basicRecipe("r", 500, 0)); err != nil {
		t.Fatalf("load recipe: %v", err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 256)
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				req := impl.CraftRequest{RecipeID: "r", PlayerID: fmt.Sprintf("p-%d", worker)}
				if _, err := r.Roll(req, craftSeed(i)); err != nil {
					errs <- err
					return
				}
			}
		}(w)
	}
	for s := 0; s < 4; s++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				if _, err := r.Snapshot(); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent operation failed: %v", err)
	}
	if got := len(r.History()); got != 200 {
		t.Fatalf("history after concurrent rolls = %d, want 200", got)
	}
}
