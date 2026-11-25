// Package task converts high bit depth frames to a lower bit depth target with
// ordered dithering and a bounded error diffusion budget.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"
)

// Supported ranges.
const (
	MinTargetBits   = 1
	MaxTargetBits   = 16
	MaxTileSize     = 32
	DefaultTileSize = 4
)

const (
	snapshotFormat  = "bitdepth-dither"
	snapshotVersion = 1

	// Keep a malformed or accidental oversized request from turning into an
	// unbounded allocation. Normal game frames are far below this limit.
	maxFrameSamples = 1 << 28
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
	mu sync.Mutex

	cfg DitherConfig

	frames           int
	pixels           int
	tiles            int
	phase            int
	maxAbsError      float64
	diffusedChannels int
	skipped          bool

	errorWidth    int
	errorHeight   int
	errorChannels int
	pendingError  []float64

	matrixTile int
	matrix     []float64
}

// NewBitDepthDither builds a pipeline with the given configuration.
func NewBitDepthDither(cfg DitherConfig) (*BitDepthDither, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	d := &BitDepthDither{cfg: cloneConfig(cfg)}
	d.rebuildMatrix()
	return d, nil
}

// Configure replaces the configuration.
func (d *BitDepthDither) Configure(cfg DitherConfig) error {
	if err := validateConfig(cfg); err != nil {
		return err
	}
	cfg = cloneConfig(cfg)

	d.mu.Lock()
	defer d.mu.Unlock()
	d.cfg = cfg
	d.rebuildMatrix()
	return nil
}

// SetStrength changes the dither amplitude.
func (d *BitDepthDither) SetStrength(v float64) error {
	if !validStrength(v) {
		return ErrInvalidStrength
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cfg.Strength = v
	return nil
}

// ProcessFrame quantises one frame into the target bit depth.
func (d *BitDepthDither) ProcessFrame(img Frame, frameSeq uint64) (Frame, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if err := validateConfig(d.cfg); err != nil {
		return Frame{}, err
	}
	samples, pixels, err := validateFrame(img)
	if err != nil {
		return Frame{}, err
	}
	channels, err := activeChannels(d.cfg.DiffuseChannels, img.Channels)
	if err != nil {
		return Frame{}, err
	}

	out := Frame{
		Width:    img.Width,
		Height:   img.Height,
		Channels: img.Channels,
		Data:     make([]float32, samples),
	}
	phase := d.phaseForLocked(frameSeq)
	tiles := tileCountFor(img.Width, img.Height, d.cfg.TileSize)

	// A zero-strength frame is a pure quantisation pass. In particular it must
	// not consume or replace the pending error used by later frames.
	if d.cfg.Strength == 0 {
		d.quantizeFrame(img, out.Data)
		d.commitFrame(pixels, tiles, phase, 0, 0, true)
		return out, nil
	}

	if len(channels) == 0 {
		d.ditherWithoutDiffusion(img, out.Data, phase)
		d.commitFrame(pixels, tiles, phase, 0, 0, false)
		return out, nil
	}

	work := make([]float64, samples)
	if img.Width == d.errorWidth &&
		img.Height == d.errorHeight &&
		img.Channels == d.errorChannels &&
		len(d.pendingError) == samples {
		copy(work, d.pendingError)
	}

	nextError := make([]float64, samples)
	frameMax := d.diffuseFrame(img, out.Data, work, nextError, channels, phase)

	// Everything above operates on local buffers, so committing here keeps a
	// failed frame from advancing either the counters or the error budget.
	d.pendingError = nextError
	d.errorWidth = img.Width
	d.errorHeight = img.Height
	d.errorChannels = img.Channels
	d.commitFrame(pixels, tiles, phase, frameMax, len(channels), false)
	return out, nil
}

// Stats reports the counters of the finished frames.
func (d *BitDepthDither) Stats() DitherStats {
	d.mu.Lock()
	defer d.mu.Unlock()
	return DitherStats{
		Frames:           d.frames,
		Pixels:           d.pixels,
		Tiles:            d.tiles,
		Phase:            d.phase,
		MaxAbsError:      d.maxAbsError,
		DiffusedChannels: d.diffusedChannels,
		Skipped:          d.skipped,
	}
}

// CurrentConfig returns the active configuration.
func (d *BitDepthDither) CurrentConfig() DitherConfig {
	d.mu.Lock()
	defer d.mu.Unlock()
	return cloneConfig(d.cfg)
}

// PhaseFor reports the dither phase used by one frame sequence number.
func (d *BitDepthDither) PhaseFor(frameSeq uint64) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.phaseForLocked(frameSeq)
}

// Reset clears the counters and the pending error budget.
func (d *BitDepthDither) Reset() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.frames = 0
	d.pixels = 0
	d.tiles = 0
	d.phase = 0
	d.maxAbsError = 0
	d.diffusedChannels = 0
	d.skipped = false
	d.errorWidth = 0
	d.errorHeight = 0
	d.errorChannels = 0
	d.pendingError = nil
	return nil
}

// Snapshot serialises the configuration, counters and pending error budget.
func (d *BitDepthDither) Snapshot() ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	state := snapshotState{
		Config:           cloneConfig(d.cfg),
		Frames:           d.frames,
		Pixels:           d.pixels,
		Tiles:            d.tiles,
		Phase:            d.phase,
		MaxAbsError:      d.maxAbsError,
		DiffusedChannels: d.diffusedChannels,
		Skipped:          d.skipped,
		ErrorWidth:       d.errorWidth,
		ErrorHeight:      d.errorHeight,
		ErrorChannels:    d.errorChannels,
		PendingError:     append([]float64(nil), d.pendingError...),
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("%w: cannot encode state", ErrBadSnapshot)
	}
	sum := sha256.Sum256(payload)
	envelope := snapshotEnvelope{
		Format:   snapshotFormat,
		Version:  snapshotVersion,
		Payload:  payload,
		Checksum: hex.EncodeToString(sum[:]),
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("%w: cannot encode envelope", ErrBadSnapshot)
	}
	return data, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (d *BitDepthDither) Restore(data []byte) error {
	state, err := decodeSnapshot(data)
	if err != nil {
		return err
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	d.cfg = state.Config
	d.frames = state.Frames
	d.pixels = state.Pixels
	d.tiles = state.Tiles
	d.phase = state.Phase
	d.maxAbsError = state.MaxAbsError
	d.diffusedChannels = state.DiffusedChannels
	d.skipped = state.Skipped
	d.errorWidth = state.ErrorWidth
	d.errorHeight = state.ErrorHeight
	d.errorChannels = state.ErrorChannels
	d.pendingError = append([]float64(nil), state.PendingError...)
	d.rebuildMatrix()
	return nil
}

type snapshotEnvelope struct {
	Format   string          `json:"format"`
	Version  int             `json:"version"`
	Payload  json.RawMessage `json:"payload"`
	Checksum string          `json:"checksum"`
}

type snapshotState struct {
	Config           DitherConfig `json:"config"`
	Frames           int          `json:"frames"`
	Pixels           int          `json:"pixels"`
	Tiles            int          `json:"tiles"`
	Phase            int          `json:"phase"`
	MaxAbsError      float64      `json:"max_abs_error"`
	DiffusedChannels int          `json:"diffused_channels"`
	Skipped          bool         `json:"skipped"`
	ErrorWidth       int          `json:"error_width"`
	ErrorHeight      int          `json:"error_height"`
	ErrorChannels    int          `json:"error_channels"`
	PendingError     []float64    `json:"pending_error"`
}

func decodeSnapshot(data []byte) (snapshotState, error) {
	var envelope snapshotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return snapshotState{}, fmt.Errorf("%w: malformed envelope", ErrBadSnapshot)
	}
	if envelope.Format != snapshotFormat || envelope.Version != snapshotVersion {
		return snapshotState{}, fmt.Errorf("%w: unsupported format", ErrBadSnapshot)
	}
	if len(envelope.Payload) == 0 {
		return snapshotState{}, fmt.Errorf("%w: empty payload", ErrBadSnapshot)
	}
	sum := sha256.Sum256(envelope.Payload)
	if envelope.Checksum != hex.EncodeToString(sum[:]) {
		return snapshotState{}, fmt.Errorf("%w: checksum mismatch", ErrBadSnapshot)
	}

	var state snapshotState
	if err := json.Unmarshal(envelope.Payload, &state); err != nil {
		return snapshotState{}, fmt.Errorf("%w: malformed state", ErrBadSnapshot)
	}
	state.Config = cloneConfig(state.Config)
	if err := validateSnapshotState(state); err != nil {
		return snapshotState{}, err
	}
	return state, nil
}

func validateSnapshotState(state snapshotState) error {
	if err := validateConfig(state.Config); err != nil {
		return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	if state.Frames < 0 || state.Pixels < 0 || state.Tiles < 0 ||
		state.DiffusedChannels < 0 || state.DiffusedChannels > 4 {
		return fmt.Errorf("%w: invalid state counter", ErrBadSnapshot)
	}
	// Configure may change the tile size after a frame has already been
	// processed, so the counter remains a historical value and can be larger
	// than the new tile.
	if state.Phase < 0 || state.Phase >= MaxTileSize {
		return fmt.Errorf("%w: phase is out of range", ErrBadSnapshot)
	}
	if math.IsNaN(state.MaxAbsError) || math.IsInf(state.MaxAbsError, 0) ||
		state.MaxAbsError < 0 {
		return fmt.Errorf("%w: invalid maximum error", ErrBadSnapshot)
	}
	if state.Frames == 0 {
		if state.Pixels != 0 || state.Tiles != 0 || state.DiffusedChannels != 0 {
			return fmt.Errorf("%w: inconsistent counters", ErrBadSnapshot)
		}
	}

	if state.ErrorWidth < 0 || state.ErrorHeight < 0 || state.ErrorChannels < 0 {
		return fmt.Errorf("%w: invalid error dimensions", ErrBadSnapshot)
	}
	if state.ErrorWidth == 0 || state.ErrorHeight == 0 {
		if state.ErrorChannels != 0 || len(state.PendingError) != 0 {
			return fmt.Errorf("%w: inconsistent error dimensions", ErrBadSnapshot)
		}
	} else {
		if state.ErrorChannels != 3 && state.ErrorChannels != 4 {
			return fmt.Errorf("%w: invalid error channels", ErrBadSnapshot)
		}
		count, _, ok := checkedSamples(state.ErrorWidth, state.ErrorHeight, state.ErrorChannels)
		if !ok || count != len(state.PendingError) {
			return fmt.Errorf("%w: invalid pending error buffer", ErrBadSnapshot)
		}
		for _, value := range state.PendingError {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return fmt.Errorf("%w: invalid pending error", ErrBadSnapshot)
			}
		}
	}
	return nil
}

func validateConfig(cfg DitherConfig) error {
	if cfg.TargetBits < MinTargetBits || cfg.TargetBits > MaxTargetBits {
		return ErrInvalidBitDepth
	}
	if cfg.TileSize <= 0 || cfg.TileSize > MaxTileSize ||
		cfg.TileSize&(cfg.TileSize-1) != 0 {
		return ErrInvalidTileSize
	}
	if !validStrength(cfg.Strength) {
		return ErrInvalidStrength
	}
	if math.IsNaN(cfg.MaxError) || math.IsInf(cfg.MaxError, 0) || cfg.MaxError < 0 {
		return ErrInvalidMaxError
	}
	return nil
}

func validStrength(value float64) bool {
	return !math.IsNaN(value) && value >= 0 && value <= 1
}

func cloneConfig(cfg DitherConfig) DitherConfig {
	if cfg.DiffuseChannels == nil {
		return cfg
	}
	channels := make([]int, len(cfg.DiffuseChannels))
	copy(channels, cfg.DiffuseChannels)
	cfg.DiffuseChannels = channels
	return cfg
}

func validateFrame(img Frame) (samples, pixels int, err error) {
	if img.Width <= 0 || img.Height <= 0 {
		return 0, 0, ErrInvalidFrame
	}
	if img.Channels != 3 && img.Channels != 4 {
		return 0, 0, ErrInvalidFrame
	}
	count, pixelCount, ok := checkedSamples(img.Width, img.Height, img.Channels)
	if !ok || count != len(img.Data) {
		return 0, 0, ErrInvalidFrame
	}
	return count, pixelCount, nil
}

func checkedSamples(width, height, channels int) (samples, pixels int, ok bool) {
	if width <= 0 || height <= 0 || channels <= 0 {
		return 0, 0, false
	}
	maxInt := int(^uint(0) >> 1)
	if width > maxInt/height {
		return 0, 0, false
	}
	pixels = width * height
	if pixels > maxInt/channels {
		return 0, 0, false
	}
	samples = pixels * channels
	if samples > maxFrameSamples {
		return 0, 0, false
	}
	return samples, pixels, true
}

func activeChannels(configured []int, frameChannels int) ([]int, error) {
	if len(configured) == 0 {
		return nil, nil
	}

	active := make([]int, 0, len(configured))
	seen := make([]bool, frameChannels)
	for _, channel := range configured {
		if channel < 0 || channel >= frameChannels {
			return nil, ErrInvalidChannel
		}
		// Alpha is ordered-dithered like the colour channels, but its error is
		// never diffused.
		if frameChannels == 4 && channel == 3 {
			continue
		}
		if seen[channel] {
			continue
		}
		seen[channel] = true
		active = append(active, channel)
	}
	return active, nil
}

func (d *BitDepthDither) phaseForLocked(frameSeq uint64) int {
	if d.cfg.TileSize <= 0 {
		return 0
	}
	return int(frameSeq % uint64(d.cfg.TileSize))
}

func tileCountFor(width, height, tileSize int) int {
	if tileSize <= 0 {
		return 0
	}
	return ((width + tileSize - 1) / tileSize) *
		((height + tileSize - 1) / tileSize)
}

func (d *BitDepthDither) commitFrame(
	pixels, tiles, phase int,
	frameMax float64,
	diffusedChannels int,
	skipped bool,
) {
	d.frames++
	d.pixels += pixels
	d.tiles += tiles
	d.phase = phase
	d.skipped = skipped
	d.diffusedChannels = diffusedChannels
	if frameMax > d.maxAbsError {
		d.maxAbsError = frameMax
	}
}

func (d *BitDepthDither) quantizeFrame(img Frame, out []float32) {
	levels := float64(uint64(1)<<uint(d.cfg.TargetBits)) - 1
	for index, value := range img.Data {
		out[index] = quantizeValue(float64(value), levels)
	}
}

func (d *BitDepthDither) ditherWithoutDiffusion(img Frame, out []float32, phase int) {
	levels := float64(uint64(1)<<uint(d.cfg.TargetBits)) - 1
	for y := 0; y < img.Height; y++ {
		for x := 0; x < img.Width; x++ {
			index := (y*img.Width + x) * img.Channels
			threshold := d.thresholdAt(x, y, phase)
			for channel := 0; channel < img.Channels; channel++ {
				adjusted := float64(img.Data[index+channel]) +
					d.cfg.Strength*threshold/levels
				out[index+channel] = quantizeValue(adjusted, levels)
			}
		}
	}
}

func (d *BitDepthDither) diffuseFrame(
	img Frame,
	out []float32,
	work []float64,
	next []float64,
	channels []int,
	phase int,
) float64 {
	levels := float64(uint64(1)<<uint(d.cfg.TargetBits)) - 1
	maxError := d.cfg.MaxError
	isDiffused := make([]bool, img.Channels)
	for _, channel := range channels {
		isDiffused[channel] = true
	}

	frameMax := 0.0
	width, height := img.Width, img.Height
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			index := (y*width + x) * img.Channels
			threshold := d.thresholdAt(x, y, phase)
			for channel := 0; channel < img.Channels; channel++ {
				sampleIndex := index + channel
				base := float64(img.Data[sampleIndex])
				if isDiffused[channel] {
					base += clampAbs(work[sampleIndex], maxError)
				}
				adjusted := base + d.cfg.Strength*threshold/levels
				quantized := quantizeValue(adjusted, levels)
				out[sampleIndex] = quantized

				if !isDiffused[channel] {
					continue
				}

				residual := clampAbs(clamp01(base)-float64(quantized), maxError)
				next[sampleIndex] = residual
				if abs := math.Abs(residual); abs > frameMax {
					frameMax = abs
				}
				d.spreadError(work, width, height, img.Channels, x, y, channel, residual)
			}
		}
	}
	return frameMax
}

func (d *BitDepthDither) spreadError(
	work []float64,
	width, height, channels, x, y, channel int,
	err float64,
) {
	if err == 0 {
		return
	}
	add := func(tx, ty int, weight float64) {
		if tx < 0 || tx >= width || ty < 0 || ty >= height {
			return
		}
		work[(ty*width+tx)*channels+channel] += err * weight
	}

	// Floyd-Steinberg diffusion. Processing in raster order makes every block
	// boundary consume the errors produced by the preceding block.
	add(x+1, y, 7.0/16.0)
	add(x-1, y+1, 3.0/16.0)
	add(x, y+1, 5.0/16.0)
	add(x+1, y+1, 1.0/16.0)
}

func (d *BitDepthDither) thresholdAt(x, y, phase int) float64 {
	size := d.cfg.TileSize
	if size <= 0 {
		return 0
	}
	if d.matrixTile != size || len(d.matrix) != size*size {
		d.rebuildMatrix()
	}
	mx := (x + phase) % size
	return d.matrix[(y%size)*size+mx]
}

func (d *BitDepthDither) rebuildMatrix() {
	size := d.cfg.TileSize
	if size <= 0 {
		d.matrixTile = 0
		d.matrix = nil
		return
	}

	values := []int{0}
	current := 1
	for current < size {
		nextSize := current * 2
		next := make([]int, nextSize*nextSize)
		for y := 0; y < current; y++ {
			for x := 0; x < current; x++ {
				value := values[y*current+x] * 4
				next[y*nextSize+x] = value
				next[y*nextSize+x+current] = value + 2
				next[(y+current)*nextSize+x] = value + 3
				next[(y+current)*nextSize+x+current] = value + 1
			}
		}
		values = next
		current = nextSize
	}

	area := float64(size * size)
	matrix := make([]float64, size*size)
	for index, value := range values {
		matrix[index] = (float64(value)+0.5)/area - 0.5
	}
	d.matrixTile = size
	d.matrix = matrix
}

func quantizeValue(value, levels float64) float32 {
	value = clamp01(value)
	quantized := math.Floor(value*levels + 0.5)
	if quantized < 0 {
		quantized = 0
	} else if quantized > levels {
		quantized = levels
	}
	return float32(quantized / levels)
}

func clamp01(value float64) float64 {
	if math.IsNaN(value) || value <= 0 {
		return 0
	}
	if value >= 1 {
		return 1
	}
	return value
}

func clampAbs(value, limit float64) float64 {
	if math.IsNaN(value) {
		return 0
	}
	if value > limit {
		return limit
	}
	if value < -limit {
		return -limit
	}
	return value
}
