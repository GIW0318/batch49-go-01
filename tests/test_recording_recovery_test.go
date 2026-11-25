package task

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	impl "gsb/batch49-go-game-c-15"
)

func writeSegment(t *testing.T, dir, name string, lines ...string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write segment %s: %v", name, err)
	}
	return path
}

func writeRawSegment(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write segment %s: %v", name, err)
	}
	return path
}

func frameLine(ptsMs, durMs int, tracks string) string {
	return fmt.Sprintf("%d %d %s", ptsMs, durMs, tracks)
}

func recoveryConfig() impl.Config {
	return impl.Config{MaxSegments: 16, MaxFillMs: 60000}
}

func scanDir(t *testing.T, r *impl.RecordingRecovery, dir string) impl.ScanCursor {
	t.Helper()
	cur, err := r.Scan(dir)
	if err != nil {
		t.Fatalf("Scan(%q) failed: %v", dir, err)
	}
	return cur
}

func rebuildDir(t *testing.T, r *impl.RecordingRecovery, cur impl.ScanCursor) impl.Report {
	t.Helper()
	rep, err := r.Rebuild(cur)
	if err != nil {
		t.Fatalf("Rebuild failed: %v", err)
	}
	return rep
}

func TestScanRejectsMissingRoot(t *testing.T) {
	dir := t.TempDir()
	r := impl.NewRecordingRecovery(recoveryConfig())
	if _, err := r.Scan(filepath.Join(dir, "missing")); !errors.Is(err, impl.ErrScanRoot) {
		t.Fatalf("missing directory must fail with ErrScanRoot, got %v", err)
	}
	file := filepath.Join(dir, "recording.seg")
	writeRawSegment(t, dir, "recording.seg", frameLine(0, 1000, "va")+"\n")
	if _, err := r.Scan(file); !errors.Is(err, impl.ErrScanRoot) {
		t.Fatalf("a regular file must fail with ErrScanRoot, got %v", err)
	}
}

func TestEmptyDirectoryRebuildsEmptyIndex(t *testing.T) {
	dir := t.TempDir()
	r := impl.NewRecordingRecovery(recoveryConfig())
	cur := scanDir(t, r, dir)
	if cur.Scanned != 0 || cur.Last != "" {
		t.Fatalf("cursor = %+v, want an empty scan", cur)
	}
	rep := rebuildDir(t, r, cur)
	if len(rep.Segments) != 0 {
		t.Fatalf("segments = %d, want 0", len(rep.Segments))
	}
	if rep.TotalMs != 0 || rep.FilledMs != 0 || rep.Repaired != 0 {
		t.Fatalf("report totals = %+v, want zeros", rep)
	}
	if rep.StopReason != impl.StopComplete {
		t.Fatalf("stop reason = %q, want %q", rep.StopReason, impl.StopComplete)
	}
}

func TestScanOrdersSegmentsByName(t *testing.T) {
	dir := t.TempDir()
	writeSegment(t, dir, "seg-0003.seg", frameLine(4000, 1000, "va"))
	writeSegment(t, dir, "seg-0001.seg", frameLine(0, 1000, "va"))
	writeSegment(t, dir, "seg-0002.seg", frameLine(2000, 1000, "va"))
	r := impl.NewRecordingRecovery(recoveryConfig())
	cur := scanDir(t, r, dir)
	if cur.Scanned != 3 {
		t.Fatalf("scanned = %d, want 3", cur.Scanned)
	}
	if cur.Last != "seg-0003.seg" {
		t.Fatalf("cursor last = %q, want the newest segment name", cur.Last)
	}
	rep := rebuildDir(t, r, cur)
	if len(rep.Segments) != 3 {
		t.Fatalf("segments = %d, want 3", len(rep.Segments))
	}
	want := []string{"seg-0001.seg", "seg-0002.seg", "seg-0003.seg"}
	for i, name := range want {
		if rep.Segments[i].Name != name {
			t.Fatalf("segment %d name = %q, want %q", i, rep.Segments[i].Name, name)
		}
	}
	capped := impl.NewRecordingRecovery(impl.Config{MaxSegments: 2, MaxFillMs: 60000})
	if _, err := capped.Rebuild(cur); !errors.Is(err, impl.ErrTooManySegments) {
		t.Fatalf("more segments than the cap must fail with ErrTooManySegments, got %v", err)
	}
}

func TestRebuildComputesSegmentDurations(t *testing.T) {
	dir := t.TempDir()
	writeSegment(t, dir, "seg-0001.seg",
		frameLine(0, 1000, "va"),
		frameLine(1000, 1000, "va"))
	writeSegment(t, dir, "seg-0002.seg",
		frameLine(2000, 500, "va"),
		frameLine(2500, 500, "va"))
	r := impl.NewRecordingRecovery(recoveryConfig())
	cur := scanDir(t, r, dir)
	rep := rebuildDir(t, r, cur)
	if len(rep.Segments) != 2 {
		t.Fatalf("segments = %d, want 2", len(rep.Segments))
	}
	if rep.Segments[0].DurationMs != 2000 {
		t.Fatalf("first segment duration = %d, want 2000", rep.Segments[0].DurationMs)
	}
	if rep.Segments[1].DurationMs != 1000 {
		t.Fatalf("second segment duration = %d, want 1000", rep.Segments[1].DurationMs)
	}
	if rep.TotalMs != 3000 {
		t.Fatalf("total = %d, want 3000", rep.TotalMs)
	}
	if rep.FilledMs != 0 || rep.Repaired != 0 {
		t.Fatalf("report = %+v, want no gap repair", rep)
	}
	if got := rep.Segments[0].Frames; got != 2 {
		t.Fatalf("first segment frames = %d, want 2", got)
	}
}

func TestRebuildFillsTimestampGap(t *testing.T) {
	dir := t.TempDir()
	writeSegment(t, dir, "seg-0001.seg",
		frameLine(0, 1000, "va"),
		frameLine(3000, 1000, "va"))
	writeSegment(t, dir, "seg-0002.seg",
		frameLine(10000, 500, "va"),
		frameLine(11500, 500, "va"))
	r := impl.NewRecordingRecovery(recoveryConfig())
	cur := scanDir(t, r, dir)
	rep := rebuildDir(t, r, cur)
	if rep.FilledMs != 6000 {
		t.Fatalf("filled = %d, want the 6000ms gap to be repaired", rep.FilledMs)
	}
	if rep.Repaired != 1 {
		t.Fatalf("repaired = %d, want 1", rep.Repaired)
	}
	if rep.TotalMs != 12000 {
		t.Fatalf("total = %d, want 4000+6000+2000", rep.TotalMs)
	}
	if rep.Segments[1].DurationMs != 2000 {
		t.Fatalf("second segment duration = %d, want its own 2000ms", rep.Segments[1].DurationMs)
	}
	tight := impl.NewRecordingRecovery(impl.Config{MaxSegments: 16, MaxFillMs: 100})
	if _, err := tight.Rebuild(cur); !errors.Is(err, impl.ErrGapTooLarge) {
		t.Fatalf("a gap above the cap must fail with ErrGapTooLarge, got %v", err)
	}
}

func TestRebuildDropsTruncatedTail(t *testing.T) {
	dir := t.TempDir()
	writeRawSegment(t, dir, "seg-0001.seg",
		frameLine(0, 1000, "va")+"\n"+
			frameLine(1000, 1000, "va")+"\n"+
			frameLine(2000, 1000, "va")+"\n"+
			"3000 10")
	r := impl.NewRecordingRecovery(recoveryConfig())
	cur := scanDir(t, r, dir)
	rep := rebuildDir(t, r, cur)
	if len(rep.Segments) != 1 {
		t.Fatalf("segments = %d, want 1", len(rep.Segments))
	}
	seg := rep.Segments[0]
	if !seg.Truncated {
		t.Fatalf("segment must be marked truncated")
	}
	if seg.Frames != 3 {
		t.Fatalf("frames = %d, want the three complete frames", seg.Frames)
	}
	if rep.DiscardedFrames != 1 {
		t.Fatalf("discarded frames = %d, want the unterminated tail line", rep.DiscardedFrames)
	}
	if rep.TotalMs != 3000 {
		t.Fatalf("total = %d, want 3000 from the complete frames only", rep.TotalMs)
	}
}

func TestRebuildStopsAtCorruptSegment(t *testing.T) {
	dir := t.TempDir()
	writeSegment(t, dir, "seg-0001.seg",
		frameLine(0, 1000, "va"),
		frameLine(1000, 1000, "va"))
	writeSegment(t, dir, "seg-0002.seg",
		frameLine(2000, 1000, "va"),
		"this line is not a frame",
		frameLine(4000, 1000, "va"))
	writeSegment(t, dir, "seg-0003.seg",
		frameLine(5000, 1000, "va"))
	r := impl.NewRecordingRecovery(recoveryConfig())
	cur := scanDir(t, r, dir)
	rep := rebuildDir(t, r, cur)
	if rep.StopReason != impl.StopCorrupt {
		t.Fatalf("stop reason = %q, want %q", rep.StopReason, impl.StopCorrupt)
	}
	if len(rep.Segments) != 2 {
		t.Fatalf("segments = %d, want the recovered part only", len(rep.Segments))
	}
	if rep.Segments[1].Name != "seg-0002.seg" {
		t.Fatalf("second segment = %q, want the partially recovered seg-0002.seg", rep.Segments[1].Name)
	}
	if rep.Segments[1].Frames != 1 {
		t.Fatalf("recovered frames in the corrupt segment = %d, want 1", rep.Segments[1].Frames)
	}
	for _, seg := range rep.Segments {
		if seg.Name == "seg-0003.seg" {
			t.Fatalf("segments after the corrupt one must not be indexed")
		}
	}
	if err := r.Validate(rep); err != nil {
		t.Fatalf("a partial report must still validate, got %v", err)
	}
}

func TestRebuildCountsTrackUsage(t *testing.T) {
	dir := t.TempDir()
	writeSegment(t, dir, "seg-0001.seg",
		frameLine(0, 1000, "v"),
		frameLine(1000, 1000, "a"),
		frameLine(2000, 1000, "va"))
	r := impl.NewRecordingRecovery(recoveryConfig())
	cur := scanDir(t, r, dir)
	rep := rebuildDir(t, r, cur)
	if len(rep.Segments) != 1 {
		t.Fatalf("segments = %d, want 1", len(rep.Segments))
	}
	seg := rep.Segments[0]
	if seg.Frames != 3 {
		t.Fatalf("frames = %d, want 3", seg.Frames)
	}
	if seg.VideoFrames != 2 {
		t.Fatalf("video frames = %d, want 2", seg.VideoFrames)
	}
	if seg.AudioFrames != 2 {
		t.Fatalf("audio frames = %d, want 2", seg.AudioFrames)
	}
	if seg.StartMs != 0 {
		t.Fatalf("segment start = %d, want 0", seg.StartMs)
	}
}

func TestRebuildIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	writeSegment(t, dir, "seg-0001.seg",
		frameLine(0, 1000, "va"),
		frameLine(1000, 1000, "va"))
	writeSegment(t, dir, "seg-0002.seg",
		frameLine(5000, 1000, "va"))
	r := impl.NewRecordingRecovery(recoveryConfig())
	cur := scanDir(t, r, dir)
	first := rebuildDir(t, r, cur)
	second := rebuildDir(t, r, cur)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated rebuilds differ:\nfirst  %+v\nsecond %+v", first, second)
	}
	if first.TotalMs != 6000 || first.FilledMs != 3000 {
		t.Fatalf("repair totals = total %d filled %d, want 6000 and 3000", first.TotalMs, first.FilledMs)
	}
}

func TestValidateAcceptsRebuiltReport(t *testing.T) {
	dir := t.TempDir()
	writeSegment(t, dir, "seg-0001.seg",
		frameLine(0, 1000, "va"),
		frameLine(1000, 500, "va"))
	writeSegment(t, dir, "seg-0002.seg",
		frameLine(2500, 500, "va"))
	r := impl.NewRecordingRecovery(recoveryConfig())
	cur := scanDir(t, r, dir)
	rep := rebuildDir(t, r, cur)
	if err := r.Validate(rep); err != nil {
		t.Fatalf("a rebuilt report must validate, got %v", err)
	}
	if rep.TotalMs != 2500+500-0 {
		t.Fatalf("total = %d, want 3000", rep.TotalMs)
	}
}

func TestValidateRejectsInconsistentReport(t *testing.T) {
	r := impl.NewRecordingRecovery(recoveryConfig())
	bad := impl.Report{
		Segments: []impl.SegmentInfo{{Name: "seg-0001.seg", Frames: 1, DurationMs: 100}},
		TotalMs:  999,
		FilledMs: 0,
	}
	if err := r.Validate(bad); !errors.Is(err, impl.ErrBadReport) {
		t.Fatalf("a total that does not match the segments must fail with ErrBadReport, got %v", err)
	}
	negative := impl.Report{FilledMs: -5}
	if err := r.Validate(negative); !errors.Is(err, impl.ErrBadReport) {
		t.Fatalf("a negative filled duration must fail with ErrBadReport, got %v", err)
	}
}

func TestResumeRejectsBadCursor(t *testing.T) {
	r := impl.NewRecordingRecovery(recoveryConfig())
	if _, err := r.Resume(impl.ScanCursor{}); !errors.Is(err, impl.ErrBadCursor) {
		t.Fatalf("an empty cursor must fail with ErrBadCursor, got %v", err)
	}
	if _, err := r.Rebuild(impl.ScanCursor{}); !errors.Is(err, impl.ErrBadCursor) {
		t.Fatalf("rebuilding from an empty cursor must fail with ErrBadCursor, got %v", err)
	}
}

func TestSnapshotRestoreKeepsReport(t *testing.T) {
	dir := t.TempDir()
	writeSegment(t, dir, "seg-0001.seg",
		frameLine(0, 1000, "va"),
		frameLine(1000, 1000, "va"))
	writeSegment(t, dir, "seg-0002.seg",
		frameLine(4000, 1000, "va"))
	r := impl.NewRecordingRecovery(recoveryConfig())
	cur := scanDir(t, r, dir)
	want := rebuildDir(t, r, cur)
	data, err := r.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewRecordingRecovery(impl.Config{MaxSegments: 1, MaxFillMs: 1})
	if err := restored.Restore(data); err != nil {
		t.Fatalf("Restore failed: %v", err)
	}
	got := restored.LastReport()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("restored report = %+v, want %+v", got, want)
	}
	if got := restored.SegmentCount(); got != 2 {
		t.Fatalf("restored segment count = %d, want 2", got)
	}
	names := restored.Names()
	if len(names) != 2 || names[0] != "seg-0001.seg" || names[1] != "seg-0002.seg" {
		t.Fatalf("restored names = %v, want the two segment files", names)
	}
	if err := restored.Validate(restored.LastReport()); err != nil {
		t.Fatalf("the restored report must validate, got %v", err)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	dir := t.TempDir()
	writeSegment(t, dir, "seg-0001.seg", frameLine(0, 1000, "va"))
	r := impl.NewRecordingRecovery(recoveryConfig())
	cur := scanDir(t, r, dir)
	want := rebuildDir(t, r, cur)
	if err := r.Restore([]byte("@@@not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := r.LastReport(); !reflect.DeepEqual(got, want) {
		t.Fatalf("a rejected restore must not touch the live report, got %+v", got)
	}
	if got := r.SegmentCount(); got != 1 {
		t.Fatalf("segment count = %d, want the live report kept", got)
	}
}

func TestConcurrentRebuild(t *testing.T) {
	dir := t.TempDir()
	writeSegment(t, dir, "seg-0001.seg",
		frameLine(0, 1000, "va"),
		frameLine(1000, 1000, "va"))
	writeSegment(t, dir, "seg-0002.seg",
		frameLine(3000, 1000, "va"),
		frameLine(4000, 1000, "va"))
	r := impl.NewRecordingRecovery(recoveryConfig())
	cur := scanDir(t, r, dir)
	reference := rebuildDir(t, r, cur)
	var failures int64
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				rep, err := r.Rebuild(cur)
				if err != nil {
					atomic.AddInt64(&failures, 1)
					return
				}
				if !reflect.DeepEqual(rep, reference) {
					atomic.AddInt64(&failures, 1)
					return
				}
				if len(r.Names()) != 2 {
					atomic.AddInt64(&failures, 1)
					return
				}
			}
		}()
	}
	wg.Wait()
	if failures != 0 {
		t.Fatalf("%d workers observed a failing or differing rebuild", failures)
	}
	if got := r.SegmentCount(); got != 2 {
		t.Fatalf("segment count = %d, want 2", got)
	}
	if got := r.LastReport().TotalMs; got != 5000 {
		t.Fatalf("last total = %d, want 2000+2000 plus the 1000ms fill", got)
	}
}
