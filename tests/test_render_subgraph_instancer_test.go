package task

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	impl "gsb/batch49-go-game-b-01"
)

func newInstancer() *impl.SubGraphInstancer {
	return impl.NewSubGraphInstancer(impl.InstancerOptions{
		MaxInstances:      64,
		MaxPassesPerFrame: 128,
		MaxGenerations:    8,
	})
}

func opaqueDef() impl.SubGraphDef {
	return impl.SubGraphDef{
		Passes: []impl.PassSpec{
			{Name: "gbuffer", Outputs: []string{"albedo", "normal"}},
			{Name: "lighting", Inputs: []string{"albedo", "normal"}, Outputs: []string{"color"}},
		},
		Params: []impl.ParamDecl{
			{Name: "quality", Default: "medium"},
			{Name: "mode", Required: true},
		},
	}
}

func mustRegister(t *testing.T, s *impl.SubGraphInstancer, name string, def impl.SubGraphDef) {
	t.Helper()
	if err := s.RegisterSubGraph(name, def); err != nil {
		t.Fatalf("register %s: %v", name, err)
	}
}

func mustInstantiate(t *testing.T, s *impl.SubGraphInstancer, graphID, subGraph string, params map[string]string) impl.InstanceID {
	t.Helper()
	id, err := s.Instantiate(graphID, subGraph, params)
	if err != nil {
		t.Fatalf("instantiate %s in %s: %v", subGraph, graphID, err)
	}
	return id
}

func TestRegisterSubGraphStoresDefinition(t *testing.T) {
	s := newInstancer()
	mustRegister(t, s, "opaque", opaqueDef())
	id := mustInstantiate(t, s, "frame", "opaque", map[string]string{"mode": "forward"})
	if id == 0 {
		t.Fatalf("instance id must be non-zero")
	}
	ids := s.Instances("frame")
	if len(ids) != 1 || ids[0] != id {
		t.Fatalf("instances = %v, want [%d]", ids, id)
	}
	nodes, err := s.Expand("frame")
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("expanded passes = %d, want 2", len(nodes))
	}
}

func TestRegisterSubGraphRejectsBadDefinitions(t *testing.T) {
	s := newInstancer()
	if err := s.RegisterSubGraph("", opaqueDef()); !errors.Is(err, impl.ErrBlankName) {
		t.Fatalf("blank name error = %v, want ErrBlankName", err)
	}
	if err := s.RegisterSubGraph("empty", impl.SubGraphDef{}); !errors.Is(err, impl.ErrEmptyDefinition) {
		t.Fatalf("empty definition error = %v, want ErrEmptyDefinition", err)
	}
	if err := s.RegisterSubGraph("nopass", impl.SubGraphDef{Passes: []impl.PassSpec{{Name: "  "}}}); !errors.Is(err, impl.ErrBadPass) {
		t.Fatalf("blank pass error = %v, want ErrBadPass", err)
	}
	mustRegister(t, s, "dup", opaqueDef())
	if err := s.RegisterSubGraph("dup", opaqueDef()); !errors.Is(err, impl.ErrDuplicateSubGraph) {
		t.Fatalf("duplicate error = %v, want ErrDuplicateSubGraph", err)
	}
}

func TestInstantiateReusesSameParameterSet(t *testing.T) {
	s := newInstancer()
	mustRegister(t, s, "opaque", opaqueDef())
	first := mustInstantiate(t, s, "frame", "opaque", map[string]string{"mode": "forward", "quality": "high"})
	second := mustInstantiate(t, s, "frame", "opaque", map[string]string{"quality": "high", "mode": "forward"})
	if first != second {
		t.Fatalf("same parameter set produced instances %d and %d", first, second)
	}
	if got := len(s.Instances("frame")); got != 1 {
		t.Fatalf("instances = %d, want 1", got)
	}
}

func TestInstantiateSeparatesDifferentParameters(t *testing.T) {
	s := newInstancer()
	mustRegister(t, s, "opaque", opaqueDef())
	high := mustInstantiate(t, s, "frame", "opaque", map[string]string{"mode": "forward", "quality": "high"})
	low := mustInstantiate(t, s, "frame", "opaque", map[string]string{"mode": "forward", "quality": "low"})
	deferred := mustInstantiate(t, s, "frame", "opaque", map[string]string{"mode": "deferred", "quality": "high"})
	if high == low || high == deferred || low == deferred {
		t.Fatalf("different parameters collapsed into one instance: %d %d %d", high, low, deferred)
	}
	if got := len(s.Instances("frame")); got != 3 {
		t.Fatalf("instances = %d, want 3", got)
	}
}

func TestInstantiateRejectsInvalidRequests(t *testing.T) {
	s := newInstancer()
	mustRegister(t, s, "opaque", opaqueDef())
	cases := []struct {
		name   string
		graph  string
		sub    string
		params map[string]string
		want   error
	}{
		{"blank graph", "", "opaque", map[string]string{"mode": "forward"}, impl.ErrBlankGraph},
		{"unknown sub graph", "frame", "missing", map[string]string{"mode": "forward"}, impl.ErrUnknownSubGraph},
		{"missing required param", "frame", "opaque", map[string]string{}, impl.ErrMissingParam},
		{"undeclared param", "frame", "opaque", map[string]string{"mode": "forward", "scale": "2"}, impl.ErrUnknownParam},
	}
	for _, tc := range cases {
		if _, err := s.Instantiate(tc.graph, tc.sub, tc.params); !errors.Is(err, tc.want) {
			t.Fatalf("%s: error = %v, want %v", tc.name, err, tc.want)
		}
	}
	if got := len(s.Instances("frame")); got != 0 {
		t.Fatalf("rejected requests left %d instances", got)
	}
}

func TestInstantiateEnforcesInstanceLimit(t *testing.T) {
	s := impl.NewSubGraphInstancer(impl.InstancerOptions{MaxInstances: 2, MaxPassesPerFrame: 16})
	mustRegister(t, s, "opaque", opaqueDef())
	mustInstantiate(t, s, "frame", "opaque", map[string]string{"mode": "forward", "quality": "high"})
	mustInstantiate(t, s, "frame", "opaque", map[string]string{"mode": "forward", "quality": "low"})
	if _, err := s.Instantiate("frame", "opaque", map[string]string{"mode": "forward", "quality": "ultra"}); !errors.Is(err, impl.ErrInstanceLimit) {
		t.Fatalf("over limit error = %v, want ErrInstanceLimit", err)
	}
	if got := len(s.Instances("frame")); got != 2 {
		t.Fatalf("instances after rejection = %d, want 2", got)
	}
}

func TestBindHistoryIsIdempotentPerResource(t *testing.T) {
	s := newInstancer()
	mustRegister(t, s, "opaque", opaqueDef())
	id := mustInstantiate(t, s, "frame", "opaque", map[string]string{"mode": "forward"})
	first, err := s.BindHistory(id, "prev_color")
	if err != nil {
		t.Fatalf("bind prev_color: %v", err)
	}
	again, err := s.BindHistory(id, "prev_color")
	if err != nil {
		t.Fatalf("rebind prev_color: %v", err)
	}
	if first != again {
		t.Fatalf("rebinding the same resource returned %+v and %+v", first, again)
	}
	other, err := s.BindHistory(id, "prev_depth")
	if err != nil {
		t.Fatalf("bind prev_depth: %v", err)
	}
	if first.ID == other.ID {
		t.Fatalf("different resources share handle id %d", first.ID)
	}
	if first.Resource != "prev_color" || other.Resource != "prev_depth" {
		t.Fatalf("handle resources = %q and %q", first.Resource, other.Resource)
	}
	if _, err := s.BindHistory(impl.InstanceID(9999), "prev_color"); !errors.Is(err, impl.ErrUnknownInstance) {
		t.Fatalf("unknown instance error = %v, want ErrUnknownInstance", err)
	}
	if _, err := s.BindHistory(id, "   "); !errors.Is(err, impl.ErrBadResource) {
		t.Fatalf("blank resource error = %v, want ErrBadResource", err)
	}
}

func TestExpandOrdersInstancesThenPasses(t *testing.T) {
	s := newInstancer()
	mustRegister(t, s, "opaque", opaqueDef())
	mustRegister(t, s, "post", impl.SubGraphDef{Passes: []impl.PassSpec{{Name: "bloom"}}})
	first := mustInstantiate(t, s, "frame", "opaque", map[string]string{"mode": "forward", "quality": "high"})
	second := mustInstantiate(t, s, "frame", "opaque", map[string]string{"mode": "forward", "quality": "low"})
	last := mustInstantiate(t, s, "frame", "post", nil)
	nodes, err := s.Expand("frame")
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	want := []struct {
		instance impl.InstanceID
		pass     string
	}{
		{first, "gbuffer"},
		{first, "lighting"},
		{second, "gbuffer"},
		{second, "lighting"},
		{last, "bloom"},
	}
	if len(nodes) != len(want) {
		t.Fatalf("expanded passes = %d, want %d", len(nodes), len(want))
	}
	for i, w := range want {
		if nodes[i].Instance != w.instance || nodes[i].Pass != w.pass {
			t.Fatalf("node %d = %+v, want instance %d pass %s", i, nodes[i], w.instance, w.pass)
		}
		if nodes[i].Ordinal != i {
			t.Fatalf("node %d ordinal = %d, want %d", i, nodes[i].Ordinal, i)
		}
		if nodes[i].SubGraph == "" {
			t.Fatalf("node %d has no sub graph name", i)
		}
	}
	if got := s.PassCount("frame"); got != 5 {
		t.Fatalf("pass count = %d, want 5", got)
	}
	if len(nodes[0].Outputs) != 2 || nodes[0].Outputs[0] != "albedo" {
		t.Fatalf("first node outputs = %v, want albedo and normal", nodes[0].Outputs)
	}
}

func TestExpandRequiresHistoryBinding(t *testing.T) {
	s := newInstancer()
	mustRegister(t, s, "taa", impl.SubGraphDef{Passes: []impl.PassSpec{{Name: "resolve", History: true}}})
	id := mustInstantiate(t, s, "frame", "taa", nil)
	if _, err := s.Expand("frame"); !errors.Is(err, impl.ErrUnboundHistory) {
		t.Fatalf("expand without binding error = %v, want ErrUnboundHistory", err)
	}
	if got := s.PassCount("frame"); got != 0 {
		t.Fatalf("pass count after failed expand = %d, want 0", got)
	}
	if _, err := s.BindHistory(id, "prev_taa"); err != nil {
		t.Fatalf("bind history: %v", err)
	}
	nodes, err := s.Expand("frame")
	if err != nil {
		t.Fatalf("expand after binding: %v", err)
	}
	if len(nodes) != 1 || nodes[0].Pass != "resolve" {
		t.Fatalf("expanded nodes = %+v, want one resolve pass", nodes)
	}
}

func TestExpandRejectsPassBudget(t *testing.T) {
	s := impl.NewSubGraphInstancer(impl.InstancerOptions{MaxInstances: 8, MaxPassesPerFrame: 3})
	mustRegister(t, s, "heavy", impl.SubGraphDef{Passes: []impl.PassSpec{
		{Name: "a"}, {Name: "b"}, {Name: "c"}, {Name: "d"},
	}})
	mustInstantiate(t, s, "frame", "heavy", nil)
	if _, err := s.Expand("frame"); !errors.Is(err, impl.ErrPassBudget) {
		t.Fatalf("over budget error = %v, want ErrPassBudget", err)
	}
	if got := s.PassCount("frame"); got != 0 {
		t.Fatalf("pass count after over budget expand = %d, want 0", got)
	}
}

func TestCommitFrameAdvancesGeneration(t *testing.T) {
	s := newInstancer()
	mustRegister(t, s, "opaque", opaqueDef())
	id := mustInstantiate(t, s, "frame", "opaque", map[string]string{"mode": "forward", "quality": "high"})
	handle, err := s.BindHistory(id, "prev_color")
	if err != nil {
		t.Fatalf("bind history: %v", err)
	}
	if handle.Generation != 0 {
		t.Fatalf("handle generation = %d, want 0", handle.Generation)
	}
	if _, err := s.Expand("frame"); err != nil {
		t.Fatalf("expand: %v", err)
	}
	if got := s.CurrentGeneration(); got != 0 {
		t.Fatalf("generation after expand = %d, want 0", got)
	}
	if err := s.CommitFrame("frame"); err != nil {
		t.Fatalf("commit frame: %v", err)
	}
	if got := s.CurrentGeneration(); got != 1 {
		t.Fatalf("generation after commit = %d, want 1", got)
	}
	if err := s.CommitFrame("frame"); !errors.Is(err, impl.ErrEmptyFrame) {
		t.Fatalf("second commit error = %v, want ErrEmptyFrame", err)
	}
	next := mustInstantiate(t, s, "frame", "opaque", map[string]string{"mode": "forward", "quality": "low"})
	nextHandle, err := s.BindHistory(next, "prev_color")
	if err != nil {
		t.Fatalf("bind next history: %v", err)
	}
	if nextHandle.Generation != 1 {
		t.Fatalf("new handle generation = %d, want 1", nextHandle.Generation)
	}
}

func TestRetireGenerationGuardsActiveAndFuture(t *testing.T) {
	s := newInstancer()
	mustRegister(t, s, "opaque", opaqueDef())
	oldID := mustInstantiate(t, s, "frame", "opaque", map[string]string{"mode": "forward", "quality": "high"})
	oldHandle, err := s.BindHistory(oldID, "prev_color")
	if err != nil {
		t.Fatalf("bind old history: %v", err)
	}
	if _, err := s.Expand("frame"); err != nil {
		t.Fatalf("expand: %v", err)
	}
	if err := s.CommitFrame("frame"); err != nil {
		t.Fatalf("commit frame: %v", err)
	}
	newID := mustInstantiate(t, s, "frame", "opaque", map[string]string{"mode": "forward", "quality": "low"})
	newHandle, err := s.BindHistory(newID, "prev_color")
	if err != nil {
		t.Fatalf("bind new history: %v", err)
	}
	if err := s.RetireGeneration(1); !errors.Is(err, impl.ErrGenerationActive) {
		t.Fatalf("retire active generation error = %v, want ErrGenerationActive", err)
	}
	if err := s.RetireGeneration(9); !errors.Is(err, impl.ErrUnknownGeneration) {
		t.Fatalf("retire future generation error = %v, want ErrUnknownGeneration", err)
	}
	if err := s.RetireGeneration(0); err != nil {
		t.Fatalf("retire old generation: %v", err)
	}
	if s.HandleAlive(oldHandle) {
		t.Fatalf("handle of retired generation is still alive")
	}
	if !s.HandleAlive(newHandle) {
		t.Fatalf("handle of current generation is not alive")
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	s := newInstancer()
	mustRegister(t, s, "opaque", opaqueDef())
	id := mustInstantiate(t, s, "frame", "opaque", map[string]string{"mode": "forward", "quality": "high"})
	handle, err := s.BindHistory(id, "prev_color")
	if err != nil {
		t.Fatalf("bind history: %v", err)
	}
	if _, err := s.Expand("frame"); err != nil {
		t.Fatalf("expand: %v", err)
	}
	if err := s.CommitFrame("frame"); err != nil {
		t.Fatalf("commit frame: %v", err)
	}
	data, err := s.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload is empty")
	}
	restored := newInstancer()
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.CurrentGeneration(); got != 1 {
		t.Fatalf("restored generation = %d, want 1", got)
	}
	ids := restored.Instances("frame")
	if len(ids) != 1 || ids[0] != id {
		t.Fatalf("restored instances = %v, want [%d]", ids, id)
	}
	if _, err := restored.Expand("frame"); err != nil {
		t.Fatalf("expand after restore: %v", err)
	}
	back, err := restored.BindHistory(id, "prev_color")
	if err != nil {
		t.Fatalf("bind after restore: %v", err)
	}
	if back.ID != handle.ID || back.Generation != handle.Generation {
		t.Fatalf("restored handle = %+v, want %+v", back, handle)
	}
}

func TestRestoreRejectsCorruptPayload(t *testing.T) {
	s := newInstancer()
	mustRegister(t, s, "opaque", opaqueDef())
	mustInstantiate(t, s, "frame", "opaque", map[string]string{"mode": "forward"})
	if err := s.Restore([]byte("{not a snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt payload error = %v, want ErrBadSnapshot", err)
	}
	if got := len(s.Instances("frame")); got != 1 {
		t.Fatalf("instances after failed restore = %d, want 1", got)
	}
	if _, err := s.Expand("frame"); err != nil {
		t.Fatalf("expand after failed restore: %v", err)
	}
}

func TestConcurrentInstantiateIsSerialized(t *testing.T) {
	s := newInstancer()
	mustRegister(t, s, "opaque", opaqueDef())
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			params := map[string]string{"mode": "forward", "quality": fmt.Sprintf("q%d", i)}
			if _, err := s.Instantiate("frame", "opaque", params); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent instantiate: %v", err)
	}
	if got := len(s.Instances("frame")); got != 8 {
		t.Fatalf("instances after concurrent create = %d, want 8", got)
	}
}
