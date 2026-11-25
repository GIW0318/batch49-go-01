// Package task splits the shadow atlas into a static cache region and a
// dynamic redraw region, invalidates static tiles per light and rebakes them
// under a per frame budget.
package task

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
)

// TileSize is the pixel size of one shadow tile.
type TileSize struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// TileHandle is the generation carrying handle of one shadow tile.
type TileHandle struct {
	ID         uint64 `json:"id"`
	LightID    string `json:"light_id"`
	Generation uint64 `json:"generation"`
	Static     bool   `json:"static"`
	Degraded   bool   `json:"degraded"`
}

// TileData is the readable payload of one live tile.
type TileData struct {
	Handle TileHandle `json:"handle"`
	Pixels int        `json:"pixels"`
	Baked  bool       `json:"baked"`
}

// Budget reports the two region quotas and their usage.
type Budget struct {
	StaticSlots      int `json:"static_slots"`
	DynamicSlots     int `json:"dynamic_slots"`
	StaticUsed       int `json:"static_used"`
	DynamicUsed      int `json:"dynamic_used"`
	RebuildsPerFrame int `json:"rebuilds_per_frame"`
}

// RebakeResult reports what one frame of rebaking produced.
type RebakeResult struct {
	Baked   int `json:"baked"`
	Queued  int `json:"queued"`
	Dropped int `json:"dropped"`
}

// CacheOptions bounds the two regions and the per frame budget.
type CacheOptions struct {
	StaticSlots      int `json:"static_slots"`
	DynamicSlots     int `json:"dynamic_slots"`
	RebuildsPerFrame int `json:"rebuilds_per_frame"`
	MaxTileEdge      int `json:"max_tile_edge"`
}

// Errors returned by the cache.
var (
	ErrBlankLight    = errors.New("light id is required")
	ErrBadTileSize   = errors.New("tile width and height must be positive")
	ErrTileTooLarge  = errors.New("tile edge exceeds the cap")
	ErrBlankReason   = errors.New("dirty reason is required")
	ErrNoCapacity    = errors.New("no shadow atlas slot left")
	ErrUnknownHandle = errors.New("tile handle is unknown")
	ErrStaleHandle   = errors.New("tile handle generation is stale")
	ErrUnknownLight  = errors.New("light has no tiles")
	ErrBadSnapshot   = errors.New("snapshot payload is invalid")
)

type regionKind uint8

const (
	regionStatic regionKind = iota
	regionDynamic
)

const (
	snapshotMagic = "static-shadow-cache/v1"
	noCascade     = -1
	allCascades   = -1
	initialGen    = uint64(1)
	firstTileID   = uint64(1)
)

type tileState struct {
	handle  TileHandle
	size    TileSize
	region  regionKind
	slot    int
	cascade int
	baked   bool
	pending bool
}

type dirtyLightState struct {
	Reason  string `json:"reason"`
	Cascade int    `json:"cascade"`
}

type tileSnapshot struct {
	Handle  TileHandle `json:"handle"`
	Width   int        `json:"width"`
	Height  int        `json:"height"`
	Region  regionKind `json:"region"`
	Slot    int        `json:"slot"`
	Cascade int        `json:"cascade"`
	Baked   bool       `json:"baked"`
	Pending bool       `json:"pending"`
}

type dirtyLightSnapshot struct {
	LightID string `json:"light_id"`
	Reason  string `json:"reason"`
	Cascade int    `json:"cascade"`
}

type snapshotData struct {
	Magic       string               `json:"magic"`
	Options     CacheOptions         `json:"options"`
	NextID      uint64               `json:"next_id"`
	Tiles       []tileSnapshot       `json:"tiles"`
	Queue       []uint64             `json:"queue"`
	DirtyLights []dirtyLightSnapshot `json:"dirty_lights"`
}

// StaticShadowCache owns the static and dynamic shadow regions.
type StaticShadowCache struct {
	mu           sync.Mutex
	options      CacheOptions
	nextID       uint64
	tiles        map[uint64]*tileState
	staticSlots  map[int]uint64
	dynamicSlots map[int]uint64
	lightTiles   map[string][]uint64
	queue        []uint64
	dirty        map[string]dirtyLightState
	dirtyOrder   []string
}

// NewStaticShadowCache builds a cache with the given quotas.
func NewStaticShadowCache(options CacheOptions) *StaticShadowCache {
	options = normalizeOptions(options)
	return &StaticShadowCache{
		options:      options,
		nextID:       firstTileID,
		tiles:        make(map[uint64]*tileState),
		staticSlots:  make(map[int]uint64),
		dynamicSlots: make(map[int]uint64),
		lightTiles:   make(map[string][]uint64),
		queue:        make([]uint64, 0),
		dirty:        make(map[string]dirtyLightState),
		dirtyOrder:   make([]string, 0),
	}
}

func normalizeOptions(options CacheOptions) CacheOptions {
	if options.StaticSlots < 0 {
		options.StaticSlots = 0
	}
	if options.DynamicSlots < 0 {
		options.DynamicSlots = 0
	}
	if options.RebuildsPerFrame < 0 {
		options.RebuildsPerFrame = 0
	}
	if options.MaxTileEdge < 0 {
		options.MaxTileEdge = 0
	}
	return options
}

// Acquire reserves one shadow tile for a light.
func (c *StaticShadowCache) Acquire(lightID string, size TileSize, isStatic bool) (TileHandle, error) {
	return c.acquireCascade(lightID, size, isStatic, noCascade)
}

// AcquireCascade reserves a tile belonging to one shadow cascade. It is an
// explicit extension for distance-tier invalidation; Acquire leaves the
// cascade unspecified.
func (c *StaticShadowCache) AcquireCascade(lightID string, size TileSize, isStatic bool, cascade int) (TileHandle, error) {
	if cascade < 0 {
		return TileHandle{}, fmt.Errorf("%w: cascade must not be negative", ErrBadTileSize)
	}
	return c.acquireCascade(lightID, size, isStatic, cascade)
}

func (c *StaticShadowCache) acquireCascade(rawLightID string, size TileSize, isStatic bool, cascade int) (TileHandle, error) {
	lightID := strings.TrimSpace(rawLightID)
	if lightID == "" {
		return TileHandle{}, ErrBlankLight
	}
	if size.Width <= 0 || size.Height <= 0 {
		return TileHandle{}, ErrBadTileSize
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if size.Width > c.options.MaxTileEdge || size.Height > c.options.MaxTileEdge {
		return TileHandle{}, ErrTileTooLarge
	}

	region, slot, degraded, err := c.allocateRegionLocked(isStatic)
	if err != nil {
		return TileHandle{}, err
	}

	id := c.nextID
	c.nextID++
	handle := TileHandle{
		ID:         id,
		LightID:    lightID,
		Generation: initialGen,
		Static:     region == regionStatic,
		Degraded:   degraded,
	}
	tile := &tileState{
		handle:  handle,
		size:    size,
		region:  region,
		slot:    slot,
		cascade: cascade,
		baked:   true,
	}

	c.tiles[id] = tile
	c.regionSlots(region)[slot] = id
	c.lightTiles[lightID] = append(c.lightTiles[lightID], id)
	return handle, nil
}

func (c *StaticShadowCache) allocateRegionLocked(isStatic bool) (regionKind, int, bool, error) {
	if isStatic {
		if slot, ok := allocateSlot(c.staticSlots, c.options.StaticSlots); ok {
			return regionStatic, slot, false, nil
		}
		if slot, ok := allocateSlot(c.dynamicSlots, c.options.DynamicSlots); ok {
			return regionDynamic, slot, true, nil
		}
		return 0, 0, false, ErrNoCapacity
	}

	if slot, ok := allocateSlot(c.dynamicSlots, c.options.DynamicSlots); ok {
		return regionDynamic, slot, false, nil
	}
	return 0, 0, false, ErrNoCapacity
}

func allocateSlot(slots map[int]uint64, quota int) (int, bool) {
	for slot := 0; slot < quota; slot++ {
		if _, used := slots[slot]; !used {
			return slot, true
		}
	}
	return 0, false
}

func (c *StaticShadowCache) regionSlots(region regionKind) map[int]uint64 {
	if region == regionStatic {
		return c.staticSlots
	}
	return c.dynamicSlots
}

// Release frees one tile and its slot.
func (c *StaticShadowCache) Release(h TileHandle) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	tile, ok := c.tiles[h.ID]
	if !ok {
		return ErrUnknownHandle
	}

	delete(c.tiles, h.ID)
	delete(c.regionSlots(tile.region), tile.slot)
	c.removeLightTileLocked(tile.handle.LightID, h.ID)
	c.removeQueueEntryLocked(h.ID)
	c.reconcileDirtyLightsLocked()
	return nil
}

func (c *StaticShadowCache) removeLightTileLocked(lightID string, id uint64) {
	ids := c.lightTiles[lightID]
	for index, tileID := range ids {
		if tileID != id {
			continue
		}
		c.lightTiles[lightID] = append(ids[:index], ids[index+1:]...)
		if len(c.lightTiles[lightID]) == 0 {
			delete(c.lightTiles, lightID)
		}
		return
	}
}

func (c *StaticShadowCache) removeQueueEntryLocked(id uint64) {
	for index, queuedID := range c.queue {
		if queuedID == id {
			c.queue = append(c.queue[:index], c.queue[index+1:]...)
			return
		}
	}
}

// Invalidate marks one tile dirty and advances its generation.
func (c *StaticShadowCache) Invalidate(h TileHandle) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	tile, ok := c.tiles[h.ID]
	if !ok {
		return ErrUnknownHandle
	}

	c.invalidateTileLocked(tile)
	return nil
}

func (c *StaticShadowCache) invalidateTileLocked(tile *tileState) {
	tile.handle.Generation++
	tile.baked = false
	if !tile.pending {
		tile.pending = true
		c.queue = append(c.queue, tile.handle.ID)
	}
}

// MarkLightDirty invalidates every tile of one light for the given reason.
func (c *StaticShadowCache) MarkLightDirty(lightID, reason string) error {
	return c.markDirty(lightID, reason, allCascades)
}

// MarkCascadeDirty invalidates only one cascade of one light. A shadow
// distance-tier change uses this to avoid rebuilding every cascade.
func (c *StaticShadowCache) MarkCascadeDirty(lightID string, cascade int, reason string) error {
	if cascade < 0 {
		return fmt.Errorf("%w: cascade must not be negative", ErrBadTileSize)
	}
	return c.markDirty(lightID, reason, cascade)
}

func (c *StaticShadowCache) markDirty(rawLightID, rawReason string, cascade int) error {
	lightID := strings.TrimSpace(rawLightID)
	if lightID == "" {
		return ErrBlankLight
	}
	reason := strings.TrimSpace(rawReason)
	if reason == "" {
		return ErrBlankReason
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	ids, ok := c.lightTiles[lightID]
	if !ok || len(ids) == 0 {
		return ErrUnknownLight
	}

	c.rememberDirtyLightLocked(lightID, reason, cascade)
	for _, id := range ids {
		tile := c.tiles[id]
		if tile == nil {
			continue
		}
		if cascade != allCascades && tile.cascade != cascade {
			continue
		}
		c.invalidateTileLocked(tile)
	}
	c.reconcileDirtyLightsLocked()
	return nil
}

func (c *StaticShadowCache) rememberDirtyLightLocked(lightID, reason string, cascade int) {
	if _, ok := c.dirty[lightID]; !ok {
		c.dirtyOrder = append(c.dirtyOrder, lightID)
	}
	c.dirty[lightID] = dirtyLightState{Reason: reason, Cascade: cascade}
}

func (c *StaticShadowCache) reconcileDirtyLightsLocked() {
	for index := 0; index < len(c.dirtyOrder); {
		lightID := c.dirtyOrder[index]
		if !c.lightHasPendingTileLocked(lightID) {
			delete(c.dirty, lightID)
			c.dirtyOrder = append(c.dirtyOrder[:index], c.dirtyOrder[index+1:]...)
			continue
		}
		index++
	}
}

func (c *StaticShadowCache) lightHasPendingTileLocked(lightID string) bool {
	for _, id := range c.lightTiles[lightID] {
		if tile := c.tiles[id]; tile != nil && tile.pending {
			return true
		}
	}
	return false
}

// ReadTile returns the payload of a live tile handle.
func (c *StaticShadowCache) ReadTile(h TileHandle) (TileData, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	tile, ok := c.tiles[h.ID]
	if !ok {
		return TileData{}, ErrUnknownHandle
	}
	if h.Generation != tile.handle.Generation {
		return TileData{}, ErrStaleHandle
	}

	return TileData{
		Handle: tile.handle,
		Pixels: tile.size.Width * tile.size.Height,
		Baked:  tile.baked,
	}, nil
}

// RebakeFrame rebakes dirty tiles within the per frame budget.
func (c *StaticShadowCache) RebakeFrame() RebakeResult {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.options.RebuildsPerFrame <= 0 {
		return RebakeResult{Queued: len(c.queue)}
	}
	limit := c.options.RebuildsPerFrame
	if limit > len(c.queue) {
		limit = len(c.queue)
	}

	baked := 0
	for _, id := range c.queue[:limit] {
		tile, ok := c.tiles[id]
		if !ok {
			continue
		}
		tile.pending = false
		tile.baked = true
		baked++
	}

	remaining := make([]uint64, len(c.queue)-limit)
	copy(remaining, c.queue[limit:])
	c.queue = remaining
	c.reconcileDirtyLightsLocked()

	return RebakeResult{Baked: baked, Queued: len(c.queue), Dropped: 0}
}

// TileBudget reports the region quotas and their usage.
func (c *StaticShadowCache) TileBudget() Budget {
	c.mu.Lock()
	defer c.mu.Unlock()

	return Budget{
		StaticSlots:      c.options.StaticSlots,
		DynamicSlots:     c.options.DynamicSlots,
		StaticUsed:       len(c.staticSlots),
		DynamicUsed:      len(c.dynamicSlots),
		RebuildsPerFrame: c.options.RebuildsPerFrame,
	}
}

// PendingRebakes returns how many tiles wait for a rebake.
func (c *StaticShadowCache) PendingRebakes() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.queue)
}

// Snapshot serialises the whole cache state.
func (c *StaticShadowCache) Snapshot() ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	snapshot := snapshotData{
		Magic:   snapshotMagic,
		Options: c.options,
		NextID:  c.nextID,
		Tiles:   make([]tileSnapshot, 0, len(c.tiles)),
		Queue:   append([]uint64(nil), c.queue...),
		DirtyLights: make(
			[]dirtyLightSnapshot,
			0,
			len(c.dirtyOrder),
		),
	}

	ids := make([]uint64, 0, len(c.tiles))
	for id := range c.tiles {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		tile := c.tiles[id]
		snapshot.Tiles = append(snapshot.Tiles, tileSnapshot{
			Handle:  tile.handle,
			Width:   tile.size.Width,
			Height:  tile.size.Height,
			Region:  tile.region,
			Slot:    tile.slot,
			Cascade: tile.cascade,
			Baked:   tile.baked,
			Pending: tile.pending,
		})
	}

	for _, lightID := range c.dirtyOrder {
		state := c.dirty[lightID]
		snapshot.DirtyLights = append(snapshot.DirtyLights, dirtyLightSnapshot{
			LightID: lightID,
			Reason:  state.Reason,
			Cascade: state.Cascade,
		})
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("encode snapshot: %w", err)
	}
	return data, nil
}

// Restore replaces the whole cache state from a snapshot.
func (c *StaticShadowCache) Restore(data []byte) error {
	restored, err := decodeSnapshot(data)
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.options = restored.options
	c.nextID = restored.nextID
	c.tiles = restored.tiles
	c.staticSlots = restored.staticSlots
	c.dynamicSlots = restored.dynamicSlots
	c.lightTiles = restored.lightTiles
	c.queue = restored.queue
	c.dirty = restored.dirty
	c.dirtyOrder = restored.dirtyOrder
	return nil
}

type restoredSnapshot struct {
	options      CacheOptions
	nextID       uint64
	tiles        map[uint64]*tileState
	staticSlots  map[int]uint64
	dynamicSlots map[int]uint64
	lightTiles   map[string][]uint64
	queue        []uint64
	dirty        map[string]dirtyLightState
	dirtyOrder   []string
}

func decodeSnapshot(data []byte) (*restoredSnapshot, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("%w: empty payload", ErrBadSnapshot)
	}

	var payload snapshotData
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("%w: decode: %v", ErrBadSnapshot, err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("%w: trailing snapshot value", ErrBadSnapshot)
	}

	if err := validateSnapshot(&payload); err != nil {
		return nil, err
	}
	return buildRestoredSnapshot(&payload)
}

func validateSnapshot(payload *snapshotData) error {
	if payload.Magic != snapshotMagic {
		return fmt.Errorf("%w: bad magic", ErrBadSnapshot)
	}
	payload.Options = normalizeOptions(payload.Options)
	if payload.NextID == 0 {
		return fmt.Errorf("%w: bad next id", ErrBadSnapshot)
	}

	seenIDs := make(map[uint64]struct{}, len(payload.Tiles))
	seenStaticSlots := make(map[int]struct{})
	seenDynamicSlots := make(map[int]struct{})
	lightTiles := make(map[string][]uint64)
	pendingLights := make(map[string]struct{})
	pendingCount := 0
	var maxID uint64

	for index := range payload.Tiles {
		tile := &payload.Tiles[index]
		if err := validateSnapshotTile(
			tile,
			payload.Options,
			seenIDs,
			seenStaticSlots,
			seenDynamicSlots,
		); err != nil {
			return err
		}
		if tile.Pending {
			pendingCount++
			pendingLights[tile.Handle.LightID] = struct{}{}
		}
		if tile.Handle.ID > maxID {
			maxID = tile.Handle.ID
		}
		lightTiles[tile.Handle.LightID] = append(lightTiles[tile.Handle.LightID], tile.Handle.ID)
	}

	if payload.NextID <= maxID {
		return fmt.Errorf("%w: next id is not ahead of tiles", ErrBadSnapshot)
	}
	if err := validateSnapshotQueue(payload.Queue, payload.Tiles, seenIDs, pendingCount); err != nil {
		return err
	}
	return validateSnapshotDirtyLights(payload.DirtyLights, lightTiles, pendingLights)
}

func validateSnapshotTile(
	tile *tileSnapshot,
	options CacheOptions,
	seenIDs map[uint64]struct{},
	seenStaticSlots map[int]struct{},
	seenDynamicSlots map[int]struct{},
) error {
	h := tile.Handle
	if h.ID == 0 || h.Generation == 0 {
		return fmt.Errorf("%w: tile handle is incomplete", ErrBadSnapshot)
	}
	if strings.TrimSpace(h.LightID) == "" {
		return fmt.Errorf("%w: tile light is blank", ErrBadSnapshot)
	}
	if tile.Width <= 0 || tile.Height <= 0 {
		return fmt.Errorf("%w: tile size is invalid", ErrBadSnapshot)
	}
	if tile.Width > options.MaxTileEdge || tile.Height > options.MaxTileEdge {
		return fmt.Errorf("%w: tile is too large", ErrBadSnapshot)
	}
	if !tile.Pending && !tile.Baked {
		return fmt.Errorf("%w: tile is neither baked nor pending", ErrBadSnapshot)
	}
	if tile.Pending && tile.Baked {
		return fmt.Errorf("%w: pending tile cannot be baked", ErrBadSnapshot)
	}
	if tile.Cascade < noCascade {
		return fmt.Errorf("%w: bad cascade", ErrBadSnapshot)
	}
	if _, duplicated := seenIDs[h.ID]; duplicated {
		return fmt.Errorf("%w: duplicate tile id %d", ErrBadSnapshot, h.ID)
	}
	seenIDs[h.ID] = struct{}{}

	switch tile.Region {
	case regionStatic:
		if tile.Slot < 0 || tile.Slot >= options.StaticSlots {
			return fmt.Errorf("%w: static slot is out of range", ErrBadSnapshot)
		}
		if _, duplicated := seenStaticSlots[tile.Slot]; duplicated {
			return fmt.Errorf("%w: duplicate static slot %d", ErrBadSnapshot, tile.Slot)
		}
		seenStaticSlots[tile.Slot] = struct{}{}
		if !h.Static || h.Degraded {
			return fmt.Errorf("%w: static handle flags are wrong", ErrBadSnapshot)
		}
	case regionDynamic:
		if tile.Slot < 0 || tile.Slot >= options.DynamicSlots {
			return fmt.Errorf("%w: dynamic slot is out of range", ErrBadSnapshot)
		}
		if _, duplicated := seenDynamicSlots[tile.Slot]; duplicated {
			return fmt.Errorf("%w: duplicate dynamic slot %d", ErrBadSnapshot, tile.Slot)
		}
		seenDynamicSlots[tile.Slot] = struct{}{}
		if h.Static {
			return fmt.Errorf("%w: dynamic handle cannot claim static region", ErrBadSnapshot)
		}
	default:
		return fmt.Errorf("%w: unknown region", ErrBadSnapshot)
	}
	return nil
}

func validateSnapshotQueue(
	queue []uint64,
	tiles []tileSnapshot,
	seenIDs map[uint64]struct{},
	pendingCount int,
) error {
	if len(queue) != pendingCount {
		return fmt.Errorf("%w: queue length does not match pending tiles", ErrBadSnapshot)
	}

	seenQueueIDs := make(map[uint64]struct{}, len(queue))
	tileByID := make(map[uint64]*tileSnapshot, len(tiles))
	for index := range tiles {
		tileByID[tiles[index].Handle.ID] = &tiles[index]
	}

	for _, id := range queue {
		if _, ok := seenIDs[id]; !ok {
			return fmt.Errorf("%w: queue references missing tile %d", ErrBadSnapshot, id)
		}
		if _, duplicated := seenQueueIDs[id]; duplicated {
			return fmt.Errorf("%w: queue duplicates tile %d", ErrBadSnapshot, id)
		}
		seenQueueIDs[id] = struct{}{}
		if tile := tileByID[id]; tile == nil || !tile.Pending {
			return fmt.Errorf("%w: queued tile %d is not pending", ErrBadSnapshot, id)
		}
	}
	return nil
}

func validateSnapshotDirtyLights(
	dirty []dirtyLightSnapshot,
	lightTiles map[string][]uint64,
	pendingLights map[string]struct{},
) error {
	seen := make(map[string]struct{}, len(dirty))
	for _, entry := range dirty {
		lightID := strings.TrimSpace(entry.LightID)
		if lightID == "" || strings.TrimSpace(entry.Reason) == "" {
			return fmt.Errorf("%w: dirty light entry is incomplete", ErrBadSnapshot)
		}
		if entry.Cascade < allCascades {
			return fmt.Errorf("%w: dirty cascade is invalid", ErrBadSnapshot)
		}
		if _, duplicated := seen[lightID]; duplicated {
			return fmt.Errorf("%w: duplicate dirty light %s", ErrBadSnapshot, lightID)
		}
		seen[lightID] = struct{}{}

		if _, hasTiles := lightTiles[lightID]; !hasTiles {
			return fmt.Errorf("%w: dirty light %s has no tiles", ErrBadSnapshot, lightID)
		}
		if _, pending := pendingLights[lightID]; !pending {
			return fmt.Errorf("%w: dirty light %s has no pending tiles", ErrBadSnapshot, lightID)
		}
	}
	return nil
}

func buildRestoredSnapshot(payload *snapshotData) (*restoredSnapshot, error) {
	restored := &restoredSnapshot{
		options:      payload.Options,
		nextID:       payload.NextID,
		tiles:        make(map[uint64]*tileState, len(payload.Tiles)),
		staticSlots:  make(map[int]uint64),
		dynamicSlots: make(map[int]uint64),
		lightTiles:   make(map[string][]uint64),
		queue:        append([]uint64(nil), payload.Queue...),
		dirty:        make(map[string]dirtyLightState, len(payload.DirtyLights)),
		dirtyOrder:   make([]string, 0, len(payload.DirtyLights)),
	}

	for index := range payload.Tiles {
		saved := payload.Tiles[index]
		tile := &tileState{
			handle:  saved.Handle,
			size:    TileSize{Width: saved.Width, Height: saved.Height},
			region:  saved.Region,
			slot:    saved.Slot,
			cascade: saved.Cascade,
			baked:   saved.Baked,
			pending: saved.Pending,
		}
		restored.tiles[tile.handle.ID] = tile
		restored.regionSlots(tile.region)[tile.slot] = tile.handle.ID
		restored.lightTiles[tile.handle.LightID] = append(
			restored.lightTiles[tile.handle.LightID],
			tile.handle.ID,
		)
	}

	for _, entry := range payload.DirtyLights {
		lightID := strings.TrimSpace(entry.LightID)
		restored.dirtyOrder = append(restored.dirtyOrder, lightID)
		restored.dirty[lightID] = dirtyLightState{
			Reason:  strings.TrimSpace(entry.Reason),
			Cascade: entry.Cascade,
		}
	}
	return restored, nil
}

func (r *restoredSnapshot) regionSlots(region regionKind) map[int]uint64 {
	if region == regionStatic {
		return r.staticSlots
	}
	return r.dynamicSlots
}
