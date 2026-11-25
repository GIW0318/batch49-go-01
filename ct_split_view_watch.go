// Package task collects signed tree heads from several log and witness sources
// and detects two contradictory views of the same certificate transparency log.
package task

import (
	"errors"
	"time"
)

// errNotImplemented marks behaviour that still has to be supplied.
var errNotImplemented = errors.New("not implemented")

// Offer verdict statuses.
const (
	StatusAccepted    = "accepted"
	StatusPending     = "pending"
	StatusStale       = "stale"
	StatusSplitView   = "split_view"
	StatusQuarantined = "quarantined"
)

// Defaults applied to zero valued Config fields.
const (
	DefaultQuorum = 2
)

// Errors returned by the watch.
var (
	ErrBadSTH      = errors.New("signed tree head is incomplete")
	ErrUnknownLog  = errors.New("log id is unknown")
	ErrNoConsensus = errors.New("no quorum reached for this log")
	ErrQuarantined = errors.New("log is quarantined")
	ErrBadReason   = errors.New("quarantine reason is required")
	ErrUnknownHead = errors.New("tree head size is unknown")
	ErrBadRange    = errors.New("tree size range is invalid")
	ErrBadSnapshot = errors.New("snapshot payload is invalid")
)

// Clock supplies the current time for bookkeeping.
type Clock interface {
	Now() time.Time
}

// SignedTreeHead is one signed view submitted by a log or witness source.
type SignedTreeHead struct {
	LogID     string    `json:"log_id"`
	Source    string    `json:"source"`
	TreeSize  uint64    `json:"tree_size"`
	RootHash  []byte    `json:"root_hash"`
	Timestamp time.Time `json:"timestamp"`
	Signature []byte    `json:"signature"`
}

// TreeHead is the agreed view of one log.
type TreeHead struct {
	LogID     string    `json:"log_id"`
	TreeSize  uint64    `json:"tree_size"`
	RootHash  []byte    `json:"root_hash"`
	Sources   int       `json:"sources"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Verdict is the answer of one Offer call.
type Verdict struct {
	LogID    string `json:"log_id"`
	Status   string `json:"status"`
	Reason   string `json:"reason"`
	TreeSize uint64 `json:"tree_size"`
	Sources  int    `json:"sources"`
}

// Evidence is one contradictory or isolated head kept for later review.
type Evidence struct {
	LogID     string    `json:"log_id"`
	TreeSize  uint64    `json:"tree_size"`
	RootHash  []byte    `json:"root_hash"`
	Source    string    `json:"source"`
	Timestamp time.Time `json:"timestamp"`
	Reason    string    `json:"reason"`
}

// Config bounds the watch.
type Config struct {
	Quorum int   `json:"quorum"`
	Clock  Clock `json:"-"`
}

// Stats aggregates the counters of the watch.
type Stats struct {
	Offers      int64 `json:"offers"`
	Accepted    int64 `json:"accepted"`
	Pending     int64 `json:"pending"`
	Stale       int64 `json:"stale"`
	SplitViews  int64 `json:"split_views"`
	Quarantined int   `json:"quarantined"`
	Logs        int   `json:"logs"`
}

// SplitViewWatch owns the per log source table, the accepted head and the
// evidence set.
type SplitViewWatch struct {
	cfg Config
}

// NewSplitViewWatch builds a watch with the given configuration.
func NewSplitViewWatch(cfg Config) *SplitViewWatch {
	return &SplitViewWatch{cfg: cfg}
}

// Offer submits one signed head for its log.
func (w *SplitViewWatch) Offer(sth SignedTreeHead) (Verdict, error) {
	return Verdict{}, errNotImplemented
}

// VerifyConsistency checks a Merkle consistency proof between two accepted
// tree sizes.
func (w *SplitViewWatch) VerifyConsistency(from, to uint64, proof [][]byte) (bool, error) {
	return false, errNotImplemented
}

// Quarantine takes one log out of consensus and keeps the reason.
func (w *SplitViewWatch) Quarantine(logID, reason string) error {
	return errNotImplemented
}

// Consensus returns the agreed head of one log.
func (w *SplitViewWatch) Consensus(logID string) (TreeHead, error) {
	return TreeHead{}, errNotImplemented
}

// Quarantined reports whether one log is isolated.
func (w *SplitViewWatch) Quarantined(logID string) bool {
	return false
}

// Evidence returns the retained evidence of one log.
func (w *SplitViewWatch) Evidence(logID string) []Evidence {
	return nil
}

// Logs lists every log the watch has seen, in ascending order.
func (w *SplitViewWatch) Logs() []string {
	return nil
}

// Stats reads the aggregate counters.
func (w *SplitViewWatch) Stats() Stats {
	return Stats{}
}

// Snapshot serialises the accepted heads, evidence sets and counters.
func (w *SplitViewWatch) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (w *SplitViewWatch) Restore(data []byte) error {
	return errNotImplemented
}
