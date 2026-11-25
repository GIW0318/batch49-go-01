// Package task manages the lifecycle of tenant level SSO connections:
// drafting, domain proof, connectivity test, enablement and rollouts.
package task

import (
	"errors"
	"time"
)

// errNotImplemented marks behaviour that is not built yet.
var errNotImplemented = errors.New("not implemented")

// Connection lifecycle states.
const (
	StateDraft         = "draft"
	StateVerifyPending = "verify_pending"
	StateTested        = "tested"
	StateEnabled       = "enabled"
	StateDisabled      = "disabled"
)

// Errors returned by the service.
var (
	ErrBlankTenant       = errors.New("tenant id is required")
	ErrBlankDomain       = errors.New("domain is required")
	ErrTenantLimit       = errors.New("connection limit reached for this tenant")
	ErrUnknownConnection = errors.New("unknown sso connection")
	ErrUnknownDomain     = errors.New("domain is not attached to this connection")
	ErrDomainTaken       = errors.New("domain is already attached to another connection")
	ErrBadProof          = errors.New("domain verification proof is invalid")
	ErrDomainUnverified  = errors.New("domain is not verified")
	ErrBadState          = errors.New("connection state does not allow this action")
	ErrBadRollout        = errors.New("rollout percent must be between 0 and 100")
	ErrBreakGlassDenied  = errors.New("break glass login denied")
	ErrBadSnapshot       = errors.New("snapshot payload is invalid")
)

// SsoLimits bounds one service instance.
type SsoLimits struct {
	MaxConnectionsPerTenant int
	MaxDomainsPerConnection int
	MaxBreakGlassPerDay     int
}

// ConnectionConfig is the submitted SSO configuration.
type ConnectionConfig struct {
	DisplayName string
	Domains     []string
	Issuer      string
	ClientID    string
}

// Connection is one tenant SSO connection.
type Connection struct {
	ID              string
	TenantID        string
	DisplayName     string
	State           string
	Domains         []string
	VerifiedDomains []string
	RolloutPercent  int
	Enforced        bool
	UpdatedAt       time.Time
}

// TestResult is the outcome of a connectivity test.
type TestResult struct {
	OK       bool
	Checked  int
	Warnings []string
}

// TenantSsoConnection owns every tenant connection.
type TenantSsoConnection struct {
	limits SsoLimits
}

// NewTenantSsoConnection builds a service with the given limits.
func NewTenantSsoConnection(limits SsoLimits) *TenantSsoConnection {
	return &TenantSsoConnection{limits: limits}
}

// Create registers a new draft connection for a tenant.
func (t *TenantSsoConnection) Create(tenantID string, cfg ConnectionConfig) (Connection, error) {
	return Connection{}, errNotImplemented
}

// VerifyDomain proves ownership of one domain of the connection.
func (t *TenantSsoConnection) VerifyDomain(tenantID, connID, domain, proof string) error {
	return errNotImplemented
}

// Test runs the connectivity test for a verified connection.
func (t *TenantSsoConnection) Test(tenantID, connID string) (TestResult, error) {
	return TestResult{}, errNotImplemented
}

// Enable turns a tested connection on.
func (t *TenantSsoConnection) Enable(tenantID, connID string) error {
	return errNotImplemented
}

// Disable turns a connection off without dropping its configuration.
func (t *TenantSsoConnection) Disable(tenantID, connID string) error {
	return errNotImplemented
}

// Rollout sets the rollout percentage of a connection.
func (t *TenantSsoConnection) Rollout(tenantID, connID string, percent int) error {
	return errNotImplemented
}

// Snapshot serialises every connection of every tenant.
func (t *TenantSsoConnection) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the service state from a snapshot.
func (t *TenantSsoConnection) Restore(data []byte) error {
	return errNotImplemented
}

// Get returns one connection by tenant and identifier.
func (t *TenantSsoConnection) Get(tenantID, connID string) (Connection, bool) {
	return Connection{}, false
}

// RouteLogin picks the connection that should serve a tenant login.
func (t *TenantSsoConnection) RouteLogin(tenantID, email string) (Connection, error) {
	return Connection{}, errNotImplemented
}

// SetEnforceSSO turns mandatory SSO on or off for a tenant.
func (t *TenantSsoConnection) SetEnforceSSO(tenantID string, on bool) error {
	return errNotImplemented
}

// BreakGlass authenticates the emergency local administrator.
func (t *TenantSsoConnection) BreakGlass(tenantID, password string) (bool, error) {
	return false, errNotImplemented
}
