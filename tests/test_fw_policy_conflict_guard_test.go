package task

import (
	"errors"
	"testing"

	impl "gsb/batch49-go-fix-a-08"
)

func testConfig() impl.GuardConfig {
	return impl.GuardConfig{MaxRules: 16, DefaultAction: impl.ActionDeny}
}

func newGuard(t *testing.T, cfg impl.GuardConfig) *impl.PolicyConflictGuard {
	t.Helper()
	g, err := impl.NewPolicyConflictGuard(cfg)
	if err != nil {
		t.Fatalf("new policy guard: %v", err)
	}
	return g
}

func addRule(t *testing.T, g *impl.PolicyConflictGuard, rule impl.Rule) int {
	t.Helper()
	id, err := g.AddRule(rule)
	if err != nil {
		t.Fatalf("add rule %+v: %v", rule, err)
	}
	return id
}

func matchingRule(prio int, action, proto string, port int) impl.Rule {
	return impl.Rule{Priority: prio, Action: action, Proto: proto, DstPort: port}
}

func TestNewGuardRequiresDenyDefault(t *testing.T) {
	if _, err := impl.NewPolicyConflictGuard(impl.GuardConfig{MaxRules: 8, DefaultAction: impl.ActionAllow}); !errors.Is(err, impl.ErrBadConfig) {
		t.Fatalf("allow default must fail with ErrBadConfig, got %v", err)
	}
	if _, err := impl.NewPolicyConflictGuard(impl.GuardConfig{MaxRules: 0, DefaultAction: impl.ActionDeny}); !errors.Is(err, impl.ErrBadConfig) {
		t.Fatalf("zero rule capacity must fail with ErrBadConfig, got %v", err)
	}
}

func TestAddRuleValidatesFields(t *testing.T) {
	g := newGuard(t, testConfig())
	cases := []impl.Rule{
		{Priority: 10, Action: "log", Proto: impl.ProtoTCP, DstPort: 80},
		{Priority: 10, Action: impl.ActionAllow, Proto: "sctp", DstPort: 80},
		{Priority: 10, Action: impl.ActionAllow, Proto: impl.ProtoTCP, DstPort: 70000},
		{Priority: 10, Action: impl.ActionAllow, Proto: impl.ProtoICMP, DstPort: 22},
	}
	for _, rule := range cases {
		if _, err := g.AddRule(rule); !errors.Is(err, impl.ErrInvalidRule) {
			t.Fatalf("rule %+v must fail with ErrInvalidRule, got %v", rule, err)
		}
	}
	if got := len(g.Rules()); got != 0 {
		t.Fatalf("rejected rules must not be stored, stored = %d", got)
	}
}

func TestAddRuleAssignsIDsAndRejectsDuplicate(t *testing.T) {
	g := newGuard(t, testConfig())
	if id := addRule(t, g, matchingRule(10, impl.ActionAllow, impl.ProtoTCP, 80)); id != 1 {
		t.Fatalf("first rule id = %d, want 1", id)
	}
	if id := addRule(t, g, matchingRule(10, impl.ActionDeny, impl.ProtoUDP, 53)); id != 2 {
		t.Fatalf("second rule id = %d, want 2", id)
	}
	dup := matchingRule(20, impl.ActionAllow, impl.ProtoTCP, 443)
	dup.ID = 2
	if _, err := g.AddRule(dup); !errors.Is(err, impl.ErrDuplicateRuleID) {
		t.Fatalf("explicit duplicate id must fail with ErrDuplicateRuleID, got %v", err)
	}
}

func TestAddRuleRejectsMalformedCIDR(t *testing.T) {
	g := newGuard(t, testConfig())
	bad := impl.Rule{Priority: 10, Action: impl.ActionAllow, Proto: impl.ProtoTCP, SrcCIDR: "10.0.0.1/8"}
	if _, err := g.AddRule(bad); !errors.Is(err, impl.ErrInvalidRule) {
		t.Fatalf("cidr with host bits must fail with ErrInvalidRule, got %v", err)
	}
	bad.DstCIDR = "10.0.0.0/33"
	bad.SrcCIDR = ""
	if _, err := g.AddRule(bad); !errors.Is(err, impl.ErrInvalidRule) {
		t.Fatalf("out of range prefix length must fail with ErrInvalidRule, got %v", err)
	}
}

func TestEvaluateFirstMatchByPriority(t *testing.T) {
	g := newGuard(t, testConfig())
	denyID := addRule(t, g, matchingRule(100, impl.ActionDeny, impl.ProtoTCP, 443))
	addRule(t, g, matchingRule(10, impl.ActionAllow, impl.ProtoTCP, 0))
	got, err := g.Evaluate(impl.Packet{SrcIP: "192.0.2.10", DstIP: "192.0.2.20", Proto: impl.ProtoTCP, DstPort: 443})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got.Allow || got.Action != impl.ActionDeny || got.RuleID != denyID || got.Reason != impl.ReasonRuleMatch {
		t.Fatalf("decision = %+v, want deny by rule %d", got, denyID)
	}
}

func TestDefaultDenyWhenNoMatch(t *testing.T) {
	g := newGuard(t, testConfig())
	got, err := g.Evaluate(impl.Packet{SrcIP: "198.51.100.1", DstIP: "198.51.100.2", Proto: impl.ProtoUDP, DstPort: 53})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got.Allow || got.RuleID != 0 || got.Reason != impl.ReasonDefaultDeny {
		t.Fatalf("decision = %+v, want the default deny tail", got)
	}
	if n := g.DefaultDenied(); n != 1 {
		t.Fatalf("default denied counter = %d, want 1", n)
	}
}

func TestRuleMatchesCIDRRange(t *testing.T) {
	g := newGuard(t, testConfig())
	rule := matchingRule(10, impl.ActionAllow, impl.ProtoTCP, 0)
	rule.SrcCIDR = "10.0.0.0/8"
	id := addRule(t, g, rule)
	got, err := g.Evaluate(impl.Packet{SrcIP: "10.9.8.7", DstIP: "203.0.113.9", Proto: impl.ProtoTCP, DstPort: 8080})
	if err != nil {
		t.Fatalf("evaluate inside cidr: %v", err)
	}
	if !got.Allow || got.RuleID != id {
		t.Fatalf("inside cidr decision = %+v, want allow by rule %d", got, id)
	}
	got, err = g.Evaluate(impl.Packet{SrcIP: "11.0.0.1", DstIP: "203.0.113.9", Proto: impl.ProtoTCP, DstPort: 8080})
	if err != nil {
		t.Fatalf("evaluate outside cidr: %v", err)
	}
	if got.Allow || got.Reason != impl.ReasonDefaultDeny {
		t.Fatalf("outside cidr decision = %+v, want default deny", got)
	}
	if _, err := g.Evaluate(impl.Packet{SrcIP: "not-an-ip", Proto: impl.ProtoTCP}); !errors.Is(err, impl.ErrBadPacket) {
		t.Fatalf("malformed addresses must fail with ErrBadPacket, got %v", err)
	}
}

func TestCheckReportsShadowed(t *testing.T) {
	g := newGuard(t, testConfig())
	wideID := addRule(t, g, matchingRule(100, impl.ActionAllow, impl.ProtoAny, 0))
	report, err := g.Check(matchingRule(50, impl.ActionDeny, impl.ProtoTCP, 443))
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !report.Shadowed || report.ShadowedBy != wideID {
		t.Fatalf("report = %+v, want shadowed by rule %d", report, wideID)
	}
}

func TestCheckReportsContradiction(t *testing.T) {
	g := newGuard(t, testConfig())
	allowID := addRule(t, g, matchingRule(50, impl.ActionAllow, impl.ProtoAny, 0))
	report, err := g.Check(matchingRule(50, impl.ActionDeny, impl.ProtoAny, 0))
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !report.Contradiction || report.SameSelectorWith != allowID {
		t.Fatalf("report = %+v, want contradiction with rule %d", report, allowID)
	}
	if _, err := g.AddRule(matchingRule(50, impl.ActionDeny, impl.ProtoAny, 0)); !errors.Is(err, impl.ErrRuleConflict) {
		t.Fatalf("contradicting rule must fail with ErrRuleConflict, got %v", err)
	}
	if got := len(g.Rules()); got != 1 {
		t.Fatalf("rejected contradiction must not be stored, rules = %d", got)
	}
}

func TestSamePriorityKeepsInsertionOrderThenReorder(t *testing.T) {
	g := newGuard(t, testConfig())
	first := addRule(t, g, matchingRule(50, impl.ActionAllow, impl.ProtoTCP, 443))
	second := addRule(t, g, matchingRule(50, impl.ActionAllow, impl.ProtoTCP, 0))
	pkt := impl.Packet{SrcIP: "192.0.2.1", DstIP: "192.0.2.2", Proto: impl.ProtoTCP, DstPort: 443}
	got, err := g.Evaluate(pkt)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got.RuleID != first {
		t.Fatalf("first inserted same priority rule must win, got rule %d want %d", got.RuleID, first)
	}
	if err := g.Reorder([]int{second, first}); err != nil {
		t.Fatalf("reorder: %v", err)
	}
	got, err = g.Evaluate(pkt)
	if err != nil {
		t.Fatalf("evaluate after reorder: %v", err)
	}
	if got.RuleID != second {
		t.Fatalf("reorder must change the tie break, got rule %d want %d", got.RuleID, second)
	}
}

func TestReorderRejectsPartialPermutation(t *testing.T) {
	g := newGuard(t, testConfig())
	first := addRule(t, g, matchingRule(50, impl.ActionAllow, impl.ProtoTCP, 443))
	addRule(t, g, matchingRule(50, impl.ActionAllow, impl.ProtoTCP, 0))
	if err := g.Reorder([]int{first}); !errors.Is(err, impl.ErrBadOrder) {
		t.Fatalf("partial permutation must fail with ErrBadOrder, got %v", err)
	}
	got, err := g.Evaluate(impl.Packet{SrcIP: "192.0.2.1", DstIP: "192.0.2.2", Proto: impl.ProtoTCP, DstPort: 443})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got.RuleID != first {
		t.Fatalf("a rejected reorder must not change the order, got rule %d want %d", got.RuleID, first)
	}
}

func TestRemoveRuleClearsHits(t *testing.T) {
	g := newGuard(t, testConfig())
	id := addRule(t, g, matchingRule(10, impl.ActionAllow, impl.ProtoTCP, 0))
	pkt := impl.Packet{SrcIP: "192.0.2.1", DstIP: "192.0.2.2", Proto: impl.ProtoTCP, DstPort: 80}
	for i := 0; i < 2; i++ {
		if _, err := g.Evaluate(pkt); err != nil {
			t.Fatalf("evaluate: %v", err)
		}
	}
	if got := g.Hits(id); got != 2 {
		t.Fatalf("hits = %d, want 2", got)
	}
	if err := g.RemoveRule(id); err != nil {
		t.Fatalf("remove rule: %v", err)
	}
	if got := g.Hits(id); got != 0 {
		t.Fatalf("hits after removal = %d, want 0", got)
	}
	if err := g.RemoveRule(id); !errors.Is(err, impl.ErrRuleNotFound) {
		t.Fatalf("second removal must fail with ErrRuleNotFound, got %v", err)
	}
}

func TestRuleLimitEnforced(t *testing.T) {
	g := newGuard(t, impl.GuardConfig{MaxRules: 2, DefaultAction: impl.ActionDeny})
	addRule(t, g, matchingRule(10, impl.ActionAllow, impl.ProtoTCP, 80))
	addRule(t, g, matchingRule(10, impl.ActionAllow, impl.ProtoTCP, 81))
	if _, err := g.AddRule(matchingRule(10, impl.ActionAllow, impl.ProtoTCP, 82)); !errors.Is(err, impl.ErrRuleLimit) {
		t.Fatalf("third rule must fail with ErrRuleLimit, got %v", err)
	}
	if got := len(g.Rules()); got != 2 {
		t.Fatalf("rule count = %d, want 2", got)
	}
}

func TestSnapshotRestoreKeepsOrderHitsAndDefault(t *testing.T) {
	g := newGuard(t, testConfig())
	ruleID := addRule(t, g, matchingRule(100, impl.ActionAllow, impl.ProtoTCP, 443))
	addRule(t, g, matchingRule(10, impl.ActionDeny, impl.ProtoTCP, 0))
	pkt := impl.Packet{SrcIP: "192.0.2.1", DstIP: "192.0.2.2", Proto: impl.ProtoTCP, DstPort: 443}
	for i := 0; i < 2; i++ {
		if _, err := g.Evaluate(pkt); err != nil {
			t.Fatalf("evaluate: %v", err)
		}
	}
	if _, err := g.Evaluate(impl.Packet{SrcIP: "192.0.2.1", DstIP: "192.0.2.2", Proto: impl.ProtoUDP, DstPort: 53}); err != nil {
		t.Fatalf("evaluate miss: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := newGuard(t, testConfig())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	got, err := restored.Evaluate(pkt)
	if err != nil {
		t.Fatalf("evaluate after restore: %v", err)
	}
	if got.RuleID != ruleID {
		t.Fatalf("restored first match = %d, want %d", got.RuleID, ruleID)
	}
	if hits := restored.Hits(ruleID); hits != 3 {
		t.Fatalf("restored hits = %d, want 3", hits)
	}
	if n := restored.DefaultDenied(); n != 1 {
		t.Fatalf("restored default deny counter = %d, want 1", n)
	}
	if rules := restored.Rules(); len(rules) != 2 || rules[0].ID != ruleID {
		t.Fatalf("restored rule order = %+v, want rule %d first", rules, ruleID)
	}
}

func TestRestoreRejectsGarbage(t *testing.T) {
	g := newGuard(t, testConfig())
	if err := g.Restore([]byte("[]")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("garbage snapshot must fail with ErrBadSnapshot, got %v", err)
	}
}
