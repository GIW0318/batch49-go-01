package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch49-go-safety-b-19"
)

func ledgerLimits() impl.Limits {
	return impl.Limits{
		KnownQuota:      100,
		ExploreQuota:    20,
		MaxSamples:      8,
		RejectThreshold: 5,
	}
}

func TestCheckAllowsKnownCategoryWithinQuota(t *testing.T) {
	l := impl.NewEgressQuotaLedger(ledgerLimits())
	verdict, err := l.Check("tenant-a", impl.Dest{Host: "objects.example", Category: "object-store", Kind: impl.KindData})
	if err != nil {
		t.Fatalf("check must succeed, got %v", err)
	}
	if !verdict.Allowed {
		t.Fatalf("known destination within quota must be allowed: %+v", verdict)
	}
	if verdict.Category != "object-store" {
		t.Fatalf("category = %q, want object-store", verdict.Category)
	}
	if verdict.Remaining != 100 {
		t.Fatalf("remaining = %d, want the full known quota 100", verdict.Remaining)
	}
	if verdict.Reason != impl.ReasonAllowed {
		t.Fatalf("reason = %q, want %q", verdict.Reason, impl.ReasonAllowed)
	}
}

func TestCheckUsesExploreQuotaForUnknownCategory(t *testing.T) {
	l := impl.NewEgressQuotaLedger(ledgerLimits())
	verdict, err := l.Check("tenant-a", impl.Dest{Host: "files.example", Kind: impl.KindData})
	if err != nil {
		t.Fatalf("check must succeed, got %v", err)
	}
	if !verdict.Allowed {
		t.Fatalf("unknown destination within the explore quota must be allowed: %+v", verdict)
	}
	if verdict.Category != impl.CategoryUnknown {
		t.Fatalf("category = %q, want %q", verdict.Category, impl.CategoryUnknown)
	}
	if verdict.Remaining != 20 {
		t.Fatalf("remaining = %d, want the explore quota 20", verdict.Remaining)
	}
	samples := l.Samples("tenant-a")
	if len(samples) != 1 || samples[0] != "files.example" {
		t.Fatalf("samples = %v, want [files.example]", samples)
	}
}

func TestCheckRejectsBlankTenantAndDestination(t *testing.T) {
	l := impl.NewEgressQuotaLedger(ledgerLimits())
	if _, err := l.Check("   ", impl.Dest{Host: "objects.example", Category: "object-store"}); !errors.Is(err, impl.ErrBlankTenant) {
		t.Fatalf("blank tenant must report ErrBlankTenant, got %v", err)
	}
	if _, err := l.Check("tenant-a", impl.Dest{}); !errors.Is(err, impl.ErrBlankDestination) {
		t.Fatalf("blank host must report ErrBlankDestination, got %v", err)
	}
}

func TestCheckDeniesKnownCategoryWhenQuotaIsConsumed(t *testing.T) {
	l := impl.NewEgressQuotaLedger(impl.Limits{KnownQuota: 10, ExploreQuota: 5, MaxSamples: 4, RejectThreshold: 9})
	if _, err := l.Reserve("tenant-a", 10); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	verdict, err := l.Check("tenant-a", impl.Dest{Host: "objects.example", Category: "object-store", Kind: impl.KindData})
	if err != nil {
		t.Fatalf("check must succeed, got %v", err)
	}
	if verdict.Allowed {
		t.Fatalf("known destination must be denied when quota is fully reserved: %+v", verdict)
	}
	if verdict.Remaining != 0 {
		t.Fatalf("remaining = %d, want 0", verdict.Remaining)
	}
	if verdict.Reason != impl.ReasonQuotaExceeded {
		t.Fatalf("reason = %q, want %q", verdict.Reason, impl.ReasonQuotaExceeded)
	}
}

func TestReserveReturnsTicketsWithMonotonicIDs(t *testing.T) {
	l := impl.NewEgressQuotaLedger(ledgerLimits())
	first, err := l.Reserve("tenant-a", 40)
	if err != nil {
		t.Fatalf("first reserve: %v", err)
	}
	if first.ID == 0 {
		t.Fatalf("ticket id must be positive")
	}
	if first.Tenant != "tenant-a" || first.Reserved != 40 {
		t.Fatalf("ticket = %+v, want tenant-a reserved=40", first)
	}
	second, err := l.Reserve("tenant-a", 25)
	if err != nil {
		t.Fatalf("second reserve: %v", err)
	}
	if second.ID <= first.ID {
		t.Fatalf("second ticket id = %d, want greater than %d", second.ID, first.ID)
	}
	usage := l.Usage("tenant-a")
	if usage.Reserved != 65 {
		t.Fatalf("reserved usage = %d, want 65", usage.Reserved)
	}
	if usage.Known != 0 {
		t.Fatalf("settled usage = %d, want 0 before settle", usage.Known)
	}
}

func TestReserveRejectsNonPositiveEstimate(t *testing.T) {
	l := impl.NewEgressQuotaLedger(ledgerLimits())
	for _, est := range []int64{0, -3} {
		if _, err := l.Reserve("tenant-a", est); !errors.Is(err, impl.ErrBadEstimate) {
			t.Fatalf("estimate %d must report ErrBadEstimate, got %v", est, err)
		}
	}
	if got := l.Usage("tenant-a").Reserved; got != 0 {
		t.Fatalf("rejected reservations left %d bytes reserved", got)
	}
}

func TestReserveDeniedWhenQuotaIsInsufficient(t *testing.T) {
	l := impl.NewEgressQuotaLedger(impl.Limits{KnownQuota: 50, ExploreQuota: 5, MaxSamples: 4, RejectThreshold: 9})
	if _, err := l.Reserve("tenant-a", 40); err != nil {
		t.Fatalf("first reserve: %v", err)
	}
	if _, err := l.Reserve("tenant-a", 20); !errors.Is(err, impl.ErrQuotaExceeded) {
		t.Fatalf("over quota reserve must report ErrQuotaExceeded, got %v", err)
	}
	if got := l.Usage("tenant-a").Reserved; got != 40 {
		t.Fatalf("reserved usage = %d, want the first reservation only", got)
	}
}

func TestSettleReturnsUnusedReservation(t *testing.T) {
	l := impl.NewEgressQuotaLedger(ledgerLimits())
	ticket, err := l.Reserve("tenant-a", 100)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if err := l.Settle(ticket, 60); err != nil {
		t.Fatalf("settle: %v", err)
	}
	usage := l.Usage("tenant-a")
	if usage.Known != 60 || usage.Reserved != 0 {
		t.Fatalf("usage = %+v, want known=60 reserved=0", usage)
	}
	if err := l.Settle(ticket, 10); !errors.Is(err, impl.ErrUnknownTicket) {
		t.Fatalf("second settle must report ErrUnknownTicket, got %v", err)
	}
}

func TestSettleChargesUsageAboveReservation(t *testing.T) {
	l := impl.NewEgressQuotaLedger(impl.Limits{KnownQuota: 200, ExploreQuota: 5, MaxSamples: 4, RejectThreshold: 9})
	ticket, err := l.Reserve("tenant-a", 50)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if err := l.Settle(ticket, 80); err != nil {
		t.Fatalf("settle above reservation: %v", err)
	}
	usage := l.Usage("tenant-a")
	if usage.Known != 80 || usage.Reserved != 0 {
		t.Fatalf("usage = %+v, want known=80 reserved=0", usage)
	}
}

func TestReleaseReturnsWholeReservation(t *testing.T) {
	l := impl.NewEgressQuotaLedger(ledgerLimits())
	ticket, err := l.Reserve("tenant-a", 70)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if err := l.Release(ticket); err != nil {
		t.Fatalf("release: %v", err)
	}
	usage := l.Usage("tenant-a")
	if usage.Reserved != 0 || usage.Known != 0 {
		t.Fatalf("usage = %+v, want reserved=0 known=0", usage)
	}
	if err := l.Release(ticket); !errors.Is(err, impl.ErrUnknownTicket) {
		t.Fatalf("second release must report ErrUnknownTicket, got %v", err)
	}
}

func TestReleaseRejectsUnknownTicket(t *testing.T) {
	l := impl.NewEgressQuotaLedger(ledgerLimits())
	if err := l.Release(impl.Ticket{ID: 999, Tenant: "tenant-a"}); !errors.Is(err, impl.ErrUnknownTicket) {
		t.Fatalf("unknown ticket must report ErrUnknownTicket, got %v", err)
	}
}

func TestSettleRejectsNegativeUsage(t *testing.T) {
	l := impl.NewEgressQuotaLedger(ledgerLimits())
	ticket, err := l.Reserve("tenant-a", 20)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if err := l.Settle(ticket, -1); !errors.Is(err, impl.ErrBadUsage) {
		t.Fatalf("negative usage must report ErrBadUsage, got %v", err)
	}
	if got := l.Usage("tenant-a").Reserved; got != 20 {
		t.Fatalf("reserved usage = %d, want 20 after a rejected settle", got)
	}
}

func TestBreakerOpensAfterConsecutiveRejections(t *testing.T) {
	l := impl.NewEgressQuotaLedger(impl.Limits{KnownQuota: 10, ExploreQuota: 5, MaxSamples: 4, RejectThreshold: 3})
	if _, err := l.Reserve("tenant-a", 10); err != nil {
		t.Fatalf("reserve the whole quota: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := l.Reserve("tenant-a", 1); !errors.Is(err, impl.ErrQuotaExceeded) {
			t.Fatalf("rejection %d must report ErrQuotaExceeded, got %v", i+1, err)
		}
	}
	if usage := l.Usage("tenant-a"); usage.Rejected != 3 {
		t.Fatalf("rejection counter = %d, want 3", usage.Rejected)
	}
	breaker := l.Breaker("tenant-a")
	if !breaker.Open {
		t.Fatalf("breaker must open after the configured number of rejections: %+v", breaker)
	}
	if _, err := l.Reserve("tenant-a", 1); !errors.Is(err, impl.ErrBreakerOpen) {
		t.Fatalf("open breaker must report ErrBreakerOpen, got %v", err)
	}
	verdict, err := l.Check("tenant-a", impl.Dest{Host: "objects.example", Category: "object-store", Kind: impl.KindData})
	if err != nil {
		t.Fatalf("check under breaker: %v", err)
	}
	if verdict.Allowed || verdict.Reason != impl.ReasonBreakerOpen {
		t.Fatalf("data destination under breaker = %+v, want denied with %q", verdict, impl.ReasonBreakerOpen)
	}
}

func TestConcurrentReserveRespectsQuota(t *testing.T) {
	l := impl.NewEgressQuotaLedger(impl.Limits{KnownQuota: 100, ExploreQuota: 5, MaxSamples: 4, RejectThreshold: 99})
	var wg sync.WaitGroup
	var mu sync.Mutex
	okCount, denied := 0, 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := l.Reserve("tenant-a", 10)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				okCount++
			} else if errors.Is(err, impl.ErrQuotaExceeded) {
				denied++
			} else {
				t.Errorf("unexpected reserve error: %v", err)
			}
		}()
	}
	wg.Wait()
	if okCount != 10 {
		t.Fatalf("successful reservations = %d, want 10", okCount)
	}
	if denied != 10 {
		t.Fatalf("denied reservations = %d, want 10", denied)
	}
	if got := l.Usage("tenant-a").Reserved; got != 100 {
		t.Fatalf("reserved usage = %d, want 100", got)
	}
}

func TestSnapshotRestoreRoundTripAndCorruptPayload(t *testing.T) {
	l := impl.NewEgressQuotaLedger(impl.Limits{KnownQuota: 200, ExploreQuota: 20, MaxSamples: 8, RejectThreshold: 9})
	ticket, err := l.Reserve("tenant-a", 100)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if err := l.Settle(ticket, 60); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if _, err := l.Check("tenant-a", impl.Dest{Host: "files.example", Kind: impl.KindData}); err != nil {
		t.Fatalf("explore check: %v", err)
	}
	snap, err := l.Snapshot()
	if err != nil {
		t.Fatalf("snapshot must succeed, got %v", err)
	}
	if len(snap) == 0 {
		t.Fatalf("snapshot payload must not be empty")
	}
	restored := impl.NewEgressQuotaLedger(impl.Limits{KnownQuota: 200, ExploreQuota: 20, MaxSamples: 8, RejectThreshold: 9})
	if err := restored.Restore(snap); err != nil {
		t.Fatalf("restore must succeed, got %v", err)
	}
	usage := restored.Usage("tenant-a")
	if usage.Known != 60 || usage.Reserved != 0 {
		t.Fatalf("restored usage = %+v, want known=60 reserved=0", usage)
	}
	if got := restored.Samples("tenant-a"); len(got) != 1 || got[0] != "files.example" {
		t.Fatalf("restored samples = %v, want [files.example]", got)
	}
	if err := restored.Restore([]byte("]]not-json[[")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt payload must report ErrBadSnapshot, got %v", err)
	}
	if got := restored.Usage("tenant-a").Known; got != 60 {
		t.Fatalf("rejected restore changed settled usage to %d", got)
	}
}
