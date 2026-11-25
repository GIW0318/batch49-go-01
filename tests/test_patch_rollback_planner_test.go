package task

import (
	"errors"
	"testing"
	"time"

	impl "gsb/batch49-go-safety-e-08"
)

func day(n int) time.Time {
	return time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC).Add(time.Duration(n) * 24 * time.Hour)
}

func clockAt(t time.Time) impl.Options {
	return impl.Options{Now: func() time.Time { return t }}
}

func blockFor(blockers []impl.Blocker, step string) (impl.Blocker, bool) {
	for _, blocker := range blockers {
		if blocker.Step == step {
			return blocker, true
		}
	}
	return impl.Blocker{}, false
}

func TestRegisterBatchRejectsBlank(t *testing.T) {
	p := impl.NewPatchRollbackPlanner(clockAt(day(1)))
	if err := p.RegisterBatch(impl.Batch{ID: "", Steps: []impl.Step{{ID: "s-1"}}}); !errors.Is(err, impl.ErrBlankField) {
		t.Fatalf("blank batch id must fail with ErrBlankField, got %v", err)
	}
	if err := p.RegisterBatch(impl.Batch{ID: "batch-1"}); !errors.Is(err, impl.ErrBlankField) {
		t.Fatalf("a batch without steps must fail with ErrBlankField, got %v", err)
	}
	if err := p.RegisterBatch(impl.Batch{ID: "batch-1", Steps: []impl.Step{{ID: "  "}}}); !errors.Is(err, impl.ErrBlankField) {
		t.Fatalf("blank step id must fail with ErrBlankField, got %v", err)
	}
}

func TestRegisterBatchRejectsDuplicate(t *testing.T) {
	p := impl.NewPatchRollbackPlanner(clockAt(day(1)))
	batch := impl.Batch{ID: "batch-1", Steps: []impl.Step{{ID: "s-1", ApplyOrder: 1}}, AppliedAt: day(0), Window: 24 * time.Hour}
	if err := p.RegisterBatch(batch); err != nil {
		t.Fatalf("register batch: %v", err)
	}
	if err := p.RegisterBatch(batch); !errors.Is(err, impl.ErrDuplicateBatch) {
		t.Fatalf("duplicate batch must fail with ErrDuplicateBatch, got %v", err)
	}
}

func TestPlanRejectsMissingDependency(t *testing.T) {
	p := impl.NewPatchRollbackPlanner(clockAt(day(1)))
	batch := impl.Batch{ID: "batch-1", Steps: []impl.Step{
		{ID: "s-1", ApplyOrder: 1},
		{ID: "s-2", ApplyOrder: 2, DependsOn: []string{"ghost"}},
	}}
	_, err := p.Plan(batch)
	if !errors.Is(err, impl.ErrDependencyMissing) {
		t.Fatalf("unknown dependency must fail with ErrDependencyMissing, got %v", err)
	}
}

func TestPlanRejectsCycle(t *testing.T) {
	p := impl.NewPatchRollbackPlanner(clockAt(day(1)))
	batch := impl.Batch{ID: "batch-1", Steps: []impl.Step{
		{ID: "s-1", ApplyOrder: 1, DependsOn: []string{"s-2"}},
		{ID: "s-2", ApplyOrder: 2, DependsOn: []string{"s-1"}},
	}}
	_, err := p.Plan(batch)
	if !errors.Is(err, impl.ErrDependencyCycle) {
		t.Fatalf("cyclic dependencies must fail with ErrDependencyCycle, got %v", err)
	}
}

func TestPlanOrdersDependenciesBackwards(t *testing.T) {
	p := impl.NewPatchRollbackPlanner(clockAt(day(1)))
	batch := impl.Batch{ID: "batch-1", Steps: []impl.Step{
		{ID: "s-d", ApplyOrder: 4, DependsOn: []string{"s-b", "s-c"}},
		{ID: "s-b", ApplyOrder: 2, DependsOn: []string{"s-a"}},
		{ID: "s-c", ApplyOrder: 3, DependsOn: []string{"s-a"}},
		{ID: "s-a", ApplyOrder: 1},
	}, AppliedAt: day(0), Window: 48 * time.Hour}
	plan, err := p.Plan(batch)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	wantOrder := []string{"s-d", "s-c", "s-b", "s-a"}
	if len(plan.Order) != len(wantOrder) {
		t.Fatalf("rollback order length = %d, want %d (%v)", len(plan.Order), len(wantOrder), plan.Order)
	}
	for i, step := range wantOrder {
		if plan.Order[i] != step {
			t.Fatalf("rollback order[%d] = %q, want %q (full %v)", i, plan.Order[i], step, plan.Order)
		}
	}
	if len(plan.Layers) != 3 {
		t.Fatalf("layers = %v, want three dependency layers", plan.Layers)
	}
	if len(plan.Layers[1]) != 2 || plan.Layers[1][0] != "s-b" || plan.Layers[1][1] != "s-c" {
		t.Fatalf("middle layer = %v, want [s-b s-c] in apply order", plan.Layers[1])
	}
	if plan.Reversible != 4 {
		t.Fatalf("reversible count = %d, want 4", plan.Reversible)
	}
}

func TestPlanReportsIrreversibleBlocker(t *testing.T) {
	p := impl.NewPatchRollbackPlanner(clockAt(day(1)))
	batch := impl.Batch{ID: "batch-1", Steps: []impl.Step{
		{ID: "s-base", ApplyOrder: 1},
		{ID: "s-data", ApplyOrder: 2, DependsOn: []string{"s-base"}, Irreversible: true},
	}, AppliedAt: day(0), Window: 48 * time.Hour}
	plan, err := p.Plan(batch)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.Reversible != 0 {
		t.Fatalf("reversible count = %d, want 0 when the first rollback step is irreversible", plan.Reversible)
	}
	blocker, ok := blockFor(plan.Blockers, "s-data")
	if !ok || blocker.Reason != impl.BlockerIrreversible {
		t.Fatalf("blockers = %+v, want s-data marked irreversible", plan.Blockers)
	}
	if feasible, blockers := p.Feasible(plan); feasible || len(blockers) == 0 {
		t.Fatalf("plan with an irreversible step must be infeasible, got feasible=%v blockers=%+v", feasible, blockers)
	}
}

func TestFeasibleTrueForReversibleBatch(t *testing.T) {
	p := impl.NewPatchRollbackPlanner(clockAt(day(1)))
	batch := impl.Batch{ID: "batch-1", Steps: []impl.Step{
		{ID: "s-1", ApplyOrder: 1},
		{ID: "s-2", ApplyOrder: 2},
	}, AppliedAt: day(0), Window: 48 * time.Hour}
	plan, err := p.Plan(batch)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	feasible, blockers := p.Feasible(plan)
	if !feasible || len(blockers) != 0 {
		t.Fatalf("fully reversible plan must be feasible, got feasible=%v blockers=%+v", feasible, blockers)
	}
	if plan.Reversible != 2 {
		t.Fatalf("reversible count = %d, want 2", plan.Reversible)
	}
}

func TestWindowReturnsDeadline(t *testing.T) {
	p := impl.NewPatchRollbackPlanner(clockAt(day(0)))
	batch := impl.Batch{ID: "batch-1", Steps: []impl.Step{{ID: "s-1", ApplyOrder: 1}}, AppliedAt: day(1), Window: 12 * time.Hour}
	if err := p.RegisterBatch(batch); err != nil {
		t.Fatalf("register batch: %v", err)
	}
	deadline, err := p.Window("batch-1")
	if err != nil {
		t.Fatalf("window: %v", err)
	}
	if want := day(1).Add(12 * time.Hour); !deadline.Equal(want) {
		t.Fatalf("deadline = %v, want %v", deadline, want)
	}
}

func TestWindowUnknownBatchFails(t *testing.T) {
	p := impl.NewPatchRollbackPlanner(clockAt(day(1)))
	if _, err := p.Window("batch-404"); !errors.Is(err, impl.ErrUnknownBatch) {
		t.Fatalf("unknown batch must fail with ErrUnknownBatch, got %v", err)
	}
}

func TestExecuteReversibleSteps(t *testing.T) {
	p := impl.NewPatchRollbackPlanner(clockAt(day(0)))
	batch := impl.Batch{ID: "batch-1", Steps: []impl.Step{
		{ID: "s-a", ApplyOrder: 1},
		{ID: "s-b", ApplyOrder: 2},
		{ID: "s-c", ApplyOrder: 3},
	}, AppliedAt: day(0), Window: 48 * time.Hour}
	if err := p.RegisterBatch(batch); err != nil {
		t.Fatalf("register batch: %v", err)
	}
	plan, err := p.Plan(batch)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	result, err := p.Execute(plan, 2)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.Status != impl.StatusExecuted {
		t.Fatalf("result status = %q, want %q", result.Status, impl.StatusExecuted)
	}
	if len(result.Completed) != 2 || result.Completed[0] != "s-c" || result.Completed[1] != "s-b" {
		t.Fatalf("completed = %v, want [s-c s-b] in rollback order", result.Completed)
	}
	if len(result.Remaining) != 1 || result.Remaining[0] != "s-a" {
		t.Fatalf("remaining = %v, want [s-a]", result.Remaining)
	}
	stored, ok := p.Status("batch-1")
	if !ok || stored.Status != impl.StatusExecuted {
		t.Fatalf("stored status = %+v ok=%v, want an executed result", stored, ok)
	}
}

func TestExecuteRefusesBeyondIrreversible(t *testing.T) {
	p := impl.NewPatchRollbackPlanner(clockAt(day(0)))
	batch := impl.Batch{ID: "batch-1", Steps: []impl.Step{
		{ID: "s-a", ApplyOrder: 1, Irreversible: true},
		{ID: "s-b", ApplyOrder: 2},
	}, AppliedAt: day(0), Window: 48 * time.Hour}
	if err := p.RegisterBatch(batch); err != nil {
		t.Fatalf("register batch: %v", err)
	}
	plan, err := p.Plan(batch)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.Reversible != 1 {
		t.Fatalf("reversible count = %d, want 1 before the irreversible step", plan.Reversible)
	}
	if _, err := p.Execute(plan, 2); !errors.Is(err, impl.ErrIrreversible) {
		t.Fatalf("crossing the irreversible step must fail with ErrIrreversible, got %v", err)
	}
	if _, ok := p.Status("batch-1"); ok {
		t.Fatalf("a refused execution must not record a result")
	}
}

func TestExecuteClosedWindowRefused(t *testing.T) {
	p := impl.NewPatchRollbackPlanner(clockAt(day(5)))
	batch := impl.Batch{ID: "batch-1", Steps: []impl.Step{{ID: "s-a", ApplyOrder: 1}}, AppliedAt: day(0), Window: 24 * time.Hour}
	if err := p.RegisterBatch(batch); err != nil {
		t.Fatalf("register batch: %v", err)
	}
	plan, err := p.Plan(batch)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if _, err := p.Execute(plan, 1); !errors.Is(err, impl.ErrWindowClosed) {
		t.Fatalf("execution after the window must fail with ErrWindowClosed, got %v", err)
	}
	if _, ok := p.Status("batch-1"); ok {
		t.Fatalf("a window closed execution must not record progress")
	}
}

func TestExecuteMidFailureListsRemaining(t *testing.T) {
	p := impl.NewPatchRollbackPlanner(clockAt(day(0)))
	batch := impl.Batch{ID: "batch-1", Steps: []impl.Step{
		{ID: "s-a", ApplyOrder: 1},
		{ID: "s-b", ApplyOrder: 2, FailRollback: true},
		{ID: "s-c", ApplyOrder: 3},
	}, AppliedAt: day(0), Window: 48 * time.Hour}
	if err := p.RegisterBatch(batch); err != nil {
		t.Fatalf("register batch: %v", err)
	}
	plan, err := p.Plan(batch)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	result, err := p.Execute(plan, 3)
	if !errors.Is(err, impl.ErrStepFailed) {
		t.Fatalf("a failing rollback step must return ErrStepFailed, got %v", err)
	}
	if result.Status != impl.StatusPartial {
		t.Fatalf("result status = %q, want %q", result.Status, impl.StatusPartial)
	}
	if len(result.Completed) != 1 || result.Completed[0] != "s-c" {
		t.Fatalf("completed = %v, want [s-c] before the failure", result.Completed)
	}
	if result.Failed != "s-b" {
		t.Fatalf("failed step = %q, want s-b", result.Failed)
	}
}

func TestExecuteTwiceRejected(t *testing.T) {
	p := impl.NewPatchRollbackPlanner(clockAt(day(0)))
	batch := impl.Batch{ID: "batch-1", Steps: []impl.Step{
		{ID: "s-a", ApplyOrder: 1},
		{ID: "s-b", ApplyOrder: 2},
	}, AppliedAt: day(0), Window: 48 * time.Hour}
	if err := p.RegisterBatch(batch); err != nil {
		t.Fatalf("register batch: %v", err)
	}
	plan, err := p.Plan(batch)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if _, err := p.Execute(plan, 1); err != nil {
		t.Fatalf("first execute: %v", err)
	}
	if _, err := p.Execute(plan, 1); !errors.Is(err, impl.ErrAlreadyExecuted) {
		t.Fatalf("a second rollback of the same batch must fail with ErrAlreadyExecuted, got %v", err)
	}
}

func TestMarkIrreversibleBlocksPlanning(t *testing.T) {
	p := impl.NewPatchRollbackPlanner(clockAt(day(0)))
	batch := impl.Batch{ID: "batch-1", Steps: []impl.Step{
		{ID: "s-a", ApplyOrder: 1},
		{ID: "s-b", ApplyOrder: 2},
	}, AppliedAt: day(0), Window: 48 * time.Hour}
	if err := p.RegisterBatch(batch); err != nil {
		t.Fatalf("register batch: %v", err)
	}
	if err := p.MarkIrreversible("s-a", "data migration"); err != nil {
		t.Fatalf("mark irreversible: %v", err)
	}
	if err := p.MarkIrreversible("s-a", "data migration"); err != nil {
		t.Fatalf("marking the same step twice must be idempotent, got %v", err)
	}
	if p.Reversible("s-a") {
		t.Fatalf("a marked step must not be reversible")
	}
	if !p.Reversible("s-b") {
		t.Fatalf("an unmarked step must stay reversible")
	}
	if err := p.MarkIrreversible("ghost", "unknown"); !errors.Is(err, impl.ErrUnknownStep) {
		t.Fatalf("marking an unknown step must fail with ErrUnknownStep, got %v", err)
	}
	plan, err := p.Plan(batch)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	blocker, ok := blockFor(plan.Blockers, "s-a")
	if !ok || blocker.Reason != impl.BlockerIrreversible {
		t.Fatalf("blockers = %+v, want s-a marked irreversible", plan.Blockers)
	}
	if feasible, _ := p.Feasible(plan); feasible {
		t.Fatalf("a plan containing a marked irreversible step must be infeasible")
	}
}
