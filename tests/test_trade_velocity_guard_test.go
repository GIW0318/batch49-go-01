package task

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	impl "gsb/batch49-go-game-a-28"
)

func tradeOptions() impl.Options {
	return impl.Options{
		WindowSeconds:    100,
		MaxTrades:        10,
		MaxAmount:        100000,
		MaxSharePermille: 800,
		SuspendSeconds:   300,
	}
}

func recordTrade(t *testing.T, g *impl.TradeVelocityGuard, from, to, id string, amount, at int64) {
	t.Helper()
	if err := g.Record(from, to, id, amount, at); err != nil {
		t.Fatalf("record %s: %v", id, err)
	}
}

func TestAllowOnCleanWindow(t *testing.T) {
	g := impl.NewTradeVelocityGuard(tradeOptions())
	v, err := g.Allow("alice", "bob", 500, 50)
	if err != nil {
		t.Fatalf("allow: %v", err)
	}
	if !v.Allowed || v.Code != impl.VerdictAllowed {
		t.Fatalf("clean window verdict = %+v, want allowed", v)
	}
	if v.Trades != 0 || v.Amount != 0 || v.SharePermille != 0 || v.ResetAt != 0 {
		t.Fatalf("clean window counters = %+v, want all zero", v)
	}
}

func TestAllowRejectsTradeCountLimit(t *testing.T) {
	opts := tradeOptions()
	opts.MaxTrades = 3
	g := impl.NewTradeVelocityGuard(opts)
	for i := 0; i < 3; i++ {
		recordTrade(t, g, "alice", "peer", fmt.Sprintf("t-%d", i), 10, int64(10*(i+1)))
	}
	v, err := g.Allow("alice", "peer", 10, 40)
	if err != nil {
		t.Fatalf("allow: %v", err)
	}
	if v.Allowed || v.Code != impl.VerdictWindowCount {
		t.Fatalf("verdict = %+v, want window count rejection", v)
	}
	if v.Trades != 3 {
		t.Fatalf("verdict trades = %d, want 3", v.Trades)
	}
	if v.ResetAt != 110 {
		t.Fatalf("reset at = %d, want 110", v.ResetAt)
	}
}

func TestAllowRejectsAmountLimit(t *testing.T) {
	opts := tradeOptions()
	opts.MaxAmount = 1000
	g := impl.NewTradeVelocityGuard(opts)
	recordTrade(t, g, "alice", "bob", "t-1", 800, 10)
	v, err := g.Allow("alice", "bob", 300, 20)
	if err != nil {
		t.Fatalf("allow: %v", err)
	}
	if v.Allowed || v.Code != impl.VerdictWindowAmount {
		t.Fatalf("over amount verdict = %+v, want window amount rejection", v)
	}
	if v.Amount != 800 || v.Trades != 1 {
		t.Fatalf("window counters = %+v, want amount 800 trades 1", v)
	}
	ok, err := g.Allow("alice", "bob", 200, 20)
	if err != nil {
		t.Fatalf("allow: %v", err)
	}
	if !ok.Allowed {
		t.Fatalf("amount within budget rejected: %+v", ok)
	}
}

func TestAllowDoesNotConsumeQuota(t *testing.T) {
	g := impl.NewTradeVelocityGuard(tradeOptions())
	for i := 0; i < 5; i++ {
		v, err := g.Allow("alice", "bob", 100, 10)
		if err != nil {
			t.Fatalf("allow %d: %v", i, err)
		}
		if !v.Allowed {
			t.Fatalf("allow %d rejected: %+v", i, v)
		}
	}
	stat := g.Window("alice", 20)
	if stat.Trades != 0 || stat.Amount != 0 {
		t.Fatalf("Allow consumed quota, window = %+v", stat)
	}
	if got := len(g.Events("alice")); got != 0 {
		t.Fatalf("Allow wrote %d events", got)
	}
}

func TestRecordAggregatesWindow(t *testing.T) {
	g := impl.NewTradeVelocityGuard(tradeOptions())
	recordTrade(t, g, "alice", "bob", "t-1", 100, 10)
	recordTrade(t, g, "alice", "carol", "t-2", 200, 20)
	stat := g.Window("alice", 30)
	if stat.Trades != 2 || stat.Amount != 300 {
		t.Fatalf("window = %+v, want 2 trades and amount 300", stat)
	}
	if stat.Peers["bob"] != 100 || stat.Peers["carol"] != 200 {
		t.Fatalf("peer amounts = %+v, want bob 100 carol 200", stat.Peers)
	}
	if stat.ResetAt != 110 {
		t.Fatalf("reset at = %d, want 110", stat.ResetAt)
	}
	events := g.Events("alice")
	if len(events) != 2 || events[0].At != 10 || events[1].At != 20 {
		t.Fatalf("events = %+v, want two ordered records", events)
	}
	events[0].Amount = 99
	if again := g.Events("alice"); again[0].Amount != 100 {
		t.Fatalf("Events must return a copy, got %d", again[0].Amount)
	}
}

func TestRecordValidation(t *testing.T) {
	g := impl.NewTradeVelocityGuard(tradeOptions())
	cases := []struct {
		from, to, id string
		amount, at   int64
	}{
		{"", "bob", "t", 10, 10},
		{"alice", "", "t", 10, 10},
		{"alice", "alice", "t", 10, 10},
		{"alice", "bob", "", 10, 10},
		{"alice", "bob", "t", 0, 10},
		{"alice", "bob", "t", -1, 10},
		{"alice", "bob", "t", 10, 0},
	}
	for i, c := range cases {
		if err := g.Record(c.from, c.to, c.id, c.amount, c.at); !errors.Is(err, impl.ErrBadTrade) {
			t.Fatalf("bad record %d = %v, want ErrBadTrade", i, err)
		}
	}
	if stat := g.Window("alice", 100); stat.Trades != 0 || stat.Amount != 0 {
		t.Fatalf("rejected records changed the window: %+v", stat)
	}
}

func TestRecordDeduplicatesTradeID(t *testing.T) {
	g := impl.NewTradeVelocityGuard(tradeOptions())
	recordTrade(t, g, "alice", "bob", "trade-1", 100, 10)
	recordTrade(t, g, "alice", "carol", "trade-1", 900, 20)
	stat := g.Window("alice", 30)
	if stat.Trades != 1 || stat.Amount != 100 {
		t.Fatalf("duplicate record counted twice: %+v", stat)
	}
	if stat.Peers["bob"] != 100 {
		t.Fatalf("first peer amount = %d, want 100", stat.Peers["bob"])
	}
	if stat.Peers["carol"] != 0 {
		t.Fatalf("duplicate record added carol amount %d", stat.Peers["carol"])
	}
	if got := len(g.Events("alice")); got != 1 {
		t.Fatalf("events = %d, want 1", got)
	}
}

func TestWindowSlidesWithTime(t *testing.T) {
	g := impl.NewTradeVelocityGuard(tradeOptions())
	recordTrade(t, g, "alice", "bob", "t-1", 100, 10)
	recordTrade(t, g, "alice", "bob", "t-2", 50, 50)
	both := g.Window("alice", 109)
	if both.Trades != 2 || both.Amount != 150 || both.ResetAt != 110 {
		t.Fatalf("window at 109 = %+v, want both records", both)
	}
	later := g.Window("alice", 120)
	if later.Trades != 1 || later.Amount != 50 || later.ResetAt != 150 {
		t.Fatalf("window at 120 = %+v, want only the record at 50", later)
	}
	empty := g.Window("alice", 300)
	if empty.Trades != 0 || empty.Amount != 0 || empty.ResetAt != 0 {
		t.Fatalf("window at 300 = %+v, want empty", empty)
	}
}

func TestSuspensionBlocksAllow(t *testing.T) {
	g := impl.NewTradeVelocityGuard(tradeOptions())
	if err := g.Suspend("alice", "manual review", 500); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	v, err := g.Allow("alice", "bob", 10, 100)
	if err != nil {
		t.Fatalf("allow: %v", err)
	}
	if v.Allowed || v.Code != impl.VerdictSuspended {
		t.Fatalf("suspended verdict = %+v, want suspended rejection", v)
	}
	if v.Until != 500 {
		t.Fatalf("verdict until = %d, want 500", v.Until)
	}
	status := g.Status("alice", 100)
	if !status.Suspended || status.Reason != "manual review" || status.Until != 500 {
		t.Fatalf("status = %+v, want a suspension until 500", status)
	}
	after, err := g.Allow("alice", "bob", 10, 600)
	if err != nil {
		t.Fatalf("allow after suspension: %v", err)
	}
	if !after.Allowed {
		t.Fatalf("expired suspension must not block: %+v", after)
	}
	if g.Status("alice", 600).Suspended {
		t.Fatalf("expired suspension still reported as active")
	}
}

func TestApproveClearsSuspension(t *testing.T) {
	g := impl.NewTradeVelocityGuard(tradeOptions())
	if err := g.Suspend("alice", "review", 900); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if err := g.Approve("alice"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if g.Status("alice", 100).Suspended {
		t.Fatalf("approved account is still suspended")
	}
	v, err := g.Allow("alice", "bob", 10, 100)
	if err != nil || !v.Allowed {
		t.Fatalf("approved account verdict = %+v err=%v, want allowed", v, err)
	}
	if err := g.Approve("nobody"); err != nil {
		t.Fatalf("approving an unknown account must not fail, got %v", err)
	}
}

func TestSuspendValidation(t *testing.T) {
	g := impl.NewTradeVelocityGuard(tradeOptions())
	if err := g.Suspend("", "review", 100); !errors.Is(err, impl.ErrBadAccount) {
		t.Fatalf("blank account = %v, want ErrBadAccount", err)
	}
	if err := g.Suspend("alice", "", 100); !errors.Is(err, impl.ErrBadReason) {
		t.Fatalf("blank reason = %v, want ErrBadReason", err)
	}
	if err := g.Suspend("alice", "review", 0); !errors.Is(err, impl.ErrBadUntil) {
		t.Fatalf("non positive until = %v, want ErrBadUntil", err)
	}
	status := g.Status("stranger", 10)
	if status.Suspended || status.Until != 0 {
		t.Fatalf("unknown account status = %+v, want inactive", status)
	}
}

func TestConcentrationRejectsAndSuspends(t *testing.T) {
	opts := tradeOptions()
	opts.MaxSharePermille = 600
	opts.SuspendSeconds = 300
	g := impl.NewTradeVelocityGuard(opts)
	recordTrade(t, g, "alice", "bob", "t-1", 700, 10)
	recordTrade(t, g, "alice", "carol", "t-2", 100, 20)
	v, err := g.Allow("alice", "bob", 500, 30)
	if err != nil {
		t.Fatalf("allow: %v", err)
	}
	if v.Allowed || v.Code != impl.VerdictConcentration {
		t.Fatalf("concentration verdict = %+v, want rejection", v)
	}
	if v.Until != 330 {
		t.Fatalf("concentration until = %d, want 330", v.Until)
	}
	status := g.Status("alice", 30)
	if !status.Suspended || status.Reason != "concentration" {
		t.Fatalf("status after concentration = %+v", status)
	}
	if err := g.Approve("alice"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	small, err := g.Allow("alice", "carol", 10, 40)
	if err != nil {
		t.Fatalf("allow after approval: %v", err)
	}
	if !small.Allowed {
		t.Fatalf("small trade to the second counterparty rejected: %+v", small)
	}
}

func TestCounterpartShare(t *testing.T) {
	g := impl.NewTradeVelocityGuard(tradeOptions())
	recordTrade(t, g, "alice", "bob", "t-1", 300, 10)
	recordTrade(t, g, "alice", "carol", "t-2", 100, 20)
	if got := g.CounterpartShare("alice", "bob", 30); got != 750 {
		t.Fatalf("bob share = %d, want 750", got)
	}
	if got := g.CounterpartShare("alice", "carol", 30); got != 250 {
		t.Fatalf("carol share = %d, want 250", got)
	}
	if got := g.CounterpartShare("alice", "dave", 30); got != 0 {
		t.Fatalf("unknown peer share = %d, want 0", got)
	}
	if got := g.CounterpartShare("nobody", "bob", 30); got != 0 {
		t.Fatalf("empty window share = %d, want 0", got)
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	g := impl.NewTradeVelocityGuard(tradeOptions())
	recordTrade(t, g, "alice", "bob", "t-1", 100, 10)
	recordTrade(t, g, "alice", "carol", "t-2", 200, 20)
	if err := g.Suspend("alice", "review", 500); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := impl.NewTradeVelocityGuard(tradeOptions())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	stat := restored.Window("alice", 30)
	if stat.Trades != 2 || stat.Amount != 300 {
		t.Fatalf("restored window = %+v, want 2 trades amount 300", stat)
	}
	if !restored.Status("alice", 30).Suspended {
		t.Fatalf("restored suspension is missing")
	}
	if err := restored.Record("alice", "dave", "t-1", 400, 40); err != nil {
		t.Fatalf("duplicate record after restore: %v", err)
	}
	if got := restored.Window("alice", 50); got.Trades != 2 {
		t.Fatalf("restored trade id index was lost, trades = %d", got.Trades)
	}
	if err := restored.Restore([]byte("broken")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("bad payload = %v, want ErrBadSnapshot", err)
	}
	if got := restored.Window("alice", 30); got.Trades != 2 {
		t.Fatalf("failed restore changed the window to %d trades", got.Trades)
	}
	if !restored.Status("alice", 30).Suspended {
		t.Fatalf("failed restore cleared the suspension")
	}
}

func TestConcurrentRecordAndAllow(t *testing.T) {
	g := impl.NewTradeVelocityGuard(impl.Options{
		WindowSeconds:    100,
		MaxTrades:        1000,
		MaxAmount:        1000000,
		MaxSharePermille: 1000,
		SuspendSeconds:   300,
	})
	var wg sync.WaitGroup
	errs := make(chan error, 256)
	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				id := fmt.Sprintf("w%d-%d", worker, i)
				if err := g.Record("alice", "bob", id, 10, 500); err != nil {
					errs <- err
					return
				}
			}
		}(w)
	}
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				if _, err := g.Allow("alice", "bob", 10, 500); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent operation failed: %v", err)
	}
	stat := g.Window("alice", 501)
	if stat.Trades != 60 || stat.Amount != 600 {
		t.Fatalf("window after concurrent records = %+v, want 60 trades amount 600", stat)
	}
}
