// Package task maintains achievement unlock counters, versioned rarity
// percentages and the per account showcase cabinet.
package task

import "errors"

// errNotImplemented marks behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

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
	opts Options
}

// NewShowcaseGuard builds an empty guard.
func NewShowcaseGuard(opts Options) *ShowcaseGuard {
	return &ShowcaseGuard{opts: opts}
}

// DefineAchievement registers one achievement definition.
func (g *ShowcaseGuard) DefineAchievement(id string, hidden bool) error {
	return errNotImplemented
}

// RemoveAchievement marks a definition as removed.
func (g *ShowcaseGuard) RemoveAchievement(id string) error {
	return errNotImplemented
}

// RecordUnlock counts one account as an unlocker of an achievement.
func (g *ShowcaseGuard) RecordUnlock(achievementID, account string) error {
	return errNotImplemented
}

// Rarity reads the current rarity record of one achievement.
func (g *ShowcaseGuard) Rarity(achievementID string) (Rarity, error) {
	return Rarity{}, errNotImplemented
}

// SetActivePlayers sets the population used by the next recalculation.
func (g *ShowcaseGuard) SetActivePlayers(n int) error {
	return errNotImplemented
}

// SetShowcase replaces the cabinet of one account.
func (g *ShowcaseGuard) SetShowcase(account string, slots []string) error {
	return errNotImplemented
}

// View renders the cabinet of an account for a viewer.
func (g *ShowcaseGuard) View(account, viewer string) []ShowcaseEntry {
	return nil
}

// RecalcRarity recomputes every unlock rate and bumps the version.
func (g *ShowcaseGuard) RecalcRarity(at int64) (RecalcResult, error) {
	return RecalcResult{}, errNotImplemented
}

// Snapshot serialises counters, population, cabinets and rarity versions.
func (g *ShowcaseGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole guard state with a snapshot, all or nothing.
func (g *ShowcaseGuard) Restore(data []byte) error {
	return errNotImplemented
}
