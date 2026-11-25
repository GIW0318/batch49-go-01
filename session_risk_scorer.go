// Package task scores behavioural deviation for established sessions and
// decides when a challenge has to be raised and latched.
package task

import (
	"errors"
	"time"
)

// errNotImplemented marks behaviour that is not built yet.
var errNotImplemented = errors.New("not implemented")

// Event types understood by the scorer.
const (
	EventLoginGeo     = "login_geo"
	EventDeviceChange = "device_change"
	EventUaChange     = "ua_change"
	EventCommandBurst = "command_burst"
)

// Reason codes returned by Explain.
const (
	ReasonBaselineDeviation = "reason_baseline_deviation"
	ReasonSaturated         = "reason_saturated"
	ReasonChallengeLatched  = "reason_challenge_latched"
)

// Challenge levels.
const (
	ChallengeStepUp = "step_up"
	ChallengeBlock  = "block"
)

// Scoring anchors.
const (
	SlotCount       = 6
	SlotHours       = 4
	InitialBaseline = 1.0
	BaselineAlpha   = 0.2
	ScoreAlpha      = 0.3
	DeviationScale  = 10.0
	DefaultMaxScore = 60
)

// Errors returned by the scorer.
var (
	ErrBlankSession   = errors.New("session id is required")
	ErrUnknownSession = errors.New("unknown session")
	ErrBadEvent       = errors.New("unknown event type")
	ErrBadChallenge   = errors.New("unknown challenge")
	ErrQueueFull      = errors.New("session event queue is full")
	ErrSessionLimit   = errors.New("session limit reached for this user")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// ScorerConfig bounds one scorer instance.
type ScorerConfig struct {
	MaxSessionsPerUser int
	MaxQueuePerSession int
	MaxScore           int
	ChallengeThreshold int
}

// Event is one behavioural observation inside a session.
type Event struct {
	UserID   string
	Type     string
	Weight   float64
	At       time.Time
	TimeZone string
}

// Score is the deviation score snapshot of a session.
type Score struct {
	SessionID string
	UserID    string
	Value     float64
	Saturated bool
	Version   uint64
	UpdatedAt time.Time
}

// Reason explains one contribution to a score.
type Reason struct {
	Code   string
	Detail string
	Delta  float64
}

// ChallengeState is the latched challenge of a session.
type ChallengeState struct {
	SessionID   string
	Level       string
	ChallengeID string
	Latched     bool
	IssuedAt    time.Time
}

// SessionRiskScorer owns per-user baselines and per-session scores.
type SessionRiskScorer struct {
	cfg ScorerConfig
}

// NewSessionRiskScorer builds a scorer with the given bounds.
func NewSessionRiskScorer(cfg ScorerConfig) *SessionRiskScorer {
	return &SessionRiskScorer{cfg: cfg}
}

// Observe folds one event into the session score.
func (s *SessionRiskScorer) Observe(sessionID string, e Event) (Score, error) {
	return Score{}, errNotImplemented
}

// Score returns the current score of a session.
func (s *SessionRiskScorer) Score(sessionID string) (Score, error) {
	return Score{}, errNotImplemented
}

// Explain returns the reasons behind the current score.
func (s *SessionRiskScorer) Explain(sessionID string) []Reason {
	return nil
}

// Challenge latches a challenge on a session.
func (s *SessionRiskScorer) Challenge(sessionID, level string) error {
	return errNotImplemented
}

// Ack acknowledges a latched challenge.
func (s *SessionRiskScorer) Ack(sessionID, challengeID string) error {
	return errNotImplemented
}

// Snapshot serialises the scorer state.
func (s *SessionRiskScorer) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the scorer state from a snapshot.
func (s *SessionRiskScorer) Restore(data []byte) error {
	return errNotImplemented
}

// Baseline returns the per-user, per-event-type, per-slot baseline.
func (s *SessionRiskScorer) Baseline(userID, eventType string, slot int) (float64, error) {
	return 0, errNotImplemented
}

// ChallengeState returns the latched challenge state of a session.
func (s *SessionRiskScorer) ChallengeState(sessionID string) (ChallengeState, bool) {
	return ChallengeState{}, false
}

// Sessions lists the session identifiers opened by a user.
func (s *SessionRiskScorer) Sessions(userID string) []string {
	return nil
}
