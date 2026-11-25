package task

import (
	"errors"
	"sync"
	"testing"
	"time"

	impl "gsb/batch49-go-safety-d-05"
)

func d05Limits() impl.Limits {
	return impl.Limits{MaxSamplesPerTenant: 8, MaxTenants: 32, MaxDriftsPerTenant: 8}
}

func d05Sample(throttled, usec, high, max, oom uint64) impl.Sample {
	return impl.Sample{NrThrottled: throttled, ThrottledUsec: usec, HighEvents: high, MaxEvents: max, OOMEvents: oom}
}

func d05At(minute int) time.Time {
	return time.Unix(10000, 0).Add(time.Duration(minute) * time.Minute)
}

func TestFirstObserveReturnsZeroDelta(t *testing.T) {
	l := impl.NewThrottleEventLedger(d05Limits())
	delta, err := l.Observe("app", d05Sample(10, 100, 1, 0, 0), d05At(0))
	if err != nil {
		t.Fatalf("first observe must succeed, got %v", err)
	}
	if delta.Throttled != 0 || delta.ThrottledUsec != 0 || delta.High != 0 || delta.Max != 0 || delta.OOM != 0 {
		t.Fatalf("first observe delta = %+v, want all zero", delta)
	}
	if delta.Restarted {
		t.Fatalf("first observe must not be marked as a restart")
	}
	if got := l.Samples("app"); got != 0 {
		t.Fatalf("interval count after baseline = %d, want 0", got)
	}
	peak, err := l.Peak("app")
	if err != nil {
		t.Fatalf("Peak must succeed, got %v", err)
	}
	if peak.ThrottledUsec != 0 {
		t.Fatalf("peak before any interval = %d, want 0", peak.ThrottledUsec)
	}
}

func TestObserveComputesCounterDelta(t *testing.T) {
	l := impl.NewThrottleEventLedger(d05Limits())
	if _, err := l.Observe("app", d05Sample(10, 100, 1, 0, 0), d05At(0)); err != nil {
		t.Fatalf("baseline observe must succeed, got %v", err)
	}
	delta, err := l.Observe("app", d05Sample(15, 160, 3, 1, 0), d05At(1))
	if err != nil {
		t.Fatalf("second observe must succeed, got %v", err)
	}
	if delta.Throttled != 5 || delta.ThrottledUsec != 60 {
		t.Fatalf("throttle delta = %d/%d, want 5/60", delta.Throttled, delta.ThrottledUsec)
	}
	if delta.High != 2 || delta.Max != 1 || delta.OOM != 0 {
		t.Fatalf("memory event delta = %d/%d/%d, want 2/1/0", delta.High, delta.Max, delta.OOM)
	}
	if delta.Restarted {
		t.Fatalf("monotonic counters must not be treated as a restart")
	}
	if got := l.Samples("app"); got != 1 {
		t.Fatalf("interval count = %d, want 1", got)
	}
}

func TestObserveRejectsOutOfOrderTime(t *testing.T) {
	l := impl.NewThrottleEventLedger(d05Limits())
	if _, err := l.Observe("app", d05Sample(10, 100, 0, 0, 0), d05At(2)); err != nil {
		t.Fatalf("baseline observe must succeed, got %v", err)
	}
	if _, err := l.Observe("app", d05Sample(12, 120, 0, 0, 0), d05At(1)); !errors.Is(err, impl.ErrOutOfOrder) {
		t.Fatalf("sample older than the baseline must get ErrOutOfOrder, got %v", err)
	}
	if got := l.Samples("app"); got != 0 {
		t.Fatalf("rejected sample must not create an interval, got %d", got)
	}
}

func TestObserveRejectsBlankTenant(t *testing.T) {
	l := impl.NewThrottleEventLedger(d05Limits())
	if _, err := l.Observe("  ", d05Sample(1, 1, 0, 0, 0), d05At(0)); !errors.Is(err, impl.ErrBlankTenant) {
		t.Fatalf("blank tenant must get ErrBlankTenant, got %v", err)
	}
	if got := l.Top(10); len(got) != 0 {
		t.Fatalf("blank tenant must not be stored, top = %+v", got)
	}
}

func TestCounterRegressionResetsBaseline(t *testing.T) {
	l := impl.NewThrottleEventLedger(d05Limits())
	if _, err := l.Observe("app", d05Sample(100, 1000, 5, 2, 1), d05At(0)); err != nil {
		t.Fatalf("baseline observe must succeed, got %v", err)
	}
	delta, err := l.Observe("app", d05Sample(30, 200, 1, 0, 0), d05At(1))
	if err != nil {
		t.Fatalf("regressed observe must succeed, got %v", err)
	}
	if !delta.Restarted {
		t.Fatalf("counter regression must be reported as a restart")
	}
	if delta.Throttled != 30 || delta.ThrottledUsec != 200 || delta.High != 1 {
		t.Fatalf("restart delta = %+v, want the full current counters 30/200/1", delta)
	}
	delta, err = l.Observe("app", d05Sample(40, 260, 1, 0, 0), d05At(2))
	if err != nil {
		t.Fatalf("third observe must succeed, got %v", err)
	}
	if delta.Throttled != 10 || delta.ThrottledUsec != 60 || delta.Restarted {
		t.Fatalf("delta after restart = %+v, want 10/60 without restart", delta)
	}
}

func TestCounterResetRecordsDrift(t *testing.T) {
	l := impl.NewThrottleEventLedger(d05Limits())
	if _, err := l.Observe("app", d05Sample(90, 900, 0, 0, 0), d05At(0)); err != nil {
		t.Fatalf("baseline observe must succeed, got %v", err)
	}
	if _, err := l.Observe("app", d05Sample(10, 100, 0, 0, 0), d05At(1)); err != nil {
		t.Fatalf("regressed observe must succeed, got %v", err)
	}
	drifts := l.Drift("app")
	if len(drifts) != 1 {
		t.Fatalf("drift count = %d, want 1", len(drifts))
	}
	if drifts[0].Kind != impl.DriftCounterReset {
		t.Fatalf("drift kind = %q, want %q", drifts[0].Kind, impl.DriftCounterReset)
	}
	if !drifts[0].At.Equal(d05At(1)) {
		t.Fatalf("drift time = %v, want %v", drifts[0].At, d05At(1))
	}
	if len(l.Drift("other")) != 0 {
		t.Fatalf("unknown tenant must have no drift")
	}
}

func TestDuplicateSampleCountedOnce(t *testing.T) {
	l := impl.NewThrottleEventLedger(d05Limits())
	if _, err := l.Observe("app", d05Sample(10, 100, 0, 0, 0), d05At(0)); err != nil {
		t.Fatalf("baseline observe must succeed, got %v", err)
	}
	if _, err := l.Observe("app", d05Sample(15, 150, 0, 0, 0), d05At(1)); err != nil {
		t.Fatalf("second observe must succeed, got %v", err)
	}
	before, err := l.Window("app", d05At(0), d05At(2))
	if err != nil {
		t.Fatalf("window must succeed, got %v", err)
	}
	delta, err := l.Observe("app", d05Sample(15, 150, 0, 0, 0), d05At(1))
	if err != nil {
		t.Fatalf("replayed sample must be accepted, got %v", err)
	}
	if delta.Throttled != 0 || delta.ThrottledUsec != 0 {
		t.Fatalf("replayed sample delta = %+v, want zero", delta)
	}
	if got := l.Samples("app"); got != 1 {
		t.Fatalf("replayed sample must not add an interval, got %d", got)
	}
	after, err := l.Window("app", d05At(0), d05At(2))
	if err != nil {
		t.Fatalf("window after replay must succeed, got %v", err)
	}
	if after != before {
		t.Fatalf("window changed after replay: before=%+v after=%+v", before, after)
	}
}

func TestWindowSumsIntervals(t *testing.T) {
	l := impl.NewThrottleEventLedger(d05Limits())
	if _, err := l.Observe("app", d05Sample(0, 0, 0, 0, 0), d05At(0)); err != nil {
		t.Fatalf("baseline observe must succeed, got %v", err)
	}
	if _, err := l.Observe("app", d05Sample(5, 50, 1, 0, 0), d05At(1)); err != nil {
		t.Fatalf("second observe must succeed, got %v", err)
	}
	if _, err := l.Observe("app", d05Sample(12, 120, 1, 1, 0), d05At(2)); err != nil {
		t.Fatalf("third observe must succeed, got %v", err)
	}
	usage, err := l.Window("app", d05At(0), d05At(3))
	if err != nil {
		t.Fatalf("window must succeed, got %v", err)
	}
	if usage.Throttled != 12 || usage.ThrottledUsec != 120 {
		t.Fatalf("window throttle = %d/%d, want 12/120", usage.Throttled, usage.ThrottledUsec)
	}
	if usage.High != 2 || usage.Max != 1 || usage.OOM != 0 {
		t.Fatalf("window memory events = %d/%d/%d, want 2/1/0", usage.High, usage.Max, usage.OOM)
	}
	narrow, err := l.Window("app", d05At(1), d05At(2))
	if err != nil {
		t.Fatalf("narrow window must succeed, got %v", err)
	}
	if narrow.ThrottledUsec != 50 {
		t.Fatalf("narrow window usec = %d, want the single interval 50", narrow.ThrottledUsec)
	}
}

func TestWindowRejectsEmptyRange(t *testing.T) {
	l := impl.NewThrottleEventLedger(d05Limits())
	if _, err := l.Observe("app", d05Sample(1, 1, 0, 0, 0), d05At(0)); err != nil {
		t.Fatalf("observe must succeed, got %v", err)
	}
	if _, err := l.Window("app", d05At(1), d05At(1)); !errors.Is(err, impl.ErrEmptyRange) {
		t.Fatalf("zero length window must get ErrEmptyRange, got %v", err)
	}
	if _, err := l.Window("app", d05At(2), d05At(1)); !errors.Is(err, impl.ErrEmptyRange) {
		t.Fatalf("reversed window must get ErrEmptyRange, got %v", err)
	}
}

func TestRetentionEvictsOldest(t *testing.T) {
	l := impl.NewThrottleEventLedger(impl.Limits{MaxSamplesPerTenant: 3, MaxTenants: 8, MaxDriftsPerTenant: 8})
	for i := 0; i < 5; i++ {
		if _, err := l.Observe("app", d05Sample(uint64(i+1), uint64((i+1)*10), 0, 0, 0), d05At(i)); err != nil {
			t.Fatalf("observe %d must succeed, got %v", i, err)
		}
	}
	if got := l.Samples("app"); got != 3 {
		t.Fatalf("retained intervals = %d, want 3", got)
	}
	if _, err := l.Window("app", d05At(0), d05At(5)); !errors.Is(err, impl.ErrOutOfRange) {
		t.Fatalf("window starting before retained coverage must get ErrOutOfRange, got %v", err)
	}
	usage, err := l.Window("app", d05At(2), d05At(5))
	if err != nil {
		t.Fatalf("window inside coverage must succeed, got %v", err)
	}
	if usage.Throttled != 3 {
		t.Fatalf("retained throttle sum = %d, want 3 from intervals 3,4,5", usage.Throttled)
	}
}

func TestSetLimitStoresQuota(t *testing.T) {
	l := impl.NewThrottleEventLedger(d05Limits())
	if err := l.SetLimit("app", impl.Quota{CPUMax: 100000, MemMax: 1024}); !errors.Is(err, impl.ErrUnknownTenant) {
		t.Fatalf("limit on unknown tenant must get ErrUnknownTenant, got %v", err)
	}
	if _, err := l.Observe("app", d05Sample(0, 0, 0, 0, 0), d05At(0)); err != nil {
		t.Fatalf("observe must succeed, got %v", err)
	}
	if err := l.SetLimit("app", impl.Quota{CPUMax: 100000, MemMax: 1024}); err != nil {
		t.Fatalf("SetLimit must succeed, got %v", err)
	}
	q, ok := l.Limit("app")
	if !ok {
		t.Fatalf("Limit must report a stored quota")
	}
	if q.CPUMax != 100000 || q.MemMax != 1024 {
		t.Fatalf("stored quota = %+v, want cpu 100000 and memory 1024", q)
	}
	if err := l.SetLimit("app", impl.Quota{CPUMax: 0, MemMax: 1024}); !errors.Is(err, impl.ErrInvalidQuota) {
		t.Fatalf("non positive quota must get ErrInvalidQuota, got %v", err)
	}
	if _, ok := l.Limit("other"); ok {
		t.Fatalf("unknown tenant must not report a quota")
	}
}

func TestTopRanksByThrottledTime(t *testing.T) {
	l := impl.NewThrottleEventLedger(d05Limits())
	if _, err := l.Observe("app", d05Sample(0, 0, 0, 0, 0), d05At(0)); err != nil {
		t.Fatalf("observe must succeed, got %v", err)
	}
	if _, err := l.Observe("app", d05Sample(1, 120, 0, 0, 0), d05At(1)); err != nil {
		t.Fatalf("observe must succeed, got %v", err)
	}
	if _, err := l.Observe("aux", d05Sample(0, 0, 0, 0, 0), d05At(0)); err != nil {
		t.Fatalf("observe must succeed, got %v", err)
	}
	if _, err := l.Observe("aux", d05Sample(1, 300, 0, 0, 0), d05At(1)); err != nil {
		t.Fatalf("observe must succeed, got %v", err)
	}
	ranks := l.Top(10)
	if len(ranks) != 2 {
		t.Fatalf("rank count = %d, want 2", len(ranks))
	}
	if ranks[0].ID != "aux" || ranks[0].ThrottledUsec != 300 {
		t.Fatalf("first rank = %+v, want aux with 300 usec", ranks[0])
	}
	if ranks[1].ID != "app" || ranks[1].ThrottledUsec != 120 {
		t.Fatalf("second rank = %+v, want app with 120 usec", ranks[1])
	}
	if got := l.Top(1); len(got) != 1 || got[0].ID != "aux" {
		t.Fatalf("Top(1) = %+v, want only aux", got)
	}
	if got := l.Top(0); len(got) != 0 {
		t.Fatalf("Top(0) = %+v, want empty", got)
	}
}

func TestPeakTracksHighestInterval(t *testing.T) {
	l := impl.NewThrottleEventLedger(d05Limits())
	if _, err := l.Observe("app", d05Sample(0, 0, 0, 0, 0), d05At(0)); err != nil {
		t.Fatalf("observe must succeed, got %v", err)
	}
	if _, err := l.Observe("app", d05Sample(5, 500, 0, 0, 0), d05At(1)); err != nil {
		t.Fatalf("observe must succeed, got %v", err)
	}
	if _, err := l.Observe("app", d05Sample(14, 1400, 0, 0, 0), d05At(2)); err != nil {
		t.Fatalf("observe must succeed, got %v", err)
	}
	if _, err := l.Observe("app", d05Sample(16, 1600, 0, 0, 0), d05At(3)); err != nil {
		t.Fatalf("observe must succeed, got %v", err)
	}
	peak, err := l.Peak("app")
	if err != nil {
		t.Fatalf("Peak must succeed, got %v", err)
	}
	if peak.Throttled != 9 || peak.ThrottledUsec != 900 {
		t.Fatalf("peak = %d/%d, want the largest interval 9/900", peak.Throttled, peak.ThrottledUsec)
	}
	if _, err := l.Peak("ghost"); !errors.Is(err, impl.ErrUnknownTenant) {
		t.Fatalf("peak of unknown tenant must get ErrUnknownTenant, got %v", err)
	}
}

func TestConcurrentObserveKeepsAllTenants(t *testing.T) {
	l := impl.NewThrottleEventLedger(d05Limits())
	ids := []string{"t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8"}
	var wg sync.WaitGroup
	errs := make(chan error, len(ids))
	for _, id := range ids {
		id := id
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := l.Observe(id, d05Sample(0, 0, 0, 0, 0), d05At(0))
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent baseline observe must succeed, got %v", err)
		}
	}
	for i, id := range ids {
		if _, err := l.Observe(id, d05Sample(uint64(i+1), uint64((i+1)*10), 0, 0, 0), d05At(1)); err != nil {
			t.Fatalf("second observe for %s must succeed, got %v", id, err)
		}
		if got := l.Samples(id); got != 1 {
			t.Fatalf("interval count for %s = %d, want 1", id, got)
		}
	}
	ranks := l.Top(100)
	if len(ranks) != len(ids) {
		t.Fatalf("rank count = %d, want %d", len(ranks), len(ids))
	}
	var total uint64
	for _, r := range ranks {
		total += r.ThrottledUsec
	}
	if total != 360 {
		t.Fatalf("total ranked usec = %d, want 360", total)
	}
}

func TestSnapshotRestorePreservesWindows(t *testing.T) {
	l := impl.NewThrottleEventLedger(d05Limits())
	if _, err := l.Observe("app", d05Sample(0, 0, 0, 0, 0), d05At(0)); err != nil {
		t.Fatalf("baseline observe must succeed, got %v", err)
	}
	if _, err := l.Observe("app", d05Sample(5, 50, 1, 0, 0), d05At(1)); err != nil {
		t.Fatalf("second observe must succeed, got %v", err)
	}
	if _, err := l.Observe("app", d05Sample(12, 120, 2, 1, 0), d05At(2)); err != nil {
		t.Fatalf("third observe must succeed, got %v", err)
	}
	if err := l.SetLimit("app", impl.Quota{CPUMax: 50000, MemMax: 512}); err != nil {
		t.Fatalf("SetLimit must succeed, got %v", err)
	}
	want, err := l.Window("app", d05At(0), d05At(3))
	if err != nil {
		t.Fatalf("window must succeed, got %v", err)
	}
	blob, err := l.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot must succeed, got %v", err)
	}
	if len(blob) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	m := impl.NewThrottleEventLedger(d05Limits())
	if err := m.Restore(blob); err != nil {
		t.Fatalf("Restore must succeed, got %v", err)
	}
	got, err := m.Window("app", d05At(0), d05At(3))
	if err != nil {
		t.Fatalf("window after restore must succeed, got %v", err)
	}
	if got != want {
		t.Fatalf("window after restore = %+v, want %+v", got, want)
	}
	if n := m.Samples("app"); n != 2 {
		t.Fatalf("interval count after restore = %d, want 2", n)
	}
	q, ok := m.Limit("app")
	if !ok || q.CPUMax != 50000 || q.MemMax != 512 {
		t.Fatalf("limit after restore = %+v ok=%v, want cpu 50000 memory 512", q, ok)
	}
}
