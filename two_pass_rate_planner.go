// Package task plans per scene bitrate from first pass complexity stats.
package task

import (
	"errors"
	"sync"
)

var errNotImplemented = errors.New("not implemented")

// Errors returned by the planner.
var (
	ErrBadStats       = errors.New("frame stats are invalid")
	ErrDuplicateFrame = errors.New("frame stats are already recorded")
	ErrOutOfOrder     = errors.New("frame sequence must increase")
	ErrBadScene       = errors.New("scene boundary is invalid")
	ErrNoStats        = errors.New("no frame stats recorded")
	ErrBadConfig      = errors.New("plan configuration is invalid")
	ErrBudgetMismatch = errors.New("allocation does not match the budget")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// FrameStats is one first pass frame measurement.
type FrameStats struct {
	Frame      int64
	Bytes      int64
	Complexity float64
}

// PlanConfig carries the second pass budget and the per scene bounds.
type PlanConfig struct {
	TotalRate        int64
	MinRate          int64
	MaxRate          int64
	Tolerance        int64
	KeyframeOverhead int64
}

// SceneAllocation is the planned rate of one scene.
type SceneAllocation struct {
	Index      int
	StartFrame int64
	EndFrame   int64
	Rate       int64
	Clamped    bool
	Missing    bool
}

// Allocation is the full second pass plan.
type Allocation struct {
	Total     int64
	Tolerance int64
	MinRate   int64
	MaxRate   int64
	Scenes    []SceneAllocation
}

// PlannerStats reports the recorded input volume.
type PlannerStats struct {
	Frames int
	Scenes int
	Bytes  int64
}

// TwoPassPlanner aggregates first pass stats into per scene complexity.
type TwoPassPlanner struct {
	mu       sync.Mutex
	scenes   []int64
	stats    []FrameStats
	seen     map[int64]bool
	counted  int64
	bytes    int64
	minScene int64
}

// NewTwoPassPlanner builds an empty planner with frame zero as the first scene.
func NewTwoPassPlanner() *TwoPassPlanner {
	return &TwoPassPlanner{scenes: []int64{0}, seen: map[int64]bool{}, minScene: 1}
}

// MarkScene declares a scene start at the given frame.
func (p *TwoPassPlanner) MarkScene(frame int64) error {
	return errNotImplemented
}

// AddStats records one first pass frame measurement.
func (p *TwoPassPlanner) AddStats(s FrameStats) error {
	return errNotImplemented
}

// Plan allocates the total rate across the effective scenes.
func (p *TwoPassPlanner) Plan(cfg PlanConfig) (Allocation, error) {
	return Allocation{}, errNotImplemented
}

// SceneBoundaries returns the effective scene start frames.
func (p *TwoPassPlanner) SceneBoundaries() []int64 {
	return nil
}

// Validate checks an allocation against its own budget and bounds.
func (p *TwoPassPlanner) Validate(a Allocation) error {
	return errNotImplemented
}

// SetSceneMinFrames changes the shortest allowed scene length.
func (p *TwoPassPlanner) SetSceneMinFrames(n int64) error {
	return errNotImplemented
}

// SceneCount returns the number of effective scenes.
func (p *TwoPassPlanner) SceneCount() int {
	return 0
}

// Stats returns a copy of the input counters.
func (p *TwoPassPlanner) Stats() PlannerStats {
	return PlannerStats{}
}

// Reset drops every scene and frame stat.
func (p *TwoPassPlanner) Reset() error {
	return errNotImplemented
}

// Snapshot serialises scenes, stats and the minimum scene length.
func (p *TwoPassPlanner) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the state with a snapshot, all or nothing.
func (p *TwoPassPlanner) Restore(data []byte) error {
	return errNotImplemented
}
