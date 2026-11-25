package task

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	impl "gsb/batch49-go-game-c-11"
)

func mustCreateBus(t *testing.T, r *impl.BusSoloRouter, parent, name string) impl.BusID {
	t.Helper()
	id, err := r.CreateBus(parent, name)
	if err != nil {
		t.Fatalf("CreateBus(%q, %q) failed: %v", parent, name, err)
	}
	return id
}

func mustAudible(t *testing.T, r *impl.BusSoloRouter, id impl.BusID) bool {
	t.Helper()
	v, err := r.Audible(string(id))
	if err != nil {
		t.Fatalf("Audible(%q) failed: %v", id, err)
	}
	return v
}

func mustLatency(t *testing.T, r *impl.BusSoloRouter, id string) int {
	t.Helper()
	v, err := r.Latency(id)
	if err != nil {
		t.Fatalf("Latency(%q) failed: %v", id, err)
	}
	return v
}

func TestNewRouterOwnsRootBus(t *testing.T) {
	r := impl.NewBusSoloRouter(3)
	if got := r.Count(); got != 1 {
		t.Fatalf("bus count = %d, want exactly the root bus", got)
	}
	info, ok := r.Bus(string(impl.RootBusID))
	if !ok {
		t.Fatalf("root bus %q must exist", impl.RootBusID)
	}
	if info.Name != "master" || info.Depth != 0 || info.Parent != "" {
		t.Fatalf("root record = %+v, want master at depth 0 without a parent", info)
	}
	path, err := r.Path(string(impl.RootBusID))
	if err != nil {
		t.Fatalf("Path(root) failed: %v", err)
	}
	if len(path) != 1 || path[0] != "master" {
		t.Fatalf("root path = %v, want [master]", path)
	}
	if !mustAudible(t, r, impl.RootBusID) {
		t.Fatalf("root bus must be audible on a fresh router")
	}
	if got := mustLatency(t, r, string(impl.RootBusID)); got != 0 {
		t.Fatalf("root latency = %d, want 0 on a fresh router", got)
	}
}

func TestCreateBusLinksTree(t *testing.T) {
	r := impl.NewBusSoloRouter(3)
	drums := mustCreateBus(t, r, string(impl.RootBusID), "drums")
	vocals := mustCreateBus(t, r, string(impl.RootBusID), "vocals")
	if drums == vocals {
		t.Fatalf("two buses share the identifier %q", drums)
	}
	if got := r.Count(); got != 3 {
		t.Fatalf("bus count = %d, want 3 after two creates", got)
	}
	kids, err := r.Children(string(impl.RootBusID))
	if err != nil {
		t.Fatalf("Children(root) failed: %v", err)
	}
	if len(kids) != 2 || kids[0] != drums || kids[1] != vocals {
		t.Fatalf("children = %v, want [%q %q] in creation order", kids, drums, vocals)
	}
	path, err := r.Path(string(drums))
	if err != nil {
		t.Fatalf("Path(drums) failed: %v", err)
	}
	if len(path) != 2 || path[0] != "master" || path[1] != "drums" {
		t.Fatalf("drums path = %v, want [master drums]", path)
	}
	info, ok := r.Bus(string(drums))
	if !ok || info.Parent != impl.RootBusID || info.Depth != 1 {
		t.Fatalf("drums record = %+v ok=%v, want parent root at depth 1", info, ok)
	}
}

func TestCreateBusRejectsBadInput(t *testing.T) {
	r := impl.NewBusSoloRouter(3)
	if _, err := r.CreateBus(string(impl.RootBusID), "   "); !errors.Is(err, impl.ErrBadBusName) {
		t.Fatalf("blank name must fail with ErrBadBusName, got %v", err)
	}
	if _, err := r.CreateBus("bus-404", "orphan"); !errors.Is(err, impl.ErrBusNotFound) {
		t.Fatalf("unknown parent must fail with ErrBusNotFound, got %v", err)
	}
	first := mustCreateBus(t, r, string(impl.RootBusID), "drums")
	if _, err := r.CreateBus(string(impl.RootBusID), "drums"); !errors.Is(err, impl.ErrDuplicateBusName) {
		t.Fatalf("duplicate sibling name must fail with ErrDuplicateBusName, got %v", err)
	}
	bass := mustCreateBus(t, r, string(impl.RootBusID), "  bass  ")
	info, ok := r.Bus(string(bass))
	if !ok || info.Name != "bass" {
		t.Fatalf("bus record = %+v ok=%v, want the trimmed name bass", info, ok)
	}
	if got := r.Count(); got != 3 {
		t.Fatalf("bus count = %d, want 3 after the rejected creates", got)
	}
	if info, _ := r.Bus(string(first)); info.Name != "drums" {
		t.Fatalf("first bus name = %q, want drums", info.Name)
	}
}

func TestCreateBusEnforcesDepthLimit(t *testing.T) {
	r := impl.NewBusSoloRouter(2)
	a := mustCreateBus(t, r, string(impl.RootBusID), "a")
	b := mustCreateBus(t, r, string(a), "b")
	if _, err := r.CreateBus(string(b), "c"); !errors.Is(err, impl.ErrDepthExceeded) {
		t.Fatalf("third level must fail with ErrDepthExceeded, got %v", err)
	}
	if got := r.Count(); got != 3 {
		t.Fatalf("bus count = %d, want 3 when the third level was rejected", got)
	}
	deep := impl.NewBusSoloRouter(1)
	if _, err := deep.CreateBus(string(impl.RootBusID), "child"); err != nil {
		t.Fatalf("depth limit 1 must still allow one child level, got %v", err)
	}
	if _, err := deep.CreateBus("bus-1", "grandchild"); !errors.Is(err, impl.ErrDepthExceeded) {
		t.Fatalf("depth limit 1 must reject the second level, got %v", err)
	}
}

func TestMuteInheritsDownTheSubtree(t *testing.T) {
	r := impl.NewBusSoloRouter(3)
	drums := mustCreateBus(t, r, string(impl.RootBusID), "drums")
	vocals := mustCreateBus(t, r, string(impl.RootBusID), "vocals")
	snare := mustCreateBus(t, r, string(drums), "snare")
	kick := mustCreateBus(t, r, string(drums), "kick")
	if err := r.SetMute(string(drums), true); err != nil {
		t.Fatalf("SetMute(drums) failed: %v", err)
	}
	for _, id := range []impl.BusID{drums, snare, kick} {
		if mustAudible(t, r, id) {
			t.Fatalf("bus %q must be silenced by the muted ancestor", id)
		}
	}
	if !mustAudible(t, r, vocals) {
		t.Fatalf("sibling vocals must stay audible when drums is muted")
	}
	if muted, err := r.Muted(string(snare)); err != nil || muted {
		t.Fatalf("snare own mute flag = %v err=%v, want false because inheritance does not rewrite it", muted, err)
	}
	if err := r.SetMute(string(drums), false); err != nil {
		t.Fatalf("unmuting drums failed: %v", err)
	}
	for _, id := range []impl.BusID{drums, snare, kick, vocals} {
		if !mustAudible(t, r, id) {
			t.Fatalf("bus %q must be audible again after the unmute", id)
		}
	}
}

func TestSoloSilencesSiblings(t *testing.T) {
	r := impl.NewBusSoloRouter(3)
	drums := mustCreateBus(t, r, string(impl.RootBusID), "drums")
	vocals := mustCreateBus(t, r, string(impl.RootBusID), "vocals")
	snare := mustCreateBus(t, r, string(drums), "snare")
	if err := r.SetSolo(string(drums), true); err != nil {
		t.Fatalf("SetSolo(drums) failed: %v", err)
	}
	if !mustAudible(t, r, drums) {
		t.Fatalf("the soloed bus itself must stay audible")
	}
	if !mustAudible(t, r, snare) {
		t.Fatalf("a child of the soloed bus must stay audible")
	}
	if mustAudible(t, r, vocals) {
		t.Fatalf("the sibling vocals must be silenced by the solo")
	}
	if err := r.SetSolo(string(drums), false); err != nil {
		t.Fatalf("clearing solo failed: %v", err)
	}
	if !mustAudible(t, r, vocals) {
		t.Fatalf("vocals must be audible again after the solo is cleared")
	}
}

func TestSoloIsScopedToItsOwnLayer(t *testing.T) {
	r := impl.NewBusSoloRouter(3)
	drums := mustCreateBus(t, r, string(impl.RootBusID), "drums")
	vocals := mustCreateBus(t, r, string(impl.RootBusID), "vocals")
	snare := mustCreateBus(t, r, string(drums), "snare")
	kick := mustCreateBus(t, r, string(drums), "kick")
	if err := r.SetSolo(string(snare), true); err != nil {
		t.Fatalf("SetSolo(snare) failed: %v", err)
	}
	if mustAudible(t, r, kick) {
		t.Fatalf("kick shares the parent of the soloed snare and must be silenced")
	}
	if !mustAudible(t, r, drums) {
		t.Fatalf("drums is the parent of the soloed bus and must stay audible")
	}
	if !mustAudible(t, r, vocals) {
		t.Fatalf("vocals lives in another layer and must not be affected by the snare solo")
	}
}

func TestBypassValidationAndFlag(t *testing.T) {
	r := impl.NewBusSoloRouter(3)
	fx := mustCreateBus(t, r, string(impl.RootBusID), "fx")
	if err := r.SetBypass(string(impl.RootBusID), true); !errors.Is(err, impl.ErrRootBus) {
		t.Fatalf("bypassing the root must fail with ErrRootBus, got %v", err)
	}
	if err := r.SetBypass("bus-404", true); !errors.Is(err, impl.ErrBusNotFound) {
		t.Fatalf("bypassing an unknown bus must fail with ErrBusNotFound, got %v", err)
	}
	before := mustLatency(t, r, string(fx))
	if err := r.SetBypass(string(fx), true); err != nil {
		t.Fatalf("SetBypass(fx) failed: %v", err)
	}
	info, ok := r.Bus(string(fx))
	if !ok || !info.Bypass {
		t.Fatalf("fx record = %+v ok=%v, want the bypass flag stored", info, ok)
	}
	if got := mustLatency(t, r, string(fx)); got != before {
		t.Fatalf("latency of a zero latency bus changed from %d to %d after the bypass", before, got)
	}
	if err := r.SetBypass(string(fx), false); err != nil {
		t.Fatalf("clearing bypass failed: %v", err)
	}
}

func TestLatencyAlignsToLongestPath(t *testing.T) {
	r := impl.NewBusSoloRouter(4)
	if err := r.SetLatency(string(impl.RootBusID), 10); err != nil {
		t.Fatalf("SetLatency(root) failed: %v", err)
	}
	a := mustCreateBus(t, r, string(impl.RootBusID), "a")
	if err := r.SetLatency(string(a), 40); err != nil {
		t.Fatalf("SetLatency(a) failed: %v", err)
	}
	b := mustCreateBus(t, r, string(a), "b")
	c := mustCreateBus(t, r, string(impl.RootBusID), "c")
	cases := []struct {
		id   string
		want int
	}{
		{string(impl.RootBusID), 40},
		{string(a), 0},
		{string(b), 0},
		{string(c), 40},
	}
	for _, tc := range cases {
		if got := mustLatency(t, r, tc.id); got != tc.want {
			t.Fatalf("Latency(%q) = %d, want %d", tc.id, got, tc.want)
		}
	}
	if err := r.SetLatency(string(a), 20); err != nil {
		t.Fatalf("SetLatency(a, 20) failed: %v", err)
	}
	for _, tc := range []struct {
		id   string
		want int
	}{
		{string(impl.RootBusID), 20},
		{string(a), 0},
		{string(b), 0},
		{string(c), 20},
	} {
		if got := mustLatency(t, r, tc.id); got != tc.want {
			t.Fatalf("after shrinking a: Latency(%q) = %d, want %d", tc.id, got, tc.want)
		}
	}
	if err := r.SetLatency(string(a), -1); !errors.Is(err, impl.ErrLatencyRange) {
		t.Fatalf("negative latency must fail with ErrLatencyRange, got %v", err)
	}
	if err := r.SetLatency(string(a), impl.MaxOwnLatencyMs+1); !errors.Is(err, impl.ErrLatencyRange) {
		t.Fatalf("latency above the cap must fail with ErrLatencyRange, got %v", err)
	}
	if got := mustLatency(t, r, string(a)); got != 0 {
		t.Fatalf("rejected latency writes must not change the compensation, got %d", got)
	}
}

func TestDeleteLeafBus(t *testing.T) {
	r := impl.NewBusSoloRouter(3)
	drums := mustCreateBus(t, r, string(impl.RootBusID), "drums")
	snare := mustCreateBus(t, r, string(drums), "snare")
	if err := r.DeleteBus(string(snare), impl.DeleteReject); err != nil {
		t.Fatalf("deleting a leaf with the reject policy must succeed, got %v", err)
	}
	if got := r.Count(); got != 2 {
		t.Fatalf("bus count = %d, want 2 after the leaf is removed", got)
	}
	kids, err := r.Children(string(drums))
	if err != nil {
		t.Fatalf("Children(drums) failed: %v", err)
	}
	if len(kids) != 0 {
		t.Fatalf("drums still lists children %v after the leaf delete", kids)
	}
	if _, err := r.Audible(string(snare)); !errors.Is(err, impl.ErrBusNotFound) {
		t.Fatalf("Audible on a deleted bus must fail with ErrBusNotFound, got %v", err)
	}
	if _, err := r.Path(string(snare)); !errors.Is(err, impl.ErrBusNotFound) {
		t.Fatalf("Path on a deleted bus must fail with ErrBusNotFound, got %v", err)
	}
}

func TestDeleteRejectsRootAndUnknown(t *testing.T) {
	r := impl.NewBusSoloRouter(3)
	drums := mustCreateBus(t, r, string(impl.RootBusID), "drums")
	if err := r.DeleteBus(string(impl.RootBusID), impl.DeleteCascade); !errors.Is(err, impl.ErrRootBus) {
		t.Fatalf("deleting the root must fail with ErrRootBus, got %v", err)
	}
	if err := r.DeleteBus("bus-404", impl.DeleteReject); !errors.Is(err, impl.ErrBusNotFound) {
		t.Fatalf("deleting an unknown bus must fail with ErrBusNotFound, got %v", err)
	}
	if got := r.Count(); got != 2 {
		t.Fatalf("bus count = %d, want 2 because both deletes must be refused", got)
	}
	if !mustAudible(t, r, drums) {
		t.Fatalf("the refused deletes must leave the tree untouched")
	}
}

func TestUnknownBusLookupsFail(t *testing.T) {
	r := impl.NewBusSoloRouter(3)
	if _, err := r.Audible("bus-404"); !errors.Is(err, impl.ErrBusNotFound) {
		t.Fatalf("Audible(unknown) must fail with ErrBusNotFound, got %v", err)
	}
	if _, err := r.Latency(""); !errors.Is(err, impl.ErrBusNotFound) {
		t.Fatalf("Latency(empty) must fail with ErrBusNotFound, got %v", err)
	}
	if err := r.SetMute("bus-404", true); !errors.Is(err, impl.ErrBusNotFound) {
		t.Fatalf("SetMute(unknown) must fail with ErrBusNotFound, got %v", err)
	}
	if err := r.SetSolo("bus-404", true); !errors.Is(err, impl.ErrBusNotFound) {
		t.Fatalf("SetSolo(unknown) must fail with ErrBusNotFound, got %v", err)
	}
	if _, err := r.Muted("bus-404"); !errors.Is(err, impl.ErrBusNotFound) {
		t.Fatalf("Muted(unknown) must fail with ErrBusNotFound, got %v", err)
	}
	if _, err := r.Children("bus-404"); !errors.Is(err, impl.ErrBusNotFound) {
		t.Fatalf("Children(unknown) must fail with ErrBusNotFound, got %v", err)
	}
}

func TestSnapshotRestorePreservesTree(t *testing.T) {
	r := impl.NewBusSoloRouter(3)
	if err := r.SetLatency(string(impl.RootBusID), 10); err != nil {
		t.Fatalf("SetLatency(root) failed: %v", err)
	}
	drums := mustCreateBus(t, r, string(impl.RootBusID), "drums")
	vocals := mustCreateBus(t, r, string(impl.RootBusID), "vocals")
	snare := mustCreateBus(t, r, string(drums), "snare")
	fx := mustCreateBus(t, r, string(vocals), "fx")
	if err := r.SetMute(string(drums), true); err != nil {
		t.Fatalf("SetMute(drums) failed: %v", err)
	}
	if err := r.SetSolo(string(vocals), true); err != nil {
		t.Fatalf("SetSolo(vocals) failed: %v", err)
	}
	if err := r.SetLatency(string(snare), 20); err != nil {
		t.Fatalf("SetLatency(snare) failed: %v", err)
	}
	if err := r.SetBypass(string(fx), true); err != nil {
		t.Fatalf("SetBypass(fx) failed: %v", err)
	}
	data, err := r.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewBusSoloRouter(1)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("Restore failed: %v", err)
	}
	if got := restored.Count(); got != 5 {
		t.Fatalf("restored bus count = %d, want 5", got)
	}
	if mustAudible(t, restored, drums) {
		t.Fatalf("restored drums must stay muted")
	}
	if mustAudible(t, restored, snare) {
		t.Fatalf("restored snare must stay silenced through its muted ancestor")
	}
	if !mustAudible(t, restored, vocals) {
		t.Fatalf("restored vocals must stay audible next to the muted branch")
	}
	info, ok := restored.Bus(string(fx))
	if !ok || !info.Bypass {
		t.Fatalf("restored fx record = %+v ok=%v, want the bypass flag kept", info, ok)
	}
	if got := mustLatency(t, restored, string(drums)); got != 20 {
		t.Fatalf("restored drums compensation = %d, want 20", got)
	}
	fresh := mustCreateBus(t, restored, string(impl.RootBusID), "keys")
	for _, old := range []impl.BusID{drums, vocals, snare, fx} {
		if fresh == old {
			t.Fatalf("bus identifier %q reused after restore", fresh)
		}
	}
	if got := restored.Count(); got != 6 {
		t.Fatalf("bus count after restore and create = %d, want 6", got)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	r := impl.NewBusSoloRouter(3)
	drums := mustCreateBus(t, r, string(impl.RootBusID), "drums")
	if err := r.SetMute(string(drums), true); err != nil {
		t.Fatalf("SetMute(drums) failed: %v", err)
	}
	if err := r.Restore([]byte("{not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if mustAudible(t, r, drums) {
		t.Fatalf("a rejected restore must not touch the live tree")
	}
	if got := r.Count(); got != 2 {
		t.Fatalf("bus count after the rejected restore = %d, want 2", got)
	}
	if _, err := r.CreateBus(string(drums), "snare"); err != nil {
		t.Fatalf("the live tree must keep working after a rejected restore, got %v", err)
	}
}

func TestConcurrentSwitchesAndSnapshot(t *testing.T) {
	r := impl.NewBusSoloRouter(3)
	ids := make([]impl.BusID, 8)
	for i := range ids {
		ids[i] = mustCreateBus(t, r, string(impl.RootBusID), fmt.Sprintf("bus-%02d", i))
	}
	var failures int64
	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := string(ids[i])
			for k := 0; k < 200; k++ {
				if err := r.SetMute(id, k%2 == 0); err != nil {
					atomic.AddInt64(&failures, 1)
					return
				}
				if err := r.SetSolo(id, k%3 == 0); err != nil {
					atomic.AddInt64(&failures, 1)
					return
				}
				if _, err := r.Audible(id); err != nil {
					atomic.AddInt64(&failures, 1)
					return
				}
				if _, err := r.Latency(id); err != nil {
					atomic.AddInt64(&failures, 1)
					return
				}
				if k%25 == 0 {
					data, err := r.Snapshot()
					if err != nil {
						atomic.AddInt64(&failures, 1)
						return
					}
					probe := impl.NewBusSoloRouter(3)
					if err := probe.Restore(data); err != nil {
						atomic.AddInt64(&failures, 1)
						return
					}
					if got := probe.Count(); got != 9 {
						atomic.AddInt64(&failures, 1)
						return
					}
				}
			}
		}(i)
	}
	wg.Wait()
	if failures != 0 {
		t.Fatalf("%d concurrent workers observed a failing switch, read or snapshot", failures)
	}
	for _, id := range ids {
		if err := r.SetMute(string(id), false); err != nil {
			t.Fatalf("final SetMute(%q) failed: %v", id, err)
		}
		if err := r.SetSolo(string(id), false); err != nil {
			t.Fatalf("final SetSolo(%q) failed: %v", id, err)
		}
	}
	for _, id := range ids {
		if !mustAudible(t, r, id) {
			t.Fatalf("bus %q must be audible after all switches are cleared", id)
		}
	}
	if got := r.Count(); got != 9 {
		t.Fatalf("bus count = %d, want 9 after the concurrent run", got)
	}
}
