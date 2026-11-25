package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch49-go-safety-d-15"
)

func baseSpec() impl.NsSpec {
	return impl.NsSpec{Mode: "private", Owner: 1000, Labels: []string{"batch", "gpu"}}
}

func createOK(t *testing.T, r *impl.SharedNsRefcount, ns string, kind impl.NsKind) {
	t.Helper()
	if err := r.Create(ns, kind); err != nil {
		t.Fatalf("create %s: %v", ns, err)
	}
}

func joinOK(t *testing.T, r *impl.SharedNsRefcount, ns, member string, spec impl.NsSpec) {
	t.Helper()
	if err := r.Join(ns, member, spec); err != nil {
		t.Fatalf("join %s/%s: %v", ns, member, err)
	}
}

func containsName(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

func TestCreateValidatesAndConflicts(t *testing.T) {
	r := impl.NewSharedNsRefcount()
	if err := r.Create("  ", impl.KindNetwork); !errors.Is(err, impl.ErrBadNamespace) {
		t.Fatalf("blank namespace must fail with ErrBadNamespace, got %v", err)
	}
	if err := r.Create("ns", impl.NsKind("cgroup")); !errors.Is(err, impl.ErrBadKind) {
		t.Fatalf("unsupported kind must fail with ErrBadKind, got %v", err)
	}
	createOK(t, r, "ns", impl.KindNetwork)
	if err := r.Create("ns", impl.KindIPC); !errors.Is(err, impl.ErrNamespaceExists) {
		t.Fatalf("duplicate create must fail with ErrNamespaceExists, got %v", err)
	}
	if got := r.Refs("ns"); got != 0 {
		t.Fatalf("fresh namespace refs = %d, want 0", got)
	}
	if members := r.Members("ns"); len(members) != 0 {
		t.Fatalf("fresh namespace members = %v, want none", members)
	}
	if kind, err := r.Kind("ns"); err != nil || kind != impl.KindNetwork {
		t.Fatalf("kind = %q err=%v, want %q", kind, err, impl.KindNetwork)
	}
	if names := r.Namespaces(); len(names) != 1 || names[0] != "ns" {
		t.Fatalf("namespaces = %v, want [ns]", names)
	}
}

func TestCreateMultipleNamespaces(t *testing.T) {
	r := impl.NewSharedNsRefcount()
	createOK(t, r, "b-ipc", impl.KindIPC)
	createOK(t, r, "a-uts", impl.KindUTS)
	names := r.Namespaces()
	if len(names) != 2 || names[0] != "a-uts" || names[1] != "b-ipc" {
		t.Fatalf("namespaces = %v, want sorted [a-uts b-ipc]", names)
	}
	if kind, err := r.Kind("a-uts"); err != nil || kind != impl.KindUTS {
		t.Fatalf("kind = %q err=%v, want %q", kind, err, impl.KindUTS)
	}
	if kind, err := r.Kind("b-ipc"); err != nil || kind != impl.KindIPC {
		t.Fatalf("kind = %q err=%v, want %q", kind, err, impl.KindIPC)
	}
}

func TestJoinIncrementsRefs(t *testing.T) {
	r := impl.NewSharedNsRefcount()
	createOK(t, r, "ns", impl.KindNetwork)
	joinOK(t, r, "ns", "m1", baseSpec())
	if got := r.Refs("ns"); got != 1 {
		t.Fatalf("refs after first join = %d, want 1", got)
	}
	joinOK(t, r, "ns", "m2", baseSpec())
	if got := r.Refs("ns"); got != 2 {
		t.Fatalf("refs after second join = %d, want 2", got)
	}
	members := r.Members("ns")
	if len(members) != 2 || members[0] != "m1" || members[1] != "m2" {
		t.Fatalf("members = %v, want sorted [m1 m2]", members)
	}
	spec, err := r.Spec("ns")
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if spec.Mode != "private" || spec.Owner != 1000 {
		t.Fatalf("namespace spec = %+v, want mode private owner 1000", spec)
	}
	if len(spec.Labels) != 2 || spec.Labels[0] != "batch" || spec.Labels[1] != "gpu" {
		t.Fatalf("spec labels = %v, want sorted [batch gpu]", spec.Labels)
	}
}

func TestJoinUnknownNamespace(t *testing.T) {
	r := impl.NewSharedNsRefcount()
	if err := r.Join("ghost", "m1", baseSpec()); !errors.Is(err, impl.ErrUnknownNamespace) {
		t.Fatalf("join of an unknown namespace must fail with ErrUnknownNamespace, got %v", err)
	}
	if got := r.Refs("ghost"); got != 0 {
		t.Fatalf("refs of an unknown namespace = %d, want 0", got)
	}
}

func TestJoinBlankMember(t *testing.T) {
	r := impl.NewSharedNsRefcount()
	createOK(t, r, "ns", impl.KindNetwork)
	if err := r.Join("ns", "   ", baseSpec()); !errors.Is(err, impl.ErrBadMember) {
		t.Fatalf("blank member must fail with ErrBadMember, got %v", err)
	}
	if got := r.Refs("ns"); got != 0 {
		t.Fatalf("refs after a rejected join = %d, want 0", got)
	}
	joinOK(t, r, "ns", "m1", baseSpec())
}

func TestJoinSpecConflictByMode(t *testing.T) {
	r := impl.NewSharedNsRefcount()
	createOK(t, r, "ns", impl.KindNetwork)
	joinOK(t, r, "ns", "m1", baseSpec())
	other := baseSpec()
	other.Mode = "host"
	err := r.Join("ns", "m2", other)
	if !errors.Is(err, impl.ErrSpecConflict) {
		t.Fatalf("mode conflict must match ErrSpecConflict, got %v", err)
	}
	var conflict *impl.SpecConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("mode conflict must be reported as *SpecConflictError, got %T", err)
	}
	if len(conflict.Fields) != 1 || conflict.Fields[0] != "mode" {
		t.Fatalf("conflict fields = %v, want [mode]", conflict.Fields)
	}
	if got := r.Refs("ns"); got != 1 {
		t.Fatalf("rejected join changed refs to %d, want 1", got)
	}
	if members := r.Members("ns"); len(members) != 1 || members[0] != "m1" {
		t.Fatalf("members after a rejected join = %v, want [m1]", members)
	}
}

func TestJoinSpecConflictByOwnerAndLabels(t *testing.T) {
	r := impl.NewSharedNsRefcount()
	createOK(t, r, "ns", impl.KindNetwork)
	joinOK(t, r, "ns", "m1", baseSpec())
	ownerDiff := baseSpec()
	ownerDiff.Owner = 2000
	err := r.Join("ns", "m2", ownerDiff)
	var conflict *impl.SpecConflictError
	if !errors.As(err, &conflict) || len(conflict.Fields) != 1 || conflict.Fields[0] != "owner" {
		t.Fatalf("owner conflict = %+v, want fields [owner]", conflict)
	}
	labelDiff := baseSpec()
	labelDiff.Labels = []string{"batch"}
	err = r.Join("ns", "m3", labelDiff)
	if !errors.As(err, &conflict) || len(conflict.Fields) != 1 || conflict.Fields[0] != "labels" {
		t.Fatalf("label conflict = %+v, want fields [labels]", conflict)
	}
	labelOrder := baseSpec()
	labelOrder.Labels = []string{"gpu", "batch"}
	joinOK(t, r, "ns", "m4", labelOrder)
	if got := r.Refs("ns"); got != 2 {
		t.Fatalf("refs after the label order join = %d, want 2", got)
	}
}

func TestLeaveDecrementsAndRejectsNonMember(t *testing.T) {
	r := impl.NewSharedNsRefcount()
	createOK(t, r, "ns", impl.KindNetwork)
	joinOK(t, r, "ns", "m1", baseSpec())
	joinOK(t, r, "ns", "m2", baseSpec())
	if err := r.Leave("ns", "m1"); err != nil {
		t.Fatalf("leave m1: %v", err)
	}
	if got := r.Refs("ns"); got != 1 {
		t.Fatalf("refs after leave = %d, want 1", got)
	}
	if members := r.Members("ns"); len(members) != 1 || members[0] != "m2" {
		t.Fatalf("members after leave = %v, want [m2]", members)
	}
	if err := r.Leave("ns", "m1"); !errors.Is(err, impl.ErrNotMember) {
		t.Fatalf("leaving twice must fail with ErrNotMember, got %v", err)
	}
	if err := r.Leave("ns", "m2"); err != nil {
		t.Fatalf("leave m2: %v", err)
	}
	if got := r.Refs("ns"); got != 0 {
		t.Fatalf("refs after the last leave = %d, want 0", got)
	}
	if members := r.Members("ns"); len(members) != 0 {
		t.Fatalf("members after the last leave = %v, want none", members)
	}
}

func TestLeaveUnknownNamespace(t *testing.T) {
	r := impl.NewSharedNsRefcount()
	if err := r.Leave("ghost", "m1"); !errors.Is(err, impl.ErrUnknownNamespace) {
		t.Fatalf("leave on an unknown namespace must fail with ErrUnknownNamespace, got %v", err)
	}
}

func TestRefsNeverNegative(t *testing.T) {
	r := impl.NewSharedNsRefcount()
	createOK(t, r, "ns", impl.KindNetwork)
	if got := r.Refs("ns"); got != 0 {
		t.Fatalf("fresh refs = %d, want 0", got)
	}
	if err := r.Leave("ns", "ghost"); !errors.Is(err, impl.ErrNotMember) {
		t.Fatalf("leaving a non member must fail with ErrNotMember, got %v", err)
	}
	if got := r.Refs("ns"); got != 0 {
		t.Fatalf("refs after a rejected leave = %d, want 0", got)
	}
	joinOK(t, r, "ns", "m1", baseSpec())
	if err := r.Leave("ns", "m1"); err != nil {
		t.Fatalf("leave m1: %v", err)
	}
	if err := r.Leave("ns", "m1"); !errors.Is(err, impl.ErrNotMember) {
		t.Fatalf("second leave must fail with ErrNotMember, got %v", err)
	}
	if got := r.Refs("ns"); got != 0 {
		t.Fatalf("refs = %d, want it to stop at zero", got)
	}
	if err := r.Destroy("ns"); err != nil {
		t.Fatalf("destroy at zero refs: %v", err)
	}
	if got := r.Refs("ns"); got != 0 {
		t.Fatalf("refs after destroy = %d, want 0", got)
	}
}

func TestDestroyRequiresZeroRefs(t *testing.T) {
	r := impl.NewSharedNsRefcount()
	createOK(t, r, "ns", impl.KindNetwork)
	joinOK(t, r, "ns", "m1", baseSpec())
	joinOK(t, r, "ns", "m2", baseSpec())
	if err := r.Destroy("ns"); !errors.Is(err, impl.ErrNamespaceBusy) {
		t.Fatalf("destroy with members must fail with ErrNamespaceBusy, got %v", err)
	}
	members := r.Members("ns")
	if len(members) != 2 || !containsName(members, "m1") || !containsName(members, "m2") {
		t.Fatalf("members after a rejected destroy = %v, want m1 and m2", members)
	}
	if got := r.Refs("ns"); got != 2 {
		t.Fatalf("refs after a rejected destroy = %d, want 2", got)
	}
	if err := r.Leave("ns", "m1"); err != nil {
		t.Fatalf("leave m1: %v", err)
	}
	if err := r.Leave("ns", "m2"); err != nil {
		t.Fatalf("leave m2: %v", err)
	}
	if err := r.Destroy("ns"); err != nil {
		t.Fatalf("destroy at zero refs: %v", err)
	}
	if err := r.Join("ns", "m3", baseSpec()); !errors.Is(err, impl.ErrUnknownNamespace) {
		t.Fatalf("join after destroy must fail with ErrUnknownNamespace, got %v", err)
	}
	if err := r.Destroy("ns"); !errors.Is(err, impl.ErrUnknownNamespace) {
		t.Fatalf("double destroy must fail with ErrUnknownNamespace, got %v", err)
	}
}

func TestDestroyRemovesNamespace(t *testing.T) {
	r := impl.NewSharedNsRefcount()
	createOK(t, r, "ns", impl.KindNetwork)
	joinOK(t, r, "ns", "m1", baseSpec())
	if err := r.Leave("ns", "m1"); err != nil {
		t.Fatalf("leave: %v", err)
	}
	if err := r.Destroy("ns"); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	if names := r.Namespaces(); len(names) != 0 {
		t.Fatalf("namespaces after destroy = %v, want none", names)
	}
	if _, err := r.Kind("ns"); !errors.Is(err, impl.ErrUnknownNamespace) {
		t.Fatalf("kind after destroy must fail with ErrUnknownNamespace, got %v", err)
	}
	if _, err := r.Spec("ns"); !errors.Is(err, impl.ErrUnknownNamespace) {
		t.Fatalf("spec after destroy must fail with ErrUnknownNamespace, got %v", err)
	}
	if members := r.Members("ns"); len(members) != 0 {
		t.Fatalf("members after destroy = %v, want none", members)
	}
	createOK(t, r, "ns", impl.KindIPC)
	if kind, err := r.Kind("ns"); err != nil || kind != impl.KindIPC {
		t.Fatalf("recreated kind = %q err=%v, want %q", kind, err, impl.KindIPC)
	}
}

func TestMembersAndSpecAreCopies(t *testing.T) {
	r := impl.NewSharedNsRefcount()
	createOK(t, r, "ns", impl.KindNetwork)
	joinOK(t, r, "ns", "b-member", baseSpec())
	joinOK(t, r, "ns", "a-member", baseSpec())
	members := r.Members("ns")
	if len(members) != 2 || members[0] != "a-member" || members[1] != "b-member" {
		t.Fatalf("members = %v, want sorted [a-member b-member]", members)
	}
	members[0] = "tampered"
	if again := r.Members("ns"); again[0] != "a-member" {
		t.Fatalf("mutating the returned member slice changed the internal set: %v", again)
	}
	spec, err := r.Spec("ns")
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	spec.Labels[0] = "tampered"
	again, err := r.Spec("ns")
	if err != nil {
		t.Fatalf("spec again: %v", err)
	}
	if again.Labels[0] != "batch" {
		t.Fatalf("mutating the returned spec labels changed the stored spec: %v", again.Labels)
	}
}

func TestConcurrentJoins(t *testing.T) {
	r := impl.NewSharedNsRefcount()
	createOK(t, r, "ns", impl.KindNetwork)
	members := []string{"m0", "m1", "m2", "m3", "m4", "m5", "m6", "m7"}
	var wg sync.WaitGroup
	var mu sync.Mutex
	failures := 0
	for _, member := range members {
		wg.Add(1)
		go func(member string) {
			defer wg.Done()
			if err := r.Join("ns", member, baseSpec()); err != nil {
				mu.Lock()
				failures++
				mu.Unlock()
			}
		}(member)
	}
	wg.Wait()
	if failures != 0 {
		t.Fatalf("concurrent joins hit %d failures, want none", failures)
	}
	if got := r.Refs("ns"); got != len(members) {
		t.Fatalf("refs after concurrent joins = %d, want %d", got, len(members))
	}
	if got := r.Members("ns"); len(got) != len(members) {
		t.Fatalf("members after concurrent joins = %v, want %d entries", got, len(members))
	}
}

func TestSnapshotRestore(t *testing.T) {
	r := impl.NewSharedNsRefcount()
	createOK(t, r, "net-ns", impl.KindNetwork)
	joinOK(t, r, "net-ns", "m1", baseSpec())
	joinOK(t, r, "net-ns", "m2", baseSpec())
	createOK(t, r, "ipc-ns", impl.KindIPC)
	data, err := r.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewSharedNsRefcount()
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.Refs("net-ns"); got != 2 {
		t.Fatalf("restored refs = %d, want 2", got)
	}
	if members := restored.Members("net-ns"); len(members) != 2 || members[0] != "m1" || members[1] != "m2" {
		t.Fatalf("restored members = %v, want [m1 m2]", members)
	}
	if kind, err := restored.Kind("net-ns"); err != nil || kind != impl.KindNetwork {
		t.Fatalf("restored kind = %q err=%v, want %q", kind, err, impl.KindNetwork)
	}
	spec, err := restored.Spec("net-ns")
	if err != nil {
		t.Fatalf("restored spec: %v", err)
	}
	if spec.Mode != "private" || spec.Owner != 1000 {
		t.Fatalf("restored spec = %+v, want mode private owner 1000", spec)
	}
	joinOK(t, restored, "net-ns", "m3", baseSpec())
	if got := restored.Refs("net-ns"); got != 3 {
		t.Fatalf("refs after joining the restored namespace = %d, want 3", got)
	}
	if err := restored.Destroy("ipc-ns"); err != nil {
		t.Fatalf("destroying the restored empty namespace: %v", err)
	}
	if err := restored.Restore([]byte("not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := restored.Refs("net-ns"); got != 3 {
		t.Fatalf("rejected restore changed refs to %d, want 3", got)
	}
	if members := restored.Members("net-ns"); len(members) != 3 {
		t.Fatalf("rejected restore changed members to %v", members)
	}
}
