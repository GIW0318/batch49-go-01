package task

import (
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	impl "gsb/batch49-go-safety-e-12"
)

// Eight byte blocks used to build deterministic banded samples.
const (
	blk0 = "band-000"
	blk1 = "band-001"
	blk2 = "band-002"
	blk3 = "band-003"
	blk4 = "band-004"
	blk5 = "band-005"
	blk6 = "band-006"
	blk7 = "band-007"
)

func sampleC() impl.Sample {
	return impl.Sample{ID: "c-1", Data: blk0 + blk1 + blk2 + blk3, AddedAt: time.Unix(1000, 0)}
}

func sampleNear() impl.Sample {
	return impl.Sample{ID: "c-2", Data: blk0 + blk1 + blk2 + blk4, AddedAt: time.Unix(1001, 0)}
}

func sampleFar() impl.Sample {
	return impl.Sample{ID: "c-3", Data: blk0 + "zzzz-999" + "yyyy-888" + "xxxx-777", AddedAt: time.Unix(1002, 0)}
}

func sampleFlat() impl.Sample {
	return impl.Sample{ID: "c-4", Data: blk0 + blk0 + blk0 + blk0, AddedAt: time.Unix(1003, 0)}
}

func sampleLong() impl.Sample {
	return impl.Sample{ID: "c-5", Data: blk0 + blk5 + blk6 + blk7 + blk0, AddedAt: time.Unix(1004, 0)}
}

func sampleOtherBucket() impl.Sample {
	return impl.Sample{ID: "c-9", Data: blk7 + blk6 + blk5 + blk4, AddedAt: time.Unix(1005, 0)}
}

func testConfig() impl.Config {
	return impl.Config{BandSize: 8, Threshold: 0.7, MaxPerBucket: 16, TTL: 24 * time.Hour}
}

func TestSimilarIdenticalSamplesIsOne(t *testing.T) {
	d := impl.NewCorpusDeduper(testConfig())
	got, err := d.Similar(sampleC(), sampleC())
	if err != nil {
		t.Fatalf("similar must succeed, got %v", err)
	}
	if got != 1.0 {
		t.Fatalf("similarity of identical samples = %v, want 1", got)
	}
}

func TestSimilarDisjointSamplesIsZero(t *testing.T) {
	d := impl.NewCorpusDeduper(testConfig())
	got, err := d.Similar(sampleC(), sampleOtherBucket())
	if err != nil {
		t.Fatalf("similar must succeed, got %v", err)
	}
	if got != 0.0 {
		t.Fatalf("similarity of disjoint samples = %v, want 0", got)
	}
}

func TestSimilarPartialOverlapIsFraction(t *testing.T) {
	d := impl.NewCorpusDeduper(testConfig())
	got, err := d.Similar(sampleC(), sampleNear())
	if err != nil {
		t.Fatalf("similar must succeed, got %v", err)
	}
	if got != 0.75 {
		t.Fatalf("similarity with 3 of 4 shared bands = %v, want 0.75", got)
	}
}

func TestFingerprintBandsAndBucket(t *testing.T) {
	d := impl.NewCorpusDeduper(testConfig())
	if _, _, err := d.Add(sampleC()); err != nil {
		t.Fatalf("add must succeed, got %v", err)
	}
	fp, err := d.FingerprintOf(sampleC())
	if err != nil {
		t.Fatalf("fingerprint must succeed, got %v", err)
	}
	if len(fp.Bands) != 4 {
		t.Fatalf("band count = %d, want 4 for 32 bytes with 8 byte bands", len(fp.Bands))
	}
	if !strings.HasPrefix(fp.Bucket, "band0-") {
		t.Fatalf("bucket = %q, want a band0- prefix", fp.Bucket)
	}
	bucket, ok := d.BucketOf("c-1")
	if !ok {
		t.Fatalf("stored sample must have a bucket")
	}
	if bucket != fp.Bucket {
		t.Fatalf("BucketOf = %q, FingerprintOf = %q, want the same bucket", bucket, fp.Bucket)
	}
	nearFP, err := d.FingerprintOf(sampleNear())
	if err != nil {
		t.Fatalf("fingerprint of near sample must succeed, got %v", err)
	}
	if nearFP.Bucket != fp.Bucket {
		t.Fatalf("samples sharing the first band landed in %q and %q", nearFP.Bucket, fp.Bucket)
	}
}

func TestAddNewSampleReturnsNew(t *testing.T) {
	d := impl.NewCorpusDeduper(testConfig())
	verdict, rep, err := d.Add(sampleC())
	if err != nil {
		t.Fatalf("add must succeed, got %v", err)
	}
	if verdict != impl.VerdictNew {
		t.Fatalf("verdict = %q, want %q", verdict, impl.VerdictNew)
	}
	if rep != "c-1" {
		t.Fatalf("representative = %q, want the first sample c-1", rep)
	}
	if got := d.Count(); got != 1 {
		t.Fatalf("count = %d, want 1", got)
	}
}

func TestAddNearDuplicateReturnsDuplicate(t *testing.T) {
	d := impl.NewCorpusDeduper(testConfig())
	if _, _, err := d.Add(sampleC()); err != nil {
		t.Fatalf("seed add must succeed, got %v", err)
	}
	verdict, rep, err := d.Add(sampleNear())
	if err != nil {
		t.Fatalf("add must succeed, got %v", err)
	}
	if verdict != impl.VerdictDuplicate {
		t.Fatalf("verdict = %q, want %q", verdict, impl.VerdictDuplicate)
	}
	if rep != "c-1" {
		t.Fatalf("representative = %q, want the seed sample c-1", rep)
	}
	if got := d.Count(); got != 1 {
		t.Fatalf("count = %d, want the duplicate to be dropped", got)
	}
}

func TestAddFarSampleSameBucketIsNew(t *testing.T) {
	d := impl.NewCorpusDeduper(testConfig())
	if _, _, err := d.Add(sampleC()); err != nil {
		t.Fatalf("seed add must succeed, got %v", err)
	}
	verdict, _, err := d.Add(sampleFar())
	if err != nil {
		t.Fatalf("add must succeed, got %v", err)
	}
	if verdict != impl.VerdictNew {
		t.Fatalf("verdict = %q, want %q for a sample below the threshold", verdict, impl.VerdictNew)
	}
	if got := d.Count(); got != 2 {
		t.Fatalf("count = %d, want 2", got)
	}
}

func TestAddRejectsEmptyData(t *testing.T) {
	d := impl.NewCorpusDeduper(testConfig())
	_, _, err := d.Add(impl.Sample{ID: "c-1"})
	if !errors.Is(err, impl.ErrEmptySample) {
		t.Fatalf("empty data error = %v, want ErrEmptySample", err)
	}
}

func TestAddRejectsEmptyID(t *testing.T) {
	d := impl.NewCorpusDeduper(testConfig())
	_, _, err := d.Add(impl.Sample{Data: blk0 + blk1})
	if !errors.Is(err, impl.ErrEmptySample) {
		t.Fatalf("empty id error = %v, want ErrEmptySample", err)
	}
}

func TestAddSameIDIsIdempotent(t *testing.T) {
	d := impl.NewCorpusDeduper(testConfig())
	if _, _, err := d.Add(sampleC()); err != nil {
		t.Fatalf("seed add must succeed, got %v", err)
	}
	same := sampleC()
	same.AddedAt = time.Unix(2000, 0)
	verdict, rep, err := d.Add(same)
	if err != nil {
		t.Fatalf("repeated add must succeed, got %v", err)
	}
	if verdict != impl.VerdictDuplicate {
		t.Fatalf("verdict = %q, want %q for a repeated sample id", verdict, impl.VerdictDuplicate)
	}
	if rep != "c-1" {
		t.Fatalf("representative = %q, want c-1", rep)
	}
	if got := d.Count(); got != 1 {
		t.Fatalf("count = %d, want 1", got)
	}
}

func TestBucketsSortedUnique(t *testing.T) {
	d := impl.NewCorpusDeduper(testConfig())
	if _, _, err := d.Add(sampleC()); err != nil {
		t.Fatalf("first add must succeed, got %v", err)
	}
	if _, _, err := d.Add(sampleOtherBucket()); err != nil {
		t.Fatalf("second add must succeed, got %v", err)
	}
	buckets := d.Buckets()
	if len(buckets) != 2 {
		t.Fatalf("bucket count = %d, want 2", len(buckets))
	}
	if !sort.StringsAreSorted(buckets) {
		t.Fatalf("buckets = %v, want ascending order", buckets)
	}
	if buckets[0] == buckets[1] {
		t.Fatalf("buckets = %v, want two distinct names", buckets)
	}
}

func TestRepresentativePrefersMoreFeatures(t *testing.T) {
	d := impl.NewCorpusDeduper(testConfig())
	if _, _, err := d.Add(sampleFlat()); err != nil {
		t.Fatalf("flat add must succeed, got %v", err)
	}
	if _, _, err := d.Add(sampleC()); err != nil {
		t.Fatalf("rich add must succeed, got %v", err)
	}
	bucket, ok := d.BucketOf("c-1")
	if !ok {
		t.Fatalf("sample c-1 must be stored")
	}
	rep, ok := d.Representative(bucket)
	if !ok {
		t.Fatalf("bucket %q must have a representative", bucket)
	}
	if rep != "c-1" {
		t.Fatalf("representative = %q, want the sample with 4 distinct bands", rep)
	}
}

func TestRepresentativePrefersLongerOnFeatureTie(t *testing.T) {
	d := impl.NewCorpusDeduper(testConfig())
	if _, _, err := d.Add(sampleC()); err != nil {
		t.Fatalf("short add must succeed, got %v", err)
	}
	if _, _, err := d.Add(sampleLong()); err != nil {
		t.Fatalf("long add must succeed, got %v", err)
	}
	bucket, ok := d.BucketOf("c-1")
	if !ok {
		t.Fatalf("sample c-1 must be stored")
	}
	rep, ok := d.Representative(bucket)
	if !ok {
		t.Fatalf("bucket %q must have a representative", bucket)
	}
	if rep != "c-5" {
		t.Fatalf("representative = %q, want the longer sample c-5 on a feature tie", rep)
	}
}

func TestEvictDropsOldestFingerprintKeepsSample(t *testing.T) {
	cfg := testConfig()
	cfg.MaxPerBucket = 1
	d := impl.NewCorpusDeduper(cfg)
	first := sampleC()
	second := sampleFar()
	if _, _, err := d.Add(first); err != nil {
		t.Fatalf("first add must succeed, got %v", err)
	}
	if _, _, err := d.Add(second); err != nil {
		t.Fatalf("second add must succeed, got %v", err)
	}
	if got := d.Evict(time.Unix(1002, 0)); got != 1 {
		t.Fatalf("evicted = %d, want 1 over capacity fingerprint", got)
	}
	if got := d.Count(); got != 2 {
		t.Fatalf("count = %d, want both samples to stay readable", got)
	}
	if _, ok := d.Lookup("c-1"); !ok {
		t.Fatalf("sample c-1 must stay readable after its fingerprint was evicted")
	}
	bucket, ok := d.BucketOf("c-3")
	if !ok {
		t.Fatalf("sample c-3 must be stored")
	}
	rep, ok := d.Representative(bucket)
	if !ok {
		t.Fatalf("bucket %q must still have a live representative", bucket)
	}
	if rep != "c-3" {
		t.Fatalf("representative = %q, want the only live fingerprint c-3", rep)
	}
}

func TestSnapshotRestoreKeepsDuplicateVerdict(t *testing.T) {
	d := impl.NewCorpusDeduper(testConfig())
	if _, _, err := d.Add(sampleC()); err != nil {
		t.Fatalf("seed add must succeed, got %v", err)
	}
	payload, err := d.Snapshot()
	if err != nil {
		t.Fatalf("snapshot must succeed, got %v", err)
	}
	restored := impl.NewCorpusDeduper(testConfig())
	if err := restored.Restore(payload); err != nil {
		t.Fatalf("restore must succeed, got %v", err)
	}
	if got := restored.Count(); got != 1 {
		t.Fatalf("restored count = %d, want 1", got)
	}
	verdict, rep, err := restored.Add(sampleNear())
	if err != nil {
		t.Fatalf("add after restore must succeed, got %v", err)
	}
	if verdict != impl.VerdictDuplicate || rep != "c-1" {
		t.Fatalf("verdict, rep = %q, %q, want duplicate of c-1", verdict, rep)
	}
	if err := restored.Restore([]byte("<<not a snapshot>>")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt restore error = %v, want ErrBadSnapshot", err)
	}
	if got := restored.Count(); got != 1 {
		t.Fatalf("count after corrupt restore = %d, want the previous state of 1", got)
	}
}
