package task

import (
	"errors"
	"strings"
	"testing"
	"time"

	impl "gsb/batch49-go-safety-e-15"
)

func dueAt(hour int) time.Time {
	return time.Date(2026, 11, 25, 10+hour, 0, 0, 0, time.UTC)
}

func findingOne() impl.Finding {
	return impl.Finding{ID: "f1", Component: "comp-a", Severity: "high", DueAt: dueAt(1)}
}

func findingTwo() impl.Finding {
	return impl.Finding{ID: "f2", Component: "comp-b", Severity: "medium", DueAt: dueAt(2)}
}

func actionOne() impl.Action {
	return impl.Action{ID: "a1", Component: "comp-a", TargetVersion: "1.2.0", Cost: 1, Fixes: []string{"f1"}}
}

func actionTwo() impl.Action {
	return impl.Action{ID: "a2", Component: "comp-b", TargetVersion: "2.1.0", Cost: 1, Fixes: []string{"f2"}}
}

func mustAdd(t *testing.T, s *impl.RemediationBatchSelector, f impl.Finding, actions []impl.Action) {
	t.Helper()
	if err := s.Add(f, actions); err != nil {
		t.Fatalf("add %s must succeed, got %v", f.ID, err)
	}
}

func seededSelector(t *testing.T) *impl.RemediationBatchSelector {
	t.Helper()
	s := impl.NewRemediationBatchSelector()
	mustAdd(t, s, findingOne(), []impl.Action{actionOne()})
	mustAdd(t, s, findingTwo(), []impl.Action{actionTwo()})
	return s
}

func TestAddRejectsBlankFindingID(t *testing.T) {
	s := impl.NewRemediationBatchSelector()
	if err := s.Add(impl.Finding{Component: "comp-a"}, nil); !errors.Is(err, impl.ErrBadFinding) {
		t.Fatalf("blank id error = %v, want ErrBadFinding", err)
	}
	if err := s.Add(impl.Finding{ID: "f1"}, nil); !errors.Is(err, impl.ErrBadFinding) {
		t.Fatalf("blank component error = %v, want ErrBadFinding", err)
	}
	if got := len(s.Findings()); got != 0 {
		t.Fatalf("finding count = %d, want 0 after rejected adds", got)
	}
}

func TestAddRejectsDuplicateFinding(t *testing.T) {
	s := impl.NewRemediationBatchSelector()
	mustAdd(t, s, findingOne(), []impl.Action{actionOne()})
	err := s.Add(findingOne(), []impl.Action{actionOne()})
	if !errors.Is(err, impl.ErrDuplicateFinding) {
		t.Fatalf("duplicate finding error = %v, want ErrDuplicateFinding", err)
	}
	if got := len(s.Findings()); got != 1 {
		t.Fatalf("finding count = %d, want 1", got)
	}
}

func TestAddRejectsActionForOtherComponent(t *testing.T) {
	s := impl.NewRemediationBatchSelector()
	foreign := impl.Action{ID: "ax", Component: "comp-z", TargetVersion: "9.0.0", Cost: 1, Fixes: []string{"f1"}}
	if err := s.Add(findingOne(), []impl.Action{foreign}); !errors.Is(err, impl.ErrActionMismatch) {
		t.Fatalf("foreign component error = %v, want ErrActionMismatch", err)
	}
	wrongFix := impl.Action{ID: "ay", Component: "comp-a", TargetVersion: "1.2.0", Cost: 1, Fixes: []string{"f-other"}}
	if err := s.Add(findingOne(), []impl.Action{wrongFix}); !errors.Is(err, impl.ErrActionMismatch) {
		t.Fatalf("wrong fix target error = %v, want ErrActionMismatch", err)
	}
}

func TestGroupAggregatesByComponent(t *testing.T) {
	s := impl.NewRemediationBatchSelector()
	mustAdd(t, s, findingOne(), []impl.Action{actionOne()})
	f2 := impl.Finding{ID: "f2", Component: "comp-a", DueAt: dueAt(2)}
	a2 := impl.Action{ID: "a2", Component: "comp-a", TargetVersion: "1.3.0", Cost: 2, Fixes: []string{"f2"}}
	mustAdd(t, s, f2, []impl.Action{a2})
	f3 := impl.Finding{ID: "f3", Component: "comp-b", DueAt: dueAt(3)}
	a3 := impl.Action{ID: "a3", Component: "comp-b", TargetVersion: "2.0.0", Cost: 1, Fixes: []string{"f3"}}
	mustAdd(t, s, f3, []impl.Action{a3})
	groups := s.Group()
	if len(groups) != 2 {
		t.Fatalf("group count = %d, want 2", len(groups))
	}
	if groups[0].Component != "comp-a" || groups[1].Component != "comp-b" {
		t.Fatalf("group order = %q,%q, want comp-a then comp-b", groups[0].Component, groups[1].Component)
	}
	if strings.Join(groups[0].Findings, ",") != "f1,f2" {
		t.Fatalf("comp-a findings = %v, want [f1 f2]", groups[0].Findings)
	}
	if strings.Join(groups[0].Actions, ",") != "a1,a2" {
		t.Fatalf("comp-a actions = %v, want [a1 a2]", groups[0].Actions)
	}
	if strings.Join(groups[0].TargetVersions, ",") != "1.2.0,1.3.0" {
		t.Fatalf("comp-a target versions = %v, want [1.2.0 1.3.0]", groups[0].TargetVersions)
	}
	if strings.Join(groups[1].Findings, ",") != "f3" || strings.Join(groups[1].Actions, ",") != "a3" {
		t.Fatalf("comp-b group = %+v, want finding f3 with action a3", groups[1])
	}
}

func TestConflictSameComponentDifferentVersions(t *testing.T) {
	s := impl.NewRemediationBatchSelector()
	a := impl.Action{ID: "a1", Component: "comp-a", TargetVersion: "1.2.0", Cost: 1}
	b := impl.Action{ID: "a2", Component: "comp-a", TargetVersion: "1.3.0", Cost: 1}
	if !s.Conflict(a, b) {
		t.Fatalf("two target versions of one component must conflict")
	}
	other := impl.Action{ID: "b1", Component: "comp-b", TargetVersion: "1.2.0", Cost: 1}
	if s.Conflict(a, other) {
		t.Fatalf("actions on different components must not conflict")
	}
}

func TestNoConflictSameComponentSameVersion(t *testing.T) {
	s := impl.NewRemediationBatchSelector()
	a := impl.Action{ID: "a1", Component: "comp-a", TargetVersion: "1.2.0", Cost: 1}
	b := impl.Action{ID: "a1b", Component: "comp-a", TargetVersion: "1.2.0", Cost: 1}
	if s.Conflict(a, b) {
		t.Fatalf("same component and same target version must not conflict")
	}
	different := impl.Action{ID: "a2", Component: "comp-a", TargetVersion: "1.3.0", Cost: 1}
	if !s.Conflict(a, different) {
		t.Fatalf("the same component at a different target version must conflict")
	}
}

func TestConflictSharedPrerequisite(t *testing.T) {
	s := impl.NewRemediationBatchSelector()
	a := impl.Action{ID: "a1", Component: "comp-a", TargetVersion: "1.2.0", Prereq: "restart-db", Cost: 1}
	b := impl.Action{ID: "b1", Component: "comp-b", TargetVersion: "2.0.0", Prereq: "restart-db", Cost: 1}
	if !s.Conflict(a, b) {
		t.Fatalf("actions sharing one prerequisite step must conflict")
	}
	c := impl.Action{ID: "c1", Component: "comp-c", TargetVersion: "3.0.0", Prereq: "rotate-key", Cost: 1}
	if s.Conflict(b, c) {
		t.Fatalf("actions with different prerequisites must not conflict")
	}
}

func TestSelectPutsCompatibleActionsInOneBatch(t *testing.T) {
	s := seededSelector(t)
	batches, err := s.Select(impl.Budget{MaxCost: 10, MaxBatches: 5})
	if err != nil {
		t.Fatalf("select must succeed, got %v", err)
	}
	if len(batches) != 1 {
		t.Fatalf("batch count = %d, want 1 (%+v)", len(batches), batches)
	}
	if strings.Join(batches[0].Actions, ",") != "a1,a2" {
		t.Fatalf("batch actions = %v, want [a1 a2]", batches[0].Actions)
	}
	if batches[0].Cost != 2 {
		t.Fatalf("batch cost = %d, want 2", batches[0].Cost)
	}
	if strings.Join(batches[0].Covers, ",") != "f1,f2" {
		t.Fatalf("batch covers = %v, want [f1 f2]", batches[0].Covers)
	}
	if got := len(s.Uncovered()); got != 0 {
		t.Fatalf("uncovered = %d, want 0 when the budget covers everything", got)
	}
}

func TestSelectReportsBudgetBlockedFinding(t *testing.T) {
	s := impl.NewRemediationBatchSelector()
	costly := impl.Action{ID: "a1", Component: "comp-a", TargetVersion: "1.2.0", Cost: 5, Fixes: []string{"f1"}}
	mustAdd(t, s, findingOne(), []impl.Action{costly})
	batches, err := s.Select(impl.Budget{MaxCost: 3, MaxBatches: 5})
	if err != nil {
		t.Fatalf("select must succeed, got %v", err)
	}
	if len(batches) != 0 {
		t.Fatalf("batch count = %d, want 0 when the only action exceeds the budget", len(batches))
	}
	uncovered := s.Uncovered()
	if len(uncovered) != 1 || uncovered[0].ID != "f1" || uncovered[0].Reason != impl.ReasonBudget {
		t.Fatalf("uncovered = %+v, want f1 blocked by budget", uncovered)
	}
}

func TestSelectLeavesUncoveredUnderBatchLimit(t *testing.T) {
	s := impl.NewRemediationBatchSelector()
	a1 := impl.Action{ID: "a1", Component: "comp-a", TargetVersion: "1.2.0", Prereq: "restart-db", Cost: 1, Fixes: []string{"f1"}}
	a2 := impl.Action{ID: "a2", Component: "comp-b", TargetVersion: "2.1.0", Prereq: "restart-db", Cost: 1, Fixes: []string{"f2"}}
	mustAdd(t, s, findingOne(), []impl.Action{a1})
	mustAdd(t, s, findingTwo(), []impl.Action{a2})
	batches, err := s.Select(impl.Budget{MaxCost: 10, MaxBatches: 1})
	if err != nil {
		t.Fatalf("select must succeed, got %v", err)
	}
	if len(batches) != 1 {
		t.Fatalf("batch count = %d, want 1", len(batches))
	}
	uncovered := s.Uncovered()
	if len(uncovered) != 1 {
		t.Fatalf("uncovered = %+v, want exactly one finding left out", uncovered)
	}
	if uncovered[0].Reason != impl.ReasonBatchLimit {
		t.Fatalf("uncovered reason = %q, want %q", uncovered[0].Reason, impl.ReasonBatchLimit)
	}
}

func TestSelectIsOrderIndependent(t *testing.T) {
	first := impl.NewRemediationBatchSelector()
	mustAdd(t, first, findingOne(), []impl.Action{actionOne()})
	mustAdd(t, first, findingTwo(), []impl.Action{actionTwo()})

	second := impl.NewRemediationBatchSelector()
	mustAdd(t, second, findingTwo(), []impl.Action{actionTwo()})
	mustAdd(t, second, findingOne(), []impl.Action{actionOne()})

	a, err := first.Select(impl.Budget{MaxCost: 10, MaxBatches: 5})
	if err != nil {
		t.Fatalf("first select must succeed, got %v", err)
	}
	b, err := second.Select(impl.Budget{MaxCost: 10, MaxBatches: 5})
	if err != nil {
		t.Fatalf("second select must succeed, got %v", err)
	}
	if describeBatches(a) != describeBatches(b) {
		t.Fatalf("select depends on input order: %q vs %q", describeBatches(a), describeBatches(b))
	}
}

func TestCoverageCountsDistinctFindings(t *testing.T) {
	s := impl.NewRemediationBatchSelector()
	v1 := impl.Action{ID: "a1", Component: "comp-a", TargetVersion: "1.2.0", Cost: 1, Fixes: []string{"f1"}}
	v2 := impl.Action{ID: "a2", Component: "comp-a", TargetVersion: "1.3.0", Cost: 1, Fixes: []string{"f1"}}
	mustAdd(t, s, findingOne(), []impl.Action{v1, v2})
	covered, err := s.Coverage([]impl.Batch{{Actions: []string{"a1", "a2"}}})
	if err != nil {
		t.Fatalf("coverage must succeed, got %v", err)
	}
	if covered != 1 {
		t.Fatalf("coverage = %d, want 1 because both actions remove the same finding", covered)
	}
}

func TestCoverageRejectsUnknownAction(t *testing.T) {
	s := seededSelector(t)
	_, err := s.Coverage([]impl.Batch{{Actions: []string{"ghost"}}})
	if !errors.Is(err, impl.ErrUnknownAction) {
		t.Fatalf("unknown action coverage error = %v, want ErrUnknownAction", err)
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	s := seededSelector(t)
	batches, err := s.Select(impl.Budget{MaxCost: 10, MaxBatches: 5})
	if err != nil {
		t.Fatalf("select must succeed, got %v", err)
	}
	if len(batches) != 1 {
		t.Fatalf("batch count = %d, want 1", len(batches))
	}
	if err := s.Apply(batches[0]); err != nil {
		t.Fatalf("apply must succeed, got %v", err)
	}
	if !s.Fixed("f1") || !s.Fixed("f2") {
		t.Fatalf("both findings must be fixed after applying the batch")
	}
	if err := s.Apply(batches[0]); err != nil {
		t.Fatalf("repeated apply must succeed, got %v", err)
	}
	if !s.Fixed("f1") || !s.Fixed("f2") {
		t.Fatalf("repeated apply must keep both findings fixed")
	}
	if got := len(s.Findings()); got != 2 {
		t.Fatalf("finding count = %d, want 2", got)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	s := seededSelector(t)
	if err := s.Restore([]byte("...not a snapshot...")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt restore error = %v, want ErrBadSnapshot", err)
	}
	if got := len(s.Findings()); got != 2 {
		t.Fatalf("finding count after corrupt restore = %d, want the previous state of 2", got)
	}
}

func describeBatches(batches []impl.Batch) string {
	parts := make([]string, 0, len(batches))
	for _, b := range batches {
		parts = append(parts, strings.Join(b.Actions, ">")+"="+strings.Join(b.Covers, ">"))
	}
	return strings.Join(parts, "|")
}
