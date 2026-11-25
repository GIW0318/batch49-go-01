// Package task meters service account key calls against a key level quota and
// an account level quota, and fences keys that reach an unauthorized target.
package task

import (
	"errors"
	"time"
)

// errNotImplemented marks behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Key lifecycle states.
const (
	KeyActive    = "active"
	KeySuspended = "suspended"
	KeyRevoked   = "revoked"
)

// Decision verdicts.
const (
	DecisionAllow = "allow"
	DecisionDeny  = "deny"
)

// Reason codes carried by audit events.
const (
	ReasonUnauthorizedTarget = "unauthorized_target"
	ReasonKeyQuota           = "key_quota"
	ReasonAccountQuota       = "account_quota"
	ReasonOperatorHold       = "operator_hold"
	ReasonOperatorResume     = "operator_resume"
	ReasonKeySuspended       = "key_suspended"
	ReasonKeyRevoked         = "key_revoked"
	ReasonCommitted          = "committed"
)

// Errors returned by the meter.
var (
	ErrEmptyKeyID           = errors.New("key id is required")
	ErrEmptyAccountID       = errors.New("account id is required")
	ErrBadTargets           = errors.New("at least one non blank target is required")
	ErrTargetLimit          = errors.New("too many targets for one key")
	ErrKeyExists            = errors.New("key is already registered")
	ErrUnknownKey           = errors.New("unknown service account key")
	ErrTargetDenied         = errors.New("target is not allowed for this key")
	ErrKeySuspended         = errors.New("key is suspended")
	ErrKeyRevoked           = errors.New("key is revoked")
	ErrKeyNotSuspended      = errors.New("key is not suspended")
	ErrBadReason            = errors.New("a non blank reason is required")
	ErrApproverRequired     = errors.New("an approver is required to resume a key")
	ErrKeyQuotaExceeded     = errors.New("key quota exceeded in the current window")
	ErrAccountQuotaExceeded = errors.New("account quota exceeded in the current window")
	ErrUnknownAccount       = errors.New("unknown billing account")
	ErrBadWindow            = errors.New("window must be positive")
	ErrDecisionNotFound     = errors.New("unknown decision")
	ErrDecisionUsed         = errors.New("decision is already committed")
	ErrDecisionExpired      = errors.New("decision has expired")
	ErrBadSnapshot          = errors.New("snapshot payload is invalid")
)

// Limits caps the meter. KeyQuota and AccountQuota are calls allowed inside
// one Window. Window also bounds the lifetime of a decision.
type Limits struct {
	KeyQuota         int
	AccountQuota     int
	Window           time.Duration
	MaxTargetsPerKey int
	MaxEvents        int
}

// KeyStat is the public view of one registered key.
type KeyStat struct {
	KeyID     string   `json:"key_id"`
	AccountID string   `json:"account_id"`
	State     string   `json:"state"`
	Reason    string   `json:"reason"`
	Targets   []string `json:"targets"`
	Allowed   int64    `json:"allowed"`
	Denied    int64    `json:"denied"`
	Seq       int64    `json:"seq"`
}

// UsageStat summarises the commits of one key inside a sliding window.
type UsageStat struct {
	KeyID   string        `json:"key_id"`
	Window  time.Duration `json:"window"`
	From    time.Time     `json:"from"`
	To      time.Time     `json:"to"`
	Count   int64         `json:"count"`
	Allowed int64         `json:"allowed"`
	Denied  int64         `json:"denied"`
}

// QuotaState is the account level roll up of allowed calls.
type QuotaState struct {
	AccountID string        `json:"account_id"`
	Used      int64         `json:"used"`
	Limit     int           `json:"limit"`
	Window    time.Duration `json:"window"`
	Exhausted bool          `json:"exhausted"`
}

// Decision is a pre flight allow verdict that may be committed once.
type Decision struct {
	ID        string    `json:"id"`
	KeyID     string    `json:"key_id"`
	Target    string    `json:"target"`
	Verdict   string    `json:"verdict"`
	ExpiresAt time.Time `json:"expires_at"`
}

// AuditEvent is one entry of the per account audit log.
type AuditEvent struct {
	KeyID     string    `json:"key_id"`
	AccountID string    `json:"account_id"`
	Target    string    `json:"target"`
	Decision  string    `json:"decision"`
	Reason    string    `json:"reason"`
	At        time.Time `json:"at"`
	Seq       int64     `json:"seq"`
}

// ServiceAccountKeyMeter owns key records, window counters and the audit log.
type ServiceAccountKeyMeter struct {
	limits Limits
}

// NewServiceAccountKeyMeter builds a meter with the given caps.
func NewServiceAccountKeyMeter(limits Limits) *ServiceAccountKeyMeter {
	return &ServiceAccountKeyMeter{limits: limits}
}

// RegisterKey records a key, its billing account and its allowed targets.
func (m *ServiceAccountKeyMeter) RegisterKey(keyID, accountID string, targets []string) error {
	return errNotImplemented
}

// Record authorises one call at the given instant and accounts for it.
func (m *ServiceAccountKeyMeter) Record(keyID, target string, at time.Time) error {
	return errNotImplemented
}

// Usage reports the commits of one key inside the window ending at the clock
// watermark.
func (m *ServiceAccountKeyMeter) Usage(keyID string, window time.Duration) (UsageStat, error) {
	return UsageStat{}, errNotImplemented
}

// Decide reserves a verdict that a later Commit may redeem.
func (m *ServiceAccountKeyMeter) Decide(keyID, target string, at time.Time) (Decision, error) {
	return Decision{}, errNotImplemented
}

// Commit redeems a decision and accounts for the call at the given instant.
func (m *ServiceAccountKeyMeter) Commit(decisionID string, at time.Time) error {
	return errNotImplemented
}

// Suspend freezes a key and records the operator reason.
func (m *ServiceAccountKeyMeter) Suspend(keyID, reason string) error {
	return errNotImplemented
}

// Resume lifts a suspension once an approver is named.
func (m *ServiceAccountKeyMeter) Resume(keyID, approverID string) error {
	return errNotImplemented
}

// Revoke retires a key for good.
func (m *ServiceAccountKeyMeter) Revoke(keyID, reason string) error {
	return errNotImplemented
}

// KeyState reads one key record.
func (m *ServiceAccountKeyMeter) KeyState(keyID string) (KeyStat, error) {
	return KeyStat{}, errNotImplemented
}

// Quota reads the account level usage roll up.
func (m *ServiceAccountKeyMeter) Quota(accountID string) (QuotaState, error) {
	return QuotaState{}, errNotImplemented
}

// Events returns the retained audit events of one account, oldest first.
func (m *ServiceAccountKeyMeter) Events(accountID string) []AuditEvent {
	return nil
}

// Snapshot serialises keys, counters, decisions and the audit log.
func (m *ServiceAccountKeyMeter) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (m *ServiceAccountKeyMeter) Restore(data []byte) error {
	return errNotImplemented
}
