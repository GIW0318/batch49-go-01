package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch49-go-game-a-10"
)

func newSettlement(t *testing.T, opts impl.Options) *impl.HealingSettlement {
	t.Helper()
	s := impl.NewHealingSettlement(opts)
	if s == nil {
		t.Fatal("NewHealingSettlement returned nil")
	}
	return s
}

func mustSetHealth(t *testing.T, s *impl.HealingSettlement, target string, hp, maxHP int) {
	t.Helper()
	if err := s.SetHealth(target, hp, maxHP); err != nil {
		t.Fatalf("SetHealth(%q, %d, %d) return error: %v", target, hp, maxHP, err)
	}
}

func mustApply(t *testing.T, s *impl.HealingSettlement, req impl.HealRequest) impl.HealResult {
	t.Helper()
	result, err := s.ApplyHeal(req)
	if err != nil {
		t.Fatalf("ApplyHeal(%+v) return error: %v", req, err)
	}
	return result
}

func TestNewSettlementDefaults(t *testing.T) {
	s := newSettlement(t, impl.Options{})
	if got := s.OverhealShieldPct(); got != 50 {
		t.Fatalf("default overheal shield pct = %d, want 50", got)
	}
	if got := s.MaxShield(); got != 10000 {
		t.Fatalf("default shield cap = %d, want 10000", got)
	}
	if got := s.MaxHoTs(); got != 4 {
		t.Fatalf("default hot cap = %d, want 4", got)
	}
}

func TestSetHealthAndReadBack(t *testing.T) {
	s := newSettlement(t, impl.Options{})
	mustSetHealth(t, s, "tank", 100, 1000)
	if got := s.Health("tank"); got != 100 {
		t.Fatalf("health = %d, want 100", got)
	}
}

func TestApplyHealAddsToHealth(t *testing.T) {
	s := newSettlement(t, impl.Options{})
	mustSetHealth(t, s, "tank", 100, 1000)
	result := mustApply(t, s, impl.HealRequest{Target: "tank", Amount: 300, At: 0})
	if result.Applied != 300 {
		t.Fatalf("applied = %d, want 300", result.Applied)
	}
	if result.Overheal != 0 {
		t.Fatalf("overheal = %d, want 0", result.Overheal)
	}
	if result.Health != 400 {
		t.Fatalf("result health = %d, want 400", result.Health)
	}
	if got := s.Health("tank"); got != 400 {
		t.Fatalf("stored health = %d, want 400", got)
	}
	if result.ShieldGained != 0 || result.ShieldTotal != 0 {
		t.Fatalf("unexpected shield in result: %+v", result)
	}
}

func TestApplyHealHonoursReduction(t *testing.T) {
	s := newSettlement(t, impl.Options{})
	mustSetHealth(t, s, "tank", 100, 1000)
	result := mustApply(t, s, impl.HealRequest{Target: "tank", Amount: 300, At: 0, ReductionPct: 50})
	if result.Applied != 150 {
		t.Fatalf("applied with 50%% reduction = %d, want 150", result.Applied)
	}
	if got := s.Health("tank"); got != 250 {
		t.Fatalf("health after reduced heal = %d, want 250", got)
	}
}

func TestApplyHealRespectsNoHeal(t *testing.T) {
	s := newSettlement(t, impl.Options{})
	mustSetHealth(t, s, "tank", 100, 1000)
	result := mustApply(t, s, impl.HealRequest{Target: "tank", Amount: 300, At: 0, NoHeal: true})
	if result.Applied != 0 {
		t.Fatalf("applied during no-heal = %d, want 0", result.Applied)
	}
	if result.Overheal != 0 {
		t.Fatalf("overheal during no-heal = %d, want 0", result.Overheal)
	}
	if got := s.Health("tank"); got != 100 {
		t.Fatalf("health during no-heal = %d, want 100", got)
	}
}

func TestOverhealBecomesShield(t *testing.T) {
	s := newSettlement(t, impl.Options{})
	mustSetHealth(t, s, "tank", 900, 1000)
	result := mustApply(t, s, impl.HealRequest{Target: "tank", Amount: 300, At: 0})
	if result.Applied != 100 {
		t.Fatalf("applied near cap = %d, want 100", result.Applied)
	}
	if result.Overheal != 200 {
		t.Fatalf("overheal = %d, want 200", result.Overheal)
	}
	if result.ShieldGained != 100 {
		t.Fatalf("shield gained = %d, want 100", result.ShieldGained)
	}
	if result.ShieldTotal != 100 {
		t.Fatalf("shield total = %d, want 100", result.ShieldTotal)
	}
	if got := s.Shield("tank").Amount; got != 100 {
		t.Fatalf("stored shield = %d, want 100", got)
	}
}

func TestOverhealAtFullHealthBecomesShield(t *testing.T) {
	s := newSettlement(t, impl.Options{})
	mustSetHealth(t, s, "tank", 1000, 1000)
	result := mustApply(t, s, impl.HealRequest{Target: "tank", Amount: 300, At: 0})
	if result.Applied != 0 {
		t.Fatalf("applied at full health = %d, want 0", result.Applied)
	}
	if result.Overheal != 300 {
		t.Fatalf("overheal at full health = %d, want 300", result.Overheal)
	}
	if result.ShieldGained != 150 {
		t.Fatalf("shield gained at full health = %d, want 150", result.ShieldGained)
	}
	if result.ShieldTotal != 150 {
		t.Fatalf("shield total at full health = %d, want 150", result.ShieldTotal)
	}
	if got := s.Shield("tank").Amount; got != 150 {
		t.Fatalf("stored shield at full health = %d, want 150", got)
	}
}

func TestApplyHealUnknownTargetRejected(t *testing.T) {
	s := newSettlement(t, impl.Options{})
	if _, err := s.ApplyHeal(impl.HealRequest{Target: "ghost", Amount: 100, At: 0}); !errors.Is(err, impl.ErrUnknownTarget) {
		t.Fatalf("unknown target error = %v, want ErrUnknownTarget", err)
	}
}

func TestAddHoTStoresEffects(t *testing.T) {
	s := newSettlement(t, impl.Options{})
	mustSetHealth(t, s, "tank", 500, 1000)
	if err := s.AddHoT("tank", impl.HoT{ID: "regen", PerTick: 100, Ticks: 3, IntervalMs: 1000, AppliedAt: 0}); err != nil {
		t.Fatalf("first AddHoT return error: %v", err)
	}
	if err := s.AddHoT("tank", impl.HoT{ID: "echo", PerTick: 50, Ticks: 2, IntervalMs: 500, AppliedAt: 0}); err != nil {
		t.Fatalf("second AddHoT return error: %v", err)
	}
	if got := s.HoTs("tank"); got != 2 {
		t.Fatalf("hot count = %d, want 2", got)
	}
}

func TestAddHoTRejectsOverCap(t *testing.T) {
	s := newSettlement(t, impl.Options{MaxHoTs: 1})
	mustSetHealth(t, s, "tank", 500, 1000)
	if err := s.AddHoT("tank", impl.HoT{ID: "regen", PerTick: 100, Ticks: 3, IntervalMs: 1000, AppliedAt: 0}); err != nil {
		t.Fatalf("first AddHoT return error: %v", err)
	}
	err := s.AddHoT("tank", impl.HoT{ID: "echo", PerTick: 50, Ticks: 2, IntervalMs: 500, AppliedAt: 0})
	if !errors.Is(err, impl.ErrHoTFull) {
		t.Fatalf("hot overflow error = %v, want ErrHoTFull", err)
	}
}

func TestTickHoTFiresOnInterval(t *testing.T) {
	s := newSettlement(t, impl.Options{})
	mustSetHealth(t, s, "tank", 100, 1000)
	if err := s.AddHoT("tank", impl.HoT{ID: "regen", PerTick: 100, Ticks: 3, IntervalMs: 1000, AppliedAt: 0}); err != nil {
		t.Fatalf("AddHoT return error: %v", err)
	}
	ticks, err := s.TickHoT("tank", 999)
	if err != nil {
		t.Fatalf("TickHoT(999) return error: %v", err)
	}
	if len(ticks) != 0 {
		t.Fatalf("ticks before interval = %d, want 0", len(ticks))
	}
	ticks, err = s.TickHoT("tank", 1000)
	if err != nil {
		t.Fatalf("TickHoT(1000) return error: %v", err)
	}
	if len(ticks) != 1 {
		t.Fatalf("ticks at interval = %d, want 1", len(ticks))
	}
	if ticks[0].At != 1000 {
		t.Fatalf("tick time = %d, want 1000", ticks[0].At)
	}
	if ticks[0].Amount != 100 {
		t.Fatalf("tick amount = %d, want 100", ticks[0].Amount)
	}
	if got := s.Health("tank"); got != 200 {
		t.Fatalf("health after one tick = %d, want 200", got)
	}
}

func TestTickHoTExpiresAfterAllTicks(t *testing.T) {
	s := newSettlement(t, impl.Options{})
	mustSetHealth(t, s, "tank", 100, 1000)
	if err := s.AddHoT("tank", impl.HoT{ID: "regen", PerTick: 100, Ticks: 3, IntervalMs: 1000, AppliedAt: 0}); err != nil {
		t.Fatalf("AddHoT return error: %v", err)
	}
	ticks, err := s.TickHoT("tank", 3000)
	if err != nil {
		t.Fatalf("TickHoT(3000) return error: %v", err)
	}
	if len(ticks) != 3 {
		t.Fatalf("ticks at 3000 = %d, want 3", len(ticks))
	}
	total := 0
	for _, tick := range ticks {
		total += tick.Amount
	}
	if total != 300 {
		t.Fatalf("total tick healing = %d, want 300", total)
	}
	if got := s.HoTs("tank"); got != 0 {
		t.Fatalf("hot count after expiry = %d, want 0", got)
	}
}

func TestExpireShieldsAtBoundary(t *testing.T) {
	s := newSettlement(t, impl.Options{})
	mustSetHealth(t, s, "tank", 900, 1000)
	if result := mustApply(t, s, impl.HealRequest{Target: "tank", Amount: 300, At: 0}); result.ShieldTotal != 100 {
		t.Fatalf("setup shield = %d, want 100", result.ShieldTotal)
	}
	if err := s.ExpireShields("tank", 9999); err != nil {
		t.Fatalf("ExpireShields before expiry return error: %v", err)
	}
	if got := s.Shield("tank").Amount; got != 100 {
		t.Fatalf("shield before expiry = %d, want 100", got)
	}
	if err := s.ExpireShields("tank", 10000); err != nil {
		t.Fatalf("ExpireShields at expiry return error: %v", err)
	}
	if got := s.Shield("tank").Amount; got != 0 {
		t.Fatalf("shield at expiry = %d, want 0", got)
	}
}

func TestSnapshotRestoreKeepsHealthAndShield(t *testing.T) {
	s := newSettlement(t, impl.Options{})
	mustSetHealth(t, s, "tank", 900, 1000)
	mustApply(t, s, impl.HealRequest{Target: "tank", Amount: 300, At: 0})
	data, err := s.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot return error: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("Snapshot returned empty payload")
	}
	restored := impl.NewHealingSettlement(impl.Options{})
	if err := restored.Restore(data); err != nil {
		t.Fatalf("Restore return error: %v", err)
	}
	if got := restored.Health("tank"); got != 1000 {
		t.Fatalf("restored health = %d, want 1000", got)
	}
	if got := restored.Shield("tank").Amount; got != 100 {
		t.Fatalf("restored shield = %d, want 100", got)
	}
}

func TestConcurrentHealsSerialize(t *testing.T) {
	s := newSettlement(t, impl.Options{})
	mustSetHealth(t, s, "tank", 0, 200000)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.ApplyHeal(impl.HealRequest{Target: "tank", Amount: 1000, At: 0}); err != nil {
				t.Errorf("concurrent ApplyHeal return error: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := s.Health("tank"); got != 10000 {
		t.Fatalf("health after concurrent heals = %d, want 10000", got)
	}
}
