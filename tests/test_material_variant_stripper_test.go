package task

import (
	"errors"
	"testing"

	impl "gsb/batch49-go-game-b-07"
)

func declaredStripper(t *testing.T) *impl.VariantStripper {
	t.Helper()
	s := impl.NewVariantStripper()
	decls := []struct {
		name  string
		group string
		deps  []string
	}{
		{"fog", "weather", nil},
		{"rain", "weather", nil},
		{"shadow", "light", nil},
		{"snow", "weather", []string{"fog"}},
		{"storm", "sky", []string{"snow"}},
		{"wet", "surface", []string{"rain"}},
	}
	for _, d := range decls {
		if err := s.DeclareKeyword(d.name, d.group, d.deps); err != nil {
			t.Fatalf("declare %s: %v", d.name, err)
		}
	}
	return s
}

func plainStripper(t *testing.T) *impl.VariantStripper {
	t.Helper()
	s := impl.NewVariantStripper()
	for i, name := range []string{"a", "b", "c"} {
		if err := s.DeclareKeyword(name, "g"+name, nil); err != nil {
			t.Fatalf("declare %s (%d): %v", name, i, err)
		}
	}
	return s
}

func keysOf(set impl.VariantSet) []string {
	out := make([]string, 0, len(set.Keys))
	for _, k := range set.Keys {
		out = append(out, string(k))
	}
	return out
}

func TestDeclareKeywordRecordsGroup(t *testing.T) {
	s := declaredStripper(t)
	if got := s.Group("fog"); got != "weather" {
		t.Fatalf("group of fog = %q, want weather", got)
	}
	if got := s.Group("shadow"); got != "light" {
		t.Fatalf("group of shadow = %q, want light", got)
	}
	if got := s.Group("missing"); got != "" {
		t.Fatalf("group of an undeclared keyword = %q, want empty", got)
	}
}

func TestDeclareKeywordRejectsBlank(t *testing.T) {
	s := impl.NewVariantStripper()
	if err := s.DeclareKeyword("   ", "weather", nil); !errors.Is(err, impl.ErrBlankKeyword) {
		t.Fatalf("blank keyword name must fail with ErrBlankKeyword, got %v", err)
	}
	if err := s.DeclareKeyword("fog", "", nil); !errors.Is(err, impl.ErrBlankGroup) {
		t.Fatalf("blank group must fail with ErrBlankGroup, got %v", err)
	}
	if got := s.Group("fog"); got != "" {
		t.Fatalf("a rejected declaration must not register the keyword, got group %q", got)
	}
}

func TestDeclareKeywordRejectsDuplicate(t *testing.T) {
	s := declaredStripper(t)
	if err := s.DeclareKeyword("fog", "surface", nil); !errors.Is(err, impl.ErrDuplicateKeyword) {
		t.Fatalf("redeclared keyword must fail with ErrDuplicateKeyword, got %v", err)
	}
	if got := s.Group("fog"); got != "weather" {
		t.Fatalf("a duplicate declaration must not move the keyword, group = %q", got)
	}
}

func TestDeclareKeywordRejectsUnknownDependency(t *testing.T) {
	s := impl.NewVariantStripper()
	if err := s.DeclareKeyword("mist", "weather", []string{"vapor"}); !errors.Is(err, impl.ErrUnknownKeyword) {
		t.Fatalf("unknown dependency must fail with ErrUnknownKeyword, got %v", err)
	}
	if got := s.Group("mist"); got != "" {
		t.Fatalf("keyword with a rejected dependency must not be registered, group %q", got)
	}
	if _, err := s.Resolve(map[string]bool{"mist": true}); !errors.Is(err, impl.ErrUnknownKeyword) {
		t.Fatalf("resolving the rejected keyword must fail with ErrUnknownKeyword, got %v", err)
	}
}

func TestDeclareKeywordRejectsCyclicDependency(t *testing.T) {
	s := impl.NewVariantStripper()
	for _, name := range []string{"a", "b", "c"} {
		if err := s.DeclareKeyword(name, "g", nil); err != nil {
			t.Fatalf("declare %s: %v", name, err)
		}
	}
	if err := s.SetDependencies("a", []string{"b"}); err != nil {
		t.Fatalf("set a->b: %v", err)
	}
	if err := s.SetDependencies("b", []string{"c"}); err != nil {
		t.Fatalf("set b->c: %v", err)
	}
	if err := s.SetDependencies("c", []string{"a"}); !errors.Is(err, impl.ErrDependencyCycle) {
		t.Fatalf("multi hop back edge must fail with ErrDependencyCycle, got %v", err)
	}
	if err := s.SetDependencies("a", []string{"a"}); !errors.Is(err, impl.ErrDependencyCycle) {
		t.Fatalf("self dependency must fail with ErrDependencyCycle, got %v", err)
	}
	key, err := s.Resolve(map[string]bool{"a": true})
	if err != nil {
		t.Fatalf("rejected dependency edits must leave the graph usable, got %v", err)
	}
	if key != "a+b+c" {
		t.Fatalf("resolved key after rejected cycle = %q, want a+b+c", key)
	}
}

func TestResolveSortsNames(t *testing.T) {
	s := declaredStripper(t)
	key, err := s.Resolve(map[string]bool{"shadow": true, "fog": true})
	if err != nil {
		t.Fatalf("resolve fog+shadow: %v", err)
	}
	if key != "fog+shadow" {
		t.Fatalf("resolved key = %q, want fog+shadow", key)
	}
	again, err := s.Resolve(map[string]bool{"fog": true, "shadow": true})
	if err != nil {
		t.Fatalf("resolve the same set twice: %v", err)
	}
	if again != key {
		t.Fatalf("map iteration order leaked into the key: %q vs %q", again, key)
	}
}

func TestResolveAddsDependencyClosure(t *testing.T) {
	s := declaredStripper(t)
	cases := []struct {
		set  map[string]bool
		want impl.VariantKey
	}{
		{map[string]bool{"snow": true}, "fog+snow"},
		{map[string]bool{"storm": true}, "fog+snow+storm"},
		{map[string]bool{"wet": true}, "rain+wet"},
	}
	for _, c := range cases {
		key, err := s.Resolve(c.set)
		if err != nil {
			t.Fatalf("resolve %v: %v", c.set, err)
		}
		if key != c.want {
			t.Fatalf("resolve %v = %q, want %q", c.set, key, c.want)
		}
	}
}

func TestResolveRejectsUnknownKeyword(t *testing.T) {
	s := declaredStripper(t)
	if _, err := s.Resolve(map[string]bool{"fog": true, "ghost": true}); !errors.Is(err, impl.ErrUnknownKeyword) {
		t.Fatalf("unknown keyword in the set must fail with ErrUnknownKeyword, got %v", err)
	}
	if got := len(s.Variants()); got != 0 {
		t.Fatalf("a rejected resolve must not register variants, got %d", got)
	}
}

func TestResolveRejectsExclusiveConflict(t *testing.T) {
	s := declaredStripper(t)
	if _, err := s.Resolve(map[string]bool{"fog": true, "rain": true}); !errors.Is(err, impl.ErrExclusiveConflict) {
		t.Fatalf("two keywords of one group must fail with ErrExclusiveConflict, got %v", err)
	}
	if got := len(s.Variants()); got != 0 {
		t.Fatalf("a conflicting resolve must not register variants, got %d", got)
	}
	if key, err := s.Resolve(map[string]bool{"fog": true, "shadow": true}); err != nil || key != "fog+shadow" {
		t.Fatalf("keywords from different groups must coexist, got %q err %v", key, err)
	}
}

func TestResolveEmptyIsBaseVariant(t *testing.T) {
	s := declaredStripper(t)
	key, err := s.Resolve(map[string]bool{})
	if err != nil {
		t.Fatalf("resolve of an empty set: %v", err)
	}
	if key != "" {
		t.Fatalf("empty set must resolve to the base variant, got %q", key)
	}
	other, err := s.Resolve(nil)
	if err != nil || other != key {
		t.Fatalf("nil set must match the empty set, got %q err %v", other, err)
	}
	if s.DefaultVariant() != impl.BaseVariant {
		t.Fatalf("default variant = %q, want the base variant", s.DefaultVariant())
	}
}

func TestStripKeepsOnlyReferencedVariants(t *testing.T) {
	s := plainStripper(t)
	for _, name := range []string{"a", "b", "c"} {
		if _, err := s.Resolve(map[string]bool{name: true}); err != nil {
			t.Fatalf("resolve %s: %v", name, err)
		}
	}
	if got := len(s.Variants()); got != 3 {
		t.Fatalf("registered variants = %d, want 3", got)
	}
	set, err := s.Strip([]impl.MaterialRef{
		{MaterialID: "m1", Key: "a"},
		{MaterialID: "m2", Key: "a"},
		{MaterialID: "m3", Key: "c"},
	})
	if err != nil {
		t.Fatalf("strip: %v", err)
	}
	want := []string{"a", "c"}
	got := keysOf(set)
	if len(got) != len(want) {
		t.Fatalf("kept variants = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("kept variants = %v, want %v (ascending order)", got, want)
		}
	}
	if set.Dropped != 1 {
		t.Fatalf("dropped variants = %d, want 1", set.Dropped)
	}
}

func TestRefCountCountsReferences(t *testing.T) {
	s := plainStripper(t)
	for _, name := range []string{"a", "b", "c"} {
		if _, err := s.Resolve(map[string]bool{name: true}); err != nil {
			t.Fatalf("resolve %s: %v", name, err)
		}
	}
	if _, err := s.Strip([]impl.MaterialRef{
		{MaterialID: "m1", Key: "a"},
		{MaterialID: "m2", Key: "a"},
		{MaterialID: "m3", Key: "b"},
	}); err != nil {
		t.Fatalf("first strip: %v", err)
	}
	if got := s.RefCount("a"); got != 2 {
		t.Fatalf("refcount of a = %d, want 2", got)
	}
	if got := s.RefCount("b"); got != 1 {
		t.Fatalf("refcount of b = %d, want 1", got)
	}
	if got := s.RefCount("c"); got != 0 {
		t.Fatalf("refcount of c = %d, want 0", got)
	}
	if _, err := s.Strip([]impl.MaterialRef{{MaterialID: "m9", Key: "b"}}); err != nil {
		t.Fatalf("second strip: %v", err)
	}
	if got := s.RefCount("a"); got != 0 {
		t.Fatalf("refcount of a after a full recount = %d, want 0", got)
	}
	if got := s.RefCount("b"); got != 1 {
		t.Fatalf("refcount of b after a full recount = %d, want 1", got)
	}
}

func TestStripRejectsUnknownReference(t *testing.T) {
	s := plainStripper(t)
	if _, err := s.Resolve(map[string]bool{"a": true}); err != nil {
		t.Fatalf("resolve a: %v", err)
	}
	if _, err := s.Strip([]impl.MaterialRef{{MaterialID: "m1", Key: "a+b"}}); !errors.Is(err, impl.ErrUnknownVariant) {
		t.Fatalf("reference to an unresolved variant must fail with ErrUnknownVariant, got %v", err)
	}
	if got := s.RefCount("a"); got != 0 {
		t.Fatalf("a rejected strip must not touch the counters, refcount of a = %d", got)
	}
}

func TestSnapshotRestoreKeepsVariants(t *testing.T) {
	s := plainStripper(t)
	for _, name := range []string{"a", "b"} {
		if _, err := s.Resolve(map[string]bool{name: true}); err != nil {
			t.Fatalf("resolve %s: %v", name, err)
		}
	}
	if _, err := s.Strip([]impl.MaterialRef{{MaterialID: "m1", Key: "a"}}); err != nil {
		t.Fatalf("strip: %v", err)
	}
	snap, err := s.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snap) == 0 {
		t.Fatalf("snapshot payload must not be empty")
	}
	if _, err := s.Strip([]impl.MaterialRef{{MaterialID: "m2", Key: "b"}}); err != nil {
		t.Fatalf("strip after the snapshot: %v", err)
	}
	if err := s.Restore(snap); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := s.RefCount("a"); got != 1 {
		t.Fatalf("refcount of a after restore = %d, want 1", got)
	}
	if got := s.RefCount("b"); got != 0 {
		t.Fatalf("refcount of b after restore = %d, want 0", got)
	}
	if got := len(s.Variants()); got != 2 {
		t.Fatalf("registered variants after restore = %d, want 2", got)
	}
	if got := s.Group("a"); got != "ga" {
		t.Fatalf("keyword group after restore = %q, want ga", got)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	s := plainStripper(t)
	if _, err := s.Resolve(map[string]bool{"a": true}); err != nil {
		t.Fatalf("resolve a: %v", err)
	}
	if _, err := s.Strip([]impl.MaterialRef{{MaterialID: "m1", Key: "a"}}); err != nil {
		t.Fatalf("strip: %v", err)
	}
	if err := s.Restore([]byte("variant-table: broken")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt payload must fail with ErrBadSnapshot, got %v", err)
	}
	if got := s.RefCount("a"); got != 1 {
		t.Fatalf("a rejected restore must leave the counters, refcount of a = %d", got)
	}
	if got := s.Group("a"); got != "ga" {
		t.Fatalf("a rejected restore must leave the keyword table, group = %q", got)
	}
}
