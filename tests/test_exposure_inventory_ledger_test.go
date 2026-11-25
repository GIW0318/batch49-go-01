package task

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	impl "gsb/batch49-go-safety-c-09"
)

func t0() time.Time { return time.Date(2026, 5, 6, 8, 0, 0, 0, time.UTC) }

func openObs(addr string, port int, proto, cert string, at time.Time) impl.Observation {
	return impl.Observation{Addr: addr, Port: port, Proto: proto, CertSubject: cert, State: impl.StateOpen, Seen: at}
}

func newLedger(t *testing.T, ttl time.Duration, max int) *impl.ExposureLedger {
	t.Helper()
	l := impl.NewExposureLedger(impl.Limits{TTL: ttl, MaxItems: max})
	if l == nil {
		t.Fatalf("NewExposureLedger returned nil")
	}
	return l
}

func lastChange(changes []impl.Change) (impl.Change, bool) {
	if len(changes) == 0 {
		return impl.Change{}, false
	}
	return changes[len(changes)-1], true
}

func TestObserveCreatesItemWithInheritedOwner(t *testing.T) {
	l := newLedger(t, time.Hour, 64)
	if err := l.Inherit("10.0.0.0/8", "netops"); err != nil {
		t.Fatalf("inherit: %v", err)
	}
	id, err := l.Observe(openObs("10.1.2.3", 443, "TCP", "CN=api", t0()))
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if id != "exp-0001" {
		t.Fatalf("item id = %q, want the first sequential id exp-0001", id)
	}
	item, ok := l.Get(id)
	if !ok {
		t.Fatalf("observed item must be readable")
	}
	if item.Owner != "netops" || item.OwnerSource != impl.OwnerInherited {
		t.Fatalf("owner = %q (%q), want the inherited netops owner", item.Owner, item.OwnerSource)
	}
	if item.Proto != impl.ProtoTCP {
		t.Fatalf("proto = %q, want the folded tcp protocol", item.Proto)
	}
	if item.State != impl.StateOpen {
		t.Fatalf("state = %q, want %q", item.State, impl.StateOpen)
	}
	if !item.FirstSeen.Equal(t0()) || !item.LastSeen.Equal(t0()) {
		t.Fatalf("seen window = %v..%v, want both %v", item.FirstSeen, item.LastSeen, t0())
	}
	if item.Evidence != 1 {
		t.Fatalf("evidence = %d, want 1", item.Evidence)
	}
	if stats := l.Stats(); stats.Live != 1 || stats.Evidence != 1 {
		t.Fatalf("stats = %+v, want one live item with one evidence row", stats)
	}
}

func TestObserveRejectsBadObservation(t *testing.T) {
	l := newLedger(t, time.Hour, 64)
	bad := []impl.Observation{
		openObs("", 443, impl.ProtoTCP, "", t0()),
		openObs("10.0.0.999", 443, impl.ProtoTCP, "", t0()),
		openObs("10.0.0.1", 0, impl.ProtoTCP, "", t0()),
		openObs("10.0.0.1", 70000, impl.ProtoTCP, "", t0()),
		openObs("10.0.0.1", 443, "sctp", "", t0()),
		{Addr: "10.0.0.1", Port: 443, Proto: impl.ProtoTCP, State: "half", Seen: t0()},
		{Addr: "10.0.0.1", Port: 443, Proto: impl.ProtoTCP, State: impl.StateOpen},
	}
	for i, obs := range bad {
		if _, err := l.Observe(obs); !errors.Is(err, impl.ErrBadObservation) {
			t.Fatalf("bad observation %d must fail with ErrBadObservation, got %v", i, err)
		}
	}
	if got := len(l.Items()); got != 0 {
		t.Fatalf("rejected observations stored %d items, want 0", got)
	}
}

func TestAttributeOverridesInheritedOwner(t *testing.T) {
	l := newLedger(t, time.Hour, 64)
	if err := l.Inherit("10.0.0.0/8", "netops"); err != nil {
		t.Fatalf("inherit: %v", err)
	}
	id, err := l.Observe(openObs("10.1.2.3", 443, impl.ProtoTCP, "CN=api", t0()))
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if err := l.Attribute(id, "appsec"); err != nil {
		t.Fatalf("attribute: %v", err)
	}
	item, _ := l.Get(id)
	if item.Owner != "appsec" || item.OwnerSource != impl.OwnerManual {
		t.Fatalf("owner = %q (%q), want the manual appsec owner", item.Owner, item.OwnerSource)
	}
	change, ok := lastChange(l.Changes())
	if !ok || change.Kind != impl.ChangeOwner {
		t.Fatalf("last change = %+v, want an owner_changed event", change)
	}
	if change.From != "netops" || change.To != "appsec" {
		t.Fatalf("owner change = %q -> %q, want netops -> appsec", change.From, change.To)
	}
	if err := l.Attribute(id, "   "); !errors.Is(err, impl.ErrBlankOwner) {
		t.Fatalf("blank owner must fail with ErrBlankOwner, got %v", err)
	}
	if err := l.Attribute("exp-9999", "appsec"); !errors.Is(err, impl.ErrNotFound) {
		t.Fatalf("attributing an unknown item must fail with ErrNotFound, got %v", err)
	}
	after, _ := l.Get(id)
	if after.Owner != "appsec" {
		t.Fatalf("failed attribute calls changed the owner to %q", after.Owner)
	}
}

func TestInheritUsesLongestPrefix(t *testing.T) {
	l := newLedger(t, time.Hour, 64)
	if err := l.Inherit("10.0.0.0/8", "netops"); err != nil {
		t.Fatalf("inherit wide: %v", err)
	}
	if err := l.Inherit("10.1.0.0/16", "appsec"); err != nil {
		t.Fatalf("inherit narrow: %v", err)
	}
	wide, err := l.Observe(openObs("10.2.3.4", 80, impl.ProtoTCP, "", t0()))
	if err != nil {
		t.Fatalf("observe wide: %v", err)
	}
	narrow, err := l.Observe(openObs("10.1.2.3", 443, impl.ProtoTCP, "", t0()))
	if err != nil {
		t.Fatalf("observe narrow: %v", err)
	}
	outside, err := l.Observe(openObs("192.168.1.5", 22, impl.ProtoTCP, "", t0()))
	if err != nil {
		t.Fatalf("observe outside: %v", err)
	}
	if item, _ := l.Get(wide); item.Owner != "netops" {
		t.Fatalf("10.2.3.4 owner = %q, want netops", item.Owner)
	}
	if item, _ := l.Get(narrow); item.Owner != "appsec" {
		t.Fatalf("10.1.2.3 owner = %q, want the longer prefix appsec", item.Owner)
	}
	if item, _ := l.Get(outside); item.Owner != "" || item.OwnerSource != "" {
		t.Fatalf("unmatched address owner = %q (%q), want empty", item.Owner, item.OwnerSource)
	}
	if err := l.Inherit("not-a-cidr", "netops"); !errors.Is(err, impl.ErrBadNetblock) {
		t.Fatalf("bad netblock must fail with ErrBadNetblock, got %v", err)
	}
	if err := l.Inherit("10.0.0.0/8", "  "); !errors.Is(err, impl.ErrBlankOwner) {
		t.Fatalf("blank inherited owner must fail with ErrBlankOwner, got %v", err)
	}
	if item, _ := l.Get(wide); item.Owner != "netops" {
		t.Fatalf("rejected inherit changed the wide prefix owner to %q", item.Owner)
	}
}

func TestObserveKeepsIdentityAndMonotonicWindow(t *testing.T) {
	l := newLedger(t, time.Hour, 64)
	first, err := l.Observe(openObs("10.1.2.3", 443, impl.ProtoTCP, "CN=api", t0().Add(30*time.Minute)))
	if err != nil {
		t.Fatalf("first observe: %v", err)
	}
	second, err := l.Observe(openObs("10.1.2.3", 443, impl.ProtoTCP, "CN=api", t0().Add(10*time.Minute)))
	if err != nil {
		t.Fatalf("second observe: %v", err)
	}
	if second != first {
		t.Fatalf("same identity returned %q and %q, want one item id", first, second)
	}
	item, _ := l.Get(first)
	if item.Evidence != 2 {
		t.Fatalf("evidence = %d, want 2", item.Evidence)
	}
	if !item.FirstSeen.Equal(t0().Add(10 * time.Minute)) {
		t.Fatalf("first seen = %v, want the earlier observation %v", item.FirstSeen, t0().Add(10*time.Minute))
	}
	if !item.LastSeen.Equal(t0().Add(30 * time.Minute)) {
		t.Fatalf("last seen = %v, want the later observation %v", item.LastSeen, t0().Add(30*time.Minute))
	}
	third, err := l.Observe(openObs("10.1.2.3", 443, impl.ProtoTCP, "CN=api", t0().Add(45*time.Minute)))
	if err != nil {
		t.Fatalf("third observe: %v", err)
	}
	if third != first {
		t.Fatalf("third observation created %q, want %q", third, first)
	}
	item, _ = l.Get(first)
	if !item.LastSeen.Equal(t0().Add(45 * time.Minute)) || item.Evidence != 3 {
		t.Fatalf("item after third observe = %+v, want last seen 45m and evidence 3", item)
	}
	if got := len(l.Items()); got != 1 {
		t.Fatalf("item count = %d, want 1", got)
	}
}

func TestObserveDifferentPortIsDifferentItem(t *testing.T) {
	l := newLedger(t, time.Hour, 64)
	first, err := l.Observe(openObs("10.1.2.3", 443, impl.ProtoTCP, "CN=api", t0()))
	if err != nil {
		t.Fatalf("first observe: %v", err)
	}
	second, err := l.Observe(openObs("10.1.2.3", 8443, impl.ProtoTCP, "CN=api", t0()))
	if err != nil {
		t.Fatalf("second observe: %v", err)
	}
	if first == second {
		t.Fatalf("different ports collapsed into one item %q", first)
	}
	items := l.Items()
	if len(items) != 2 {
		t.Fatalf("items = %+v, want two entries", items)
	}
	if items[0].ID != first || items[1].ID != second {
		t.Fatalf("items ordered %q,%q want ascending ids %q,%q", items[0].ID, items[1].ID, first, second)
	}
	if items[0].Port != 443 || items[1].Port != 8443 {
		t.Fatalf("ports = %d,%d want 443,8443", items[0].Port, items[1].Port)
	}
}

func TestSweepDowngradesStaleItems(t *testing.T) {
	l := newLedger(t, time.Hour, 64)
	stale, err := l.Observe(openObs("10.1.1.1", 443, impl.ProtoTCP, "", t0()))
	if err != nil {
		t.Fatalf("observe stale: %v", err)
	}
	fresh, err := l.Observe(openObs("10.1.1.2", 443, impl.ProtoTCP, "", t0().Add(90*time.Minute)))
	if err != nil {
		t.Fatalf("observe fresh: %v", err)
	}
	changes, err := l.Sweep(t0().Add(2 * time.Hour))
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(changes) != 1 || changes[0].ItemID != stale || changes[0].Kind != impl.ChangeDowngraded {
		t.Fatalf("sweep changes = %+v, want one downgrade for %q", changes, stale)
	}
	if item, _ := l.Get(stale); item.State != impl.StateHistorical {
		t.Fatalf("stale state = %q, want %q", item.State, impl.StateHistorical)
	}
	if item, _ := l.Get(fresh); item.State != impl.StateOpen {
		t.Fatalf("fresh state = %q, want %q", item.State, impl.StateOpen)
	}
	if stats := l.Stats(); stats.Live != 1 || stats.Historical != 1 {
		t.Fatalf("stats = %+v, want one live and one historical item", stats)
	}
	again, err := l.Sweep(t0().Add(2*time.Hour + time.Minute))
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("second sweep produced %+v, want no duplicate downgrade", again)
	}
	if _, err := l.Sweep(time.Time{}); !errors.Is(err, impl.ErrBadSweep) {
		t.Fatalf("zero sweep instant must fail with ErrBadSweep, got %v", err)
	}
}

func TestHistoricalItemsLeaveTheLiveList(t *testing.T) {
	l := newLedger(t, time.Hour, 64)
	stale, err := l.Observe(openObs("10.1.1.1", 443, impl.ProtoTCP, "", t0()))
	if err != nil {
		t.Fatalf("observe stale: %v", err)
	}
	fresh, err := l.Observe(openObs("10.1.1.2", 443, impl.ProtoTCP, "", t0().Add(90*time.Minute)))
	if err != nil {
		t.Fatalf("observe fresh: %v", err)
	}
	if _, err := l.Sweep(t0().Add(2 * time.Hour)); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	items := l.Items()
	if len(items) != 1 || items[0].ID != fresh {
		t.Fatalf("live items = %+v, want only %q", items, fresh)
	}
	item, ok := l.Get(stale)
	if !ok {
		t.Fatalf("historical item must stay readable")
	}
	if item.State != impl.StateHistorical {
		t.Fatalf("historical state = %q", item.State)
	}
	if stats := l.Stats(); stats.Live != 1 || stats.Historical != 1 {
		t.Fatalf("stats = %+v, want 1 live and 1 historical", stats)
	}
}

func TestMergeKeepsEarliestAndLatestSeen(t *testing.T) {
	l := newLedger(t, time.Hour, 64)
	primary, err := l.Observe(openObs("10.1.1.1", 443, impl.ProtoTCP, "CN=a", t0()))
	if err != nil {
		t.Fatalf("observe primary: %v", err)
	}
	duplicate, err := l.Observe(openObs("10.1.1.1", 8443, impl.ProtoTCP, "CN=b", t0().Add(2*time.Hour)))
	if err != nil {
		t.Fatalf("observe duplicate: %v", err)
	}
	if err := l.Merge([]string{primary, duplicate}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	item, ok := l.Get(primary)
	if !ok {
		t.Fatalf("merged item must stay readable under the oldest id %q", primary)
	}
	if !item.FirstSeen.Equal(t0()) || !item.LastSeen.Equal(t0().Add(2*time.Hour)) {
		t.Fatalf("merged window = %v..%v, want %v..%v", item.FirstSeen, item.LastSeen, t0(), t0().Add(2*time.Hour))
	}
	if item.Evidence != 2 {
		t.Fatalf("merged evidence = %d, want 2", item.Evidence)
	}
	if item.Port != 443 {
		t.Fatalf("merged port = %d, want the primary port 443", item.Port)
	}
	if len(item.MergedFrom) != 1 || item.MergedFrom[0] != duplicate {
		t.Fatalf("merged from = %v, want [%q]", item.MergedFrom, duplicate)
	}
	if _, ok := l.Get(duplicate); ok {
		t.Fatalf("absorbed item %q must leave the inventory", duplicate)
	}
	change, ok := lastChange(l.Changes())
	if !ok || change.Kind != impl.ChangeMerged || change.ItemID != primary {
		t.Fatalf("last change = %+v, want a merged event for %q", change, primary)
	}
	if stats := l.Stats(); stats.Merged != 1 || stats.Live != 1 {
		t.Fatalf("stats = %+v, want 1 merged and 1 live item", stats)
	}
}

func TestMergeValidationKeepsInventory(t *testing.T) {
	l := newLedger(t, time.Hour, 64)
	first, err := l.Observe(openObs("10.1.1.1", 443, impl.ProtoTCP, "", t0()))
	if err != nil {
		t.Fatalf("observe first: %v", err)
	}
	second, err := l.Observe(openObs("10.1.1.1", 8443, impl.ProtoTCP, "", t0()))
	if err != nil {
		t.Fatalf("observe second: %v", err)
	}
	third, err := l.Observe(openObs("10.1.1.2", 443, impl.ProtoTCP, "", t0()))
	if err != nil {
		t.Fatalf("observe third: %v", err)
	}
	if err := l.Merge([]string{first}); !errors.Is(err, impl.ErrBadMerge) {
		t.Fatalf("single id merge must fail with ErrBadMerge, got %v", err)
	}
	if err := l.Merge([]string{first, "exp-9999"}); !errors.Is(err, impl.ErrNotFound) {
		t.Fatalf("unknown id merge must fail with ErrNotFound, got %v", err)
	}
	if err := l.Merge([]string{first, third}); !errors.Is(err, impl.ErrMergeMismatch) {
		t.Fatalf("different address merge must fail with ErrMergeMismatch, got %v", err)
	}
	if got := len(l.Items()); got != 3 {
		t.Fatalf("failed merges changed the inventory to %d items", got)
	}
	if err := l.Merge([]string{first, second}); err != nil {
		t.Fatalf("valid merge: %v", err)
	}
}

func TestChangesAppendInOrder(t *testing.T) {
	l := newLedger(t, time.Hour, 64)
	id, err := l.Observe(openObs("10.1.1.1", 443, impl.ProtoTCP, "", t0()))
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if err := l.Attribute(id, "appsec"); err != nil {
		t.Fatalf("attribute: %v", err)
	}
	if _, err := l.Sweep(t0().Add(2 * time.Hour)); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	changes := l.Changes()
	if len(changes) != 3 {
		t.Fatalf("changes = %+v, want three events", changes)
	}
	want := []string{impl.ChangeNew, impl.ChangeOwner, impl.ChangeDowngraded}
	for i, kind := range want {
		if changes[i].Kind != kind {
			t.Fatalf("change %d kind = %q, want %q", i, changes[i].Kind, kind)
		}
		if changes[i].ItemID != id {
			t.Fatalf("change %d item = %q, want %q", i, changes[i].ItemID, id)
		}
	}
	for i := 1; i < len(changes); i++ {
		if changes[i].Seq <= changes[i-1].Seq {
			t.Fatalf("change sequence must strictly increase, got %d then %d", changes[i-1].Seq, changes[i].Seq)
		}
	}
}

func TestSnapshotRestoreKeepsIdentityAndOwners(t *testing.T) {
	limits := impl.Limits{TTL: time.Hour, MaxItems: 64}
	source := impl.NewExposureLedger(limits)
	if err := source.Inherit("10.0.0.0/8", "netops"); err != nil {
		t.Fatalf("inherit: %v", err)
	}
	id, err := source.Observe(openObs("10.1.2.3", 443, impl.ProtoTCP, "CN=api", t0()))
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if err := source.Attribute(id, "appsec"); err != nil {
		t.Fatalf("attribute: %v", err)
	}
	data, err := source.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewExposureLedger(limits)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	again, err := restored.Observe(openObs("10.1.2.3", 443, impl.ProtoTCP, "CN=api", t0().Add(10*time.Minute)))
	if err != nil {
		t.Fatalf("observe after restore: %v", err)
	}
	if again != id {
		t.Fatalf("item id after restore = %q, want the original %q", again, id)
	}
	item, _ := restored.Get(id)
	if item.Evidence != 2 {
		t.Fatalf("evidence after restore = %d, want 2", item.Evidence)
	}
	if item.Owner != "appsec" || item.OwnerSource != impl.OwnerManual {
		t.Fatalf("owner after restore = %q (%q), want manual appsec", item.Owner, item.OwnerSource)
	}
	inherited, err := restored.Observe(openObs("10.9.9.9", 443, impl.ProtoTCP, "", t0()))
	if err != nil {
		t.Fatalf("observe inherited after restore: %v", err)
	}
	if other, _ := restored.Get(inherited); other.Owner != "netops" {
		t.Fatalf("inheritance table lost after restore, owner = %q", other.Owner)
	}
	if stats := restored.Stats(); stats.Live != 2 {
		t.Fatalf("live items after restore = %d, want 2", stats.Live)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	l := newLedger(t, time.Hour, 64)
	id, err := l.Observe(openObs("10.1.2.3", 443, impl.ProtoTCP, "", t0()))
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if err := l.Restore([]byte("<<not a snapshot>>")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if err := l.Restore(nil); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("empty snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	item, ok := l.Get(id)
	if !ok || item.Evidence != 1 {
		t.Fatalf("failed restore touched the live item, got %+v ok=%v", item, ok)
	}
	if got := len(l.Changes()); got != 1 {
		t.Fatalf("failed restore changed the audit log to %d events", got)
	}
	if _, err := l.Observe(openObs("10.1.2.4", 443, impl.ProtoTCP, "", t0())); err != nil {
		t.Fatalf("observe after rejected restore: %v", err)
	}
}

func TestConcurrentObserveSameIdentity(t *testing.T) {
	l := newLedger(t, time.Hour, 64)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ids := make(map[string]struct{})
	failures := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id, err := l.Observe(openObs("10.1.2.3", 443, impl.ProtoTCP, "CN=api", t0().Add(time.Duration(i)*time.Minute)))
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failures++
				return
			}
			ids[id] = struct{}{}
		}(i)
	}
	wg.Wait()
	if failures != 0 {
		t.Fatalf("%d concurrent observations failed", failures)
	}
	if len(ids) != 1 {
		t.Fatalf("concurrent observations produced %d item ids, want 1", len(ids))
	}
	items := l.Items()
	if len(items) != 1 || items[0].Evidence != 8 {
		t.Fatalf("items = %+v, want one item with 8 evidence rows", items)
	}
}

func TestConcurrentObserveRespectsItemLimit(t *testing.T) {
	l := newLedger(t, time.Hour, 8)
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted, limited := 0, 0
	ids := make(map[string]struct{})
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id, err := l.Observe(openObs(fmt.Sprintf("10.1.0.%d", i), 443, impl.ProtoTCP, "", t0()))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				accepted++
				ids[id] = struct{}{}
			case errors.Is(err, impl.ErrItemLimit):
				limited++
			}
		}(i)
	}
	wg.Wait()
	if accepted != 8 {
		t.Fatalf("concurrent observations stored %d items, want exactly the cap 8", accepted)
	}
	if limited != 8 {
		t.Fatalf("concurrent observations reported %d limit errors, want 8", limited)
	}
	if len(ids) != 8 {
		t.Fatalf("accepted observations reused %d distinct ids among 8 items", len(ids))
	}
	if got := len(l.Items()); got != 8 {
		t.Fatalf("item count after the race = %d, want 8", got)
	}
}
