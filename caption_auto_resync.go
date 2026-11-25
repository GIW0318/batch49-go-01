// Package task estimates the offset between a caption track and a voice track
// from activity anchors and pushes captions back into alignment step by step.
package task

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
)

// Default tuning values used when the configuration leaves a field at zero.
const (
	DefaultMaxStepMs          = 40
	DefaultOutlierThresholdMs = 250
	DefaultMinAnchors         = 3
)

// Errors returned by the resync engine.
var (
	ErrBlankCue            = errors.New("cue id is required")
	ErrDuplicateAnchor     = errors.New("anchor for this cue already exists")
	ErrBadAnchor           = errors.New("anchor times must be non-negative")
	ErrInsufficientAnchors = errors.New("not enough anchors to estimate")
	ErrUnknownCue          = errors.New("cue is not known")
	ErrBadSnapshot         = errors.New("snapshot payload is invalid")
)

// Anchor pairs one caption cue start with the matching voice activity start.
type Anchor struct {
	CueID        string
	CueStartMs   int64
	VoiceStartMs int64
}

// Estimate is the result of comparing all usable anchor pairs.
type Estimate struct {
	OffsetMs   int64
	Samples    int
	Outliers   int
	Confidence float64
}

// Shift reports how far one ApplyStep call moved the caption track.
type Shift struct {
	AppliedMs   int64
	RemainingMs int64
}

// Config tunes the step bound, the outlier threshold and the sample floor.
type Config struct {
	MaxStepMs          int64
	OutlierThresholdMs int64
	MinAnchors         int
}

// CaptionAutoResync owns anchors, accumulated shifts, locks and truncation marks.
type CaptionAutoResync struct {
	cfg Config

	mu          sync.Mutex
	anchors     map[string]Anchor
	shifts      map[string]int64
	locked      map[string]bool
	truncated   map[string]bool
	globalShift int64
}

// NewCaptionAutoResync builds an engine, replacing zero configuration fields
// with their defaults.
func NewCaptionAutoResync(cfg Config) *CaptionAutoResync {
	cfg = withDefaults(cfg)
	return &CaptionAutoResync{cfg: cfg}
}

func withDefaults(cfg Config) Config {
	if cfg.MaxStepMs <= 0 {
		cfg.MaxStepMs = DefaultMaxStepMs
	}
	if cfg.OutlierThresholdMs <= 0 {
		cfg.OutlierThresholdMs = DefaultOutlierThresholdMs
	}
	if cfg.MinAnchors <= 0 {
		cfg.MinAnchors = DefaultMinAnchors
	}
	return cfg
}

// SubmitAnchor records one caption/voice pair for a cue.
func (c *CaptionAutoResync) SubmitAnchor(a Anchor) error {
	if strings.TrimSpace(a.CueID) == "" {
		return ErrBlankCue
	}
	if a.CueStartMs < 0 || a.VoiceStartMs < 0 {
		return ErrBadAnchor
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.anchors[a.CueID]; ok {
		return ErrDuplicateAnchor
	}
	c.ensureMapsLocked()
	c.anchors[a.CueID] = a
	if _, ok := c.shifts[a.CueID]; !ok {
		c.shifts[a.CueID] = 0
	}
	return nil
}

// Estimate computes the median offset after removing outlier pairs.
func (c *CaptionAutoResync) Estimate() (Estimate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.estimateLocked()
}

// ApplyStep moves the caption track towards the estimated offset.
func (c *CaptionAutoResync) ApplyStep() (Shift, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	cfg := withDefaults(c.cfg)
	est, err := c.estimateLocked()
	if err != nil {
		return Shift{}, err
	}

	move := est.OffsetMs - c.globalShift
	if move > cfg.MaxStepMs {
		move = cfg.MaxStepMs
	} else if move < -cfg.MaxStepMs {
		move = -cfg.MaxStepMs
	}
	c.globalShift += move

	for cueID := range c.shifts {
		if c.locked[cueID] {
			continue
		}
		step := move
		if c.truncated[cueID] {
			step = boundTruncatedMove(c.shifts[cueID], move, cfg.MaxStepMs)
		}
		c.shifts[cueID] += step
	}

	return Shift{
		AppliedMs:   move,
		RemainingMs: est.OffsetMs - c.globalShift,
	}, nil
}

// Lock excludes one cue from further correction.
func (c *CaptionAutoResync) Lock(cueID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.shifts[cueID]; !ok {
		return ErrUnknownCue
	}
	c.locked[cueID] = true
	return nil
}

// Unlock lets one cue follow correction again.
func (c *CaptionAutoResync) Unlock(cueID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.shifts[cueID]; !ok {
		return ErrUnknownCue
	}
	delete(c.locked, cueID)
	return nil
}

// Locked reports whether a cue is currently locked.
func (c *CaptionAutoResync) Locked(cueID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.locked[cueID]
}

// AnchorCount reports how many anchors are stored.
func (c *CaptionAutoResync) AnchorCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.anchors)
}

// CueShift reports the accumulated shift of one cue.
func (c *CaptionAutoResync) CueShift(cueID string) (int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	shift, ok := c.shifts[cueID]
	return shift, ok
}

// MarkTruncated records that a cue was cut off and needs bounded correction.
func (c *CaptionAutoResync) MarkTruncated(cueID string) error {
	if strings.TrimSpace(cueID) == "" {
		return ErrBlankCue
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensureMapsLocked()
	c.truncated[cueID] = true
	return nil
}

// Reset clears anchors, shifts, locks and truncation marks.
func (c *CaptionAutoResync) Reset() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.anchors = make(map[string]Anchor)
	c.shifts = make(map[string]int64)
	c.locked = make(map[string]bool)
	c.truncated = make(map[string]bool)
	c.globalShift = 0
	return nil
}

// Snapshot serialises anchors, shifts, locks and the global progress.
func (c *CaptionAutoResync) Snapshot() ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	snap := snapshot{
		Kind:          snapshotKind,
		Version:       snapshotVersion,
		GlobalShiftMs: c.globalShift,
		Anchors:       make([]anchorState, 0, len(c.anchors)),
		Cues:          make([]cueState, 0, len(c.shifts)),
		Truncated:     make([]string, 0, len(c.truncated)),
	}
	for _, a := range c.anchors {
		snap.Anchors = append(snap.Anchors, anchorState{
			CueID:        a.CueID,
			CueStartMs:   a.CueStartMs,
			VoiceStartMs: a.VoiceStartMs,
		})
	}
	for cueID, shift := range c.shifts {
		snap.Cues = append(snap.Cues, cueState{
			CueID:   cueID,
			ShiftMs: shift,
			Locked:  c.locked[cueID],
		})
	}
	for cueID := range c.truncated {
		snap.Truncated = append(snap.Truncated, cueID)
	}
	sort.Slice(snap.Anchors, func(i, j int) bool { return snap.Anchors[i].CueID < snap.Anchors[j].CueID })
	sort.Slice(snap.Cues, func(i, j int) bool { return snap.Cues[i].CueID < snap.Cues[j].CueID })
	sort.Strings(snap.Truncated)

	return json.Marshal(snap)
}

// Restore replaces the whole engine state from a snapshot, all or nothing.
func (c *CaptionAutoResync) Restore(data []byte) error {
	var snap snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return ErrBadSnapshot
	}
	if snap.Kind != snapshotKind || snap.Version != snapshotVersion {
		return ErrBadSnapshot
	}

	anchors := make(map[string]Anchor, len(snap.Anchors))
	for _, a := range snap.Anchors {
		if strings.TrimSpace(a.CueID) == "" || a.CueStartMs < 0 || a.VoiceStartMs < 0 {
			return ErrBadSnapshot
		}
		if _, ok := anchors[a.CueID]; ok {
			return ErrBadSnapshot
		}
		anchors[a.CueID] = Anchor{
			CueID:        a.CueID,
			CueStartMs:   a.CueStartMs,
			VoiceStartMs: a.VoiceStartMs,
		}
	}

	shifts := make(map[string]int64, len(snap.Cues))
	locked := make(map[string]bool)
	for _, cue := range snap.Cues {
		if strings.TrimSpace(cue.CueID) == "" {
			return ErrBadSnapshot
		}
		if _, ok := shifts[cue.CueID]; ok {
			return ErrBadSnapshot
		}
		if _, ok := anchors[cue.CueID]; !ok {
			return ErrBadSnapshot
		}
		shifts[cue.CueID] = cue.ShiftMs
		if cue.Locked {
			locked[cue.CueID] = true
		}
	}
	for cueID := range anchors {
		if _, ok := shifts[cueID]; !ok {
			return ErrBadSnapshot
		}
	}

	truncated := make(map[string]bool, len(snap.Truncated))
	for _, cueID := range snap.Truncated {
		if strings.TrimSpace(cueID) == "" {
			return ErrBadSnapshot
		}
		truncated[cueID] = true
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.anchors = anchors
	c.shifts = shifts
	c.locked = locked
	c.truncated = truncated
	c.globalShift = snap.GlobalShiftMs
	return nil
}

const (
	snapshotKind    = "caption-auto-resync"
	snapshotVersion = 1
)

type snapshot struct {
	Kind          string        `json:"kind"`
	Version       int           `json:"version"`
	Anchors       []anchorState `json:"anchors"`
	Cues          []cueState    `json:"cues"`
	Truncated     []string      `json:"truncated"`
	GlobalShiftMs int64         `json:"global_shift_ms"`
}

type anchorState struct {
	CueID        string `json:"cue_id"`
	CueStartMs   int64  `json:"cue_start_ms"`
	VoiceStartMs int64  `json:"voice_start_ms"`
}

type cueState struct {
	CueID   string `json:"cue_id"`
	ShiftMs int64  `json:"shift_ms"`
	Locked  bool   `json:"locked"`
}

func (c *CaptionAutoResync) ensureMapsLocked() {
	if c.anchors == nil {
		c.anchors = make(map[string]Anchor)
	}
	if c.shifts == nil {
		c.shifts = make(map[string]int64)
	}
	if c.locked == nil {
		c.locked = make(map[string]bool)
	}
	if c.truncated == nil {
		c.truncated = make(map[string]bool)
	}
}

func (c *CaptionAutoResync) estimateLocked() (Estimate, error) {
	cfg := withDefaults(c.cfg)
	n := len(c.anchors)
	if n < cfg.MinAnchors {
		return Estimate{}, ErrInsufficientAnchors
	}

	offsets := make([]int64, 0, n)
	for _, a := range c.anchors {
		offsets = append(offsets, a.VoiceStartMs-a.CueStartMs)
	}
	sort.Slice(offsets, func(i, j int) bool { return offsets[i] < offsets[j] })

	base := medianSorted(offsets)
	kept := make([]int64, 0, len(offsets))
	for _, offset := range offsets {
		if absInt64(offset-base) <= cfg.OutlierThresholdMs {
			kept = append(kept, offset)
		}
	}

	final := kept
	outliers := len(offsets) - len(kept)
	var offset int64
	if len(kept) >= cfg.MinAnchors {
		offset = medianSorted(kept)
	} else {
		final = offsets
		offset = base
	}

	return Estimate{
		OffsetMs:   offset,
		Samples:    len(final),
		Outliers:   outliers,
		Confidence: confidence(final, cfg.OutlierThresholdMs),
	}, nil
}

func medianSorted(values []int64) int64 {
	n := len(values)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return values[n/2]
	}
	return midpoint(values[n/2-1], values[n/2])
}

func midpoint(a, b int64) int64 {
	if (a < 0) != (b < 0) {
		return (a + b) / 2
	}
	return a/2 + b/2 + (a%2+b%2)/2
}

func confidence(values []int64, threshold int64) float64 {
	if len(values) == 0 || threshold <= 0 {
		return 0
	}
	minValue, maxValue := values[0], values[0]
	for _, value := range values[1:] {
		if value < minValue {
			minValue = value
		}
		if value > maxValue {
			maxValue = value
		}
	}
	spread := float64(maxValue) - float64(minValue)
	confidence := 1 - spread/(2*float64(threshold))
	if confidence < 0 {
		return 0
	}
	if confidence > 1 {
		return 1
	}
	return confidence
}

func absInt64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}

func boundTruncatedMove(current, move, limit int64) int64 {
	if move == 0 {
		return 0
	}
	if limit <= 0 {
		return 0
	}
	if current >= limit && move > 0 {
		return 0
	}
	if current <= -limit && move < 0 {
		return 0
	}

	next := current + move
	if move > 0 && next > limit {
		allowed := limit - current
		if allowed < 0 {
			return 0
		}
		return allowed
	}
	if move < 0 && next < -limit {
		allowed := -limit - current
		if allowed > 0 {
			return 0
		}
		return allowed
	}
	return move
}
