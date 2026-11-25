package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch49-go-game-b-08"
)

func newSwapper() *impl.VariantHotSwapper {
	return impl.NewVariantHotSwapper(impl.SwapConfig{MaxSwapsPerFrame: 2, FailureLimit: 3})
}

func queuedVariant(t *testing.T, s *impl.VariantHotSwapper, key impl.VariantKey) impl.Future {
	t.Helper()
	f, err := s.Request(key)
	if err != nil {
		t.Fatalf("request %q: %v", key, err)
	}
	return f
}

// compiledVariant drives one variant from request to the compiled state.
func compiledVariant(t *testing.T, s *impl.VariantHotSwapper, key impl.VariantKey) {
	t.Helper()
	queuedVariant(t, s, key)
	if err := s.StartCompile(key); err != nil {
		t.Fatalf("start compile %q: %v", key, err)
	}
	if err := s.CompleteCompile(key, true); err != nil {
		t.Fatalf("complete compile %q: %v", key, err)
	}
}

func TestRequestQueuesCompile(t *testing.T) {
	s := newSwapper()
	f, err := s.Request("night")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if f.Key != "night" || f.ID == "" || f.Seq != 1 {
		t.Fatalf("future = %+v, want key night with a non empty id and seq 1", f)
	}
	handle, st := s.Query("night")
	if st != impl.StateQueued {
		t.Fatalf("state after request = %q, want %q", st, impl.StateQueued)
	}
	if handle.Key != "night" || handle.Gen != 1 || handle.Fallback {
		t.Fatalf("placeholder handle = %+v, want gen 1 without fallback", handle)
	}
	if got := s.PendingCompiles(); got != 1 {
		t.Fatalf("pending compiles = %d, want 1", got)
	}
}

func TestRequestMergesSameKey(t *testing.T) {
	s := newSwapper()
	first, err := s.Request("night")
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	second, err := s.Request("night")
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	if first.ID != second.ID || first.Seq != second.Seq {
		t.Fatalf("same key must share one future, got %+v and %+v", first, second)
	}
	if got := s.PendingCompiles(); got != 1 {
		t.Fatalf("pending compiles after a duplicate request = %d, want 1", got)
	}
	other, err := s.Request("fog")
	if err != nil {
		t.Fatalf("request fog: %v", err)
	}
	if other.ID == first.ID {
		t.Fatalf("different keys must get different futures")
	}
	if got := s.PendingCompiles(); got != 2 {
		t.Fatalf("pending compiles after a second key = %d, want 2", got)
	}
}

func TestRequestRejectsBlankKey(t *testing.T) {
	s := newSwapper()
	for _, key := range []impl.VariantKey{"", "   "} {
		if _, err := s.Request(key); !errors.Is(err, impl.ErrBlankVariant) {
			t.Fatalf("request %q must fail with ErrBlankVariant, got %v", key, err)
		}
	}
	if got := s.PendingCompiles(); got != 0 {
		t.Fatalf("rejected requests must not queue work, pending = %d", got)
	}
}

func TestStartCompileMovesToCompiling(t *testing.T) {
	s := newSwapper()
	queuedVariant(t, s, "night")
	if err := s.StartCompile("night"); err != nil {
		t.Fatalf("start compile: %v", err)
	}
	if _, st := s.Query("night"); st != impl.StateCompiling {
		t.Fatalf("state after start = %q, want %q", st, impl.StateCompiling)
	}
	if err := s.StartCompile("ghost"); !errors.Is(err, impl.ErrUnknownVariant) {
		t.Fatalf("start of an unknown variant must fail with ErrUnknownVariant, got %v", err)
	}
}

func TestCompleteCompileMarksCompiled(t *testing.T) {
	s := newSwapper()
	queuedVariant(t, s, "night")
	if err := s.StartCompile("night"); err != nil {
		t.Fatalf("start compile: %v", err)
	}
	if err := s.CompleteCompile("night", true); err != nil {
		t.Fatalf("complete compile: %v", err)
	}
	if _, st := s.Query("night"); st != impl.StateCompiled {
		t.Fatalf("state after a successful compile = %q, want %q before the frame boundary", st, impl.StateCompiled)
	}
	if got := s.Generation("night"); got != 1 {
		t.Fatalf("generation before the swap = %d, want 1", got)
	}
}

func TestAdvanceFrameSwapsAtBoundary(t *testing.T) {
	s := newSwapper()
	compiledVariant(t, s, "night")
	report := s.AdvanceFrame()
	if report.Frame != 1 || report.Swapped != 1 || report.Deferred != 0 || report.Failed != 0 {
		t.Fatalf("frame report = %+v, want frame 1 with one swap", report)
	}
	handle, st := s.Query("night")
	if st != impl.StateReady {
		t.Fatalf("state after the frame boundary = %q, want %q", st, impl.StateReady)
	}
	if handle.Gen != 2 {
		t.Fatalf("handle generation after the swap = %d, want 2", handle.Gen)
	}
	if got := s.Generation("night"); got != 2 {
		t.Fatalf("generation after the swap = %d, want 2", got)
	}
}

func TestSwapRespectsPerFrameLimit(t *testing.T) {
	s := impl.NewVariantHotSwapper(impl.SwapConfig{MaxSwapsPerFrame: 1, FailureLimit: 3})
	for _, key := range []impl.VariantKey{"a", "b", "c"} {
		compiledVariant(t, s, key)
	}
	first := s.AdvanceFrame()
	if first.Swapped != 1 || first.Deferred != 2 {
		t.Fatalf("first frame = %+v, want swapped 1 deferred 2", first)
	}
	second := s.AdvanceFrame()
	if second.Swapped != 1 || second.Deferred != 1 {
		t.Fatalf("second frame = %+v, want swapped 1 deferred 1", second)
	}
	third := s.AdvanceFrame()
	if third.Swapped != 1 || third.Deferred != 0 {
		t.Fatalf("third frame = %+v, want swapped 1 deferred 0", third)
	}
	for _, key := range []impl.VariantKey{"a", "b", "c"} {
		if _, st := s.Query(key); st != impl.StateReady {
			t.Fatalf("variant %q state = %q, want ready", key, st)
		}
	}
}

func TestCancelQueuedVariant(t *testing.T) {
	s := newSwapper()
	queuedVariant(t, s, "night")
	if err := s.Cancel("night"); err != nil {
		t.Fatalf("cancel a queued variant: %v", err)
	}
	if _, st := s.Query("night"); st != impl.StateCancelled {
		t.Fatalf("state after cancel = %q, want %q", st, impl.StateCancelled)
	}
	if got := s.PendingCompiles(); got != 0 {
		t.Fatalf("pending compiles after cancel = %d, want 0", got)
	}
}

func TestCancelUnknownVariant(t *testing.T) {
	s := newSwapper()
	if err := s.Cancel("ghost"); !errors.Is(err, impl.ErrUnknownVariant) {
		t.Fatalf("cancel of an unknown variant must fail with ErrUnknownVariant, got %v", err)
	}
	if err := s.Cancel(""); !errors.Is(err, impl.ErrUnknownVariant) {
		t.Fatalf("cancel of a blank variant must fail with ErrUnknownVariant, got %v", err)
	}
}

func TestFailureAtLimitFallsBack(t *testing.T) {
	s := impl.NewVariantHotSwapper(impl.SwapConfig{MaxSwapsPerFrame: 2, FailureLimit: 2})
	queuedVariant(t, s, "night")
	if err := s.StartCompile("night"); err != nil {
		t.Fatalf("first start: %v", err)
	}
	if err := s.CompleteCompile("night", false); err != nil {
		t.Fatalf("first failure: %v", err)
	}
	if _, st := s.Query("night"); st != impl.StateQueued {
		t.Fatalf("state after one failure = %q, want a requeue", st)
	}
	if err := s.StartCompile("night"); err != nil {
		t.Fatalf("second start: %v", err)
	}
	if err := s.CompleteCompile("night", false); err != nil {
		t.Fatalf("second failure: %v", err)
	}
	if _, st := s.Query("night"); st != impl.StateFailed {
		t.Fatalf("state after reaching the failure limit = %q, want %q", st, impl.StateFailed)
	}
	handle, _ := s.Query("night")
	if !handle.Fallback {
		t.Fatalf("handle after the failure limit = %+v, want the fallback material", handle)
	}
	if got := s.PendingCompiles(); got != 0 {
		t.Fatalf("pending compiles after the failure limit = %d, want 0", got)
	}
}

func TestAdvanceFrameNoopIncrementsFrame(t *testing.T) {
	s := newSwapper()
	first := s.AdvanceFrame()
	if first.Frame != 1 || first.Swapped != 0 || first.Deferred != 0 || first.Failed != 0 {
		t.Fatalf("idle frame report = %+v, want frame 1 without work", first)
	}
	second := s.AdvanceFrame()
	if second.Frame != 2 {
		t.Fatalf("frame counter = %d, want 2", second.Frame)
	}
}

func TestSnapshotRestoreKeepsGeneration(t *testing.T) {
	s := newSwapper()
	compiledVariant(t, s, "night")
	if report := s.AdvanceFrame(); report.Swapped != 1 {
		t.Fatalf("swap before snapshot = %+v", report)
	}
	snap, err := s.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snap) == 0 {
		t.Fatalf("snapshot payload must not be empty")
	}
	fresh := newSwapper()
	if err := fresh.Restore(snap); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := fresh.Generation("night"); got != 2 {
		t.Fatalf("generation after restore = %d, want 2", got)
	}
	if _, st := fresh.Query("night"); st != impl.StateReady {
		t.Fatalf("state after restore = %q, want ready", st)
	}
	if got := fresh.PendingCompiles(); got != 0 {
		t.Fatalf("pending compiles after restore = %d, want 0", got)
	}
	if report := fresh.AdvanceFrame(); report.Swapped != 0 || report.Deferred != 0 {
		t.Fatalf("frame after restore = %+v, want an idle frame", report)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	s := newSwapper()
	queuedVariant(t, s, "night")
	if err := s.Restore([]byte("swapper-state: truncated")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt payload must fail with ErrBadSnapshot, got %v", err)
	}
	if got := s.Generation("night"); got != 1 {
		t.Fatalf("a rejected restore must keep the runtime state, generation = %d", got)
	}
	if _, st := s.Query("night"); st != impl.StateQueued {
		t.Fatalf("state after a rejected restore = %q, want queued", st)
	}
}

func TestConcurrentRequestSingleCompile(t *testing.T) {
	s := newSwapper()
	const workers = 8
	var wg sync.WaitGroup
	futures := make([]impl.Future, workers)
	errs := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			futures[idx], errs[idx] = s.Request("night")
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d request: %v", i, err)
		}
	}
	for i := 1; i < workers; i++ {
		if futures[i].ID != futures[0].ID {
			t.Fatalf("worker %d got future %q, want the shared %q", i, futures[i].ID, futures[0].ID)
		}
	}
	if got := s.PendingCompiles(); got != 1 {
		t.Fatalf("pending compiles after %d concurrent requests = %d, want 1", workers, got)
	}
}

func TestSetParamBeforeSwapReadback(t *testing.T) {
	s := newSwapper()
	queuedVariant(t, s, "night")
	if err := s.SetParam("night", "exposure", 2.5); err != nil {
		t.Fatalf("set exposure: %v", err)
	}
	if err := s.SetParam("night", "roughness", 0.75); err != nil {
		t.Fatalf("set roughness: %v", err)
	}
	params := s.Params("night")
	if len(params) != 2 {
		t.Fatalf("params = %v, want two entries", params)
	}
	if params["exposure"] != 2.5 || params["roughness"] != 0.75 {
		t.Fatalf("params = %v, want exposure 2.5 and roughness 0.75", params)
	}
	if err := s.SetParam("ghost", "exposure", 1); !errors.Is(err, impl.ErrUnknownVariant) {
		t.Fatalf("param write on an unknown variant must fail with ErrUnknownVariant, got %v", err)
	}
}
