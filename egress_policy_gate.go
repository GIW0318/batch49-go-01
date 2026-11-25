// Package task evaluates outbound egress destinations, issues short lived
// grants and re-evaluates every redirect hop.
package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Schemes accepted on a target.
const (
	SchemeHTTP  = "http"
	SchemeHTTPS = "https"
)

// How a rule matched a host.
type MatchKind string

// Match kinds ordered from strongest to weakest.
const (
	MatchExact    MatchKind = "exact"
	MatchSuffix   MatchKind = "suffix"
	MatchCategory MatchKind = "category"
	MatchNone     MatchKind = "none"
)

// Errors returned by the gate.
var (
	ErrBlankTenant     = errors.New("tenant is required")
	ErrBadTarget       = errors.New("target is invalid")
	ErrDenied          = errors.New("egress denied by policy")
	ErrGrantExpired    = errors.New("grant has expired")
	ErrGrantRevoked    = errors.New("grant is no longer valid")
	ErrTenantRevoked   = errors.New("tenant is revoked")
	ErrHopLimit        = errors.New("redirect hop limit reached")
	ErrMethodWidened   = errors.New("redirect widens the granted method set")
	ErrSchemeDowngrade = errors.New("https to http downgrade is not allowed")
	ErrGrantCap        = errors.New("live grant cap reached")
	ErrBadRule         = errors.New("egress rule is invalid")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// Target is one destination a client wants to reach.
type Target struct {
	Scheme string
	Host   string
	Port   int
	Path   string
	Method string
}

// EgressRequest asks for a grant for one tenant and destination.
type EgressRequest struct {
	Tenant string
	Target Target
}

// Grant is a short lived authorisation bound to tenant, destination and method
// set.
type Grant struct {
	ID        string
	Tenant    string
	Scheme    string
	Host      string
	Port      int
	Methods   []string
	ExpiresAt int64
	Hops      int
	CreatedAt int64
}

// Rule is one allow list entry. Pattern is an exact host, a "*.suffix"
// wildcard, or "#category" referring to Config.Categories.
type Rule struct {
	Tenant   string
	Pattern  string
	Ports    []int
	Methods  []string
	Priority int
}

// Config holds the immutable policy set and the limits of the gate.
type Config struct {
	Rules      []Rule
	Categories map[string][]string
	TTL        int64
	MaxHops    int
	MaxGrants  int
}

// EgressGate owns the live grants and the logical clock.
type EgressGate struct {
}

// NewEgressGate builds a gate from the given config.
func NewEgressGate(cfg Config) *EgressGate {
	return &EgressGate{}
}

// Authorize evaluates the request and issues a grant when a rule allows it.
func (g *EgressGate) Authorize(req EgressRequest) (Grant, error) {
	return Grant{}, errNotImplemented
}

// FollowRedirect re-evaluates the next hop under an existing grant.
func (g *EgressGate) FollowRedirect(grant Grant, next Target) (Grant, error) {
	return Grant{}, errNotImplemented
}

// Revoke invalidates every live grant of the tenant and returns how many were
// dropped.
func (g *EgressGate) Revoke(tenant string) int {
	return 0
}

// Snapshot serialises grants, revoked tenants and the logical clock.
func (g *EgressGate) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the live state from a snapshot.
func (g *EgressGate) Restore(data []byte) error {
	return errNotImplemented
}

// MatchRule returns the strongest rule of the tenant matching host and port.
func (g *EgressGate) MatchRule(tenant string, target Target) (Rule, MatchKind, bool) {
	return Rule{}, MatchNone, false
}

// Active lists the live grants of one tenant ordered by grant id.
func (g *EgressGate) Active(tenant string) []Grant {
	return nil
}

// Grant reads one live grant by id.
func (g *EgressGate) Grant(id string) (Grant, bool) {
	return Grant{}, false
}

// ValidateRules checks the configured rules before any authorisation happens.
func (g *EgressGate) ValidateRules() error {
	return errNotImplemented
}

// Advance moves the logical clock forward by the given milliseconds.
func (g *EgressGate) Advance(ms int64) {
}

// Clock reports the current logical time in milliseconds.
func (g *EgressGate) Clock() int64 {
	return 0
}
