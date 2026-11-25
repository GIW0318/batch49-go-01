// Package task gates mTLS handshakes on a revocation ledger: serial and issuer
// revocations, revocation epochs propagated to terminating workers, session
// draining and restart safe snapshots.
package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Decision reasons.
const (
	ReasonOK        = "ok"
	ReasonRevoked   = "revoked"
	ReasonExpired   = "expired"
	ReasonStaleView = "stale_view"
)

// Errors returned by the gate.
var (
	ErrBadConfig       = errors.New("gate config is invalid")
	ErrBadRevocation   = errors.New("revocation record is invalid")
	ErrEpochFuture     = errors.New("worker epoch is ahead of the gate")
	ErrEpochRegression = errors.New("worker epoch would go backwards")
	ErrBadHandshake    = errors.New("handshake is invalid")
	ErrBadSession      = errors.New("session is invalid")
	ErrSessionLimit    = errors.New("session limit reached for this serial")
	ErrRevocationLimit = errors.New("revocation limit reached")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// Config bounds one gate. MaxLag is the largest epoch distance a worker may
// trail before handshakes fail closed, MaxRevoked caps the revoked serials and
// MaxSessionsPerSerial caps the live sessions of one certificate.
type Config struct {
	MaxLag               uint64
	MaxRevoked           int
	MaxSessionsPerSerial int
	Now                  func() int64
}

// Revocation is one revoked certificate serial.
type Revocation struct {
	Serial string
	Issuer string
	At     int64
	Reason string
}

// Handshake is one inbound client certificate to admit.
type Handshake struct {
	Serial   string
	Issuer   string
	WorkerID string
	NotAfter int64
}

// Decision is the admission result of one handshake.
type Decision struct {
	Allow  bool
	Reason string
	Epoch  uint64
}

// Session is one established mTLS session.
type Session struct {
	ID       string
	Serial   string
	WorkerID string
	At       int64
}

// MtlsRevocationGate owns the revocation ledger, the worker epoch views and the
// session table.
type MtlsRevocationGate struct {
	cfg Config
}

// NewMtlsRevocationGate validates the configuration and builds an empty gate.
func NewMtlsRevocationGate(cfg Config) (*MtlsRevocationGate, error) {
	return nil, errNotImplemented
}

// PublishRevocation revokes one serial and bumps the revocation epoch. A
// repeated serial is idempotent and does not bump again.
func (g *MtlsRevocationGate) PublishRevocation(rec Revocation) (uint64, error) {
	return 0, errNotImplemented
}

// RevokeIssuer revokes every certificate of one issuer and bumps the epoch. A
// repeated issuer is idempotent.
func (g *MtlsRevocationGate) RevokeIssuer(issuer string) (uint64, error) {
	return 0, errNotImplemented
}

// Epoch reports the current revocation epoch.
func (g *MtlsRevocationGate) Epoch() uint64 {
	return 0
}

// Admit decides whether one client certificate may complete a handshake on the
// given worker.
func (g *MtlsRevocationGate) Admit(hs Handshake) (Decision, error) {
	return Decision{}, errNotImplemented
}

// SyncWorker records that one worker now serves the given revocation epoch.
func (g *MtlsRevocationGate) SyncWorker(workerID string, epoch uint64) error {
	return errNotImplemented
}

// AppliedEpoch reports the epoch one worker last acknowledged.
func (g *MtlsRevocationGate) AppliedEpoch(workerID string) uint64 {
	return 0
}

// RegisterSession records one established session of a certificate.
func (g *MtlsRevocationGate) RegisterSession(s Session) error {
	return errNotImplemented
}

// SessionsFor lists the live session ids of one serial, sorted.
func (g *MtlsRevocationGate) SessionsFor(serial string) []string {
	return nil
}

// DrainSessions removes and returns the live session ids of one serial.
func (g *MtlsRevocationGate) DrainSessions(serial string) ([]string, error) {
	return nil, errNotImplemented
}

// Revoked reports whether one serial was revoked by name.
func (g *MtlsRevocationGate) Revoked(serial string) bool {
	return false
}

// Snapshot serialises the revoked serials, the revoked issuers, the epoch, the
// worker views and the session table.
func (g *MtlsRevocationGate) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *MtlsRevocationGate) Restore(data []byte) error {
	return errNotImplemented
}
