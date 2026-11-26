package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
)

func evidenceLimits() Limits {
	return Limits{MaxBatch: 8, MaxRecords: 64, MaxAudit: 32, MaxLag: 4}
}

func evidenceVersion(epoch, counter int64) Version {
	return Version{Epoch: epoch, Counter: counter}
}

func evidencePut(id, name string, version Version) Change {
	return Change{ID: id, Op: OpPut, Version: version, Origin: "source-region", Name: name}
}

func applyEvidence(t *testing.T, s *IdentityReplicaStream, region string, changes ...Change) ApplyResult {
	t.Helper()
	res, err := s.Apply(region, changes)
	if err != nil {
		t.Fatalf("apply into %s: %v", region, err)
	}
	return res
}

func TestEvidenceDuplicateBatchAndResumeIdempotent(t *testing.T) {
	s := NewIdentityReplicaStream(evidenceLimits())
	if err := s.RegisterRegion("region-a"); err != nil {
		t.Fatal(err)
	}
	batch := []Change{
		evidencePut("u1", "Alice", evidenceVersion(1, 1)),
		evidencePut("u2", "Bob", evidenceVersion(1, 2)),
	}
	if res, err := s.Apply("region-a", batch); err != nil || res.Applied != 2 {
		t.Fatalf("first apply = %+v, %v", res, err)
	}
	res, err := s.Apply("region-a", batch)
	if err != nil {
		t.Fatalf("duplicate apply: %v", err)
	}
	if res.Applied != 0 || res.Duplicates != 2 || res.Stale != 0 {
		t.Fatalf("duplicate result = %+v", res)
	}
	if audit := s.Audit("region-a"); len(audit) != 2 {
		t.Fatalf("audit len = %d, want 2", len(audit))
	}

	cp, err := s.Checkpoint("region-a")
	if err != nil {
		t.Fatal(err)
	}
	restored := NewIdentityReplicaStream(evidenceLimits())
	if err := restored.RegisterRegion("region-a"); err != nil {
		t.Fatal(err)
	}
	if err := restored.Resume("region-a", cp); err != nil {
		t.Fatal(err)
	}
	res, err = restored.Apply("region-a", batch)
	if err != nil {
		t.Fatalf("resumed duplicate apply: %v", err)
	}
	if res.Applied != 0 || res.Duplicates != 2 {
		t.Fatalf("resumed duplicate result = %+v", res)
	}
	if audit := restored.Audit("region-a"); len(audit) != 2 || audit[1].Seq != 2 {
		t.Fatalf("resumed audit = %+v", audit)
	}
}

func TestEvidenceTombstoneBoundaryAndRebuild(t *testing.T) {
	s := NewIdentityReplicaStream(evidenceLimits())
	if err := s.RegisterRegion("region-a"); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterRegion("region-b"); err != nil {
		t.Fatal(err)
	}
	applyEvidence(t, s, "region-b", evidencePut("u1", "Alice", evidenceVersion(1, 1)))
	if err := s.Tombstone("u1", evidenceVersion(1, 5)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply("region-b", []Change{
		evidencePut("other", "Other", evidenceVersion(1, 3)),
		evidencePut("u1", "Old", evidenceVersion(1, 4)),
	}); !errors.Is(err, ErrTombstoneVersion) {
		t.Fatalf("below tombstone error = %v, want ErrTombstoneVersion", err)
	}
	if _, err := s.Read("region-b", "other"); !errors.Is(err, ErrUnknownRecord) {
		t.Fatalf("failed tombstone batch leaked other record: %v", err)
	}
	if _, err := s.Apply("region-b", []Change{
		evidencePut("u1", "SameVersion", evidenceVersion(1, 5)),
	}); !errors.Is(err, ErrTombstoneVersion) {
		t.Fatalf("equal tombstone put error = %v", err)
	}
	res, err := s.Apply("region-b", []Change{{
		ID: "u1", Op: OpDelete, Version: evidenceVersion(1, 5), Origin: "region-a",
	}})
	if err != nil {
		t.Fatalf("tombstone delete propagation: %v", err)
	}
	if res.Applied != 1 {
		t.Fatalf("delete propagation result = %+v", res)
	}
	rec, err := s.Read("region-b", "u1")
	if err != nil || !rec.Deleted || rec.Version != evidenceVersion(1, 5) {
		t.Fatalf("propagated tombstone view = %+v, %v", rec, err)
	}
	applyEvidence(t, s, "region-b", evidencePut("u1", "New Alice", evidenceVersion(1, 6)))
	rec, err = s.Read("region-b", "u1")
	if err != nil || rec.Deleted || rec.Name != "New Alice" || rec.Version != evidenceVersion(1, 6) {
		t.Fatalf("rebuilt record = %+v, %v", rec, err)
	}

	if err := s.Tombstone("ghost", evidenceVersion(1, 2)); err != nil {
		t.Fatal(err)
	}
	ghost, err := s.Read("region-a", "ghost")
	if err != nil || !ghost.Deleted || ghost.Version != evidenceVersion(1, 2) {
		t.Fatalf("ghost tombstone view = %+v, %v", ghost, err)
	}
}

func TestEvidenceReadOnlyThresholdBoundary(t *testing.T) {
	limits := evidenceLimits()
	limits.MaxLag = 2
	s := NewIdentityReplicaStream(limits)
	if err := s.RegisterRegion("region-a"); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterRegion("region-b"); err != nil {
		t.Fatal(err)
	}
	applyEvidence(t, s, "region-a",
		evidencePut("a1", "A1", evidenceVersion(1, 1)),
		evidencePut("a2", "A2", evidenceVersion(1, 2)),
		evidencePut("a3", "A3", evidenceVersion(1, 3)),
		evidencePut("a4", "A4", evidenceVersion(1, 4)),
	)
	applyEvidence(t, s, "region-b", evidencePut("b1", "B1", evidenceVersion(1, 2)))
	lag, err := s.Lag("region-b")
	if err != nil || lag != 2 {
		t.Fatalf("boundary lag = %d, %v", lag, err)
	}
	readOnly, err := s.ReadOnly("region-b")
	if err != nil || readOnly {
		t.Fatalf("lag equal threshold readOnly = %v, %v; want false", readOnly, err)
	}
	applyEvidence(t, s, "region-a", evidencePut("a5", "A5", evidenceVersion(1, 5)))
	lag, err = s.Lag("region-b")
	if err != nil || lag != 3 {
		t.Fatalf("over-threshold lag = %d, %v", lag, err)
	}
	readOnly, err = s.ReadOnly("region-b")
	if err != nil || !readOnly {
		t.Fatalf("lag %d readOnly = %v, %v; want true", lag, readOnly, err)
	}
	beforeWM, _ := s.Watermark("region-b")
	beforeAudit := len(s.Audit("region-b"))
	if err := s.Write("region-b", "b1", "Blocked", evidenceVersion(9, 9)); !errors.Is(err, ErrRegionReadOnly) {
		t.Fatalf("write while read only = %v, want ErrRegionReadOnly", err)
	}
	afterWM, _ := s.Watermark("region-b")
	if beforeWM != afterWM || len(s.Audit("region-b")) != beforeAudit {
		t.Fatal("read only write changed watermark or audit")
	}
	applyEvidence(t, s, "region-b",
		evidencePut("b2", "B2", evidenceVersion(1, 3)),
		evidencePut("b3", "B3", evidenceVersion(1, 4)),
		evidencePut("b4", "B4", evidenceVersion(1, 5)),
	)
	if readOnly, err := s.ReadOnly("region-b"); err != nil || readOnly {
		t.Fatalf("caught-up readOnly = %v, %v", readOnly, err)
	}
	if err := s.Write("region-b", "b1", "Allowed", evidenceVersion(1, 6)); err != nil {
		t.Fatalf("write after catch-up: %v", err)
	}
}

func TestEvidenceCrossEpochLagSaturates(t *testing.T) {
	s := NewIdentityReplicaStream(evidenceLimits())
	if err := s.RegisterRegion("old"); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterRegion("new"); err != nil {
		t.Fatal(err)
	}
	applyEvidence(t, s, "new", evidencePut("u", "New", evidenceVersion(2, 1)))
	applyEvidence(t, s, "old", evidencePut("u", "Old", evidenceVersion(1, 9)))
	lag, err := s.Lag("old")
	if err != nil || lag <= evidenceLimits().MaxLag {
		t.Fatalf("cross-epoch lag = %d, %v; want > %d", lag, err, evidenceLimits().MaxLag)
	}
	if lag != math.MaxInt64 {
		t.Fatalf("cross-epoch lag = %d, want saturated %d", lag, math.MaxInt64)
	}
	if readOnly, err := s.ReadOnly("old"); err != nil || !readOnly {
		t.Fatalf("cross-epoch readOnly = %v, %v", readOnly, err)
	}
}

func TestEvidenceWriteEqualWatermarkRejected(t *testing.T) {
	s := NewIdentityReplicaStream(evidenceLimits())
	if err := s.RegisterRegion("region-a"); err != nil {
		t.Fatal(err)
	}
	version := evidenceVersion(1, 1)
	if err := s.Write("region-a", "u1", "Alice", version); err != nil {
		t.Fatal(err)
	}
	if err := s.Write("region-a", "u1", "Alice-Equal", version); !errors.Is(err, ErrVersionRegression) {
		t.Fatalf("equal watermark write = %v, want ErrVersionRegression", err)
	}
	rec, err := s.Read("region-a", "u1")
	if err != nil || rec.Name != "Alice" {
		t.Fatalf("record after equal write = %+v, %v", rec, err)
	}
	if audit := s.Audit("region-a"); len(audit) != 1 {
		t.Fatalf("audit len = %d, want 1", len(audit))
	}
}

func TestEvidenceBatchSizeAndVersionBoundaries(t *testing.T) {
	limits := evidenceLimits()
	limits.MaxBatch = 2
	s := NewIdentityReplicaStream(limits)
	if err := s.RegisterRegion("region-a"); err != nil {
		t.Fatal(err)
	}
	exact := []Change{
		evidencePut("u1", "A", evidenceVersion(1, 1)),
		evidencePut("u2", "B", evidenceVersion(1, 2)),
	}
	if _, err := s.Apply("region-a", exact); err != nil {
		t.Fatalf("batch at limit: %v", err)
	}
	over := append(exact, evidencePut("u3", "C", evidenceVersion(1, 3)))
	if _, err := s.Apply("region-a", over); !errors.Is(err, ErrBatchTooLarge) {
		t.Fatalf("over limit error = %v", err)
	}
	for _, version := range []Version{
		{Epoch: 0, Counter: 1},
		{Epoch: 1, Counter: 0},
		{Epoch: -1, Counter: 1},
		{Epoch: 1, Counter: -1},
	} {
		if _, err := s.Apply("region-a", []Change{evidencePut("bad", "Bad", version)}); !errors.Is(err, ErrBadVersion) {
			t.Fatalf("version %+v error = %v, want ErrBadVersion", version, err)
		}
	}
}

func TestEvidenceTamperedCheckpointAndSnapshotKeepState(t *testing.T) {
	s := NewIdentityReplicaStream(evidenceLimits())
	if err := s.RegisterRegion("region-a"); err != nil {
		t.Fatal(err)
	}
	applyEvidence(t, s, "region-a",
		evidencePut("u1", "Alice", evidenceVersion(1, 1)),
		evidencePut("u2", "Bob", evidenceVersion(1, 2)),
	)
	cp, err := s.Checkpoint("region-a")
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint map[string]any
	if err := json.Unmarshal(cp, &checkpoint); err != nil {
		t.Fatal(err)
	}
	checkpoint["checksum"] = "0000000000000000000000000000000000000000000000000000000000000000"
	badCP, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	r := NewIdentityReplicaStream(evidenceLimits())
	if err := r.RegisterRegion("region-a"); err != nil {
		t.Fatal(err)
	}
	if err := r.Resume("region-a", cp); err != nil {
		t.Fatal(err)
	}
	if err := r.Resume("region-a", badCP); !errors.Is(err, ErrBadCheckpoint) {
		t.Fatalf("tampered checkpoint error = %v", err)
	}
	if wm, err := r.Watermark("region-a"); err != nil || wm != evidenceVersion(1, 2) {
		t.Fatalf("watermark after bad checkpoint = %+v, %v", wm, err)
	}
	if _, err := r.Read("region-a", "u1"); err != nil {
		t.Fatalf("record after bad checkpoint: %v", err)
	}

	snap, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	var snapshot map[string]any
	if err := json.Unmarshal(snap, &snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot["checksum"] = "1111111111111111111111111111111111111111111111111111111111111111"
	badSnap, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Restore(badSnap); !errors.Is(err, ErrBadSnapshot) {
		t.Fatalf("tampered snapshot error = %v", err)
	}
	if wm, _ := r.Watermark("region-a"); wm != evidenceVersion(1, 2) {
		t.Fatalf("watermark after bad snapshot = %+v", wm)
	}
}

func TestEvidenceConcurrentSameVersionWritesOneAudit(t *testing.T) {
	s := NewIdentityReplicaStream(evidenceLimits())
	if err := s.RegisterRegion("region-a"); err != nil {
		t.Fatal(err)
	}

	const goroutines = 32
	var wg sync.WaitGroup
	var mu sync.Mutex
	applied := 0
	duplicates := 0
	errs := 0
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			res, err := s.Apply("region-a", []Change{
				evidencePut("u1", "Alice", evidenceVersion(1, 1)),
			})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs++
				return
			}
			applied += res.Applied
			duplicates += res.Duplicates
		}()
	}
	close(start)
	wg.Wait()

	if errs != 0 || applied != 1 || duplicates != goroutines-1 {
		t.Fatalf("concurrent redelivery errs=%d applied=%d duplicates=%d", errs, applied, duplicates)
	}
	if audit := s.Audit("region-a"); len(audit) != 1 || audit[0].Seq != 1 {
		t.Fatalf("audit = %+v, want one seq=1 entry", audit)
	}
	if wm, _ := s.Watermark("region-a"); wm != evidenceVersion(1, 1) {
		t.Fatalf("watermark = %+v", wm)
	}
}

func TestEvidenceCheckpointAfterAuditRingWrap(t *testing.T) {
	limits := evidenceLimits()
	limits.MaxAudit = 3
	s := NewIdentityReplicaStream(limits)
	if err := s.RegisterRegion("region-a"); err != nil {
		t.Fatal(err)
	}
	for i := int64(1); i <= 5; i++ {
		applyEvidence(t, s, "region-a", evidencePut(
			fmt.Sprintf("u%d", i), "name", evidenceVersion(1, i),
		))
	}
	cp, err := s.Checkpoint("region-a")
	if err != nil {
		t.Fatal(err)
	}
	r := NewIdentityReplicaStream(limits)
	if err := r.RegisterRegion("region-a"); err != nil {
		t.Fatal(err)
	}
	if err := r.Resume("region-a", cp); err != nil {
		t.Fatal(err)
	}
	audit := r.Audit("region-a")
	if len(audit) != 3 || audit[0].ID != "u3" || audit[2].Seq != 5 {
		t.Fatalf("wrapped checkpoint audit = %+v", audit)
	}
	if _, err := r.Apply("region-a", []Change{
		evidencePut("u6", "Name", evidenceVersion(1, 6)),
	}); err != nil {
		t.Fatal(err)
	}
	audit = r.Audit("region-a")
	if len(audit) != 3 || audit[0].ID != "u4" || audit[1].ID != "u5" || audit[2].ID != "u6" {
		t.Fatalf("post-resume wrapped audit = %+v", audit)
	}
	if audit[2].Seq != 6 {
		t.Fatalf("post-resume last seq = %d, want 6", audit[2].Seq)
	}
}
