// Package task picks the step-up method for a sensitive operation and keeps
// the ladder from collapsing to the weakest rung.
package task

import (
	"errors"
	"time"
)

// errNotImplemented is returned by behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Authentication methods, ordered from strongest to weakest.
const (
	MethodSecurityKey  = "security_key"
	MethodPlatformAuth = "platform_authenticator"
	MethodTOTP         = "totp"
	MethodPush         = "push"
	MethodSMS          = "sms"
)

// Method strengths. The ladder never moves up to a weaker rung on its own.
const (
	StrengthSecurityKey  = 5
	StrengthPlatformAuth = 4
	StrengthTOTP         = 3
	StrengthPush         = 2
	StrengthSMS          = 1
)

// Challenge states.
const (
	ChallengeOpen      = "open"
	ChallengeCompleted = "completed"
	ChallengeLocked    = "locked"
	ChallengeAbandoned = "abandoned"
)

// Result reasons.
const (
	ReasonVerified     = "verified"
	ReasonAttemptLimit = "attempt_limit"
)

// DowngradeUnavailable is the only reason a weaker rung may be chosen.
const DowngradeUnavailable = "method_unavailable"

// Errors reported by the ladder.
var (
	ErrBlankID           = errors.New("user id and operation are required")
	ErrUnknownMethod     = errors.New("unknown authentication method")
	ErrMethodEnrolled    = errors.New("method is already enrolled")
	ErrMethodNotEnrolled = errors.New("method is not enrolled")
	ErrBadReason         = errors.New("disable reason is required")
	ErrNoMethod          = errors.New("no usable authentication method")
	ErrChallengeNotFound = errors.New("challenge not found")
	ErrChallengeClosed   = errors.New("challenge is already closed")
	ErrChallengeExpired  = errors.New("challenge has expired")
	ErrChallengeLimit    = errors.New("concurrent challenge limit reached for this user")
	ErrMethodNotAllowed  = errors.New("submitted method is not the one the challenge is on")
	ErrBadProof          = errors.New("proof is empty")
	ErrAttemptLimit      = errors.New("attempt limit reached for this challenge")
	ErrNoStrongerMethod  = errors.New("no stronger method is available")
	ErrBadSnapshot       = errors.New("snapshot payload is invalid")
)

// Method is one rung of the ladder as offered to a user.
type Method struct {
	Kind     string `json:"kind"`
	Strength int    `json:"strength"`
}

// Proof is what the user presents for one method.
type Proof struct {
	Code      string `json:"code"`
	Assertion string `json:"assertion"`
	DeviceID  string `json:"device_id"`
}

// Downgrade records why the ladder left a stronger rung.
type Downgrade struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason"`
}

// Challenge is one step-up attempt for an operation.
type Challenge struct {
	ID         string      `json:"id"`
	UserID     string      `json:"user_id"`
	Operation  string      `json:"operation"`
	Method     string      `json:"method"`
	Strength   int         `json:"strength"`
	Attempts   int         `json:"attempts"`
	Downgrades []Downgrade `json:"downgrades"`
	State      string      `json:"state"`
	ExpiresAt  time.Time   `json:"expires_at"`
	Seq        int64       `json:"seq"`
}

// Result is one completed step-up.
type Result struct {
	ChallengeID string    `json:"challenge_id"`
	UserID      string    `json:"user_id"`
	Operation   string    `json:"operation"`
	Method      string    `json:"method"`
	Strength    int       `json:"strength"`
	Verified    bool      `json:"verified"`
	Reason      string    `json:"reason"`
	ExpiresAt   time.Time `json:"expires_at"`
	Seq         int64     `json:"seq"`
}

// Options bounds the ladder and supplies the clock.
type Options struct {
	MaxChallengesPerUser int
	MaxAttemptsPerMethod int
	ChallengeTTL         time.Duration
	ResultTTL            time.Duration
	Now                  func() time.Time
}

// StepUpMethodLadder owns enrolments, challenges and the reusable results.
type StepUpMethodLadder struct {
	opts Options
}

// NewStepUpMethodLadder builds a ladder with the given options.
func NewStepUpMethodLadder(opts Options) *StepUpMethodLadder {
	return &StepUpMethodLadder{opts: opts}
}

// Enroll registers one method for a user.
func (l *StepUpMethodLadder) Enroll(userID, method string) error {
	return errNotImplemented
}

// Revoke unregisters one method for a user.
func (l *StepUpMethodLadder) Revoke(userID, method string) error {
	return errNotImplemented
}

// Disable marks an enrolled method temporarily unavailable and records why.
func (l *StepUpMethodLadder) Disable(userID, method, reason string) error {
	return errNotImplemented
}

// Enable brings a disabled method back into the ladder.
func (l *StepUpMethodLadder) Enable(userID, method string) error {
	return errNotImplemented
}

// Available lists the usable methods of a user, strongest first.
func (l *StepUpMethodLadder) Available(userID string) []Method {
	return nil
}

// Begin starts a challenge on the strongest usable method.
func (l *StepUpMethodLadder) Begin(userID, operation string) (Challenge, error) {
	return Challenge{}, errNotImplemented
}

// Submit presents a proof for the method the challenge is on.
func (l *StepUpMethodLadder) Submit(challengeID, method string, proof Proof) (Result, error) {
	return Result{}, errNotImplemented
}

// Escalate moves an open challenge to the next stronger usable method.
func (l *StepUpMethodLadder) Escalate(challengeID string) (Challenge, error) {
	return Challenge{}, errNotImplemented
}

// Abandon closes a challenge without a result.
func (l *StepUpMethodLadder) Abandon(challengeID string) error {
	return errNotImplemented
}

// Reuse returns a still valid completed result for the operation.
func (l *StepUpMethodLadder) Reuse(userID, operation string) (Result, bool) {
	return Result{}, false
}

// LookupChallenge reads one challenge record.
func (l *StepUpMethodLadder) LookupChallenge(challengeID string) (Challenge, bool) {
	return Challenge{}, false
}

// Snapshot serialises enrolments, challenges, results and the counters.
func (l *StepUpMethodLadder) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (l *StepUpMethodLadder) Restore(data []byte) error {
	return errNotImplemented
}
