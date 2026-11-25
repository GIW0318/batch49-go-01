// Package task governs emergency break glass grants: activation, usage,
// post incident review and the cooldown after a rejected review.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

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

// grantRecord is the stored form of a grant plus its issuance order.
type grantRecord struct {
	Grant
	Seq int64 `json:"seq"`
}

// snapshotState is the serialised form of the whole ledger.
type snapshotState struct {
	Grants    map[string]grantRecord `json:"grants"`
	NextGrant int64                  `json:"next_grant"`
	Cooldowns map[string]time.Time   `json:"cooldowns"`
	Events    map[string][]Event     `json:"events"`
	EventSeq  map[string]int64       `json:"event_seq"`
}

// AdminBreakGlassLedger owns grants, reviews, cooldowns and the audit log.
type AdminBreakGlassLedger struct {
	mu        sync.Mutex
	limits    Limits
	grants    map[string]grantRecord
	nextGrant int64
	cooldowns map[string]time.Time
	events    map[string][]Event
	eventSeq  map[string]int64
}

// NewAdminBreakGlassLedger builds a ledger with the given caps.
func NewAdminBreakGlassLedger(limits Limits) *AdminBreakGlassLedger {
	return &AdminBreakGlassLedger{
		limits:    limits,
		grants:    make(map[string]grantRecord),
		cooldowns: make(map[string]time.Time),
		events:    make(map[string][]Event),
		eventSeq:  make(map[string]int64),
	}
}

func (l *AdminBreakGlassLedger) now() time.Time {
	if l.limits.Now != nil {
		return l.limits.Now()
	}
	return time.Now()
}

func blank(s string) bool {
	return strings.TrimSpace(s) == ""
}

// openGrant reports whether the admin already holds an open grant on the
// incident, and returns its id if so.
func (l *AdminBreakGlassLedger) openGrant(adminID, incidentID string) bool {
	for _, rec := range l.grants {
		if rec.AdminID == adminID && rec.IncidentID == incidentID &&
			(rec.State == GrantActive || rec.State == GrantInUse) {
			return true
		}
	}
	return false
}

// adminIncidents returns the distinct incidents the admin has grants on.
func (l *AdminBreakGlassLedger) adminIncidents(adminID string) map[string]struct{} {
	set := make(map[string]struct{})
	for _, rec := range l.grants {
		if rec.AdminID == adminID {
			set[rec.IncidentID] = struct{}{}
		}
	}
	return set
}

// incidentAdmins returns the distinct admins that broke glass on the incident.
func (l *AdminBreakGlassLedger) incidentAdmins(incidentID string) map[string]struct{} {
	set := make(map[string]struct{})
	for _, rec := range l.grants {
		if rec.IncidentID == incidentID {
			set[rec.AdminID] = struct{}{}
		}
	}
	return set
}

// appendEvent records one audit entry on the incident log, keeping only the
// newest MaxEvents entries while the sequence keeps strictly increasing.
func (l *AdminBreakGlassLedger) appendEvent(rec grantRecord, kind, action string, at time.Time) {
	l.eventSeq[rec.IncidentID]++
	event := Event{
		GrantID:    rec.ID,
		AdminID:    rec.AdminID,
		IncidentID: rec.IncidentID,
		Kind:       kind,
		Action:     action,
		State:      rec.State,
		At:         at,
		Seq:        l.eventSeq[rec.IncidentID],
	}
	log := append(l.events[rec.IncidentID], event)
	if l.limits.MaxEvents > 0 && len(log) > l.limits.MaxEvents {
		log = append([]Event(nil), log[len(log)-l.limits.MaxEvents:]...)
	}
	l.events[rec.IncidentID] = log
}

// remaining computes the live remaining lifetime of a grant.
func (l *AdminBreakGlassLedger) remaining(rec grantRecord, now time.Time) time.Duration {
	if rec.State != GrantActive && rec.State != GrantInUse {
		return 0
	}
	left := rec.ExpiresAt.Sub(now)
	if left < 0 {
		return 0
	}
	return left
}

// Trigger opens an emergency grant for one admin and incident.
func (l *AdminBreakGlassLedger) Trigger(adminID, incidentID, reason string) (Grant, error) {
	if blank(adminID) {
		return Grant{}, ErrBlankAdmin
	}
	if blank(incidentID) {
		return Grant{}, ErrBlankIncident
	}
	if blank(reason) {
		return Grant{}, ErrBadReason
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	if until, ok := l.cooldowns[adminID]; ok && until.After(now) {
		return Grant{}, ErrCooldown
	}
	if l.openGrant(adminID, incidentID) {
		return Grant{}, ErrAlreadyGranted
	}
	if l.limits.MaxGrantsPerAdmin > 0 {
		incidents := l.adminIncidents(adminID)
		if _, seen := incidents[incidentID]; !seen && len(incidents) >= l.limits.MaxGrantsPerAdmin {
			return Grant{}, ErrQuotaExceeded
		}
	}
	if l.limits.MaxAdminsPerIncident > 0 {
		admins := l.incidentAdmins(incidentID)
		if _, seen := admins[adminID]; !seen && len(admins) >= l.limits.MaxAdminsPerIncident {
			return Grant{}, ErrIncidentLimit
		}
	}

	l.nextGrant++
	rec := grantRecord{
		Grant: Grant{
			ID:         fmt.Sprintf("grant-%d", l.nextGrant),
			AdminID:    adminID,
			IncidentID: incidentID,
			Reason:     reason,
			State:      GrantActive,
			IssuedAt:   now,
			ExpiresAt:  now.Add(l.limits.MaxDuration),
			Remaining:  l.limits.MaxDuration,
		},
		Seq: l.nextGrant,
	}
	l.grants[rec.ID] = rec
	l.appendEvent(rec, EventNotify, "", now)
	return rec.Grant, nil
}

// Use records one operation performed under an active grant.
func (l *AdminBreakGlassLedger) Use(grantID, action string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	rec, ok := l.grants[grantID]
	if !ok {
		return ErrGrantNotFound
	}
	if blank(action) {
		return ErrBadAction
	}
	switch rec.State {
	case GrantActive, GrantInUse:
	case GrantPendingReview:
		return ErrGrantExpired
	case GrantRejected:
		return ErrGrantRejected
	default:
		return ErrGrantClosed
	}
	rec.State = GrantInUse
	rec.Remaining = l.remaining(rec, l.now())
	l.grants[rec.ID] = rec
	l.appendEvent(rec, EventUse, action, l.now())
	return nil
}

// Review records the post incident verdict.
func (l *AdminBreakGlassLedger) Review(grantID, reviewerID string, uphold bool) (Verdict, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	rec, ok := l.grants[grantID]
	if !ok {
		return Verdict{}, ErrGrantNotFound
	}
	if rec.State != GrantPendingReview {
		return Verdict{}, ErrNotPendingReview
	}
	if blank(reviewerID) {
		return Verdict{}, ErrBlankReviewer
	}
	if reviewerID == rec.AdminID {
		return Verdict{}, ErrSelfReview
	}
	now := l.now()
	if l.limits.ReviewWindow > 0 && now.After(rec.ExpiresAt.Add(l.limits.ReviewWindow)) {
		return Verdict{}, ErrReviewWindowClosed
	}

	verdict := Verdict{
		GrantID:    rec.ID,
		ReviewerID: reviewerID,
		At:         now,
	}
	if uphold {
		rec.State = GrantUpheld
		verdict.Verdict = VerdictUpheld
	} else {
		rec.State = GrantRejected
		verdict.Verdict = VerdictRejected
		l.cooldowns[rec.AdminID] = now.Add(l.limits.Cooldown)
	}
	rec.ReviewerID = reviewerID
	rec.Remaining = 0
	l.grants[rec.ID] = rec
	l.appendEvent(rec, EventReview, "", now)
	return verdict, nil
}

// Expire ends the grant and moves it to the review queue.
func (l *AdminBreakGlassLedger) Expire(grantID string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	rec, ok := l.grants[grantID]
	if !ok {
		return ErrGrantNotFound
	}
	if rec.State != GrantActive && rec.State != GrantInUse {
		return ErrNotGrantActive
	}
	rec.State = GrantPendingReview
	rec.Remaining = 0
	l.grants[rec.ID] = rec
	l.appendEvent(rec, EventExpire, "", l.now())
	return nil
}

// Quota reports how many distinct incidents the admin may still open.
func (l *AdminBreakGlassLedger) Quota(adminID string) (int, error) {
	if blank(adminID) {
		return 0, ErrBlankAdmin
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	remaining := l.limits.MaxGrantsPerAdmin - len(l.adminIncidents(adminID))
	if remaining < 0 {
		remaining = 0
	}
	return remaining, nil
}

// GrantState reads one grant with its remaining lifetime.
func (l *AdminBreakGlassLedger) GrantState(grantID string) (Grant, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	rec, ok := l.grants[grantID]
	if !ok {
		return Grant{}, ErrGrantNotFound
	}
	grant := rec.Grant
	grant.Remaining = l.remaining(rec, l.now())
	return grant, nil
}

// Active lists the open grants of one admin, ordered by grant id.
func (l *AdminBreakGlassLedger) Active(adminID string) []Grant {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	var records []grantRecord
	for _, rec := range l.grants {
		if rec.AdminID == adminID && (rec.State == GrantActive || rec.State == GrantInUse) {
			records = append(records, rec)
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Seq < records[j].Seq })
	grants := make([]Grant, 0, len(records))
	for _, rec := range records {
		grant := rec.Grant
		grant.Remaining = l.remaining(rec, now)
		grants = append(grants, grant)
	}
	return grants
}

// Events returns the retained events of one incident, oldest first.
func (l *AdminBreakGlassLedger) Events(incidentID string) []Event {
	l.mu.Lock()
	defer l.mu.Unlock()

	log := l.events[incidentID]
	out := make([]Event, len(log))
	copy(out, log)
	return out
}

// CooldownUntil reports when the admin may trigger again, or the zero time.
func (l *AdminBreakGlassLedger) CooldownUntil(adminID string) (time.Time, error) {
	if blank(adminID) {
		return time.Time{}, ErrBlankAdmin
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	until, ok := l.cooldowns[adminID]
	if !ok || !until.After(l.now()) {
		return time.Time{}, nil
	}
	return until, nil
}

// Snapshot serialises grants, verdicts, cooldowns and events.
func (l *AdminBreakGlassLedger) Snapshot() ([]byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	state := snapshotState{
		Grants:    make(map[string]grantRecord, len(l.grants)),
		NextGrant: l.nextGrant,
		Cooldowns: make(map[string]time.Time, len(l.cooldowns)),
		Events:    make(map[string][]Event, len(l.events)),
		EventSeq:  make(map[string]int64, len(l.eventSeq)),
	}
	for id, rec := range l.grants {
		state.Grants[id] = rec
	}
	for admin, until := range l.cooldowns {
		state.Cooldowns[admin] = until
	}
	for incident, log := range l.events {
		state.Events[incident] = append([]Event(nil), log...)
	}
	for incident, seq := range l.eventSeq {
		state.EventSeq[incident] = seq
	}
	return json.Marshal(state)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (l *AdminBreakGlassLedger) Restore(data []byte) error {
	var state snapshotState
	if err := json.Unmarshal(data, &state); err != nil {
		return ErrBadSnapshot
	}
	if state.Grants == nil || state.Events == nil || state.EventSeq == nil ||
		state.Cooldowns == nil || state.NextGrant < 0 {
		return ErrBadSnapshot
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.grants = state.Grants
	l.nextGrant = state.NextGrant
	l.cooldowns = state.Cooldowns
	l.events = state.Events
	l.eventSeq = state.EventSeq
	return nil
}
