package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch49-go-game-b-05"
)

func newCache(static, dynamic, perFrame int) *impl.StaticShadowCache {
	return impl.NewStaticShadowCache(impl.CacheOptions{
		StaticSlots:      static,
		DynamicSlots:     dynamic,
		RebuildsPerFrame: perFrame,
		MaxTileEdge:      1024,
	})
}

func tileSize(w, h int) impl.TileSize {
	return impl.TileSize{Width: w, Height: h}
}

func mustAcquire(t *testing.T, c *impl.StaticShadowCache, light string, size impl.TileSize, isStatic bool) impl.TileHandle {
	t.Helper()
	h, err := c.Acquire(light, size, isStatic)
	if err != nil {
		t.Fatalf("acquire tile for %s: %v", light, err)
	}
	return h
}

func TestAcquireReturnsDistinctHandles(t *testing.T) {
	c := newCache(4, 4, 4)
	first := mustAcquire(t, c, "sun", tileSize(256, 256), true)
	second := mustAcquire(t, c, "sun", tileSize(256, 256), true)
	if first.ID == second.ID {
		t.Fatalf("two acquires share handle id %d", first.ID)
	}
	if first.Generation != 1 || second.Generation != 1 {
		t.Fatalf("initial generations = %d and %d, want 1", first.Generation, second.Generation)
	}
	if first.LightID != "sun" || second.LightID != "sun" {
		t.Fatalf("handle lights = %q and %q", first.LightID, second.LightID)
	}
	if !first.Static || !second.Static || first.Degraded || second.Degraded {
		t.Fatalf("static handles = %+v %+v", first, second)
	}
}

func TestAcquireRejectsBlankLightAndBadSize(t *testing.T) {
	c := newCache(4, 4, 4)
	if _, err := c.Acquire("   ", tileSize(64, 64), true); !errors.Is(err, impl.ErrBlankLight) {
		t.Fatalf("blank light error = %v, want ErrBlankLight", err)
	}
	if _, err := c.Acquire("sun", tileSize(0, 64), true); !errors.Is(err, impl.ErrBadTileSize) {
		t.Fatalf("zero width error = %v, want ErrBadTileSize", err)
	}
	if _, err := c.Acquire("sun", tileSize(64, -1), true); !errors.Is(err, impl.ErrBadTileSize) {
		t.Fatalf("negative height error = %v, want ErrBadTileSize", err)
	}
	if _, err := c.Acquire("sun", tileSize(2048, 64), true); !errors.Is(err, impl.ErrTileTooLarge) {
		t.Fatalf("oversized tile error = %v, want ErrTileTooLarge", err)
	}
	if got := c.TileBudget().StaticUsed; got != 0 {
		t.Fatalf("rejected acquires used %d static slots", got)
	}
}

func TestAcquireFillsStaticQuota(t *testing.T) {
	c := newCache(2, 4, 4)
	mustAcquire(t, c, "sun", tileSize(128, 128), true)
	mustAcquire(t, c, "moon", tileSize(128, 128), true)
	budget := c.TileBudget()
	if budget.StaticSlots != 2 || budget.DynamicSlots != 4 {
		t.Fatalf("budget quotas = %+v", budget)
	}
	if budget.StaticUsed != 2 {
		t.Fatalf("static used = %d, want 2", budget.StaticUsed)
	}
	if budget.DynamicUsed != 0 {
		t.Fatalf("dynamic used = %d, want 0", budget.DynamicUsed)
	}
	if budget.RebuildsPerFrame != 4 {
		t.Fatalf("rebuilds per frame = %d, want 4", budget.RebuildsPerFrame)
	}
}

func TestAcquireDegradesWhenStaticFull(t *testing.T) {
	c := newCache(2, 4, 4)
	mustAcquire(t, c, "sun", tileSize(128, 128), true)
	mustAcquire(t, c, "moon", tileSize(128, 128), true)
	degraded := mustAcquire(t, c, "lamp", tileSize(128, 128), true)
	if !degraded.Degraded {
		t.Fatalf("tile acquired past the static quota is not marked degraded: %+v", degraded)
	}
	if degraded.Static {
		t.Fatalf("degraded tile still claims a static slot: %+v", degraded)
	}
	budget := c.TileBudget()
	if budget.StaticUsed != 2 || budget.DynamicUsed != 1 {
		t.Fatalf("budget after degrade = %+v", budget)
	}
}

func TestAcquireRejectsWhenNoCapacity(t *testing.T) {
	c := newCache(1, 1, 4)
	mustAcquire(t, c, "sun", tileSize(64, 64), true)
	mustAcquire(t, c, "moon", tileSize(64, 64), true)
	if _, err := c.Acquire("lamp", tileSize(64, 64), true); !errors.Is(err, impl.ErrNoCapacity) {
		t.Fatalf("static over capacity error = %v, want ErrNoCapacity", err)
	}
	if _, err := c.Acquire("lamp", tileSize(64, 64), false); !errors.Is(err, impl.ErrNoCapacity) {
		t.Fatalf("dynamic over capacity error = %v, want ErrNoCapacity", err)
	}
}

func TestReleaseFreesSlots(t *testing.T) {
	c := newCache(1, 0, 4)
	first := mustAcquire(t, c, "sun", tileSize(64, 64), true)
	if got := c.TileBudget().StaticUsed; got != 1 {
		t.Fatalf("static used = %d, want 1", got)
	}
	if err := c.Release(first); err != nil {
		t.Fatalf("release: %v", err)
	}
	if got := c.TileBudget().StaticUsed; got != 0 {
		t.Fatalf("static used after release = %d, want 0", got)
	}
	again := mustAcquire(t, c, "moon", tileSize(64, 64), true)
	if again.Degraded || !again.Static {
		t.Fatalf("freed slot was not reused: %+v", again)
	}
}

func TestReleaseRejectsUnknownHandle(t *testing.T) {
	c := newCache(2, 2, 4)
	if err := c.Release(impl.TileHandle{ID: 999}); !errors.Is(err, impl.ErrUnknownHandle) {
		t.Fatalf("unknown handle error = %v, want ErrUnknownHandle", err)
	}
	h := mustAcquire(t, c, "sun", tileSize(64, 64), true)
	if err := c.Release(h); err != nil {
		t.Fatalf("first release: %v", err)
	}
	if err := c.Release(h); !errors.Is(err, impl.ErrUnknownHandle) {
		t.Fatalf("second release error = %v, want ErrUnknownHandle", err)
	}
}

func TestInvalidateStalesOldHandle(t *testing.T) {
	c := newCache(2, 2, 4)
	h := mustAcquire(t, c, "sun", tileSize(64, 32), true)
	data, err := c.ReadTile(h)
	if err != nil {
		t.Fatalf("read fresh tile: %v", err)
	}
	if data.Pixels != 64*32 || !data.Baked {
		t.Fatalf("fresh tile data = %+v", data)
	}
	if err := c.Invalidate(h); err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	if _, err := c.ReadTile(h); !errors.Is(err, impl.ErrStaleHandle) {
		t.Fatalf("stale read error = %v, want ErrStaleHandle", err)
	}
	if got := c.PendingRebakes(); got != 1 {
		t.Fatalf("pending rebakes = %d, want 1", got)
	}
}

func TestInvalidateRejectsUnknownHandle(t *testing.T) {
	c := newCache(2, 2, 4)
	if err := c.Invalidate(impl.TileHandle{ID: 404}); !errors.Is(err, impl.ErrUnknownHandle) {
		t.Fatalf("unknown handle error = %v, want ErrUnknownHandle", err)
	}
	h := mustAcquire(t, c, "sun", tileSize(64, 64), true)
	if err := c.Release(h); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := c.Invalidate(h); !errors.Is(err, impl.ErrUnknownHandle) {
		t.Fatalf("released handle error = %v, want ErrUnknownHandle", err)
	}
}

func TestMarkLightDirtyValidatesInput(t *testing.T) {
	c := newCache(2, 2, 4)
	mustAcquire(t, c, "sun", tileSize(64, 64), true)
	if err := c.MarkLightDirty("", "geometry"); !errors.Is(err, impl.ErrBlankLight) {
		t.Fatalf("blank light error = %v, want ErrBlankLight", err)
	}
	if err := c.MarkLightDirty("sun", "   "); !errors.Is(err, impl.ErrBlankReason) {
		t.Fatalf("blank reason error = %v, want ErrBlankReason", err)
	}
	if err := c.MarkLightDirty("moon", "geometry"); !errors.Is(err, impl.ErrUnknownLight) {
		t.Fatalf("unknown light error = %v, want ErrUnknownLight", err)
	}
	if got := c.PendingRebakes(); got != 0 {
		t.Fatalf("rejected dirty marks queued %d rebakes", got)
	}
}

func TestMarkLightDirtyQueuesLightTiles(t *testing.T) {
	c := newCache(4, 4, 4)
	first := mustAcquire(t, c, "sun", tileSize(64, 64), true)
	second := mustAcquire(t, c, "sun", tileSize(64, 64), true)
	other := mustAcquire(t, c, "moon", tileSize(64, 64), true)
	if err := c.MarkLightDirty("sun", "geometry"); err != nil {
		t.Fatalf("mark sun dirty: %v", err)
	}
	if got := c.PendingRebakes(); got != 2 {
		t.Fatalf("pending rebakes = %d, want 2", got)
	}
	if _, err := c.ReadTile(first); !errors.Is(err, impl.ErrStaleHandle) {
		t.Fatalf("first sun tile error = %v, want ErrStaleHandle", err)
	}
	if _, err := c.ReadTile(second); !errors.Is(err, impl.ErrStaleHandle) {
		t.Fatalf("second sun tile error = %v, want ErrStaleHandle", err)
	}
	if _, err := c.ReadTile(other); err != nil {
		t.Fatalf("moon tile must stay readable: %v", err)
	}
}

func TestRebakeFrameBakesPending(t *testing.T) {
	c := newCache(4, 4, 100)
	mustAcquire(t, c, "sun", tileSize(64, 64), true)
	mustAcquire(t, c, "sun", tileSize(64, 64), true)
	if err := c.MarkLightDirty("sun", "geometry"); err != nil {
		t.Fatalf("mark sun dirty: %v", err)
	}
	result := c.RebakeFrame()
	if result.Baked != 2 || result.Queued != 0 {
		t.Fatalf("rebake result = %+v, want 2 baked", result)
	}
	if got := c.PendingRebakes(); got != 0 {
		t.Fatalf("pending rebakes after frame = %d, want 0", got)
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	c := newCache(4, 4, 4)
	clean := mustAcquire(t, c, "sun", tileSize(256, 64), true)
	dirty := mustAcquire(t, c, "moon", tileSize(64, 64), true)
	star := mustAcquire(t, c, "star", tileSize(32, 32), true)
	if err := c.Invalidate(dirty); err != nil {
		t.Fatalf("invalidate dirty tile: %v", err)
	}
	if err := c.MarkLightDirty("star", "geometry"); err != nil {
		t.Fatalf("mark star dirty: %v", err)
	}
	wantBudget := c.TileBudget()
	wantPending := c.PendingRebakes()
	data, err := c.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload is empty")
	}
	restored := newCache(4, 4, 4)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.TileBudget(); got != wantBudget {
		t.Fatalf("restored budget = %+v, want %+v", got, wantBudget)
	}
	if got := restored.PendingRebakes(); got != wantPending {
		t.Fatalf("restored pending = %d, want %d", got, wantPending)
	}
	payload, err := restored.ReadTile(clean)
	if err != nil {
		t.Fatalf("read restored clean tile: %v", err)
	}
	if payload.Pixels != 256*64 || !payload.Baked {
		t.Fatalf("restored payload = %+v", payload)
	}
	if _, err := restored.ReadTile(dirty); !errors.Is(err, impl.ErrStaleHandle) {
		t.Fatalf("restored dirty handle error = %v, want ErrStaleHandle", err)
	}
	if _, err := restored.ReadTile(star); !errors.Is(err, impl.ErrStaleHandle) {
		t.Fatalf("restored star handle error = %v, want ErrStaleHandle", err)
	}
}

func TestRestoreRejectsCorruptPayload(t *testing.T) {
	c := newCache(2, 2, 4)
	mustAcquire(t, c, "sun", tileSize(64, 64), true)
	if err := c.Restore([]byte("not a snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt payload error = %v, want ErrBadSnapshot", err)
	}
	if got := c.TileBudget().StaticUsed; got != 1 {
		t.Fatalf("static used after failed restore = %d, want 1", got)
	}
	mustAcquire(t, c, "moon", tileSize(64, 64), true)
}

func TestConcurrentAcquireReleaseHonorsQuota(t *testing.T) {
	c := newCache(8, 0, 4)
	var wg sync.WaitGroup
	handles := make(chan impl.TileHandle, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h, err := c.Acquire("sun", tileSize(32, 32), true)
			if err != nil {
				errs <- err
				return
			}
			handles <- h
		}()
	}
	wg.Wait()
	close(handles)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent acquire: %v", err)
	}
	if got := c.TileBudget().StaticUsed; got != 8 {
		t.Fatalf("static used after concurrent acquire = %d, want 8", got)
	}
	count := 0
	for h := range handles {
		count++
		if err := c.Release(h); err != nil {
			t.Fatalf("release concurrent handle: %v", err)
		}
	}
	if count != 8 {
		t.Fatalf("handles = %d, want 8", count)
	}
	if got := c.TileBudget().StaticUsed; got != 0 {
		t.Fatalf("static used after releases = %d, want 0", got)
	}
}
