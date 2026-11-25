// Package task runs the daily schedule state machine of town NPCs, including
// place capacity, waiting queues and interruptions.
package task

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
)

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

const (
	minuteMillis    = int64(60_000)
	dayMinutes      = 1440
	snapshotVersion = 1
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
	opts      Options
	mu        sync.Mutex
	npcs      map[string]*npcRuntime
	occupancy map[string]int
	queues    map[string][]string
}

type npcRuntime struct {
	schedule        []ScheduleItem
	state           ScheduleState
	heldPlace       string
	queuedPlace     string
	entered         bool
	interruptUntil  int64
	interruptReason string
}

type snapshotData struct {
	Version   int                    `json:"version"`
	NPCs      map[string]npcSnapshot `json:"npcs"`
	Occupancy map[string]int         `json:"occupancy"`
	Queues    map[string][]string    `json:"queues"`
}

type npcSnapshot struct {
	Schedule        []ScheduleItem `json:"schedule"`
	State           ScheduleState  `json:"state"`
	HeldPlace       string         `json:"held_place"`
	QueuedPlace     string         `json:"queued_place"`
	Entered         bool           `json:"entered"`
	InterruptUntil  int64          `json:"interrupt_until"`
	InterruptReason string         `json:"interrupt_reason,omitempty"`
}

// NewNpcScheduleGuard builds an empty guard.
func NewNpcScheduleGuard(opts Options) *NpcScheduleGuard {
	if opts.Capacities != nil {
		capacities := make(map[string]int, len(opts.Capacities))
		for placeID, capacity := range opts.Capacities {
			capacities[placeID] = capacity
		}
		opts.Capacities = capacities
	}

	return &NpcScheduleGuard{
		opts:      opts,
		npcs:      make(map[string]*npcRuntime),
		occupancy: make(map[string]int),
		queues:    make(map[string][]string),
	}
}

// DefineSchedule replaces the daily schedule of one NPC.
func (g *NpcScheduleGuard) DefineSchedule(npcID string, items []ScheduleItem) error {
	schedule, err := prepareSchedule(npcID, items)
	if err != nil {
		return err
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.ensureMapsLocked()

	if previous := g.npcs[npcID]; previous != nil {
		g.removeFromQueueLocked(previous)
		g.releaseHeldPlaceLocked(previous)
	}

	g.npcs[npcID] = &npcRuntime{
		schedule: schedule,
		state:    ScheduleState{NPCID: npcID},
	}
	return nil
}

// Tick advances one NPC to the given wall clock moment.
func (g *NpcScheduleGuard) Tick(npcID string, at int64) (ScheduleState, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	rt, ok := g.npcs[npcID]
	if !ok {
		return ScheduleState{}, ErrUnknownNPC
	}
	if rt.state.Interrupted {
		return rt.state, nil
	}

	return g.advanceLocked(rt, npcID, at), nil
}

// Interrupt pauses the schedule of one NPC until the given moment.
func (g *NpcScheduleGuard) Interrupt(npcID, reason string, until int64) error {
	if strings.TrimSpace(reason) == "" {
		return ErrBadReason
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	rt, ok := g.npcs[npcID]
	if !ok {
		return ErrUnknownNPC
	}

	g.removeFromQueueLocked(rt)
	rt.state.Waiting = false
	g.releaseHeldPlaceLocked(rt)

	rt.state.Interrupted = true
	rt.entered = false
	rt.interruptUntil = until
	rt.interruptReason = reason
	return nil
}

// Resume ends an interruption and re-enters the schedule.
func (g *NpcScheduleGuard) Resume(npcID string, at int64) (ScheduleState, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	rt, ok := g.npcs[npcID]
	if !ok {
		return ScheduleState{}, ErrUnknownNPC
	}
	if !rt.state.Interrupted {
		return ScheduleState{}, ErrNotInterrupted
	}
	if at < rt.interruptUntil {
		return ScheduleState{}, ErrStillInterrupted
	}

	rt.state.Interrupted = false
	rt.interruptUntil = 0
	rt.interruptReason = ""
	rt.entered = false

	return g.advanceLocked(rt, npcID, at), nil
}

// State reports the current state of one NPC.
func (g *NpcScheduleGuard) State(npcID string) (ScheduleState, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	rt, ok := g.npcs[npcID]
	if !ok {
		return ScheduleState{}, false
	}
	return rt.state, true
}

// Occupancy reports how many NPCs occupy a place right now.
func (g *NpcScheduleGuard) Occupancy(placeID string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.occupancy[placeID]
}

// Waiting reports how many NPCs queue for a place right now.
func (g *NpcScheduleGuard) Waiting(placeID string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.queues[placeID])
}

// Release makes one NPC leave its place and admits the first waiter.
func (g *NpcScheduleGuard) Release(npcID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	rt, ok := g.npcs[npcID]
	if !ok {
		return ErrUnknownNPC
	}

	interrupted := rt.state.Interrupted
	until := rt.interruptUntil
	reason := rt.interruptReason

	g.removeFromQueueLocked(rt)
	rt.state.Waiting = false
	g.releaseHeldPlaceLocked(rt)

	rt.state = ScheduleState{NPCID: npcID, Interrupted: interrupted}
	rt.entered = false
	rt.interruptUntil = until
	rt.interruptReason = reason
	return nil
}

// Snapshot serialises schedules, states, occupancy and queues.
func (g *NpcScheduleGuard) Snapshot() ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	snapshot := snapshotData{
		Version:   snapshotVersion,
		NPCs:      make(map[string]npcSnapshot, len(g.npcs)),
		Occupancy: make(map[string]int, len(g.occupancy)),
		Queues:    make(map[string][]string, len(g.queues)),
	}

	for npcID, rt := range g.npcs {
		schedule := append([]ScheduleItem(nil), rt.schedule...)
		snapshot.NPCs[npcID] = npcSnapshot{
			Schedule:        schedule,
			State:           rt.state,
			HeldPlace:       rt.heldPlace,
			QueuedPlace:     rt.queuedPlace,
			Entered:         rt.entered,
			InterruptUntil:  rt.interruptUntil,
			InterruptReason: rt.interruptReason,
		}
	}
	for placeID, count := range g.occupancy {
		snapshot.Occupancy[placeID] = count
	}
	for placeID, queue := range g.queues {
		snapshot.Queues[placeID] = append([]string(nil), queue...)
	}

	data, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("encode schedule snapshot: %w", err)
	}
	return data, nil
}

// Restore replaces the whole guard state with a snapshot.
func (g *NpcScheduleGuard) Restore(data []byte) error {
	next, occupancy, queues, err := decodeSnapshot(data)
	if err != nil {
		return ErrBadRestore
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.npcs = next
	g.occupancy = occupancy
	g.queues = queues
	g.ensureMapsLocked()
	return nil
}

func (g *NpcScheduleGuard) ensureMapsLocked() {
	if g.npcs == nil {
		g.npcs = make(map[string]*npcRuntime)
	}
	if g.occupancy == nil {
		g.occupancy = make(map[string]int)
	}
	if g.queues == nil {
		g.queues = make(map[string][]string)
	}
}

func prepareSchedule(npcID string, items []ScheduleItem) ([]ScheduleItem, error) {
	if strings.TrimSpace(npcID) == "" {
		return nil, ErrBlankNPC
	}
	if len(items) == 0 {
		return nil, ErrBadSchedule
	}

	for _, item := range items {
		if strings.TrimSpace(item.SlotID) == "" ||
			strings.TrimSpace(item.Place) == "" ||
			strings.TrimSpace(item.Behavior) == "" {
			return nil, ErrBadSchedule
		}
		if item.StartMin < 0 || item.StartMin >= dayMinutes {
			return nil, ErrBadSchedule
		}
		if item.EndMin < 0 || item.EndMin > dayMinutes || item.StartMin == item.EndMin {
			return nil, ErrBadSchedule
		}
	}

	prepared := append([]ScheduleItem(nil), items...)
	sort.SliceStable(prepared, func(i, j int) bool {
		return prepared[i].StartMin < prepared[j].StartMin
	})

	occupiedMinutes := [dayMinutes]bool{}
	for _, item := range prepared {
		for minute := 0; minute < dayMinutes; minute++ {
			if !scheduleItemContains(item, minute) {
				continue
			}
			if occupiedMinutes[minute] {
				return nil, ErrOverlap
			}
			occupiedMinutes[minute] = true
		}
	}

	return prepared, nil
}

func scheduleItemContains(item ScheduleItem, minute int) bool {
	if item.StartMin < item.EndMin {
		return minute >= item.StartMin && minute < item.EndMin
	}
	return minute >= item.StartMin || minute < item.EndMin
}

func minuteOfDay(at int64) int {
	minute := (at / minuteMillis) % dayMinutes
	if minute < 0 {
		minute += dayMinutes
	}
	return int(minute)
}

func (g *NpcScheduleGuard) capacityLocked(placeID string) int {
	if capacity, ok := g.opts.Capacities[placeID]; ok {
		return capacity
	}
	return g.opts.DefaultPlaceCapacity
}

func (g *NpcScheduleGuard) advanceLocked(rt *npcRuntime, npcID string, at int64) ScheduleState {
	minute := minuteOfDay(at)
	item, found := findScheduleItem(rt.schedule, minute)

	if !found {
		g.removeFromQueueLocked(rt)
		g.releaseHeldPlaceLocked(rt)
		rt.state = ScheduleState{NPCID: npcID, Since: at}
		rt.entered = false
		return rt.state
	}

	sameSlot := rt.entered && rt.state.SlotID == item.SlotID
	rt.state.SlotID = item.SlotID
	rt.state.Place = item.Place
	rt.state.Behavior = item.Behavior
	rt.state.Interrupted = false
	if !sameSlot {
		rt.state.Since = at
		rt.entered = true
	}

	g.requestPlaceLocked(rt, npcID, item.Place)
	return rt.state
}

func findScheduleItem(items []ScheduleItem, minute int) (ScheduleItem, bool) {
	for _, item := range items {
		if scheduleItemContains(item, minute) {
			return item, true
		}
	}
	return ScheduleItem{}, false
}

func (g *NpcScheduleGuard) requestPlaceLocked(rt *npcRuntime, npcID, placeID string) {
	if rt.heldPlace == placeID {
		if rt.queuedPlace != "" {
			g.removeFromQueueLocked(rt)
		}
		rt.queuedPlace = ""
		rt.state.Waiting = false
		return
	}

	if rt.queuedPlace == placeID {
		rt.state.Waiting = true
		return
	}

	if rt.queuedPlace != "" {
		g.removeFromQueueLocked(rt)
	}

	if g.occupancy[placeID] < g.capacityLocked(placeID) {
		oldPlace := rt.heldPlace
		if oldPlace != "" {
			g.occupancy[oldPlace]--
			if g.occupancy[oldPlace] <= 0 {
				delete(g.occupancy, oldPlace)
			}
			rt.heldPlace = ""
		}

		g.occupancy[placeID]++
		rt.heldPlace = placeID
		rt.queuedPlace = ""
		rt.state.Waiting = false

		if oldPlace != "" {
			g.admitNextLocked(oldPlace)
		}
		return
	}

	g.queues[placeID] = append(g.queues[placeID], npcID)
	rt.queuedPlace = placeID
	rt.state.Waiting = true
}

func (g *NpcScheduleGuard) releaseHeldPlaceLocked(rt *npcRuntime) {
	placeID := rt.heldPlace
	if placeID == "" {
		return
	}

	g.occupancy[placeID]--
	if g.occupancy[placeID] <= 0 {
		delete(g.occupancy, placeID)
	}
	rt.heldPlace = ""
	g.admitNextLocked(placeID)
}

func (g *NpcScheduleGuard) removeFromQueueLocked(rt *npcRuntime) {
	placeID := rt.queuedPlace
	if placeID == "" {
		return
	}

	queue := g.queues[placeID]
	npcID := rt.state.NPCID
	for index, candidate := range queue {
		if candidate != npcID {
			continue
		}
		g.queues[placeID] = append(queue[:index], queue[index+1:]...)
		if len(g.queues[placeID]) == 0 {
			delete(g.queues, placeID)
		}
		break
	}
	rt.queuedPlace = ""
	rt.state.Waiting = false
}

func (g *NpcScheduleGuard) admitNextLocked(placeID string) {
	if g.occupancy[placeID] >= g.capacityLocked(placeID) {
		return
	}

	queue := g.queues[placeID]
	for len(queue) > 0 {
		npcID := queue[0]
		queue = queue[1:]
		g.queues[placeID] = queue
		if len(queue) == 0 {
			delete(g.queues, placeID)
		}

		waiter := g.npcs[npcID]
		if waiter == nil || waiter.queuedPlace != placeID {
			continue
		}

		oldPlace := waiter.heldPlace
		if oldPlace != "" && oldPlace != placeID {
			g.occupancy[oldPlace]--
			if g.occupancy[oldPlace] <= 0 {
				delete(g.occupancy, oldPlace)
			}
			waiter.heldPlace = ""
		}

		g.occupancy[placeID]++
		waiter.heldPlace = placeID
		waiter.queuedPlace = ""
		waiter.state.Waiting = false
		waiter.state.Place = placeID

		if oldPlace != "" && oldPlace != placeID {
			g.admitNextLocked(oldPlace)
		}
		return
	}
}

func decodeSnapshot(data []byte) (map[string]*npcRuntime, map[string]int, map[string][]string, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil, nil, fmt.Errorf("%w: empty snapshot", ErrBadRestore)
	}

	var snapshot snapshotData
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&snapshot); err != nil {
		return nil, nil, nil, fmt.Errorf("%w: %v", ErrBadRestore, err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, nil, nil, fmt.Errorf("%w: trailing snapshot data", ErrBadRestore)
	}
	if snapshot.Version != snapshotVersion || snapshot.NPCs == nil {
		return nil, nil, nil, fmt.Errorf("%w: unsupported snapshot", ErrBadRestore)
	}

	npcs := make(map[string]*npcRuntime, len(snapshot.NPCs))
	for npcID, saved := range snapshot.NPCs {
		schedule, err := prepareSchedule(npcID, saved.Schedule)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("%w: %v", ErrBadRestore, err)
		}
		if saved.State.NPCID != "" && saved.State.NPCID != npcID {
			return nil, nil, nil, fmt.Errorf("%w: npc id mismatch", ErrBadRestore)
		}

		state := saved.State
		state.NPCID = npcID
		rt := &npcRuntime{
			schedule:        schedule,
			state:           state,
			heldPlace:       saved.HeldPlace,
			queuedPlace:     saved.QueuedPlace,
			entered:         saved.Entered,
			interruptUntil:  saved.InterruptUntil,
			interruptReason: saved.InterruptReason,
		}

		if err := validateRestoredRuntime(rt); err != nil {
			return nil, nil, nil, fmt.Errorf("%w: %v", ErrBadRestore, err)
		}
		npcs[npcID] = rt
	}

	occupancy := make(map[string]int, len(snapshot.Occupancy))
	for placeID, count := range snapshot.Occupancy {
		if placeID == "" || count < 0 {
			return nil, nil, nil, fmt.Errorf("%w: bad occupancy", ErrBadRestore)
		}
		occupancy[placeID] = count
	}

	heldCounts := make(map[string]int)
	for _, rt := range npcs {
		if rt.heldPlace != "" {
			heldCounts[rt.heldPlace]++
		}
	}
	for placeID, count := range occupancy {
		if heldCounts[placeID] != count {
			return nil, nil, nil, fmt.Errorf("%w: occupancy mismatch", ErrBadRestore)
		}
	}
	for placeID, count := range heldCounts {
		if occupancy[placeID] != count {
			return nil, nil, nil, fmt.Errorf("%w: occupancy mismatch", ErrBadRestore)
		}
	}

	queues := make(map[string][]string, len(snapshot.Queues))
	for placeID, queue := range snapshot.Queues {
		if placeID == "" {
			return nil, nil, nil, fmt.Errorf("%w: bad queue", ErrBadRestore)
		}
		seen := make(map[string]bool, len(queue))
		for _, npcID := range queue {
			if seen[npcID] {
				return nil, nil, nil, fmt.Errorf("%w: duplicate queued npc", ErrBadRestore)
			}
			seen[npcID] = true

			rt := npcs[npcID]
			if rt == nil || rt.queuedPlace != placeID || !rt.state.Waiting {
				return nil, nil, nil, fmt.Errorf("%w: queue mismatch", ErrBadRestore)
			}
		}
		if len(queue) > 0 {
			queues[placeID] = append([]string(nil), queue...)
		}
	}

	for npcID, rt := range npcs {
		if rt.queuedPlace == "" {
			continue
		}
		if !containsString(queues[rt.queuedPlace], npcID) {
			return nil, nil, nil, fmt.Errorf("%w: missing queue entry", ErrBadRestore)
		}
	}

	return npcs, occupancy, queues, nil
}

func validateRestoredRuntime(rt *npcRuntime) error {
	state := rt.state
	if state.Interrupted {
		if rt.heldPlace != "" || rt.queuedPlace != "" || state.Waiting {
			return errors.New("interrupted npc retains a place")
		}
		return nil
	}

	if rt.queuedPlace != "" && !state.Waiting {
		return errors.New("queued npc is not marked waiting")
	}
	if rt.queuedPlace == "" && state.Waiting {
		return errors.New("waiting npc has no queue")
	}
	if rt.heldPlace != "" && rt.heldPlace == rt.queuedPlace {
		return errors.New("npc is both occupying and queued for one place")
	}
	if state.SlotID == "" && (rt.heldPlace != "" || rt.queuedPlace != "") {
		return errors.New("idle npc retains a place")
	}
	if !state.Waiting && rt.heldPlace != "" && rt.heldPlace != state.Place {
		return errors.New("occupied place does not match state")
	}
	if state.SlotID != "" && !scheduleHasSlot(rt.schedule, state.SlotID) {
		return errors.New("state references unknown slot")
	}
	return nil
}

func scheduleHasSlot(items []ScheduleItem, slotID string) bool {
	for _, item := range items {
		if item.SlotID == slotID {
			return true
		}
	}
	return false
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
