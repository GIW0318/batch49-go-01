// Package task gates traffic by issuing proof of work challenges to new
// sources, verifying one time solutions and rate limiting admitted sources.
package task

import (
	"errors"
	"time"
)

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Decision actions.
const (
	ActionAllow     = "allow"
	ActionChallenge = "challenge"
	ActionReject    = "reject"
)

// Errors returned by the gate.
var (
	ErrBlankSource      = errors.New("source address is required")
	ErrBadSource        = errors.New("source address is not a valid ip")
	ErrBadCIDR          = errors.New("prefix is not valid cidr")
	ErrUnknownChallenge = errors.New("challenge not found")
	ErrChallengeExpired = errors.New("challenge has expired")
	ErrReplay           = errors.New("challenge has already been solved")
	ErrBadSolution      = errors.New("proof of work does not meet the difficulty")
	ErrBlocked          = errors.New("source is blocked")
	ErrRateLimited      = errors.New("source exceeded its admission rate")
	ErrSourceLimit      = errors.New("tracked source capacity reached")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

// GateConfig holds the challenge policy and the bucket limits.
type GateConfig struct {
	DifficultyBase  int
	DifficultyMax   int
	ChallengeTTL    time.Duration
	PassTTL         time.Duration
	MaxFailures     int
	BlockDuration   time.Duration
	PenaltyDecay    time.Duration
	RefillPerSecond float64
	Burst           int
	MaxSources      int
	IPv6PrefixBits  int
}

// Challenge is one proof of work request for a source prefix.
type Challenge struct {
	ID         string    `json:"id"`
	Prefix     string    `json:"prefix"`
	Difficulty int       `json:"difficulty"`
	ExpiresAt  time.Time `json:"expires_at"`
	Used       bool      `json:"used"`
}

// Pass is the admission token granted after a solved challenge.
type Pass struct {
	Token     string    `json:"token"`
	Prefix    string    `json:"prefix"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Decision is the admission answer for one source.
type Decision struct {
	Action      string        `json:"action"`
	Difficulty  int           `json:"difficulty"`
	ChallengeID string        `json:"challenge_id"`
	RetryAfter  time.Duration `json:"retry_after"`
}

// BucketState is the live rate limit view of one source prefix.
type BucketState struct {
	Prefix       string    `json:"prefix"`
	Tokens       float64   `json:"tokens"`
	Failures     int       `json:"failures"`
	BlockedUntil time.Time `json:"blocked_until"`
}

// GateStats reports the gate counters.
type GateStats struct {
	Sources      int `json:"sources"`
	Challenges   int `json:"challenges"`
	Passes       int `json:"passes"`
	Allowed      int `json:"allowed"`
	Challenged   int `json:"challenged"`
	Rejected     int `json:"rejected"`
	Blocked      int `json:"blocked"`
	Failures     int `json:"failures"`
	Evictions    int `json:"evictions"`
}

// ChallengeRateGate owns the prefixes, challenges, passes and buckets.
type ChallengeRateGate struct {
	cfg GateConfig
	now time.Time
	seq int64
}

// NewChallengeRateGate builds an empty gate.
func NewChallengeRateGate(cfg GateConfig) *ChallengeRateGate {
	return &ChallengeRateGate{cfg: cfg}
}

// SetNow fixes the clock used for challenges, blocks and refill.
func (g *ChallengeRateGate) SetNow(now time.Time) {
}

// Admit decides whether one source may proceed.
func (g *ChallengeRateGate) Admit(source string) (Decision, error) {
	return Decision{}, errNotImplemented
}

// Verify checks one proof of work and grants a pass on success.
func (g *ChallengeRateGate) Verify(challengeID, nonce, digest string) (Pass, error) {
	return Pass{}, errNotImplemented
}

// AllowPrefix adds one prefix to the allow list.
func (g *ChallengeRateGate) AllowPrefix(cidr string) error {
	return errNotImplemented
}

// DenyPrefix adds one prefix to the deny list.
func (g *ChallengeRateGate) DenyPrefix(cidr string) error {
	return errNotImplemented
}

// Challenge returns a stored challenge by id.
func (g *ChallengeRateGate) Challenge(challengeID string) (Challenge, bool) {
	return Challenge{}, false
}

// Bucket returns the live bucket of one source prefix.
func (g *ChallengeRateGate) Bucket(source string) (BucketState, bool) {
	return BucketState{}, false
}

// Sweep drops expired challenges, passes and idle buckets.
func (g *ChallengeRateGate) Sweep() int {
	return 0
}

// Stats returns the current gate counters.
func (g *ChallengeRateGate) Stats() GateStats {
	return GateStats{}
}

// Snapshot serialises buckets, challenges, passes and counters.
func (g *ChallengeRateGate) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole gate state from a snapshot.
func (g *ChallengeRateGate) Restore(data []byte) error {
	return errNotImplemented
}
