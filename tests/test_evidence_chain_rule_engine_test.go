package task

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	impl "gsb/batch49-go-safety-c-01"
)

func ref(kind, subject string) impl.EvidenceRef {
	return impl.EvidenceRef{Kind: kind, Subject: subject}
}

func cascadeRules() []impl.Rule {
	return []impl.Rule{
		{
			ID:       "rule-exposure",
			Severity: impl.SeverityHigh,
			Produces: []impl.EvidenceRef{ref("exposed-port", "svc-a")},
		},
		{
			ID:       "rule-exploit",
			Severity: impl.SeverityCritical,
			Requires: []impl.EvidenceRef{ref("exposed-port", "svc-a")},
			Produces: []impl.EvidenceRef{ref("session-token", "svc-a")},
		},
	}
}

func cascadeEngine(t *testing.T) *impl.EvidenceRuleEngine {
	t.Helper()
	e := impl.NewEvidenceRuleEngine()
	for _, rule := range cascadeRules() {
		if err := e.Register(rule); err != nil {
			t.Fatalf("register %s: %v", rule.ID, err)
		}
	}
	return e
}

func cascadeTarget() impl.Target {
	return impl.Target{ID: "scan-a", Subjects: []string{"svc-a"}}
}

func TestRegisterRejectsBlankAndDuplicateRule(t *testing.T) {
	e := impl.NewEvidenceRuleEngine()
	if err := e.Register(impl.Rule{Produces: []impl.EvidenceRef{ref("net-conn", "svc-a")}}); !errors.Is(err, impl.ErrBlankRuleID) {
		t.Fatalf("blank rule id must be rejected with ErrBlankRuleID, got %v", err)
	}
	rule := impl.Rule{ID: "rule-port", Severity: impl.SeverityHigh, Produces: []impl.EvidenceRef{ref("net-conn", "svc-a")}}
	if err := e.Register(rule); err != nil {
		t.Fatalf("register first rule: %v", err)
	}
	if err := e.Register(rule); !errors.Is(err, impl.ErrDuplicateRule) {
		t.Fatalf("duplicate rule id must be rejected with ErrDuplicateRule, got %v", err)
	}
	if err := e.Register(impl.Rule{ID: "rule-empty"}); !errors.Is(err, impl.ErrBadRule) {
		t.Fatalf("rule without produced evidence must be rejected with ErrBadRule, got %v", err)
	}
	if got := e.Rules(); len(got) != 1 || got[0] != "rule-port" {
		t.Fatalf("registered rules = %v, want exactly [rule-port]", got)
	}
}

func TestRegisterRejectsDependencyCycle(t *testing.T) {
	e := impl.NewEvidenceRuleEngine()
	first := impl.Rule{
		ID:       "rule-a",
		Requires: []impl.EvidenceRef{ref("kind-b", "svc-a")},
		Produces: []impl.EvidenceRef{ref("kind-a", "svc-a")},
	}
	if err := e.Register(first); err != nil {
		t.Fatalf("first half of the chain must register: %v", err)
	}
	second := impl.Rule{
		ID:       "rule-b",
		Requires: []impl.EvidenceRef{ref("kind-a", "svc-a")},
		Produces: []impl.EvidenceRef{ref("kind-b", "svc-a")},
	}
	if err := e.Register(second); !errors.Is(err, impl.ErrRuleCycle) {
		t.Fatalf("closing a dependency cycle must fail with ErrRuleCycle, got %v", err)
	}
	self := impl.Rule{
		ID:       "rule-self",
		Requires: []impl.EvidenceRef{ref("kind-s", "svc-a")},
		Produces: []impl.EvidenceRef{ref("kind-s", "svc-a")},
	}
	if err := e.Register(self); !errors.Is(err, impl.ErrRuleCycle) {
		t.Fatalf("a rule requiring its own product must fail with ErrRuleCycle, got %v", err)
	}
	if got := e.Rules(); len(got) != 1 || got[0] != "rule-a" {
		t.Fatalf("rejected rules must not be stored, got %v", got)
	}
}

func TestRulesKeepRegistrationOrder(t *testing.T) {
	e := impl.NewEvidenceRuleEngine()
	for _, id := range []string{"rule-zeta", "rule-alpha", "rule-mike"} {
		if err := e.Register(impl.Rule{ID: id, Produces: []impl.EvidenceRef{ref("kind-"+id, "svc-a")}}); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
	got := e.Rules()
	want := []string{"rule-zeta", "rule-alpha", "rule-mike"}
	if len(got) != len(want) {
		t.Fatalf("rules = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rules = %v, want registration order %v", got, want)
		}
	}
}

func TestRunFiresRuleWithoutRequirements(t *testing.T) {
	e := impl.NewEvidenceRuleEngine()
	if err := e.Register(impl.Rule{
		ID:       "rule-exposure",
		Severity: impl.SeverityHigh,
		Produces: []impl.EvidenceRef{ref("exposed-port", "svc-a")},
	}); err != nil {
		t.Fatalf("register rule: %v", err)
	}
	res, err := e.Run(cascadeTarget(), impl.Budget{MaxRounds: 5, MaxFindings: 10})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(res.Findings))
	}
	f := res.Findings[0]
	if f.RuleID != "rule-exposure" {
		t.Fatalf("finding rule = %q, want rule-exposure", f.RuleID)
	}
	if f.Round != 1 {
		t.Fatalf("finding round = %d, want 1", f.Round)
	}
	if f.Severity != impl.SeverityHigh {
		t.Fatalf("finding severity = %q, want %q", f.Severity, impl.SeverityHigh)
	}
	if f.Subject != "svc-a" {
		t.Fatalf("finding subject = %q, want svc-a", f.Subject)
	}
	if f.ID == "" {
		t.Fatalf("finding id must not be empty")
	}
	if !res.Converged {
		t.Fatalf("single rule run must converge, got %+v", res)
	}
	if len(res.Incomplete) != 0 {
		t.Fatalf("incomplete rules = %v, want none", res.Incomplete)
	}
	evidence := e.Evidence("scan-a")
	if len(evidence) != 1 || evidence[0].Kind != "exposed-port" || evidence[0].Subject != "svc-a" {
		t.Fatalf("evidence ledger = %+v, want one exposed-port item for svc-a", evidence)
	}
	if got := e.Findings("scan-a"); len(got) != 1 {
		t.Fatalf("stored findings = %d, want 1", len(got))
	}
}

func TestRunCascadesAcrossRounds(t *testing.T) {
	e := cascadeEngine(t)
	res, err := e.Run(cascadeTarget(), impl.Budget{MaxRounds: 6, MaxFindings: 10})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Findings) != 2 {
		t.Fatalf("findings = %d, want 2 (exposure then exploit)", len(res.Findings))
	}
	if res.Findings[0].RuleID != "rule-exposure" || res.Findings[0].Round != 1 {
		t.Fatalf("first finding = %+v, want rule-exposure in round 1", res.Findings[0])
	}
	if res.Findings[1].RuleID != "rule-exploit" || res.Findings[1].Round != 2 {
		t.Fatalf("second finding = %+v, want rule-exploit in round 2", res.Findings[1])
	}
	if res.Rounds < 2 {
		t.Fatalf("rounds = %d, want at least 2 for a two level cascade", res.Rounds)
	}
	if !res.Converged {
		t.Fatalf("cascade must converge, got %+v", res)
	}
	evidence := e.Evidence("scan-a")
	if len(evidence) != 2 {
		t.Fatalf("evidence ledger = %d items, want 2", len(evidence))
	}
}

func TestRunIsIncrementalPerTarget(t *testing.T) {
	e := cascadeEngine(t)
	first, err := e.Run(cascadeTarget(), impl.Budget{MaxRounds: 6, MaxFindings: 10})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if len(first.Findings) != 2 {
		t.Fatalf("first run findings = %d, want 2", len(first.Findings))
	}
	second, err := e.Run(cascadeTarget(), impl.Budget{MaxRounds: 6, MaxFindings: 10})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(second.Findings) != 0 {
		t.Fatalf("second run findings = %d, want 0 because every rule already fired", len(second.Findings))
	}
	if !second.Converged {
		t.Fatalf("second run must converge, got %+v", second)
	}
	if got := e.Findings("scan-a"); len(got) != 2 {
		t.Fatalf("stored findings = %d, want 2 after the incremental run", len(got))
	}
	if got := e.Evidence("scan-a"); len(got) != 2 {
		t.Fatalf("evidence ledger = %d, want 2 after the incremental run", len(got))
	}
}

func TestRunFindingsCapTruncates(t *testing.T) {
	e := impl.NewEvidenceRuleEngine()
	for _, id := range []string{"rule-one", "rule-two"} {
		if err := e.Register(impl.Rule{ID: id, Severity: impl.SeverityLow, Produces: []impl.EvidenceRef{ref("kind-"+id, "svc-a")}}); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
	res, err := e.Run(cascadeTarget(), impl.Budget{MaxRounds: 4, MaxFindings: 1})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Truncated {
		t.Fatalf("result must be marked truncated when the findings cap is hit, got %+v", res)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("findings = %d, want the cap of 1", len(res.Findings))
	}
	if got := e.Findings("scan-a"); len(got) != 1 {
		t.Fatalf("stored findings = %d, want 1", len(got))
	}
}

func TestRunRejectsNonPositiveBudget(t *testing.T) {
	e := cascadeEngine(t)
	if _, err := e.Run(cascadeTarget(), impl.Budget{MaxRounds: 0, MaxFindings: 5}); !errors.Is(err, impl.ErrBadBudget) {
		t.Fatalf("zero max rounds must fail with ErrBadBudget, got %v", err)
	}
	if _, err := e.Run(cascadeTarget(), impl.Budget{MaxRounds: 3, MaxFindings: -1}); !errors.Is(err, impl.ErrBadBudget) {
		t.Fatalf("negative findings cap must fail with ErrBadBudget, got %v", err)
	}
	if got := e.Findings("scan-a"); len(got) != 0 {
		t.Fatalf("rejected runs must not record findings, got %d", len(got))
	}
}

func TestRunRequiresExactSubjectMatch(t *testing.T) {
	e := impl.NewEvidenceRuleEngine()
	if err := e.Register(impl.Rule{
		ID:       "rule-other-host",
		Severity: impl.SeverityMedium,
		Requires: []impl.EvidenceRef{ref("net-conn", "svc-b")},
		Produces: []impl.EvidenceRef{ref("lateral-move", "svc-a")},
	}); err != nil {
		t.Fatalf("register rule: %v", err)
	}
	target := impl.Target{ID: "scan-a", Subjects: []string{"svc-a"}, Seeds: []impl.EvidenceRef{ref("net-conn", "svc-a")}}
	res, err := e.Run(target, impl.Budget{MaxRounds: 4, MaxFindings: 5})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("findings = %d, want 0 because the evidence belongs to another subject", len(res.Findings))
	}
	if got := e.Evidence("scan-a"); len(got) != 1 {
		t.Fatalf("evidence ledger = %d items, want only the seed", len(got))
	}
}

func TestRunKeepsTargetsIsolated(t *testing.T) {
	e := cascadeEngine(t)
	a := impl.Target{ID: "scan-a", Subjects: []string{"svc-a"}}
	b := impl.Target{ID: "scan-b", Subjects: []string{"svc-a"}}
	ra, err := e.Run(a, impl.Budget{MaxRounds: 6, MaxFindings: 10})
	if err != nil {
		t.Fatalf("run scan-a: %v", err)
	}
	rb, err := e.Run(b, impl.Budget{MaxRounds: 6, MaxFindings: 10})
	if err != nil {
		t.Fatalf("run scan-b: %v", err)
	}
	if len(ra.Findings) != 2 || len(rb.Findings) != 2 {
		t.Fatalf("findings a=%d b=%d, want 2 per target", len(ra.Findings), len(rb.Findings))
	}
	if ra.Findings[1].ID == rb.Findings[1].ID {
		t.Fatalf("finding ids must be unique across targets, both got %q", ra.Findings[1].ID)
	}
	again, err := e.Run(a, impl.Budget{MaxRounds: 6, MaxFindings: 10})
	if err != nil {
		t.Fatalf("rerun scan-a: %v", err)
	}
	if len(again.Findings) != 0 {
		t.Fatalf("rerun of scan-a produced %d new findings, want 0", len(again.Findings))
	}
	if got := e.Findings("scan-b"); len(got) != 2 {
		t.Fatalf("scan-b findings = %d, want 2", len(got))
	}
}

func TestExplainRebuildsDependencyChain(t *testing.T) {
	e := cascadeEngine(t)
	res, err := e.Run(cascadeTarget(), impl.Budget{MaxRounds: 6, MaxFindings: 10})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Findings) != 2 {
		t.Fatalf("findings = %d, want 2", len(res.Findings))
	}
	finding := res.Findings[1]
	chain, err := e.Explain(finding.ID)
	if err != nil {
		t.Fatalf("explain %s: %v", finding.ID, err)
	}
	if chain.FindingID != finding.ID || chain.RuleID != "rule-exploit" {
		t.Fatalf("chain header = %+v, want finding %s produced by rule-exploit", chain, finding.ID)
	}
	if len(chain.Steps) != 1 {
		t.Fatalf("chain steps = %d, want the single required evidence item", len(chain.Steps))
	}
	step := chain.Steps[0]
	if step.Kind != "exposed-port" || step.Subject != "svc-a" || step.RuleID != "rule-exposure" || step.Round != 1 {
		t.Fatalf("chain step = %+v, want exposed-port for svc-a produced by rule-exposure in round 1", step)
	}
	if _, err := e.Explain("finding-404"); !errors.Is(err, impl.ErrUnknownFinding) {
		t.Fatalf("unknown finding must fail with ErrUnknownFinding, got %v", err)
	}
	if _, err := e.Explain(""); !errors.Is(err, impl.ErrUnknownFinding) {
		t.Fatalf("empty finding id must fail with ErrUnknownFinding, got %v", err)
	}
}

func TestResetClearsTargetLedger(t *testing.T) {
	e := cascadeEngine(t)
	if _, err := e.Run(cascadeTarget(), impl.Budget{MaxRounds: 6, MaxFindings: 10}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if err := e.Reset("scan-a"); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if got := e.Findings("scan-a"); len(got) != 0 {
		t.Fatalf("findings after reset = %d, want 0", len(got))
	}
	if got := e.Evidence("scan-a"); len(got) != 0 {
		t.Fatalf("evidence after reset = %d, want 0", len(got))
	}
	res, err := e.Run(cascadeTarget(), impl.Budget{MaxRounds: 6, MaxFindings: 10})
	if err != nil {
		t.Fatalf("run after reset: %v", err)
	}
	if len(res.Findings) != 2 {
		t.Fatalf("findings after reset = %d, want the cascade to replay", len(res.Findings))
	}
	if got := e.Rules(); len(got) != 2 {
		t.Fatalf("rules after reset = %v, want the registry untouched", got)
	}
}

func TestConcurrentRunsAreRejectedNotMixed(t *testing.T) {
	e := cascadeEngine(t)
	const workers = 8
	var wg sync.WaitGroup
	errs := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			target := impl.Target{ID: fmt.Sprintf("scan-%02d", i), Subjects: []string{"svc-a"}}
			_, err := e.Run(target, impl.Budget{MaxRounds: 6, MaxFindings: 10})
			errs[i] = err
		}(i)
	}
	wg.Wait()
	started, refused := 0, 0
	for i, err := range errs {
		targetID := fmt.Sprintf("scan-%02d", i)
		switch {
		case err == nil:
			started++
			if got := len(e.Findings(targetID)); got != 2 {
				t.Fatalf("%s ran but stored %d findings, want 2", targetID, got)
			}
		case errors.Is(err, impl.ErrRunBusy):
			refused++
			if got := len(e.Findings(targetID)); got != 0 {
				t.Fatalf("%s was refused but stored %d findings, want 0", targetID, got)
			}
		default:
			t.Fatalf("%s returned an unexpected error: %v", targetID, err)
		}
	}
	if started == 0 {
		t.Fatalf("no concurrent run was accepted")
	}
	if started+refused != workers {
		t.Fatalf("started=%d refused=%d, want %d accounted runs", started, refused, workers)
	}
}

func TestSnapshotRestoreKeepsFindingsAndChains(t *testing.T) {
	e := cascadeEngine(t)
	res, err := e.Run(cascadeTarget(), impl.Budget{MaxRounds: 6, MaxFindings: 10})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Findings) != 2 {
		t.Fatalf("findings = %d, want 2", len(res.Findings))
	}
	data, err := e.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewEvidenceRuleEngine()
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.Rules(); len(got) != 2 {
		t.Fatalf("restored rules = %v, want 2", got)
	}
	if got := restored.Findings("scan-a"); len(got) != 2 {
		t.Fatalf("restored findings = %d, want 2", len(got))
	}
	chain, err := restored.Explain(res.Findings[1].ID)
	if err != nil {
		t.Fatalf("explain after restore: %v", err)
	}
	if len(chain.Steps) != 1 || chain.Steps[0].RuleID != "rule-exposure" {
		t.Fatalf("restored chain = %+v, want the rule-exposure step", chain)
	}
	again, err := restored.Run(cascadeTarget(), impl.Budget{MaxRounds: 6, MaxFindings: 10})
	if err != nil {
		t.Fatalf("run after restore: %v", err)
	}
	if len(again.Findings) != 0 {
		t.Fatalf("run after restore produced %d findings, want 0 because the ledger survived", len(again.Findings))
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	e := cascadeEngine(t)
	if _, err := e.Run(cascadeTarget(), impl.Budget{MaxRounds: 6, MaxFindings: 10}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if err := e.Restore([]byte("{not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := e.Findings("scan-a"); len(got) != 2 {
		t.Fatalf("findings after rejected restore = %d, want 2", len(got))
	}
	if got := e.Evidence("scan-a"); len(got) != 2 {
		t.Fatalf("evidence after rejected restore = %d, want 2", len(got))
	}
}
