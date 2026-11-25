package task

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	impl "gsb/batch49-go-safety-b-11"
)

func ingressPolicies() []impl.Policy {
	return []impl.Policy{{
		Name:      "allow-billing",
		Namespace: "pay",
		Selector:  map[string]string{"app": "bill"},
		Ingress: []impl.Peer{{
			NamespaceSelector: map[string]string{"team": "pay"},
			PodSelector:       map[string]string{"app": "api"},
			Ports:             []impl.PortRef{{Port: 8080, Protocol: impl.ProtocolTCP}},
		}},
	}}
}

func netpolWorkloads() []impl.Workload {
	return []impl.Workload{
		{ID: "logger", Namespace: "ops", Labels: map[string]string{"app": "logger"}},
		{ID: "bill", Namespace: "pay", Labels: map[string]string{"app": "bill"}, Ports: map[string]int{"http": 8080}},
	}
}

func matchingFlow() impl.Flow {
	return impl.Flow{
		Direction:     impl.DirectionIngress,
		PeerIP:        "10.2.3.4",
		PeerNamespace: "pay",
		PeerLabels:    map[string]string{"app": "api"},
		Port:          8080,
		Protocol:      impl.ProtocolTCP,
	}
}

func TestUnselectedWorkloadKeepsDefaultAllow(t *testing.T) {
	c := impl.NewNetPolCompiler()
	if _, err := c.Compile(ingressPolicies(), netpolWorkloads()); err != nil {
		t.Fatalf("compile must succeed: %v", err)
	}
	d, r := c.Evaluate("logger", impl.Flow{Direction: impl.DirectionIngress, Port: 443, Protocol: impl.ProtocolTCP})
	if d != impl.DecisionAllow || r.Code != impl.ReasonDefaultAllow {
		t.Fatalf("unselected workload got decision=%q reason=%q, want allow/default_allow", d, r.Code)
	}
	d, r = c.Evaluate("logger", impl.Flow{Direction: impl.DirectionEgress, Port: 5432, Protocol: impl.ProtocolTCP})
	if d != impl.DecisionAllow || r.Code != impl.ReasonDefaultAllow {
		t.Fatalf("unselected egress got decision=%q reason=%q, want allow/default_allow", d, r.Code)
	}
	if got := c.WorkloadIDs(); !reflect.DeepEqual(got, []string{"bill", "logger"}) {
		t.Fatalf("workload ids = %v, want the sorted ids [bill logger]", got)
	}
}

func TestSelectedWorkloadWithoutMatchingRuleIsDenied(t *testing.T) {
	c := impl.NewNetPolCompiler()
	if _, err := c.Compile(ingressPolicies(), netpolWorkloads()); err != nil {
		t.Fatalf("compile must succeed: %v", err)
	}
	d, r := c.Evaluate("bill", matchingFlow())
	if d != impl.DecisionAllow || r.Code != impl.ReasonRuleMatch || r.Policy != "allow-billing" || r.Rule != 0 {
		t.Fatalf("matching flow got decision=%q reason=%+v, want allow/rule_match on allow-billing rule 0", d, r)
	}
	miss := matchingFlow()
	miss.PeerLabels = map[string]string{"app": "web"}
	d, r = c.Evaluate("bill", miss)
	if d != impl.DecisionDeny || r.Code != impl.ReasonNoRule {
		t.Fatalf("non matching peer got decision=%q reason=%q, want deny/no_rule", d, r.Code)
	}
}

func TestSelectorMatchesNamespaceAndPodLabels(t *testing.T) {
	if !impl.SelectorMatches(map[string]string{}, map[string]string{"anything": "ok"}) {
		t.Fatalf("an empty selector must match every label set")
	}
	if !impl.SelectorMatches(map[string]string{"app": "api"}, map[string]string{"app": "api", "team": "pay"}) {
		t.Fatalf("a subset selector must match")
	}
	if impl.SelectorMatches(map[string]string{"app": "api"}, map[string]string{"app": "web"}) {
		t.Fatalf("a selector with a different value must not match")
	}
	if impl.SelectorMatches(map[string]string{"app": "api"}, map[string]string{"team": "pay"}) {
		t.Fatalf("a selector with a missing key must not match")
	}
	c := impl.NewNetPolCompiler()
	if _, err := c.Compile(ingressPolicies(), netpolWorkloads()); err != nil {
		t.Fatalf("compile must succeed: %v", err)
	}
	noPod := matchingFlow()
	noPod.PeerLabels = nil
	if d, _ := c.Evaluate("bill", noPod); d != impl.DecisionDeny {
		t.Fatalf("flow without the pod labels must be denied, got %q", d)
	}
	noNS := matchingFlow()
	noNS.PeerNamespace = "ops"
	if d, _ := c.Evaluate("bill", noNS); d != impl.DecisionDeny {
		t.Fatalf("flow from another namespace must be denied, got %q", d)
	}
}

func TestIPBlockExceptSubtractsFromAllowList(t *testing.T) {
	policies := []impl.Policy{{
		Name:      "allow-cidr",
		Namespace: "pay",
		Selector:  map[string]string{"app": "bill"},
		Ingress: []impl.Peer{{
			IPBlock: "10.0.0.0/8",
			Except:  []string{"10.1.0.0/16"},
			Ports:   []impl.PortRef{{Port: 80, Protocol: impl.ProtocolTCP}},
		}},
	}}
	c := impl.NewNetPolCompiler()
	if _, err := c.Compile(policies, netpolWorkloads()); err != nil {
		t.Fatalf("compile must succeed: %v", err)
	}
	inside := impl.Flow{Direction: impl.DirectionIngress, PeerIP: "10.2.3.4", Port: 80, Protocol: impl.ProtocolTCP}
	if d, _ := c.Evaluate("bill", inside); d != impl.DecisionAllow {
		t.Fatalf("address inside the block outside except must be allowed, got %q", d)
	}
	excluded := inside
	excluded.PeerIP = "10.1.9.9"
	if d, _ := c.Evaluate("bill", excluded); d != impl.DecisionDeny {
		t.Fatalf("address inside an except subnet must be denied, got %q", d)
	}
	outside := inside
	outside.PeerIP = "11.0.0.1"
	if d, _ := c.Evaluate("bill", outside); d != impl.DecisionDeny {
		t.Fatalf("address outside the block must be denied, got %q", d)
	}
}

func TestPortRangeNormalisedAtCompileTime(t *testing.T) {
	policies := []impl.Policy{{
		Name:      "range",
		Namespace: "pay",
		Selector:  map[string]string{"app": "bill"},
		Ingress: []impl.Peer{{
			NamespaceSelector: map[string]string{"team": "pay"},
			Ports:             []impl.PortRef{{Port: 8000, EndPort: 8080, Protocol: impl.ProtocolTCP}},
		}},
	}}
	c := impl.NewNetPolCompiler()
	if _, err := c.Compile(policies, netpolWorkloads()); err != nil {
		t.Fatalf("compile must succeed: %v", err)
	}
	entries := c.Entries("bill")
	if len(entries) != 1 {
		t.Fatalf("entries for bill = %d, want 1 normalised range", len(entries))
	}
	if entries[0].PortStart != 8000 || entries[0].PortEnd != 8080 {
		t.Fatalf("entry ports = %d-%d, want 8000-8080", entries[0].PortStart, entries[0].PortEnd)
	}
	atEdge := impl.Flow{Direction: impl.DirectionIngress, PeerNamespace: "pay", Port: 8080, Protocol: impl.ProtocolTCP}
	if d, _ := c.Evaluate("bill", atEdge); d != impl.DecisionAllow {
		t.Fatalf("upper edge of the range must be allowed, got %q", d)
	}
	above := atEdge
	above.Port = 8081
	if d, _ := c.Evaluate("bill", above); d != impl.DecisionDeny {
		t.Fatalf("port just above the range must be denied, got %q", d)
	}
}

func TestNamedPortResolvedFromWorkload(t *testing.T) {
	policies := []impl.Policy{{
		Name:      "named",
		Namespace: "pay",
		Selector:  map[string]string{"app": "bill"},
		Ingress: []impl.Peer{{
			NamespaceSelector: map[string]string{"team": "pay"},
			Ports:             []impl.PortRef{{Name: "http", Protocol: impl.ProtocolTCP}},
		}},
	}}
	c := impl.NewNetPolCompiler()
	if _, err := c.Compile(policies, netpolWorkloads()); err != nil {
		t.Fatalf("compile must succeed: %v", err)
	}
	flow := impl.Flow{Direction: impl.DirectionIngress, PeerNamespace: "pay", Port: 8080, Protocol: impl.ProtocolTCP}
	if d, _ := c.Evaluate("bill", flow); d != impl.DecisionAllow {
		t.Fatalf("resolved named port must allow the mapped port, got %q", d)
	}
	flow.Port = 8081
	if d, _ := c.Evaluate("bill", flow); d != impl.DecisionDeny {
		t.Fatalf("resolved named port must not allow an unrelated port, got %q", d)
	}
}

func TestCompileIsDeterministicAcrossInputOrders(t *testing.T) {
	workloads := netpolWorkloads()
	p1 := impl.Policy{
		Name:      "p-one",
		Namespace: "pay",
		Selector:  map[string]string{"app": "bill"},
		Ingress:   []impl.Peer{{NamespaceSelector: map[string]string{"team": "pay"}, Ports: []impl.PortRef{{Port: 80}}}},
		Egress:    []impl.Peer{{NamespaceSelector: map[string]string{"team": "ops"}, Ports: []impl.PortRef{{Port: 53, Protocol: impl.ProtocolUDP}}}},
	}
	p2 := impl.Policy{
		Name:      "p-two",
		Namespace: "pay",
		Selector:  map[string]string{"app": "bill"},
		Ingress:   []impl.Peer{{IPBlock: "10.0.0.0/8", Ports: []impl.PortRef{{Port: 8080, EndPort: 8090}}}},
	}
	a := impl.NewNetPolCompiler()
	m1, err := a.Compile([]impl.Policy{p1, p2}, workloads)
	if err != nil {
		t.Fatalf("first compile: %v", err)
	}
	b := impl.NewNetPolCompiler()
	m2, err := b.Compile([]impl.Policy{p2, p1}, []impl.Workload{workloads[0], workloads[1]})
	if err != nil {
		t.Fatalf("second compile: %v", err)
	}
	if !reflect.DeepEqual(m1.Workloads, m2.Workloads) {
		t.Fatalf("workload order depends on input order: %v vs %v", m1.Workloads, m2.Workloads)
	}
	if !reflect.DeepEqual(m1.Entries, m2.Entries) {
		t.Fatalf("entries depend on input order:\n%+v\n%+v", m1.Entries, m2.Entries)
	}
	if !reflect.DeepEqual(a.PolicyNames(), []string{"p-one", "p-two"}) {
		t.Fatalf("policy names = %v, want sorted [p-one p-two]", a.PolicyNames())
	}
}

func TestDiffReportsAddedEntries(t *testing.T) {
	workloads := netpolWorkloads()
	base := impl.NewNetPolCompiler()
	m1, err := base.Compile(ingressPolicies(), workloads)
	if err != nil {
		t.Fatalf("base compile: %v", err)
	}
	extra := ingressPolicies()
	extra = append(extra, impl.Policy{
		Name:      "allow-logger",
		Namespace: "ops",
		Selector:  map[string]string{"app": "logger"},
		Ingress:   []impl.Peer{{NamespaceSelector: map[string]string{"team": "ops"}, Ports: []impl.PortRef{{Port: 9000}}}},
	})
	next := impl.NewNetPolCompiler()
	m2, err := next.Compile(extra, workloads)
	if err != nil {
		t.Fatalf("next compile: %v", err)
	}
	added := impl.Diff(m1, m2)
	if len(added) != 1 {
		t.Fatalf("diff added = %d changes, want exactly 1 for the new workload rule", len(added))
	}
	if added[0].Kind != impl.ChangeAdded || added[0].WorkloadID != "logger" || added[0].Policy != "allow-logger" {
		t.Fatalf("added change = %+v, want added/logger/allow-logger", added[0])
	}
	if added[0].PortStart != 9000 || added[0].PortEnd != 9000 {
		t.Fatalf("added change ports = %d-%d, want 9000-9000", added[0].PortStart, added[0].PortEnd)
	}
	removed := impl.Diff(m2, m1)
	if len(removed) != 1 || removed[0].Kind != impl.ChangeRemoved || removed[0].WorkloadID != "logger" {
		t.Fatalf("reverse diff = %+v, want one removed change for logger", removed)
	}
}

func TestDiffOfIdenticalMatricesIsEmpty(t *testing.T) {
	a := impl.NewNetPolCompiler()
	m1, err := a.Compile(ingressPolicies(), netpolWorkloads())
	if err != nil {
		t.Fatalf("first compile: %v", err)
	}
	b := impl.NewNetPolCompiler()
	m2, err := b.Compile(ingressPolicies(), netpolWorkloads())
	if err != nil {
		t.Fatalf("second compile: %v", err)
	}
	if changes := impl.Diff(m1, m2); len(changes) != 0 {
		t.Fatalf("identical matrices produced %d changes, want none", len(changes))
	}
}

func TestIngressAndEgressEvaluatedSeparately(t *testing.T) {
	policies := []impl.Policy{{
		Name:      "ingress-only",
		Namespace: "pay",
		Selector:  map[string]string{"app": "bill"},
		Ingress:   []impl.Peer{{NamespaceSelector: map[string]string{"team": "pay"}, Ports: []impl.PortRef{{Port: 80}}}},
	}}
	c := impl.NewNetPolCompiler()
	if _, err := c.Compile(policies, netpolWorkloads()); err != nil {
		t.Fatalf("compile must succeed: %v", err)
	}
	in := impl.Flow{Direction: impl.DirectionIngress, PeerNamespace: "pay", Port: 80, Protocol: impl.ProtocolTCP}
	if d, _ := c.Evaluate("bill", in); d != impl.DecisionAllow {
		t.Fatalf("declared ingress must be allowed, got %q", d)
	}
	out := impl.Flow{Direction: impl.DirectionEgress, PeerNamespace: "pay", Port: 80, Protocol: impl.ProtocolTCP}
	if d, r := c.Evaluate("bill", out); d != impl.DecisionDeny || r.Code != impl.ReasonNoRule {
		t.Fatalf("egress without rules got decision=%q reason=%q, want deny/no_rule", d, r.Code)
	}
}

func TestBlankAndDuplicatePolicyRejected(t *testing.T) {
	c := impl.NewNetPolCompiler()
	dup := []impl.Policy{{Name: "same"}, {Name: "same"}}
	if _, err := c.Compile(dup, netpolWorkloads()); !errors.Is(err, impl.ErrDuplicatePolicy) {
		t.Fatalf("duplicate policy names must fail with ErrDuplicatePolicy, got %v", err)
	}
	blank := []impl.Policy{{Name: "  "}}
	if _, err := c.Compile(blank, netpolWorkloads()); !errors.Is(err, impl.ErrBlankPolicy) {
		t.Fatalf("blank policy name must fail with ErrBlankPolicy, got %v", err)
	}
	if err := impl.ValidatePolicies(blank); !errors.Is(err, impl.ErrBlankPolicy) {
		t.Fatalf("ValidatePolicies must reject a blank name with ErrBlankPolicy, got %v", err)
	}
	badPort := []impl.Policy{{Name: "bad", Ingress: []impl.Peer{{Ports: []impl.PortRef{{Port: 9000, EndPort: 80}}}}}}
	if err := impl.ValidatePolicies(badPort); !errors.Is(err, impl.ErrInvalidPolicy) {
		t.Fatalf("reversed port range must fail with ErrInvalidPolicy, got %v", err)
	}
}

func TestBlankAndDuplicateWorkloadRejected(t *testing.T) {
	c := impl.NewNetPolCompiler()
	if _, err := c.Compile(ingressPolicies(), []impl.Workload{{ID: ""}}); !errors.Is(err, impl.ErrBlankWorkload) {
		t.Fatalf("blank workload id must fail with ErrBlankWorkload, got %v", err)
	}
	dup := []impl.Workload{{ID: "bill"}, {ID: "bill"}}
	if _, err := c.Compile(ingressPolicies(), dup); !errors.Is(err, impl.ErrDuplicateWorkload) {
		t.Fatalf("duplicate workload id must fail with ErrDuplicateWorkload, got %v", err)
	}
	if d, r := c.Evaluate("bill", matchingFlow()); d != impl.DecisionDeny || r.Code != impl.ReasonUnknown {
		t.Fatalf("failed compile must not expose a matrix, got decision=%q reason=%q", d, r.Code)
	}
}

func TestSnapshotRestoreKeepsCompiledMatrix(t *testing.T) {
	c := impl.NewNetPolCompiler()
	if _, err := c.Compile(ingressPolicies(), netpolWorkloads()); err != nil {
		t.Fatalf("compile must succeed: %v", err)
	}
	data, err := c.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewNetPolCompiler()
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !reflect.DeepEqual(restored.WorkloadIDs(), []string{"bill", "logger"}) {
		t.Fatalf("restored workload ids = %v, want [bill logger]", restored.WorkloadIDs())
	}
	if d, r := restored.Evaluate("bill", matchingFlow()); d != impl.DecisionAllow || r.Code != impl.ReasonRuleMatch {
		t.Fatalf("restored matrix got decision=%q reason=%q, want allow/rule_match", d, r.Code)
	}
	if d, _ := restored.Evaluate("logger", impl.Flow{Direction: impl.DirectionEgress, Port: 1}); d != impl.DecisionAllow {
		t.Fatalf("restored default allow lost, got %q", d)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	c := impl.NewNetPolCompiler()
	if _, err := c.Compile(ingressPolicies(), netpolWorkloads()); err != nil {
		t.Fatalf("compile must succeed: %v", err)
	}
	if err := c.Restore([]byte("{broken")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if d, _ := c.Evaluate("bill", matchingFlow()); d != impl.DecisionAllow {
		t.Fatalf("rejected restore must leave the previous matrix serving, got %q", d)
	}
}

func TestConcurrentCompileAndEvaluateIsConsistent(t *testing.T) {
	c := impl.NewNetPolCompiler()
	if _, err := c.Compile(ingressPolicies(), netpolWorkloads()); err != nil {
		t.Fatalf("compile must succeed: %v", err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			m, err := c.Compile(ingressPolicies(), netpolWorkloads())
			if err != nil {
				errs <- err
				return
			}
			if !reflect.DeepEqual(m.Workloads, []string{"bill", "logger"}) {
				errs <- fmt.Errorf("worker %d saw workloads %v", i, m.Workloads)
				return
			}
			if d, _ := c.Evaluate("bill", matchingFlow()); d != impl.DecisionAllow {
				errs <- fmt.Errorf("worker %d evaluated %q", i, d)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent compile/evaluate failed: %v", err)
	}
}
