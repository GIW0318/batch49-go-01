// Package task runs the ticket flow of a service account key: request, review,
// issue, activate and finally disable.
package task

import (
	"errors"
	"time"
)

// errNotImplemented is returned by behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Request states.
const (
	RequestPending  = "pending"
	RequestApproved = "approved"
	RequestRejected = "rejected"
	RequestIssued   = "issued"
)

// Key states.
const (
	KeyPendingActivation = "pending_activation"
	KeyActive            = "active"
	KeyDisabled          = "disabled"
	KeyExpired           = "expired"
)

// Errors reported by the issuer.
var (
	ErrBlankID         = errors.New("account, requester or approver id is required")
	ErrBadSpec         = errors.New("key name, scopes and ttl are required")
	ErrTTLTooLong      = errors.New("requested ttl exceeds the hard limit")
	ErrSelfApproval    = errors.New("requester cannot approve the own request")
	ErrRequestNotFound = errors.New("request not found")
	ErrRequestClosed   = errors.New("request is already closed")
	ErrNotApproved     = errors.New("request has not reached the approval quorum")
	ErrAlreadyIssued   = errors.New("request was already issued")
	ErrRequestLimit    = errors.New("pending request limit reached for this account")
	ErrKeyLimit        = errors.New("key limit reached for this account")
	ErrKeyNotFound     = errors.New("key not found")
	ErrTooEarly        = errors.New("key activation delay has not elapsed")
	ErrKeyExpired      = errors.New("key has expired")
	ErrKeyDisabled     = errors.New("key is disabled")
	ErrBadReason       = errors.New("disable reason is required")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// KeySpec is what the requester asks for.
type KeySpec struct {
	Name   string        `json:"name"`
	Scopes []string      `json:"scopes"`
	TTL    time.Duration `json:"ttl"`
}

// Request is one key ticket in the review flow.
type Request struct {
	ID           string   `json:"id"`
	AccountID    string   `json:"account_id"`
	RequesterID  string   `json:"requester_id"`
	Spec         KeySpec  `json:"spec"`
	Approvals    []string `json:"approvals"`
	Required     int      `json:"required"`
	State        string   `json:"state"`
	RejectReason string   `json:"reject_reason"`
	Seq          int64    `json:"seq"`
}

// IssuedKey is the one response that carries the plaintext secret.
type IssuedKey struct {
	KeyID      string    `json:"key_id"`
	RequestID  string    `json:"request_id"`
	AccountID  string    `json:"account_id"`
	Plaintext  string    `json:"plaintext"`
	Prefix     string    `json:"prefix"`
	Scopes     []string  `json:"scopes"`
	State      string    `json:"state"`
	IssuedAt   time.Time `json:"issued_at"`
	ActivateAt time.Time `json:"activate_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Seq        int64     `json:"seq"`
}

// KeyMeta is what the issuer exposes after issuance: never the plaintext.
type KeyMeta struct {
	KeyID      string    `json:"key_id"`
	AccountID  string    `json:"account_id"`
	Prefix     string    `json:"prefix"`
	Scopes     []string  `json:"scopes"`
	State      string    `json:"state"`
	ActivateAt time.Time `json:"activate_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Seq        int64     `json:"seq"`
}

// Options bounds the issuer and supplies the clock.
type Options struct {
	RequiredApprovals  int
	MaxTTL             time.Duration
	ActivationDelay    time.Duration
	MaxPendingRequests int
	MaxKeysPerAccount  int
	Now                func() time.Time
}

// ServiceAccountKeyIssuer owns the request table, the key table and the
// monotonic counters.
type ServiceAccountKeyIssuer struct {
	opts Options
}

// NewServiceAccountKeyIssuer builds an issuer with the given options.
func NewServiceAccountKeyIssuer(opts Options) *ServiceAccountKeyIssuer {
	return &ServiceAccountKeyIssuer{opts: opts}
}

// Request opens a key ticket for an account.
func (i *ServiceAccountKeyIssuer) Request(accountID, requesterID string, spec KeySpec) (Request, error) {
	return Request{}, errNotImplemented
}

// Approve records one reviewer vote and closes the quorum when reached.
func (i *ServiceAccountKeyIssuer) Approve(requestID, approverID string) (Request, error) {
	return Request{}, errNotImplemented
}

// Reject terminates a ticket without issuing anything.
func (i *ServiceAccountKeyIssuer) Reject(requestID, approverID, reason string) (Request, error) {
	return Request{}, errNotImplemented
}

// Issue turns an approved ticket into a key and returns the plaintext once.
func (i *ServiceAccountKeyIssuer) Issue(requestID string) (IssuedKey, error) {
	return IssuedKey{}, errNotImplemented
}

// Activate enables a key once its activation delay has elapsed.
func (i *ServiceAccountKeyIssuer) Activate(keyID string) error {
	return errNotImplemented
}

// Disable turns a key off with an operator reason.
func (i *ServiceAccountKeyIssuer) Disable(keyID, reason string) error {
	return errNotImplemented
}

// List returns the key metadata of one account, oldest first.
func (i *ServiceAccountKeyIssuer) List(accountID string) []KeyMeta {
	return nil
}

// Get reads one request record.
func (i *ServiceAccountKeyIssuer) Get(requestID string) (Request, bool) {
	return Request{}, false
}

// LookupKey reads one key metadata record.
func (i *ServiceAccountKeyIssuer) LookupKey(keyID string) (KeyMeta, bool) {
	return KeyMeta{}, false
}

// Snapshot serialises requests, key metadata, schedules and the counters.
func (i *ServiceAccountKeyIssuer) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (i *ServiceAccountKeyIssuer) Restore(data []byte) error {
	return errNotImplemented
}
