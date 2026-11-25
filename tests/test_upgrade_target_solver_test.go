package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch49-go-safety-e-05"
)

var nginxReleases = []string{"1.0.0", "1.1.0", "1.2.0", "1.3.0", "1.4.0", "1.5.0", "1.6.0", "2.0.0"}

func seededSolver(t *testing.T, compat string) *impl.UpgradeTargetSolver {
	t.Helper()
	s := impl.NewUpgradeTargetSolver()
	if s == nil {
		t.Fatalf("constructor returned nil")
	}
	for _, v := range nginxReleases {
		if err := s.AddReleased("nginx-core", v); err != nil {
			t.Fatalf("add release %s: %v", v, err)
		}
	}
	rng := impl.Range{Component: "nginx-core", CVE: "CVE-2026-1111", Introduced: "1.2.0", Fixed: "1.5.0"}
	if err := s.AddRange(rng); err != nil {
		t.Fatalf("add range: %v", err)
	}
	if err := s.Compatibility("nginx-core", compat); err != nil {
		t.Fatalf("compatibility: %v", err)
	}
	return s
}

func stringSlicesEqual(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestAddRangeRejectsBlankComponent(t *testing.T) {
	s := impl.NewUpgradeTargetSolver()
	err := s.AddRange(impl.Range{Component: "  ", CVE: "CVE-2026-1", Introduced: "1.0.0", Fixed: "1.1.0"})
	if !errors.Is(err, impl.ErrBadRange) {
		t.Fatalf("blank component must report ErrBadRange, got %v", err)
	}
	if got := len(s.Components()); got != 0 {
		t.Fatalf("rejected range must not create a component, got %d", got)
	}
}

func TestAddRangeRejectsBadVersion(t *testing.T) {
	s := impl.NewUpgradeTargetSolver()
	err := s.AddRange(impl.Range{Component: "nginx-core", CVE: "CVE-2026-1", Introduced: "1.2", Fixed: "1.5.0"})
	if !errors.Is(err, impl.ErrBadVersion) {
		t.Fatalf("two segment version must report ErrBadVersion, got %v", err)
	}
}

func TestAddRangeRejectsInvertedRange(t *testing.T) {
	s := impl.NewUpgradeTargetSolver()
	err := s.AddRange(impl.Range{Component: "nginx-core", CVE: "CVE-2026-1", Introduced: "1.6.0", Fixed: "1.5.0"})
	if !errors.Is(err, impl.ErrBadRange) {
		t.Fatalf("introduced above fixed must report ErrBadRange, got %v", err)
	}
}

func TestAddReleasedRejectsBadVersion(t *testing.T) {
	s := impl.NewUpgradeTargetSolver()
	if err := s.AddReleased("log-agent", "1.2"); !errors.Is(err, impl.ErrBadVersion) {
		t.Fatalf("two segment release must report ErrBadVersion, got %v", err)
	}
	if got := len(s.Released("log-agent")); got != 0 {
		t.Fatalf("rejected release must not be stored, got %d", got)
	}
}

func TestAddReleasedDeduplicates(t *testing.T) {
	s := impl.NewUpgradeTargetSolver()
	for i := 0; i < 2; i++ {
		if err := s.AddReleased("log-agent", "2.3.4"); err != nil {
			t.Fatalf("add release %d: %v", i, err)
		}
	}
	got := s.Released("log-agent")
	if len(got) != 1 || got[0] != "2.3.4" {
		t.Fatalf("released = %v, want a single 2.3.4 entry", got)
	}
}

func TestReleasedSortedAscending(t *testing.T) {
	s := impl.NewUpgradeTargetSolver()
	for _, v := range []string{"1.5.0", "1.1.0", "1.3.0"} {
		if err := s.AddReleased("log-agent", v); err != nil {
			t.Fatalf("add release %s: %v", v, err)
		}
	}
	want := []string{"1.1.0", "1.3.0", "1.5.0"}
	if got := s.Released("log-agent"); !stringSlicesEqual(got, want) {
		t.Fatalf("released = %v, want ascending %v", got, want)
	}
}

func TestSolveRejectsUnknownComponent(t *testing.T) {
	s := impl.NewUpgradeTargetSolver()
	_, err := s.Solve(impl.Installed{Component: "ghost", Version: "1.0.0"})
	if !errors.Is(err, impl.ErrUnknownComponent) {
		t.Fatalf("unknown component must report ErrUnknownComponent, got %v", err)
	}
}

func TestSolvePicksSmallestSafeSameMajor(t *testing.T) {
	s := seededSolver(t, "1.6.0")
	target, err := s.Solve(impl.Installed{Component: "nginx-core", Version: "1.3.0"})
	if err != nil {
		t.Fatalf("solve: %v", err)
	}
	if target.To != "1.5.0" {
		t.Fatalf("target = %q, want the fixed boundary 1.5.0", target.To)
	}
	if !target.Moved {
		t.Fatalf("vulnerable install must move")
	}
	if target.CrossMajor {
		t.Fatalf("1.3.0 to 1.5.0 must stay inside the major version")
	}
	if target.Reason != impl.ReasonUpgrade {
		t.Fatalf("reason = %q, want %q", target.Reason, impl.ReasonUpgrade)
	}
	if target.From != "1.3.0" {
		t.Fatalf("from = %q, want 1.3.0", target.From)
	}
}

func TestIntroducedEndpointInclusive(t *testing.T) {
	s := seededSolver(t, "1.6.0")
	target, err := s.Solve(impl.Installed{Component: "nginx-core", Version: "1.2.0"})
	if err != nil {
		t.Fatalf("solve of the introduced boundary: %v", err)
	}
	if target.To != "1.5.0" {
		t.Fatalf("target = %q, want 1.5.0 because 1.2.0 is affected", target.To)
	}
}

func TestCandidatesFilters(t *testing.T) {
	s := seededSolver(t, "1.6.0")
	got := s.Candidates(impl.Installed{Component: "nginx-core", Version: "1.4.0"})
	want := []string{"1.5.0", "1.6.0"}
	if !stringSlicesEqual(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
}

func TestSolveRespectsCompatibilityLimit(t *testing.T) {
	s := seededSolver(t, "1.5.0")
	target, err := s.Solve(impl.Installed{Component: "nginx-core", Version: "1.3.0"})
	if err != nil {
		t.Fatalf("solve: %v", err)
	}
	if target.To != "1.5.0" {
		t.Fatalf("target = %q, want the compatible ceiling 1.5.0", target.To)
	}
	got := s.Candidates(impl.Installed{Component: "nginx-core", Version: "1.3.0"})
	want := []string{"1.5.0"}
	if !stringSlicesEqual(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
}

func TestNoFixFalseWhenSafeExists(t *testing.T) {
	s := seededSolver(t, "1.6.0")
	installed := impl.Installed{Component: "nginx-core", Version: "1.3.0"}
	risk, ok := s.NoFix(installed)
	if ok {
		t.Fatalf("safe target exists, NoFix must be false, got %+v", risk)
	}
	if risk.Reason == impl.ReasonNoFix {
		t.Fatalf("a solved install must not carry the no_fix reason")
	}
	if got := s.Candidates(installed); len(got) == 0 {
		t.Fatalf("safe candidates must exist for 1.3.0, got none")
	}
}

func TestSolveRejectsBlankInstalled(t *testing.T) {
	s := seededSolver(t, "1.6.0")
	if _, err := s.Solve(impl.Installed{Component: "nginx-core", Version: "   "}); !errors.Is(err, impl.ErrBadInstalled) {
		t.Fatalf("blank installed version must report ErrBadInstalled, got %v", err)
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	s := seededSolver(t, "1.6.0")
	data, err := s.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	fresh := impl.NewUpgradeTargetSolver()
	if err := fresh.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	target, err := fresh.Solve(impl.Installed{Component: "nginx-core", Version: "1.3.0"})
	if err != nil {
		t.Fatalf("solve after restore: %v", err)
	}
	if target.To != "1.5.0" {
		t.Fatalf("restored target = %q, want 1.5.0", target.To)
	}
	if err := fresh.Restore([]byte("broken")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must report ErrBadSnapshot, got %v", err)
	}
	target, err = fresh.Solve(impl.Installed{Component: "nginx-core", Version: "1.3.0"})
	if err != nil || target.To != "1.5.0" {
		t.Fatalf("rejected restore must not disturb live state, target=%+v err=%v", target, err)
	}
}

func TestConcurrentSolveDeterministic(t *testing.T) {
	s := seededSolver(t, "1.6.0")
	installed := impl.Installed{Component: "nginx-core", Version: "1.4.0"}
	baseline, err := s.Solve(installed)
	if err != nil {
		t.Fatalf("baseline solve: %v", err)
	}
	const workers = 8
	targets := make([]impl.Target, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			targets[idx], errs[idx] = s.Solve(installed)
		}(i)
	}
	wg.Wait()
	for i := 0; i < workers; i++ {
		if errs[i] != nil {
			t.Fatalf("worker %d solve: %v", i, errs[i])
		}
		if targets[i].To != baseline.To || targets[i].Reason != baseline.Reason {
			t.Fatalf("worker %d target = %+v, want %+v", i, targets[i], baseline)
		}
	}
	if baseline.To != "1.5.0" {
		t.Fatalf("baseline target = %q, want 1.5.0", baseline.To)
	}
}
