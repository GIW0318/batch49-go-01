package task

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	impl "gsb/batch49-go-safety-a-12"
)

func testLimits() impl.Limits {
	return impl.Limits{
		MaxBatch:   8,
		MaxRecords: 64,
		MaxAudit:   32,
		MaxLag:     4,
	}
}

func v(epoch, counter int64) impl.Version {
	return impl.Version{Epoch: epoch, Counter: counter}
}

func put(id, name string, version impl.Version) impl.Change {
	return impl.Change{ID: id, Op: impl.OpPut, Version: version, Origin: "region-b", Name: name}
}

func mustRegion(t *testing.T, s *impl.IdentityReplicaStream, regionID string) {
	t.Helper()
	if err := s.RegisterRegion(regionID); err != nil {
		t.Fatalf("register %s: %v", regionID, err)
	}
}

func mustApply(t *testing.T, s *impl.IdentityReplicaStream, regionID string, batch ...impl.Change) impl.ApplyResult {
	t.Helper()
	res, err := s.Apply(regionID, batch)
	if err != nil {
		t.Fatalf("apply into %s: %v", regionID, err)
	}
	return res
}

func TestRegisterRegionAndDuplicate(t *testing.T) {
	s := impl.NewIdentityReplicaStream(testLimits())
	if err := s.RegisterRegion("  "); !errors.Is(err, impl.ErrEmptyRegion) {
		t.Fatalf("blank region must fail with ErrEmptyRegion, got %v", err)
	}
	if err := s.RegisterRegion("region-a"); err != nil {
		t.Fatalf("register region-a: %v", err)
	}
	if err := s.RegisterRegion("region-a"); !errors.Is(err, impl.ErrRegionExists) {
		t.Fatalf("repeat register must fail with ErrRegionExists, got %v", err)
	}
	wm, err := s.Watermark("region-a")
	if err != nil {
		t.Fatalf("watermark: %v", err)
	}
	if wm != (impl.Version{}) {
		t.Fatalf("fresh watermark = %+v, want the zero version", wm)
	}
	if _, err := s.Watermark("region-x"); !errors.Is(err, impl.ErrUnknownRegion) {
		t.Fatalf("watermark of an unknown region must fail with ErrUnknownRegion, got %v", err)
	}
}

func TestApplyBatchAdvancesWatermark(t *testing.T) {
	s := impl.NewIdentityReplicaStream(testLimits())
	mustRegion(t, s, "region-a")
	res := mustApply(t, s, "region-a", put("u1", "Alice", v(1, 1)), put("u2", "Bob", v(1, 2)))
	if res.Applied != 2 || res.Duplicates != 0 || res.Stale != 0 {
		t.Fatalf("result = %+v, want two applied and no duplicates", res)
	}
	if res.Watermark != v(1, 2) {
		t.Fatalf("result watermark = %+v, want v1.2", res.Watermark)
	}
	wm, err := s.Watermark("region-a")
	if err != nil {
		t.Fatalf("watermark: %v", err)
	}
	if wm != v(1, 2) {
		t.Fatalf("watermark = %+v, want v1.2", wm)
	}
	rec, err := s.Read("region-a", "u1")
	if err != nil {
		t.Fatalf("read u1: %v", err)
	}
	if rec.Name != "Alice" || rec.Version != v(1, 1) || rec.Deleted || rec.Origin != "region-b" {
		t.Fatalf("record = %+v, want Alice at v1.1 from region-b", rec)
	}
	if g := s.GlobalVersion(); g != v(1, 2) {
		t.Fatalf("global version = %+v, want v1.2", g)
	}
	audit := s.Audit("region-a")
	if len(audit) != 2 {
		t.Fatalf("audit length = %d, want 2", len(audit))
	}
	if audit[0].Seq != 1 || audit[1].Seq != 2 {
		t.Fatalf("audit seqs = [%d %d], want [1 2]", audit[0].Seq, audit[1].Seq)
	}
	if audit[1].ID != "u2" || audit[1].Reason != impl.ReasonApplied {
		t.Fatalf("last audit entry = %+v, want u2 applied", audit[1])
	}
}

func TestApplyRejectsUnknownRegionAndBadInput(t *testing.T) {
	s := impl.NewIdentityReplicaStream(testLimits())
	mustRegion(t, s, "region-a")
	if _, err := s.Apply("region-x", []impl.Change{put("u1", "Alice", v(1, 1))}); !errors.Is(err, impl.ErrUnknownRegion) {
		t.Fatalf("apply into an unknown region must fail with ErrUnknownRegion, got %v", err)
	}
	if _, err := s.Apply("region-a", nil); !errors.Is(err, impl.ErrBadBatch) {
		t.Fatalf("an empty batch must fail with ErrBadBatch, got %v", err)
	}
	if _, err := s.Apply("region-a", []impl.Change{put("u1", "Alice", v(0, 0))}); !errors.Is(err, impl.ErrBadVersion) {
		t.Fatalf("a zero version must fail with ErrBadVersion, got %v", err)
	}
	if _, err := s.Apply("region-a", []impl.Change{{ID: "u1", Op: "merge", Version: v(1, 1)}}); !errors.Is(err, impl.ErrBadOp) {
		t.Fatalf("an unknown operation must fail with ErrBadOp, got %v", err)
	}
	if _, err := s.Apply("region-a", []impl.Change{put("  ", "Alice", v(1, 1))}); !errors.Is(err, impl.ErrEmptyUser) {
		t.Fatalf("a blank identity must fail with ErrEmptyUser, got %v", err)
	}
	wm, err := s.Watermark("region-a")
	if err != nil {
		t.Fatalf("watermark: %v", err)
	}
	if wm != (impl.Version{}) {
		t.Fatalf("rejected batches must not move the watermark, got %+v", wm)
	}
	if len(s.Audit("region-a")) != 0 {
		t.Fatalf("rejected batches must not be audited")
	}
}

func TestApplyBatchLimitIsAtomic(t *testing.T) {
	limits := testLimits()
	limits.MaxBatch = 2
	s := impl.NewIdentityReplicaStream(limits)
	mustRegion(t, s, "region-a")
	_, err := s.Apply("region-a", []impl.Change{
		put("u1", "Alice", v(1, 1)),
		put("u2", "Bob", v(1, 2)),
		put("u3", "Carol", v(1, 3)),
	})
	if !errors.Is(err, impl.ErrBatchTooLarge) {
		t.Fatalf("an oversized batch must fail with ErrBatchTooLarge, got %v", err)
	}
	wm, err := s.Watermark("region-a")
	if err != nil {
		t.Fatalf("watermark: %v", err)
	}
	if wm != (impl.Version{}) {
		t.Fatalf("an oversized batch must not apply partially, watermark = %+v", wm)
	}
	if len(s.Audit("region-a")) != 0 {
		t.Fatalf("an oversized batch must not be audited")
	}
}

func TestApplyRejectsDescendingBatch(t *testing.T) {
	s := impl.NewIdentityReplicaStream(testLimits())
	mustRegion(t, s, "region-a")
	_, err := s.Apply("region-a", []impl.Change{
		put("u1", "Alice", v(1, 2)),
		put("u2", "Bob", v(1, 1)),
	})
	if !errors.Is(err, impl.ErrVersionRegression) {
		t.Fatalf("a descending batch must fail with ErrVersionRegression, got %v", err)
	}
	wm, err := s.Watermark("region-a")
	if err != nil {
		t.Fatalf("watermark: %v", err)
	}
	if wm != (impl.Version{}) {
		t.Fatalf("rejected batch must not move the watermark, got %+v", wm)
	}
	if len(s.Audit("region-a")) != 0 {
		t.Fatalf("rejected batch must not be audited")
	}
}

func TestApplyRejectsDuplicateVersionInBatch(t *testing.T) {
	s := impl.NewIdentityReplicaStream(testLimits())
	mustRegion(t, s, "region-a")
	_, err := s.Apply("region-a", []impl.Change{
		put("u1", "Alice", v(1, 1)),
		put("u2", "Bob", v(1, 1)),
	})
	if !errors.Is(err, impl.ErrVersionRegression) {
		t.Fatalf("equal versions in one batch must fail with ErrVersionRegression, got %v", err)
	}
	if _, err := s.Read("region-a", "u1"); !errors.Is(err, impl.ErrUnknownRecord) {
		t.Fatalf("the rejected batch must leave no record, got %v", err)
	}
}

func TestCompareVersionOrdering(t *testing.T) {
	if got := impl.CompareVersion(v(1, 1), v(1, 2)); got >= 0 {
		t.Fatalf("v1.1 versus v1.2 = %d, want a negative result", got)
	}
	if got := impl.CompareVersion(v(1, 9), v(1, 2)); got <= 0 {
		t.Fatalf("v1.9 versus v1.2 = %d, want a positive result", got)
	}
	if got := impl.CompareVersion(v(2, 1), v(1, 9)); got <= 0 {
		t.Fatalf("epoch 2 counter 1 versus epoch 1 counter 9 = %d, want the epoch to dominate", got)
	}
	if got := impl.CompareVersion(v(3, 4), v(3, 4)); got != 0 {
		t.Fatalf("equal versions = %d, want 0", got)
	}
}

func TestLocalWriteOnlyTouchesItsRegion(t *testing.T) {
	s := impl.NewIdentityReplicaStream(testLimits())
	mustRegion(t, s, "region-a")
	mustRegion(t, s, "region-b")
	if err := s.Write("region-a", "u1", "Alice", v(1, 3)); err != nil {
		t.Fatalf("local write: %v", err)
	}
	rec, err := s.Read("region-a", "u1")
	if err != nil {
		t.Fatalf("read on region-a: %v", err)
	}
	if rec.Name != "Alice" || rec.Version != v(1, 3) || rec.Deleted || rec.Origin != "region-a" {
		t.Fatalf("record = %+v, want Alice at v1.3 originated in region-a", rec)
	}
	if _, err := s.Read("region-b", "u1"); !errors.Is(err, impl.ErrUnknownRecord) {
		t.Fatalf("region-b must not see the local write, got %v", err)
	}
	wmA, err := s.Watermark("region-a")
	if err != nil {
		t.Fatalf("watermark region-a: %v", err)
	}
	if wmA != v(1, 3) {
		t.Fatalf("region-a watermark = %+v, want v1.3", wmA)
	}
	wmB, err := s.Watermark("region-b")
	if err != nil {
		t.Fatalf("watermark region-b: %v", err)
	}
	if wmB != (impl.Version{}) {
		t.Fatalf("region-b watermark = %+v, want untouched", wmB)
	}
	audit := s.Audit("region-a")
	if len(audit) != 1 || audit[0].Reason != impl.ReasonLocalWrite {
		t.Fatalf("audit = %+v, want one local write entry", audit)
	}
}

func TestWriteRejectsVersionRegression(t *testing.T) {
	s := impl.NewIdentityReplicaStream(testLimits())
	mustRegion(t, s, "region-a")
	if err := s.Write("region-a", "u1", "Alice", v(1, 2)); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := s.Write("region-a", "u1", "Alice-old", v(1, 1)); !errors.Is(err, impl.ErrVersionRegression) {
		t.Fatalf("a lower local version must fail with ErrVersionRegression, got %v", err)
	}
	rec, err := s.Read("region-a", "u1")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if rec.Name != "Alice" || rec.Version != v(1, 2) {
		t.Fatalf("record = %+v, want the v1.2 value kept", rec)
	}
	if len(s.Audit("region-a")) != 1 {
		t.Fatalf("the rejected write must not be audited")
	}
}

func TestTombstoneMarksRecordDeleted(t *testing.T) {
	s := impl.NewIdentityReplicaStream(testLimits())
	mustRegion(t, s, "region-a")
	mustApply(t, s, "region-a", put("u1", "Alice", v(1, 2)))
	if err := s.Tombstone("u1", v(1, 5)); err != nil {
		t.Fatalf("tombstone: %v", err)
	}
	rec, err := s.Read("region-a", "u1")
	if err != nil {
		t.Fatalf("read after tombstone: %v", err)
	}
	if !rec.Deleted {
		t.Fatalf("record = %+v, want a deleted view", rec)
	}
	if rec.Version != v(1, 5) {
		t.Fatalf("deleted version = %+v, want the tombstone version v1.5", rec.Version)
	}
	if err := s.Tombstone("u1", v(1, 3)); !errors.Is(err, impl.ErrVersionRegression) {
		t.Fatalf("a lower tombstone must fail with ErrVersionRegression, got %v", err)
	}
	if err := s.Tombstone("  ", v(1, 6)); !errors.Is(err, impl.ErrEmptyUser) {
		t.Fatalf("a blank identity must fail with ErrEmptyUser, got %v", err)
	}
	if err := s.Tombstone("u2", v(0, 1)); !errors.Is(err, impl.ErrBadVersion) {
		t.Fatalf("a zero epoch must fail with ErrBadVersion, got %v", err)
	}
}

func TestLagReportsDistance(t *testing.T) {
	s := impl.NewIdentityReplicaStream(testLimits())
	mustRegion(t, s, "region-a")
	mustRegion(t, s, "region-b")
	mustApply(t, s, "region-a",
		put("u1", "A1", v(1, 1)),
		put("u2", "A2", v(1, 2)),
		put("u3", "A3", v(1, 3)),
		put("u4", "A4", v(1, 4)),
		put("u5", "A5", v(1, 5)),
	)
	mustApply(t, s, "region-b", put("u1", "B1", v(1, 1)), put("u2", "B2", v(1, 2)))
	lagA, err := s.Lag("region-a")
	if err != nil {
		t.Fatalf("lag region-a: %v", err)
	}
	if lagA != 0 {
		t.Fatalf("lag of the leading region = %d, want 0", lagA)
	}
	lagB, err := s.Lag("region-b")
	if err != nil {
		t.Fatalf("lag region-b: %v", err)
	}
	if lagB != 3 {
		t.Fatalf("lag of region-b = %d, want 3", lagB)
	}
	if _, err := s.Lag("region-x"); !errors.Is(err, impl.ErrUnknownRegion) {
		t.Fatalf("lag of an unknown region must fail with ErrUnknownRegion, got %v", err)
	}
}

func TestAuditRetainsNewest(t *testing.T) {
	limits := testLimits()
	limits.MaxAudit = 3
	s := impl.NewIdentityReplicaStream(limits)
	mustRegion(t, s, "region-a")
	for i := 1; i <= 5; i++ {
		mustApply(t, s, "region-a", put(fmt.Sprintf("u%d", i), "name", v(1, int64(i))))
	}
	audit := s.Audit("region-a")
	if len(audit) != 3 {
		t.Fatalf("retained audit = %d, want the cap 3", len(audit))
	}
	for i := 1; i < len(audit); i++ {
		if audit[i].Seq <= audit[i-1].Seq {
			t.Fatalf("audit seq must strictly increase, got %d then %d", audit[i-1].Seq, audit[i].Seq)
		}
	}
	if audit[0].ID != "u3" || audit[2].ID != "u5" {
		t.Fatalf("retained ids = [%s %s %s], want the newest window [u3 u4 u5]", audit[0].ID, audit[1].ID, audit[2].ID)
	}
	if audit[2].Seq != 5 {
		t.Fatalf("last seq = %d, want 5", audit[2].Seq)
	}
}

func TestCheckpointResumeRestoresRegion(t *testing.T) {
	limits := testLimits()
	s := impl.NewIdentityReplicaStream(limits)
	mustRegion(t, s, "region-a")
	mustApply(t, s, "region-a", put("u1", "Alice", v(1, 1)), put("u2", "Bob", v(1, 2)))
	cp, err := s.Checkpoint("region-a")
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if len(cp) == 0 {
		t.Fatalf("checkpoint payload must not be empty")
	}
	restored := impl.NewIdentityReplicaStream(limits)
	mustRegion(t, restored, "region-a")
	if err := restored.Resume("region-a", cp); err != nil {
		t.Fatalf("resume: %v", err)
	}
	wm, err := restored.Watermark("region-a")
	if err != nil {
		t.Fatalf("restored watermark: %v", err)
	}
	if wm != v(1, 2) {
		t.Fatalf("restored watermark = %+v, want v1.2", wm)
	}
	rec, err := restored.Read("region-a", "u1")
	if err != nil {
		t.Fatalf("restored read: %v", err)
	}
	if rec.Name != "Alice" || rec.Version != v(1, 1) {
		t.Fatalf("restored record = %+v, want Alice at v1.1", rec)
	}
	if err := restored.Resume("region-a", []byte("not-a-checkpoint")); !errors.Is(err, impl.ErrBadCheckpoint) {
		t.Fatalf("a corrupt checkpoint must fail with ErrBadCheckpoint, got %v", err)
	}
	if _, err := s.Checkpoint("region-x"); !errors.Is(err, impl.ErrUnknownRegion) {
		t.Fatalf("checkpoint of an unknown region must fail with ErrUnknownRegion, got %v", err)
	}
	mustApply(t, restored, "region-a", put("u3", "Carol", v(1, 3)))
	wmAfter, err := restored.Watermark("region-a")
	if err != nil {
		t.Fatalf("watermark after resume apply: %v", err)
	}
	if wmAfter != v(1, 3) {
		t.Fatalf("watermark after resume = %+v, want v1.3", wmAfter)
	}
}

func TestSnapshotRestoreWholeStream(t *testing.T) {
	limits := testLimits()
	s := impl.NewIdentityReplicaStream(limits)
	mustRegion(t, s, "region-a")
	mustRegion(t, s, "region-b")
	mustApply(t, s, "region-a", put("u1", "Alice", v(1, 1)), put("u2", "Bob", v(1, 2)))
	if err := s.Write("region-b", "u9", "Zoe", v(1, 4)); err != nil {
		t.Fatalf("local write: %v", err)
	}
	data, err := s.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload must not be empty")
	}
	restored := impl.NewIdentityReplicaStream(limits)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	wmA, err := restored.Watermark("region-a")
	if err != nil {
		t.Fatalf("watermark region-a: %v", err)
	}
	if wmA != v(1, 2) {
		t.Fatalf("restored region-a watermark = %+v, want v1.2", wmA)
	}
	wmB, err := restored.Watermark("region-b")
	if err != nil {
		t.Fatalf("watermark region-b: %v", err)
	}
	if wmB != v(1, 4) {
		t.Fatalf("restored region-b watermark = %+v, want v1.4", wmB)
	}
	rec, err := restored.Read("region-b", "u9")
	if err != nil {
		t.Fatalf("restored read: %v", err)
	}
	if rec.Name != "Zoe" {
		t.Fatalf("restored record = %+v, want Zoe", rec)
	}
	if err := restored.Restore([]byte("}{")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("a corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	wmKeep, err := restored.Watermark("region-a")
	if err != nil {
		t.Fatalf("watermark after rejected restore: %v", err)
	}
	if wmKeep != v(1, 2) {
		t.Fatalf("a rejected restore must keep the live state, watermark = %+v", wmKeep)
	}
	mustApply(t, restored, "region-a", put("u5", "Eve", v(1, 9)))
}

func TestConcurrentApplySingleRegion(t *testing.T) {
	limits := testLimits()
	limits.MaxBatch = 1
	limits.MaxAudit = 64
	s := impl.NewIdentityReplicaStream(limits)
	mustRegion(t, s, "region-a")
	var wg sync.WaitGroup
	var mu sync.Mutex
	failures := 0
	for i := 1; i <= 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := s.Apply("region-a", []impl.Change{put(fmt.Sprintf("u%02d", i), "name", v(1, int64(i)))})
			mu.Lock()
			defer mu.Unlock()
			if err != nil || res.Applied != 1 {
				failures++
			}
		}(i)
	}
	wg.Wait()
	if failures != 0 {
		t.Fatalf("%d concurrent applies failed, want every unique version to apply", failures)
	}
	wm, err := s.Watermark("region-a")
	if err != nil {
		t.Fatalf("watermark: %v", err)
	}
	if wm != v(1, 20) {
		t.Fatalf("watermark = %+v, want v1.20", wm)
	}
	audit := s.Audit("region-a")
	if len(audit) != 20 {
		t.Fatalf("audit length = %d, want 20", len(audit))
	}
	if audit[19].Seq != 20 {
		t.Fatalf("last audit seq = %d, want 20", audit[19].Seq)
	}
}
