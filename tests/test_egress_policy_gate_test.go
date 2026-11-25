package task

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	impl "gsb/batch49-go-safety-b-12"
)

func target(scheme, host string, port int, method string) impl.Target {
	return impl.Target{Scheme: scheme, Host: host, Port: port, Path: "/", Method: method}
}

func TestAuthorizeExactRuleIssuesBoundedGrant(t *testing.T) {
	cfg := impl.Config{
		Rules:     []impl.Rule{{Tenant: "t1", Pattern: "api.corp.example", Ports: []int{443}, Methods: []string{"POST", "GET"}, Priority: 1}},
		TTL:       30,
		MaxHops:   2,
		MaxGrants: 20,
	}
	g := impl.NewEgressGate(cfg)
	if err := g.ValidateRules(); err != nil {
		t.Fatalf("valid rules rejected: %v", err)
	}
	gr, err := g.Authorize(impl.EgressRequest{Tenant: "t1", Target: target(impl.SchemeHTTPS, "api.corp.example", 443, "GET")})
	if err != nil {
		t.Fatalf("exact rule must authorise the request: %v", err)
	}
	if gr.ID == "" || gr.Tenant != "t1" {
		t.Fatalf("grant identity = %+v, want a non empty id bound to t1", gr)
	}
	if gr.Scheme != impl.SchemeHTTPS || gr.Host != "api.corp.example" || gr.Port != 443 {
		t.Fatalf("grant destination = %s://%s:%d, want https://api.corp.example:443", gr.Scheme, gr.Host, gr.Port)
	}
	if len(gr.Methods) != 2 || gr.Methods[0] != "GET" || gr.Methods[1] != "POST" {
		t.Fatalf("grant methods = %v, want sorted [GET POST]", gr.Methods)
	}
	if gr.ExpiresAt != 30 || gr.Hops != 0 {
		t.Fatalf("grant expiry=%d hops=%d, want 30 and 0", gr.ExpiresAt, gr.Hops)
	}
	if g.Clock() != 0 {
		t.Fatalf("fresh gate clock = %d, want 0", g.Clock())
	}
}

func TestSuffixWildcardAllowsSubdomain(t *testing.T) {
	cfg := impl.Config{
		Rules:     []impl.Rule{{Tenant: "t1", Pattern: "*.corp.example"}},
		TTL:       60,
		MaxHops:   2,
		MaxGrants: 10,
	}
	g := impl.NewEgressGate(cfg)
	req := impl.EgressRequest{Tenant: "t1", Target: target(impl.SchemeHTTPS, "api.corp.example", 443, "GET")}
	if _, err := g.Authorize(req); err != nil {
		t.Fatalf("subdomain must match the suffix wildcard: %v", err)
	}
	rule, kind, ok := g.MatchRule("t1", req.Target)
	if !ok || kind != impl.MatchSuffix || rule.Pattern != "*.corp.example" {
		t.Fatalf("match = %+v %q %v, want the suffix wildcard rule", rule, kind, ok)
	}
}

func TestCategoryRuleMatchesConfiguredMembers(t *testing.T) {
	cfg := impl.Config{
		Rules:      []impl.Rule{{Tenant: "t1", Pattern: "#partner", Ports: []int{8443}, Methods: []string{"GET"}}},
		Categories: map[string][]string{"partner": {"partner-a.example"}},
		TTL:        60,
		MaxHops:    2,
		MaxGrants:  10,
	}
	g := impl.NewEgressGate(cfg)
	req := impl.EgressRequest{Tenant: "t1", Target: target(impl.SchemeHTTPS, "api.partner-a.example", 8443, "GET")}
	if _, err := g.Authorize(req); err != nil {
		t.Fatalf("category member subdomain must be authorised: %v", err)
	}
	_, kind, ok := g.MatchRule("t1", req.Target)
	if !ok || kind != impl.MatchCategory {
		t.Fatalf("category match = %q %v, want category and true", kind, ok)
	}
}

func TestExactRuleBeatsSuffixForMethodSet(t *testing.T) {
	cfg := impl.Config{
		Rules: []impl.Rule{
			{Tenant: "t1", Pattern: "api.corp.example", Ports: []int{443}, Methods: []string{"GET"}, Priority: 1},
			{Tenant: "t1", Pattern: "*.corp.example", Ports: []int{443}, Methods: []string{"GET", "POST"}, Priority: 500},
		},
		TTL:       60,
		MaxHops:   2,
		MaxGrants: 10,
	}
	g := impl.NewEgressGate(cfg)
	post := impl.EgressRequest{Tenant: "t1", Target: target(impl.SchemeHTTPS, "api.corp.example", 443, "POST")}
	if _, err := g.Authorize(post); !errors.Is(err, impl.ErrDenied) {
		t.Fatalf("the exact rule only grants GET, POST must be denied, got %v", err)
	}
	_, kind, ok := g.MatchRule("t1", post.Target)
	if !ok || kind != impl.MatchExact {
		t.Fatalf("precedence match = %q %v, want exact and true", kind, ok)
	}
	get := impl.EgressRequest{Tenant: "t1", Target: target(impl.SchemeHTTPS, "api.corp.example", 443, "GET")}
	gr, err := g.Authorize(get)
	if err != nil {
		t.Fatalf("GET must be authorised by the exact rule: %v", err)
	}
	if len(gr.Methods) != 1 || gr.Methods[0] != "GET" {
		t.Fatalf("grant methods = %v, want only the exact rule method set [GET]", gr.Methods)
	}
}

func TestUnknownHostDenied(t *testing.T) {
	cfg := impl.Config{
		Rules:     []impl.Rule{{Tenant: "t1", Pattern: "api.corp.example", Ports: []int{443}}},
		TTL:       60,
		MaxHops:   2,
		MaxGrants: 10,
	}
	g := impl.NewEgressGate(cfg)
	req := impl.EgressRequest{Tenant: "t1", Target: target(impl.SchemeHTTPS, "unknown.example", 443, "GET")}
	if _, err := g.Authorize(req); !errors.Is(err, impl.ErrDenied) {
		t.Fatalf("host without a rule must be denied, got %v", err)
	}
}

func TestMethodOutsideRuleDenied(t *testing.T) {
	cfg := impl.Config{
		Rules:     []impl.Rule{{Tenant: "t1", Pattern: "api.corp.example", Ports: []int{443}, Methods: []string{"GET", "HEAD"}}},
		TTL:       60,
		MaxHops:   2,
		MaxGrants: 10,
	}
	g := impl.NewEgressGate(cfg)
	req := impl.EgressRequest{Tenant: "t1", Target: target(impl.SchemeHTTPS, "api.corp.example", 443, "DELETE")}
	if _, err := g.Authorize(req); !errors.Is(err, impl.ErrDenied) {
		t.Fatalf("DELETE is outside the rule method set and must be denied, got %v", err)
	}
}

func TestPortOutsideRuleDenied(t *testing.T) {
	cfg := impl.Config{
		Rules:     []impl.Rule{{Tenant: "t1", Pattern: "api.corp.example", Ports: []int{443}}},
		TTL:       60,
		MaxHops:   2,
		MaxGrants: 10,
	}
	g := impl.NewEgressGate(cfg)
	req := impl.EgressRequest{Tenant: "t1", Target: target(impl.SchemeHTTPS, "api.corp.example", 8443, "GET")}
	if _, err := g.Authorize(req); !errors.Is(err, impl.ErrDenied) {
		t.Fatalf("port outside the rule must be denied, got %v", err)
	}
}

func TestFollowRedirectIssuesNextHopGrant(t *testing.T) {
	cfg := impl.Config{
		Rules: []impl.Rule{
			{Tenant: "t1", Pattern: "a.corp.example", Ports: []int{443}, Methods: []string{"GET"}},
			{Tenant: "t1", Pattern: "b.corp.example", Ports: []int{443}, Methods: []string{"GET"}},
		},
		TTL:       60,
		MaxHops:   2,
		MaxGrants: 10,
	}
	g := impl.NewEgressGate(cfg)
	gr, err := g.Authorize(impl.EgressRequest{Tenant: "t1", Target: target(impl.SchemeHTTPS, "a.corp.example", 443, "GET")})
	if err != nil {
		t.Fatalf("authorize first hop: %v", err)
	}
	next, err := g.FollowRedirect(gr, target(impl.SchemeHTTPS, "b.corp.example", 443, "GET"))
	if err != nil {
		t.Fatalf("redirect to a whitelisted host must be authorised: %v", err)
	}
	if next.ID == gr.ID || next.Host != "b.corp.example" || next.Hops != 1 {
		t.Fatalf("next hop grant = %+v, want a new id for b with hops 1", next)
	}
	if len(next.Methods) != 1 || next.Methods[0] != "GET" {
		t.Fatalf("next hop methods = %v, want the unchanged set [GET]", next.Methods)
	}
	if _, ok := g.Grant(gr.ID); ok {
		t.Fatalf("the previous hop grant must be superseded")
	}
	if _, ok := g.Grant(next.ID); !ok {
		t.Fatalf("the new hop grant must be live")
	}
}

func TestFollowRedirectRejectsMethodWidening(t *testing.T) {
	cfg := impl.Config{
		Rules: []impl.Rule{
			{Tenant: "t1", Pattern: "a.corp.example", Ports: []int{443}, Methods: []string{"GET"}},
			{Tenant: "t1", Pattern: "b.corp.example", Ports: []int{443}, Methods: []string{"GET", "POST"}},
		},
		TTL:       60,
		MaxHops:   2,
		MaxGrants: 10,
	}
	g := impl.NewEgressGate(cfg)
	gr, err := g.Authorize(impl.EgressRequest{Tenant: "t1", Target: target(impl.SchemeHTTPS, "a.corp.example", 443, "GET")})
	if err != nil {
		t.Fatalf("authorize first hop: %v", err)
	}
	_, err = g.FollowRedirect(gr, target(impl.SchemeHTTPS, "b.corp.example", 443, "POST"))
	if !errors.Is(err, impl.ErrMethodWidened) {
		t.Fatalf("redirect widening GET to POST must fail with ErrMethodWidened, got %v", err)
	}
}

func TestFollowRedirectStopsAtHopLimit(t *testing.T) {
	cfg := impl.Config{
		Rules: []impl.Rule{
			{Tenant: "t1", Pattern: "a.corp.example", Ports: []int{443}, Methods: []string{"GET"}},
			{Tenant: "t1", Pattern: "b.corp.example", Ports: []int{443}, Methods: []string{"GET"}},
			{Tenant: "t1", Pattern: "c.corp.example", Ports: []int{443}, Methods: []string{"GET"}},
		},
		TTL:       60,
		MaxHops:   1,
		MaxGrants: 10,
	}
	g := impl.NewEgressGate(cfg)
	gr, err := g.Authorize(impl.EgressRequest{Tenant: "t1", Target: target(impl.SchemeHTTPS, "a.corp.example", 443, "GET")})
	if err != nil {
		t.Fatalf("authorize first hop: %v", err)
	}
	second, err := g.FollowRedirect(gr, target(impl.SchemeHTTPS, "b.corp.example", 443, "GET"))
	if err != nil {
		t.Fatalf("first redirect must be allowed: %v", err)
	}
	_, err = g.FollowRedirect(second, target(impl.SchemeHTTPS, "c.corp.example", 443, "GET"))
	if !errors.Is(err, impl.ErrHopLimit) {
		t.Fatalf("second redirect must hit the hop limit, got %v", err)
	}
}

func TestGrantExpiresAtBoundary(t *testing.T) {
	cfg := impl.Config{
		Rules: []impl.Rule{
			{Tenant: "t1", Pattern: "a.corp.example", Ports: []int{443}},
			{Tenant: "t1", Pattern: "b.corp.example", Ports: []int{443}},
		},
		TTL:       10,
		MaxHops:   2,
		MaxGrants: 10,
	}
	g := impl.NewEgressGate(cfg)
	gr, err := g.Authorize(impl.EgressRequest{Tenant: "t1", Target: target(impl.SchemeHTTPS, "a.corp.example", 443, "GET")})
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	g.Advance(9)
	if _, ok := g.Grant(gr.ID); !ok {
		t.Fatalf("grant must still be live one tick before the ttl boundary")
	}
	g.Advance(1)
	if _, err := g.FollowRedirect(gr, target(impl.SchemeHTTPS, "b.corp.example", 443, "GET")); !errors.Is(err, impl.ErrGrantExpired) {
		t.Fatalf("grant must be expired at the ttl boundary, got %v", err)
	}
	if _, ok := g.Grant(gr.ID); ok {
		t.Fatalf("an expired grant must not stay in the live table")
	}
}

func TestRevokeTenantInvalidatesLiveGrants(t *testing.T) {
	cfg := impl.Config{
		Rules: []impl.Rule{
			{Tenant: "t1", Pattern: "a.corp.example", Ports: []int{443}},
			{Tenant: "t1", Pattern: "b.corp.example", Ports: []int{443}},
			{Tenant: "t2", Pattern: "a.corp.example", Ports: []int{443}},
		},
		TTL:       60,
		MaxHops:   2,
		MaxGrants: 10,
	}
	g := impl.NewEgressGate(cfg)
	a, err := g.Authorize(impl.EgressRequest{Tenant: "t1", Target: target(impl.SchemeHTTPS, "a.corp.example", 443, "GET")})
	if err != nil {
		t.Fatalf("authorize t1 a: %v", err)
	}
	if _, err := g.Authorize(impl.EgressRequest{Tenant: "t1", Target: target(impl.SchemeHTTPS, "b.corp.example", 443, "GET")}); err != nil {
		t.Fatalf("authorize t1 b: %v", err)
	}
	other, err := g.Authorize(impl.EgressRequest{Tenant: "t2", Target: target(impl.SchemeHTTPS, "a.corp.example", 443, "GET")})
	if err != nil {
		t.Fatalf("authorize t2 a: %v", err)
	}
	if n := g.Revoke("t1"); n != 2 {
		t.Fatalf("revoke t1 dropped %d grants, want 2", n)
	}
	if _, ok := g.Grant(a.ID); ok {
		t.Fatalf("revoked tenant grants must leave the live table")
	}
	if _, err := g.FollowRedirect(a, target(impl.SchemeHTTPS, "b.corp.example", 443, "GET")); !errors.Is(err, impl.ErrGrantRevoked) {
		t.Fatalf("follow redirect with a revoked grant must fail with ErrGrantRevoked, got %v", err)
	}
	if _, err := g.Authorize(impl.EgressRequest{Tenant: "t1", Target: target(impl.SchemeHTTPS, "a.corp.example", 443, "GET")}); !errors.Is(err, impl.ErrTenantRevoked) {
		t.Fatalf("authorize after tenant revocation must fail with ErrTenantRevoked, got %v", err)
	}
	if _, ok := g.Grant(other.ID); !ok {
		t.Fatalf("revoking one tenant must not drop another tenant's grant")
	}
	if n := g.Revoke("missing"); n != 0 {
		t.Fatalf("revoking a tenant without live grants dropped %d, want 0", n)
	}
}

func TestSnapshotRestoreKeepsLiveGrantsAndClock(t *testing.T) {
	cfg := impl.Config{
		Rules: []impl.Rule{
			{Tenant: "t1", Pattern: "a.corp.example", Ports: []int{443}, Methods: []string{"GET"}},
			{Tenant: "t1", Pattern: "b.corp.example", Ports: []int{443}, Methods: []string{"GET"}},
		},
		TTL:       100,
		MaxHops:   2,
		MaxGrants: 10,
	}
	g := impl.NewEgressGate(cfg)
	gr, err := g.Authorize(impl.EgressRequest{Tenant: "t1", Target: target(impl.SchemeHTTPS, "a.corp.example", 443, "GET")})
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewEgressGate(cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restored.Clock() != 0 {
		t.Fatalf("restored clock = %d, want 0", restored.Clock())
	}
	if _, ok := restored.Grant(gr.ID); !ok {
		t.Fatalf("restored gate lost the live grant")
	}
	next, err := restored.FollowRedirect(gr, target(impl.SchemeHTTPS, "b.corp.example", 443, "GET"))
	if err != nil {
		t.Fatalf("restored grant must follow a redirect: %v", err)
	}
	restored.Advance(100)
	if _, err := restored.FollowRedirect(next, target(impl.SchemeHTTPS, "a.corp.example", 443, "GET")); !errors.Is(err, impl.ErrGrantExpired) {
		t.Fatalf("restored expiry must follow the logical clock, got %v", err)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	cfg := impl.Config{
		Rules:     []impl.Rule{{Tenant: "t1", Pattern: "a.corp.example", Ports: []int{443}}},
		TTL:       60,
		MaxHops:   2,
		MaxGrants: 10,
	}
	g := impl.NewEgressGate(cfg)
	gr, err := g.Authorize(impl.EgressRequest{Tenant: "t1", Target: target(impl.SchemeHTTPS, "a.corp.example", 443, "GET")})
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if err := g.Restore([]byte("not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if _, ok := g.Grant(gr.ID); !ok {
		t.Fatalf("a rejected restore must not touch the live grant table")
	}
}

func TestConcurrentAuthorizeRespectsGrantCap(t *testing.T) {
	rules := make([]impl.Rule, 0, 20)
	for i := 0; i < 20; i++ {
		rules = append(rules, impl.Rule{Tenant: "cap", Pattern: fmt.Sprintf("h%02d.corp.example", i), Ports: []int{443}})
	}
	cfg := impl.Config{Rules: rules, TTL: 1000, MaxHops: 1, MaxGrants: 5}
	g := impl.NewEgressGate(cfg)
	var wg sync.WaitGroup
	var mu sync.Mutex
	started, capped := 0, 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := g.Authorize(impl.EgressRequest{
				Tenant: "cap",
				Target: target(impl.SchemeHTTPS, fmt.Sprintf("h%02d.corp.example", i), 443, "GET"),
			})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				started++
			case errors.Is(err, impl.ErrGrantCap):
				capped++
			}
		}(i)
	}
	wg.Wait()
	if started != 5 {
		t.Fatalf("concurrent authorise issued %d grants, want exactly the cap 5", started)
	}
	if capped != 15 {
		t.Fatalf("concurrent authorise reported %d cap errors, want 15", capped)
	}
	if got := len(g.Active("cap")); got != 5 {
		t.Fatalf("live grants after the race = %d, want 5", got)
	}
}
