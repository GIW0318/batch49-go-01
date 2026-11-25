// Package task manages recipes gated by skill level and prerequisites, plus
// per discipline mastery and the one shot first craft bonus.
package task

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
)

// Mastery scale shared by all disciplines.
const (
	MasteryPerLevel        = 100
	DefaultMaxMasteryLevel = 10
	FirstCraftBonus        = 50
)

// Options tunes the mastery cap.
type Options struct {
	MaxLevel int
}

// Errors returned by the guard.
var (
	ErrInvalidRecipe   = errors.New("recipe definition is invalid")
	ErrDuplicateRecipe = errors.New("recipe already defined")
	ErrUnknownRecipe   = errors.New("unknown recipe")
	ErrPrereqCycle     = errors.New("prerequisite chain contains a cycle")
	ErrBlankAccount    = errors.New("account id is required")
	ErrSkillTooLow     = errors.New("skill level is too low for the recipe")
	ErrPrereqMissing   = errors.New("a prerequisite recipe is not unlocked")
	ErrNotDiscovered   = errors.New("recipe is not discovered")
	ErrStaleCraft      = errors.New("craft timestamp is older than the last craft")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// Recipe is one craftable entry of the recipe book.
type Recipe struct {
	ID         string   `json:"id"`
	Discipline string   `json:"discipline"`
	SkillReq   int      `json:"skill_req"`
	Prereqs    []string `json:"prereqs"`
	Points     int      `json:"points"`
	Deleted    bool     `json:"deleted"`
}

// MasteryInfo is the mastery read model of one discipline.
type MasteryInfo struct {
	Discipline string `json:"discipline"`
	Level      int    `json:"level"`
	Points     int    `json:"points"`
	MaxLevel   int    `json:"max_level"`
}

// CraftResult reports one craft attempt.
type CraftResult struct {
	RecipeID   string `json:"recipe_id"`
	Account    string `json:"account"`
	Discipline string `json:"discipline"`
	Points     int    `json:"points"`
	Level      int    `json:"level"`
	Bonus      int    `json:"bonus"`
	FirstCraft bool   `json:"first_craft"`
	At         int64  `json:"at"`
}

// RecipeMasteryGuard owns recipe definitions, unlock sets and mastery.
type RecipeMasteryGuard struct {
	mu       sync.RWMutex
	opts     Options
	recipes  map[string]Recipe
	accounts map[string]*accountState
	acctMu   sync.Map // account id -> *sync.Mutex
}

// accountState is the whole per-account bookkeeping, updated as one unit.
type accountState struct {
	unlocked   map[string]struct{}
	points     map[string]int
	firstCraft map[string]struct{}
	lastCraft  map[string]int64
}

func newAccountState() *accountState {
	return &accountState{
		unlocked:   make(map[string]struct{}),
		points:     make(map[string]int),
		firstCraft: make(map[string]struct{}),
		lastCraft:  make(map[string]int64),
	}
}

// NewRecipeMasteryGuard builds an empty guard.
func NewRecipeMasteryGuard(opts Options) *RecipeMasteryGuard {
	return &RecipeMasteryGuard{
		opts:     opts,
		recipes:  make(map[string]Recipe),
		accounts: make(map[string]*accountState),
	}
}

func (g *RecipeMasteryGuard) maxLevel() int {
	if g.opts.MaxLevel > 0 {
		return g.opts.MaxLevel
	}
	return DefaultMaxMasteryLevel
}

func levelFor(points, maxLevel int) int {
	level := points / MasteryPerLevel
	if level > maxLevel {
		level = maxLevel
	}
	return level
}

// accountLock returns the per-account mutex, creating it on first use. The
// mutex map is independent of g.mu so an account lock is never taken while the
// state lock is held, which would deadlock against the following write lock.
func (g *RecipeMasteryGuard) accountLock(account string) *sync.Mutex {
	mu, _ := g.acctMu.LoadOrStore(account, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

// hasCycleStartingAt reports whether walking prerequisites from start ever
// reaches start again. Not-yet-defined prerequisites simply end the walk.
func (g *RecipeMasteryGuard) hasCycleStartingAt(start string, next map[string][]string) bool {
	visited := map[string]bool{start: true}
	queue := append([]string(nil), next[start]...)
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if id == start {
			return true
		}
		if visited[id] {
			continue
		}
		visited[id] = true
		queue = append(queue, next[id]...)
	}
	return false
}

// DefineRecipe validates and stores one recipe definition.
func (g *RecipeMasteryGuard) DefineRecipe(r Recipe) error {
	if strings.TrimSpace(r.ID) == "" ||
		strings.TrimSpace(r.Discipline) == "" ||
		r.Points <= 0 ||
		r.SkillReq < 0 {
		return ErrInvalidRecipe
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if _, exists := g.recipes[r.ID]; exists {
		return ErrDuplicateRecipe
	}

	next := make(map[string][]string, len(g.recipes)+1)
	for id, def := range g.recipes {
		next[id] = append([]string(nil), def.Prereqs...)
	}
	next[r.ID] = append([]string(nil), r.Prereqs...)
	if g.hasCycleStartingAt(r.ID, next) {
		return ErrPrereqCycle
	}

	stored := r
	stored.Prereqs = append([]string(nil), r.Prereqs...)
	g.recipes[r.ID] = stored
	return nil
}

// DeleteRecipe marks a recipe as deleted.
func (g *RecipeMasteryGuard) DeleteRecipe(id string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	def, ok := g.recipes[id]
	if !ok {
		return ErrUnknownRecipe
	}
	if def.Deleted {
		return nil
	}
	def.Deleted = true
	g.recipes[id] = def
	return nil
}

// Discover unlocks one recipe when the gates are satisfied.
func (g *RecipeMasteryGuard) Discover(account, recipeID string) (bool, error) {
	if strings.TrimSpace(account) == "" {
		return false, ErrBlankAccount
	}

	mu := g.accountLock(account)
	mu.Lock()
	defer mu.Unlock()

	g.mu.Lock()
	defer g.mu.Unlock()

	recipe, ok := g.recipes[recipeID]
	if !ok || recipe.Deleted {
		return false, ErrUnknownRecipe
	}

	state, ok := g.accounts[account]
	if !ok {
		state = newAccountState()
		g.accounts[account] = state
	}
	if _, unlocked := state.unlocked[recipeID]; unlocked {
		return false, nil
	}

	if levelFor(state.points[recipe.Discipline], g.maxLevel()) < recipe.SkillReq {
		return false, ErrSkillTooLow
	}
	for _, prereq := range recipe.Prereqs {
		preDef, known := g.recipes[prereq]
		if !known || preDef.Deleted {
			return false, ErrPrereqMissing
		}
		if _, unlocked := state.unlocked[prereq]; !unlocked {
			return false, ErrPrereqMissing
		}
	}

	state.unlocked[recipeID] = struct{}{}
	return true, nil
}

// Craft consumes one craft attempt of a discovered recipe.
func (g *RecipeMasteryGuard) Craft(account, recipeID string, at int64) (CraftResult, error) {
	if strings.TrimSpace(account) == "" {
		return CraftResult{}, ErrBlankAccount
	}

	mu := g.accountLock(account)
	mu.Lock()
	defer mu.Unlock()

	g.mu.Lock()
	defer g.mu.Unlock()

	state, ok := g.accounts[account]
	if !ok {
		state = newAccountState()
		g.accounts[account] = state
	}
	if _, unlocked := state.unlocked[recipeID]; !unlocked {
		return CraftResult{}, ErrNotDiscovered
	}

	if last, crafted := state.lastCraft[recipeID]; crafted && at < last {
		return CraftResult{}, ErrStaleCraft
	}

	recipe := g.recipes[recipeID]

	// The first-craft mark and the mastery points move together; any earlier
	// failure returns before either of them changes.
	bonus := 0
	firstCraft := false
	if _, claimed := state.firstCraft[recipeID]; !claimed {
		bonus = FirstCraftBonus
		firstCraft = true
		state.firstCraft[recipeID] = struct{}{}
	}

	state.points[recipe.Discipline] += recipe.Points + bonus
	state.lastCraft[recipeID] = at

	points := state.points[recipe.Discipline]
	level := levelFor(points, g.maxLevel())
	return CraftResult{
		RecipeID:   recipeID,
		Account:    account,
		Discipline: recipe.Discipline,
		Points:     points,
		Level:      level,
		Bonus:      bonus,
		FirstCraft: firstCraft,
		At:         at,
	}, nil
}

// Mastery reads the mastery of one discipline.
func (g *RecipeMasteryGuard) Mastery(account, discipline string) MasteryInfo {
	g.mu.RLock()
	defer g.mu.RUnlock()

	points := 0
	if state, ok := g.accounts[account]; ok {
		points = state.points[discipline]
	}
	return MasteryInfo{
		Discipline: discipline,
		Level:      levelFor(points, g.maxLevel()),
		Points:     points,
		MaxLevel:   g.maxLevel(),
	}
}

// Unlocked lists the unlocked recipe ids of an account in id order.
func (g *RecipeMasteryGuard) Unlocked(account string) []string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	state, ok := g.accounts[account]
	if !ok || len(state.unlocked) == 0 {
		return []string{}
	}
	ids := make([]string, 0, len(state.unlocked))
	for id := range state.unlocked {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// IsUnlocked reports whether the account unlocked one recipe.
func (g *RecipeMasteryGuard) IsUnlocked(account, recipeID string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()

	state, ok := g.accounts[account]
	if !ok {
		return false
	}
	_, unlocked := state.unlocked[recipeID]
	return unlocked
}

// Recipes lists the defined recipe ids in id order.
func (g *RecipeMasteryGuard) Recipes() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	ids := make([]string, 0, len(g.recipes))
	for id, recipe := range g.recipes {
		if !recipe.Deleted {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

type snapshotFormat struct {
	MaxLevel int                     `json:"max_level"`
	Recipes  map[string]Recipe       `json:"recipes"`
	Accounts map[string]snapshotAcct `json:"accounts"`
}

type snapshotAcct struct {
	Unlocked   []string         `json:"unlocked"`
	Mastery    map[string]int   `json:"mastery"`
	FirstCraft []string         `json:"first_craft"`
	LastCraft  map[string]int64 `json:"last_craft"`
}

// Snapshot serialises definitions, unlock sets, mastery and first craft marks.
func (g *RecipeMasteryGuard) Snapshot() ([]byte, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	snap := snapshotFormat{
		MaxLevel: g.opts.MaxLevel,
		Recipes:  make(map[string]Recipe, len(g.recipes)),
		Accounts: make(map[string]snapshotAcct, len(g.accounts)),
	}
	for id, recipe := range g.recipes {
		stored := recipe
		stored.Prereqs = append([]string(nil), recipe.Prereqs...)
		snap.Recipes[id] = stored
	}
	for account, state := range g.accounts {
		entry := snapshotAcct{
			Unlocked:   sortedKeys(state.unlocked),
			FirstCraft: sortedKeys(state.firstCraft),
			Mastery:    copyIntMap(state.points),
			LastCraft:  copyInt64Map(state.lastCraft),
		}
		snap.Accounts[account] = entry
	}
	return json.Marshal(snap)
}

// Restore replaces the whole guard state with a snapshot.
func (g *RecipeMasteryGuard) Restore(data []byte) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return ErrBadSnapshot
	}

	var snap snapshotFormat
	if err := json.Unmarshal(data, &snap); err != nil {
		return ErrBadSnapshot
	}

	recipes := make(map[string]Recipe, len(snap.Recipes))
	for id, recipe := range snap.Recipes {
		if id != recipe.ID || strings.TrimSpace(recipe.ID) == "" ||
			strings.TrimSpace(recipe.Discipline) == "" ||
			recipe.Points <= 0 || recipe.SkillReq < 0 {
			return ErrBadSnapshot
		}
		if _, dup := recipes[id]; dup {
			return ErrBadSnapshot
		}
		recipes[id] = recipe
	}

	accounts := make(map[string]*accountState, len(snap.Accounts))
	for account, entry := range snap.Accounts {
		if strings.TrimSpace(account) == "" {
			return ErrBadSnapshot
		}
		state := newAccountState()
		for _, id := range entry.Unlocked {
			state.unlocked[id] = struct{}{}
		}
		for discipline, points := range entry.Mastery {
			if points < 0 {
				return ErrBadSnapshot
			}
			state.points[discipline] = points
		}
		for _, id := range entry.FirstCraft {
			state.firstCraft[id] = struct{}{}
		}
		for id, at := range entry.LastCraft {
			state.lastCraft[id] = at
		}
		accounts[account] = state
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	g.opts.MaxLevel = snap.MaxLevel
	g.recipes = recipes
	g.accounts = accounts
	return nil
}

func sortedKeys(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func copyIntMap(src map[string]int) map[string]int {
	dst := make(map[string]int, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func copyInt64Map(src map[string]int64) map[string]int64 {
	dst := make(map[string]int64, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}
