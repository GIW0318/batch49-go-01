// Package task runs the daily schedule state machine of town NPCs, including
// place capacity, waiting queues and interruptions.
package task

import "errors"

// errNotImplemented marks behaviour that still has to be written.
var errNotImplemented = errors.New("not implemented")

// Errors reported by the schedule guard.
var (
	ErrBlankNPC         = errors.New("npc id is required")
	ErrBadSchedule      = errors.New("schedule definition is invalid")
	ErrOverlap          = errors.New("schedule items overlap")
	ErrBadReason        = errors.New("interrupt reason is required")
	ErrNotInterrupted   = errors.New("npc is not interrupted")
	ErrStillInterrupted = errors.New("interrupt window has not ended")
	ErrUnknownNPC       = errors.New("npc has no schedule")
	ErrBadRestore       = errors.New("snapshot payload is invalid")
)

// ScheduleItem is one left closed right open window of a daily schedule.
type ScheduleItem struct {
	SlotID   string
	StartMin int
	EndMin   int
	Place    string
	Behavior string
}

// Options tunes place capacity.
type Options struct {
	// DefaultPlaceCapacity applies to places without an explicit entry.
	DefaultPlaceCapacity int
	// Capacities overrides the capacity of named places.
	Capacities map[string]int
}

// ScheduleState is the observable state of one NPC.
type ScheduleState struct {
	NPCID       string
	SlotID      string
	Place       string
	Behavior    string
	Since       int64
	Interrupted bool
	Waiting     bool
}

// NpcScheduleGuard owns schedules, per NPC state, place occupancy and queues.
type NpcScheduleGuard struct {
	opts Options
}

// NewNpcScheduleGuard builds an empty guard.
func NewNpcScheduleGuard(opts Options) *NpcScheduleGuard {
	return &NpcScheduleGuard{opts: opts}
}

// DefineSchedule replaces the daily schedule of one NPC.
func (g *NpcScheduleGuard) DefineSchedule(npcID string, items []ScheduleItem) error {
	return errNotImplemented
}

// Tick advances one NPC to the given wall clock moment.
func (g *NpcScheduleGuard) Tick(npcID string, at int64) (ScheduleState, error) {
	return ScheduleState{}, errNotImplemented
}

// Interrupt pauses the schedule of one NPC until the given moment.
func (g *NpcScheduleGuard) Interrupt(npcID, reason string, until int64) error {
	return errNotImplemented
}

// Resume ends an interruption and re-enters the schedule.
func (g *NpcScheduleGuard) Resume(npcID string, at int64) (ScheduleState, error) {
	return ScheduleState{}, errNotImplemented
}

// State reports the current state of one NPC.
func (g *NpcScheduleGuard) State(npcID string) (ScheduleState, bool) {
	return ScheduleState{}, false
}

// Occupancy reports how many NPCs occupy a place right now.
func (g *NpcScheduleGuard) Occupancy(placeID string) int {
	return 0
}

// Waiting reports how many NPCs queue for a place right now.
func (g *NpcScheduleGuard) Waiting(placeID string) int {
	return 0
}

// Release makes one NPC leave its place and admits the first waiter.
func (g *NpcScheduleGuard) Release(npcID string) error {
	return errNotImplemented
}

// Snapshot serialises schedules, states, occupancy and queues.
func (g *NpcScheduleGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole guard state with a snapshot.
func (g *NpcScheduleGuard) Restore(data []byte) error {
	return errNotImplemented
}
