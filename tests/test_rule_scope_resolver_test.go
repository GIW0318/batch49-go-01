package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch49-go-safety-e-01"
)

func numValue(n int64, bound string) impl.Value {
	return impl.Value{Kind: impl.KindNum, Num: n, Bound: bound}
}

func enumValue(values ...string) impl.Value {
	return impl.Value{Kind: impl.KindEnum, Set: values}
}

func boolValue(v bool) impl.Value {
	return impl.Value{Kind: impl.KindBool, Bool: v}
}

// seedResolver builds the three layer tree used by most cases:
// product-base -> product-edge (declares nothing) -> product-edge-prod.
func seedResolver(t *testing.T) *impl.RuleScopeResolver {
	t.Helper()
	r := impl.NewRuleScopeResolver()
	if r == nil {
		t.Fatalf("constructor returned nil")
	}
	root := impl.Package{Name: "product-base", Values: map[string]impl.Value{
		"scan.threads":    numValue(100, impl.BoundMax),
		"scan.timeout.ms": numValue(5000, impl.BoundMax),
		"scan.mode":       enumValue("fast", "full"),
		"scan.upload":     boolValue(true),
	}}
	if err := r.Define(root); err != nil {
		t.Fatalf("define root package: %v", err)
	}
	if err := r.Define(impl.Package{Name: "product-edge", Parent: "product-base"}); err != nil {
		t.Fatalf("define middle package: %v", err)
	}
	leaf := impl.Package{Name: "product-edge-prod", Parent: "product-edge", Values: map[string]impl.Value{
		"scan.threads": numValue(40, impl.BoundMax),
		"scan.mode":    enumValue("full"),
	}}
	if err := r.Define(leaf); err != nil {
		t.Fatalf("define leaf package: %v", err)
	}
	return r
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

func TestDefineRejectsDuplicatePackage(t *testing.T) {
	r := impl.NewRuleScopeResolver()
	if err := r.Define(impl.Package{Name: "base"}); err != nil {
		t.Fatalf("first define must succeed, got %v", err)
	}
	err := r.Define(impl.Package{Name: "base"})
	if !errors.Is(err, impl.ErrDuplicatePackage) {
		t.Fatalf("second define of the same name must report ErrDuplicatePackage, got %v", err)
	}
	if got := len(r.Packages()); got != 1 {
		t.Fatalf("rejected define must not add a package, package count = %d", got)
	}
}

func TestDefineRejectsUnknownParent(t *testing.T) {
	r := impl.NewRuleScopeResolver()
	err := r.Define(impl.Package{Name: "child", Parent: "missing-parent"})
	if !errors.Is(err, impl.ErrUnknownPackage) {
		t.Fatalf("unknown parent must report ErrUnknownPackage, got %v", err)
	}
	if got := len(r.Packages()); got != 0 {
		t.Fatalf("rejected define must not leave a record, package count = %d", got)
	}
}

func TestDefineRejectsBlankName(t *testing.T) {
	r := impl.NewRuleScopeResolver()
	if err := r.Define(impl.Package{Name: "   "}); !errors.Is(err, impl.ErrBadPackage) {
		t.Fatalf("blank package name must report ErrBadPackage, got %v", err)
	}
}

func TestChainIsRootFirst(t *testing.T) {
	r := seedResolver(t)
	chain, err := r.Chain("product-edge-prod")
	if err != nil {
		t.Fatalf("chain of a defined leaf: %v", err)
	}
	want := []string{"product-base", "product-edge", "product-edge-prod"}
	if !stringSlicesEqual(chain, want) {
		t.Fatalf("chain = %v, want %v", chain, want)
	}
}

func TestResolveNarrowsAcrossChain(t *testing.T) {
	r := seedResolver(t)
	leaf, err := r.Resolve("product-edge-prod")
	if err != nil {
		t.Fatalf("resolve leaf: %v", err)
	}
	if got := leaf.Values["scan.threads"].Num; got != 40 {
		t.Fatalf("leaf scan.threads = %d, want the narrowed 40", got)
	}
	mode := leaf.Values["scan.mode"]
	if !stringSlicesEqual(mode.Set, []string{"full"}) {
		t.Fatalf("leaf scan.mode = %v, want the intersect [full]", mode.Set)
	}
	if !leaf.Values["scan.upload"].Bool {
		t.Fatalf("leaf scan.upload must inherit true from the root layer")
	}
	mid, err := r.Resolve("product-edge")
	if err != nil {
		t.Fatalf("resolve middle package: %v", err)
	}
	if got := mid.Values["scan.threads"].Num; got != 100 {
		t.Fatalf("middle package scan.threads = %d, want inherited 100", got)
	}
}

func TestOverrideRejectsWidening(t *testing.T) {
	r := seedResolver(t)
	err := r.Override("product-edge", "scan.threads", numValue(120, impl.BoundMax))
	if !errors.Is(err, impl.ErrWiden) {
		t.Fatalf("widening 100 back to 120 must report ErrWiden, got %v", err)
	}
	resolved, rerr := r.Resolve("product-edge")
	if rerr != nil {
		t.Fatalf("resolve after rejected override: %v", rerr)
	}
	if got := resolved.Values["scan.threads"].Num; got != 100 {
		t.Fatalf("rejected override must leave the value at 100, got %d", got)
	}
}

func TestOverrideAcceptsNarrowerValue(t *testing.T) {
	r := seedResolver(t)
	if err := r.Override("product-edge-prod", "scan.threads", numValue(20, impl.BoundMax)); err != nil {
		t.Fatalf("narrowing 40 to 20 must succeed, got %v", err)
	}
	resolved, err := r.Resolve("product-edge-prod")
	if err != nil {
		t.Fatalf("resolve after narrowing: %v", err)
	}
	if got := resolved.Values["scan.threads"].Num; got != 20 {
		t.Fatalf("effective scan.threads = %d, want 20", got)
	}
}

func TestOverrideBoolOnlyTurnsOff(t *testing.T) {
	r := seedResolver(t)
	if err := r.Override("product-edge-prod", "scan.upload", boolValue(false)); err != nil {
		t.Fatalf("turning the switch off must succeed, got %v", err)
	}
	err := r.Override("product-edge-prod", "scan.upload", boolValue(true))
	if !errors.Is(err, impl.ErrWiden) {
		t.Fatalf("turning the switch back on must report ErrWiden, got %v", err)
	}
}

func TestOverrideRejectsUnknownTargets(t *testing.T) {
	r := seedResolver(t)
	if err := r.Override("product-edge-prod", "scan.missing", numValue(1, impl.BoundMax)); !errors.Is(err, impl.ErrUnknownKey) {
		t.Fatalf("override of an undeclared key must report ErrUnknownKey, got %v", err)
	}
	if err := r.Override("no-such-package", "scan.threads", numValue(1, impl.BoundMax)); !errors.Is(err, impl.ErrUnknownPackage) {
		t.Fatalf("override of an unknown package must report ErrUnknownPackage, got %v", err)
	}
}

func TestExplainListsEveryLayer(t *testing.T) {
	r := seedResolver(t)
	layers := r.Explain("product-edge-prod", "scan.threads")
	if len(layers) != 3 {
		t.Fatalf("explain must return one entry per chain layer, got %d", len(layers))
	}
	wantPackages := []string{"product-base", "product-edge", "product-edge-prod"}
	wantActions := []string{impl.ActionDeclare, impl.ActionInherit, impl.ActionNarrow}
	for i := range layers {
		if layers[i].Package != wantPackages[i] {
			t.Fatalf("layer %d package = %q, want %q", i, layers[i].Package, wantPackages[i])
		}
		if layers[i].Action != wantActions[i] {
			t.Fatalf("layer %d action = %q, want %q", i, layers[i].Action, wantActions[i])
		}
	}
	if got := layers[2].Value.Num; got != 40 {
		t.Fatalf("last layer value = %d, want 40", got)
	}
}

func TestKeysAreSorted(t *testing.T) {
	r := seedResolver(t)
	keys, err := r.Keys("product-edge-prod")
	if err != nil {
		t.Fatalf("keys: %v", err)
	}
	want := []string{"scan.mode", "scan.threads", "scan.timeout.ms", "scan.upload"}
	if !stringSlicesEqual(keys, want) {
		t.Fatalf("keys = %v, want sorted %v", keys, want)
	}
}

func TestMatchWildcardIsSorted(t *testing.T) {
	r := seedResolver(t)
	matches, err := r.Match("product-edge-prod", "scan.t*")
	if err != nil {
		t.Fatalf("match scan.t*: %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("scan.t* must select two keys, got %d", len(matches))
	}
	if matches[0].Key != "scan.threads" || matches[0].Value.Num != 40 {
		t.Fatalf("first match = %q/%d, want scan.threads/40", matches[0].Key, matches[0].Value.Num)
	}
	if matches[1].Key != "scan.timeout.ms" || matches[1].Value.Num != 5000 {
		t.Fatalf("second match = %q/%d, want scan.timeout.ms/5000", matches[1].Key, matches[1].Value.Num)
	}
	if len(matches[1].Layers) != 3 {
		t.Fatalf("match must carry the full layer trace, got %d layers", len(matches[1].Layers))
	}
}

func TestDigestIsStable(t *testing.T) {
	r := seedResolver(t)
	first, err := r.Digest("product-edge-prod")
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if len(first) != 64 {
		t.Fatalf("digest length = %d, want 64 hex characters", len(first))
	}
	for i := 0; i < len(first); i++ {
		c := first[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			t.Fatalf("digest must be lowercase hex, offending byte %q", c)
		}
	}
	second, err := r.Digest("product-edge-prod")
	if err != nil {
		t.Fatalf("second digest: %v", err)
	}
	if first != second {
		t.Fatalf("repeated digest changed: %s vs %s", first, second)
	}
}

func TestConcurrentResolveSharesDigest(t *testing.T) {
	r := seedResolver(t)
	baseline, err := r.Digest("product-edge-prod")
	if err != nil {
		t.Fatalf("baseline digest: %v", err)
	}
	const workers = 8
	results := make([]string, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			resolved, rerr := r.Resolve("product-edge-prod")
			if rerr != nil {
				errs[idx] = rerr
				return
			}
			if resolved.Values["scan.threads"].Num != 40 {
				errs[idx] = errors.New("concurrent resolve saw a different value")
				return
			}
			results[idx], errs[idx] = r.Digest("product-edge-prod")
		}(i)
	}
	wg.Wait()
	for i := 0; i < workers; i++ {
		if errs[i] != nil {
			t.Fatalf("worker %d failed: %v", i, errs[i])
		}
		if results[i] != baseline {
			t.Fatalf("worker %d digest = %s, want %s", i, results[i], baseline)
		}
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	r := seedResolver(t)
	data, err := r.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	fresh := impl.NewRuleScopeResolver()
	if err := fresh.Restore(data); err != nil {
		t.Fatalf("restore of a valid snapshot: %v", err)
	}
	resolved, err := fresh.Resolve("product-edge-prod")
	if err != nil {
		t.Fatalf("resolve after restore: %v", err)
	}
	if got := resolved.Values["scan.threads"].Num; got != 40 {
		t.Fatalf("restored scan.threads = %d, want 40", got)
	}
	if err := fresh.Restore([]byte("not a snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must report ErrBadSnapshot, got %v", err)
	}
	resolved, err = fresh.Resolve("product-edge-prod")
	if err != nil {
		t.Fatalf("resolve after rejected restore: %v", err)
	}
	if got := resolved.Values["scan.threads"].Num; got != 40 {
		t.Fatalf("rejected restore must not disturb live state, got %d", got)
	}
}
