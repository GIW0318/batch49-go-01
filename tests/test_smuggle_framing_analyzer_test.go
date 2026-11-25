package task

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	impl "gsb/batch49-go-fix-a-05"
)

func analyzerConfig() impl.AnalyzerConfig {
	return impl.AnalyzerConfig{
		FrontPreference: impl.PreferTE,
		BackPreference:  impl.PreferCL,
		MaxHeaderBytes:  4096,
		MaxHeaders:      20,
		MaxChunkBytes:   1024,
		MaxChunks:       5,
		MaxAudit:        10,
	}
}

func rawWith(headers ...string) []byte {
	var b strings.Builder
	b.WriteString("POST /submit HTTP/1.1\r\n")
	b.WriteString("Host: api.example.com\r\n")
	for _, h := range headers {
		b.WriteString(h)
		b.WriteString("\r\n")
	}
	b.WriteString("\r\n")
	return []byte(b.String())
}

func classCount(counts []impl.ClassCount, class string) int {
	for _, c := range counts {
		if c.Class == class {
			return c.Count
		}
	}
	return 0
}

func TestParseSplitsRequest(t *testing.T) {
	a := impl.NewSmuggleFramingAnalyzer(analyzerConfig())
	raw := append(rawWith("Content-Length: 5"), []byte("hello")...)
	req, err := a.Parse(raw)
	if err != nil {
		t.Fatalf("parse must succeed, got %v", err)
	}
	if req.Method != "POST" || req.Target != "/submit" || req.Version != "HTTP/1.1" {
		t.Fatalf("request line = %s %s %s", req.Method, req.Target, req.Version)
	}
	if len(req.Headers) != 2 {
		t.Fatalf("header count = %d, want 2", len(req.Headers))
	}
	if req.Headers[0].Name != "Host" || req.Headers[0].Value != "api.example.com" {
		t.Fatalf("first header = %+v", req.Headers[0])
	}
	if req.Headers[1].Name != "Content-Length" || req.Headers[1].Value != "5" {
		t.Fatalf("second header = %+v", req.Headers[1])
	}
	if string(req.Body) != "hello" {
		t.Fatalf("body = %q, want hello", string(req.Body))
	}
}

func TestParseRejectsMalformedLineAndVersion(t *testing.T) {
	a := impl.NewSmuggleFramingAnalyzer(analyzerConfig())
	if _, err := a.Parse([]byte("BOGUS\r\n\r\n")); !errors.Is(err, impl.ErrMalformedHead) {
		t.Fatalf("request line without three parts must be rejected, got %v", err)
	}
	if _, err := a.Parse([]byte("POST /x HTTP/2.0\r\nHost: h\r\n\r\n")); !errors.Is(err, impl.ErrBadVersion) {
		t.Fatalf("HTTP/2.0 request line must be rejected, got %v", err)
	}
	if _, err := a.Parse(nil); !errors.Is(err, impl.ErrEmptyRequest) {
		t.Fatalf("empty request must be rejected, got %v", err)
	}
}

func TestParseEnforcesHeadCaps(t *testing.T) {
	cfg := analyzerConfig()
	cfg.MaxHeaderBytes = 40
	a := impl.NewSmuggleFramingAnalyzer(cfg)
	if _, err := a.Parse(rawWith("Content-Length: 5")); !errors.Is(err, impl.ErrHeaderTooLarge) {
		t.Fatalf("oversized head must be rejected, got %v", err)
	}
	cfg = analyzerConfig()
	cfg.MaxHeaders = 2
	b := impl.NewSmuggleFramingAnalyzer(cfg)
	if _, err := b.Parse(rawWith("X-A: 1", "X-B: 2")); !errors.Is(err, impl.ErrTooManyHeaders) {
		t.Fatalf("header count above the cap must be rejected, got %v", err)
	}
}

func TestParseRejectsObsoleteFolding(t *testing.T) {
	a := impl.NewSmuggleFramingAnalyzer(analyzerConfig())
	raw := []byte("POST /submit HTTP/1.1\r\nHost: h\r\nX-Long: a\r\n b\r\n\r\n")
	if _, err := a.Parse(raw); !errors.Is(err, impl.ErrObsFold) {
		t.Fatalf("obs-fold continuation must be rejected, got %v", err)
	}
}

func TestParseRejectsWhitespaceBeforeColon(t *testing.T) {
	a := impl.NewSmuggleFramingAnalyzer(analyzerConfig())
	raw := []byte("POST /submit HTTP/1.1\r\nHost : api.example.com\r\n\r\n")
	if _, err := a.Parse(raw); !errors.Is(err, impl.ErrHeaderWhitespace) {
		t.Fatalf("whitespace before the colon must be rejected, got %v", err)
	}
}

func TestAnalyzeRejectsConflictingContentLength(t *testing.T) {
	a := impl.NewSmuggleFramingAnalyzer(analyzerConfig())
	raw := rawWith("Content-Length: 5", "Content-Length: 6")
	verdict, err := a.Analyze(raw)
	if !errors.Is(err, impl.ErrAmbiguousFraming) {
		t.Fatalf("conflicting content length must be rejected, got %v", err)
	}
	if verdict.Class != impl.ClassCLConflict || verdict.Action != impl.ActionReject {
		t.Fatalf("verdict = %+v, want class cl_conflict and a reject", verdict)
	}
}

func TestAnalyzeDetectsCLTE(t *testing.T) {
	a := impl.NewSmuggleFramingAnalyzer(analyzerConfig())
	raw := append(rawWith("Content-Length: 5", "Transfer-Encoding: chunked"), []byte("0\r\n\r\n")...)
	verdict, err := a.Analyze(raw)
	if !errors.Is(err, impl.ErrSmugglingDetected) {
		t.Fatalf("cl.te request must be rejected, got %v", err)
	}
	if verdict.Class != impl.ClassCLTE || verdict.Action != impl.ActionReject {
		t.Fatalf("verdict = %+v, want class cl_te and a reject", verdict)
	}
	counts := a.Counts()
	if len(counts) != 1 || counts[0].Class != impl.ClassCLTE || counts[0].Count != 1 {
		t.Fatalf("counts = %+v, want a single cl_te row", counts)
	}
	audit := a.Audit()
	if len(audit) != 1 || audit[0].Class != impl.ClassCLTE || audit[0].Seq != 1 {
		t.Fatalf("audit = %+v, want one cl_te entry with sequence 1", audit)
	}
}

func TestAnalyzeDetectsTECL(t *testing.T) {
	cfg := analyzerConfig()
	cfg.FrontPreference = impl.PreferCL
	cfg.BackPreference = impl.PreferTE
	a := impl.NewSmuggleFramingAnalyzer(cfg)
	raw := append(rawWith("Content-Length: 5", "Transfer-Encoding: chunked"), []byte("0\r\n\r\n")...)
	verdict, err := a.Analyze(raw)
	if !errors.Is(err, impl.ErrSmugglingDetected) {
		t.Fatalf("te.cl request must be rejected, got %v", err)
	}
	if verdict.Class != impl.ClassTECL {
		t.Fatalf("verdict class = %q, want %q", verdict.Class, impl.ClassTECL)
	}
}

func TestAnalyzeDetectsTETE(t *testing.T) {
	cfg := analyzerConfig()
	cfg.BackPreference = impl.PreferTE
	a := impl.NewSmuggleFramingAnalyzer(cfg)
	raw := append(rawWith("Transfer-Encoding: chunked", "Transfer-Encoding: identity"), []byte("0\r\n\r\n")...)
	verdict, err := a.Analyze(raw)
	if !errors.Is(err, impl.ErrSmugglingDetected) {
		t.Fatalf("te.te request must be rejected, got %v", err)
	}
	if verdict.Class != impl.ClassTETE {
		t.Fatalf("verdict class = %q, want %q", verdict.Class, impl.ClassTETE)
	}
}

func TestChunkedBodyWithExtensionsIsAccepted(t *testing.T) {
	cfg := analyzerConfig()
	cfg.BackPreference = impl.PreferTE
	a := impl.NewSmuggleFramingAnalyzer(cfg)
	raw := append(rawWith("Transfer-Encoding: chunked"), []byte("5;ext=1\r\nhello\r\n0\r\n\r\n")...)
	verdict, err := a.Analyze(raw)
	if err != nil {
		t.Fatalf("chunked request with extensions must be accepted, got %v", err)
	}
	if verdict.Class != impl.ClassNone || verdict.Action != impl.ActionAllow {
		t.Fatalf("verdict = %+v, want a clean allow", verdict)
	}
	req, err := a.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	n, err := a.BodyLength(req)
	if err != nil {
		t.Fatalf("body length: %v", err)
	}
	if n != 5 {
		t.Fatalf("chunked body length = %d, want 5", n)
	}
}

func TestChunkSizeOverflowIsRejected(t *testing.T) {
	cfg := analyzerConfig()
	cfg.BackPreference = impl.PreferTE
	a := impl.NewSmuggleFramingAnalyzer(cfg)
	raw := append(rawWith("Transfer-Encoding: chunked"), []byte("500\r\n")...)
	if _, err := a.Analyze(raw); !errors.Is(err, impl.ErrChunkOverflow) {
		t.Fatalf("chunk larger than the limit must be rejected, got %v", err)
	}
}

func TestChunkCountLimitIsEnforced(t *testing.T) {
	cfg := analyzerConfig()
	cfg.BackPreference = impl.PreferTE
	cfg.MaxChunks = 2
	a := impl.NewSmuggleFramingAnalyzer(cfg)
	body := "1\r\na\r\n1\r\nb\r\n1\r\nc\r\n0\r\n\r\n"
	raw := append(rawWith("Transfer-Encoding: chunked"), []byte(body)...)
	if _, err := a.Analyze(raw); !errors.Is(err, impl.ErrChunkCount) {
		t.Fatalf("chunk count above the limit must be rejected, got %v", err)
	}
}

func TestBodyLengthMismatchIsRejected(t *testing.T) {
	a := impl.NewSmuggleFramingAnalyzer(analyzerConfig())
	raw := append(rawWith("Content-Length: 9"), []byte("hello")...)
	if _, err := a.Analyze(raw); !errors.Is(err, impl.ErrBodyLengthMismatch) {
		t.Fatalf("body shorter than content length must be rejected, got %v", err)
	}
}

func TestCanonicalizeNormalizesAndStripsHopByHop(t *testing.T) {
	a := impl.NewSmuggleFramingAnalyzer(analyzerConfig())
	raw := append(rawWith(
		"content-length: 5",
		"content-length: 5",
		"X-Custom: keep",
		"Proxy-Connection: keep-alive",
		"Transfer-Encoding: chunked",
	), []byte("hello")...)
	req, err := a.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out, err := a.Canonicalize(req)
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	if out.Method != "POST" || out.Target != "/submit" || out.Version != "HTTP/1.1" {
		t.Fatalf("request line changed: %s %s %s", out.Method, out.Target, out.Version)
	}
	if string(out.Body) != "hello" {
		t.Fatalf("body = %q, want hello", string(out.Body))
	}
	var lengths, custom, proxy, te int
	for _, h := range out.Headers {
		switch h.Name {
		case "Content-Length":
			lengths++
			if h.Value != "5" {
				t.Fatalf("content length = %q, want 5", h.Value)
			}
		case "X-Custom":
			custom++
		case "Proxy-Connection":
			proxy++
		case "Transfer-Encoding":
			te++
		}
	}
	if lengths != 1 {
		t.Fatalf("content-length occurrences = %d, want exactly one", lengths)
	}
	if custom != 1 {
		t.Fatalf("end to end header was dropped")
	}
	if proxy != 0 || te != 0 {
		t.Fatalf("hop by hop headers survived: proxy=%d te=%d", proxy, te)
	}
}

func TestSnapshotRestoreKeepsCountersAndAudit(t *testing.T) {
	a := impl.NewSmuggleFramingAnalyzer(analyzerConfig())
	if _, err := a.Analyze(append(rawWith("Content-Length: 5", "Transfer-Encoding: chunked"), []byte("0\r\n\r\n")...)); err == nil {
		t.Fatalf("first analysis must be rejected as cl.te")
	}
	data, err := a.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := impl.NewSmuggleFramingAnalyzer(analyzerConfig())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !reflect.DeepEqual(restored.Counts(), a.Counts()) {
		t.Fatalf("restored counts = %+v, want %+v", restored.Counts(), a.Counts())
	}
	if !reflect.DeepEqual(restored.Audit(), a.Audit()) {
		t.Fatalf("restored audit = %+v, want %+v", restored.Audit(), a.Audit())
	}
	if _, err := restored.Analyze(append(rawWith("Content-Length: 5", "Transfer-Encoding: chunked"), []byte("0\r\n\r\n")...)); err == nil {
		t.Fatalf("restored analyzer must still detect cl.te")
	}
	audit := restored.Audit()
	if len(audit) != 2 {
		t.Fatalf("audit length after restore and one analysis = %d, want 2", len(audit))
	}
	if audit[1].Seq <= audit[0].Seq {
		t.Fatalf("audit sequence did not continue after restore: %+v", audit)
	}
	before := restored.Counts()
	if err := restored.Restore([]byte("nope")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must report ErrBadSnapshot, got %v", err)
	}
	if !reflect.DeepEqual(restored.Counts(), before) {
		t.Fatalf("failed restore mutated the counters")
	}
}
