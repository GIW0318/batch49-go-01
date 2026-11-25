package task

import (
	"errors"
	"reflect"
	"sync"
	"testing"

	impl "gsb/batch49-go-game-a-07"
)

func addLayer(t *testing.T, tbl *impl.LayeredDropTable, id string, cond impl.LayerCond, entries []impl.Entry) {
	t.Helper()
	if err := tbl.AddLayer(id, cond, entries); err != nil {
		t.Fatalf("AddLayer(%q) return error: %v", id, err)
	}
}

func effectiveTable(t *testing.T, tbl *impl.LayeredDropTable, ctx impl.Context) impl.Table {
	t.Helper()
	table, err := tbl.Effective(ctx)
	if err != nil {
		t.Fatalf("Effective(%+v) return error: %v", ctx, err)
	}
	return table
}

func TestNewTableStartsEmpty(t *testing.T) {
	tbl := impl.NewLayeredDropTable(impl.Options{})
	if tbl == nil {
		t.Fatal("NewLayeredDropTable returned nil")
	}
	table := effectiveTable(t, tbl, impl.Context{})
	if len(table.Entries) != 0 {
		t.Fatalf("entry count = %d, want 0", len(table.Entries))
	}
	if table.TotalWeight != 0 {
		t.Fatalf("total weight = %d, want 0", table.TotalWeight)
	}
}

func TestAddBaseLayerExposesEntries(t *testing.T) {
	tbl := impl.NewLayeredDropTable(impl.Options{})
	addLayer(t, tbl, "base", impl.LayerCond{Kind: "base"}, []impl.Entry{
		{ID: "sword", Weight: 10, MinCount: 1, MaxCount: 1},
	})
	table := effectiveTable(t, tbl, impl.Context{})
	if len(table.Entries) != 1 {
		t.Fatalf("entry count = %d, want 1", len(table.Entries))
	}
	if table.Entries[0].ID != "sword" {
		t.Fatalf("entry id = %q, want sword", table.Entries[0].ID)
	}
	if table.Entries[0].Weight != 10 {
		t.Fatalf("entry weight = %d, want 10", table.Entries[0].Weight)
	}
}

func TestAddDuplicateLayerRejected(t *testing.T) {
	tbl := impl.NewLayeredDropTable(impl.Options{})
	addLayer(t, tbl, "base", impl.LayerCond{Kind: "base"}, []impl.Entry{{ID: "coin", Weight: 1, MinCount: 1, MaxCount: 1}})
	err := tbl.AddLayer("base", impl.LayerCond{Kind: "base"}, []impl.Entry{{ID: "gem", Weight: 1, MinCount: 1, MaxCount: 1}})
	if !errors.Is(err, impl.ErrLayerExists) {
		t.Fatalf("duplicate layer error = %v, want ErrLayerExists", err)
	}
}

func TestAddLayerRejectsNegativeWeight(t *testing.T) {
	tbl := impl.NewLayeredDropTable(impl.Options{})
	err := tbl.AddLayer("base", impl.LayerCond{Kind: "base"}, []impl.Entry{{ID: "coin", Weight: -3, MinCount: 1, MaxCount: 1}})
	if !errors.Is(err, impl.ErrInvalidWeight) {
		t.Fatalf("negative weight error = %v, want ErrInvalidWeight", err)
	}
}

func TestAddLayerRejectsUnknownCondition(t *testing.T) {
	tbl := impl.NewLayeredDropTable(impl.Options{})
	err := tbl.AddLayer("season", impl.LayerCond{Kind: "seasonal"}, []impl.Entry{{ID: "coin", Weight: 1, MinCount: 1, MaxCount: 1}})
	if !errors.Is(err, impl.ErrInvalidCond) {
		t.Fatalf("unknown condition error = %v, want ErrInvalidCond", err)
	}
}

func TestTotalWeightExplainAndGroups(t *testing.T) {
	tbl := impl.NewLayeredDropTable(impl.Options{})
	addLayer(t, tbl, "base", impl.LayerCond{Kind: "base"}, []impl.Entry{
		{ID: "sword", Weight: 10, MinCount: 1, MaxCount: 1, Group: "weapon"},
		{ID: "axe", Weight: 20, MinCount: 1, MaxCount: 1, Group: "weapon"},
		{ID: "shield", Weight: 30, MinCount: 1, MaxCount: 2, Group: "armor"},
		{ID: "coin", Weight: 5, MinCount: 3, MaxCount: 9},
	})
	ctx := impl.Context{}
	if got := effectiveTable(t, tbl, ctx).TotalWeight; got != 65 {
		t.Fatalf("total weight = %d, want 65", got)
	}
	weight, err := tbl.Explain(ctx, "shield")
	if err != nil {
		t.Fatalf("Explain(shield) return error: %v", err)
	}
	if weight != 30 {
		t.Fatalf("explain shield = %d, want 30", weight)
	}
	if _, err := tbl.Explain(ctx, "missing"); !errors.Is(err, impl.ErrEntryNotFound) {
		t.Fatalf("Explain(missing) error = %v, want ErrEntryNotFound", err)
	}
	groups := tbl.Groups(ctx)
	if !reflect.DeepEqual(groups["weapon"], []string{"sword", "axe"}) {
		t.Fatalf("weapon group = %v, want [sword axe]", groups["weapon"])
	}
	if !reflect.DeepEqual(groups["armor"], []string{"shield"}) {
		t.Fatalf("armor group = %v, want [shield]", groups["armor"])
	}
	if !reflect.DeepEqual(groups[""], []string{"coin"}) {
		t.Fatalf("ungrouped entries = %v, want [coin]", groups[""])
	}
}

func TestDifficultyLayerSkippedOnMismatch(t *testing.T) {
	tbl := impl.NewLayeredDropTable(impl.Options{})
	addLayer(t, tbl, "base", impl.LayerCond{Kind: "base"}, []impl.Entry{{ID: "sword", Weight: 10, MinCount: 1, MaxCount: 1}})
	addLayer(t, tbl, "hero", impl.LayerCond{Kind: "difficulty", Difficulty: "heroic"}, []impl.Entry{{ID: "axe", Weight: 20, MinCount: 1, MaxCount: 1}})
	table := effectiveTable(t, tbl, impl.Context{Difficulty: "normal"})
	if len(table.Entries) != 1 {
		t.Fatalf("entry count = %d, want 1", len(table.Entries))
	}
	if table.Entries[0].ID != "sword" {
		t.Fatalf("entry id = %q, want sword", table.Entries[0].ID)
	}
}

func TestDifficultyLayerAppliesOnMatch(t *testing.T) {
	tbl := impl.NewLayeredDropTable(impl.Options{})
	addLayer(t, tbl, "base", impl.LayerCond{Kind: "base"}, []impl.Entry{{ID: "sword", Weight: 10, MinCount: 1, MaxCount: 1}})
	addLayer(t, tbl, "hero", impl.LayerCond{Kind: "difficulty", Difficulty: "heroic"}, []impl.Entry{{ID: "axe", Weight: 20, MinCount: 1, MaxCount: 1}})
	table := effectiveTable(t, tbl, impl.Context{Difficulty: "heroic"})
	if len(table.Entries) != 2 {
		t.Fatalf("entry count = %d, want 2", len(table.Entries))
	}
	if table.Entries[1].ID != "axe" || table.Entries[1].Weight != 20 {
		t.Fatalf("second entry = %+v, want axe weight 20", table.Entries[1])
	}
}

func TestFirstKillLayerAppliesOnlyOnFlag(t *testing.T) {
	tbl := impl.NewLayeredDropTable(impl.Options{})
	addLayer(t, tbl, "base", impl.LayerCond{Kind: "base"}, []impl.Entry{{ID: "sword", Weight: 10, MinCount: 1, MaxCount: 1}})
	addLayer(t, tbl, "first", impl.LayerCond{Kind: "firstkill"}, []impl.Entry{{ID: "trophy", Weight: 100, MinCount: 1, MaxCount: 1}})
	table := effectiveTable(t, tbl, impl.Context{FirstKill: true})
	if len(table.Entries) != 2 {
		t.Fatalf("entry count = %d, want 2", len(table.Entries))
	}
	if table.Entries[1].ID != "trophy" {
		t.Fatalf("second entry = %q, want trophy", table.Entries[1].ID)
	}
}

func TestLaterLayerOverridesWeightInPlace(t *testing.T) {
	tbl := impl.NewLayeredDropTable(impl.Options{})
	addLayer(t, tbl, "base", impl.LayerCond{Kind: "base"}, []impl.Entry{{ID: "sword", Weight: 10, MinCount: 1, MaxCount: 1}})
	addLayer(t, tbl, "hero", impl.LayerCond{Kind: "difficulty", Difficulty: "heroic"}, []impl.Entry{{ID: "sword", Weight: 40, MinCount: 1, MaxCount: 1}})
	table := effectiveTable(t, tbl, impl.Context{Difficulty: "heroic"})
	if len(table.Entries) != 1 {
		t.Fatalf("entry count = %d, want 1", len(table.Entries))
	}
	if table.Entries[0].Weight != 40 {
		t.Fatalf("overridden weight = %d, want 40", table.Entries[0].Weight)
	}
	if table.TotalWeight != 40 {
		t.Fatalf("total weight = %d, want 40", table.TotalWeight)
	}
}

func TestRollIsDeterministic(t *testing.T) {
	tbl := impl.NewLayeredDropTable(impl.Options{})
	addLayer(t, tbl, "base", impl.LayerCond{Kind: "base"}, []impl.Entry{
		{ID: "sword", Weight: 10, MinCount: 1, MaxCount: 1},
		{ID: "coin", Weight: 90, MinCount: 2, MaxCount: 5},
	})
	first, err := tbl.Roll(impl.Context{}, 42)
	if err != nil {
		t.Fatalf("first Roll return error: %v", err)
	}
	second, err := tbl.Roll(impl.Context{}, 42)
	if err != nil {
		t.Fatalf("second Roll return error: %v", err)
	}
	if first.Seed != 42 {
		t.Fatalf("drop seed = %d, want 42", first.Seed)
	}
	if len(first.Items) != len(second.Items) {
		t.Fatalf("item count mismatch: %d vs %d", len(first.Items), len(second.Items))
	}
	for i := range first.Items {
		if first.Items[i] != second.Items[i] {
			t.Fatalf("item %d differs: %+v vs %+v", i, first.Items[i], second.Items[i])
		}
	}
}

func TestRollOnlyReturnsEffectiveEntries(t *testing.T) {
	tbl := impl.NewLayeredDropTable(impl.Options{})
	addLayer(t, tbl, "base", impl.LayerCond{Kind: "base"}, []impl.Entry{
		{ID: "sword", Weight: 10, MinCount: 1, MaxCount: 2},
		{ID: "coin", Weight: 30, MinCount: 2, MaxCount: 4},
		{ID: "gem", Weight: 60, MinCount: 1, MaxCount: 1},
	})
	table := effectiveTable(t, tbl, impl.Context{})
	index := make(map[string]impl.Entry, len(table.Entries))
	for _, entry := range table.Entries {
		index[entry.ID] = entry
	}
	for seed := uint64(1); seed <= 20; seed++ {
		drop, err := tbl.Roll(impl.Context{}, seed)
		if err != nil {
			t.Fatalf("Roll(seed=%d) return error: %v", seed, err)
		}
		if len(drop.Items) == 0 {
			t.Fatalf("Roll(seed=%d) returned no items", seed)
		}
		for _, item := range drop.Items {
			entry, ok := index[item.EntryID]
			if !ok {
				t.Fatalf("Roll(seed=%d) returned unknown entry %q", seed, item.EntryID)
			}
			if item.Count < entry.MinCount || item.Count > entry.MaxCount {
				t.Fatalf("Roll(seed=%d) count %d outside [%d,%d] for %q", seed, item.Count, entry.MinCount, entry.MaxCount, item.EntryID)
			}
		}
	}
}

func TestRollKeepsOneItemPerGroup(t *testing.T) {
	tbl := impl.NewLayeredDropTable(impl.Options{})
	addLayer(t, tbl, "base", impl.LayerCond{Kind: "base"}, []impl.Entry{
		{ID: "blade", Weight: 10, MinCount: 1, MaxCount: 1, Group: "weapon"},
		{ID: "axe", Weight: 10, MinCount: 1, MaxCount: 1, Group: "weapon"},
		{ID: "helm", Weight: 10, MinCount: 1, MaxCount: 1, Group: "armor"},
		{ID: "boots", Weight: 10, MinCount: 1, MaxCount: 1, Group: "armor"},
		{ID: "coin", Weight: 5, MinCount: 1, MaxCount: 3},
	})
	for seed := uint64(1); seed <= 10; seed++ {
		drop, err := tbl.Roll(impl.Context{}, seed)
		if err != nil {
			t.Fatalf("Roll(seed=%d) return error: %v", seed, err)
		}
		if len(drop.Items) > 3 {
			t.Fatalf("Roll(seed=%d) returned %d items, want at most 3", seed, len(drop.Items))
		}
		perGroup := map[string]int{}
		for _, item := range drop.Items {
			perGroup[item.Group]++
		}
		if perGroup["weapon"] > 1 {
			t.Fatalf("Roll(seed=%d) returned %d weapon items", seed, perGroup["weapon"])
		}
		if perGroup["armor"] > 1 {
			t.Fatalf("Roll(seed=%d) returned %d armor items", seed, perGroup["armor"])
		}
		if perGroup[""] > 1 {
			t.Fatalf("Roll(seed=%d) returned %d ungrouped items", seed, perGroup[""])
		}
	}
}

func TestSnapshotRestoreKeepsLayers(t *testing.T) {
	tbl := impl.NewLayeredDropTable(impl.Options{})
	addLayer(t, tbl, "base", impl.LayerCond{Kind: "base"}, []impl.Entry{{ID: "sword", Weight: 10, MinCount: 1, MaxCount: 1}})
	addLayer(t, tbl, "hero", impl.LayerCond{Kind: "difficulty", Difficulty: "heroic"}, []impl.Entry{{ID: "axe", Weight: 20, MinCount: 1, MaxCount: 2}})
	data, err := tbl.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot return error: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("Snapshot returned empty payload")
	}
	restored := impl.NewLayeredDropTable(impl.Options{})
	if err := restored.Restore(data); err != nil {
		t.Fatalf("Restore return error: %v", err)
	}
	before := effectiveTable(t, tbl, impl.Context{Difficulty: "heroic"})
	after := effectiveTable(t, restored, impl.Context{Difficulty: "heroic"})
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("effective table mismatch: %+v vs %+v", before, after)
	}
	if ids := restored.LayerIDs(); !reflect.DeepEqual(ids, []string{"base", "hero"}) {
		t.Fatalf("restored layer ids = %v, want [base hero]", ids)
	}
}

func TestConcurrentAddAndRollKeepTableConsistent(t *testing.T) {
	tbl := impl.NewLayeredDropTable(impl.Options{})
	addLayer(t, tbl, "base", impl.LayerCond{Kind: "base"}, []impl.Entry{{ID: "coin", Weight: 10, MinCount: 1, MaxCount: 1}})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		id := []string{"extra-a", "extra-b", "extra-c", "extra-d"}[i]
		wg.Add(1)
		go func(layerID string) {
			defer wg.Done()
			err := tbl.AddLayer(layerID, impl.LayerCond{Kind: "base"}, []impl.Entry{{ID: layerID, Weight: 1, MinCount: 1, MaxCount: 1}})
			if err != nil {
				t.Errorf("concurrent AddLayer(%q) return error: %v", layerID, err)
			}
		}(id)
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(base uint64) {
			defer wg.Done()
			for step := uint64(0); step < 10; step++ {
				if _, err := tbl.Roll(impl.Context{}, base+step); err != nil {
					t.Errorf("concurrent Roll return error: %v", err)
					return
				}
			}
		}(uint64(i * 100))
	}
	wg.Wait()
	table := effectiveTable(t, tbl, impl.Context{})
	if len(table.Entries) != 5 {
		t.Fatalf("entry count after concurrent adds = %d, want 5", len(table.Entries))
	}
}
