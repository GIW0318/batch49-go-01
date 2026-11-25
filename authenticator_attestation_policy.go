// Package task grades WebAuthn registration attempts against the per tenant
// authenticator attestation policy before a credential reaches the ledger.
package task

import (
	"errors"
)

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Attestation conveyance modes understood by the policy engine.
const (
	ConveyanceNone       = "none"
	ConveyanceIndirect   = "indirect"
	ConveyanceEnterprise = "enterprise"
)

// Authenticator strength tiers.
const (
	StrengthLow    = "low"
	StrengthMedium = "medium"
	StrengthHigh   = "high"
)

// Reason codes attached to a decision.
const (
	ReasonAllowlisted         = "reason_allowlisted"
	ReasonDenylisted          = "reason_denylisted"
	ReasonNotAllowlisted      = "reason_not_allowlisted"
	ReasonConveyanceNone      = "reason_conveyance_none"
	ReasonUntrustedChain      = "reason_untrusted_chain"
	ReasonAttestationRequired = "reason_attestation_required"
	ReasonUserNotVerified     = "reason_user_not_verified"
)

// Errors returned by the policy engine.
var (
	ErrBlankTenant       = errors.New("tenant id is required")
	ErrBadConveyance     = errors.New("unknown attestation conveyance")
	ErrBadStrength       = errors.New("unknown strength tier")
	ErrUnknownTenant     = errors.New("tenant has no policy")
	ErrPolicyLimit       = errors.New("policy version limit reached for tenant")
	ErrAAGUIDLimit       = errors.New("aaguid list limit reached")
	ErrTrustAnchorLimit  = errors.New("trust anchor limit reached")
	ErrBlankRegistration = errors.New("registration id is required")
	ErrBadSnapshot       = errors.New("snapshot payload is invalid")
)

// Limits bounds the policy tables.
type Limits struct {
	MaxEntriesPerList int
	MaxTrustAnchors   int
	MaxVersions       int
}

// Policy is the attestation policy configured for one tenant.
type Policy struct {
	TenantID           string   `json:"tenant_id"`
	Conveyance         string   `json:"conveyance"`
	AllowAAGUIDs       []string `json:"allow_aaguids"`
	DenyAAGUIDs        []string `json:"deny_aaguids"`
	TrustAnchors       []string `json:"trust_anchors"`
	RequireAttestation bool     `json:"require_attestation"`
	MinStrength        string   `json:"min_strength"`
}

// Version describes one stored policy revision.
type Version struct {
	TenantID   string `json:"tenant_id"`
	Generation int    `json:"generation"`
	Allow      int    `json:"allow"`
	Deny       int    `json:"deny"`
	Anchors    int    `json:"anchors"`
}

// Registration is one WebAuthn registration presented by a client.
type Registration struct {
	ID           string   `json:"id"`
	AAGUID       string   `json:"aaguid"`
	Conveyance   string   `json:"conveyance"`
	Chain        []string `json:"chain"`
	Attested     bool     `json:"attested"`
	UserVerified bool     `json:"user_verified"`
}

// Decision is the graded outcome for one registration.
type Decision struct {
	ID             string   `json:"id"`
	TenantID       string   `json:"tenant_id"`
	RegistrationID string   `json:"registration_id"`
	Accepted       bool     `json:"accepted"`
	Strength       string   `json:"strength"`
	Generation     int      `json:"generation"`
	Codes          []string `json:"codes"`
	Seq            int64    `json:"seq"`
}

// Reason is one explanation line attached to a decision.
type Reason struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// AuthenticatorAttestationPolicy owns the policies, their versions and the
// evaluation cache.
type AuthenticatorAttestationPolicy struct {
	limits Limits
}

// NewAuthenticatorAttestationPolicy builds a policy engine with the given caps.
func NewAuthenticatorAttestationPolicy(limits Limits) *AuthenticatorAttestationPolicy {
	return &AuthenticatorAttestationPolicy{limits: limits}
}

// SetPolicy installs a new policy revision and returns the stored version.
func (e *AuthenticatorAttestationPolicy) SetPolicy(tenantID string, p Policy) (Version, error) {
	return Version{}, errNotImplemented
}

// Policy reads the current policy of one tenant.
func (e *AuthenticatorAttestationPolicy) Policy(tenantID string) (Policy, error) {
	return Policy{}, errNotImplemented
}

// Versions lists the stored policy revisions of one tenant, oldest first.
func (e *AuthenticatorAttestationPolicy) Versions(tenantID string) []Version {
	return nil
}

// Evaluate grades one registration against the tenant policy.
func (e *AuthenticatorAttestationPolicy) Evaluate(tenantID string, reg Registration) (Decision, error) {
	return Decision{}, errNotImplemented
}

// Decision reads one cached decision by id.
func (e *AuthenticatorAttestationPolicy) Decision(decisionID string) (Decision, bool) {
	return Decision{}, false
}

// Explain returns the ordered reasons behind one cached decision.
func (e *AuthenticatorAttestationPolicy) Explain(decisionID string) []Reason {
	return nil
}

// Reevaluate re-grades the cached registrations of one tenant and reports how
// many credentials were revisited.
func (e *AuthenticatorAttestationPolicy) Reevaluate(tenantID string) (int, error) {
	return 0, errNotImplemented
}

// Snapshot serialises the policies, the versions and the decision cache.
func (e *AuthenticatorAttestationPolicy) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (e *AuthenticatorAttestationPolicy) Restore(data []byte) error {
	return errNotImplemented
}
