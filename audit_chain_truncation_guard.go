// Package task maintains per stream audit chains, exchanges signed head
// anchors between nodes and reports truncation and forked history.
package task

import "errors"

// errNotImplemented marks behaviour that is still missing.
var errNotImplemented = errors.New("not implemented")

// Verdict status values.
const (
	StatusIntact     = "intact"
	StatusTruncated  = "truncated"
	StatusFork       = "fork"
	StatusUnanchored = "unanchored"
)

// Gap kinds.
const (
	GapTailTruncation = "tail_truncation"
)

// Signer signs an anchor for one stream position. KeyID identifies the key so
// that retired keys can still be recognised.
type Signer interface {
	KeyID() string
	Sign(stream string, seq uint64, digest string) string
}

// Record is one audit entry in a stream chain.
type Record struct {
	Stream  string `json:"stream"`
	Seq     uint64 `json:"seq"`
	Payload string `json:"payload"`
	Digest  string `json:"digest"`
}

// Anchor is a signed claim about the digest at one stream position.
type Anchor struct {
	Stream    string `json:"stream"`
	Seq       uint64 `json:"seq"`
	Digest    string `json:"digest"`
	KeyID     string `json:"key_id"`
	Signature string `json:"signature"`
}

// Gap is a missing sequence range.
type Gap struct {
	From uint64 `json:"from"`
	To   uint64 `json:"to"`
	Kind string `json:"kind"`
}

// Fork is one sequence position with conflicting digests.
type Fork struct {
	Seq     uint64   `json:"seq"`
	Digests []string `json:"digests"`
}

// Verdict is the result of comparing the local chain with trusted anchors.
type Verdict struct {
	Stream      string `json:"stream"`
	Status      string `json:"status"`
	Head        uint64 `json:"head"`
	TrustedHead uint64 `json:"trusted_head"`
	Gaps        []Gap  `json:"gaps"`
	Forks       []Fork `json:"forks"`
}

// Limits bounds the guard tables.
type Limits struct {
	MaxStreams int
	MaxRecords int
	MaxAnchors int
}

// Errors returned by the guard.
var (
	ErrBlankStream      = errors.New("stream id is required")
	ErrDuplicateStream  = errors.New("stream already registered")
	ErrUnknownStream    = errors.New("unknown stream")
	ErrNilSigner        = errors.New("signer is required")
	ErrEmptyPayload     = errors.New("audit payload is required")
	ErrNoRecords        = errors.New("stream has no records to anchor")
	ErrAnchorInvalid    = errors.New("anchor fields are incomplete")
	ErrSignatureInvalid = errors.New("anchor signature does not verify")
	ErrUnknownKey       = errors.New("signing key is not trusted")
	ErrKeyRetired       = errors.New("signing key is retired beyond this sequence")
	ErrLimitExceeded    = errors.New("limit exceeded")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

// AuditChainTruncationGuard owns stream chains, the trusted key table and the
// anchors exchanged with peer nodes.
type AuditChainTruncationGuard struct {
	limits Limits
	signer Signer
}

// NewAuditChainTruncationGuard builds a guard with the given caps and the
// initial signing key.
func NewAuditChainTruncationGuard(limits Limits, signer Signer) *AuditChainTruncationGuard {
	return &AuditChainTruncationGuard{limits: limits, signer: signer}
}

// RegisterStream adds a stream that will be written by this node.
func (g *AuditChainTruncationGuard) RegisterStream(id string) error {
	return errNotImplemented
}

// Append writes one audit record and returns the sealed entry.
func (g *AuditChainTruncationGuard) Append(stream, payload string) (Record, error) {
	return Record{}, errNotImplemented
}

// Records returns the local chain of a stream in sequence order.
func (g *AuditChainTruncationGuard) Records(stream string) ([]Record, error) {
	return nil, errNotImplemented
}

// Head returns the newest local record.
func (g *AuditChainTruncationGuard) Head(stream string) (Record, bool) {
	return Record{}, false
}

// Anchor signs the current head and records the anchor locally.
func (g *AuditChainTruncationGuard) Anchor(stream string) (Anchor, error) {
	return Anchor{}, errNotImplemented
}

// IngestAnchor validates and stores an anchor received from a peer.
func (g *AuditChainTruncationGuard) IngestAnchor(a Anchor) error {
	return errNotImplemented
}

// Verify compares the local chain with every trusted anchor.
func (g *AuditChainTruncationGuard) Verify(stream string) (Verdict, error) {
	return Verdict{}, errNotImplemented
}

// Gaps returns the missing sequence ranges of a stream.
func (g *AuditChainTruncationGuard) Gaps(stream string) ([]Gap, error) {
	return nil, errNotImplemented
}

// RotateKey makes next the active signer and keeps the previous keys trusted.
func (g *AuditChainTruncationGuard) RotateKey(next Signer) error {
	return errNotImplemented
}

// RetireKey stops accepting anchors signed by keyID beyond afterSeq.
func (g *AuditChainTruncationGuard) RetireKey(keyID string, afterSeq uint64) error {
	return errNotImplemented
}

// Snapshot serialises streams, records, anchors and key retirement state.
func (g *AuditChainTruncationGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state from a snapshot.
func (g *AuditChainTruncationGuard) Restore(data []byte) error {
	return errNotImplemented
}
