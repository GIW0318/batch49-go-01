// Package task runs the account recovery case file: it collects evidence,
// keeps the mandatory waiting window, records objections and hands out one
// restricted recovery session per approved case.
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

// caseRecord is the mutable aggregate kept inside the case file.
type caseRecord struct {
	Case
	Contested     bool      `json:"contested"`
	ContestActor  string    `json:"contest_actor,omitempty"`
	ContestReason string    `json:"contest_reason,omitempty"`
	ContestedAt   time.Time `json:"contested_at"`
	ReviewerID    string    `json:"reviewer_id,omitempty"`
	GrantID       string    `json:"grant_id,omitempty"`
}

func (r *caseRecord) view() Case {
	out := r.Case
	out.Evidence = cloneEvidence(r.Evidence)
	return out
}

func (r *caseRecord) active() bool {
	switch r.State {
	case CaseEvidencePending, CaseWaiting, CaseReviewPending:
		return true
	default:
		return false
	}
}

func (r *caseRecord) closed() bool {
	switch r.State {
	case CaseApproved, CaseRejected, CaseCancelled:
		return true
	default:
		return false
	}
}

type snapshotData struct {
	Version   int                    `json:"version"`
	Seq       int64                  `json:"seq"`
	Cases     map[string]*caseRecord `json:"cases"`
	UserCases map[string][]string    `json:"user_cases"`
	Attempts  map[string]int         `json:"attempts"`
	Grants    map[string]Grant       `json:"grants"`
}

// AccountRecoveryCasefile owns the cases, the attempts and the grants.
type AccountRecoveryCasefile struct {
	mu        sync.Mutex
	limits    Limits
	now       time.Time
	seq       int64
	cases     map[string]*caseRecord
	grants    map[string]Grant
	userCases map[string][]string
	attempts  map[string]int
}

// NewAccountRecoveryCasefile builds a case file with the given caps.
func NewAccountRecoveryCasefile(limits Limits) *AccountRecoveryCasefile {
	return &AccountRecoveryCasefile{
		limits:    limits,
		cases:     make(map[string]*caseRecord),
		grants:    make(map[string]Grant),
		userCases: make(map[string][]string),
		attempts:  make(map[string]int),
	}
}

// SetNow pins the clock used by the case file.
func (f *AccountRecoveryCasefile) SetNow(now time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = now
}

func (f *AccountRecoveryCasefile) currentTimeLocked() time.Time {
	if f.now.IsZero() {
		return time.Now()
	}
	return f.now
}

func knownChannel(channel Channel) bool {
	switch channel {
	case ChannelEmail, ChannelSMS, ChannelSupport:
		return true
	default:
		return false
	}
}

func evidenceExpired(ev Evidence, now time.Time) bool {
	return !ev.ExpiresAt.IsZero() && ev.ExpiresAt.Before(now)
}

func cloneEvidence(items []Evidence) []Evidence {
	if items == nil {
		return nil
	}
	out := make([]Evidence, len(items))
	copy(out, items)
	return out
}

func allVerified(items []Evidence) bool {
	for _, ev := range items {
		if !ev.Verified {
			return false
		}
	}
	return true
}

func anyVerified(items []Evidence) bool {
	for _, ev := range items {
		if ev.Verified {
			return true
		}
	}
	return false
}

func validateEvidence(items []Evidence, now time.Time) error {
	for _, ev := range items {
		if strings.TrimSpace(ev.ID) == "" {
			return ErrBadEvidence
		}
	}
	for _, ev := range items {
		if evidenceExpired(ev, now) {
			return ErrEvidenceExpired
		}
	}
	return nil
}

func (f *AccountRecoveryCasefile) activeCaseIDLocked(userID string) string {
	ids := f.userCases[userID]
	for i := len(ids) - 1; i >= 0; i-- {
		if rec, ok := f.cases[ids[i]]; ok && rec.active() {
			return rec.ID
		}
	}
	return ""
}

// Open starts a recovery case for one account. A repeated application while a
// case is in flight merges into that case instead of opening a parallel one.
func (f *AccountRecoveryCasefile) Open(userID string, channel Channel, evidence []Evidence) (Case, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	now := f.currentTimeLocked()

	if strings.TrimSpace(userID) == "" {
		return Case{}, ErrBlankUser
	}
	if !knownChannel(channel) {
		return Case{}, ErrBadChannel
	}
	if len(evidence) == 0 {
		return Case{}, ErrNoEvidence
	}
	if err := validateEvidence(evidence, now); err != nil {
		return Case{}, err
	}
	if len(evidence) > f.limits.MaxEvidencePerCase {
		return Case{}, ErrEvidenceLimit
	}

	if activeID := f.activeCaseIDLocked(userID); activeID != "" {
		rec := f.cases[activeID]
		if len(rec.Evidence)+len(evidence) > f.limits.MaxEvidencePerCase {
			return Case{}, ErrEvidenceLimit
		}
		rec.Evidence = append(rec.Evidence, cloneEvidence(evidence)...)
		if rec.State == CaseEvidencePending && !rec.Contested && anyVerified(evidence) {
			rec.State = CaseWaiting
			rec.WaitUntil = now.Add(f.limits.WaitPeriod)
		}
		return rec.view(), nil
	}

	if f.attempts[userID] >= f.limits.MaxAttemptsPerUser {
		return Case{}, ErrAttemptsExceeded
	}

	f.seq++
	state := CaseEvidencePending
	var waitUntil time.Time
	if allVerified(evidence) {
		state = CaseWaiting
		waitUntil = now.Add(f.limits.WaitPeriod)
	}

	rec := &caseRecord{
		Case: Case{
			ID:        fmt.Sprintf("case-%d", f.seq),
			UserID:    userID,
			Channel:   channel,
			State:     state,
			Evidence:  cloneEvidence(evidence),
			OpenedAt:  now,
			WaitUntil: waitUntil,
			Seq:       f.seq,
		},
	}
	f.cases[rec.ID] = rec
	f.userCases[userID] = append(f.userCases[userID], rec.ID)
	f.attempts[userID]++

	return rec.view(), nil
}

// Submit adds one evidence item to a pending case.
func (f *AccountRecoveryCasefile) Submit(caseID string, e Evidence) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	rec, ok := f.cases[caseID]
	if !ok {
		return ErrCaseNotFound
	}
	if rec.State != CaseEvidencePending || rec.Contested {
		return ErrCaseState
	}
	if strings.TrimSpace(e.ID) == "" {
		return ErrBadEvidence
	}
	now := f.currentTimeLocked()
	if evidenceExpired(e, now) {
		return ErrEvidenceExpired
	}
	if len(rec.Evidence)+1 > f.limits.MaxEvidencePerCase {
		return ErrEvidenceLimit
	}

	rec.Evidence = append(rec.Evidence, e)
	if e.Verified {
		rec.State = CaseWaiting
		rec.WaitUntil = now.Add(f.limits.WaitPeriod)
	}
	return nil
}

// Contest records an objection raised by the original owner. A contested case
// is frozen for manual handling and the scheduled sweep never releases it.
func (f *AccountRecoveryCasefile) Contest(caseID, actorID, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if strings.TrimSpace(actorID) == "" {
		return ErrBadActor
	}
	rec, ok := f.cases[caseID]
	if !ok {
		return ErrCaseNotFound
	}
	if !rec.active() {
		return ErrCaseState
	}

	rec.Contested = true
	rec.ContestActor = actorID
	rec.ContestReason = reason
	rec.ContestedAt = f.currentTimeLocked()
	return nil
}

// Approve issues the recovery grant for a case that finished its waiting window.
func (f *AccountRecoveryCasefile) Approve(caseID, reviewerID string) (Grant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if strings.TrimSpace(reviewerID) == "" {
		return Grant{}, ErrBadActor
	}
	rec, ok := f.cases[caseID]
	if !ok {
		return Grant{}, ErrCaseNotFound
	}
	if rec.Contested || rec.GrantID != "" || rec.State != CaseReviewPending {
		return Grant{}, ErrCaseState
	}

	now := f.currentTimeLocked()
	grant := Grant{
		ID:           fmt.Sprintf("grant-%d", rec.Seq),
		CaseID:       rec.ID,
		UserID:       rec.UserID,
		IssuedAt:     now,
		CoolingUntil: now.Add(f.limits.CoolingPeriod),
	}

	rec.State = CaseApproved
	rec.ReviewerID = reviewerID
	rec.GrantID = grant.ID
	f.grants[grant.ID] = grant

	return grant, nil
}

// Cancel closes a case without a grant.
func (f *AccountRecoveryCasefile) Cancel(caseID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	rec, ok := f.cases[caseID]
	if !ok {
		return ErrCaseNotFound
	}
	if rec.closed() {
		return ErrCaseState
	}
	rec.State = CaseCancelled
	return nil
}

// Get reads one case. The returned Case is a defensive copy.
func (f *AccountRecoveryCasefile) Get(caseID string) (Case, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	rec, ok := f.cases[caseID]
	if !ok {
		return Case{}, ErrCaseNotFound
	}
	return rec.view(), nil
}

// Advance moves the waiting cases whose window has elapsed to review and
// returns their ids in ascending order.
func (f *AccountRecoveryCasefile) Advance(now time.Time) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if now.IsZero() {
		now = f.currentTimeLocked()
	}

	due := make([]string, 0)
	for id, rec := range f.cases {
		if rec.State != CaseWaiting || rec.Contested {
			continue
		}
		if now.Before(rec.WaitUntil) {
			continue
		}
		rec.State = CaseReviewPending
		due = append(due, id)
	}
	sort.Strings(due)
	return due, nil
}

// Permitted tells whether one operation may run through a recovery grant.
func (f *AccountRecoveryCasefile) Permitted(grantID string, op Operation) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	grant, ok := f.grants[grantID]
	if !ok {
		return false, ErrUnknownGrant
	}

	now := f.currentTimeLocked()
	if now.Before(grant.CoolingUntil) {
		switch op {
		case OpChangePassword, OpReEnrollFactor:
			return true, nil
		default:
			return false, nil
		}
	}

	switch op {
	case OpChangePassword, OpReEnrollFactor, OpDisableTwoFactor, OpChangePrimaryEmail:
		return true, nil
	default:
		return false, nil
	}
}

// Grant reads one recovery grant.
func (f *AccountRecoveryCasefile) Grant(grantID string) (Grant, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	grant, ok := f.grants[grantID]
	return grant, ok
}

// Snapshot serialises the cases, the attempts and the grants.
func (f *AccountRecoveryCasefile) Snapshot() ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	dto := snapshotData{
		Version:   1,
		Seq:       f.seq,
		Cases:     f.cases,
		UserCases: f.userCases,
		Attempts:  f.attempts,
		Grants:    f.grants,
	}
	return json.Marshal(&dto)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (f *AccountRecoveryCasefile) Restore(data []byte) error {
	var dto snapshotData
	if err := json.Unmarshal(data, &dto); err != nil {
		return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	if dto.Cases == nil || dto.Grants == nil {
		return fmt.Errorf("%w: missing cases or grants", ErrBadSnapshot)
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.seq = dto.Seq
	f.cases = dto.Cases
	f.grants = dto.Grants
	if dto.UserCases != nil {
		f.userCases = dto.UserCases
	} else {
		f.userCases = make(map[string][]string)
		for id, rec := range f.cases {
			if rec == nil {
				continue
			}
			f.userCases[rec.UserID] = append(f.userCases[rec.UserID], id)
		}
	}
	if dto.Attempts != nil {
		f.attempts = dto.Attempts
	} else {
		f.attempts = make(map[string]int)
		for userID, ids := range f.userCases {
			f.attempts[userID] = len(ids)
		}
	}
	if f.userCases == nil {
		f.userCases = make(map[string][]string)
	}
	if f.attempts == nil {
		f.attempts = make(map[string]int)
	}
	return nil
}
