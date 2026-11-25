// Package task implements the receive side of a VPN tunnel rekey: epoch key
// installation, a sliding replay window per epoch, send counter allocation and
// restart safe snapshots.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

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

// replayWindowBits is the width of the RFC style sliding replay window. A
// counter that lags the window top by replayWindowBits or more is outside the
// representable bitmap and must be treated as too old.
const replayWindowBits = uint64(64)

// snapshotVersion is the only snapshot encoding understood by Restore.
const snapshotVersion = 1

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
// epochs kept for traffic and also the tombstone retention cap.
// MaxFutureEpochs caps how far ahead a new epoch may jump; zero means the jump
// is not limited. MaxBytesPerEpoch caps the accepted payload bytes of one
// epoch.
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

// epochState is the mutable bookkeeping of one installed epoch.
type epochState struct {
	epoch    uint64
	keyID    string
	retired  bool
	top      uint64
	bits     uint64
	accepted uint64
	replays  uint64
	bytes    int64
	sendCtr  uint64
}

// RekeyReplayGuard owns the epoch table, the per epoch replay windows and the
// send counters. A single mutex makes epoch transitions and the check-and-set
// of a replay bit atomic across concurrent goroutines.
type RekeyReplayGuard struct {
	mu  sync.RWMutex
	cfg GuardConfig

	// newest is the strictly increasing high water mark of every installed
	// epoch and equals ActiveEpoch. It survives tombstone eviction so stale
	// epochs can never be reinstalled.
	newest uint64
	epochs map[uint64]*epochState
	// order lists every retained epoch (active and tombstone) ascending.
	order []uint64
}

// NewRekeyReplayGuard validates the configuration and builds an empty guard.
func NewRekeyReplayGuard(cfg GuardConfig) (*RekeyReplayGuard, error) {
	if cfg.MaxEpochs <= 0 {
		return nil, fmt.Errorf("%w: MaxEpochs must be positive", ErrBadConfig)
	}
	if cfg.MaxBytesPerEpoch < 0 {
		return nil, fmt.Errorf("%w: MaxBytesPerEpoch must not be negative", ErrBadConfig)
	}
	return &RekeyReplayGuard{
		cfg:    cfg,
		epochs: make(map[uint64]*epochState),
	}, nil
}

// InstallEpoch installs a new epoch key and retires the oldest active epoch
// when the active table is full. All validation happens before any state
// mutation so a rejected install is an exact no-op.
func (g *RekeyReplayGuard) InstallEpoch(epoch uint64, keyID string) error {
	if epoch == 0 {
		return ErrBadEpoch
	}
	if strings.TrimSpace(keyID) == "" {
		return ErrBadKeyID
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if epoch <= g.newest {
		return ErrEpochStale
	}
	if g.cfg.MaxFutureEpochs > 0 && epoch-g.newest > g.cfg.MaxFutureEpochs {
		return fmt.Errorf("%w: epoch %d is more than %d ahead of %d",
			ErrEpochTooFar, epoch, g.cfg.MaxFutureEpochs, g.newest)
	}

	if g.activeCountLocked() >= g.cfg.MaxEpochs {
		g.retireOldestActiveLocked()
	}

	g.epochs[epoch] = &epochState{epoch: epoch, keyID: keyID}
	// epoch is greater than newest, so appending keeps order ascending.
	g.order = append(g.order, epoch)
	g.newest = epoch

	g.enforceTombstoneCapLocked()
	return nil
}

// ActiveEpoch reports the newest installed epoch, zero when none is installed.
func (g *RekeyReplayGuard) ActiveEpoch() uint64 {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.newest
}

// AcceptPacket classifies one inbound packet against its epoch replay window.
// The whole classification and the resulting bit insertion run under the write
// lock, so concurrent copies of one counter have a single winner.
func (g *RekeyReplayGuard) AcceptPacket(pkt Packet) (Verdict, error) {
	if pkt.Counter == 0 {
		return Verdict{}, ErrPacketInvalid
	}
	if pkt.Bytes < 0 {
		return Verdict{}, ErrPacketInvalid
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	e, ok := g.epochs[pkt.Epoch]
	if !ok {
		return dropVerdict(ReasonEpochUnknown, pkt), nil
	}
	if e.retired {
		return dropVerdict(ReasonEpochRetired, pkt), nil
	}

	// Hard byte budget: a packet that would push the accepted total past the
	// cap is refused before the window or any statistic is touched.
	addedBytes := int64(pkt.Bytes)
	if e.bytes+addedBytes > g.cfg.MaxBytesPerEpoch {
		return dropVerdict(ReasonBudget, pkt), nil
	}

	if pkt.Counter > e.top {
		shift := pkt.Counter - e.top
		e.top = pkt.Counter
		if shift >= replayWindowBits {
			// The jump clears every representable history bit.
			e.bits = 0
		} else {
			e.bits <<= shift
		}
		e.bits |= 1 << 0 // bit 0 tracks the current window top.
	} else {
		lag := e.top - pkt.Counter
		if lag >= replayWindowBits {
			// Outside the bitmap: never move the window or clear bits.
			return dropVerdict(ReasonCounterOld, pkt), nil
		}
		mask := uint64(1) << lag
		if e.bits&mask != 0 {
			e.replays++
			return Verdict{
				Result:  VerdictReplay,
				Epoch:   pkt.Epoch,
				Counter: pkt.Counter,
			}, nil
		}
		e.bits |= mask
	}

	e.accepted++
	e.bytes += addedBytes
	return Verdict{
		Result:  VerdictAccept,
		Epoch:   pkt.Epoch,
		Counter: pkt.Counter,
	}, nil
}

// NextCounter allocates the next send counter of one epoch.
func (g *RekeyReplayGuard) NextCounter(epoch uint64) (uint64, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	e, ok := g.epochs[epoch]
	if !ok {
		return 0, ErrEpochUnknown
	}
	if e.retired {
		return 0, ErrEpochRetired
	}
	e.sendCtr++
	return e.sendCtr, nil
}

// RetireEpoch retires one non active epoch. Retiring an already retired or
// unknown epoch is a no-op.
func (g *RekeyReplayGuard) RetireEpoch(epoch uint64) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	e, ok := g.epochs[epoch]
	if !ok || e.retired {
		return nil
	}
	if epoch == g.newest {
		return ErrActiveEpoch
	}
	e.retired = true
	g.enforceTombstoneCapLocked()
	return nil
}

// WindowTop reports the highest accepted counter of one retained epoch. The
// boolean is false only for epochs that have been evicted or never installed.
func (g *RekeyReplayGuard) WindowTop(epoch uint64) (uint64, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	e, ok := g.epochs[epoch]
	if !ok {
		return 0, false
	}
	return e.top, true
}

// Epochs lists the retained epochs, retired tombstones included, in ascending
// epoch order.
func (g *RekeyReplayGuard) Epochs() []EpochState {
	g.mu.RLock()
	defer g.mu.RUnlock()

	out := make([]EpochState, 0, len(g.order))
	for _, epoch := range g.order {
		e := g.epochs[epoch]
		if e == nil {
			continue
		}
		out = append(out, EpochState{
			Epoch:     e.epoch,
			KeyID:     e.keyID,
			WindowTop: e.top,
			Accepted:  e.accepted,
			Replays:   e.replays,
			Retired:   e.retired,
		})
	}
	return out
}

// ReplayCount aggregates the replay verdicts of one key id across every
// retained epoch bound to that key. Statistics of an evicted tombstone
// disappear together with the epoch record.
func (g *RekeyReplayGuard) ReplayCount(keyID string) uint64 {
	g.mu.RLock()
	defer g.mu.RUnlock()

	var total uint64
	for _, e := range g.epochs {
		if e.keyID == keyID {
			total += e.replays
		}
	}
	return total
}

// AcceptedCount reports how many packets one epoch accepted.
func (g *RekeyReplayGuard) AcceptedCount(epoch uint64) uint64 {
	g.mu.RLock()
	defer g.mu.RUnlock()

	e, ok := g.epochs[epoch]
	if !ok {
		return 0
	}
	return e.accepted
}

// Snapshot serialises the epoch table, the windows, the counters and the
// statistics. The encoded envelope carries a checksum so a damaged payload is
// rejected by Restore instead of producing an amnesiac guard.
func (g *RekeyReplayGuard) Snapshot() ([]byte, error) {
	g.mu.RLock()
	data := snapshotData{
		Newest: g.newest,
		Epochs: make([]snapshotEpoch, 0, len(g.order)),
	}
	for _, epoch := range g.order {
		e := g.epochs[epoch]
		if e == nil {
			continue
		}
		data.Epochs = append(data.Epochs, snapshotEpoch{
			Epoch:    e.epoch,
			KeyID:    e.keyID,
			Retired:  e.retired,
			Top:      e.top,
			Bits:     e.bits,
			Accepted: e.accepted,
			Replays:  e.replays,
			Bytes:    e.bytes,
			SendCtr:  e.sendCtr,
		})
	}
	g.mu.RUnlock()

	raw, err := json.Marshal(&data)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	envelope := snapshotEnvelope{
		Version: snapshotVersion,
		SHA256:  hex.EncodeToString(sum[:]),
		Data:    raw,
	}
	out, err := json.Marshal(&envelope)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Restore replaces the whole state with a snapshot, all or nothing. Decoding
// and validation happen before the guard lock is taken, so a rejected payload
// leaves the current state untouched.
func (g *RekeyReplayGuard) Restore(payload []byte) error {
	data, err := decodeSnapshot(payload)
	if err != nil {
		return err
	}

	restored := make(map[uint64]*epochState, len(data.Epochs))
	order := make([]uint64, 0, len(data.Epochs))
	for _, se := range data.Epochs {
		restored[se.Epoch] = &epochState{
			epoch:    se.Epoch,
			keyID:    se.KeyID,
			retired:  se.Retired,
			top:      se.Top,
			bits:     se.Bits,
			accepted: se.Accepted,
			replays:  se.Replays,
			bytes:    se.Bytes,
			sendCtr:  se.SendCtr,
		}
		order = append(order, se.Epoch)
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })

	g.mu.Lock()
	g.newest = data.Newest
	g.epochs = restored
	g.order = order
	g.mu.Unlock()
	return nil
}

// snapshotEnvelope wraps the versioned payload with an integrity checksum.
type snapshotEnvelope struct {
	Version int             `json:"version"`
	SHA256  string          `json:"sha256"`
	Data    json.RawMessage `json:"data"`
}

// snapshotData is the versioned serialisable guard state.
type snapshotData struct {
	Newest uint64          `json:"newest"`
	Epochs []snapshotEpoch `json:"epochs"`
}

// snapshotEpoch is the serialisable per epoch record.
type snapshotEpoch struct {
	Epoch    uint64 `json:"epoch"`
	KeyID    string `json:"key_id"`
	Retired  bool   `json:"retired"`
	Top      uint64 `json:"top"`
	Bits     uint64 `json:"bits"`
	Accepted uint64 `json:"accepted"`
	Replays  uint64 `json:"replays"`
	Bytes    int64  `json:"bytes"`
	SendCtr  uint64 `json:"send_ctr"`
}

// decodeSnapshot validates the envelope, checksum and semantic consistency and
// returns the decoded state without touching the guard.
func decodeSnapshot(payload []byte) (snapshotData, error) {
	var zero snapshotData
	if len(payload) == 0 {
		return zero, ErrBadSnapshot
	}

	var envelope snapshotEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return zero, fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	if envelope.Version != snapshotVersion || envelope.SHA256 == "" || len(envelope.Data) == 0 {
		return zero, fmt.Errorf("%w: invalid snapshot envelope", ErrBadSnapshot)
	}

	sum := sha256.Sum256(envelope.Data)
	if envelope.SHA256 != hex.EncodeToString(sum[:]) {
		return zero, fmt.Errorf("%w: checksum mismatch", ErrBadSnapshot)
	}

	var data snapshotData
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		return zero, fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}

	seen := make(map[uint64]struct{}, len(data.Epochs))
	var newest uint64
	for _, se := range data.Epochs {
		if se.Epoch == 0 {
			return zero, fmt.Errorf("%w: zero epoch", ErrBadSnapshot)
		}
		if _, dup := seen[se.Epoch]; dup {
			return zero, fmt.Errorf("%w: duplicate epoch %d", ErrBadSnapshot, se.Epoch)
		}
		seen[se.Epoch] = struct{}{}
		if se.Epoch > newest {
			newest = se.Epoch
		}
		if se.Bytes < 0 {
			return zero, fmt.Errorf("%w: negative byte total", ErrBadSnapshot)
		}
	}

	if len(data.Epochs) == 0 {
		if data.Newest != 0 {
			return zero, fmt.Errorf("%w: non zero newest without epochs", ErrBadSnapshot)
		}
		return data, nil
	}
	if data.Newest != newest {
		return zero, fmt.Errorf("%w: newest %d does not match epoch table", ErrBadSnapshot, data.Newest)
	}
	// The newest epoch is always the active one: retiring it is refused.
	var newestRecord *snapshotEpoch
	for i := range data.Epochs {
		if data.Epochs[i].Epoch == newest {
			newestRecord = &data.Epochs[i]
			break
		}
	}
	if newestRecord == nil || newestRecord.Retired {
		return zero, fmt.Errorf("%w: newest epoch must be active", ErrBadSnapshot)
	}
	return data, nil
}

// dropVerdict builds a drop verdict carrying the packet coordinates.
func dropVerdict(reason string, pkt Packet) Verdict {
	return Verdict{
		Result:  VerdictDrop,
		Reason:  reason,
		Epoch:   pkt.Epoch,
		Counter: pkt.Counter,
	}
}

// activeCountLocked returns the number of non retired epochs.
func (g *RekeyReplayGuard) activeCountLocked() int {
	n := 0
	for _, epoch := range g.order {
		if e := g.epochs[epoch]; e != nil && !e.retired {
			n++
		}
	}
	return n
}

// tombstoneCountLocked returns the number of retired tombstones.
func (g *RekeyReplayGuard) tombstoneCountLocked() int {
	n := 0
	for _, epoch := range g.order {
		if e := g.epochs[epoch]; e != nil && e.retired {
			n++
		}
	}
	return n
}

// retireOldestActiveLocked turns the earliest active epoch in ascending order
// into a retired tombstone.
func (g *RekeyReplayGuard) retireOldestActiveLocked() {
	for _, epoch := range g.order {
		if e := g.epochs[epoch]; e != nil && !e.retired {
			e.retired = true
			return
		}
	}
}

// enforceTombstoneCapLocked evicts the oldest tombstones until the retained
// tombstone count is within MaxEpochs.
func (g *RekeyReplayGuard) enforceTombstoneCapLocked() {
	for g.tombstoneCountLocked() > g.cfg.MaxEpochs {
		g.deleteOldestTombstoneLocked()
	}
}

// deleteOldestTombstoneLocked removes the smallest retired epoch together
// with all of its statistics.
func (g *RekeyReplayGuard) deleteOldestTombstoneLocked() {
	for i, epoch := range g.order {
		if e := g.epochs[epoch]; e != nil && e.retired {
			delete(g.epochs, epoch)
			g.order = append(g.order[:i], g.order[i+1:]...)
			return
		}
	}
}
