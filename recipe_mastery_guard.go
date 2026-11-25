// Package task manages recipes gated by skill level and prerequisites, plus
// per discipline mastery and the one shot first craft bonus.
package task

import "errors"

// errNotImplemented marks behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

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
	opts Options
}

// NewRecipeMasteryGuard builds an empty guard.
func NewRecipeMasteryGuard(opts Options) *RecipeMasteryGuard {
	return &RecipeMasteryGuard{opts: opts}
}

// DefineRecipe validates and stores one recipe definition.
func (g *RecipeMasteryGuard) DefineRecipe(r Recipe) error {
	return errNotImplemented
}

// DeleteRecipe marks a recipe as deleted.
func (g *RecipeMasteryGuard) DeleteRecipe(id string) error {
	return errNotImplemented
}

// Discover unlocks one recipe when the gates are satisfied.
func (g *RecipeMasteryGuard) Discover(account, recipeID string) (bool, error) {
	return false, errNotImplemented
}

// Craft consumes one craft attempt of a discovered recipe.
func (g *RecipeMasteryGuard) Craft(account, recipeID string, at int64) (CraftResult, error) {
	return CraftResult{}, errNotImplemented
}

// Mastery reads the mastery of one discipline.
func (g *RecipeMasteryGuard) Mastery(account, discipline string) MasteryInfo {
	return MasteryInfo{}
}

// Unlocked lists the unlocked recipe ids of an account in id order.
func (g *RecipeMasteryGuard) Unlocked(account string) []string {
	return nil
}

// IsUnlocked reports whether the account unlocked one recipe.
func (g *RecipeMasteryGuard) IsUnlocked(account, recipeID string) bool {
	return false
}

// Recipes lists the defined recipe ids in id order.
func (g *RecipeMasteryGuard) Recipes() []string {
	return nil
}

// Snapshot serialises definitions, unlock sets, mastery and first craft marks.
func (g *RecipeMasteryGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole guard state with a snapshot.
func (g *RecipeMasteryGuard) Restore(data []byte) error {
	return errNotImplemented
}
