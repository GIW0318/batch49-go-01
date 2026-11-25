// Package task keeps a resource level sharing consent ledger: one user grants
// one third party read access to one data object for one purpose, and every
// read attempt is checked again against the stored record.
package task

import (
	"errors"
	"time"
)

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Reason codes carried by a decision.
const (
	ReasonAllowed         = "allowed"
	ReasonNotGranted      = "not_granted"
	ReasonPurposeMismatch = "purpose_mismatch"
	ReasonRevoked         = "revoked"
	ReasonExpired         = "expired"
)

// Errors reported by the ledger.
var (
	ErrBlankArgument  = errors.New("user id, recipient id, object id and purpose are required")
	ErrBadTTL         = errors.New("ttl must be positive")
	ErrGrantLimit     = errors.New("grant limit reached for this user")
	ErrRecipientLimit = errors.New("recipient limit reached for this object")
	ErrUnknownGrant   = errors.New("consent grant not found")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// Grant is one consent record: user grants recipient read access to object for
// one purpose until ExpiresAt.
type Grant struct {
	UserID      string    `json:"user_id"`
	RecipientID string    `json:"recipient_id"`
	ObjectID    string    `json:"object_id"`
	Purpose     string    `json:"purpose"`
	GrantedAt   time.Time `json:"granted_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	Revoked     bool      `json:"revoked"`
	RevokedAt   time.Time `json:"revoked_at"`
	Seq         int64     `json:"seq"`
}

// Decision is the explainable answer to one read attempt.
type Decision struct {
	Allowed   bool      `json:"allowed"`
	Reason    string    `json:"reason"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Limits bounds the live tables of the ledger.
type Limits struct {
	MaxGrantsPerUser       int
	MaxRecipientsPerObject int
}

// DataSharingConsentLedger owns the consent records and the monotonic counter.
type DataSharingConsentLedger struct {
	limits Limits
}

// NewDataSharingConsentLedger builds a ledger with the given caps.
func NewDataSharingConsentLedger(limits Limits) *DataSharingConsentLedger {
	return &DataSharingConsentLedger{limits: limits}
}

// Grant records one consent for the whole ttl, counted from now.
func (l *DataSharingConsentLedger) Grant(userID, recipientID, objectID, purpose string, ttl time.Duration) (Grant, error) {
	return Grant{}, errNotImplemented
}

// GrantAt records one consent with a caller supplied issue time.
func (l *DataSharingConsentLedger) GrantAt(userID, recipientID, objectID, purpose string, ttl time.Duration, now time.Time) (Grant, error) {
	return Grant{}, errNotImplemented
}

// Revoke withdraws one consent pair and keeps the audit trail.
func (l *DataSharingConsentLedger) Revoke(userID, objectID, recipientID string) error {
	return errNotImplemented
}

// Check answers whether recipient may read object for purpose right now.
func (l *DataSharingConsentLedger) Check(recipientID, objectID, purpose string) (Decision, error) {
	return Decision{}, errNotImplemented
}

// CheckAt answers the same question at a caller supplied instant.
func (l *DataSharingConsentLedger) CheckAt(recipientID, objectID, purpose string, now time.Time) (Decision, error) {
	return Decision{}, errNotImplemented
}

// ListByObject returns the consents one user keeps on one object, oldest first.
func (l *DataSharingConsentLedger) ListByObject(userID, objectID string) []Grant {
	return nil
}

// ListByRecipient returns the consents granted to one recipient, oldest first.
func (l *DataSharingConsentLedger) ListByRecipient(recipientID string) []Grant {
	return nil
}

// SweepExpired drops expired records and reports how many went away.
func (l *DataSharingConsentLedger) SweepExpired(now time.Time) (int, error) {
	return 0, errNotImplemented
}

// Snapshot serialises the records, the revocation flags and the sequence.
func (l *DataSharingConsentLedger) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole ledger with a snapshot, all or nothing.
func (l *DataSharingConsentLedger) Restore(data []byte) error {
	return errNotImplemented
}
