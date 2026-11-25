// Package task chains particle sub emitters behind death, collision and expiry
// triggers while keeping the live particle count inside a budget.
package task

import "errors"

// errNotImplemented marks behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

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

// SubEmitterChain owns the emitter table, the trigger graph and the particle
// bookkeeping.
type SubEmitterChain struct {
	limits ChainLimits
}

// NewSubEmitterChain builds a chain and normalises zero limits to defaults.
func NewSubEmitterChain(limits ChainLimits) *SubEmitterChain {
	return &SubEmitterChain{limits: limits}
}

// Register adds one emitter definition.
func (c *SubEmitterChain) Register(emitterID string, def EmitterDef) error {
	return errNotImplemented
}

// Link declares one parent to child trigger edge.
func (c *SubEmitterChain) Link(parentID, childID, trigger string) error {
	return errNotImplemented
}

// Links lists the trigger edges declared on one parent, in declaration order.
func (c *SubEmitterChain) Links(emitterID string) []LinkDef {
	return nil
}

// Emit spawns particles of one emitter directly.
func (c *SubEmitterChain) Emit(emitterID string, count int) error {
	return errNotImplemented
}

// Step advances lifetimes, collects triggers and spawns the children.
func (c *SubEmitterChain) Step(dt float64) StepReport {
	return StepReport{}
}

// Alive reports the live particle count of one emitter.
func (c *SubEmitterChain) Alive(emitterID string) int {
	return 0
}

// PendingTriggers reports the queued child emissions of one emitter.
func (c *SubEmitterChain) PendingTriggers(emitterID string) int {
	return 0
}

// Frame reports how many steps have run so far.
func (c *SubEmitterChain) Frame() int {
	return 0
}

// Capacity reports the registered capacity of one emitter.
func (c *SubEmitterChain) Capacity(emitterID string) (int, bool) {
	return 0, false
}

// Snapshot serialises emitters, links, particles and queued triggers.
func (c *SubEmitterChain) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (c *SubEmitterChain) Restore(data []byte) error {
	return errNotImplemented
}
