// Package task aggregates the endpoint posture signals of managed devices into
// a compliance tier per device and per user.
package task

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

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
	mu        sync.Mutex
	maxSig    int
	maxDev    int
	now       func() time.Time
	weights   map[string]int
	freshness map[string]time.Duration
	devices   map[string]*deviceState
	audit     map[string][]AuditEvent
	counter   int64
}

// deviceState is the internal record of one managed device.
type deviceState struct {
	owner            string
	signals          map[string]Signal
	manualQuarantine bool
	quarantineReason string
	seq              int64
}

// kindOrder is the canonical ordering of signal kinds.
var kindOrder = []string{SignalEDR, SignalDiskCrypto, SignalPatch, SignalJailbreak}

var defaultWeights = map[string]int{
	SignalEDR:        40,
	SignalDiskCrypto: 25,
	SignalPatch:      20,
	SignalJailbreak:  15,
}

var defaultFreshness = map[string]time.Duration{
	SignalEDR:        10 * time.Minute,
	SignalDiskCrypto: 24 * time.Hour,
	SignalPatch:      72 * time.Hour,
	SignalJailbreak:  24 * time.Hour,
}

// NewDevicePostureAggregator builds an aggregator with the given options.
func NewDevicePostureAggregator(opts Options) *DevicePostureAggregator {
	a := &DevicePostureAggregator{
		maxSig:    opts.MaxSignalsPerDevice,
		maxDev:    opts.MaxDevicesPerUser,
		now:       opts.Now,
		weights:   make(map[string]int, len(defaultWeights)),
		freshness: make(map[string]time.Duration, len(defaultFreshness)),
		devices:   make(map[string]*deviceState),
		audit:     make(map[string][]AuditEvent),
	}
	for kind, w := range defaultWeights {
		a.weights[kind] = w
	}
	for kind, w := range opts.Weights {
		a.weights[kind] = w
	}
	for kind, d := range defaultFreshness {
		a.freshness[kind] = d
	}
	for kind, d := range opts.Freshness {
		a.freshness[kind] = d
	}
	if a.now == nil {
		a.now = time.Now
	}
	return a
}

func knownKind(kind string) bool {
	switch kind {
	case SignalEDR, SignalDiskCrypto, SignalPatch, SignalJailbreak:
		return true
	}
	return false
}

func blank(id string) bool {
	return strings.TrimSpace(id) == ""
}

// countDevices returns how many devices an account currently owns. Callers
// must hold the lock.
func (a *DevicePostureAggregator) countDevices(userID string) int {
	n := 0
	for _, dev := range a.devices {
		if dev.owner == userID {
			n++
		}
	}
	return n
}

// postureLocked computes the posture of one device. Callers must hold the
// lock.
func (a *DevicePostureAggregator) postureLocked(deviceID string, dev *deviceState) Posture {
	now := a.now()
	score := 0
	var stale []string
	for _, kind := range kindOrder {
		s, ok := dev.signals[kind]
		if !ok {
			continue
		}
		contrib := a.weights[kind] * s.Score / 100
		if window := a.freshness[kind]; window > 0 && now.Sub(s.At) > window {
			contrib = contrib * 40 / 100
			stale = append(stale, kind)
		}
		score += contrib
	}
	hard := false
	if s, ok := dev.signals[SignalJailbreak]; ok && s.Score > 0 {
		hard = true
	}
	if s, ok := dev.signals[SignalEDR]; ok && s.State == EDRRemoved {
		hard = true
	}
	quarantined := hard || dev.manualQuarantine
	tier := TierQuarantined
	if !quarantined {
		switch {
		case score >= CompliantMinScore:
			tier = TierCompliant
		case score >= DegradedMinScore:
			tier = TierDegraded
		}
	}
	return Posture{
		DeviceID:    deviceID,
		UserID:      dev.owner,
		Score:       score,
		Tier:        tier,
		Quarantined: quarantined,
		Stale:       stale,
		Seq:         dev.seq,
	}
}

// appendAudit records one event for an account. Callers must hold the lock.
func (a *DevicePostureAggregator) appendAudit(userID, deviceID, action, tier string) {
	a.audit[userID] = append(a.audit[userID], AuditEvent{
		UserID:   userID,
		DeviceID: deviceID,
		Action:   action,
		Tier:     tier,
		Seq:      a.counter,
		At:       a.now(),
	})
}

// Report records one signal for a device, binding it to the reporting account
// on first sight.
func (a *DevicePostureAggregator) Report(deviceID string, s Signal) error {
	if blank(deviceID) || blank(s.UserID) {
		return ErrBlankID
	}
	if !knownKind(s.Kind) {
		return ErrUnknownKind
	}
	if s.Score < 0 || s.Score > 100 {
		return ErrBadScore
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	dev, ok := a.devices[deviceID]
	if ok {
		if dev.owner != s.UserID {
			return ErrDeviceOwner
		}
	} else {
		if a.maxDev > 0 && a.countDevices(s.UserID) >= a.maxDev {
			return ErrDeviceLimit
		}
		dev = &deviceState{owner: s.UserID, signals: make(map[string]Signal)}
		a.devices[deviceID] = dev
	}
	if existing, ok := dev.signals[s.Kind]; ok {
		if s.At.Before(existing.At) || (s.At.Equal(existing.At) && s.Seq <= existing.Seq) {
			return ErrStaleSignal
		}
	} else if a.maxSig > 0 && len(dev.signals) >= a.maxSig {
		return ErrSignalLimit
	}
	dev.signals[s.Kind] = s
	a.counter++
	dev.seq = a.counter
	a.appendAudit(dev.owner, deviceID, AuditReported, a.postureLocked(deviceID, dev).Tier)
	return nil
}

// Posture returns the aggregate view of one device.
func (a *DevicePostureAggregator) Posture(deviceID string) (Posture, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	dev, ok := a.devices[deviceID]
	if !ok {
		return Posture{}, ErrDeviceNotFound
	}
	return a.postureLocked(deviceID, dev), nil
}

// Evaluate returns the postures of every device owned by one account, ordered
// by device id.
func (a *DevicePostureAggregator) Evaluate(userID string) ([]Posture, error) {
	if blank(userID) {
		return nil, ErrBlankID
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	var ids []string
	for id, dev := range a.devices {
		if dev.owner == userID {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	postures := make([]Posture, 0, len(ids))
	for _, id := range ids {
		postures = append(postures, a.postureLocked(id, a.devices[id]))
	}
	return postures, nil
}

// Quarantine pins a device in the quarantined tier until an operator releases
// it.
func (a *DevicePostureAggregator) Quarantine(deviceID, reason string) error {
	if blank(deviceID) {
		return ErrBlankID
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	dev, ok := a.devices[deviceID]
	if !ok {
		return ErrDeviceNotFound
	}
	if blank(reason) {
		return ErrBadReason
	}
	dev.manualQuarantine = true
	dev.quarantineReason = reason
	a.counter++
	dev.seq = a.counter
	a.appendAudit(dev.owner, deviceID, AuditQuarantined, a.postureLocked(deviceID, dev).Tier)
	return nil
}

// Release lifts a manual quarantine after an approver signs off.
func (a *DevicePostureAggregator) Release(deviceID, approverID string) error {
	if blank(deviceID) {
		return ErrBlankID
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	dev, ok := a.devices[deviceID]
	if !ok {
		return ErrDeviceNotFound
	}
	if blank(approverID) {
		return ErrNoApprover
	}
	if !dev.manualQuarantine {
		return ErrNotQuarantined
	}
	dev.manualQuarantine = false
	dev.quarantineReason = ""
	a.counter++
	dev.seq = a.counter
	a.appendAudit(dev.owner, deviceID, AuditReleased, a.postureLocked(deviceID, dev).Tier)
	return nil
}

// Transfer moves a device to another account without carrying the audit log.
func (a *DevicePostureAggregator) Transfer(deviceID, toUserID string) error {
	if blank(deviceID) || blank(toUserID) {
		return ErrBlankID
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	dev, ok := a.devices[deviceID]
	if !ok {
		return ErrDeviceNotFound
	}
	if dev.owner == toUserID {
		return ErrSameOwner
	}
	if a.maxDev > 0 && a.countDevices(toUserID) >= a.maxDev {
		return ErrDeviceLimit
	}
	oldOwner := dev.owner
	dev.owner = toUserID
	a.counter++
	dev.seq = a.counter
	a.appendAudit(oldOwner, deviceID, AuditTransferred, a.postureLocked(deviceID, dev).Tier)
	return nil
}

// Signals lists the stored signals of one device in canonical kind order.
func (a *DevicePostureAggregator) Signals(deviceID string) []Signal {
	a.mu.Lock()
	defer a.mu.Unlock()
	dev, ok := a.devices[deviceID]
	if !ok {
		return nil
	}
	out := make([]Signal, 0, len(dev.signals))
	for _, kind := range kindOrder {
		if s, ok := dev.signals[kind]; ok {
			out = append(out, s)
		}
	}
	return out
}

// Audit returns the posture events of one account, oldest first.
func (a *DevicePostureAggregator) Audit(userID string) []AuditEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	events := a.audit[userID]
	out := make([]AuditEvent, len(events))
	copy(out, events)
	return out
}

// snapshotVersion tags the serialised layout so foreign payloads are
// rejected.
const snapshotVersion = 1

type snapshotDevice struct {
	ID               string   `json:"id"`
	Owner            string   `json:"owner"`
	Signals          []Signal `json:"signals"`
	ManualQuarantine bool     `json:"manual_quarantine"`
	QuarantineReason string   `json:"quarantine_reason"`
	Seq              int64    `json:"seq"`
}

type snapshot struct {
	Version int              `json:"version"`
	Counter int64            `json:"counter"`
	Devices []snapshotDevice `json:"devices"`
	Audit   []AuditEvent     `json:"audit"`
}

// Snapshot serialises devices, signals, quarantine flags, audit events and the
// monotonic counters.
func (a *DevicePostureAggregator) Snapshot() ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	snap := snapshot{Version: snapshotVersion, Counter: a.counter}
	for id, dev := range a.devices {
		sd := snapshotDevice{
			ID:               id,
			Owner:            dev.owner,
			ManualQuarantine: dev.manualQuarantine,
			QuarantineReason: dev.quarantineReason,
			Seq:              dev.seq,
		}
		for _, kind := range kindOrder {
			if s, ok := dev.signals[kind]; ok {
				sd.Signals = append(sd.Signals, s)
			}
		}
		snap.Devices = append(snap.Devices, sd)
	}
	sort.Slice(snap.Devices, func(i, j int) bool { return snap.Devices[i].ID < snap.Devices[j].ID })
	users := make([]string, 0, len(a.audit))
	for userID := range a.audit {
		users = append(users, userID)
	}
	sort.Strings(users)
	for _, userID := range users {
		snap.Audit = append(snap.Audit, a.audit[userID]...)
	}
	return json.Marshal(snap)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (a *DevicePostureAggregator) Restore(data []byte) error {
	var snap snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return ErrBadSnapshot
	}
	if snap.Version != snapshotVersion {
		return ErrBadSnapshot
	}
	devices := make(map[string]*deviceState, len(snap.Devices))
	audit := make(map[string][]AuditEvent)
	for _, sd := range snap.Devices {
		if blank(sd.ID) || blank(sd.Owner) {
			return ErrBadSnapshot
		}
		if _, dup := devices[sd.ID]; dup {
			return ErrBadSnapshot
		}
		dev := &deviceState{
			owner:            sd.Owner,
			signals:          make(map[string]Signal, len(sd.Signals)),
			manualQuarantine: sd.ManualQuarantine,
			quarantineReason: sd.QuarantineReason,
			seq:              sd.Seq,
		}
		for _, s := range sd.Signals {
			if !knownKind(s.Kind) {
				return ErrBadSnapshot
			}
			dev.signals[s.Kind] = s
		}
		devices[sd.ID] = dev
	}
	for _, ev := range snap.Audit {
		if blank(ev.UserID) {
			return ErrBadSnapshot
		}
		audit[ev.UserID] = append(audit[ev.UserID], ev)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.devices = devices
	a.audit = audit
	a.counter = snap.Counter
	return nil
}
