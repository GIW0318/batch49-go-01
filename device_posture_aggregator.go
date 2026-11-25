// Package task aggregates the endpoint posture signals of managed devices into
// a compliance tier per device and per user.
package task

import (
	"errors"
	"time"
)

// errNotImplemented is returned by behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Posture tiers.
const (
	TierCompliant   = "compliant"
	TierDegraded    = "degraded"
	TierQuarantined = "quarantined"
)

// Signal kinds accepted by the aggregator.
const (
	SignalEDR        = "edr_agent"
	SignalDiskCrypto = "disk_encryption"
	SignalPatch      = "patch_level"
	SignalJailbreak  = "jailbreak"
)

// EDR agent states carried by a Signal.
const (
	EDRInstalled = "installed"
	EDRRemoved   = "removed"
)

// Audit actions recorded per user.
const (
	AuditReported    = "reported"
	AuditQuarantined = "quarantined"
	AuditReleased    = "released"
	AuditTransferred = "transferred"
)

// Tier thresholds.
const (
	CompliantMinScore = 80
	DegradedMinScore  = 40
)

// Errors reported by the aggregator.
var (
	ErrBlankID        = errors.New("device id and user id are required")
	ErrUnknownKind    = errors.New("unknown signal kind")
	ErrBadScore       = errors.New("signal score must be between 0 and 100")
	ErrStaleSignal    = errors.New("signal is older than the recorded one")
	ErrDeviceOwner    = errors.New("device belongs to another user")
	ErrDeviceNotFound = errors.New("device not found")
	ErrDeviceLimit    = errors.New("device limit reached for this user")
	ErrSignalLimit    = errors.New("signal entry limit reached for this device")
	ErrBadReason      = errors.New("quarantine reason is required")
	ErrNoApprover     = errors.New("release approver is required")
	ErrNotQuarantined = errors.New("device is not quarantined")
	ErrSameOwner      = errors.New("device already belongs to that user")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// Signal is one measurement reported for a device. UserID is the account the
// reporting agent belongs to on first sight.
type Signal struct {
	UserID string    `json:"user_id"`
	Kind   string    `json:"kind"`
	State  string    `json:"state"`
	Score  int       `json:"score"`
	At     time.Time `json:"at"`
	Seq    int64     `json:"seq"`
}

// Posture is the aggregate view of one device.
type Posture struct {
	DeviceID    string   `json:"device_id"`
	UserID      string   `json:"user_id"`
	Score       int      `json:"score"`
	Tier        string   `json:"tier"`
	Quarantined bool     `json:"quarantined"`
	Stale       []string `json:"stale"`
	Seq         int64    `json:"seq"`
}

// AuditEvent is one entry of the per user posture log.
type AuditEvent struct {
	UserID   string    `json:"user_id"`
	DeviceID string    `json:"device_id"`
	Action   string    `json:"action"`
	Tier     string    `json:"tier"`
	Seq      int64     `json:"seq"`
	At       time.Time `json:"at"`
}

// Options bounds the aggregator, weights the signal kinds and supplies the
// clock used for freshness.
type Options struct {
	Weights             map[string]int
	Freshness           map[string]time.Duration
	MaxSignalsPerDevice int
	MaxDevicesPerUser   int
	Now                 func() time.Time
}

// DevicePostureAggregator owns the device table, the signal table, the
// quarantine flags and the per user audit log.
type DevicePostureAggregator struct {
	opts Options
}

// NewDevicePostureAggregator builds an aggregator with the given options.
func NewDevicePostureAggregator(opts Options) *DevicePostureAggregator {
	return &DevicePostureAggregator{opts: opts}
}

// Report records one signal for a device, binding it to the reporting account
// on first sight.
func (a *DevicePostureAggregator) Report(deviceID string, s Signal) error {
	return errNotImplemented
}

// Posture returns the aggregate view of one device.
func (a *DevicePostureAggregator) Posture(deviceID string) (Posture, error) {
	return Posture{}, errNotImplemented
}

// Evaluate returns the postures of every device owned by one account, ordered
// by device id.
func (a *DevicePostureAggregator) Evaluate(userID string) ([]Posture, error) {
	return nil, errNotImplemented
}

// Quarantine pins a device in the quarantined tier until an operator releases
// it.
func (a *DevicePostureAggregator) Quarantine(deviceID, reason string) error {
	return errNotImplemented
}

// Release lifts a manual quarantine after an approver signs off.
func (a *DevicePostureAggregator) Release(deviceID, approverID string) error {
	return errNotImplemented
}

// Transfer moves a device to another account without carrying the audit log.
func (a *DevicePostureAggregator) Transfer(deviceID, toUserID string) error {
	return errNotImplemented
}

// Signals lists the stored signals of one device in canonical kind order.
func (a *DevicePostureAggregator) Signals(deviceID string) []Signal {
	return nil
}

// Audit returns the posture events of one account, oldest first.
func (a *DevicePostureAggregator) Audit(userID string) []AuditEvent {
	return nil
}

// Snapshot serialises devices, signals, quarantine flags, audit events and the
// monotonic counters.
func (a *DevicePostureAggregator) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (a *DevicePostureAggregator) Restore(data []byte) error {
	return errNotImplemented
}
