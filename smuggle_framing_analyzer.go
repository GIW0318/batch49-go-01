// Package task inspects HTTP/1.1 requests under two hop parsing profiles
// and reports framing differences that enable request smuggling.
package task

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/textproto"
	"sort"
	"strconv"
	"strings"
	"sync"
)

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

const http11Version = "HTTP/1.1"

const (
	defaultMaxHeaderBytes = 1 << 20
	defaultMaxHeaders     = 128
	defaultMaxChunkBytes  = 1 << 20
	defaultMaxChunks      = 4096
	defaultMaxAudit       = 100
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

	mu     sync.Mutex
	seq    int64
	counts map[string]int
	audit  []AuditEntry
}

type snapshotData struct {
	Seq    int64          `json:"seq"`
	Counts map[string]int `json:"counts"`
	Audit  []AuditEntry   `json:"audit"`
}

type transferInfo struct {
	present     bool
	occurrences int
	chunked     bool
}

// NewSmuggleFramingAnalyzer builds an analyzer with the given hop profiles.
func NewSmuggleFramingAnalyzer(cfg AnalyzerConfig) *SmuggleFramingAnalyzer {
	cfg = normalizeConfig(cfg)
	return &SmuggleFramingAnalyzer{
		cfg:    cfg,
		counts: make(map[string]int),
		audit:  make([]AuditEntry, 0),
	}
}

func normalizeConfig(cfg AnalyzerConfig) AnalyzerConfig {
	cfg.FrontPreference = normalizePreference(cfg.FrontPreference, PreferTE)
	cfg.BackPreference = normalizePreference(cfg.BackPreference, PreferCL)
	if cfg.MaxHeaderBytes <= 0 {
		cfg.MaxHeaderBytes = defaultMaxHeaderBytes
	}
	if cfg.MaxHeaders <= 0 {
		cfg.MaxHeaders = defaultMaxHeaders
	}
	if cfg.MaxChunkBytes <= 0 {
		cfg.MaxChunkBytes = defaultMaxChunkBytes
	}
	if cfg.MaxChunks <= 0 {
		cfg.MaxChunks = defaultMaxChunks
	}
	if cfg.MaxAudit <= 0 {
		cfg.MaxAudit = defaultMaxAudit
	}
	return cfg
}

func normalizePreference(value, fallback string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case PreferTE:
		return PreferTE
	case PreferCL:
		return PreferCL
	default:
		return fallback
	}
}

func (a *SmuggleFramingAnalyzer) config() AnalyzerConfig {
	if a == nil {
		return normalizeConfig(AnalyzerConfig{})
	}
	return normalizeConfig(a.cfg)
}

// Parse splits a raw request into method, target, version, headers and body.
// It never writes to raw and the returned body aliases raw's post-head bytes.
func (a *SmuggleFramingAnalyzer) Parse(raw []byte) (Request, error) {
	cfg := a.config()
	if len(raw) == 0 || len(bytes.TrimSpace(raw)) == 0 {
		return Request{}, ErrEmptyRequest
	}

	separator, bodyStart, separatorLen, found := findHeadSeparator(raw)
	if !found {
		if len(raw) > cfg.MaxHeaderBytes {
			return Request{}, ErrHeaderTooLarge
		}
		return Request{}, ErrMalformedHead
	}

	headerBytes := separator
	if separatorLen == len("\r\n\r\n") {
		headerBytes += len("\r\n")
	} else {
		headerBytes++
	}
	if headerBytes > cfg.MaxHeaderBytes {
		return Request{}, ErrHeaderTooLarge
	}

	headLines := splitHeadLines(raw[:separator])
	if len(headLines) == 0 {
		return Request{}, ErrMalformedHead
	}

	requestParts := strings.Fields(headLines[0])
	if len(requestParts) != 3 {
		return Request{}, ErrMalformedHead
	}
	if requestParts[2] != http11Version {
		return Request{}, ErrBadVersion
	}

	req := Request{
		Method:  requestParts[0],
		Target:  requestParts[1],
		Version: requestParts[2],
		Headers: make([]Header, 0, len(headLines)-1),
		Body:    raw[bodyStart:],
	}

	for i := 1; i < len(headLines); i++ {
		line := headLines[i]
		if line == "" {
			return Request{}, ErrMalformedHead
		}
		if line[0] == ' ' || line[0] == '\t' {
			return Request{}, ErrObsFold
		}
		if len(req.Headers) >= cfg.MaxHeaders {
			return Request{}, ErrTooManyHeaders
		}
		header, err := parseHeaderLine(line)
		if err != nil {
			return Request{}, err
		}
		req.Headers = append(req.Headers, header)
	}

	return req, nil
}

func findHeadSeparator(raw []byte) (separator int, bodyStart int, separatorLen int, found bool) {
	if index := bytes.Index(raw, []byte("\r\n\r\n")); index >= 0 {
		return index, index + 4, 4, true
	}
	if index := bytes.Index(raw, []byte("\n\n")); index >= 0 {
		return index, index + 2, 2, true
	}
	return 0, 0, 0, false
}

func splitHeadLines(head []byte) []string {
	parts := strings.Split(string(head), "\n")
	for i := range parts {
		parts[i] = strings.TrimSuffix(parts[i], "\r")
	}
	return parts
}

func parseHeaderLine(line string) (Header, error) {
	colon := strings.IndexByte(line, ':')
	if colon <= 0 {
		return Header{}, ErrMalformedHead
	}

	name := line[:colon]
	if strings.ContainsAny(name, " \t") {
		return Header{}, ErrHeaderWhitespace
	}
	if !isToken(name) {
		return Header{}, fmt.Errorf("%w: invalid header name", ErrMalformedHead)
	}

	return Header{
		Name:  name,
		Value: strings.Trim(line[colon+1:], " \t"),
	}, nil
}

// BodyLength computes the framed body length for one parsed request.
func (a *SmuggleFramingAnalyzer) BodyLength(req Request) (int, error) {
	cfg := a.config()

	contentLength, hasCL, err := contentLengthInfo(req.Headers)
	if err != nil {
		return 0, err
	}
	if hasCL {
		return int(contentLength), nil
	}

	te, err := transferEncodingInfo(req.Headers)
	if err != nil {
		return 0, err
	}
	if te.present {
		if !te.chunked {
			return 0, ErrBadFraming
		}
		return chunkedLength(req.Body, cfg)
	}

	return 0, nil
}

// Analyze parses and grades one raw request.
func (a *SmuggleFramingAnalyzer) Analyze(raw []byte) (Verdict, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	verdict, err := a.assess(raw)
	if verdict.Class == "" {
		verdict.Class = ClassNone
	}
	if verdict.Action == "" {
		if err != nil {
			verdict.Action = ActionReject
		} else {
			verdict.Action = ActionAllow
		}
	}

	if a.counts == nil {
		a.counts = make(map[string]int)
	}
	a.counts[verdict.Class]++
	a.seq++
	a.audit = append(a.audit, AuditEntry{
		Seq:    a.seq,
		Class:  verdict.Class,
		Action: verdict.Action,
	})

	maxAudit := a.config().MaxAudit
	if maxAudit > 0 && len(a.audit) > maxAudit {
		kept := make([]AuditEntry, maxAudit)
		copy(kept, a.audit[len(a.audit)-maxAudit:])
		a.audit = kept
	}

	return verdict, err
}

func (a *SmuggleFramingAnalyzer) assess(raw []byte) (Verdict, error) {
	cfg := a.config()
	req, err := a.Parse(raw)
	if err != nil {
		class := ClassNone
		if errors.Is(err, ErrObsFold) {
			class = ClassObsFold
		}
		return Verdict{
			Class:   class,
			Action:  ActionReject,
			Reasons: []string{err.Error()},
		}, err
	}

	contentLength, hasCL, err := contentLengthInfo(req.Headers)
	if err != nil {
		class := ClassNone
		if errors.Is(err, ErrAmbiguousFraming) {
			class = ClassCLConflict
		}
		return Verdict{
			Class:   class,
			Action:  ActionReject,
			Reasons: []string{err.Error()},
		}, err
	}

	te, err := transferEncodingInfo(req.Headers)
	if err != nil {
		return Verdict{
			Class:   ClassTETE,
			Action:  ActionReject,
			Reasons: []string{err.Error()},
		}, err
	}

	if hasCL && te.present {
		class := mixedFramingClass(cfg.FrontPreference, cfg.BackPreference)
		return Verdict{
			Class:    class,
			Action:   ActionReject,
			Reasons:  []string{ErrSmugglingDetected.Error()},
			Evidence: []int{int(contentLength)},
		}, ErrSmugglingDetected
	}

	if te.chunked {
		length, err := chunkedLength(req.Body, cfg)
		if err != nil {
			return Verdict{
				Class:   ClassNone,
				Action:  ActionReject,
				Reasons: []string{err.Error()},
			}, err
		}
		return Verdict{
			Class:    ClassNone,
			Action:   ActionAllow,
			Evidence: []int{length},
		}, nil
	}

	if hasCL {
		if int64(len(req.Body)) < contentLength {
			return Verdict{
				Class:    ClassNone,
				Action:   ActionReject,
				Reasons:  []string{ErrBodyLengthMismatch.Error()},
				Evidence: []int{int(contentLength), len(req.Body)},
			}, ErrBodyLengthMismatch
		}
		return Verdict{
			Class:    ClassNone,
			Action:   ActionAllow,
			Evidence: []int{int(contentLength)},
		}, nil
	}

	return Verdict{Class: ClassNone, Action: ActionAllow}, nil
}

func mixedFramingClass(front, back string) string {
	switch {
	case front == PreferTE && back == PreferCL:
		return ClassCLTE
	case front == PreferCL && back == PreferTE:
		return ClassTECL
	case front == PreferTE && back == PreferTE:
		return ClassTETE
	default:
		// Both profiles selecting CL still leaves a mixed CL/TE request.
		return ClassCLTE
	}
}

// Canonicalize returns a forwarding safe copy of one request.
func (a *SmuggleFramingAnalyzer) Canonicalize(req Request) (Request, error) {
	length, lengthErr := a.BodyLength(req)
	if lengthErr != nil && !errors.Is(lengthErr, ErrSmugglingDetected) {
		return Request{}, lengthErr
	}

	_, hasCL, clErr := contentLengthInfo(req.Headers)
	if clErr != nil {
		return Request{}, clErr
	}
	teInfo, _ := transferEncodingInfo(req.Headers)

	out := Request{
		Method:  req.Method,
		Target:  req.Target,
		Version: req.Version,
		Headers: make([]Header, 0, len(req.Headers)+1),
		Body:    append([]byte(nil), req.Body...),
	}

	connectionNames := connectionHeaderNames(req.Headers)
	hopByHop := map[string]struct{}{
		"connection":          {},
		"keep-alive":          {},
		"proxy-authenticate":  {},
		"proxy-authorization": {},
		"proxy-connection":    {},
		"te":                  {},
		"trailer":             {},
		"transfer-encoding":   {},
		"upgrade":             {},
	}

	clWritten := false
	for _, header := range req.Headers {
		canonicalName := textproto.CanonicalMIMEHeaderKey(header.Name)
		lowerName := strings.ToLower(canonicalName)

		if _, blocked := hopByHop[lowerName]; blocked {
			continue
		}
		if _, named := connectionNames[lowerName]; named {
			continue
		}

		if lowerName == "content-length" {
			if clWritten {
				continue
			}
			value := header.Value
			if lengthErr == nil {
				value = strconv.Itoa(length)
			}
			out.Headers = append(out.Headers, Header{
				Name:  canonicalName,
				Value: value,
			})
			clWritten = true
			continue
		}

		out.Headers = append(out.Headers, Header{
			Name:  canonicalName,
			Value: header.Value,
		})
	}

	if !clWritten && lengthErr == nil && (hasCL || teInfo.present) {
		out.Headers = append(out.Headers, Header{
			Name:  "Content-Length",
			Value: strconv.Itoa(length),
		})
	}

	return out, nil
}

func connectionHeaderNames(headers []Header) map[string]struct{} {
	names := make(map[string]struct{})
	for _, header := range headers {
		if !strings.EqualFold(header.Name, "Connection") {
			continue
		}
		for _, token := range strings.Split(header.Value, ",") {
			token = strings.TrimSpace(token)
			if token != "" {
				names[strings.ToLower(token)] = struct{}{}
			}
		}
	}
	return names
}

// Counts returns the per class counters sorted by class name.
func (a *SmuggleFramingAnalyzer) Counts() []ClassCount {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.counts == nil {
		return make([]ClassCount, 0)
	}
	classes := make([]string, 0, len(a.counts))
	for class, count := range a.counts {
		if count > 0 {
			classes = append(classes, class)
		}
	}
	sort.Strings(classes)

	out := make([]ClassCount, 0, len(classes))
	for _, class := range classes {
		out = append(out, ClassCount{Class: class, Count: a.counts[class]})
	}
	return out
}

// Audit returns the retained audit entries in sequence order.
func (a *SmuggleFramingAnalyzer) Audit() []AuditEntry {
	a.mu.Lock()
	defer a.mu.Unlock()

	out := make([]AuditEntry, len(a.audit))
	copy(out, a.audit)
	return out
}

// Reset clears counters, audit entries and the sequence.
func (a *SmuggleFramingAnalyzer) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.seq = 0
	a.counts = make(map[string]int)
	a.audit = make([]AuditEntry, 0)
}

// Snapshot serialises counters and the audit ring.
func (a *SmuggleFramingAnalyzer) Snapshot() ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	counts := make(map[string]int, len(a.counts))
	for class, count := range a.counts {
		counts[class] = count
	}
	audit := make([]AuditEntry, len(a.audit))
	copy(audit, a.audit)

	data, err := json.Marshal(snapshotData{
		Seq:    a.seq,
		Counts: counts,
		Audit:  audit,
	})
	if err != nil {
		return nil, fmt.Errorf("encode analyzer snapshot: %w", err)
	}
	return data, nil
}

// Restore replaces counters and the audit ring from a snapshot.
func (a *SmuggleFramingAnalyzer) Restore(data []byte) error {
	snapshot, err := decodeSnapshot(data)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	cfg := a.config()
	audit := append([]AuditEntry(nil), snapshot.Audit...)
	if cfg.MaxAudit > 0 && len(audit) > cfg.MaxAudit {
		audit = append([]AuditEntry(nil), audit[len(audit)-cfg.MaxAudit:]...)
	}

	counts := make(map[string]int, len(snapshot.Counts))
	for class, count := range snapshot.Counts {
		counts[class] = count
	}

	a.seq = snapshot.Seq
	a.counts = counts
	a.audit = audit
	return nil
}

func decodeSnapshot(data []byte) (snapshotData, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return snapshotData{}, errors.New("empty snapshot")
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	var snapshot snapshotData
	if err := decoder.Decode(&snapshot); err != nil {
		return snapshotData{}, err
	}

	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return snapshotData{}, errors.New("unexpected trailing JSON value")
		}
		return snapshotData{}, err
	}

	if snapshot.Seq < 0 {
		return snapshotData{}, errors.New("negative sequence")
	}
	for class, count := range snapshot.Counts {
		if class == "" || count < 0 {
			return snapshotData{}, errors.New("invalid counter")
		}
	}

	var previous int64
	for i, entry := range snapshot.Audit {
		if entry.Seq <= 0 || entry.Class == "" || entry.Action == "" {
			return snapshotData{}, errors.New("invalid audit entry")
		}
		if i > 0 && entry.Seq <= previous {
			return snapshotData{}, errors.New("audit sequence is not increasing")
		}
		previous = entry.Seq
	}
	if len(snapshot.Audit) > 0 && snapshot.Seq < snapshot.Audit[len(snapshot.Audit)-1].Seq {
		return snapshotData{}, errors.New("sequence is behind the audit ring")
	}

	return snapshot, nil
}

func headerValues(headers []Header, name string) []string {
	values := make([]string, 0)
	for _, header := range headers {
		if strings.EqualFold(header.Name, name) {
			values = append(values, header.Value)
		}
	}
	return values
}

func contentLengthInfo(headers []Header) (value int64, present bool, err error) {
	values := headerValues(headers, "Content-Length")
	if len(values) == 0 {
		return 0, false, nil
	}

	for index, rawValue := range values {
		parsed, parseErr := parseContentLengthValue(rawValue)
		if parseErr != nil {
			return 0, true, parseErr
		}
		if index == 0 {
			value = parsed
			continue
		}
		if parsed != value {
			return 0, true, fmt.Errorf("%w: %d and %d", ErrAmbiguousFraming, value, parsed)
		}
	}

	return value, true, nil
}

func parseContentLengthValue(rawValue string) (int64, error) {
	value := strings.Trim(rawValue, " \t")
	if value == "" {
		return 0, fmt.Errorf("%w: empty content-length", ErrMalformedHead)
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return 0, fmt.Errorf("%w: invalid content-length %q", ErrMalformedHead, rawValue)
		}
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: invalid content-length %q: %v", ErrMalformedHead, rawValue, err)
	}
	return parsed, nil
}

var knownTransferCodings = map[string]struct{}{
	"chunked":  {},
	"identity": {},
	"gzip":     {},
	"x-gzip":   {},
	"deflate":  {},
	"compress": {},
	"zstd":     {},
	"br":       {},
}

func transferEncodingInfo(headers []Header) (transferInfo, error) {
	values := headerValues(headers, "Transfer-Encoding")
	info := transferInfo{present: len(values) > 0, occurrences: len(values)}
	if !info.present {
		return info, nil
	}

	if info.occurrences > 1 {
		return info, fmt.Errorf("%w: multiple transfer-encoding headers", ErrSmugglingDetected)
	}

	lastCoding := ""
	parts := strings.Split(values[0], ",")
	for _, part := range parts {
		coding := strings.Trim(part, " \t")
		if semicolon := strings.IndexByte(coding, ';'); semicolon >= 0 {
			coding = coding[:semicolon]
		}
		coding = strings.Trim(coding, " \t")
		if coding == "" || !isToken(coding) {
			return info, fmt.Errorf("%w: invalid transfer coding %q", ErrBadFraming, values[0])
		}
		coding = strings.ToLower(coding)
		if _, known := knownTransferCodings[coding]; !known {
			return info, fmt.Errorf("%w: unknown transfer coding %q", ErrBadFraming, coding)
		}
		lastCoding = coding
	}

	info.chunked = lastCoding == "chunked"
	if !info.chunked {
		return info, fmt.Errorf("%w: final transfer coding is %q", ErrBadFraming, lastCoding)
	}
	return info, nil
}

func chunkedLength(body []byte, cfg AnalyzerConfig) (int, error) {
	total := 0
	dataChunks := 0
	pos := 0

	for {
		line, next, _ := readChunkLine(body, pos)
		sizePart := line
		if semicolon := bytes.IndexByte(line, ';'); semicolon >= 0 {
			if err := validateChunkExtensions(string(line[semicolon+1:])); err != nil {
				return 0, err
			}
			sizePart = line[:semicolon]
		}

		size, err := parseChunkSize(sizePart, cfg.MaxChunkBytes)
		if err != nil {
			return 0, err
		}

		if size == 0 {
			if err := validateChunkTerminator(body[next:]); err != nil {
				return 0, err
			}
			return total, nil
		}

		dataChunks++
		if dataChunks > cfg.MaxChunks {
			return 0, ErrChunkCount
		}
		if total > int(^uint(0)>>1)-size {
			return 0, fmt.Errorf("%w: chunk total overflows", ErrMalformedHead)
		}

		dataStart := next
		available := len(body) - dataStart
		if size > available {
			return 0, ErrBodyLengthMismatch
		}
		dataEnd := dataStart + size

		switch {
		case dataEnd+2 <= len(body) && body[dataEnd] == '\r' && body[dataEnd+1] == '\n':
			pos = dataEnd + 2
		case dataEnd+1 <= len(body) && body[dataEnd] == '\n':
			pos = dataEnd + 1
		case dataEnd >= len(body):
			return 0, ErrBodyLengthMismatch
		default:
			return 0, fmt.Errorf("%w: invalid chunk data terminator", ErrMalformedHead)
		}

		total += size
	}
}

func readChunkLine(body []byte, pos int) (line []byte, next int, hasLineEnd bool) {
	newline := bytes.IndexByte(body[pos:], '\n')
	if newline < 0 {
		line = bytes.TrimSuffix(body[pos:], []byte("\r"))
		return line, len(body), false
	}

	end := pos + newline
	line = body[pos:end]
	line = bytes.TrimSuffix(line, []byte("\r"))
	return line, end + 1, true
}

func parseChunkSize(sizeBytes []byte, maxChunkBytes int) (int, error) {
	if len(sizeBytes) == 0 {
		return 0, fmt.Errorf("%w: empty chunk size", ErrMalformedHead)
	}
	for _, char := range sizeBytes {
		if !isHexDigit(char) {
			return 0, fmt.Errorf("%w: invalid chunk size %q", ErrMalformedHead, string(sizeBytes))
		}
	}

	parsed, err := strconv.ParseUint(string(sizeBytes), 16, 64)
	if err != nil {
		if errors.Is(err, strconv.ErrRange) {
			return 0, ErrChunkOverflow
		}
		return 0, fmt.Errorf("%w: invalid chunk size: %v", ErrMalformedHead, err)
	}
	if parsed > uint64(maxChunkBytes) {
		return 0, ErrChunkOverflow
	}
	return int(parsed), nil
}

func isHexDigit(char byte) bool {
	return char >= '0' && char <= '9' ||
		char >= 'a' && char <= 'f' ||
		char >= 'A' && char <= 'F'
}

func validateChunkTerminator(rest []byte) error {
	if len(rest) >= 2 && rest[0] == '\r' && rest[1] == '\n' {
		return nil
	}
	if len(rest) >= 1 && rest[0] == '\n' {
		return nil
	}
	if bytes.Index(rest, []byte("\r\n\r\n")) >= 0 || bytes.Index(rest, []byte("\n\n")) >= 0 {
		return nil
	}
	return ErrBodyLengthMismatch
}

func validateChunkExtensions(extensions string) error {
	if extensions == "" {
		return fmt.Errorf("%w: empty chunk extension", ErrMalformedHead)
	}

	pos := 0
	for pos < len(extensions) {
		nameStart := pos
		for pos < len(extensions) && isTChar(extensions[pos]) {
			pos++
		}
		if pos == nameStart {
			return fmt.Errorf("%w: invalid chunk extension", ErrMalformedHead)
		}

		if pos < len(extensions) && extensions[pos] == '=' {
			pos++
			if pos < len(extensions) && extensions[pos] == '"' {
				var err error
				pos, err = scanQuotedExtensionValue(extensions, pos)
				if err != nil {
					return err
				}
			} else {
				valueStart := pos
				for pos < len(extensions) && isTChar(extensions[pos]) {
					pos++
				}
				if pos == valueStart {
					return fmt.Errorf("%w: invalid chunk extension value", ErrMalformedHead)
				}
			}
		}

		if pos == len(extensions) {
			return nil
		}
		if extensions[pos] != ';' {
			return fmt.Errorf("%w: invalid chunk extension character", ErrMalformedHead)
		}
		pos++
		if pos == len(extensions) {
			return fmt.Errorf("%w: empty chunk extension", ErrMalformedHead)
		}
	}
	return nil
}

func scanQuotedExtensionValue(value string, pos int) (int, error) {
	pos++ // opening quote
	for pos < len(value) {
		char := value[pos]
		if char == '\\' {
			pos++
			if pos >= len(value) || !validQuotedPairByte(value[pos]) {
				return pos, fmt.Errorf("%w: invalid quoted chunk extension", ErrMalformedHead)
			}
			pos++
			continue
		}
		if char == '"' {
			return pos + 1, nil
		}
		if !validQuotedTextByte(char) {
			return pos, fmt.Errorf("%w: illegal chunk extension character", ErrMalformedHead)
		}
		pos++
	}
	return pos, fmt.Errorf("%w: unterminated quoted chunk extension", ErrMalformedHead)
}

func validQuotedTextByte(char byte) bool {
	return char == '\t' || char == ' ' || char == 0x21 ||
		(char >= 0x23 && char <= 0x5b) ||
		(char >= 0x5d && char <= 0x7e) ||
		char >= 0x80
}

func validQuotedPairByte(char byte) bool {
	return char == '\t' || char == ' ' ||
		(char >= 0x20 && char <= 0x7e) || char >= 0x80
}

func isToken(value string) bool {
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		if !isTChar(value[i]) {
			return false
		}
	}
	return true
}

func isTChar(char byte) bool {
	switch {
	case char >= 'a' && char <= 'z':
		return true
	case char >= 'A' && char <= 'Z':
		return true
	case char >= '0' && char <= '9':
		return true
	}
	switch char {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
		return true
	default:
		return false
	}
}
