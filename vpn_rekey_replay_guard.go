// Package task implements the receive side of a VPN tunnel rekey: epoch key
// installation, a sliding replay window per epoch, send counter allocation and
// restart safe snapshots.
package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Verdict results for packet admission.
const (
	VerdictAccept = "accept"
	VerdictReplay = "replay"
	VerdictDrop   = "drop"
)

// Drop reasons reported together with VerdictDrop.
const (
	ReasonEpochUnknown = "epoch_unknown"
	ReasonEpochRetired = "epoch_retired"
	ReasonCounterOld   = "counter_too_old"
	ReasonBudget       = "budget_exhausted"
)

// Errors returned by the guard.
var (
	ErrBadConfig     = errors.New("guard config is invalid")
	ErrBadEpoch      = errors.New("epoch must be positive")
	ErrBadKeyID      = errors.New("key id is required")
	ErrEpochStale    = errors.New("epoch is not newer than the active one")
	ErrEpochTooFar   = errors.New("epoch is too far ahead of the active one")
	ErrEpochUnknown  = errors.New("epoch is not installed")
	ErrEpochRetired  = errors.New("epoch has been retired")
	ErrActiveEpoch   = errors.New("the active epoch cannot be retired")
	ErrPacketInvalid = errors.New("packet is malformed")
	ErrBadSnapshot   = errors.New("snapshot payload is invalid")
)

// GuardConfig bounds one guard instance. MaxEpochs is the number of active
// epochs kept for traffic. MaxFutureEpochs caps how far ahead a new epoch may
// jump; zero means the jump is not limited. MaxBytesPerEpoch caps the accepted
// payload bytes of one epoch.
type GuardConfig struct {
	MaxEpochs        int
	MaxFutureEpochs  uint64
	MaxBytesPerEpoch int64
	Now              func() int64
}

// Packet is one encrypted transport packet header as it arrives.
type Packet struct {
	Epoch   uint64
	Counter uint64
	Bytes   int
	KeyID   string
}

// Verdict is the admission result for one packet.
type Verdict struct {
	Result  string
	Reason  string
	Epoch   uint64
	Counter uint64
}

// EpochState is the observability record of one epoch.
type EpochState struct {
	Epoch     uint64
	KeyID     string
	WindowTop uint64
	Accepted  uint64
	Replays   uint64
	Retired   bool
}

// RekeyReplayGuard owns the epoch table, the per epoch replay windows and the
// send counters.
type RekeyReplayGuard struct {
	cfg GuardConfig
}

// NewRekeyReplayGuard validates the configuration and builds an empty guard.
func NewRekeyReplayGuard(cfg GuardConfig) (*RekeyReplayGuard, error) {
	return nil, errNotImplemented
}

// InstallEpoch installs a new epoch key and retires the oldest active epoch
// when the active table is full.
func (g *RekeyReplayGuard) InstallEpoch(epoch uint64, keyID string) error {
	return errNotImplemented
}

// ActiveEpoch reports the newest installed epoch, zero when none is installed.
func (g *RekeyReplayGuard) ActiveEpoch() uint64 {
	return 0
}

// AcceptPacket classifies one inbound packet against its epoch replay window.
func (g *RekeyReplayGuard) AcceptPacket(pkt Packet) (Verdict, error) {
	return Verdict{}, errNotImplemented
}

// NextCounter allocates the next send counter of one epoch.
func (g *RekeyReplayGuard) NextCounter(epoch uint64) (uint64, error) {
	return 0, errNotImplemented
}

// RetireEpoch retires one non active epoch. Retiring an already retired or
// unknown epoch is a no-op.
func (g *RekeyReplayGuard) RetireEpoch(epoch uint64) error {
	return errNotImplemented
}

// WindowTop reports the highest accepted counter of one epoch.
func (g *RekeyReplayGuard) WindowTop(epoch uint64) (uint64, bool) {
	return 0, false
}

// Epochs lists the retained epochs, retired tombstones included, in epoch
// order.
func (g *RekeyReplayGuard) Epochs() []EpochState {
	return nil
}

// ReplayCount aggregates the replay verdicts of one key id across epochs.
func (g *RekeyReplayGuard) ReplayCount(keyID string) uint64 {
	return 0
}

// AcceptedCount reports how many packets one epoch accepted.
func (g *RekeyReplayGuard) AcceptedCount(epoch uint64) uint64 {
	return 0
}

// Snapshot serialises the epoch table, the windows, the counters and the
// statistics.
func (g *RekeyReplayGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *RekeyReplayGuard) Restore(data []byte) error {
	return errNotImplemented
}
