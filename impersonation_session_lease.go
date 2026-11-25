// Package task governs time limited administrator impersonation sessions with
// user consent, read only or writable modes and immediate revocation.
package task

import (
	"errors"
	"time"
)

// errNotImplemented marks behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Mode is the privilege level written into a consent and its lease.
type Mode string

// Supported impersonation modes.
const (
	ModeReadOnly  Mode = "read_only"
	ModeReadWrite Mode = "read_write"
)

// Consent states.
const (
	ConsentPending = "pending"
	ConsentGranted = "granted"
)

// Lease states.
const (
	LeaseActive  = "active"
	LeaseRevoked = "revoked"
)

// Envelope actions and reasons.
const (
	ActionRevoke     = "revoke"
	ReasonAuthorized = "authorized"
	ReasonReadOnly   = "read_only"
)

// Errors returned by the lease manager.
var (
	ErrBlankAdmin      = errors.New("admin id is required")
	ErrBlankUser       = errors.New("user id is required")
	ErrBlankScope      = errors.New("scope is required")
	ErrBadMode         = errors.New("mode must be read_only or read_write")
	ErrBadAction       = errors.New("action is required")
	ErrBadReason       = errors.New("a non blank reason is required")
	ErrBadActor        = errors.New("actor id is required")
	ErrUnknownConsent  = errors.New("unknown consent")
	ErrConsentMismatch = errors.New("consent belongs to another user")
	ErrConsentUsed     = errors.New("consent is already granted")
	ErrConsentExpired  = errors.New("consent has expired")
	ErrUserLeaseLimit  = errors.New("too many live leases for this user")
	ErrAdminLeaseLimit = errors.New("too many live leases held by this admin")
	ErrUnknownLease    = errors.New("unknown lease")
	ErrLeaseRevoked    = errors.New("lease is revoked")
	ErrLeaseExpired    = errors.New("lease has expired")
	ErrLeaseBusy       = errors.New("lease already has a live session")
	ErrUnknownSession  = errors.New("unknown impersonation session")
	ErrReadOnlyLease   = errors.New("lease is read only")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// Limits bounds lease lifetime, consent lifetime and the live tables. Now
// supplies the clock and may be replaced by a deterministic one in tests.
type Limits struct {
	LeaseTTL          time.Duration
	ConsentTTL        time.Duration
	MaxLeasesPerUser  int
	MaxLeasesPerAdmin int
	MaxEnvelopes      int
	Now               func() time.Time
}

// Consent is one user approval for an admin to act on their behalf.
type Consent struct {
	ID        string    `json:"id"`
	AdminID   string    `json:"admin_id"`
	UserID    string    `json:"user_id"`
	Scope     string    `json:"scope"`
	Mode      Mode      `json:"mode"`
	State     string    `json:"state"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Lease is the time limited authority derived from a granted consent.
type Lease struct {
	ID        string        `json:"id"`
	ConsentID string        `json:"consent_id"`
	AdminID   string        `json:"admin_id"`
	UserID    string        `json:"user_id"`
	Scope     string        `json:"scope"`
	Mode      Mode          `json:"mode"`
	State     string        `json:"state"`
	IssuedAt  time.Time     `json:"issued_at"`
	ExpiresAt time.Time     `json:"expires_at"`
	Remaining time.Duration `json:"remaining"`
}

// Session is one browsing context opened on a lease.
type Session struct {
	ID        string    `json:"id"`
	LeaseID   string    `json:"lease_id"`
	AdminID   string    `json:"admin_id"`
	UserID    string    `json:"user_id"`
	Mode      Mode      `json:"mode"`
	StartedAt time.Time `json:"started_at"`
}

// Envelope records one authorised or refused operation of a lease.
type Envelope struct {
	ID        string    `json:"id"`
	LeaseID   string    `json:"lease_id"`
	SessionID string    `json:"session_id"`
	Action    string    `json:"action"`
	Mode      Mode      `json:"mode"`
	Allowed   bool      `json:"allowed"`
	Reason    string    `json:"reason"`
	At        time.Time `json:"at"`
	Seq       int64     `json:"seq"`
}

// ImpersonationSessionLease owns consents, leases, sessions and envelopes.
type ImpersonationSessionLease struct {
	limits Limits
}

// NewImpersonationSessionLease builds a manager with the given caps.
func NewImpersonationSessionLease(limits Limits) *ImpersonationSessionLease {
	return &ImpersonationSessionLease{limits: limits}
}

// RequestConsent asks a user to approve one administrator action.
func (m *ImpersonationSessionLease) RequestConsent(adminID, userID, scope string, mode Mode) (Consent, error) {
	return Consent{}, errNotImplemented
}

// Grant turns a pending consent into an active lease for the named user.
func (m *ImpersonationSessionLease) Grant(consentID, userID string) (Lease, error) {
	return Lease{}, errNotImplemented
}

// Begin opens one session on an active lease.
func (m *ImpersonationSessionLease) Begin(leaseID string) (Session, error) {
	return Session{}, errNotImplemented
}

// Authorize admits or refuses one action inside a session.
func (m *ImpersonationSessionLease) Authorize(sessionID, action string) error {
	return errNotImplemented
}

// Revoke stops a lease immediately and records the actor and reason.
func (m *ImpersonationSessionLease) Revoke(leaseID, actorID, reason string) error {
	return errNotImplemented
}

// Active lists the live leases of one user, ordered by lease id.
func (m *ImpersonationSessionLease) Active(userID string) []Lease {
	return nil
}

// LeaseState reads one lease with its remaining lifetime.
func (m *ImpersonationSessionLease) LeaseState(leaseID string) (Lease, error) {
	return Lease{}, errNotImplemented
}

// ConsentState reads one consent.
func (m *ImpersonationSessionLease) ConsentState(consentID string) (Consent, error) {
	return Consent{}, errNotImplemented
}

// Envelopes returns the retained envelopes of one lease, oldest first.
func (m *ImpersonationSessionLease) Envelopes(leaseID string) []Envelope {
	return nil
}

// EndSession closes one live session and frees the lease.
func (m *ImpersonationSessionLease) EndSession(sessionID string) error {
	return errNotImplemented
}

// Snapshot serialises consents, leases, sessions and envelopes.
func (m *ImpersonationSessionLease) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (m *ImpersonationSessionLease) Restore(data []byte) error {
	return errNotImplemented
}
