package task

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	impl "gsb/batch49-go-safety-b-04"
)

func limiterConfig(now *int64) impl.LimiterConfig {
	return impl.LimiterConfig{
		Limit:           10,
		WindowMillis:    1000,
		ShardBudget:     64,
		IdleEvictMillis: 500,
		FoldParent:      "overflow",
		Now:             func() int64 { return *now },
	}
}

func mustAllow(t *testing.T, s *impl.ShardLimiter, key string, cost int) impl.Decision {
	t.Helper()
	d, err := s.Allow(key, cost)
	if err != nil {
		t.Fatalf("allow %q cost %d: %v", key, cost, err)
	}
	return d
}

func TestAllowWithinLimitReportsRemaining(t *testing.T) {
	now := int64(1000)
	s := impl.NewShardLimiter(limiterConfig(&now))
	d := mustAllow(t, s, "k1", 3)
	if !d.Allowed {
		t.Fatalf("first charge under the limit must be allowed, got %+v", d)
	}
	if d.Key != "k1" || d.Limit != 10 || d.Used != 3 || d.Remaining != 7 {
		t.Fatalf("decision = %+v, want key k1 limit 10 used 3 remaining 7", d)
	}
	if d.Reason != impl.ReasonAllowed {
		t.Fatalf("reason = %q, want %q", d.Reason, impl.ReasonAllowed)
	}
	d2 := mustAllow(t, s, "k1", 4)
	if !d2.Allowed || d2.Used != 7 || d2.Remaining != 3 {
		t.Fatalf("second decision = %+v, want used 7 remaining 3", d2)
	}
	u := s.Usage("k1")
	if u.Key != "k1" || u.Used != 7 || u.Remaining != 3 || u.Limit != 10 {
		t.Fatalf("usage = %+v, want used 7 remaining 3 limit 10", u)
	}
	if u.Folded || u.Parent != "" || u.Shards != 0 {
		t.Fatalf("usage of a plain key = %+v, want no fold state", u)
	}
	if got := s.ShardCount(); got != 1 {
		t.Fatalf("shard count = %d, want 1", got)
	}
	if got := len(s.Keys()); got != 1 {
		t.Fatalf("live keys = %d, want 1", got)
	}
	if got := s.Limit(); got != 10 {
		t.Fatalf("limit reader = %d, want 10", got)
	}
}

func TestAllowRejectsOverLimit(t *testing.T) {
	now := int64(1000)
	s := impl.NewShardLimiter(limiterConfig(&now))
	mustAllow(t, s, "k1", 8)
	d := mustAllow(t, s, "k1", 5)
	if d.Allowed {
		t.Fatalf("a charge that overflows the window must be denied, got %+v", d)
	}
	if d.Reason != impl.ReasonExceeded {
		t.Fatalf("reason = %q, want %q", d.Reason, impl.ReasonExceeded)
	}
	if d.Used != 8 || d.Remaining != 2 || d.Limit != 10 {
		t.Fatalf("denied decision = %+v, want used 8 remaining 2", d)
	}
	u := s.Usage("k1")
	if u.Used != 8 {
		t.Fatalf("usage after a denial = %d, want the denied charge to be dropped", u.Used)
	}
	d2 := mustAllow(t, s, "k2", 1)
	if !d2.Allowed {
		t.Fatalf("another key must not be affected by the denial, got %+v", d2)
	}
}

func TestAllowRejectsBadCost(t *testing.T) {
	now := int64(1000)
	s := impl.NewShardLimiter(limiterConfig(&now))
	for _, cost := range []int{0, -1, -100} {
		if _, err := s.Allow("k1", cost); !errors.Is(err, impl.ErrBadCost) {
			t.Fatalf("cost %d must fail with ErrBadCost, got %v", cost, err)
		}
	}
	if got := s.ShardCount(); got != 0 {
		t.Fatalf("rejected charges must not create shards, got %d", got)
	}
}

func TestAllowRejectsBlankKey(t *testing.T) {
	now := int64(1000)
	s := impl.NewShardLimiter(limiterConfig(&now))
	for _, key := range []string{"", "   ", "\t"} {
		if _, err := s.Allow(key, 1); !errors.Is(err, impl.ErrBlankKey) {
			t.Fatalf("blank key %q must fail with ErrBlankKey, got %v", key, err)
		}
	}
	if got := s.ShardCount(); got != 0 {
		t.Fatalf("rejected charges must not create shards, got %d", got)
	}
}

func TestAllowRejectsCostAboveLimit(t *testing.T) {
	now := int64(1000)
	s := impl.NewShardLimiter(limiterConfig(&now))
	d := mustAllow(t, s, "k1", 11)
	if d.Allowed {
		t.Fatalf("a single charge above the limit must be denied, got %+v", d)
	}
	if d.Reason != impl.ReasonCostTooBig {
		t.Fatalf("reason = %q, want %q", d.Reason, impl.ReasonCostTooBig)
	}
	if d.Used != 0 || d.Remaining != 10 {
		t.Fatalf("decision = %+v, want used 0 remaining 10", d)
	}
	if u := s.Usage("k1"); u.Used != 0 {
		t.Fatalf("usage = %d, want the oversized charge to be dropped", u.Used)
	}
	if got := s.ShardCount(); got != 0 {
		t.Fatalf("an oversized charge must not create a shard, got %d", got)
	}
}

func TestSlidingWindowExpiresOldUsage(t *testing.T) {
	now := int64(1000)
	s := impl.NewShardLimiter(limiterConfig(&now))
	mustAllow(t, s, "k1", 4)
	now = 1500
	d := mustAllow(t, s, "k1", 4)
	if !d.Allowed || d.Used != 8 || d.Remaining != 2 {
		t.Fatalf("decision at 1500 = %+v, want used 8 remaining 2", d)
	}
	now = 1501
	d = mustAllow(t, s, "k1", 4)
	if d.Allowed {
		t.Fatalf("the charge at 1501 must overflow the window, got %+v", d)
	}
	if d.Used != 8 || d.Remaining != 2 {
		t.Fatalf("denied decision at 1501 = %+v, want used 8 remaining 2", d)
	}
	now = 2000
	d = mustAllow(t, s, "k1", 4)
	if !d.Allowed {
		t.Fatalf("the charge at 2000 must be allowed after the oldest entry aged out, got %+v", d)
	}
	if d.Used != 8 || d.Remaining != 2 {
		t.Fatalf("decision at 2000 = %+v, want the 1500 and 2000 entries only, used 8", d)
	}
}

func TestFoldRedirectsChildKeysToParent(t *testing.T) {
	now := int64(1000)
	s := impl.NewShardLimiter(limiterConfig(&now))
	if err := s.Fold([]string{"a", "b"}, "parent"); err != nil {
		t.Fatalf("fold a and b into parent: %v", err)
	}
	ua := s.Usage("a")
	if !ua.Folded || ua.Parent != "parent" {
		t.Fatalf("usage of a folded key = %+v, want it routed to parent", ua)
	}
	if got, ok := s.ParentOf("a"); !ok || got != "parent" {
		t.Fatalf("parent of a = %q ok=%v, want parent", got, ok)
	}
	if _, ok := s.ParentOf("parent"); ok {
		t.Fatalf("the parent itself must not report a parent")
	}
	if d := mustAllow(t, s, "a", 3); !d.Allowed {
		t.Fatalf("the folded key must still be allowed, got %+v", d)
	}
	up := s.Usage("parent")
	if up.Used != 3 {
		t.Fatalf("parent usage = %d, want the 3 charged through a", up.Used)
	}
	if up.Shards != 2 {
		t.Fatalf("parent shard tally = %d, want the two folded sub keys", up.Shards)
	}
	if d := mustAllow(t, s, "b", 2); !d.Allowed {
		t.Fatalf("the second folded key must be allowed, got %+v", d)
	}
	if up := s.Usage("parent"); up.Used != 5 {
		t.Fatalf("parent usage = %d, want 3+2 = 5", up.Used)
	}
	if ua := s.Usage("a"); ua.Folded != true || ua.Parent != "parent" {
		t.Fatalf("usage of a after the second charge = %+v, want it still folded", ua)
	}
}

func TestFoldRejectsBlankParentAndCycle(t *testing.T) {
	now := int64(1000)
	s := impl.NewShardLimiter(limiterConfig(&now))
	if err := s.Fold([]string{"a"}, "  "); !errors.Is(err, impl.ErrBlankKey) {
		t.Fatalf("a blank parent must fail with ErrBlankKey, got %v", err)
	}
	if err := s.Fold([]string{"p"}, "p"); !errors.Is(err, impl.ErrFoldCycle) {
		t.Fatalf("folding a key into itself must fail with ErrFoldCycle, got %v", err)
	}
	if err := s.Fold([]string{"a"}, "p"); err != nil {
		t.Fatalf("fold a into p: %v", err)
	}
	if err := s.Fold([]string{"p"}, "a"); !errors.Is(err, impl.ErrFoldCycle) {
		t.Fatalf("folding the parent back into its child must fail with ErrFoldCycle, got %v", err)
	}
	if got, ok := s.ParentOf("a"); !ok || got != "p" {
		t.Fatalf("parent of a = %q ok=%v, want the original fold to survive", got, ok)
	}
}

func TestShardBudgetEnforcedByAutoFold(t *testing.T) {
	now := int64(1000)
	cfg := limiterConfig(&now)
	cfg.ShardBudget = 4
	s := impl.NewShardLimiter(cfg)
	for i := 0; i < 8; i++ {
		key := fmt.Sprintf("tenant-%02d", i)
		d, err := s.Allow(key, 1)
		if err != nil {
			t.Fatalf("allow %s under shard pressure: %v", key, err)
		}
		if !d.Allowed {
			t.Fatalf("allow %s = %+v, want it admitted while the parent window has room", key, d)
		}
		if got := s.ShardCount(); got > 4 {
			t.Fatalf("shard count = %d after %d keys, want it held at the budget 4", got, i+1)
		}
	}
	if got := s.ShardCount(); got < 1 {
		t.Fatalf("shard count = %d, want the table to stay usable", got)
	}
}

func TestSweepEvictsIdleKeys(t *testing.T) {
	now := int64(1000)
	s := impl.NewShardLimiter(limiterConfig(&now))
	mustAllow(t, s, "k1", 1)
	mustAllow(t, s, "k2", 1)
	if got := s.ShardCount(); got != 2 {
		t.Fatalf("shard count = %d, want 2", got)
	}
	now = 1400
	if got := s.Sweep(); got != 0 {
		t.Fatalf("sweep at 400ms idle evicted %d keys, want 0", got)
	}
	if got := s.ShardCount(); got != 2 {
		t.Fatalf("shard count after the early sweep = %d, want 2", got)
	}
	now = 1600
	if got := s.Sweep(); got != 2 {
		t.Fatalf("sweep at 600ms idle evicted %d keys, want 2", got)
	}
	if got := s.ShardCount(); got != 0 {
		t.Fatalf("shard count after the sweep = %d, want 0", got)
	}
	if got := s.Sweep(); got != 0 {
		t.Fatalf("a second sweep on an empty table evicted %d keys, want 0", got)
	}
}

func TestUsageUnknownKeyIsZero(t *testing.T) {
	now := int64(1000)
	s := impl.NewShardLimiter(limiterConfig(&now))
	mustAllow(t, s, "k1", 2)
	u := s.Usage("ghost")
	if u.Key != "ghost" {
		t.Fatalf("usage key = %q, want the requested key echoed back", u.Key)
	}
	if u.Used != 0 || u.Shards != 0 || u.Folded || u.Parent != "" {
		t.Fatalf("usage of an unknown key = %+v, want all zero", u)
	}
	if _, ok := s.ParentOf("ghost"); ok {
		t.Fatalf("an unknown key must not report a parent")
	}
	if got := s.ShardCount(); got != 1 {
		t.Fatalf("reading an unknown key must not allocate, got %d shards", got)
	}
}

func TestResetClearsUsage(t *testing.T) {
	now := int64(1000)
	s := impl.NewShardLimiter(limiterConfig(&now))
	mustAllow(t, s, "k1", 4)
	mustAllow(t, s, "k2", 7)
	s.Reset("k1")
	if u := s.Usage("k1"); u.Used != 0 || u.Remaining != 10 {
		t.Fatalf("usage after reset = %+v, want a clean window", u)
	}
	if u := s.Usage("k2"); u.Used != 7 {
		t.Fatalf("resetting one key touched another, got %d", u.Used)
	}
	d := mustAllow(t, s, "k1", 10)
	if !d.Allowed || d.Remaining != 0 {
		t.Fatalf("a full charge after reset = %+v, want it allowed with no remainder", d)
	}
	s.Reset("ghost")
	if got := s.ShardCount(); got != 2 {
		t.Fatalf("resetting an unknown key must be a no-op, got %d shards", got)
	}
}

func TestConcurrentAllowRespectsLimit(t *testing.T) {
	now := int64(1000)
	cfg := limiterConfig(&now)
	cfg.Limit = 10
	s := impl.NewShardLimiter(cfg)
	var allowed, denied int64
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := s.Allow("hot", 1)
			if err != nil {
				atomic.AddInt64(&denied, 1000)
				return
			}
			if d.Allowed {
				atomic.AddInt64(&allowed, 1)
				return
			}
			if d.Reason == impl.ReasonExceeded {
				atomic.AddInt64(&denied, 1)
			}
		}()
	}
	wg.Wait()
	if allowed != 10 {
		t.Fatalf("concurrent charges admitted %d, want exactly the limit 10", allowed)
	}
	if denied != 54 {
		t.Fatalf("concurrent charges reported %d window denials, want 54", denied)
	}
	if u := s.Usage("hot"); u.Used != 10 {
		t.Fatalf("usage after the race = %d, want 10", u.Used)
	}
	if got := s.ShardCount(); got != 1 {
		t.Fatalf("shard count after the race = %d, want 1", got)
	}
}

func TestSnapshotRestoreKeepsUsageAndFolds(t *testing.T) {
	now := int64(1000)
	cfg := limiterConfig(&now)
	s := impl.NewShardLimiter(cfg)
	mustAllow(t, s, "k1", 4)
	if err := s.Fold([]string{"a", "b"}, "parent"); err != nil {
		t.Fatalf("fold: %v", err)
	}
	mustAllow(t, s, "a", 3)
	data, err := s.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewShardLimiter(cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if u := restored.Usage("k1"); u.Used != 4 {
		t.Fatalf("restored usage of k1 = %d, want 4", u.Used)
	}
	if got, ok := restored.ParentOf("a"); !ok || got != "parent" {
		t.Fatalf("restored fold of a = %q ok=%v, want parent", got, ok)
	}
	if u := restored.Usage("parent"); u.Used != 3 || u.Shards != 2 {
		t.Fatalf("restored parent usage = %+v, want used 3 with two sub keys", u)
	}
	if d := mustAllow(t, restored, "k1", 6); !d.Allowed || d.Remaining != 0 {
		t.Fatalf("the restored window must keep counting, got %+v", d)
	}
	d, err := restored.Allow("k1", 1)
	if err != nil {
		t.Fatalf("allow after restore: %v", err)
	}
	if d.Allowed || d.Reason != impl.ReasonExceeded {
		t.Fatalf("a charge past the restored window = %+v, want it denied", d)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	now := int64(1000)
	cfg := limiterConfig(&now)
	s := impl.NewShardLimiter(cfg)
	mustAllow(t, s, "k1", 5)
	for _, payload := range [][]byte{nil, []byte(""), []byte("{not-a-snapshot"), []byte(`{"shards":`)} {
		if err := s.Restore(payload); !errors.Is(err, impl.ErrBadSnapshot) {
			t.Fatalf("restoring %q must fail with ErrBadSnapshot, got %v", string(payload), err)
		}
	}
	if u := s.Usage("k1"); u.Used != 5 {
		t.Fatalf("a rejected restore must not touch the live window, got %d", u.Used)
	}
	if d := mustAllow(t, s, "k1", 5); !d.Allowed {
		t.Fatalf("the live window must keep working after a rejected restore, got %+v", d)
	}
}
