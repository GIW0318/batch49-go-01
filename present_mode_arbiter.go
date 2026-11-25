// Package task arbitrates the display present mode from recent frame pacing.
package task

import (
	"errors"
	"sync"
	"time"
)

var errNotImplemented = errors.New("not implemented")

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
	mu        sync.Mutex
	cfg       ArbiterConfig
	current   PresentMode
	window    []time.Duration
	stats     PresentStats
	streak    int
	residence int
}

// NewPresentModeArbiter builds an arbiter with the given policy.
func NewPresentModeArbiter(cfg ArbiterConfig) *PresentModeArbiter {
	return &PresentModeArbiter{cfg: cfg, current: ModeSync}
}

// ReportFrame advances the window with one frame and maybe switches mode.
func (a *PresentModeArbiter) ReportFrame(d FrameTiming) error {
	return errNotImplemented
}

// RequestMode asks for one supported present mode explicitly.
func (a *PresentModeArbiter) RequestMode(m PresentMode) error {
	return errNotImplemented
}

// Current returns the active present mode.
func (a *PresentModeArbiter) Current() PresentMode {
	return ""
}

// Stats returns a copy of the counters.
func (a *PresentModeArbiter) Stats() PresentStats {
	return PresentStats{}
}

// SetVRRRange changes the variable refresh interval.
func (a *PresentModeArbiter) SetVRRRange(lo, hi time.Duration) error {
	return errNotImplemented
}

// SetHysteresis changes how many consecutive frames a switch needs.
func (a *PresentModeArbiter) SetHysteresis(n int) error {
	return errNotImplemented
}

// SupportedModes lists the modes the arbiter may select.
func (a *PresentModeArbiter) SupportedModes() []PresentMode {
	return nil
}

// Reset clears the window, the counters and returns to sync present.
func (a *PresentModeArbiter) Reset() error {
	return errNotImplemented
}

// Snapshot serialises the window, the mode and the counters.
func (a *PresentModeArbiter) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the state with a snapshot, all or nothing.
func (a *PresentModeArbiter) Restore(data []byte) error {
	return errNotImplemented
}
