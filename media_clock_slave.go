// Package task keeps a local media clock slaved to a reference clock through
// periodic sync samples, with slew correction and holdover.
package task

import (
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"
)

// errNotImplemented is retained for compatibility with the scaffold.
var errNotImplemented = errors.New("not implemented")

// MediaTime is a media timeline position in nanoseconds.
type MediaTime int64

// SyncSample is one round trip measurement taken against the reference clock.
type SyncSample struct {
	Seq        int64
	MasterTime MediaTime
	LocalRecv  time.Duration
	RTT        time.Duration
}

// Config bounds the servo.
type Config struct {
	WindowSize    int
	MaxRTT        time.Duration
	StepThreshold time.Duration
	SlewLimitPPM  int
	HoldoverAfter time.Duration
}

// Defaults used when the matching config field is zero.
const (
	DefaultWindowSize    = 8
	DefaultMaxRTT        = 500 * time.Millisecond
	DefaultStepThreshold = 50 * time.Millisecond
	DefaultSlewLimitPPM  = 500
	DefaultHoldoverAfter = 2 * time.Second
	MaxSlewLimitPPM      = 2000
)

// Errors reported by the servo.
var (
	ErrBadSample   = errors.New("sync sample is not acceptable")
	ErrStaleSample = errors.New("sync sample timestamp went backwards")
	ErrOutlier     = errors.New("sync sample round trip is an outlier")
	ErrSlewRange   = errors.New("slew limit out of range")
	ErrBadAdvance  = errors.New("advance must be positive")
	ErrBadSnapshot = errors.New("snapshot payload is invalid")
)

// windowEntry is one accepted measurement kept for the medians.
type windowEntry struct {
	Offset time.Duration `json:"o"`
	RTT    time.Duration `json:"r"`
}

// snapshotData is the versioned, self-contained serialised servo state.
type snapshotData struct {
	Magic         string        `json:"magic"`
	Version       int           `json:"version"`
	Pos           int64         `json:"pos"`
	Offset        int64         `json:"offset"`
	Target        int64         `json:"target"`
	SlewPPM       int           `json:"slew_ppm"`
	Steps         int           `json:"steps"`
	Rejected      int           `json:"rejected"`
	Idle          int64         `json:"idle"`
	Holdover      bool          `json:"holdover"`
	HasSample     bool          `json:"has_sample"`
	LastMaster    int64         `json:"last_master"`
	HasLastMaster bool          `json:"has_last_master"`
	LastLocalRecv int64         `json:"last_local_recv"`
	Window        []windowEntry `json:"window"`

	WindowSize    int   `json:"window_size"`
	MaxRTT        int64 `json:"max_rtt"`
	StepThreshold int64 `json:"step_threshold"`
	SlewLimitPPM  int   `json:"slew_limit_ppm"`
	HoldoverAfter int64 `json:"holdover_after"`
}

const snapshotMagic = "media-clock-slave-v1"

// MediaClockSlave is the local clock and its servo state.
type MediaClockSlave struct {
	mu sync.Mutex

	cfg Config

	pos    time.Duration // raw locally advanced position
	offset time.Duration // currently applied correction
	target time.Duration // median target offset
	slew   int           // signed active correction in ppm

	steps    int
	rejected int

	window []windowEntry

	lastMaster    MediaTime
	hasLastMaster bool
	hasSample     bool
	lastLocalRecv time.Duration

	idle     time.Duration // advance accumulated since the last accepted sample
	holdover bool
}

// NewMediaClockSlave builds a slave servo with the given bounds.
func NewMediaClockSlave(cfg Config) *MediaClockSlave {
	if cfg.WindowSize <= 0 {
		cfg.WindowSize = DefaultWindowSize
	}
	if cfg.MaxRTT <= 0 {
		cfg.MaxRTT = DefaultMaxRTT
	}
	if cfg.StepThreshold <= 0 {
		cfg.StepThreshold = DefaultStepThreshold
	}
	if cfg.HoldoverAfter <= 0 {
		cfg.HoldoverAfter = DefaultHoldoverAfter
	}
	if cfg.SlewLimitPPM == 0 {
		cfg.SlewLimitPPM = DefaultSlewLimitPPM
	}
	if cfg.SlewLimitPPM > MaxSlewLimitPPM {
		cfg.SlewLimitPPM = MaxSlewLimitPPM
	}
	if cfg.SlewLimitPPM < 0 {
		cfg.SlewLimitPPM = 1
	}
	return &MediaClockSlave{cfg: cfg}
}

// medianDuration returns the median of a copy of v. For an even count the two
// middle values are averaged.
func medianDuration(v []time.Duration) time.Duration {
	if len(v) == 0 {
		return 0
	}
	sorted := make([]time.Duration, len(v))
	copy(sorted, v)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

func (m *MediaClockSlave) medianOffset() time.Duration {
	values := make([]time.Duration, len(m.window))
	for i, e := range m.window {
		values[i] = e.Offset
	}
	return medianDuration(values)
}

func (m *MediaClockSlave) medianRTT() time.Duration {
	values := make([]time.Duration, len(m.window))
	for i, e := range m.window {
		values[i] = e.RTT
	}
	return medianDuration(values)
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// PushSample feeds one round trip measurement into the offset window.
func (m *MediaClockSlave) PushSample(s SyncSample) error {
	if s.RTT < 0 || s.RTT > m.cfg.MaxRTT || s.LocalRecv < 0 || s.MasterTime <= 0 {
		return ErrBadSample
	}

	midpoint := s.LocalRecv - s.RTT/2
	off := time.Duration(s.MasterTime) - midpoint

	m.mu.Lock()
	defer m.mu.Unlock()

	prevNow := m.pos + m.offset

	// A master timestamp that moved backwards relative to the last accepted
	// sample is discarded wholesale.
	if m.hasLastMaster && s.MasterTime < m.lastMaster {
		return ErrStaleSample
	}

	// Outliers are judged against the RTT median of the samples already in the
	// window; the candidate is not part of that median.
	if len(m.window) >= 3 {
		rttMedian := m.medianRTT()
		if s.RTT > 3*rttMedian {
			m.rejected++
			return ErrOutlier
		}
	}

	first := !m.hasSample

	m.window = append(m.window, windowEntry{Offset: off, RTT: s.RTT})
	if len(m.window) > m.cfg.WindowSize {
		excess := len(m.window) - m.cfg.WindowSize
		m.window = append([]windowEntry(nil), m.window[excess:]...)
	}

	m.lastMaster = s.MasterTime
	m.hasLastMaster = true
	m.hasSample = true
	m.lastLocalRecv = s.LocalRecv
	m.idle = 0
	m.holdover = false

	m.target = m.medianOffset()

	if first {
		// The first trustworthy measurement establishes the offset directly
		// without a step or a slew.
		m.offset = m.target
		m.slew = 0
		// Establishing a negative offset must never rewind the timeline: raise
		// the raw position just enough to preserve the current reading.
		if now := m.pos + m.offset; now < prevNow {
			m.pos = prevNow - m.offset
		}
		return nil
	}

	delta := m.target - m.offset
	if absDuration(delta) <= m.cfg.StepThreshold {
		switch {
		case delta == 0:
			m.slew = 0
		case delta > 0:
			m.slew = m.cfg.SlewLimitPPM
		default:
			m.slew = -m.cfg.SlewLimitPPM
		}
		return nil
	}

	if delta > 0 {
		// Forward correction is safe: step straight to the target.
		m.offset = m.target
		m.steps++
		m.slew = 0
	} else {
		// A backward correction would rewind the timeline; never step it,
		// slew negatively and chase the target gradually.
		m.slew = -m.cfg.SlewLimitPPM
	}
	return nil
}

// Now returns the current media time.
func (m *MediaClockSlave) Now() MediaTime {
	m.mu.Lock()
	defer m.mu.Unlock()
	return MediaTime(m.pos + m.offset)
}

// Offset returns the applied offset and whether it is known yet.
func (m *MediaClockSlave) Offset() (time.Duration, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.offset, m.hasSample
}

// SetSlewLimit changes the maximum rate correction in ppm.
func (m *MediaClockSlave) SetSlewLimit(ppm int) error {
	if ppm < 1 || ppm > MaxSlewLimitPPM {
		return ErrSlewRange
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg.SlewLimitPPM = ppm
	// A tightening clamps the correction currently taking effect immediately.
	if m.slew > ppm {
		m.slew = ppm
	} else if m.slew < -ppm {
		m.slew = -ppm
	}
	return nil
}

// Holdover reports whether the reference clock has been silent too long.
func (m *MediaClockSlave) Holdover() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.holdover
}

// Degraded reports whether the clock runs on its own estimate.
func (m *MediaClockSlave) Degraded() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.holdover
}

// Advance moves the local clock forward by d.
func (m *MediaClockSlave) Advance(d time.Duration) error {
	if d <= 0 {
		return ErrBadAdvance
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// A settled servo has no rate correction running. When silence first
	// crosses the holdover threshold, establish the free-run extrapolation
	// basis from the latest receive timestamp plus the elapsed silence. The
	// basis is only ever moved forward, so the timeline cannot rewind.
	settled := m.slew == 0 && m.offset == m.target
	wasHoldover := m.holdover

	m.pos += d

	if m.slew != 0 {
		// Compensation is d * ppm / 1e6; multiply before dividing so small
		// corrections are not truncated away.
		comp := time.Duration(int64(d) * int64(m.slew) / 1_000_000)
		next := m.offset + comp
		switch {
		case m.slew > 0 && next >= m.target:
			m.offset = m.target
			m.slew = 0
		case m.slew < 0 && next <= m.target:
			m.offset = m.target
			m.slew = 0
		default:
			m.offset = next
		}
	}

	if m.hasSample {
		m.idle += d
		if !m.holdover && m.idle > m.cfg.HoldoverAfter {
			m.holdover = true
		}
		if m.holdover && !wasHoldover && settled {
			anchor := m.lastLocalRecv + m.idle
			if anchor > m.pos {
				m.pos = anchor
			}
		}
	}

	return nil
}

// SampleCount returns how many samples are in the offset window.
func (m *MediaClockSlave) SampleCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.window)
}

// Rejected returns how many samples were dropped as outliers.
func (m *MediaClockSlave) Rejected() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rejected
}

// Steps returns how many forward steps were applied.
func (m *MediaClockSlave) Steps() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.steps
}

// Slew returns the applied rate correction in ppm.
func (m *MediaClockSlave) Slew() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.slew
}

// Snapshot serialises the clock position and the servo state.
func (m *MediaClockSlave) Snapshot() ([]byte, error) {
	m.mu.Lock()
	d := snapshotData{
		Magic:         snapshotMagic,
		Version:       1,
		Pos:           int64(m.pos),
		Offset:        int64(m.offset),
		Target:        int64(m.target),
		SlewPPM:       m.slew,
		Steps:         m.steps,
		Rejected:      m.rejected,
		Idle:          int64(m.idle),
		Holdover:      m.holdover,
		HasSample:     m.hasSample,
		LastMaster:    int64(m.lastMaster),
		HasLastMaster: m.hasLastMaster,
		LastLocalRecv: int64(m.lastLocalRecv),
		Window:        append([]windowEntry(nil), m.window...),
		WindowSize:    m.cfg.WindowSize,
		MaxRTT:        int64(m.cfg.MaxRTT),
		StepThreshold: int64(m.cfg.StepThreshold),
		SlewLimitPPM:  m.cfg.SlewLimitPPM,
		HoldoverAfter: int64(m.cfg.HoldoverAfter),
	}
	m.mu.Unlock()

	return json.Marshal(&d)
}

// Restore replaces the whole servo state with a snapshot, all or nothing.
func (m *MediaClockSlave) Restore(data []byte) error {
	var d snapshotData
	if err := json.Unmarshal(data, &d); err != nil {
		return ErrBadSnapshot
	}
	if err := d.validate(); err != nil {
		return ErrBadSnapshot
	}

	cfg := Config{
		WindowSize:    d.WindowSize,
		MaxRTT:        time.Duration(d.MaxRTT),
		StepThreshold: time.Duration(d.StepThreshold),
		SlewLimitPPM:  d.SlewLimitPPM,
		HoldoverAfter: time.Duration(d.HoldoverAfter),
	}
	window := append([]windowEntry(nil), d.Window...)

	m.mu.Lock()
	m.cfg = cfg
	m.pos = time.Duration(d.Pos)
	m.offset = time.Duration(d.Offset)
	m.target = time.Duration(d.Target)
	m.slew = d.SlewPPM
	m.steps = d.Steps
	m.rejected = d.Rejected
	m.idle = time.Duration(d.Idle)
	m.holdover = d.Holdover
	m.hasSample = d.HasSample
	m.lastMaster = MediaTime(d.LastMaster)
	m.hasLastMaster = d.HasLastMaster
	m.lastLocalRecv = time.Duration(d.LastLocalRecv)
	m.window = window
	m.mu.Unlock()
	return nil
}

func (d *snapshotData) validate() error {
	if d.Magic != snapshotMagic || d.Version != 1 {
		return ErrBadSnapshot
	}
	if d.WindowSize <= 0 || d.SlewLimitPPM < 1 || d.SlewLimitPPM > MaxSlewLimitPPM {
		return ErrBadSnapshot
	}
	if d.Steps < 0 || d.Rejected < 0 || d.Idle < 0 {
		return ErrBadSnapshot
	}
	if d.MaxRTT < 0 || d.StepThreshold < 0 || d.HoldoverAfter < 0 {
		return ErrBadSnapshot
	}
	if d.SlewPPM > d.SlewLimitPPM || d.SlewPPM < -d.SlewLimitPPM {
		return ErrBadSnapshot
	}
	if len(d.Window) > d.WindowSize {
		return ErrBadSnapshot
	}
	return nil
}
