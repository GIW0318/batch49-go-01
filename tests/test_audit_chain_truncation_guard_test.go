package task

import (
	"errors"
	"sort"
	"strconv"
	"sync"
	"testing"

	impl "gsb/batch49-go-fix-a-15"
)

type t3FakeSigner struct {
	id string
}

func (s t3FakeSigner) KeyID() string { return s.id }

func (s t3FakeSigner) Sign(stream string, seq uint64, digest string) string {
	return s.id + "|" + stream + "|" + strconv.FormatUint(seq, 10) + "|" + digest
}

func t3Limits() impl.Limits {
	return impl.Limits{MaxStreams: 4, MaxRecords: 64, MaxAnchors: 64}
}

func t3New(t *testing.T) *impl.AuditChainTruncationGuard {
	t.Helper()
	g := impl.NewAuditChainTruncationGuard(t3Limits(), t3FakeSigner{id: "key-1"})
	if err := g.RegisterStream("audit-main"); err != nil {
		t.Fatalf("register stream must succeed, got %v", err)
	}
	return g
}

func t3Append(t *testing.T, g *impl.AuditChainTruncationGuard, stream, payload string) impl.Record {
	t.Helper()
	r, err := g.Append(stream, payload)
	if err != nil {
		t.Fatalf("append %q must succeed, got %v", payload, err)
	}
	return r
}

func t3Anchor(t *testing.T, g *impl.AuditChainTruncationGuard, stream string) impl.Anchor {
	t.Helper()
	a, err := g.Anchor(stream)
	if err != nil {
		t.Fatalf("anchor %q must succeed, got %v", stream, err)
	}
	return a
}

func TestRegisterStreamValidation(t *testing.T) {
	noKey := impl.NewAuditChainTruncationGuard(t3Limits(), nil)
	if err := noKey.RegisterStream("audit-main"); !errors.Is(err, impl.ErrNilSigner) {
		t.Fatalf("nil signer must report ErrNilSigner, got %v", err)
	}
	g := impl.NewAuditChainTruncationGuard(t3Limits(), t3FakeSigner{id: "key-1"})
	if err := g.RegisterStream("  "); !errors.Is(err, impl.ErrBlankStream) {
		t.Fatalf("blank stream must report ErrBlankStream, got %v", err)
	}
	if err := g.RegisterStream("audit-main"); err != nil {
		t.Fatalf("first register must succeed, got %v", err)
	}
	if err := g.RegisterStream("audit-main"); !errors.Is(err, impl.ErrDuplicateStream) {
		t.Fatalf("duplicate stream must report ErrDuplicateStream, got %v", err)
	}
	if _, err := g.Append("audit-ghost", "p"); !errors.Is(err, impl.ErrUnknownStream) {
		t.Fatalf("append to unknown stream must report ErrUnknownStream, got %v", err)
	}
	if _, err := g.Records("audit-ghost"); !errors.Is(err, impl.ErrUnknownStream) {
		t.Fatalf("records of unknown stream must report ErrUnknownStream, got %v", err)
	}
	if _, err := g.Verify("audit-ghost"); !errors.Is(err, impl.ErrUnknownStream) {
		t.Fatalf("verify of unknown stream must report ErrUnknownStream, got %v", err)
	}
}

func TestAppendAssignsMonotonicSequences(t *testing.T) {
	g := t3New(t)
	first := t3Append(t, g, "audit-main", "login ok")
	second := t3Append(t, g, "audit-main", "role change")
	third := t3Append(t, g, "audit-main", "logout")
	if first.Seq != 1 || second.Seq != 2 || third.Seq != 3 {
		t.Fatalf("sequences = %d,%d,%d, want 1,2,3", first.Seq, second.Seq, third.Seq)
	}
	if first.Stream != "audit-main" || first.Payload != "login ok" {
		t.Fatalf("first record = %+v, want audit-main/login ok", first)
	}
	if first.Digest == "" || first.Digest == second.Digest || second.Digest == third.Digest {
		t.Fatalf("record digests must be non empty and distinct per position")
	}
	head, ok := g.Head("audit-main")
	if !ok || head.Seq != 3 || head.Digest != third.Digest {
		t.Fatalf("head = %+v ok=%v, want sequence 3 with the third digest", head, ok)
	}
	records, err := g.Records("audit-main")
	if err != nil {
		t.Fatalf("records must succeed, got %v", err)
	}
	if len(records) != 3 || records[0].Seq != 1 || records[2].Seq != 3 {
		t.Fatalf("records = %+v, want three entries in sequence order", records)
	}
	if _, err := g.Append("audit-main", "   "); !errors.Is(err, impl.ErrEmptyPayload) {
		t.Fatalf("blank payload must report ErrEmptyPayload, got %v", err)
	}
	head, _ = g.Head("audit-main")
	if head.Seq != 3 {
		t.Fatalf("rejected append must not change the head, seq = %d", head.Seq)
	}
}

func TestDigestDeterministicAcrossGuards(t *testing.T) {
	a := impl.NewAuditChainTruncationGuard(t3Limits(), t3FakeSigner{id: "key-1"})
	b := impl.NewAuditChainTruncationGuard(t3Limits(), t3FakeSigner{id: "key-1"})
	c := impl.NewAuditChainTruncationGuard(t3Limits(), t3FakeSigner{id: "key-1"})
	for _, g := range []*impl.AuditChainTruncationGuard{a, b, c} {
		if err := g.RegisterStream("audit-main"); err != nil {
			t.Fatalf("register stream must succeed, got %v", err)
		}
	}
	for _, payload := range []string{"alpha", "beta", "gamma"} {
		ra := t3Append(t, a, "audit-main", payload)
		rb := t3Append(t, b, "audit-main", payload)
		if ra.Digest != rb.Digest {
			t.Fatalf("digest for %q differs across guards: %q vs %q", payload, ra.Digest, rb.Digest)
		}
	}
	t3Append(t, c, "audit-main", "alpha")
	t3Append(t, c, "audit-main", "beta-other")
	t3Append(t, c, "audit-main", "gamma")
	ca, err := c.Records("audit-main")
	if err != nil {
		t.Fatalf("records must succeed, got %v", err)
	}
	aa, err := a.Records("audit-main")
	if err != nil {
		t.Fatalf("records must succeed, got %v", err)
	}
	if ca[1].Digest == aa[1].Digest {
		t.Fatalf("a different payload at sequence 2 must change that digest")
	}
	if ca[2].Digest == aa[2].Digest {
		t.Fatalf("a changed earlier link must change every later digest")
	}
}

func TestAnchorSignsCurrentHead(t *testing.T) {
	g := t3New(t)
	if _, err := g.Anchor("audit-main"); !errors.Is(err, impl.ErrNoRecords) {
		t.Fatalf("anchor without records must report ErrNoRecords, got %v", err)
	}
	t3Append(t, g, "audit-main", "first")
	head := t3Append(t, g, "audit-main", "second")
	anchor := t3Anchor(t, g, "audit-main")
	if anchor.Stream != "audit-main" || anchor.Seq != 2 || anchor.Digest != head.Digest {
		t.Fatalf("anchor = %+v, want stream audit-main at sequence 2 with head digest", anchor)
	}
	if anchor.KeyID != "key-1" || anchor.Signature == "" {
		t.Fatalf("anchor must carry the active key id and a signature: %+v", anchor)
	}
}

func TestVerifyUnanchoredIsNotIntact(t *testing.T) {
	g := t3New(t)
	t3Append(t, g, "audit-main", "first")
	t3Append(t, g, "audit-main", "second")
	verdict, err := g.Verify("audit-main")
	if err != nil {
		t.Fatalf("verify must succeed, got %v", err)
	}
	if verdict.Status != impl.StatusUnanchored {
		t.Fatalf("status = %q, want unanchored without any peer anchor", verdict.Status)
	}
	if verdict.Head != 2 || verdict.TrustedHead != 0 {
		t.Fatalf("verdict heads = %d/%d, want 2/0", verdict.Head, verdict.TrustedHead)
	}
	if len(verdict.Gaps) != 0 || len(verdict.Forks) != 0 {
		t.Fatalf("unanchored verdict must not invent gaps or forks: %+v", verdict)
	}
}

func TestVerifyIntactWithMatchingAnchor(t *testing.T) {
	g := t3New(t)
	t3Append(t, g, "audit-main", "first")
	t3Append(t, g, "audit-main", "second")
	t3Append(t, g, "audit-main", "third")
	t3Anchor(t, g, "audit-main")
	verdict, err := g.Verify("audit-main")
	if err != nil {
		t.Fatalf("verify must succeed, got %v", err)
	}
	if verdict.Status != impl.StatusIntact {
		t.Fatalf("status = %q, want intact, verdict %+v", verdict.Status, verdict)
	}
	if verdict.Head != 3 || verdict.TrustedHead != 3 {
		t.Fatalf("verdict heads = %d/%d, want 3/3", verdict.Head, verdict.TrustedHead)
	}
	if len(verdict.Gaps) != 0 || len(verdict.Forks) != 0 {
		t.Fatalf("intact verdict must be clean: %+v", verdict)
	}
	gaps, err := g.Gaps("audit-main")
	if err != nil {
		t.Fatalf("gaps must succeed, got %v", err)
	}
	if len(gaps) != 0 {
		t.Fatalf("gaps = %+v, want none", gaps)
	}
}

func TestVerifyDetectsTailTruncation(t *testing.T) {
	local := t3New(t)
	t3Append(t, local, "audit-main", "e1")
	t3Append(t, local, "audit-main", "e2")
	t3Append(t, local, "audit-main", "e3")
	peer := impl.NewAuditChainTruncationGuard(t3Limits(), t3FakeSigner{id: "key-1"})
	if err := peer.RegisterStream("audit-main"); err != nil {
		t.Fatalf("register peer stream must succeed, got %v", err)
	}
	for _, payload := range []string{"e1", "e2", "e3", "e4", "e5"} {
		t3Append(t, peer, "audit-main", payload)
	}
	peerAnchor := t3Anchor(t, peer, "audit-main")
	if peerAnchor.Seq != 5 {
		t.Fatalf("peer anchor seq = %d, want 5", peerAnchor.Seq)
	}
	if err := local.IngestAnchor(peerAnchor); err != nil {
		t.Fatalf("ingest peer anchor must succeed, got %v", err)
	}
	verdict, err := local.Verify("audit-main")
	if err != nil {
		t.Fatalf("verify must succeed, got %v", err)
	}
	if verdict.Status != impl.StatusTruncated {
		t.Fatalf("status = %q, want truncated, verdict %+v", verdict.Status, verdict)
	}
	if verdict.Head != 3 || verdict.TrustedHead != 5 {
		t.Fatalf("verdict heads = %d/%d, want 3/5", verdict.Head, verdict.TrustedHead)
	}
	if len(verdict.Gaps) != 1 || verdict.Gaps[0].From != 4 || verdict.Gaps[0].To != 5 || verdict.Gaps[0].Kind != impl.GapTailTruncation {
		t.Fatalf("gaps = %+v, want one tail_truncation gap from 4 to 5", verdict.Gaps)
	}
	gaps, err := local.Gaps("audit-main")
	if err != nil {
		t.Fatalf("gaps must succeed, got %v", err)
	}
	if len(gaps) != 1 || gaps[0] != verdict.Gaps[0] {
		t.Fatalf("gaps accessor = %+v, want %+v", gaps, verdict.Gaps)
	}
}

func TestVerifyDetectsForkAtSameSequence(t *testing.T) {
	a := t3New(t)
	for _, payload := range []string{"p1", "p2", "p3", "p4", "p5"} {
		t3Append(t, a, "audit-main", payload)
	}
	t3Anchor(t, a, "audit-main")
	b := impl.NewAuditChainTruncationGuard(t3Limits(), t3FakeSigner{id: "key-1"})
	if err := b.RegisterStream("audit-main"); err != nil {
		t.Fatalf("register second stream must succeed, got %v", err)
	}
	for _, payload := range []string{"p1", "p2", "different", "p4", "p5"} {
		t3Append(t, b, "audit-main", payload)
	}
	if err := a.IngestAnchor(t3Anchor(t, b, "audit-main")); err != nil {
		t.Fatalf("ingest conflicting anchor must succeed for later audit, got %v", err)
	}
	verdict, err := a.Verify("audit-main")
	if err != nil {
		t.Fatalf("verify must succeed, got %v", err)
	}
	if verdict.Status != impl.StatusFork {
		t.Fatalf("status = %q, want fork, verdict %+v", verdict.Status, verdict)
	}
	if len(verdict.Forks) != 1 || verdict.Forks[0].Seq != 5 {
		t.Fatalf("forks = %+v, want one fork at sequence 5", verdict.Forks)
	}
}

func TestIngestAnchorRejectsBadInput(t *testing.T) {
	g := t3New(t)
	t3Append(t, g, "audit-main", "e1")
	t3Append(t, g, "audit-main", "e2")
	if err := g.IngestAnchor(impl.Anchor{Stream: "audit-ghost", Seq: 1, Digest: "d", KeyID: "key-1", Signature: "s"}); !errors.Is(err, impl.ErrUnknownStream) {
		t.Fatalf("anchor for unknown stream must report ErrUnknownStream, got %v", err)
	}
	if err := g.IngestAnchor(impl.Anchor{Stream: "audit-main", Seq: 1, Digest: "d", Signature: "s"}); !errors.Is(err, impl.ErrAnchorInvalid) {
		t.Fatalf("anchor without key id must report ErrAnchorInvalid, got %v", err)
	}
	good := t3Anchor(t, g, "audit-main")
	forged := good
	forged.Signature = forged.Signature + "-forged"
	if err := g.IngestAnchor(forged); !errors.Is(err, impl.ErrSignatureInvalid) {
		t.Fatalf("forged anchor must report ErrSignatureInvalid, got %v", err)
	}
	verdict, err := g.Verify("audit-main")
	if err != nil {
		t.Fatalf("verify must succeed, got %v", err)
	}
	if verdict.Status != impl.StatusIntact {
		t.Fatalf("rejected anchors must not change trust state, status = %q", verdict.Status)
	}
}

func TestIngestAnchorIsIdempotent(t *testing.T) {
	g := t3New(t)
	t3Append(t, g, "audit-main", "e1")
	t3Append(t, g, "audit-main", "e2")
	t3Append(t, g, "audit-main", "e3")
	anchor := t3Anchor(t, g, "audit-main")
	if err := g.IngestAnchor(anchor); err != nil {
		t.Fatalf("re-ingesting an anchor must succeed, got %v", err)
	}
	if err := g.IngestAnchor(anchor); err != nil {
		t.Fatalf("third ingest must succeed, got %v", err)
	}
	verdict, err := g.Verify("audit-main")
	if err != nil {
		t.Fatalf("verify must succeed, got %v", err)
	}
	if verdict.Status != impl.StatusIntact || len(verdict.Forks) != 0 {
		t.Fatalf("duplicate anchors must not create forks: %+v", verdict)
	}
}

func TestWatermarkStaysAtHighestAnchor(t *testing.T) {
	g := t3New(t)
	for _, payload := range []string{"e1", "e2", "e3", "e4", "e5"} {
		t3Append(t, g, "audit-main", payload)
	}
	t3Anchor(t, g, "audit-main")
	early := impl.NewAuditChainTruncationGuard(t3Limits(), t3FakeSigner{id: "key-1"})
	if err := early.RegisterStream("audit-main"); err != nil {
		t.Fatalf("register early stream must succeed, got %v", err)
	}
	for _, payload := range []string{"e1", "e2", "e3"} {
		t3Append(t, early, "audit-main", payload)
	}
	if err := g.IngestAnchor(t3Anchor(t, early, "audit-main")); err != nil {
		t.Fatalf("ingest older anchor must succeed, got %v", err)
	}
	verdict, err := g.Verify("audit-main")
	if err != nil {
		t.Fatalf("verify must succeed, got %v", err)
	}
	if verdict.TrustedHead != 5 {
		t.Fatalf("trusted head = %d, want it to stay at the highest anchor 5", verdict.TrustedHead)
	}
	if verdict.Status != impl.StatusIntact {
		t.Fatalf("status = %q, want intact", verdict.Status)
	}
}

func TestKeyRotationKeepsOldAnchorsTrusted(t *testing.T) {
	g := t3New(t)
	t3Append(t, g, "audit-main", "e1")
	t3Append(t, g, "audit-main", "e2")
	t3Append(t, g, "audit-main", "e3")
	oldAnchor := t3Anchor(t, g, "audit-main")
	if oldAnchor.KeyID != "key-1" {
		t.Fatalf("old anchor key = %q, want key-1", oldAnchor.KeyID)
	}
	if err := g.RotateKey(t3FakeSigner{id: "key-2"}); err != nil {
		t.Fatalf("rotate key must succeed, got %v", err)
	}
	if err := g.RotateKey(nil); !errors.Is(err, impl.ErrNilSigner) {
		t.Fatalf("rotating to nil must report ErrNilSigner, got %v", err)
	}
	t3Append(t, g, "audit-main", "e4")
	newAnchor := t3Anchor(t, g, "audit-main")
	if newAnchor.KeyID != "key-2" || newAnchor.Seq != 4 {
		t.Fatalf("new anchor = %+v, want key-2 at sequence 4", newAnchor)
	}
	if err := g.IngestAnchor(oldAnchor); err != nil {
		t.Fatalf("old key anchor must stay trusted after rotation, got %v", err)
	}
	verdict, err := g.Verify("audit-main")
	if err != nil {
		t.Fatalf("verify must succeed, got %v", err)
	}
	if verdict.Status != impl.StatusIntact || verdict.TrustedHead != 4 {
		t.Fatalf("verdict = %+v, want intact at trusted head 4", verdict)
	}
}

func TestRetireKeyRejectsLaterAnchors(t *testing.T) {
	g := t3New(t)
	t3Append(t, g, "audit-main", "e1")
	t3Append(t, g, "audit-main", "e2")
	t3Append(t, g, "audit-main", "e3")
	if err := g.RetireKey("key-1", 2); err != nil {
		t.Fatalf("retire key must succeed, got %v", err)
	}
	if _, err := g.Anchor("audit-main"); !errors.Is(err, impl.ErrKeyRetired) {
		t.Fatalf("anchor beyond the retirement watermark must report ErrKeyRetired, got %v", err)
	}
	if err := g.RetireKey("key-ghost", 5); !errors.Is(err, impl.ErrUnknownKey) {
		t.Fatalf("retiring an unknown key must report ErrUnknownKey, got %v", err)
	}
	verdict, err := g.Verify("audit-main")
	if err != nil {
		t.Fatalf("verify must succeed, got %v", err)
	}
	if verdict.TrustedHead != 0 || verdict.Status != impl.StatusUnanchored {
		t.Fatalf("verdict = %+v, want unanchored because the only anchor was rejected", verdict)
	}
}

func TestConcurrentAppendKeepsUniqueSequences(t *testing.T) {
	g := t3New(t)
	const writers = 30
	var wg sync.WaitGroup
	errCh := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			if _, err := g.Append("audit-main", "payload-"+strconv.Itoa(n)); err != nil {
				errCh <- err
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent append must not fail, got %v", err)
	}
	records, err := g.Records("audit-main")
	if err != nil {
		t.Fatalf("records must succeed, got %v", err)
	}
	if len(records) != writers {
		t.Fatalf("record count = %d, want %d", len(records), writers)
	}
	seen := make(map[uint64]bool, writers)
	for _, r := range records {
		if seen[r.Seq] {
			t.Fatalf("duplicate sequence %d in concurrent appends", r.Seq)
		}
		seen[r.Seq] = true
	}
	seqs := make([]int, 0, writers)
	for seq := range seen {
		seqs = append(seqs, int(seq))
	}
	sort.Ints(seqs)
	for i, seq := range seqs {
		if seq != i+1 {
			t.Fatalf("sequence set = %v, want a contiguous 1..%d range", seqs, writers)
		}
	}
	head, ok := g.Head("audit-main")
	if !ok || head.Seq != writers {
		t.Fatalf("head = %+v ok=%v, want sequence %d", head, ok, writers)
	}
}

func TestSnapshotRestorePreservesChainAndAnchors(t *testing.T) {
	source := t3New(t)
	t3Append(t, source, "audit-main", "e1")
	t3Append(t, source, "audit-main", "e2")
	t3Append(t, source, "audit-main", "e3")
	t3Anchor(t, source, "audit-main")
	data, err := source.Snapshot()
	if err != nil {
		t.Fatalf("snapshot must succeed, got %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	target := impl.NewAuditChainTruncationGuard(t3Limits(), t3FakeSigner{id: "key-1"})
	if err := target.Restore(data); err != nil {
		t.Fatalf("restore must succeed, got %v", err)
	}
	records, err := target.Records("audit-main")
	if err != nil {
		t.Fatalf("records after restore must succeed, got %v", err)
	}
	if len(records) != 3 || records[0].Payload != "e1" || records[2].Payload != "e3" {
		t.Fatalf("records after restore = %+v, want e1..e3", records)
	}
	verdict, err := target.Verify("audit-main")
	if err != nil {
		t.Fatalf("verify after restore must succeed, got %v", err)
	}
	if verdict.Status != impl.StatusIntact || verdict.TrustedHead != 3 {
		t.Fatalf("verdict after restore = %+v, want intact with trusted head 3", verdict)
	}
	if err := target.Restore([]byte("not a snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must report ErrBadSnapshot, got %v", err)
	}
	records, err = target.Records("audit-main")
	if err != nil {
		t.Fatalf("records after failed restore must succeed, got %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("failed restore must leave the live chain untouched, got %d records", len(records))
	}
}
