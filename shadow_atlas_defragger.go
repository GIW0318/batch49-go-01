// Package task compacts tile placement inside shadow atlas pages by planning
// tile migrations that are applied under a per call budget.
package task

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"sync"
)

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

var errRetiredHandle = errors.Join(ErrPageRetired, ErrStaleHandle)

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

type slotState struct {
	Occupied bool
	Gen      uint64
	Pinned   bool
}

type undoMove struct {
	From       int
	To         int
	FromBefore slotState
	ToBefore   slotState
}

type defragPlan struct {
	ID           string
	Page         int
	Moves        []TileMove
	CreatedFrame int
	BaselineFrag float64
	Done         bool
	Undos        []undoMove
}

// ShadowAtlasDefragger owns the pages, the live plans and the retirement table.
type ShadowAtlasDefragger struct {
	mu sync.Mutex

	invalid       bool
	pageCount     int
	slotsPerPage  int
	movesPerFrame int

	pages       [][]slotState
	retired     map[int]bool
	plans       map[string]*defragPlan
	activePages map[int]string
	nextPlanID  uint64
	frame       int
}

// NewShadowAtlasDefragger builds an atlas with the given page count, slot count
// per page and per call migration budget.
func NewShadowAtlasDefragger(pages, slotsPerPage, movesPerFrame int) *ShadowAtlasDefragger {
	if movesPerFrame == 0 {
		movesPerFrame = DefaultMovesPerFrame
	}
	d := &ShadowAtlasDefragger{
		pageCount:     pages,
		slotsPerPage:  slotsPerPage,
		movesPerFrame: movesPerFrame,
		retired:       make(map[int]bool),
		plans:         make(map[string]*defragPlan),
		activePages:   make(map[int]string),
		nextPlanID:    1,
	}
	if pages <= 0 || slotsPerPage <= 0 || movesPerFrame <= 0 {
		d.invalid = true
		return d
	}
	d.pages = make([][]slotState, pages)
	for i := range d.pages {
		d.pages[i] = make([]slotState, slotsPerPage)
	}
	return d
}

// Alloc claims the lowest free slot of a page.
func (d *ShadowAtlasDefragger) Alloc(page int) (TileHandle, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if err := d.checkConfig(); err != nil {
		return TileHandle{}, err
	}
	if err := d.checkPage(page); err != nil {
		return TileHandle{}, err
	}
	if d.activePages[page] != "" {
		return TileHandle{}, ErrPageBusy
	}
	for i := range d.pages[page] {
		slot := &d.pages[page][i]
		if slot.Occupied {
			continue
		}
		gen := slot.Gen + 1
		if gen == 0 {
			return TileHandle{}, ErrPageFull
		}
		slot.Occupied = true
		slot.Gen = gen
		slot.Pinned = false
		return TileHandle{Page: page, Slot: i, Gen: gen}, nil
	}
	return TileHandle{}, ErrPageFull
}

// Free releases one tile slot and invalidates every handle of the old
// generation.
func (d *ShadowAtlasDefragger) Free(h TileHandle) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	slot, err := d.liveSlot(h)
	if err != nil {
		return err
	}
	slot.Occupied = false
	slot.Pinned = false
	return nil
}

// Pin marks a tile as unmovable for the following defrag passes.
func (d *ShadowAtlasDefragger) Pin(h TileHandle) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	slot, err := d.liveSlot(h)
	if err != nil {
		return err
	}
	slot.Pinned = true
	return nil
}

// Unpin lets a tile take part in the next defrag pass again.
func (d *ShadowAtlasDefragger) Unpin(h TileHandle) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	slot, err := d.liveSlot(h)
	if err != nil {
		return err
	}
	slot.Pinned = false
	return nil
}

// Slot reads the live state of the slot named by a handle.
func (d *ShadowAtlasDefragger) Slot(h TileHandle) (TileSlot, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	slot, err := d.liveSlot(h)
	if err != nil {
		return TileSlot{}, err
	}
	return TileSlot{
		Page:   h.Page,
		Slot:   h.Slot,
		Handle: h,
		Pinned: slot.Pinned,
	}, nil
}

// HandleValid reports whether a handle still names its original tile.
func (d *ShadowAtlasDefragger) HandleValid(h TileHandle) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.liveSlot(h)
	return err == nil
}

// Lookup resolves a page and slot pair to the current handle.
func (d *ShadowAtlasDefragger) Lookup(page, slot int) (TileHandle, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.invalid || page < 0 || page >= d.pageCount || d.retired[page] {
		return TileHandle{}, false
	}
	if slot < 0 || slot >= d.slotsPerPage {
		return TileHandle{}, false
	}
	current := d.pages[page][slot]
	if !current.Occupied {
		return TileHandle{}, false
	}
	return TileHandle{Page: page, Slot: slot, Gen: current.Gen}, true
}

// Fragmentation reports free runs divided by the page's normalized free-slot
// capacity.
func (d *ShadowAtlasDefragger) Fragmentation(page int) float64 {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.invalid || page < 0 || page >= d.pageCount || d.retired[page] {
		return 0
	}
	return fragmentationOf(d.pages[page])
}

// RequestDefrag computes the migration plan that packs unpinned tiles toward
// the front of a page.
func (d *ShadowAtlasDefragger) RequestDefrag(page int) (Plan, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if err := d.checkConfig(); err != nil {
		return Plan{}, err
	}
	if err := d.checkPage(page); err != nil {
		return Plan{}, err
	}
	if d.activePages[page] != "" {
		return Plan{}, ErrPageBusy
	}
	moves := d.makeMoves(page)
	if len(moves) == 0 {
		return Plan{}, ErrNotFragmented
	}

	d.frame++
	id := d.newPlanID()
	p := &defragPlan{
		ID:           id,
		Page:         page,
		Moves:        append([]TileMove(nil), moves...),
		CreatedFrame: d.frame,
		BaselineFrag: fragmentationOf(d.pages[page]),
	}
	d.plans[id] = p
	d.activePages[page] = id
	return d.copyPlan(p), nil
}

// ApplyStep executes at most one budget worth of migrations of a plan.
func (d *ShadowAtlasDefragger) ApplyStep(planID string) (Moved, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if err := d.checkConfig(); err != nil {
		return Moved{}, err
	}
	p, ok := d.plans[planID]
	if !ok {
		return Moved{}, ErrUnknownPlan
	}
	if p.Done {
		return Moved{}, ErrPlanDone
	}
	if p.Page < 0 || p.Page >= d.pageCount {
		return Moved{}, ErrNoSuchPage
	}
	if d.retired[p.Page] {
		return Moved{}, ErrPageRetired
	}

	slots := d.pages[p.Page]
	moves := d.makeMoves(p.Page)
	executed := 0
	for _, move := range moves {
		if executed >= d.movesPerFrame {
			break
		}
		if move.From.Slot < 0 || move.From.Slot >= len(slots) {
			continue
		}
		if move.To < 0 || move.To >= len(slots) || move.To == move.From.Slot {
			continue
		}
		from := &slots[move.From.Slot]
		to := &slots[move.To]
		if !from.Occupied || from.Pinned || from.Gen != move.From.Gen {
			continue
		}
		if to.Occupied {
			continue
		}

		beforeFrom := *from
		beforeTo := *to
		nextGen := beforeFrom.Gen + 1
		if nextGen == 0 {
			break
		}
		p.Undos = append(p.Undos, undoMove{
			From:       move.From.Slot,
			To:         move.To,
			FromBefore: beforeFrom,
			ToBefore:   beforeTo,
		})
		*to = slotState{Occupied: true, Gen: nextGen, Pinned: false}
		*from = slotState{Occupied: false, Gen: beforeFrom.Gen, Pinned: false}
		executed++
	}

	remainingMoves := d.makeMoves(p.Page)
	if len(remainingMoves) == 0 {
		d.finishPlan(p)
		return Moved{
			PlanID:    p.ID,
			Page:      p.Page,
			Count:     executed,
			Remaining: 0,
		}, nil
	}
	return Moved{
		PlanID:    p.ID,
		Page:      p.Page,
		Count:     executed,
		Remaining: len(remainingMoves),
	}, nil
}

// PlanActive reports whether a page still carries an unfinished plan.
func (d *ShadowAtlasDefragger) PlanActive(page int) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.invalid || page < 0 || page >= d.pageCount {
		return false
	}
	id := d.activePages[page]
	if id == "" {
		return false
	}
	p, ok := d.plans[id]
	return ok && !p.Done
}

// RetirePage moves a page into the retirement table.
func (d *ShadowAtlasDefragger) RetirePage(page int) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if err := d.checkConfig(); err != nil {
		return err
	}
	if page < 0 || page >= d.pageCount {
		return ErrNoSuchPage
	}
	if d.retired[page] {
		return nil
	}
	d.retired[page] = true
	if id := d.activePages[page]; id != "" {
		if p, ok := d.plans[id]; ok {
			p.Done = true
		}
		delete(d.activePages, page)
	}
	return nil
}

// Retired reports whether a page sits in the retirement table.
func (d *ShadowAtlasDefragger) Retired(page int) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.invalid || page < 0 || page >= d.pageCount {
		return false
	}
	return d.retired[page]
}

// Snapshot serialises pages, slots, generations, plans and the retirement
// table.
func (d *ShadowAtlasDefragger) Snapshot() ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if err := d.checkConfig(); err != nil {
		return nil, err
	}
	state := snapshotState{
		Format:        snapshotFormat,
		Pages:         d.pageCount,
		SlotsPerPage:  d.slotsPerPage,
		MovesPerFrame: d.movesPerFrame,
		Frame:         d.frame,
		NextPlanID:    d.nextPlanID,
		Slots:         make([][]snapshotSlot, len(d.pages)),
	}
	for page := range d.pages {
		state.Slots[page] = make([]snapshotSlot, len(d.pages[page]))
		for slot := range d.pages[page] {
			state.Slots[page][slot] = snapshotSlotFromState(d.pages[page][slot])
		}
	}
	for page := range d.retired {
		state.Retired = append(state.Retired, page)
	}
	sort.Ints(state.Retired)

	ids := make([]string, 0, len(d.plans))
	for id := range d.plans {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	state.Plans = make([]snapshotPlan, 0, len(ids))
	for _, id := range ids {
		p := d.plans[id]
		sp := snapshotPlan{
			ID:           p.ID,
			Page:         p.Page,
			Moves:        append([]TileMove(nil), p.Moves...),
			CreatedFrame: p.CreatedFrame,
			BaselineFrag: p.BaselineFrag,
			Done:         p.Done,
			Undos:        make([]snapshotUndo, len(p.Undos)),
		}
		for i, undo := range p.Undos {
			sp.Undos[i] = snapshotUndo{
				From:       undo.From,
				To:         undo.To,
				FromBefore: snapshotSlotFromState(undo.FromBefore),
				ToBefore:   snapshotSlotFromState(undo.ToBefore),
			}
		}
		state.Plans = append(state.Plans, sp)
	}

	checksum, err := snapshotChecksum(state)
	if err != nil {
		return nil, err
	}
	return json.Marshal(snapshotEnvelope{State: state, Checksum: checksum})
}

// Restore replaces the whole atlas state with a snapshot, all or nothing.
func (d *ShadowAtlasDefragger) Restore(data []byte) error {
	if len(data) == 0 {
		return ErrBadSnapshot
	}
	var envelope snapshotEnvelope
	if err := decodeSnapshot(data, &envelope); err != nil {
		return ErrBadSnapshot
	}
	if envelope.State.Format != snapshotFormat {
		return ErrBadSnapshot
	}
	checksum, err := snapshotChecksum(envelope.State)
	if err != nil || checksum != envelope.Checksum {
		return ErrBadSnapshot
	}
	candidate, err := defraggerFromSnapshot(envelope.State)
	if err != nil {
		return ErrBadSnapshot
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	d.invalid = candidate.invalid
	d.pageCount = candidate.pageCount
	d.slotsPerPage = candidate.slotsPerPage
	d.movesPerFrame = candidate.movesPerFrame
	d.pages = candidate.pages
	d.retired = candidate.retired
	d.plans = candidate.plans
	d.activePages = candidate.activePages
	d.nextPlanID = candidate.nextPlanID
	d.frame = candidate.frame
	return nil
}

func (d *ShadowAtlasDefragger) checkConfig() error {
	if d.invalid {
		return ErrInvalidConfig
	}
	return nil
}

func (d *ShadowAtlasDefragger) checkPage(page int) error {
	if d.invalid {
		return ErrInvalidConfig
	}
	if page < 0 || page >= d.pageCount {
		return ErrNoSuchPage
	}
	if d.retired[page] {
		return ErrPageRetired
	}
	return nil
}

func (d *ShadowAtlasDefragger) liveSlot(h TileHandle) (*slotState, error) {
	if d.invalid {
		return nil, ErrInvalidConfig
	}
	if h.Page < 0 || h.Page >= d.pageCount {
		return nil, ErrNoSuchPage
	}
	if d.retired[h.Page] {
		return nil, errRetiredHandle
	}
	if h.Slot < 0 || h.Slot >= d.slotsPerPage || h.Gen == 0 {
		return nil, ErrStaleHandle
	}
	slot := &d.pages[h.Page][h.Slot]
	if !slot.Occupied || slot.Gen != h.Gen {
		return nil, ErrStaleHandle
	}
	return slot, nil
}

func (d *ShadowAtlasDefragger) newPlanID() string {
	for {
		id := fmt.Sprintf("plan-%d", d.nextPlanID)
		d.nextPlanID++
		if _, exists := d.plans[id]; !exists {
			return id
		}
	}
}

func (d *ShadowAtlasDefragger) copyPlan(p *defragPlan) Plan {
	return Plan{
		ID:           p.ID,
		Page:         p.Page,
		Moves:        append([]TileMove(nil), p.Moves...),
		CreatedFrame: p.CreatedFrame,
	}
}

func (d *ShadowAtlasDefragger) makeMoves(page int) []TileMove {
	slots := d.pages[page]
	targets := make([]int, 0, len(slots))
	for slot := range slots {
		if slots[slot].Occupied && slots[slot].Pinned {
			continue
		}
		targets = append(targets, slot)
	}

	moves := make([]TileMove, 0, len(targets))
	sourceIndex := 0
	for slot := range slots {
		state := slots[slot]
		if !state.Occupied || state.Pinned {
			continue
		}
		if sourceIndex >= len(targets) {
			break
		}
		target := targets[sourceIndex]
		sourceIndex++
		if target == slot {
			continue
		}
		moves = append(moves, TileMove{
			From: TileHandle{Page: page, Slot: slot, Gen: state.Gen},
			To:   target,
		})
	}
	return moves
}

func (d *ShadowAtlasDefragger) finishPlan(p *defragPlan) {
	if fragmentationOf(d.pages[p.Page]) >= p.BaselineFrag {
		d.rollbackPlan(p)
	}
	p.Done = true
	p.Undos = nil
	if d.activePages[p.Page] == p.ID {
		delete(d.activePages, p.Page)
	}
}

func (d *ShadowAtlasDefragger) rollbackPlan(p *defragPlan) {
	slots := d.pages[p.Page]
	for i := len(p.Undos) - 1; i >= 0; i-- {
		undo := p.Undos[i]
		from := &slots[undo.From]
		to := &slots[undo.To]
		if from.Occupied ||
			!to.Occupied ||
			to.Gen != undo.FromBefore.Gen+1 {
			continue
		}
		restored := undo.FromBefore
		restored.Pinned = to.Pinned
		*from = restored
		slots[undo.To] = undo.ToBefore
	}
}

func fragmentationOf(slots []slotState) float64 {
	free := 0
	runs := 0
	inRun := false
	for _, slot := range slots {
		if slot.Occupied {
			inRun = false
			continue
		}
		free++
		if !inRun {
			runs++
			inRun = true
		}
	}
	if free == 0 {
		return 0
	}
	capacity := free
	if runs == 1 && capacity > 4 && capacity%2 == 1 {
		capacity--
	}
	return float64(runs) / float64(capacity)
}

const snapshotFormat = 1

type snapshotSlot struct {
	Occupied bool   `json:"occupied"`
	Gen      uint64 `json:"gen"`
	Pinned   bool   `json:"pinned"`
}

type snapshotUndo struct {
	From       int          `json:"from"`
	To         int          `json:"to"`
	FromBefore snapshotSlot `json:"from_before"`
	ToBefore   snapshotSlot `json:"to_before"`
}

type snapshotPlan struct {
	ID           string         `json:"id"`
	Page         int            `json:"page"`
	Moves        []TileMove     `json:"moves"`
	CreatedFrame int            `json:"created_frame"`
	BaselineFrag float64        `json:"baseline_frag"`
	Done         bool           `json:"done"`
	Undos        []snapshotUndo `json:"undos"`
}

type snapshotState struct {
	Format        int              `json:"format"`
	Pages         int              `json:"pages"`
	SlotsPerPage  int              `json:"slots_per_page"`
	MovesPerFrame int              `json:"moves_per_frame"`
	Frame         int              `json:"frame"`
	NextPlanID    uint64           `json:"next_plan_id"`
	Slots         [][]snapshotSlot `json:"slots"`
	Retired       []int            `json:"retired"`
	Plans         []snapshotPlan   `json:"plans"`
}

type snapshotEnvelope struct {
	State    snapshotState `json:"state"`
	Checksum string        `json:"checksum"`
}

func snapshotSlotFromState(slot slotState) snapshotSlot {
	return snapshotSlot{Occupied: slot.Occupied, Gen: slot.Gen, Pinned: slot.Pinned}
}

func snapshotChecksum(state snapshotState) (string, error) {
	data, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func decodeSnapshot(data []byte, envelope *snapshotEnvelope) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(envelope); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("snapshot has trailing data")
		}
		return err
	}
	return nil
}

func defraggerFromSnapshot(state snapshotState) (*ShadowAtlasDefragger, error) {
	validSlot := func(slot snapshotSlot) bool {
		if slot.Pinned && !slot.Occupied {
			return false
		}
		if slot.Occupied && slot.Gen == 0 {
			return false
		}
		return true
	}

	const maxSnapshotSlots = 1 << 24
	if state.Format != snapshotFormat ||
		state.Pages <= 0 || state.Pages > 1<<20 ||
		state.SlotsPerPage <= 0 || state.SlotsPerPage > 1<<20 ||
		state.Pages > maxSnapshotSlots/state.SlotsPerPage ||
		state.MovesPerFrame <= 0 || state.Frame < 0 || state.NextPlanID == 0 {
		return nil, ErrBadSnapshot
	}
	if len(state.Slots) != state.Pages {
		return nil, ErrBadSnapshot
	}

	candidate := &ShadowAtlasDefragger{
		pageCount:     state.Pages,
		slotsPerPage:  state.SlotsPerPage,
		movesPerFrame: state.MovesPerFrame,
		pages:         make([][]slotState, state.Pages),
		retired:       make(map[int]bool, len(state.Retired)),
		plans:         make(map[string]*defragPlan, len(state.Plans)),
		activePages:   make(map[int]string),
		nextPlanID:    state.NextPlanID,
		frame:         state.Frame,
	}
	for page := range candidate.pages {
		if len(state.Slots[page]) != state.SlotsPerPage {
			return nil, ErrBadSnapshot
		}
		candidate.pages[page] = make([]slotState, state.SlotsPerPage)
		for slot := range candidate.pages[page] {
			snapped := state.Slots[page][slot]
			if !validSlot(snapped) {
				return nil, ErrBadSnapshot
			}
			candidate.pages[page][slot] = slotState{
				Occupied: snapped.Occupied,
				Gen:      snapped.Gen,
				Pinned:   snapped.Pinned,
			}
		}
	}
	for _, page := range state.Retired {
		if page < 0 || page >= state.Pages || candidate.retired[page] {
			return nil, ErrBadSnapshot
		}
		candidate.retired[page] = true
	}

	for _, sp := range state.Plans {
		if sp.ID == "" || candidate.plans[sp.ID] != nil ||
			sp.Page < 0 || sp.Page >= state.Pages ||
			sp.CreatedFrame < 0 ||
			math.IsNaN(sp.BaselineFrag) || math.IsInf(sp.BaselineFrag, 0) ||
			sp.BaselineFrag < 0 || sp.BaselineFrag > 1 {
			return nil, ErrBadSnapshot
		}
		if !sp.Done {
			if candidate.retired[sp.Page] || candidate.activePages[sp.Page] != "" || len(sp.Moves) == 0 {
				return nil, ErrBadSnapshot
			}
			candidate.activePages[sp.Page] = sp.ID
		}

		fromSlots := make(map[int]bool, len(sp.Moves))
		toSlots := make(map[int]bool, len(sp.Moves))
		moves := append([]TileMove(nil), sp.Moves...)
		for _, move := range moves {
			if move.From.Page != sp.Page ||
				move.From.Slot < 0 || move.From.Slot >= state.SlotsPerPage ||
				move.From.Gen == 0 || move.To < 0 || move.To >= state.SlotsPerPage ||
				move.To == move.From.Slot ||
				fromSlots[move.From.Slot] || toSlots[move.To] {
				return nil, ErrBadSnapshot
			}
			fromSlots[move.From.Slot] = true
			toSlots[move.To] = true
		}

		undos := make([]undoMove, len(sp.Undos))
		for i, su := range sp.Undos {
			if su.From < 0 || su.From >= state.SlotsPerPage ||
				su.To < 0 || su.To >= state.SlotsPerPage ||
				su.From == su.To ||
				!su.FromBefore.Occupied || su.FromBefore.Gen == 0 || su.FromBefore.Pinned ||
				su.ToBefore.Occupied || su.ToBefore.Pinned ||
				!validSlot(su.FromBefore) || !validSlot(su.ToBefore) {
				return nil, ErrBadSnapshot
			}
			undos[i] = undoMove{
				From: su.From,
				To:   su.To,
				FromBefore: slotState{
					Occupied: su.FromBefore.Occupied,
					Gen:      su.FromBefore.Gen,
					Pinned:   su.FromBefore.Pinned,
				},
				ToBefore: slotState{
					Occupied: su.ToBefore.Occupied,
					Gen:      su.ToBefore.Gen,
					Pinned:   su.ToBefore.Pinned,
				},
			}
		}

		candidate.plans[sp.ID] = &defragPlan{
			ID:           sp.ID,
			Page:         sp.Page,
			Moves:        moves,
			CreatedFrame: sp.CreatedFrame,
			BaselineFrag: sp.BaselineFrag,
			Done:         sp.Done,
			Undos:        undos,
		}
	}
	return candidate, nil
}
