// Package task arbitrates the display present mode from recent frame pacing.
package task

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"sort"
	"sync"
	"time"
)

// PresentMode is one swap chain present mode.
type PresentMode string

// Supported present modes.
const (
	ModeSync      PresentMode = "sync"
	ModeMailbox   PresentMode = "mailbox"
	ModeImmediate PresentMode = "immediate"
)

// Arbiter defaults used when a configuration field is zero.
const (
	DefaultRefreshInterval = 16 * time.Millisecond
	DefaultVRRMin          = 8 * time.Millisecond
	DefaultVRRMax          = 34 * time.Millisecond
	DefaultWindow          = 30
	DefaultPercentile      = 95
	DefaultHysteresis      = 3
)

// Errors returned by the arbiter.
var (
	ErrBadFrame      = errors.New("frame timing is invalid")
	ErrUnknownMode   = errors.New("present mode is unknown")
	ErrBadVRRRange   = errors.New("vrr range is invalid")
	ErrBadHysteresis = errors.New("hysteresis is invalid")
	ErrBadSnapshot   = errors.New("snapshot payload is invalid")
)

// FrameTiming is one presented frame report.
type FrameTiming struct {
	Frame   time.Duration
	Focused bool
}

// ArbiterConfig tunes the window and the switching policy.
type ArbiterConfig struct {
	RefreshInterval time.Duration
	VRRMin          time.Duration
	VRRMax          time.Duration
	Window          int
	Percentile      int
	Hysteresis      int
	MinResidence    int
	Modes           []PresentMode
}

// PresentStats reports the counters of the arbiter.
type PresentStats struct {
	Frames     int
	Tears      int
	Switches   int
	Hysteresis int
	Percentile time.Duration
}

// PresentModeArbiter chooses the present mode from a sliding window.
type PresentModeArbiter struct {
	mu      sync.Mutex
	cfg     ArbiterConfig
	modes   []PresentMode
	allowed map[PresentMode]bool
	current PresentMode
	window  []time.Duration
	stats   PresentStats

	// streak is the number of consecutive frames whose candidate mode has
	// stayed the same. candidate is that mode. A switch frame resets both.
	streak    int
	candidate PresentMode

	// residence is the number of focused, non-switching frames since the
	// last actual switch. hasSwitched makes the first switch exempt from the
	// minimum-residence restriction.
	residence   int
	hasSwitched bool
}

// NewPresentModeArbiter builds an arbiter with the given policy.
func NewPresentModeArbiter(cfg ArbiterConfig) *PresentModeArbiter {
	cfg = normalizeConfig(cfg)
	modes := normalizeModes(cfg.Modes)
	allowed := make(map[PresentMode]bool, len(modes))
	for _, m := range modes {
		allowed[m] = true
	}
	return &PresentModeArbiter{
		cfg:     cfg,
		modes:   modes,
		allowed: allowed,
		current: ModeSync,
	}
}

func normalizeConfig(cfg ArbiterConfig) ArbiterConfig {
	if cfg.RefreshInterval <= 0 {
		cfg.RefreshInterval = DefaultRefreshInterval
	}
	if cfg.VRRMin <= 0 {
		cfg.VRRMin = DefaultVRRMin
	}
	if cfg.VRRMax <= 0 {
		cfg.VRRMax = DefaultVRRMax
	}
	if cfg.Window <= 0 {
		cfg.Window = DefaultWindow
	}
	if cfg.Percentile <= 0 {
		cfg.Percentile = DefaultPercentile
	}
	if cfg.Percentile > 100 {
		cfg.Percentile = 100
	}
	if cfg.Hysteresis <= 0 {
		cfg.Hysteresis = DefaultHysteresis
	}
	if cfg.MinResidence < 0 {
		cfg.MinResidence = 0
	}
	return cfg
}

// normalizeModes returns the supported modes in the fixed policy order.
// A nil/empty configuration means all three modes are supported. The sync
// mode is always retained because it is the mandatory safe fallback.
func normalizeModes(configured []PresentMode) []PresentMode {
	allowed := map[PresentMode]bool{ModeSync: true}
	if len(configured) == 0 {
		allowed[ModeMailbox] = true
		allowed[ModeImmediate] = true
	} else {
		for _, m := range configured {
			switch m {
			case ModeSync, ModeMailbox, ModeImmediate:
				allowed[m] = true
			}
		}
	}
	modes := make([]PresentMode, 0, len(allowed))
	for _, m := range []PresentMode{ModeSync, ModeMailbox, ModeImmediate} {
		if allowed[m] {
			modes = append(modes, m)
		}
	}
	return modes
}

// ReportFrame advances the window with one frame and maybe switches mode.
func (a *PresentModeArbiter) ReportFrame(d FrameTiming) error {
	if d.Frame <= 0 {
		return ErrBadFrame
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	// Losing focus forces sync present immediately. It is an operational
	// event, not a frame sample: do not add it to the window or Frames/Tears.
	if !d.Focused {
		if a.current != ModeSync {
			a.current = ModeSync
			a.stats.Switches++
			a.streak = 0
			a.candidate = ""
			a.stats.Hysteresis = 0
			a.residence = 0
			a.hasSwitched = true
		}
		return nil
	}

	a.appendFrameLocked(d.Frame)
	percentile := windowPercentile(a.window, a.cfg.Percentile)
	a.stats.Percentile = percentile
	target := a.targetModeLocked(percentile)

	if !a.allowed[target] {
		// An unavailable automatic target must not be silently replaced by
		// a different mode. Keep the current mode and clear the candidate.
		a.streak = 0
		a.candidate = ""
		a.stats.Hysteresis = 0
		a.countFrameLocked(d.Frame)
		return nil
	}

	if target == a.current {
		a.streak = 0
		a.candidate = ""
		a.stats.Hysteresis = 0
		a.countFrameLocked(d.Frame)
		return nil
	}

	// Only consecutive frames proposing the same different mode build up
	// the hysteresis streak. Any change of candidate (including a frame that
	// proposes the active mode) restarts the count.
	if a.streak > 0 && a.candidate == target {
		a.streak++
	} else {
		a.streak = 1
	}
	a.candidate = target
	a.stats.Hysteresis = a.streak

	canSwitch := !a.hasSwitched || a.residence >= a.cfg.MinResidence
	if a.streak >= a.cfg.Hysteresis && canSwitch {
		a.current = target
		a.stats.Switches++
		a.streak = 0
		a.candidate = ""
		a.stats.Hysteresis = 0
		a.residence = 0
		a.hasSwitched = true
		// The switching frame has already entered the window, but it is not
		// counted as a presented frame and cannot produce a tear.
		return nil
	}

	a.countFrameLocked(d.Frame)
	return nil
}

func (a *PresentModeArbiter) appendFrameLocked(frame time.Duration) {
	a.window = append(a.window, frame)
	if len(a.window) <= a.cfg.Window {
		return
	}
	copy(a.window, a.window[len(a.window)-a.cfg.Window:])
	a.window = a.window[:a.cfg.Window]
}

func (a *PresentModeArbiter) countFrameLocked(frame time.Duration) {
	a.residence++
	a.stats.Frames++
	if a.current != ModeSync && frame > a.cfg.RefreshInterval {
		a.stats.Tears++
	}
}

func (a *PresentModeArbiter) targetModeLocked(percentile time.Duration) PresentMode {
	switch {
	case percentile > a.cfg.VRRMax:
		return ModeSync
	case percentile < a.cfg.VRRMin:
		return ModeImmediate
	default:
		return ModeMailbox
	}
}

func windowPercentile(window []time.Duration, percentile int) time.Duration {
	if len(window) == 0 {
		return 0
	}
	values := append([]time.Duration(nil), window...)
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })

	// Nearest-rank percentile: rank = ceil(percentile/100 * n). Integer
	// arithmetic keeps the boundary deterministic for every sample count.
	rank := (percentile*len(values) + 99) / 100
	if rank < 1 {
		rank = 1
	}
	if rank > len(values) {
		rank = len(values)
	}
	return values[rank-1]
}

// RequestMode asks for one supported present mode explicitly.
func (a *PresentModeArbiter) RequestMode(m PresentMode) error {
	if !knownMode(m) {
		return ErrUnknownMode
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.allowed[m] {
		return ErrUnknownMode
	}
	if m != a.current {
		a.current = m
		a.stats.Switches++
		a.residence = 0
		a.hasSwitched = true
	}
	a.streak = 0
	a.candidate = ""
	a.stats.Hysteresis = 0
	return nil
}

func knownMode(m PresentMode) bool {
	switch m {
	case ModeSync, ModeMailbox, ModeImmediate:
		return true
	default:
		return false
	}
}

// Current returns the active present mode.
func (a *PresentModeArbiter) Current() PresentMode {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.current
}

// Stats returns a copy of the counters.
func (a *PresentModeArbiter) Stats() PresentStats {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stats
}

// SetVRRRange changes the variable refresh interval.
func (a *PresentModeArbiter) SetVRRRange(lo, hi time.Duration) error {
	if lo < 0 || lo > hi {
		return ErrBadVRRRange
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cfg.VRRMin = lo
	a.cfg.VRRMax = hi
	return nil
}

// SetHysteresis changes how many consecutive frames a switch needs.
func (a *PresentModeArbiter) SetHysteresis(n int) error {
	if n < 1 {
		return ErrBadHysteresis
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cfg.Hysteresis = n
	return nil
}

// SupportedModes lists the modes the arbiter may select.
func (a *PresentModeArbiter) SupportedModes() []PresentMode {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]PresentMode(nil), a.modes...)
}

// Reset clears the window, the counters and returns to sync present.
func (a *PresentModeArbiter) Reset() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.current = ModeSync
	a.window = nil
	a.stats = PresentStats{}
	a.streak = 0
	a.candidate = ""
	a.residence = 0
	a.hasSwitched = false
	return nil
}

const (
	snapshotMagic   = "PMA1"
	snapshotVersion = 1
)

// snapshotPayload is deliberately explicit: durations are stored as
// nanoseconds so a snapshot does not depend on the platform's Duration
// representation.
type snapshotPayload struct {
	Version     int           `json:"version"`
	Mode        PresentMode   `json:"mode"`
	Window      []int64       `json:"window"`
	Frames      int           `json:"frames"`
	Tears       int           `json:"tears"`
	Switches    int           `json:"switches"`
	Streak      int           `json:"streak"`
	Candidate   PresentMode   `json:"candidate"`
	Residence   int           `json:"residence"`
	HasSwitched bool          `json:"has_switched"`
	Percentile  time.Duration `json:"percentile"`
}

// Snapshot serialises the window, the mode and the counters.
func (a *PresentModeArbiter) Snapshot() ([]byte, error) {
	a.mu.Lock()
	payload := snapshotPayload{
		Version:     snapshotVersion,
		Mode:        a.current,
		Window:      make([]int64, len(a.window)),
		Frames:      a.stats.Frames,
		Tears:       a.stats.Tears,
		Switches:    a.stats.Switches,
		Streak:      a.streak,
		Candidate:   a.candidate,
		Residence:   a.residence,
		HasSwitched: a.hasSwitched,
		Percentile:  a.stats.Percentile,
	}
	for i, d := range a.window {
		payload.Window[i] = int64(d)
	}
	a.mu.Unlock()

	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, ErrBadSnapshot
	}

	out := make([]byte, 0, len(snapshotMagic)+8+len(raw))
	out = append(out, snapshotMagic...)
	var word [4]byte
	binary.BigEndian.PutUint32(word[:], uint32(len(raw)))
	out = append(out, word[:]...)
	out = append(out, raw...)
	binary.BigEndian.PutUint32(word[:], crc32.ChecksumIEEE(raw))
	out = append(out, word[:]...)
	return out, nil
}

// Restore replaces the state with a snapshot, all or nothing.
func (a *PresentModeArbiter) Restore(data []byte) error {
	if len(data) < len(snapshotMagic)+8 || string(data[:len(snapshotMagic)]) != snapshotMagic {
		return ErrBadSnapshot
	}
	payloadLen := int(binary.BigEndian.Uint32(data[len(snapshotMagic) : len(snapshotMagic)+4]))
	payloadStart := len(snapshotMagic) + 4
	payloadEnd := payloadStart + payloadLen
	if payloadLen < 0 || payloadEnd+4 != len(data) {
		return ErrBadSnapshot
	}
	raw := data[payloadStart:payloadEnd]
	wantCRC := binary.BigEndian.Uint32(data[payloadEnd:])
	if crc32.ChecksumIEEE(raw) != wantCRC {
		return ErrBadSnapshot
	}

	var payload snapshotPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ErrBadSnapshot
	}
	if payload.Version != snapshotVersion || !knownMode(payload.Mode) {
		return ErrBadSnapshot
	}
	for _, d := range payload.Window {
		if d <= 0 {
			return ErrBadSnapshot
		}
	}
	if payload.Frames < 0 || payload.Tears < 0 || payload.Switches < 0 ||
		payload.Streak < 0 || payload.Residence < 0 || payload.Percentile < 0 {
		return ErrBadSnapshot
	}
	if payload.Streak == 0 {
		if payload.Candidate != "" {
			return ErrBadSnapshot
		}
	} else if !knownMode(payload.Candidate) || payload.Candidate == payload.Mode {
		return ErrBadSnapshot
	}

	// Decode and validate before taking the lock and changing anything, so a
	// corrupt payload leaves the live arbiter untouched.
	a.mu.Lock()
	defer a.mu.Unlock()

	window := make([]time.Duration, len(payload.Window))
	for i, d := range payload.Window {
		window[i] = time.Duration(d)
	}
	if len(window) > a.cfg.Window {
		window = append([]time.Duration(nil), window[len(window)-a.cfg.Window:]...)
	}

	a.current = payload.Mode
	a.window = window
	a.stats = PresentStats{
		Frames:     payload.Frames,
		Tears:      payload.Tears,
		Switches:   payload.Switches,
		Hysteresis: payload.Streak,
		Percentile: windowPercentile(window, a.cfg.Percentile),
	}
	a.streak = payload.Streak
	a.candidate = payload.Candidate
	a.residence = payload.Residence
	a.hasSwitched = payload.HasSwitched
	return nil
}
