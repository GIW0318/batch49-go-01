// Package task settles crafting attempts: a capped success rate, a quality
// tier picked from an independent draw, and per material refunds on failure.
package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Crafting limits and defaults.
const (
	// MaxPermille is the top of every permille scale used by the roller.
	MaxPermille = 1000
	// DefaultMaxSuccessPermille caps success rates when Options leaves it unset.
	DefaultMaxSuccessPermille = 950
	// DrawRange is the number of distinct roll values produced per draw.
	DrawRange = 1000
)

// Errors returned by the roller.
var (
	ErrBadOptions    = errors.New("invalid roller options")
	ErrBadRecipe     = errors.New("invalid recipe specification")
	ErrUnknownRecipe = errors.New("recipe not loaded")
	ErrBadTier       = errors.New("invalid quality tier table")
	ErrBadRequest    = errors.New("invalid craft request")
	ErrBadSnapshot   = errors.New("snapshot payload is invalid")
)

// Options bounds the roller.
type Options struct {
	// MaxSuccessPermille caps the derived success rate. Zero means the default.
	MaxSuccessPermille int
	// MaxHistory keeps at most this many outcomes. Zero keeps everything.
	MaxHistory int
}

// Material is one ingredient of a recipe.
type Material struct {
	ID    string `json:"id"`
	Count int    `json:"count"`
}

// RecipeSpec is one loaded recipe.
type RecipeSpec struct {
	ID                  string     `json:"id"`
	BasePermille        int        `json:"base_permille"`
	ProficiencyPerLevel int        `json:"proficiency_per_level"`
	Materials           []Material `json:"materials"`
	RefundPermille      int        `json:"refund_permille"`
}

// CraftRequest is one settle request.
type CraftRequest struct {
	RecipeID    string `json:"recipe_id"`
	PlayerID    string `json:"player_id"`
	Proficiency int    `json:"proficiency"`
}

// QualityTier maps the low bound of a quality roll to a tier name.
type QualityTier struct {
	Name        string `json:"name"`
	MinPermille int    `json:"min_permille"`
}

// Outcome is the full result of one settle, including both raw draws.
type Outcome struct {
	RecipeID        string         `json:"recipe_id"`
	PlayerID        string         `json:"player_id"`
	Success         bool           `json:"success"`
	SuccessPermille int            `json:"success_permille"`
	SuccessDraw     int            `json:"success_draw"`
	QualityDraw     int            `json:"quality_draw"`
	Quality         string         `json:"quality"`
	Refund          map[string]int `json:"refund"`
}

// CraftOutcomeRoller owns the recipe table, the tier table and the history.
type CraftOutcomeRoller struct {
	opts Options
}

// NewCraftOutcomeRoller builds a roller, installing the default tier table.
func NewCraftOutcomeRoller(opts Options) *CraftOutcomeRoller {
	return &CraftOutcomeRoller{opts: opts}
}

// SetQualityTiers replaces the quality ladder, all or nothing.
func (r *CraftOutcomeRoller) SetQualityTiers(tiers []QualityTier) error {
	return errNotImplemented
}

// QualityTiers returns a copy of the quality ladder, lowest first.
func (r *CraftOutcomeRoller) QualityTiers() []QualityTier {
	return nil
}

// SetRecipe validates and stores one recipe, replacing an older revision.
func (r *CraftOutcomeRoller) SetRecipe(spec RecipeSpec) error {
	return errNotImplemented
}

// Recipe reads one loaded recipe.
func (r *CraftOutcomeRoller) Recipe(id string) (RecipeSpec, bool) {
	return RecipeSpec{}, false
}

// Recipes lists every loaded recipe ordered by id.
func (r *CraftOutcomeRoller) Recipes() []RecipeSpec {
	return nil
}

// SuccessPermille derives the capped success rate for one request.
func (r *CraftOutcomeRoller) SuccessPermille(req CraftRequest) (int, error) {
	return 0, errNotImplemented
}

// Roll settles one craft attempt with the supplied seed.
func (r *CraftOutcomeRoller) Roll(req CraftRequest, seed uint64) (Outcome, error) {
	return Outcome{}, errNotImplemented
}

// Batch settles requests in input order, all or nothing.
func (r *CraftOutcomeRoller) Batch(reqs []CraftRequest, seed uint64) ([]Outcome, error) {
	return nil, errNotImplemented
}

// Refund returns a copy of the refund table held by one outcome.
func (r *CraftOutcomeRoller) Refund(o Outcome) map[string]int {
	return nil
}

// History returns a copy of the recent outcomes, oldest first.
func (r *CraftOutcomeRoller) History() []Outcome {
	return nil
}

// Snapshot serialises the tier table, the recipes and the history.
func (r *CraftOutcomeRoller) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (r *CraftOutcomeRoller) Restore(data []byte) error {
	return errNotImplemented
}
