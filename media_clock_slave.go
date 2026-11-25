// Package task keeps a local media clock slaved to a reference clock through
// periodic sync samples, with slew correction and holdover.
package task

import (
	"errors"
	"time"
)

// errNotImplemented is returned by every behaviour that still needs work.
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

// MediaClockSlave is the local clock and its servo state.
type MediaClockSlave struct {
	cfg Config
}

// NewMediaClockSlave builds a slave servo with the given bounds.
func NewMediaClockSlave(cfg Config) *MediaClockSlave {
	return &MediaClockSlave{cfg: cfg}
}

// PushSample feeds one round trip measurement into the offset window.
func (m *MediaClockSlave) PushSample(s SyncSample) error {
	return errNotImplemented
}

// Now returns the current media time.
func (m *MediaClockSlave) Now() MediaTime {
	return 0
}

// Offset returns the applied offset and whether it is known yet.
func (m *MediaClockSlave) Offset() (time.Duration, bool) {
	return 0, false
}

// SetSlewLimit changes the maximum rate correction in ppm.
func (m *MediaClockSlave) SetSlewLimit(ppm int) error {
	return errNotImplemented
}

// Holdover reports whether the reference clock has been silent too long.
func (m *MediaClockSlave) Holdover() bool {
	return false
}

// Degraded reports whether the clock runs on its own estimate.
func (m *MediaClockSlave) Degraded() bool {
	return false
}

// Advance moves the local clock forward by d.
func (m *MediaClockSlave) Advance(d time.Duration) error {
	return errNotImplemented
}

// SampleCount returns how many samples are in the offset window.
func (m *MediaClockSlave) SampleCount() int {
	return 0
}

// Rejected returns how many samples were dropped as outliers.
func (m *MediaClockSlave) Rejected() int {
	return 0
}

// Steps returns how many forward steps were applied.
func (m *MediaClockSlave) Steps() int {
	return 0
}

// Slew returns the applied rate correction in ppm.
func (m *MediaClockSlave) Slew() int {
	return 0
}

// Snapshot serialises the clock position and the servo state.
func (m *MediaClockSlave) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole servo state with a snapshot, all or nothing.
func (m *MediaClockSlave) Restore(data []byte) error {
	return errNotImplemented
}
