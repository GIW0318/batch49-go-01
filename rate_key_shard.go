// Package task rate limits high cardinality keys with a sliding window held in
// a bounded shard table, folding low cardinality sub keys into a parent key
// and evicting cold keys.
package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Decision reasons.
const (
	ReasonAllowed    = "allowed"
	ReasonExceeded   = "window_exceeded"
	ReasonCostTooBig = "cost_above_limit"
)

// Errors returned by the limiter.
var (
	ErrBadConfig   = errors.New("shard limiter config is invalid")
	ErrBlankKey    = errors.New("limit key is required")
	ErrBadCost     = errors.New("cost must be positive")
	ErrFoldCycle   = errors.New("fold would create a cycle")
	ErrBadSnapshot = errors.New("snapshot payload is invalid")
)

// LimiterConfig bounds one limiter. WindowMillis is the sliding window width,
// ShardBudget caps the live shard table and IdleEvictMillis is the cold key
// idle threshold.
type LimiterConfig struct {
	Limit           int
	WindowMillis    int64
	ShardBudget     int
	IdleEvictMillis int64
	FoldParent      string
	Now             func() int64
}

// Decision is the verdict of one Allow call.
type Decision struct {
	Key       string `json:"key"`
	Allowed   bool   `json:"allowed"`
	Limit     int    `json:"limit"`
	Used      int    `json:"used"`
	Remaining int    `json:"remaining"`
	Reason    string `json:"reason"`
}

// Usage is the observed state of one key.
type Usage struct {
	Key       string `json:"key"`
	Used      int    `json:"used"`
	Limit     int    `json:"limit"`
	Remaining int    `json:"remaining"`
	Shards    int    `json:"shards"`
	Folded    bool   `json:"folded"`
	Parent    string `json:"parent"`
}

// ShardLimiter owns the bounded shard table and the sliding windows.
type ShardLimiter struct {
	cfg LimiterConfig
}

// NewShardLimiter builds a limiter from the given configuration.
func NewShardLimiter(cfg LimiterConfig) *ShardLimiter {
	return &ShardLimiter{cfg: cfg}
}

// Allow charges cost against key inside the current sliding window.
func (s *ShardLimiter) Allow(key string, cost int) (Decision, error) {
	return Decision{}, errNotImplemented
}

// Fold routes the given sub keys to parent so they share the parent window.
func (s *ShardLimiter) Fold(keys []string, parent string) error {
	return errNotImplemented
}

// Usage reads the observed state of one key.
func (s *ShardLimiter) Usage(key string) Usage {
	return Usage{}
}

// ParentOf reports the parent key a folded key is routed to.
func (s *ShardLimiter) ParentOf(key string) (string, bool) {
	return "", false
}

// ShardCount reports how many shards are currently live.
func (s *ShardLimiter) ShardCount() int {
	return 0
}

// Keys lists the live keys ordered by name.
func (s *ShardLimiter) Keys() []string {
	return nil
}

// Sweep evicts the keys that stayed idle past the configured threshold and
// returns how many were evicted.
func (s *ShardLimiter) Sweep() int {
	return 0
}

// Reset clears the window of one key.
func (s *ShardLimiter) Reset(key string) {
}

// Limit reports the configured per window amount.
func (s *ShardLimiter) Limit() int {
	return 0
}

// Snapshot serialises the shard table, the windows and the fold mapping.
func (s *ShardLimiter) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole table with a snapshot, all or nothing.
func (s *ShardLimiter) Restore(data []byte) error {
	return errNotImplemented
}
