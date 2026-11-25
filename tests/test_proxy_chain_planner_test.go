package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch49-go-safety-b-03"
)

func plannerConfig(now *int64) impl.PlannerConfig {
	return impl.PlannerConfig{
		MaxHops:            4,
		MaxLeasesPerDomain: 3,
		DomainSuffixes:     []string{"example.com", "example.net"},
		Now:                func() int64 { return *now },
	}
}

func standardHops() []impl.Hop {
	return []impl.Hop{
		{ID: "edge-a", Scheme: "http", Addr: "10.0.0.1:3128", Zone: "edge"},
		{ID: "edge-b", Scheme: "http", Addr: "10.0.0.2:3128", Zone: "edge"},
		{ID: "exit-c", Scheme: "https", Addr: "10.0.0.3:8080", Zone: "exit", Auth: "basic", CredRef: "cred-1"},
	}
}

func newPlanner(t *testing.T, cfg impl.PlannerConfig) *impl.ChainPlanner {
	t.Helper()
	c := impl.NewChainPlanner(cfg)
	for _, h := range standardHops() {
		if err := c.RegisterHop(h); err != nil {
			t.Fatalf("register hop %s: %v", h.ID, err)
		}
	}
	return c
}

func mustPlan(t *testing.T, c *impl.ChainPlanner, domain string, hops []string) impl.Route {
	t.Helper()
	r, err := c.Plan(impl.Request{Domain: domain, Hops: hops})
	if err != nil {
		t.Fatalf("plan %s over %v: %v", domain, hops, err)
	}
	return r
}

func mustAcquire(t *testing.T, c *impl.ChainPlanner, route impl.Route) impl.Lease {
	t.Helper()
	l, err := c.Acquire(route)
	if err != nil {
		t.Fatalf("acquire route %s: %v", route.ID, err)
	}
	return l
}

func TestRegisterHopRejectsDuplicateAndBlank(t *testing.T) {
	now := int64(1000)
	c := newPlanner(t, plannerConfig(&now))
	if err := c.RegisterHop(impl.Hop{ID: "edge-a", Scheme: "http", Addr: "10.0.0.1:3128"}); !errors.Is(err, impl.ErrHopExists) {
		t.Fatalf("duplicate hop id must fail with ErrHopExists, got %v", err)
	}
	if err := c.RegisterHop(impl.Hop{ID: "  ", Scheme: "http", Addr: "10.0.0.9:3128"}); !errors.Is(err, impl.ErrBlankHop) {
		t.Fatalf("blank hop id must fail with ErrBlankHop, got %v", err)
	}
	if err := c.RegisterHop(impl.Hop{ID: "edge-z", Scheme: "http", Addr: "   "}); !errors.Is(err, impl.ErrBlankHop) {
		t.Fatalf("blank hop address must fail with ErrBlankHop, got %v", err)
	}
	if err := c.RegisterHop(impl.Hop{ID: "edge-z", Scheme: "", Addr: "10.0.0.9:3128"}); !errors.Is(err, impl.ErrBlankScheme) {
		t.Fatalf("blank hop scheme must fail with ErrBlankScheme, got %v", err)
	}
	hops := c.Hops()
	if len(hops) != 3 {
		t.Fatalf("registry holds %d hops, want the three accepted ones", len(hops))
	}
	for i := 1; i < len(hops); i++ {
		if hops[i-1].ID >= hops[i].ID {
			t.Fatalf("hops must be listed in id order, got %v then %v", hops[i-1].ID, hops[i].ID)
		}
	}
	h, ok := c.Hop("exit-c")
	if !ok || h.Addr != "10.0.0.3:8080" || h.CredRef != "cred-1" {
		t.Fatalf("hop read back = %+v ok=%v, want the registered exit node", h, ok)
	}
	if _, ok := c.Hop("edge-404"); ok {
		t.Fatalf("an unregistered hop must not be readable")
	}
}

func TestPlanBuildsOrderedChain(t *testing.T) {
	now := int64(1000)
	c := newPlanner(t, plannerConfig(&now))
	r := mustPlan(t, c, "api.example.com", []string{"edge-a", "edge-b", "exit-c"})
	if r.ID == "" {
		t.Fatalf("route id must not be empty")
	}
	if r.Domain != "api.example.com" {
		t.Fatalf("route domain = %q, want api.example.com", r.Domain)
	}
	want := []string{"edge-a", "edge-b", "exit-c"}
	if len(r.Hops) != len(want) {
		t.Fatalf("route hops = %v, want %v", r.Hops, want)
	}
	for i, id := range want {
		if r.Hops[i] != id {
			t.Fatalf("route hops = %v, want the requested order %v", r.Hops, want)
		}
	}
	if r.Egress != "10.0.0.3:8080" {
		t.Fatalf("egress = %q, want the last hop address 10.0.0.3:8080", r.Egress)
	}
	if r.Gen != 1 {
		t.Fatalf("route generation = %d, want 1", r.Gen)
	}
	if got := c.Stats("api.example.com"); got.Planned != 1 {
		t.Fatalf("planned tally = %d, want 1 after one plan", got.Planned)
	}
	stored, ok := c.RouteFor("api.example.com")
	if !ok || stored.ID != r.ID || stored.Gen != 1 {
		t.Fatalf("route reader = %+v ok=%v, want the planned route", stored, ok)
	}
	if _, ok := c.RouteFor("other.example.net"); ok {
		t.Fatalf("an unplanned domain must not expose a route")
	}
}

func TestPlanRejectsBlankDomainAndNoHops(t *testing.T) {
	now := int64(1000)
	c := newPlanner(t, plannerConfig(&now))
	if _, err := c.Plan(impl.Request{Domain: "  ", Hops: []string{"edge-a"}}); !errors.Is(err, impl.ErrBlankDomain) {
		t.Fatalf("blank domain must fail with ErrBlankDomain, got %v", err)
	}
	if _, err := c.Plan(impl.Request{Domain: "api.example.com"}); !errors.Is(err, impl.ErrNoHops) {
		t.Fatalf("an empty hop list must fail with ErrNoHops, got %v", err)
	}
	if _, err := c.Plan(impl.Request{Domain: "api.example.com", Hops: []string{"edge-a", "ghost"}}); !errors.Is(err, impl.ErrUnknownHop) {
		t.Fatalf("an unregistered hop must fail with ErrUnknownHop, got %v", err)
	}
	if got := c.Stats("api.example.com"); got.Planned != 0 {
		t.Fatalf("rejected plans must not add to the tally, got %d", got.Planned)
	}
	if _, ok := c.RouteFor("api.example.com"); ok {
		t.Fatalf("rejected plans must not publish a route")
	}
}

func TestPlanRejectsHopLoop(t *testing.T) {
	now := int64(1000)
	c := impl.NewChainPlanner(plannerConfig(&now))
	for _, h := range []impl.Hop{
		{ID: "loop-a", Scheme: "http", Addr: "10.9.9.9:3128", Zone: "edge"},
		{ID: "loop-b", Scheme: "https", Addr: "10.9.9.9:3128", Zone: "edge"},
		{ID: "loop-c", Scheme: "http", Addr: "10.9.9.10:3128", Zone: "edge"},
	} {
		if err := c.RegisterHop(h); err != nil {
			t.Fatalf("register %s: %v", h.ID, err)
		}
	}
	if _, err := c.Plan(impl.Request{Domain: "api.example.com", Hops: []string{"loop-a", "loop-b"}}); !errors.Is(err, impl.ErrHopLoop) {
		t.Fatalf("two hop ids sharing one address must fail with ErrHopLoop, got %v", err)
	}
	if _, err := c.Plan(impl.Request{Domain: "api.example.com", Hops: []string{"loop-a", "loop-c", "loop-a"}}); !errors.Is(err, impl.ErrHopLoop) {
		t.Fatalf("revisiting one hop id must fail with ErrHopLoop, got %v", err)
	}
	r := mustPlan(t, c, "api.example.com", []string{"loop-a", "loop-c"})
	if len(r.Hops) != 2 {
		t.Fatalf("route hops = %v, want the two distinct addresses", r.Hops)
	}
}

func TestPlanEnforcesHopLimit(t *testing.T) {
	now := int64(1000)
	cfg := plannerConfig(&now)
	cfg.MaxHops = 3
	c := impl.NewChainPlanner(cfg)
	for _, h := range []impl.Hop{
		{ID: "h1", Scheme: "http", Addr: "10.1.0.1:3128"},
		{ID: "h2", Scheme: "http", Addr: "10.1.0.2:3128"},
		{ID: "h3", Scheme: "http", Addr: "10.1.0.3:3128"},
		{ID: "h4", Scheme: "http", Addr: "10.1.0.4:3128"},
	} {
		if err := c.RegisterHop(h); err != nil {
			t.Fatalf("register %s: %v", h.ID, err)
		}
	}
	if _, err := c.Plan(impl.Request{Domain: "api.example.com", Hops: []string{"h1", "h2", "h3", "h4"}}); !errors.Is(err, impl.ErrHopLimit) {
		t.Fatalf("four hops against a cap of three must fail with ErrHopLimit, got %v", err)
	}
	r := mustPlan(t, c, "api.example.com", []string{"h1", "h2", "h3"})
	if len(r.Hops) != 3 || r.Egress != "10.1.0.3:3128" {
		t.Fatalf("route = %+v, want three hops ending at 10.1.0.3:3128", r)
	}
}

func TestPlanRejectsCredentialCrossZoneReuse(t *testing.T) {
	now := int64(1000)
	c := impl.NewChainPlanner(plannerConfig(&now))
	for _, h := range []impl.Hop{
		{ID: "z-edge", Scheme: "https", Addr: "10.2.0.1:8443", Zone: "edge", Auth: "basic", CredRef: "cred-9"},
		{ID: "z-core", Scheme: "https", Addr: "10.2.0.2:8443", Zone: "core", Auth: "basic", CredRef: "cred-9"},
		{ID: "z-none", Scheme: "https", Addr: "10.2.0.3:8443", Zone: "core", Auth: "basic"},
	} {
		if err := c.RegisterHop(h); err != nil {
			t.Fatalf("register %s: %v", h.ID, err)
		}
	}
	if _, err := c.Plan(impl.Request{Domain: "api.example.com", Hops: []string{"z-edge", "z-core"}}); !errors.Is(err, impl.ErrCredentialReuse) {
		t.Fatalf("one credential across two zones must fail with ErrCredentialReuse, got %v", err)
	}
	if _, err := c.Plan(impl.Request{Domain: "api.example.com", Hops: []string{"z-edge", "z-none"}}); !errors.Is(err, impl.ErrBlankCredRef) {
		t.Fatalf("an authenticated hop without a credential reference must fail with ErrBlankCredRef, got %v", err)
	}
	if _, ok := c.RouteFor("api.example.com"); ok {
		t.Fatalf("rejected chains must not publish a route")
	}
}

func TestAcquireIssuesLeaseAndEnforcesLimit(t *testing.T) {
	now := int64(1000)
	cfg := plannerConfig(&now)
	cfg.MaxLeasesPerDomain = 2
	c := newPlanner(t, cfg)
	r := mustPlan(t, c, "api.example.com", []string{"edge-a", "edge-b", "exit-c"})
	l1 := mustAcquire(t, c, r)
	if l1.ID == "" {
		t.Fatalf("lease id must not be empty")
	}
	if l1.RouteID != r.ID || l1.Domain != r.Domain || l1.Gen != r.Gen {
		t.Fatalf("lease = %+v, want it bound to route %s at generation %d", l1, r.ID, r.Gen)
	}
	if l1.Egress != "10.0.0.3:8080" {
		t.Fatalf("lease egress = %q, want the route egress", l1.Egress)
	}
	if l1.IssuedAt != 1000 {
		t.Fatalf("lease issued at = %d, want the injected clock 1000", l1.IssuedAt)
	}
	l2 := mustAcquire(t, c, r)
	if l2.ID == l1.ID {
		t.Fatalf("lease ids must differ, both are %q", l1.ID)
	}
	if _, err := c.Acquire(r); !errors.Is(err, impl.ErrLeaseLimit) {
		t.Fatalf("the third lease must fail with ErrLeaseLimit, got %v", err)
	}
	if got := len(c.ActiveLeases()); got != 2 {
		t.Fatalf("active leases = %d, want the configured cap 2", got)
	}
	stored, ok := c.Lease(l1.ID)
	if !ok || stored.RouteID != r.ID {
		t.Fatalf("lease reader = %+v ok=%v, want the open lease", stored, ok)
	}
}

func TestAcquireRejectsUnknownRoute(t *testing.T) {
	now := int64(1000)
	c := newPlanner(t, plannerConfig(&now))
	if _, err := c.Acquire(impl.Route{}); !errors.Is(err, impl.ErrUnknownRoute) {
		t.Fatalf("a zero route must fail with ErrUnknownRoute, got %v", err)
	}
	if _, err := c.Acquire(impl.Route{ID: "route:ghost:1", Domain: "api.example.com", Gen: 1}); !errors.Is(err, impl.ErrUnknownRoute) {
		t.Fatalf("a fabricated route id must fail with ErrUnknownRoute, got %v", err)
	}
	if got := len(c.ActiveLeases()); got != 0 {
		t.Fatalf("rejected acquires must not open leases, got %d", got)
	}
}

func TestReportSuccessRecordsStatsAndClosesLease(t *testing.T) {
	now := int64(1000)
	c := newPlanner(t, plannerConfig(&now))
	r := mustPlan(t, c, "api.example.com", []string{"edge-a", "edge-b", "exit-c"})
	l := mustAcquire(t, c, r)
	if err := c.Report(l.ID, impl.Outcome{OK: true, HopID: "exit-c", Code: 200, LatencyMillis: 42}); err != nil {
		t.Fatalf("report a successful lease: %v", err)
	}
	got := c.Stats("api.example.com")
	if got.Planned != 1 || got.Succeeded != 1 || got.Failed != 0 || got.Orphaned != 0 {
		t.Fatalf("stats after one success = %+v, want planned 1 succeeded 1", got)
	}
	if err := c.Report(l.ID, impl.Outcome{OK: true, Code: 200}); !errors.Is(err, impl.ErrUnknownLease) {
		t.Fatalf("reporting the same lease twice must fail with ErrUnknownLease, got %v", err)
	}
	if _, ok := c.Lease(l.ID); ok {
		t.Fatalf("a settled lease must not stay readable")
	}
	if got := len(c.ActiveLeases()); got != 0 {
		t.Fatalf("active leases = %d, want 0 after the report", got)
	}
	if stored, _ := c.RouteFor("api.example.com"); stored.Gen != 1 {
		t.Fatalf("route generation = %d, a successful report must not rebuild the chain", stored.Gen)
	}
}

func TestReportFailureRebuildsChainGeneration(t *testing.T) {
	now := int64(1000)
	c := newPlanner(t, plannerConfig(&now))
	r := mustPlan(t, c, "api.example.com", []string{"edge-a", "edge-b", "exit-c"})
	l := mustAcquire(t, c, r)
	if err := c.Report(l.ID, impl.Outcome{OK: false, HopID: "edge-b", Code: 502}); err != nil {
		t.Fatalf("report a failed lease: %v", err)
	}
	got := c.Stats("api.example.com")
	if got.Failed != 1 || got.Succeeded != 0 {
		t.Fatalf("stats after one failure = %+v, want failed 1", got)
	}
	rebuilt, ok := c.RouteFor("api.example.com")
	if !ok {
		t.Fatalf("the chain must stay published after a rebuild")
	}
	if rebuilt.Gen != 2 {
		t.Fatalf("rebuilt route generation = %d, want 2 after one failed report", rebuilt.Gen)
	}
	if rebuilt.ID == r.ID {
		t.Fatalf("the rebuilt route must get a new identity")
	}
	if _, err := c.Acquire(r); !errors.Is(err, impl.ErrStaleRoute) {
		t.Fatalf("acquiring the superseded route must fail with ErrStaleRoute, got %v", err)
	}
	fresh := mustAcquire(t, c, rebuilt)
	if fresh.Gen != 2 {
		t.Fatalf("lease generation = %d, want the rebuilt generation 2", fresh.Gen)
	}
}

func TestReportAndReleaseRejectUnknownLease(t *testing.T) {
	now := int64(1000)
	c := newPlanner(t, plannerConfig(&now))
	if err := c.Report("lease-404", impl.Outcome{OK: true}); !errors.Is(err, impl.ErrUnknownLease) {
		t.Fatalf("reporting an unknown lease must fail with ErrUnknownLease, got %v", err)
	}
	if err := c.Release("lease-404"); !errors.Is(err, impl.ErrUnknownLease) {
		t.Fatalf("releasing an unknown lease must fail with ErrUnknownLease, got %v", err)
	}
	r := mustPlan(t, c, "api.example.com", []string{"edge-a", "exit-c"})
	l := mustAcquire(t, c, r)
	if err := c.Release(l.ID); err != nil {
		t.Fatalf("release an open lease: %v", err)
	}
	if err := c.Release(l.ID); !errors.Is(err, impl.ErrUnknownLease) {
		t.Fatalf("releasing twice must fail with ErrUnknownLease, got %v", err)
	}
	if err := c.Report(l.ID, impl.Outcome{OK: true}); !errors.Is(err, impl.ErrUnknownLease) {
		t.Fatalf("reporting a released lease must fail with ErrUnknownLease, got %v", err)
	}
	if got := c.Stats("api.example.com"); got.Succeeded != 0 || got.Failed != 0 {
		t.Fatalf("a released lease must not move the tally, got %+v", got)
	}
}

func TestConcurrentAcquireRespectsLeaseLimit(t *testing.T) {
	now := int64(1000)
	cfg := plannerConfig(&now)
	cfg.MaxLeasesPerDomain = 3
	c := newPlanner(t, cfg)
	r := mustPlan(t, c, "api.example.com", []string{"edge-a", "edge-b", "exit-c"})
	var wg sync.WaitGroup
	var mu sync.Mutex
	got, limited := 0, 0
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := c.Acquire(r)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				got++
			case errors.Is(err, impl.ErrLeaseLimit):
				limited++
			}
		}()
	}
	wg.Wait()
	if got != 3 {
		t.Fatalf("concurrent acquires opened %d leases, want the cap 3", got)
	}
	if limited != 9 {
		t.Fatalf("concurrent acquires reported %d limit errors, want 9", limited)
	}
	if n := len(c.ActiveLeases()); n != 3 {
		t.Fatalf("active leases after the race = %d, want 3", n)
	}
}

func TestStatsAreScopedPerDomain(t *testing.T) {
	now := int64(1000)
	c := newPlanner(t, plannerConfig(&now))
	ra := mustPlan(t, c, "a.example.com", []string{"edge-a", "exit-c"})
	rb := mustPlan(t, c, "b.example.net", []string{"edge-b", "exit-c"})
	if ra.Egress != rb.Egress {
		t.Fatalf("both plans end on exit-c, got egress %q and %q", ra.Egress, rb.Egress)
	}
	la := mustAcquire(t, c, ra)
	if err := c.Report(la.ID, impl.Outcome{OK: true, Code: 200}); err != nil {
		t.Fatalf("report the first domain lease: %v", err)
	}
	ga := c.Stats("a.example.com")
	if ga.Planned != 1 || ga.Succeeded != 1 || ga.Failed != 0 {
		t.Fatalf("stats of the reported domain = %+v, want planned 1 succeeded 1", ga)
	}
	gb := c.Stats("b.example.net")
	if gb.Planned != 1 || gb.Succeeded != 0 || gb.Failed != 0 {
		t.Fatalf("stats of the untouched domain = %+v, want planned 1 and nothing settled", gb)
	}
	if gz := c.Stats("c.example.net"); gz.Planned != 0 || gz.Succeeded != 0 {
		t.Fatalf("stats of an unplanned domain = %+v, want all zero", gz)
	}
	if err := c.Report(la.ID, impl.Outcome{OK: true}); !errors.Is(err, impl.ErrUnknownLease) {
		t.Fatalf("re-reporting must fail with ErrUnknownLease, got %v", err)
	}
}

func TestSnapshotRestoreKeepsRoutesLeasesAndStats(t *testing.T) {
	now := int64(1000)
	cfg := plannerConfig(&now)
	c := newPlanner(t, cfg)
	r := mustPlan(t, c, "api.example.com", []string{"edge-a", "edge-b", "exit-c"})
	done := mustAcquire(t, c, r)
	if err := c.Report(done.ID, impl.Outcome{OK: true, Code: 200}); err != nil {
		t.Fatalf("report the settled lease: %v", err)
	}
	open := mustAcquire(t, c, r)
	data, err := c.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewChainPlanner(cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	route, ok := restored.RouteFor("api.example.com")
	if !ok || route.ID != r.ID || route.Gen != 1 {
		t.Fatalf("restored route = %+v ok=%v, want %s at generation 1", route, ok, r.ID)
	}
	lease, ok := restored.Lease(open.ID)
	if !ok || lease.RouteID != r.ID || lease.Egress != "10.0.0.3:8080" {
		t.Fatalf("restored lease = %+v ok=%v, want the open lease on the same route", lease, ok)
	}
	if got := len(restored.ActiveLeases()); got != 1 {
		t.Fatalf("restored active leases = %d, want the single open lease", got)
	}
	stats := restored.Stats("api.example.com")
	if stats.Planned != 1 || stats.Succeeded != 1 {
		t.Fatalf("restored stats = %+v, want planned 1 succeeded 1", stats)
	}
	next := mustAcquire(t, restored, route)
	if next.ID == open.ID {
		t.Fatalf("lease ids must not be reused after a restore")
	}
	if len(restored.Hops()) != 3 {
		t.Fatalf("restored registry holds %d hops, want 3", len(restored.Hops()))
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	now := int64(1000)
	cfg := plannerConfig(&now)
	c := newPlanner(t, cfg)
	r := mustPlan(t, c, "api.example.com", []string{"edge-a", "exit-c"})
	l := mustAcquire(t, c, r)
	for _, payload := range [][]byte{nil, []byte(""), []byte("{not-a-snapshot"), []byte(`{"routes":`)} {
		if err := c.Restore(payload); !errors.Is(err, impl.ErrBadSnapshot) {
			t.Fatalf("restoring %q must fail with ErrBadSnapshot, got %v", string(payload), err)
		}
	}
	if stored, ok := c.RouteFor("api.example.com"); !ok || stored.ID != r.ID {
		t.Fatalf("a rejected restore must not drop the live route, got %+v ok=%v", stored, ok)
	}
	if _, ok := c.Lease(l.ID); !ok {
		t.Fatalf("a rejected restore must not drop the open lease")
	}
	if err := c.Release(l.ID); err != nil {
		t.Fatalf("the live lease must stay usable after a rejected restore, got %v", err)
	}
}
