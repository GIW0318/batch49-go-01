// Package task converts high bit depth frames to a lower bit depth target with
// ordered dithering and a bounded error diffusion budget.
package task

import "errors"

// errNotImplemented marks behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Supported ranges.
const (
	MinTargetBits   = 1
	MaxTargetBits   = 16
	MaxTileSize     = 32
	DefaultTileSize = 4
)

// Errors returned by the pipeline.
var (
	ErrInvalidBitDepth = errors.New("target bit depth must be within 1 and 16")
	ErrInvalidTileSize = errors.New("dither tile size must be a power of two up to 32")
	ErrInvalidStrength = errors.New("strength must be within 0 and 1")
	ErrInvalidChannel  = errors.New("diffusion channel is out of range")
	ErrInvalidMaxError = errors.New("max error budget must not be negative")
	ErrInvalidFrame    = errors.New("frame buffer does not match its dimensions")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// DitherConfig is the output format and the diffusion budget.
type DitherConfig struct {
	TargetBits      int     `json:"target_bits"`
	TileSize        int     `json:"tile_size"`
	Strength        float64 `json:"strength"`
	DiffuseChannels []int   `json:"diffuse_channels"`
	MaxError        float64 `json:"max_error"`
}

// Frame is a linear float frame with three or four channels.
type Frame struct {
	Width    int       `json:"width"`
	Height   int       `json:"height"`
	Channels int       `json:"channels"`
	Data     []float32 `json:"data"`
}

// DitherStats summarises the processed frames.
type DitherStats struct {
	Frames           int     `json:"frames"`
	Pixels           int     `json:"pixels"`
	Tiles            int     `json:"tiles"`
	Phase            int     `json:"phase"`
	MaxAbsError      float64 `json:"max_abs_error"`
	DiffusedChannels int     `json:"diffused_channels"`
	Skipped          bool    `json:"skipped"`
}

// BitDepthDither owns the configuration, the frame counter and the error
// budget.
type BitDepthDither struct {
	cfg DitherConfig
}

// NewBitDepthDither builds a pipeline with the given configuration.
func NewBitDepthDither(cfg DitherConfig) (*BitDepthDither, error) {
	return &BitDepthDither{cfg: cfg}, nil
}

// Configure replaces the configuration.
func (d *BitDepthDither) Configure(cfg DitherConfig) error {
	return errNotImplemented
}

// SetStrength changes the dither amplitude.
func (d *BitDepthDither) SetStrength(v float64) error {
	return errNotImplemented
}

// ProcessFrame quantises one frame into the target bit depth.
func (d *BitDepthDither) ProcessFrame(img Frame, frameSeq uint64) (Frame, error) {
	return Frame{}, errNotImplemented
}

// Stats reports the counters of the finished frames.
func (d *BitDepthDither) Stats() DitherStats {
	return DitherStats{}
}

// CurrentConfig returns the active configuration.
func (d *BitDepthDither) CurrentConfig() DitherConfig {
	return DitherConfig{}
}

// PhaseFor reports the dither phase used by one frame sequence number.
func (d *BitDepthDither) PhaseFor(frameSeq uint64) int {
	return 0
}

// Reset clears the counters and the pending error budget.
func (d *BitDepthDither) Reset() error {
	return errNotImplemented
}

// Snapshot serialises the configuration, counters and pending error budget.
func (d *BitDepthDither) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (d *BitDepthDither) Restore(data []byte) error {
	return errNotImplemented
}
