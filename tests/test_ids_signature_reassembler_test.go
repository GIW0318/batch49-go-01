package task

import (
	"errors"
	"testing"

	impl "gsb/batch49-go-fix-a-09"
)

func testConfig() impl.Config {
	return impl.Config{
		MaxStreamBytes: 1 << 16,
		MaxSegments:    64,
		MaxFlows:       8,
		FlowTTL:        60000,
	}
}

func newReassembler(t *testing.T, cfg impl.Config) *impl.SignatureReassembler {
	t.Helper()
	r, err := impl.NewSignatureReassembler(cfg)
	if err != nil {
		t.Fatalf("new reassembler: %v", err)
	}
	return r
}

func addSignature(t *testing.T, r *impl.SignatureReassembler, sig impl.Signature) int {
	t.Helper()
	id, err := r.AddSignature(sig)
	if err != nil {
		t.Fatalf("add signature %+v: %v", sig, err)
	}
	return id
}

func feed(t *testing.T, r *impl.SignatureReassembler, flow string, seq uint64, data string, at int64) []impl.Alert {
	t.Helper()
	alerts, err := r.Feed(impl.Segment{FlowID: flow, Seq: seq, Data: []byte(data), At: at})
	if err != nil {
		t.Fatalf("feed %q seq=%d: %v", flow, seq, err)
	}
	return alerts
}

func mustStream(t *testing.T, r *impl.SignatureReassembler, flow string) string {
	t.Helper()
	data, err := r.Stream(flow)
	if err != nil {
		t.Fatalf("stream %q: %v", flow, err)
	}
	return string(data)
}

func TestNewReassemblerRejectsBadConfig(t *testing.T) {
	if _, err := impl.NewSignatureReassembler(impl.Config{MaxStreamBytes: 1024, MaxSegments: 8, MaxFlows: 0, FlowTTL: 1000}); !errors.Is(err, impl.ErrBadConfig) {
		t.Fatalf("zero flow capacity must fail with ErrBadConfig, got %v", err)
	}
	if _, err := impl.NewSignatureReassembler(impl.Config{MaxStreamBytes: 0, MaxSegments: 8, MaxFlows: 4, FlowTTL: 1000}); !errors.Is(err, impl.ErrBadConfig) {
		t.Fatalf("zero stream budget must fail with ErrBadConfig, got %v", err)
	}
}

func TestAddSignatureValidates(t *testing.T) {
	r := newReassembler(t, testConfig())
	if _, err := r.AddSignature(impl.Signature{Name: "empty", Pattern: nil}); !errors.Is(err, impl.ErrInvalidSignature) {
		t.Fatalf("empty pattern must fail with ErrInvalidSignature, got %v", err)
	}
	sig := impl.Signature{ID: 7, Name: "host", Pattern: []byte("Host:")}
	if id := addSignature(t, r, sig); id != 7 {
		t.Fatalf("signature id = %d, want 7", id)
	}
	if _, err := r.AddSignature(sig); !errors.Is(err, impl.ErrDuplicateSignature) {
		t.Fatalf("duplicate signature id must fail with ErrDuplicateSignature, got %v", err)
	}
}

func TestInOrderSegmentsAssemble(t *testing.T) {
	r := newReassembler(t, testConfig())
	feed(t, r, "f1", 100, "GET /", 1000)
	feed(t, r, "f1", 104, "HTTP", 1001)
	if got := mustStream(t, r, "f1"); got != "GET /HTTP" {
		t.Fatalf("stream = %q, want %q", got, "GET /HTTP")
	}
	if got := r.Pending("f1"); got != 0 {
		t.Fatalf("pending = %d, want 0", got)
	}
}

func TestOutOfOrderSegmentBuffered(t *testing.T) {
	r := newReassembler(t, testConfig())
	feed(t, r, "f1", 100, "abc", 1000)
	feed(t, r, "f1", 106, "ghi", 1001)
	if got := mustStream(t, r, "f1"); got != "abc" {
		t.Fatalf("stream before the gap closes = %q, want %q", got, "abc")
	}
	if got := r.Pending("f1"); got != 3 {
		t.Fatalf("pending = %d, want 3", got)
	}
	feed(t, r, "f1", 103, "def", 1002)
	if got := mustStream(t, r, "f1"); got != "abcdefghi" {
		t.Fatalf("stream after the gap closes = %q, want %q", got, "abcdefghi")
	}
	if got := r.Pending("f1"); got != 0 {
		t.Fatalf("pending after the gap closes = %d, want 0", got)
	}
}

func TestDuplicateSegmentIdempotent(t *testing.T) {
	r := newReassembler(t, testConfig())
	feed(t, r, "f1", 100, "abc", 1000)
	feed(t, r, "f1", 100, "abc", 1001)
	if got := mustStream(t, r, "f1"); got != "abc" {
		t.Fatalf("stream = %q, want %q", got, "abc")
	}
	if got := r.Pending("f1"); got != 0 {
		t.Fatalf("pending = %d, want 0", got)
	}
}

func TestOverlapFirstWins(t *testing.T) {
	r := newReassembler(t, testConfig())
	feed(t, r, "f1", 100, "abcde", 1000)
	feed(t, r, "f1", 103, "XXf", 1001)
	if got := mustStream(t, r, "f1"); got != "abcdef" {
		t.Fatalf("stream = %q, want %q with the first bytes kept", got, "abcdef")
	}
}

func TestSignatureMatchAcrossSegments(t *testing.T) {
	r := newReassembler(t, testConfig())
	id := addSignature(t, r, impl.Signature{Name: "http", Pattern: []byte("HTTP/1.1"), AlertOnce: true})
	if alerts := feed(t, r, "f1", 100, "GET / HTTP/1.", 1000); len(alerts) != 0 {
		t.Fatalf("a partial pattern must not alert, got %+v", alerts)
	}
	alerts := feed(t, r, "f1", 113, "1\r\nHost: x", 1001)
	if len(alerts) != 1 {
		t.Fatalf("cross segment match must alert once, got %+v", alerts)
	}
	if alerts[0].SigID != id || alerts[0].Offset != 5 {
		t.Fatalf("alert = %+v, want signature %d at offset 5", alerts[0], id)
	}
	if got := r.AlertCount(id); got != 1 {
		t.Fatalf("alert count = %d, want 1", got)
	}
}

func TestAlertOnceDedupesPerFlow(t *testing.T) {
	r := newReassembler(t, testConfig())
	id := addSignature(t, r, impl.Signature{Name: "marker", Pattern: []byte("XYZ"), AlertOnce: true})
	alerts := feed(t, r, "f1", 100, "XYZ---XYZ", 1000)
	if len(alerts) != 1 {
		t.Fatalf("AlertOnce must keep one alert per flow, got %+v", alerts)
	}
	if got := r.AlertCount(id); got != 1 {
		t.Fatalf("alert count = %d, want 1", got)
	}
}

func TestNoCaseSignature(t *testing.T) {
	r := newReassembler(t, testConfig())
	id := addSignature(t, r, impl.Signature{Name: "nocase", Pattern: []byte("Attack"), NoCase: true, AlertOnce: true})
	alerts := feed(t, r, "f1", 100, "xxaTtAcKxx", 1000)
	if len(alerts) != 1 {
		t.Fatalf("case insensitive pattern must alert, got %+v", alerts)
	}
	if alerts[0].Offset != 2 {
		t.Fatalf("alert offset = %d, want 2", alerts[0].Offset)
	}
	if got := r.AlertCount(id); got != 1 {
		t.Fatalf("alert count = %d, want 1", got)
	}
}

func TestOffsetWindowSkipsEarlyMatch(t *testing.T) {
	r := newReassembler(t, testConfig())
	id := addSignature(t, r, impl.Signature{Name: "late", Pattern: []byte("ABC"), Offset: 4, AlertOnce: true})
	alerts := feed(t, r, "f1", 100, "ABC---ABC", 1000)
	if len(alerts) != 1 {
		t.Fatalf("only the in window match may alert, got %+v", alerts)
	}
	if alerts[0].Offset != 4 {
		t.Fatalf("alert offset = %d, want 4", alerts[0].Offset)
	}
	if got := r.AlertCount(id); got != 1 {
		t.Fatalf("alert count = %d, want 1", got)
	}
}

func TestFlowLimitEnforced(t *testing.T) {
	cfg := testConfig()
	cfg.MaxFlows = 1
	r := newReassembler(t, cfg)
	feed(t, r, "f1", 100, "abc", 1000)
	if _, err := r.Feed(impl.Segment{FlowID: "f2", Seq: 1, Data: []byte("x"), At: 1001}); !errors.Is(err, impl.ErrFlowLimit) {
		t.Fatalf("second flow must fail with ErrFlowLimit, got %v", err)
	}
	if got := r.FlowCount(); got != 1 {
		t.Fatalf("flow count = %d, want 1", got)
	}
}

func TestStreamLimitEvictsFlow(t *testing.T) {
	cfg := testConfig()
	cfg.MaxStreamBytes = 5
	r := newReassembler(t, cfg)
	feed(t, r, "f1", 100, "abcd", 1000)
	if _, err := r.Feed(impl.Segment{FlowID: "f1", Seq: 104, Data: []byte("efgh"), At: 1001}); !errors.Is(err, impl.ErrStreamTooLarge) {
		t.Fatalf("overflowing the stream budget must fail with ErrStreamTooLarge, got %v", err)
	}
	if _, err := r.Stream("f1"); !errors.Is(err, impl.ErrUnknownFlow) {
		t.Fatalf("the evicted flow must be gone, got %v", err)
	}
	if got := r.FlowCount(); got != 0 {
		t.Fatalf("flow count = %d, want 0", got)
	}
}

func TestCloseFlowBlocksFeed(t *testing.T) {
	r := newReassembler(t, testConfig())
	feed(t, r, "f1", 100, "abc", 1000)
	if err := r.CloseFlow("f1"); err != nil {
		t.Fatalf("close flow: %v", err)
	}
	if _, err := r.Feed(impl.Segment{FlowID: "f1", Seq: 103, Data: []byte("d"), At: 1001}); !errors.Is(err, impl.ErrFlowClosed) {
		t.Fatalf("feeding a closed flow must fail with ErrFlowClosed, got %v", err)
	}
	if got := mustStream(t, r, "f1"); got != "abc" {
		t.Fatalf("stream after close = %q, want %q", got, "abc")
	}
	if err := r.CloseFlow("f1"); err != nil {
		t.Fatalf("closing twice must be a no-op, got %v", err)
	}
}

func TestEvictIdleFlows(t *testing.T) {
	r := newReassembler(t, testConfig())
	feed(t, r, "f1", 100, "abc", 1000)
	feed(t, r, "f2", 200, "xyz", 1000)
	if got := r.EvictIdle(1000 + testConfig().FlowTTL); got != 2 {
		t.Fatalf("evicted = %d, want 2", got)
	}
	if got := r.FlowCount(); got != 0 {
		t.Fatalf("flow count after eviction = %d, want 0", got)
	}
	if _, err := r.Stream("f1"); !errors.Is(err, impl.ErrUnknownFlow) {
		t.Fatalf("evicted flow stream must fail with ErrUnknownFlow, got %v", err)
	}
}

func TestSnapshotRestoreKeepsStreamAndAlerts(t *testing.T) {
	r := newReassembler(t, testConfig())
	id := addSignature(t, r, impl.Signature{ID: 3, Name: "http", Pattern: []byte("HTTP"), AlertOnce: true})
	if alerts := feed(t, r, "f1", 100, "GET HTTP", 1000); len(alerts) != 1 {
		t.Fatalf("first segment must alert once, got %+v", alerts)
	}
	data, err := r.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := newReassembler(t, testConfig())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := mustStream(t, restored, "f1"); got != "GET HTTP" {
		t.Fatalf("restored stream = %q, want %q", got, "GET HTTP")
	}
	if got := restored.AlertCount(id); got != 1 {
		t.Fatalf("restored alert count = %d, want 1", got)
	}
	if _, err := restored.AddSignature(impl.Signature{ID: id, Name: "http", Pattern: []byte("HTTP")}); !errors.Is(err, impl.ErrDuplicateSignature) {
		t.Fatalf("the signature table must be restored, got %v", err)
	}
	feed(t, restored, "f1", 108, "/1.1", 1002)
	if got := mustStream(t, restored, "f1"); got != "GET HTTP/1.1" {
		t.Fatalf("continued stream = %q, want %q", got, "GET HTTP/1.1")
	}
}
