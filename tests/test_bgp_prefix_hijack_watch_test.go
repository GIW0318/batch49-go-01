package task

import (
	"errors"
	"testing"

	impl "gsb/batch49-go-fix-a-10"
)

func testConfig() impl.Config {
	return impl.Config{MaxBaselines: 8, MaxHistory: 8}
}

func newWatch(t *testing.T, cfg impl.Config) *impl.PrefixHijackWatch {
	t.Helper()
	w, err := impl.NewPrefixHijackWatch(cfg)
	if err != nil {
		t.Fatalf("new prefix watch: %v", err)
	}
	return w
}

func learn(t *testing.T, w *impl.PrefixHijackWatch, prefix string, origin uint32, maxLen int) {
	t.Helper()
	if err := w.Learn(impl.Baseline{Prefix: prefix, OriginAS: origin, MaxLength: maxLen}); err != nil {
		t.Fatalf("learn %s origin %d: %v", prefix, origin, err)
	}
}

func observe(t *testing.T, w *impl.PrefixHijackWatch, route impl.Route) impl.Verdict {
	t.Helper()
	v, err := w.Observe(route)
	if err != nil {
		t.Fatalf("observe %+v: %v", route, err)
	}
	return v
}

func hasReason(v impl.Verdict, reason string) bool {
	for _, r := range v.Reasons {
		if r == reason {
			return true
		}
	}
	return false
}

func TestNewWatchRejectsBadConfig(t *testing.T) {
	if _, err := impl.NewPrefixHijackWatch(impl.Config{MaxBaselines: 0, MaxHistory: 8}); !errors.Is(err, impl.ErrBadConfig) {
		t.Fatalf("zero baseline capacity must fail with ErrBadConfig, got %v", err)
	}
	if _, err := impl.NewPrefixHijackWatch(impl.Config{MaxBaselines: 4, MaxHistory: 0}); !errors.Is(err, impl.ErrBadConfig) {
		t.Fatalf("zero history capacity must fail with ErrBadConfig, got %v", err)
	}
}

func TestLearnRejectsNonCanonicalPrefix(t *testing.T) {
	w := newWatch(t, testConfig())
	if err := w.Learn(impl.Baseline{Prefix: "10.0.0.1/8", OriginAS: 15169, MaxLength: 8}); !errors.Is(err, impl.ErrBadPrefix) {
		t.Fatalf("host bits must fail with ErrBadPrefix, got %v", err)
	}
	if err := w.Learn(impl.Baseline{Prefix: "10.0.0.0/8", OriginAS: 15169, MaxLength: 33}); !errors.Is(err, impl.ErrBadPrefix) {
		t.Fatalf("max length beyond the address width must fail with ErrBadPrefix, got %v", err)
	}
}

func TestLearnIdempotentAndLimit(t *testing.T) {
	cfg := testConfig()
	cfg.MaxBaselines = 1
	w := newWatch(t, cfg)
	learn(t, w, "203.0.113.0/24", 15169, 24)
	if err := w.Learn(impl.Baseline{Prefix: "203.0.113.0/24", OriginAS: 15169, MaxLength: 24}); err != nil {
		t.Fatalf("relearning the same baseline must be a no-op, got %v", err)
	}
	if err := w.Learn(impl.Baseline{Prefix: "198.51.100.0/24", OriginAS: 15169, MaxLength: 24}); !errors.Is(err, impl.ErrBaselineLimit) {
		t.Fatalf("second prefix must fail with ErrBaselineLimit, got %v", err)
	}
	got, ok := w.BaselineFor("203.0.113.0/24")
	if !ok || got.OriginAS != 15169 {
		t.Fatalf("baseline = %+v ok=%v, want origin 15169", got, ok)
	}
}

func TestLearnOriginConflict(t *testing.T) {
	w := newWatch(t, testConfig())
	learn(t, w, "203.0.113.0/24", 15169, 24)
	if err := w.Learn(impl.Baseline{Prefix: "203.0.113.0/24", OriginAS: 174, MaxLength: 24}); !errors.Is(err, impl.ErrOriginConflict) {
		t.Fatalf("a second origin must fail with ErrOriginConflict, got %v", err)
	}
	cfg := testConfig()
	cfg.AllowMultipleOrigins = true
	multi := newWatch(t, cfg)
	learn(t, multi, "203.0.113.0/24", 15169, 24)
	if err := multi.Learn(impl.Baseline{Prefix: "203.0.113.0/24", OriginAS: 174, MaxLength: 24}); err != nil {
		t.Fatalf("a second origin must be allowed, got %v", err)
	}
}

func TestObserveCleanRoute(t *testing.T) {
	w := newWatch(t, testConfig())
	learn(t, w, "203.0.113.0/24", 15169, 24)
	v := observe(t, w, impl.Route{Prefix: "203.0.113.0/24", OriginAS: 15169, ASPath: []uint32{3356, 15169}, PeerAS: 3356, Seq: 1, At: 1000})
	if v.Level != impl.LevelClean || v.Score != 0 || len(v.Reasons) != 0 {
		t.Fatalf("verdict = %+v, want a clean route with score 0", v)
	}
}

func TestOriginMismatchSuspected(t *testing.T) {
	w := newWatch(t, testConfig())
	learn(t, w, "203.0.113.0/24", 15169, 24)
	v := observe(t, w, impl.Route{Prefix: "203.0.113.0/24", OriginAS: 174, ASPath: []uint32{3356, 174}, PeerAS: 3356, Seq: 2, At: 1001})
	if v.Level != impl.LevelSuspected || v.Score != impl.ScoreOriginMismatch {
		t.Fatalf("verdict = %+v, want suspected with score %d", v, impl.ScoreOriginMismatch)
	}
	if !hasReason(v, impl.ReasonOriginMismatch) {
		t.Fatalf("reasons = %v, want %s", v.Reasons, impl.ReasonOriginMismatch)
	}
}

func TestMoreSpecificConfirmed(t *testing.T) {
	w := newWatch(t, testConfig())
	learn(t, w, "203.0.113.0/24", 15169, 24)
	v := observe(t, w, impl.Route{Prefix: "203.0.113.0/25", OriginAS: 174, ASPath: []uint32{3356, 174}, PeerAS: 3356, Seq: 3, At: 1002})
	if !hasReason(v, impl.ReasonMoreSpecific) || !hasReason(v, impl.ReasonOriginMismatch) {
		t.Fatalf("reasons = %v, want both more specific and origin mismatch", v.Reasons)
	}
	if v.Score != impl.ScoreMoreSpecific+impl.ScoreOriginMismatch || v.Level != impl.LevelConfirmed {
		t.Fatalf("verdict = %+v, want confirmed with score %d", v, impl.ScoreMoreSpecific+impl.ScoreOriginMismatch)
	}
}

func TestPathSpoofDetected(t *testing.T) {
	w := newWatch(t, testConfig())
	learn(t, w, "203.0.113.0/24", 15169, 24)
	v := observe(t, w, impl.Route{Prefix: "203.0.113.0/24", OriginAS: 15169, ASPath: []uint32{174, 15169}, PeerAS: 3356, Seq: 4, At: 1003})
	if !hasReason(v, impl.ReasonPathSpoofed) {
		t.Fatalf("reasons = %v, want %s", v.Reasons, impl.ReasonPathSpoofed)
	}
	if v.Score != impl.ScorePathSpoofed || v.Level != impl.LevelSuspected {
		t.Fatalf("verdict = %+v, want suspected with score %d", v, impl.ScorePathSpoofed)
	}
}

func TestPrivateASAndLoopReasons(t *testing.T) {
	w := newWatch(t, testConfig())
	learn(t, w, "203.0.113.0/24", 15169, 24)
	v := observe(t, w, impl.Route{Prefix: "203.0.113.0/24", OriginAS: 15169, ASPath: []uint32{3356, 65100, 65100}, PeerAS: 3356, Seq: 5, At: 1004})
	if !hasReason(v, impl.ReasonPrivateAS) || !hasReason(v, impl.ReasonPathLoop) {
		t.Fatalf("reasons = %v, want private as and path loop", v.Reasons)
	}
	if v.Score != impl.ScorePrivateAS+impl.ScorePathLoop {
		t.Fatalf("score = %d, want %d", v.Score, impl.ScorePrivateAS+impl.ScorePathLoop)
	}
}

func TestSuppressMarksVerdict(t *testing.T) {
	now := int64(5000)
	cfg := testConfig()
	cfg.Now = func() int64 { return now }
	w := newWatch(t, cfg)
	learn(t, w, "203.0.113.0/24", 15169, 24)
	if err := w.Suppress("203.0.113.0/24", now+600); err != nil {
		t.Fatalf("suppress: %v", err)
	}
	v := observe(t, w, impl.Route{Prefix: "203.0.113.0/24", OriginAS: 174, ASPath: []uint32{3356, 174}, PeerAS: 3356, Seq: 6, At: now})
	if v.Level != impl.LevelSuppressed || !v.Suppressed {
		t.Fatalf("verdict = %+v, want a suppressed verdict", v)
	}
	stat := w.PeerStats(3356)
	if stat.Hijacks != 0 || stat.Observations != 1 {
		t.Fatalf("peer stats = %+v, want one observation and no hijack", stat)
	}
}

func TestHistoryBounded(t *testing.T) {
	cfg := testConfig()
	cfg.MaxHistory = 2
	w := newWatch(t, cfg)
	learn(t, w, "203.0.113.0/24", 15169, 24)
	for i := 0; i < 3; i++ {
		observe(t, w, impl.Route{Prefix: "203.0.113.0/24", OriginAS: 174, ASPath: []uint32{3356, 174}, PeerAS: 3356, Seq: uint64(i + 1), At: int64(2000 + i)})
	}
	history := w.History("203.0.113.0/24")
	if len(history) != 2 {
		t.Fatalf("history length = %d, want 2", len(history))
	}
	for _, v := range history {
		if v.Level != impl.LevelSuspected {
			t.Fatalf("history entry = %+v, want a suspected verdict", v)
		}
	}
}

func TestRemoveBaselineUnknownAndUncovered(t *testing.T) {
	w := newWatch(t, testConfig())
	if err := w.RemoveBaseline("203.0.113.0/24"); !errors.Is(err, impl.ErrBaselineNotFound) {
		t.Fatalf("removing an unknown baseline must fail with ErrBaselineNotFound, got %v", err)
	}
	learn(t, w, "203.0.113.0/24", 15169, 24)
	if err := w.RemoveBaseline("203.0.113.0/24"); err != nil {
		t.Fatalf("remove baseline: %v", err)
	}
	v := observe(t, w, impl.Route{Prefix: "203.0.113.0/24", OriginAS: 15169, ASPath: []uint32{3356, 15169}, PeerAS: 3356, Seq: 7, At: 3000})
	if !hasReason(v, impl.ReasonUncovered) || v.Score != impl.ScoreUncovered {
		t.Fatalf("verdict = %+v, want uncovered with score %d", v, impl.ScoreUncovered)
	}
}

func TestPeerStatsAndTopOffenders(t *testing.T) {
	w := newWatch(t, testConfig())
	learn(t, w, "203.0.113.0/24", 15169, 24)
	for i := 0; i < 2; i++ {
		observe(t, w, impl.Route{Prefix: "203.0.113.0/24", OriginAS: 174, ASPath: []uint32{3356, 174}, PeerAS: 3356, Seq: uint64(i + 1), At: int64(4000 + i)})
	}
	observe(t, w, impl.Route{Prefix: "203.0.113.128/25", OriginAS: 15169, ASPath: []uint32{174, 15169}, PeerAS: 174, Seq: 3, At: 4002})
	worst := w.PeerStats(3356)
	if worst.Hijacks != 2 || worst.Observations != 2 {
		t.Fatalf("peer 3356 stats = %+v, want two hijacks out of two observations", worst)
	}
	other := w.PeerStats(174)
	if other.Hijacks != 1 || other.Observations != 1 {
		t.Fatalf("peer 174 stats = %+v, want one hijack out of one observation", other)
	}
	top := w.TopOffenders(5)
	if len(top) != 2 || top[0] != 3356 || top[1] != 174 {
		t.Fatalf("top offenders = %v, want [3356 174]", top)
	}
}

func TestSnapshotRestoreKeepsSuppression(t *testing.T) {
	now := int64(7000)
	cfg := testConfig()
	cfg.Now = func() int64 { return now }
	w := newWatch(t, cfg)
	learn(t, w, "203.0.113.0/24", 15169, 24)
	if err := w.Suppress("203.0.113.0/24", now+900); err != nil {
		t.Fatalf("suppress: %v", err)
	}
	observe(t, w, impl.Route{Prefix: "203.0.113.0/24", OriginAS: 174, ASPath: []uint32{3356, 174}, PeerAS: 3356, Seq: 1, At: now})
	data, err := w.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := newWatch(t, cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !restored.Suppressed("203.0.113.0/24") {
		t.Fatal("the suppression window must survive a restore")
	}
	if _, ok := restored.BaselineFor("203.0.113.0/24"); !ok {
		t.Fatal("the baseline must survive a restore")
	}
	stat := restored.PeerStats(3356)
	if stat.Observations != 1 || stat.Hijacks != 0 {
		t.Fatalf("restored peer stats = %+v, want one observation and no hijack", stat)
	}
}

func TestRestoreRejectsGarbage(t *testing.T) {
	w := newWatch(t, testConfig())
	if err := w.Restore([]byte("null")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("garbage snapshot must fail with ErrBadSnapshot, got %v", err)
	}
}
