package task

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	impl "gsb/batch49-go-game-a-24"
)

func shopOptions() impl.Options {
	return impl.Options{MaxOffers: 16, MaxAnchors: 8}
}

func mustOffer(t *testing.T, s *impl.StockReplenisher, o impl.Offer) {
	t.Helper()
	if err := s.DefineOffer(o); err != nil {
		t.Fatalf("define offer %q: %v", o.ID, err)
	}
}

func simpleOffer(id string, stock, limit int) impl.Offer {
	return impl.Offer{ID: id, Cap: stock, Limit: limit, WindowSecs: 1000}
}

func TestDefineOfferRejectsInvalid(t *testing.T) {
	s := impl.NewStockReplenisher(shopOptions())
	bad := []impl.Offer{
		{ID: "", Cap: 10, Limit: 2, WindowSecs: 100},
		{ID: "a", Cap: 0, Limit: 2, WindowSecs: 100},
		{ID: "b", Cap: 10, Limit: 0, WindowSecs: 100},
		{ID: "c", Cap: 10, Limit: 2, WindowSecs: 0},
	}
	for _, o := range bad {
		if err := s.DefineOffer(o); !errors.Is(err, impl.ErrInvalidOffer) {
			t.Fatalf("offer %+v must fail with ErrInvalidOffer, got %v", o, err)
		}
	}
}

func TestDefineOfferRejectsBadAnchorsAndDuplicate(t *testing.T) {
	s := impl.NewStockReplenisher(impl.Options{MaxAnchors: 3})
	cases := [][]int64{
		{500, 100},
		{100, 100},
		{100, 200, 300, 400},
	}
	for _, anchors := range cases {
		o := simpleOffer("limited", 10, 2)
		o.Anchors = anchors
		if err := s.DefineOffer(o); !errors.Is(err, impl.ErrInvalidAnchor) {
			t.Fatalf("anchors %v must fail with ErrInvalidAnchor, got %v", anchors, err)
		}
	}
	mustOffer(t, s, simpleOffer("limited", 10, 2))
	if err := s.DefineOffer(simpleOffer("limited", 10, 2)); !errors.Is(err, impl.ErrDuplicateOffer) {
		t.Fatalf("duplicate offer must fail with ErrDuplicateOffer, got %v", err)
	}
}

func TestPurchaseRejectsBadInput(t *testing.T) {
	s := impl.NewStockReplenisher(shopOptions())
	mustOffer(t, s, simpleOffer("o1", 10, 5))
	if _, err := s.Purchase("acc", "missing", 1, 10); !errors.Is(err, impl.ErrUnknownOffer) {
		t.Fatalf("unknown offer must fail with ErrUnknownOffer, got %v", err)
	}
	if _, err := s.Purchase("   ", "o1", 1, 10); !errors.Is(err, impl.ErrBlankAccount) {
		t.Fatalf("blank account must fail with ErrBlankAccount, got %v", err)
	}
	for _, qty := range []int{0, -2} {
		if _, err := s.Purchase("acc", "o1", qty, 10); !errors.Is(err, impl.ErrInvalidQuantity) {
			t.Fatalf("qty %d must fail with ErrInvalidQuantity, got %v", qty, err)
		}
	}
}

func TestPurchaseDecrementsStock(t *testing.T) {
	s := impl.NewStockReplenisher(shopOptions())
	mustOffer(t, s, simpleOffer("o1", 10, 5))
	r, err := s.Purchase("acc-1", "o1", 3, 100)
	if err != nil {
		t.Fatalf("purchase: %v", err)
	}
	if r.ID == "" || r.Account != "acc-1" || r.OfferID != "o1" || r.Qty != 3 || r.Reversed {
		t.Fatalf("receipt = %+v, want a fresh receipt for 3 units", r)
	}
	if got := s.Stock("o1", 100); got != 7 {
		t.Fatalf("stock = %d, want 7", got)
	}
	if got := s.Remaining("acc-1", "o1"); got != 2 {
		t.Fatalf("remaining limit = %d, want 2", got)
	}
	receipts := s.Receipts("acc-1")
	if len(receipts) != 1 || receipts[0].ID != r.ID {
		t.Fatalf("receipts = %+v, want the returned receipt", receipts)
	}
}

func TestPurchaseRejectsOutOfStock(t *testing.T) {
	s := impl.NewStockReplenisher(shopOptions())
	mustOffer(t, s, simpleOffer("o1", 5, 10))
	if _, err := s.Purchase("acc-1", "o1", 6, 100); !errors.Is(err, impl.ErrOutOfStock) {
		t.Fatalf("oversized purchase must fail with ErrOutOfStock, got %v", err)
	}
	if got := s.Stock("o1", 100); got != 5 {
		t.Fatalf("rejected purchase changed stock to %d, want 5", got)
	}
	if got := len(s.Receipts("acc-1")); got != 0 {
		t.Fatalf("rejected purchase left %d receipts", got)
	}
}

func TestPurchaseEnforcesAccountLimit(t *testing.T) {
	s := impl.NewStockReplenisher(shopOptions())
	mustOffer(t, s, simpleOffer("o1", 10, 2))
	if _, err := s.Purchase("acc-1", "o1", 2, 100); err != nil {
		t.Fatalf("first purchase: %v", err)
	}
	if _, err := s.Purchase("acc-1", "o1", 1, 200); !errors.Is(err, impl.ErrLimitExceeded) {
		t.Fatalf("purchase over the limit must fail with ErrLimitExceeded, got %v", err)
	}
	if got := s.Stock("o1", 200); got != 8 {
		t.Fatalf("rejected purchase changed stock to %d, want 8", got)
	}
	if got := s.Remaining("acc-1", "o1"); got != 0 {
		t.Fatalf("remaining = %d, want 0", got)
	}
}

func TestLimitIsPerAccount(t *testing.T) {
	s := impl.NewStockReplenisher(shopOptions())
	mustOffer(t, s, simpleOffer("o1", 10, 2))
	for _, account := range []string{"acc-a", "acc-b"} {
		if _, err := s.Purchase(account, "o1", 2, 100); err != nil {
			t.Fatalf("purchase for %s: %v", account, err)
		}
	}
	if got := s.Stock("o1", 100); got != 6 {
		t.Fatalf("stock after two accounts = %d, want 6", got)
	}
	if got := s.Remaining("acc-a", "o1"); got != 0 {
		t.Fatalf("acc-a remaining = %d, want 0", got)
	}
	if _, err := s.Purchase("acc-c", "o1", 1, 100); err != nil {
		t.Fatalf("third account purchase: %v", err)
	}
	if got := s.Stock("o1", 100); got != 5 {
		t.Fatalf("stock after third account = %d, want 5", got)
	}
}

func TestAnchorRestocksAtFixedTime(t *testing.T) {
	s := impl.NewStockReplenisher(shopOptions())
	o := simpleOffer("o1", 10, 10)
	o.Anchors = []int64{1000}
	mustOffer(t, s, o)
	if _, err := s.Purchase("acc-1", "o1", 6, 900); err != nil {
		t.Fatalf("purchase before anchor: %v", err)
	}
	if got := s.Stock("o1", 900); got != 4 {
		t.Fatalf("stock before anchor = %d, want 4", got)
	}
	res, err := s.Restock("o1", 1000)
	if err != nil {
		t.Fatalf("restock: %v", err)
	}
	if len(res.Anchors) != 1 || res.Anchors[0] != 1000 {
		t.Fatalf("applied anchors = %v, want [1000]", res.Anchors)
	}
	if res.Added != 6 || res.Stock != 10 {
		t.Fatalf("restock result = %+v, want added 6 stock 10", res)
	}
	if got := s.Stock("o1", 1000); got != 10 {
		t.Fatalf("stock at anchor = %d, want 10", got)
	}
}

func TestRestockIsIdempotentPerAnchor(t *testing.T) {
	s := impl.NewStockReplenisher(shopOptions())
	o := simpleOffer("o1", 10, 10)
	o.Anchors = []int64{1000}
	mustOffer(t, s, o)
	if _, err := s.Purchase("acc-1", "o1", 4, 900); err != nil {
		t.Fatalf("purchase: %v", err)
	}
	first, err := s.Restock("o1", 1000)
	if err != nil {
		t.Fatalf("first restock: %v", err)
	}
	if first.Added != 4 || len(first.Anchors) != 1 {
		t.Fatalf("first restock = %+v, want one anchor and 4 units", first)
	}
	second, err := s.Restock("o1", 1000)
	if err != nil {
		t.Fatalf("second restock: %v", err)
	}
	if second.Added != 0 || len(second.Anchors) != 0 || second.Stock != 10 {
		t.Fatalf("second restock = %+v, want no anchors and no units", second)
	}
}

func TestReverseRestoresStock(t *testing.T) {
	s := impl.NewStockReplenisher(shopOptions())
	mustOffer(t, s, simpleOffer("o1", 10, 5))
	r, err := s.Purchase("acc-1", "o1", 3, 100)
	if err != nil {
		t.Fatalf("purchase: %v", err)
	}
	if err := s.Reverse("acc-1", r.ID, 200); err != nil {
		t.Fatalf("reverse: %v", err)
	}
	if got := s.Stock("o1", 200); got != 10 {
		t.Fatalf("stock after reverse = %d, want 10", got)
	}
	receipts := s.Receipts("acc-1")
	if len(receipts) != 1 || !receipts[0].Reversed {
		t.Fatalf("receipts after reverse = %+v, want the receipt marked reversed", receipts)
	}
}

func TestReverseRejectsBadReceipts(t *testing.T) {
	s := impl.NewStockReplenisher(shopOptions())
	mustOffer(t, s, simpleOffer("o1", 10, 5))
	r, err := s.Purchase("acc-1", "o1", 1, 100)
	if err != nil {
		t.Fatalf("purchase: %v", err)
	}
	if err := s.Reverse("acc-1", r.ID, 200); err != nil {
		t.Fatalf("first reverse: %v", err)
	}
	if err := s.Reverse("acc-1", r.ID, 300); !errors.Is(err, impl.ErrAlreadyReversed) {
		t.Fatalf("second reverse must fail with ErrAlreadyReversed, got %v", err)
	}
	if err := s.Reverse("acc-2", r.ID, 300); !errors.Is(err, impl.ErrReceiptOwner) {
		t.Fatalf("foreign reverse must fail with ErrReceiptOwner, got %v", err)
	}
	if err := s.Reverse("acc-1", "no-such-receipt", 300); !errors.Is(err, impl.ErrReceiptNotFound) {
		t.Fatalf("unknown receipt must fail with ErrReceiptNotFound, got %v", err)
	}
}

func TestConcurrentPurchaseNoOversell(t *testing.T) {
	s := impl.NewStockReplenisher(shopOptions())
	mustOffer(t, s, simpleOffer("o1", 100, 1))
	const workers = 200
	var wg sync.WaitGroup
	var mu sync.Mutex
	sold, soldOut := 0, 0
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.Purchase(fmt.Sprintf("acc-%03d", i), "o1", 1, 100)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				sold++
			case errors.Is(err, impl.ErrOutOfStock):
				soldOut++
			default:
				t.Errorf("unexpected purchase error: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if sold != 100 || soldOut != workers-100 {
		t.Fatalf("sold = %d soldOut = %d, want 100 successful and 100 rejected", sold, soldOut)
	}
	if got := s.Stock("o1", 100); got != 0 {
		t.Fatalf("stock after concurrent purchases = %d, want 0", got)
	}
	total := 0
	for i := 0; i < workers; i++ {
		total += len(s.Receipts(fmt.Sprintf("acc-%03d", i)))
	}
	if total != 100 {
		t.Fatalf("receipts total = %d, want 100", total)
	}
}

func TestConcurrentPurchasePerAccountLimit(t *testing.T) {
	s := impl.NewStockReplenisher(shopOptions())
	mustOffer(t, s, simpleOffer("o1", 1000, 3))
	const workers = 30
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, limited := 0, 0
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Purchase("acc-1", "o1", 1, 100)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, impl.ErrLimitExceeded):
				limited++
			default:
				t.Errorf("unexpected purchase error: %v", err)
			}
		}()
	}
	wg.Wait()
	if ok != 3 || limited != workers-3 {
		t.Fatalf("ok = %d limited = %d, want 3 successful and 27 rejected", ok, limited)
	}
	if got := s.Stock("o1", 100); got != 997 {
		t.Fatalf("stock = %d, want 997", got)
	}
	if got := s.Remaining("acc-1", "o1"); got != 0 {
		t.Fatalf("remaining = %d, want 0", got)
	}
}

func TestSnapshotRestorePreservesStockAndLimits(t *testing.T) {
	opts := shopOptions()
	s := impl.NewStockReplenisher(opts)
	o := simpleOffer("o1", 10, 5)
	o.Anchors = []int64{1000}
	mustOffer(t, s, o)
	r, err := s.Purchase("acc-1", "o1", 3, 900)
	if err != nil {
		t.Fatalf("purchase: %v", err)
	}
	data, err := s.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	restored := impl.NewStockReplenisher(opts)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.Stock("o1", 900); got != 7 {
		t.Fatalf("restored stock = %d, want 7", got)
	}
	if got := restored.Remaining("acc-1", "o1"); got != 2 {
		t.Fatalf("restored remaining = %d, want 2", got)
	}
	receipts := restored.Receipts("acc-1")
	if len(receipts) != 1 || receipts[0].ID != r.ID || receipts[0].Reversed {
		t.Fatalf("restored receipts = %+v, want the original receipt", receipts)
	}
	if err := restored.Reverse("acc-1", r.ID, 950); err != nil {
		t.Fatalf("reverse after restore: %v", err)
	}
	if got := restored.Stock("o1", 950); got != 10 {
		t.Fatalf("stock after restored reverse = %d, want 10", got)
	}
	res, err := restored.Restock("o1", 1000)
	if err != nil {
		t.Fatalf("restock after restore: %v", err)
	}
	if len(res.Anchors) != 1 || res.Stock != 10 {
		t.Fatalf("restock after restore = %+v, want anchor 1000 applied to stock 10", res)
	}
}

func TestRestoreRejectsGarbage(t *testing.T) {
	s := impl.NewStockReplenisher(shopOptions())
	mustOffer(t, s, simpleOffer("o1", 10, 5))
	if _, err := s.Purchase("acc-1", "o1", 3, 100); err != nil {
		t.Fatalf("purchase: %v", err)
	}
	if err := s.Restore([]byte("<<<not a snapshot>>>")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("garbage snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := s.Stock("o1", 100); got != 7 {
		t.Fatalf("failed restore changed stock to %d, want 7", got)
	}
	if got := len(s.Receipts("acc-1")); got != 1 {
		t.Fatalf("failed restore changed receipts to %d, want 1", got)
	}
}
