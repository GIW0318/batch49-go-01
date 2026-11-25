package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch49-go-game-a-17"
)

func newFighterSelector() *impl.UtilitySelector {
	s := impl.NewUtilitySelector(impl.Options{CooldownMs: 500, HysteresisPercent: 10, PenaltyCap: 400, PenaltyStep: 50})
	_ = s.AddBehavior(impl.Behavior{ID: "attack", Var: "hp", Curve: impl.CurveLinear, InMin: 0, InMax: 100})
	_ = s.AddBehavior(impl.Behavior{ID: "reload", Var: "ammo", Curve: impl.CurveLinear, InMin: 0, InMax: 100})
	return s
}

func TestAddBehaviorValidatesSortsAndCommitUnknown(t *testing.T) {
	s := impl.NewUtilitySelector(impl.Options{})
	if err := s.AddBehavior(impl.Behavior{ID: "", Var: "hp", Curve: impl.CurveLinear, InMin: 0, InMax: 100}); !errors.Is(err, impl.ErrBadBehavior) {
		t.Fatalf("blank behavior id must be rejected with ErrBadBehavior, got %v", err)
	}
	if err := s.AddBehavior(impl.Behavior{ID: "bad", Var: "hp", Curve: "sigmoid", InMin: 0, InMax: 100}); !errors.Is(err, impl.ErrBadBehavior) {
		t.Fatalf("unknown curve kind must be rejected with ErrBadBehavior, got %v", err)
	}
	if err := s.AddBehavior(impl.Behavior{ID: "bad", Var: "", Curve: impl.CurveLinear, InMin: 0, InMax: 100}); !errors.Is(err, impl.ErrBadBehavior) {
		t.Fatalf("blank variable name must be rejected with ErrBadBehavior, got %v", err)
	}
	if err := s.AddBehavior(impl.Behavior{ID: "bad", Var: "hp", Curve: impl.CurveLinear, InMin: 100, InMax: 100}); !errors.Is(err, impl.ErrBadBehavior) {
		t.Fatalf("empty input span must be rejected with ErrBadBehavior, got %v", err)
	}
	for _, b := range []impl.Behavior{
		{ID: "guard", Var: "hp", Curve: impl.CurveLinear, InMin: 0, InMax: 100},
		{ID: "flank", Var: "hp", Curve: impl.CurveStep, InMin: 30},
	} {
		if err := s.AddBehavior(b); err != nil {
			t.Fatalf("add %s: %v", b.ID, err)
		}
	}
	list := s.Behaviors()
	if len(list) != 2 || list[0].ID != "flank" || list[1].ID != "guard" {
		t.Fatalf("behaviors = %+v, want flank then guard ordered by id", list)
	}
	if err := s.AddBehavior(impl.Behavior{ID: "guard", Var: "hp", Curve: impl.CurveStep, InMin: 1}); !errors.Is(err, impl.ErrDuplicateBehavior) {
		t.Fatalf("duplicate id must fail with ErrDuplicateBehavior, got %v", err)
	}
	if err := s.Commit("npc-1", "ghost", 0); !errors.Is(err, impl.ErrUnknownBehavior) {
		t.Fatalf("committing an unregistered behavior must fail with ErrUnknownBehavior, got %v", err)
	}
}

func TestScoreLinearCurveClamps(t *testing.T) {
	s := impl.NewUtilitySelector(impl.Options{})
	if err := s.AddBehavior(impl.Behavior{ID: "flee", Var: "threat", Curve: impl.CurveLinear, InMin: 0, InMax: 100}); err != nil {
		t.Fatalf("add behavior: %v", err)
	}
	for _, tc := range []struct {
		value int
		want  int
	}{{-20, 0}, {70, 700}, {150, 1000}} {
		got, err := s.Score("npc-1", "flee", impl.World{Vars: map[string]int{"threat": tc.value}})
		if err != nil {
			t.Fatalf("score with threat=%d: %v", tc.value, err)
		}
		if got != tc.want {
			t.Fatalf("score with threat=%d is %d, want %d", tc.value, got, tc.want)
		}
	}
}

func TestScoreStepCurve(t *testing.T) {
	s := impl.NewUtilitySelector(impl.Options{})
	if err := s.AddBehavior(impl.Behavior{ID: "ambush", Var: "range", Curve: impl.CurveStep, InMin: 50, InMax: 100}); err != nil {
		t.Fatalf("add behavior: %v", err)
	}
	for _, tc := range []struct {
		value int
		want  int
	}{{49, 0}, {50, 1000}, {99, 1000}} {
		got, err := s.Score("npc-1", "ambush", impl.World{Vars: map[string]int{"range": tc.value}})
		if err != nil {
			t.Fatalf("score with range=%d: %v", tc.value, err)
		}
		if got != tc.want {
			t.Fatalf("step score with range=%d is %d, want %d", tc.value, got, tc.want)
		}
	}
}

func TestScoreInverseSCurve(t *testing.T) {
	s := impl.NewUtilitySelector(impl.Options{})
	if err := s.AddBehavior(impl.Behavior{ID: "duel", Var: "range", Curve: impl.CurveInverseS, InMin: 0, InMax: 100}); err != nil {
		t.Fatalf("add behavior: %v", err)
	}
	for _, tc := range []struct {
		value int
		want  int
	}{{0, 0}, {25, 500}, {50, 1000}, {75, 500}, {100, 0}} {
		got, err := s.Score("npc-1", "duel", impl.World{Vars: map[string]int{"range": tc.value}})
		if err != nil {
			t.Fatalf("score with range=%d: %v", tc.value, err)
		}
		if got != tc.want {
			t.Fatalf("inverse_s score with range=%d is %d, want %d", tc.value, got, tc.want)
		}
	}
}

func TestScoreRejectsUnknownBehaviorAndAgent(t *testing.T) {
	s := newFighterSelector()
	if _, err := s.Score("npc-1", "ghost", impl.World{}); !errors.Is(err, impl.ErrUnknownBehavior) {
		t.Fatalf("scoring an unregistered behavior must fail with ErrUnknownBehavior, got %v", err)
	}
	if _, err := s.Score("   ", "attack", impl.World{}); !errors.Is(err, impl.ErrBlankAgent) {
		t.Fatalf("scoring for a blank agent must fail with ErrBlankAgent, got %v", err)
	}
}

func TestPenalizeLowersScoreAndCaps(t *testing.T) {
	s := impl.NewUtilitySelector(impl.Options{PenaltyCap: 400})
	if err := s.AddBehavior(impl.Behavior{ID: "attack", Var: "hp", Curve: impl.CurveLinear, InMin: 0, InMax: 100}); err != nil {
		t.Fatalf("add behavior: %v", err)
	}
	world := impl.World{Vars: map[string]int{"hp": 100}}
	base, err := s.Score("npc-1", "attack", world)
	if err != nil {
		t.Fatalf("base score: %v", err)
	}
	if base != 1000 {
		t.Fatalf("base score = %d, want 1000", base)
	}
	if err := s.Penalize("npc-1", "attack", 300); err != nil {
		t.Fatalf("penalize: %v", err)
	}
	got, err := s.Score("npc-1", "attack", world)
	if err != nil {
		t.Fatalf("score after penalty: %v", err)
	}
	if got != 700 {
		t.Fatalf("score after 300 penalty = %d, want 700", got)
	}
	if err := s.Penalize("npc-1", "attack", 500); err != nil {
		t.Fatalf("second penalize: %v", err)
	}
	got, err = s.Score("npc-1", "attack", world)
	if err != nil {
		t.Fatalf("score after capped penalty: %v", err)
	}
	if got != 600 {
		t.Fatalf("score after capping at 400 penalty = %d, want 600", got)
	}
}

func TestPenalizeRejectsNonPositiveAmount(t *testing.T) {
	s := newFighterSelector()
	if err := s.Penalize("npc-1", "attack", 0); !errors.Is(err, impl.ErrBadAmount) {
		t.Fatalf("zero amount must fail with ErrBadAmount, got %v", err)
	}
	if err := s.Penalize("npc-1", "attack", -5); !errors.Is(err, impl.ErrBadAmount) {
		t.Fatalf("negative amount must fail with ErrBadAmount, got %v", err)
	}
	got, err := s.Score("npc-1", "attack", impl.World{Vars: map[string]int{"hp": 100}})
	if err != nil {
		t.Fatalf("score: %v", err)
	}
	if got != 1000 {
		t.Fatalf("rejected penalties must not change the score, got %d", got)
	}
}

func TestSelectPicksHighestInitially(t *testing.T) {
	s := newFighterSelector()
	sel, err := s.Select("npc-1", impl.World{Vars: map[string]int{"hp": 90, "ammo": 80}}, 1000)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if sel.Behavior != "attack" || sel.Score != 900 {
		t.Fatalf("selection = %+v, want attack at 900", sel)
	}
	if !sel.Switch || sel.Reason != impl.ReasonInitial {
		t.Fatalf("first selection must switch with reason %q, got switch=%v reason=%q", impl.ReasonInitial, sel.Switch, sel.Reason)
	}
	if current, ok := s.Current("npc-1"); ok {
		t.Fatalf("select must not commit a behavior, current=%q", current)
	}
}

func TestSelectHonorsCooldown(t *testing.T) {
	s := newFighterSelector()
	if err := s.Commit("npc-1", "attack", 0); err != nil {
		t.Fatalf("commit: %v", err)
	}
	sel, err := s.Select("npc-1", impl.World{Vars: map[string]int{"hp": 90, "ammo": 80}}, 100)
	if err != nil {
		t.Fatalf("select during cooldown: %v", err)
	}
	if sel.Behavior != "reload" {
		t.Fatalf("selection during cooldown = %q, want reload", sel.Behavior)
	}
	if !sel.Switch || sel.Reason != impl.ReasonCooldown {
		t.Fatalf("selection = %+v, want a forced switch with reason %q", sel, impl.ReasonCooldown)
	}
	if got := s.CooldownUntil("npc-1", "attack"); got != 500 {
		t.Fatalf("cooldown deadline = %d, want 500", got)
	}
}

func TestSelectHoldsUnderHysteresis(t *testing.T) {
	s := impl.NewUtilitySelector(impl.Options{HysteresisPercent: 10, PenaltyCap: 400})
	_ = s.AddBehavior(impl.Behavior{ID: "attack", Var: "hp", Curve: impl.CurveLinear, InMin: 0, InMax: 100})
	_ = s.AddBehavior(impl.Behavior{ID: "reload", Var: "ammo", Curve: impl.CurveLinear, InMin: 0, InMax: 100})
	if err := s.Commit("npc-1", "attack", 0); err != nil {
		t.Fatalf("commit: %v", err)
	}
	sel, err := s.Select("npc-1", impl.World{Vars: map[string]int{"hp": 90, "ammo": 80}}, 100)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if sel.Behavior != "attack" {
		t.Fatalf("selection = %q, want the current attack to be kept", sel.Behavior)
	}
	if sel.Switch || sel.Reason != impl.ReasonHold {
		t.Fatalf("selection = %+v, want hold without switching", sel)
	}
	if sel.Score != 900 {
		t.Fatalf("held score = %d, want the current behavior score 900", sel.Score)
	}
}

func TestSelectSwitchesPastHysteresis(t *testing.T) {
	s := impl.NewUtilitySelector(impl.Options{HysteresisPercent: 5, PenaltyCap: 400})
	_ = s.AddBehavior(impl.Behavior{ID: "attack", Var: "hp", Curve: impl.CurveLinear, InMin: 0, InMax: 100})
	_ = s.AddBehavior(impl.Behavior{ID: "reload", Var: "ammo", Curve: impl.CurveLinear, InMin: 0, InMax: 100})
	if err := s.Commit("npc-1", "attack", 0); err != nil {
		t.Fatalf("commit: %v", err)
	}
	sel, err := s.Select("npc-1", impl.World{Vars: map[string]int{"hp": 90, "ammo": 88}}, 100)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if sel.Behavior != "reload" || sel.Score != 880 {
		t.Fatalf("selection = %+v, want reload at 880", sel)
	}
	if !sel.Switch || sel.Reason != impl.ReasonSwitch {
		t.Fatalf("selection = %+v, want a switch", sel)
	}
}

func TestSelectErrorCases(t *testing.T) {
	empty := impl.NewUtilitySelector(impl.Options{})
	if _, err := empty.Select("npc-1", impl.World{}, 0); !errors.Is(err, impl.ErrNoBehaviors) {
		t.Fatalf("selecting without behaviors must fail with ErrNoBehaviors, got %v", err)
	}
	s := newFighterSelector()
	if _, err := s.Select("  ", impl.World{}, 0); !errors.Is(err, impl.ErrBlankAgent) {
		t.Fatalf("selecting for a blank agent must fail with ErrBlankAgent, got %v", err)
	}
	if err := s.Commit("npc-1", "attack", 0); err != nil {
		t.Fatalf("commit attack: %v", err)
	}
	if err := s.Commit("npc-1", "reload", 0); err != nil {
		t.Fatalf("commit reload: %v", err)
	}
	if _, err := s.Select("npc-1", impl.World{Vars: map[string]int{"hp": 90, "ammo": 80}}, 499); !errors.Is(err, impl.ErrNoCandidate) {
		t.Fatalf("selecting with every behavior on cooldown must fail with ErrNoCandidate, got %v", err)
	}
}

func TestConcurrentScoresAgree(t *testing.T) {
	s := newFighterSelector()
	world := impl.World{Vars: map[string]int{"hp": 70}}
	var wg sync.WaitGroup
	var mu sync.Mutex
	got := make([]int, 0, 8)
	failures := make([]error, 0, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			score, err := s.Score("npc-race", "attack", world)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failures = append(failures, err)
				return
			}
			got = append(got, score)
		}()
	}
	wg.Wait()
	if len(failures) != 0 {
		t.Fatalf("concurrent scoring failed: %v", failures)
	}
	if len(got) != 8 {
		t.Fatalf("collected %d scores, want 8", len(got))
	}
	for _, score := range got {
		if score != 700 {
			t.Fatalf("concurrent score = %d, want 700 for hp=70", score)
		}
	}
}

func TestSnapshotRestoreKeepsCooldownAndPenalty(t *testing.T) {
	s := newFighterSelector()
	if err := s.Commit("npc-1", "attack", 1000); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := s.Penalize("npc-1", "attack", 200); err != nil {
		t.Fatalf("penalize: %v", err)
	}
	data, err := s.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewUtilitySelector(impl.Options{CooldownMs: 500, HysteresisPercent: 10, PenaltyCap: 400, PenaltyStep: 50})
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if list := restored.Behaviors(); len(list) != 2 {
		t.Fatalf("restored behavior count = %d, want 2", len(list))
	}
	current, ok := restored.Current("npc-1")
	if !ok || current != "attack" {
		t.Fatalf("restored current = %q ok=%v, want attack", current, ok)
	}
	if got := restored.CooldownUntil("npc-1", "attack"); got != 1500 {
		t.Fatalf("restored cooldown deadline = %d, want 1500", got)
	}
	score, err := restored.Score("npc-1", "attack", impl.World{Vars: map[string]int{"hp": 100}})
	if err != nil {
		t.Fatalf("score after restore: %v", err)
	}
	if score != 800 {
		t.Fatalf("restored score = %d, want 1000-200 = 800", score)
	}
}

func TestRestoreRejectsCorruptPayload(t *testing.T) {
	s := newFighterSelector()
	if err := s.Commit("npc-1", "attack", 1000); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := s.Restore([]byte("{broken-payload")); !errors.Is(err, impl.ErrBadPayload) {
		t.Fatalf("corrupt snapshot must fail with ErrBadPayload, got %v", err)
	}
	current, ok := s.Current("npc-1")
	if !ok || current != "attack" {
		t.Fatalf("a rejected restore touched live state: current=%q ok=%v", current, ok)
	}
	if got := s.CooldownUntil("npc-1", "attack"); got != 1500 {
		t.Fatalf("live cooldown deadline = %d, want 1500", got)
	}
}
