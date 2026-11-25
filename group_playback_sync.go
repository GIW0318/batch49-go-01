// Package task keeps one shared playback timeline for a group of devices and
// brings drifted members back without restarting the group.
package task

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

// GroupTime is a group timeline position in milliseconds.
type GroupTime int64

// MemberID identifies one member of the group.
type MemberID string

// Caps are the playback capabilities a device reports when joining.
type Caps struct {
	BufferMs   int
	MaxDriftMs int
}

// Member is one device inside the group.
type Member struct {
	ID       MemberID
	DeviceID string
	Leader   bool
	Ready    bool
	Paused   bool
	DriftMs  int
	Position GroupTime
}

// CatchupEntry is the progress of one member inside one catch up round.
type CatchupEntry struct {
	Member      MemberID
	DeviceID    string
	AdvanceMs   int
	RemainingMs int
	Done        bool
}

// CatchupReport is one catch up round across the whole group.
type CatchupReport struct {
	Entries  []CatchupEntry
	Timeline GroupTime
}

// Config bounds the group.
type Config struct {
	MaxMembers     int
	MaxDriftMs     int
	CatchupStepMs  int
	StartTimeoutMs int
}

// Errors reported by the group.
var (
	ErrBadDevice       = errors.New("device id is required")
	ErrDuplicateDevice = errors.New("device already joined")
	ErrGroupFull       = errors.New("group member limit reached")
	ErrMemberNotFound  = errors.New("member not found")
	ErrNotReady        = errors.New("not every member is ready")
	ErrAlreadyStarted  = errors.New("group playback already started")
	ErrBadStartTime    = errors.New("start time is behind the group timeline")
	ErrNotStarted      = errors.New("group playback has not started")
	ErrBadAdvance      = errors.New("advance must be positive")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// GroupPlaybackSync owns the member table and the group timeline.
type GroupPlaybackSync struct {
	mu       sync.RWMutex
	cfg      Config
	members  []*member
	byDev    map[string]*member
	leader   *member
	started  bool
	timeline GroupTime
	nextSeq  int64
}

// member is the internal, lock-protected state of one device.
type member struct {
	id       MemberID
	deviceID string
	bufferMs int
	joinSeq  int64
	ready    bool
	paused   bool
	driftMs  int
	position GroupTime
}

func (m *member) snapshotView() Member {
	return Member{
		ID:       m.id,
		DeviceID: m.deviceID,
		Leader:   false,
		Ready:    m.ready,
		Paused:   m.paused,
		DriftMs:  m.driftMs,
		Position: m.position,
	}
}

// snapshotPayload is the serialised form of the whole group state.
type snapshotPayload struct {
	Version  int              `json:"version"`
	Started  bool             `json:"started"`
	Timeline GroupTime        `json:"timeline"`
	NextSeq  int64            `json:"next_seq"`
	Members  []memberSnapshot `json:"members"`
}

type memberSnapshot struct {
	DeviceID string    `json:"device_id"`
	BufferMs int       `json:"buffer_ms"`
	JoinSeq  int64     `json:"join_seq"`
	Ready    bool      `json:"ready"`
	Paused   bool      `json:"paused"`
	DriftMs  int       `json:"drift_ms"`
	Position GroupTime `json:"position"`
}

// NewGroupPlaybackSync builds an idle group with the given bounds.
func NewGroupPlaybackSync(cfg Config) *GroupPlaybackSync {
	return &GroupPlaybackSync{
		cfg:   cfg,
		byDev: make(map[string]*member),
	}
}

// Join adds one device to the group and returns its member identifier.
func (g *GroupPlaybackSync) Join(devID string, caps Caps) (MemberID, error) {
	trimmed := strings.TrimSpace(devID)
	if trimmed == "" {
		return "", ErrBadDevice
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, exists := g.byDev[trimmed]; exists {
		return "", ErrDuplicateDevice
	}
	if g.cfg.MaxMembers > 0 && len(g.members) >= g.cfg.MaxMembers {
		return "", ErrGroupFull
	}
	g.nextSeq++
	m := &member{
		id:       MemberID(trimmed),
		deviceID: trimmed,
		bufferMs: caps.BufferMs,
		joinSeq:  g.nextSeq,
		ready:    false,
		paused:   false,
		position: g.timeline,
	}
	g.members = append(g.members, m)
	g.byDev[trimmed] = m
	if g.leader == nil {
		g.leader = m
	}
	return m.id, nil
}

// Leave removes one device from the group.
func (g *GroupPlaybackSync) Leave(devID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	m, ok := g.byDev[devID]
	if !ok {
		return ErrMemberNotFound
	}
	delete(g.byDev, devID)
	for i, existing := range g.members {
		if existing == m {
			g.members = append(g.members[:i], g.members[i+1:]...)
			break
		}
	}
	if len(g.members) == 0 {
		g.resetLocked()
		return nil
	}
	if g.leader == m {
		g.leader = g.members[0]
	}
	return nil
}

// ReportOffset records the drift reported by one device.
func (g *GroupPlaybackSync) ReportOffset(devID string, d time.Duration) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	m, ok := g.byDev[devID]
	if !ok {
		return ErrMemberNotFound
	}
	drift := int(d / time.Millisecond)
	m.driftMs = drift
	if absInt(drift) <= g.cfg.MaxDriftMs {
		m.ready = true
		if !m.paused {
			return nil
		}
		// The device re-calibrated inside the tolerance window. It keeps
		// playing eligibility but still has to walk its frozen position
		// back to the timeline through the bounded catch-up steps.
		return nil
	}
	m.ready = false
	m.paused = true
	return nil
}

// Start pins the group timeline to the given position.
func (g *GroupPlaybackSync) Start(at GroupTime) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.started {
		return ErrAlreadyStarted
	}
	for _, m := range g.members {
		if !m.ready {
			return ErrNotReady
		}
	}
	if at < g.timeline {
		return ErrBadStartTime
	}
	g.started = true
	g.timeline = at
	for _, m := range g.members {
		m.position = at
	}
	return nil
}

// CatchupStep advances every lagging member by at most one bounded step.
func (g *GroupPlaybackSync) CatchupStep() CatchupReport {
	g.mu.Lock()
	defer g.mu.Unlock()
	report := CatchupReport{Timeline: g.timeline}
	for _, m := range g.members {
		if !m.paused {
			continue
		}
		gap := int(g.timeline - m.position)
		if gap <= 0 {
			m.paused = false
			m.ready = true
			continue
		}
		step := g.cfg.CatchupStepMs
		if step <= 0 || step > gap {
			step = gap
		}
		m.position += GroupTime(step)
		remaining := int(g.timeline - m.position)
		done := remaining == 0
		if done {
			m.paused = false
			m.ready = true
		}
		report.Entries = append(report.Entries, CatchupEntry{
			Member:      m.id,
			DeviceID:    m.deviceID,
			AdvanceMs:   step,
			RemainingMs: remaining,
			Done:        done,
		})
	}
	return report
}

// Members returns the members in join order.
func (g *GroupPlaybackSync) Members() []Member {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]Member, 0, len(g.members))
	for _, m := range g.members {
		view := m.snapshotView()
		if g.leader == m {
			view.Leader = true
		}
		out = append(out, view)
	}
	return out
}

// GroupNow returns the current group timeline position.
func (g *GroupPlaybackSync) GroupNow() GroupTime {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.timeline
}

// Started reports whether playback has begun.
func (g *GroupPlaybackSync) Started() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.started
}

// Leader returns the member identifier of the current leader.
func (g *GroupPlaybackSync) Leader() (MemberID, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.leader == nil {
		return "", false
	}
	return g.leader.id, true
}

// Ready reports whether every member is ready to play.
func (g *GroupPlaybackSync) Ready() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if len(g.members) == 0 {
		return false
	}
	for _, m := range g.members {
		if !m.ready {
			return false
		}
	}
	return true
}

// Missing lists the members that are not ready yet, in join order.
func (g *GroupPlaybackSync) Missing() []MemberID {
	g.mu.RLock()
	defer g.mu.RUnlock()
	var out []MemberID
	for _, m := range g.members {
		if !m.ready {
			out = append(out, m.id)
		}
	}
	return out
}

// Advance moves the group timeline and every playing member forward.
func (g *GroupPlaybackSync) Advance(d time.Duration) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.started {
		return ErrNotStarted
	}
	if d <= 0 {
		return ErrBadAdvance
	}
	delta := GroupTime(d / time.Millisecond)
	g.timeline += delta
	for _, m := range g.members {
		if !m.paused {
			m.position += delta
		}
	}
	return nil
}

// Snapshot serialises the member table and the timeline.
func (g *GroupPlaybackSync) Snapshot() ([]byte, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	payload := snapshotPayload{
		Version:  1,
		Started:  g.started,
		Timeline: g.timeline,
		NextSeq:  g.nextSeq,
		Members:  make([]memberSnapshot, 0, len(g.members)),
	}
	for _, m := range g.members {
		payload.Members = append(payload.Members, memberSnapshot{
			DeviceID: m.deviceID,
			BufferMs: m.bufferMs,
			JoinSeq:  m.joinSeq,
			Ready:    m.ready,
			Paused:   m.paused,
			DriftMs:  m.driftMs,
			Position: m.position,
		})
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Restore replaces the whole group state with a snapshot, all or nothing.
func (g *GroupPlaybackSync) Restore(data []byte) error {
	var payload snapshotPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return ErrBadSnapshot
	}
	if payload.Version != 1 {
		return ErrBadSnapshot
	}
	seen := make(map[string]bool, len(payload.Members))
	rebuilt := make([]*member, 0, len(payload.Members))
	byDev := make(map[string]*member, len(payload.Members))
	var leader *member
	for _, ms := range payload.Members {
		if strings.TrimSpace(ms.DeviceID) == "" {
			return ErrBadSnapshot
		}
		if seen[ms.DeviceID] {
			return ErrBadSnapshot
		}
		seen[ms.DeviceID] = true
		if ms.Paused && ms.Ready {
			return ErrBadSnapshot
		}
		if ms.Position > payload.Timeline {
			return ErrBadSnapshot
		}
		m := &member{
			id:       MemberID(ms.DeviceID),
			deviceID: ms.DeviceID,
			bufferMs: ms.BufferMs,
			joinSeq:  ms.JoinSeq,
			ready:    ms.Ready,
			paused:   ms.Paused,
			driftMs:  ms.DriftMs,
			position: ms.Position,
		}
		rebuilt = append(rebuilt, m)
		byDev[ms.DeviceID] = m
	}
	if len(rebuilt) > 0 {
		leader = rebuilt[0]
	}
	if payload.Started && leader == nil {
		return ErrBadSnapshot
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.members = rebuilt
	g.byDev = byDev
	g.leader = leader
	g.started = payload.Started
	g.timeline = payload.Timeline
	g.nextSeq = payload.NextSeq
	return nil
}

// resetLocked returns an empty group to the idle state. Callers hold g.mu.
func (g *GroupPlaybackSync) resetLocked() {
	g.members = nil
	g.byDev = make(map[string]*member)
	g.leader = nil
	g.started = false
	g.timeline = 0
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
