// Package task reassembles TCP flows from out of order segments and runs
// content signatures against the contiguous stream, keeping per flow alert
// de-duplication and restart safe snapshots.
package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Errors returned by the reassembler.
var (
	ErrBadConfig          = errors.New("reassembler config is invalid")
	ErrBadFlow            = errors.New("flow id is required")
	ErrBadSegment         = errors.New("segment is invalid")
	ErrInvalidSignature   = errors.New("signature is invalid")
	ErrDuplicateSignature = errors.New("signature id already exists")
	ErrFlowLimit          = errors.New("flow limit reached")
	ErrStreamTooLarge     = errors.New("stream cache limit reached")
	ErrUnknownFlow        = errors.New("flow is not tracked")
	ErrFlowClosed         = errors.New("flow is closed")
	ErrBadSnapshot        = errors.New("snapshot payload is invalid")
)

// Config bounds one reassembler. MaxStreamBytes and MaxSegments cap the cache
// of one flow, MaxFlows caps the tracked flows in total and FlowTTL is the idle
// age used by EvictIdle.
type Config struct {
	MaxStreamBytes int
	MaxSegments    int
	MaxFlows       int
	FlowTTL        int64
	Now            func() int64
}

// Signature is one content rule. Offset is the first position a match may
// start at and Depth, when positive, limits how far the start may travel.
type Signature struct {
	ID        int
	Name      string
	Pattern   []byte
	Offset    int
	Depth     int
	NoCase    bool
	AlertOnce bool
}

// Segment is one TCP segment delivered to the reassembler.
type Segment struct {
	FlowID string
	Seq    uint64
	Data   []byte
	At     int64
}

// Alert is one signature hit on a reassembled flow.
type Alert struct {
	FlowID string
	SigID  int
	Name   string
	Offset uint64
	At     int64
}

// SignatureReassembler owns the flow table and the signature table.
type SignatureReassembler struct {
	cfg Config
}

// NewSignatureReassembler validates the configuration and builds an empty
// reassembler.
func NewSignatureReassembler(cfg Config) (*SignatureReassembler, error) {
	return nil, errNotImplemented
}

// AddSignature registers one content signature.
func (r *SignatureReassembler) AddSignature(sig Signature) (int, error) {
	return 0, errNotImplemented
}

// Feed ingests one segment, returns the alerts of the newly assembled bytes
// and enforces the per flow cache limits.
func (r *SignatureReassembler) Feed(seg Segment) ([]Alert, error) {
	return nil, errNotImplemented
}

// Stream returns the contiguous bytes assembled for one flow.
func (r *SignatureReassembler) Stream(flowID string) ([]byte, error) {
	return nil, errNotImplemented
}

// Pending reports how many bytes are buffered beyond the contiguous prefix.
func (r *SignatureReassembler) Pending(flowID string) int {
	return 0
}

// CloseFlow marks one flow as closed. Closing twice is a no-op.
func (r *SignatureReassembler) CloseFlow(flowID string) error {
	return errNotImplemented
}

// AlertCount reports how many alerts one signature produced.
func (r *SignatureReassembler) AlertCount(sigID int) uint64 {
	return 0
}

// FlowCount reports how many flows are tracked.
func (r *SignatureReassembler) FlowCount() int {
	return 0
}

// OverlapConflicts reports how many overlapping bytes of one flow disagreed
// with the bytes received first.
func (r *SignatureReassembler) OverlapConflicts(flowID string) int {
	return 0
}

// EvictIdle drops the flows whose idle age reached FlowTTL and returns how many
// were dropped.
func (r *SignatureReassembler) EvictIdle(now int64) int {
	return 0
}

// Snapshot serialises the flows, the gap buffers, the alert de-duplication set
// and the signature table.
func (r *SignatureReassembler) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (r *SignatureReassembler) Restore(data []byte) error {
	return errNotImplemented
}
