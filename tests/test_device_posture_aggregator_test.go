package task

import (
	"errors"
	"sync"
	"testing"
	"time"

	impl "gsb/batch49-go-safety-a-07"
)

type fakeClock struct {
	mu sync.Mutex
	at time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{at: time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

func postureOptions(clock *fakeClock) impl.Options {
	return impl.Options{
		Weights: map[string]int{
			impl.SignalEDR:        40,
			impl.SignalDiskCrypto: 25,
			impl.SignalPatch:      20,
			impl.SignalJailbreak:  15,
		},
		Freshness: map[string]time.Duration{
			impl.SignalEDR:        10 * time.Minute,
			impl.SignalDiskCrypto: 24 * time.Hour,
			impl.SignalPatch:      72 * time.Hour,
			impl.SignalJailbreak:  24 * time.Hour,
		},
		MaxSignalsPerDevice: 4,
		MaxDevicesPerUser:   4,
		Now:                 clock.Now,
	}
}

func signal(user, kind string, score int, at time.Time, seq int64) impl.Signal {
	return impl.Signal{UserID: user, Kind: kind, Score: score, At: at, Seq: seq}
}

func TestReportBindsDeviceToFirstUser(t *testing.T) {
	clock := newFakeClock()
	a := impl.NewDevicePostureAggregator(postureOptions(clock))
	if err := a.Report("dev-1", signal("user-1", impl.SignalEDR, 100, clock.Now(), 1)); err != nil {
		t.Fatalf("report edr heartbeat: %v", err)
	}
	p, err := a.Posture("dev-1")
	if err != nil {
		t.Fatalf("posture: %v", err)
	}
	if p.UserID != "user-1" || p.DeviceID != "dev-1" {
		t.Fatalf("posture identity = user %q device %q, want user-1 / dev-1", p.UserID, p.DeviceID)
	}
	if p.Score != 40 {
		t.Fatalf("posture score = %d, want the full edr weight 40", p.Score)
	}
	if p.Tier != impl.TierDegraded {
		t.Fatalf("posture tier = %q, want %q at the 40 point threshold", p.Tier, impl.TierDegraded)
	}
	if p.Quarantined {
		t.Fatalf("fresh edr heartbeat must not quarantine the device")
	}
	if len(p.Stale) != 0 {
		t.Fatalf("fresh signal must not be listed as stale, got %v", p.Stale)
	}
	if p.Seq <= 0 {
		t.Fatalf("posture sequence = %d, want a positive number", p.Seq)
	}
	signals := a.Signals("dev-1")
	if len(signals) != 1 || signals[0].Kind != impl.SignalEDR || !signals[0].At.Equal(clock.Now()) {
		t.Fatalf("stored signals = %+v, want the single reported edr heartbeat", signals)
	}
}

func TestReportRejectsBlankAndUnknownKind(t *testing.T) {
	clock := newFakeClock()
	a := impl.NewDevicePostureAggregator(postureOptions(clock))
	if err := a.Report("  ", signal("user-1", impl.SignalEDR, 100, clock.Now(), 1)); !errors.Is(err, impl.ErrBlankID) {
		t.Fatalf("blank device id must fail with ErrBlankID, got %v", err)
	}
	if err := a.Report("dev-1", signal("", impl.SignalEDR, 100, clock.Now(), 1)); !errors.Is(err, impl.ErrBlankID) {
		t.Fatalf("blank user id must fail with ErrBlankID, got %v", err)
	}
	if err := a.Report("dev-1", signal("user-1", "usb_history", 100, clock.Now(), 1)); !errors.Is(err, impl.ErrUnknownKind) {
		t.Fatalf("unknown kind must fail with ErrUnknownKind, got %v", err)
	}
	if err := a.Report("dev-1", signal("user-1", impl.SignalEDR, 101, clock.Now(), 1)); !errors.Is(err, impl.ErrBadScore) {
		t.Fatalf("score above 100 must fail with ErrBadScore, got %v", err)
	}
	if got := len(a.Signals("dev-1")); got != 0 {
		t.Fatalf("rejected reports must not be stored, got %d signals", got)
	}
}

func TestReportRejectsSignalFromOtherUser(t *testing.T) {
	clock := newFakeClock()
	a := impl.NewDevicePostureAggregator(postureOptions(clock))
	if err := a.Report("dev-1", signal("user-1", impl.SignalEDR, 100, clock.Now(), 1)); err != nil {
		t.Fatalf("first report: %v", err)
	}
	if err := a.Report("dev-1", signal("user-2", impl.SignalDiskCrypto, 100, clock.Now(), 2)); !errors.Is(err, impl.ErrDeviceOwner) {
		t.Fatalf("report from another account must fail with ErrDeviceOwner, got %v", err)
	}
	if got := len(a.Signals("dev-1")); got != 1 {
		t.Fatalf("rejected report changed the signal table, got %d entries", got)
	}
}

func TestReportIgnoresStaleSignalByTime(t *testing.T) {
	clock := newFakeClock()
	a := impl.NewDevicePostureAggregator(postureOptions(clock))
	base := clock.Now()
	if err := a.Report("dev-1", signal("user-1", impl.SignalPatch, 100, base, 5)); err != nil {
		t.Fatalf("first patch report: %v", err)
	}
	if err := a.Report("dev-1", signal("user-1", impl.SignalPatch, 90, base.Add(-time.Minute), 6)); !errors.Is(err, impl.ErrStaleSignal) {
		t.Fatalf("older report must fail with ErrStaleSignal, got %v", err)
	}
	if err := a.Report("dev-1", signal("user-1", impl.SignalPatch, 80, base, 4)); !errors.Is(err, impl.ErrStaleSignal) {
		t.Fatalf("same time with a smaller sequence must fail with ErrStaleSignal, got %v", err)
	}
	stored := a.Signals("dev-1")
	if len(stored) != 1 || stored[0].Score != 100 || stored[0].Seq != 5 {
		t.Fatalf("stale reports must not overwrite the entry, got %+v", stored)
	}
	if err := a.Report("dev-1", signal("user-1", impl.SignalPatch, 70, base, 6)); err != nil {
		t.Fatalf("same time with a larger sequence must be accepted, got %v", err)
	}
	stored = a.Signals("dev-1")
	if len(stored) != 1 || stored[0].Score != 70 || stored[0].Seq != 6 {
		t.Fatalf("newer sequence must replace the entry, got %+v", stored)
	}
}

func TestReportEnforcesCaps(t *testing.T) {
	clock := newFakeClock()
	opts := postureOptions(clock)
	opts.MaxSignalsPerDevice = 2
	opts.MaxDevicesPerUser = 1
	a := impl.NewDevicePostureAggregator(opts)
	if err := a.Report("dev-1", signal("user-1", impl.SignalEDR, 100, clock.Now(), 1)); err != nil {
		t.Fatalf("report edr: %v", err)
	}
	if err := a.Report("dev-1", signal("user-1", impl.SignalDiskCrypto, 100, clock.Now(), 2)); err != nil {
		t.Fatalf("report disk: %v", err)
	}
	if err := a.Report("dev-1", signal("user-1", impl.SignalPatch, 100, clock.Now(), 3)); !errors.Is(err, impl.ErrSignalLimit) {
		t.Fatalf("third kind must fail with ErrSignalLimit, got %v", err)
	}
	if err := a.Report("dev-2", signal("user-1", impl.SignalEDR, 100, clock.Now(), 4)); !errors.Is(err, impl.ErrDeviceLimit) {
		t.Fatalf("second device must fail with ErrDeviceLimit, got %v", err)
	}
	if got := len(a.Signals("dev-1")); got != 2 {
		t.Fatalf("signal count after the cap rejection = %d, want 2", got)
	}
}

func TestPostureScoresFreshSignalsByWeight(t *testing.T) {
	clock := newFakeClock()
	a := impl.NewDevicePostureAggregator(postureOptions(clock))
	at := clock.Now()
	for i, s := range []impl.Signal{
		signal("user-1", impl.SignalEDR, 100, at, 1),
		signal("user-1", impl.SignalDiskCrypto, 80, at, 2),
		signal("user-1", impl.SignalPatch, 50, at, 3),
	} {
		if err := a.Report("dev-1", s); err != nil {
			t.Fatalf("report signal %d: %v", i, err)
		}
	}
	p, err := a.Posture("dev-1")
	if err != nil {
		t.Fatalf("posture: %v", err)
	}
	if p.Score != 70 {
		t.Fatalf("posture score = %d, want 40 + 20 + 10 = 70", p.Score)
	}
	if p.Tier != impl.TierDegraded {
		t.Fatalf("posture tier = %q, want %q", p.Tier, impl.TierDegraded)
	}
	if len(p.Stale) != 0 {
		t.Fatalf("all signals are fresh, stale list must be empty, got %v", p.Stale)
	}
}

func TestExpiredSignalUsesReducedWeight(t *testing.T) {
	clock := newFakeClock()
	a := impl.NewDevicePostureAggregator(postureOptions(clock))
	if err := a.Report("dev-1", signal("user-1", impl.SignalPatch, 50, clock.Now(), 1)); err != nil {
		t.Fatalf("report patch: %v", err)
	}
	clock.Advance(73 * time.Hour)
	if err := a.Report("dev-1", signal("user-1", impl.SignalEDR, 100, clock.Now(), 2)); err != nil {
		t.Fatalf("report edr: %v", err)
	}
	if err := a.Report("dev-1", signal("user-1", impl.SignalDiskCrypto, 100, clock.Now(), 3)); err != nil {
		t.Fatalf("report disk: %v", err)
	}
	p, err := a.Posture("dev-1")
	if err != nil {
		t.Fatalf("posture: %v", err)
	}
	if p.Score != 69 {
		t.Fatalf("posture score = %d, want 40 + 25 + 4 with the stale patch weighted at 40 percent", p.Score)
	}
	if len(p.Stale) != 1 || p.Stale[0] != impl.SignalPatch {
		t.Fatalf("stale list = %v, want exactly the expired patch signal", p.Stale)
	}
}

func TestSignalsReturnsDefensiveCopy(t *testing.T) {
	clock := newFakeClock()
	a := impl.NewDevicePostureAggregator(postureOptions(clock))
	if err := a.Report("dev-1", signal("user-1", impl.SignalEDR, 100, clock.Now(), 1)); err != nil {
		t.Fatalf("report edr: %v", err)
	}
	first := a.Signals("dev-1")
	if len(first) != 1 {
		t.Fatalf("signal count = %d, want 1", len(first))
	}
	first[0].Score = 0
	first[0].Kind = "tampered"
	second := a.Signals("dev-1")
	if len(second) != 1 || second[0].Score != 100 || second[0].Kind != impl.SignalEDR {
		t.Fatalf("mutating the returned slice changed stored state: %+v", second)
	}
}

func TestQuarantineThenReleaseRecomputesTier(t *testing.T) {
	clock := newFakeClock()
	a := impl.NewDevicePostureAggregator(postureOptions(clock))
	at := clock.Now()
	for _, s := range []impl.Signal{
		signal("user-1", impl.SignalEDR, 100, at, 1),
		signal("user-1", impl.SignalDiskCrypto, 100, at, 2),
		signal("user-1", impl.SignalPatch, 100, at, 3),
	} {
		if err := a.Report("dev-1", s); err != nil {
			t.Fatalf("report %s: %v", s.Kind, err)
		}
	}
	p, err := a.Posture("dev-1")
	if err != nil {
		t.Fatalf("posture before quarantine: %v", err)
	}
	if p.Tier != impl.TierCompliant {
		t.Fatalf("healthy device tier = %q, want %q", p.Tier, impl.TierCompliant)
	}
	if err := a.Quarantine("dev-1", "left in a taxi"); err != nil {
		t.Fatalf("quarantine: %v", err)
	}
	p, err = a.Posture("dev-1")
	if err != nil {
		t.Fatalf("posture while quarantined: %v", err)
	}
	if !p.Quarantined || p.Tier != impl.TierQuarantined {
		t.Fatalf("quarantined device tier=%q flag=%v, want quarantined", p.Tier, p.Quarantined)
	}
	if err := a.Release("dev-1", "sec-lead"); err != nil {
		t.Fatalf("release: %v", err)
	}
	p, err = a.Posture("dev-1")
	if err != nil {
		t.Fatalf("posture after release: %v", err)
	}
	if p.Quarantined || p.Tier != impl.TierCompliant {
		t.Fatalf("released device tier=%q flag=%v, want compliant again", p.Tier, p.Quarantined)
	}
	audit := a.Audit("user-1")
	if len(audit) != 5 {
		t.Fatalf("audit length = %d, want 3 reports plus quarantine and release", len(audit))
	}
	if audit[3].Action != impl.AuditQuarantined || audit[4].Action != impl.AuditReleased {
		t.Fatalf("audit tail = %q then %q, want quarantined then released", audit[3].Action, audit[4].Action)
	}
}

func TestReleaseRequiresApproverAndQuarantineState(t *testing.T) {
	clock := newFakeClock()
	a := impl.NewDevicePostureAggregator(postureOptions(clock))
	if err := a.Report("dev-1", signal("user-1", impl.SignalEDR, 100, clock.Now(), 1)); err != nil {
		t.Fatalf("report edr: %v", err)
	}
	if err := a.Quarantine("dev-1", "   "); !errors.Is(err, impl.ErrBadReason) {
		t.Fatalf("blank quarantine reason must fail with ErrBadReason, got %v", err)
	}
	if err := a.Quarantine("dev-404", "lost"); !errors.Is(err, impl.ErrDeviceNotFound) {
		t.Fatalf("quarantining an unknown device must fail with ErrDeviceNotFound, got %v", err)
	}
	if err := a.Release("dev-1", "  "); !errors.Is(err, impl.ErrNoApprover) {
		t.Fatalf("blank approver must fail with ErrNoApprover, got %v", err)
	}
	if err := a.Release("dev-1", "sec-lead"); !errors.Is(err, impl.ErrNotQuarantined) {
		t.Fatalf("releasing a healthy device must fail with ErrNotQuarantined, got %v", err)
	}
	if err := a.Release("dev-404", "sec-lead"); !errors.Is(err, impl.ErrDeviceNotFound) {
		t.Fatalf("releasing an unknown device must fail with ErrDeviceNotFound, got %v", err)
	}
}

func TestEvaluateListsOwnedDevicesSorted(t *testing.T) {
	clock := newFakeClock()
	a := impl.NewDevicePostureAggregator(postureOptions(clock))
	for _, dev := range []string{"dev-b", "dev-a"} {
		if err := a.Report(dev, signal("user-1", impl.SignalEDR, 100, clock.Now(), 1)); err != nil {
			t.Fatalf("report %s: %v", dev, err)
		}
	}
	if err := a.Report("dev-c", signal("user-2", impl.SignalEDR, 100, clock.Now(), 2)); err != nil {
		t.Fatalf("report dev-c: %v", err)
	}
	postures, err := a.Evaluate("user-1")
	if err != nil {
		t.Fatalf("evaluate user-1: %v", err)
	}
	if len(postures) != 2 || postures[0].DeviceID != "dev-a" || postures[1].DeviceID != "dev-b" {
		t.Fatalf("evaluate user-1 = %+v, want dev-a then dev-b", postures)
	}
	for i, p := range postures {
		if p.UserID != "user-1" {
			t.Fatalf("evaluate entry %d belongs to %q, want user-1", i, p.UserID)
		}
	}
	other, err := a.Evaluate("user-2")
	if err != nil {
		t.Fatalf("evaluate user-2: %v", err)
	}
	if len(other) != 1 || other[0].DeviceID != "dev-c" {
		t.Fatalf("evaluate user-2 = %+v, want only dev-c", other)
	}
	empty, err := a.Evaluate("user-404")
	if err != nil {
		t.Fatalf("evaluate unknown user: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("unknown user must have no devices, got %+v", empty)
	}
}

func TestTransferMovesOwnership(t *testing.T) {
	clock := newFakeClock()
	a := impl.NewDevicePostureAggregator(postureOptions(clock))
	if err := a.Report("dev-1", signal("user-1", impl.SignalEDR, 100, clock.Now(), 1)); err != nil {
		t.Fatalf("report edr: %v", err)
	}
	if err := a.Transfer("dev-1", "user-2"); err != nil {
		t.Fatalf("transfer: %v", err)
	}
	old, err := a.Evaluate("user-1")
	if err != nil {
		t.Fatalf("evaluate old owner: %v", err)
	}
	if len(old) != 0 {
		t.Fatalf("old owner still lists the transferred device: %+v", old)
	}
	moved, err := a.Evaluate("user-2")
	if err != nil {
		t.Fatalf("evaluate new owner: %v", err)
	}
	if len(moved) != 1 || moved[0].DeviceID != "dev-1" || moved[0].UserID != "user-2" {
		t.Fatalf("new owner postures = %+v, want dev-1 under user-2", moved)
	}
	audit := a.Audit("user-1")
	if len(audit) != 2 || audit[1].Action != impl.AuditTransferred {
		t.Fatalf("old owner audit = %+v, want a trailing transferred event", audit)
	}
	if err := a.Transfer("dev-1", "user-2"); !errors.Is(err, impl.ErrSameOwner) {
		t.Fatalf("transfer to the same owner must fail with ErrSameOwner, got %v", err)
	}
	if err := a.Transfer("dev-1", "  "); !errors.Is(err, impl.ErrBlankID) {
		t.Fatalf("blank target must fail with ErrBlankID, got %v", err)
	}
	if err := a.Transfer("dev-404", "user-2"); !errors.Is(err, impl.ErrDeviceNotFound) {
		t.Fatalf("unknown device transfer must fail with ErrDeviceNotFound, got %v", err)
	}
}

func TestSnapshotRestoreKeepsQuarantineAndSignals(t *testing.T) {
	clock := newFakeClock()
	opts := postureOptions(clock)
	a := impl.NewDevicePostureAggregator(opts)
	at := clock.Now()
	if err := a.Report("dev-1", signal("user-1", impl.SignalEDR, 100, at, 1)); err != nil {
		t.Fatalf("report edr: %v", err)
	}
	if err := a.Report("dev-1", signal("user-1", impl.SignalDiskCrypto, 100, at, 2)); err != nil {
		t.Fatalf("report disk: %v", err)
	}
	if err := a.Quarantine("dev-1", "manual hold"); err != nil {
		t.Fatalf("quarantine: %v", err)
	}
	data, err := a.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewDevicePostureAggregator(opts)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	p, err := restored.Posture("dev-1")
	if err != nil {
		t.Fatalf("posture after restore: %v", err)
	}
	if p.Score != 65 || !p.Quarantined {
		t.Fatalf("restored posture score=%d quarantined=%v, want 65 and quarantined", p.Score, p.Quarantined)
	}
	signals := restored.Signals("dev-1")
	if len(signals) != 2 {
		t.Fatalf("restored signal count = %d, want 2", len(signals))
	}
	for _, s := range signals {
		if !s.At.Equal(at) {
			t.Fatalf("restored signal %q time = %v, want %v", s.Kind, s.At, at)
		}
	}
	if err := restored.Release("dev-1", "sec-lead"); err != nil {
		t.Fatalf("release after restore: %v", err)
	}
	p, err = restored.Posture("dev-1")
	if err != nil {
		t.Fatalf("posture after release: %v", err)
	}
	if p.Quarantined || p.Tier != impl.TierDegraded {
		t.Fatalf("released posture tier=%q flag=%v, want degraded at score 65", p.Tier, p.Quarantined)
	}
	if got := len(restored.Audit("user-1")); got != 4 {
		t.Fatalf("restored audit length = %d, want 2 reports plus quarantine and release", got)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	clock := newFakeClock()
	a := impl.NewDevicePostureAggregator(postureOptions(clock))
	if err := a.Report("dev-1", signal("user-1", impl.SignalEDR, 100, clock.Now(), 1)); err != nil {
		t.Fatalf("report edr: %v", err)
	}
	if err := a.Restore([]byte("<<<not-a-snapshot>>>")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := len(a.Signals("dev-1")); got != 1 {
		t.Fatalf("failed restore must not touch live signals, got %d", got)
	}
	if _, err := a.Posture("dev-1"); err != nil {
		t.Fatalf("live device must still be readable after a rejected restore, got %v", err)
	}
}

func TestConcurrentReportsKeepNewestSignal(t *testing.T) {
	clock := newFakeClock()
	a := impl.NewDevicePostureAggregator(postureOptions(clock))
	base := clock.Now()
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := signal("user-1", impl.SignalEDR, 100, base.Add(time.Duration(i)*time.Minute), int64(i+1))
			_ = a.Report("dev-1", s)
		}(i)
	}
	wg.Wait()
	signals := a.Signals("dev-1")
	if len(signals) != 1 {
		t.Fatalf("signal entries after the race = %d, want exactly 1 per kind", len(signals))
	}
	want := base.Add(11 * time.Minute)
	if !signals[0].At.Equal(want) || signals[0].Seq != 12 {
		t.Fatalf("kept signal = %+v, want the newest one at %v with sequence 12", signals[0], want)
	}
	p, err := a.Posture("dev-1")
	if err != nil {
		t.Fatalf("posture: %v", err)
	}
	if p.Score != 40 || p.Seq <= 0 {
		t.Fatalf("posture after the race = %+v, want score 40 and a positive sequence", p)
	}
}
