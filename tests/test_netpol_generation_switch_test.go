package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch49-go-safety-b-17"
)

func policyLimits() impl.Limits {
	return impl.Limits{MaxGenerations: 8, MaxPolicies: 8, MaxWorkloads: 8}
}

func allowWeb() []impl.Policy {
	return []impl.Policy{{
		Name:     "allow-web",
		Selector: map[string]string{"app": "web"},
		Ports:    []int{443},
		Action:   impl.ActionAllow,
		Priority: 10,
	}}
}

func TestStageAssignsMonotonicGenerations(t *testing.T) {
	p := impl.NewPolicyGeneration(policyLimits())
	if got := p.Active().ID; got != 0 {
		t.Fatalf("fresh active generation = %d, want zero", got)
	}
	g1, err := p.Stage(allowWeb())
	if err != nil {
		t.Fatalf("first stage: %v", err)
	}
	if g1.ID == 0 {
		t.Fatalf("staged generation id must be positive")
	}
	if len(g1.Policies) != 1 || g1.Policies[0].Name != "allow-web" {
		t.Fatalf("staged policies = %+v, want allow-web", g1.Policies)
	}
	g2, err := p.Stage([]impl.Policy{{
		Name:     "deny-ssh",
		Selector: map[string]string{"app": "bastion"},
		Ports:    []int{22},
		Action:   impl.ActionDeny,
		Priority: 5,
	}})
	if err != nil {
		t.Fatalf("second stage: %v", err)
	}
	if g2.ID <= g1.ID {
		t.Fatalf("second generation id = %d, want greater than %d", g2.ID, g1.ID)
	}
	if got := len(p.History()); got != 2 {
		t.Fatalf("history length = %d, want 2", got)
	}
}

func TestStageRejectsBlankPolicyName(t *testing.T) {
	p := impl.NewPolicyGeneration(policyLimits())
	_, err := p.Stage([]impl.Policy{{
		Name:     "   ",
		Selector: map[string]string{"app": "web"},
		Ports:    []int{443},
		Action:   impl.ActionAllow,
	}})
	if !errors.Is(err, impl.ErrBlankPolicyName) {
		t.Fatalf("blank policy name must report ErrBlankPolicyName, got %v", err)
	}
	if got := len(p.History()); got != 0 {
		t.Fatalf("rejected stage must not enter history, got %d", got)
	}
}

func TestStageRejectsBadActionAndPort(t *testing.T) {
	p := impl.NewPolicyGeneration(policyLimits())
	_, err := p.Stage([]impl.Policy{{
		Name:     "bad-action",
		Selector: map[string]string{"app": "web"},
		Ports:    []int{443},
		Action:   "drop",
	}})
	if !errors.Is(err, impl.ErrBadAction) {
		t.Fatalf("unknown action must report ErrBadAction, got %v", err)
	}
	for _, port := range []int{0, 70000} {
		_, err = p.Stage([]impl.Policy{{
			Name:     "bad-port",
			Selector: map[string]string{"app": "web"},
			Ports:    []int{port},
			Action:   impl.ActionAllow,
		}})
		if !errors.Is(err, impl.ErrBadPort) {
			t.Fatalf("port %d must report ErrBadPort, got %v", port, err)
		}
	}
}

func TestStagingDoesNotChangeActiveGeneration(t *testing.T) {
	p := impl.NewPolicyGeneration(policyLimits())
	g, err := p.Stage(allowWeb())
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if got := p.Active().ID; got != 0 {
		t.Fatalf("active generation after stage = %d, want the previous zero generation", got)
	}
	if got := len(p.History()); got != 1 {
		t.Fatalf("history length = %d, want 1", got)
	}
	if g.ID == 0 {
		t.Fatalf("staged id must be positive")
	}
}

func TestCommitActivatesStagedGeneration(t *testing.T) {
	p := impl.NewPolicyGeneration(policyLimits())
	g, err := p.Stage(allowWeb())
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if err := p.Commit(g); err != nil {
		t.Fatalf("commit must succeed, got %v", err)
	}
	active := p.Active()
	if active.ID != g.ID {
		t.Fatalf("active id = %d, want %d", active.ID, g.ID)
	}
	if len(active.Policies) != 1 || active.Policies[0].Name != "allow-web" {
		t.Fatalf("active policies = %+v, want allow-web", active.Policies)
	}
}

func TestCommitRejectsUnknownGeneration(t *testing.T) {
	p := impl.NewPolicyGeneration(policyLimits())
	err := p.Commit(impl.Generation{ID: 99})
	if !errors.Is(err, impl.ErrUnknownGeneration) {
		t.Fatalf("unknown generation must report ErrUnknownGeneration, got %v", err)
	}
	if got := p.Active().ID; got != 0 {
		t.Fatalf("rejected commit changed the active generation to %d", got)
	}
}

func TestRollbackReturnsPreviousGeneration(t *testing.T) {
	p := impl.NewPolicyGeneration(policyLimits())
	g1, err := p.Stage(allowWeb())
	if err != nil {
		t.Fatalf("stage g1: %v", err)
	}
	if err := p.Commit(g1); err != nil {
		t.Fatalf("commit g1: %v", err)
	}
	g2, err := p.Stage([]impl.Policy{{
		Name:     "deny-web",
		Selector: map[string]string{"app": "web"},
		Ports:    []int{443},
		Action:   impl.ActionDeny,
	}})
	if err != nil {
		t.Fatalf("stage g2: %v", err)
	}
	if err := p.Commit(g2); err != nil {
		t.Fatalf("commit g2: %v", err)
	}
	if err := p.Rollback(); err != nil {
		t.Fatalf("rollback must succeed, got %v", err)
	}
	if got := p.Active().ID; got != g1.ID {
		t.Fatalf("active generation after rollback = %d, want %d", got, g1.ID)
	}
	if len(p.Active().Policies) != 1 || p.Active().Policies[0].Name != "allow-web" {
		t.Fatalf("rolled back policies = %+v, want allow-web", p.Active().Policies)
	}
}

func TestRollbackWithoutCommittedHistoryFails(t *testing.T) {
	p := impl.NewPolicyGeneration(policyLimits())
	if err := p.Rollback(); !errors.Is(err, impl.ErrNoRollback) {
		t.Fatalf("rollback without any commit must report ErrNoRollback, got %v", err)
	}
	if _, err := p.Stage(allowWeb()); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if err := p.Rollback(); !errors.Is(err, impl.ErrNoRollback) {
		t.Fatalf("rollback with only staged generations must report ErrNoRollback, got %v", err)
	}
	if got := p.Active().ID; got != 0 {
		t.Fatalf("failed rollback changed the active generation to %d", got)
	}
}

func TestEvaluateAllowRule(t *testing.T) {
	p := impl.NewPolicyGeneration(policyLimits())
	if err := p.SetWorkload("checkout", map[string]string{"app": "checkout", "tier": "gold"}); err != nil {
		t.Fatalf("set workload: %v", err)
	}
	g, err := p.Stage([]impl.Policy{{
		Name:     "allow-checkout",
		Selector: map[string]string{"app": "checkout"},
		Ports:    []int{8443},
		Action:   impl.ActionAllow,
		Priority: 7,
	}})
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if err := p.Commit(g); err != nil {
		t.Fatalf("commit: %v", err)
	}
	tok, err := p.BeginEvaluation("checkout", 8443)
	if err != nil {
		t.Fatalf("begin evaluation: %v", err)
	}
	decision, err := p.FinishEvaluation(tok)
	if err != nil {
		t.Fatalf("finish evaluation: %v", err)
	}
	if decision.Action != impl.ActionAllow {
		t.Fatalf("action = %q, want %q", decision.Action, impl.ActionAllow)
	}
	if decision.Rule != "allow-checkout" {
		t.Fatalf("rule = %q, want allow-checkout", decision.Rule)
	}
	if decision.Gen != g.ID {
		t.Fatalf("decision generation = %d, want %d", decision.Gen, g.ID)
	}
	if decision.Workload != "checkout" || decision.Port != 8443 {
		t.Fatalf("decision identity = %+v", decision)
	}
}

func TestEvaluateFallsBackToDefaultDeny(t *testing.T) {
	p := impl.NewPolicyGeneration(policyLimits())
	if err := p.SetWorkload("checkout", map[string]string{"app": "checkout"}); err != nil {
		t.Fatalf("set workload: %v", err)
	}
	g, err := p.Stage(allowWeb())
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if err := p.Commit(g); err != nil {
		t.Fatalf("commit: %v", err)
	}
	tok, err := p.BeginEvaluation("checkout", 9000)
	if err != nil {
		t.Fatalf("begin evaluation: %v", err)
	}
	decision, err := p.FinishEvaluation(tok)
	if err != nil {
		t.Fatalf("finish evaluation: %v", err)
	}
	if decision.Action != impl.ActionDeny {
		t.Fatalf("action = %q, want %q", decision.Action, impl.ActionDeny)
	}
	if decision.Rule != impl.RuleDefaultDeny {
		t.Fatalf("rule = %q, want %q", decision.Rule, impl.RuleDefaultDeny)
	}
	if decision.Gen != g.ID {
		t.Fatalf("decision generation = %d, want %d", decision.Gen, g.ID)
	}
}

func TestEvaluateRejectsUnknownWorkload(t *testing.T) {
	p := impl.NewPolicyGeneration(policyLimits())
	if _, err := p.BeginEvaluation("ghost", 443); !errors.Is(err, impl.ErrUnknownWorkload) {
		t.Fatalf("unknown workload must report ErrUnknownWorkload, got %v", err)
	}
}

func TestDiffReportsAddedAndRemovedWorkloads(t *testing.T) {
	p := impl.NewPolicyGeneration(policyLimits())
	if err := p.SetWorkload("shop", map[string]string{"app": "shop"}); err != nil {
		t.Fatalf("set shop: %v", err)
	}
	if err := p.SetWorkload("mail", map[string]string{"app": "mail"}); err != nil {
		t.Fatalf("set mail: %v", err)
	}
	g1, err := p.Stage([]impl.Policy{{
		Name:     "allow-shop",
		Selector: map[string]string{"app": "shop"},
		Ports:    []int{443},
		Action:   impl.ActionAllow,
	}})
	if err != nil {
		t.Fatalf("stage g1: %v", err)
	}
	g2, err := p.Stage([]impl.Policy{{
		Name:     "allow-mail",
		Selector: map[string]string{"app": "mail"},
		Ports:    []int{25},
		Action:   impl.ActionAllow,
	}})
	if err != nil {
		t.Fatalf("stage g2: %v", err)
	}
	diff, err := p.Diff(g1, g2)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(diff.Added) != 1 || diff.Added[0] != "mail" {
		t.Fatalf("added workloads = %v, want [mail]", diff.Added)
	}
	if len(diff.Removed) != 1 || diff.Removed[0] != "shop" {
		t.Fatalf("removed workloads = %v, want [shop]", diff.Removed)
	}
	if len(diff.Changed) != 0 {
		t.Fatalf("changed workloads = %v, want none", diff.Changed)
	}
	if diff.From != g1.ID || diff.To != g2.ID {
		t.Fatalf("diff generation range = %d..%d, want %d..%d", diff.From, diff.To, g1.ID, g2.ID)
	}
}

func TestDiffReportsChangedAssignment(t *testing.T) {
	p := impl.NewPolicyGeneration(policyLimits())
	if err := p.SetWorkload("shop", map[string]string{"app": "shop"}); err != nil {
		t.Fatalf("set shop: %v", err)
	}
	g1, err := p.Stage([]impl.Policy{{
		Name:     "allow-v1",
		Selector: map[string]string{"app": "shop"},
		Ports:    []int{443},
		Action:   impl.ActionAllow,
	}})
	if err != nil {
		t.Fatalf("stage g1: %v", err)
	}
	g2, err := p.Stage([]impl.Policy{{
		Name:     "allow-v2",
		Selector: map[string]string{"app": "shop"},
		Ports:    []int{443},
		Action:   impl.ActionAllow,
	}})
	if err != nil {
		t.Fatalf("stage g2: %v", err)
	}
	diff, err := p.Diff(g1, g2)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(diff.Changed) != 1 || diff.Changed[0] != "shop" {
		t.Fatalf("changed workloads = %v, want [shop]", diff.Changed)
	}
	if len(diff.Added) != 0 || len(diff.Removed) != 0 {
		t.Fatalf("added=%v removed=%v, want both empty", diff.Added, diff.Removed)
	}
}

func TestConcurrentCommitOfSameGenerationOneWins(t *testing.T) {
	p := impl.NewPolicyGeneration(policyLimits())
	g, err := p.Stage(allowWeb())
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	var wg sync.WaitGroup
	results := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = p.Commit(g)
		}(i)
	}
	wg.Wait()
	okCount, activeCount := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			okCount++
		case errors.Is(err, impl.ErrAlreadyActive):
			activeCount++
		default:
			t.Fatalf("unexpected commit error: %v", err)
		}
	}
	if okCount != 1 || activeCount != 1 {
		t.Fatalf("commit results = %v, want exactly one success and one ErrAlreadyActive", results)
	}
	if got := p.Active().ID; got != g.ID {
		t.Fatalf("active generation = %d, want %d", got, g.ID)
	}
}

func TestSnapshotRestoreAndCorruptPayload(t *testing.T) {
	p := impl.NewPolicyGeneration(policyLimits())
	if err := p.SetWorkload("checkout", map[string]string{"app": "checkout"}); err != nil {
		t.Fatalf("set workload: %v", err)
	}
	g, err := p.Stage([]impl.Policy{{
		Name:     "allow-checkout",
		Selector: map[string]string{"app": "checkout"},
		Ports:    []int{8443},
		Action:   impl.ActionAllow,
		Priority: 3,
	}})
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if err := p.Commit(g); err != nil {
		t.Fatalf("commit: %v", err)
	}
	snap, err := p.Snapshot()
	if err != nil {
		t.Fatalf("snapshot must succeed, got %v", err)
	}
	if len(snap) == 0 {
		t.Fatalf("snapshot payload must not be empty")
	}
	restored := impl.NewPolicyGeneration(policyLimits())
	if err := restored.Restore(snap); err != nil {
		t.Fatalf("restore must succeed, got %v", err)
	}
	if got := restored.Active().ID; got != g.ID {
		t.Fatalf("restored active generation = %d, want %d", got, g.ID)
	}
	tok, err := restored.BeginEvaluation("checkout", 8443)
	if err != nil {
		t.Fatalf("begin evaluation on restored state: %v", err)
	}
	decision, err := restored.FinishEvaluation(tok)
	if err != nil {
		t.Fatalf("finish evaluation on restored state: %v", err)
	}
	if decision.Action != impl.ActionAllow {
		t.Fatalf("restored action = %q, want allow", decision.Action)
	}
	if err := restored.Restore([]byte("not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt payload must report ErrBadSnapshot, got %v", err)
	}
	if got := restored.Active().ID; got != g.ID {
		t.Fatalf("rejected restore changed the active generation to %d", got)
	}
}
