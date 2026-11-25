package task

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	impl "gsb/batch49-go-safety-c-06"
)

// distinctCover derives a coverage set from the distinct byte values of an
// input. It is the reducer injected into the vault in every test.
func distinctCover(input []byte) impl.CoverSet {
	seen := make(map[uint64]struct{}, len(input))
	edges := make([]uint64, 0, len(input))
	for _, b := range input {
		e := uint64(b)
		if _, ok := seen[e]; ok {
			continue
		}
		seen[e] = struct{}{}
		edges = append(edges, e)
	}
	return impl.CoverSet{Hash: fmt.Sprintf("cov-%02d", len(edges)), Edges: edges}
}

func newVault(t *testing.T, maxBytes, maxEntries int) *impl.CorpusVault {
	t.Helper()
	v := impl.NewCorpusVault(impl.Limits{MaxBytes: maxBytes, MaxEntries: maxEntries}, distinctCover)
	if v == nil {
		t.Fatalf("NewCorpusVault returned nil")
	}
	return v
}

func TestAddStoresEntryWithNewCoverage(t *testing.T) {
	v := newVault(t, 1024, 8)
	ver, err := v.Add([]byte("alpha"), impl.CoverSet{Hash: "cov-a", Edges: []uint64{1, 2, 3}}, "ff-a")
	if err != nil {
		t.Fatalf("add new coverage: %v", err)
	}
	if !ver.Accepted {
		t.Fatalf("entry with three new edges must be accepted, verdict = %+v", ver)
	}
	if ver.Reason != impl.ReasonAccepted {
		t.Fatalf("verdict reason = %q, want %q", ver.Reason, impl.ReasonAccepted)
	}
	if ver.ID != "corpus-0001" {
		t.Fatalf("verdict id = %q, want the first sequential id corpus-0001", ver.ID)
	}
	if ver.NewEdges != 3 {
		t.Fatalf("verdict new edges = %d, want 3", ver.NewEdges)
	}
	if ver.Bytes != 5 {
		t.Fatalf("verdict bytes = %d, want 5", ver.Bytes)
	}
	entry, ok := v.Get(ver.ID)
	if !ok {
		t.Fatalf("accepted entry must be readable")
	}
	if entry.Bytes != 5 || entry.NewEdges != 3 || entry.CoverHash != "cov-a" {
		t.Fatalf("stored entry = %+v, want 5 bytes, 3 new edges and cover hash cov-a", entry)
	}
	if entry.Seq != 1 {
		t.Fatalf("stored entry sequence = %d, want 1", entry.Seq)
	}
	if len(entry.Origins) != 1 || entry.Origins[0] != "ff-a" {
		t.Fatalf("stored origins = %v, want exactly [ff-a]", entry.Origins)
	}
	if entry.Pinned || entry.Crash || entry.Confirmed {
		t.Fatalf("fresh entry must be unpinned and not a confirmed crash, got %+v", entry)
	}
	stats := v.Stats()
	if stats.Entries != 1 || stats.Bytes != 5 || stats.Evicted != 0 {
		t.Fatalf("stats = %+v, want 1 entry, 5 bytes and no eviction", stats)
	}
}

func TestAddRequiresOrigin(t *testing.T) {
	v := newVault(t, 1024, 8)
	if _, err := v.Add([]byte("alpha"), impl.CoverSet{Hash: "cov-a", Edges: []uint64{1}}, "   "); !errors.Is(err, impl.ErrBlankOrigin) {
		t.Fatalf("blank origin must fail with ErrBlankOrigin, got %v", err)
	}
	if _, err := v.Add([]byte("alpha"), impl.CoverSet{Hash: "cov-a", Edges: []uint64{1}}, ""); !errors.Is(err, impl.ErrBlankOrigin) {
		t.Fatalf("empty origin must fail with ErrBlankOrigin, got %v", err)
	}
	if got := len(v.Entries()); got != 0 {
		t.Fatalf("rejected adds must not store entries, got %d", got)
	}
}

func TestAddDeduplicatesByContentAndCountsOrigins(t *testing.T) {
	v := newVault(t, 1024, 8)
	first, err := v.Add([]byte("alpha"), impl.CoverSet{Hash: "cov-a", Edges: []uint64{1, 2}}, "ff-a")
	if err != nil {
		t.Fatalf("first add: %v", err)
	}
	second, err := v.Add([]byte("alpha"), impl.CoverSet{Hash: "cov-a", Edges: []uint64{3}}, "ff-b")
	if err != nil {
		t.Fatalf("duplicate add must not fail: %v", err)
	}
	if second.Accepted {
		t.Fatalf("identical content must not be stored twice, got %+v", second)
	}
	if !second.Duplicate {
		t.Fatalf("identical content must be reported as a duplicate, got %+v", second)
	}
	if second.Reason != impl.ReasonDuplicate || second.NewEdges != 0 {
		t.Fatalf("duplicate verdict = %+v, want reason %q and zero new edges", second, impl.ReasonDuplicate)
	}
	if second.ID != first.ID {
		t.Fatalf("duplicate must point at the existing entry %q, got %q", first.ID, second.ID)
	}
	third, err := v.Add([]byte("alpha"), impl.CoverSet{Hash: "cov-a", Edges: []uint64{4}}, "ff-c")
	if err != nil {
		t.Fatalf("third add: %v", err)
	}
	if third.ID != first.ID {
		t.Fatalf("third duplicate id = %q, want %q", third.ID, first.ID)
	}
	entry, ok := v.Get(first.ID)
	if !ok {
		t.Fatalf("deduplicated entry must stay readable")
	}
	if len(entry.Origins) != 3 {
		t.Fatalf("origins = %v, want three accumulated sources", entry.Origins)
	}
	for i, want := range []string{"ff-a", "ff-b", "ff-c"} {
		if entry.Origins[i] != want {
			t.Fatalf("origins = %v, want %v in first seen order", entry.Origins, []string{"ff-a", "ff-b", "ff-c"})
		}
	}
	if got := len(v.Entries()); got != 1 {
		t.Fatalf("entry count = %d, want 1", got)
	}
	if stats := v.Stats(); stats.Duplicates != 2 {
		t.Fatalf("duplicate counter = %d, want 2", stats.Duplicates)
	}
}

func TestAddRejectsRedundantCoverage(t *testing.T) {
	v := newVault(t, 1024, 8)
	if _, err := v.Add([]byte("aa"), impl.CoverSet{Hash: "cov-aa", Edges: []uint64{7}}, "ff-a"); err != nil {
		t.Fatalf("seed add: %v", err)
	}
	ver, err := v.Add([]byte("bb"), impl.CoverSet{Hash: "cov-bb", Edges: []uint64{7}}, "ff-b")
	if err != nil {
		t.Fatalf("redundant add must not fail: %v", err)
	}
	if ver.Accepted {
		t.Fatalf("content without unseen edges must not be stored, got %+v", ver)
	}
	if ver.Reason != impl.ReasonNoNewCoverage || ver.NewEdges != 0 {
		t.Fatalf("redundant verdict = %+v, want reason %q", ver, impl.ReasonNoNewCoverage)
	}
	if got := len(v.Entries()); got != 1 {
		t.Fatalf("entry count = %d, want 1", got)
	}
	if stats := v.Stats(); stats.Redundant != 1 {
		t.Fatalf("redundant counter = %d, want 1", stats.Redundant)
	}
	grown, err := v.Add([]byte("cc"), impl.CoverSet{Hash: "cov-cc", Edges: []uint64{7, 8}}, "ff-c")
	if err != nil {
		t.Fatalf("partially new add: %v", err)
	}
	if !grown.Accepted || grown.NewEdges != 1 {
		t.Fatalf("only the unseen edge must count, got %+v", grown)
	}
}

func TestAddCountsOnlyUnseenEdges(t *testing.T) {
	v := newVault(t, 1024, 8)
	if _, err := v.Add([]byte("aaa"), impl.CoverSet{Hash: "cov-1", Edges: []uint64{1, 2}}, "ff-a"); err != nil {
		t.Fatalf("first add: %v", err)
	}
	ver, err := v.Add([]byte("bbbbbb"), impl.CoverSet{Hash: "cov-2", Edges: []uint64{1, 2, 3, 4, 5}}, "ff-b")
	if err != nil {
		t.Fatalf("second add: %v", err)
	}
	if !ver.Accepted {
		t.Fatalf("entry with unseen edges must be accepted, got %+v", ver)
	}
	if ver.NewEdges != 3 {
		t.Fatalf("new edge count = %d, want 3 (edges 1 and 2 were already seen)", ver.NewEdges)
	}
	entries := v.Entries()
	if len(entries) != 2 {
		t.Fatalf("entry count = %d, want 2", len(entries))
	}
	if entries[0].ID != "corpus-0001" || entries[1].ID != "corpus-0002" {
		t.Fatalf("entries = %q, %q, want admission order corpus-0001 then corpus-0002", entries[0].ID, entries[1].ID)
	}
	if entries[0].Seq >= entries[1].Seq {
		t.Fatalf("entry sequences must strictly increase, got %d then %d", entries[0].Seq, entries[1].Seq)
	}
}

func TestAddRejectsMissingReducer(t *testing.T) {
	v := impl.NewCorpusVault(impl.Limits{MaxBytes: 1024, MaxEntries: 8}, nil)
	if _, err := v.Add([]byte("alpha"), impl.CoverSet{Hash: "cov-a", Edges: []uint64{1}}, "ff-a"); !errors.Is(err, impl.ErrNilReducer) {
		t.Fatalf("add without a reducer must fail with ErrNilReducer, got %v", err)
	}
	if got := len(v.Entries()); got != 0 {
		t.Fatalf("rejected add must not store entries, got %d", got)
	}
}

func TestAddEvictsLowestContributionUnderQuota(t *testing.T) {
	v := newVault(t, 10, 8)
	first, err := v.Add([]byte("aaaaaa"), impl.CoverSet{Hash: "cov-1", Edges: []uint64{1}}, "ff-a")
	if err != nil {
		t.Fatalf("first add: %v", err)
	}
	if _, err := v.Add([]byte("bb"), impl.CoverSet{Hash: "cov-2", Edges: []uint64{2, 3}}, "ff-b"); err != nil {
		t.Fatalf("second add: %v", err)
	}
	third, err := v.Add([]byte("cccc"), impl.CoverSet{Hash: "cov-3", Edges: []uint64{4, 5, 6}}, "ff-c")
	if err != nil {
		t.Fatalf("third add: %v", err)
	}
	if !third.Accepted {
		t.Fatalf("entry with the highest contribution must displace the weakest one, got %+v", third)
	}
	if len(third.Evicted) != 1 || third.Evicted[0] != first.ID {
		t.Fatalf("evicted = %v, want exactly the weakest entry %q", third.Evicted, first.ID)
	}
	if _, ok := v.Get(first.ID); ok {
		t.Fatalf("evicted entry %q must be gone", first.ID)
	}
	if got := len(v.Entries()); got != 2 {
		t.Fatalf("entry count after eviction = %d, want 2", got)
	}
	if stats := v.Stats(); stats.Evicted != 1 || stats.Bytes != 6 {
		t.Fatalf("stats = %+v, want 1 eviction and 6 retained bytes", stats)
	}
}

func TestPromoteProtectsEntryFromEviction(t *testing.T) {
	v := newVault(t, 10, 8)
	pinned, err := v.Add([]byte("aaaaaa"), impl.CoverSet{Hash: "cov-1", Edges: []uint64{1}}, "ff-a")
	if err != nil {
		t.Fatalf("first add: %v", err)
	}
	if err := v.Promote(pinned.ID); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if err := v.Promote(pinned.ID); err != nil {
		t.Fatalf("promoting twice must be idempotent, got %v", err)
	}
	if err := v.Promote("corpus-9999"); !errors.Is(err, impl.ErrNotFound) {
		t.Fatalf("promoting an unknown entry must fail with ErrNotFound, got %v", err)
	}
	if _, err := v.Add([]byte("bb"), impl.CoverSet{Hash: "cov-2", Edges: []uint64{2, 3}}, "ff-b"); err != nil {
		t.Fatalf("second add: %v", err)
	}
	third, err := v.Add([]byte("cccc"), impl.CoverSet{Hash: "cov-3", Edges: []uint64{4, 5, 6}}, "ff-c")
	if err != nil {
		t.Fatalf("third add: %v", err)
	}
	if third.Accepted {
		t.Fatalf("the newcomer is weaker than every evictable entry and must be rejected, got %+v", third)
	}
	if third.Reason != impl.ReasonQuotaFull || len(third.Evicted) != 0 {
		t.Fatalf("weak newcomer verdict = %+v, want reason %q and no eviction", third, impl.ReasonQuotaFull)
	}
	entry, ok := v.Get(pinned.ID)
	if !ok {
		t.Fatalf("pinned entry must survive eviction")
	}
	if !entry.Pinned {
		t.Fatalf("pinned flag lost after eviction pass")
	}
	if _, ok := v.Get(third.ID); ok {
		t.Fatalf("rejected newcomer must not be stored")
	}
	if got := len(v.Entries()); got != 2 {
		t.Fatalf("entry count = %d, want the pinned entry plus the middle entry", got)
	}
}

func TestMarkCrashTracksManualConfirmation(t *testing.T) {
	v := newVault(t, 1024, 8)
	ver, err := v.Add([]byte("crash"), impl.CoverSet{Hash: "cov-c", Edges: []uint64{9}}, "ff-crash")
	if err != nil {
		t.Fatalf("add crash case: %v", err)
	}
	if err := v.MarkCrash(ver.ID); err != nil {
		t.Fatalf("mark crash: %v", err)
	}
	entry, ok := v.Get(ver.ID)
	if !ok || !entry.Crash {
		t.Fatalf("entry must be flagged as a crash case, got %+v ok=%v", entry, ok)
	}
	if stats := v.Stats(); stats.Crash != 1 {
		t.Fatalf("crash counter = %d, want 1", stats.Crash)
	}
	if err := v.MarkCrash("corpus-9999"); !errors.Is(err, impl.ErrNotFound) {
		t.Fatalf("marking an unknown entry must fail with ErrNotFound, got %v", err)
	}
	other, err := v.Add([]byte("clean"), impl.CoverSet{Hash: "cov-d", Edges: []uint64{10}}, "ff-d")
	if err != nil {
		t.Fatalf("add clean case: %v", err)
	}
	if err := v.ConfirmCrash(other.ID); !errors.Is(err, impl.ErrNotCrash) {
		t.Fatalf("confirming a non crash entry must fail with ErrNotCrash, got %v", err)
	}
	if err := v.ConfirmCrash(ver.ID); err != nil {
		t.Fatalf("confirm crash: %v", err)
	}
	confirmed, _ := v.Get(ver.ID)
	if !confirmed.Confirmed {
		t.Fatalf("manual confirmation must be recorded")
	}
	if stats := v.Stats(); stats.Crash != 1 {
		t.Fatalf("crash counter changed to %d after confirmation, want 1", stats.Crash)
	}
}

func TestMinimizeTrimsTrailingBytesKeepingCoverage(t *testing.T) {
	v := newVault(t, 1024, 8)
	input := []byte("abcda")
	cov := distinctCover(input)
	ver, err := v.Add(input, cov, "ff-a")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	min, err := v.Minimize(ver.ID)
	if err != nil {
		t.Fatalf("minimize: %v", err)
	}
	if string(min) != "abcd" {
		t.Fatalf("minimized input = %q, want the shortest prefix %q", string(min), "abcd")
	}
	got := distinctCover(min)
	if got.Hash != cov.Hash || len(got.Edges) != len(cov.Edges) {
		t.Fatalf("minimized input covers %+v, want the original cover %+v", got, cov)
	}
	if _, err := v.Minimize("corpus-9999"); !errors.Is(err, impl.ErrNotFound) {
		t.Fatalf("minimizing an unknown entry must fail with ErrNotFound, got %v", err)
	}
}

func TestMinimizeReturnsIndependentCopy(t *testing.T) {
	v := newVault(t, 1024, 8)
	input := []byte("abcda")
	ver, err := v.Add(input, distinctCover(input), "ff-a")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	first, err := v.Minimize(ver.ID)
	if err != nil {
		t.Fatalf("first minimize: %v", err)
	}
	first[0] = 'z'
	second, err := v.Minimize(ver.ID)
	if err != nil {
		t.Fatalf("second minimize: %v", err)
	}
	if string(second) != "abcd" {
		t.Fatalf("mutating a returned slice must not corrupt the entry, got %q", string(second))
	}
}

func TestReapAfterRestoreHonorsSmallerQuota(t *testing.T) {
	source := newVault(t, 100, 8)
	first, err := source.Add([]byte("aaaaaa"), impl.CoverSet{Hash: "cov-1", Edges: []uint64{1}}, "ff-a")
	if err != nil {
		t.Fatalf("first add: %v", err)
	}
	second, err := source.Add([]byte("bbbbbb"), impl.CoverSet{Hash: "cov-2", Edges: []uint64{2, 3, 4, 5}}, "ff-b")
	if err != nil {
		t.Fatalf("second add: %v", err)
	}
	data, err := source.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := newVault(t, 6, 8)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	evicted := restored.Reap()
	if len(evicted) != 1 || evicted[0] != first.ID {
		t.Fatalf("reap evicted %v, want exactly the lowest contribution entry %q", evicted, first.ID)
	}
	if _, ok := restored.Get(first.ID); ok {
		t.Fatalf("reaped entry %q must be gone", first.ID)
	}
	if entry, ok := restored.Get(second.ID); !ok || entry.Bytes != 6 {
		t.Fatalf("strong entry must survive, got %+v ok=%v", entry, ok)
	}
	if again := restored.Reap(); len(again) != 0 {
		t.Fatalf("second reap evicted %v, want nothing left to evict", again)
	}
	if stats := restored.Stats(); stats.Evicted != 1 || stats.Entries != 1 {
		t.Fatalf("stats = %+v, want 1 eviction and 1 retained entry", stats)
	}
}

func TestSnapshotRestoreContinuesSequence(t *testing.T) {
	limits := impl.Limits{MaxBytes: 1024, MaxEntries: 8}
	source := impl.NewCorpusVault(limits, distinctCover)
	first, err := source.Add([]byte("aaa"), impl.CoverSet{Hash: "cov-1", Edges: []uint64{1}}, "ff-a")
	if err != nil {
		t.Fatalf("first add: %v", err)
	}
	if err := source.Promote(first.ID); err != nil {
		t.Fatalf("promote: %v", err)
	}
	data, err := source.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewCorpusVault(limits, distinctCover)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	entry, ok := restored.Get(first.ID)
	if !ok {
		t.Fatalf("restored vault lost the entry")
	}
	if !entry.Pinned || len(entry.Origins) != 1 || entry.Origins[0] != "ff-a" {
		t.Fatalf("restored entry = %+v, want pinned entry with origin ff-a", entry)
	}
	if stats := restored.Stats(); stats.Entries != 1 || stats.Bytes != 3 {
		t.Fatalf("restored stats = %+v, want 1 entry and 3 bytes", stats)
	}
	redundant, err := restored.Add([]byte("zz"), impl.CoverSet{Hash: "cov-2", Edges: []uint64{1}}, "ff-z")
	if err != nil {
		t.Fatalf("add after restore: %v", err)
	}
	if redundant.Accepted || redundant.Reason != impl.ReasonNoNewCoverage {
		t.Fatalf("coverage watermark must survive the snapshot, got %+v", redundant)
	}
	next, err := restored.Add([]byte("yy"), impl.CoverSet{Hash: "cov-3", Edges: []uint64{2}}, "ff-y")
	if err != nil {
		t.Fatalf("second add after restore: %v", err)
	}
	if next.ID != "corpus-0002" {
		t.Fatalf("id after restore = %q, want corpus-0002", next.ID)
	}
	if next.NewEdges != 1 {
		t.Fatalf("new edges after restore = %d, want 1", next.NewEdges)
	}
	entry2, _ := restored.Get(next.ID)
	if entry2.Seq <= entry.Seq {
		t.Fatalf("entry sequence after restore = %d, must continue past %d", entry2.Seq, entry.Seq)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	v := newVault(t, 1024, 8)
	ver, err := v.Add([]byte("alpha"), impl.CoverSet{Hash: "cov-a", Edges: []uint64{1, 2}}, "ff-a")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := v.Restore([]byte("not a snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if err := v.Restore(nil); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("empty snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	entry, ok := v.Get(ver.ID)
	if !ok || entry.Bytes != 5 {
		t.Fatalf("failed restore must not touch live state, got %+v ok=%v", entry, ok)
	}
	if got := len(v.Entries()); got != 1 {
		t.Fatalf("entry count after rejected restore = %d, want 1", got)
	}
}

func TestConcurrentAddsRespectEntryCap(t *testing.T) {
	v := newVault(t, 1<<20, 4)
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted, limited := 0, 0
	ids := make(map[string]struct{})
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ver, err := v.Add([]byte(fmt.Sprintf("input-%02d", i)), impl.CoverSet{Hash: "cov", Edges: []uint64{uint64(100 + i)}}, fmt.Sprintf("ff-%02d", i))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil && ver.Accepted:
				accepted++
				ids[ver.ID] = struct{}{}
			case err == nil && ver.Reason == impl.ReasonQuotaFull:
				limited++
			}
		}(i)
	}
	wg.Wait()
	if accepted != 4 {
		t.Fatalf("concurrent adds stored %d entries, want exactly the cap 4", accepted)
	}
	if limited != 8 {
		t.Fatalf("concurrent adds reported %d quota rejections, want 8", limited)
	}
	if len(ids) != 4 {
		t.Fatalf("accepted verdicts reused %d distinct ids among 4 entries", len(ids))
	}
	if got := len(v.Entries()); got != 4 {
		t.Fatalf("entry count after the race = %d, want 4", got)
	}
	if stats := v.Stats(); stats.Entries != 4 {
		t.Fatalf("stats entry count = %d, want 4", stats.Entries)
	}
}
