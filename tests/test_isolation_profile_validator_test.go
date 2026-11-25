package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch49-go-safety-d-14"
)

func validProfile(name string) impl.Profile {
	return impl.Profile{
		Name:           name,
		Namespaces:     []string{"user", "net", "pid"},
		Mounts:         []impl.MountSpec{{Source: "/tmp/build", Target: "/data", Writable: true}},
		ReadOnlyRootFS: false,
		UserMaps:       []impl.IDMap{{ContainerID: 0, HostID: 100000, Size: 65536}},
		RunAsUser:      1000,
	}
}

func containsField(fields []string, want string) bool {
	for _, f := range fields {
		if f == want {
			return true
		}
	}
	return false
}

func TestValidateAcceptsWellFormedProfile(t *testing.T) {
	v := impl.NewIsolationProfileValidator()
	p := validProfile("web")
	verdict, reason, err := v.Validate(p)
	if err != nil {
		t.Fatalf("validate must not fail on a well formed profile: %v", err)
	}
	if !verdict.OK || reason != impl.ReasonOK || verdict.Reason != impl.ReasonOK {
		t.Fatalf("verdict = %+v reason=%q, want ok", verdict, reason)
	}
	if verdict.Name != "web" {
		t.Fatalf("verdict name = %q, want web", verdict.Name)
	}
	if len(verdict.Digest) != 16 {
		t.Fatalf("digest = %q, want 16 hex characters", verdict.Digest)
	}
	if len(verdict.Fields) != 0 {
		t.Fatalf("a valid profile must not list offending fields, got %v", verdict.Fields)
	}
	digest, err := v.Digest(p)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if digest != verdict.Digest {
		t.Fatalf("Digest() = %q, validate digest = %q", digest, verdict.Digest)
	}
}

func TestValidateBlankName(t *testing.T) {
	v := impl.NewIsolationProfileValidator()
	p := validProfile("   ")
	verdict, reason, err := v.Validate(p)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if verdict.OK || reason != impl.ReasonBlankName || verdict.Reason != impl.ReasonBlankName {
		t.Fatalf("verdict = %+v reason=%q, want %q", verdict, reason, impl.ReasonBlankName)
	}
}

func TestValidateNoNamespaces(t *testing.T) {
	v := impl.NewIsolationProfileValidator()
	p := validProfile("web")
	p.Namespaces = nil
	verdict, reason, err := v.Validate(p)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if verdict.OK || reason != impl.ReasonNoNamespace || verdict.Reason != impl.ReasonNoNamespace {
		t.Fatalf("verdict = %+v reason=%q, want %q", verdict, reason, impl.ReasonNoNamespace)
	}
}

func TestValidateUnknownNamespace(t *testing.T) {
	v := impl.NewIsolationProfileValidator()
	p := validProfile("web")
	p.Namespaces = []string{"user", "gpu"}
	verdict, reason, err := v.Validate(p)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if verdict.OK || reason != impl.ReasonUnknownNamespace {
		t.Fatalf("verdict = %+v reason=%q, want %q", verdict, reason, impl.ReasonUnknownNamespace)
	}
	if !containsField(verdict.Fields, "gpu") {
		t.Fatalf("fields = %v, want the offending namespace gpu", verdict.Fields)
	}
}

func TestValidateDuplicateNamespace(t *testing.T) {
	v := impl.NewIsolationProfileValidator()
	p := validProfile("web")
	p.Namespaces = []string{"net", "user", "net"}
	verdict, reason, err := v.Validate(p)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if verdict.OK || reason != impl.ReasonDuplicateNamespace {
		t.Fatalf("verdict = %+v reason=%q, want %q", verdict, reason, impl.ReasonDuplicateNamespace)
	}
	if !containsField(verdict.Fields, "net") {
		t.Fatalf("fields = %v, want the duplicated namespace net", verdict.Fields)
	}
}

func TestValidateBadMount(t *testing.T) {
	v := impl.NewIsolationProfileValidator()
	p := validProfile("web")
	p.Mounts = []impl.MountSpec{{Source: "/tmp/build", Target: "relative/data", Writable: false}}
	verdict, reason, err := v.Validate(p)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if verdict.OK || reason != impl.ReasonBadMount {
		t.Fatalf("verdict = %+v reason=%q, want %q", verdict, reason, impl.ReasonBadMount)
	}
	if !containsField(verdict.Fields, "mounts") {
		t.Fatalf("fields = %v, want the mounts field", verdict.Fields)
	}
	p.Mounts = []impl.MountSpec{{Source: "  ", Target: "/data", Writable: false}}
	if _, reason, err := v.Validate(p); err != nil || reason != impl.ReasonBadMount {
		t.Fatalf("blank mount source gave reason=%q err=%v, want %q", reason, err, impl.ReasonBadMount)
	}
}

func TestValidateMountOverlap(t *testing.T) {
	v := impl.NewIsolationProfileValidator()
	p := validProfile("web")
	p.Mounts = []impl.MountSpec{
		{Source: "/tmp/a", Target: "/data", Writable: true},
		{Source: "/tmp/b", Target: "/data", Writable: false},
	}
	verdict, reason, err := v.Validate(p)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if verdict.OK || reason != impl.ReasonMountOverlap {
		t.Fatalf("verdict = %+v reason=%q, want %q", verdict, reason, impl.ReasonMountOverlap)
	}
	if !containsField(verdict.Fields, "/data") {
		t.Fatalf("fields = %v, want the overlapping target /data", verdict.Fields)
	}
}

func TestValidateInvalidIDMap(t *testing.T) {
	v := impl.NewIsolationProfileValidator()
	p := validProfile("web")
	p.UserMaps = []impl.IDMap{
		{ContainerID: 0, HostID: 100000, Size: 65536},
		{ContainerID: -5, HostID: 200000, Size: 10},
	}
	verdict, reason, err := v.Validate(p)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if verdict.OK || reason != impl.ReasonInvalidIDMap {
		t.Fatalf("verdict = %+v reason=%q, want %q", verdict, reason, impl.ReasonInvalidIDMap)
	}
	p.UserMaps = []impl.IDMap{
		{ContainerID: 0, HostID: 100000, Size: 65536},
		{ContainerID: 5, HostID: 200000, Size: 0},
	}
	if _, reason, err := v.Validate(p); err != nil || reason != impl.ReasonInvalidIDMap {
		t.Fatalf("zero sized mapping gave reason=%q err=%v, want %q", reason, err, impl.ReasonInvalidIDMap)
	}
}

func TestNormalizeSortsAndDedupes(t *testing.T) {
	v := impl.NewIsolationProfileValidator()
	p := validProfile(" web ")
	p.Namespaces = []string{"net", "user", "net"}
	p.Mounts = []impl.MountSpec{
		{Source: "/tmp/s2", Target: "/b", Writable: true},
		{Source: "/tmp/s1", Target: "/a", Writable: false},
		{Source: "/tmp/s2", Target: "/b", Writable: true},
	}
	p.UserMaps = []impl.IDMap{
		{ContainerID: 1, HostID: 200000, Size: 10},
		{ContainerID: 0, HostID: 100000, Size: 65536},
		{ContainerID: 1, HostID: 200000, Size: 10},
	}
	norm, digest, err := v.Normalize(p)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if norm.Name != "web" {
		t.Fatalf("normalized name = %q, want web", norm.Name)
	}
	if len(norm.Namespaces) != 2 || norm.Namespaces[0] != "net" || norm.Namespaces[1] != "user" {
		t.Fatalf("normalized namespaces = %v, want [net user]", norm.Namespaces)
	}
	if len(norm.Mounts) != 2 || norm.Mounts[0].Target != "/a" || norm.Mounts[1].Target != "/b" {
		t.Fatalf("normalized mounts = %+v, want /a before /b with duplicates dropped", norm.Mounts)
	}
	if len(norm.UserMaps) != 2 || norm.UserMaps[0].ContainerID != 0 || norm.UserMaps[1].ContainerID != 1 {
		t.Fatalf("normalized id maps = %+v, want container id order", norm.UserMaps)
	}
	if len(digest) != 16 {
		t.Fatalf("digest = %q, want 16 hex characters", digest)
	}
	reordered := validProfile("web")
	reordered.Namespaces = []string{"user", "net"}
	reordered.Mounts = []impl.MountSpec{
		{Source: "/tmp/s1", Target: "/a", Writable: false},
		{Source: "/tmp/s2", Target: "/b", Writable: true},
	}
	reordered.UserMaps = []impl.IDMap{
		{ContainerID: 0, HostID: 100000, Size: 65536},
		{ContainerID: 1, HostID: 200000, Size: 10},
	}
	_, reorderedDigest, err := v.Normalize(reordered)
	if err != nil {
		t.Fatalf("normalize reordered: %v", err)
	}
	if reorderedDigest != digest {
		t.Fatalf("field order changed the digest: %q vs %q", reorderedDigest, digest)
	}
}

func TestNormalizeRejectsBadProfile(t *testing.T) {
	v := impl.NewIsolationProfileValidator()
	if _, _, err := v.Normalize(validProfile("  ")); !errors.Is(err, impl.ErrBadProfile) {
		t.Fatalf("blank name must fail with ErrBadProfile, got %v", err)
	}
	p := validProfile("web")
	p.Namespaces = nil
	if _, _, err := v.Normalize(p); !errors.Is(err, impl.ErrBadProfile) {
		t.Fatalf("empty namespace set must fail with ErrBadProfile, got %v", err)
	}
}

func TestDigestMatchesNormalize(t *testing.T) {
	v := impl.NewIsolationProfileValidator()
	p := validProfile("api")
	_, digest, err := v.Normalize(p)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	got, err := v.Digest(p)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if got != digest {
		t.Fatalf("Digest() = %q, Normalize digest = %q", got, digest)
	}
	if _, err := v.Digest(validProfile(" ")); !errors.Is(err, impl.ErrBadProfile) {
		t.Fatalf("digest of a bad profile must fail with ErrBadProfile, got %v", err)
	}
}

func TestCompatible(t *testing.T) {
	v := impl.NewIsolationProfileValidator()
	a := validProfile("a")
	b := validProfile("b")
	b.Namespaces = []string{"pid", "user", "net"}
	if !v.Compatible(a, b) {
		t.Fatalf("profiles with the same namespace set must be compatible regardless of order")
	}
	if v.Compatible(a, b) != v.Compatible(b, a) {
		t.Fatalf("compatibility must be symmetric")
	}
	if !v.Compatible(a, a) {
		t.Fatalf("a profile must be compatible with itself")
	}
	different := validProfile("c")
	different.Namespaces = []string{"net", "user"}
	if v.Compatible(a, different) {
		t.Fatalf("profiles with different namespace sets must not be compatible")
	}
	conflicting := validProfile("d")
	conflicting.Mounts = []impl.MountSpec{{Source: "/tmp/other", Target: "/data", Writable: false}}
	if v.Compatible(a, conflicting) {
		t.Fatalf("the same mount target with a different writable flag must not be compatible")
	}
}

func TestExplainReasons(t *testing.T) {
	v := impl.NewIsolationProfileValidator()
	reasons := []impl.Reason{impl.ReasonOK, impl.ReasonUnknownNamespace, impl.ReasonBadMount, impl.ReasonMountOverlap}
	seen := map[string]bool{}
	for _, reason := range reasons {
		text := v.Explain(reason)
		if text == "" {
			t.Fatalf("Explain(%q) returned an empty sentence", reason)
		}
		if seen[text] {
			t.Fatalf("Explain(%q) duplicates another reason's sentence", reason)
		}
		seen[text] = true
	}
}

func TestBatchIsolatesFailures(t *testing.T) {
	v := impl.NewIsolationProfileValidator()
	blank := validProfile("broken")
	blank.Name = ""
	unknown := validProfile("odd")
	unknown.Namespaces = []string{"user", "gpu"}
	profiles := []impl.Profile{validProfile("first"), blank, validProfile("second"), unknown}
	verdicts := v.Batch(profiles)
	if len(verdicts) != 4 {
		t.Fatalf("batch returned %d verdicts, want 4", len(verdicts))
	}
	if !verdicts[0].OK || verdicts[0].Name != "first" {
		t.Fatalf("verdict 0 = %+v, want first accepted", verdicts[0])
	}
	if verdicts[1].OK || verdicts[1].Reason != impl.ReasonBlankName || verdicts[1].Name != "broken" {
		t.Fatalf("verdict 1 = %+v, want broken rejected with %q", verdicts[1], impl.ReasonBlankName)
	}
	if !verdicts[2].OK || verdicts[2].Name != "second" {
		t.Fatalf("verdict 2 = %+v, want second accepted after a failure", verdicts[2])
	}
	if verdicts[3].OK || verdicts[3].Reason != impl.ReasonUnknownNamespace {
		t.Fatalf("verdict 3 = %+v, want odd rejected with %q", verdicts[3], impl.ReasonUnknownNamespace)
	}
}

func TestConcurrentValidate(t *testing.T) {
	v := impl.NewIsolationProfileValidator()
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := 0
	bad := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p := validProfile("svc")
			verdict, _, err := v.Validate(p)
			mu.Lock()
			defer mu.Unlock()
			if err != nil || !verdict.OK || len(verdict.Digest) != 16 {
				bad++
				return
			}
			accepted++
		}(i)
	}
	wg.Wait()
	if bad != 0 {
		t.Fatalf("%d concurrent validations failed", bad)
	}
	if accepted != 8 {
		t.Fatalf("concurrent validations accepted %d profiles, want 8", accepted)
	}
	verdicts := v.Batch([]impl.Profile{validProfile("a"), validProfile("b"), validProfile("c")})
	if len(verdicts) != 3 {
		t.Fatalf("concurrent batch returned %d verdicts, want 3", len(verdicts))
	}
	for i, verdict := range verdicts {
		if !verdict.OK {
			t.Fatalf("batch verdict %d rejected a valid profile: %+v", i, verdict)
		}
	}
}
