package task

import (
	"errors"
	"strings"
	"testing"

	impl "gsb/batch49-go-safety-e-13"
)

func hexDigest(prefix string) string { return strings.Repeat(prefix, 64) }

func mustRegister(t *testing.T, g *impl.LockGraphGuard, name, version, digest string, reqs []impl.Requirement) {
	t.Helper()
	if err := g.Register(name, version, digest, reqs); err != nil {
		t.Fatalf("register %s %s must succeed, got %v", name, version, err)
	}
}

func mustPin(t *testing.T, g *impl.LockGraphGuard, name, version, digest string) {
	t.Helper()
	if err := g.Pin(name, version, digest); err != nil {
		t.Fatalf("pin %s %s must succeed, got %v", name, version, err)
	}
}

// seededGuard builds app 1.0.0 -> lib 1.0.0 -> util 1.0.0 with every module
// pinned directly.
func seededGuard(t *testing.T) *impl.LockGraphGuard {
	t.Helper()
	g := impl.NewLockGraphGuard()
	mustRegister(t, g, "app", "1.0.0", hexDigest("a"), []impl.Requirement{{Name: "lib", Version: "1.0.0"}})
	mustRegister(t, g, "lib", "1.0.0", hexDigest("b"), []impl.Requirement{{Name: "util", Version: "1.0.0"}})
	mustRegister(t, g, "util", "1.0.0", hexDigest("c"), nil)
	mustPin(t, g, "app", "1.0.0", hexDigest("a"))
	mustPin(t, g, "lib", "1.0.0", hexDigest("b"))
	mustPin(t, g, "util", "1.0.0", hexDigest("c"))
	return g
}

func TestRegisterRejectsBlankName(t *testing.T) {
	g := impl.NewLockGraphGuard()
	err := g.Register("", "1.0.0", hexDigest("a"), nil)
	if !errors.Is(err, impl.ErrBadName) {
		t.Fatalf("blank name error = %v, want ErrBadName", err)
	}
}

func TestRegisterRejectsFloatingVersion(t *testing.T) {
	g := impl.NewLockGraphGuard()
	for _, version := range []string{"^1.0.0", "1.2", "1.x.0"} {
		err := g.Register("app", version, hexDigest("a"), nil)
		if !errors.Is(err, impl.ErrFloatingVersion) {
			t.Fatalf("version %q error = %v, want ErrFloatingVersion", version, err)
		}
	}
}

func TestRegisterRejectsBadDigest(t *testing.T) {
	g := impl.NewLockGraphGuard()
	for _, digest := range []string{"", "abc", strings.Repeat("A", 64)} {
		err := g.Register("app", "1.0.0", digest, nil)
		if !errors.Is(err, impl.ErrBadDigest) {
			t.Fatalf("digest %q error = %v, want ErrBadDigest", digest, err)
		}
	}
}

func TestPinUnknownReleaseFails(t *testing.T) {
	g := impl.NewLockGraphGuard()
	err := g.Pin("app", "1.0.0", hexDigest("a"))
	if !errors.Is(err, impl.ErrUnknownRelease) {
		t.Fatalf("pin unknown release error = %v, want ErrUnknownRelease", err)
	}
	if _, ok := g.Locked("app"); ok {
		t.Fatalf("a rejected pin must not lock anything")
	}
}

func TestPinRejectsBlankName(t *testing.T) {
	g := impl.NewLockGraphGuard()
	if err := g.Pin("", "1.0.0", hexDigest("a")); !errors.Is(err, impl.ErrBadName) {
		t.Fatalf("blank pin name error = %v, want ErrBadName", err)
	}
}

func TestClosureIncludesTransitiveDependencies(t *testing.T) {
	g := seededGuard(t)
	got, err := g.Closure("app")
	if err != nil {
		t.Fatalf("closure must succeed, got %v", err)
	}
	want := []string{"app", "lib", "util"}
	if len(got) != len(want) {
		t.Fatalf("closure = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("closure = %v, want %v", got, want)
		}
	}
}

func TestClosureUnknownRootFails(t *testing.T) {
	g := seededGuard(t)
	_, err := g.Closure("ghost")
	if !errors.Is(err, impl.ErrNotLocked) {
		t.Fatalf("unknown root error = %v, want ErrNotLocked", err)
	}
}

func TestSerializeDeterministicAcrossPinOrder(t *testing.T) {
	first := impl.NewLockGraphGuard()
	mustRegister(t, first, "app", "1.0.0", hexDigest("a"), []impl.Requirement{{Name: "lib", Version: "1.0.0"}})
	mustRegister(t, first, "lib", "1.0.0", hexDigest("b"), nil)
	mustPin(t, first, "app", "1.0.0", hexDigest("a"))
	mustPin(t, first, "lib", "1.0.0", hexDigest("b"))

	second := impl.NewLockGraphGuard()
	mustRegister(t, second, "lib", "1.0.0", hexDigest("b"), nil)
	mustRegister(t, second, "app", "1.0.0", hexDigest("a"), []impl.Requirement{{Name: "lib", Version: "1.0.0"}})
	mustPin(t, second, "lib", "1.0.0", hexDigest("b"))
	mustPin(t, second, "app", "1.0.0", hexDigest("a"))

	a, err := first.Serialize()
	if err != nil {
		t.Fatalf("first serialize must succeed, got %v", err)
	}
	b, err := second.Serialize()
	if err != nil {
		t.Fatalf("second serialize must succeed, got %v", err)
	}
	if string(a) != string(b) {
		t.Fatalf("serialized lock sets differ:\n%q\n%q", string(a), string(b))
	}
}

func TestSerializeExactSortedOutput(t *testing.T) {
	g := seededGuard(t)
	payload, err := g.Serialize()
	if err != nil {
		t.Fatalf("serialize must succeed, got %v", err)
	}
	want := "app\t1.0.0\t" + hexDigest("a") + "\n" +
		"lib\t1.0.0\t" + hexDigest("b") + "\n" +
		"util\t1.0.0\t" + hexDigest("c") + "\n"
	if string(payload) != want {
		t.Fatalf("serialized = %q, want %q", string(payload), want)
	}
}

func TestUpgradePlanListsVersionChanges(t *testing.T) {
	g := seededGuard(t)
	mustRegister(t, g, "app", "2.0.0", hexDigest("d"), []impl.Requirement{{Name: "lib", Version: "2.0.0"}})
	mustRegister(t, g, "lib", "2.0.0", hexDigest("e"), nil)
	plan, err := g.Upgrade("app", "2.0.0")
	if err != nil {
		t.Fatalf("upgrade must succeed, got %v", err)
	}
	if plan.Root != "app" || plan.Version != "2.0.0" {
		t.Fatalf("plan root/version = %q/%q, want app/2.0.0", plan.Root, plan.Version)
	}
	if len(plan.Changes) != 2 {
		t.Fatalf("change count = %d, want 2 (%v)", len(plan.Changes), plan.Changes)
	}
	if plan.Changes[0].Name != "app" || plan.Changes[0].OldVersion != "1.0.0" || plan.Changes[0].NewVersion != "2.0.0" {
		t.Fatalf("first change = %+v, want app 1.0.0 -> 2.0.0", plan.Changes[0])
	}
	if plan.Changes[1].Name != "lib" || plan.Changes[1].NewDigest != hexDigest("e") {
		t.Fatalf("second change = %+v, want lib moved to digest e", plan.Changes[1])
	}
	if got, _ := g.Locked("app"); got.Version != "1.0.0" {
		t.Fatalf("upgrade must not mutate the lock set, app is at %q", got.Version)
	}
}

func TestUpgradeUnknownVersionFails(t *testing.T) {
	g := seededGuard(t)
	_, err := g.Upgrade("app", "9.9.9")
	if !errors.Is(err, impl.ErrUnknownRelease) {
		t.Fatalf("unknown upgrade version error = %v, want ErrUnknownRelease", err)
	}
}

func TestApplyRejectsStalePlan(t *testing.T) {
	g := seededGuard(t)
	mustRegister(t, g, "app", "2.0.0", hexDigest("d"), []impl.Requirement{{Name: "lib", Version: "2.0.0"}})
	mustRegister(t, g, "lib", "2.0.0", hexDigest("e"), nil)
	plan, err := g.Upgrade("app", "2.0.0")
	if err != nil {
		t.Fatalf("upgrade must succeed, got %v", err)
	}
	if err := g.Apply(plan); err != nil {
		t.Fatalf("first apply must succeed, got %v", err)
	}
	if err := g.Apply(plan); !errors.Is(err, impl.ErrStalePlan) {
		t.Fatalf("repeated apply error = %v, want ErrStalePlan", err)
	}
	got, ok := g.Locked("app")
	if !ok || got.Version != "2.0.0" || got.Digest != hexDigest("d") {
		t.Fatalf("lock set after rejected stale plan = %+v, want app still at 2.0.0", got)
	}
}

func TestApplyUpdatesEntries(t *testing.T) {
	g := seededGuard(t)
	mustRegister(t, g, "app", "2.0.0", hexDigest("d"), []impl.Requirement{{Name: "lib", Version: "2.0.0"}})
	mustRegister(t, g, "lib", "2.0.0", hexDigest("e"), nil)
	plan, err := g.Upgrade("app", "2.0.0")
	if err != nil {
		t.Fatalf("upgrade must succeed, got %v", err)
	}
	if err := g.Apply(plan); err != nil {
		t.Fatalf("apply must succeed, got %v", err)
	}
	entries := g.Entries()
	if len(entries) != 3 {
		t.Fatalf("entry count = %d, want 3", len(entries))
	}
	if entries[0].Name != "app" || entries[0].Version != "2.0.0" || entries[0].Digest != hexDigest("d") {
		t.Fatalf("app entry = %+v, want the upgraded coordinate", entries[0])
	}
	if entries[1].Name != "lib" || entries[1].Version != "2.0.0" || entries[1].Digest != hexDigest("e") {
		t.Fatalf("lib entry = %+v, want the propagated upgrade", entries[1])
	}
	if entries[2].Name != "util" || entries[2].Version != "1.0.0" {
		t.Fatalf("util entry = %+v, want it untouched", entries[2])
	}
}

func TestVerifyReportsUnsatisfiedRequirement(t *testing.T) {
	g := impl.NewLockGraphGuard()
	mustRegister(t, g, "app", "1.0.0", hexDigest("a"), []impl.Requirement{{Name: "lib", Version: "2.0.0"}})
	mustRegister(t, g, "lib", "1.0.0", hexDigest("b"), nil)
	mustRegister(t, g, "lib", "2.0.0", hexDigest("c"), nil)
	mustPin(t, g, "app", "1.0.0", hexDigest("a"))
	mustPin(t, g, "lib", "1.0.0", hexDigest("b"))
	problems := g.Verify()
	found := false
	for _, p := range problems {
		if p.Name == "lib" && p.Kind == impl.ProblemUnsatisfied {
			found = true
			if !strings.Contains(p.Detail, "2.0.0") {
				t.Fatalf("problem detail = %q, want it to mention the required version 2.0.0", p.Detail)
			}
		}
	}
	if !found {
		t.Fatalf("verify problems = %+v, want an unsatisfied_requirement for lib", problems)
	}
}

func TestRestoreRoundTripAndCorruptPayload(t *testing.T) {
	g := seededGuard(t)
	payload, err := g.Serialize()
	if err != nil {
		t.Fatalf("serialize must succeed, got %v", err)
	}
	restored := impl.NewLockGraphGuard()
	if err := restored.Restore(payload); err != nil {
		t.Fatalf("restore must succeed, got %v", err)
	}
	again, err := restored.Serialize()
	if err != nil {
		t.Fatalf("serialize after restore must succeed, got %v", err)
	}
	if string(again) != string(payload) {
		t.Fatalf("round trip changed the lock file:\n%q\n%q", string(payload), string(again))
	}
	if err := restored.Restore([]byte("app\t1.0.0\n")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt restore error = %v, want ErrBadSnapshot", err)
	}
	if got := len(restored.Entries()); got != 3 {
		t.Fatalf("entry count after corrupt restore = %d, want the previous state of 3", got)
	}
}
