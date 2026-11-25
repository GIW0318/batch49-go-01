// Package task orchestrates identity proofing cases built from a document, a
// liveness frame and vendor comparison results.
package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

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
}

// Case is one proofing case.
type Case struct {
	ID       string `json:"id"`
	UserID   string `json:"user_id"`
	Level    Level  `json:"level"`
	State    string `json:"state"`
	Digest   string `json:"digest"`
	Liveness bool   `json:"liveness"`
	Retries  int    `json:"retries"`
	Seq      int64  `json:"seq"`
}

// ProofState is the per user projection returned by Status.
type ProofState struct {
	UserID   string `json:"user_id"`
	CaseID   string `json:"case_id"`
	Level    Level  `json:"level"`
	State    string `json:"state"`
	Approved bool   `json:"approved"`
}

// Limits bounds the orchestrator tables.
type Limits struct {
	MaxRetries      int
	MaxCasesPerUser int
}

// IdentityProofingCase owns the case table and the per user counters.
type IdentityProofingCase struct {
	limits Limits
}

// NewIdentityProofingCase builds an orchestrator with the given caps.
func NewIdentityProofingCase(limits Limits) *IdentityProofingCase {
	return &IdentityProofingCase{limits: limits}
}

// Start opens a new proofing case for one user.
func (c *IdentityProofingCase) Start(userID string, level Level) (Case, error) {
	return Case{}, errNotImplemented
}

// SubmitDocument attaches the identity document to a case.
func (c *IdentityProofingCase) SubmitDocument(caseID string, doc Document) error {
	return errNotImplemented
}

// SubmitLiveness attaches the liveness frame to a case.
func (c *IdentityProofingCase) SubmitLiveness(caseID string, frame []byte) error {
	return errNotImplemented
}

// ApplyVendorResult normalises one vendor comparison for a case.
func (c *IdentityProofingCase) ApplyVendorResult(caseID string, r VendorResult) (Verdict, error) {
	return Verdict{}, errNotImplemented
}

// Review records the human decision on a manual case.
func (c *IdentityProofingCase) Review(caseID, reviewerID string, approve bool) error {
	return errNotImplemented
}

// Status projects the newest case of one user.
func (c *IdentityProofingCase) Status(userID string) (ProofState, error) {
	return ProofState{}, errNotImplemented
}

// ExpireCase downgrades a passed case whose verification went stale.
func (c *IdentityProofingCase) ExpireCase(caseID string) error {
	return errNotImplemented
}

// CaseState reads the state of one case.
func (c *IdentityProofingCase) CaseState(caseID string) (string, error) {
	return "", errNotImplemented
}

// ThresholdFor returns the vendor score threshold of one level.
func (c *IdentityProofingCase) ThresholdFor(level Level) (float64, error) {
	return 0, errNotImplemented
}

// Snapshot serialises cases, counters and the sequence watermark.
func (c *IdentityProofingCase) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// LoadSnapshot replaces the whole state with a snapshot, all or nothing.
func (c *IdentityProofingCase) LoadSnapshot(data []byte) error {
	return errNotImplemented
}
