// Package task implements GCRA smoothing with burst tolerance, bounded waiting
// and idle key compaction.
package task

import (
	"context"
	"errors"
)

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Decision reason codes.
const (
	ReasonAllowed       = "allowed"
	ReasonRateLimited   = "rate_limited"
	ReasonBurstExceeded = "burst_exceeded"
)

// Errors returned by the throttle.
var (
	ErrBlankKey      = errors.New("key is required")
	ErrBadUnits      = errors.New("units must be positive")
	ErrBurstExceeded = errors.New("units exceed the burst tolerance")
	ErrWaitTooLong   = errors.New("wait exceeds the configured maximum")
	ErrBadSnapshot   = errors.New("snapshot payload is invalid")
)

// GcraConfig bounds the rate, burst and waiting policy.
type GcraConfig struct {
	EmissionMs int64
	BurstUnits int
	MaxWaitMs  int64
	IdleTtlMs  int64
}

// Decision is the result of one immediate take.
type Decision struct {
	Allowed        bool
	WaitMs         int64
	RemainingUnits int
	TatMs          int64
	Reason         string
}

// Usage is the exported view of one key.
type Usage struct {
	RemainingUnits int
	TatMs          int64
	LastAt         int64
	Allowed        int
	Denied         int
}

// GcraThrottle owns the per key virtual scheduling state and the clock.
type GcraThrottle struct {
}

// NewGcraThrottle builds an empty throttle with the given policy.
func NewGcraThrottle(cfg GcraConfig) *GcraThrottle {
	return &GcraThrottle{}
}

// Take decides one request immediately without waiting.
func (t *GcraThrottle) Take(key string, units int) (Decision, error) {
	return Decision{}, errNotImplemented
}

// TakeWait decides one request and waits until the key allows it, bounded by
// MaxWaitMs and the context.
func (t *GcraThrottle) TakeWait(ctx context.Context, key string, units int) error {
	return errNotImplemented
}

// Reset drops the state of one key.
func (t *GcraThrottle) Reset(key string) error {
	return errNotImplemented
}

// Usage reads the state and counters of one key.
func (t *GcraThrottle) Usage(key string) Usage {
	return Usage{}
}

// Keys lists the keys holding state, in ascending order.
func (t *GcraThrottle) Keys() []string {
	return nil
}

// Prune compacts keys idle beyond the configured ttl and returns how many were
// dropped.
func (t *GcraThrottle) Prune() int {
	return 0
}

// Advance moves the logical clock forward by the given milliseconds.
func (t *GcraThrottle) Advance(ms int64) {
}

// Clock reports the current logical time in milliseconds.
func (t *GcraThrottle) Clock() int64 {
	return 0
}

// Snapshot serialises the clock and every key state.
func (t *GcraThrottle) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole throttle state from a snapshot.
func (t *GcraThrottle) Restore(data []byte) error {
	return errNotImplemented
}
