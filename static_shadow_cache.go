// Package task splits the shadow atlas into a static cache region and a
// dynamic redraw region, invalidates static tiles per light and rebakes them
// under a per frame budget.
package task

import "errors"

// errNotImplemented marks behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

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

// StaticShadowCache owns the static and dynamic shadow regions.
type StaticShadowCache struct {
	options CacheOptions
}

// NewStaticShadowCache builds a cache with the given quotas.
func NewStaticShadowCache(options CacheOptions) *StaticShadowCache {
	return &StaticShadowCache{options: options}
}

// Acquire reserves one shadow tile for a light.
func (c *StaticShadowCache) Acquire(lightID string, size TileSize, isStatic bool) (TileHandle, error) {
	return TileHandle{}, errNotImplemented
}

// Release frees one tile and its slot.
func (c *StaticShadowCache) Release(h TileHandle) error {
	return errNotImplemented
}

// Invalidate marks one tile dirty and advances its generation.
func (c *StaticShadowCache) Invalidate(h TileHandle) error {
	return errNotImplemented
}

// MarkLightDirty invalidates every tile of one light for the given reason.
func (c *StaticShadowCache) MarkLightDirty(lightID, reason string) error {
	return errNotImplemented
}

// ReadTile returns the payload of a live tile handle.
func (c *StaticShadowCache) ReadTile(h TileHandle) (TileData, error) {
	return TileData{}, errNotImplemented
}

// RebakeFrame rebakes dirty tiles within the per frame budget.
func (c *StaticShadowCache) RebakeFrame() RebakeResult {
	return RebakeResult{}
}

// TileBudget reports the region quotas and their usage.
func (c *StaticShadowCache) TileBudget() Budget {
	return Budget{}
}

// PendingRebakes returns how many tiles wait for a rebake.
func (c *StaticShadowCache) PendingRebakes() int {
	return 0
}

// Snapshot serialises the whole cache state.
func (c *StaticShadowCache) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole cache state from a snapshot.
func (c *StaticShadowCache) Restore(data []byte) error {
	return errNotImplemented
}
