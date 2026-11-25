package task

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	impl "gsb/batch49-go-game-c-08"
)

func beBox(typ string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[0:4], uint32(8+len(payload)))
	copy(out[4:8], typ)
	copy(out[8:], payload)
	return out
}

func testPair(ticks uint32, sap bool, payload int) []byte {
	head := make([]byte, 8)
	binary.BigEndian.PutUint32(head[0:4], ticks)
	if sap {
		head[4] = 1
	}
	out := beBox("moof", head)
	return append(out, beBox("mdat", make([]byte, payload))...)
}

func sidxBox(timescale uint32, entries [][3]uint32) []byte {
	payload := make([]byte, 8+12*len(entries))
	binary.BigEndian.PutUint32(payload[0:4], timescale)
	binary.BigEndian.PutUint32(payload[4:8], uint32(len(entries)))
	for i, e := range entries {
		base := 8 + i*12
		binary.BigEndian.PutUint32(payload[base:base+4], e[0])
		binary.BigEndian.PutUint32(payload[base+4:base+8], e[1])
		binary.BigEndian.PutUint32(payload[base+8:base+12], e[2])
	}
	return beBox("sidx", payload)
}

func indexConfig() impl.IndexConfig {
	return impl.IndexConfig{Timescale: 1000, MaxEntries: 64}
}

func mustIndex(t *testing.T, b *impl.ByteRangeIndexer, seg string, data []byte) {
	t.Helper()
	if err := b.IndexSegment(seg, data); err != nil {
		t.Fatalf("index %s: %v", seg, err)
	}
}

func TestIndexSegmentBuildsContiguousEntries(t *testing.T) {
	b := impl.NewByteRangeIndexer(indexConfig())
	data := append(testPair(40, true, 20), testPair(40, false, 20)...)
	mustIndex(t, b, "seg-a", data)
	if got := b.Entries(); got != 2 {
		t.Fatalf("entries = %d, want 2", got)
	}
	first, err := b.RangeAt("seg-a", 0)
	if err != nil {
		t.Fatalf("range 0: %v", err)
	}
	if first.Offset != 0 || first.Size != 44 {
		t.Fatalf("range 0 offset=%d size=%d, want 0/44", first.Offset, first.Size)
	}
	if first.Start != 0 || first.End != 40*time.Millisecond || !first.SAP {
		t.Fatalf("range 0 time=%v..%v sap=%v, want 0..40ms sap", first.Start, first.End, first.SAP)
	}
	second, err := b.RangeAt("seg-a", 1)
	if err != nil {
		t.Fatalf("range 1: %v", err)
	}
	if second.Offset != 44 || second.Size != 44 {
		t.Fatalf("range 1 offset=%d size=%d, want 44/44", second.Offset, second.Size)
	}
	if second.Start != 40*time.Millisecond || second.End != 80*time.Millisecond || second.SAP {
		t.Fatalf("range 1 time=%v..%v sap=%v, want 40..80ms non sap", second.Start, second.End, second.SAP)
	}
}

func TestIndexSegmentRejectsMalformedPayload(t *testing.T) {
	b := impl.NewByteRangeIndexer(indexConfig())
	broken := make([]byte, 8)
	binary.BigEndian.PutUint32(broken[0:4], 4096)
	copy(broken[4:8], "moof")
	if err := b.IndexSegment("seg-bad", broken); !errors.Is(err, impl.ErrMalformedSegment) {
		t.Fatalf("truncated box = %v, want ErrMalformedSegment", err)
	}
	if got := b.Entries(); got != 0 {
		t.Fatalf("malformed segment must not add entries, got %d", got)
	}
}

func TestIndexSegmentRejectsDuplicateSegment(t *testing.T) {
	b := impl.NewByteRangeIndexer(indexConfig())
	data := testPair(40, true, 20)
	mustIndex(t, b, "seg-a", data)
	if err := b.IndexSegment("seg-a", data); !errors.Is(err, impl.ErrDuplicateSegment) {
		t.Fatalf("duplicate segment = %v, want ErrDuplicateSegment", err)
	}
	if got := b.Entries(); got != 1 {
		t.Fatalf("entries after duplicate = %d, want 1", got)
	}
}

func TestIndexSegmentFromIndexBox(t *testing.T) {
	b := impl.NewByteRangeIndexer(indexConfig())
	head := sidxBox(1000, [][3]uint32{{40, 100, 1}, {20, 50, 0}})
	data := append(head, make([]byte, 150)...)
	mustIndex(t, b, "seg-idx", data)
	if got := b.Entries(); got != 2 {
		t.Fatalf("entries = %d, want 2", got)
	}
	first, err := b.RangeAt("seg-idx", 0)
	if err != nil {
		t.Fatalf("range 0: %v", err)
	}
	if first.Offset != 40 || first.Size != 100 || !first.SAP {
		t.Fatalf("range 0 offset=%d size=%d sap=%v, want 40/100 sap", first.Offset, first.Size, first.SAP)
	}
	if first.Start != 0 || first.End != 40*time.Millisecond {
		t.Fatalf("range 0 time = %v..%v, want 0..40ms", first.Start, first.End)
	}
	second, err := b.RangeAt("seg-idx", 1)
	if err != nil {
		t.Fatalf("range 1: %v", err)
	}
	if second.Offset != 140 || second.Size != 50 || second.SAP {
		t.Fatalf("range 1 offset=%d size=%d sap=%v, want 140/50 non sap", second.Offset, second.Size, second.SAP)
	}
	if total := b.TotalBytes(); total != 150 {
		t.Fatalf("total bytes = %d, want 150", total)
	}
}

func TestLocateFindsRangeByTime(t *testing.T) {
	b := impl.NewByteRangeIndexer(indexConfig())
	mustIndex(t, b, "seg-a", append(testPair(40, true, 20), testPair(40, true, 20)...))
	mustIndex(t, b, "seg-b", testPair(40, true, 20))
	got, err := b.Locate(50*time.Millisecond, false)
	if err != nil {
		t.Fatalf("locate 50ms: %v", err)
	}
	if got.Segment != "seg-a" || got.Index != 1 {
		t.Fatalf("locate 50ms = %s/%d, want seg-a/1", got.Segment, got.Index)
	}
	got, err = b.Locate(90*time.Millisecond, false)
	if err != nil {
		t.Fatalf("locate 90ms: %v", err)
	}
	if got.Segment != "seg-b" || got.Index != 0 {
		t.Fatalf("locate 90ms = %s/%d, want seg-b/0", got.Segment, got.Index)
	}
	if got.Start != 80*time.Millisecond || got.End != 120*time.Millisecond {
		t.Fatalf("seg-b range = %v..%v, want 80..120ms", got.Start, got.End)
	}
}

func TestLocateRequiresSAP(t *testing.T) {
	b := impl.NewByteRangeIndexer(indexConfig())
	mustIndex(t, b, "seg-a", append(testPair(10, false, 10), testPair(30, true, 10)...))
	got, err := b.Locate(5*time.Millisecond, true)
	if err != nil {
		t.Fatalf("locate with sap: %v", err)
	}
	if got.Index != 1 || !got.SAP {
		t.Fatalf("locate with sap = index %d sap %v, want the next SAP range index 1", got.Index, got.SAP)
	}
	if got.Start != 10*time.Millisecond {
		t.Fatalf("sap range start = %v, want 10ms", got.Start)
	}
}

func TestLocateBeyondEndFails(t *testing.T) {
	b := impl.NewByteRangeIndexer(indexConfig())
	mustIndex(t, b, "seg-a", testPair(40, true, 20))
	if _, err := b.Locate(200*time.Millisecond, false); !errors.Is(err, impl.ErrOutOfRange) {
		t.Fatalf("locate past the end = %v, want ErrOutOfRange", err)
	}
}

func TestLocateNegativeTimeFails(t *testing.T) {
	b := impl.NewByteRangeIndexer(indexConfig())
	mustIndex(t, b, "seg-a", testPair(40, true, 20))
	if _, err := b.Locate(-time.Millisecond, false); !errors.Is(err, impl.ErrOutOfRange) {
		t.Fatalf("negative locate = %v, want ErrOutOfRange", err)
	}
}

func TestInvalidateRemovesSegment(t *testing.T) {
	b := impl.NewByteRangeIndexer(indexConfig())
	mustIndex(t, b, "seg-a", append(testPair(40, true, 20), testPair(40, true, 20)...))
	mustIndex(t, b, "seg-b", testPair(40, true, 20))
	if err := b.Invalidate("seg-b"); err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	if got := b.Entries(); got != 2 {
		t.Fatalf("entries after invalidation = %d, want 2", got)
	}
	segs := b.Segments()
	if len(segs) != 1 || segs[0] != "seg-a" {
		t.Fatalf("segments after invalidation = %v, want only seg-a", segs)
	}
	got, err := b.Locate(50*time.Millisecond, false)
	if err != nil {
		t.Fatalf("locate after invalidation: %v", err)
	}
	if got.Segment != "seg-a" || got.Index != 1 {
		t.Fatalf("locate after invalidation = %s/%d, want seg-a/1", got.Segment, got.Index)
	}
	if st := b.Stats(); st.Invalidated != 1 {
		t.Fatalf("invalidated counter = %d, want 1", st.Invalidated)
	}
}

func TestInvalidateUnknownSegmentFails(t *testing.T) {
	b := impl.NewByteRangeIndexer(indexConfig())
	if err := b.Invalidate("seg-missing"); !errors.Is(err, impl.ErrUnknownSegment) {
		t.Fatalf("invalidate unknown = %v, want ErrUnknownSegment", err)
	}
	if err := b.Invalidate("   "); !errors.Is(err, impl.ErrUnknownSegment) {
		t.Fatalf("invalidate blank = %v, want ErrUnknownSegment", err)
	}
}

func TestEntriesCapEnforced(t *testing.T) {
	cfg := indexConfig()
	cfg.MaxEntries = 2
	b := impl.NewByteRangeIndexer(cfg)
	mustIndex(t, b, "seg-a", append(testPair(40, true, 20), testPair(40, true, 20)...))
	err := b.IndexSegment("seg-b", testPair(40, true, 20))
	if !errors.Is(err, impl.ErrIndexFull) {
		t.Fatalf("entry over the cap = %v, want ErrIndexFull", err)
	}
	if got := b.Entries(); got != 2 {
		t.Fatalf("entries after rejected segment = %d, want 2", got)
	}
	if got := len(b.Segments()); got != 1 {
		t.Fatalf("segments after rejected segment = %d, want 1", got)
	}
	if got := b.TotalBytes(); got != 88 {
		t.Fatalf("total bytes after rejected segment = %d, want 88", got)
	}
}

func TestAccumulatedOffsetsAcrossSegments(t *testing.T) {
	b := impl.NewByteRangeIndexer(indexConfig())
	mustIndex(t, b, "seg-a", append(testPair(40, true, 20), testPair(40, true, 20)...))
	mustIndex(t, b, "seg-b", testPair(40, true, 20))
	got, err := b.RangeAt("seg-b", 0)
	if err != nil {
		t.Fatalf("range at seg-b: %v", err)
	}
	if got.Offset != 88 {
		t.Fatalf("seg-b offset = %d, want the accumulated 88", got.Offset)
	}
	if total := b.TotalBytes(); total != 132 {
		t.Fatalf("total bytes = %d, want 132", total)
	}
	if _, err := b.RangeAt("seg-a", 5); !errors.Is(err, impl.ErrOutOfRange) {
		t.Fatalf("range index out of bounds = %v, want ErrOutOfRange", err)
	}
}

func TestConcurrentIndexAndLocate(t *testing.T) {
	cfg := indexConfig()
	cfg.MaxEntries = 64
	b := impl.NewByteRangeIndexer(cfg)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			seg := fmt.Sprintf("seg-%02d", i)
			if err := b.IndexSegment(seg, testPair(40, true, 20)); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if firstErr != nil {
		t.Fatalf("concurrent index failed: %v", firstErr)
	}
	if got := b.Entries(); got != 6 {
		t.Fatalf("entries = %d, want 6", got)
	}
	if total := b.TotalBytes(); total != 264 {
		t.Fatalf("total bytes = %d, want 264", total)
	}
	if _, err := b.Locate(10*time.Millisecond, false); err != nil {
		t.Fatalf("locate after concurrent indexing: %v", err)
	}
}

func TestSnapshotRestoreKeepsEntriesAndOffsets(t *testing.T) {
	cfg := indexConfig()
	b := impl.NewByteRangeIndexer(cfg)
	mustIndex(t, b, "seg-a", append(testPair(40, true, 20), testPair(40, true, 20)...))
	mustIndex(t, b, "seg-b", testPair(40, true, 20))
	data, err := b.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewByteRangeIndexer(cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.Entries(); got != 3 {
		t.Fatalf("restored entries = %d, want 3", got)
	}
	if got := restored.TotalBytes(); got != 132 {
		t.Fatalf("restored total bytes = %d, want 132", got)
	}
	got, err := restored.Locate(90*time.Millisecond, false)
	if err != nil {
		t.Fatalf("locate after restore: %v", err)
	}
	if got.Segment != "seg-b" || got.Offset != 88 {
		t.Fatalf("locate after restore = %s offset %d, want seg-b offset 88", got.Segment, got.Offset)
	}
	if segs := restored.Segments(); len(segs) != 2 {
		t.Fatalf("restored segments = %v, want 2", segs)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	b := impl.NewByteRangeIndexer(indexConfig())
	mustIndex(t, b, "seg-a", testPair(40, true, 20))
	if err := b.Restore([]byte("broken-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot = %v, want ErrBadSnapshot", err)
	}
	if got := b.Entries(); got != 1 {
		t.Fatalf("failed restore must keep live state, got %d entries", got)
	}
	if _, err := b.Locate(10*time.Millisecond, false); err != nil {
		t.Fatalf("live index must keep working after rejected restore, got %v", err)
	}
}
