package task

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	impl "gsb/batch49-go-safety-d-02"
)

type d02Tree struct {
	snap impl.TreeSnapshot
	err  error
}

func (f *d02Tree) Snapshot(root string) (impl.TreeSnapshot, error) {
	return f.snap, f.err
}

func d02Baseline() impl.Baseline {
	return impl.Baseline{
		Version: "base-2026-11",
		Rules: map[string][]impl.Entry{
			"/data/a": {{Subject: "user:1001", Bits: 6}},
			"/data/b": {{Subject: "user:1001", Bits: 6}},
		},
		Default: []impl.Entry{{Subject: "user:1001", Bits: 4}},
	}
}

func d02Files() []impl.FileAcl {
	return []impl.FileAcl{
		{
			Path:        "/data/a",
			Depth:       0,
			ContentHash: "h-a1",
			MTime:       time.Unix(1000, 0),
			Entries: []impl.Entry{
				{Subject: "user:1001", Bits: 6},
				{Subject: "group:50", Bits: 4},
			},
		},
		{
			Path:        "/data/b",
			Depth:       1,
			ContentHash: "h-b1",
			MTime:       time.Unix(1000, 0),
			Entries:     []impl.Entry{{Subject: "user:1001", Bits: 4}},
		},
		{
			Path:        "/data/c",
			Depth:       2,
			ContentHash: "h-c1",
			MTime:       time.Unix(1000, 0),
			Entries:     []impl.Entry{{Subject: "user:1001", Bits: 4}},
		},
	}
}

func d02Find(drifts []impl.Drift, path string) (impl.Drift, bool) {
	for _, d := range drifts {
		if d.Path == path {
			return d, true
		}
	}
	return impl.Drift{}, false
}

func d02Limits() impl.Limits {
	return impl.Limits{MaxFiles: 100, MaxDrifts: 100, MaxSteps: 100}
}

func TestScanDetectsOverGrant(t *testing.T) {
	tree := &d02Tree{snap: impl.TreeSnapshot{Generation: 5, Files: d02Files()}}
	r := impl.NewAclBaselineReconciler(tree, d02Limits(), time.Now)
	drifts, err := r.Scan("/data", d02Baseline())
	if err != nil {
		t.Fatalf("Scan must succeed, got %v", err)
	}
	d, ok := d02Find(drifts, "/data/a")
	if !ok {
		t.Fatalf("expected a drift on /data/a, got %+v", drifts)
	}
	if d.Kind != impl.DriftOverGrant {
		t.Fatalf("drift kind = %q, want %q", d.Kind, impl.DriftOverGrant)
	}
	if len(d.Extra) != 1 {
		t.Fatalf("extra entries = %d, want 1 (group:50 read)", len(d.Extra))
	}
	if d.Extra[0].Subject != "group:50" || d.Extra[0].Bits != 4 {
		t.Fatalf("extra entry = %+v, want group:50 with read", d.Extra[0])
	}
	if len(d.Missing) != 0 {
		t.Fatalf("missing entries on a fully granted path = %d, want 0", len(d.Missing))
	}
	if d.Generation != 5 {
		t.Fatalf("drift generation = %d, want the snapshot generation 5", d.Generation)
	}
}

func TestScanDetectsMissingBits(t *testing.T) {
	tree := &d02Tree{snap: impl.TreeSnapshot{Generation: 5, Files: d02Files()}}
	r := impl.NewAclBaselineReconciler(tree, d02Limits(), time.Now)
	drifts, err := r.Scan("/data", d02Baseline())
	if err != nil {
		t.Fatalf("Scan must succeed, got %v", err)
	}
	d, ok := d02Find(drifts, "/data/b")
	if !ok {
		t.Fatalf("expected a drift on /data/b, got %+v", drifts)
	}
	if d.Kind != impl.DriftMissing {
		t.Fatalf("drift kind = %q, want %q", d.Kind, impl.DriftMissing)
	}
	if len(d.Missing) != 1 || d.Missing[0].Subject != "user:1001" || d.Missing[0].Bits != 2 {
		t.Fatalf("missing entries = %+v, want user:1001 with write bit", d.Missing)
	}
	if len(d.Extra) != 0 {
		t.Fatalf("extra entries = %d, want 0", len(d.Extra))
	}
	if _, ok := d02Find(drifts, "/data/c"); ok {
		t.Fatalf("/data/c matches the baseline default and must not be reported")
	}
}

func TestScanRejectsBlankRootAndBaseline(t *testing.T) {
	tree := &d02Tree{snap: impl.TreeSnapshot{Generation: 5, Files: d02Files()}}
	r := impl.NewAclBaselineReconciler(tree, d02Limits(), time.Now)
	if _, err := r.Scan("  ", d02Baseline()); !errors.Is(err, impl.ErrBlankRoot) {
		t.Fatalf("blank root must get ErrBlankRoot, got %v", err)
	}
	empty := d02Baseline()
	empty.Version = ""
	if _, err := r.Scan("/data", empty); !errors.Is(err, impl.ErrBadBaseline) {
		t.Fatalf("blank baseline version must get ErrBadBaseline, got %v", err)
	}
}

func TestClassifyMapsKinds(t *testing.T) {
	r := impl.NewAclBaselineReconciler(&d02Tree{}, d02Limits(), time.Now)
	cases := []struct {
		kind impl.DriftKind
		want impl.DriftKind
	}{
		{impl.DriftOverGrant, impl.DriftOverGrant},
		{impl.DriftMissing, impl.DriftMissing},
		{impl.DriftExpiredTemp, impl.DriftExpiredTemp},
		{impl.DriftInheritConflict, impl.DriftInheritConflict},
	}
	for _, tc := range cases {
		got := r.Classify(impl.Drift{Path: "/data/a", Kind: tc.kind})
		if got != tc.want {
			t.Fatalf("Classify(%q) = %q, want %q", tc.kind, got, tc.want)
		}
	}
}

func TestScanSkipsExemptPath(t *testing.T) {
	now := time.Unix(2000, 0)
	tree := &d02Tree{snap: impl.TreeSnapshot{Generation: 5, Files: d02Files()}}
	r := impl.NewAclBaselineReconciler(tree, d02Limits(), func() time.Time { return now })
	if err := r.Exempt("/data/a", now.Add(time.Hour), "CHG-7001"); err != nil {
		t.Fatalf("Exempt must succeed, got %v", err)
	}
	drifts, err := r.Scan("/data", d02Baseline())
	if err != nil {
		t.Fatalf("Scan must succeed, got %v", err)
	}
	if _, ok := d02Find(drifts, "/data/a"); ok {
		t.Fatalf("exempted path must not be reported")
	}
	if _, ok := d02Find(drifts, "/data/b"); !ok {
		t.Fatalf("non exempted path must still be reported")
	}
	if got := len(r.Exemptions()); got != 1 {
		t.Fatalf("exemption count = %d, want 1", got)
	}
}

func TestExemptRequiresTicketAndFutureExpiry(t *testing.T) {
	now := time.Unix(2000, 0)
	r := impl.NewAclBaselineReconciler(&d02Tree{}, d02Limits(), func() time.Time { return now })
	if err := r.Exempt("/data/a", now.Add(time.Hour), "   "); !errors.Is(err, impl.ErrBlankTicket) {
		t.Fatalf("blank ticket must get ErrBlankTicket, got %v", err)
	}
	if err := r.Exempt("/data/a", now.Add(-time.Minute), "CHG-7002"); !errors.Is(err, impl.ErrExemptPast) {
		t.Fatalf("past expiry must get ErrExemptPast, got %v", err)
	}
	if got := len(r.Exemptions()); got != 0 {
		t.Fatalf("rejected exemptions must not be stored, got %d", got)
	}
}

func TestExemptedPathReappearsAfterExpiry(t *testing.T) {
	now := time.Unix(2000, 0)
	tree := &d02Tree{snap: impl.TreeSnapshot{Generation: 5, Files: d02Files()}}
	r := impl.NewAclBaselineReconciler(tree, d02Limits(), func() time.Time { return now })
	if err := r.Exempt("/data/a", now.Add(time.Hour), "CHG-7003"); err != nil {
		t.Fatalf("Exempt must succeed, got %v", err)
	}
	if drifts, err := r.Scan("/data", d02Baseline()); err != nil {
		t.Fatalf("Scan must succeed, got %v", err)
	} else if _, ok := d02Find(drifts, "/data/a"); ok {
		t.Fatalf("path must be excluded while the exemption holds")
	}
	now = now.Add(2 * time.Hour)
	drifts, err := r.Scan("/data", d02Baseline())
	if err != nil {
		t.Fatalf("Scan after expiry must succeed, got %v", err)
	}
	if _, ok := d02Find(drifts, "/data/a"); !ok {
		t.Fatalf("drift must reappear once the exemption expires")
	}
}

func TestPlanOrdersByDepth(t *testing.T) {
	r := impl.NewAclBaselineReconciler(&d02Tree{}, d02Limits(), time.Now)
	drifts := []impl.Drift{
		{Path: "/x/2", Depth: 2, Kind: impl.DriftOverGrant, Extra: []impl.Entry{{Subject: "group:9", Bits: 4}}},
		{Path: "/x/0", Depth: 0, Kind: impl.DriftOverGrant, Extra: []impl.Entry{{Subject: "group:9", Bits: 4}}},
		{Path: "/x/1", Depth: 1, Kind: impl.DriftOverGrant, Extra: []impl.Entry{{Subject: "group:9", Bits: 4}}},
	}
	steps, err := r.Plan(drifts)
	if err != nil {
		t.Fatalf("Plan must succeed, got %v", err)
	}
	if len(steps) != 3 {
		t.Fatalf("step count = %d, want 3", len(steps))
	}
	want := []string{"/x/0", "/x/1", "/x/2"}
	for i, path := range want {
		if steps[i].Path != path {
			t.Fatalf("step %d path = %q, want %q (ascending depth)", i, steps[i].Path, path)
		}
		if steps[i].Batch != i {
			t.Fatalf("step %d batch = %d, want %d", i, steps[i].Batch, i)
		}
		if steps[i].Key == "" {
			t.Fatalf("step %d must carry a stable idempotency key", i)
		}
	}
}

func TestPlanBatchesPerDepth(t *testing.T) {
	r := impl.NewAclBaselineReconciler(&d02Tree{}, d02Limits(), time.Now)
	drifts := []impl.Drift{
		{Path: "/x/1a", Depth: 1, Kind: impl.DriftMissing, Missing: []impl.Entry{{Subject: "user:7", Bits: 4}}},
		{Path: "/x/1b", Depth: 1, Kind: impl.DriftMissing, Missing: []impl.Entry{{Subject: "user:7", Bits: 4}}},
		{Path: "/x/0", Depth: 0, Kind: impl.DriftMissing, Missing: []impl.Entry{{Subject: "user:7", Bits: 4}}},
	}
	steps, err := r.Plan(drifts)
	if err != nil {
		t.Fatalf("Plan must succeed, got %v", err)
	}
	if len(steps) != 3 {
		t.Fatalf("step count = %d, want 3", len(steps))
	}
	batches := map[int]int{}
	for _, s := range steps {
		batches[s.Depth] = s.Batch
	}
	if batches[0] != 0 {
		t.Fatalf("depth 0 batch = %d, want 0", batches[0])
	}
	if batches[1] != 1 {
		t.Fatalf("depth 1 batch = %d, want 1", batches[1])
	}
	if steps[1].Depth != 1 || steps[2].Depth != 1 {
		t.Fatalf("both depth 1 paths must share the same batch, got depths %d and %d", steps[1].Depth, steps[2].Depth)
	}
}

func TestPlanStableAcrossInputOrder(t *testing.T) {
	r := impl.NewAclBaselineReconciler(&d02Tree{}, d02Limits(), time.Now)
	a := impl.Drift{Path: "/x/a", Depth: 1, Kind: impl.DriftOverGrant, Extra: []impl.Entry{{Subject: "group:9", Bits: 4}}}
	b := impl.Drift{Path: "/x/b", Depth: 1, Kind: impl.DriftMissing, Missing: []impl.Entry{{Subject: "user:7", Bits: 2}}}
	c := impl.Drift{Path: "/x/c", Depth: 0, Kind: impl.DriftOverGrant, Extra: []impl.Entry{{Subject: "group:3", Bits: 2}}}
	first, err := r.Plan([]impl.Drift{a, b, c})
	if err != nil {
		t.Fatalf("Plan must succeed, got %v", err)
	}
	second, err := r.Plan([]impl.Drift{c, a, b})
	if err != nil {
		t.Fatalf("Plan on reordered input must succeed, got %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("plan depends on input order:\nfirst=%+v\nsecond=%+v", first, second)
	}
}

func TestApplyStepIsIdempotent(t *testing.T) {
	tree := &d02Tree{snap: impl.TreeSnapshot{Generation: 5, Files: d02Files()}}
	r := impl.NewAclBaselineReconciler(tree, d02Limits(), time.Now)
	drifts, err := r.Scan("/data", d02Baseline())
	if err != nil {
		t.Fatalf("Scan must succeed, got %v", err)
	}
	steps, err := r.Plan(drifts)
	if err != nil {
		t.Fatalf("Plan must succeed, got %v", err)
	}
	if len(steps) == 0 {
		t.Fatalf("expected at least one repair step")
	}
	if err := r.ApplyStep(steps[0]); err != nil {
		t.Fatalf("first ApplyStep must succeed, got %v", err)
	}
	if err := r.ApplyStep(steps[0]); err != nil {
		t.Fatalf("repeated ApplyStep must be idempotent, got %v", err)
	}
	if got := r.AppliedSteps(); got != 1 {
		t.Fatalf("applied steps = %d, want 1 after replay", got)
	}
}

func TestApplyStepRejectsStaleGeneration(t *testing.T) {
	tree := &d02Tree{snap: impl.TreeSnapshot{Generation: 5, Files: d02Files()}}
	r := impl.NewAclBaselineReconciler(tree, d02Limits(), time.Now)
	drifts, err := r.Scan("/data", d02Baseline())
	if err != nil {
		t.Fatalf("Scan must succeed, got %v", err)
	}
	steps, err := r.Plan(drifts)
	if err != nil {
		t.Fatalf("Plan must succeed, got %v", err)
	}
	if len(steps) == 0 {
		t.Fatalf("expected at least one repair step")
	}
	tree.snap.Generation = 6
	if _, err := r.Scan("/data", d02Baseline()); err != nil {
		t.Fatalf("second Scan must succeed, got %v", err)
	}
	if err := r.ApplyStep(steps[0]); !errors.Is(err, impl.ErrStaleGeneration) {
		t.Fatalf("step planned on generation 5 must be rejected after generation 6, got %v", err)
	}
	if got := r.AppliedSteps(); got != 0 {
		t.Fatalf("stale step must not be applied, got %d", got)
	}
}

func TestScanShardResumesWithBudget(t *testing.T) {
	tree := &d02Tree{snap: impl.TreeSnapshot{Generation: 5, Files: d02Files()}}
	r := impl.NewAclBaselineReconciler(tree, d02Limits(), time.Now)
	first, err := r.ScanShard("/data", d02Baseline(), 2)
	if err != nil {
		t.Fatalf("first shard must succeed, got %v", err)
	}
	if first.Scanned != 2 || first.Done {
		t.Fatalf("first shard scanned=%d done=%v, want 2/false", first.Scanned, first.Done)
	}
	second, err := r.ScanShard("/data", d02Baseline(), 2)
	if err != nil {
		t.Fatalf("second shard must succeed, got %v", err)
	}
	if second.Scanned != 1 || !second.Done || second.Remaining != 0 {
		t.Fatalf("second shard scanned=%d done=%v remaining=%d, want 1/true/0", second.Scanned, second.Done, second.Remaining)
	}
	if len(second.Drifts) != 2 {
		t.Fatalf("cumulative drifts = %d, want 2", len(second.Drifts))
	}
	if second.Generation != 5 {
		t.Fatalf("shard generation = %d, want 5", second.Generation)
	}
}

func TestScanUsesContentDigestCache(t *testing.T) {
	tree := &d02Tree{snap: impl.TreeSnapshot{Generation: 7, Files: []impl.FileAcl{{
		Path:        "/data/a",
		Depth:       0,
		ContentHash: "same-content",
		MTime:       time.Unix(1000, 0),
		Entries:     []impl.Entry{{Subject: "user:1001", Bits: 6}},
	}}}}
	baseline := impl.Baseline{Version: "v1", Rules: map[string][]impl.Entry{"/data/a": {{Subject: "user:1001", Bits: 6}}}}
	r := impl.NewAclBaselineReconciler(tree, d02Limits(), time.Now)
	if drifts, err := r.Scan("/data", baseline); err != nil {
		t.Fatalf("first scan must succeed, got %v", err)
	} else if len(drifts) != 0 {
		t.Fatalf("compliant file must not drift, got %+v", drifts)
	}
	tree.snap.Files[0].Entries = append(tree.snap.Files[0].Entries, impl.Entry{Subject: "group:50", Bits: 4})
	tree.snap.Files[0].MTime = time.Unix(9000, 0)
	drifts, err := r.Scan("/data", baseline)
	if err != nil {
		t.Fatalf("second scan must succeed, got %v", err)
	}
	if len(drifts) != 0 {
		t.Fatalf("unchanged content digest must reuse the previous verdict, got %+v", drifts)
	}
	tree.snap.Files[0].ContentHash = "changed-content"
	drifts, err = r.Scan("/data", baseline)
	if err != nil {
		t.Fatalf("third scan must succeed, got %v", err)
	}
	if len(drifts) != 1 || drifts[0].Kind != impl.DriftOverGrant {
		t.Fatalf("changed content must be re-compared, got %+v", drifts)
	}
}

func TestDigestStableAcrossInstances(t *testing.T) {
	tree := &d02Tree{snap: impl.TreeSnapshot{Generation: 5, Files: d02Files()}}
	one := impl.NewAclBaselineReconciler(tree, d02Limits(), time.Now)
	two := impl.NewAclBaselineReconciler(tree, d02Limits(), time.Now)
	if _, err := one.Scan("/data", d02Baseline()); err != nil {
		t.Fatalf("first scan must succeed, got %v", err)
	}
	if _, err := two.Scan("/data", d02Baseline()); err != nil {
		t.Fatalf("second scan must succeed, got %v", err)
	}
	d1, err := one.Digest()
	if err != nil {
		t.Fatalf("Digest must succeed, got %v", err)
	}
	d2, err := two.Digest()
	if err != nil {
		t.Fatalf("Digest must succeed, got %v", err)
	}
	if len(d1) != 64 {
		t.Fatalf("digest length = %d, want 64 hex characters of sha256", len(d1))
	}
	for _, c := range d1 {
		if !strings.ContainsRune("0123456789abcdef", c) {
			t.Fatalf("digest %q must be lowercase hex", d1)
		}
	}
	if d1 != d2 {
		t.Fatalf("digest differs for identical state: %q vs %q", d1, d2)
	}
}
