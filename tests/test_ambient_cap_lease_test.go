package task

import (
	"errors"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	impl "gsb/batch49-go-safety-d-10"
)

func leaseConfig() impl.LeaseConfig {
	return impl.LeaseConfig{
		MaxTTL:          time.Hour,
		MaxLeases:       8,
		MaxCapsPerLease: 2,
		Bounding:        []impl.Cap{impl.CapNetConnect, impl.CapFSWrite, impl.CapProcSignal},
		Permitted:       []impl.Cap{impl.CapNetConnect, impl.CapFSWrite, impl.CapProcSignal, impl.CapSysAdmin},
	}
}

func ledger() *impl.AmbientCapLease {
	return impl.NewAmbientCapLease(leaseConfig())
}

func mustAcquire(t *testing.T, l *impl.AmbientCapLease, op string, caps []impl.Cap, ttl time.Duration) impl.Lease {
	t.Helper()
	lease, err := l.Acquire(op, caps, ttl)
	if err != nil {
		t.Fatalf("acquire %s %v: %v", op, caps, err)
	}
	if lease.ID == "" {
		t.Fatalf("acquire %s returned an empty lease id", op)
	}
	return lease
}

func TestAcquireMakesLeaseVisible(t *testing.T) {
	l := ledger()
	lease, err := l.Acquire("net.connect", []impl.Cap{impl.CapNetConnect}, 10*time.Minute)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if !strings.HasPrefix(lease.ID, "lease-") {
		t.Fatalf("lease id = %q, want a lease- prefix", lease.ID)
	}
	if lease.Op != "net.connect" || lease.State != impl.LeaseStateActive {
		t.Fatalf("lease = %+v, want op net.connect in state %q", lease, impl.LeaseStateActive)
	}
	if !reflect.DeepEqual(lease.Caps, []impl.Cap{impl.CapNetConnect}) {
		t.Fatalf("lease caps = %v, want [net.connect]", lease.Caps)
	}
	if active := l.Active(); len(active) != 1 || active[0].ID != lease.ID {
		t.Fatalf("active = %+v, want only %q", active, lease.ID)
	}
	if readback := l.Readback(); !reflect.DeepEqual(readback, []impl.Cap{impl.CapNetConnect}) {
		t.Fatalf("readback = %v, want [net.connect]", readback)
	}
}

func TestAcquireRejectsEmptyOpAndEmptyCaps(t *testing.T) {
	l := ledger()
	if _, err := l.Acquire("", []impl.Cap{impl.CapNetConnect}, time.Minute); !errors.Is(err, impl.ErrBadOp) {
		t.Fatalf("blank op err = %v, want ErrBadOp", err)
	}
	if _, err := l.Acquire("net.connect", nil, time.Minute); !errors.Is(err, impl.ErrNoCaps) {
		t.Fatalf("empty caps err = %v, want ErrNoCaps", err)
	}
	if active := l.Active(); len(active) != 0 {
		t.Fatalf("active = %+v, want none after rejected requests", active)
	}
}

func TestAcquireRejectsTTLOutOfRange(t *testing.T) {
	l := ledger()
	if _, err := l.Acquire("net.connect", []impl.Cap{impl.CapNetConnect}, 0); !errors.Is(err, impl.ErrBadTTL) {
		t.Fatalf("zero ttl err = %v, want ErrBadTTL", err)
	}
	if _, err := l.Acquire("net.connect", []impl.Cap{impl.CapNetConnect}, impl.DefaultMaxTTL+time.Second); !errors.Is(err, impl.ErrBadTTL) {
		t.Fatalf("ttl above the configured maximum err = %v, want ErrBadTTL", err)
	}
	if active := l.Active(); len(active) != 0 {
		t.Fatalf("active = %+v, want none after rejected requests", active)
	}
}

func TestAcquireRejectsCapOutsideBounding(t *testing.T) {
	l := ledger()
	if _, err := l.Acquire("sys.admin", []impl.Cap{impl.CapSysAdmin}, time.Minute); !errors.Is(err, impl.ErrCapOutOfBounds) {
		t.Fatalf("out of bounds cap err = %v, want ErrCapOutOfBounds", err)
	}
	if active := l.Active(); len(active) != 0 {
		t.Fatalf("active = %+v, want the whole request rejected", active)
	}
}

func TestAcquireRejectsCapOutsidePermitted(t *testing.T) {
	l := ledger()
	if _, err := l.Acquire("fs.read", []impl.Cap{impl.CapFSRead}, time.Minute); !errors.Is(err, impl.ErrCapNotPermitted) {
		t.Fatalf("unpermitted cap err = %v, want ErrCapNotPermitted", err)
	}
	if readback := l.Readback(); len(readback) != 0 {
		t.Fatalf("readback = %v, want none after a rejected request", readback)
	}
}

func TestAcquireEnforcesCapsPerLeaseLimit(t *testing.T) {
	l := ledger()
	caps := []impl.Cap{impl.CapNetConnect, impl.CapFSWrite, impl.CapProcSignal}
	if _, err := l.Acquire("multi", caps, time.Minute); !errors.Is(err, impl.ErrTooManyCaps) {
		t.Fatalf("oversized lease err = %v, want ErrTooManyCaps", err)
	}
	if active := l.Active(); len(active) != 0 {
		t.Fatalf("active = %+v, want none after a rejected request", active)
	}
}

func TestReadbackIsUnionOfActiveLeases(t *testing.T) {
	l := ledger()
	first := mustAcquire(t, l, "net.connect", []impl.Cap{impl.CapNetConnect}, time.Minute)
	mustAcquire(t, l, "fs.write", []impl.Cap{impl.CapFSWrite}, time.Minute)
	if readback := l.Readback(); !reflect.DeepEqual(readback, []impl.Cap{impl.CapFSWrite, impl.CapNetConnect}) {
		t.Fatalf("readback = %v, want sorted union [fs.write net.connect]", readback)
	}
	if err := l.Release(first.ID); err != nil {
		t.Fatalf("release first: %v", err)
	}
	if readback := l.Readback(); !reflect.DeepEqual(readback, []impl.Cap{impl.CapFSWrite}) {
		t.Fatalf("readback after release = %v, want [fs.write] only", readback)
	}
}

func TestReleaseRemovesLeaseAndAudits(t *testing.T) {
	l := ledger()
	lease := mustAcquire(t, l, "net.connect", []impl.Cap{impl.CapNetConnect}, time.Minute)
	if err := l.Release(lease.ID); err != nil {
		t.Fatalf("release: %v", err)
	}
	if active := l.Active(); len(active) != 0 {
		t.Fatalf("active = %+v, want none after release", active)
	}
	if _, ok := l.LeaseByID(lease.ID); ok {
		t.Fatalf("released lease %q is still looked up", lease.ID)
	}
	records := l.Audit()
	if len(records) != 1 {
		t.Fatalf("audit = %+v, want exactly one record", records)
	}
	if records[0].ID != lease.ID || records[0].Reason != impl.ReasonReleased {
		t.Fatalf("audit record = %+v, want id %q reason %q", records[0], lease.ID, impl.ReasonReleased)
	}
	if err := l.Release(lease.ID); !errors.Is(err, impl.ErrUnknownLease) {
		t.Fatalf("second release err = %v, want ErrUnknownLease", err)
	}
}

func TestSweepExpiresOnlyDueLeases(t *testing.T) {
	l := ledger()
	base := time.Now()
	first := mustAcquire(t, l, "net.connect", []impl.Cap{impl.CapNetConnect}, time.Minute)
	second := mustAcquire(t, l, "fs.write", []impl.Cap{impl.CapFSWrite}, time.Minute)
	if swept := l.Sweep(base.Add(30 * time.Second)); len(swept) != 0 {
		t.Fatalf("sweep before expiry reclaimed %v, want none", swept)
	}
	swept := l.Sweep(base.Add(2 * time.Minute))
	want := []string{first.ID, second.ID}
	sort.Strings(want)
	if !reflect.DeepEqual(swept, want) {
		t.Fatalf("sweep = %v, want %v", swept, want)
	}
	if active := l.Active(); len(active) != 0 {
		t.Fatalf("active = %+v, want none after sweep", active)
	}
	if readback := l.Readback(); len(readback) != 0 {
		t.Fatalf("readback = %v, want empty after sweep", readback)
	}
}

func TestSweepIsIdempotent(t *testing.T) {
	l := ledger()
	base := time.Now()
	mustAcquire(t, l, "net.connect", []impl.Cap{impl.CapNetConnect}, time.Minute)
	mustAcquire(t, l, "fs.write", []impl.Cap{impl.CapFSWrite}, time.Minute)
	later := base.Add(2 * time.Minute)
	if swept := l.Sweep(later); len(swept) != 2 {
		t.Fatalf("first sweep = %v, want two leases", swept)
	}
	if swept := l.Sweep(later); len(swept) != 0 {
		t.Fatalf("second sweep = %v, want nothing left", swept)
	}
	if records := l.Audit(); len(records) != 2 {
		t.Fatalf("audit records = %d, want 2 (one per lease)", len(records))
	}
}

func TestExpiredLeaseCannotBeReleasedAgain(t *testing.T) {
	l := ledger()
	base := time.Now()
	lease := mustAcquire(t, l, "net.connect", []impl.Cap{impl.CapNetConnect}, time.Minute)
	l.Sweep(base.Add(2 * time.Minute))
	if err := l.Release(lease.ID); !errors.Is(err, impl.ErrUnknownLease) {
		t.Fatalf("release after expiry err = %v, want ErrUnknownLease", err)
	}
	records := l.Audit()
	if len(records) != 1 {
		t.Fatalf("audit = %+v, want exactly one reclamation record", records)
	}
	if records[0].Reason != impl.ReasonExpired {
		t.Fatalf("audit reason = %q, want %q", records[0].Reason, impl.ReasonExpired)
	}
}

func TestSetBoundingRejectsEmptyAndUnpermitted(t *testing.T) {
	l := ledger()
	if err := l.SetBounding(nil); !errors.Is(err, impl.ErrEmptyBounding) {
		t.Fatalf("empty bounding err = %v, want ErrEmptyBounding", err)
	}
	if err := l.SetBounding([]impl.Cap{impl.CapFSRead}); !errors.Is(err, impl.ErrCapNotPermitted) {
		t.Fatalf("unpermitted bounding err = %v, want ErrCapNotPermitted", err)
	}
}

func TestSetBoundingNarrowsFutureAcquires(t *testing.T) {
	l := ledger()
	if err := l.SetBounding([]impl.Cap{impl.CapNetConnect, impl.CapFSWrite}); err != nil {
		t.Fatalf("shrink bounding: %v", err)
	}
	mustAcquire(t, l, "net.connect", []impl.Cap{impl.CapNetConnect}, time.Minute)
	if _, err := l.Acquire("proc.signal", []impl.Cap{impl.CapProcSignal}, time.Minute); !errors.Is(err, impl.ErrCapOutOfBounds) {
		t.Fatalf("acquire of a removed cap err = %v, want ErrCapOutOfBounds", err)
	}
}

func TestSnapshotRestoreKeepsLeasesAndBounding(t *testing.T) {
	l := ledger()
	lease := mustAcquire(t, l, "net.connect", []impl.Cap{impl.CapNetConnect}, 10*time.Minute)
	if err := l.SetBounding([]impl.Cap{impl.CapNetConnect, impl.CapFSWrite}); err != nil {
		t.Fatalf("shrink bounding: %v", err)
	}
	payload, err := l.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(payload) == 0 {
		t.Fatalf("snapshot payload is empty")
	}
	restored := impl.NewAmbientCapLease(leaseConfig())
	if err := restored.Restore(payload); err != nil {
		t.Fatalf("restore: %v", err)
	}
	active := restored.Active()
	if len(active) != 1 || active[0].ID != lease.ID {
		t.Fatalf("restored active = %+v, want only %q", active, lease.ID)
	}
	if readback := restored.Readback(); !reflect.DeepEqual(readback, []impl.Cap{impl.CapNetConnect}) {
		t.Fatalf("restored readback = %v, want [net.connect]", readback)
	}
	if _, err := restored.Acquire("proc.signal", []impl.Cap{impl.CapProcSignal}, time.Minute); !errors.Is(err, impl.ErrCapOutOfBounds) {
		t.Fatalf("acquire after restore err = %v, want the restored bounding set to still apply", err)
	}
	if err := restored.Restore([]byte("not a snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("restore garbage err = %v, want ErrBadSnapshot", err)
	}
}

func TestConcurrentLeaseChurnKeepsReadbackClean(t *testing.T) {
	cfg := leaseConfig()
	cfg.MaxLeases = 64
	l := impl.NewAmbientCapLease(cfg)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			op := "op-" + strconv.Itoa(idx)
			lease, err := l.AcquireFor("holder", op, []impl.Cap{impl.CapNetConnect}, time.Minute)
			if err != nil {
				t.Errorf("concurrent acquire: %v", err)
				return
			}
			if err := l.Release(lease.ID); err != nil {
				t.Errorf("concurrent release: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if readback := l.Readback(); len(readback) != 0 {
		t.Fatalf("readback = %v, want empty once every lease is released", readback)
	}
	if active := l.Active(); len(active) != 0 {
		t.Fatalf("active = %+v, want none", active)
	}
	if records := l.Audit(); len(records) != 32 {
		t.Fatalf("audit records = %d, want 32 releases", len(records))
	}
}
