package task

import (
	"errors"
	"reflect"
	"sync"
	"testing"

	impl "gsb/batch49-go-safety-d-09"
)

func solver() *impl.CapabilityDemandSolver {
	return impl.NewCapabilityDemandSolver(impl.SolverConfig{MaxAccesses: 64})
}

func mustAdd(t *testing.T, s *impl.CapabilityDemandSolver, a impl.Access) {
	t.Helper()
	if err := s.AddAccess(a); err != nil {
		t.Fatalf("add access %+v: %v", a, err)
	}
}

func openRead(path string, seq uint64) impl.Access {
	return impl.Access{Op: impl.OpOpen, Path: path, Mode: impl.ModeRead, Seq: seq}
}

func TestAddAccessRejectsBlankFields(t *testing.T) {
	s := solver()
	if err := s.AddAccess(impl.Access{Op: "", Path: "/etc/app.conf", Mode: impl.ModeRead}); !errors.Is(err, impl.ErrBadAccess) {
		t.Fatalf("blank op err = %v, want ErrBadAccess", err)
	}
	if err := s.AddAccess(impl.Access{Op: impl.OpStat, Path: "", Mode: ""}); !errors.Is(err, impl.ErrBadAccess) {
		t.Fatalf("blank path err = %v, want ErrBadAccess", err)
	}
	if got := s.Count(); got != 0 {
		t.Fatalf("count after rejected accesses = %d, want 0", got)
	}
}

func TestAddAccessEnforcesTraceLimit(t *testing.T) {
	s := impl.NewCapabilityDemandSolver(impl.SolverConfig{MaxAccesses: 3})
	mustAdd(t, s, openRead("/a", 1))
	mustAdd(t, s, openRead("/b", 2))
	mustAdd(t, s, openRead("/c", 3))
	if err := s.AddAccess(openRead("/d", 4)); !errors.Is(err, impl.ErrAccessLimit) {
		t.Fatalf("fourth access err = %v, want ErrAccessLimit", err)
	}
	if got := s.Count(); got != 3 {
		t.Fatalf("count = %d, want 3", got)
	}
	if got := len(s.Accesses()); got != 3 {
		t.Fatalf("accesses length = %d, want 3", got)
	}
}

func TestSolveMapsReadAndWriteToCaps(t *testing.T) {
	s := solver()
	mustAdd(t, s, openRead("/etc/app.conf", 1))
	mustAdd(t, s, impl.Access{Op: impl.OpOpen, Path: "/var/log/app.log", Mode: impl.ModeWrite, Seq: 2})
	demand, err := s.Solve()
	if err != nil {
		t.Fatalf("solve: %v", err)
	}
	if !reflect.DeepEqual(demand.Caps, []string{impl.CapReadFile, impl.CapWriteFile}) {
		t.Fatalf("caps = %v, want [%s %s]", demand.Caps, impl.CapReadFile, impl.CapWriteFile)
	}
	if !reflect.DeepEqual(demand.ByCap[impl.CapReadFile], []string{"/etc/app.conf"}) {
		t.Fatalf("read paths = %v, want [/etc/app.conf]", demand.ByCap[impl.CapReadFile])
	}
	if !reflect.DeepEqual(demand.ByCap[impl.CapWriteFile], []string{"/var/log/app.log"}) {
		t.Fatalf("write paths = %v, want [/var/log/app.log]", demand.ByCap[impl.CapWriteFile])
	}
}

func TestSolveGroupsPathsPerCapability(t *testing.T) {
	s := solver()
	mustAdd(t, s, openRead("/beta", 1))
	mustAdd(t, s, openRead("/alpha", 2))
	mustAdd(t, s, openRead("/beta", 3))
	demand, err := s.Solve()
	if err != nil {
		t.Fatalf("solve: %v", err)
	}
	if len(demand.Caps) != 1 || demand.Caps[0] != impl.CapReadFile {
		t.Fatalf("caps = %v, want exactly %s", demand.Caps, impl.CapReadFile)
	}
	if !reflect.DeepEqual(demand.ByCap[impl.CapReadFile], []string{"/alpha", "/beta"}) {
		t.Fatalf("paths = %v, want sorted unique [/alpha /beta]", demand.ByCap[impl.CapReadFile])
	}
}

func TestSolveIsRepeatable(t *testing.T) {
	s := solver()
	mustAdd(t, s, openRead("/a", 1))
	mustAdd(t, s, impl.Access{Op: impl.OpChmod, Path: "/a", Seq: 2})
	first, err := s.Solve()
	if err != nil {
		t.Fatalf("first solve: %v", err)
	}
	second, err := s.Solve()
	if err != nil {
		t.Fatalf("second solve: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("solve is not repeatable:\n%+v\n%+v", first, second)
	}
	if got := len(s.Accesses()); got != 2 {
		t.Fatalf("solve changed the trace length to %d, want 2", got)
	}
}

func TestUncoveredAccessInvalidatesDemand(t *testing.T) {
	s := solver()
	mustAdd(t, s, impl.Access{Op: "frobnicate", Path: "/dev/odd", Seq: 1})
	demand, err := s.Solve()
	if !errors.Is(err, impl.ErrUncoveredAccess) {
		t.Fatalf("solve err = %v, want ErrUncoveredAccess", err)
	}
	if len(demand.Caps) != 0 {
		t.Fatalf("caps = %v, want an unusable empty demand", demand.Caps)
	}
	uncovered := s.Uncovered()
	if len(uncovered) != 1 {
		t.Fatalf("uncovered = %+v, want one access", uncovered)
	}
	if uncovered[0].Path != "/dev/odd" || uncovered[0].Op != "frobnicate" {
		t.Fatalf("uncovered access = %+v, want frobnicate /dev/odd", uncovered[0])
	}
}

func TestStatNeedsItsOwnCapability(t *testing.T) {
	s := solver()
	mustAdd(t, s, impl.Access{Op: impl.OpStat, Path: "/etc/app.conf", Seq: 1})
	demand, err := s.Solve()
	if err != nil {
		t.Fatalf("solve: %v", err)
	}
	if !reflect.DeepEqual(demand.Caps, []string{impl.CapStatFile}) {
		t.Fatalf("caps = %v, want [%s]", demand.Caps, impl.CapStatFile)
	}
	if _, ok := demand.ByCap[impl.CapReadFile]; ok {
		t.Fatalf("stat access must not be reported as %s", impl.CapReadFile)
	}
}

func TestChmodChownExecAreDistinctCaps(t *testing.T) {
	s := solver()
	mustAdd(t, s, impl.Access{Op: impl.OpChmod, Path: "/srv/one", Seq: 1})
	mustAdd(t, s, impl.Access{Op: impl.OpChown, Path: "/srv/two", Seq: 2})
	mustAdd(t, s, impl.Access{Op: impl.OpExec, Path: "/usr/bin/tool", Seq: 3})
	demand, err := s.Solve()
	if err != nil {
		t.Fatalf("solve: %v", err)
	}
	want := []string{impl.CapChmod, impl.CapChown, impl.CapExec}
	if !reflect.DeepEqual(demand.Caps, want) {
		t.Fatalf("caps = %v, want %v", demand.Caps, want)
	}
}

func TestDiffReportsMissingAndExtra(t *testing.T) {
	s := solver()
	mustAdd(t, s, openRead("/etc/app.conf", 1))
	mustAdd(t, s, impl.Access{Op: impl.OpOpen, Path: "/var/log/app.log", Mode: impl.ModeWrite, Seq: 2})
	demand, err := s.Solve()
	if err != nil {
		t.Fatalf("solve: %v", err)
	}
	current := impl.Profile{Caps: []string{impl.CapWriteFile, impl.CapChmod}}
	diff, err := s.Diff(demand, current)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if !reflect.DeepEqual(diff.Missing, []string{impl.CapReadFile}) {
		t.Fatalf("missing = %v, want [%s]", diff.Missing, impl.CapReadFile)
	}
	if !reflect.DeepEqual(diff.Extra, []string{impl.CapChmod}) {
		t.Fatalf("extra = %v, want [%s]", diff.Extra, impl.CapChmod)
	}
}

func TestDiffDedupesAndSortsCurrent(t *testing.T) {
	s := solver()
	mustAdd(t, s, impl.Access{Op: impl.OpChmod, Path: "/srv/one", Seq: 1})
	demand, err := s.Solve()
	if err != nil {
		t.Fatalf("solve: %v", err)
	}
	current := impl.Profile{Caps: []string{impl.CapExec, impl.CapChmod, impl.CapExec}}
	diff, err := s.Diff(demand, current)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(diff.Missing) != 0 {
		t.Fatalf("missing = %v, want none", diff.Missing)
	}
	if !reflect.DeepEqual(diff.Extra, []string{impl.CapExec}) {
		t.Fatalf("extra = %v, want [%s]", diff.Extra, impl.CapExec)
	}
}

func TestExplainReturnsAccessesInInsertionOrder(t *testing.T) {
	s := solver()
	mustAdd(t, s, openRead("/c", 1))
	mustAdd(t, s, openRead("/a", 2))
	mustAdd(t, s, openRead("/b", 3))
	explained := s.Explain(impl.CapReadFile)
	if len(explained) != 3 {
		t.Fatalf("explained = %+v, want three accesses", explained)
	}
	got := []string{explained[0].Path, explained[1].Path, explained[2].Path}
	if !reflect.DeepEqual(got, []string{"/c", "/a", "/b"}) {
		t.Fatalf("explain order = %v, want insertion order [/c /a /b]", got)
	}
}

func TestExplainUnknownCapabilityIsEmpty(t *testing.T) {
	s := solver()
	mustAdd(t, s, openRead("/a", 1))
	if _, err := s.Solve(); err != nil {
		t.Fatalf("solve: %v", err)
	}
	if got := s.Explain("cap_does_not_exist"); len(got) != 0 {
		t.Fatalf("explain unknown capability = %+v, want none", got)
	}
}

func TestSnapshotRestoreKeepsTrace(t *testing.T) {
	s := solver()
	mustAdd(t, s, openRead("/etc/app.conf", 1))
	mustAdd(t, s, impl.Access{Op: impl.OpChmod, Path: "/srv/one", Seq: 2})
	mustAdd(t, s, impl.Access{Op: impl.OpExec, Path: "/usr/bin/tool", Seq: 3})
	before, err := s.Solve()
	if err != nil {
		t.Fatalf("solve: %v", err)
	}
	payload, err := s.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(payload) == 0 {
		t.Fatalf("snapshot payload is empty")
	}
	restored := impl.NewCapabilityDemandSolver(impl.SolverConfig{MaxAccesses: 64})
	if err := restored.Restore(payload); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.Count(); got != 3 {
		t.Fatalf("restored count = %d, want 3", got)
	}
	after, err := restored.Solve()
	if err != nil {
		t.Fatalf("restored solve: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("restored demand differs:\n%+v\n%+v", before, after)
	}
	if err := restored.Restore([]byte("not a snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("restore garbage err = %v, want ErrBadSnapshot", err)
	}
}

func TestCountAccessesAndReset(t *testing.T) {
	s := solver()
	mustAdd(t, s, openRead("/first", 1))
	mustAdd(t, s, impl.Access{Op: impl.OpStat, Path: "/second", Seq: 2})
	if got := s.Count(); got != 2 {
		t.Fatalf("count = %d, want 2", got)
	}
	paths := []string{s.Accesses()[0].Path, s.Accesses()[1].Path}
	if !reflect.DeepEqual(paths, []string{"/first", "/second"}) {
		t.Fatalf("accesses = %v, want insertion order", paths)
	}
	if err := s.Reset(); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if got := s.Count(); got != 0 {
		t.Fatalf("count after reset = %d, want 0", got)
	}
	if _, err := s.Solve(); !errors.Is(err, impl.ErrEmptyTrace) {
		t.Fatalf("solve after reset err = %v, want ErrEmptyTrace", err)
	}
}

func TestConcurrentAddAndSolveStayConsistent(t *testing.T) {
	s := solver()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			path := []string{"/p0", "/p1", "/p2", "/p3", "/p4", "/p5", "/p6", "/p7"}[idx]
			if err := s.AddAccess(openRead(path, uint64(idx+1))); err != nil {
				t.Errorf("concurrent add: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if got := s.Count(); got != 8 {
		t.Fatalf("count = %d, want 8", got)
	}
	demand, err := s.Solve()
	if err != nil {
		t.Fatalf("solve: %v", err)
	}
	wantPaths := []string{"/p0", "/p1", "/p2", "/p3", "/p4", "/p5", "/p6", "/p7"}
	if !reflect.DeepEqual(demand.ByCap[impl.CapReadFile], wantPaths) {
		t.Fatalf("paths = %v, want %v", demand.ByCap[impl.CapReadFile], wantPaths)
	}
}
