// Package task inspects HTTP/1.1 requests under two hop parsing profiles
// and reports framing differences that enable request smuggling.
package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Verdict classes.
const (
	ClassNone       = "none"
	ClassCLTE       = "cl_te"
	ClassTECL       = "te_cl"
	ClassTETE       = "te_te"
	ClassCLConflict = "cl_conflict"
	ClassObsFold    = "obs_fold"
)

// Verdict actions.
const (
	ActionAllow  = "allow"
	ActionReject = "reject"
)

// Hop framing preferences.
const (
	PreferTE = "te"
	PreferCL = "cl"
)

// Errors returned by the analyzer.
var (
	ErrEmptyRequest       = errors.New("request is empty")
	ErrMalformedHead      = errors.New("request head is malformed")
	ErrBadVersion         = errors.New("only HTTP/1.1 is accepted")
	ErrHeaderTooLarge     = errors.New("request head exceeds the byte limit")
	ErrTooManyHeaders     = errors.New("header count limit reached")
	ErrObsFold            = errors.New("obsolete header folding is not accepted")
	ErrHeaderWhitespace   = errors.New("whitespace before the colon is not accepted")
	ErrAmbiguousFraming   = errors.New("conflicting content length values")
	ErrSmugglingDetected  = errors.New("front and back framing disagree")
	ErrBadFraming         = errors.New("transfer encoding is obfuscated")
	ErrChunkOverflow      = errors.New("chunk size exceeds the limit")
	ErrChunkCount         = errors.New("chunk count exceeds the limit")
	ErrBodyLengthMismatch = errors.New("body length does not match the framing")
	ErrBadSnapshot        = errors.New("snapshot payload is invalid")
)

// AnalyzerConfig holds the hop preferences and the hard limits.
type AnalyzerConfig struct {
	FrontPreference string
	BackPreference  string
	MaxHeaderBytes  int
	MaxHeaders      int
	MaxChunkBytes   int
	MaxChunks       int
	MaxAudit        int
}

// Header is one raw request header field.
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Request is the parsed request line, headers and remaining body octets.
type Request struct {
	Method  string   `json:"method"`
	Target  string   `json:"target"`
	Version string   `json:"version"`
	Headers []Header `json:"headers"`
	Body    []byte   `json:"body"`
}

// Verdict is the smuggling assessment of one request.
type Verdict struct {
	Class    string   `json:"class"`
	Action   string   `json:"action"`
	Reasons  []string `json:"reasons"`
	Evidence []int    `json:"evidence"`
}

// ClassCount is one counter row.
type ClassCount struct {
	Class string `json:"class"`
	Count int    `json:"count"`
}

// AuditEntry is one recorded analysis.
type AuditEntry struct {
	Seq    int64  `json:"seq"`
	Class  string `json:"class"`
	Action string `json:"action"`
}

// SmuggleFramingAnalyzer owns the counters and the audit ring.
type SmuggleFramingAnalyzer struct {
	cfg AnalyzerConfig
	seq int64
}

// NewSmuggleFramingAnalyzer builds an analyzer with the given hop profiles.
func NewSmuggleFramingAnalyzer(cfg AnalyzerConfig) *SmuggleFramingAnalyzer {
	return &SmuggleFramingAnalyzer{cfg: cfg}
}

// Parse splits a raw request into method, target, version, headers and body.
func (a *SmuggleFramingAnalyzer) Parse(raw []byte) (Request, error) {
	return Request{}, errNotImplemented
}

// BodyLength computes the framed body length for one parsed request.
func (a *SmuggleFramingAnalyzer) BodyLength(req Request) (int, error) {
	return 0, errNotImplemented
}

// Analyze parses and grades one raw request.
func (a *SmuggleFramingAnalyzer) Analyze(raw []byte) (Verdict, error) {
	return Verdict{}, errNotImplemented
}

// Canonicalize returns a forwarding safe copy of one request.
func (a *SmuggleFramingAnalyzer) Canonicalize(req Request) (Request, error) {
	return Request{}, errNotImplemented
}

// Counts returns the per class counters sorted by class name.
func (a *SmuggleFramingAnalyzer) Counts() []ClassCount {
	return nil
}

// Audit returns the retained audit entries in sequence order.
func (a *SmuggleFramingAnalyzer) Audit() []AuditEntry {
	return nil
}

// Reset clears counters, audit entries and the sequence.
func (a *SmuggleFramingAnalyzer) Reset() {
}

// Snapshot serialises counters and the audit ring.
func (a *SmuggleFramingAnalyzer) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces counters and the audit ring from a snapshot.
func (a *SmuggleFramingAnalyzer) Restore(data []byte) error {
	return errNotImplemented
}
