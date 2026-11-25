// Package task keeps a resource level sharing consent ledger: one user grants
// one third party read access to one data object for one purpose, and every
// read attempt is checked again against the stored record.
package task

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

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

type grantKey struct {
	userID      string
	recipientID string
	objectID    string
	purpose     string
}

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
	mu     sync.Mutex
	limits Limits
	seq    int64
	grants map[grantKey]Grant
}

type snapshotData struct {
	Seq    int64   `json:"seq"`
	Grants []Grant `json:"grants"`
}

// NewDataSharingConsentLedger builds a ledger with the given caps.
func NewDataSharingConsentLedger(limits Limits) *DataSharingConsentLedger {
	return &DataSharingConsentLedger{
		limits: limits,
		grants: make(map[grantKey]Grant),
	}
}

func isBlank(value string) bool {
	return strings.TrimSpace(value) == ""
}

func newGrantKey(userID, recipientID, objectID, purpose string) grantKey {
	return grantKey{
		userID:      userID,
		recipientID: recipientID,
		objectID:    objectID,
		purpose:     purpose,
	}
}

// Grant records one consent for the whole ttl, counted from now.
func (l *DataSharingConsentLedger) Grant(userID, recipientID, objectID, purpose string, ttl time.Duration) (Grant, error) {
	return l.GrantAt(userID, recipientID, objectID, purpose, ttl, time.Now())
}

// GrantAt records one consent with a caller supplied issue time.
func (l *DataSharingConsentLedger) GrantAt(userID, recipientID, objectID, purpose string, ttl time.Duration, now time.Time) (Grant, error) {
	if isBlank(userID) || isBlank(recipientID) || isBlank(objectID) || isBlank(purpose) {
		return Grant{}, ErrBlankArgument
	}
	if ttl <= 0 {
		return Grant{}, ErrBadTTL
	}

	key := newGrantKey(userID, recipientID, objectID, purpose)

	l.mu.Lock()
	defer l.mu.Unlock()

	if _, exists := l.grants[key]; !exists {
		userGrantCount := 0
		objectRecipients := make(map[string]struct{})
		for _, grant := range l.grants {
			if grant.UserID == userID {
				userGrantCount++
			}
			if grant.ObjectID == objectID {
				objectRecipients[grant.RecipientID] = struct{}{}
			}
		}

		if userGrantCount >= l.limits.MaxGrantsPerUser {
			return Grant{}, ErrGrantLimit
		}

		_, recipientAlreadyOnObject := objectRecipients[recipientID]
		if !recipientAlreadyOnObject && len(objectRecipients) >= l.limits.MaxRecipientsPerObject {
			return Grant{}, ErrRecipientLimit
		}
	}

	l.seq++
	grant := Grant{
		UserID:      userID,
		RecipientID: recipientID,
		ObjectID:    objectID,
		Purpose:     purpose,
		GrantedAt:   now,
		ExpiresAt:   now.Add(ttl),
		Seq:         l.seq,
	}
	l.grants[key] = grant

	return grant, nil
}

// Revoke withdraws one consent pair and keeps the audit trail.
func (l *DataSharingConsentLedger) Revoke(userID, objectID, recipientID string) error {
	if isBlank(userID) || isBlank(objectID) || isBlank(recipientID) {
		return ErrBlankArgument
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now().UTC()
	found := false
	for key, grant := range l.grants {
		if grant.UserID != userID || grant.ObjectID != objectID || grant.RecipientID != recipientID {
			continue
		}

		found = true
		if !grant.Revoked {
			grant.Revoked = true
			grant.RevokedAt = now
			l.grants[key] = grant
		}
	}

	if !found {
		return ErrUnknownGrant
	}
	return nil
}

// Check answers whether recipient may read object for purpose right now.
func (l *DataSharingConsentLedger) Check(recipientID, objectID, purpose string) (Decision, error) {
	return l.CheckAt(recipientID, objectID, purpose, time.Now())
}

// CheckAt answers the same question at a caller supplied instant.
func (l *DataSharingConsentLedger) CheckAt(recipientID, objectID, purpose string, now time.Time) (Decision, error) {
	if isBlank(recipientID) || isBlank(objectID) || isBlank(purpose) {
		return Decision{}, ErrBlankArgument
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	foundExactPurpose := false
	revoked := false
	expired := false
	purposeMismatch := false
	var allowedExpiry time.Time

	for _, grant := range l.grants {
		if grant.RecipientID != recipientID || grant.ObjectID != objectID {
			continue
		}

		if grant.Purpose != purpose {
			purposeMismatch = true
			continue
		}

		foundExactPurpose = true
		if grant.Revoked {
			revoked = true
			continue
		}
		if !now.Before(grant.ExpiresAt) {
			expired = true
			continue
		}

		if allowedExpiry.IsZero() || grant.ExpiresAt.After(allowedExpiry) {
			allowedExpiry = grant.ExpiresAt
		}
	}

	if !allowedExpiry.IsZero() {
		return Decision{Allowed: true, Reason: ReasonAllowed, ExpiresAt: allowedExpiry}, nil
	}
	if revoked {
		return Decision{Reason: ReasonRevoked}, nil
	}
	if expired {
		return Decision{Reason: ReasonExpired}, nil
	}
	if foundExactPurpose || purposeMismatch {
		return Decision{Reason: ReasonPurposeMismatch}, nil
	}
	return Decision{Reason: ReasonNotGranted}, nil
}

// ListByObject returns the consents one user keeps on one object, oldest first.
func (l *DataSharingConsentLedger) ListByObject(userID, objectID string) []Grant {
	l.mu.Lock()
	defer l.mu.Unlock()

	grants := make([]Grant, 0)
	for _, grant := range l.grants {
		if grant.UserID == userID && grant.ObjectID == objectID {
			grants = append(grants, grant)
		}
	}
	sortGrantsBySeq(grants)
	return grants
}

// ListByRecipient returns the consents granted to one recipient, oldest first.
func (l *DataSharingConsentLedger) ListByRecipient(recipientID string) []Grant {
	l.mu.Lock()
	defer l.mu.Unlock()

	grants := make([]Grant, 0)
	for _, grant := range l.grants {
		if grant.RecipientID == recipientID {
			grants = append(grants, grant)
		}
	}
	sortGrantsBySeq(grants)
	return grants
}

// SweepExpired drops expired records and reports how many went away.
func (l *DataSharingConsentLedger) SweepExpired(now time.Time) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	removed := 0
	for key, grant := range l.grants {
		if !grant.Revoked && !now.Before(grant.ExpiresAt) {
			delete(l.grants, key)
			removed++
		}
	}
	return removed, nil
}

// Snapshot serialises the records, the revocation flags and the sequence.
func (l *DataSharingConsentLedger) Snapshot() ([]byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	snapshot := snapshotData{
		Seq:    l.seq,
		Grants: make([]Grant, 0, len(l.grants)),
	}
	for _, grant := range l.grants {
		snapshot.Grants = append(snapshot.Grants, grant)
	}
	sortGrantsBySeq(snapshot.Grants)

	data, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Restore replaces the whole ledger with a snapshot, all or nothing.
func (l *DataSharingConsentLedger) Restore(data []byte) error {
	var snapshot snapshotData
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return ErrBadSnapshot
	}
	if err := validateSnapshot(snapshot); err != nil {
		return err
	}

	restored := make(map[grantKey]Grant, len(snapshot.Grants))
	for _, grant := range snapshot.Grants {
		restored[newGrantKey(grant.UserID, grant.RecipientID, grant.ObjectID, grant.Purpose)] = grant
	}

	l.mu.Lock()
	l.seq = snapshot.Seq
	l.grants = restored
	l.mu.Unlock()
	return nil
}

func sortGrantsBySeq(grants []Grant) {
	sort.Slice(grants, func(i, j int) bool {
		return grants[i].Seq < grants[j].Seq
	})
}

func validateSnapshot(snapshot snapshotData) error {
	if snapshot.Seq < 0 {
		return ErrBadSnapshot
	}

	seenKeys := make(map[grantKey]struct{}, len(snapshot.Grants))
	seenSeqs := make(map[int64]struct{}, len(snapshot.Grants))
	for _, grant := range snapshot.Grants {
		if isBlank(grant.UserID) || isBlank(grant.RecipientID) || isBlank(grant.ObjectID) || isBlank(grant.Purpose) {
			return ErrBadSnapshot
		}
		if grant.Seq <= 0 || grant.Seq > snapshot.Seq {
			return ErrBadSnapshot
		}
		if !grant.ExpiresAt.After(grant.GrantedAt) {
			return ErrBadSnapshot
		}
		if grant.Revoked && grant.RevokedAt.IsZero() {
			return ErrBadSnapshot
		}
		if !grant.Revoked && !grant.RevokedAt.IsZero() {
			return ErrBadSnapshot
		}

		key := newGrantKey(grant.UserID, grant.RecipientID, grant.ObjectID, grant.Purpose)
		if _, exists := seenKeys[key]; exists {
			return ErrBadSnapshot
		}
		seenKeys[key] = struct{}{}

		if _, exists := seenSeqs[grant.Seq]; exists {
			return ErrBadSnapshot
		}
		seenSeqs[grant.Seq] = struct{}{}
	}

	return nil
}
