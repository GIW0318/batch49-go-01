// Package task screens score submissions before they enter a board, parks
// suspicious ones in quarantine and admits them again after manual release.
package task

import "errors"

// errNotImplemented marks behaviour that still has to be written.
var errNotImplemented = errors.New("not implemented")

// Submission and quarantine statuses.
const (
	StatusAdmitted    = "admitted"
	StatusQuarantined = "quarantined"
	StatusReleased    = "released"
	StatusRejected    = "rejected"
	StatusDropped     = "dropped"
	StatusDuplicate   = "duplicate"
)

// Reasons attached to a verdict.
const (
	ReasonOK         = "ok"
	ReasonSingleGain = "single_gain"
	ReasonWindowGain = "window_gain"
	ReasonDuration   = "duration"
)

// Errors reported by the guard.
var (
	ErrBlankKey          = errors.New("board and player are required")
	ErrBadDuration       = errors.New("round duration must be positive")
	ErrBadScore          = errors.New("score must not be negative")
	ErrUnknownSubmission = errors.New("submission id is unknown")
	ErrBadReason         = errors.New("reject reason is required")
	ErrBadState          = errors.New("submission is not pending")
	ErrBadPayload        = errors.New("snapshot payload is invalid")
)

// Options tunes the three screening checks and the quarantine queue.
type Options struct {
	// MaxSingleGain caps the gain over the current best score. Zero disables
	// the check.
	MaxSingleGain int64
	// WindowMs is the sliding window width. Zero disables the window check.
	WindowMs int64
	// WindowGain caps the gain over the best score inside the window.
	WindowGain int64
	// MaxScorePerMinute caps how much score one minute of play may produce.
	// Zero disables the duration check.
	MaxScorePerMinute int64
	// QuarantineLimit caps pending quarantine entries per board. Values
	// below one fall back to 16.
	QuarantineLimit int
}

// Verdict is the outcome of one Submit call.
type Verdict struct {
	SubmissionID string
	Board        string
	Player       string
	Score        int64
	Status       string
	Reason       string
}

// Quarantined is one ledger record kept for a screened submission.
type Quarantined struct {
	ID         string
	Board      string
	Player     string
	Score      int64
	DurationMs int64
	At         int64
	Reasons    []string
	Status     string
}

// EffectiveEntry is one admitted score currently visible on a board.
type EffectiveEntry struct {
	Player string
	Score  int64
	At     int64
}

// Stats is the ledger summary of one board.
type Stats struct {
	Submitted   int
	Admitted    int
	Quarantined int
	Released    int
	Rejected    int
	Dropped     int
	Duplicates  int
}

// ScoreGuard owns the submission ledger, quarantine queues and board state.
type ScoreGuard struct {
	opts Options
}

// NewScoreGuard builds an empty guard.
func NewScoreGuard(opts Options) *ScoreGuard {
	return &ScoreGuard{opts: opts}
}

// Submit screens one score submission and files it in the ledger.
func (g *ScoreGuard) Submit(board, player string, score, durationMs int64, at int64) (Verdict, error) {
	return Verdict{}, errNotImplemented
}

// Quarantine lists the pending quarantine entries of one board, oldest first.
func (g *ScoreGuard) Quarantine(board string) []Quarantined {
	return nil
}

// Submission reads one ledger record by submission id.
func (g *ScoreGuard) Submission(board, submissionID string) (Quarantined, bool) {
	return Quarantined{}, false
}

// Release admits one pending submission with its submitted score.
func (g *ScoreGuard) Release(board, submissionID string) error {
	return errNotImplemented
}

// Reject discards one pending submission with an operator reason.
func (g *ScoreGuard) Reject(board, submissionID, reason string) error {
	return errNotImplemented
}

// Effective reports the best admitted score of one player.
func (g *ScoreGuard) Effective(board, player string) (int64, bool) {
	return 0, false
}

// EffectiveAll lists admitted scores of one board, highest first.
func (g *ScoreGuard) EffectiveAll(board string) []EffectiveEntry {
	return nil
}

// Watermark reports the highest admitted score of one board.
func (g *ScoreGuard) Watermark(board string) (int64, bool) {
	return 0, false
}

// Stats summarises the ledger of one board.
func (g *ScoreGuard) Stats(board string) Stats {
	return Stats{}
}

// Snapshot serialises the ledger, quarantine queues and board state.
func (g *ScoreGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole guard state with a snapshot.
func (g *ScoreGuard) Restore(data []byte) error {
	return errNotImplemented
}
