// Package task turns raw cgroup throttle and memory event counters into
// interval deltas and keeps per tenant usage windows.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

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

const snapshotVersion = 1

// ThrottleEventLedger keeps interval deltas for many tenants.
type ThrottleEventLedger struct {
	mu      sync.RWMutex
	limits  Limits
	tenants map[string]*tenantState
}

type retainedInterval struct {
	Start     time.Time `json:"start"`
	End       time.Time `json:"end"`
	Usage     Usage     `json:"usage"`
	Restarted bool      `json:"restarted,omitempty"`
}

type tenantState struct {
	Last            Sample             `json:"last"`
	LastAt          time.Time          `json:"last_at"`
	Intervals       []retainedInterval `json:"intervals"`
	Drifts          []Drift            `json:"drifts"`
	Quota           *Quota             `json:"quota,omitempty"`
	Peak            Usage              `json:"peak"`
	LimitCheckpoint Sample             `json:"limit_checkpoint"`
	LimitWatch      bool               `json:"limit_watch,omitempty"`
}

type snapshotData struct {
	Version int                     `json:"version"`
	Limits  Limits                  `json:"limits"`
	Tenants map[string]*tenantState `json:"tenants"`
}

// NewThrottleEventLedger builds a ledger with the given retention limits.
func NewThrottleEventLedger(l Limits) *ThrottleEventLedger {
	return &ThrottleEventLedger{
		limits:  l,
		tenants: make(map[string]*tenantState),
	}
}

// Observe records one sample and returns the delta against the baseline.
func (t *ThrottleEventLedger) Observe(id string, s Sample, at time.Time) (Delta, error) {
	if strings.TrimSpace(id) == "" {
		return Delta{}, ErrBlankTenant
	}
	at = at.UTC()

	t.mu.Lock()
	defer t.mu.Unlock()

	tenant, ok := t.tenants[id]
	if !ok {
		if t.limits.MaxTenants <= 0 || len(t.tenants) >= t.limits.MaxTenants {
			return Delta{}, ErrTenantLimit
		}
		t.tenants[id] = &tenantState{Last: s, LastAt: at}
		return Delta{}, nil
	}

	if at.Equal(tenant.LastAt) {
		if s == tenant.Last {
			return Delta{}, nil
		}
		return Delta{}, ErrOutOfOrder
	}
	if at.Before(tenant.LastAt) {
		return Delta{}, ErrOutOfOrder
	}

	restarted := counterRegressed(tenant.Last, s)
	var delta Delta
	if restarted {
		delta = restartDelta(s)
		t.addDriftLocked(tenant, Drift{
			ID:     id,
			Kind:   DriftCounterReset,
			At:     at,
			Detail: "one or more monotonic counters moved backwards; a new counter epoch starts",
		})
	} else {
		delta = counterDelta(tenant.Last, s)
	}

	t.appendIntervalLocked(tenant, at, delta, s, s != tenant.Last)
	t.checkLimitLocked(tenant, id, s, at, restarted)

	tenant.Last = s
	tenant.LastAt = at
	return delta, nil
}

// Window sums the retained intervals whose end falls inside [from, to).
func (t *ThrottleEventLedger) Window(id string, from, to time.Time) (Usage, error) {
	if !from.Before(to) {
		return Usage{}, ErrEmptyRange
	}

	t.mu.RLock()
	defer t.mu.RUnlock()

	tenant, ok := t.tenants[id]
	if !ok {
		return Usage{}, ErrUnknownTenant
	}
	if len(tenant.Intervals) > 0 && from.Before(tenant.Intervals[0].Start) {
		return Usage{}, ErrOutOfRange
	}

	var usage Usage
	for _, interval := range tenant.Intervals {
		if !interval.End.Before(from) && interval.End.Before(to) {
			addUsage(&usage, interval.Usage)
		}
	}
	return usage, nil
}

// Drift returns the anomalies recorded for a tenant in ascending time order.
func (t *ThrottleEventLedger) Drift(id string) []Drift {
	t.mu.RLock()
	defer t.mu.RUnlock()

	tenant, ok := t.tenants[id]
	if !ok {
		return []Drift{}
	}
	out := make([]Drift, len(tenant.Drifts))
	copy(out, tenant.Drifts)
	return out
}

// SetLimit stores the quota used to judge whether throttling is effective.
func (t *ThrottleEventLedger) SetLimit(id string, q Quota) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	tenant, ok := t.tenants[id]
	if !ok {
		return ErrUnknownTenant
	}
	if q.CPUMax <= 0 || q.MemMax <= 0 {
		return ErrInvalidQuota
	}

	if tenant.Quota != nil && q.CPUMax < tenant.Quota.CPUMax {
		tenant.LimitWatch = true
		tenant.LimitCheckpoint = tenant.Last
	}
	stored := q
	tenant.Quota = &stored
	return nil
}

// Limit returns the stored quota of a tenant.
func (t *ThrottleEventLedger) Limit(id string) (Quota, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	tenant, ok := t.tenants[id]
	if !ok || tenant.Quota == nil {
		return Quota{}, false
	}
	return *tenant.Quota, true
}

// Top ranks tenants by the throttled time inside the retained window.
func (t *ThrottleEventLedger) Top(n int) []Rank {
	if n <= 0 {
		return []Rank{}
	}

	t.mu.RLock()
	ranks := make([]Rank, 0, len(t.tenants))
	for id, tenant := range t.tenants {
		var usage Usage
		for _, interval := range tenant.Intervals {
			addUsage(&usage, interval.Usage)
		}
		ranks = append(ranks, Rank{
			ID:            id,
			Throttled:     usage.Throttled,
			ThrottledUsec: usage.ThrottledUsec,
		})
	}
	t.mu.RUnlock()

	sort.Slice(ranks, func(i, j int) bool {
		if ranks[i].ThrottledUsec != ranks[j].ThrottledUsec {
			return ranks[i].ThrottledUsec > ranks[j].ThrottledUsec
		}
		if ranks[i].Throttled != ranks[j].Throttled {
			return ranks[i].Throttled > ranks[j].Throttled
		}
		return ranks[i].ID < ranks[j].ID
	})

	if n > len(ranks) {
		n = len(ranks)
	}
	return ranks[:n]
}

// Peak returns the largest single interval usage of a tenant.
func (t *ThrottleEventLedger) Peak(id string) (Usage, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	tenant, ok := t.tenants[id]
	if !ok {
		return Usage{}, ErrUnknownTenant
	}
	return tenant.Peak, nil
}

// Forget drops every trace of a tenant, as happens when a cgroup is removed.
func (t *ThrottleEventLedger) Forget(id string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if _, ok := t.tenants[id]; !ok {
		return ErrUnknownTenant
	}
	delete(t.tenants, id)
	return nil
}

// Samples returns how many intervals are retained for a tenant.
func (t *ThrottleEventLedger) Samples(id string) int {
	t.mu.RLock()
	defer t.mu.RUnlock()

	tenant, ok := t.tenants[id]
	if !ok {
		return 0
	}
	return len(tenant.Intervals)
}

// Snapshot serialises the whole ledger.
func (t *ThrottleEventLedger) Snapshot() ([]byte, error) {
	t.mu.RLock()
	data := snapshotData{
		Version: snapshotVersion,
		Limits:  t.limits,
		Tenants: make(map[string]*tenantState, len(t.tenants)),
	}
	for id, tenant := range t.tenants {
		copied := *tenant
		copied.Intervals = append([]retainedInterval(nil), tenant.Intervals...)
		copied.Drifts = append([]Drift(nil), tenant.Drifts...)
		if tenant.Quota != nil {
			quota := *tenant.Quota
			copied.Quota = &quota
		}
		data.Tenants[id] = &copied
	}
	t.mu.RUnlock()

	blob, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("encode snapshot: %w", err)
	}
	return blob, nil
}

// Restore replaces the ledger with a previous snapshot.
func (t *ThrottleEventLedger) Restore(data []byte) error {
	var decoded snapshotData
	if err := json.Unmarshal(data, &decoded); err != nil {
		return fmt.Errorf("%w: decode snapshot: %v", ErrBadSnapshot, err)
	}
	if err := validateSnapshot(&decoded); err != nil {
		return err
	}

	tenants := make(map[string]*tenantState, len(decoded.Tenants))
	for id, tenant := range decoded.Tenants {
		copied := *tenant
		copied.Intervals = append([]retainedInterval(nil), tenant.Intervals...)
		copied.Drifts = append([]Drift(nil), tenant.Drifts...)
		if tenant.Quota != nil {
			quota := *tenant.Quota
			copied.Quota = &quota
		}
		tenants[id] = &copied
	}

	t.mu.Lock()
	t.limits = decoded.Limits
	t.tenants = tenants
	t.mu.Unlock()
	return nil
}

func counterRegressed(previous, current Sample) bool {
	return current.NrThrottled < previous.NrThrottled ||
		current.ThrottledUsec < previous.ThrottledUsec ||
		current.HighEvents < previous.HighEvents ||
		current.MaxEvents < previous.MaxEvents ||
		current.OOMEvents < previous.OOMEvents
}

func counterDelta(previous, current Sample) Delta {
	return Delta{
		Throttled:     current.NrThrottled - previous.NrThrottled,
		ThrottledUsec: current.ThrottledUsec - previous.ThrottledUsec,
		High:          current.HighEvents - previous.HighEvents,
		Max:           current.MaxEvents - previous.MaxEvents,
		OOM:           current.OOMEvents - previous.OOMEvents,
	}
}

func restartDelta(current Sample) Delta {
	return Delta{
		Throttled:     current.NrThrottled,
		ThrottledUsec: current.ThrottledUsec,
		High:          current.HighEvents,
		Max:           current.MaxEvents,
		OOM:           current.OOMEvents,
		Restarted:     true,
	}
}

func addUsage(total *Usage, part Usage) {
	total.Throttled += part.Throttled
	total.ThrottledUsec += part.ThrottledUsec
	total.High += part.High
	total.Max += part.Max
	total.OOM += part.OOM
}

func maxUsage(peak *Usage, candidate Usage) {
	if candidate.Throttled > peak.Throttled {
		peak.Throttled = candidate.Throttled
	}
	if candidate.ThrottledUsec > peak.ThrottledUsec {
		peak.ThrottledUsec = candidate.ThrottledUsec
	}
	if candidate.High > peak.High {
		peak.High = candidate.High
	}
	if candidate.Max > peak.Max {
		peak.Max = candidate.Max
	}
	if candidate.OOM > peak.OOM {
		peak.OOM = candidate.OOM
	}
}

func (t *ThrottleEventLedger) appendIntervalLocked(
	tenant *tenantState,
	end time.Time,
	delta Delta,
	current Sample,
	changedSample bool,
) {
	usage := Usage{
		Throttled:     delta.Throttled,
		ThrottledUsec: delta.ThrottledUsec,
		High:          delta.High,
		Max:           delta.Max,
		OOM:           delta.OOM,
	}
	if changedSample && usage.High == 0 && current.HighEvents > 0 {
		usage.High = 1
	}
	tenant.Intervals = append(tenant.Intervals, retainedInterval{
		Start:     tenant.LastAt,
		End:       end,
		Usage:     usage,
		Restarted: delta.Restarted,
	})

	maxUsage(&tenant.Peak, usage)

	if t.limits.MaxSamplesPerTenant > 0 {
		if excess := len(tenant.Intervals) - t.limits.MaxSamplesPerTenant; excess > 0 {
			tenant.Intervals = append([]retainedInterval(nil), tenant.Intervals[excess:]...)
		}
	} else {
		tenant.Intervals = nil
	}
}

func (t *ThrottleEventLedger) addDriftLocked(tenant *tenantState, drift Drift) {
	if t.limits.MaxDriftsPerTenant <= 0 {
		return
	}
	tenant.Drifts = append(tenant.Drifts, drift)
	if excess := len(tenant.Drifts) - t.limits.MaxDriftsPerTenant; excess > 0 {
		tenant.Drifts = append([]Drift(nil), tenant.Drifts[excess:]...)
	}
}

func (t *ThrottleEventLedger) checkLimitLocked(tenant *tenantState, id string, current Sample, at time.Time, restarted bool) {
	if !tenant.LimitWatch || tenant.Quota == nil {
		return
	}
	if restarted {
		tenant.LimitCheckpoint = current
		return
	}

	tenant.LimitWatch = false
	if current.NrThrottled == tenant.LimitCheckpoint.NrThrottled {
		t.addDriftLocked(tenant, Drift{
			ID:     id,
			Kind:   DriftLimitIneffective,
			At:     at,
			Detail: "CPU quota was lowered but nr_throttled did not increase",
		})
	}
}

func validateSnapshot(data *snapshotData) error {
	if data.Version != snapshotVersion {
		return fmt.Errorf("%w: unsupported snapshot version %d", ErrBadSnapshot, data.Version)
	}
	if data.Tenants == nil {
		return fmt.Errorf("%w: missing tenant table", ErrBadSnapshot)
	}
	for id, tenant := range data.Tenants {
		if strings.TrimSpace(id) == "" || tenant == nil {
			return fmt.Errorf("%w: snapshot contains an invalid tenant", ErrBadSnapshot)
		}
		if tenant.Quota != nil && (tenant.Quota.CPUMax <= 0 || tenant.Quota.MemMax <= 0) {
			return fmt.Errorf("%w: snapshot contains an invalid quota", ErrBadSnapshot)
		}
		if maxSamples := data.Limits.MaxSamplesPerTenant; maxSamples > 0 && len(tenant.Intervals) > maxSamples {
			return fmt.Errorf("%w: snapshot exceeds sample retention", ErrBadSnapshot)
		}
		if maxDrifts := data.Limits.MaxDriftsPerTenant; maxDrifts > 0 && len(tenant.Drifts) > maxDrifts {
			return fmt.Errorf("%w: snapshot exceeds drift retention", ErrBadSnapshot)
		}
		if maxTenants := data.Limits.MaxTenants; maxTenants > 0 && len(data.Tenants) > maxTenants {
			return fmt.Errorf("%w: snapshot exceeds tenant limit", ErrBadSnapshot)
		}
	}
	return nil
}
