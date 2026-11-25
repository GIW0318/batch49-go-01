// Package task drives a mixer chain block by block, keeping filter state and
// parameter ramps continuous across block boundaries and padding short supply.
package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Parameter names accepted by SetParam.
const (
	ParamCutoff = "cutoff"
	ParamGain   = "gain"
	ParamMix    = "mix"
)

// Hard bounds for the chain configuration.
const (
	MinSampleRate = 8000
	MaxSampleRate = 192000
	MinBlockSize  = 1
	MaxBlockSize  = 4096
)

// Errors returned by the scheduler.
var (
	ErrBadSampleRate = errors.New("sample rate out of range")
	ErrBadBlockSize  = errors.New("block size out of range")
	ErrBadFrame      = errors.New("frame channels must be equal and non-empty")
	ErrFrameTooLong  = errors.New("frame longer than the configured block")
	ErrUnknownParam  = errors.New("unknown parameter")
	ErrOutOfRange    = errors.New("parameter value out of range")
	ErrBadSnapshot   = errors.New("snapshot payload is invalid")
)

// Frame is one interleaved stereo buffer handed to the chain.
type Frame struct {
	Left  []float32
	Right []float32
}

// MixStats is a snapshot of the scheduler counters.
type MixStats struct {
	Blocks       int
	InputFrames  int
	OutputFrames int
	PadFrames    int
	Underruns    int
	SampleRate   int
	BlockSize    int
}

// MixBlockScheduler owns the chain configuration, filter state and counters.
type MixBlockScheduler struct {
	sampleRate int
	blockSize  int
	bypass     bool
	stats      MixStats
}

// NewMixBlockScheduler builds a chain for the given rate and block size.
func NewMixBlockScheduler(sampleRate, blockSize int) (*MixBlockScheduler, error) {
	return nil, errNotImplemented
}

// SubmitBlock processes one input buffer and returns exactly one block.
func (s *MixBlockScheduler) SubmitBlock(in Frame) (Frame, error) {
	return Frame{}, errNotImplemented
}

// SetBlockSize changes the output block size for later submissions.
func (s *MixBlockScheduler) SetBlockSize(n int) error {
	return errNotImplemented
}

// SetSampleRate changes the chain rate and recomputes derived coefficients.
func (s *MixBlockScheduler) SetSampleRate(hz int) error {
	return errNotImplemented
}

// SetParam writes one chain parameter, ramping towards the new value.
func (s *MixBlockScheduler) SetParam(name string, v float64) error {
	return errNotImplemented
}

// Param reads the current value of one chain parameter.
func (s *MixBlockScheduler) Param(name string) float64 {
	return 0
}

// SetBypass enables or disables the filter path.
func (s *MixBlockScheduler) SetBypass(on bool) {
}

// ResetFilter clears the filter memory without touching the counters.
func (s *MixBlockScheduler) ResetFilter() error {
	return errNotImplemented
}

// LatencySamples reports the delay compensation in samples at the current rate.
func (s *MixBlockScheduler) LatencySamples() int {
	return 0
}

// Stats returns a copy of the running counters.
func (s *MixBlockScheduler) Stats() MixStats {
	return MixStats{}
}

// Snapshot serialises configuration, filter state, parameters and counters.
func (s *MixBlockScheduler) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole chain state from a snapshot, all or nothing.
func (s *MixBlockScheduler) Restore(data []byte) error {
	return errNotImplemented
}
