package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch49-go-game-a-27"
)

func stallOptions() impl.Options {
	return impl.Options{MaxSlots: 4, MaxPageSize: 10, FeePermille: 250}
}

func stallItem(id string) impl.Item {
	return impl.Item{ID: id, Name: "item-" + id, Count: 1}
}

func openStall(t *testing.T, g *impl.StallGuard, owner string, slots int) impl.Stall {
	t.Helper()
	st, err := g.Open(owner, slots)
	if err != nil {
		t.Fatalf("open stall %s: %v", owner, err)
	}
	return st
}

func listItem(t *testing.T, g *impl.StallGuard, owner, item string, price int64) impl.Listing {
	t.Helper()
	l, err := g.List(owner, stallItem(item), price)
	if err != nil {
		t.Fatalf("list %s for %s: %v", item, owner, err)
	}
	return l
}

func TestOpenStallAndValidation(t *testing.T) {
	g := impl.NewStallGuard(stallOptions())
	st := openStall(t, g, "s1", 3)
	if st.Owner != "s1" || st.Slots != 3 || st.Used != 0 {
		t.Fatalf("opened stall = %+v, want owner s1 slots 3 used 0", st)
	}
	if _, err := g.Open("s1", 2); !errors.Is(err, impl.ErrStallOpen) {
		t.Fatalf("duplicate open = %v, want ErrStallOpen", err)
	}
	if _, err := g.Open("s2", 0); !errors.Is(err, impl.ErrBadSlots) {
		t.Fatalf("zero slots = %v, want ErrBadSlots", err)
	}
	if _, err := g.Open("s2", 5); !errors.Is(err, impl.ErrBadSlots) {
		t.Fatalf("slots above cap = %v, want ErrBadSlots", err)
	}
	if _, err := g.Open("   ", 2); !errors.Is(err, impl.ErrBadOwner) {
		t.Fatalf("blank owner = %v, want ErrBadOwner", err)
	}
}

func TestStallLookupReturnsCopy(t *testing.T) {
	g := impl.NewStallGuard(stallOptions())
	if _, ok := g.Stall("s1"); ok {
		t.Fatalf("unknown stall must not be found")
	}
	openStall(t, g, "s1", 3)
	st, ok := g.Stall("s1")
	if !ok {
		t.Fatalf("opened stall must be found")
	}
	st.Used = 99
	again, _ := g.Stall("s1")
	if again.Used != 0 {
		t.Fatalf("Stall must return a copy, used = %d", again.Used)
	}
}

func TestListConsumesSlot(t *testing.T) {
	g := impl.NewStallGuard(stallOptions())
	openStall(t, g, "s1", 2)
	first := listItem(t, g, "s1", "cloak", 400)
	if first.ID == "" || first.State != impl.ListingOnSale || first.Seq <= 0 {
		t.Fatalf("listing = %+v, want a fresh on sale listing", first)
	}
	if first.Owner != "s1" || first.Price != 400 || first.Item.ID != "cloak" {
		t.Fatalf("listing fields = %+v", first)
	}
	second := listItem(t, g, "s1", "boots", 250)
	if second.Seq <= first.Seq {
		t.Fatalf("listing seq must grow: %d then %d", first.Seq, second.Seq)
	}
	st, _ := g.Stall("s1")
	if st.Used != 2 {
		t.Fatalf("used slots = %d, want 2", st.Used)
	}
	if _, err := g.List("s1", stallItem("ring"), 100); !errors.Is(err, impl.ErrSlotLimit) {
		t.Fatalf("over slot limit = %v, want ErrSlotLimit", err)
	}
	stored, ok := g.ListingOf(first.ID)
	if !ok || stored.Price != 400 || stored.Item.Name != "item-cloak" {
		t.Fatalf("ListingOf = %+v ok=%v", stored, ok)
	}
	if _, ok := g.ListingOf("missing"); ok {
		t.Fatalf("unknown listing must not be found")
	}
}

func TestListValidation(t *testing.T) {
	g := impl.NewStallGuard(stallOptions())
	if _, err := g.List("s1", stallItem("cloak"), 100); !errors.Is(err, impl.ErrNoStall) {
		t.Fatalf("list without stall = %v, want ErrNoStall", err)
	}
	openStall(t, g, "s1", 3)
	cases := []struct {
		item  impl.Item
		price int64
		want  error
	}{
		{impl.Item{ID: "", Name: "x", Count: 1}, 100, impl.ErrBadItem},
		{impl.Item{ID: "x", Name: "", Count: 1}, 100, impl.ErrBadItem},
		{impl.Item{ID: "x", Name: "x", Count: 0}, 100, impl.ErrBadItem},
		{impl.Item{ID: "x", Name: "x", Count: 1}, 0, impl.ErrBadPrice},
		{impl.Item{ID: "x", Name: "x", Count: 1}, -5, impl.ErrBadPrice},
	}
	for i, c := range cases {
		if _, err := g.List("s1", c.item, c.price); !errors.Is(err, c.want) {
			t.Fatalf("bad listing %d = %v, want %v", i, err, c.want)
		}
	}
	st, _ := g.Stall("s1")
	if st.Used != 0 {
		t.Fatalf("rejected listings must not consume slots, used = %d", st.Used)
	}
}

func TestBrowseUsesRefreshedSnapshot(t *testing.T) {
	g := impl.NewStallGuard(stallOptions())
	openStall(t, g, "s1", 4)
	listItem(t, g, "s1", "a", 100)
	listItem(t, g, "s1", "b", 200)
	before, err := g.Browse(1, 10)
	if err != nil {
		t.Fatalf("browse before refresh: %v", err)
	}
	if before.Total != 0 || len(before.Entries) != 0 || before.Version != 0 {
		t.Fatalf("initial snapshot = %+v, want empty version 0", before)
	}
	if err := g.Refresh(); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	first, err := g.Browse(1, 10)
	if err != nil {
		t.Fatalf("browse after refresh: %v", err)
	}
	if first.Total != 2 || first.Version != 1 {
		t.Fatalf("first snapshot total=%d version=%d, want 2/1", first.Total, first.Version)
	}
	if g.SnapshotVersion() != 1 {
		t.Fatalf("SnapshotVersion = %d, want 1", g.SnapshotVersion())
	}
	listItem(t, g, "s1", "c", 300)
	stale, err := g.Browse(1, 10)
	if err != nil {
		t.Fatalf("browse stale: %v", err)
	}
	if stale.Total != 2 || stale.Version != 1 {
		t.Fatalf("browse must stay on the old snapshot, got total=%d version=%d", stale.Total, stale.Version)
	}
	if err := g.Refresh(); err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	fresh, _ := g.Browse(1, 10)
	if fresh.Total != 3 || fresh.Version != 2 {
		t.Fatalf("second snapshot total=%d version=%d, want 3/2", fresh.Total, fresh.Version)
	}
}

func TestBrowsePagingBounds(t *testing.T) {
	g := impl.NewStallGuard(stallOptions())
	openStall(t, g, "s1", 4)
	for i, price := range []int64{500, 100, 300, 200, 400} {
		listItem(t, g, "s1", string(rune('a'+i)), price)
	}
	if err := g.Refresh(); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	page1, err := g.Browse(1, 2)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if page1.Total != 5 || len(page1.Entries) != 2 || page1.Page != 1 || page1.PageSize != 2 {
		t.Fatalf("page 1 = %+v, want 2 of 5", page1)
	}
	page3, err := g.Browse(3, 2)
	if err != nil {
		t.Fatalf("page 3: %v", err)
	}
	if len(page3.Entries) != 1 {
		t.Fatalf("page 3 entries = %d, want 1", len(page3.Entries))
	}
	page9, err := g.Browse(9, 2)
	if err != nil {
		t.Fatalf("page beyond the end must be empty, got %v", err)
	}
	if len(page9.Entries) != 0 || page9.Total != 5 {
		t.Fatalf("page 9 = %+v, want empty entries with total 5", page9)
	}
	if _, err := g.Browse(0, 2); !errors.Is(err, impl.ErrBadPage) {
		t.Fatalf("page 0 = %v, want ErrBadPage", err)
	}
	if _, err := g.Browse(1, 0); !errors.Is(err, impl.ErrBadPage) {
		t.Fatalf("page size 0 = %v, want ErrBadPage", err)
	}
	if _, err := g.Browse(1, 11); !errors.Is(err, impl.ErrBadPage) {
		t.Fatalf("page size above cap = %v, want ErrBadPage", err)
	}
}

func TestBrowseOrderingByPrice(t *testing.T) {
	g := impl.NewStallGuard(stallOptions())
	openStall(t, g, "s1", 4)
	listItem(t, g, "s1", "expensive", 900)
	listItem(t, g, "s1", "cheap", 100)
	listItem(t, g, "s1", "middle", 500)
	if err := g.Refresh(); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	page, err := g.Browse(1, 10)
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	want := []string{"cheap", "middle", "expensive"}
	if len(page.Entries) != len(want) {
		t.Fatalf("entries = %d, want %d", len(page.Entries), len(want))
	}
	for i, name := range want {
		if page.Entries[i].Item.Name != "item-"+name {
			t.Fatalf("entry %d = %s, want item-%s", i, page.Entries[i].Item.Name, name)
		}
		if !page.Entries[i].Available {
			t.Fatalf("entry %d must be marked available", i)
		}
	}
}

func TestBuyRequiresFunds(t *testing.T) {
	g := impl.NewStallGuard(stallOptions())
	openStall(t, g, "s1", 2)
	l := listItem(t, g, "s1", "cloak", 400)
	if _, err := g.Buy("b1", l.ID, 100); !errors.Is(err, impl.ErrInsufficientFunds) {
		t.Fatalf("unfunded buy = %v, want ErrInsufficientFunds", err)
	}
	if got, _ := g.ListingOf(l.ID); got.State != impl.ListingOnSale {
		t.Fatalf("failed buy changed listing state to %q", got.State)
	}
	if got := g.Balance("b1"); got != 0 {
		t.Fatalf("failed buy charged %d", got)
	}
	if err := g.Deposit("b1", 1000); err != nil {
		t.Fatalf("deposit: %v", err)
	}
	p, err := g.Buy("b1", l.ID, 150)
	if err != nil {
		t.Fatalf("buy: %v", err)
	}
	if p.Price != 400 || p.Fee != 100 || p.Net != 300 || p.Buyer != "b1" || p.Seller != "s1" || p.At != 150 {
		t.Fatalf("purchase = %+v, want price 400 fee 100 net 300", p)
	}
	if got := g.Balance("b1"); got != 600 {
		t.Fatalf("buyer balance = %d, want 600", got)
	}
	if got := g.Balance("s1"); got != 0 {
		t.Fatalf("seller balance before settle = %d, want 0", got)
	}
	ledger := g.Ledger("s1")
	if len(ledger) != 1 || ledger[0].Gross != 400 || ledger[0].Net != 300 || ledger[0].Settled {
		t.Fatalf("ledger = %+v", ledger)
	}
	if sold, _ := g.ListingOf(l.ID); sold.State != impl.ListingSold {
		t.Fatalf("listing state = %q, want sold", sold.State)
	}
}

func TestBuyRejectsSelfTrade(t *testing.T) {
	g := impl.NewStallGuard(stallOptions())
	openStall(t, g, "s1", 2)
	l := listItem(t, g, "s1", "cloak", 400)
	if err := g.Deposit("s1", 1000); err != nil {
		t.Fatalf("deposit: %v", err)
	}
	if _, err := g.Buy("s1", l.ID, 100); !errors.Is(err, impl.ErrSelfTrade) {
		t.Fatalf("self trade = %v, want ErrSelfTrade", err)
	}
	if got := g.Balance("s1"); got != 1000 {
		t.Fatalf("self trade changed balance to %d", got)
	}
	if got, _ := g.ListingOf(l.ID); got.State != impl.ListingOnSale {
		t.Fatalf("self trade changed listing state to %q", got.State)
	}
	if got := len(g.Ledger("s1")); got != 0 {
		t.Fatalf("self trade wrote %d ledger entries", got)
	}
}

func TestBuyOnlyOnce(t *testing.T) {
	g := impl.NewStallGuard(stallOptions())
	openStall(t, g, "s1", 2)
	l := listItem(t, g, "s1", "cloak", 400)
	for _, buyer := range []string{"b1", "b2"} {
		if err := g.Deposit(buyer, 1000); err != nil {
			t.Fatalf("deposit %s: %v", buyer, err)
		}
	}
	if _, err := g.Buy("b1", l.ID, 100); err != nil {
		t.Fatalf("first buy: %v", err)
	}
	if _, err := g.Buy("b2", l.ID, 200); !errors.Is(err, impl.ErrListingGone) {
		t.Fatalf("second buy = %v, want ErrListingGone", err)
	}
	if got := g.Balance("b2"); got != 1000 {
		t.Fatalf("losing buyer balance = %d, want 1000", got)
	}
	if got := len(g.Ledger("s1")); got != 1 {
		t.Fatalf("ledger entries = %d, want 1", got)
	}
}

func TestStaleListingChargesNothing(t *testing.T) {
	g := impl.NewStallGuard(stallOptions())
	openStall(t, g, "s1", 2)
	l := listItem(t, g, "s1", "cloak", 400)
	if err := g.Refresh(); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	for _, buyer := range []string{"b1", "b2"} {
		if err := g.Deposit(buyer, 1000); err != nil {
			t.Fatalf("deposit %s: %v", buyer, err)
		}
	}
	if _, err := g.Buy("b1", l.ID, 100); err != nil {
		t.Fatalf("buy: %v", err)
	}
	if _, err := g.Buy("b2", l.ID, 200); !errors.Is(err, impl.ErrListingGone) {
		t.Fatalf("stale buy = %v, want ErrListingGone", err)
	}
	if got := g.Balance("b2"); got != 1000 {
		t.Fatalf("stale buy charged the buyer, balance = %d", got)
	}
}

func TestCloseReturnsEscrow(t *testing.T) {
	g := impl.NewStallGuard(stallOptions())
	openStall(t, g, "s1", 3)
	first := listItem(t, g, "s1", "cloak", 400)
	second := listItem(t, g, "s1", "boots", 250)
	back, err := g.Close("s1")
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if len(back) != 2 {
		t.Fatalf("close returned %d listings, want 2", len(back))
	}
	ids := map[string]bool{first.ID: true, second.ID: true}
	for _, l := range back {
		if !ids[l.ID] || l.State != impl.ListingCancelled {
			t.Fatalf("returned listing = %+v, want cancelled original", l)
		}
	}
	if _, ok := g.Stall("s1"); ok {
		t.Fatalf("closed stall must not be readable")
	}
	if got, _ := g.ListingOf(first.ID); got.State != impl.ListingCancelled {
		t.Fatalf("listing state after close = %q", got.State)
	}
	if err := g.Deposit("b1", 1000); err != nil {
		t.Fatalf("deposit: %v", err)
	}
	if _, err := g.Buy("b1", first.ID, 100); !errors.Is(err, impl.ErrListingGone) {
		t.Fatalf("buy cancelled listing = %v, want ErrListingGone", err)
	}
	reopened := openStall(t, g, "s1", 1)
	if reopened.Used != 0 {
		t.Fatalf("reopened stall used = %d, want 0", reopened.Used)
	}
}

func TestSettleReleasesUpToTime(t *testing.T) {
	g := impl.NewStallGuard(stallOptions())
	openStall(t, g, "s1", 4)
	listings := []impl.Listing{
		listItem(t, g, "s1", "a", 400),
		listItem(t, g, "s1", "b", 400),
		listItem(t, g, "s1", "c", 400),
	}
	for i, buyer := range []string{"b1", "b2", "b3"} {
		if err := g.Deposit(buyer, 1000); err != nil {
			t.Fatalf("deposit %s: %v", buyer, err)
		}
		if _, err := g.Buy(buyer, listings[i].ID, int64(100*(i+1))); err != nil {
			t.Fatalf("buy %d: %v", i, err)
		}
	}
	first, err := g.Settle("s1", 250)
	if err != nil {
		t.Fatalf("settle 250: %v", err)
	}
	if len(first.Entries) != 2 || first.Total != 600 {
		t.Fatalf("settle 250 entries=%d total=%d, want 2/600", len(first.Entries), first.Total)
	}
	if first.Entries[0].At != 100 || first.Entries[1].At != 200 {
		t.Fatalf("settle 250 order = %+v, want at 100 then 200", first.Entries)
	}
	if got := g.Balance("s1"); got != 600 {
		t.Fatalf("seller balance after first settle = %d, want 600", got)
	}
	second, err := g.Settle("s1", 1000)
	if err != nil {
		t.Fatalf("settle 1000: %v", err)
	}
	if len(second.Entries) != 1 || second.Total != 300 {
		t.Fatalf("settle 1000 entries=%d total=%d, want 1/300", len(second.Entries), second.Total)
	}
	if got := g.Balance("s1"); got != 900 {
		t.Fatalf("seller balance after second settle = %d, want 900", got)
	}
	third, err := g.Settle("s1", 2000)
	if err != nil {
		t.Fatalf("settle 2000: %v", err)
	}
	if len(third.Entries) != 0 || third.Total != 0 {
		t.Fatalf("third settle = %+v, want empty", third)
	}
	ledger := g.Ledger("s1")
	if len(ledger) != 3 {
		t.Fatalf("ledger len = %d, want 3", len(ledger))
	}
	for i, entry := range ledger {
		if !entry.Settled {
			t.Fatalf("ledger entry %d still unsettled", i)
		}
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	g := impl.NewStallGuard(stallOptions())
	openStall(t, g, "s1", 3)
	first := listItem(t, g, "s1", "a", 400)
	listItem(t, g, "s1", "b", 250)
	if err := g.Deposit("b1", 1000); err != nil {
		t.Fatalf("deposit: %v", err)
	}
	if _, err := g.Buy("b1", first.ID, 100); err != nil {
		t.Fatalf("buy: %v", err)
	}
	if err := g.Refresh(); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := impl.NewStallGuard(stallOptions())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.Balance("b1"); got != 600 {
		t.Fatalf("restored buyer balance = %d, want 600", got)
	}
	st, ok := restored.Stall("s1")
	if !ok || st.Used != 1 {
		t.Fatalf("restored stall = %+v ok=%v, want used 1", st, ok)
	}
	if got, _ := restored.ListingOf(first.ID); got.State != impl.ListingSold {
		t.Fatalf("restored listing state = %q", got.State)
	}
	if got := len(restored.Ledger("s1")); got != 1 {
		t.Fatalf("restored ledger len = %d, want 1", got)
	}
	page, err := restored.Browse(1, 10)
	if err != nil || page.Total != 1 || page.Version != 1 {
		t.Fatalf("restored browse = %+v err=%v, want total 1 version 1", page, err)
	}
	if err := restored.Restore([]byte("not json")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("bad payload = %v, want ErrBadSnapshot", err)
	}
	if got := restored.Balance("b1"); got != 600 {
		t.Fatalf("failed restore changed balance to %d", got)
	}
	if _, ok := restored.Stall("s1"); !ok {
		t.Fatalf("failed restore dropped the stall")
	}
}

func TestConcurrentBuySameListing(t *testing.T) {
	g := impl.NewStallGuard(stallOptions())
	openStall(t, g, "s1", 2)
	l := listItem(t, g, "s1", "cloak", 400)
	buyers := []string{"b1", "b2", "b3", "b4", "b5", "b6", "b7", "b8"}
	for _, buyer := range buyers {
		if err := g.Deposit(buyer, 1000); err != nil {
			t.Fatalf("deposit %s: %v", buyer, err)
		}
	}
	var wg sync.WaitGroup
	results := make(chan error, len(buyers))
	for _, buyer := range buyers {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			_, err := g.Buy(name, l.ID, 100)
			results <- err
		}(buyer)
	}
	wg.Wait()
	close(results)
	wins, gone := 0, 0
	for err := range results {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, impl.ErrListingGone):
			gone++
		default:
			t.Fatalf("unexpected concurrent buy error: %v", err)
		}
	}
	if wins != 1 || gone != 7 {
		t.Fatalf("wins=%d gone=%d, want exactly one winner", wins, gone)
	}
	total := int64(0)
	for _, buyer := range buyers {
		total += g.Balance(buyer)
	}
	if total != int64(len(buyers))*1000-l.Price {
		t.Fatalf("buyer balances sum = %d, want %d", total, int64(len(buyers))*1000-l.Price)
	}
	if got := len(g.Ledger("s1")); got != 1 {
		t.Fatalf("ledger entries = %d, want 1", got)
	}
}
