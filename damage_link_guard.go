// Package task models linked damage sharing inside a party, the per-source
// reflect budget within one combat tick and the state that must survive a
// process restart.
package task

import (
	"errors"
	"sync"
)

var errNotImplemented = errors.New("not implemented")

// Sharing and tick anchors.
const (
	SharePctMax       = 100
	DefaultHealth     = int64(1000)
	DefaultTickSpan   = int64(1000)
	DefaultTickCap    = int64(100)
	DefaultMaxGroups  = 64
	DefaultMaxMembers = 8
)

// Errors returned by the guard.
var (
	ErrBadOptions      = errors.New("options are out of range")
	ErrBlankGroup      = errors.New("group id is required")
	ErrBlankMember     = errors.New("member id is required")
	ErrUnknownGroup    = errors.New("unknown link group")
	ErrUnknownMember   = errors.New("unknown member")
	ErrNotMember       = errors.New("member is not linked to the group")
	ErrDuplicateMember = errors.New("duplicate member in group")
	ErrBadSharePct     = errors.New("share percent must be between 1 and 100")
	ErrBadAmount       = errors.New("amount must be positive")
	ErrGroupLimit      = errors.New("link group limit reached")
	ErrMemberLimit     = errors.New("group member limit reached")
	ErrDeadMember      = errors.New("member is already dead")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// Options bounds the live tables and the reflect window of the guard.
type Options struct {
	MaxGroups     int
	MaxMembers    int
	TickSpan      int64
	TickCap       int64
	DefaultHealth int64
}

// Hit is one direct damage event that should be routed through a link group.
type Hit struct {
	Source  string
	Target  string
	GroupID string
	Amount  int64
	At      int64
}

// Share is one line of a settled hit. Direct marks the part that stays on the
// primary target instead of being split across the group.
type Share struct {
	GroupID string
	Member  string
	Amount  int64
	Direct  bool
	Order   int
}

type groupState struct {
	ID      string
	Members []string
	Pct     int
}

// DamageLinkGuard owns link groups, member health and reflect accounting.
type DamageLinkGuard struct {
	mu           sync.Mutex
	opts         Options
	groups       map[string]*groupState
	health       map[string]int64
	reflectTick  map[string]int64
	reflectTotal map[string]int64
	lastTick     int64
}

// NewDamageLinkGuard builds a guard, filling defaults for zero options.
func NewDamageLinkGuard(opts Options) *DamageLinkGuard {
	return &DamageLinkGuard{
		opts:         opts,
		groups:       make(map[string]*groupState),
		health:       make(map[string]int64),
		reflectTick:  make(map[string]int64),
		reflectTotal: make(map[string]int64),
	}
}

// Link creates or replaces a group, keeping the member order as given.
func (g *DamageLinkGuard) Link(groupID string, members []string, sharePct int) error {
	return errNotImplemented
}

// Unlink removes one member from a group, leaving the order of the rest intact.
func (g *DamageLinkGuard) Unlink(groupID, member string) error {
	return errNotImplemented
}

// Settle splits one hit across the live members of the hit group.
func (g *DamageLinkGuard) Settle(hit Hit) ([]Share, error) {
	return nil, errNotImplemented
}

// Reflect sends actual damage back to its source, capped per source and tick.
func (g *DamageLinkGuard) Reflect(source, target string, amount int64, at int64) (int64, error) {
	return 0, errNotImplemented
}

// TickReset drops the reflect counters of the previous tick.
func (g *DamageLinkGuard) TickReset(at int64) {
}

// GroupMembers returns the member ids of a group in join order.
func (g *DamageLinkGuard) GroupMembers(groupID string) []string {
	return nil
}

// SharePct returns the configured link percentage of a group.
func (g *DamageLinkGuard) SharePct(groupID string) (int, error) {
	return 0, errNotImplemented
}

// SetHealth sets the current health of one member.
func (g *DamageLinkGuard) SetHealth(member string, hp int64) error {
	return errNotImplemented
}

// Health returns the current health of one member.
func (g *DamageLinkGuard) Health(member string) int64 {
	return 0
}

// ReflectedInTick returns the reflected amount already spent by one source in
// the tick that contains at.
func (g *DamageLinkGuard) ReflectedInTick(source string, at int64) int64 {
	return 0
}

// ReflectedTotal returns the total amount reflected at one target.
func (g *DamageLinkGuard) ReflectedTotal(target string) int64 {
	return 0
}

// Snapshot serialises groups, member order, health and reflect counters.
func (g *DamageLinkGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *DamageLinkGuard) Restore(data []byte) error {
	return errNotImplemented
}
