package task

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	impl "gsb/batch49-go-safety-b-10"
)

type fixedClock struct {
	at time.Time
}

func (c fixedClock) Now() time.Time { return c.at }

func watchConfig(quorum int, clock impl.Clock) impl.Config {
	return impl.Config{Quorum: quorum, Clock: clock}
}

func hashBytes(seed byte) []byte {
	out := make([]byte, 32)
	for i := range out {
		out[i] = seed
	}
	return out
}

func treeHead(logID, source string, size uint64, root []byte, at time.Time) impl.SignedTreeHead {
	return impl.SignedTreeHead{
		LogID:     logID,
		Source:    source,
		TreeSize:  size,
		RootHash:  root,
		Timestamp: at,
		Signature: []byte("sig-" + source),
	}
}

func chainRoot(start []byte, nodes [][]byte) []byte {
	cur := append([]byte(nil), start...)
	for _, node := range nodes {
		buf := make([]byte, 0, len(cur)+len(node))
		buf = append(buf, cur...)
		buf = append(buf, node...)
		sum := sha256.Sum256(buf)
		cur = sum[:]
	}
	return cur
}

func acceptHead(t *testing.T, w *impl.SplitViewWatch, logID string, size uint64, root []byte, at time.Time) {
	t.Helper()
	for i, source := range []string{"src-a", "src-b"} {
		v, err := w.Offer(treeHead(logID, source, size, root, at))
		if err != nil {
			t.Fatalf("offer %s/%d from %s failed: %v", logID, size, source, err)
		}
		if i == 0 {
			if v.Status != impl.StatusPending {
				t.Fatalf("first source of %s/%d status = %q, want pending", logID, size, v.Status)
			}
			continue
		}
		if v.Status != impl.StatusAccepted {
			t.Fatalf("offer %s/%d from %s status = %q, want accepted", logID, size, source, v.Status)
		}
	}
}

func TestOfferQuorumAccepted(t *testing.T) {
	clock := fixedClock{at: time.Unix(30000, 0)}
	w := impl.NewSplitViewWatch(watchConfig(2, clock))
	root := hashBytes(0x11)
	v, err := w.Offer(treeHead("ct-1", "src-a", 8, root, clock.Now()))
	if err != nil {
		t.Fatalf("first offer must succeed, got %v", err)
	}
	if v.Status != impl.StatusPending || v.Sources != 1 {
		t.Fatalf("first offer verdict = %+v, want a single source pending", v)
	}
	v, err = w.Offer(treeHead("ct-1", "src-b", 8, root, clock.Now()))
	if err != nil {
		t.Fatalf("second offer must succeed, got %v", err)
	}
	if v.Status != impl.StatusAccepted || v.Sources != 2 {
		t.Fatalf("second offer verdict = %+v, want accepted with two sources", v)
	}
	head, err := w.Consensus("ct-1")
	if err != nil {
		t.Fatalf("consensus after quorum must succeed, got %v", err)
	}
	if head.TreeSize != 8 || head.Sources != 2 || !bytes.Equal(head.RootHash, root) {
		t.Fatalf("agreed head = %+v", head)
	}
	st := w.Stats()
	if st.Accepted != 1 || st.Pending != 1 || st.Offers != 2 {
		t.Fatalf("stats after quorum = %+v", st)
	}
}

func TestOfferBelowQuorumPending(t *testing.T) {
	clock := fixedClock{at: time.Unix(30100, 0)}
	w := impl.NewSplitViewWatch(watchConfig(3, clock))
	root := hashBytes(0x12)
	for _, source := range []string{"src-a", "src-b"} {
		v, err := w.Offer(treeHead("ct-2", source, 6, root, clock.Now()))
		if err != nil {
			t.Fatalf("offer from %s failed: %v", source, err)
		}
		if v.Status != impl.StatusPending {
			t.Fatalf("offer from %s status = %q, want pending below a quorum of three", source, v.Status)
		}
	}
	if _, err := w.Consensus("ct-2"); !errors.Is(err, impl.ErrNoConsensus) {
		t.Fatalf("consensus below quorum must fail with ErrNoConsensus, got %v", err)
	}
	if got := w.Stats().Pending; got != 2 {
		t.Fatalf("pending counter = %d, want 2", got)
	}
}

func TestDisagreeingSourcesStayPending(t *testing.T) {
	clock := fixedClock{at: time.Unix(30200, 0)}
	w := impl.NewSplitViewWatch(watchConfig(2, clock))
	if _, err := w.Offer(treeHead("ct-3", "src-a", 5, hashBytes(0x21), clock.Now())); err != nil {
		t.Fatalf("first offer failed: %v", err)
	}
	v, err := w.Offer(treeHead("ct-3", "src-b", 5, hashBytes(0x22), clock.Now()))
	if err != nil {
		t.Fatalf("second offer failed: %v", err)
	}
	if v.Status != impl.StatusPending {
		t.Fatalf("disagreeing sources without an agreed head must stay pending, got %q", v.Status)
	}
	if _, err := w.Consensus("ct-3"); !errors.Is(err, impl.ErrNoConsensus) {
		t.Fatalf("disagreeing sources must not produce consensus, got %v", err)
	}
	if w.Quarantined("ct-3") {
		t.Fatalf("a log without an agreed head must not be quarantined on disagreement alone")
	}
	if got := w.Stats().SplitViews; got != 0 {
		t.Fatalf("split view counter = %d, want 0 before any head was accepted", got)
	}
}

func TestConflictingRootAfterConsensusSplitsView(t *testing.T) {
	clock := fixedClock{at: time.Unix(30300, 0)}
	w := impl.NewSplitViewWatch(watchConfig(2, clock))
	acceptHead(t, w, "ct-4", 10, hashBytes(0x31), clock.Now())
	v, err := w.Offer(treeHead("ct-4", "src-c", 10, hashBytes(0x32), clock.Now()))
	if err != nil {
		t.Fatalf("conflicting offer must be classified, got %v", err)
	}
	if v.Status != impl.StatusSplitView {
		t.Fatalf("same size with a different root status = %q, want split_view", v.Status)
	}
	if !w.Quarantined("ct-4") {
		t.Fatalf("a split view must quarantine the log")
	}
	if _, err := w.Consensus("ct-4"); !errors.Is(err, impl.ErrQuarantined) {
		t.Fatalf("consensus on a quarantined log must fail with ErrQuarantined, got %v", err)
	}
	if got := w.Stats().SplitViews; got != 1 {
		t.Fatalf("split view counter = %d, want 1", got)
	}
}

func TestSmallerTreeHeadIsStale(t *testing.T) {
	clock := fixedClock{at: time.Unix(30400, 0)}
	w := impl.NewSplitViewWatch(watchConfig(2, clock))
	acceptHead(t, w, "ct-5", 10, hashBytes(0x41), clock.Now())
	for _, source := range []string{"src-c", "src-d"} {
		v, err := w.Offer(treeHead("ct-5", source, 8, hashBytes(0x42), clock.Now()))
		if err != nil {
			t.Fatalf("stale offer from %s failed: %v", source, err)
		}
		if v.Status != impl.StatusStale {
			t.Fatalf("stale offer from %s status = %q, want stale", source, v.Status)
		}
	}
	head, err := w.Consensus("ct-5")
	if err != nil {
		t.Fatalf("stale offers must not break consensus, got %v", err)
	}
	if head.TreeSize != 10 {
		t.Fatalf("agreed tree size = %d, want the accepted 10", head.TreeSize)
	}
	if w.Quarantined("ct-5") {
		t.Fatalf("a smaller tree must not quarantine the log")
	}
	if got := w.Stats().Stale; got != 2 {
		t.Fatalf("stale counter = %d, want 2", got)
	}
}

func TestVerifyConsistencyValidChain(t *testing.T) {
	clock := fixedClock{at: time.Unix(30500, 0)}
	w := impl.NewSplitViewWatch(watchConfig(2, clock))
	root4 := hashBytes(0x51)
	nodes := [][]byte{hashBytes(0x52), hashBytes(0x53)}
	root16 := chainRoot(root4, nodes)
	acceptHead(t, w, "ct-6", 4, root4, clock.Now())
	acceptHead(t, w, "ct-6", 16, root16, clock.Now())
	ok, err := w.VerifyConsistency(4, 16, nodes)
	if err != nil {
		t.Fatalf("valid consistency proof must not error, got %v", err)
	}
	if !ok {
		t.Fatalf("valid consistency proof must verify")
	}
}

func TestVerifyConsistencyTamperedNode(t *testing.T) {
	clock := fixedClock{at: time.Unix(30600, 0)}
	w := impl.NewSplitViewWatch(watchConfig(2, clock))
	root4 := hashBytes(0x61)
	nodes := [][]byte{hashBytes(0x62), hashBytes(0x63)}
	root16 := chainRoot(root4, nodes)
	acceptHead(t, w, "ct-7", 4, root4, clock.Now())
	acceptHead(t, w, "ct-7", 16, root16, clock.Now())
	tampered := [][]byte{hashBytes(0x62), hashBytes(0x64)}
	ok, err := w.VerifyConsistency(4, 16, tampered)
	if err != nil {
		t.Fatalf("a well formed but wrong proof must not error, got %v", err)
	}
	if ok {
		t.Fatalf("tampered consistency proof must not verify")
	}
}

func TestVerifyConsistencyUnknownSizes(t *testing.T) {
	clock := fixedClock{at: time.Unix(30700, 0)}
	w := impl.NewSplitViewWatch(watchConfig(2, clock))
	acceptHead(t, w, "ct-8", 4, hashBytes(0x71), clock.Now())
	if _, err := w.VerifyConsistency(4, 8, [][]byte{hashBytes(0x72)}); !errors.Is(err, impl.ErrUnknownHead) {
		t.Fatalf("unknown to size must fail with ErrUnknownHead, got %v", err)
	}
	if _, err := w.VerifyConsistency(2, 4, [][]byte{hashBytes(0x73)}); !errors.Is(err, impl.ErrUnknownHead) {
		t.Fatalf("unknown from size must fail with ErrUnknownHead, got %v", err)
	}
}

func TestVerifyConsistencyEqualSizes(t *testing.T) {
	clock := fixedClock{at: time.Unix(30800, 0)}
	w := impl.NewSplitViewWatch(watchConfig(2, clock))
	acceptHead(t, w, "ct-9", 4, hashBytes(0x81), clock.Now())
	ok, err := w.VerifyConsistency(4, 4, nil)
	if err != nil {
		t.Fatalf("equal size proof must not error, got %v", err)
	}
	if !ok {
		t.Fatalf("equal size proof with no nodes must verify")
	}
}

func TestVerifyConsistencyBadRange(t *testing.T) {
	clock := fixedClock{at: time.Unix(30900, 0)}
	w := impl.NewSplitViewWatch(watchConfig(2, clock))
	if _, err := w.VerifyConsistency(0, 4, nil); !errors.Is(err, impl.ErrBadRange) {
		t.Fatalf("zero from size must fail with ErrBadRange, got %v", err)
	}
	if _, err := w.VerifyConsistency(9, 5, nil); !errors.Is(err, impl.ErrBadRange) {
		t.Fatalf("from above to must fail with ErrBadRange, got %v", err)
	}
}

func TestQuarantineValidation(t *testing.T) {
	clock := fixedClock{at: time.Unix(31000, 0)}
	w := impl.NewSplitViewWatch(watchConfig(2, clock))
	if _, err := w.Offer(treeHead("ct-10", "src-a", 3, hashBytes(0x91), clock.Now())); err != nil {
		t.Fatalf("offer must succeed, got %v", err)
	}
	if err := w.Quarantine("ct-10", "   "); !errors.Is(err, impl.ErrBadReason) {
		t.Fatalf("blank reason must fail with ErrBadReason, got %v", err)
	}
	if w.Quarantined("ct-10") {
		t.Fatalf("a rejected quarantine must not isolate the log")
	}
	if err := w.Quarantine("ghost", "manual"); !errors.Is(err, impl.ErrUnknownLog) {
		t.Fatalf("unknown log must fail with ErrUnknownLog, got %v", err)
	}
	if err := w.Quarantine("ct-10", "manual"); err != nil {
		t.Fatalf("valid quarantine must succeed, got %v", err)
	}
	if !w.Quarantined("ct-10") {
		t.Fatalf("quarantined log must be reported as isolated")
	}
}

func TestQuarantinedLogExcludedFromConsensus(t *testing.T) {
	clock := fixedClock{at: time.Unix(31100, 0)}
	w := impl.NewSplitViewWatch(watchConfig(2, clock))
	acceptHead(t, w, "ct-11", 7, hashBytes(0xa1), clock.Now())
	if err := w.Quarantine("ct-11", "operator_hold"); err != nil {
		t.Fatalf("quarantine must succeed, got %v", err)
	}
	if _, err := w.Consensus("ct-11"); !errors.Is(err, impl.ErrQuarantined) {
		t.Fatalf("consensus must fail with ErrQuarantined, got %v", err)
	}
	if got := w.Stats().Quarantined; got != 1 {
		t.Fatalf("quarantined counter = %d, want 1", got)
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	clock := fixedClock{at: time.Unix(31200, 0)}
	cfg := watchConfig(2, clock)
	w := impl.NewSplitViewWatch(cfg)
	acceptHead(t, w, "ct-a", 8, hashBytes(0xb1), clock.Now())
	acceptHead(t, w, "ct-b", 4, hashBytes(0xb2), clock.Now())
	if err := w.Quarantine("ct-b", "manual"); err != nil {
		t.Fatalf("quarantine before snapshot must succeed, got %v", err)
	}
	blob, err := w.Snapshot()
	if err != nil {
		t.Fatalf("snapshot must succeed, got %v", err)
	}
	restored := impl.NewSplitViewWatch(cfg)
	if err := restored.Restore(blob); err != nil {
		t.Fatalf("restore must accept a snapshot written by Snapshot, got %v", err)
	}
	head, err := restored.Consensus("ct-a")
	if err != nil {
		t.Fatalf("restored consensus must succeed, got %v", err)
	}
	if head.TreeSize != 8 || !bytes.Equal(head.RootHash, hashBytes(0xb1)) {
		t.Fatalf("restored head = %+v", head)
	}
	if !restored.Quarantined("ct-b") {
		t.Fatalf("restored watch must keep the quarantine")
	}
	ok, err := restored.VerifyConsistency(4, 4, nil)
	if err != nil || !ok {
		t.Fatalf("restored size index must answer equal size proofs, got ok=%v err=%v", ok, err)
	}
}

func TestRestoreRejectsBadSnapshot(t *testing.T) {
	clock := fixedClock{at: time.Unix(31300, 0)}
	w := impl.NewSplitViewWatch(watchConfig(2, clock))
	acceptHead(t, w, "ct-12", 5, hashBytes(0xc1), clock.Now())
	if err := w.Restore([]byte("###broken###")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt payload must fail with ErrBadSnapshot, got %v", err)
	}
	head, err := w.Consensus("ct-12")
	if err != nil {
		t.Fatalf("failed restore must leave the live state intact, got %v", err)
	}
	if head.TreeSize != 5 {
		t.Fatalf("live tree size after failed restore = %d, want 5", head.TreeSize)
	}
}

func TestLogsSortedAndStats(t *testing.T) {
	clock := fixedClock{at: time.Unix(31400, 0)}
	w := impl.NewSplitViewWatch(watchConfig(2, clock))
	for _, logID := range []string{"zeta", "alpha", "mike"} {
		if _, err := w.Offer(treeHead(logID, "src-a", 2, hashBytes(0xd1), clock.Now())); err != nil {
			t.Fatalf("offer for %s failed: %v", logID, err)
		}
	}
	logs := w.Logs()
	if len(logs) != 3 || logs[0] != "alpha" || logs[1] != "mike" || logs[2] != "zeta" {
		t.Fatalf("logs = %v, want sorted alpha mike zeta", logs)
	}
	st := w.Stats()
	if st.Logs != 3 || st.Pending != 3 || st.Offers != 3 {
		t.Fatalf("stats after three pending offers = %+v", st)
	}
}
