// Package task compacts tile placement inside shadow atlas pages by planning
// tile migrations that are applied under a per call budget.
package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Page lifecycle states.
const (
	PageLive    = "live"
	PageRetired = "retired"
)

// DefaultMovesPerFrame is the migration budget used when the caller does not
// care to tune it.
const DefaultMovesPerFrame = 4

// Errors returned by the defragger.
var (
	ErrInvalidConfig = errors.New("invalid atlas configuration")
	ErrNoSuchPage    = errors.New("atlas page does not exist")
	ErrPageRetired   = errors.New("atlas page is retired")
	ErrPageFull      = errors.New("atlas page has no free slot")
	ErrPageBusy      = errors.New("atlas page is being defragmented")
	ErrStaleHandle   = errors.New("tile handle is stale")
	ErrUnknownPlan   = errors.New("defrag plan is unknown")
	ErrPlanDone      = errors.New("defrag plan is complete")
	ErrNotFragmented = errors.New("page is already compact")
	ErrBadSnapshot   = errors.New("snapshot payload is invalid")
)

// TileHandle identifies one tile slot together with its generation counter.
type TileHandle struct {
	Page int    `json:"page"`
	Slot int    `json:"slot"`
	Gen  uint64 `json:"gen"`
}

// TileSlot is the live state of one slot inside a page.
type TileSlot struct {
	Page   int        `json:"page"`
	Slot   int        `json:"slot"`
	Handle TileHandle `json:"handle"`
	Pinned bool       `json:"pinned"`
}

// TileMove is one migration inside a defrag plan.
type TileMove struct {
	From TileHandle `json:"from"`
	To   int        `json:"to"`
}

// Plan is the ordered list of migrations computed for one page.
type Plan struct {
	ID           string     `json:"id"`
	Page         int        `json:"page"`
	Moves        []TileMove `json:"moves"`
	CreatedFrame int        `json:"created_frame"`
}

// Moved reports the progress of one ApplyStep call.
type Moved struct {
	PlanID    string `json:"plan_id"`
	Page      int    `json:"page"`
	Count     int    `json:"count"`
	Remaining int    `json:"remaining"`
}

// ShadowAtlasDefragger owns the pages, the live plans and the retirement table.
type ShadowAtlasDefragger struct{}

// NewShadowAtlasDefragger builds an atlas with the given page count, slot count
// per page and per call migration budget.
func NewShadowAtlasDefragger(pages, slotsPerPage, movesPerFrame int) *ShadowAtlasDefragger {
	return &ShadowAtlasDefragger{}
}

// Alloc claims the lowest free slot of a page.
func (d *ShadowAtlasDefragger) Alloc(page int) (TileHandle, error) {
	return TileHandle{}, errNotImplemented
}

// Free releases one tile slot and invalidates every handle of the old
// generation.
func (d *ShadowAtlasDefragger) Free(h TileHandle) error {
	return errNotImplemented
}

// Pin marks a tile as unmovable for the following defrag passes.
func (d *ShadowAtlasDefragger) Pin(h TileHandle) error {
	return errNotImplemented
}

// Unpin lets a tile take part in the next defrag pass again.
func (d *ShadowAtlasDefragger) Unpin(h TileHandle) error {
	return errNotImplemented
}

// Slot reads the live state of the slot named by a handle.
func (d *ShadowAtlasDefragger) Slot(h TileHandle) (TileSlot, error) {
	return TileSlot{}, errNotImplemented
}

// HandleValid reports whether a handle still names its original tile.
func (d *ShadowAtlasDefragger) HandleValid(h TileHandle) bool {
	return false
}

// Lookup resolves a page and slot pair to the current handle.
func (d *ShadowAtlasDefragger) Lookup(page, slot int) (TileHandle, bool) {
	return TileHandle{}, false
}

// Fragmentation reports free runs divided by free slots for one page.
func (d *ShadowAtlasDefragger) Fragmentation(page int) float64 {
	return 0
}

// RequestDefrag computes the migration plan that packs unpinned tiles toward
// the front of a page.
func (d *ShadowAtlasDefragger) RequestDefrag(page int) (Plan, error) {
	return Plan{}, errNotImplemented
}

// ApplyStep executes at most one budget worth of migrations of a plan.
func (d *ShadowAtlasDefragger) ApplyStep(planID string) (Moved, error) {
	return Moved{}, errNotImplemented
}

// PlanActive reports whether a page still carries an unfinished plan.
func (d *ShadowAtlasDefragger) PlanActive(page int) bool {
	return false
}

// RetirePage moves a page into the retirement table.
func (d *ShadowAtlasDefragger) RetirePage(page int) error {
	return errNotImplemented
}

// Retired reports whether a page sits in the retirement table.
func (d *ShadowAtlasDefragger) Retired(page int) bool {
	return false
}

// Snapshot serialises pages, slots, generations, plans and the retirement
// table.
func (d *ShadowAtlasDefragger) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole atlas state with a snapshot, all or nothing.
func (d *ShadowAtlasDefragger) Restore(data []byte) error {
	return errNotImplemented
}
