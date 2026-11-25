// Package task plans ordered outbound proxy chains and hands them out as
// leases, keeping per registered domain egress stickiness and credential
// scoping across security zones.
package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

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
	cfg PlannerConfig
}

// NewChainPlanner builds a planner from the given configuration.
func NewChainPlanner(cfg PlannerConfig) *ChainPlanner {
	return &ChainPlanner{cfg: cfg}
}

// RegisterHop adds one proxy node to the registry.
func (c *ChainPlanner) RegisterHop(h Hop) error {
	return errNotImplemented
}

// Hop reads one registered node.
func (c *ChainPlanner) Hop(id string) (Hop, bool) {
	return Hop{}, false
}

// Hops lists the registered nodes ordered by id.
func (c *ChainPlanner) Hops() []Hop {
	return nil
}

// Plan validates the candidate hops and returns the route for the domain.
func (c *ChainPlanner) Plan(req Request) (Route, error) {
	return Route{}, errNotImplemented
}

// Acquire turns a planned route into an execution lease.
func (c *ChainPlanner) Acquire(route Route) (Lease, error) {
	return Lease{}, errNotImplemented
}

// Report settles one lease and rebuilds the chain when the hop failed.
func (c *ChainPlanner) Report(leaseID string, out Outcome) error {
	return errNotImplemented
}

// Release closes a lease without a verdict.
func (c *ChainPlanner) Release(leaseID string) error {
	return errNotImplemented
}

// RouteFor reads the live route of one registered domain.
func (c *ChainPlanner) RouteFor(domain string) (Route, bool) {
	return Route{}, false
}

// Lease reads one open lease.
func (c *ChainPlanner) Lease(leaseID string) (Lease, bool) {
	return Lease{}, false
}

// ActiveLeases lists the open leases ordered by id.
func (c *ChainPlanner) ActiveLeases() []Lease {
	return nil
}

// Stats reports the execution tally of one domain.
func (c *ChainPlanner) Stats(domain string) Stats {
	return Stats{}
}

// Snapshot serialises the registry, the routes, the leases and the tallies.
func (c *ChainPlanner) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (c *ChainPlanner) Restore(data []byte) error {
	return errNotImplemented
}
