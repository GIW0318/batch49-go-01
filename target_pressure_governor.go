// Package task adapts scan pressure per target from a running health score.
package task

import (
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// Outcome kinds reported by the scanner.
const (
	OutcomeOK        = "ok"
	OutcomeThrottled = "throttled"
	OutcomeError     = "error"
	OutcomeProbe     = "probe"
)

// Health anchors.
const (
	HealthMax = 100
	HealthMin = 0
)

// Errors returned by the governor.
var (
	ErrBadTarget     = errors.New("target is blank")
	ErrBadOutcome    = errors.New("outcome kind is invalid")
	ErrUnknownTarget = errors.New("target is not tracked")
	ErrBadSnapshot   = errors.New("snapshot payload is invalid")
	ErrBadAlias      = errors.New("target alias is invalid")
)

// Config tunes the governor.
type Config struct {
	MaxConcurrency   int
	BaseDelay        time.Duration
	MaxDelay         time.Duration
	PenaltyThreshold int
	RecoveryStreak   int
	MaxTargets       int
}

// Outcome is one scan result reported for a target.
type Outcome struct {
	Kind     string        `json:"kind"`
	Bytes    int64         `json:"bytes"`
	Duration time.Duration `json:"duration"`
	At       time.Time     `json:"at"`
}

// TargetState is the inspectable posture of one target.
type TargetState struct {
	Target      string        `json:"target"`
	Health      int           `json:"health"`
	Penalized   bool          `json:"penalized"`
	Concurrency int           `json:"concurrency"`
	Delay       time.Duration `json:"delay"`
	InFlight    int           `json:"in_flight"`
}

type targetRecord struct {
	target string

	health    int
	penalized bool
	inFlight  int

	// throttleRun records consecutive throttles. recoveryRun records
	// consecutive qualifying probes while the target is penalized.
	throttleRun int
	recoveryRun int

	aliases      map[string]struct{}
	lastActivity uint64
}

// TargetPressureGovernor owns per target health, permits and penalties.
type TargetPressureGovernor struct {
	mu       sync.Mutex
	cfg      Config
	targets  map[string]*targetRecord
	aliases  map[string]string
	activity uint64
}

// NewTargetPressureGovernor builds a governor with the given configuration.
func NewTargetPressureGovernor(cfg Config) *TargetPressureGovernor {
	return &TargetPressureGovernor{
		cfg:     normalizeConfig(cfg),
		targets: make(map[string]*targetRecord),
		aliases: make(map[string]string),
	}
}

func normalizeConfig(cfg Config) Config {
	if cfg.MaxConcurrency < 1 {
		cfg.MaxConcurrency = 1
	}
	if cfg.BaseDelay < 0 {
		cfg.BaseDelay = 0
	}
	if cfg.MaxDelay < cfg.BaseDelay {
		cfg.MaxDelay = cfg.BaseDelay
	}
	if cfg.PenaltyThreshold < HealthMin {
		cfg.PenaltyThreshold = HealthMin
	}
	if cfg.PenaltyThreshold >= HealthMax {
		cfg.PenaltyThreshold = HealthMax - 1
	}
	if cfg.RecoveryStreak < 1 {
		cfg.RecoveryStreak = 1
	}
	if cfg.MaxTargets < 0 {
		cfg.MaxTargets = 0
	}
	return cfg
}

// Report folds one scan outcome into the health score of the target.
func (g *TargetPressureGovernor) Report(target string, r Outcome) error {
	key, _, ok := normalizeTarget(target)
	if !ok {
		return ErrBadTarget
	}
	if !validOutcomeKind(r.Kind) {
		return ErrBadOutcome
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	rec := g.ensureTargetLocked(key)
	switch r.Kind {
	case OutcomeThrottled:
		rec.throttleRun++
		rec.recoveryRun = 0
		rec.health = clampHealth(rec.health - 25)
		if rec.throttleRun >= 2 || rec.health <= g.cfg.PenaltyThreshold {
			enterPenalty(rec)
		}
	case OutcomeError:
		rec.throttleRun = 0
		rec.recoveryRun = 0
		rec.health = clampHealth(rec.health - 15)
		if rec.health <= g.cfg.PenaltyThreshold {
			enterPenalty(rec)
		}
	case OutcomeOK, OutcomeProbe:
		rec.throttleRun = 0
		if rec.penalized {
			// A success with no payload is not a healthy probe sample.
			if r.Bytes > 0 {
				rec.recoveryRun++
				if rec.recoveryRun >= g.cfg.RecoveryStreak {
					rec.penalized = false
					rec.recoveryRun = 0
					// Leave the penalty zone just above its threshold. Later
					// successes can raise the health and concurrency gradually.
					rec.health = clampHealth(g.cfg.PenaltyThreshold + 5)
					if rec.health >= HealthMax {
						rec.health = HealthMax - 1
					}
				}
			} else {
				rec.recoveryRun = 0
			}
		} else {
			rec.health = clampHealth(rec.health + 5)
		}
	}
	g.touchLocked(rec)
	return nil
}

// Permit asks for one concurrency slot of the target.
func (g *TargetPressureGovernor) Permit(target string) (bool, error) {
	key, _, ok := normalizeTarget(target)
	if !ok {
		return false, ErrBadTarget
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	rec := g.ensureTargetLocked(key)
	g.touchLocked(rec)
	if rec.inFlight >= concurrencyFor(rec.health, rec.penalized, g.cfg.MaxConcurrency) {
		return false, nil
	}
	rec.inFlight++
	return true, nil
}

// Release returns one concurrency slot of the target.
func (g *TargetPressureGovernor) Release(target string) error {
	key, _, ok := normalizeTarget(target)
	if !ok {
		return ErrBadTarget
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	rec := g.lookupLocked(key)
	if rec == nil {
		return ErrUnknownTarget
	}
	if rec.inFlight > 0 {
		rec.inFlight--
	}
	g.touchLocked(rec)
	return nil
}

// Health returns the current health score of the target.
func (g *TargetPressureGovernor) Health(target string) int {
	key, _, ok := normalizeTarget(target)
	if !ok {
		return 0
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if rec := g.lookupLocked(key); rec != nil {
		return rec.health
	}
	return HealthMax
}

// Concurrency returns the permit cap of the target.
func (g *TargetPressureGovernor) Concurrency(target string) int {
	key, _, ok := normalizeTarget(target)
	if !ok {
		return 0
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if rec := g.lookupLocked(key); rec != nil {
		return concurrencyFor(rec.health, rec.penalized, g.cfg.MaxConcurrency)
	}
	return g.cfg.MaxConcurrency
}

// Delay returns the pause the scanner must keep between requests.
func (g *TargetPressureGovernor) Delay(target string) time.Duration {
	key, _, ok := normalizeTarget(target)
	if !ok {
		return 0
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if rec := g.lookupLocked(key); rec != nil {
		return delayFor(rec.health, g.cfg)
	}
	return delayFor(HealthMax, g.cfg)
}

// Penalized reports whether the target sits in the penalty zone.
func (g *TargetPressureGovernor) Penalized(target string) bool {
	key, _, ok := normalizeTarget(target)
	if !ok {
		return false
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	rec := g.lookupLocked(key)
	return rec != nil && rec.penalized
}

// State returns the full posture of one target.
func (g *TargetPressureGovernor) State(target string) (TargetState, error) {
	key, _, ok := normalizeTarget(target)
	if !ok {
		return TargetState{}, ErrBadTarget
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	rec := g.lookupLocked(key)
	if rec == nil {
		return TargetState{}, ErrUnknownTarget
	}
	return TargetState{
		Target:      rec.target,
		Health:      rec.health,
		Penalized:   rec.penalized,
		Concurrency: concurrencyFor(rec.health, rec.penalized, g.cfg.MaxConcurrency),
		Delay:       delayFor(rec.health, g.cfg),
		InFlight:    rec.inFlight,
	}, nil
}

// Targets lists the tracked targets in lexicographic order.
func (g *TargetPressureGovernor) Targets() []string {
	g.mu.Lock()
	defer g.mu.Unlock()

	out := make([]string, 0, len(g.targets))
	for _, rec := range g.targets {
		out = append(out, rec.target)
	}
	sort.Strings(out)
	return out
}

// AddAlias makes alias share the health score of target.
func (g *TargetPressureGovernor) AddAlias(target, alias string) error {
	targetKey, _, ok := normalizeTarget(target)
	if !ok {
		return ErrBadTarget
	}
	aliasKey, _, ok := normalizeTarget(alias)
	if !ok {
		return ErrBadAlias
	}
	if targetKey == aliasKey {
		return nil
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	targetRec := g.lookupLocked(targetKey)
	aliasRec := g.lookupLocked(aliasKey)
	switch {
	case targetRec != nil && aliasRec != nil && targetRec != aliasRec:
		g.mergeRecordsLocked(targetRec, aliasRec)
	case targetRec == nil && aliasRec != nil:
		targetRec = aliasRec
	case targetRec == nil:
		targetRec = g.ensureTargetLocked(targetKey)
	}

	if targetRec.aliases == nil {
		targetRec.aliases = make(map[string]struct{})
	}
	if targetKey != targetRec.target {
		targetRec.aliases[targetKey] = struct{}{}
		g.aliases[targetKey] = targetRec.target
	}
	targetRec.aliases[aliasKey] = struct{}{}
	g.aliases[aliasKey] = targetRec.target
	g.touchLocked(targetRec)
	return nil
}

type snapshotTarget struct {
	Target      string        `json:"target"`
	Health      int           `json:"health"`
	Penalized   bool          `json:"penalized"`
	Concurrency int           `json:"concurrency"`
	Delay       time.Duration `json:"delay"`
	InFlight    int           `json:"in_flight"`

	ThrottleRun  int      `json:"throttle_run,omitempty"`
	RecoveryRun  int      `json:"recovery_run,omitempty"`
	Aliases      []string `json:"aliases,omitempty"`
	LastActivity uint64   `json:"last_activity,omitempty"`
}

type snapshotEnvelope struct {
	Version int              `json:"version"`
	Targets []snapshotTarget `json:"targets"`
}

const snapshotVersion = 1

// Snapshot serialises every tracked target and its counters.
func (g *TargetPressureGovernor) Snapshot() ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	keys := make([]string, 0, len(g.targets))
	for key := range g.targets {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	envelope := snapshotEnvelope{
		Version: snapshotVersion,
		Targets: make([]snapshotTarget, 0, len(keys)),
	}
	for _, key := range keys {
		rec := g.targets[key]
		aliases := make([]string, 0, len(rec.aliases))
		for alias := range rec.aliases {
			aliases = append(aliases, alias)
		}
		sort.Strings(aliases)
		envelope.Targets = append(envelope.Targets, snapshotTarget{
			Target:       rec.target,
			Health:       rec.health,
			Penalized:    rec.penalized,
			Concurrency:  concurrencyFor(rec.health, rec.penalized, g.cfg.MaxConcurrency),
			Delay:        delayFor(rec.health, g.cfg),
			InFlight:     rec.inFlight,
			ThrottleRun:  rec.throttleRun,
			RecoveryRun:  rec.recoveryRun,
			Aliases:      aliases,
			LastActivity: rec.lastActivity,
		})
	}
	return json.Marshal(envelope)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *TargetPressureGovernor) Restore(data []byte) error {
	var envelope snapshotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return ErrBadSnapshot
	}
	if envelope.Version != snapshotVersion || envelope.Targets == nil {
		return ErrBadSnapshot
	}

	if g.cfg.MaxTargets > 0 && len(envelope.Targets) > g.cfg.MaxTargets {
		return ErrBadSnapshot
	}

	targets := make(map[string]*targetRecord, len(envelope.Targets))
	aliases := make(map[string]string)
	var maxActivity uint64

	for i, item := range envelope.Targets {
		key, normalized, ok := normalizeTarget(item.Target)
		if !ok {
			return ErrBadSnapshot
		}
		if _, exists := targets[key]; exists {
			return ErrBadSnapshot
		}
		if item.Health < HealthMin || item.Health > HealthMax {
			return ErrBadSnapshot
		}
		if item.InFlight < 0 || item.ThrottleRun < 0 || item.RecoveryRun < 0 {
			return ErrBadSnapshot
		}

		activity := item.LastActivity
		if activity == 0 {
			activity = uint64(i + 1)
		}
		if activity > maxActivity {
			maxActivity = activity
		}

		rec := &targetRecord{
			target:       normalized,
			health:       item.Health,
			penalized:    item.Penalized,
			inFlight:     item.InFlight,
			throttleRun:  item.ThrottleRun,
			recoveryRun:  item.RecoveryRun,
			lastActivity: activity,
		}
		if len(item.Aliases) > 0 {
			rec.aliases = make(map[string]struct{}, len(item.Aliases))
		}
		targets[key] = rec
	}

	for _, item := range envelope.Targets {
		key, _, _ := normalizeTarget(item.Target)
		for _, alias := range item.Aliases {
			aliasKey, _, ok := normalizeTarget(alias)
			if !ok {
				return ErrBadSnapshot
			}
			if aliasKey == key {
				continue
			}
			if _, exists := aliases[aliasKey]; exists {
				return ErrBadSnapshot
			}
			if _, isTarget := targets[aliasKey]; isTarget {
				return ErrBadSnapshot
			}
			aliases[aliasKey] = key
			targets[key].aliases[aliasKey] = struct{}{}
		}
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.targets = targets
	g.aliases = aliases
	g.activity = maxActivity
	return nil
}

func validOutcomeKind(kind string) bool {
	switch kind {
	case OutcomeOK, OutcomeThrottled, OutcomeError, OutcomeProbe:
		return true
	default:
		return false
	}
}

func clampHealth(health int) int {
	if health < HealthMin {
		return HealthMin
	}
	if health > HealthMax {
		return HealthMax
	}
	return health
}

func enterPenalty(rec *targetRecord) {
	rec.penalized = true
	rec.recoveryRun = 0
}

func concurrencyFor(health int, penalized bool, maxConcurrency int) int {
	if penalized {
		return 1
	}
	concurrency := int(int64(maxConcurrency) * int64(health) / int64(HealthMax))
	if concurrency < 1 {
		return 1
	}
	if concurrency > maxConcurrency {
		return maxConcurrency
	}
	return concurrency
}

func delayFor(health int, cfg Config) time.Duration {
	var multiplier int64
	switch {
	case health >= 80:
		multiplier = 1
	case health >= 60:
		multiplier = 2
	case health >= 40:
		multiplier = 4
	default:
		multiplier = 8
	}
	if cfg.BaseDelay <= 0 {
		return 0
	}
	maxDelay := cfg.MaxDelay
	if maxDelay < cfg.BaseDelay {
		maxDelay = cfg.BaseDelay
	}
	if int64(cfg.BaseDelay) > int64(maxDelay)/multiplier {
		return maxDelay
	}
	return cfg.BaseDelay * time.Duration(multiplier)
}

func (g *TargetPressureGovernor) touchLocked(rec *targetRecord) {
	g.activity++
	rec.lastActivity = g.activity
}

func (g *TargetPressureGovernor) lookupLocked(key string) *targetRecord {
	if canonical, ok := g.aliases[key]; ok {
		return g.targets[canonical]
	}
	return g.targets[key]
}

func (g *TargetPressureGovernor) ensureTargetLocked(key string) *targetRecord {
	if rec := g.lookupLocked(key); rec != nil {
		return rec
	}
	if g.cfg.MaxTargets > 0 && len(g.targets) >= g.cfg.MaxTargets {
		g.evictLeastRecentLocked()
	}
	g.activity++
	rec := &targetRecord{
		target:       key,
		health:       HealthMax,
		inFlight:     0,
		lastActivity: g.activity,
	}
	g.targets[key] = rec
	return rec
}

func (g *TargetPressureGovernor) evictLeastRecentLocked() {
	var victim string
	var victimActivity uint64
	first := true
	for key, rec := range g.targets {
		if first || rec.lastActivity < victimActivity {
			first = false
			victim = key
			victimActivity = rec.lastActivity
		}
	}
	if first {
		return
	}

	delete(g.targets, victim)
	for alias, canonical := range g.aliases {
		if canonical == victim {
			delete(g.aliases, alias)
		}
	}
}

// normalizeTarget converts target spellings to one stable identity. Host
// names are case-insensitive, URL default ports and trailing slashes are
// immaterial, while path and query spelling is preserved.
func normalizeTarget(raw string) (key, display string, ok bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", "", false
	}

	if strings.Contains(s, "://") {
		if u, err := url.Parse(s); err == nil && u.Scheme != "" && u.Host != "" {
			u.Scheme = strings.ToLower(u.Scheme)
			host := strings.ToLower(u.Hostname())
			port := u.Port()
			if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
				port = ""
			}
			if strings.Contains(host, ":") {
				host = "[" + host + "]"
			}
			if port != "" {
				host += ":" + port
			}
			u.Host = host
			u.Fragment = ""
			u.RawFragment = ""
			u.Path = strings.TrimRight(u.Path, "/")
			u.RawPath = strings.TrimRight(u.RawPath, "/")
			normalized := u.String()
			return normalized, normalized, true
		}
	}

	s = strings.TrimRight(s, "/")
	if s == "" {
		return "", "", false
	}
	cut := strings.IndexAny(s, "/?#")
	if cut < 0 {
		s = strings.ToLower(s)
	} else {
		s = strings.ToLower(s[:cut]) + s[cut:]
	}
	return s, s, true
}

func (g *TargetPressureGovernor) mergeRecordsLocked(primary, secondary *targetRecord) {
	if primary == secondary {
		return
	}

	secondaryKey := secondary.target
	primary.inFlight += secondary.inFlight
	if primary.aliases == nil {
		primary.aliases = make(map[string]struct{})
	}
	for alias := range secondary.aliases {
		primary.aliases[alias] = struct{}{}
		g.aliases[alias] = primary.target
	}
	primary.aliases[secondaryKey] = struct{}{}
	g.aliases[secondaryKey] = primary.target
	delete(g.targets, secondaryKey)
}
