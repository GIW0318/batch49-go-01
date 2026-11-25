// Package task plans ordered outbound proxy chains and hands them out as
// leases, keeping per registered domain egress stickiness and credential
// scoping across security zones.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Errors returned by the planner.
var (
	ErrBadConfig          = errors.New("chain planner config is invalid")
	ErrBlankHop           = errors.New("hop id and address are required")
	ErrBlankScheme        = errors.New("hop scheme is required")
	ErrHopExists          = errors.New("hop is already registered")
	ErrUnknownHop         = errors.New("hop is not registered")
	ErrBlankDomain        = errors.New("request domain is required")
	ErrNoHops             = errors.New("a route needs at least one hop")
	ErrHopLimit           = errors.New("hop count exceeds the configured cap")
	ErrHopLoop            = errors.New("route revisits the same proxy address")
	ErrAuthScopeViolation = errors.New("credentialed hop follows an unauthenticated hop")
	ErrCredentialReuse    = errors.New("credential reference crosses a security zone")
	ErrBlankCredRef       = errors.New("authenticated hop needs a credential reference")
	ErrUnknownRoute       = errors.New("route is unknown")
	ErrStaleRoute         = errors.New("route generation is stale")
	ErrLeaseLimit         = errors.New("lease limit reached for this domain")
	ErrUnknownLease       = errors.New("lease is unknown or already closed")
	ErrBadSnapshot        = errors.New("snapshot payload is invalid")
)

// PlannerConfig bounds one planner instance. DomainSuffixes lists the
// registered domain suffixes used for egress stickiness.
type PlannerConfig struct {
	MaxHops            int
	MaxLeasesPerDomain int
	DomainSuffixes     []string
	Now                func() int64
}

// Hop is one registered proxy node.
type Hop struct {
	ID      string `json:"id"`
	Scheme  string `json:"scheme"`
	Addr    string `json:"addr"`
	Zone    string `json:"zone"`
	Auth    string `json:"auth"`
	CredRef string `json:"cred_ref"`
}

// Request asks for a route to one registered domain over candidate hops.
type Request struct {
	Domain string   `json:"domain"`
	Hops   []string `json:"hops"`
}

// Route is an ordered hop list with its egress and planning generation.
type Route struct {
	ID     string   `json:"id"`
	Domain string   `json:"domain"`
	Hops   []string `json:"hops"`
	Egress string   `json:"egress"`
	Gen    int64    `json:"gen"`
}

// Lease is one execution grant of a route.
type Lease struct {
	ID       string   `json:"id"`
	RouteID  string   `json:"route_id"`
	Domain   string   `json:"domain"`
	Egress   string   `json:"egress"`
	Hops     []string `json:"hops"`
	Gen      int64    `json:"gen"`
	IssuedAt int64    `json:"issued_at"`
}

// Outcome is the result reported back for one lease.
type Outcome struct {
	OK            bool   `json:"ok"`
	HopID         string `json:"hop_id"`
	Code          int    `json:"code"`
	LatencyMillis int64  `json:"latency_millis"`
}

// Stats is the per domain execution tally.
type Stats struct {
	Planned   int `json:"planned"`
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
	Orphaned  int `json:"orphaned"`
}

// ChainPlanner owns the hop registry, the planned routes and the leases.
type ChainPlanner struct {
	mu       sync.Mutex
	cfg      PlannerConfig
	hops     map[string]Hop
	routes   map[string]Route
	leases   map[string]Lease
	stats    map[string]Stats
	leaseSeq uint64
}

// NewChainPlanner builds a planner from the given configuration.
func NewChainPlanner(cfg PlannerConfig) *ChainPlanner {
	if cfg.Now == nil {
		cfg.Now = func() int64 { return time.Now().Unix() }
	}
	return &ChainPlanner{
		cfg:    cfg,
		hops:   make(map[string]Hop),
		routes: make(map[string]Route),
		leases: make(map[string]Lease),
		stats:  make(map[string]Stats),
	}
}

// RegisterHop adds one proxy node to the registry.
func (c *ChainPlanner) RegisterHop(h Hop) error {
	h.ID = strings.TrimSpace(h.ID)
	h.Scheme = strings.TrimSpace(h.Scheme)
	h.Addr = strings.TrimSpace(h.Addr)
	h.Zone = strings.TrimSpace(h.Zone)
	h.Auth = strings.TrimSpace(h.Auth)
	h.CredRef = strings.TrimSpace(h.CredRef)
	if h.ID == "" || h.Addr == "" {
		return ErrBlankHop
	}
	if h.Scheme == "" {
		return ErrBlankScheme
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.hops[h.ID]; ok {
		return ErrHopExists
	}
	c.hops[h.ID] = h
	return nil
}

// Hop reads one registered node.
func (c *ChainPlanner) Hop(id string) (Hop, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	h, ok := c.hops[strings.TrimSpace(id)]
	return h, ok
}

// Hops lists the registered nodes ordered by id.
func (c *ChainPlanner) Hops() []Hop {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Hop, 0, len(c.hops))
	for _, h := range c.hops {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Plan validates the candidate hops and returns the route for the domain.
func (c *ChainPlanner) Plan(req Request) (Route, error) {
	domain := normalizeDomain(req.Domain)
	if domain == "" {
		return Route{}, ErrBlankDomain
	}
	if len(req.Hops) == 0 {
		return Route{}, ErrNoHops
	}
	if c.cfg.MaxHops > 0 && len(req.Hops) > c.cfg.MaxHops {
		return Route{}, ErrHopLimit
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	chain := make([]Hop, 0, len(req.Hops))
	seenIDs := make(map[string]struct{}, len(req.Hops))
	seenAddrs := make(map[string]struct{}, len(req.Hops))
	for _, raw := range req.Hops {
		id := strings.TrimSpace(raw)
		h, ok := c.hops[id]
		if !ok {
			return Route{}, ErrUnknownHop
		}
		if _, dup := seenIDs[id]; dup {
			return Route{}, ErrHopLoop
		}
		seenIDs[id] = struct{}{}
		addr := normalizeAddr(h.Addr)
		if _, dup := seenAddrs[addr]; dup {
			return Route{}, ErrHopLoop
		}
		seenAddrs[addr] = struct{}{}
		chain = append(chain, h)
	}

	credZones := make(map[string]string)
	for _, h := range chain {
		if h.Auth != "" && h.CredRef == "" {
			return Route{}, ErrBlankCredRef
		}
		if h.CredRef == "" {
			continue
		}
		if zone, ok := credZones[h.CredRef]; ok && zone != h.Zone {
			return Route{}, ErrCredentialReuse
		}
		credZones[h.CredRef] = h.Zone
	}

	if existing, ok := c.routes[domain]; ok {
		return existing, nil
	}

	hopIDs := make([]string, 0, len(chain))
	for _, h := range chain {
		hopIDs = append(hopIDs, h.ID)
	}
	route := Route{
		ID:     routeID(domain, 1),
		Domain: domain,
		Hops:   hopIDs,
		Egress: chain[len(chain)-1].Addr,
		Gen:    1,
	}
	c.routes[domain] = route
	st := c.stats[domain]
	st.Planned++
	c.stats[domain] = st
	return route, nil
}

// Acquire turns a planned route into an execution lease.
func (c *ChainPlanner) Acquire(route Route) (Lease, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	domain := normalizeDomain(route.Domain)
	cur, ok := c.routes[domain]
	if !ok {
		return Lease{}, ErrUnknownRoute
	}
	if cur.ID != route.ID || cur.Gen != route.Gen {
		if route.Gen < cur.Gen {
			return Lease{}, ErrStaleRoute
		}
		return Lease{}, ErrUnknownRoute
	}
	if c.cfg.MaxLeasesPerDomain > 0 && c.activeLeasesLocked(domain) >= c.cfg.MaxLeasesPerDomain {
		return Lease{}, ErrLeaseLimit
	}
	c.leaseSeq++
	lease := Lease{
		ID:       leaseID(c.leaseSeq),
		RouteID:  cur.ID,
		Domain:   cur.Domain,
		Egress:   cur.Egress,
		Hops:     append([]string(nil), cur.Hops...),
		Gen:      cur.Gen,
		IssuedAt: c.cfg.Now(),
	}
	c.leases[lease.ID] = lease
	return lease, nil
}

// Report settles one lease and rebuilds the chain when the hop failed.
func (c *ChainPlanner) Report(leaseID string, out Outcome) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	lease, ok := c.leases[leaseID]
	if !ok {
		return ErrUnknownLease
	}
	delete(c.leases, leaseID)
	st := c.stats[lease.Domain]
	cur := c.routes[lease.Domain]
	if lease.Gen != cur.Gen {
		st.Orphaned++
		c.stats[lease.Domain] = st
		return nil
	}
	if out.OK {
		st.Succeeded++
		c.stats[lease.Domain] = st
		return nil
	}
	st.Failed++
	st.Planned++
	c.stats[lease.Domain] = st
	gen := cur.Gen + 1
	c.routes[lease.Domain] = Route{
		ID:     routeID(lease.Domain, gen),
		Domain: lease.Domain,
		Hops:   cur.Hops,
		Egress: cur.Egress,
		Gen:    gen,
	}
	return nil
}

// Release closes a lease without a verdict.
func (c *ChainPlanner) Release(leaseID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.leases[leaseID]; !ok {
		return ErrUnknownLease
	}
	delete(c.leases, leaseID)
	return nil
}

// RouteFor reads the live route of one registered domain.
func (c *ChainPlanner) RouteFor(domain string) (Route, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.routes[normalizeDomain(domain)]
	return r, ok
}

// Lease reads one open lease.
func (c *ChainPlanner) Lease(leaseID string) (Lease, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	l, ok := c.leases[leaseID]
	return l, ok
}

// ActiveLeases lists the open leases ordered by id.
func (c *ChainPlanner) ActiveLeases() []Lease {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Lease, 0, len(c.leases))
	for _, l := range c.leases {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool {
		si, sj := leaseSeqOf(out[i].ID), leaseSeqOf(out[j].ID)
		if si != sj {
			return si < sj
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Stats reports the execution tally of one domain.
func (c *ChainPlanner) Stats(domain string) Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stats[normalizeDomain(domain)]
}

const snapshotVersion = 1

type snapshot struct {
	Version  int              `json:"version"`
	Hops     []Hop            `json:"hops"`
	Routes   []Route          `json:"routes"`
	Leases   []Lease          `json:"leases"`
	Stats    map[string]Stats `json:"stats"`
	LeaseSeq uint64           `json:"lease_seq"`
}

// Snapshot serialises the registry, the routes, the leases and the tallies.
func (c *ChainPlanner) Snapshot() ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := snapshot{
		Version:  snapshotVersion,
		Hops:     make([]Hop, 0, len(c.hops)),
		Routes:   make([]Route, 0, len(c.routes)),
		Leases:   make([]Lease, 0, len(c.leases)),
		Stats:    make(map[string]Stats, len(c.stats)),
		LeaseSeq: c.leaseSeq,
	}
	for _, h := range c.hops {
		s.Hops = append(s.Hops, h)
	}
	sort.Slice(s.Hops, func(i, j int) bool { return s.Hops[i].ID < s.Hops[j].ID })
	for _, r := range c.routes {
		s.Routes = append(s.Routes, r)
	}
	sort.Slice(s.Routes, func(i, j int) bool { return s.Routes[i].Domain < s.Routes[j].Domain })
	for _, l := range c.leases {
		s.Leases = append(s.Leases, l)
	}
	sort.Slice(s.Leases, func(i, j int) bool { return s.Leases[i].ID < s.Leases[j].ID })
	for d, st := range c.stats {
		s.Stats[d] = st
	}
	return json.Marshal(s)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (c *ChainPlanner) Restore(data []byte) error {
	var s snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return ErrBadSnapshot
	}
	if s.Version != snapshotVersion {
		return ErrBadSnapshot
	}
	hops := make(map[string]Hop, len(s.Hops))
	for _, h := range s.Hops {
		hops[h.ID] = h
	}
	routes := make(map[string]Route, len(s.Routes))
	for _, r := range s.Routes {
		routes[r.Domain] = r
	}
	leases := make(map[string]Lease, len(s.Leases))
	var maxSeq uint64
	for _, l := range s.Leases {
		leases[l.ID] = l
		if n := leaseSeqOf(l.ID); n > maxSeq {
			maxSeq = n
		}
	}
	stats := make(map[string]Stats, len(s.Stats))
	for d, st := range s.Stats {
		stats[d] = st
	}
	leaseSeq := s.LeaseSeq
	if maxSeq >= leaseSeq {
		leaseSeq = maxSeq + 1
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hops = hops
	c.routes = routes
	c.leases = leases
	c.stats = stats
	c.leaseSeq = leaseSeq
	return nil
}

func (c *ChainPlanner) activeLeasesLocked(domain string) int {
	n := 0
	for _, l := range c.leases {
		if l.Domain == domain {
			n++
		}
	}
	return n
}

func routeID(domain string, gen int64) string {
	return fmt.Sprintf("route:%s:%d", domain, gen)
}

func leaseID(seq uint64) string {
	return fmt.Sprintf("lease-%d", seq)
}

func leaseSeqOf(id string) uint64 {
	const prefix = "lease-"
	if !strings.HasPrefix(id, prefix) {
		return 0
	}
	n, err := strconv.ParseUint(id[len(prefix):], 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func normalizeAddr(addr string) string {
	return strings.ToLower(strings.TrimSpace(addr))
}

func normalizeDomain(domain string) string {
	d := strings.ToLower(strings.TrimSpace(domain))
	d = strings.TrimSuffix(d, ".")
	if host, _, err := net.SplitHostPort(d); err == nil {
		return host
	}
	if i := strings.LastIndex(d, ":"); i > 0 && strings.Count(d, ":") == 1 {
		if port := d[i+1:]; port != "" && isDigits(port) {
			return d[:i]
		}
	}
	return d
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(s) > 0
}
