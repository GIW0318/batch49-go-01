package task

import (
	"errors"
	"testing"

	impl "gsb/batch49-go-game-b-12"
)

func mustChain(t *testing.T, limits impl.ChainLimits) *impl.SubEmitterChain {
	t.Helper()
	c := impl.NewSubEmitterChain(limits)
	if c == nil {
		t.Fatalf("chain must not be nil")
	}
	return c
}

func mustRegister(t *testing.T, c *impl.SubEmitterChain, id string, def impl.EmitterDef) {
	t.Helper()
	if err := c.Register(id, def); err != nil {
		t.Fatalf("register %s: %v", id, err)
	}
}

func mustLink(t *testing.T, c *impl.SubEmitterChain, parent, child, trigger string) {
	t.Helper()
	if err := c.Link(parent, child, trigger); err != nil {
		t.Fatalf("link %s -> %s on %s: %v", parent, child, trigger, err)
	}
}

func TestRegisterValidatesDefinition(t *testing.T) {
	c := mustChain(t, impl.ChainLimits{})
	if err := c.Register("", impl.EmitterDef{Capacity: 4, Burst: 1, Lifetime: 1}); !errors.Is(err, impl.ErrInvalidEmitter) {
		t.Fatalf("empty emitter id must report ErrInvalidEmitter, got %v", err)
	}
	if err := c.Register("zero-cap", impl.EmitterDef{Capacity: 0, Burst: 1, Lifetime: 1}); !errors.Is(err, impl.ErrInvalidEmitter) {
		t.Fatalf("zero capacity must report ErrInvalidEmitter, got %v", err)
	}
	if err := c.Register("zero-burst", impl.EmitterDef{Capacity: 4, Burst: 0, Lifetime: 1}); !errors.Is(err, impl.ErrInvalidEmitter) {
		t.Fatalf("zero burst must report ErrInvalidEmitter, got %v", err)
	}
	if err := c.Register("zero-life", impl.EmitterDef{Capacity: 4, Burst: 1, Lifetime: 0}); !errors.Is(err, impl.ErrInvalidEmitter) {
		t.Fatalf("non positive lifetime must report ErrInvalidEmitter, got %v", err)
	}
}

func TestRegisterRejectsDuplicateEmitter(t *testing.T) {
	c := mustChain(t, impl.ChainLimits{})
	mustRegister(t, c, "spark", impl.EmitterDef{Capacity: 4, Burst: 1, Lifetime: 1})
	if err := c.Register("spark", impl.EmitterDef{Capacity: 8, Burst: 2, Lifetime: 2}); !errors.Is(err, impl.ErrDuplicateEmitter) {
		t.Fatalf("duplicate register must report ErrDuplicateEmitter, got %v", err)
	}
	if cap, ok := c.Capacity("spark"); !ok || cap != 4 {
		t.Fatalf("capacity after duplicate register = %d/%v, want 4/true", cap, ok)
	}
}

func TestLinkValidatesEndpointsAndTrigger(t *testing.T) {
	c := mustChain(t, impl.ChainLimits{})
	mustRegister(t, c, "a", impl.EmitterDef{Capacity: 8, Burst: 1, Lifetime: 1})
	mustRegister(t, c, "b", impl.EmitterDef{Capacity: 8, Burst: 1, Lifetime: 1})
	if err := c.Link("ghost", "b", impl.TriggerDeath); !errors.Is(err, impl.ErrUnknownEmitter) {
		t.Fatalf("unknown parent must report ErrUnknownEmitter, got %v", err)
	}
	if err := c.Link("a", "ghost", impl.TriggerDeath); !errors.Is(err, impl.ErrUnknownEmitter) {
		t.Fatalf("unknown child must report ErrUnknownEmitter, got %v", err)
	}
	if err := c.Link("a", "b", "explode"); !errors.Is(err, impl.ErrUnknownTrigger) {
		t.Fatalf("unknown trigger must report ErrUnknownTrigger, got %v", err)
	}
	if got := len(c.Links("a")); got != 0 {
		t.Fatalf("rejected links must not be stored, got %d", got)
	}
}

func TestLinkRejectsCycle(t *testing.T) {
	c := mustChain(t, impl.ChainLimits{})
	mustRegister(t, c, "a", impl.EmitterDef{Capacity: 8, Burst: 1, Lifetime: 1})
	mustRegister(t, c, "b", impl.EmitterDef{Capacity: 8, Burst: 1, Lifetime: 1})
	mustLink(t, c, "a", "b", impl.TriggerDeath)
	if err := c.Link("b", "a", impl.TriggerDeath); !errors.Is(err, impl.ErrCycle) {
		t.Fatalf("closing the ring must report ErrCycle, got %v", err)
	}
	if err := c.Link("a", "a", impl.TriggerCollision); !errors.Is(err, impl.ErrCycle) {
		t.Fatalf("self link must report ErrCycle, got %v", err)
	}
	links := c.Links("a")
	if len(links) != 1 || links[0].ChildID != "b" {
		t.Fatalf("links of a = %+v, want exactly a -> b", links)
	}
}

func TestEmitRejectsUnknownEmitter(t *testing.T) {
	c := mustChain(t, impl.ChainLimits{})
	if err := c.Emit("ghost", 1); !errors.Is(err, impl.ErrUnknownEmitter) {
		t.Fatalf("emit on unknown emitter must report ErrUnknownEmitter, got %v", err)
	}
}

func TestEmitSpawnsWithinCapacity(t *testing.T) {
	c := mustChain(t, impl.ChainLimits{})
	mustRegister(t, c, "spark", impl.EmitterDef{Capacity: 4, Burst: 1, Lifetime: 2})
	if err := c.Emit("spark", 3); err != nil {
		t.Fatalf("emit 3: %v", err)
	}
	if got := c.Alive("spark"); got != 3 {
		t.Fatalf("alive after emit = %d, want 3", got)
	}
	if got := c.Frame(); got != 0 {
		t.Fatalf("frame before any step = %d, want 0", got)
	}
}

func TestEmitOverCapacityIsRejectedWhole(t *testing.T) {
	c := mustChain(t, impl.ChainLimits{})
	mustRegister(t, c, "spark", impl.EmitterDef{Capacity: 4, Burst: 1, Lifetime: 2})
	if err := c.Emit("spark", 5); !errors.Is(err, impl.ErrOverCapacity) {
		t.Fatalf("emit over capacity must report ErrOverCapacity, got %v", err)
	}
	if got := c.Alive("spark"); got != 0 {
		t.Fatalf("rejected emit must not spawn partially, alive = %d", got)
	}
}

func TestStepExpiresParticlesOnLifetime(t *testing.T) {
	c := mustChain(t, impl.ChainLimits{})
	mustRegister(t, c, "spark", impl.EmitterDef{Capacity: 4, Burst: 1, Lifetime: 1})
	if err := c.Emit("spark", 2); err != nil {
		t.Fatalf("emit: %v", err)
	}
	first := c.Step(0.5)
	if first.Expired != 0 {
		t.Fatalf("half life step expired %d particles, want 0", first.Expired)
	}
	if got := c.Alive("spark"); got != 2 {
		t.Fatalf("alive at half life = %d, want 2", got)
	}
	second := c.Step(0.5)
	if second.Expired != 2 {
		t.Fatalf("full life step expired %d particles, want 2", second.Expired)
	}
	if got := c.Alive("spark"); got != 0 {
		t.Fatalf("alive after lifetime = %d, want 0", got)
	}
	if got := c.Frame(); got != 2 {
		t.Fatalf("frame after two steps = %d, want 2", got)
	}
}

func TestDeathTriggerSpawnsChild(t *testing.T) {
	c := mustChain(t, impl.ChainLimits{})
	mustRegister(t, c, "body", impl.EmitterDef{Capacity: 8, Burst: 1, Lifetime: 0.5})
	mustRegister(t, c, "debris", impl.EmitterDef{Capacity: 8, Burst: 1, Lifetime: 1})
	mustLink(t, c, "body", "debris", impl.TriggerDeath)
	if err := c.Emit("body", 2); err != nil {
		t.Fatalf("emit body: %v", err)
	}
	report := c.Step(0.6)
	if report.Expired != 2 {
		t.Fatalf("expired count = %d, want 2", report.Expired)
	}
	if report.ChildFired != 2 {
		t.Fatalf("child fired = %d, want 2", report.ChildFired)
	}
	if got := c.Alive("debris"); got != 2 {
		t.Fatalf("debris alive = %d, want 2", got)
	}
}

func TestDelayWindowDefersChildSpawn(t *testing.T) {
	c := mustChain(t, impl.ChainLimits{DelayFrames: 1})
	mustRegister(t, c, "body", impl.EmitterDef{Capacity: 8, Burst: 1, Lifetime: 0.5})
	mustRegister(t, c, "trail", impl.EmitterDef{Capacity: 8, Burst: 1, Lifetime: 1})
	mustLink(t, c, "body", "trail", impl.TriggerDeath)
	if err := c.Emit("body", 2); err != nil {
		t.Fatalf("emit body: %v", err)
	}
	c.Step(0.6)
	if got := c.Alive("trail"); got != 0 {
		t.Fatalf("child must wait for the delay window, alive = %d", got)
	}
	if got := c.PendingTriggers("trail"); got != 2 {
		t.Fatalf("queued triggers = %d, want 2", got)
	}
	c.Step(0.1)
	if got := c.Alive("trail"); got != 2 {
		t.Fatalf("child alive after delay = %d, want 2", got)
	}
	if got := c.PendingTriggers("trail"); got != 0 {
		t.Fatalf("queued triggers after firing = %d, want 0", got)
	}
}

func TestPerFrameCapClampsChildSpawn(t *testing.T) {
	c := mustChain(t, impl.ChainLimits{MaxPerFrame: 3})
	mustRegister(t, c, "body", impl.EmitterDef{Capacity: 16, Burst: 1, Lifetime: 0.5})
	mustRegister(t, c, "debris", impl.EmitterDef{Capacity: 16, Burst: 1, Lifetime: 1})
	mustLink(t, c, "body", "debris", impl.TriggerDeath)
	if err := c.Emit("body", 10); err != nil {
		t.Fatalf("emit body: %v", err)
	}
	report := c.Step(0.6)
	if got := c.Alive("debris"); got != 3 {
		t.Fatalf("debris alive = %d, want the per frame cap 3", got)
	}
	if report.Dropped != 7 {
		t.Fatalf("dropped = %d, want 7", report.Dropped)
	}
}

func TestTotalBudgetRejectsEmit(t *testing.T) {
	c := mustChain(t, impl.ChainLimits{TotalBudget: 5})
	mustRegister(t, c, "spark", impl.EmitterDef{Capacity: 16, Burst: 1, Lifetime: 1})
	if err := c.Emit("spark", 3); err != nil {
		t.Fatalf("emit within budget: %v", err)
	}
	if err := c.Emit("spark", 3); !errors.Is(err, impl.ErrBudgetExceeded) {
		t.Fatalf("emit over the total budget must report ErrBudgetExceeded, got %v", err)
	}
	if got := c.Alive("spark"); got != 3 {
		t.Fatalf("budget rejection must not spawn, alive = %d want 3", got)
	}
}

func TestDepthLimitStopsDeepChain(t *testing.T) {
	c := mustChain(t, impl.ChainLimits{MaxDepth: 1})
	mustRegister(t, c, "a", impl.EmitterDef{Capacity: 8, Burst: 1, Lifetime: 0.5})
	mustRegister(t, c, "b", impl.EmitterDef{Capacity: 8, Burst: 1, Lifetime: 0.5})
	mustRegister(t, c, "cc", impl.EmitterDef{Capacity: 8, Burst: 1, Lifetime: 0.5})
	mustLink(t, c, "a", "b", impl.TriggerDeath)
	mustLink(t, c, "b", "cc", impl.TriggerDeath)
	if err := c.Emit("a", 1); err != nil {
		t.Fatalf("emit a: %v", err)
	}
	c.Step(0.6)
	if got := c.Alive("b"); got != 1 {
		t.Fatalf("depth 1 child must spawn, alive b = %d", got)
	}
	report := c.Step(0.6)
	if report.DepthLimited == 0 {
		t.Fatalf("depth limited counter must move when the chain is deeper than the limit")
	}
	if got := c.Alive("cc"); got != 0 {
		t.Fatalf("depth 2 child must not spawn, alive cc = %d", got)
	}
}

func TestSnapshotRestoreKeepsParticlesAndFrame(t *testing.T) {
	c := mustChain(t, impl.ChainLimits{})
	mustRegister(t, c, "spark", impl.EmitterDef{Capacity: 8, Burst: 1, Lifetime: 1})
	if err := c.Emit("spark", 3); err != nil {
		t.Fatalf("emit: %v", err)
	}
	c.Step(0.1)
	snap, err := c.Snapshot()
	if err != nil || len(snap) == 0 {
		t.Fatalf("snapshot err=%v len=%d, want a non empty payload", err, len(snap))
	}
	c.Step(0.6)
	if got := c.Alive("spark"); got != 0 {
		t.Fatalf("particles must expire before the restore, alive = %d", got)
	}
	if err := c.Restore(snap); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := c.Alive("spark"); got != 3 {
		t.Fatalf("restored alive = %d, want 3", got)
	}
	if got := c.Frame(); got != 1 {
		t.Fatalf("restored frame = %d, want 1", got)
	}
}

func TestRestoreRejectsCorruptPayload(t *testing.T) {
	c := mustChain(t, impl.ChainLimits{})
	if err := c.Restore([]byte{0xde, 0xad}); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must report ErrBadSnapshot, got %v", err)
	}
}
