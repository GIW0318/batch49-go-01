// Package task models linked damage sharing inside a party, the per-source
// reflect budget within one combat tick and the state that must survive a
// process restart.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

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

type snapshotGroup struct {
	ID      string   `json:"id"`
	Members []string `json:"members"`
	Pct     int      `json:"pct"`
}

type snapshotData struct {
	Version      int              `json:"version"`
	Tick         int64            `json:"tick"`
	Groups       []snapshotGroup  `json:"groups"`
	Health       map[string]int64 `json:"health"`
	ReflectTick  map[string]int64 `json:"reflect_tick"`
	ReflectTotal map[string]int64 `json:"reflect_total"`
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
	if opts.MaxGroups <= 0 {
		opts.MaxGroups = DefaultMaxGroups
	}
	if opts.MaxMembers <= 0 {
		opts.MaxMembers = DefaultMaxMembers
	}
	if opts.TickSpan <= 0 {
		opts.TickSpan = DefaultTickSpan
	}
	if opts.TickCap <= 0 {
		opts.TickCap = DefaultTickCap
	}
	if opts.DefaultHealth <= 0 {
		opts.DefaultHealth = DefaultHealth
	}
	return &DamageLinkGuard{
		opts:         opts,
		groups:       make(map[string]*groupState),
		health:       make(map[string]int64),
		reflectTick:  make(map[string]int64),
		reflectTotal: make(map[string]int64),
	}
}

func blankID(id string) bool {
	return strings.TrimSpace(id) == ""
}

func (g *DamageLinkGuard) tickIndex(at int64) int64 {
	span := g.opts.TickSpan
	if at >= 0 {
		return at / span
	}
	return -((-at + span - 1) / span)
}

func (g *DamageLinkGuard) rotateTickLocked(at int64) {
	tick := g.tickIndex(at)
	if tick != g.lastTick {
		g.lastTick = tick
		g.reflectTick = make(map[string]int64)
	}
}

func (g *DamageLinkGuard) healthLocked(member string) int64 {
	if hp, ok := g.health[member]; ok {
		return hp
	}
	return g.opts.DefaultHealth
}

func subtractHealth(hp, amount int64) int64 {
	if amount >= hp {
		return 0
	}
	return hp - amount
}

func linkedQuota(amount int64, pct int) int64 {
	whole := amount / 100
	remainder := amount % 100
	return whole*int64(pct) + remainder*int64(pct)/100
}

// Link creates or replaces a group, keeping the member order as given.
func (g *DamageLinkGuard) Link(groupID string, members []string, sharePct int) error {
	if blankID(groupID) {
		return ErrBlankGroup
	}
	if sharePct < 1 || sharePct > SharePctMax {
		return ErrBadSharePct
	}
	if len(members) == 0 {
		return ErrBlankMember
	}

	seen := make(map[string]struct{}, len(members))
	for _, member := range members {
		if blankID(member) {
			return ErrBlankMember
		}
		if _, duplicate := seen[member]; duplicate {
			return ErrDuplicateMember
		}
		seen[member] = struct{}{}
	}
	if len(members) > g.opts.MaxMembers {
		return ErrMemberLimit
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if _, exists := g.groups[groupID]; !exists && len(g.groups) >= g.opts.MaxGroups {
		return ErrGroupLimit
	}

	copied := append([]string(nil), members...)
	g.groups[groupID] = &groupState{
		ID:      groupID,
		Members: copied,
		Pct:     sharePct,
	}
	return nil
}

// Unlink removes one member from a group, leaving the order of the rest intact.
func (g *DamageLinkGuard) Unlink(groupID, member string) error {
	if blankID(member) {
		return ErrBlankMember
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	group, ok := g.groups[groupID]
	if !ok {
		return ErrUnknownGroup
	}

	index := -1
	for i, id := range group.Members {
		if id == member {
			index = i
			break
		}
	}
	if index < 0 {
		return ErrNotMember
	}

	group.Members = append(group.Members[:index], group.Members[index+1:]...)
	return nil
}

// Settle splits one hit across the live members of the hit group.
func (g *DamageLinkGuard) Settle(hit Hit) ([]Share, error) {
	if hit.Amount <= 0 {
		return nil, ErrBadAmount
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	group, ok := g.groups[hit.GroupID]
	if !ok {
		return nil, ErrUnknownGroup
	}

	targetIndex := -1
	for i, member := range group.Members {
		if member == hit.Target {
			targetIndex = i
			break
		}
	}
	if targetIndex < 0 {
		return nil, ErrNotMember
	}

	linkedTotal := linkedQuota(hit.Amount, group.Pct)
	directTotal := hit.Amount - linkedTotal
	linkedAmount := make(map[string]int64, len(group.Members))

	eligible := make([]string, 0, len(group.Members))
	for _, member := range group.Members {
		if g.healthLocked(member) > 0 {
			eligible = append(eligible, member)
		}
	}

	remaining := linkedTotal
	for remaining > 0 && len(eligible) > 0 {
		count := int64(len(eligible))
		base := remaining / count
		extraCount := int(remaining % count)
		nextEligible := make([]string, 0, len(eligible))

		for i, member := range eligible {
			share := base
			if i < extraCount {
				share++
			}
			if share == 0 {
				nextEligible = append(nextEligible, member)
				continue
			}

			hp := g.healthLocked(member)
			if share >= hp {
				linkedAmount[member] += hp
				g.health[member] = 0
				remaining -= hp
				continue
			}

			linkedAmount[member] += share
			hp -= share
			g.health[member] = hp
			remaining -= share
			if hp > 0 {
				nextEligible = append(nextEligible, member)
			}
		}

		eligible = nextEligible
	}

	linkedSettled := int64(0)
	for _, amount := range linkedAmount {
		linkedSettled += amount
	}
	if linkedSettled > linkedTotal {
		return nil, fmt.Errorf("internal linked settlement overflow: %w", ErrBadAmount)
	}
	directTotal += linkedTotal - linkedSettled

	if directTotal > 0 {
		targetHP := g.healthLocked(hit.Target)
		g.health[hit.Target] = subtractHealth(targetHP, directTotal)
	}

	shares := make([]Share, 0, len(group.Members)+1)
	for _, member := range group.Members {
		amount := linkedAmount[member]
		if amount <= 0 {
			continue
		}
		shares = append(shares, Share{
			GroupID: hit.GroupID,
			Member:  member,
			Amount:  amount,
			Direct:  false,
			Order:   len(shares),
		})
	}
	if directTotal > 0 {
		shares = append(shares, Share{
			GroupID: hit.GroupID,
			Member:  hit.Target,
			Amount:  directTotal,
			Direct:  true,
			Order:   len(shares),
		})
	}

	return shares, nil
}

// Reflect sends actual damage back to its source, capped per source and tick.
func (g *DamageLinkGuard) Reflect(source, target string, amount int64, at int64) (int64, error) {
	if amount <= 0 {
		return 0, ErrBadAmount
	}
	if blankID(source) || blankID(target) {
		return 0, ErrBlankMember
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	g.rotateTickLocked(at)
	used := g.reflectTick[source]
	remainingCap := g.opts.TickCap - used
	granted := amount
	if remainingCap <= 0 {
		granted = 0
	} else if granted > remainingCap {
		granted = remainingCap
	}

	if granted > 0 {
		g.reflectTick[source] = used + granted
		g.reflectTotal[target] += granted
		sourceHP := g.healthLocked(source)
		g.health[source] = subtractHealth(sourceHP, granted)
	}
	return granted, nil
}

// TickReset drops the reflect counters of the previous tick.
func (g *DamageLinkGuard) TickReset(at int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.lastTick = g.tickIndex(at)
	g.reflectTick = make(map[string]int64)
}

// GroupMembers returns the member ids of a group in join order.
func (g *DamageLinkGuard) GroupMembers(groupID string) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	group, ok := g.groups[groupID]
	if !ok {
		return nil
	}
	members := make([]string, len(group.Members))
	copy(members, group.Members)
	return members
}

// SharePct returns the configured link percentage of a group.
func (g *DamageLinkGuard) SharePct(groupID string) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	group, ok := g.groups[groupID]
	if !ok {
		return 0, ErrUnknownGroup
	}
	return group.Pct, nil
}

// SetHealth sets the current health of one member.
func (g *DamageLinkGuard) SetHealth(member string, hp int64) error {
	if blankID(member) {
		return ErrBlankMember
	}
	if hp < 0 {
		hp = 0
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.health[member] = hp
	return nil
}

// Health returns the current health of one member.
func (g *DamageLinkGuard) Health(member string) int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.healthLocked(member)
}

// ReflectedInTick returns the reflected amount already spent by one source in
// the tick that contains at.
func (g *DamageLinkGuard) ReflectedInTick(source string, at int64) int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.tickIndex(at) != g.lastTick {
		return 0
	}
	return g.reflectTick[source]
}

// ReflectedTotal returns the total amount reflected at one target.
func (g *DamageLinkGuard) ReflectedTotal(target string) int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.reflectTotal[target]
}

// Snapshot serialises groups, member order, health and reflect counters.
func (g *DamageLinkGuard) Snapshot() ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	groups := make([]snapshotGroup, 0, len(g.groups))
	for _, group := range g.groups {
		members := make([]string, len(group.Members))
		copy(members, group.Members)
		groups = append(groups, snapshotGroup{
			ID:      group.ID,
			Members: members,
			Pct:     group.Pct,
		})
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].ID < groups[j].ID })

	health := make(map[string]int64, len(g.health))
	for member, hp := range g.health {
		health[member] = hp
	}
	for _, group := range g.groups {
		for _, member := range group.Members {
			if _, exists := health[member]; !exists {
				health[member] = g.opts.DefaultHealth
			}
		}
	}

	payload := snapshotData{
		Version:      1,
		Tick:         g.lastTick,
		Groups:       groups,
		Health:       health,
		ReflectTick:  copyInt64Map(g.reflectTick),
		ReflectTotal: copyInt64Map(g.reflectTotal),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("snapshot encode failed: %w", err)
	}
	return data, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *DamageLinkGuard) Restore(data []byte) error {
	var payload snapshotData
	if err := json.Unmarshal(data, &payload); err != nil {
		return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	if payload.Version != 1 {
		return fmt.Errorf("%w: unsupported version %d", ErrBadSnapshot, payload.Version)
	}

	groups, err := decodeSnapshotGroups(payload.Groups, g.opts)
	if err != nil {
		return err
	}
	health, err := decodeSnapshotHealth(payload.Health)
	if err != nil {
		return err
	}
	reflectTick, err := decodeSnapshotCounters(payload.ReflectTick)
	if err != nil {
		return err
	}
	reflectTotal, err := decodeSnapshotCounters(payload.ReflectTotal)
	if err != nil {
		return err
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.groups = groups
	g.health = health
	g.reflectTick = reflectTick
	g.reflectTotal = reflectTotal
	g.lastTick = payload.Tick
	return nil
}

func copyInt64Map(source map[string]int64) map[string]int64 {
	result := make(map[string]int64, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func decodeSnapshotGroups(items []snapshotGroup, opts Options) (map[string]*groupState, error) {
	if len(items) > opts.MaxGroups {
		return nil, fmt.Errorf("%w: %v", ErrBadSnapshot, ErrGroupLimit)
	}

	groups := make(map[string]*groupState, len(items))
	for _, item := range items {
		if blankID(item.ID) {
			return nil, fmt.Errorf("%w: %v", ErrBadSnapshot, ErrBlankGroup)
		}
		if _, exists := groups[item.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate group %q", ErrBadSnapshot, item.ID)
		}
		if item.Pct < 1 || item.Pct > SharePctMax {
			return nil, fmt.Errorf("%w: %v", ErrBadSnapshot, ErrBadSharePct)
		}
		if len(item.Members) > opts.MaxMembers {
			return nil, fmt.Errorf("%w: %v", ErrBadSnapshot, ErrMemberLimit)
		}

		seen := make(map[string]struct{}, len(item.Members))
		members := make([]string, 0, len(item.Members))
		for _, member := range item.Members {
			if blankID(member) {
				return nil, fmt.Errorf("%w: %v", ErrBadSnapshot, ErrBlankMember)
			}
			if _, duplicate := seen[member]; duplicate {
				return nil, fmt.Errorf("%w: %v", ErrBadSnapshot, ErrDuplicateMember)
			}
			seen[member] = struct{}{}
			members = append(members, member)
		}
		groups[item.ID] = &groupState{ID: item.ID, Members: members, Pct: item.Pct}
	}
	return groups, nil
}

func decodeSnapshotHealth(values map[string]int64) (map[string]int64, error) {
	health := make(map[string]int64, len(values))
	for member, hp := range values {
		if blankID(member) {
			return nil, fmt.Errorf("%w: %v", ErrBadSnapshot, ErrBlankMember)
		}
		if hp < 0 {
			return nil, fmt.Errorf("%w: health for %q is negative", ErrBadSnapshot, member)
		}
		health[member] = hp
	}
	return health, nil
}

func decodeSnapshotCounters(values map[string]int64) (map[string]int64, error) {
	result := make(map[string]int64, len(values))
	for id, value := range values {
		if blankID(id) {
			return nil, fmt.Errorf("%w: %v", ErrBadSnapshot, ErrBlankMember)
		}
		if value < 0 {
			return nil, fmt.Errorf("%w: negative reflect counter for %q", ErrBadSnapshot, id)
		}
		result[id] = value
	}
	return result, nil
}
