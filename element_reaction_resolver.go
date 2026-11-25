// Package task resolves elemental auras, their decay and the reactions
// produced by incoming hits, including the per skill reaction cooldown.
package task

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
)

// Aura and cooldown anchors. All gauges are integer units and all times are
// whole multiples of one hundred milliseconds.
const (
	TickMs            = int64(100)
	DefaultMaxTargets = 1024
	DefaultMaxAuras   = 2
	DefaultGauge      = 100
	DefaultICDFrames  = 12
	DefaultDecay      = 10
)

// Element identifiers.
const (
	ElementPyro    = "pyro"
	ElementHydro   = "hydro"
	ElementCryo    = "cryo"
	ElementElectro = "electro"
)

// Reaction identifiers.
const (
	ReactionVaporize       = "vaporize"
	ReactionMelt           = "melt"
	ReactionOverload       = "overload"
	ReactionFreeze         = "freeze"
	ReactionElectrocharged = "electrocharged"
	ReactionSuperconduct   = "superconduct"
)

// Errors returned by the resolver.
var (
	ErrBadOptions     = errors.New("options are out of range")
	ErrBlankTarget    = errors.New("target id is required")
	ErrUnknownElement = errors.New("unknown element")
	ErrUnknownTarget  = errors.New("unknown target")
	ErrBadGauge       = errors.New("gauge must be positive")
	ErrBadTime        = errors.New("time must not be negative")
	ErrBadFrames      = errors.New("cooldown frames must not be negative")
	ErrBadDecay       = errors.New("decay rate must not be negative")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// Options bounds the live aura table.
type Options struct {
	MaxTargets       int
	MaxAuras         int
	DefaultGauge     int
	DefaultICDFrames int
}

// ReactHit is one incoming elemental hit.
type ReactHit struct {
	SkillID string
	Target  string
	Element string
	Gauge   int
	Damage  int64
	At      int64
}

// Aura is one element attachment with its current gauge.
type Aura struct {
	Element string
	Gauge   int
}

// ReactionResult reports what one hit resolved to.
type ReactionResult struct {
	Reaction   string
	Element    string
	Consumed   int
	Factor     int
	Damage     int64
	Suppressed bool
}

type auraState struct {
	Element    string
	Gauge      int
	AttachedAt int64
}

type targetState struct {
	Auras []auraState
}

type cooldownKey struct {
	Target  string
	SkillID string
}

// ReactionResolver owns aura tables, decay rates and cooldown windows.
type ReactionResolver struct {
	mu         sync.Mutex
	opts       Options
	targets    map[string]*targetState
	decay      map[string]int
	icd        map[string]int
	lastReact  map[cooldownKey]int64
	suppressed map[string]int
}

var reactionOrder = []string{
	ReactionVaporize,
	ReactionMelt,
	ReactionOverload,
	ReactionFreeze,
	ReactionElectrocharged,
	ReactionSuperconduct,
}

// ReactionPriority returns the fixed reaction resolution order.
func ReactionPriority() []string {
	out := make([]string, len(reactionOrder))
	copy(out, reactionOrder)
	return out
}

// ReactionFactor returns the damage factor in percent for a reaction.
func ReactionFactor(reaction string) int {
	switch reaction {
	case ReactionVaporize, ReactionMelt:
		return 200
	case ReactionOverload:
		return 150
	case ReactionFreeze, ReactionElectrocharged, ReactionSuperconduct:
		return 100
	default:
		return 0
	}
}

// NewReactionResolver builds a resolver, filling defaults for zero options.
func NewReactionResolver(opts Options) *ReactionResolver {
	if opts.MaxTargets <= 0 {
		opts.MaxTargets = DefaultMaxTargets
	}
	if opts.MaxAuras <= 0 {
		opts.MaxAuras = DefaultMaxAuras
	}
	if opts.DefaultGauge <= 0 {
		opts.DefaultGauge = DefaultGauge
	}
	if opts.DefaultICDFrames <= 0 {
		opts.DefaultICDFrames = DefaultICDFrames
	}

	r := &ReactionResolver{
		opts:       opts,
		targets:    make(map[string]*targetState),
		decay:      make(map[string]int),
		icd:        make(map[string]int),
		lastReact:  make(map[cooldownKey]int64),
		suppressed: make(map[string]int),
	}
	for _, element := range []string{ElementPyro, ElementHydro, ElementCryo, ElementElectro} {
		r.decay[element] = DefaultDecay
	}
	return r
}

// Attach applies one elemental attachment to a target at the given time.
func (r *ReactionResolver) Attach(target, element string, gauge int, at int64) error {
	if strings.TrimSpace(target) == "" {
		return ErrBlankTarget
	}
	if !knownElement(element) {
		return ErrUnknownElement
	}
	if gauge <= 0 {
		return ErrBadGauge
	}
	if at < 0 {
		return ErrBadTime
	}
	at = alignTime(at)

	r.mu.Lock()
	defer r.mu.Unlock()

	state, ok := r.targets[target]
	if !ok {
		if len(r.targets) >= r.opts.MaxTargets {
			return ErrBadOptions
		}
		state = &targetState{}
		r.targets[target] = state
	}

	live := make([]auraState, 0, len(state.Auras)+1)
	sameElement := -1
	for _, aura := range state.Auras {
		remaining := r.remainingLocked(aura, at)
		if remaining <= 0 {
			continue
		}
		if aura.Element == element && sameElement < 0 {
			sameElement = len(live)
			if gauge > remaining {
				live = append(live, auraState{Element: element, Gauge: gauge, AttachedAt: at})
			} else {
				live = append(live, aura)
			}
			continue
		}
		live = append(live, aura)
	}

	if sameElement >= 0 {
		state.Auras = live
		return nil
	}

	next := auraState{Element: element, Gauge: gauge, AttachedAt: at}
	if len(live) >= r.opts.MaxAuras {
		live[weakestAuraIndex(live, at, r.decay)] = next
	} else {
		live = append(live, next)
	}
	state.Auras = live
	return nil
}

// React resolves one incoming hit against the auras of its target.
func (r *ReactionResolver) React(hit ReactHit) (ReactionResult, error) {
	if strings.TrimSpace(hit.Target) == "" {
		return ReactionResult{}, ErrBlankTarget
	}
	if !knownElement(hit.Element) {
		return ReactionResult{}, ErrUnknownElement
	}
	if hit.Gauge <= 0 {
		return ReactionResult{}, ErrBadGauge
	}
	if hit.At < 0 {
		return ReactionResult{}, ErrBadTime
	}
	hit.At = alignTime(hit.At)

	r.mu.Lock()
	defer r.mu.Unlock()

	state := r.targets[hit.Target]
	if state == nil {
		return ReactionResult{}, nil
	}

	live := make([]auraState, 0, len(state.Auras))
	remaining := make([]int, 0, len(state.Auras))
	for _, aura := range state.Auras {
		gauge := r.remainingLocked(aura, hit.At)
		if gauge <= 0 {
			continue
		}
		live = append(live, aura)
		remaining = append(remaining, gauge)
	}

	auraIndex, reaction := chooseReaction(live, hit.Element)
	if auraIndex < 0 {
		state.Auras = live
		return ReactionResult{}, nil
	}

	key := cooldownKey{Target: hit.Target, SkillID: hit.SkillID}
	if at, ok := r.lastReact[key]; ok && inICDWindow(at, hit.At, r.icdWindowLocked(hit.SkillID)) {
		r.suppressed[hit.Target]++
		state.Auras = live
		return ReactionResult{Suppressed: true}, nil
	}

	reactedElement := live[auraIndex].Element
	consumed := remaining[auraIndex]
	if hit.Gauge < consumed {
		consumed = hit.Gauge
	}
	if consumed >= remaining[auraIndex] {
		live = append(live[:auraIndex], live[auraIndex+1:]...)
	} else {
		live[auraIndex].Gauge = remaining[auraIndex] - consumed
		live[auraIndex].AttachedAt = hit.At
	}
	state.Auras = live

	r.lastReact[key] = hit.At
	factor := ReactionFactor(reaction)
	return ReactionResult{
		Reaction: reaction,
		Element:  reactedElement,
		Consumed: consumed,
		Factor:   factor,
		Damage:   hit.Damage * int64(factor) / 100,
	}, nil
}

// Aura returns the live auras of a target at the given time, sorted by element.
func (r *ReactionResolver) Aura(target string, at int64) []Aura {
	if strings.TrimSpace(target) == "" {
		return nil
	}
	at = alignTime(at)

	r.mu.Lock()
	defer r.mu.Unlock()

	state := r.targets[target]
	if state == nil {
		return nil
	}
	out := make([]Aura, 0, len(state.Auras))
	for _, aura := range state.Auras {
		gauge := r.remainingLocked(aura, at)
		if gauge <= 0 {
			continue
		}
		out = append(out, Aura{Element: aura.Element, Gauge: gauge})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Element < out[j].Element
	})
	return out
}

// SetICD sets the reaction cooldown of one skill in whole frames.
func (r *ReactionResolver) SetICD(skillID string, frames int) error {
	if frames < 0 {
		return ErrBadFrames
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.icd[skillID] = frames
	return nil
}

// ICDWindow returns the configured cooldown of one skill in frames.
func (r *ReactionResolver) ICDWindow(skillID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.icdWindowLocked(skillID)
}

// SetDecay sets the per tick decay rate of one element.
func (r *ReactionResolver) SetDecay(element string, perTick int) error {
	if !knownElement(element) {
		return ErrUnknownElement
	}
	if perTick <= 0 {
		return ErrBadDecay
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.decay[element] = perTick
	return nil
}

// DecayOf returns the per tick decay rate of one element.
func (r *ReactionResolver) DecayOf(element string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.decay[element]
}

// Clear removes every aura and cooldown record of one target.
func (r *ReactionResolver) Clear(target string) error {
	if strings.TrimSpace(target) == "" {
		return ErrBlankTarget
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.targets, target)
	delete(r.suppressed, target)
	for key := range r.lastReact {
		if key.Target == target {
			delete(r.lastReact, key)
		}
	}
	return nil
}

// SuppressedCount returns how many hits of one target were suppressed.
func (r *ReactionResolver) SuppressedCount(target string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.suppressed[target]
}

// Snapshot serialises auras, decay rates and cooldown windows.
func (r *ReactionResolver) Snapshot() ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	snapshot := snapshotState{Version: snapshotVersion}
	targetIDs := make([]string, 0, len(r.targets))
	for target := range r.targets {
		targetIDs = append(targetIDs, target)
	}
	sort.Strings(targetIDs)
	for _, target := range targetIDs {
		state := r.targets[target]
		entry := snapshotTarget{Target: target, Auras: make([]snapshotAura, 0, len(state.Auras))}
		for _, aura := range state.Auras {
			entry.Auras = append(entry.Auras, snapshotAura{
				Element:    aura.Element,
				Gauge:      aura.Gauge,
				AttachedAt: aura.AttachedAt,
			})
		}
		sort.Slice(entry.Auras, func(i, j int) bool {
			return entry.Auras[i].Element < entry.Auras[j].Element
		})
		snapshot.Targets = append(snapshot.Targets, entry)
	}

	elements := make([]string, 0, len(r.decay))
	for element := range r.decay {
		elements = append(elements, element)
	}
	sort.Strings(elements)
	for _, element := range elements {
		snapshot.Decay = append(snapshot.Decay, snapshotDecay{Element: element, PerTick: r.decay[element]})
	}

	skills := make([]string, 0, len(r.icd))
	for skillID := range r.icd {
		skills = append(skills, skillID)
	}
	sort.Strings(skills)
	for _, skillID := range skills {
		snapshot.ICD = append(snapshot.ICD, snapshotICD{SkillID: skillID, Frames: r.icd[skillID]})
	}

	keys := make([]cooldownKey, 0, len(r.lastReact))
	for key := range r.lastReact {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Target != keys[j].Target {
			return keys[i].Target < keys[j].Target
		}
		return keys[i].SkillID < keys[j].SkillID
	})
	for _, key := range keys {
		snapshot.Reactions = append(snapshot.Reactions, snapshotReaction{
			Target:  key.Target,
			SkillID: key.SkillID,
			LastAt:  r.lastReact[key],
		})
	}

	suppressedTargets := make([]string, 0, len(r.suppressed))
	for target := range r.suppressed {
		suppressedTargets = append(suppressedTargets, target)
	}
	sort.Strings(suppressedTargets)
	for _, target := range suppressedTargets {
		snapshot.Suppressed = append(snapshot.Suppressed, snapshotSuppressed{
			Target: target,
			Count:  r.suppressed[target],
		})
	}

	data, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (r *ReactionResolver) Restore(data []byte) error {
	var snapshot snapshotState
	if err := json.Unmarshal(data, &snapshot); err != nil || snapshot.Version != snapshotVersion {
		return ErrBadSnapshot
	}

	targets := make(map[string]*targetState, len(snapshot.Targets))
	decay := make(map[string]int, len(snapshot.Decay))
	for _, element := range []string{ElementPyro, ElementHydro, ElementCryo, ElementElectro} {
		decay[element] = DefaultDecay
	}
	icd := make(map[string]int, len(snapshot.ICD))
	lastReact := make(map[cooldownKey]int64, len(snapshot.Reactions))
	suppressed := make(map[string]int, len(snapshot.Suppressed))
	targetIDs := make(map[string]struct{}, len(snapshot.Targets))
	allTargets := make(map[string]struct{})

	for _, entry := range snapshot.Targets {
		if strings.TrimSpace(entry.Target) == "" {
			return ErrBadSnapshot
		}
		if _, exists := targetIDs[entry.Target]; exists {
			return ErrBadSnapshot
		}
		targetIDs[entry.Target] = struct{}{}
		allTargets[entry.Target] = struct{}{}
		if len(entry.Auras) > r.opts.MaxAuras {
			return ErrBadSnapshot
		}
		state := &targetState{Auras: make([]auraState, 0, len(entry.Auras))}
		seenElements := make(map[string]struct{}, len(entry.Auras))
		for _, aura := range entry.Auras {
			if !knownElement(aura.Element) || aura.Gauge <= 0 || aura.AttachedAt < 0 ||
				aura.AttachedAt%TickMs != 0 {
				return ErrBadSnapshot
			}
			if _, exists := seenElements[aura.Element]; exists {
				return ErrBadSnapshot
			}
			seenElements[aura.Element] = struct{}{}
			state.Auras = append(state.Auras, auraState{
				Element:    aura.Element,
				Gauge:      aura.Gauge,
				AttachedAt: aura.AttachedAt,
			})
		}
		targets[entry.Target] = state
	}

	seenDecay := make(map[string]struct{}, len(snapshot.Decay))
	for _, entry := range snapshot.Decay {
		if !knownElement(entry.Element) || entry.PerTick <= 0 {
			return ErrBadSnapshot
		}
		if _, exists := seenDecay[entry.Element]; exists {
			return ErrBadSnapshot
		}
		seenDecay[entry.Element] = struct{}{}
		decay[entry.Element] = entry.PerTick
	}

	for _, entry := range snapshot.ICD {
		if entry.Frames < 0 {
			return ErrBadSnapshot
		}
		if _, exists := icd[entry.SkillID]; exists {
			return ErrBadSnapshot
		}
		icd[entry.SkillID] = entry.Frames
	}

	for _, entry := range snapshot.Reactions {
		if strings.TrimSpace(entry.Target) == "" || entry.LastAt < 0 || entry.LastAt%TickMs != 0 {
			return ErrBadSnapshot
		}
		key := cooldownKey{Target: entry.Target, SkillID: entry.SkillID}
		if _, exists := lastReact[key]; exists {
			return ErrBadSnapshot
		}
		lastReact[key] = entry.LastAt
		allTargets[entry.Target] = struct{}{}
	}

	for _, entry := range snapshot.Suppressed {
		if strings.TrimSpace(entry.Target) == "" || entry.Count < 0 {
			return ErrBadSnapshot
		}
		if _, exists := suppressed[entry.Target]; exists {
			return ErrBadSnapshot
		}
		suppressed[entry.Target] = entry.Count
		allTargets[entry.Target] = struct{}{}
	}

	if len(allTargets) > r.opts.MaxTargets {
		return ErrBadSnapshot
	}

	r.mu.Lock()
	r.targets = targets
	r.decay = decay
	r.icd = icd
	r.lastReact = lastReact
	r.suppressed = suppressed
	r.mu.Unlock()
	return nil
}

const snapshotVersion = 1

type snapshotState struct {
	Version    int                  `json:"version"`
	Targets    []snapshotTarget     `json:"targets,omitempty"`
	Decay      []snapshotDecay      `json:"decay,omitempty"`
	ICD        []snapshotICD        `json:"icd,omitempty"`
	Reactions  []snapshotReaction   `json:"reactions,omitempty"`
	Suppressed []snapshotSuppressed `json:"suppressed,omitempty"`
}

type snapshotTarget struct {
	Target string         `json:"target"`
	Auras  []snapshotAura `json:"auras,omitempty"`
}

type snapshotAura struct {
	Element    string `json:"element"`
	Gauge      int    `json:"gauge"`
	AttachedAt int64  `json:"attached_at"`
}

type snapshotDecay struct {
	Element string `json:"element"`
	PerTick int    `json:"per_tick"`
}

type snapshotICD struct {
	SkillID string `json:"skill_id"`
	Frames  int    `json:"frames"`
}

type snapshotReaction struct {
	Target  string `json:"target"`
	SkillID string `json:"skill_id"`
	LastAt  int64  `json:"last_at"`
}

type snapshotSuppressed struct {
	Target string `json:"target"`
	Count  int    `json:"count"`
}

func knownElement(element string) bool {
	switch element {
	case ElementPyro, ElementHydro, ElementCryo, ElementElectro:
		return true
	default:
		return false
	}
}

func alignTime(at int64) int64 {
	if at <= 0 {
		return 0
	}
	return at - at%TickMs
}

func (r *ReactionResolver) remainingLocked(aura auraState, at int64) int {
	if at <= aura.AttachedAt {
		return aura.Gauge
	}
	rate := r.decay[aura.Element]
	if rate <= 0 {
		return aura.Gauge
	}
	ticks := (at - aura.AttachedAt) / TickMs
	if ticks <= 0 {
		return aura.Gauge
	}
	if ticks > int64(aura.Gauge)/int64(rate) {
		return 0
	}
	decayed := ticks * int64(rate)
	if decayed >= int64(aura.Gauge) {
		return 0
	}
	return aura.Gauge - int(decayed)
}

func weakestAuraIndex(auras []auraState, at int64, decay map[string]int) int {
	weakest := 0
	weakestGauge := remainingForDecay(auras[0], at, decay)
	for i := 1; i < len(auras); i++ {
		gauge := remainingForDecay(auras[i], at, decay)
		if gauge < weakestGauge ||
			(gauge == weakestGauge && auras[i].Element < auras[weakest].Element) {
			weakest = i
			weakestGauge = gauge
		}
	}
	return weakest
}

func remainingForDecay(aura auraState, at int64, decay map[string]int) int {
	if at <= aura.AttachedAt {
		return aura.Gauge
	}
	rate := decay[aura.Element]
	if rate <= 0 {
		return aura.Gauge
	}
	ticks := (at - aura.AttachedAt) / TickMs
	if ticks <= 0 || ticks > int64(aura.Gauge)/int64(rate) {
		return 0
	}
	decayed := ticks * int64(rate)
	if decayed >= int64(aura.Gauge) {
		return 0
	}
	return aura.Gauge - int(decayed)
}

func chooseReaction(auras []auraState, hitElement string) (int, string) {
	for _, reaction := range reactionOrder {
		for i, aura := range auras {
			if reactionForPair(hitElement, aura.Element) == reaction {
				return i, reaction
			}
		}
	}
	return -1, ""
}

func reactionForPair(hitElement, auraElement string) string {
	switch hitElement {
	case ElementPyro:
		switch auraElement {
		case ElementHydro:
			return ReactionVaporize
		case ElementCryo:
			return ReactionMelt
		case ElementElectro:
			return ReactionOverload
		}
	case ElementHydro:
		switch auraElement {
		case ElementPyro:
			return ReactionVaporize
		case ElementCryo:
			return ReactionFreeze
		case ElementElectro:
			return ReactionElectrocharged
		}
	case ElementCryo:
		switch auraElement {
		case ElementPyro:
			return ReactionMelt
		case ElementHydro:
			return ReactionFreeze
		case ElementElectro:
			return ReactionSuperconduct
		}
	case ElementElectro:
		switch auraElement {
		case ElementPyro:
			return ReactionOverload
		case ElementHydro:
			return ReactionElectrocharged
		case ElementCryo:
			return ReactionSuperconduct
		}
	}
	return ""
}

func (r *ReactionResolver) icdWindowLocked(skillID string) int {
	if frames, ok := r.icd[skillID]; ok {
		return frames
	}
	return r.opts.DefaultICDFrames
}

func inICDWindow(last, at int64, frames int) bool {
	if frames <= 0 || at < last {
		return false
	}
	const maxInt64 = int64(1<<63 - 1)
	if int64(frames) > maxInt64/TickMs {
		return true
	}
	return at-last < int64(frames)*TickMs
}
