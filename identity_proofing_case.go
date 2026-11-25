// Package task orchestrates identity proofing cases built from a document, a
// liveness frame and vendor comparison results.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Level is the assurance level requested by the account.
type Level int

// Supported proofing levels.
const (
	LevelBasic    Level = 1
	LevelStandard Level = 2
	LevelHigh     Level = 3
)

// Case states.
const (
	CaseCollected = "collected"
	CaseComparing = "comparing"
	CaseManual    = "manual"
	CaseApproved  = "approved"
	CaseRejected  = "rejected"
	CaseExpired   = "expired"
)

// Verdict outcomes.
const (
	OutcomeApproved = "approved"
	OutcomeRejected = "rejected"
	OutcomePending  = "pending"
	OutcomeManual   = "manual"
)

// Vendor result kinds.
const (
	VendorPass    = "pass"
	VendorFail    = "fail"
	VendorRetry   = "retry"
	VendorRefer   = "refer"
	VendorTimeout = "timeout"
)

// Auditable event types recorded for replay.
const (
	EventCaseStarted     = "case_started"
	EventDocument        = "document_submitted"
	EventLiveness        = "liveness_submitted"
	EventVendorResult    = "vendor_result"
	EventRetryScheduled  = "vendor_retry_scheduled"
	EventRetryExhausted  = "vendor_retry_exhausted"
	EventManualReferral  = "manual_referral"
	EventConflictHold    = "document_conflict_hold"
	EventReviewCompleted = "review_completed"
	EventCaseExpired     = "case_expired"
)

// Errors returned by the case orchestrator.
var (
	ErrBlankUser       = errors.New("user id is required")
	ErrBadLevel        = errors.New("unsupported proofing level")
	ErrUnknownCase     = errors.New("unknown case")
	ErrBadDocument     = errors.New("document digest is required")
	ErrDocumentLocked  = errors.New("case already holds a different document")
	ErrEmptyFrame      = errors.New("liveness frame is empty")
	ErrNotReady        = errors.New("case has not collected document and liveness")
	ErrNotPending      = errors.New("case is not waiting for review")
	ErrBlankReviewer   = errors.New("reviewer id is required")
	ErrRetryExhausted  = errors.New("vendor retry budget exhausted")
	ErrCaseLimit       = errors.New("case limit reached for this user")
	ErrNotApproved     = errors.New("only an approved case can expire")
	ErrUnknownUser     = errors.New("unknown user")
	ErrBadVendorResult = errors.New("unknown vendor result kind")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// Document is one submitted identity document.
type Document struct {
	Kind   string `json:"kind"`
	Number string `json:"number"`
	Digest string `json:"digest"`
	Issuer string `json:"issuer"`
}

// VendorResult is one normalised comparison result from a vendor.
type VendorResult struct {
	Vendor string  `json:"vendor"`
	Kind   string  `json:"kind"`
	Score  float64 `json:"score"`
}

// Verdict is the outcome of one vendor comparison.
type Verdict struct {
	CaseID    string  `json:"case_id"`
	Outcome   string  `json:"outcome"`
	Score     float64 `json:"score"`
	Threshold float64 `json:"threshold"`
	Pending   bool    `json:"pending"`
	Retries   int     `json:"retries"`
	Conflict  bool    `json:"conflict,omitempty"`
}

// Case is one proofing case.
type Case struct {
	ID           string   `json:"id"`
	UserID       string   `json:"user_id"`
	Level        Level    `json:"level"`
	State        string   `json:"state"`
	Digest       string   `json:"digest"`
	Liveness     bool     `json:"liveness"`
	Retries      int      `json:"retries"`
	Seq          int64    `json:"seq"`
	Conflict     bool     `json:"conflict,omitempty"`
	ConflictWith []string `json:"conflict_with,omitempty"`
}

// ProofState is the per user projection returned by Status.
type ProofState struct {
	UserID   string `json:"user_id"`
	CaseID   string `json:"case_id"`
	Level    Level  `json:"level"`
	State    string `json:"state"`
	Approved bool   `json:"approved"`
	Conflict bool   `json:"conflict,omitempty"`
}

// Limits bounds the orchestrator tables.
type Limits struct {
	MaxRetries      int
	MaxCasesPerUser int
}

// Event is one auditable fact in a case stream. The stream answers replay
// questions: who submitted which document/frame, what the vendor scored and
// who performed the manual review.
type Event struct {
	Seq       int64     `json:"seq"`
	At        time.Time `json:"at"`
	CaseID    string    `json:"case_id"`
	UserID    string    `json:"user_id"`
	Type      string    `json:"type"`
	Actor     string    `json:"actor,omitempty"`
	FromState string    `json:"from_state,omitempty"`
	ToState   string    `json:"to_state,omitempty"`
	Kind      string    `json:"kind,omitempty"`
	Digest    string    `json:"digest,omitempty"`
	Vendor    string    `json:"vendor,omitempty"`
	Score     float64   `json:"score,omitempty"`
	Threshold float64   `json:"threshold,omitempty"`
	Outcome   string    `json:"outcome,omitempty"`
	Approve   bool      `json:"approve,omitempty"`
	Retries   int       `json:"retries,omitempty"`
	Bytes     int       `json:"bytes,omitempty"`
	SHA256    string    `json:"sha256,omitempty"`
	Reason    string    `json:"reason,omitempty"`
}

// proofCase is the persisted, mutable case record.
type proofCase struct {
	Case
	Document    Document `json:"document"`
	Reviewer    string   `json:"reviewer,omitempty"`
	LastVendor  string   `json:"last_vendor,omitempty"`
	DigestBound bool     `json:"digest_bound,omitempty"`
	Events      []Event  `json:"events,omitempty"`
}

// IdentityProofingCase owns the case table and the per user counters.
//
// Every exported entry point takes c.mu for its whole read-modify-write
// section, so retry counters and state transitions for one case commit
// atomically even under concurrent callers.
type IdentityProofingCase struct {
	mu          sync.Mutex
	limits      Limits
	seq         int64
	eventSeq    int64
	cases       map[string]*proofCase
	userCaseIDs map[string][]string
	digestOwner map[string]string
}

// NewIdentityProofingCase builds an orchestrator with the given caps.
func NewIdentityProofingCase(limits Limits) *IdentityProofingCase {
	if limits.MaxRetries < 0 {
		limits.MaxRetries = 0
	}
	if limits.MaxCasesPerUser < 0 {
		limits.MaxCasesPerUser = 0
	}
	return &IdentityProofingCase{
		limits:      limits,
		cases:       make(map[string]*proofCase),
		userCaseIDs: make(map[string][]string),
		digestOwner: make(map[string]string),
	}
}

// Start opens a new proofing case for one user.
func (c *IdentityProofingCase) Start(userID string, level Level) (Case, error) {
	if strings.TrimSpace(userID) == "" {
		return Case{}, ErrBlankUser
	}
	if !validLevel(level) {
		return Case{}, ErrBadLevel
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	ids := c.userCaseIDs[userID]
	if len(ids) >= c.limits.MaxCasesPerUser {
		return Case{}, ErrCaseLimit
	}

	c.seq++
	rec := &proofCase{
		Case: Case{
			ID:     formatCaseID(c.seq),
			UserID: userID,
			Level:  level,
			State:  CaseCollected,
			Seq:    c.seq,
		},
	}
	c.cases[rec.ID] = rec
	c.userCaseIDs[userID] = append(ids, rec.ID)
	c.appendEventLocked(rec, Event{
		Type:      EventCaseStarted,
		ToState:   CaseCollected,
		Threshold: mustThreshold(level),
	})
	return rec.Case, nil
}

// SubmitDocument attaches the identity document to a case. The first digest
// locks the case; a different digest is rejected while an identical resubmit
// is an idempotent no-op.
func (c *IdentityProofingCase) SubmitDocument(caseID string, doc Document) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	rec, ok := c.cases[caseID]
	if !ok {
		return ErrUnknownCase
	}
	if strings.TrimSpace(doc.Digest) == "" {
		return ErrBadDocument
	}
	if rec.Digest != "" {
		if rec.Digest != doc.Digest {
			return ErrDocumentLocked
		}
		return nil
	}

	rec.Digest = doc.Digest
	rec.Document = doc
	c.detectDigestConflictLocked(rec)

	from := rec.State
	c.collectIfCompleteLocked(rec)
	ev := Event{
		Type:      EventDocument,
		Kind:      doc.Kind,
		Digest:    doc.Digest,
		FromState: from,
	}
	if rec.State != from {
		ev.ToState = rec.State
	}
	c.appendEventLocked(rec, ev)

	if rec.Conflict {
		c.appendEventLocked(rec, Event{
			Type:      EventConflictHold,
			Digest:    doc.Digest,
			FromState: rec.State,
			ToState:   rec.State,
			Reason:    "document digest already bound to another account",
		})
	}
	return nil
}

// SubmitLiveness attaches the liveness frame to a case.
func (c *IdentityProofingCase) SubmitLiveness(caseID string, frame []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	rec, ok := c.cases[caseID]
	if !ok {
		return ErrUnknownCase
	}
	if len(frame) == 0 {
		return ErrEmptyFrame
	}

	rec.Liveness = true
	sum := sha256.Sum256(frame)

	from := rec.State
	c.collectIfCompleteLocked(rec)
	ev := Event{
		Type:      EventLiveness,
		Bytes:     len(frame),
		SHA256:    hex.EncodeToString(sum[:]),
		FromState: from,
	}
	if rec.State != from {
		ev.ToState = rec.State
	}
	c.appendEventLocked(rec, ev)
	return nil
}

// ApplyVendorResult normalises one vendor comparison for a case. Retry and
// timeout results stay pending and consume one retry slot; refer results park
// the case for a human; pass/fail results are judged against the level
// threshold. A cross-account document conflict blocks an otherwise automatic
// approval and parks the case for review.
func (c *IdentityProofingCase) ApplyVendorResult(caseID string, r VendorResult) (Verdict, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	rec, ok := c.cases[caseID]
	if !ok {
		return Verdict{}, ErrUnknownCase
	}
	if rec.State != CaseComparing {
		return Verdict{CaseID: caseID}, ErrNotReady
	}

	threshold, _ := c.ThresholdFor(rec.Level)

	switch r.Kind {
	case VendorRetry, VendorTimeout:
		if rec.Retries >= c.limits.MaxRetries {
			c.appendEventLocked(rec, Event{
				Type:      EventRetryExhausted,
				Vendor:    r.Vendor,
				Kind:      r.Kind,
				Score:     r.Score,
				Threshold: threshold,
				Retries:   rec.Retries,
				FromState: rec.State,
				ToState:   rec.State,
				Reason:    "retry budget exhausted",
			})
			return Verdict{}, ErrRetryExhausted
		}
		rec.Retries++
		rec.LastVendor = r.Vendor
		c.appendEventLocked(rec, Event{
			Type:      EventRetryScheduled,
			Vendor:    r.Vendor,
			Kind:      r.Kind,
			Score:     r.Score,
			Threshold: threshold,
			Retries:   rec.Retries,
			FromState: rec.State,
			ToState:   rec.State,
			Outcome:   OutcomePending,
		})
		return Verdict{
			CaseID:    rec.ID,
			Outcome:   OutcomePending,
			Pending:   true,
			Retries:   rec.Retries,
			Score:     r.Score,
			Threshold: threshold,
		}, nil

	case VendorRefer:
		from := rec.State
		rec.State = CaseManual
		rec.LastVendor = r.Vendor
		c.appendEventLocked(rec, Event{
			Type:      EventManualReferral,
			Vendor:    r.Vendor,
			Kind:      r.Kind,
			Score:     r.Score,
			Threshold: threshold,
			Outcome:   OutcomeManual,
			FromState: from,
			ToState:   CaseManual,
		})
		return Verdict{
			CaseID:    rec.ID,
			Outcome:   OutcomeManual,
			Score:     r.Score,
			Threshold: threshold,
			Conflict:  rec.Conflict,
		}, nil

	case VendorPass, VendorFail:
		approved := r.Score >= threshold

		// A digest already bound to another account must never be silently
		// auto-approved. Hold the otherwise-passing case for a human with the
		// conflict marker set; objective below-threshold rejections remain
		// deterministic.
		if approved && c.crossAccountConflictLocked(rec) {
			from := rec.State
			rec.State = CaseManual
			rec.LastVendor = r.Vendor
			c.appendEventLocked(rec, Event{
				Type:      EventConflictHold,
				Vendor:    r.Vendor,
				Kind:      r.Kind,
				Score:     r.Score,
				Threshold: threshold,
				Outcome:   OutcomeManual,
				FromState: from,
				ToState:   CaseManual,
				Reason:    "cross-account document conflict requires review",
			})
			return Verdict{
				CaseID:    rec.ID,
				Outcome:   OutcomeManual,
				Score:     r.Score,
				Threshold: threshold,
				Conflict:  true,
			}, nil
		}

		outcome := OutcomeRejected
		toState := CaseRejected
		if approved {
			outcome = OutcomeApproved
			toState = CaseApproved
		}
		from := rec.State
		rec.State = toState
		rec.LastVendor = r.Vendor
		if approved {
			c.bindDigestLocked(rec)
		}
		c.appendEventLocked(rec, Event{
			Type:      EventVendorResult,
			Vendor:    r.Vendor,
			Kind:      r.Kind,
			Score:     r.Score,
			Threshold: threshold,
			Outcome:   outcome,
			FromState: from,
			ToState:   toState,
		})
		return Verdict{
			CaseID:    rec.ID,
			Outcome:   outcome,
			Score:     r.Score,
			Threshold: threshold,
			Conflict:  rec.Conflict,
		}, nil

	default:
		return Verdict{}, ErrBadVendorResult
	}
}

// Review records the human decision on a manual case.
func (c *IdentityProofingCase) Review(caseID, reviewerID string, approve bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	rec, ok := c.cases[caseID]
	if !ok {
		return ErrUnknownCase
	}
	if strings.TrimSpace(reviewerID) == "" {
		return ErrBlankReviewer
	}
	if rec.State != CaseManual {
		return ErrNotPending
	}

	from := rec.State
	if approve {
		rec.State = CaseApproved
	} else {
		rec.State = CaseRejected
	}
	rec.Reviewer = reviewerID
	if approve {
		// A human approval (including a resolved conflict) establishes the
		// digest-to-account binding.
		c.bindDigestLocked(rec)
	}
	c.appendEventLocked(rec, Event{
		Type:      EventReviewCompleted,
		Actor:     reviewerID,
		Approve:   approve,
		Outcome:   rec.State,
		FromState: from,
		ToState:   rec.State,
	})
	return nil
}

// Status projects the newest case of one user.
func (c *IdentityProofingCase) Status(userID string) (ProofState, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	ids := c.userCaseIDs[userID]
	if len(ids) == 0 {
		return ProofState{}, ErrUnknownUser
	}
	rec := c.cases[ids[len(ids)-1]]
	return ProofState{
		UserID:   userID,
		CaseID:   rec.ID,
		Level:    rec.Level,
		State:    rec.State,
		Approved: rec.State == CaseApproved,
		Conflict: rec.Conflict,
	}, nil
}

// ExpireCase downgrades a passed case whose verification went stale. Only an
// approved case may expire; the resulting state is expired, which gates high
// assurance operations without freezing the account.
func (c *IdentityProofingCase) ExpireCase(caseID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	rec, ok := c.cases[caseID]
	if !ok {
		return ErrUnknownCase
	}
	if rec.State != CaseApproved {
		return ErrNotApproved
	}

	from := rec.State
	rec.State = CaseExpired
	c.appendEventLocked(rec, Event{
		Type:      EventCaseExpired,
		FromState: from,
		ToState:   CaseExpired,
	})
	return nil
}

// CaseState reads the state of one case.
func (c *IdentityProofingCase) CaseState(caseID string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	rec, ok := c.cases[caseID]
	if !ok {
		return "", ErrUnknownCase
	}
	return rec.State, nil
}

// ThresholdFor returns the vendor score threshold of one level.
func (c *IdentityProofingCase) ThresholdFor(level Level) (float64, error) {
	switch level {
	case LevelBasic:
		return 0.60, nil
	case LevelStandard:
		return 0.75, nil
	case LevelHigh:
		return 0.90, nil
	default:
		return 0, ErrBadLevel
	}
}

// Replay returns the ordered audit stream of one case. The returned slice is
// a copy and may be retained or modified by the caller.
func (c *IdentityProofingCase) Replay(caseID string) ([]Event, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	rec, ok := c.cases[caseID]
	if !ok {
		return nil, ErrUnknownCase
	}
	return append([]Event(nil), rec.Events...), nil
}

// Events returns the ordered global audit stream across all cases.
func (c *IdentityProofingCase) Events() []Event {
	c.mu.Lock()
	defer c.mu.Unlock()

	var all []Event
	for _, rec := range c.cases {
		all = append(all, rec.Events...)
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].Seq < all[j].Seq
	})
	return all
}

// OperationAllowed models the post-expiry policy: a stale verification only
// restricts high assurance operations. Approved cases authorise operations at
// or below their level; expired cases keep lower assurance actions available
// so the account is never globally frozen.
func (c *IdentityProofingCase) OperationAllowed(userID string, required Level) (bool, error) {
	if !validLevel(required) {
		return false, ErrBadLevel
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	ids := c.userCaseIDs[userID]
	if len(ids) == 0 {
		return false, ErrUnknownUser
	}
	rec := c.cases[ids[len(ids)-1]]

	switch rec.State {
	case CaseApproved:
		return rec.Level >= required, nil
	case CaseExpired:
		return required < LevelHigh, nil
	default:
		return false, nil
	}
}

// Snapshot serialises cases, counters, audit streams and the sequence
// watermark.
func (c *IdentityProofingCase) Snapshot() ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	dto := snapshotData{
		Version:  snapshotVersion,
		Seq:      c.seq,
		EventSeq: c.eventSeq,
		Cases:    make([]proofCase, 0, len(c.cases)),
	}
	for _, rec := range c.cases {
		cp := *rec
		cp.ConflictWith = append([]string(nil), rec.ConflictWith...)
		cp.Events = append([]Event(nil), rec.Events...)
		dto.Cases = append(dto.Cases, cp)
	}
	sort.Slice(dto.Cases, func(i, j int) bool {
		return dto.Cases[i].Seq < dto.Cases[j].Seq
	})
	return json.Marshal(&dto)
}

// LoadSnapshot replaces the whole state with a snapshot, all or nothing. The
// payload is parsed and validated into fresh tables before the live state is
// touched, so a corrupt snapshot leaves the running orchestrator unchanged.
func (c *IdentityProofingCase) LoadSnapshot(data []byte) error {
	dto, err := decodeSnapshot(data)
	if err != nil {
		return err
	}
	sort.Slice(dto.Cases, func(i, j int) bool {
		return dto.Cases[i].Seq < dto.Cases[j].Seq
	})

	cases := make(map[string]*proofCase, len(dto.Cases))
	userCaseIDs := make(map[string][]string)
	digestOwner := make(map[string]string)

	for i := range dto.Cases {
		rec := dto.Cases[i]
		if rec.ConflictWith == nil {
			rec.ConflictWith = []string{}
		}
		if rec.Events == nil {
			rec.Events = []Event{}
		}
		cp := rec
		cases[cp.ID] = &cp
		userCaseIDs[cp.UserID] = append(userCaseIDs[cp.UserID], cp.ID)
	}

	for userID := range userCaseIDs {
		ids := userCaseIDs[userID]
		sort.Slice(ids, func(i, j int) bool {
			return cases[ids[i]].Seq < cases[ids[j]].Seq
		})
		userCaseIDs[userID] = ids
	}
	// dto.Cases is sequence ordered; applying bound records in order leaves
	// the newest binding as the owner.
	for i := range dto.Cases {
		rec := dto.Cases[i]
		if rec.DigestBound && rec.Digest != "" {
			digestOwner[rec.Digest] = rec.UserID
		}
	}

	eventSeq := dto.EventSeq
	for _, rec := range cases {
		for _, ev := range rec.Events {
			if ev.Seq > eventSeq {
				eventSeq = ev.Seq
			}
		}
	}

	c.mu.Lock()
	c.seq = dto.Seq
	c.eventSeq = eventSeq
	c.cases = cases
	c.userCaseIDs = userCaseIDs
	c.digestOwner = digestOwner
	c.mu.Unlock()
	return nil
}

const snapshotVersion = 1

type snapshotData struct {
	Version  int         `json:"version"`
	Seq      int64       `json:"seq"`
	EventSeq int64       `json:"event_seq"`
	Cases    []proofCase `json:"cases"`
}

// decodeSnapshot parses and strictly validates a snapshot without touching the
// live orchestrator state.
func decodeSnapshot(data []byte) (snapshotData, error) {
	var dto snapshotData
	if err := json.Unmarshal(data, &dto); err != nil {
		return snapshotData{}, ErrBadSnapshot
	}
	if dto.Version != snapshotVersion || dto.Seq < 0 || dto.EventSeq < 0 {
		return snapshotData{}, ErrBadSnapshot
	}

	seenIDs := make(map[string]struct{}, len(dto.Cases))
	seenSeqs := make(map[int64]struct{}, len(dto.Cases))
	for i := range dto.Cases {
		rec := &dto.Cases[i]
		if strings.TrimSpace(rec.ID) == "" || strings.TrimSpace(rec.UserID) == "" {
			return snapshotData{}, ErrBadSnapshot
		}
		if !validLevel(rec.Level) || !knownState(rec.State) {
			return snapshotData{}, ErrBadSnapshot
		}
		if rec.Seq <= 0 || rec.Seq > dto.Seq || rec.Retries < 0 {
			return snapshotData{}, ErrBadSnapshot
		}
		if _, dup := seenIDs[rec.ID]; dup {
			return snapshotData{}, ErrBadSnapshot
		}
		if _, dup := seenSeqs[rec.Seq]; dup {
			return snapshotData{}, ErrBadSnapshot
		}
		seenIDs[rec.ID] = struct{}{}
		seenSeqs[rec.Seq] = struct{}{}

		if rec.Digest != "" && rec.Document.Digest != "" && rec.Digest != rec.Document.Digest {
			return snapshotData{}, ErrBadSnapshot
		}
		switch rec.State {
		case CaseComparing, CaseManual, CaseApproved, CaseRejected, CaseExpired:
			if rec.Digest == "" || !rec.Liveness {
				return snapshotData{}, ErrBadSnapshot
			}
		}

		var lastEventSeq int64
		for _, ev := range rec.Events {
			if ev.Seq <= 0 || ev.Seq < lastEventSeq {
				return snapshotData{}, ErrBadSnapshot
			}
			lastEventSeq = ev.Seq
		}
	}
	return dto, nil
}

func (c *IdentityProofingCase) appendEventLocked(rec *proofCase, ev Event) Event {
	c.eventSeq++
	ev.Seq = c.eventSeq
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	if ev.CaseID == "" {
		ev.CaseID = rec.ID
	}
	if ev.UserID == "" {
		ev.UserID = rec.UserID
	}
	rec.Events = append(rec.Events, ev)
	return ev
}

// detectDigestConflictLocked flags a fresh case when its digest is already
// bound to a different account. It never changes the case state by itself.
func (c *IdentityProofingCase) detectDigestConflictLocked(rec *proofCase) {
	owner, ok := c.digestOwner[rec.Digest]
	if !ok || owner == rec.UserID {
		return
	}
	rec.Conflict = true
	rec.ConflictWith = addUser(rec.ConflictWith, owner)
	sort.Strings(rec.ConflictWith)
}

// crossAccountConflictLocked reports whether the case digest is bound to
// another account, including a conflict discovered at document intake.
func (c *IdentityProofingCase) crossAccountConflictLocked(rec *proofCase) bool {
	if rec.Conflict {
		return true
	}
	owner, ok := c.digestOwner[rec.Digest]
	return ok && owner != rec.UserID
}

// bindDigestLocked establishes the digest-to-account binding after a final
// approval. Callers hold c.mu.
func (c *IdentityProofingCase) bindDigestLocked(rec *proofCase) {
	if rec.Digest == "" {
		return
	}
	c.digestOwner[rec.Digest] = rec.UserID
	rec.DigestBound = true
}

func (c *IdentityProofingCase) collectIfCompleteLocked(rec *proofCase) {
	if rec.State == CaseCollected && rec.Digest != "" && rec.Liveness {
		rec.State = CaseComparing
	}
}

func validLevel(level Level) bool {
	return level == LevelBasic || level == LevelStandard || level == LevelHigh
}

func knownState(state string) bool {
	switch state {
	case CaseCollected, CaseComparing, CaseManual, CaseApproved, CaseRejected, CaseExpired:
		return true
	default:
		return false
	}
}

func mustThreshold(level Level) float64 {
	switch level {
	case LevelBasic:
		return 0.60
	case LevelStandard:
		return 0.75
	case LevelHigh:
		return 0.90
	default:
		return 0
	}
}

func formatCaseID(seq int64) string {
	return fmt.Sprintf("case-%06d", seq)
}

func addUser(users []string, user string) []string {
	for _, existing := range users {
		if existing == user {
			return users
		}
	}
	return append(users, user)
}
