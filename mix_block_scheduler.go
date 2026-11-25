// Package task drives a mixer chain block by block, keeping filter state and
// parameter ramps continuous across block boundaries and padding short supply.
package task

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"
)

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

// Accepted parameter ranges.
const (
	MinCutoff = 100.0
	MaxCutoff = 20000.0
	MinGain   = 0.0
	MaxGain   = 4.0
	MinMix    = 0.0
	MaxMix    = 1.0
)

// Default chain targets. Mix and unity gain keep the filtered signal audible.
const (
	DefaultCutoff = 1000.0
	DefaultGain   = 1.0
	DefaultMix    = 1.0
)

// Fixed time budgets used for control ramps and delay compensation.
const (
	ParamRampMilliseconds     = 10.0
	LatencyBudgetMilliseconds = 5.0
)

const (
	snapshotEnvelopeMagic = "mix-block-scheduler-envelope-v1"
	snapshotDataMagic     = "mix-block-scheduler"
	snapshotFormatVersion = 1
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

// ticketLock is a small FIFO mutual-exclusion gate. Tickets are assigned in
// the order goroutines observe the short coordination mutex.
type ticketLock struct {
	mu      sync.Mutex
	cond    *sync.Cond
	next    uint64
	serving uint64
}

func (l *ticketLock) lock() {
	l.mu.Lock()
	if l.cond == nil {
		l.cond = sync.NewCond(&l.mu)
	}

	ticket := l.next
	l.next++
	for l.serving != ticket {
		l.cond.Wait()
	}
	l.mu.Unlock()
}

func (l *ticketLock) unlock() {
	l.mu.Lock()
	l.serving++
	if l.cond != nil {
		l.cond.Broadcast()
	}
	l.mu.Unlock()
}

type rampState struct {
	Start          float64
	Current        float64
	Target         float64
	ElapsedSamples int64
	RampSamples    int64
}

func steadyRamp(value float64) rampState {
	return rampState{Start: value, Current: value, Target: value}
}

func rampDurationSamples(sampleRate int) int64 {
	samples := int64(math.Ceil(ParamRampMilliseconds * float64(sampleRate) / 1000.0))
	if samples < 1 {
		return 1
	}
	return samples
}

func (r *rampState) begin(target float64, sampleRate int) {
	r.Start = r.Current
	r.Target = target
	r.ElapsedSamples = 0
	if target == r.Current {
		r.RampSamples = 0
		r.Current = target
		return
	}
	r.RampSamples = rampDurationSamples(sampleRate)
}

func (r *rampState) advance() {
	if r.RampSamples <= 0 || r.ElapsedSamples >= r.RampSamples {
		r.Current = r.Target
		r.ElapsedSamples = r.RampSamples
		return
	}

	r.ElapsedSamples++
	if r.ElapsedSamples >= r.RampSamples {
		r.Current = r.Target
		return
	}

	progress := float64(r.ElapsedSamples) / float64(r.RampSamples)
	r.Current = r.Start + (r.Target-r.Start)*progress
}

func (r *rampState) changeSampleRate(oldRate, newRate int) {
	if oldRate == newRate || oldRate <= 0 || r.RampSamples <= 0 {
		return
	}

	progress := float64(r.ElapsedSamples) / float64(r.RampSamples)
	progress = math.Max(0, math.Min(1, progress))
	r.RampSamples = rampDurationSamples(newRate)
	r.ElapsedSamples = int64(math.Round(progress * float64(r.RampSamples)))
	if r.ElapsedSamples >= r.RampSamples {
		r.ElapsedSamples = r.RampSamples
		r.Current = r.Target
		return
	}
	r.Current = r.Start + (r.Target-r.Start)*progress
}

// MixBlockScheduler owns configuration, filter state, parameters and counters.
type MixBlockScheduler struct {
	gate ticketLock

	sampleRate int
	blockSize  int
	bypass     bool
	stats      MixStats

	latencySamples    int
	filterCoefficient float64
	filterLeft        float64
	filterRight       float64

	cutoff rampState
	gain   rampState
	mix    rampState
}

type paramSnapshot struct {
	Start          float64
	Current        float64
	Target         float64
	ElapsedSamples int64
	RampSamples    int64
}

type snapshotData struct {
	Magic             string
	Version           int
	SampleRate        int
	BlockSize         int
	Bypass            bool
	Stats             MixStats
	LatencySamples    int
	FilterCoefficient float64
	FilterLeft        float64
	FilterRight       float64
	Cutoff            paramSnapshot
	Gain              paramSnapshot
	Mix               paramSnapshot
}

type snapshotEnvelope struct {
	Magic   string `json:"magic"`
	Payload []byte `json:"payload"`
	SHA256  string `json:"sha256"`
	Version int    `json:"version"`
}

// NewMixBlockScheduler builds a chain for the given rate and block size.
func NewMixBlockScheduler(sampleRate, blockSize int) (*MixBlockScheduler, error) {
	if err := validateSampleRate(sampleRate); err != nil {
		return nil, err
	}
	if err := validateBlockSize(blockSize); err != nil {
		return nil, err
	}

	s := &MixBlockScheduler{
		sampleRate: sampleRate,
		blockSize:  blockSize,
		cutoff:     steadyRamp(DefaultCutoff),
		gain:       steadyRamp(DefaultGain),
		mix:        steadyRamp(DefaultMix),
		stats:      MixStats{SampleRate: sampleRate, BlockSize: blockSize},
	}
	s.latencySamples = latencySamplesForRate(sampleRate)
	s.filterCoefficient = lowPassCoefficient(s.cutoff.Current, sampleRate)
	return s, nil
}

func validateSampleRate(sampleRate int) error {
	if sampleRate < MinSampleRate || sampleRate > MaxSampleRate {
		return ErrBadSampleRate
	}
	return nil
}

func validateBlockSize(blockSize int) error {
	if blockSize < MinBlockSize || blockSize > MaxBlockSize {
		return ErrBadBlockSize
	}
	return nil
}

func validateParamValue(name string, value float64) error {
	var minValue, maxValue float64
	switch name {
	case ParamCutoff:
		minValue, maxValue = MinCutoff, MaxCutoff
	case ParamGain:
		minValue, maxValue = MinGain, MaxGain
	case ParamMix:
		minValue, maxValue = MinMix, MaxMix
	default:
		return ErrUnknownParam
	}

	if math.IsNaN(value) || value < minValue || value > maxValue {
		return ErrOutOfRange
	}
	return nil
}

func lowPassCoefficient(cutoff float64, sampleRate int) float64 {
	deltaTime := 1.0 / float64(sampleRate)
	resistorCapacitor := 1.0 / (2.0 * math.Pi * cutoff)
	coefficient := deltaTime / (resistorCapacitor + deltaTime)
	return math.Max(0, math.Min(1, coefficient))
}

func latencySamplesForRate(sampleRate int) int {
	samples := int(math.Ceil(LatencyBudgetMilliseconds * float64(sampleRate) / 1000.0))
	if samples < 1 {
		return 1
	}
	return samples
}

// SubmitBlock processes one input buffer and returns exactly one block.
func (s *MixBlockScheduler) SubmitBlock(in Frame) (Frame, error) {
	inputFrames := len(in.Left)
	if inputFrames == 0 || len(in.Right) == 0 {
		return Frame{}, ErrBadFrame
	}
	if len(in.Right) != inputFrames {
		return Frame{}, ErrBadFrame
	}

	s.gate.lock()
	defer s.gate.unlock()

	if inputFrames > s.blockSize {
		return Frame{}, ErrFrameTooLong
	}

	blockSize := s.blockSize
	out := Frame{
		Left:  make([]float32, blockSize),
		Right: make([]float32, blockSize),
	}

	padFrames := blockSize - inputFrames
	s.stats.Blocks++
	s.stats.InputFrames += inputFrames
	s.stats.OutputFrames += blockSize
	s.stats.PadFrames += padFrames
	if padFrames > 0 {
		s.stats.Underruns++
	}

	if s.bypass {
		copy(out.Left, in.Left)
		copy(out.Right, in.Right)
		return out, nil
	}

	for i := 0; i < blockSize; i++ {
		s.cutoff.advance()
		s.gain.advance()
		s.mix.advance()

		coefficient := lowPassCoefficient(s.cutoff.Current, s.sampleRate)
		s.filterCoefficient = coefficient

		var leftInput, rightInput float64
		if i < inputFrames {
			leftInput = float64(in.Left[i])
			rightInput = float64(in.Right[i])
		}

		s.filterLeft += coefficient * (leftInput - s.filterLeft)
		s.filterRight += coefficient * (rightInput - s.filterRight)

		// Returned padding is explicit silence. The filter is still clocked
		// through zero so recovery continues from the decayed tail.
		if i >= inputFrames {
			continue
		}

		gain := s.gain.Current
		mix := s.mix.Current
		leftWet := s.filterLeft * gain
		rightWet := s.filterRight * gain
		out.Left[i] = float32(leftInput*(1.0-mix) + leftWet*mix)
		out.Right[i] = float32(rightInput*(1.0-mix) + rightWet*mix)
	}

	return out, nil
}

// SetBlockSize changes the output block size for later submissions.
func (s *MixBlockScheduler) SetBlockSize(n int) error {
	if err := validateBlockSize(n); err != nil {
		return err
	}

	s.gate.lock()
	defer s.gate.unlock()

	s.blockSize = n
	s.stats.BlockSize = n
	return nil
}

// SetSampleRate changes the chain rate and recomputes derived coefficients.
func (s *MixBlockScheduler) SetSampleRate(hz int) error {
	if err := validateSampleRate(hz); err != nil {
		return err
	}

	s.gate.lock()
	defer s.gate.unlock()

	oldRate := s.sampleRate
	s.sampleRate = hz
	s.stats.SampleRate = hz
	s.latencySamples = latencySamplesForRate(hz)
	s.cutoff.changeSampleRate(oldRate, hz)
	s.gain.changeSampleRate(oldRate, hz)
	s.mix.changeSampleRate(oldRate, hz)
	s.filterCoefficient = lowPassCoefficient(s.cutoff.Current, hz)
	return nil
}

// SetParam writes one chain parameter, ramping towards the new value.
func (s *MixBlockScheduler) SetParam(name string, v float64) error {
	if err := validateParamValue(name, v); err != nil {
		return err
	}

	s.gate.lock()
	defer s.gate.unlock()

	switch name {
	case ParamCutoff:
		s.cutoff.begin(v, s.sampleRate)
	case ParamGain:
		s.gain.begin(v, s.sampleRate)
	case ParamMix:
		s.mix.begin(v, s.sampleRate)
	}
	return nil
}

// Param reads the target value of one chain parameter.
func (s *MixBlockScheduler) Param(name string) float64 {
	s.gate.lock()
	defer s.gate.unlock()

	switch name {
	case ParamCutoff:
		return s.cutoff.Target
	case ParamGain:
		return s.gain.Target
	case ParamMix:
		return s.mix.Target
	default:
		return 0
	}
}

// SetBypass enables or disables the processed path.
func (s *MixBlockScheduler) SetBypass(on bool) {
	s.gate.lock()
	defer s.gate.unlock()
	s.bypass = on
}

// ResetFilter clears filter memory without touching counters or parameters.
func (s *MixBlockScheduler) ResetFilter() error {
	s.gate.lock()
	defer s.gate.unlock()

	s.filterLeft = 0
	s.filterRight = 0
	s.filterCoefficient = lowPassCoefficient(s.cutoff.Current, s.sampleRate)
	return nil
}

// LatencySamples reports delay compensation at the current sample rate.
func (s *MixBlockScheduler) LatencySamples() int {
	s.gate.lock()
	defer s.gate.unlock()
	return s.latencySamples
}

// Stats returns a copy of the running counters and current configuration.
func (s *MixBlockScheduler) Stats() MixStats {
	s.gate.lock()
	defer s.gate.unlock()

	stats := s.stats
	stats.SampleRate = s.sampleRate
	stats.BlockSize = s.blockSize
	return stats
}

func snapshotFromRamp(r rampState) paramSnapshot {
	return paramSnapshot{
		Start:          r.Start,
		Current:        r.Current,
		Target:         r.Target,
		ElapsedSamples: r.ElapsedSamples,
		RampSamples:    r.RampSamples,
	}
}

func rampFromSnapshot(p paramSnapshot) rampState {
	return rampState{
		Start:          p.Start,
		Current:        p.Current,
		Target:         p.Target,
		ElapsedSamples: p.ElapsedSamples,
		RampSamples:    p.RampSamples,
	}
}

func (s *MixBlockScheduler) snapshotDataLocked() snapshotData {
	stats := s.stats
	stats.SampleRate = s.sampleRate
	stats.BlockSize = s.blockSize

	return snapshotData{
		Magic:             snapshotDataMagic,
		Version:           snapshotFormatVersion,
		SampleRate:        s.sampleRate,
		BlockSize:         s.blockSize,
		Bypass:            s.bypass,
		Stats:             stats,
		LatencySamples:    s.latencySamples,
		FilterCoefficient: s.filterCoefficient,
		FilterLeft:        s.filterLeft,
		FilterRight:       s.filterRight,
		Cutoff:            snapshotFromRamp(s.cutoff),
		Gain:              snapshotFromRamp(s.gain),
		Mix:               snapshotFromRamp(s.mix),
	}
}

// Snapshot serialises configuration, filter state, parameters and counters.
func (s *MixBlockScheduler) Snapshot() ([]byte, error) {
	s.gate.lock()
	data := s.snapshotDataLocked()
	s.gate.unlock()

	var payload bytes.Buffer
	if err := gob.NewEncoder(&payload).Encode(&data); err != nil {
		return nil, err
	}

	checksum := sha256.Sum256(payload.Bytes())
	envelope := snapshotEnvelope{
		Magic:   snapshotEnvelopeMagic,
		Version: snapshotFormatVersion,
		Payload: payload.Bytes(),
		SHA256:  hex.EncodeToString(checksum[:]),
	}

	raw, err := json.Marshal(&envelope)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// Restore replaces all chain state from a validated snapshot or changes nothing.
func (s *MixBlockScheduler) Restore(raw []byte) error {
	var envelope snapshotEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("%w: invalid envelope: %v", ErrBadSnapshot, err)
	}
	if envelope.Magic != snapshotEnvelopeMagic || envelope.Version != snapshotFormatVersion {
		return fmt.Errorf("%w: unsupported envelope", ErrBadSnapshot)
	}

	checksum, err := hex.DecodeString(envelope.SHA256)
	if err != nil || len(checksum) != sha256.Size {
		return fmt.Errorf("%w: invalid checksum encoding", ErrBadSnapshot)
	}
	actualChecksum := sha256.Sum256(envelope.Payload)
	if subtle.ConstantTimeCompare(checksum, actualChecksum[:]) != 1 {
		return fmt.Errorf("%w: checksum mismatch", ErrBadSnapshot)
	}

	var data snapshotData
	if err := gob.NewDecoder(bytes.NewReader(envelope.Payload)).Decode(&data); err != nil {
		return fmt.Errorf("%w: invalid payload: %v", ErrBadSnapshot, err)
	}
	if err := validateSnapshotData(&data); err != nil {
		return err
	}

	s.gate.lock()
	defer s.gate.unlock()

	s.sampleRate = data.SampleRate
	s.blockSize = data.BlockSize
	s.bypass = data.Bypass
	s.stats = data.Stats
	s.stats.SampleRate = data.SampleRate
	s.stats.BlockSize = data.BlockSize
	s.latencySamples = latencySamplesForRate(data.SampleRate)
	s.filterLeft = data.FilterLeft
	s.filterRight = data.FilterRight
	s.cutoff = rampFromSnapshot(data.Cutoff)
	s.gain = rampFromSnapshot(data.Gain)
	s.mix = rampFromSnapshot(data.Mix)
	s.filterCoefficient = lowPassCoefficient(s.cutoff.Current, data.SampleRate)
	return nil
}

func validateSnapshotData(data *snapshotData) error {
	if data.Magic != snapshotDataMagic || data.Version != snapshotFormatVersion {
		return fmt.Errorf("%w: unsupported payload version", ErrBadSnapshot)
	}
	if err := validateSampleRate(data.SampleRate); err != nil {
		return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	if err := validateBlockSize(data.BlockSize); err != nil {
		return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	if data.Stats.SampleRate != data.SampleRate || data.Stats.BlockSize != data.BlockSize {
		return fmt.Errorf("%w: statistics configuration mismatch", ErrBadSnapshot)
	}
	if data.LatencySamples != latencySamplesForRate(data.SampleRate) {
		return fmt.Errorf("%w: latency mismatch", ErrBadSnapshot)
	}
	if data.Stats.Blocks < 0 || data.Stats.InputFrames < 0 || data.Stats.OutputFrames < 0 ||
		data.Stats.PadFrames < 0 || data.Stats.Underruns < 0 {
		return fmt.Errorf("%w: negative statistic", ErrBadSnapshot)
	}
	if err := validateParamSnapshot(data.Cutoff, MinCutoff, MaxCutoff); err != nil {
		return err
	}
	if err := validateParamSnapshot(data.Gain, MinGain, MaxGain); err != nil {
		return err
	}
	if err := validateParamSnapshot(data.Mix, MinMix, MaxMix); err != nil {
		return err
	}
	return nil
}

func validateParamSnapshot(param paramSnapshot, minValue, maxValue float64) error {
	if math.IsNaN(param.Start) || math.IsInf(param.Start, 0) ||
		math.IsNaN(param.Current) || math.IsInf(param.Current, 0) ||
		math.IsNaN(param.Target) || math.IsInf(param.Target, 0) {
		return fmt.Errorf("%w: non-finite parameter", ErrBadSnapshot)
	}
	if param.Start < minValue || param.Start > maxValue ||
		param.Current < minValue || param.Current > maxValue ||
		param.Target < minValue || param.Target > maxValue {
		return fmt.Errorf("%w: parameter out of range", ErrBadSnapshot)
	}
	if param.RampSamples < 0 || param.ElapsedSamples < 0 || param.ElapsedSamples > param.RampSamples {
		return fmt.Errorf("%w: invalid ramp position", ErrBadSnapshot)
	}
	if param.RampSamples == 0 && param.ElapsedSamples != 0 {
		return fmt.Errorf("%w: invalid steady ramp", ErrBadSnapshot)
	}
	return nil
}
