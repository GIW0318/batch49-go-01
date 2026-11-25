// Package task rate limits high cardinality keys with a sliding window held in
// a bounded shard table, folding low cardinality sub keys into a parent key
// and evicting cold keys.
package task

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

const snapshotVersion = 1

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

type timestampUsage struct {
	At   int64 `json:"at"`
	Cost int   `json:"cost"`
}

type shardState struct {
	Events     []timestampUsage `json:"events"`
	LastAccess int64            `json:"last_access"`
}

type snapshotPayload struct {
	Version int                   `json:"version"`
	Shards  map[string]shardState `json:"shards"`
	Folds   map[string]string     `json:"folds"`
}

// ShardLimiter owns the bounded shard table and the sliding windows.
type ShardLimiter struct {
	cfg LimiterConfig
	mu  sync.Mutex

	shards   map[string]*shardState
	folds    map[string]string
	children map[string]map[string]struct{}
}

// NewShardLimiter builds a limiter from the given configuration.
func NewShardLimiter(cfg LimiterConfig) *ShardLimiter {
	if cfg.Now == nil {
		cfg.Now = func() int64 { return time.Now().UnixMilli() }
	}
	return &ShardLimiter{
		cfg:      cfg,
		shards:   make(map[string]*shardState),
		folds:    make(map[string]string),
		children: make(map[string]map[string]struct{}),
	}
}

// Allow charges cost against key inside the current sliding window.
func (s *ShardLimiter) Allow(key string, cost int) (Decision, error) {
	if isBlankKey(key) {
		return Decision{}, ErrBlankKey
	}
	if cost <= 0 {
		return Decision{}, ErrBadCost
	}

	limit := s.Limit()
	if cost > limit {
		return Decision{
			Key:       key,
			Allowed:   false,
			Limit:     limit,
			Used:      0,
			Remaining: remainingQuota(limit, 0),
			Reason:    ReasonCostTooBig,
		}, nil
	}

	now := s.nowMillis()

	s.mu.Lock()
	defer s.mu.Unlock()

	shard, _ := s.shardForUpdateLocked(key, now)
	used := activeUsageLocked(shard, now, s.cfg.WindowMillis)
	left := remainingQuota(limit, used)

	if used > limit || cost > limit-used {
		shard.LastAccess = now
		return Decision{
			Key:       key,
			Allowed:   false,
			Limit:     limit,
			Used:      used,
			Remaining: left,
			Reason:    ReasonExceeded,
		}, nil
	}

	shard.Events = append(shard.Events, timestampUsage{At: now, Cost: cost})
	shard.LastAccess = now
	used += cost

	return Decision{
		Key:       key,
		Allowed:   true,
		Limit:     limit,
		Used:      used,
		Remaining: remainingQuota(limit, used),
		Reason:    ReasonAllowed,
	}, nil
}

// Fold routes the given sub keys to parent so they share the parent window.
func (s *ShardLimiter) Fold(keys []string, parent string) error {
	if isBlankKey(parent) {
		return ErrBlankKey
	}

	children := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if isBlankKey(key) {
			return ErrBlankKey
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		children = append(children, key)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, child := range children {
		if child == parent || s.reachesLocked(parent, child) {
			return fmt.Errorf("%w: %q cannot be folded into %q", ErrFoldCycle, child, parent)
		}
	}

	for _, child := range children {
		s.addFoldLocked(child, parent)
	}

	root := s.rootLocked(parent)
	var target *shardState
	if existing, ok := s.shards[root]; ok {
		target = existing
	}

	for _, child := range children {
		source, ok := s.shards[child]
		if !ok {
			continue
		}
		if target == nil {
			target = &shardState{LastAccess: source.LastAccess}
			s.shards[root] = target
		} else if source.LastAccess > target.LastAccess {
			target.LastAccess = source.LastAccess
		}
		target.Events = append(target.Events, source.Events...)
		delete(s.shards, child)
	}

	return nil
}

// Usage reads the observed state of one key.
func (s *ShardLimiter) Usage(key string) Usage {
	limit := s.Limit()
	result := Usage{
		Key:       key,
		Limit:     limit,
		Remaining: remainingQuota(limit, 0),
	}
	if isBlankKey(key) {
		return result
	}

	now := s.nowMillis()

	s.mu.Lock()
	defer s.mu.Unlock()

	if parent, ok := s.folds[key]; ok {
		result.Folded = true
		result.Parent = parent
	}
	result.Shards = len(s.children[key])

	root := s.rootLocked(key)
	if shard, ok := s.shards[root]; ok && shard != nil {
		result.Used = activeUsageLocked(shard, now, s.cfg.WindowMillis)
		result.Remaining = remainingQuota(limit, result.Used)
	}

	return result
}

// ParentOf reports the parent key a folded key is routed to.
func (s *ShardLimiter) ParentOf(key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	parent, ok := s.folds[key]
	return parent, ok
}

// ShardCount reports how many shards are currently live.
func (s *ShardLimiter) ShardCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.shards)
}

// Keys lists the live keys ordered by name.
func (s *ShardLimiter) Keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	keys := make([]string, 0, len(s.shards))
	for key := range s.shards {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// Sweep evicts the keys that stayed idle past the configured threshold and
// returns how many were evicted.
func (s *ShardLimiter) Sweep() int {
	now := s.nowMillis()

	s.mu.Lock()
	defer s.mu.Unlock()

	evicted := 0
	for key, shard := range s.shards {
		if shard == nil {
			delete(s.shards, key)
			evicted++
			continue
		}
		if now-shard.LastAccess >= s.cfg.IdleEvictMillis {
			delete(s.shards, key)
			evicted++
		}
	}
	return evicted
}

// Reset clears the window of one key.
func (s *ShardLimiter) Reset(key string) {
	if isBlankKey(key) {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	root := s.rootLocked(key)
	if shard, ok := s.shards[root]; ok && shard != nil {
		shard.Events = nil
	}
}

// Limit reports the configured per window amount.
func (s *ShardLimiter) Limit() int {
	return s.cfg.Limit
}

// Snapshot serialises the shard table, the windows and the fold mapping.
func (s *ShardLimiter) Snapshot() ([]byte, error) {
	s.mu.Lock()
	payload := snapshotPayload{
		Version: snapshotVersion,
		Shards:  make(map[string]shardState, len(s.shards)),
		Folds:   make(map[string]string, len(s.folds)),
	}
	for key, shard := range s.shards {
		if shard == nil {
			payload.Shards[key] = shardState{}
			continue
		}
		events := make([]timestampUsage, len(shard.Events))
		copy(events, shard.Events)
		payload.Shards[key] = shardState{
			Events:     events,
			LastAccess: shard.LastAccess,
		}
	}
	for child, parent := range s.folds {
		payload.Folds[child] = parent
	}
	s.mu.Unlock()

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal shard snapshot: %w", err)
	}
	return data, nil
}

// Restore replaces the whole table with a snapshot, all or nothing.
func (s *ShardLimiter) Restore(data []byte) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return fmt.Errorf("%w: snapshot is empty", ErrBadSnapshot)
	}

	var payload snapshotPayload
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&payload); err != nil {
		return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("%w: multiple JSON values", ErrBadSnapshot)
		}
		return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}

	shards, folds, children, err := buildSnapshotState(&payload)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.shards = shards
	s.folds = folds
	s.children = children
	s.mu.Unlock()
	return nil
}

func buildSnapshotState(payload *snapshotPayload) (map[string]*shardState, map[string]string, map[string]map[string]struct{}, error) {
	if payload.Version != snapshotVersion {
		return nil, nil, nil, fmt.Errorf("%w: unsupported version %d", ErrBadSnapshot, payload.Version)
	}
	if payload.Shards == nil {
		return nil, nil, nil, fmt.Errorf("%w: missing shard table", ErrBadSnapshot)
	}
	if payload.Folds == nil {
		payload.Folds = make(map[string]string)
	}

	shards := make(map[string]*shardState, len(payload.Shards))
	for key, shard := range payload.Shards {
		if isBlankKey(key) {
			return nil, nil, nil, fmt.Errorf("%w: blank shard key", ErrBadSnapshot)
		}
		events := make([]timestampUsage, len(shard.Events))
		for i, event := range shard.Events {
			if event.Cost <= 0 {
				return nil, nil, nil, fmt.Errorf("%w: non-positive usage cost", ErrBadSnapshot)
			}
			events[i] = event
		}
		shards[key] = &shardState{
			Events:     events,
			LastAccess: shard.LastAccess,
		}
	}

	folds := make(map[string]string, len(payload.Folds))
	children := make(map[string]map[string]struct{})
	for child, parent := range payload.Folds {
		if isBlankKey(child) || isBlankKey(parent) {
			return nil, nil, nil, fmt.Errorf("%w: blank fold key", ErrBadSnapshot)
		}
		if child == parent {
			return nil, nil, nil, fmt.Errorf("%w: self fold", ErrBadSnapshot)
		}
		folds[child] = parent
		set := children[parent]
		if set == nil {
			set = make(map[string]struct{})
			children[parent] = set
		}
		set[child] = struct{}{}
	}

	for child := range folds {
		if foldCycleFrom(child, folds) {
			return nil, nil, nil, fmt.Errorf("%w: restored fold graph", ErrBadSnapshot)
		}
	}

	return shards, folds, children, nil
}

func foldCycleFrom(start string, folds map[string]string) bool {
	seen := make(map[string]struct{})
	current := start
	for {
		if _, ok := seen[current]; ok {
			return true
		}
		seen[current] = struct{}{}
		parent, ok := folds[current]
		if !ok {
			return false
		}
		current = parent
	}
}

func (s *ShardLimiter) nowMillis() int64 {
	if s.cfg.Now != nil {
		return s.cfg.Now()
	}
	return time.Now().UnixMilli()
}

func isBlankKey(key string) bool {
	return strings.TrimSpace(key) == ""
}

func remainingQuota(limit, used int) int {
	value := limit - used
	if value < 0 {
		return 0
	}
	return value
}

func activeUsageLocked(shard *shardState, now, windowMillis int64) int {
	if shard == nil {
		return 0
	}
	cutoff := now - windowMillis
	kept := shard.Events[:0]
	used := 0
	for _, event := range shard.Events {
		if event.At > cutoff {
			kept = append(kept, event)
			used += event.Cost
		}
	}
	shard.Events = kept
	return used
}

func (s *ShardLimiter) rootLocked(key string) string {
	current := key
	for i := 0; i <= len(s.folds); i++ {
		parent, ok := s.folds[current]
		if !ok {
			return current
		}
		current = parent
	}
	return current
}

// reachesLocked reports whether following parent links from start reaches target.
func (s *ShardLimiter) reachesLocked(start, target string) bool {
	current := start
	for i := 0; i <= len(s.folds); i++ {
		if current == target {
			return true
		}
		parent, ok := s.folds[current]
		if !ok {
			return false
		}
		current = parent
	}
	return false
}

func (s *ShardLimiter) addFoldLocked(child, parent string) {
	if oldParent, ok := s.folds[child]; ok && oldParent != parent {
		if set := s.children[oldParent]; set != nil {
			delete(set, child)
			if len(set) == 0 {
				delete(s.children, oldParent)
			}
		}
	}

	s.folds[child] = parent
	set := s.children[parent]
	if set == nil {
		set = make(map[string]struct{})
		s.children[parent] = set
	}
	set[child] = struct{}{}
}

func (s *ShardLimiter) shardBudget() int {
	if s.cfg.ShardBudget > 0 {
		return s.cfg.ShardBudget
	}
	return 1
}

func (s *ShardLimiter) overflowParent() string {
	if isBlankKey(s.cfg.FoldParent) {
		return "overflow"
	}
	return s.cfg.FoldParent
}

func (s *ShardLimiter) shardForUpdateLocked(key string, now int64) (*shardState, string) {
	root := s.rootLocked(key)
	if shard, ok := s.shards[root]; ok {
		return shard, root
	}

	budget := s.shardBudget()
	if len(s.shards) < budget {
		shard := &shardState{LastAccess: now}
		s.shards[root] = shard
		return shard, root
	}

	overflowName := s.overflowParent()
	overflowRoot := s.rootLocked(overflowName)
	if _, ok := s.shards[overflowRoot]; !ok {
		victim := s.chooseVictimLocked()
		if victim != "" {
			s.moveShardToOverflowLocked(victim, overflowName, overflowRoot)
		}
	}

	root = s.rootLocked(key)
	if shard, ok := s.shards[root]; ok {
		return shard, root
	}

	overflowRoot = s.rootLocked(overflowName)
	if root != overflowRoot {
		s.addFoldLocked(root, overflowName)
		root = s.rootLocked(key)
	}

	if shard, ok := s.shards[root]; ok {
		return shard, root
	}
	if shard, ok := s.shards[overflowRoot]; ok {
		return shard, overflowRoot
	}

	shard := &shardState{LastAccess: now}
	s.shards[overflowRoot] = shard
	return shard, overflowRoot
}

func (s *ShardLimiter) chooseVictimLocked() string {
	var victim string
	var oldest int64
	have := false
	for key, shard := range s.shards {
		access := int64(0)
		if shard != nil {
			access = shard.LastAccess
		}
		if !have || access < oldest || (access == oldest && key < victim) {
			victim = key
			oldest = access
			have = true
		}
	}
	return victim
}

func (s *ShardLimiter) moveShardToOverflowLocked(victim, overflowName, overflowRoot string) {
	s.addFoldLocked(victim, overflowName)

	source := s.shards[victim]
	delete(s.shards, victim)
	if source == nil {
		source = &shardState{LastAccess: s.nowMillis()}
	}

	root := s.rootLocked(overflowName)
	if root != overflowRoot {
		overflowRoot = root
	}
	if existing, ok := s.shards[overflowRoot]; ok {
		existing.Events = append(existing.Events, source.Events...)
		if source.LastAccess > existing.LastAccess {
			existing.LastAccess = source.LastAccess
		}
		return
	}
	s.shards[overflowRoot] = source
}
