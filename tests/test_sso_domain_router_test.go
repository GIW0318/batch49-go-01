package task

import (
	"errors"
	"testing"
	"time"

	impl "gsb/batch49-go-safety-a-24"
)

var domainBase = time.Date(2026, 6, 8, 14, 0, 0, 0, time.UTC)

func domainLimits() impl.DomainRouterLimits {
	return impl.DomainRouterLimits{
		MaxDomains:         8,
		MaxClaimsPerTenant: 3,
		CacheTTL:           time.Minute,
	}
}

func findClaim(claims []impl.Claim, id string) (impl.Claim, bool) {
	for _, claim := range claims {
		if claim.ID == id {
			return claim, true
		}
	}
	return impl.Claim{}, false
}

func TestClaimStartsPending(t *testing.T) {
	r := impl.NewSsoDomainRouter(domainLimits())
	claim, err := r.Claim("tenant-1", "acme.example")
	if err != nil {
		t.Fatalf("claim must succeed: %v", err)
	}
	if claim.ID == "" {
		t.Fatalf("claim identifier must not be empty")
	}
	if claim.State != impl.ClaimPending {
		t.Fatalf("new claim must be pending, got %q", claim.State)
	}
	if claim.TenantID != "tenant-1" || claim.Domain != "acme.example" {
		t.Fatalf("claim must echo tenant and domain, got %q / %q", claim.TenantID, claim.Domain)
	}
}

func TestClaimRejectsBlankDomain(t *testing.T) {
	r := impl.NewSsoDomainRouter(domainLimits())
	if _, err := r.Claim("tenant-1", ""); !errors.Is(err, impl.ErrBlankDomain) {
		t.Fatalf("blank domain must return ErrBlankDomain, got %v", err)
	}
}

func TestClaimRejectsBlankTenant(t *testing.T) {
	r := impl.NewSsoDomainRouter(domainLimits())
	if _, err := r.Claim("", "acme.example"); !errors.Is(err, impl.ErrBlankTenant) {
		t.Fatalf("blank tenant must return ErrBlankTenant, got %v", err)
	}
}

func TestVerifyActivatesClaim(t *testing.T) {
	r := impl.NewSsoDomainRouter(domainLimits())
	claim, err := r.Claim("tenant-1", "acme.example")
	if err != nil {
		t.Fatalf("claim must succeed: %v", err)
	}
	if err := r.Verify(claim.ID, "txt-record=gsb-verify"); err != nil {
		t.Fatalf("verify must succeed: %v", err)
	}
	claims := r.Claims("tenant-1")
	got, ok := findClaim(claims, claim.ID)
	if !ok {
		t.Fatalf("claim %q must be listed after verification", claim.ID)
	}
	if got.State != impl.ClaimActive {
		t.Fatalf("verified claim must be active, got %q", got.State)
	}
	if got.VerifiedAt.IsZero() {
		t.Fatalf("verified claim must carry a verification timestamp")
	}
}

func TestVerifyRejectsBadProof(t *testing.T) {
	r := impl.NewSsoDomainRouter(domainLimits())
	claim, err := r.Claim("tenant-1", "acme.example")
	if err != nil {
		t.Fatalf("claim must succeed: %v", err)
	}
	if err := r.Verify(claim.ID, ""); !errors.Is(err, impl.ErrBadProof) {
		t.Fatalf("blank proof must return ErrBadProof, got %v", err)
	}
}

func TestVerifyRejectsUnknownClaim(t *testing.T) {
	r := impl.NewSsoDomainRouter(domainLimits())
	if err := r.Verify("claim-missing", "txt-record=gsb-verify"); !errors.Is(err, impl.ErrUnknownClaim) {
		t.Fatalf("unknown claim must return ErrUnknownClaim, got %v", err)
	}
}

func TestRouteMatchesVerifiedDomain(t *testing.T) {
	r := impl.NewSsoDomainRouter(domainLimits())
	claim, err := r.Claim("tenant-1", "acme.example")
	if err != nil {
		t.Fatalf("claim must succeed: %v", err)
	}
	if err := r.Verify(claim.ID, "txt-record=gsb-verify"); err != nil {
		t.Fatalf("verify must succeed: %v", err)
	}
	route, err := r.Route("bob@acme.example")
	if err != nil {
		t.Fatalf("route must succeed: %v", err)
	}
	if route.Default {
		t.Fatalf("verified domain must not fall back to the default route")
	}
	if route.TenantID != "tenant-1" {
		t.Fatalf("route must return the owning tenant, got %q", route.TenantID)
	}
	if route.MatchedDomain != "acme.example" {
		t.Fatalf("route must report the matched domain, got %q", route.MatchedDomain)
	}
	if route.ClaimID != claim.ID {
		t.Fatalf("route must report the claim identifier, got %q want %q", route.ClaimID, claim.ID)
	}
}

func TestRouteFallsBackToDefault(t *testing.T) {
	r := impl.NewSsoDomainRouter(domainLimits())
	route, err := r.Route("eve@unclaimed.example")
	if err != nil {
		t.Fatalf("unclaimed domain must not error: %v", err)
	}
	if !route.Default {
		t.Fatalf("unclaimed domain must use the default route")
	}
	if route.TenantID != "" {
		t.Fatalf("default route must not carry a tenant, got %q", route.TenantID)
	}
}

func TestRouteRejectsMalformedEmail(t *testing.T) {
	r := impl.NewSsoDomainRouter(domainLimits())
	if _, err := r.Route("bob(at)acme.example"); !errors.Is(err, impl.ErrBadEmail) {
		t.Fatalf("email without a single at sign must return ErrBadEmail, got %v", err)
	}
	if _, err := r.Route(""); !errors.Is(err, impl.ErrBadEmail) {
		t.Fatalf("blank email must return ErrBadEmail, got %v", err)
	}
}

func TestReleaseRemovesRoute(t *testing.T) {
	r := impl.NewSsoDomainRouter(domainLimits())
	claim, err := r.Claim("tenant-1", "acme.example")
	if err != nil {
		t.Fatalf("claim must succeed: %v", err)
	}
	if err := r.Verify(claim.ID, "txt-record=gsb-verify"); err != nil {
		t.Fatalf("verify must succeed: %v", err)
	}
	if err := r.Release("tenant-1", "acme.example"); err != nil {
		t.Fatalf("release must succeed: %v", err)
	}
	if _, ok := r.Lookup("acme.example"); ok {
		t.Fatalf("released domain must not stay in the route table")
	}
	route, err := r.Route("bob@acme.example")
	if err != nil {
		t.Fatalf("route after release must not error: %v", err)
	}
	if !route.Default {
		t.Fatalf("released domain must fall back to the default route")
	}
}

func TestReleaseRejectsUnknownClaim(t *testing.T) {
	r := impl.NewSsoDomainRouter(domainLimits())
	if err := r.Release("tenant-1", "acme.example"); !errors.Is(err, impl.ErrNoClaim) {
		t.Fatalf("release without a claim must return ErrNoClaim, got %v", err)
	}
}

func TestDomainCapacityLimit(t *testing.T) {
	limits := domainLimits()
	limits.MaxDomains = 2
	limits.MaxClaimsPerTenant = 5
	r := impl.NewSsoDomainRouter(limits)
	if _, err := r.Claim("tenant-1", "one.example"); err != nil {
		t.Fatalf("first claim must succeed: %v", err)
	}
	if _, err := r.Claim("tenant-2", "two.example"); err != nil {
		t.Fatalf("second claim must succeed: %v", err)
	}
	if _, err := r.Claim("tenant-3", "three.example"); !errors.Is(err, impl.ErrDomainLimit) {
		t.Fatalf("third domain must return ErrDomainLimit, got %v", err)
	}
}

func TestClaimsPerTenantLimit(t *testing.T) {
	limits := domainLimits()
	limits.MaxDomains = 5
	limits.MaxClaimsPerTenant = 1
	r := impl.NewSsoDomainRouter(limits)
	if _, err := r.Claim("tenant-1", "one.example"); err != nil {
		t.Fatalf("first claim must succeed: %v", err)
	}
	if _, err := r.Claim("tenant-1", "two.example"); !errors.Is(err, impl.ErrClaimLimit) {
		t.Fatalf("second claim for the tenant must return ErrClaimLimit, got %v", err)
	}
}

func TestSnapshotRestoreKeepsOwnership(t *testing.T) {
	r := impl.NewSsoDomainRouter(domainLimits())
	claim, err := r.Claim("tenant-1", "acme.example")
	if err != nil {
		t.Fatalf("claim must succeed: %v", err)
	}
	if err := r.Verify(claim.ID, "txt-record=gsb-verify"); err != nil {
		t.Fatalf("verify must succeed: %v", err)
	}
	snap, err := r.Snapshot()
	if err != nil {
		t.Fatalf("snapshot must succeed: %v", err)
	}
	restored := impl.NewSsoDomainRouter(domainLimits())
	if err := restored.Restore(snap); err != nil {
		t.Fatalf("restore must succeed: %v", err)
	}
	route, ok := restored.Lookup("acme.example")
	if !ok {
		t.Fatalf("restored route must be found")
	}
	if route.TenantID != "tenant-1" || route.ClaimID != claim.ID {
		t.Fatalf("restored route must keep ownership, got tenant=%q claim=%q", route.TenantID, route.ClaimID)
	}
	routed, err := restored.Route("bob@acme.example")
	if err != nil {
		t.Fatalf("route after restore must succeed: %v", err)
	}
	if routed.TenantID != "tenant-1" {
		t.Fatalf("route after restore must match the tenant, got %q", routed.TenantID)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	r := impl.NewSsoDomainRouter(domainLimits())
	if err := r.Restore([]byte("corrupt::snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must return ErrBadSnapshot, got %v", err)
	}
}
