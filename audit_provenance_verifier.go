// Package task verifies that audit events pushed by several collection
// sources form one monotonic sequence per source and survive a receiver
// restart without losing their provenance.
package task

import (
	"errors"
	"time"
)

// errNotImplemented marks behaviour that still has to be implemented.
var errNotImplemented = errors.New("not implemented")

// Verdict is the outcome of verifying a single audit event.
type Verdict string

// Reason explains a verdict and is what the audit console groups by.
type Reason string

const (
	VerdictAccept            Verdict = "accept"
	VerdictDuplicate         Verdict = "duplicate"
	VerdictOutOfOrder        Verdict = "out_of_order"
	VerdictGapPending        Verdict = "gap_pending"
	VerdictGap               Verdict = "gap"
	VerdictSourceMismatch    Verdict = "source_mismatch"
	VerdictSignatureMismatch Verdict = "signature_mismatch"
	VerdictIsolated          Verdict = "isolated"
)

const (
	ReasonNone              Reason = ""
	ReasonDuplicateSeq      Reason = "duplicate_seq"
	ReasonDuplicateConflict Reason = "duplicate_conflict"
	ReasonBelowWatermark    Reason = "below_watermark"
	ReasonGapWithinWindow   Reason = "gap_within_window"
	ReasonGapBeyondWindow   Reason = "gap_beyond_window"
	ReasonUnknownSource     Reason = "unknown_source"
	ReasonOwnerMismatch     Reason = "owner_mismatch"
	ReasonSignatureMismatch Reason = "signature_mismatch"
	ReasonCreditExhausted   Reason = "credit_exhausted"
	ReasonManualIsolation   Reason = "manual_isolation"
)

// Defaults applied by the constructor when a configuration field is left
// unset.
const (
	DefaultGapWindow uint64 = 3
	DefaultCredit    int    = 5
)

var (
	ErrBadSource      = errors.New("source id and boot id are required")
	ErrSourceExists   = errors.New("source already registered")
	ErrUnknownSource  = errors.New("source is not registered")
	ErrSourceIsolated = errors.New("source is isolated")
	ErrBadEvent       = errors.New("event id, digest and source are required")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// VerifierConfig carries the tuning knobs of one receiver.
type VerifierConfig struct {
	GapWindow        uint64
	MaxCredit        int
	MaxTimestampSkew time.Duration
	MaxGapsPerSource int
}

// Source is one registered collector together with the boot it belongs to.
type Source struct {
	ID     string
	BootID string
	Key    string
}

// Event is one audit record pushed by a source.
type Event struct {
	ID     string
	Source string
	BootID string
	Seq    uint64
	At     time.Time
	Signer string
	Digest string
}

// Gap is a run of sequence numbers that never arrived.
type Gap struct {
	Source string
	From   uint64
	To     uint64
}

// AuditProvenanceVerifier keeps one high watermark, one credit balance and one
// gap ledger per registered source.
type AuditProvenanceVerifier struct {
	config VerifierConfig
}

// NewAuditProvenanceVerifier returns a receiver with no source registered.
func NewAuditProvenanceVerifier(cfg VerifierConfig) *AuditProvenanceVerifier {
	return &AuditProvenanceVerifier{config: cfg}
}

// Register adds a source that is allowed to report events.
func (v *AuditProvenanceVerifier) Register(src Source) error {
	return errNotImplemented
}

// Verify checks one event against the state of its source and advances the
// source watermark when the event is fresh.
func (v *AuditProvenanceVerifier) Verify(ev Event) (Verdict, Reason, error) {
	return "", "", errNotImplemented
}

// Watermark reports the highest sequence number accepted for a source.
func (v *AuditProvenanceVerifier) Watermark(src string) (uint64, error) {
	return 0, errNotImplemented
}

// Gaps returns the recorded gaps of a source ordered by their first number.
func (v *AuditProvenanceVerifier) Gaps(src string) []Gap {
	return nil
}

// Isolate puts a source into quarantine with a human readable reason.
func (v *AuditProvenanceVerifier) Isolate(src string, reason string) error {
	return errNotImplemented
}

// Isolated reports whether a source is quarantined and why.
func (v *AuditProvenanceVerifier) Isolated(src string) (bool, string) {
	return false, ""
}

// Credit reports how many trust points a source still has.
func (v *AuditProvenanceVerifier) Credit(src string) (int, error) {
	return 0, errNotImplemented
}

// Owner reports which source registered an event identifier first.
func (v *AuditProvenanceVerifier) Owner(eventID string) (string, bool) {
	return "", false
}

// Snapshot serialises watermarks, credits, gaps and isolation state.
func (v *AuditProvenanceVerifier) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore rebuilds the receiver from a snapshot produced by Snapshot.
func (v *AuditProvenanceVerifier) Restore(payload []byte) error {
	return errNotImplemented
}
