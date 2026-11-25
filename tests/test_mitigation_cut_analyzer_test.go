package task

import (
	"errors"
	"strings"
	"testing"

	impl "gsb/batch49-go-safety-e-11"
)

func graphEdges() []impl.Edge {
	return []impl.Edge{
		{ID: "e1", From: "s", To: "a"},
		{ID: "e2", From: "a", To: "t"},
		{ID: "e3", From: "s", To: "b"},
		{ID: "e4", From: "b", To: "t"},
		{ID: "e5", From: "s", To: "c"},
		{ID: "e6", From: "c", To: "t"},
	}
}

func newAnalyzer() *impl.MitigationCutAnalyzer {
	return impl.NewMitigationCutAnalyzer("s", "t", graphEdges())
}

func cutAll() impl.Mitigation {
	return impl.Mitigation{Name: "cut-all", Cost: 9, Covers: []string{"e2", "e4", "e6"}}
}

func cutAllCheap() impl.Mitigation {
	return impl.Mitigation{Name: "cut-all-cheap", Cost: 4, Covers: []string{"e2", "e4", "e6"}}
}

func cutTwo() impl.Mitigation {
	return impl.Mitigation{Name: "cut-two", Cost: 2, Covers: []string{"e1", "e3"}}
}

func cutOne() impl.Mitigation {
	return impl.Mitigation{Name: "cut-one", Cost: 5, Covers: []string{"e2"}}
}

func joined(ch impl.Chain) string { return strings.Join(ch.Edges, ">") }

func TestPathsListsSimplePathsInEdgeOrder(t *testing.T) {
	a := newAnalyzer()
	chains, err := a.Paths("s", "t")
	if err != nil {
		t.Fatalf("paths must succeed, got %v", err)
	}
	if len(chains) != 3 {
		t.Fatalf("path count = %d, want 3", len(chains))
	}
	wantEdges := []string{"e1>e2", "e3>e4", "e5>e6"}
	for i, want := range wantEdges {
		if got := joined(chains[i]); got != want {
			t.Fatalf("path %d edges = %q, want %q", i, got, want)
		}
	}
	if got := strings.Join(chains[0].Nodes, ">"); got != "s>a>t" {
		t.Fatalf("path 0 nodes = %q, want %q", got, "s>a>t")
	}
}

func TestPathsUnknownGoalFails(t *testing.T) {
	a := newAnalyzer()
	_, err := a.Paths("s", "nowhere")
	if !errors.Is(err, impl.ErrUnknownState) {
		t.Fatalf("unknown goal error = %v, want ErrUnknownState", err)
	}
}

func TestResidualDropsBlockedEdge(t *testing.T) {
	a := newAnalyzer()
	if err := a.Block(cutTwo(), []string{"e1"}); err != nil {
		t.Fatalf("block must succeed, got %v", err)
	}
	chains, err := a.Residual("t")
	if err != nil {
		t.Fatalf("residual must succeed, got %v", err)
	}
	if len(chains) != 2 {
		t.Fatalf("residual chain count = %d, want 2", len(chains))
	}
	if got := joined(chains[0]); got != "e3>e4" {
		t.Fatalf("first residual chain = %q, want %q", got, "e3>e4")
	}
	if got := joined(chains[1]); got != "e5>e6" {
		t.Fatalf("second residual chain = %q, want %q", got, "e5>e6")
	}
}

func TestBlockRejectsUnknownEdge(t *testing.T) {
	a := newAnalyzer()
	err := a.Block(cutTwo(), []string{"e99"})
	if !errors.Is(err, impl.ErrUnknownEdge) {
		t.Fatalf("unknown edge error = %v, want ErrUnknownEdge", err)
	}
	if got := len(a.BlockedEdges()); got != 0 {
		t.Fatalf("blocked edge count = %d, want 0 after a rejected block", got)
	}
}

func TestBlockRejectsUncoveredEdge(t *testing.T) {
	a := newAnalyzer()
	err := a.Block(cutTwo(), []string{"e2"})
	if !errors.Is(err, impl.ErrNotCovered) {
		t.Fatalf("uncovered edge error = %v, want ErrNotCovered", err)
	}
	if got := len(a.BlockedEdges()); got != 0 {
		t.Fatalf("blocked edge count = %d, want 0 after a rejected block", got)
	}
}

func TestBlockAccumulatesAcrossEdgesAndIsIdempotent(t *testing.T) {
	a := newAnalyzer()
	if err := a.Block(cutTwo(), []string{"e1"}); err != nil {
		t.Fatalf("first block must succeed, got %v", err)
	}
	if err := a.Block(cutTwo(), []string{"e1"}); err != nil {
		t.Fatalf("repeated block must succeed, got %v", err)
	}
	if err := a.Block(cutTwo(), []string{"e3"}); err != nil {
		t.Fatalf("second edge block must succeed, got %v", err)
	}
	got := a.BlockedEdges()
	if len(got) != 2 || got[0] != "e1" || got[1] != "e3" {
		t.Fatalf("blocked edges = %v, want [e1 e3]", got)
	}
}

func TestMinCutChoosesSingleMitigationCuttingAllPaths(t *testing.T) {
	a := newAnalyzer()
	set, err := a.MinCut("s", "t", []impl.Mitigation{cutTwo(), cutAll(), cutOne()})
	if err != nil {
		t.Fatalf("min cut must succeed, got %v", err)
	}
	if len(set) != 1 || set[0].Name != "cut-all" {
		t.Fatalf("min cut = %v, want the single mitigation cut-all", namesOf(set))
	}
}

func TestMinCutPrefersLowerCostOnEqualSize(t *testing.T) {
	a := newAnalyzer()
	set, err := a.MinCut("s", "t", []impl.Mitigation{cutAll(), cutAllCheap()})
	if err != nil {
		t.Fatalf("min cut must succeed, got %v", err)
	}
	if len(set) != 1 || set[0].Name != "cut-all-cheap" {
		t.Fatalf("min cut = %v, want the cheaper single mitigation", namesOf(set))
	}
}

func TestMinCutNameTieIsDeterministic(t *testing.T) {
	alpha := impl.Mitigation{Name: "alpha-cut", Cost: 3, Covers: []string{"e2", "e4", "e6"}}
	beta := impl.Mitigation{Name: "beta-cut", Cost: 3, Covers: []string{"e2", "e4", "e6"}}
	a := newAnalyzer()
	first, err := a.MinCut("s", "t", []impl.Mitigation{beta, alpha})
	if err != nil {
		t.Fatalf("min cut must succeed, got %v", err)
	}
	second, err := a.MinCut("s", "t", []impl.Mitigation{alpha, beta})
	if err != nil {
		t.Fatalf("repeated min cut must succeed, got %v", err)
	}
	if len(first) != 1 || first[0].Name != "alpha-cut" {
		t.Fatalf("min cut = %v, want alpha-cut", namesOf(first))
	}
	if namesOf(first) != namesOf(second) {
		t.Fatalf("min cut not repeatable: %v vs %v", namesOf(first), namesOf(second))
	}
}

func TestMinCutReportsUncuttableResidual(t *testing.T) {
	a := newAnalyzer()
	set, err := a.MinCut("s", "t", []impl.Mitigation{cutTwo(), cutOne()})
	if !errors.Is(err, impl.ErrUncuttable) {
		t.Fatalf("uncuttable error = %v, want ErrUncuttable", err)
	}
	if len(set) != 0 {
		t.Fatalf("uncuttable result = %v, want an empty set", namesOf(set))
	}
	residual, rerr := a.Residual("t")
	if rerr != nil {
		t.Fatalf("residual must still be queryable, got %v", rerr)
	}
	if len(residual) != 3 {
		t.Fatalf("residual count = %d, want the 3 untouched chains", len(residual))
	}
}

func TestMinCutEnforcesCandidateLimit(t *testing.T) {
	a := impl.NewMitigationCutAnalyzerWithLimit("s", "t", graphEdges(), 4)
	candidates := make([]impl.Mitigation, 0, 5)
	for _, name := range []string{"c1", "c2", "c3", "c4", "c5"} {
		candidates = append(candidates, impl.Mitigation{Name: name, Cost: 1, Covers: []string{"e2", "e4", "e6"}})
	}
	_, err := a.MinCut("s", "t", candidates)
	if !errors.Is(err, impl.ErrCandidateLimit) {
		t.Fatalf("candidate limit error = %v, want ErrCandidateLimit", err)
	}
}

func TestApplyRejectsConflictingSet(t *testing.T) {
	x := impl.Mitigation{Name: "x-cut", Cost: 1, Covers: []string{"e2", "e4", "e6"}, Conflicts: []string{"y-cut"}}
	y := impl.Mitigation{Name: "y-cut", Cost: 1, Covers: []string{"e1", "e3"}, Conflicts: []string{"x-cut"}}
	a := newAnalyzer()
	err := a.Apply([]impl.Mitigation{x, y})
	if !errors.Is(err, impl.ErrConflict) {
		t.Fatalf("conflicting apply error = %v, want ErrConflict", err)
	}
	if got := len(a.BlockedEdges()); got != 0 {
		t.Fatalf("blocked edges = %d, want 0 after a rejected apply", got)
	}
}

func TestApplyAppliesWholeSet(t *testing.T) {
	a := newAnalyzer()
	if err := a.Apply([]impl.Mitigation{cutAll()}); err != nil {
		t.Fatalf("apply must succeed, got %v", err)
	}
	residual, err := a.Residual("t")
	if err != nil {
		t.Fatalf("residual must succeed, got %v", err)
	}
	if len(residual) != 0 {
		t.Fatalf("residual count = %d, want 0 after cutting every chain", len(residual))
	}
	if err := a.Apply([]impl.Mitigation{cutAll()}); err != nil {
		t.Fatalf("repeated apply must succeed, got %v", err)
	}
	blocked := a.BlockedEdges()
	if len(blocked) != 3 || blocked[0] != "e2" || blocked[1] != "e4" || blocked[2] != "e6" {
		t.Fatalf("blocked edges = %v, want [e2 e4 e6] without duplicates", blocked)
	}
}

func TestImpactCountsPathsNotEdges(t *testing.T) {
	a := newAnalyzer()
	if got := a.Impact(cutTwo()); got != 2 {
		t.Fatalf("impact of cut-two = %d, want 2 independent chains", got)
	}
	if got := a.Impact(cutOne()); got != 1 {
		t.Fatalf("impact of cut-one = %d, want 1 chain", got)
	}
	if got := a.Impact(cutAll()); got != 3 {
		t.Fatalf("impact of cut-all = %d, want 3 chains", got)
	}
}

func TestSnapshotRestoreKeepsAppliedBlocks(t *testing.T) {
	a := newAnalyzer()
	if err := a.Block(cutTwo(), []string{"e1"}); err != nil {
		t.Fatalf("block must succeed, got %v", err)
	}
	payload, err := a.Snapshot()
	if err != nil {
		t.Fatalf("snapshot must succeed, got %v", err)
	}
	if len(payload) == 0 {
		t.Fatalf("snapshot payload must not be empty")
	}
	b := newAnalyzer()
	if err := b.Restore(payload); err != nil {
		t.Fatalf("restore must succeed, got %v", err)
	}
	blocked := b.BlockedEdges()
	if len(blocked) != 1 || blocked[0] != "e1" {
		t.Fatalf("restored blocked edges = %v, want [e1]", blocked)
	}
	residual, err := b.Residual("t")
	if err != nil {
		t.Fatalf("residual after restore must succeed, got %v", err)
	}
	if len(residual) != 2 {
		t.Fatalf("residual count after restore = %d, want 2", len(residual))
	}
	if err := b.Restore([]byte("{{not a snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt restore error = %v, want ErrBadSnapshot", err)
	}
	if got := len(b.BlockedEdges()); got != 1 {
		t.Fatalf("blocked edges after corrupt restore = %d, want the old state of 1", got)
	}
}

func namesOf(set []impl.Mitigation) string {
	names := make([]string, 0, len(set))
	for _, m := range set {
		names = append(names, m.Name)
	}
	return "[" + strings.Join(names, ",") + "]"
}
