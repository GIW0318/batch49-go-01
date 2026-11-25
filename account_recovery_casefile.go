// Package task runs the account recovery case file: it collects evidence,
// keeps the mandatory waiting window, records objections and hands out one
// restricted recovery session per approved case.
package task

import (
	"errors"
	"time"
)

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Case states.
const (
	CaseEvidencePending = "evidence_pending"
	CaseWaiting         = "waiting"
	CaseReviewPending   = "review_pending"
	CaseApproved        = "approved"
	CaseRejected        = "rejected"
	CaseCancelled       = "cancelled"
)

// Channel is the intake channel of a recovery case.
type Channel string

// Supported intake channels.
const (
	ChannelEmail   Channel = "email"
	ChannelSMS     Channel = "sms"
	ChannelSupport Channel = "support"
)

// Operation is one action attempted through a recovery grant.
type Operation string

// Operations guarded by the cooling period.
const (
	OpChangePassword     Operation = "change_password"
	OpReEnrollFactor     Operation = "reenroll_factor"
	OpDisableTwoFactor   Operation = "disable_two_factor"
	OpChangePrimaryEmail Operation = "change_primary_email"
)

// Errors returned by the case file.
var (
	ErrBlankUser        = errors.New("user id is required")
	ErrBadChannel       = errors.New("unknown intake channel")
	ErrNoEvidence       = errors.New("at least one evidence item is required")
	ErrBadEvidence      = errors.New("evidence id is required")
	ErrEvidenceExpired  = errors.New("evidence has expired")
	ErrEvidenceLimit    = errors.New("evidence limit reached for the case")
	ErrCaseNotFound     = errors.New("case not found")
	ErrCaseState        = errors.New("case is not in the required state")
	ErrBadActor         = errors.New("actor id is required")
	ErrAttemptsExceeded = errors.New("recovery attempt limit reached for the user")
	ErrUnknownGrant     = errors.New("recovery grant not found")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

// Limits bounds the case file.
type Limits struct {
	MaxEvidencePerCase int
	MaxAttemptsPerUser int
	WaitPeriod         time.Duration
	CoolingPeriod      time.Duration
}

// Evidence is one item submitted to prove ownership of the account.
type Evidence struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Verified  bool      `json:"verified"`
}

// Case is one account recovery case.
type Case struct {
	ID        string     `json:"id"`
	UserID    string     `json:"user_id"`
	Channel   Channel    `json:"channel"`
	State     string     `json:"state"`
	Evidence  []Evidence `json:"evidence"`
	OpenedAt  time.Time  `json:"opened_at"`
	WaitUntil time.Time  `json:"wait_until"`
	Seq       int64      `json:"seq"`
}

// Grant is the restricted recovery session issued after approval.
type Grant struct {
	ID           string    `json:"id"`
	CaseID       string    `json:"case_id"`
	UserID       string    `json:"user_id"`
	IssuedAt     time.Time `json:"issued_at"`
	CoolingUntil time.Time `json:"cooling_until"`
}

// AccountRecoveryCasefile owns the cases, the attempts and the grants.
type AccountRecoveryCasefile struct {
	limits Limits
	now    time.Time
}

// NewAccountRecoveryCasefile builds a case file with the given caps.
func NewAccountRecoveryCasefile(limits Limits) *AccountRecoveryCasefile {
	return &AccountRecoveryCasefile{limits: limits}
}

// SetNow pins the clock used by the case file.
func (f *AccountRecoveryCasefile) SetNow(now time.Time) {
	f.now = now
}

// Open starts a recovery case for one account.
func (f *AccountRecoveryCasefile) Open(userID string, channel Channel, evidence []Evidence) (Case, error) {
	return Case{}, errNotImplemented
}

// Submit adds one evidence item to a pending case.
func (f *AccountRecoveryCasefile) Submit(caseID string, e Evidence) error {
	return errNotImplemented
}

// Contest records an objection raised by the original owner.
func (f *AccountRecoveryCasefile) Contest(caseID, actorID, reason string) error {
	return errNotImplemented
}

// Approve issues the recovery grant for a case that finished its waiting window.
func (f *AccountRecoveryCasefile) Approve(caseID, reviewerID string) (Grant, error) {
	return Grant{}, errNotImplemented
}

// Cancel closes a case without a grant.
func (f *AccountRecoveryCasefile) Cancel(caseID string) error {
	return errNotImplemented
}

// Get reads one case.
func (f *AccountRecoveryCasefile) Get(caseID string) (Case, error) {
	return Case{}, errNotImplemented
}

// Advance moves the waiting cases whose window has elapsed to review and
// returns their ids in ascending order.
func (f *AccountRecoveryCasefile) Advance(now time.Time) ([]string, error) {
	return nil, errNotImplemented
}

// Permitted tells whether one operation may run through a recovery grant.
func (f *AccountRecoveryCasefile) Permitted(grantID string, op Operation) (bool, error) {
	return false, errNotImplemented
}

// Grant reads one recovery grant.
func (f *AccountRecoveryCasefile) Grant(grantID string) (Grant, bool) {
	return Grant{}, false
}

// Snapshot serialises the cases, the attempts and the grants.
func (f *AccountRecoveryCasefile) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (f *AccountRecoveryCasefile) Restore(data []byte) error {
	return errNotImplemented
}
