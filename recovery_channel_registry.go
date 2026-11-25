// Package task keeps the recovery channels of an account: the spare mailbox and
// the spare handset that a locked out user can still reach.
package task

import (
	"errors"
	"time"
)

// errNotImplemented is returned by behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Channel states kept by the registry.
const (
	ChannelUnverified = "unverified"
	ChannelActive     = "active"
	ChannelBounced    = "bounced"
	ChannelRemoved    = "removed"
)

// Channel kinds the registry accepts.
const (
	KindEmail = "email"
	KindSMS   = "sms"
)

// Errors reported by the registry.
var (
	ErrBlankIdentity   = errors.New("user id and channel id are required")
	ErrBadChannelKind  = errors.New("channel kind must be email or sms")
	ErrChannelExists   = errors.New("channel is already registered")
	ErrChannelNotFound = errors.New("channel not found")
	ErrChannelLimit    = errors.New("channel limit reached for this user")
	ErrBadProof        = errors.New("verification proof does not match")
	ErrVerifyLocked    = errors.New("verification attempts exhausted")
	ErrLastChannel     = errors.New("cannot drop the last active recovery channel")
	ErrNotVerified     = errors.New("channel is not verified")
	ErrBadDetail       = errors.New("bounce detail is required")
	ErrNoPrimary       = errors.New("account has no primary recovery channel")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// Channel is one registered recovery endpoint.
type Channel struct {
	ID       string `json:"id"`
	UserID   string `json:"user_id"`
	Kind     string `json:"kind"`
	Address  string `json:"address"`
	State    string `json:"state"`
	Detail   string `json:"detail"`
	Attempts int    `json:"attempts"`
	Seq      int64  `json:"seq"`
}

// Pending is what a registration hands back so the proof can be delivered.
type Pending struct {
	UserID       string `json:"user_id"`
	ChannelID    string `json:"channel_id"`
	Proof        string `json:"proof"`
	AttemptsLeft int    `json:"attempts_left"`
	Seq          int64  `json:"seq"`
}

// Notice is one security notification queued for a channel.
type Notice struct {
	UserID    string    `json:"user_id"`
	ChannelID string    `json:"channel_id"`
	Kind      string    `json:"kind"`
	Message   string    `json:"message"`
	At        time.Time `json:"at"`
	Seq       int64     `json:"seq"`
}

// Options bounds the registry and supplies the clock used for cooldowns.
type Options struct {
	MaxChannelsPerUser int
	MaxVerifyAttempts  int
	Cooldown           time.Duration
	Now                func() time.Time
}

// RecoveryChannelRegistry owns the channel table, the notice log and the
// primary switch deadline of every account.
type RecoveryChannelRegistry struct {
	opts Options
}

// NewRecoveryChannelRegistry builds a registry with the given options.
func NewRecoveryChannelRegistry(opts Options) *RecoveryChannelRegistry {
	return &RecoveryChannelRegistry{opts: opts}
}

// Add registers a channel in the unverified state and returns the proof to
// deliver plus the attempts left.
func (r *RecoveryChannelRegistry) Add(userID string, ch Channel) (Pending, error) {
	return Pending{}, errNotImplemented
}

// Verify consumes a proof and promotes the channel to the active state.
func (r *RecoveryChannelRegistry) Verify(userID, channelID, proof string) error {
	return errNotImplemented
}

// Remove drops a channel, refusing when it is the last active one.
func (r *RecoveryChannelRegistry) Remove(userID, channelID string) error {
	return errNotImplemented
}

// SetPrimary schedules the primary switch after the cooldown elapses.
func (r *RecoveryChannelRegistry) SetPrimary(userID, channelID string) error {
	return errNotImplemented
}

// Primary reports the channel currently used first for account recovery.
func (r *RecoveryChannelRegistry) Primary(userID string) (Channel, error) {
	return Channel{}, errNotImplemented
}

// Usable lists the active channels in registration order.
func (r *RecoveryChannelRegistry) Usable(userID string) ([]Channel, error) {
	return nil, errNotImplemented
}

// Channels lists every channel still on record, whatever its state.
func (r *RecoveryChannelRegistry) Channels(userID string) []Channel {
	return nil
}

// MarkBounce records a delivery failure and takes the channel out of recovery.
func (r *RecoveryChannelRegistry) MarkBounce(channelID, detail string) error {
	return errNotImplemented
}

// Notices returns the security notifications queued for one account, oldest
// first.
func (r *RecoveryChannelRegistry) Notices(userID string) []Notice {
	return nil
}

// Snapshot serialises channels, proofs, counters and the primary switch
// deadline.
func (r *RecoveryChannelRegistry) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (r *RecoveryChannelRegistry) Restore(data []byte) error {
	return errNotImplemented
}
