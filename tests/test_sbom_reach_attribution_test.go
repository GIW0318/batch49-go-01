package task

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	impl "gsb/batch49-go-safety-c-03"
)

const (
	appID  = "app@1.0.0"
	authID = "auth@1.0.0"
	billID = "billing@1.0.0"
	libID  = "lib@1.2.3"
)

func diamondGraph() impl.Graph {
	return impl.Graph{
		Components: []impl.Component{
			{Name: "app", Version: "1.0.0"},
			{Name: "auth", Version: "1.0.0"},
			{Name: "billing", Version: "1.0.0"},
			{Name: "lib", Version: "1.2.3"},
		},
		Edges: []impl.Edge{
			{From: appID, To: authID, Scope: impl.ScopeProd},
			{From: appID, To: billID, Scope: impl.ScopeProd},
			{From: authID, To: libID, Scope: impl.ScopeProd},
			{From: billID, To: libID, Scope: impl.ScopeProd},
		},
		Roots: []string{appID},
	}
}

func loaded(t *testing.T, graph impl.Graph) *impl.ReachAttributor {
	t.Helper()
	r := impl.NewReachAttributor()
	if err := r.Load(graph); err != nil {
		t.Fatalf("load graph: %v", err)
	}
	return r
}

func pathNodes(path impl.Path) string {
	joined := ""
	for i, node := range path.Nodes {
		if i > 0 {
			joined += impl.PathSeparator
		}
		joined += node
	}
	return joined
}

func TestLoadRejectsBadGraph(t *testing.T) {
	r := impl.NewReachAttributor()
	bad := []impl.Graph{
		{},
		{Components: []impl.Component{{Name: "app", Version: "1.0.0"}}},
		{Components: []impl.Component{{Name: "app", Version: ""}}, Roots: []string{"app@"}},
		{
			Components: []impl.Component{{Name: "app", Version: "1.0.0"}, {Name: "app", Version: "1.0.0"}},
			Roots:      []string{appID},
		},
		{
			Components: []impl.Component{{Name: "app", Version: "1.0.0"}},
			Roots:      []string{appID},
			Edges:      []impl.Edge{{From: appID, To: "ghost@9.9.9", Scope: impl.ScopeProd}},
		},
		{
			Components: []impl.Component{{Name: "app", Version: "1.0.0"}},
			Roots:      []string{"missing@1.0.0"},
		},
		{
			Components: []impl.Component{{Name: "app", Version: "1.0.0"}},
			Roots:      []string{appID},
			Edges:      []impl.Edge{{From: appID, To: appID, Scope: impl.ScopeProd}},
		},
	}
	for i, graph := range bad {
		if err := r.Load(graph); !errors.Is(err, impl.ErrBadGraph) {
			t.Fatalf("graph %d must be rejected with ErrBadGraph, got %v", i, err)
		}
	}
	if got := r.Components(); len(got) != 0 {
		t.Fatalf("rejected graphs must not be installed, got %v", got)
	}
}

func TestLoadIsAtomicOnError(t *testing.T) {
	r := loaded(t, diamondGraph())
	if err := r.Load(impl.Graph{Components: []impl.Component{{Name: "broken", Version: ""}}}); !errors.Is(err, impl.ErrBadGraph) {
		t.Fatalf("bad graph must fail with ErrBadGraph, got %v", err)
	}
	if got := r.Components(); len(got) != 4 {
		t.Fatalf("components after rejected load = %v, want the previous four", got)
	}
	paths, err := r.PathsTo(libID, 10)
	if err != nil {
		t.Fatalf("paths after rejected load: %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("paths after rejected load = %d, want the previous graph's 2", len(paths))
	}
}

func TestComponentsAndRootsAreSorted(t *testing.T) {
	r := loaded(t, diamondGraph())
	got := r.Components()
	want := []string{appID, authID, billID, libID}
	if len(got) != len(want) {
		t.Fatalf("components = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("components = %v, want ascending %v", got, want)
		}
	}
	roots := r.Roots()
	if len(roots) != 1 || roots[0] != appID {
		t.Fatalf("roots = %v, want [%s]", roots, appID)
	}
}

func TestNodeLookup(t *testing.T) {
	r := loaded(t, diamondGraph())
	node, ok := r.Node(libID)
	if !ok {
		t.Fatalf("expected lib to be present")
	}
	if node.Name != "lib" || node.Version != "1.2.3" {
		t.Fatalf("node = %+v, want lib/1.2.3", node)
	}
	if node.ID() != libID {
		t.Fatalf("node id = %q, want %q", node.ID(), libID)
	}
	if _, ok := r.Node("lib"); ok {
		t.Fatalf("component name alone must not resolve as a node")
	}
	if _, ok := r.Node("lib@9.9.9"); ok {
		t.Fatalf("unknown version must not resolve as a node")
	}
}

func TestPathsToDiamond(t *testing.T) {
	r := loaded(t, diamondGraph())
	paths, err := r.PathsTo(libID, 10)
	if err != nil {
		t.Fatalf("paths: %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("paths = %d, want 2 through auth and billing", len(paths))
	}
	if got := pathNodes(paths[0]); got != "app@1.0.0 -> auth@1.0.0 -> lib@1.2.3" {
		t.Fatalf("first path = %q, want the auth branch", got)
	}
	if got := pathNodes(paths[1]); got != "app@1.0.0 -> billing@1.0.0 -> lib@1.2.3" {
		t.Fatalf("second path = %q, want the billing branch", got)
	}
}

func TestPathsToOrdersByLength(t *testing.T) {
	graph := impl.Graph{
		Components: []impl.Component{
			{Name: "app", Version: "1.0.0"},
			{Name: "hub", Version: "1.0.0"},
			{Name: "mid", Version: "2.0.0"},
			{Name: "lib", Version: "1.2.3"},
		},
		Edges: []impl.Edge{
			{From: appID, To: "mid@2.0.0", Scope: impl.ScopeProd},
			{From: "mid@2.0.0", To: "hub@1.0.0", Scope: impl.ScopeProd},
			{From: "hub@1.0.0", To: libID, Scope: impl.ScopeProd},
			{From: appID, To: libID, Scope: impl.ScopeProd},
		},
		Roots: []string{appID},
	}
	r := loaded(t, graph)
	paths, err := r.PathsTo(libID, 10)
	if err != nil {
		t.Fatalf("paths: %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("paths = %d, want 2", len(paths))
	}
	if len(paths[0].Nodes) != 2 {
		t.Fatalf("first path = %q, want the direct two node path", pathNodes(paths[0]))
	}
	if len(paths[1].Nodes) != 4 {
		t.Fatalf("second path = %q, want the four node detour", pathNodes(paths[1]))
	}
}

func TestPathsToRespectsLimit(t *testing.T) {
	r := loaded(t, diamondGraph())
	one, err := r.PathsTo(libID, 1)
	if err != nil {
		t.Fatalf("paths limit 1: %v", err)
	}
	if len(one) != 1 {
		t.Fatalf("paths with limit 1 = %d, want 1", len(one))
	}
	if got := pathNodes(one[0]); got != "app@1.0.0 -> auth@1.0.0 -> lib@1.2.3" {
		t.Fatalf("truncated path = %q, want the first ordered path", got)
	}
	all, err := r.PathsTo(libID, 5)
	if err != nil {
		t.Fatalf("paths limit 5: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("paths with limit 5 = %d, want 2", len(all))
	}
}

func TestPathsToRejectsBadLimitAndUnknown(t *testing.T) {
	r := loaded(t, diamondGraph())
	if _, err := r.PathsTo(libID, 0); !errors.Is(err, impl.ErrBadLimit) {
		t.Fatalf("zero limit must fail with ErrBadLimit, got %v", err)
	}
	if _, err := r.PathsTo(libID, -3); !errors.Is(err, impl.ErrBadLimit) {
		t.Fatalf("negative limit must fail with ErrBadLimit, got %v", err)
	}
	if _, err := r.PathsTo("lib@9.9.9", 5); !errors.Is(err, impl.ErrUnknownComponent) {
		t.Fatalf("unknown component must fail with ErrUnknownComponent, got %v", err)
	}
	if _, err := r.PathsTo("", 5); !errors.Is(err, impl.ErrUnknownComponent) {
		t.Fatalf("empty component id must fail with ErrUnknownComponent, got %v", err)
	}
}

func TestShortestReturnsMinimalPath(t *testing.T) {
	r := loaded(t, diamondGraph())
	shortest, err := r.Shortest(libID)
	if err != nil {
		t.Fatalf("shortest: %v", err)
	}
	if got := pathNodes(shortest); got != "app@1.0.0 -> auth@1.0.0 -> lib@1.2.3" {
		t.Fatalf("shortest = %q, want the lexicographically first minimal path", got)
	}
	graph := diamondGraph()
	graph.Components = append(graph.Components, impl.Component{Name: "orphan", Version: "1.0.0"})
	r = loaded(t, graph)
	if _, err := r.Shortest("orphan@1.0.0"); !errors.Is(err, impl.ErrNoPath) {
		t.Fatalf("unreachable component must fail with ErrNoPath, got %v", err)
	}
	if _, err := r.Shortest("ghost@1.0.0"); !errors.Is(err, impl.ErrUnknownComponent) {
		t.Fatalf("unknown component must fail with ErrUnknownComponent, got %v", err)
	}
}

func TestCycleHandledAsSimplePath(t *testing.T) {
	graph := impl.Graph{
		Components: []impl.Component{
			{Name: "app", Version: "1.0.0"},
			{Name: "b", Version: "1.0.0"},
			{Name: "c", Version: "1.0.0"},
		},
		Edges: []impl.Edge{
			{From: appID, To: "b@1.0.0", Scope: impl.ScopeProd},
			{From: "b@1.0.0", To: "c@1.0.0", Scope: impl.ScopeProd},
			{From: "c@1.0.0", To: "b@1.0.0", Scope: impl.ScopeProd},
		},
		Roots: []string{appID},
	}
	r := loaded(t, graph)
	paths, err := r.PathsTo("c@1.0.0", 10)
	if err != nil {
		t.Fatalf("paths: %v", err)
	}
	if len(paths) != 1 {
		t.Fatalf("paths = %d, want the single simple path", len(paths))
	}
	seen := map[string]bool{}
	for _, node := range paths[0].Nodes {
		if seen[node] {
			t.Fatalf("path %q repeats node %q", pathNodes(paths[0]), node)
		}
		seen[node] = true
	}
	if got := pathNodes(paths[0]); got != "app@1.0.0 -> b@1.0.0 -> c@1.0.0" {
		t.Fatalf("path = %q, want app -> b -> c", got)
	}
	toB, err := r.PathsTo("b@1.0.0", 10)
	if err != nil {
		t.Fatalf("paths to b: %v", err)
	}
	if len(toB) != 1 || len(toB[0].Nodes) != 2 {
		t.Fatalf("paths to b = %+v, want the two node path", toB)
	}
}

func TestIntroducersListsDirectOwners(t *testing.T) {
	r := loaded(t, diamondGraph())
	owners := r.Introducers(libID)
	want := []string{authID, billID}
	if len(owners) != len(want) {
		t.Fatalf("owners = %+v, want %v", owners, want)
	}
	for i := range want {
		if owners[i].ID != want[i] {
			t.Fatalf("owners = %+v, want ascending %v", owners, want)
		}
	}
	if got := r.Introducers(appID); len(got) != 0 {
		t.Fatalf("a root must have no introducers, got %+v", got)
	}
	if got := r.Introducers("ghost@1.0.0"); len(got) != 0 {
		t.Fatalf("unknown component must have no introducers, got %+v", got)
	}
}

func TestPolicyPrunesDevScope(t *testing.T) {
	graph := impl.Graph{
		Components: []impl.Component{
			{Name: "app", Version: "1.0.0"},
			{Name: "devhub", Version: "1.0.0"},
			{Name: "opthub", Version: "1.0.0"},
			{Name: "lib", Version: "1.2.3"},
		},
		Edges: []impl.Edge{
			{From: appID, To: libID, Scope: impl.ScopeProd},
			{From: appID, To: "devhub@1.0.0", Scope: impl.ScopeDev},
			{From: "devhub@1.0.0", To: libID, Scope: impl.ScopeDev},
			{From: appID, To: "opthub@1.0.0", Scope: impl.ScopeOptional},
			{From: "opthub@1.0.0", To: libID, Scope: impl.ScopeOptional},
		},
		Roots: []string{appID},
	}
	r := loaded(t, graph)
	if paths, err := r.PathsTo(libID, 10); err != nil || len(paths) != 3 {
		t.Fatalf("unpruned paths = %d (%v), want 3", len(paths), err)
	}
	if err := r.SetPolicy(impl.PrunePolicy{DropDev: true}); err != nil {
		t.Fatalf("set policy: %v", err)
	}
	if !r.Policy().DropDev {
		t.Fatalf("policy must report DropDev")
	}
	paths, err := r.PathsTo(libID, 10)
	if err != nil {
		t.Fatalf("paths after dev prune: %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("paths after dev prune = %d, want the prod and optional paths", len(paths))
	}
	if err := r.SetPolicy(impl.PrunePolicy{DropDev: true, DropOptional: true}); err != nil {
		t.Fatalf("set policy: %v", err)
	}
	paths, err = r.PathsTo(libID, 10)
	if err != nil {
		t.Fatalf("paths after full prune: %v", err)
	}
	if len(paths) != 1 || pathNodes(paths[0]) != "app@1.0.0 -> lib@1.2.3" {
		t.Fatalf("paths after full prune = %+v, want only the production path", paths)
	}
}

func TestSnapshotRestoreKeepsGraphAndPolicy(t *testing.T) {
	r := loaded(t, diamondGraph())
	if err := r.SetPolicy(impl.PrunePolicy{DropOptional: true}); err != nil {
		t.Fatalf("set policy: %v", err)
	}
	data, err := r.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewReachAttributor()
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.Components(); len(got) != 4 {
		t.Fatalf("restored components = %v, want 4", got)
	}
	if !restored.Policy().DropOptional {
		t.Fatalf("restored policy lost DropOptional")
	}
	paths, err := restored.PathsTo(libID, 10)
	if err != nil {
		t.Fatalf("paths after restore: %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("paths after restore = %d, want 2", len(paths))
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	r := loaded(t, diamondGraph())
	if err := r.SetPolicy(impl.PrunePolicy{DropDev: true}); err != nil {
		t.Fatalf("set policy: %v", err)
	}
	if err := r.Restore([]byte("<<<not-a-snapshot>>>")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := r.Components(); len(got) != 4 {
		t.Fatalf("components after rejected restore = %v, want 4", got)
	}
	if !r.Policy().DropDev {
		t.Fatalf("policy after rejected restore lost DropDev")
	}
}

func TestConcurrentPathsSeeWholeGraph(t *testing.T) {
	diamond := diamondGraph()
	direct := impl.Graph{
		Components: []impl.Component{
			{Name: "app", Version: "1.0.0"},
			{Name: "lib", Version: "1.2.3"},
		},
		Edges: []impl.Edge{{From: appID, To: libID, Scope: impl.ScopeProd}},
		Roots: []string{appID},
	}
	r := loaded(t, diamond)
	const readers = 6
	const iterations = 40
	errCh := make(chan error, readers*iterations)
	var wg sync.WaitGroup
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				paths, err := r.PathsTo(libID, 10)
				if err != nil {
					errCh <- fmt.Errorf("paths during load: %w", err)
					return
				}
				switch len(paths) {
				case 1:
					if pathNodes(paths[0]) != "app@1.0.0 -> lib@1.2.3" {
						errCh <- fmt.Errorf("torn single path: %q", pathNodes(paths[0]))
						return
					}
				case 2:
					if pathNodes(paths[0]) != "app@1.0.0 -> auth@1.0.0 -> lib@1.2.3" {
						errCh <- fmt.Errorf("torn diamond path: %q", pathNodes(paths[0]))
						return
					}
				default:
					errCh <- fmt.Errorf("paths returned %d entries, want a whole graph", len(paths))
					return
				}
			}
		}()
	}
	for i := 0; i < 24; i++ {
		var err error
		if i%2 == 0 {
			err = r.Load(direct)
		} else {
			err = r.Load(diamond)
		}
		if err != nil {
			t.Fatalf("load %d: %v", i, err)
		}
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent traversal observed a torn graph: %v", err)
	}
}
