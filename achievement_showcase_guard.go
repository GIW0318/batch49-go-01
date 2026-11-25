// Package task maintains achievement unlock counters, versioned rarity
// percentages and the per account showcase cabinet.
package task

import (
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"sync"
)

// ShowcasePlaceholder is shown to other viewers in place of a hidden id.
const ShowcasePlaceholder = "hidden-slot"

// Errors returned by the guard.
var (
	ErrInvalidAchievement   = errors.New("achievement id is invalid")
	ErrDuplicateAchievement = errors.New("achievement already defined")
	ErrUnknownAchievement   = errors.New("unknown achievement")
	ErrBlankAccount         = errors.New("account id is required")
	ErrInvalidActivePlayers = errors.New("active player count must be positive")
	ErrStaleRecalc          = errors.New("recalc timestamp is older than the last recalculation")
	ErrSlotLimit            = errors.New("showcase slot limit exceeded")
	ErrNotUnlocked          = errors.New("achievement is not unlocked by the account")
	ErrBadSnapshot          = errors.New("snapshot payload is invalid")
)

// Options configures the showcase cabinet.
type Options struct {
	MaxSlots int
}

// AchievementDef is one catalogue entry.
type AchievementDef struct {
	ID      string `json:"id"`
	Hidden  bool   `json:"hidden"`
	Removed bool   `json:"removed"`
}

// Rarity is the versioned unlock rate of one achievement.
type Rarity struct {
	AchievementID string  `json:"achievement_id"`
	Version       int64   `json:"version"`
	Unlocked      int     `json:"unlocked"`
	ActivePlayers int     `json:"active_players"`
	Percent       float64 `json:"percent"`
}

// RecalcResult reports one rarity recalculation.
type RecalcResult struct {
	Version int64 `json:"version"`
	Changed int   `json:"changed"`
	At      int64 `json:"at"`
}

// ShowcaseEntry is one occupied cabinet slot.
type ShowcaseEntry struct {
	Slot          int    `json:"slot"`
	AchievementID string `json:"achievement_id"`
	Masked        bool   `json:"masked"`
}

// ShowcaseGuard owns definitions, unlock sets, rarity versions and cabinets.
type ShowcaseGuard struct {
	mu            sync.RWMutex
	opts          Options
	defs          map[string]*AchievementDef
	unlocks       map[string]map[string]struct{}
	rarities      map[string]Rarity
	cabinets      map[string][]string
	activePlayers int
	lastRecalcAt  int64
}

// NewShowcaseGuard builds an empty guard.
func NewShowcaseGuard(opts Options) *ShowcaseGuard {
	return &ShowcaseGuard{
		opts:     opts,
		defs:     make(map[string]*AchievementDef),
		unlocks:  make(map[string]map[string]struct{}),
		rarities: make(map[string]Rarity),
		cabinets: make(map[string][]string),
	}
}

// DefineAchievement registers one achievement definition.
func (g *ShowcaseGuard) DefineAchievement(id string, hidden bool) error {
	if strings.TrimSpace(id) == "" {
		return ErrInvalidAchievement
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.defs[id]; ok {
		return ErrDuplicateAchievement
	}
	g.defs[id] = &AchievementDef{ID: id, Hidden: hidden}
	return nil
}

// RemoveAchievement marks a definition as removed.
func (g *ShowcaseGuard) RemoveAchievement(id string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	def, ok := g.defs[id]
	if !ok {
		return ErrUnknownAchievement
	}
	def.Removed = true
	return nil
}

// RecordUnlock counts one account as an unlocker of an achievement.
func (g *ShowcaseGuard) RecordUnlock(achievementID, account string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	def, ok := g.defs[achievementID]
	if !ok || def.Removed {
		return ErrUnknownAchievement
	}
	if strings.TrimSpace(account) == "" {
		return ErrBlankAccount
	}
	accounts := g.unlocks[achievementID]
	if accounts == nil {
		accounts = make(map[string]struct{})
		g.unlocks[achievementID] = accounts
	}
	accounts[account] = struct{}{}
	return nil
}

// Rarity reads the current rarity record of one achievement.
func (g *ShowcaseGuard) Rarity(achievementID string) (Rarity, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	def, ok := g.defs[achievementID]
	if !ok || def.Removed {
		return Rarity{}, ErrUnknownAchievement
	}
	if r, ok := g.rarities[achievementID]; ok && r.Version > 0 {
		return r, nil
	}
	return Rarity{
		AchievementID: achievementID,
		Unlocked:      len(g.unlocks[achievementID]),
		ActivePlayers: g.activePlayers,
	}, nil
}

// SetActivePlayers sets the population used by the next recalculation.
func (g *ShowcaseGuard) SetActivePlayers(n int) error {
	if n <= 0 {
		return ErrInvalidActivePlayers
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.activePlayers = n
	return nil
}

// SetShowcase replaces the cabinet of one account.
func (g *ShowcaseGuard) SetShowcase(account string, slots []string) error {
	if strings.TrimSpace(account) == "" {
		return ErrBlankAccount
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	deduped := make([]string, 0, len(slots))
	seen := make(map[string]struct{}, len(slots))
	for _, id := range slots {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		deduped = append(deduped, id)
	}
	if g.opts.MaxSlots > 0 && len(deduped) > g.opts.MaxSlots {
		return ErrSlotLimit
	}

	unlocked := g.unlocks
	for _, id := range deduped {
		def, ok := g.defs[id]
		if !ok || def.Removed {
			return ErrUnknownAchievement
		}
		if _, ok := unlocked[id][account]; !ok {
			return ErrNotUnlocked
		}
	}

	g.cabinets[account] = deduped
	return nil
}

// View renders the cabinet of an account for a viewer.
func (g *ShowcaseGuard) View(account, viewer string) []ShowcaseEntry {
	g.mu.RLock()
	defer g.mu.RUnlock()

	slots := g.cabinets[account]
	entries := make([]ShowcaseEntry, 0, len(slots))
	for i, id := range slots {
		entry := ShowcaseEntry{Slot: i + 1, AchievementID: id}
		if viewer != account {
			if def, ok := g.defs[id]; ok && def.Hidden {
				entry.AchievementID = ShowcasePlaceholder
				entry.Masked = true
			}
		}
		entries = append(entries, entry)
	}
	return entries
}

// ExportShowcase returns the cabinet of an account exactly as its owner sees
// it, in slot order, for manual operational review.
func (g *ShowcaseGuard) ExportShowcase(account string) []ShowcaseEntry {
	return g.View(account, account)
}

// RecalcRarity recomputes every unlock rate and bumps the version.
func (g *ShowcaseGuard) RecalcRarity(at int64) (RecalcResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if at < g.lastRecalcAt {
		return RecalcResult{}, ErrStaleRecalc
	}
	if g.activePlayers <= 0 {
		return RecalcResult{}, ErrInvalidActivePlayers
	}

	ids := make([]string, 0, len(g.defs))
	for id, def := range g.defs {
		if !def.Removed {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)

	next := make(map[string]Rarity, len(ids))
	newVersion := int64(0)
	for _, id := range ids {
		if prev, ok := g.rarities[id]; ok {
			if prev.Version > newVersion {
				newVersion = prev.Version
			}
		}
	}
	newVersion++

	changed := 0
	for _, id := range ids {
		unlocked := len(g.unlocks[id])
		percent := math.Round(float64(unlocked)/float64(g.activePlayers)*10000) / 100
		record := Rarity{
			AchievementID: id,
			Version:       newVersion,
			Unlocked:      unlocked,
			ActivePlayers: g.activePlayers,
			Percent:       percent,
		}
		if prev, ok := g.rarities[id]; !ok ||
			prev.Unlocked != record.Unlocked ||
			prev.ActivePlayers != record.ActivePlayers ||
			prev.Percent != record.Percent {
			changed++
		}
		next[id] = record
	}
	for id, r := range g.rarities {
		if _, ok := next[id]; !ok {
			next[id] = r
		}
	}

	g.rarities = next
	g.lastRecalcAt = at
	return RecalcResult{Version: newVersion, Changed: changed, At: at}, nil
}

type snapshotPayload struct {
	MaxSlots      int                 `json:"max_slots"`
	ActivePlayers int                 `json:"active_players"`
	LastRecalcAt  int64               `json:"last_recalc_at"`
	Definitions   []*AchievementDef   `json:"definitions"`
	Unlocks       map[string][]string `json:"unlocks"`
	Rarities      map[string]Rarity   `json:"rarities"`
	Cabinets      map[string][]string `json:"cabinets"`
}

// Snapshot serialises counters, population, cabinets and rarity versions.
func (g *ShowcaseGuard) Snapshot() ([]byte, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	defs := make([]*AchievementDef, 0, len(g.defs))
	for _, def := range g.defs {
		copyDef := *def
		defs = append(defs, &copyDef)
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].ID < defs[j].ID })

	unlocks := make(map[string][]string, len(g.unlocks))
	for id, accounts := range g.unlocks {
		list := make([]string, 0, len(accounts))
		for account := range accounts {
			list = append(list, account)
		}
		sort.Strings(list)
		unlocks[id] = list
	}

	payload := snapshotPayload{
		MaxSlots:      g.opts.MaxSlots,
		ActivePlayers: g.activePlayers,
		LastRecalcAt:  g.lastRecalcAt,
		Definitions:   defs,
		Unlocks:       unlocks,
		Rarities:      g.rarities,
		Cabinets:      g.cabinets,
	}
	return json.Marshal(payload)
}

// Restore replaces the whole guard state with a snapshot, all or nothing.
func (g *ShowcaseGuard) Restore(data []byte) error {
	var payload snapshotPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return ErrBadSnapshot
	}
	if payload.ActivePlayers < 0 || payload.LastRecalcAt < 0 || payload.MaxSlots < 0 {
		return ErrBadSnapshot
	}

	defs := make(map[string]*AchievementDef, len(payload.Definitions))
	for _, def := range payload.Definitions {
		if def == nil || strings.TrimSpace(def.ID) == "" {
			return ErrBadSnapshot
		}
		if _, dup := defs[def.ID]; dup {
			return ErrBadSnapshot
		}
		copyDef := *def
		defs[def.ID] = &copyDef
	}

	unlocks := make(map[string]map[string]struct{}, len(payload.Unlocks))
	for id, accounts := range payload.Unlocks {
		if _, ok := defs[id]; !ok {
			return ErrBadSnapshot
		}
		set := make(map[string]struct{}, len(accounts))
		for _, account := range accounts {
			if strings.TrimSpace(account) == "" {
				return ErrBadSnapshot
			}
			set[account] = struct{}{}
		}
		unlocks[id] = set
	}

	rarities := make(map[string]Rarity, len(payload.Rarities))
	for id, r := range payload.Rarities {
		if _, ok := defs[id]; !ok {
			return ErrBadSnapshot
		}
		if r.Version < 0 || r.Unlocked < 0 || r.ActivePlayers < 0 || r.Percent < 0 {
			return ErrBadSnapshot
		}
		rarities[id] = r
	}

	cabinets := make(map[string][]string, len(payload.Cabinets))
	for account, slots := range payload.Cabinets {
		if strings.TrimSpace(account) == "" {
			return ErrBadSnapshot
		}
		copySlots := make([]string, len(slots))
		copy(copySlots, slots)
		cabinets[account] = copySlots
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.defs = defs
	g.unlocks = unlocks
	g.rarities = rarities
	g.cabinets = cabinets
	g.activePlayers = payload.ActivePlayers
	g.lastRecalcAt = payload.LastRecalcAt
	if payload.MaxSlots > 0 {
		g.opts.MaxSlots = payload.MaxSlots
	}
	return nil
}
