// Package task keeps vulnerability findings identical across scan rounds:
// drift tolerant identity resolution, a fix/reopen state machine and
// reversible merging.
package task

import "errors"

// errNotImplemented is returned by behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Finding states.
const (
	StateNew      = "new"
	StateOpen     = "open"
	StateFixed    = "fixed"
	StateReopened = "reopened"
	StateIgnored  = "ignored"
)

// Event kinds appended to the audit log.
const (
	KindNew      = "new"
	KindSeen     = "seen"
	KindFixed    = "fixed"
	KindReopened = "reopened"
	KindMerged   = "merged"
	KindSplit    = "split"
	KindIgnored  = "ignored"
)

// Errors returned by the ledger.
var (
	ErrBlankScanID = errors.New("scan id is required")
	ErrBadRaw      = errors.New("raw finding is invalid")
	ErrNoMatch     = errors.New("no finding matches the raw finding")
	ErrNotFound    = errors.New("finding not found")
	ErrBadSplit    = errors.New("split request is invalid")
	ErrBadSnapshot = errors.New("snapshot payload is invalid")
)

// RawFinding is one finding as reported by a scanner.
type RawFinding struct {
	Source      string
	RuleID      string
	Path        string
	Line        int
	Fingerprint string
	Message     string
	Severity    string
}

// Finding is one identity that survives across scans.
type Finding struct {
	ID          string
	RuleID      string
	Path        string
	Line        int
	Fingerprint string
	State       string
	Origins     []string
	Seen        int
	ReopenCount int
	Seq         int64
	FirstScan   string
	LastScan    string
}

// Event is one state transition of one finding.
type Event struct {
	ScanID    string
	FindingID string
	Kind      string
	From      string
	To        string
	Source    string
	Seq       int64
}

// Config tunes identity resolution.
type Config struct {
	LineDrift        int
	FingerprintDrift int
}

// Stats is the counter block of the ledger.
type Stats struct {
	New     int
	Open    int
	Fixed   int
	Ignored int
}

// FindingIdentityLedger owns findings, their sources and the event log.
type FindingIdentityLedger struct {
	cfg Config
}

// NewFindingIdentityLedger builds an empty ledger.
func NewFindingIdentityLedger(cfg Config) *FindingIdentityLedger {
	return &FindingIdentityLedger{cfg: cfg}
}

// Identity joins rule, path, line and semantic fingerprint into the raw key.
func (l *FindingIdentityLedger) Identity(raw RawFinding) string {
	return ""
}

// Resolve maps a raw finding onto a stored finding without mutating state.
func (l *FindingIdentityLedger) Resolve(raw RawFinding) (Finding, error) {
	return Finding{}, errNotImplemented
}

// Ingest applies one whole scan round and returns the events it produced.
func (l *FindingIdentityLedger) Ingest(scanID string, raw []RawFinding) ([]Event, error) {
	return nil, errNotImplemented
}

// Split peels a subset of sources off one finding.
func (l *FindingIdentityLedger) Split(findingID string, keep []string) error {
	return errNotImplemented
}

// Ignore silences one finding for the following rounds.
func (l *FindingIdentityLedger) Ignore(findingID string) error {
	return errNotImplemented
}

// Get reads one finding.
func (l *FindingIdentityLedger) Get(id string) (Finding, bool) {
	return Finding{}, false
}

// Findings lists every finding ordered by id.
func (l *FindingIdentityLedger) Findings() []Finding {
	return nil
}

// Events lists the audit log in append order.
func (l *FindingIdentityLedger) Events() []Event {
	return nil
}

// Stats reads the counter block.
func (l *FindingIdentityLedger) Stats() Stats {
	return Stats{}
}

// Rounds reports how many scan rounds the ledger has applied.
func (l *FindingIdentityLedger) Rounds() int {
	return 0
}

// Snapshot serialises findings, origins, events and the round counter.
func (l *FindingIdentityLedger) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole ledger state with a snapshot, all or nothing.
func (l *FindingIdentityLedger) Restore(data []byte) error {
	return errNotImplemented
}
