package task

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	impl "gsb/batch49-go-safety-e-06"
)

func day(n int) time.Time {
	return time.Date(2026, 1, 10, 9, 0, 0, 0, time.UTC).Add(time.Duration(n) * 24 * time.Hour)
}

func mustRelease(t *testing.T, a *impl.BuildVersionAttributor, component, version string, at time.Time) {
	t.Helper()
	if err := a.RegisterRelease(component, version, at); err != nil {
		t.Fatalf("register %s %s: %v", component, version, err)
	}
}

func TestRegisterReleaseRejectsBlankFields(t *testing.T) {
	a := impl.NewBuildVersionAttributor()
	if err := a.RegisterRelease("", "1.0.0", day(0)); !errors.Is(err, impl.ErrBlankField) {
		t.Fatalf("blank component must fail with ErrBlankField, got %v", err)
	}
	if err := a.RegisterRelease("svc", "", day(0)); !errors.Is(err, impl.ErrBlankField) {
		t.Fatalf("blank version must fail with ErrBlankField, got %v", err)
	}
	if err := a.RegisterRelease("   ", "1.0.0", day(0)); !errors.Is(err, impl.ErrBlankField) {
		t.Fatalf("whitespace component must fail with ErrBlankField, got %v", err)
	}
	if got := len(a.Timeline("svc")); got != 0 {
		t.Fatalf("rejected registrations must not enter the timeline, got %d releases", got)
	}
}

func TestRegisterReleaseRejectsDuplicateVersion(t *testing.T) {
	a := impl.NewBuildVersionAttributor()
	mustRelease(t, a, "svc", "1.0.0", day(0))
	if err := a.RegisterRelease("svc", "1.0.0", day(1)); !errors.Is(err, impl.ErrDuplicateRelease) {
		t.Fatalf("duplicate version must fail with ErrDuplicateRelease, got %v", err)
	}
	if got := len(a.Timeline("svc")); got != 1 {
		t.Fatalf("duplicate registration must be ignored, timeline has %d releases", got)
	}
	mustRelease(t, a, "other", "1.0.0", day(0))
}

func TestTimelineOrdersByReleaseTime(t *testing.T) {
	a := impl.NewBuildVersionAttributor()
	mustRelease(t, a, "svc", "1.0.0", day(2))
	mustRelease(t, a, "svc", "1.1.0", day(0))
	mustRelease(t, a, "svc", "1.2.0-rc1", day(1))
	tl := a.Timeline("svc")
	if len(tl) != 3 {
		t.Fatalf("timeline length = %d, want 3", len(tl))
	}
	want := []string{"1.1.0", "1.2.0-rc1", "1.0.0"}
	for i, version := range want {
		if tl[i].Version != version {
			t.Fatalf("timeline[%d] = %q, want %q (full order %+v)", i, tl[i].Version, version, tl)
		}
	}
	if tl[1].Prerelease != true {
		t.Fatalf("1.2.0-rc1 must be flagged as a prerelease")
	}
	if tl[0].Prerelease || tl[2].Prerelease {
		t.Fatalf("formal releases must not be flagged as prereleases")
	}
	if latest, ok := a.Latest("svc"); !ok || latest.Version != "1.0.0" {
		t.Fatalf("latest = %+v ok=%v, want 1.0.0", latest, ok)
	}
}

func TestAttributeTrustedTimePicksNearestRelease(t *testing.T) {
	a := impl.NewBuildVersionAttributor()
	mustRelease(t, a, "svc", "1.0.0", day(0))
	mustRelease(t, a, "svc", "1.1.0", day(1))
	mustRelease(t, a, "svc", "1.2.0", day(3))
	got, err := a.Attribute(impl.BuildRef{ID: "b-1", Component: "svc", BuiltAt: day(1).Add(6 * time.Hour), ClockTrusted: true})
	if err != nil {
		t.Fatalf("attribute build: %v", err)
	}
	if got.Version != "1.1.0" || got.Basis != impl.BasisTime || got.Confidence != impl.ConfidenceTime || got.Outside {
		t.Fatalf("attribution = %+v, want version 1.1.0 basis time confidence %d outside false", got, impl.ConfidenceTime)
	}
}

func TestAttributeBeforeFirstReleaseIsOutside(t *testing.T) {
	a := impl.NewBuildVersionAttributor()
	mustRelease(t, a, "svc", "1.0.0", day(1))
	got, err := a.Attribute(impl.BuildRef{ID: "b-old", Component: "svc", BuiltAt: day(0), ClockTrusted: true})
	if err != nil {
		t.Fatalf("a build before the first release must still be attributed, got %v", err)
	}
	if !got.Outside || got.Basis != impl.BasisBeforeTimeline {
		t.Fatalf("attribution = %+v, want outside before_timeline", got)
	}
	if got.Version != "" {
		t.Fatalf("unlocated build version = %q, want empty", got.Version)
	}
	if got.Confidence != 0 {
		t.Fatalf("unlocated build confidence = %d, want 0", got.Confidence)
	}
}

func TestAttributeAfterLatestReleaseIsMarked(t *testing.T) {
	a := impl.NewBuildVersionAttributor()
	mustRelease(t, a, "svc", "1.0.0", day(0))
	mustRelease(t, a, "svc", "1.1.0", day(1))
	got, err := a.Attribute(impl.BuildRef{ID: "b-new", Component: "svc", BuiltAt: day(2), ClockTrusted: true})
	if err != nil {
		t.Fatalf("attribute build: %v", err)
	}
	if !got.Outside || got.Basis != impl.BasisAfterTimeline {
		t.Fatalf("attribution = %+v, want outside after_timeline", got)
	}
	if got.Version != "1.1.0" || got.Confidence != impl.ConfidenceAfterTimeline {
		t.Fatalf("attribution = %+v, want version 1.1.0 confidence %d", got, impl.ConfidenceAfterTimeline)
	}
}

func TestAttributeDeclaredVersionInsideTimeline(t *testing.T) {
	a := impl.NewBuildVersionAttributor()
	mustRelease(t, a, "svc", "1.0.0", day(0))
	mustRelease(t, a, "svc", "1.1.0", day(2))
	got, err := a.Attribute(impl.BuildRef{ID: "b-dec", Component: "svc", Version: "1.0.0", BuiltAt: day(1), ClockTrusted: true})
	if err != nil {
		t.Fatalf("attribute declared build: %v", err)
	}
	if got.Basis != impl.BasisDeclared || got.Confidence != impl.ConfidenceDeclared || got.Version != "1.0.0" || got.Outside {
		t.Fatalf("attribution = %+v, want declared 1.0.0 confidence %d inside", got, impl.ConfidenceDeclared)
	}
}

func TestAttributeDeclaredVersionConflictsWithTimeline(t *testing.T) {
	a := impl.NewBuildVersionAttributor()
	mustRelease(t, a, "svc", "1.0.0", day(0))
	mustRelease(t, a, "svc", "1.1.0", day(2))
	_, err := a.Attribute(impl.BuildRef{ID: "b-conf", Component: "svc", Version: "1.1.0", BuiltAt: day(1), ClockTrusted: true})
	if !errors.Is(err, impl.ErrTimelineConflict) {
		t.Fatalf("declared 1.1.0 built before its release must fail with ErrTimelineConflict, got %v", err)
	}
	if _, ok := a.Lookup("b-conf"); ok {
		t.Fatalf("a conflicting attribution must not be stored")
	}
}

func TestAttributeUnknownDeclaredVersionFails(t *testing.T) {
	a := impl.NewBuildVersionAttributor()
	mustRelease(t, a, "svc", "1.0.0", day(0))
	_, err := a.Attribute(impl.BuildRef{ID: "b-u", Component: "svc", Version: "9.9.9", BuiltAt: day(0), ClockTrusted: true})
	if !errors.Is(err, impl.ErrUnknownVersion) {
		t.Fatalf("unknown declared version must fail with ErrUnknownVersion, got %v", err)
	}
	if _, err := a.Attribute(impl.BuildRef{ID: "b-c", Component: "missing", BuiltAt: day(0), ClockTrusted: true}); !errors.Is(err, impl.ErrUnknownComponent) {
		t.Fatalf("unknown component must fail with ErrUnknownComponent, got %v", err)
	}
}

func TestConcurrentRegisterKeepsUniqueSequence(t *testing.T) {
	a := impl.NewBuildVersionAttributor()
	var wg sync.WaitGroup
	var mu sync.Mutex
	failures := 0
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := a.RegisterRelease("svc", fmt.Sprintf("1.0.%d", i), day(0).Add(time.Duration(i)*time.Minute))
			if err != nil {
				mu.Lock()
				failures++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if failures != 0 {
		t.Fatalf("%d concurrent registrations failed", failures)
	}
	tl := a.Timeline("svc")
	if len(tl) != 12 {
		t.Fatalf("timeline length = %d, want 12", len(tl))
	}
	seen := map[int]bool{}
	for _, rel := range tl {
		if rel.Seq <= 0 {
			t.Fatalf("release %s has non positive sequence %d", rel.Version, rel.Seq)
		}
		if seen[rel.Seq] {
			t.Fatalf("sequence %d was handed out twice", rel.Seq)
		}
		seen[rel.Seq] = true
	}
}

func TestAffectedWithinRange(t *testing.T) {
	a := impl.NewBuildVersionAttributor()
	mustRelease(t, a, "svc", "1.0.0", day(0))
	mustRelease(t, a, "svc", "1.1.0", day(1))
	mustRelease(t, a, "svc", "1.2.0", day(2))
	v, err := a.Affected(impl.BuildRef{ID: "b-aff", Component: "svc", BuiltAt: day(1).Add(time.Hour), ClockTrusted: true}, []impl.Range{{Introduced: "1.0.0", Fixed: "1.2.0"}})
	if err != nil {
		t.Fatalf("affected: %v", err)
	}
	if !v.Affected {
		t.Fatalf("1.1.0 inside [1.0.0,1.2.0) must be affected, got %+v", v)
	}
	if v.Introduced != "1.0.0" || v.Fixed != "1.2.0" {
		t.Fatalf("verdict must echo the matched range, got %+v", v)
	}
	if v.Confidence != impl.ConfidenceTime || v.Basis != impl.BasisTime {
		t.Fatalf("verdict basis/confidence = %s/%d, want time/%d", v.Basis, v.Confidence, impl.ConfidenceTime)
	}
	if v.Reason != impl.ReasonInRange {
		t.Fatalf("verdict reason = %q, want %q", v.Reason, impl.ReasonInRange)
	}
}

func TestAffectedFixedBoundaryIsExclusive(t *testing.T) {
	a := impl.NewBuildVersionAttributor()
	mustRelease(t, a, "svc", "1.0.0", day(0))
	mustRelease(t, a, "svc", "1.2.0", day(2))
	v, err := a.Affected(impl.BuildRef{ID: "b-fix", Component: "svc", BuiltAt: day(2), ClockTrusted: true}, []impl.Range{{Introduced: "1.0.0", Fixed: "1.2.0"}})
	if err != nil {
		t.Fatalf("affected: %v", err)
	}
	if v.Affected {
		t.Fatalf("version equal to the fixed endpoint must not be affected, got %+v", v)
	}
	if v.Reason != impl.ReasonOutsideRanges {
		t.Fatalf("verdict reason = %q, want %q", v.Reason, impl.ReasonOutsideRanges)
	}
}

func TestAffectedOpenEndedRangeStaysAffected(t *testing.T) {
	a := impl.NewBuildVersionAttributor()
	mustRelease(t, a, "svc", "1.0.0", day(0))
	mustRelease(t, a, "svc", "1.1.0", day(1))
	v, err := a.Affected(impl.BuildRef{ID: "b-open", Component: "svc", BuiltAt: day(1), ClockTrusted: true}, []impl.Range{{Introduced: "1.0.0"}})
	if err != nil {
		t.Fatalf("affected: %v", err)
	}
	if !v.Affected || v.Fixed != "" {
		t.Fatalf("an empty fixed endpoint means affected forever, got %+v", v)
	}
}

func TestAffectedUnlocatedBuildIsConservative(t *testing.T) {
	a := impl.NewBuildVersionAttributor()
	mustRelease(t, a, "svc", "1.0.0", day(1))
	v, err := a.Affected(impl.BuildRef{ID: "b-unknown", Component: "svc", BuiltAt: day(0), ClockTrusted: true}, []impl.Range{{Introduced: "1.0.0", Fixed: "1.2.0"}})
	if err != nil {
		t.Fatalf("unlocated build: %v", err)
	}
	if !v.Affected {
		t.Fatalf("an unlocated build must be treated as affected, got %+v", v)
	}
	if v.Confidence != impl.ConfidenceUnlocated {
		t.Fatalf("unlocated confidence = %d, want %d", v.Confidence, impl.ConfidenceUnlocated)
	}
	if v.Basis != impl.BasisBeforeTimeline {
		t.Fatalf("unlocated basis = %q, want %q", v.Basis, impl.BasisBeforeTimeline)
	}
}

func TestLookupAndExplainTrace(t *testing.T) {
	a := impl.NewBuildVersionAttributor()
	mustRelease(t, a, "svc", "1.0.0", day(0))
	if _, err := a.Attribute(impl.BuildRef{ID: "b-exp", Component: "svc", BuiltAt: day(0), ClockTrusted: true}); err != nil {
		t.Fatalf("attribute: %v", err)
	}
	first, ok := a.Lookup("b-exp")
	if !ok {
		t.Fatalf("lookup must find the stored attribution")
	}
	second, err := a.Attribute(impl.BuildRef{ID: "b-exp", Component: "svc", BuiltAt: day(0), ClockTrusted: true})
	if err != nil {
		t.Fatalf("repeat attribute: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("repeat attribution changed: first %+v second %+v", first, second)
	}
	steps := a.Explain("b-exp")
	if len(steps) != 3 {
		t.Fatalf("explain returned %d steps, want 3", len(steps))
	}
	wantActions := []string{"basis", "version", "boundary"}
	for i, step := range steps {
		if step.Ordinal != i+1 {
			t.Fatalf("step %d ordinal = %d", i, step.Ordinal)
		}
		if step.Action != wantActions[i] {
			t.Fatalf("step %d action = %q, want %q", i, step.Action, wantActions[i])
		}
	}
	if steps[0].Detail != impl.BasisTime {
		t.Fatalf("basis step detail = %q, want %q", steps[0].Detail, impl.BasisTime)
	}
	if len(a.Explain("missing")) != 0 {
		t.Fatalf("unknown attribution must have no explanation")
	}
}
