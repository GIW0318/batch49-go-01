// Package task settles crafting attempts: a capped success rate, a quality
// tier picked from an independent draw, and per material refunds on failure.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
)

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
	mu      sync.RWMutex
	opts    Options
	tiers   []QualityTier
	recipes map[string]RecipeSpec
	history []Outcome
}

// snapshotPayload is the persisted form of a roller's complete state.
type snapshotPayload struct {
	Tiers   []QualityTier `json:"tiers"`
	Recipes []RecipeSpec  `json:"recipes"`
	History []Outcome     `json:"history"`
}

// Two unrelated salts keep the success draw and the quality draw on
// independently derived streams instead of reusing one value.
const (
	successSegment uint64 = 0x9E3779B97F4A7C15
	qualitySegment uint64 = 0xC2B2AE3D27D4EB4F
)

// NewCraftOutcomeRoller builds a roller, installing the default tier table.
func NewCraftOutcomeRoller(opts Options) *CraftOutcomeRoller {
	return &CraftOutcomeRoller{
		opts:    opts,
		tiers:   defaultQualityTiers(),
		recipes: make(map[string]RecipeSpec),
	}
}

// successCap resolves the effective success cap, applying the default whenever
// the option leaves it unset (and defending a zero-value roller).
func (r *CraftOutcomeRoller) successCap() int {
	if r.opts.MaxSuccessPermille <= 0 {
		return DefaultMaxSuccessPermille
	}
	return r.opts.MaxSuccessPermille
}

// defaultQualityTiers installs a valid lowest-first ladder.
func defaultQualityTiers() []QualityTier {
	return []QualityTier{
		{Name: "common", MinPermille: 0},
		{Name: "fine", MinPermille: 400},
		{Name: "rare", MinPermille: 750},
		{Name: "epic", MinPermille: 900},
	}
}

// SetQualityTiers replaces the quality ladder, all or nothing.
func (r *CraftOutcomeRoller) SetQualityTiers(tiers []QualityTier) error {
	if err := validateQualityTiers(tiers); err != nil {
		return err
	}
	next := cloneTiers(tiers)

	r.mu.Lock()
	r.tiers = next
	r.mu.Unlock()
	return nil
}

// QualityTiers returns a copy of the quality ladder, lowest first.
func (r *CraftOutcomeRoller) QualityTiers() []QualityTier {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return cloneTiers(r.tiers)
}

// SetRecipe validates and stores one recipe, replacing an older revision.
func (r *CraftOutcomeRoller) SetRecipe(spec RecipeSpec) error {
	if err := validateRecipe(spec); err != nil {
		return err
	}
	stored := cloneRecipe(spec)

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.recipes == nil {
		r.recipes = make(map[string]RecipeSpec)
	}
	// A single map assignment under the write lock publishes one complete
	// revision; failed validation never reaches this point, so an existing
	// recipe with the same id is left untouched.
	r.recipes[spec.ID] = stored
	return nil
}

// Recipe reads one loaded recipe.
func (r *CraftOutcomeRoller) Recipe(id string) (RecipeSpec, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	spec, ok := r.recipes[id]
	if !ok {
		return RecipeSpec{}, false
	}
	return cloneRecipe(spec), true
}

// Recipes lists every loaded recipe ordered by id.
func (r *CraftOutcomeRoller) Recipes() []RecipeSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()

	ids := make([]string, 0, len(r.recipes))
	for id := range r.recipes {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	out := make([]RecipeSpec, 0, len(ids))
	for _, id := range ids {
		out = append(out, cloneRecipe(r.recipes[id]))
	}
	return out
}

// SuccessPermille derives the capped success rate for one request.
func (r *CraftOutcomeRoller) SuccessPermille(req CraftRequest) (int, error) {
	if err := validateRequest(req); err != nil {
		return 0, err
	}

	r.mu.RLock()
	spec, ok := r.recipes[req.RecipeID]
	capPermille := r.successCap()
	r.mu.RUnlock()
	if !ok {
		return 0, fmt.Errorf("%w: %q", ErrUnknownRecipe, req.RecipeID)
	}

	return deriveSuccessPermille(spec, req.Proficiency, capPermille), nil
}

// Roll settles one craft attempt with the supplied seed.
func (r *CraftOutcomeRoller) Roll(req CraftRequest, seed uint64) (Outcome, error) {
	view, err := r.loadView(req)
	if err != nil {
		return Outcome{}, err
	}

	out := settle(req, seed, view)
	r.appendHistory(out)
	// The history keeps an independent refund map; the returned outcome owns
	// the map it was built with.
	return out, nil
}

// Batch settles requests in input order, all or nothing.
func (r *CraftOutcomeRoller) Batch(reqs []CraftRequest, seed uint64) ([]Outcome, error) {
	if len(reqs) == 0 {
		return []Outcome{}, nil
	}

	// Resolve one complete table revision and settle every request before
	// touching history, so a later invalid request cannot leave a partial
	// batch behind.
	view := r.tableSnapshot()

	outcomes := make([]Outcome, 0, len(reqs))
	for _, req := range reqs {
		if err := validateRequest(req); err != nil {
			return nil, err
		}
		spec, ok := view.recipes[req.RecipeID]
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrUnknownRecipe, req.RecipeID)
		}
		itemView := view
		itemView.spec = spec
		outcomes = append(outcomes, settle(req, seed, itemView))
	}

	r.appendHistory(outcomes...)
	return outcomes, nil
}

// Refund returns a copy of the refund table held by one outcome.
func (r *CraftOutcomeRoller) Refund(o Outcome) map[string]int {
	out := make(map[string]int, len(o.Refund))
	for id, count := range o.Refund {
		out[id] = count
	}
	return out
}

// History returns a copy of the recent outcomes, oldest first.
func (r *CraftOutcomeRoller) History() []Outcome {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]Outcome, len(r.history))
	for i, item := range r.history {
		out[i] = cloneOutcome(item)
	}
	return out
}

// Snapshot serialises the tier table, the recipes and the history.
func (r *CraftOutcomeRoller) Snapshot() ([]byte, error) {
	r.mu.RLock()
	payload := snapshotPayload{
		Tiers:   cloneTiers(r.tiers),
		Recipes: make([]RecipeSpec, 0, len(r.recipes)),
		History: make([]Outcome, 0, len(r.history)),
	}
	ids := make([]string, 0, len(r.recipes))
	for id := range r.recipes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		payload.Recipes = append(payload.Recipes, cloneRecipe(r.recipes[id]))
	}
	for _, item := range r.history {
		payload.History = append(payload.History, cloneOutcome(item))
	}
	r.mu.RUnlock()

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	return data, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (r *CraftOutcomeRoller) Restore(data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("%w: empty payload", ErrBadSnapshot)
	}

	var payload snapshotPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}

	// Validate and normalise everything into fresh storage before taking the
	// write lock. Any failure leaves the running state exactly as it was.
	if err := validateQualityTiers(payload.Tiers); err != nil {
		return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	tiers := cloneTiers(payload.Tiers)

	recipes := make(map[string]RecipeSpec, len(payload.Recipes))
	for _, spec := range payload.Recipes {
		if err := validateRecipe(spec); err != nil {
			return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
		}
		recipes[spec.ID] = cloneRecipe(spec)
	}

	history := make([]Outcome, len(payload.History))
	for i, item := range payload.History {
		history[i] = cloneOutcome(item)
	}

	r.mu.Lock()
	r.tiers = tiers
	r.recipes = recipes
	r.history = history
	r.mu.Unlock()
	return nil
}

// stateView is one immutable revision of the tables needed for a settle.
type stateView struct {
	tiers       []QualityTier
	recipes     map[string]RecipeSpec
	spec        RecipeSpec
	capPermille int
}

// tableSnapshot copies one complete, immutable revision of the tables.
func (r *CraftOutcomeRoller) tableSnapshot() stateView {
	r.mu.RLock()
	defer r.mu.RUnlock()

	view := stateView{
		tiers:       cloneTiers(r.tiers),
		capPermille: r.successCap(),
		recipes:     make(map[string]RecipeSpec, len(r.recipes)),
	}
	for id, spec := range r.recipes {
		view.recipes[id] = cloneRecipe(spec)
	}
	return view
}

// loadView validates the request and resolves its recipe against one complete
// table revision.
func (r *CraftOutcomeRoller) loadView(req CraftRequest) (stateView, error) {
	if err := validateRequest(req); err != nil {
		return stateView{}, err
	}

	view := r.tableSnapshot()
	spec, ok := view.recipes[req.RecipeID]
	if !ok {
		return stateView{}, fmt.Errorf("%w: %q", ErrUnknownRecipe, req.RecipeID)
	}
	view.spec = spec
	return view, nil
}

// appendHistory stores outcomes oldest first, keeps only the newest entries
// and gives the history its own refund maps.
func (r *CraftOutcomeRoller) appendHistory(outcomes ...Outcome) {
	if len(outcomes) == 0 {
		return
	}

	r.mu.Lock()
	for _, outcome := range outcomes {
		outcome.Refund = cloneRefund(outcome.Refund)
		r.history = append(r.history, outcome)
	}
	if maxHistory := r.opts.MaxHistory; maxHistory > 0 && len(r.history) > maxHistory {
		kept := make([]Outcome, maxHistory)
		copy(kept, r.history[len(r.history)-maxHistory:])
		r.history = kept
	}
	r.mu.Unlock()
}

// settle is the pure settlement function: identical request, seed and table
// revision always produce field-identical output, independent of call count
// or history.
func settle(req CraftRequest, seed uint64, view stateView) Outcome {
	rate := deriveSuccessPermille(view.spec, req.Proficiency, view.capPermille)
	successDraw := permilleDraw(req, seed, successSegment)
	qualityDraw := permilleDraw(req, seed, qualitySegment)

	outcome := Outcome{
		RecipeID:        req.RecipeID,
		PlayerID:        req.PlayerID,
		SuccessPermille: rate,
		SuccessDraw:     successDraw,
		QualityDraw:     qualityDraw,
		Refund:          map[string]int{},
	}

	// Success is strictly below the derived rate. Both draws always run, so a
	// capped/guaranteed rate still goes through quality selection.
	if successDraw < rate {
		outcome.Success = true
		outcome.Quality = qualityForDraw(view.tiers, qualityDraw)
		return outcome
	}

	outcome.Success = false
	outcome.Quality = ""
	outcome.Refund = refundForFailure(view.spec)
	return outcome
}

// validateQualityTiers enforces the all-or-nothing ladder rules.
func validateQualityTiers(tiers []QualityTier) error {
	if len(tiers) < 3 {
		return fmt.Errorf("%w: at least three tiers are required", ErrBadTier)
	}
	if tiers[0].MinPermille != 0 {
		return fmt.Errorf("%w: lowest tier must start at zero", ErrBadTier)
	}

	names := make(map[string]struct{}, len(tiers))
	for i, tier := range tiers {
		if strings.TrimSpace(tier.Name) == "" {
			return fmt.Errorf("%w: tier %d has an empty name", ErrBadTier, i)
		}
		if _, duplicated := names[tier.Name]; duplicated {
			return fmt.Errorf("%w: duplicate tier name %q", ErrBadTier, tier.Name)
		}
		names[tier.Name] = struct{}{}
		if i > 0 && tier.MinPermille <= tiers[i-1].MinPermille {
			return fmt.Errorf("%w: tier minima must be strictly increasing", ErrBadTier)
		}
	}
	return nil
}

// validateRecipe enforces the all-or-nothing recipe rules.
func validateRecipe(spec RecipeSpec) error {
	if strings.TrimSpace(spec.ID) == "" {
		return fmt.Errorf("%w: recipe id must not be empty", ErrBadRecipe)
	}
	if len(spec.Materials) == 0 {
		return fmt.Errorf("%w: recipe %q needs at least one material", ErrBadRecipe, spec.ID)
	}

	materialIDs := make(map[string]struct{}, len(spec.Materials))
	for _, material := range spec.Materials {
		if strings.TrimSpace(material.ID) == "" {
			return fmt.Errorf("%w: recipe %q has a material with an empty id", ErrBadRecipe, spec.ID)
		}
		if material.Count <= 0 {
			return fmt.Errorf("%w: material %q count must be positive", ErrBadRecipe, material.ID)
		}
		if _, duplicated := materialIDs[material.ID]; duplicated {
			return fmt.Errorf("%w: material %q listed more than once", ErrBadRecipe, material.ID)
		}
		materialIDs[material.ID] = struct{}{}
	}

	if spec.BasePermille < 0 || spec.BasePermille > MaxPermille {
		return fmt.Errorf("%w: base success rate out of range", ErrBadRecipe)
	}
	if spec.RefundPermille < 0 || spec.RefundPermille > MaxPermille {
		return fmt.Errorf("%w: refund rate out of range", ErrBadRecipe)
	}
	if spec.ProficiencyPerLevel < 0 {
		return fmt.Errorf("%w: proficiency bonus must not be negative", ErrBadRecipe)
	}
	return nil
}

// validateRequest checks the per-request legality rules.
func validateRequest(req CraftRequest) error {
	if strings.TrimSpace(req.RecipeID) == "" ||
		strings.TrimSpace(req.PlayerID) == "" ||
		req.Proficiency < 0 {
		return fmt.Errorf("%w: recipe id, player id and non-negative proficiency are required", ErrBadRequest)
	}
	return nil
}

// deriveSuccessPermille computes base plus proficiency bonus and caps it.
func deriveSuccessPermille(spec RecipeSpec, proficiency, capPermille int) int {
	rate := spec.BasePermille
	if proficiency > 0 && spec.ProficiencyPerLevel > 0 {
		// Saturate instead of overflowing on extreme inputs.
		if spec.ProficiencyPerLevel > (math.MaxInt-rate)/proficiency {
			rate = math.MaxInt
		} else {
			rate += proficiency * spec.ProficiencyPerLevel
		}
	}
	if capPermille > 0 && rate > capPermille {
		rate = capPermille
	}
	if rate < 0 {
		rate = 0
	}
	return rate
}

// qualityForDraw selects the highest tier whose minimum is at or below draw,
// so a draw exactly on a boundary belongs to the higher tier.
func qualityForDraw(tiers []QualityTier, draw int) string {
	name := ""
	for _, tier := range tiers {
		if draw >= tier.MinPermille {
			name = tier.Name
		}
	}
	return name
}

// refundForFailure floors every material share independently; remainders are
// neither carried forward nor rounded up.
func refundForFailure(spec RecipeSpec) map[string]int {
	refund := make(map[string]int, len(spec.Materials))
	for _, material := range spec.Materials {
		refund[material.ID] = int(int64(material.Count) * int64(spec.RefundPermille) / int64(MaxPermille))
	}
	return refund
}

// permilleDraw derives one deterministic value in [0, DrawRange) from the
// seed, a segment salt and the request identity. The two settlement segments
// pass different salts and therefore never reuse the same draw.
func permilleDraw(req CraftRequest, seed, segment uint64) int {
	h := splitMix64(seed ^ segment)
	h = splitMix64(h ^ fnv1a64(req.RecipeID))
	h = splitMix64(h ^ fnv1a64(req.PlayerID))
	h = splitMix64(h ^ uint64(req.Proficiency))
	return int(h % uint64(DrawRange))
}

// splitMix64 is a small avalanching mixer so nearby seeds decorrelate.
func splitMix64(x uint64) uint64 {
	x += 0x9E3779B97F4A7C15
	x = (x ^ (x >> 30)) * 0xBF58476D1CE4E5B9
	x = (x ^ (x >> 27)) * 0x94D049BB133111EB
	return x ^ (x >> 31)
}

// fnv1a64 hashes request strings into the draw derivation.
func fnv1a64(value string) uint64 {
	h := uint64(1469598103934665603)
	for i := 0; i < len(value); i++ {
		h ^= uint64(value[i])
		h *= 1099511628211
	}
	return h
}

func cloneTiers(tiers []QualityTier) []QualityTier {
	if tiers == nil {
		return nil
	}
	out := make([]QualityTier, len(tiers))
	copy(out, tiers)
	return out
}

func cloneRecipe(spec RecipeSpec) RecipeSpec {
	out := spec
	out.Materials = cloneMaterials(spec.Materials)
	return out
}

func cloneMaterials(materials []Material) []Material {
	if materials == nil {
		return nil
	}
	out := make([]Material, len(materials))
	copy(out, materials)
	return out
}

func cloneRefund(refund map[string]int) map[string]int {
	if refund == nil {
		return nil
	}
	out := make(map[string]int, len(refund))
	for id, count := range refund {
		out[id] = count
	}
	return out
}

func cloneOutcome(outcome Outcome) Outcome {
	out := outcome
	out.Refund = cloneRefund(outcome.Refund)
	return out
}
