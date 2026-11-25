package task

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	impl "gsb/batch49-go-safety-d-07"
)

func baseline() impl.Profile {
	return impl.Profile{
		Default: impl.ActionErrno,
		Arch:    "amd64",
		Rules: []impl.Rule{
			{Name: "write", Number: 1},
			{Name: "openat", Number: 257},
			{Name: "read", Number: 0},
		},
	}
}

func TestComposeNarrowsToCommonNames(t *testing.T) {
	l := impl.NewSeccompProfileLattice()
	overlay := impl.Profile{
		Default: impl.ActionErrno,
		Arch:    "amd64",
		Rules: []impl.Rule{
			{Name: "openat", Number: 257},
			{Name: "write", Number: 1},
		},
	}
	got, err := l.Compose(baseline(), overlay)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	names, err := l.Names(got)
	if err != nil {
		t.Fatalf("names: %v", err)
	}
	if !reflect.DeepEqual(names, []string{"openat", "write"}) {
		t.Fatalf("composed names = %v, want [openat write]", names)
	}
	if len(got.Rules) != 2 {
		t.Fatalf("composed rule count = %d, want 2", len(got.Rules))
	}
	if got.Default != impl.ActionErrno {
		t.Fatalf("composed default = %q, want %q", got.Default, impl.ActionErrno)
	}
}

func TestComposeRejectsCallNameMissingFromBase(t *testing.T) {
	l := impl.NewSeccompProfileLattice()
	overlay := impl.Profile{
		Default: impl.ActionErrno,
		Arch:    "amd64",
		Rules: []impl.Rule{
			{Name: "openat", Number: 257},
			{Name: "mount", Number: 165},
		},
	}
	if _, err := l.Compose(baseline(), overlay); !errors.Is(err, impl.ErrWidenAttempt) {
		t.Fatalf("compose err = %v, want ErrWidenAttempt", err)
	}
}

func TestComposeIntersectsCompatibleArgConditions(t *testing.T) {
	l := impl.NewSeccompProfileLattice()
	base := impl.Profile{
		Default: impl.ActionErrno,
		Arch:    "amd64",
		Rules: []impl.Rule{
			{Name: "openat", Number: 257, Args: []impl.ArgFilter{{Index: 1, Op: impl.OpEq, Value: 0}}},
		},
	}
	overlay := impl.Profile{
		Default: impl.ActionErrno,
		Arch:    "amd64",
		Rules: []impl.Rule{
			{Name: "openat", Number: 257, Args: []impl.ArgFilter{
				{Index: 1, Op: impl.OpEq, Value: 0},
				{Index: 2, Op: impl.OpNe, Value: 5},
			}},
		},
	}
	got, err := l.Compose(base, overlay)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	rule, ok, err := l.Lookup(got, "openat")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if !ok {
		t.Fatalf("openat missing from composed profile")
	}
	want := []impl.ArgFilter{
		{Index: 1, Op: impl.OpEq, Value: 0},
		{Index: 2, Op: impl.OpNe, Value: 5},
	}
	if !reflect.DeepEqual(rule.Args, want) {
		t.Fatalf("composed args = %+v, want %+v", rule.Args, want)
	}
	if rule.Number != 257 {
		t.Fatalf("composed openat number = %d, want 257", rule.Number)
	}
}

func TestComposeCanonicalIsOrderIndependent(t *testing.T) {
	l := impl.NewSeccompProfileLattice()
	ordered := baseline()
	shuffled := impl.Profile{
		Default: impl.ActionErrno,
		Arch:    "amd64",
		Rules: []impl.Rule{
			{Name: "read", Number: 0},
			{Name: "write", Number: 1},
			{Name: "openat", Number: 257},
		},
	}
	c1, err := l.Canonical(ordered)
	if err != nil {
		t.Fatalf("canonical ordered: %v", err)
	}
	c2, err := l.Canonical(shuffled)
	if err != nil {
		t.Fatalf("canonical shuffled: %v", err)
	}
	if c1 != c2 {
		t.Fatalf("canonical differs by rule order:\n%s\n%s", c1, c2)
	}
	if strings.Index(c1, "openat") > strings.Index(c1, "read") || strings.Index(c1, "read") > strings.Index(c1, "write") {
		t.Fatalf("canonical does not list call names in ascending order: %s", c1)
	}
	merged1, err := l.Compose(ordered, ordered)
	if err != nil {
		t.Fatalf("compose ordered: %v", err)
	}
	merged2, err := l.Compose(shuffled, shuffled)
	if err != nil {
		t.Fatalf("compose shuffled: %v", err)
	}
	cm1, err := l.Canonical(merged1)
	if err != nil {
		t.Fatalf("canonical merged1: %v", err)
	}
	cm2, err := l.Canonical(merged2)
	if err != nil {
		t.Fatalf("canonical merged2: %v", err)
	}
	if cm1 != cm2 {
		t.Fatalf("compose output depends on input rule order:\n%s\n%s", cm1, cm2)
	}
}

func TestValidateRejectsEmptyAllowSet(t *testing.T) {
	l := impl.NewSeccompProfileLattice()
	if err := l.Validate(impl.Profile{Default: impl.ActionErrno, Arch: "amd64"}); !errors.Is(err, impl.ErrEmptyAllowSet) {
		t.Fatalf("validate empty allow set err = %v, want ErrEmptyAllowSet", err)
	}
}

func TestValidateRejectsUnknownDefaultAction(t *testing.T) {
	l := impl.NewSeccompProfileLattice()
	bad := impl.Profile{
		Default: impl.Action("log"),
		Arch:    "amd64",
		Rules:   []impl.Rule{{Name: "read", Number: 0}},
	}
	if err := l.Validate(bad); !errors.Is(err, impl.ErrBadDefaultAction) {
		t.Fatalf("validate unknown default err = %v, want ErrBadDefaultAction", err)
	}
}

func TestValidateRejectsDuplicateBlankAndBadOp(t *testing.T) {
	l := impl.NewSeccompProfileLattice()
	dup := impl.Profile{
		Default: impl.ActionErrno,
		Arch:    "amd64",
		Rules:   []impl.Rule{{Name: "read", Number: 0}, {Name: "read", Number: 0}},
	}
	if err := l.Validate(dup); !errors.Is(err, impl.ErrDuplicateRule) {
		t.Fatalf("validate duplicate err = %v, want ErrDuplicateRule", err)
	}
	blank := impl.Profile{
		Default: impl.ActionErrno,
		Arch:    "amd64",
		Rules:   []impl.Rule{{Name: "", Number: 0}},
	}
	if err := l.Validate(blank); !errors.Is(err, impl.ErrBadRuleName) {
		t.Fatalf("validate blank name err = %v, want ErrBadRuleName", err)
	}
	badOp := impl.Profile{
		Default: impl.ActionErrno,
		Arch:    "amd64",
		Rules: []impl.Rule{{Name: "read", Number: 0, Args: []impl.ArgFilter{
			{Index: 0, Op: "gt", Value: 1},
		}}},
	}
	if err := l.Validate(badOp); !errors.Is(err, impl.ErrBadArgOp) {
		t.Fatalf("validate bad operator err = %v, want ErrBadArgOp", err)
	}
}

func TestAllowedReportsAllowSet(t *testing.T) {
	l := impl.NewSeccompProfileLattice()
	p := baseline()
	allowed, err := l.Allowed(p, "openat")
	if err != nil {
		t.Fatalf("allowed openat: %v", err)
	}
	if !allowed {
		t.Fatalf("openat must be allowed")
	}
	allowed, err = l.Allowed(p, "mount")
	if err != nil {
		t.Fatalf("allowed mount: %v", err)
	}
	if allowed {
		t.Fatalf("mount must not be allowed by the baseline")
	}
	if _, err := l.Allowed(p, ""); !errors.Is(err, impl.ErrBadRuleName) {
		t.Fatalf("allowed blank name err = %v, want ErrBadRuleName", err)
	}
}

func TestNamesAreSorted(t *testing.T) {
	l := impl.NewSeccompProfileLattice()
	names, err := l.Names(baseline())
	if err != nil {
		t.Fatalf("names: %v", err)
	}
	if !reflect.DeepEqual(names, []string{"openat", "read", "write"}) {
		t.Fatalf("names = %v, want [openat read write]", names)
	}
}

func TestDefaultActionReturnsProfileAction(t *testing.T) {
	l := impl.NewSeccompProfileLattice()
	action, err := l.DefaultAction(baseline())
	if err != nil {
		t.Fatalf("default action: %v", err)
	}
	if action != impl.ActionErrno {
		t.Fatalf("default action = %q, want %q", action, impl.ActionErrno)
	}
	strict := impl.Profile{
		Default: impl.ActionKill,
		Arch:    "amd64",
		Rules:   []impl.Rule{{Name: "read", Number: 0}},
	}
	action, err = l.DefaultAction(strict)
	if err != nil {
		t.Fatalf("default action of strict profile: %v", err)
	}
	if action != impl.ActionKill {
		t.Fatalf("default action = %q, want %q", action, impl.ActionKill)
	}
}

func TestLookupReturnsRuleAndNumber(t *testing.T) {
	l := impl.NewSeccompProfileLattice()
	rule, ok, err := l.Lookup(baseline(), "openat")
	if err != nil {
		t.Fatalf("lookup openat: %v", err)
	}
	if !ok {
		t.Fatalf("openat must be found in the baseline")
	}
	if rule.Name != "openat" || rule.Number != 257 {
		t.Fatalf("lookup openat = %+v, want name openat number 257", rule)
	}
	rule, ok, err = l.Lookup(baseline(), "mount")
	if err != nil {
		t.Fatalf("lookup mount: %v", err)
	}
	if ok {
		t.Fatalf("mount returned %+v, want not found", rule)
	}
}

func TestTranslateMapsNamesToTargetNumbers(t *testing.T) {
	l := impl.NewSeccompProfileLattice()
	got, err := l.Translate(baseline(), "arm64")
	if err != nil {
		t.Fatalf("translate to arm64: %v", err)
	}
	if got.Arch != "arm64" {
		t.Fatalf("translated arch = %q, want arm64", got.Arch)
	}
	if got.Default != impl.ActionErrno {
		t.Fatalf("translated default = %q, want %q", got.Default, impl.ActionErrno)
	}
	for name, want := range map[string]int{"openat": 56, "read": 63, "write": 64} {
		rule, ok, err := l.Lookup(got, name)
		if err != nil {
			t.Fatalf("lookup %s: %v", name, err)
		}
		if !ok {
			t.Fatalf("%s missing after translation", name)
		}
		if rule.Number != want {
			t.Fatalf("%s arm64 number = %d, want %d", name, rule.Number, want)
		}
	}
	names, err := l.Names(got)
	if err != nil {
		t.Fatalf("names after translation: %v", err)
	}
	if !reflect.DeepEqual(names, []string{"openat", "read", "write"}) {
		t.Fatalf("names after translation = %v", names)
	}
}

func TestNarrowReportsRemovalAsNarrowing(t *testing.T) {
	l := impl.NewSeccompProfileLattice()
	prev := baseline()
	next := impl.Profile{
		Default: impl.ActionErrno,
		Arch:    "amd64",
		Rules: []impl.Rule{
			{Name: "write", Number: 1},
			{Name: "openat", Number: 257},
		},
	}
	diff, err := l.Narrow(prev, next)
	if err != nil {
		t.Fatalf("narrow: %v", err)
	}
	if !reflect.DeepEqual(diff.Removed, []string{"read"}) {
		t.Fatalf("diff.Removed = %v, want [read]", diff.Removed)
	}
	if len(diff.Added) != 0 {
		t.Fatalf("diff.Added = %v, want none", diff.Added)
	}
	if !diff.Narrowed {
		t.Fatalf("diff.Narrowed = false, want true")
	}
	if diff.DefaultChanged {
		t.Fatalf("diff.DefaultChanged = true, want false when both sides use %q", impl.ActionErrno)
	}
}

func TestNarrowRejectsAddedCallName(t *testing.T) {
	l := impl.NewSeccompProfileLattice()
	prev := baseline()
	next := baseline()
	next.Rules = append(next.Rules, impl.Rule{Name: "mount", Number: 165})
	diff, err := l.Narrow(prev, next)
	if !errors.Is(err, impl.ErrWidenAttempt) {
		t.Fatalf("narrow widening err = %v, want ErrWidenAttempt", err)
	}
	if !reflect.DeepEqual(diff.Added, []string{"mount"}) {
		t.Fatalf("diff.Added = %v, want [mount]", diff.Added)
	}
}

func TestConcurrentComposeIsStable(t *testing.T) {
	l := impl.NewSeccompProfileLattice()
	base := baseline()
	overlay := impl.Profile{
		Default: impl.ActionErrno,
		Arch:    "amd64",
		Rules: []impl.Rule{
			{Name: "read", Number: 0},
			{Name: "write", Number: 1},
		},
	}
	want, err := l.Compose(base, overlay)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	wantCanonical, err := l.Canonical(want)
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := l.Compose(base, overlay)
			if err != nil {
				t.Errorf("concurrent compose: %v", err)
				return
			}
			canonical, err := l.Canonical(got)
			if err != nil {
				t.Errorf("concurrent canonical: %v", err)
				return
			}
			if canonical != wantCanonical {
				t.Errorf("concurrent compose drifted:\n%s\n%s", canonical, wantCanonical)
			}
		}()
	}
	wg.Wait()
	if len(want.Rules) != 2 {
		t.Fatalf("composed rule count = %d, want 2", len(want.Rules))
	}
}
