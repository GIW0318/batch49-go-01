// Package task implements GCRA smoothing with burst tolerance, bounded waiting
// and idle key compaction.
package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
	"sync"
	"time"
)

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

// snapshotVersion tags the serialised payload format.
const snapshotVersion = 1

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

// keyState is the per key virtual schedule and counters. All fields are guarded
// by mu; the global GcraThrottle.mu read barrier additionally makes commits
// mutually exclusive with snapshots and map replacements.
type keyState struct {
	mu sync.Mutex
	// wake is closed (and replaced) when the key is reset or compacted so
	// waiting callers recompute against the fresh state.
	wake chan struct{}
	// gen increments on every reset invalidation.
	gen uint64

	tatMs   int64
	lastAt  int64
	allowed int
	denied  int
	waiters int
}

// GcraThrottle owns the per key virtual scheduling state and the clock.
type GcraThrottle struct {
	cfg GcraConfig

	// mu guards clock, clockWait and keys. Read lock is held across a key's
	// atomic evaluate/commit so cross key commits stay concurrent while
	// snapshots take the write lock for a globally consistent view.
	mu        sync.RWMutex
	clock     int64
	clockWait chan struct{}
	keys      map[string]*keyState
}

// NewGcraThrottle builds an empty throttle with the given policy.
func NewGcraThrottle(cfg GcraConfig) *GcraThrottle {
	if cfg.EmissionMs <= 0 {
		// A non positive emission interval would divide by zero when the
		// virtual debt is converted back into whole units. Normalise it.
		cfg.EmissionMs = 1
	}
	return &GcraThrottle{
		cfg:       cfg,
		clockWait: make(chan struct{}),
		keys:      make(map[string]*keyState),
	}
}

func validateKey(key string) error {
	if strings.TrimSpace(key) == "" {
		return ErrBlankKey
	}
	return nil
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// remainingUnits converts the idle time accumulated against tat back into
// whole available units, rounded down and capped at the burst tolerance.
func remainingUnits(tat, now int64, burst int, emission int64) int {
	budget := now + int64(burst)*emission - tat
	if budget <= 0 {
		return 0
	}
	remaining := budget / emission
	if cap := int64(burst); remaining > cap {
		remaining = cap
	}
	if remaining < 0 {
		return 0
	}
	return int(remaining)
}

// waitForUnits reports the exact number of logical milliseconds until the
// request is admissible. It is only meaningful for a request that fails the
// allow predicate while units <= burst.
func waitForUnits(tat, now int64, units int, burst int, emission int64) int64 {
	wait := tat + int64(units)*emission - int64(burst)*emission - now
	if wait < 0 {
		return 0
	}
	return wait
}

// acquire returns the current key state with both the global read barrier and
// the key lock held. The returned release closure must be called exactly once.
// Missing keys are created here.
func (t *GcraThrottle) acquire(key string) (*keyState, func()) {
	for {
		t.mu.RLock()
		state := t.keys[key]
		if state != nil {
			state.mu.Lock()
			release := func() {
				state.mu.Unlock()
				t.mu.RUnlock()
			}
			return state, release
		}
		t.mu.RUnlock()

		t.mu.Lock()
		state = t.keys[key]
		if state == nil {
			state = &keyState{wake: make(chan struct{})}
			t.keys[key] = state
		}
		t.mu.Unlock()
	}
}

// Take decides one request immediately without waiting.
func (t *GcraThrottle) Take(key string, units int) (Decision, error) {
	if err := validateKey(key); err != nil {
		return Decision{}, err
	}
	if units <= 0 {
		return Decision{}, ErrBadUnits
	}

	emission := t.cfg.EmissionMs
	burst := t.cfg.BurstUnits

	// A batch larger than the whole burst can never be scheduled, even by
	// waiting, so reject it outright without creating or mutating state.
	if units > burst {
		t.mu.RLock()
		now := t.clock
		var tat int64
		if state := t.keys[key]; state != nil {
			state.mu.Lock()
			tat = state.tatMs
			state.mu.Unlock()
		}
		t.mu.RUnlock()
		return Decision{
			Allowed:        false,
			WaitMs:         0,
			RemainingUnits: remainingUnits(tat, now, burst, emission),
			TatMs:          tat,
			Reason:         ReasonBurstExceeded,
		}, nil
	}

	state, release := t.acquire(key)
	defer release()

	now := t.clock
	newTat := maxInt64(state.tatMs, now) + int64(units)*emission
	burstLimit := now + int64(burst)*emission

	if newTat <= burstLimit {
		// Allow predicate, TAT advance and counter update happen in one
		// atomic read/modify/write critical section for the key.
		state.tatMs = newTat
		state.lastAt = now
		state.allowed += units
		return Decision{
			Allowed:        true,
			WaitMs:         0,
			RemainingUnits: remainingUnits(state.tatMs, now, burst, emission),
			TatMs:          state.tatMs,
			Reason:         ReasonAllowed,
		}, nil
	}

	// Denied: leave TAT and the last-allow anchor untouched and report the
	// precise retry delay against the current allowance.
	waitMs := waitForUnits(state.tatMs, now, units, burst, emission)
	remaining := remainingUnits(state.tatMs, now, burst, emission)
	state.denied++
	return Decision{
		Allowed:        false,
		WaitMs:         waitMs,
		RemainingUnits: remaining,
		TatMs:          state.tatMs,
		Reason:         ReasonRateLimited,
	}, nil
}

// TakeWait decides one request and waits until the key allows it, bounded by
// MaxWaitMs and the context.
func (t *GcraThrottle) TakeWait(ctx context.Context, key string, units int) error {
	if err := validateKey(key); err != nil {
		return err
	}
	if units <= 0 {
		return ErrBadUnits
	}
	if units > t.cfg.BurstUnits {
		// Never split an over-burst batch into several smaller waits.
		return ErrBurstExceeded
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	emission := t.cfg.EmissionMs
	burst := t.cfg.BurstUnits
	maxWait := t.cfg.MaxWaitMs

	// One stable state for the whole blocking call. Holding a waiter marker
	// for the entire lifetime (including recompute windows) guarantees Prune
	// never compacts a key this caller is still queued on. The marker is only
	// an integer; no lock is held while sleeping.
	state, release := t.acquire(key)
	state.waiters++
	release()
	defer func() {
		state.mu.Lock()
		if state.waiters > 0 {
			state.waiters--
		}
		state.mu.Unlock()
	}()

	// virtualNow tracks the scheduling clock this caller has actually waited
	// through. The shared logical clock stays authoritative when it is
	// advanced manually. Real timers bridge the gap for callers that block
	// without an explicit Advance.
	var virtualNow int64
	var waitedBudget int64
	var lastGen uint64
	basisKnown := false

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		t.mu.RLock()
		state.mu.Lock()
		now := t.clock
		clockWake := t.clockWait

		if !basisKnown || state.gen != lastGen {
			// A reset invalidation forces a fresh calculation from the new
			// theoretical arrival time rather than the stale waited value.
			virtualNow = now
			waitedBudget = 0
			basisKnown = true
		} else if now > virtualNow {
			virtualNow = now
		}
		lastGen = state.gen

		newTat := maxInt64(state.tatMs, virtualNow) + int64(units)*emission
		burstLimit := virtualNow + int64(burst)*emission

		if newTat <= burstLimit {
			state.tatMs = newTat
			// The pruning anchor stays expressed in the shared logical clock.
			state.lastAt = now
			state.allowed += units
			state.mu.Unlock()
			t.mu.RUnlock()
			return nil
		}

		waitMs := waitForUnits(state.tatMs, virtualNow, units, burst, emission)
		if waitMs <= 0 {
			state.mu.Unlock()
			t.mu.RUnlock()
			continue
		}

		// Bounded waiting: reject immediately when the required delay cannot
		// fit inside the configured maximum wait budget.
		if maxWait >= 0 && waitedBudget+waitMs > maxWait {
			state.mu.Unlock()
			t.mu.RUnlock()
			return ErrWaitTooLong
		}

		wake := state.wake
		timer := time.NewTimer(time.Duration(waitMs) * time.Millisecond)
		state.mu.Unlock()
		t.mu.RUnlock()

		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-wake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			// Reset closed wake; loop and recompute from the fresh TAT.
		case <-clockWake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			// Logical clock advanced; loop and pick up the new clock value.
		case <-timer.C:
			virtualNow += waitMs
			waitedBudget += waitMs
		}
	}
}

// Reset drops the scheduling state and counters of one key. It is idempotent.
func (t *GcraThrottle) Reset(key string) error {
	if err := validateKey(key); err != nil {
		return err
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	state := t.keys[key]
	if state == nil {
		return nil
	}

	state.mu.Lock()
	state.tatMs = 0
	state.lastAt = 0
	state.allowed = 0
	state.denied = 0
	state.gen++
	close(state.wake)
	state.wake = make(chan struct{})
	state.mu.Unlock()
	return nil
}

// Usage reads the state and counters of one key. Unknown and blank keys
// return the zero Usage.
func (t *GcraThrottle) Usage(key string) Usage {
	if validateKey(key) != nil {
		return Usage{}
	}

	t.mu.RLock()
	defer t.mu.RUnlock()

	state := t.keys[key]
	if state == nil {
		return Usage{}
	}

	state.mu.Lock()
	defer state.mu.Unlock()

	return Usage{
		RemainingUnits: remainingUnits(state.tatMs, t.clock, t.cfg.BurstUnits, t.cfg.EmissionMs),
		TatMs:          state.tatMs,
		LastAt:         state.lastAt,
		Allowed:        state.allowed,
		Denied:         state.denied,
	}
}

// Keys lists the keys holding state, in ascending order. Reset keys are
// compacted out of the table lazily here so a reset key disappears from the
// listing immediately while an immediate Usage before any compaction still
// observes the restored full burst.
func (t *GcraThrottle) Keys() []string {
	t.mu.Lock()
	defer t.mu.Unlock()

	keys := make([]string, 0, len(t.keys))
	for key, state := range t.keys {
		state.mu.Lock()
		empty := state.tatMs == 0 && state.allowed == 0 && state.denied == 0
		if empty && state.waiters == 0 {
			delete(t.keys, key)
			state.mu.Unlock()
			continue
		}
		state.mu.Unlock()
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// Prune compacts keys whose last allowed theoretical time is older than the
// idle ttl. Keys with waiting callers are retained. It returns how many keys
// were physically dropped.
func (t *GcraThrottle) Prune() int {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.clock
	removed := 0
	for key, state := range t.keys {
		state.mu.Lock()
		empty := state.tatMs == 0 && state.allowed == 0 && state.denied == 0
		idleExpired := t.cfg.IdleTtlMs > 0 && now-state.lastAt >= t.cfg.IdleTtlMs
		droppable := state.waiters == 0 && (empty || idleExpired)
		if droppable {
			delete(t.keys, key)
		}
		state.mu.Unlock()
		if droppable {
			removed++
		}
	}
	return removed
}

// Advance moves the logical clock forward by the given milliseconds.
func (t *GcraThrottle) Advance(ms int64) {
	if ms <= 0 {
		return
	}
	t.mu.Lock()
	t.clock += ms
	close(t.clockWait)
	t.clockWait = make(chan struct{})
	t.mu.Unlock()
}

// Clock reports the current logical time in milliseconds.
func (t *GcraThrottle) Clock() int64 {
	t.mu.RLock()
	clock := t.clock
	t.mu.RUnlock()
	return clock
}

type snapshotKey struct {
	Key     string `json:"key"`
	TatMs   int64  `json:"tat_ms"`
	LastAt  int64  `json:"last_at"`
	Allowed int    `json:"allowed"`
	Denied  int    `json:"denied"`
}

type snapshotBody struct {
	Version int           `json:"version"`
	Clock   int64         `json:"clock"`
	Keys    []snapshotKey `json:"keys"`
}

type snapshotFile struct {
	Version  int           `json:"version"`
	Clock    int64         `json:"clock"`
	Keys     []snapshotKey `json:"keys"`
	Checksum string        `json:"checksum"`
}

func snapshotChecksum(body snapshotBody) string {
	payload, _ := json.Marshal(body)
	hash := fnv.New64a()
	_, _ = hash.Write(payload)
	return fmt.Sprintf("%016x", hash.Sum64())
}

// Snapshot serialises the clock and every key state.
func (t *GcraThrottle) Snapshot() ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	entries := make([]snapshotKey, 0, len(t.keys))
	for key, state := range t.keys {
		state.mu.Lock()
		entries = append(entries, snapshotKey{
			Key:     key,
			TatMs:   state.tatMs,
			LastAt:  state.lastAt,
			Allowed: state.allowed,
			Denied:  state.denied,
		})
		state.mu.Unlock()
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })

	body := snapshotBody{Version: snapshotVersion, Clock: t.clock, Keys: entries}
	file := snapshotFile{
		Version:  body.Version,
		Clock:    body.Clock,
		Keys:     body.Keys,
		Checksum: snapshotChecksum(body),
	}
	return json.Marshal(file)
}

// Restore replaces the clock and key states from a snapshot. Parsing and
// validation finish before any live state is touched, so a corrupt payload
// leaves the running throttle unchanged.
func (t *GcraThrottle) Restore(data []byte) error {
	body, err := decodeSnapshot(data)
	if err != nil {
		return err
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	states := make(map[string]*keyState, len(body.Keys))
	for _, entry := range body.Keys {
		states[entry.Key] = &keyState{
			wake:    make(chan struct{}),
			tatMs:   entry.TatMs,
			lastAt:  entry.LastAt,
			allowed: entry.Allowed,
			denied:  entry.Denied,
		}
	}

	t.clock = body.Clock
	t.keys = states
	close(t.clockWait)
	t.clockWait = make(chan struct{})
	return nil
}

func decodeSnapshot(data []byte) (snapshotBody, error) {
	invalid := func(detail string) error {
		if detail == "" {
			return ErrBadSnapshot
		}
		return fmt.Errorf("%w: %s", ErrBadSnapshot, detail)
	}

	if len(strings.TrimSpace(string(data))) == 0 {
		return snapshotBody{}, invalid("empty payload")
	}

	var file snapshotFile
	if err := json.Unmarshal(data, &file); err != nil {
		return snapshotBody{}, invalid("invalid json")
	}
	if file.Version != snapshotVersion {
		return snapshotBody{}, invalid("unsupported version")
	}
	if file.Clock < 0 {
		return snapshotBody{}, invalid("negative clock")
	}
	if file.Checksum == "" {
		return snapshotBody{}, invalid("missing checksum")
	}
	if file.Keys == nil {
		file.Keys = []snapshotKey{}
	}

	seen := make(map[string]struct{}, len(file.Keys))
	for _, entry := range file.Keys {
		if strings.TrimSpace(entry.Key) == "" {
			return snapshotBody{}, invalid("blank key")
		}
		if entry.TatMs < 0 || entry.LastAt < 0 || entry.Allowed < 0 || entry.Denied < 0 {
			return snapshotBody{}, invalid("negative state")
		}
		if _, dup := seen[entry.Key]; dup {
			return snapshotBody{}, invalid("duplicate key")
		}
		seen[entry.Key] = struct{}{}
	}
	sort.Slice(file.Keys, func(i, j int) bool { return file.Keys[i].Key < file.Keys[j].Key })

	body := snapshotBody{Version: file.Version, Clock: file.Clock, Keys: file.Keys}
	if snapshotChecksum(body) != file.Checksum {
		return snapshotBody{}, invalid("checksum mismatch")
	}
	return body, nil
}
