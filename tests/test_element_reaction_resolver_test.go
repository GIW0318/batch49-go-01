package task

import (
	"errors"
	"testing"

	impl "gsb/batch49-go-game-a-12"
)

func resolverOptions() impl.Options {
	return impl.Options{MaxTargets: 32, MaxAuras: 2, DefaultGauge: 100, DefaultICDFrames: 12}
}

func TestAttachRejectsUnknownElement(t *testing.T) {
	r := impl.NewReactionResolver(resolverOptions())
	if err := r.Attach("dummy", "fire", 10, 0); !errors.Is(err, impl.ErrUnknownElement) {
		t.Fatalf("unknown element must be rejected, got %v", err)
	}
	if err := r.Attach("dummy", "pyro", 10, 0); err != nil {
		t.Fatalf("known element must be accepted, got %v", err)
	}
}

func TestAttachRejectsBadInput(t *testing.T) {
	r := impl.NewReactionResolver(resolverOptions())
	if err := r.Attach("dummy", "pyro", 0, 0); !errors.Is(err, impl.ErrBadGauge) {
		t.Fatalf("zero gauge must be rejected, got %v", err)
	}
	if err := r.Attach("   ", "pyro", 10, 0); !errors.Is(err, impl.ErrBlankTarget) {
		t.Fatalf("blank target must be rejected, got %v", err)
	}
	if err := r.Attach("dummy", "pyro", 10, -1); !errors.Is(err, impl.ErrBadTime) {
		t.Fatalf("negative time must be rejected, got %v", err)
	}
}

func TestAuraReturnsAttachedGauge(t *testing.T) {
	r := impl.NewReactionResolver(resolverOptions())
	if err := r.Attach("dummy", "pyro", 80, 1000); err != nil {
		t.Fatalf("attach failed: %v", err)
	}
	auras := r.Aura("dummy", 1000)
	if len(auras) != 1 {
		t.Fatalf("auras = %v, want one pyro aura", auras)
	}
	if auras[0].Element != "pyro" || auras[0].Gauge != 80 {
		t.Fatalf("aura = %+v, want pyro 80", auras[0])
	}
}

func TestAuraDecaysByElementRate(t *testing.T) {
	r := impl.NewReactionResolver(resolverOptions())
	if err := r.SetDecay("pyro", 10); err != nil {
		t.Fatalf("set decay failed: %v", err)
	}
	if err := r.Attach("dummy", "pyro", 80, 0); err != nil {
		t.Fatalf("attach failed: %v", err)
	}
	auras := r.Aura("dummy", 500)
	if len(auras) != 1 {
		t.Fatalf("auras = %v, want one aura after 500ms", auras)
	}
	if auras[0].Gauge != 30 {
		t.Fatalf("gauge after 500ms = %d, want 30", auras[0].Gauge)
	}
	if rate := r.DecayOf("pyro"); rate != 10 {
		t.Fatalf("default pyro decay = %d, want 10", rate)
	}
}

func TestAuraDisappearsWhenFullyDecayed(t *testing.T) {
	r := impl.NewReactionResolver(resolverOptions())
	if err := r.Attach("dummy", "pyro", 80, 0); err != nil {
		t.Fatalf("attach failed: %v", err)
	}
	if auras := r.Aura("dummy", 5000); len(auras) != 0 {
		t.Fatalf("auras = %v, want none after full decay", auras)
	}
}

func TestAuraSortedByElementName(t *testing.T) {
	r := impl.NewReactionResolver(resolverOptions())
	if err := r.Attach("dummy", "pyro", 80, 0); err != nil {
		t.Fatalf("attach pyro failed: %v", err)
	}
	if err := r.Attach("dummy", "hydro", 60, 0); err != nil {
		t.Fatalf("attach hydro failed: %v", err)
	}
	auras := r.Aura("dummy", 0)
	if len(auras) != 2 {
		t.Fatalf("auras = %v, want two auras", auras)
	}
	if auras[0].Element != "hydro" || auras[1].Element != "pyro" {
		t.Fatalf("aura order = [%s %s], want [hydro pyro]", auras[0].Element, auras[1].Element)
	}
}

func TestAttachEvictsWeakestAura(t *testing.T) {
	r := impl.NewReactionResolver(resolverOptions())
	if err := r.Attach("dummy", "pyro", 80, 0); err != nil {
		t.Fatalf("attach pyro failed: %v", err)
	}
	if err := r.Attach("dummy", "hydro", 50, 0); err != nil {
		t.Fatalf("attach hydro failed: %v", err)
	}
	if err := r.Attach("dummy", "cryo", 90, 0); err != nil {
		t.Fatalf("attach cryo failed: %v", err)
	}
	auras := r.Aura("dummy", 0)
	if len(auras) != 2 {
		t.Fatalf("auras = %v, want two auras at the limit", auras)
	}
	if auras[0].Element != "cryo" || auras[1].Element != "pyro" {
		t.Fatalf("auras = %v, want the weakest hydro aura evicted", auras)
	}
}

func TestReactProducesVaporizeDamage(t *testing.T) {
	r := impl.NewReactionResolver(resolverOptions())
	if err := r.Attach("dummy", "hydro", 60, 0); err != nil {
		t.Fatalf("attach failed: %v", err)
	}
	res, err := r.React(impl.ReactHit{SkillID: "skill-a", Target: "dummy", Element: "pyro", Gauge: 40, Damage: 100, At: 0})
	if err != nil {
		t.Fatalf("react failed: %v", err)
	}
	if res.Reaction != impl.ReactionVaporize {
		t.Fatalf("reaction = %q, want %q", res.Reaction, impl.ReactionVaporize)
	}
	if res.Element != "hydro" {
		t.Fatalf("consumed element = %q, want hydro", res.Element)
	}
	if res.Consumed != 40 {
		t.Fatalf("consumed = %d, want 40", res.Consumed)
	}
	if res.Factor != 200 {
		t.Fatalf("factor = %d, want 200", res.Factor)
	}
	if res.Damage != 200 {
		t.Fatalf("damage = %d, want 200", res.Damage)
	}
	auras := r.Aura("dummy", 0)
	if len(auras) != 1 || auras[0].Gauge != 20 {
		t.Fatalf("aura after reaction = %v, want hydro 20", auras)
	}
}

func TestReactConsumesAvailableGauge(t *testing.T) {
	r := impl.NewReactionResolver(resolverOptions())
	if err := r.Attach("dummy", "hydro", 20, 0); err != nil {
		t.Fatalf("attach failed: %v", err)
	}
	res, err := r.React(impl.ReactHit{SkillID: "skill-b", Target: "dummy", Element: "pyro", Gauge: 50, Damage: 10, At: 0})
	if err != nil {
		t.Fatalf("react failed: %v", err)
	}
	if res.Consumed != 20 {
		t.Fatalf("consumed = %d, want the available 20", res.Consumed)
	}
	if auras := r.Aura("dummy", 0); len(auras) != 0 {
		t.Fatalf("aura after reaction = %v, want none", auras)
	}
}

func TestReactWithoutAuraProducesNothing(t *testing.T) {
	r := impl.NewReactionResolver(resolverOptions())
	if err := r.Attach("dummy", "hydro", 10, 0); err != nil {
		t.Fatalf("attach failed: %v", err)
	}
	res, err := r.React(impl.ReactHit{SkillID: "skill-c", Target: "dummy", Element: "pyro", Gauge: 50, Damage: 100, At: 5000})
	if err != nil {
		t.Fatalf("react without a live aura must not fail: %v", err)
	}
	if res.Reaction != "" {
		t.Fatalf("reaction = %q, want no reaction", res.Reaction)
	}
	if res.Damage != 0 {
		t.Fatalf("damage = %d, want 0", res.Damage)
	}
}

func TestReactionPriorityIsFixed(t *testing.T) {
	want := []string{
		impl.ReactionVaporize,
		impl.ReactionMelt,
		impl.ReactionOverload,
		impl.ReactionFreeze,
		impl.ReactionElectrocharged,
		impl.ReactionSuperconduct,
	}
	got := impl.ReactionPriority()
	if len(got) != len(want) {
		t.Fatalf("priority = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("priority[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestReactICDSuppressesHit(t *testing.T) {
	r := impl.NewReactionResolver(resolverOptions())
	if err := r.SetICD("skill-a", 3); err != nil {
		t.Fatalf("set icd failed: %v", err)
	}
	if err := r.Attach("dummy", "hydro", 200, 0); err != nil {
		t.Fatalf("attach failed: %v", err)
	}
	first, err := r.React(impl.ReactHit{SkillID: "skill-a", Target: "dummy", Element: "pyro", Gauge: 10, Damage: 100, At: 0})
	if err != nil {
		t.Fatalf("first react failed: %v", err)
	}
	if first.Suppressed {
		t.Fatalf("first hit must not be suppressed")
	}
	second, err := r.React(impl.ReactHit{SkillID: "skill-a", Target: "dummy", Element: "pyro", Gauge: 10, Damage: 100, At: 100})
	if err != nil {
		t.Fatalf("second react failed: %v", err)
	}
	if !second.Suppressed {
		t.Fatalf("second hit inside the cooldown window must be suppressed")
	}
	if second.Reaction != "" {
		t.Fatalf("suppressed hit produced reaction %q", second.Reaction)
	}
	if count := r.SuppressedCount("dummy"); count != 1 {
		t.Fatalf("suppressed count = %d, want 1", count)
	}
}

func TestSetICDAndDecayValidation(t *testing.T) {
	r := impl.NewReactionResolver(resolverOptions())
	if err := r.SetICD("skill-a", -1); !errors.Is(err, impl.ErrBadFrames) {
		t.Fatalf("negative frames must be rejected, got %v", err)
	}
	if window := r.ICDWindow("never-configured"); window != impl.DefaultICDFrames {
		t.Fatalf("default window = %d, want %d", window, impl.DefaultICDFrames)
	}
	if err := r.SetDecay("pyro", 0); !errors.Is(err, impl.ErrBadDecay) {
		t.Fatalf("zero decay must be rejected, got %v", err)
	}
	if err := r.SetDecay("water", 5); !errors.Is(err, impl.ErrUnknownElement) {
		t.Fatalf("unknown decay element must be rejected, got %v", err)
	}
	if err := r.SetDecay("pyro", 20); err != nil {
		t.Fatalf("valid decay update failed: %v", err)
	}
	if rate := r.DecayOf("pyro"); rate != 20 {
		t.Fatalf("updated decay = %d, want 20", rate)
	}
}

func TestClearRemovesTarget(t *testing.T) {
	r := impl.NewReactionResolver(resolverOptions())
	if err := r.Attach("dummy", "pyro", 80, 0); err != nil {
		t.Fatalf("attach failed: %v", err)
	}
	if err := r.Clear("dummy"); err != nil {
		t.Fatalf("clear failed: %v", err)
	}
	if auras := r.Aura("dummy", 0); len(auras) != 0 {
		t.Fatalf("auras after clear = %v, want none", auras)
	}
	if err := r.Clear("dummy"); err != nil {
		t.Fatalf("clearing an empty target must be idempotent, got %v", err)
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	r := impl.NewReactionResolver(resolverOptions())
	if err := r.Attach("dummy", "hydro", 80, 0); err != nil {
		t.Fatalf("attach failed: %v", err)
	}
	if _, err := r.React(impl.ReactHit{SkillID: "skill-a", Target: "dummy", Element: "pyro", Gauge: 30, Damage: 100, At: 0}); err != nil {
		t.Fatalf("react failed: %v", err)
	}
	if err := r.SetICD("skill-a", 5); err != nil {
		t.Fatalf("set icd failed: %v", err)
	}
	snap, err := r.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	restored := impl.NewReactionResolver(resolverOptions())
	if err := restored.Restore(snap); err != nil {
		t.Fatalf("restore failed: %v", err)
	}
	auras := restored.Aura("dummy", 0)
	if len(auras) != 1 || auras[0].Element != "hydro" || auras[0].Gauge != 50 {
		t.Fatalf("restored auras = %v, want hydro 50", auras)
	}
	if window := restored.ICDWindow("skill-a"); window != 5 {
		t.Fatalf("restored window = %d, want 5", window)
	}
	if count := restored.SuppressedCount("dummy"); count != 0 {
		t.Fatalf("restored suppressed count = %d, want 0", count)
	}
	if err := restored.Restore([]byte("[broken")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("broken snapshot must be rejected, got %v", err)
	}
}
