// Package task governs emergency break glass grants: activation, usage,
// post incident review and the cooldown after a rejected review.
package task

import (
	"errors"
	"time"
)

// errNotImplemented marks behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Grant states.
const (
	GrantActive        = "active"
	GrantInUse         = "in_use"
	GrantPendingReview = "pending_review"
	GrantUpheld        = "upheld"
	GrantRejected      = "rejected"
)

// Review verdicts.
const (
	VerdictUpheld   = "upheld"
	VerdictRejected = "rejected"
)

// Event kinds.
const (
	EventNotify = "notify"
	EventUse    = "use"
	EventExpire = "expire"
	EventReview = "review"
)

// Errors returned by the ledger.
var (
	ErrBlankAdmin         = errors.New("admin id is required")
	ErrBlankIncident      = errors.New("incident id is required")
	ErrBlankReviewer      = errors.New("reviewer id is required")
	ErrBadReason          = errors.New("a non blank reason is required")
	ErrBadAction          = errors.New("action is required")
	ErrCooldown           = errors.New("admin is in the post rejection cooldown")
	ErrAlreadyGranted     = errors.New("an open grant already exists for this admin and incident")
	ErrIncidentLimit      = errors.New("too many administrators broke glass on this incident")
	ErrQuotaExceeded      = errors.New("event quota for this admin is exhausted")
	ErrGrantNotFound      = errors.New("unknown break glass grant")
	ErrGrantRejected      = errors.New("grant was rejected in review")
	ErrGrantClosed        = errors.New("grant is no longer usable")
	ErrGrantExpired       = errors.New("grant has expired and is awaiting review")
	ErrNotGrantActive     = errors.New("grant is not active")
	ErrNotPendingReview   = errors.New("grant is not awaiting review")
	ErrSelfReview         = errors.New("the granting admin may not review the grant")
	ErrReviewWindowClosed = errors.New("the review window has closed")
	ErrBadSnapshot        = errors.New("snapshot payload is invalid")
)

// Limits bounds duration, review, cooldown and the audit log. Now supplies the
// clock and may be replaced by a deterministic one in tests.
type Limits struct {
	MaxGrantsPerAdmin    int
	MaxAdminsPerIncident int
	MaxDuration          time.Duration
	ReviewWindow         time.Duration
	Cooldown             time.Duration
	MaxEvents            int
	Now                  func() time.Time
}

// Grant is one break glass authority.
type Grant struct {
	ID         string        `json:"id"`
	AdminID    string        `json:"admin_id"`
	IncidentID string        `json:"incident_id"`
	Reason     string        `json:"reason"`
	State      string        `json:"state"`
	IssuedAt   time.Time     `json:"issued_at"`
	ExpiresAt  time.Time     `json:"expires_at"`
	Remaining  time.Duration `json:"remaining"`
	ReviewerID string        `json:"reviewer_id"`
}

// Verdict is the outcome of one post incident review.
type Verdict struct {
	GrantID    string    `json:"grant_id"`
	Verdict    string    `json:"verdict"`
	ReviewerID string    `json:"reviewer_id"`
	At         time.Time `json:"at"`
}

// Event is one entry of the per incident ledger log.
type Event struct {
	GrantID    string    `json:"grant_id"`
	AdminID    string    `json:"admin_id"`
	IncidentID string    `json:"incident_id"`
	Kind       string    `json:"kind"`
	Action     string    `json:"action"`
	State      string    `json:"state"`
	At         time.Time `json:"at"`
	Seq        int64     `json:"seq"`
}

// AdminBreakGlassLedger owns grants, reviews, cooldowns and the audit log.
type AdminBreakGlassLedger struct {
	limits Limits
}

// NewAdminBreakGlassLedger builds a ledger with the given caps.
func NewAdminBreakGlassLedger(limits Limits) *AdminBreakGlassLedger {
	return &AdminBreakGlassLedger{limits: limits}
}

// Trigger opens an emergency grant for one admin and incident.
func (l *AdminBreakGlassLedger) Trigger(adminID, incidentID, reason string) (Grant, error) {
	return Grant{}, errNotImplemented
}

// Use records one operation performed under an active grant.
func (l *AdminBreakGlassLedger) Use(grantID, action string) error {
	return errNotImplemented
}

// Review records the post incident verdict.
func (l *AdminBreakGlassLedger) Review(grantID, reviewerID string, uphold bool) (Verdict, error) {
	return Verdict{}, errNotImplemented
}

// Expire ends the grant and moves it to the review queue.
func (l *AdminBreakGlassLedger) Expire(grantID string) error {
	return errNotImplemented
}

// Quota reports how many distinct incidents the admin may still open.
func (l *AdminBreakGlassLedger) Quota(adminID string) (int, error) {
	return 0, errNotImplemented
}

// GrantState reads one grant with its remaining lifetime.
func (l *AdminBreakGlassLedger) GrantState(grantID string) (Grant, error) {
	return Grant{}, errNotImplemented
}

// Active lists the open grants of one admin, ordered by grant id.
func (l *AdminBreakGlassLedger) Active(adminID string) []Grant {
	return nil
}

// Events returns the retained events of one incident, oldest first.
func (l *AdminBreakGlassLedger) Events(incidentID string) []Event {
	return nil
}

// CooldownUntil reports when the admin may trigger again, or the zero time.
func (l *AdminBreakGlassLedger) CooldownUntil(adminID string) (time.Time, error) {
	return time.Time{}, errNotImplemented
}

// Snapshot serialises grants, verdicts, cooldowns and events.
func (l *AdminBreakGlassLedger) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (l *AdminBreakGlassLedger) Restore(data []byte) error {
	return errNotImplemented
}
