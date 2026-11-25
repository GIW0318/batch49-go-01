// Package task keeps one shared playback timeline for a group of devices and
// brings drifted members back without restarting the group.
package task

import (
	"errors"
	"time"
)

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

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
	cfg Config
}

// NewGroupPlaybackSync builds an idle group with the given bounds.
func NewGroupPlaybackSync(cfg Config) *GroupPlaybackSync {
	return &GroupPlaybackSync{cfg: cfg}
}

// Join adds one device to the group and returns its member identifier.
func (g *GroupPlaybackSync) Join(devID string, caps Caps) (MemberID, error) {
	return "", errNotImplemented
}

// Leave removes one device from the group.
func (g *GroupPlaybackSync) Leave(devID string) error {
	return errNotImplemented
}

// ReportOffset records the drift reported by one device.
func (g *GroupPlaybackSync) ReportOffset(devID string, d time.Duration) error {
	return errNotImplemented
}

// Start pins the group timeline to the given position.
func (g *GroupPlaybackSync) Start(at GroupTime) error {
	return errNotImplemented
}

// CatchupStep advances every lagging member by at most one bounded step.
func (g *GroupPlaybackSync) CatchupStep() CatchupReport {
	return CatchupReport{}
}

// Members returns the members in join order.
func (g *GroupPlaybackSync) Members() []Member {
	return nil
}

// GroupNow returns the current group timeline position.
func (g *GroupPlaybackSync) GroupNow() GroupTime {
	return 0
}

// Started reports whether playback has begun.
func (g *GroupPlaybackSync) Started() bool {
	return false
}

// Leader returns the member identifier of the current leader.
func (g *GroupPlaybackSync) Leader() (MemberID, bool) {
	return "", false
}

// Ready reports whether every member is ready to play.
func (g *GroupPlaybackSync) Ready() bool {
	return false
}

// Missing lists the members that are not ready yet, in join order.
func (g *GroupPlaybackSync) Missing() []MemberID {
	return nil
}

// Advance moves the group timeline and every playing member forward.
func (g *GroupPlaybackSync) Advance(d time.Duration) error {
	return errNotImplemented
}

// Snapshot serialises the member table and the timeline.
func (g *GroupPlaybackSync) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole group state with a snapshot, all or nothing.
func (g *GroupPlaybackSync) Restore(data []byte) error {
	return errNotImplemented
}
