package task

import (
	"context"
	"errors"
	"sync"
	"testing"

	impl "gsb/batch49-go-safety-b-15"
)

func gcraConfig() impl.GcraConfig {
	return impl.GcraConfig{EmissionMs: 100, BurstUnits: 5, MaxWaitMs: 1000, IdleTtlMs: 10000}
}

func TestTakeAllowsBurstThenDenies(t *testing.T) {
	th := impl.NewGcraThrottle(gcraConfig())
	for i := 0; i < 5; i++ {
		d, err := th.Take("k", 1)
		if err != nil {
			t.Fatalf("take %d: %v", i, err)
		}
		if !d.Allowed || d.Reason != impl.ReasonAllowed {
			t.Fatalf("take %d = %+v, want allowed", i, d)
		}
	}
	last, err := th.Take("k", 1)
	if err != nil {
		t.Fatalf("denied take must not return an error: %v", err)
	}
	if last.Allowed {
		t.Fatalf("take beyond the burst must be denied, got %+v", last)
	}
	if last.WaitMs != 100 || last.Reason != impl.ReasonRateLimited {
		t.Fatalf("denied take = %+v, want wait 100 and rate_limited", last)
	}
	usage := th.Usage("k")
	if usage.RemainingUnits != 0 || usage.TatMs != 500 {
		t.Fatalf("usage = %+v, want no remaining units and tat 500", usage)
	}
	if usage.Allowed != 5 || usage.Denied != 1 || usage.LastAt != 0 {
		t.Fatalf("counters = %+v, want 5 allowed 1 denied at time 0", usage)
	}
}

func TestDeniedDecisionReportsExactWait(t *testing.T) {
	cfg := gcraConfig()
	cfg.BurstUnits = 3
	th := impl.NewGcraThrottle(cfg)
	for i := 0; i < 3; i++ {
		if d, err := th.Take("k", 1); err != nil || !d.Allowed {
			t.Fatalf("take %d = %+v err=%v, want allowed", i, d, err)
		}
	}
	d, err := th.Take("k", 1)
	if err != nil {
		t.Fatalf("denied take: %v", err)
	}
	if d.Allowed || d.WaitMs != 100 {
		t.Fatalf("denied decision = %+v, want denied after exactly 100ms", d)
	}
	if d.RemainingUnits != 0 {
		t.Fatalf("remaining units on denial = %d, want 0", d.RemainingUnits)
	}
	if d.TatMs != 300 {
		t.Fatalf("tat on denial = %d, want the unchanged 300", d.TatMs)
	}
}

func TestRefillOverTimeAllowsAgain(t *testing.T) {
	cfg := gcraConfig()
	cfg.BurstUnits = 3
	th := impl.NewGcraThrottle(cfg)
	for i := 0; i < 3; i++ {
		if d, err := th.Take("k", 1); err != nil || !d.Allowed {
			t.Fatalf("initial take %d = %+v err=%v", i, d, err)
		}
	}
	th.Advance(250)
	for i := 0; i < 2; i++ {
		d, err := th.Take("k", 1)
		if err != nil {
			t.Fatalf("refilled take %d: %v", i, err)
		}
		if !d.Allowed {
			t.Fatalf("refilled take %d = %+v, want allowed", i, d)
		}
	}
	d, err := th.Take("k", 1)
	if err != nil {
		t.Fatalf("third refilled take: %v", err)
	}
	if d.Allowed || d.WaitMs != 50 {
		t.Fatalf("third refilled take = %+v, want denied after 50ms", d)
	}
}

func TestTakeRejectsNonPositiveUnits(t *testing.T) {
	th := impl.NewGcraThrottle(gcraConfig())
	if _, err := th.Take("k", 0); !errors.Is(err, impl.ErrBadUnits) {
		t.Fatalf("zero units must fail with ErrBadUnits, got %v", err)
	}
	if _, err := th.Take("k", -2); !errors.Is(err, impl.ErrBadUnits) {
		t.Fatalf("negative units must fail with ErrBadUnits, got %v", err)
	}
	if usage := th.Usage("k"); usage.Allowed != 0 || usage.Denied != 0 {
		t.Fatalf("rejected units must not change counters, got %+v", usage)
	}
}

func TestTakeRejectsBlankKey(t *testing.T) {
	th := impl.NewGcraThrottle(gcraConfig())
	if _, err := th.Take("   ", 1); !errors.Is(err, impl.ErrBlankKey) {
		t.Fatalf("blank key must fail with ErrBlankKey, got %v", err)
	}
	if _, err := th.Take("", 1); !errors.Is(err, impl.ErrBlankKey) {
		t.Fatalf("empty key must fail with ErrBlankKey, got %v", err)
	}
}

func TestTakeWaitDebitsReservation(t *testing.T) {
	cfg := impl.GcraConfig{EmissionMs: 10, BurstUnits: 1, MaxWaitMs: 100, IdleTtlMs: 1000}
	th := impl.NewGcraThrottle(cfg)
	if d, err := th.Take("k", 1); err != nil || !d.Allowed {
		t.Fatalf("initial take = %+v err=%v, want allowed", d, err)
	}
	if err := th.TakeWait(context.Background(), "k", 1); err != nil {
		t.Fatalf("bounded wait must succeed: %v", err)
	}
	d, err := th.Take("k", 1)
	if err != nil {
		t.Fatalf("take after wait: %v", err)
	}
	if d.Allowed || d.WaitMs != 20 {
		t.Fatalf("take after wait = %+v, want denied after 20ms", d)
	}
}

func TestTakeWaitRejectsBeyondMaxWait(t *testing.T) {
	cfg := impl.GcraConfig{EmissionMs: 100, BurstUnits: 1, MaxWaitMs: 20, IdleTtlMs: 1000}
	th := impl.NewGcraThrottle(cfg)
	if d, err := th.Take("k", 1); err != nil || !d.Allowed {
		t.Fatalf("initial take = %+v err=%v, want allowed", d, err)
	}
	if err := th.TakeWait(context.Background(), "k", 1); !errors.Is(err, impl.ErrWaitTooLong) {
		t.Fatalf("wait beyond the configured maximum must fail with ErrWaitTooLong, got %v", err)
	}
	d, err := th.Take("k", 1)
	if err != nil {
		t.Fatalf("take after rejected wait: %v", err)
	}
	if d.Allowed || d.WaitMs != 100 {
		t.Fatalf("rejected wait must not consume the reservation, got %+v", d)
	}
}

func TestTakeWaitHonoursContextCancel(t *testing.T) {
	cfg := impl.GcraConfig{EmissionMs: 100, BurstUnits: 1, MaxWaitMs: 1000, IdleTtlMs: 1000}
	th := impl.NewGcraThrottle(cfg)
	if d, err := th.Take("k", 1); err != nil || !d.Allowed {
		t.Fatalf("initial take = %+v err=%v, want allowed", d, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := th.TakeWait(ctx, "k", 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context must abort the wait with context.Canceled, got %v", err)
	}
	if usage := th.Usage("k"); usage.Allowed != 1 || usage.Denied != 0 {
		t.Fatalf("canceled wait counters = %+v, want one allowed and no denial", usage)
	}
}

func TestResetRestoresFullBurst(t *testing.T) {
	cfg := gcraConfig()
	cfg.BurstUnits = 2
	th := impl.NewGcraThrottle(cfg)
	for i := 0; i < 2; i++ {
		if d, err := th.Take("k", 1); err != nil || !d.Allowed {
			t.Fatalf("initial take %d = %+v err=%v", i, d, err)
		}
	}
	if d, err := th.Take("k", 1); err != nil || d.Allowed {
		t.Fatalf("before reset = %+v err=%v, want denied", d, err)
	}
	if err := th.Reset("k"); err != nil {
		t.Fatalf("reset: %v", err)
	}
	usage := th.Usage("k")
	if usage.RemainingUnits != 2 || usage.TatMs != 0 || usage.Allowed != 0 || usage.Denied != 0 {
		t.Fatalf("usage after reset = %+v, want a clean full burst", usage)
	}
	for i := 0; i < 2; i++ {
		if d, err := th.Take("k", 1); err != nil || !d.Allowed {
			t.Fatalf("post reset take %d = %+v err=%v, want allowed", i, d, err)
		}
	}
	if err := th.Reset("unknown"); err != nil {
		t.Fatalf("reset of an unknown key must be a no-op, got %v", err)
	}
}

func TestUsageReportsRemainingAndTat(t *testing.T) {
	th := impl.NewGcraThrottle(gcraConfig())
	d, err := th.Take("k", 2)
	if err != nil || !d.Allowed {
		t.Fatalf("take 2 = %+v err=%v, want allowed", d, err)
	}
	if d.RemainingUnits != 3 || d.TatMs != 200 {
		t.Fatalf("take 2 decision = %+v, want remaining 3 and tat 200", d)
	}
	denied, err := th.Take("k", 4)
	if err != nil {
		t.Fatalf("take 4: %v", err)
	}
	if denied.Allowed || denied.WaitMs != 100 {
		t.Fatalf("take 4 = %+v, want denied after 100ms", denied)
	}
	usage := th.Usage("k")
	if usage.RemainingUnits != 3 || usage.TatMs != 200 {
		t.Fatalf("usage = %+v, want remaining 3 and tat 200", usage)
	}
	if usage.Allowed != 2 || usage.Denied != 1 || usage.LastAt != 0 {
		t.Fatalf("usage counters = %+v, want 2 allowed 1 denied at 0", usage)
	}
}

func TestKeysSortedAndResetRemovesKey(t *testing.T) {
	cfg := gcraConfig()
	cfg.BurstUnits = 10
	th := impl.NewGcraThrottle(cfg)
	for _, key := range []string{"c", "a", "b"} {
		if d, err := th.Take(key, 1); err != nil || !d.Allowed {
			t.Fatalf("take %s = %+v err=%v", key, d, err)
		}
	}
	if got := th.Keys(); !equalStrings(got, []string{"a", "b", "c"}) {
		t.Fatalf("keys = %v, want sorted [a b c]", got)
	}
	if err := th.Reset("b"); err != nil {
		t.Fatalf("reset b: %v", err)
	}
	if got := th.Keys(); !equalStrings(got, []string{"a", "c"}) {
		t.Fatalf("keys after reset = %v, want [a c]", got)
	}
	if usage := th.Usage("b"); usage.RemainingUnits != 0 || usage.TatMs != 0 || usage.Allowed != 0 {
		t.Fatalf("usage of a reset key = %+v, want zero", usage)
	}
}

func TestKeysAreIndependent(t *testing.T) {
	cfg := gcraConfig()
	cfg.BurstUnits = 2
	th := impl.NewGcraThrottle(cfg)
	for i := 0; i < 2; i++ {
		if d, err := th.Take("a", 1); err != nil || !d.Allowed {
			t.Fatalf("take a %d = %+v err=%v", i, d, err)
		}
	}
	if d, err := th.Take("a", 1); err != nil || d.Allowed {
		t.Fatalf("exhausted key a = %+v err=%v, want denied", d, err)
	}
	for i := 0; i < 2; i++ {
		if d, err := th.Take("b", 1); err != nil || !d.Allowed {
			t.Fatalf("independent key b take %d = %+v err=%v, want allowed", i, d, err)
		}
	}
}

func TestConcurrentTakesOnOneKeySerialized(t *testing.T) {
	cfg := gcraConfig()
	cfg.BurstUnits = 10
	th := impl.NewGcraThrottle(cfg)
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed, denied := 0, 0
	errs := make(chan error, 30)
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := th.Take("hot", 1)
			if err != nil {
				errs <- err
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if d.Allowed {
				allowed++
			} else {
				denied++
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent take failed: %v", err)
	}
	if allowed != 10 {
		t.Fatalf("concurrent takes allowed %d, want exactly the burst 10", allowed)
	}
	if denied != 20 {
		t.Fatalf("concurrent takes denied %d, want 20", denied)
	}
	if usage := th.Usage("hot"); usage.Allowed != 10 || usage.Denied != 20 {
		t.Fatalf("concurrent usage counters = %+v, want 10 allowed 20 denied", usage)
	}
}

func TestSnapshotRestorePreservesTat(t *testing.T) {
	th := impl.NewGcraThrottle(gcraConfig())
	for i := 0; i < 3; i++ {
		if d, err := th.Take("k", 1); err != nil || !d.Allowed {
			t.Fatalf("initial take %d = %+v err=%v", i, d, err)
		}
	}
	th.Advance(150)
	data, err := th.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewGcraThrottle(gcraConfig())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.Clock(); got != 150 {
		t.Fatalf("restored clock = %d, want 150", got)
	}
	if got := restored.Keys(); !equalStrings(got, []string{"k"}) {
		t.Fatalf("restored keys = %v, want [k]", got)
	}
	usage := restored.Usage("k")
	if usage.RemainingUnits != 3 || usage.TatMs != 300 {
		t.Fatalf("restored usage = %+v, want remaining 3 and tat 300", usage)
	}
	d, err := restored.Take("k", 1)
	if err != nil {
		t.Fatalf("take after restore: %v", err)
	}
	if !d.Allowed || d.TatMs != 400 || d.RemainingUnits != 2 {
		t.Fatalf("take after restore = %+v, want allowed with the restored clock", d)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	th := impl.NewGcraThrottle(gcraConfig())
	if d, err := th.Take("k", 1); err != nil || !d.Allowed {
		t.Fatalf("initial take = %+v err=%v, want allowed", d, err)
	}
	if err := th.Restore([]byte("{oops")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	usage := th.Usage("k")
	if usage.TatMs != 100 || usage.RemainingUnits != 4 {
		t.Fatalf("rejected restore changed the live state: %+v", usage)
	}
	if d, err := th.Take("k", 1); err != nil || !d.Allowed {
		t.Fatalf("live state must keep serving after a rejected restore, got %+v err=%v", d, err)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
