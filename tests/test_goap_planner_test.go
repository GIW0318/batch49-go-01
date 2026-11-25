package task

import (
	"errors"
	"reflect"
	"sync"
	"testing"

	impl "gsb/batch49-go-game-a-16"
)

func newChainPlanner() *impl.GoapPlanner {
	p := impl.NewGoapPlanner(impl.Options{MaxDepth: 4})
	_ = p.AddAction(impl.Action{ID: "gather", Cost: 3, Effect: impl.State{"wood": true}})
	_ = p.AddAction(impl.Action{ID: "saw", Cost: 4, Pre: impl.State{"wood": true}, Effect: impl.State{"plank": true}})
	return p
}

func TestAddActionRejectsBadDefinitions(t *testing.T) {
	p := impl.NewGoapPlanner(impl.Options{MaxDepth: 4})
	if err := p.AddAction(impl.Action{ID: "", Cost: 1, Effect: impl.State{"a": true}}); !errors.Is(err, impl.ErrBadAction) {
		t.Fatalf("blank action id must be rejected with ErrBadAction, got %v", err)
	}
	if err := p.AddAction(impl.Action{ID: "neg", Cost: -2, Effect: impl.State{"a": true}}); !errors.Is(err, impl.ErrBadAction) {
		t.Fatalf("negative cost must be rejected with ErrBadAction, got %v", err)
	}
	if err := p.AddAction(impl.Action{ID: "empty", Cost: 1}); !errors.Is(err, impl.ErrBadAction) {
		t.Fatalf("an action without effects must be rejected with ErrBadAction, got %v", err)
	}
	if got := len(p.Actions()); got != 0 {
		t.Fatalf("rejected actions must not be registered, got %d", got)
	}
}

func TestAddActionKeepsSortedRegistry(t *testing.T) {
	p := impl.NewGoapPlanner(impl.Options{MaxDepth: 4})
	for _, spec := range []struct {
		id   string
		cost int
	}{{"chop", 2}, {"build", 5}, {"haul", 1}} {
		if err := p.AddAction(impl.Action{ID: spec.id, Cost: spec.cost, Effect: impl.State{spec.id: true}}); err != nil {
			t.Fatalf("add %s: %v", spec.id, err)
		}
	}
	got := p.Actions()
	want := []string{"build", "chop", "haul"}
	if len(got) != len(want) {
		t.Fatalf("registered %d actions, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i] {
			t.Fatalf("action %d is %q, want %q (registry must be ordered by id)", i, got[i].ID, want[i])
		}
	}
	if err := p.AddAction(impl.Action{ID: "chop", Cost: 9, Effect: impl.State{"chop": true}}); !errors.Is(err, impl.ErrDuplicateAction) {
		t.Fatalf("duplicate id must fail with ErrDuplicateAction, got %v", err)
	}
	if len(p.Actions()) != 3 {
		t.Fatalf("duplicate add must not grow the registry")
	}
}

func TestSetGoalRequiresAgentAndStoresCopy(t *testing.T) {
	p := impl.NewGoapPlanner(impl.Options{MaxDepth: 4})
	if err := p.SetGoal("   ", "raid", impl.State{"armed": true}); !errors.Is(err, impl.ErrBlankAgent) {
		t.Fatalf("blank agent must be rejected with ErrBlankAgent, got %v", err)
	}
	if _, ok := p.Goal("npc-1"); ok {
		t.Fatalf("an agent without a goal must report no goal")
	}
	desired := impl.State{"armed": true}
	if err := p.SetGoal("npc-1", "raid", desired); err != nil {
		t.Fatalf("set goal: %v", err)
	}
	desired["armed"] = false
	got, ok := p.Goal("npc-1")
	if !ok {
		t.Fatalf("goal must be readable after SetGoal")
	}
	if !got["armed"] {
		t.Fatalf("SetGoal must copy the desired facts, caller mutation leaked in: %v", got)
	}
}

func TestPlanSingleAction(t *testing.T) {
	p := impl.NewGoapPlanner(impl.Options{MaxDepth: 4})
	if err := p.AddAction(impl.Action{ID: "gather", Cost: 3, Effect: impl.State{"wood": true}}); err != nil {
		t.Fatalf("add action: %v", err)
	}
	if err := p.SetGoal("npc-1", "stock", impl.State{"wood": true}); err != nil {
		t.Fatalf("set goal: %v", err)
	}
	plan, err := p.Plan("npc-1", impl.State{})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.Goal != "stock" || plan.Agent != "npc-1" {
		t.Fatalf("plan identity = %q/%q, want stock/npc-1", plan.Goal, plan.Agent)
	}
	if len(plan.Actions) != 1 || plan.Actions[0] != "gather" {
		t.Fatalf("plan actions = %v, want [gather]", plan.Actions)
	}
	if plan.Cost != 3 || plan.Steps != 1 {
		t.Fatalf("plan cost/steps = %d/%d, want 3/1", plan.Cost, plan.Steps)
	}
	if got := p.Cursor("npc-1"); got != 0 {
		t.Fatalf("a fresh plan must start at cursor 0, got %d", got)
	}
}

func TestPlanChainsPreconditions(t *testing.T) {
	p := newChainPlanner()
	if err := p.SetGoal("npc-1", "bench", impl.State{"plank": true}); err != nil {
		t.Fatalf("set goal: %v", err)
	}
	plan, err := p.Plan("npc-1", impl.State{})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Actions) != 2 || plan.Actions[0] != "gather" || plan.Actions[1] != "saw" {
		t.Fatalf("plan actions = %v, want [gather saw]", plan.Actions)
	}
	if plan.Cost != 7 || plan.Steps != 2 {
		t.Fatalf("plan cost/steps = %d/%d, want 7/2", plan.Cost, plan.Steps)
	}
	if _, err := p.Plan("npc-404", impl.State{}); !errors.Is(err, impl.ErrNoGoal) {
		t.Fatalf("planning for an agent without a goal must fail with ErrNoGoal, got %v", err)
	}
}

func TestPlanPrefersCheaperSequence(t *testing.T) {
	p := impl.NewGoapPlanner(impl.Options{MaxDepth: 4})
	actions := []impl.Action{
		{ID: "hunt", Cost: 9, Effect: impl.State{"food": true}},
		{ID: "forage", Cost: 2, Effect: impl.State{"fruit": true}},
		{ID: "cook", Cost: 2, Pre: impl.State{"fruit": true}, Effect: impl.State{"food": true}},
	}
	for _, a := range actions {
		if err := p.AddAction(a); err != nil {
			t.Fatalf("add %s: %v", a.ID, err)
		}
	}
	if err := p.SetGoal("npc-2", "eat", impl.State{"food": true}); err != nil {
		t.Fatalf("set goal: %v", err)
	}
	plan, err := p.Plan("npc-2", impl.State{})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.Cost != 4 {
		t.Fatalf("plan cost = %d, want the cheaper total 4", plan.Cost)
	}
	if plan.Steps != 2 || len(plan.Actions) != 2 || plan.Actions[0] != "forage" || plan.Actions[1] != "cook" {
		t.Fatalf("plan = %v, want the two step forage/cook route", plan.Actions)
	}
}

func TestPlanIsDeterministicAndCaches(t *testing.T) {
	p := newChainPlanner()
	if err := p.SetGoal("npc-1", "bench", impl.State{"plank": true}); err != nil {
		t.Fatalf("set goal: %v", err)
	}
	first, err := p.Plan("npc-1", impl.State{"wood": false})
	if err != nil {
		t.Fatalf("first plan: %v", err)
	}
	second, err := p.Plan("npc-1", impl.State{"wood": false})
	if err != nil {
		t.Fatalf("second plan: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("two identical requests returned different plans: %+v vs %+v", first, second)
	}
	hits, misses := p.CacheStats()
	if misses < 1 {
		t.Fatalf("cache misses = %d, want at least one miss for the first request", misses)
	}
	if hits < 1 {
		t.Fatalf("cache hits = %d, want a hit for the repeated request", hits)
	}
}

func TestPlanRespectsMaxDepth(t *testing.T) {
	p := impl.NewGoapPlanner(impl.Options{MaxDepth: 1})
	if err := p.AddAction(impl.Action{ID: "gather", Cost: 3, Effect: impl.State{"wood": true}}); err != nil {
		t.Fatalf("add gather: %v", err)
	}
	if err := p.AddAction(impl.Action{ID: "saw", Cost: 4, Pre: impl.State{"wood": true}, Effect: impl.State{"plank": true}}); err != nil {
		t.Fatalf("add saw: %v", err)
	}
	if err := p.SetGoal("npc-3", "bench", impl.State{"plank": true}); err != nil {
		t.Fatalf("set goal: %v", err)
	}
	if _, err := p.Plan("npc-3", impl.State{}); !errors.Is(err, impl.ErrNoPlan) {
		t.Fatalf("a depth cap of one must make the two step goal unreachable, got %v", err)
	}
}

func TestExecuteRunsStepsInOrder(t *testing.T) {
	p := newChainPlanner()
	if err := p.SetGoal("npc-1", "bench", impl.State{"plank": true}); err != nil {
		t.Fatalf("set goal: %v", err)
	}
	if _, err := p.Plan("npc-1", impl.State{}); err != nil {
		t.Fatalf("plan: %v", err)
	}
	first, err := p.Execute("npc-1", 1000)
	if err != nil {
		t.Fatalf("first execute: %v", err)
	}
	if first.Action != "gather" || first.Index != 1 || first.Remaining != 1 || first.Done {
		t.Fatalf("first result = %+v, want gather index 1 remaining 1 not done", first)
	}
	if got := p.Cursor("npc-1"); got != 1 {
		t.Fatalf("cursor after one step = %d, want 1", got)
	}
	second, err := p.Execute("npc-1", 2000)
	if err != nil {
		t.Fatalf("second execute: %v", err)
	}
	if second.Action != "saw" || second.Index != 2 || second.Remaining != 0 || !second.Done {
		t.Fatalf("second result = %+v, want saw index 2 remaining 0 done", second)
	}
	if _, err := p.Execute("npc-1", 3000); !errors.Is(err, impl.ErrPlanDone) {
		t.Fatalf("executing past the last step must fail with ErrPlanDone, got %v", err)
	}
}

func TestInvalidateClearsPlanAndCursor(t *testing.T) {
	p := newChainPlanner()
	if err := p.SetGoal("npc-1", "bench", impl.State{"plank": true}); err != nil {
		t.Fatalf("set goal: %v", err)
	}
	if _, err := p.Plan("npc-1", impl.State{}); err != nil {
		t.Fatalf("plan: %v", err)
	}
	if _, err := p.Execute("npc-1", 1000); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if err := p.Invalidate("npc-1"); err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	if got := p.Cursor("npc-1"); got != 0 {
		t.Fatalf("cursor after invalidate = %d, want 0", got)
	}
	if _, err := p.Execute("npc-1", 2000); !errors.Is(err, impl.ErrNoPlan) {
		t.Fatalf("execute after invalidate must fail with ErrNoPlan, got %v", err)
	}
	if _, err := p.Plan("npc-1", impl.State{}); err != nil {
		t.Fatalf("re-plan after invalidate: %v", err)
	}
	again, err := p.Execute("npc-1", 3000)
	if err != nil {
		t.Fatalf("execute after re-plan: %v", err)
	}
	if again.Index != 1 || again.Action != "gather" {
		t.Fatalf("re-planned execution = %+v, want step 1 gather", again)
	}
}

func TestExecuteWithoutPlanFails(t *testing.T) {
	p := newChainPlanner()
	if _, err := p.Execute("npc-cold", 100); !errors.Is(err, impl.ErrNoPlan) {
		t.Fatalf("execute without a plan must fail with ErrNoPlan, got %v", err)
	}
}

func TestExecuteRejectsBackwardsTime(t *testing.T) {
	p := newChainPlanner()
	if err := p.SetGoal("npc-1", "bench", impl.State{"plank": true}); err != nil {
		t.Fatalf("set goal: %v", err)
	}
	if _, err := p.Plan("npc-1", impl.State{}); err != nil {
		t.Fatalf("plan: %v", err)
	}
	if _, err := p.Execute("npc-1", 5000); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := p.Execute("npc-1", 4000); !errors.Is(err, impl.ErrBadTime) {
		t.Fatalf("a backwards timestamp must fail with ErrBadTime, got %v", err)
	}
	if got := p.Cursor("npc-1"); got != 1 {
		t.Fatalf("a rejected execute changed the cursor to %d, want 1", got)
	}
	next, err := p.Execute("npc-1", 6000)
	if err != nil {
		t.Fatalf("execute after the rejected call: %v", err)
	}
	if next.Index != 2 || next.Action != "saw" {
		t.Fatalf("next result = %+v, want step 2 saw", next)
	}
}

func TestConcurrentExecuteRunsEachStepOnce(t *testing.T) {
	p := impl.NewGoapPlanner(impl.Options{MaxDepth: 4})
	if err := p.AddAction(impl.Action{ID: "step-a", Cost: 1, Effect: impl.State{"s1": true}}); err != nil {
		t.Fatalf("add step-a: %v", err)
	}
	if err := p.AddAction(impl.Action{ID: "step-b", Cost: 1, Pre: impl.State{"s1": true}, Effect: impl.State{"s2": true}}); err != nil {
		t.Fatalf("add step-b: %v", err)
	}
	if err := p.AddAction(impl.Action{ID: "step-c", Cost: 1, Pre: impl.State{"s2": true}, Effect: impl.State{"s3": true}}); err != nil {
		t.Fatalf("add step-c: %v", err)
	}
	if err := p.SetGoal("npc-race", "finish", impl.State{"s3": true}); err != nil {
		t.Fatalf("set goal: %v", err)
	}
	plan, err := p.Plan("npc-race", impl.State{})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.Steps != 3 {
		t.Fatalf("plan steps = %d, want 3", plan.Steps)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	indexes := make([]int, 0, 3)
	failures := make([]error, 0, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := p.Execute("npc-race", int64(1000*(i+1)))
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failures = append(failures, err)
				return
			}
			indexes = append(indexes, res.Index)
		}(i)
	}
	wg.Wait()
	if len(failures) != 0 {
		t.Fatalf("concurrent execute failed: %v", failures)
	}
	seen := map[int]bool{}
	for _, idx := range indexes {
		seen[idx] = true
	}
	for idx := 1; idx <= 3; idx++ {
		if !seen[idx] {
			t.Fatalf("step index %d was never executed, got %v", idx, indexes)
		}
	}
}

func TestSnapshotRestoreKeepsPlanAndCursor(t *testing.T) {
	p := newChainPlanner()
	if err := p.SetGoal("npc-1", "bench", impl.State{"plank": true}); err != nil {
		t.Fatalf("set goal: %v", err)
	}
	if _, err := p.Plan("npc-1", impl.State{}); err != nil {
		t.Fatalf("plan: %v", err)
	}
	if _, err := p.Execute("npc-1", 1000); err != nil {
		t.Fatalf("execute: %v", err)
	}
	data, err := p.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewGoapPlanner(impl.Options{MaxDepth: 4})
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if err := restored.AddAction(impl.Action{ID: "gather", Cost: 3, Effect: impl.State{"wood": true}}); !errors.Is(err, impl.ErrDuplicateAction) {
		t.Fatalf("restored planner must already hold the action set, adding gather returned %v", err)
	}
	if got := restored.Cursor("npc-1"); got != 1 {
		t.Fatalf("restored cursor = %d, want 1", got)
	}
	if _, ok := restored.Goal("npc-1"); !ok {
		t.Fatalf("restored planner lost the goal")
	}
	next, err := restored.Execute("npc-1", 2000)
	if err != nil {
		t.Fatalf("execute after restore: %v", err)
	}
	if next.Index != 2 || next.Action != "saw" || !next.Done {
		t.Fatalf("restored execution = %+v, want step 2 saw done", next)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	p := newChainPlanner()
	if err := p.SetGoal("npc-1", "bench", impl.State{"plank": true}); err != nil {
		t.Fatalf("set goal: %v", err)
	}
	if _, err := p.Plan("npc-1", impl.State{}); err != nil {
		t.Fatalf("plan: %v", err)
	}
	if _, err := p.Execute("npc-1", 1000); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if err := p.Restore([]byte("{broken-payload")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := p.Cursor("npc-1"); got != 1 {
		t.Fatalf("a rejected restore touched the live cursor: %d", got)
	}
	next, err := p.Execute("npc-1", 2000)
	if err != nil {
		t.Fatalf("live planner must keep working after a rejected restore: %v", err)
	}
	if next.Index != 2 {
		t.Fatalf("live execution index = %d, want 2", next.Index)
	}
}
