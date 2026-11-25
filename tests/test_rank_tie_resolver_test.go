package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch49-go-game-a-19"
)

func TestPutRejectsBlankKeys(t *testing.T) {
	r := impl.NewTieRankResolver(impl.Options{})
	if err := r.Put("   ", "player-1", 10); !errors.Is(err, impl.ErrBlankKey) {
		t.Fatalf("blank board must fail with ErrBlankKey, got %v", err)
	}
	if err := r.Put("season", "", 10); !errors.Is(err, impl.ErrBlankKey) {
		t.Fatalf("blank player must fail with ErrBlankKey, got %v", err)
	}
	if got := r.Len("season"); got != 0 {
		t.Fatalf("rejected puts must not create entries, len=%d", got)
	}
}

func TestPutThenRankReflectsScore(t *testing.T) {
	r := impl.NewTieRankResolver(impl.Options{})
	for player, score := range map[string]int64{"alpha": 10, "bravo": 20, "delta": 5} {
		if err := r.Put("season", player, score); err != nil {
			t.Fatalf("put %s: %v", player, err)
		}
	}
	cases := []struct {
		player string
		score  int64
		rank   int
		dense  int
	}{
		{"bravo", 20, 1, 1},
		{"alpha", 10, 2, 2},
		{"delta", 5, 3, 3},
	}
	for _, tc := range cases {
		info, err := r.Rank("season", tc.player)
		if err != nil {
			t.Fatalf("rank %s: %v", tc.player, err)
		}
		if info.Score != tc.score || info.Rank != tc.rank || info.Dense != tc.dense {
			t.Fatalf("rank %s = score %d rank %d dense %d, want %d/%d/%d", tc.player, info.Score, info.Rank, info.Dense, tc.score, tc.rank, tc.dense)
		}
		if info.Total != 3 {
			t.Fatalf("total for %s = %d, want 3", tc.player, info.Total)
		}
		if info.Board != "season" || info.Player != tc.player {
			t.Fatalf("rank identity = %q/%q, want season/%s", info.Board, info.Player, tc.player)
		}
	}
}

func TestRankUnknownPlayerFails(t *testing.T) {
	r := impl.NewTieRankResolver(impl.Options{})
	if _, err := r.Rank("season", "ghost"); !errors.Is(err, impl.ErrPlayerNotFound) {
		t.Fatalf("ranking a missing player must fail with ErrPlayerNotFound, got %v", err)
	}
	if err := r.Put("season", "alpha", 10); err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, err := r.Rank("season", "ghost"); !errors.Is(err, impl.ErrPlayerNotFound) {
		t.Fatalf("ranking a missing player must fail with ErrPlayerNotFound, got %v", err)
	}
}

func TestPutUpdatesExistingPlayer(t *testing.T) {
	r := impl.NewTieRankResolver(impl.Options{})
	if err := r.Put("season", "alpha", 10); err != nil {
		t.Fatalf("put alpha: %v", err)
	}
	if err := r.Put("season", "bravo", 20); err != nil {
		t.Fatalf("put bravo: %v", err)
	}
	if err := r.Put("season", "alpha", 30); err != nil {
		t.Fatalf("update alpha: %v", err)
	}
	if got := r.Len("season"); got != 2 {
		t.Fatalf("board len after an update = %d, want 2", got)
	}
	info, err := r.Rank("season", "alpha")
	if err != nil {
		t.Fatalf("rank alpha: %v", err)
	}
	if info.Score != 30 || info.Rank != 1 || info.Dense != 1 || info.Total != 2 {
		t.Fatalf("updated rank = %+v, want score 30 rank 1 dense 1 total 2", info)
	}
}

func TestTopOrdersTiedScoresByPlayer(t *testing.T) {
	r := impl.NewTieRankResolver(impl.Options{})
	for player, score := range map[string]int64{"zoe": 500, "amy": 500, "bob": 600} {
		if err := r.Put("season", player, score); err != nil {
			t.Fatalf("put %s: %v", player, err)
		}
	}
	rows := r.Top("season", 10)
	if len(rows) != 3 {
		t.Fatalf("top rows = %d, want 3", len(rows))
	}
	wantPlayers := []string{"bob", "amy", "zoe"}
	wantScores := []int64{600, 500, 500}
	for i := range wantPlayers {
		if rows[i].Player != wantPlayers[i] || rows[i].Score != wantScores[i] {
			t.Fatalf("row %d = %s/%d, want %s/%d", i, rows[i].Player, rows[i].Score, wantPlayers[i], wantScores[i])
		}
	}
}

func TestTopLimitsRows(t *testing.T) {
	r := impl.NewTieRankResolver(impl.Options{})
	for i, player := range []string{"alpha", "bravo", "delta"} {
		if err := r.Put("season", player, int64(30-10*i)); err != nil {
			t.Fatalf("put %s: %v", player, err)
		}
	}
	if got := len(r.Top("season", 2)); got != 2 {
		t.Fatalf("top 2 rows = %d, want 2", got)
	}
	if got := len(r.Top("season", 0)); got != 0 {
		t.Fatalf("top 0 rows = %d, want 0", got)
	}
	if got := len(r.Top("season", -3)); got != 0 {
		t.Fatalf("top with a negative limit = %d, want 0", got)
	}
	if got := len(r.Top("season", 50)); got != 3 {
		t.Fatalf("top beyond the board = %d, want 3", got)
	}
	if got := len(r.Top("no-such-board", 5)); got != 0 {
		t.Fatalf("top of an empty board = %d, want 0", got)
	}
}

func TestPageFirstPageHasNextCursor(t *testing.T) {
	r := impl.NewTieRankResolver(impl.Options{})
	for player, score := range map[string]int64{"p1": 10, "p2": 20, "p3": 30, "p4": 40} {
		if err := r.Put("season", player, score); err != nil {
			t.Fatalf("put %s: %v", player, err)
		}
	}
	page, err := r.Page("season", impl.Cursor{}, 2)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(page.Rows) != 2 || page.Rows[0].Player != "p4" || page.Rows[1].Player != "p3" {
		t.Fatalf("first page rows = %+v, want p4 then p3", page.Rows)
	}
	if page.Total != 4 {
		t.Fatalf("first page total = %d, want 4", page.Total)
	}
	if !page.HasMore {
		t.Fatalf("first page must report more rows")
	}
	if page.Next != (impl.Cursor{Score: 30, Player: "p3"}) {
		t.Fatalf("next cursor = %+v, want score 30 player p3", page.Next)
	}
}

func TestPageSecondPageEnds(t *testing.T) {
	r := impl.NewTieRankResolver(impl.Options{})
	for player, score := range map[string]int64{"p1": 10, "p2": 20, "p3": 30, "p4": 40} {
		if err := r.Put("season", player, score); err != nil {
			t.Fatalf("put %s: %v", player, err)
		}
	}
	page, err := r.Page("season", impl.Cursor{Score: 30, Player: "p3"}, 2)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(page.Rows) != 2 || page.Rows[0].Player != "p2" || page.Rows[1].Player != "p1" {
		t.Fatalf("second page rows = %+v, want p2 then p1", page.Rows)
	}
	if page.HasMore {
		t.Fatalf("the last page must not report more rows")
	}
	if page.Next != (impl.Cursor{Score: 10, Player: "p1"}) {
		t.Fatalf("last page cursor = %+v, want score 10 player p1", page.Next)
	}
}

func TestPageRejectsBadLimit(t *testing.T) {
	r := impl.NewTieRankResolver(impl.Options{})
	if err := r.Put("season", "alpha", 10); err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, err := r.Page("season", impl.Cursor{}, 0); !errors.Is(err, impl.ErrBadLimit) {
		t.Fatalf("zero limit must fail with ErrBadLimit, got %v", err)
	}
	if _, err := r.Page("season", impl.Cursor{}, -2); !errors.Is(err, impl.ErrBadLimit) {
		t.Fatalf("negative limit must fail with ErrBadLimit, got %v", err)
	}
}

func TestPercentileCleanBoard(t *testing.T) {
	r := impl.NewTieRankResolver(impl.Options{})
	for player, score := range map[string]int64{"p1": 10, "p2": 20, "p3": 30, "p4": 40} {
		if err := r.Put("season", player, score); err != nil {
			t.Fatalf("put %s: %v", player, err)
		}
	}
	cases := []struct {
		score int64
		want  int
	}{{5, 0}, {30, 50}, {40, 75}, {100, 100}}
	for _, tc := range cases {
		if got := r.Percentile("season", tc.score); got != tc.want {
			t.Fatalf("percentile of %d = %d, want %d", tc.score, got, tc.want)
		}
	}
}

func TestPercentileEmptyBoard(t *testing.T) {
	r := impl.NewTieRankResolver(impl.Options{})
	if got := r.Percentile("season", 50); got != 0 {
		t.Fatalf("percentile of an empty board = %d, want 0", got)
	}
	if err := r.Put("season", "alpha", 100); err != nil {
		t.Fatalf("put: %v", err)
	}
	if got := r.Percentile("season", 100); got != 0 {
		t.Fatalf("percentile of the only score = %d, want 0 because nobody is strictly below", got)
	}
	if got := r.Percentile("season", 101); got != 100 {
		t.Fatalf("percentile above the only score = %d, want 100", got)
	}
}

func TestEvictionSetsWatermark(t *testing.T) {
	r := impl.NewTieRankResolver(impl.Options{MaxEntries: 3})
	for player, score := range map[string]int64{"p1": 10, "p2": 20, "p3": 30} {
		if err := r.Put("season", player, score); err != nil {
			t.Fatalf("put %s: %v", player, err)
		}
	}
	if _, ok := r.Watermark("season"); ok {
		t.Fatalf("a board below the cap must not report an eviction watermark")
	}
	if err := r.Put("season", "p4", 40); err != nil {
		t.Fatalf("put p4: %v", err)
	}
	if got := r.Len("season"); got != 3 {
		t.Fatalf("board len after eviction = %d, want the cap 3", got)
	}
	if _, err := r.Rank("season", "p1"); !errors.Is(err, impl.ErrPlayerNotFound) {
		t.Fatalf("the evicted player must be gone, got %v", err)
	}
	watermark, ok := r.Watermark("season")
	if !ok || watermark != 10 {
		t.Fatalf("watermark = %d ok=%v, want the evicted score 10", watermark, ok)
	}
	if _, err := r.Rank("season", "p4"); err != nil {
		t.Fatalf("the new player must be ranked, got %v", err)
	}
}

func TestConcurrentPutsSerialize(t *testing.T) {
	r := impl.NewTieRankResolver(impl.Options{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	failures := make([]error, 0, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			player := string(rune('a' + i))
			if err := r.Put("season", player, int64(100*(i+1))); err != nil {
				mu.Lock()
				failures = append(failures, err)
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if len(failures) != 0 {
		t.Fatalf("concurrent puts failed: %v", failures)
	}
	if got := r.Len("season"); got != 8 {
		t.Fatalf("board len after concurrent puts = %d, want 8", got)
	}
	rows := r.Top("season", 1)
	if len(rows) != 1 || rows[0].Score != 800 {
		t.Fatalf("top row after concurrent puts = %+v, want score 800", rows)
	}
}

func TestSnapshotRestoreKeepsScoresAndWatermark(t *testing.T) {
	r := impl.NewTieRankResolver(impl.Options{MaxEntries: 3})
	for player, score := range map[string]int64{"p1": 10, "p2": 20, "p3": 30, "p4": 40} {
		if err := r.Put("season", player, score); err != nil {
			t.Fatalf("put %s: %v", player, err)
		}
	}
	data, err := r.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewTieRankResolver(impl.Options{MaxEntries: 3})
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.Len("season"); got != 3 {
		t.Fatalf("restored board len = %d, want 3", got)
	}
	info, err := restored.Rank("season", "p4")
	if err != nil {
		t.Fatalf("rank p4 after restore: %v", err)
	}
	if info.Score != 40 || info.Rank != 1 || info.Total != 3 {
		t.Fatalf("restored rank = %+v, want score 40 rank 1 total 3", info)
	}
	watermark, ok := restored.Watermark("season")
	if !ok || watermark != 10 {
		t.Fatalf("restored watermark = %d ok=%v, want 10", watermark, ok)
	}
	rows := restored.Top("season", 10)
	if len(rows) != 3 || rows[0].Player != "p4" || rows[2].Player != "p2" {
		t.Fatalf("restored top rows = %+v, want p4 first and p2 last", rows)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	r := impl.NewTieRankResolver(impl.Options{})
	if err := r.Put("season", "alpha", 10); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := r.Restore([]byte("{broken-payload")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := r.Len("season"); got != 1 {
		t.Fatalf("a rejected restore changed the live board, len=%d", got)
	}
	info, err := r.Rank("season", "alpha")
	if err != nil || info.Score != 10 {
		t.Fatalf("live rank after a rejected restore = %+v err=%v", info, err)
	}
}
