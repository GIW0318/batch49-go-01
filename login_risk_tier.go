// Package task grades login requests into risk tiers before authentication and
// picks the matching challenge.
package task

import (
	"errors"
	"time"
)

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Risk tiers.
const (
	TierLow    = "low"
	TierMedium = "medium"
	TierHigh   = "high"
	TierSevere = "severe"
)

// Challenge kinds.
const (
	ChallengeNone  = "none"
	ChallengeSMS   = "sms"
	ChallengeTOTP  = "totp"
	ChallengeBlock = "block"
)

// Decision results.
const (
	DecisionAllow = "allow"
	DecisionDeny  = "deny"
)

// Reasons attached to a tier or a decision.
const (
	ReasonClean            = "reason_clean"
	ReasonSoftSignals      = "reason_soft_signals"
	ReasonHardRule         = "reason_hard_rule"
	ReasonOperatorOverride = "reason_operator_override"
	ReasonCodeMismatch     = "reason_code_mismatch"
	ReasonLocked           = "reason_attempts_exhausted"
)

// RegionBlocked is the hard rule region that always lands in the severe tier.
const RegionBlocked = "blocked"

// MaxAttempts is the number of rejected proofs a challenge tolerates.
const MaxAttempts = 3

// Errors returned by the tier engine.
var (
	ErrBadEvent           = errors.New("login event is missing required fields")
	ErrUnknownTier        = errors.New("unknown tier")
	ErrBlankDevice        = errors.New("user id and device id are required")
	ErrBadTTL             = errors.New("device trust ttl must be positive")
	ErrDeviceLimit        = errors.New("trusted device limit reached for this user")
	ErrBlankField         = errors.New("actor and reason are required")
	ErrAlreadyOverridden  = errors.New("tier was already overridden")
	ErrTooManyAttempts    = errors.New("challenge attempts exhausted")
	ErrBadSnapshot        = errors.New("snapshot payload is invalid")
)

// LoginEvent is one login request entering the tier engine.
type LoginEvent struct {
	UserID      string    `json:"user_id"`
	DeviceID    string    `json:"device_id"`
	Region      string    `json:"region"`
	Hour        int       `json:"hour"`
	NewDevice   bool      `json:"new_device"`
	UnusualHour bool      `json:"unusual_hour"`
	NewNetwork  bool      `json:"new_network"`
	LeakedCred  bool      `json:"leaked_credential"`
	At          time.Time `json:"at"`
}

// Tier is one graded login record.
type Tier struct {
	ID         string `json:"id"`
	UserID     string `json:"user_id"`
	Level      string `json:"level"`
	Score      int    `json:"score"`
	Reason     string `json:"reason"`
	Overridden bool   `json:"overridden"`
	Seq        int64  `json:"seq"`
}

// Challenge is one issued challenge for a tier.
type Challenge struct {
	TierID   string `json:"tier_id"`
	UserID   string `json:"user_id"`
	Kind     string `json:"kind"`
	Code     string `json:"code"`
	Attempts int    `json:"attempts"`
}

// Proof is the answer a client sends back for a challenge.
type Proof struct {
	Code string `json:"code"`
}

// Decision is the outcome of one proof check.
type Decision struct {
	TierID   string `json:"tier_id"`
	Result   string `json:"result"`
	Reason   string `json:"reason"`
	Attempts int    `json:"attempts"`
	Locked   bool   `json:"locked"`
}

// Limits bounds the tier engine tables.
type Limits struct {
	MaxDevicesPerUser int
	MaxTiersPerUser   int
}

// LoginRiskTier owns the tier table, the trusted device table and the clock.
type LoginRiskTier struct {
	limits Limits
	now    func() time.Time
}

// NewLoginRiskTier builds a tier engine with the given caps and clock.
func NewLoginRiskTier(limits Limits, now func() time.Time) *LoginRiskTier {
	return &LoginRiskTier{limits: limits, now: now}
}

// Classify grades one login event into a risk tier.
func (r *LoginRiskTier) Classify(e LoginEvent) (Tier, error) {
	return Tier{}, errNotImplemented
}

// Challenge issues the challenge that matches a tier.
func (r *LoginRiskTier) Challenge(tierID, userID string) (Challenge, error) {
	return Challenge{}, errNotImplemented
}

// Verify checks one proof against the challenge of a tier.
func (r *LoginRiskTier) Verify(tierID string, proof Proof) (Decision, error) {
	return Decision{}, errNotImplemented
}

// TrustDevice marks a device as trusted for the given ttl.
func (r *LoginRiskTier) TrustDevice(userID, deviceID string, ttl time.Duration) error {
	return errNotImplemented
}

// Override records an operator decision that downgrades a tier to a challenge.
func (r *LoginRiskTier) Override(tierID, actorID, reason string) error {
	return errNotImplemented
}

// Tiers lists the tier records of one user, oldest first.
func (r *LoginRiskTier) Tiers(userID string) []Tier {
	return nil
}

// Trusted reports whether a device is still inside its trust window.
func (r *LoginRiskTier) Trusted(userID, deviceID string) bool {
	return false
}

// Snapshot serialises tiers, trusted device expiry and the sequence watermark.
func (r *LoginRiskTier) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// LoadSnapshot replaces the whole state with a snapshot, all or nothing.
func (r *LoginRiskTier) LoadSnapshot(data []byte) error {
	return errNotImplemented
}
