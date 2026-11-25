// Package task turns raw cgroup throttle and memory event counters into
// interval deltas and keeps per tenant usage windows.
package task

import (
	"errors"
	"time"
)

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Sample is one read of the monotonic counters of a cgroup.
type Sample struct {
	NrThrottled   uint64 `json:"nr_throttled"`
	ThrottledUsec uint64 `json:"throttled_usec"`
	HighEvents    uint64 `json:"high_events"`
	MaxEvents     uint64 `json:"max_events"`
	OOMEvents     uint64 `json:"oom_events"`
}

// Delta is the increase between two consecutive samples.
type Delta struct {
	Throttled     uint64 `json:"throttled"`
	ThrottledUsec uint64 `json:"throttled_usec"`
	High          uint64 `json:"high"`
	Max           uint64 `json:"max"`
	OOM           uint64 `json:"oom"`
	Restarted     bool   `json:"restarted"`
}

// Usage is the sum of deltas inside one time window.
type Usage struct {
	Throttled     uint64 `json:"throttled"`
	ThrottledUsec uint64 `json:"throttled_usec"`
	High          uint64 `json:"high"`
	Max           uint64 `json:"max"`
	OOM           uint64 `json:"oom"`
}

// Quota is the configured limit of a cgroup.
type Quota struct {
	CPUMax int64 `json:"cpu_max"`
	MemMax int64 `json:"mem_max"`
}

// DriftKind classifies an anomaly seen while collecting counters.
type DriftKind string

// Drift classes reported by the ledger.
const (
	DriftCounterReset     DriftKind = "counter_reset"
	DriftLimitIneffective DriftKind = "limit_ineffective"
	DriftBaselineShift    DriftKind = "baseline_shift"
)

// Drift is one anomaly attached to a tenant.
type Drift struct {
	ID     string    `json:"id"`
	Kind   DriftKind `json:"kind"`
	At     time.Time `json:"at"`
	Detail string    `json:"detail"`
}

// Rank is one line of the throttling leaderboard.
type Rank struct {
	ID            string `json:"id"`
	Throttled     uint64 `json:"throttled"`
	ThrottledUsec uint64 `json:"throttled_usec"`
}

// Limits caps the per tenant and global retention.
type Limits struct {
	MaxSamplesPerTenant int `json:"max_samples_per_tenant"`
	MaxTenants          int `json:"max_tenants"`
	MaxDriftsPerTenant  int `json:"max_drifts_per_tenant"`
}

// Errors reported by the ledger.
var (
	ErrBlankTenant   = errors.New("tenant id is required")
	ErrUnknownTenant = errors.New("unknown tenant")
	ErrTenantLimit   = errors.New("tenant limit reached")
	ErrOutOfOrder    = errors.New("sample time is not after the previous sample")
	ErrEmptyRange    = errors.New("window must have a positive length")
	ErrOutOfRange    = errors.New("window starts before the retained coverage")
	ErrInvalidQuota  = errors.New("quota values must be positive")
	ErrBadSnapshot   = errors.New("snapshot payload is invalid")
)

// ThrottleEventLedger keeps interval deltas for many tenants.
type ThrottleEventLedger struct {
	limits Limits
}

// NewThrottleEventLedger builds a ledger with the given retention limits.
func NewThrottleEventLedger(l Limits) *ThrottleEventLedger {
	return &ThrottleEventLedger{limits: l}
}

// Observe records one sample and returns the delta against the baseline.
func (t *ThrottleEventLedger) Observe(id string, s Sample, at time.Time) (Delta, error) {
	return Delta{}, errNotImplemented
}

// Window sums the retained intervals whose end falls inside [from, to).
func (t *ThrottleEventLedger) Window(id string, from, to time.Time) (Usage, error) {
	return Usage{}, errNotImplemented
}

// Drift returns the anomalies recorded for a tenant in ascending time order.
func (t *ThrottleEventLedger) Drift(id string) []Drift {
	return nil
}

// SetLimit stores the quota used to judge whether throttling is effective.
func (t *ThrottleEventLedger) SetLimit(id string, q Quota) error {
	return errNotImplemented
}

// Limit returns the stored quota of a tenant.
func (t *ThrottleEventLedger) Limit(id string) (Quota, bool) {
	return Quota{}, false
}

// Top ranks tenants by the throttled time inside the retained window.
func (t *ThrottleEventLedger) Top(n int) []Rank {
	return nil
}

// Peak returns the largest single interval usage of a tenant.
func (t *ThrottleEventLedger) Peak(id string) (Usage, error) {
	return Usage{}, errNotImplemented
}

// Forget drops every trace of a tenant, as happens when a cgroup is removed.
func (t *ThrottleEventLedger) Forget(id string) error {
	return errNotImplemented
}

// Samples returns how many intervals are retained for a tenant.
func (t *ThrottleEventLedger) Samples(id string) int {
	return 0
}

// Snapshot serialises the whole ledger.
func (t *ThrottleEventLedger) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the ledger with a previous snapshot.
func (t *ThrottleEventLedger) Restore(data []byte) error {
	return errNotImplemented
}
