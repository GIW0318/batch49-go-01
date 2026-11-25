// Package task routes a login email address to the tenant SSO connection that
// owns the email domain, with claims, verification and releases.
package task

import (
	"errors"
	"time"
)

// errNotImplemented marks behaviour that is not built yet.
var errNotImplemented = errors.New("not implemented")

// Domain claim states.
const (
	ClaimPending  = "claim_pending"
	ClaimActive   = "claim_active"
	ClaimDisputed = "claim_disputed"
	ClaimReleased = "claim_released"
)

// Errors returned by the router.
var (
	ErrBlankTenant  = errors.New("tenant id is required")
	ErrBlankDomain  = errors.New("domain is required")
	ErrBadEmail     = errors.New("email address is malformed")
	ErrUnknownClaim = errors.New("unknown domain claim")
	ErrBadProof     = errors.New("domain proof is invalid")
	ErrNoClaim      = errors.New("no claim for this tenant and domain")
	ErrDomainLimit  = errors.New("domain route table is full")
	ErrClaimLimit   = errors.New("claim limit reached for this tenant")
	ErrBadSnapshot  = errors.New("snapshot payload is invalid")
)

// DomainRouterLimits bounds one router instance.
type DomainRouterLimits struct {
	MaxDomains         int
	MaxClaimsPerTenant int
	CacheTTL           time.Duration
}

// Route is the outcome of routing one email address.
type Route struct {
	Email         string
	Domain        string
	MatchedDomain string
	TenantID      string
	ClaimID       string
	Default       bool
	Cached        bool
}

// Claim is one tenant claim over one domain.
type Claim struct {
	ID         string
	TenantID   string
	Domain     string
	State      string
	Proof      string
	CreatedAt  time.Time
	VerifiedAt time.Time
}

// SsoDomainRouter owns the global domain ownership table.
type SsoDomainRouter struct {
	limits DomainRouterLimits
}

// NewSsoDomainRouter builds a router with the given limits.
func NewSsoDomainRouter(limits DomainRouterLimits) *SsoDomainRouter {
	return &SsoDomainRouter{limits: limits}
}

// Route resolves an email address to a tenant connection.
func (r *SsoDomainRouter) Route(email string) (Route, error) {
	return Route{}, errNotImplemented
}

// Claim records a tenant claim over a domain.
func (r *SsoDomainRouter) Claim(tenantID, domain string) (Claim, error) {
	return Claim{}, errNotImplemented
}

// Verify activates a pending claim after proof of ownership.
func (r *SsoDomainRouter) Verify(claimID, proof string) error {
	return errNotImplemented
}

// Release drops the claim a tenant holds over a domain.
func (r *SsoDomainRouter) Release(tenantID, domain string) error {
	return errNotImplemented
}

// Invalidate drops any cached route for a domain.
func (r *SsoDomainRouter) Invalidate(domain string) error {
	return errNotImplemented
}

// Snapshot serialises the domain ownership table.
func (r *SsoDomainRouter) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the router state from a snapshot.
func (r *SsoDomainRouter) Restore(data []byte) error {
	return errNotImplemented
}

// Lookup returns the active route of an exact domain.
func (r *SsoDomainRouter) Lookup(domain string) (Route, bool) {
	return Route{}, false
}

// Claims lists the claims of one tenant.
func (r *SsoDomainRouter) Claims(tenantID string) []Claim {
	return nil
}

// Disputes lists frozen claims.
func (r *SsoDomainRouter) Disputes() []Claim {
	return nil
}

// Capacity returns current occupancy and the hard limit of the route table.
func (r *SsoDomainRouter) Capacity() (int, int) {
	return 0, 0
}
