// Package task plans per scene bitrate from first pass complexity stats.
package task

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"math"
	"sort"
	"sync"
)

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

const snapshotVersion byte = 1

var snapshotMagic = []byte("TPRP")

// NewTwoPassPlanner builds an empty planner with frame zero as the first scene.
func NewTwoPassPlanner() *TwoPassPlanner {
	return &TwoPassPlanner{scenes: []int64{0}, seen: map[int64]bool{}, minScene: 1}
}

// ensureLocked makes the zero value usable as well as values returned by
// NewTwoPassPlanner. The caller must hold p.mu.
func (p *TwoPassPlanner) ensureLocked() {
	if len(p.scenes) == 0 {
		p.scenes = []int64{0}
	}
	if p.seen == nil {
		p.seen = make(map[int64]bool, len(p.stats))
		for _, s := range p.stats {
			p.seen[s.Frame] = true
		}
	}
	if p.minScene < 1 {
		p.minScene = 1
	}
}

// effectiveBoundariesLocked applies the minimum scene length to the declared
// boundaries. A too-short scene is merged into the scene before it. The caller
// must hold p.mu.
func (p *TwoPassPlanner) effectiveBoundariesLocked() []int64 {
	p.ensureLocked()
	out := make([]int64, 0, len(p.scenes))
	out = append(out, p.scenes[0])
	for _, boundary := range p.scenes[1:] {
		if boundary-out[len(out)-1] < p.minScene {
			continue
		}
		out = append(out, boundary)
	}
	return out
}

// MarkScene declares a scene start at the given frame.
func (p *TwoPassPlanner) MarkScene(frame int64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ensureLocked()

	if frame <= 0 {
		return ErrBadScene
	}
	last := p.scenes[len(p.scenes)-1]
	if frame <= last {
		return ErrBadScene
	}
	p.scenes = append(p.scenes, frame)
	return nil
}

// AddStats records one first pass frame measurement.
func (p *TwoPassPlanner) AddStats(s FrameStats) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ensureLocked()

	if s.Frame < 0 || s.Bytes < 0 || s.Complexity < 0 ||
		math.IsNaN(s.Complexity) || math.IsInf(s.Complexity, 0) {
		return ErrBadStats
	}
	if p.seen[s.Frame] {
		return ErrDuplicateFrame
	}
	if len(p.stats) > 0 && s.Frame <= p.stats[len(p.stats)-1].Frame {
		return ErrOutOfOrder
	}
	if s.Bytes > math.MaxInt64-p.bytes {
		return ErrBadStats
	}

	p.stats = append(p.stats, s)
	p.seen[s.Frame] = true
	p.counted++
	p.bytes += s.Bytes
	return nil
}

// Plan allocates the total rate across the effective scenes.
func (p *TwoPassPlanner) Plan(cfg PlanConfig) (Allocation, error) {
	if cfg.TotalRate <= 0 || cfg.MinRate < 0 || cfg.MaxRate < cfg.MinRate ||
		cfg.Tolerance < 0 || cfg.KeyframeOverhead < 0 {
		return Allocation{}, ErrBadConfig
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.ensureLocked()

	if len(p.stats) == 0 {
		return Allocation{}, ErrNoStats
	}

	bounds := p.effectiveBoundariesLocked()
	n := len(bounds)
	if n > 1 && cfg.KeyframeOverhead > math.MaxInt64/int64(n-1) {
		return Allocation{}, ErrBadConfig
	}

	sceneFrames := make([]int, n)
	sceneComplexity := make([]float64, n)
	for _, s := range p.stats {
		idx := sceneForFrame(bounds, s.Frame)
		sceneFrames[idx]++
		sceneComplexity[idx] += s.Complexity
	}

	existingScenes := 0
	existingComplexity := 0.0
	for i, sum := range sceneComplexity {
		if sceneFrames[i] > 0 {
			existingScenes++
			existingComplexity += sum
		}
	}

	averageComplexity := 0.0
	if existingScenes > 0 {
		averageComplexity = existingComplexity / float64(existingScenes)
	}

	weights := make([]float64, n)
	missing := make([]bool, n)
	for i := range weights {
		weights[i] = sceneComplexity[i]
		if sceneFrames[i] == 0 {
			missing[i] = true
			weights[i] = averageComplexity
		}
	}

	// A keyframe at a scene switch is charged to the incoming scene. Credit
	// the reserved total back to the first scene so that the allocator can
	// still satisfy the overall budget; bound handling below redistributes it
	// if the first scene cannot absorb the credit.
	offsets := make([]int64, n)
	if n > 1 && cfg.KeyframeOverhead > 0 {
		offsets[0] = cfg.KeyframeOverhead * int64(n-1)
		for i := 1; i < n; i++ {
			offsets[i] = -cfg.KeyframeOverhead
		}
	}

	rates, clamped := allocateRates(weights, offsets, cfg)

	maxFrame := p.stats[len(p.stats)-1].Frame
	scenes := make([]SceneAllocation, n)
	for i, start := range bounds {
		end := start
		if i+1 < n {
			end = bounds[i+1] - 1
		} else if maxFrame > start {
			end = maxFrame
		}
		scenes[i] = SceneAllocation{
			Index:      i,
			StartFrame: start,
			EndFrame:   end,
			Rate:       rates[i],
			Clamped:    clamped[i],
			Missing:    missing[i],
		}
	}

	return Allocation{
		Total:     cfg.TotalRate,
		Tolerance: cfg.Tolerance,
		MinRate:   cfg.MinRate,
		MaxRate:   cfg.MaxRate,
		Scenes:    scenes,
	}, nil
}

// SceneBoundaries returns the effective scene start frames.
func (p *TwoPassPlanner) SceneBoundaries() []int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.effectiveBoundariesLocked()
}

// Validate checks an allocation against its own budget and bounds.
func (p *TwoPassPlanner) Validate(a Allocation) error {
	if a.MinRate < 0 || a.MaxRate < a.MinRate || a.Tolerance < 0 {
		return ErrBadConfig
	}

	var sum int64
	for _, scene := range a.Scenes {
		if scene.Rate < a.MinRate || scene.Rate > a.MaxRate {
			return ErrBudgetMismatch
		}
		if scene.Rate > 0 && sum > math.MaxInt64-scene.Rate {
			return ErrBudgetMismatch
		}
		if scene.Rate < 0 && sum < math.MinInt64-scene.Rate {
			return ErrBudgetMismatch
		}
		sum += scene.Rate
	}

	diff := sum - a.Total
	if diff > a.Tolerance || diff < -a.Tolerance {
		return ErrBudgetMismatch
	}
	return nil
}

// SetSceneMinFrames changes the shortest allowed scene length.
func (p *TwoPassPlanner) SetSceneMinFrames(n int64) error {
	if n < 1 {
		return ErrBadConfig
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ensureLocked()
	p.minScene = n
	return nil
}

// SceneCount returns the number of effective scenes.
func (p *TwoPassPlanner) SceneCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.effectiveBoundariesLocked())
}

// Stats returns a copy of the input counters.
func (p *TwoPassPlanner) Stats() PlannerStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return PlannerStats{
		Frames: len(p.stats),
		Scenes: len(p.effectiveBoundariesLocked()),
		Bytes:  p.bytes,
	}
}

// Reset drops every scene and frame stat while keeping the configured minimum
// scene length.
func (p *TwoPassPlanner) Reset() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.scenes = []int64{0}
	p.stats = nil
	p.seen = make(map[int64]bool)
	p.counted = 0
	p.bytes = 0
	if p.minScene < 1 {
		p.minScene = 1
	}
	return nil
}

// Snapshot serialises scenes, stats and the minimum scene length.
func (p *TwoPassPlanner) Snapshot() ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ensureLocked()

	if len(p.scenes) > math.MaxUint32 || len(p.stats) > math.MaxUint32 {
		return nil, ErrBadSnapshot
	}

	var payload bytes.Buffer
	if err := binary.Write(&payload, binary.LittleEndian, p.minScene); err != nil {
		return nil, err
	}
	if err := binary.Write(&payload, binary.LittleEndian, uint32(len(p.scenes))); err != nil {
		return nil, err
	}
	for _, scene := range p.scenes {
		if err := binary.Write(&payload, binary.LittleEndian, scene); err != nil {
			return nil, err
		}
	}
	if err := binary.Write(&payload, binary.LittleEndian, uint32(len(p.stats))); err != nil {
		return nil, err
	}
	for _, s := range p.stats {
		if err := binary.Write(&payload, binary.LittleEndian, s.Frame); err != nil {
			return nil, err
		}
		if err := binary.Write(&payload, binary.LittleEndian, s.Bytes); err != nil {
			return nil, err
		}
		if err := binary.Write(&payload, binary.LittleEndian, math.Float64bits(s.Complexity)); err != nil {
			return nil, err
		}
	}

	body := payload.Bytes()
	out := make([]byte, 0, len(snapshotMagic)+1+4+len(body))
	out = append(out, snapshotMagic...)
	out = append(out, snapshotVersion)
	var crc [4]byte
	binary.LittleEndian.PutUint32(crc[:], crc32.ChecksumIEEE(body))
	out = append(out, crc[:]...)
	out = append(out, body...)
	return out, nil
}

// Restore replaces the state with a snapshot, all or nothing.
func (p *TwoPassPlanner) Restore(data []byte) error {
	scenes, stats, minScene, err := decodeSnapshot(data)
	if err != nil {
		return ErrBadSnapshot
	}

	seen := make(map[int64]bool, len(stats))
	var totalBytes int64
	for _, s := range stats {
		seen[s.Frame] = true
		totalBytes += s.Bytes
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.scenes = scenes
	p.stats = stats
	p.seen = seen
	p.counted = int64(len(stats))
	p.bytes = totalBytes
	p.minScene = minScene
	return nil
}

func sceneForFrame(bounds []int64, frame int64) int {
	idx := sort.Search(len(bounds), func(i int) bool {
		return bounds[i] > frame
	}) - 1
	if idx < 0 {
		return 0
	}
	return idx
}

// allocateRates solves for per-scene rates. The weighted part is floored and
// the remaining units are handed out by largest fractional remainder. Scenes
// outside the bounds are fixed and removed from subsequent rebalancing rounds.
func allocateRates(weights []float64, offsets []int64, cfg PlanConfig) ([]int64, []bool) {
	n := len(weights)
	rates := make([]int64, n)
	clamped := make([]bool, n)
	fixed := make([]bool, n)
	remaining := cfg.TotalRate
	active := make([]int, 0, n)
	for i := 0; i < n; i++ {
		active = append(active, i)
	}

	for len(active) > 0 {
		activeWeight := 0.0
		useEqualWeights := true
		for _, idx := range active {
			w := weights[idx]
			if w > 0 && !math.IsInf(w, 0) && !math.IsNaN(w) {
				activeWeight += w
				useEqualWeights = false
			}
		}
		if useEqualWeights || activeWeight <= 0 {
			useEqualWeights = true
			activeWeight = float64(len(active))
		}

		var offsetSum int64
		for _, idx := range active {
			offsetSum += offsets[idx]
		}
		pool := remaining - offsetSum

		weightOf := func(idx int) float64 {
			if useEqualWeights {
				return 1
			}
			w := weights[idx]
			if w <= 0 || math.IsNaN(w) || math.IsInf(w, 0) {
				return 0
			}
			return w
		}

		next := make([]int, 0, len(active))
		clampedThisRound := false
		for _, idx := range active {
			ideal := float64(pool)*weightOf(idx)/activeWeight + float64(offsets[idx])
			switch {
			case ideal < float64(cfg.MinRate):
				rates[idx] = cfg.MinRate
				clamped[idx] = true
				fixed[idx] = true
				remaining -= cfg.MinRate
				clampedThisRound = true
			case ideal > float64(cfg.MaxRate):
				rates[idx] = cfg.MaxRate
				clamped[idx] = true
				fixed[idx] = true
				remaining -= cfg.MaxRate
				clampedThisRound = true
			default:
				next = append(next, idx)
			}
		}
		if clampedThisRound {
			active = next
			continue
		}

		type remainder struct {
			index int
			frac  float64
		}
		remainders := make([]remainder, 0, len(active))
		var assigned int64
		for _, idx := range active {
			share := float64(pool) * weightOf(idx) / activeWeight
			floor := math.Floor(share)
			rates[idx] = int64(floor) + offsets[idx]
			assigned += rates[idx]
			remainders = append(remainders, remainder{
				index: idx,
				frac:  share - floor,
			})
		}

		leftover := remaining - assigned
		sort.Slice(remainders, func(i, j int) bool {
			if remainders[i].frac == remainders[j].frac {
				return remainders[i].index < remainders[j].index
			}
			return remainders[i].frac > remainders[j].frac
		})
		for leftover > 0 {
			progressed := false
			for _, r := range remainders {
				if leftover == 0 {
					break
				}
				if rates[r.index] < cfg.MaxRate {
					rates[r.index]++
					leftover--
					progressed = true
				}
			}
			if !progressed {
				break
			}
		}
		return rates, clamped
	}

	return rates, clamped
}

func decodeSnapshot(data []byte) ([]int64, []FrameStats, int64, error) {
	headerLen := len(snapshotMagic) + 1 + 4
	if len(data) < headerLen || !bytes.Equal(data[:len(snapshotMagic)], snapshotMagic) {
		return nil, nil, 0, ErrBadSnapshot
	}
	pos := len(snapshotMagic)
	if data[pos] != snapshotVersion {
		return nil, nil, 0, ErrBadSnapshot
	}
	pos++
	wantCRC := binary.LittleEndian.Uint32(data[pos : pos+4])
	pos += 4
	body := data[pos:]
	if crc32.ChecksumIEEE(body) != wantCRC {
		return nil, nil, 0, ErrBadSnapshot
	}

	r := bytes.NewReader(body)
	var minScene int64
	if err := binary.Read(r, binary.LittleEndian, &minScene); err != nil {
		return nil, nil, 0, ErrBadSnapshot
	}
	if minScene < 1 {
		return nil, nil, 0, ErrBadSnapshot
	}

	var sceneCount uint32
	if err := binary.Read(r, binary.LittleEndian, &sceneCount); err != nil {
		return nil, nil, 0, ErrBadSnapshot
	}
	if sceneCount == 0 || int64(sceneCount) > int64(r.Len()/8) {
		return nil, nil, 0, ErrBadSnapshot
	}
	scenes := make([]int64, int(sceneCount))
	for i := range scenes {
		if err := binary.Read(r, binary.LittleEndian, &scenes[i]); err != nil {
			return nil, nil, 0, ErrBadSnapshot
		}
		if scenes[i] < 0 || (i == 0 && scenes[i] != 0) ||
			(i > 0 && scenes[i] <= scenes[i-1]) {
			return nil, nil, 0, ErrBadSnapshot
		}
	}

	var statCount uint32
	if err := binary.Read(r, binary.LittleEndian, &statCount); err != nil {
		return nil, nil, 0, ErrBadSnapshot
	}
	if int64(statCount) > int64(r.Len()/24) {
		return nil, nil, 0, ErrBadSnapshot
	}
	stats := make([]FrameStats, int(statCount))
	lastFrame := int64(-1)
	for i := range stats {
		var complexityBits uint64
		if err := binary.Read(r, binary.LittleEndian, &stats[i].Frame); err != nil {
			return nil, nil, 0, ErrBadSnapshot
		}
		if err := binary.Read(r, binary.LittleEndian, &stats[i].Bytes); err != nil {
			return nil, nil, 0, ErrBadSnapshot
		}
		if err := binary.Read(r, binary.LittleEndian, &complexityBits); err != nil {
			return nil, nil, 0, ErrBadSnapshot
		}
		stats[i].Complexity = math.Float64frombits(complexityBits)
		if stats[i].Frame < 0 || stats[i].Frame <= lastFrame ||
			stats[i].Bytes < 0 || stats[i].Complexity < 0 ||
			math.IsNaN(stats[i].Complexity) || math.IsInf(stats[i].Complexity, 0) {
			return nil, nil, 0, ErrBadSnapshot
		}
		lastFrame = stats[i].Frame
	}
	if r.Len() != 0 {
		return nil, nil, 0, ErrBadSnapshot
	}
	return scenes, stats, minScene, nil
}
