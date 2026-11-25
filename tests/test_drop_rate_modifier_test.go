package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch49-go-game-a-08"
)

func newModifier(t *testing.T) *impl.DropRateModifier {
	t.Helper()
	m := impl.NewDropRateModifier(impl.Options{})
	if m == nil {
		t.Fatal("NewDropRateModifier returned nil")
	}
	return m
}

func mustSetBase(t *testing.T, m *impl.DropRateModifier, itemID string, ppm int) {
	t.Helper()
	if err := m.SetBase(itemID, ppm); err != nil {
		t.Fatalf("SetBase(%q, %d) return error: %v", itemID, ppm, err)
	}
}

func mustAddSource(t *testing.T, m *impl.DropRateModifier, player, itemID, kind, sourceID string, ppm int, until int64) {
	t.Helper()
	if err := m.AddSource(player, itemID, kind, sourceID, ppm, until); err != nil {
		t.Fatalf("AddSource(%q, %q, %q, %q, %d, %d) return error: %v", player, itemID, kind, sourceID, ppm, until, err)
	}
}

func mustEffective(t *testing.T, m *impl.DropRateModifier, player, itemID string, at int64) int {
	t.Helper()
	got, err := m.Effective(player, itemID, at)
	if err != nil {
		t.Fatalf("Effective(%q, %q, %d) return error: %v", player, itemID, at, err)
	}
	return got
}

func TestNewModifierUsesMillionCap(t *testing.T) {
	m := newModifier(t)
	if got := m.Cap(); got != 1000000 {
		t.Fatalf("default cap = %d, want 1000000", got)
	}
}

func TestSetBaseAndReadBack(t *testing.T) {
	m := newModifier(t)
	mustSetBase(t, m, "sword", 10000)
	got, err := m.Base("sword")
	if err != nil {
		t.Fatalf("Base(sword) return error: %v", err)
	}
	if got != 10000 {
		t.Fatalf("base = %d, want 10000", got)
	}
}

func TestSetBaseRejectsNegativePPM(t *testing.T) {
	m := newModifier(t)
	if err := m.SetBase("sword", -1); !errors.Is(err, impl.ErrInvalidPPM) {
		t.Fatalf("negative base error = %v, want ErrInvalidPPM", err)
	}
}

func TestEffectiveWithSingleSource(t *testing.T) {
	m := newModifier(t)
	mustSetBase(t, m, "sword", 10000)
	mustAddSource(t, m, "alice", "sword", "luck", "charm", 100000, 1000000)
	if got := mustEffective(t, m, "alice", "sword", 0); got != 11000 {
		t.Fatalf("effective = %d, want 11000", got)
	}
}

func TestSameKindSourcesDiminish(t *testing.T) {
	m := newModifier(t)
	mustSetBase(t, m, "sword", 10000)
	mustAddSource(t, m, "alice", "sword", "luck", "a", 100000, 1000000)
	mustAddSource(t, m, "alice", "sword", "luck", "b", 100000, 1000000)
	if got := mustEffective(t, m, "alice", "sword", 0); got != 11500 {
		t.Fatalf("effective with two sources = %d, want 11500", got)
	}
}

func TestSameKindThirdSourceUsesThirdDivisor(t *testing.T) {
	m := newModifier(t)
	mustSetBase(t, m, "sword", 10000)
	mustAddSource(t, m, "alice", "sword", "luck", "a", 100000, 1000000)
	mustAddSource(t, m, "alice", "sword", "luck", "b", 100000, 1000000)
	mustAddSource(t, m, "alice", "sword", "luck", "c", 100000, 1000000)
	if got := mustEffective(t, m, "alice", "sword", 0); got != 11833 {
		t.Fatalf("effective with three sources = %d, want 11833", got)
	}
}

func TestSameKindOrderFollowsSourceID(t *testing.T) {
	m := newModifier(t)
	mustSetBase(t, m, "sword", 10000)
	mustAddSource(t, m, "alice", "sword", "luck", "b", 100000, 1000000)
	mustAddSource(t, m, "alice", "sword", "luck", "a", 200000, 1000000)
	if got := mustEffective(t, m, "alice", "sword", 0); got != 12500 {
		t.Fatalf("effective with out-of-order insert = %d, want 12500", got)
	}
}

func TestRemoveSourceRestoresBase(t *testing.T) {
	m := newModifier(t)
	mustSetBase(t, m, "sword", 10000)
	mustAddSource(t, m, "alice", "sword", "luck", "charm", 100000, 1000000)
	if err := m.RemoveSource("alice", "charm"); err != nil {
		t.Fatalf("RemoveSource return error: %v", err)
	}
	if got := mustEffective(t, m, "alice", "sword", 0); got != 10000 {
		t.Fatalf("effective after remove = %d, want 10000", got)
	}
	if got := m.SourceCount("alice"); got != 0 {
		t.Fatalf("source count after remove = %d, want 0", got)
	}
}

func TestRemoveUnknownSourceRejected(t *testing.T) {
	m := newModifier(t)
	if err := m.RemoveSource("alice", "missing"); !errors.Is(err, impl.ErrSourceNotFound) {
		t.Fatalf("remove unknown source error = %v, want ErrSourceNotFound", err)
	}
}

func TestBreakdownListsKinds(t *testing.T) {
	m := newModifier(t)
	mustSetBase(t, m, "sword", 10000)
	mustAddSource(t, m, "alice", "sword", "luck", "charm", 100000, 1000000)
	mustAddSource(t, m, "alice", "sword", "event", "festival", 200000, 1000000)
	breakdown := m.Breakdown("alice", "sword", 0)
	if len(breakdown) != 2 {
		t.Fatalf("breakdown size = %d, want 2", len(breakdown))
	}
	byKind := map[string]impl.Contribution{}
	for _, item := range breakdown {
		byKind[item.Kind] = item
	}
	luck, ok := byKind["luck"]
	if !ok {
		t.Fatal("breakdown missing luck contribution")
	}
	if luck.PPM != 100000 || luck.Applied != 100000 || luck.Expired {
		t.Fatalf("luck contribution = %+v, want ppm 100000 applied 100000 live", luck)
	}
	event, ok := byKind["event"]
	if !ok {
		t.Fatal("breakdown missing event contribution")
	}
	if event.PPM != 200000 || event.Applied != 200000 || event.Expired {
		t.Fatalf("event contribution = %+v, want ppm 200000 applied 200000 live", event)
	}
}

func TestBreakdownUnknownPlayerIsEmpty(t *testing.T) {
	m := newModifier(t)
	mustSetBase(t, m, "sword", 10000)
	mustAddSource(t, m, "alice", "sword", "luck", "charm", 100000, 1000000)
	if got := m.Breakdown("alice", "sword", 0); len(got) != 1 {
		t.Fatalf("known player breakdown size = %d, want 1", len(got))
	}
	if got := m.Breakdown("nobody", "sword", 0); len(got) != 0 {
		t.Fatalf("unknown player breakdown size = %d, want 0", len(got))
	}
}

func TestExpiredSourceDroppedAtBoundary(t *testing.T) {
	m := newModifier(t)
	mustSetBase(t, m, "sword", 10000)
	mustAddSource(t, m, "alice", "sword", "luck", "charm", 100000, 5000)
	if got := mustEffective(t, m, "alice", "sword", 4999); got != 11000 {
		t.Fatalf("effective before expiry = %d, want 11000", got)
	}
	if got := mustEffective(t, m, "alice", "sword", 5000); got != 10000 {
		t.Fatalf("effective at expiry = %d, want 10000", got)
	}
}

func TestEffectiveTruncatedAtCap(t *testing.T) {
	m := newModifier(t)
	mustSetBase(t, m, "sword", 900000)
	mustAddSource(t, m, "alice", "sword", "luck", "charm", 500000, 1000000)
	if got := mustEffective(t, m, "alice", "sword", 0); got != 1000000 {
		t.Fatalf("effective above cap = %d, want 1000000", got)
	}
}

func TestSnapshotRestoreKeepsSources(t *testing.T) {
	m := newModifier(t)
	mustSetBase(t, m, "sword", 10000)
	mustAddSource(t, m, "alice", "sword", "luck", "charm", 100000, 90000)
	data, err := m.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot return error: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("Snapshot returned empty payload")
	}
	restored := impl.NewDropRateModifier(impl.Options{})
	if err := restored.Restore(data); err != nil {
		t.Fatalf("Restore return error: %v", err)
	}
	if got := restored.SourceCount("alice"); got != 1 {
		t.Fatalf("restored source count = %d, want 1", got)
	}
	got, err := restored.Effective("alice", "sword", 0)
	if err != nil {
		t.Fatalf("restored Effective return error: %v", err)
	}
	if got != 11000 {
		t.Fatalf("restored effective = %d, want 11000", got)
	}
}

func TestConcurrentSourcesStayWithinCap(t *testing.T) {
	m := newModifier(t)
	mustSetBase(t, m, "sword", 1000)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		sourceID := []string{"s-a", "s-b", "s-c", "s-d"}[i]
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if err := m.AddSource("alice", "sword", "luck", id, 100000, 1000000); err != nil {
				t.Errorf("concurrent AddSource(%q) return error: %v", id, err)
			}
		}(sourceID)
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for step := 0; step < 10; step++ {
				value, err := m.Effective("alice", "sword", 0)
				if err != nil {
					t.Errorf("concurrent Effective return error: %v", err)
					return
				}
				if value < 0 || value > 1000000 {
					t.Errorf("concurrent effective = %d, want within [0, 1000000]", value)
					return
				}
			}
		}()
	}
	wg.Wait()
	if got := m.SourceCount("alice"); got != 4 {
		t.Fatalf("source count after concurrent adds = %d, want 4", got)
	}
}
