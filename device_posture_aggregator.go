// Package task aggregates the endpoint posture signals of managed devices into
// a compliance tier per device and per user.
package task

import (
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// errNotImplemented is returned by behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// DisableNewLogicEnv is the operational kill switch. Set it to "1" to restore
// the original not-implemented API behaviour.
const DisableNewLogicEnv = "DEVICE_POSTURE_DISABLE_NEW_LOGIC"

var legacyNewLogicDisabled = legacySwitchEnabled()

func legacySwitchEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(DisableNewLogicEnv))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

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
	CompliantMinScore  = 80
	DegradedMinScore   = 40
	staleWeightPercent = 40
	snapshotVersion    = 1
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
	opts         Options
	mu           sync.RWMutex
	now          func() time.Time
	weights      map[string]int
	freshness    map[string]time.Duration
	maxSignals   int
	maxDevices   int
	counter      int64
	devices      map[string]*deviceState
	ownerDevices map[string]map[string]struct{}
	audit        map[string][]AuditEvent
}

type deviceState struct {
	owner            string
	signals          map[string]Signal
	manualQuarantine bool
	quarantineReason string
	seq              int64
}

var kindOrder = []string{
	SignalEDR,
	SignalDiskCrypto,
	SignalPatch,
	SignalJailbreak,
}

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
	a := &DevicePostureAggregator{opts: opts}
	if legacyNewLogicDisabled {
		return a
	}

	a.now = opts.Now
	if a.now == nil {
		a.now = time.Now
	}
	a.weights = make(map[string]int, len(defaultWeights)+len(opts.Weights))
	for kind, weight := range defaultWeights {
		a.weights[kind] = weight
	}
	for kind, weight := range opts.Weights {
		a.weights[kind] = weight
	}

	a.freshness = make(map[string]time.Duration, len(defaultFreshness)+len(opts.Freshness))
	for kind, window := range defaultFreshness {
		a.freshness[kind] = window
	}
	for kind, window := range opts.Freshness {
		a.freshness[kind] = window
	}

	a.maxSignals = opts.MaxSignalsPerDevice
	a.maxDevices = opts.MaxDevicesPerUser
	a.devices = make(map[string]*deviceState)
	a.ownerDevices = make(map[string]map[string]struct{})
	a.audit = make(map[string][]AuditEvent)
	return a
}

func isBlank(value string) bool {
	return strings.TrimSpace(value) == ""
}

func knownKind(kind string) bool {
	switch kind {
	case SignalEDR, SignalDiskCrypto, SignalPatch, SignalJailbreak:
		return true
	default:
		return false
	}
}

func knownAuditAction(action string) bool {
	switch action {
	case AuditReported, AuditQuarantined, AuditReleased, AuditTransferred:
		return true
	default:
		return false
	}
}

func knownTier(tier string) bool {
	switch tier {
	case TierCompliant, TierDegraded, TierQuarantined:
		return true
	default:
		return false
	}
}

func signalIsNewer(incoming, existing Signal) bool {
	if !incoming.At.Equal(existing.At) {
		return incoming.At.After(existing.At)
	}
	return incoming.Seq > existing.Seq
}

func hardSignalLocked(dev *deviceState) bool {
	if signal, ok := dev.signals[SignalJailbreak]; ok && signal.Score > 0 {
		return true
	}
	if signal, ok := dev.signals[SignalEDR]; ok && signal.State == EDRRemoved {
		return true
	}
	return false
}

func (a *DevicePostureAggregator) postureLocked(deviceID string, dev *deviceState) Posture {
	now := a.now()
	score := 0
	stale := make([]string, 0)

	for _, kind := range kindOrder {
		signal, ok := dev.signals[kind]
		if !ok {
			continue
		}

		weight := a.weights[kind]
		contribution := weight * signal.Score / 100
		if window := a.freshness[kind]; window > 0 && now.Sub(signal.At) > window {
			contribution = contribution * staleWeightPercent / 100
			stale = append(stale, kind)
		}
		score += contribution
	}

	quarantined := dev.manualQuarantine || hardSignalLocked(dev) || score < DegradedMinScore
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

func (a *DevicePostureAggregator) nextVersionLocked(dev *deviceState) {
	a.counter++
	dev.seq = a.counter
}

func (a *DevicePostureAggregator) appendAuditLocked(userID, deviceID, action string, posture Posture) {
	a.audit[userID] = append(a.audit[userID], AuditEvent{
		UserID:   userID,
		DeviceID: deviceID,
		Action:   action,
		Tier:     posture.Tier,
		Seq:      a.counter,
		At:       a.now(),
	})
}

// Report records one signal for a device, binding it to the reporting account
// on first sight.
func (a *DevicePostureAggregator) Report(deviceID string, s Signal) error {
	if legacyNewLogicDisabled {
		return errNotImplemented
	}
	if isBlank(deviceID) || isBlank(s.UserID) {
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

	dev, exists := a.devices[deviceID]
	if exists && dev.owner != s.UserID {
		return ErrDeviceOwner
	}

	if !exists {
		ownerDevices := a.ownerDevices[s.UserID]
		if a.maxDevices > 0 && len(ownerDevices) >= a.maxDevices {
			return ErrDeviceLimit
		}

		dev = &deviceState{owner: s.UserID, signals: make(map[string]Signal)}
		a.devices[deviceID] = dev
		if ownerDevices == nil {
			ownerDevices = make(map[string]struct{})
			a.ownerDevices[s.UserID] = ownerDevices
		}
		ownerDevices[deviceID] = struct{}{}
	}

	if existing, ok := dev.signals[s.Kind]; ok {
		if !signalIsNewer(s, existing) {
			return ErrStaleSignal
		}
	} else if a.maxSignals > 0 && len(dev.signals) >= a.maxSignals {
		return ErrSignalLimit
	}

	dev.signals[s.Kind] = s
	a.nextVersionLocked(dev)
	a.appendAuditLocked(dev.owner, deviceID, AuditReported, a.postureLocked(deviceID, dev))
	return nil
}

// Posture returns the aggregate view of one device.
func (a *DevicePostureAggregator) Posture(deviceID string) (Posture, error) {
	if legacyNewLogicDisabled {
		return Posture{}, errNotImplemented
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	dev, ok := a.devices[deviceID]
	if !ok {
		return Posture{}, ErrDeviceNotFound
	}
	return a.postureLocked(deviceID, dev), nil
}

// Evaluate returns the postures of every device owned by one account, ordered
// by device id.
func (a *DevicePostureAggregator) Evaluate(userID string) ([]Posture, error) {
	if legacyNewLogicDisabled {
		return nil, errNotImplemented
	}
	if isBlank(userID) {
		return nil, ErrBlankID
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	ids := make([]string, 0, len(a.ownerDevices[userID]))
	for deviceID := range a.ownerDevices[userID] {
		ids = append(ids, deviceID)
	}
	sort.Strings(ids)

	postures := make([]Posture, 0, len(ids))
	for _, deviceID := range ids {
		if dev := a.devices[deviceID]; dev != nil {
			postures = append(postures, a.postureLocked(deviceID, dev))
		}
	}
	return postures, nil
}

// Quarantine pins a device in the quarantined tier until an operator releases
// it.
func (a *DevicePostureAggregator) Quarantine(deviceID, reason string) error {
	if legacyNewLogicDisabled {
		return errNotImplemented
	}
	if isBlank(deviceID) {
		return ErrBlankID
	}
	if isBlank(reason) {
		return ErrBadReason
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	dev, ok := a.devices[deviceID]
	if !ok {
		return ErrDeviceNotFound
	}

	dev.manualQuarantine = true
	dev.quarantineReason = reason
	a.nextVersionLocked(dev)
	a.appendAuditLocked(dev.owner, deviceID, AuditQuarantined, a.postureLocked(deviceID, dev))
	return nil
}

// Release lifts a manual quarantine after an approver signs off.
func (a *DevicePostureAggregator) Release(deviceID, approverID string) error {
	if legacyNewLogicDisabled {
		return errNotImplemented
	}
	if isBlank(deviceID) {
		return ErrBlankID
	}
	if isBlank(approverID) {
		return ErrNoApprover
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	dev, ok := a.devices[deviceID]
	if !ok {
		return ErrDeviceNotFound
	}

	current := a.postureLocked(deviceID, dev)
	if !current.Quarantined {
		return ErrNotQuarantined
	}

	dev.manualQuarantine = false
	dev.quarantineReason = ""
	a.nextVersionLocked(dev)
	a.appendAuditLocked(dev.owner, deviceID, AuditReleased, a.postureLocked(deviceID, dev))
	return nil
}

// Transfer moves a device to another account without carrying the audit log.
func (a *DevicePostureAggregator) Transfer(deviceID, toUserID string) error {
	if legacyNewLogicDisabled {
		return errNotImplemented
	}
	if isBlank(deviceID) || isBlank(toUserID) {
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

	newOwnerDevices := a.ownerDevices[toUserID]
	if a.maxDevices > 0 && len(newOwnerDevices) >= a.maxDevices {
		return ErrDeviceLimit
	}

	oldOwner := dev.owner
	oldOwnerDevices := a.ownerDevices[oldOwner]
	delete(oldOwnerDevices, deviceID)
	if len(oldOwnerDevices) == 0 {
		delete(a.ownerDevices, oldOwner)
	}
	if newOwnerDevices == nil {
		newOwnerDevices = make(map[string]struct{})
		a.ownerDevices[toUserID] = newOwnerDevices
	}
	newOwnerDevices[deviceID] = struct{}{}
	dev.owner = toUserID

	a.nextVersionLocked(dev)
	a.appendAuditLocked(oldOwner, deviceID, AuditTransferred, a.postureLocked(deviceID, dev))
	return nil
}

// Signals lists the stored signals of one device in canonical kind order.
func (a *DevicePostureAggregator) Signals(deviceID string) []Signal {
	if legacyNewLogicDisabled {
		return nil
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	dev, ok := a.devices[deviceID]
	if !ok {
		return nil
	}

	out := make([]Signal, 0, len(dev.signals))
	for _, kind := range kindOrder {
		if signal, ok := dev.signals[kind]; ok {
			out = append(out, signal)
		}
	}
	return out
}

// Audit returns the posture events of one account, oldest first.
func (a *DevicePostureAggregator) Audit(userID string) []AuditEvent {
	if legacyNewLogicDisabled {
		return nil
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	events := a.audit[userID]
	out := make([]AuditEvent, len(events))
	copy(out, events)
	return out
}

type snapshotDevice struct {
	ID               string   `json:"id"`
	Owner            string   `json:"owner"`
	Signals          []Signal `json:"signals"`
	ManualQuarantine bool     `json:"manual_quarantine"`
	QuarantineReason string   `json:"quarantine_reason"`
	Seq              int64    `json:"seq"`
}

type snapshotData struct {
	Version int                     `json:"version"`
	Counter int64                   `json:"counter"`
	Devices []snapshotDevice        `json:"devices"`
	Audit   map[string][]AuditEvent `json:"audit"`
}

// Snapshot serialises devices, signals, quarantine flags, audit events and the
// monotonic counters.
func (a *DevicePostureAggregator) Snapshot() ([]byte, error) {
	if legacyNewLogicDisabled {
		return nil, errNotImplemented
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	deviceIDs := make([]string, 0, len(a.devices))
	for deviceID := range a.devices {
		deviceIDs = append(deviceIDs, deviceID)
	}
	sort.Strings(deviceIDs)

	snapshot := snapshotData{
		Version: snapshotVersion,
		Counter: a.counter,
		Devices: make([]snapshotDevice, 0, len(deviceIDs)),
		Audit:   make(map[string][]AuditEvent, len(a.audit)),
	}

	for _, deviceID := range deviceIDs {
		dev := a.devices[deviceID]
		item := snapshotDevice{
			ID:               deviceID,
			Owner:            dev.owner,
			Signals:          make([]Signal, 0, len(dev.signals)),
			ManualQuarantine: dev.manualQuarantine,
			QuarantineReason: dev.quarantineReason,
			Seq:              dev.seq,
		}
		for _, kind := range kindOrder {
			if signal, ok := dev.signals[kind]; ok {
				item.Signals = append(item.Signals, signal)
			}
		}
		snapshot.Devices = append(snapshot.Devices, item)
	}

	for userID, events := range a.audit {
		snapshot.Audit[userID] = append([]AuditEvent(nil), events...)
	}

	return json.Marshal(snapshot)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (a *DevicePostureAggregator) Restore(data []byte) error {
	if legacyNewLogicDisabled {
		return errNotImplemented
	}

	var snapshot snapshotData
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return ErrBadSnapshot
	}
	if err := validateSnapshot(snapshot); err != nil {
		return err
	}

	devices := make(map[string]*deviceState, len(snapshot.Devices))
	ownerDevices := make(map[string]map[string]struct{}, len(snapshot.Devices))
	for _, item := range snapshot.Devices {
		signals := make(map[string]Signal, len(item.Signals))
		for _, signal := range item.Signals {
			signals[signal.Kind] = signal
		}

		devices[item.ID] = &deviceState{
			owner:            item.Owner,
			signals:          signals,
			manualQuarantine: item.ManualQuarantine,
			quarantineReason: item.QuarantineReason,
			seq:              item.Seq,
		}

		ownerSet := ownerDevices[item.Owner]
		if ownerSet == nil {
			ownerSet = make(map[string]struct{})
			ownerDevices[item.Owner] = ownerSet
		}
		ownerSet[item.ID] = struct{}{}
	}

	audit := make(map[string][]AuditEvent, len(snapshot.Audit))
	for userID, events := range snapshot.Audit {
		audit[userID] = append(make([]AuditEvent, 0, len(events)), events...)
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	a.counter = snapshot.Counter
	a.devices = devices
	a.ownerDevices = ownerDevices
	a.audit = audit
	return nil
}

func validateSnapshot(snapshot snapshotData) error {
	if snapshot.Version != snapshotVersion || snapshot.Counter < 0 {
		return ErrBadSnapshot
	}

	seenDevices := make(map[string]struct{}, len(snapshot.Devices))
	for _, item := range snapshot.Devices {
		if isBlank(item.ID) || isBlank(item.Owner) || item.Seq < 0 {
			return ErrBadSnapshot
		}
		if item.ManualQuarantine && isBlank(item.QuarantineReason) {
			return ErrBadSnapshot
		}
		if _, duplicated := seenDevices[item.ID]; duplicated {
			return ErrBadSnapshot
		}
		seenDevices[item.ID] = struct{}{}

		seenKinds := make(map[string]struct{}, len(item.Signals))
		for _, signal := range item.Signals {
			if isBlank(signal.UserID) || !knownKind(signal.Kind) {
				return ErrBadSnapshot
			}
			if signal.Score < 0 || signal.Score > 100 {
				return ErrBadSnapshot
			}
			if _, duplicated := seenKinds[signal.Kind]; duplicated {
				return ErrBadSnapshot
			}
			seenKinds[signal.Kind] = struct{}{}
		}
	}

	for userID, events := range snapshot.Audit {
		if isBlank(userID) {
			return ErrBadSnapshot
		}
		for _, event := range events {
			if event.UserID != userID || isBlank(event.DeviceID) {
				return ErrBadSnapshot
			}
			if !knownAuditAction(event.Action) || !knownTier(event.Tier) || event.Seq < 0 {
				return ErrBadSnapshot
			}
		}
	}
	return nil
}
