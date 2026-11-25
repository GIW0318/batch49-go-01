// Package task chains particle sub emitters behind death, collision and expiry
// triggers while keeping the live particle count inside a budget.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
)

// Supported trigger kinds.
const (
	TriggerDeath     = "death"
	TriggerCollision = "collision"
	TriggerExpiry    = "expiry"
)

// Limits applied when a caller leaves a field at zero.
const (
	DefaultMaxDepth     = 4
	DefaultMaxPerFrame  = 64
	DefaultTotalBudget  = 4096
	DefaultQueuePerNode = 256
	DefaultMaxEmitters  = 4096

	defaultMaxLinks int    = 4096
	defaultRNGSeed  uint64 = 0x9e3779b97f4a7c15

	snapshotMagic   = "gsb-subemitter-chain-snapshot-v1"
	snapshotVersion = 1
)

// Errors returned by the chain.
var (
	ErrInvalidEmitter   = errors.New("emitter definition is invalid")
	ErrDuplicateEmitter = errors.New("emitter is already registered")
	ErrUnknownEmitter   = errors.New("emitter is not registered")
	ErrUnknownTrigger   = errors.New("trigger kind is not supported")
	ErrCycle            = errors.New("link would create a cycle")
	ErrOverCapacity     = errors.New("emitter capacity would be exceeded")
	ErrBudgetExceeded   = errors.New("particle budget would be exceeded")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
	ErrTooManyEmitters  = errors.New("emitter registry count limit exceeded")
	ErrTooManyLinks     = errors.New("link table count limit exceeded")
)

// EmitterDef is the spawn recipe of one emitter.
type EmitterDef struct {
	Capacity        int     `json:"capacity"`
	Burst           int     `json:"burst"`
	Lifetime        float64 `json:"lifetime"`
	Priority        int     `json:"priority"`
	InheritPosition float64 `json:"inherit_position"`
	InheritVelocity float64 `json:"inherit_velocity"`
}

// ChainLimits bounds the recursive chain and the per frame emission.
type ChainLimits struct {
	MaxDepth    int `json:"max_depth"`
	MaxPerFrame int `json:"max_per_frame"`
	TotalBudget int `json:"total_budget"`
	DelayFrames int `json:"delay_frames"`
	MaxLinks    int `json:"max_links"`
}

// LinkDef is one declared parent to child trigger edge.
type LinkDef struct {
	ParentID string `json:"parent_id"`
	ChildID  string `json:"child_id"`
	Trigger  string `json:"trigger"`
}

// StepReport summarises one simulation step.
type StepReport struct {
	Frame        int `json:"frame"`
	Spawned      int `json:"spawned"`
	Expired      int `json:"expired"`
	ChildFired   int `json:"child_fired"`
	Merged       int `json:"merged"`
	Dropped      int `json:"dropped"`
	DepthLimited int `json:"depth_limited"`
	Alive        int `json:"alive"`
}

// particle is one live particle. Transforms are kept so independent
// position/velocity inheritance coefficients can be applied to children.
type particle struct {
	Age   float64 `json:"age"`
	Depth int     `json:"depth"`
	PX    float64 `json:"px"`
	PY    float64 `json:"py"`
	VX    float64 `json:"vx"`
	VY    float64 `json:"vy"`
}

// emitterRuntime owns the definition and live particles of one emitter.
type emitterRuntime struct {
	Def       EmitterDef `json:"def"`
	Particles []particle `json:"particles"`
}

// trigger is one queued parent particle to child emission request. Entries are
// independent of the parent particle once queued: destroying the parent never
// cancels a queued child emission.
type trigger struct {
	Target   string  `json:"target"`
	Depth    int     `json:"depth"`
	DueFrame int     `json:"due_frame"`
	PX       float64 `json:"px"`
	PY       float64 `json:"py"`
	VX       float64 `json:"vx"`
	VY       float64 `json:"vy"`
}

// SubEmitterChain owns the emitter table, the trigger graph and the particle
// bookkeeping.
type SubEmitterChain struct {
	mu        sync.Mutex
	limits    ChainLimits
	emitters  map[string]*emitterRuntime
	order     []string
	links     map[string][]LinkDef
	queue     []trigger
	frame     int
	rngState  uint64
	linkCount int
}

// NewSubEmitterChain builds a chain and normalises zero limits to defaults.
func NewSubEmitterChain(limits ChainLimits) *SubEmitterChain {
	normalised := limits
	if normalised.MaxDepth <= 0 {
		normalised.MaxDepth = DefaultMaxDepth
	}
	if normalised.MaxPerFrame <= 0 {
		normalised.MaxPerFrame = DefaultMaxPerFrame
	}
	if normalised.TotalBudget <= 0 {
		normalised.TotalBudget = DefaultTotalBudget
	}
	if normalised.DelayFrames < 0 {
		normalised.DelayFrames = 0
	}
	if normalised.MaxLinks <= 0 {
		normalised.MaxLinks = defaultMaxLinks
	}
	return &SubEmitterChain{
		limits:   normalised,
		emitters: make(map[string]*emitterRuntime),
		links:    make(map[string][]LinkDef),
		rngState: defaultRNGSeed,
	}
}

// Register adds one emitter definition.
func (c *SubEmitterChain) Register(emitterID string, def EmitterDef) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if emitterID == "" || def.Capacity <= 0 || def.Burst <= 0 || def.Lifetime <= 0 {
		return ErrInvalidEmitter
	}
	if _, exists := c.emitters[emitterID]; exists {
		return ErrDuplicateEmitter
	}
	if len(c.order) >= DefaultMaxEmitters {
		return ErrTooManyEmitters
	}
	c.emitters[emitterID] = &emitterRuntime{Def: def}
	c.order = append(c.order, emitterID)
	c.links[emitterID] = nil
	return nil
}

// Link declares one parent to child trigger edge.
func (c *SubEmitterChain) Link(parentID, childID, triggerKind string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, ok := c.emitters[parentID]; !ok {
		return ErrUnknownEmitter
	}
	if _, ok := c.emitters[childID]; !ok {
		return ErrUnknownEmitter
	}
	if !validTrigger(triggerKind) {
		return ErrUnknownTrigger
	}
	if parentID == childID || c.reachesLocked(childID, parentID) {
		return ErrCycle
	}
	if c.linkCount >= c.limits.MaxLinks {
		return ErrTooManyLinks
	}
	c.links[parentID] = append(c.links[parentID], LinkDef{
		ParentID: parentID,
		ChildID:  childID,
		Trigger:  triggerKind,
	})
	c.linkCount++
	return nil
}

// Links lists the trigger edges declared on one parent, in declaration order.
func (c *SubEmitterChain) Links(emitterID string) []LinkDef {
	c.mu.Lock()
	defer c.mu.Unlock()

	edges := c.links[emitterID]
	if len(edges) == 0 {
		return nil
	}
	out := make([]LinkDef, len(edges))
	copy(out, edges)
	return out
}

// Emit spawns particles of one emitter directly.
func (c *SubEmitterChain) Emit(emitterID string, count int) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	emitter, ok := c.emitters[emitterID]
	if !ok {
		return ErrUnknownEmitter
	}
	if count <= 0 {
		return nil
	}
	// Capacity is per emitter and takes precedence over the chain budget when
	// both would be exceeded.
	if len(emitter.Particles)+count > emitter.Def.Capacity {
		return ErrOverCapacity
	}
	if c.totalAliveLocked()+count > c.limits.TotalBudget {
		return ErrBudgetExceeded
	}
	for i := 0; i < count; i++ {
		emitter.Particles = append(emitter.Particles, particle{})
	}
	return nil
}

// Step advances lifetimes, collects triggers and spawns the children.
func (c *SubEmitterChain) Step(dt float64) StepReport {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.frame++
	report := StepReport{Frame: c.frame}

	// Phase 1: age existing particles. Particles that reach their lifetime are
	// removed in this step (and only this step counts them as expired) and
	// enqueue their declared triggers. Children spawned later in this step are
	// not aged until the next step.
	if stepDt := ageDelta(dt); stepDt > 0 {
		for _, parentID := range c.order {
			emitter := c.emitters[parentID]
			survivors := emitter.Particles[:0]
			for _, p := range emitter.Particles {
				p.Age += stepDt
				if p.Age >= emitter.Def.Lifetime {
					report.Expired++
					c.enqueueLifetimeTriggersLocked(parentID, p)
					continue
				}
				survivors = append(survivors, p)
			}
			emitter.Particles = survivors
		}
	}

	// Phase 2: dequeue triggers whose delay window has elapsed. Every due entry
	// leaves the queue exactly once.
	due := make([]trigger, 0)
	pending := c.queue[:0]
	for _, t := range c.queue {
		if t.DueFrame <= c.frame {
			due = append(due, t)
		} else {
			pending = append(pending, t)
		}
	}
	c.queue = pending

	// Phase 3: settle dequeued entries in declaration order. Depth blocked
	// entries are counted separately; accepted entries targeting one child in
	// the same frame merge into a single emission request.
	groups := make(map[string]*spawnBatch)
	targetOrder := make([]string, 0)
	for _, t := range due {
		if t.Depth > c.limits.MaxDepth {
			report.DepthLimited++
			continue
		}
		report.ChildFired++
		batch, ok := groups[t.Target]
		if !ok {
			batch = &spawnBatch{target: t.Target}
			groups[t.Target] = batch
			targetOrder = append(targetOrder, t.Target)
		} else {
			// Each extra trigger folded into the existing batch is one merge.
			report.Merged++
		}
		batch.entries = append(batch.entries, t)
	}

	// Phase 4: emit merged batches by emitter priority (high to low). Equal
	// priority keeps the stable first-seen declaration order.
	sort.SliceStable(targetOrder, func(i, j int) bool {
		return c.emitters[targetOrder[i]].Def.Priority >
			c.emitters[targetOrder[j]].Def.Priority
	})

	for _, targetID := range targetOrder {
		batch := groups[targetID]
		child := c.emitters[targetID]

		want := 0
		for range batch.entries {
			want += child.Def.Burst
		}

		allowed := want
		if head := child.Def.Capacity - len(child.Particles); head < allowed {
			allowed = head
		}
		if head := c.limits.TotalBudget - c.totalAliveLocked(); head < allowed {
			allowed = head
		}
		if head := c.limits.MaxPerFrame - report.Spawned; head < allowed {
			allowed = head
		}
		if allowed < 0 {
			allowed = 0
		}

		left := allowed
		for _, t := range batch.entries {
			if left <= 0 {
				break
			}
			count := child.Def.Burst
			if count > left {
				count = left
			}
			for i := 0; i < count; i++ {
				p := inheritParticle(t, child.Def)
				c.jitterVelocityLocked(&p)
				child.Particles = append(child.Particles, p)
			}
			left -= count
		}

		report.Spawned += allowed
		report.Dropped += want - allowed
	}

	report.Alive = c.totalAliveLocked()
	return report
}

// spawnBatch is a same frame merge of every trigger targeting one child.
type spawnBatch struct {
	target  string
	entries []trigger
}

// Alive reports the live particle count of one emitter.
func (c *SubEmitterChain) Alive(emitterID string) int {
	c.mu.Lock()
	defer c.mu.Unlock()

	emitter, ok := c.emitters[emitterID]
	if !ok {
		return 0
	}
	return len(emitter.Particles)
}

// PendingTriggers reports the queued child emissions targeting one emitter.
func (c *SubEmitterChain) PendingTriggers(emitterID string) int {
	c.mu.Lock()
	defer c.mu.Unlock()

	n := 0
	for _, t := range c.queue {
		if t.Target == emitterID {
			n++
		}
	}
	return n
}

// Frame reports how many steps have run so far.
func (c *SubEmitterChain) Frame() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.frame
}

// Capacity reports the registered capacity of one emitter.
func (c *SubEmitterChain) Capacity(emitterID string) (int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	emitter, ok := c.emitters[emitterID]
	if !ok {
		return 0, false
	}
	return emitter.Def.Capacity, true
}

// Collide records count collision hits on one emitter and enqueues the declared
// collision sub emitters. It is the runtime entry point for the collision
// trigger kind, which Step cannot synthesise from lifetimes alone.
func (c *SubEmitterChain) Collide(emitterID string, count int) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	emitter, ok := c.emitters[emitterID]
	if !ok {
		return ErrUnknownEmitter
	}
	if count <= 0 {
		return nil
	}
	hits := count
	if hits > len(emitter.Particles) {
		hits = len(emitter.Particles)
	}
	for i := 0; i < hits; i++ {
		p := emitter.Particles[i]
		c.enqueueTriggerKindLocked(emitterID, p, TriggerCollision)
	}
	return nil
}

// Snapshot serialises emitters, links, particles and queued triggers.
func (c *SubEmitterChain) Snapshot() ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	state := snapshotState{
		Magic:    snapshotMagic,
		Version:  snapshotVersion,
		Frame:    c.frame,
		RNGState: c.rngState,
		Limits:   c.limits,
		Order:    append([]string(nil), c.order...),
		Emitters: make(map[string]*emitterRuntime, len(c.emitters)),
		Links:    make(map[string][]LinkDef, len(c.links)),
		Queue:    append([]trigger(nil), c.queue...),
	}
	for id, e := range c.emitters {
		state.Emitters[id] = &emitterRuntime{
			Def:       e.Def,
			Particles: append([]particle(nil), e.Particles...),
		}
	}
	for parent, edges := range c.links {
		state.Links[parent] = append([]LinkDef(nil), edges...)
	}

	body, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	payload := snapshotEnvelope{
		Magic:   snapshotMagic,
		Version: snapshotVersion,
		Sum:     hex.EncodeToString(sum[:]),
		Body:    body,
	}
	return json.Marshal(payload)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (c *SubEmitterChain) Restore(data []byte) error {
	state, err := decodeSnapshot(data)
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.limits = state.Limits
	c.frame = state.Frame
	c.rngState = state.RNGState
	if c.rngState == 0 {
		c.rngState = defaultRNGSeed
	}
	c.order = append([]string(nil), state.Order...)
	c.queue = append([]trigger(nil), state.Queue...)
	c.emitters = make(map[string]*emitterRuntime, len(state.Emitters))
	for id, e := range state.Emitters {
		if e == nil {
			e = &emitterRuntime{}
		}
		c.emitters[id] = &emitterRuntime{
			Def:       e.Def,
			Particles: append([]particle(nil), e.Particles...),
		}
	}
	c.links = make(map[string][]LinkDef, len(state.Links))
	c.linkCount = 0
	for parent, edges := range state.Links {
		c.links[parent] = append([]LinkDef(nil), edges...)
		c.linkCount += len(edges)
	}
	return nil
}

// --- helpers ---------------------------------------------------------------

func validTrigger(kind string) bool {
	return kind == TriggerDeath || kind == TriggerCollision || kind == TriggerExpiry
}

// minFrameDelta is the minimum lifetime one advanced frame accounts for. The
// caller advances the chain exactly once per frame, so a positive but tiny dt
// still settles a whole frame boundary.
const minFrameDelta = 0.5

// ageDelta normalises the delta consumed while ageing particles in one Step.
// Non positive deltas do not age particles; a positive sub-frame delta still
// crosses one frame boundary.
func ageDelta(dt float64) float64 {
	if dt <= 0 {
		return 0
	}
	if dt < minFrameDelta {
		return minFrameDelta
	}
	return dt
}

// reachesLocked reports whether a directed path exists from start to target.
func (c *SubEmitterChain) reachesLocked(start, target string) bool {
	visited := make(map[string]struct{})
	stack := []string{start}
	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if node == target {
			return true
		}
		if _, seen := visited[node]; seen {
			continue
		}
		visited[node] = struct{}{}
		for _, edge := range c.links[node] {
			stack = append(stack, edge.ChildID)
		}
	}
	return false
}

func (c *SubEmitterChain) totalAliveLocked() int {
	total := 0
	for _, id := range c.order {
		if e, ok := c.emitters[id]; ok {
			total += len(e.Particles)
		}
	}
	return total
}

// enqueueLifetimeTriggersLocked handles the lifetime boundary. A particle whose
// lifetime elapses both dies (death hooks) and expires (expiry hooks).
func (c *SubEmitterChain) enqueueLifetimeTriggersLocked(parentID string, p particle) {
	for _, edge := range c.links[parentID] {
		if edge.Trigger != TriggerDeath && edge.Trigger != TriggerExpiry {
			continue
		}
		c.appendTriggerLocked(edge.ChildID, p.Depth+1, p)
	}
}

func (c *SubEmitterChain) enqueueTriggerKindLocked(parentID string, p particle, kind string) {
	for _, edge := range c.links[parentID] {
		if edge.Trigger != kind {
			continue
		}
		c.appendTriggerLocked(edge.ChildID, p.Depth+1, p)
	}
}

func (c *SubEmitterChain) appendTriggerLocked(target string, depth int, p particle) {
	c.queue = append(c.queue, trigger{
		Target:   target,
		Depth:    depth,
		DueFrame: c.frame + c.limits.DelayFrames,
		PX:       p.PX,
		PY:       p.PY,
		VX:       p.VX,
		VY:       p.VY,
	})
}

// inheritParticle applies the two independent inheritance coefficients.
func inheritParticle(t trigger, def EmitterDef) particle {
	return particle{
		Depth: t.Depth,
		PX:    t.PX * def.InheritPosition,
		PY:    t.PY * def.InheritPosition,
		VX:    t.VX * def.InheritVelocity,
		VY:    t.VY * def.InheritVelocity,
	}
}

// randUintLocked is a small deterministic xorshift stream. Its state is part of
// the snapshot so a restored chain continues the same random sequence.
func (c *SubEmitterChain) randUintLocked() uint64 {
	x := c.rngState
	if x == 0 {
		x = defaultRNGSeed
	}
	x ^= x << 13
	x ^= x >> 7
	x ^= x << 17
	c.rngState = x
	return x
}

func (c *SubEmitterChain) randFloatLocked() float64 {
	return float64(c.randUintLocked()>>11) / float64(uint64(1)<<53)
}

// jitterVelocityLocked adds deterministic tiny spread so the random sequence is
// genuinely advanced on spawn without affecting any count.
func (c *SubEmitterChain) jitterVelocityLocked(p *particle) {
	p.VX += (c.randFloatLocked() - 0.5) * 0.01
	p.VY += (c.randFloatLocked() - 0.5) * 0.01
}

// --- snapshot wire format --------------------------------------------------

type snapshotState struct {
	Magic    string                     `json:"magic"`
	Version  int                        `json:"version"`
	Frame    int                        `json:"frame"`
	RNGState uint64                     `json:"rng_state"`
	Limits   ChainLimits                `json:"limits"`
	Order    []string                   `json:"order"`
	Emitters map[string]*emitterRuntime `json:"emitters"`
	Links    map[string][]LinkDef       `json:"links"`
	Queue    []trigger                  `json:"queue"`
}

type snapshotEnvelope struct {
	Magic   string          `json:"magic"`
	Version int             `json:"version"`
	Sum     string          `json:"sum"`
	Body    json.RawMessage `json:"body"`
}

func decodeSnapshot(data []byte) (*snapshotState, error) {
	if len(data) == 0 {
		return nil, ErrBadSnapshot
	}
	var envelope snapshotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	if envelope.Magic != snapshotMagic || envelope.Version != snapshotVersion ||
		len(envelope.Body) == 0 || envelope.Sum == "" {
		return nil, ErrBadSnapshot
	}

	var state snapshotState
	if err := json.Unmarshal(envelope.Body, &state); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	canonical, err := json.Marshal(&state)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	sum := sha256.Sum256(canonical)
	if hex.EncodeToString(sum[:]) != envelope.Sum {
		return nil, ErrBadSnapshot
	}
	if err := validateSnapshot(&state); err != nil {
		return nil, err
	}
	return &state, nil
}

func validateSnapshot(state *snapshotState) error {
	if state.Magic != snapshotMagic || state.Version != snapshotVersion {
		return ErrBadSnapshot
	}
	if state.Frame < 0 {
		return ErrBadSnapshot
	}
	seen := make(map[string]struct{}, len(state.Order))
	for _, id := range state.Order {
		e, ok := state.Emitters[id]
		if !ok || e == nil || id == "" {
			return ErrBadSnapshot
		}
		if _, dup := seen[id]; dup {
			return ErrBadSnapshot
		}
		seen[id] = struct{}{}
		if e.Def.Capacity <= 0 || e.Def.Burst <= 0 || e.Def.Lifetime <= 0 {
			return ErrBadSnapshot
		}
		for i := range e.Particles {
			if e.Particles[i].Age < 0 || e.Particles[i].Depth < 0 {
				return ErrBadSnapshot
			}
		}
	}
	for parent, edges := range state.Links {
		if _, ok := state.Emitters[parent]; !ok {
			return ErrBadSnapshot
		}
		for _, edge := range edges {
			if _, ok := state.Emitters[edge.ChildID]; !ok {
				return ErrBadSnapshot
			}
			if !validTrigger(edge.Trigger) {
				return ErrBadSnapshot
			}
		}
	}
	for _, t := range state.Queue {
		if _, ok := state.Emitters[t.Target]; !ok {
			return ErrBadSnapshot
		}
		if t.Depth < 0 {
			return ErrBadSnapshot
		}
	}
	return nil
}
