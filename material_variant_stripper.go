// Package task resolves material keyword sets into ordered variant keys,
// enforces exclusive keyword groups and strips variants nobody references.
package task

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
)

// KeySeparator joins the keyword names of one variant key.
const KeySeparator = "+"

// BaseVariant is the key of the variant without any keyword set.
const BaseVariant = VariantKey("")

// Errors returned by the stripper.
var (
	ErrBlankKeyword      = errors.New("keyword name is required")
	ErrBlankGroup        = errors.New("keyword group is required")
	ErrDuplicateKeyword  = errors.New("keyword is already declared")
	ErrUnknownKeyword    = errors.New("keyword is not declared")
	ErrDependencyCycle   = errors.New("keyword dependency cycle")
	ErrExclusiveConflict = errors.New("two keywords of the same exclusive group are set")
	ErrUnknownVariant    = errors.New("variant key was never resolved")
	ErrUnorderedVariant  = errors.New("variant key is not sorted")
	ErrBadSnapshot       = errors.New("snapshot payload is invalid")
)

// VariantKey is the canonical name of one keyword combination.
type VariantKey string

// MaterialRef is one material referencing a variant it wants compiled.
type MaterialRef struct {
	MaterialID string     `json:"material_id"`
	Key        VariantKey `json:"key"`
}

// VariantSet is the trimmed list of variants that survived the strip pass.
type VariantSet struct {
	Keys    []VariantKey `json:"keys"`
	Dropped int          `json:"dropped"`
}

// VariantInfo explains how one variant key was assembled.
type VariantInfo struct {
	Key      VariantKey `json:"key"`
	Explicit []string   `json:"explicit"`
	Closure  []string   `json:"closure"`
	Disabled []string   `json:"disabled"`
}

type keywordSpec struct {
	group string
	deps  []string
}

type variantRecord struct {
	explicit []string
	closure  []string
	disabled []string
}

// VariantStripper owns the keyword table, the resolved variants and the
// per variant reference counters.
type VariantStripper struct {
	mu       sync.RWMutex
	keywords map[string]*keywordSpec
	variants map[VariantKey]*variantRecord
	refs     map[VariantKey]int
}

// NewVariantStripper builds an empty stripper.
func NewVariantStripper() *VariantStripper {
	return &VariantStripper{
		keywords: make(map[string]*keywordSpec),
		variants: make(map[VariantKey]*variantRecord),
		refs:     make(map[VariantKey]int),
	}
}

func blank(value string) bool {
	return strings.TrimSpace(value) == ""
}

// normalizeNames removes duplicates and orders names canonically.
func normalizeNames(names []string) []string {
	out := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func copyNames(names []string) []string {
	out := make([]string, len(names))
	copy(out, names)
	return out
}

func unionNames(left, right []string) []string {
	combined := make([]string, 0, len(left)+len(right))
	combined = append(combined, left...)
	combined = append(combined, right...)
	return normalizeNames(combined)
}

func withoutNames(names []string, excluded map[string]bool) []string {
	out := make([]string, 0, len(names))
	for _, name := range names {
		if !excluded[name] {
			out = append(out, name)
		}
	}
	return out
}

func containsName(names []string, target string) bool {
	index := sort.SearchStrings(names, target)
	return index < len(names) && names[index] == target
}

// hasCycle reports whether the proposed dependency graph contains a cycle.
// A dependency that is not part of the graph is ignored; callers validate
// unknown names before asking for a cycle check.
func hasCycle(graph map[string][]string) bool {
	const (
		unvisited = iota
		visiting
		visited
	)
	state := make(map[string]int, len(graph))

	var visit func(string) bool
	visit = func(name string) bool {
		switch state[name] {
		case visiting:
			return true
		case visited:
			return false
		}
		state[name] = visiting
		for _, dep := range graph[name] {
			if _, ok := graph[dep]; !ok {
				continue
			}
			if visit(dep) {
				return true
			}
		}
		state[name] = visited
		return false
	}

	names := make([]string, 0, len(graph))
	for name := range graph {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if visit(name) {
			return true
		}
	}
	return false
}

// DeclareKeyword registers one keyword with its exclusive group and the
// keywords it depends on.
func (s *VariantStripper) DeclareKeyword(name, group string, deps []string) error {
	if blank(name) {
		return ErrBlankKeyword
	}
	if blank(group) {
		return ErrBlankGroup
	}
	dependencies := normalizeNames(deps)

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.keywords[name]; exists {
		return ErrDuplicateKeyword
	}
	for _, dep := range dependencies {
		if dep == name {
			return ErrDependencyCycle
		}
		if _, exists := s.keywords[dep]; !exists {
			return ErrUnknownKeyword
		}
	}

	graph := make(map[string][]string, len(s.keywords)+1)
	for keyword, spec := range s.keywords {
		graph[keyword] = spec.deps
	}
	graph[name] = dependencies
	if hasCycle(graph) {
		return ErrDependencyCycle
	}

	s.keywords[name] = &keywordSpec{group: group, deps: dependencies}
	return nil
}

// SetDependencies replaces the dependency list of an already declared keyword.
func (s *VariantStripper) SetDependencies(name string, deps []string) error {
	dependencies := normalizeNames(deps)

	s.mu.Lock()
	defer s.mu.Unlock()

	spec, exists := s.keywords[name]
	if !exists {
		return ErrUnknownKeyword
	}
	for _, dep := range dependencies {
		if _, exists := s.keywords[dep]; !exists {
			return ErrUnknownKeyword
		}
	}

	graph := make(map[string][]string, len(s.keywords))
	for keyword, current := range s.keywords {
		graph[keyword] = current.deps
	}
	graph[name] = dependencies
	if hasCycle(graph) {
		return ErrDependencyCycle
	}

	spec.deps = dependencies
	return nil
}

// Group reports the exclusive group of a keyword.
func (s *VariantStripper) Group(name string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if spec, ok := s.keywords[name]; ok {
		return spec.group
	}
	return ""
}

// Resolve turns a keyword set into a canonical variant key and registers it.
func (s *VariantStripper) Resolve(keywords map[string]bool) (VariantKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	explicit := make([]string, 0, len(keywords))
	disabled := make([]string, 0, len(keywords))
	disabledSet := make(map[string]bool, len(keywords))
	for name, set := range keywords {
		if _, ok := s.keywords[name]; !ok {
			return "", ErrUnknownKeyword
		}
		if set {
			explicit = append(explicit, name)
			continue
		}
		disabled = append(disabled, name)
		disabledSet[name] = true
	}
	sort.Strings(explicit)
	sort.Strings(disabled)

	active := make(map[string]bool, len(s.keywords))
	var addWithDependencies func(string)
	addWithDependencies = func(name string) {
		if active[name] || disabledSet[name] {
			return
		}
		active[name] = true
		for _, dep := range s.keywords[name].deps {
			addWithDependencies(dep)
		}
	}
	for _, name := range explicit {
		addWithDependencies(name)
	}

	closure := make([]string, 0, len(active))
	for name := range active {
		closure = append(closure, name)
	}
	sort.Strings(closure)

	// Only explicitly set keywords compete for an exclusive group. Keywords
	// pulled in by a dependency closure are supporting keywords and may share
	// a group with the explicit root they support (for example fog supporting
	// snow).
	groups := make(map[string]string, len(explicit))
	for _, name := range explicit {
		group := s.keywords[name].group
		if previous, ok := groups[group]; ok && previous != name {
			return "", ErrExclusiveConflict
		}
		groups[group] = name
	}

	key := VariantKey(strings.Join(closure, KeySeparator))
	record := &variantRecord{
		explicit: copyNames(explicit),
		closure:  copyNames(closure),
		disabled: copyNames(disabled),
	}
	if previous := s.variants[key]; previous != nil {
		// Re-resolving the same key must not downgrade a keyword that was
		// explicitly set earlier merely because it is now reached through a
		// dependency closure.
		record.explicit = unionNames(previous.explicit, explicit)
		staleDisabled := unionNames(previous.disabled, disabled)
		record.disabled = withoutNames(staleDisabled, active)
	}
	s.variants[key] = record
	return key, nil
}

// Strip keeps the variants referenced by the given materials and recounts the
// reference table from scratch.
func (s *VariantStripper) Strip(refs []MaterialRef) (VariantSet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, ref := range refs {
		if _, ok := s.variants[ref.Key]; !ok {
			return VariantSet{}, ErrUnknownVariant
		}
	}

	counts := make(map[VariantKey]int, len(refs))
	for _, ref := range refs {
		counts[ref.Key]++
	}

	keys := make([]VariantKey, 0, len(s.variants))
	for key := range s.variants {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })

	kept := make([]VariantKey, 0, len(keys))
	dropped := 0
	for _, key := range keys {
		if counts[key] > 0 || key == BaseVariant {
			kept = append(kept, key)
			continue
		}
		dropped++
	}

	s.refs = counts
	return VariantSet{Keys: kept, Dropped: dropped}, nil
}

// RefCount reports how many materials reference one variant.
func (s *VariantStripper) RefCount(key VariantKey) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.refs[key]
}

// Variants lists every registered variant key in ascending order.
func (s *VariantStripper) Variants() []VariantKey {
	s.mu.RLock()
	defer s.mu.RUnlock()

	keys := make([]VariantKey, 0, len(s.variants))
	for key := range s.variants {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

// DefaultVariant is the variant used when no keyword is set.
func (s *VariantStripper) DefaultVariant() VariantKey {
	return BaseVariant
}

// Explain reports the explicit, closure and disabled keywords of a variant.
func (s *VariantStripper) Explain(key VariantKey) (VariantInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	record, ok := s.variants[key]
	if !ok {
		return VariantInfo{}, ErrUnknownVariant
	}
	return VariantInfo{
		Key:      key,
		Explicit: copyNames(record.explicit),
		Closure:  copyNames(record.closure),
		Disabled: copyNames(record.disabled),
	}, nil
}

// Validate checks that a variant key is registered and correctly ordered.
func (s *VariantStripper) Validate(key VariantKey) error {
	if key != BaseVariant {
		parts := strings.Split(string(key), KeySeparator)
		previous := ""
		for index, part := range parts {
			if part == "" {
				return ErrUnorderedVariant
			}
			if index > 0 && part <= previous {
				return ErrUnorderedVariant
			}
			previous = part
		}
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.variants[key]; !ok {
		return ErrUnknownVariant
	}
	return nil
}

const (
	snapshotFormat  = "gsb/material-variant-stripper"
	snapshotVersion = 1
)

type snapshotKeyword struct {
	Name  string   `json:"name"`
	Group string   `json:"group"`
	Deps  []string `json:"deps"`
}

type snapshotVariant struct {
	Key      VariantKey `json:"key"`
	Explicit []string   `json:"explicit"`
	Closure  []string   `json:"closure"`
	Disabled []string   `json:"disabled"`
}

type snapshotRef struct {
	Key   VariantKey `json:"key"`
	Count int        `json:"count"`
}

type snapshotState struct {
	Keywords []snapshotKeyword `json:"keywords"`
	Variants []snapshotVariant `json:"variants"`
	Refs     []snapshotRef     `json:"refs"`
}

type snapshotEnvelope struct {
	Format   string          `json:"format"`
	Version  int             `json:"version"`
	Payload  json.RawMessage `json:"payload"`
	Checksum string          `json:"checksum"`
}

// Snapshot serialises the keyword table, the resolved variants and the
// reference counters.
func (s *VariantStripper) Snapshot() ([]byte, error) {
	s.mu.RLock()

	state := snapshotState{
		Keywords: make([]snapshotKeyword, 0, len(s.keywords)),
		Variants: make([]snapshotVariant, 0, len(s.variants)),
		Refs:     make([]snapshotRef, 0, len(s.variants)),
	}

	keywordNames := make([]string, 0, len(s.keywords))
	for name := range s.keywords {
		keywordNames = append(keywordNames, name)
	}
	sort.Strings(keywordNames)
	for _, name := range keywordNames {
		spec := s.keywords[name]
		state.Keywords = append(state.Keywords, snapshotKeyword{
			Name:  name,
			Group: spec.group,
			Deps:  copyNames(spec.deps),
		})
	}

	variantKeys := make([]VariantKey, 0, len(s.variants))
	for key := range s.variants {
		variantKeys = append(variantKeys, key)
	}
	sort.Slice(variantKeys, func(i, j int) bool { return variantKeys[i] < variantKeys[j] })
	for _, key := range variantKeys {
		record := s.variants[key]
		state.Variants = append(state.Variants, snapshotVariant{
			Key:      key,
			Explicit: copyNames(record.explicit),
			Closure:  copyNames(record.closure),
			Disabled: copyNames(record.disabled),
		})
		state.Refs = append(state.Refs, snapshotRef{Key: key, Count: s.refs[key]})
	}
	s.mu.RUnlock()

	payload, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(payload)
	envelope := snapshotEnvelope{
		Format:   snapshotFormat,
		Version:  snapshotVersion,
		Payload:  payload,
		Checksum: hex.EncodeToString(digest[:]),
	}
	return json.Marshal(envelope)
}

func decodeSnapshot(state snapshotState) (map[string]*keywordSpec, map[VariantKey]*variantRecord, map[VariantKey]int, bool) {
	keywords := make(map[string]*keywordSpec, len(state.Keywords))
	graph := make(map[string][]string, len(state.Keywords))
	for _, entry := range state.Keywords {
		if blank(entry.Name) || blank(entry.Group) {
			return nil, nil, nil, false
		}
		if _, exists := keywords[entry.Name]; exists {
			return nil, nil, nil, false
		}
		deps := normalizeNames(entry.Deps)
		keywords[entry.Name] = &keywordSpec{group: entry.Group, deps: deps}
		graph[entry.Name] = deps
	}
	for _, spec := range keywords {
		for _, dep := range spec.deps {
			if _, exists := keywords[dep]; !exists {
				return nil, nil, nil, false
			}
		}
	}
	if hasCycle(graph) {
		return nil, nil, nil, false
	}

	variants := make(map[VariantKey]*variantRecord, len(state.Variants))
	for _, entry := range state.Variants {
		if _, exists := variants[entry.Key]; exists {
			return nil, nil, nil, false
		}
		closure := normalizeNames(entry.Closure)
		explicit := normalizeNames(entry.Explicit)
		disabled := normalizeNames(entry.Disabled)

		for _, name := range closure {
			if _, exists := keywords[name]; !exists {
				return nil, nil, nil, false
			}
		}
		for _, name := range explicit {
			if _, exists := keywords[name]; !exists || !containsName(closure, name) {
				return nil, nil, nil, false
			}
		}
		for _, name := range disabled {
			if _, exists := keywords[name]; !exists || containsName(closure, name) {
				return nil, nil, nil, false
			}
		}
		if VariantKey(strings.Join(closure, KeySeparator)) != entry.Key {
			return nil, nil, nil, false
		}

		variants[entry.Key] = &variantRecord{
			explicit: explicit,
			closure:  closure,
			disabled: disabled,
		}
	}

	refs := make(map[VariantKey]int, len(state.Refs))
	for _, entry := range state.Refs {
		if entry.Count < 0 {
			return nil, nil, nil, false
		}
		if _, exists := variants[entry.Key]; !exists {
			return nil, nil, nil, false
		}
		if _, duplicate := refs[entry.Key]; duplicate {
			return nil, nil, nil, false
		}
		if entry.Count > 0 {
			refs[entry.Key] = entry.Count
		}
	}

	return keywords, variants, refs, true
}

// Restore replaces the whole stripper state with a snapshot, all or nothing.
func (s *VariantStripper) Restore(data []byte) error {
	var envelope snapshotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return ErrBadSnapshot
	}
	if envelope.Format != snapshotFormat ||
		envelope.Version != snapshotVersion ||
		len(envelope.Payload) == 0 ||
		envelope.Checksum == "" {
		return ErrBadSnapshot
	}
	checksum, err := hex.DecodeString(envelope.Checksum)
	if err != nil || len(checksum) != sha256.Size {
		return ErrBadSnapshot
	}

	var state snapshotState
	if err := json.Unmarshal(envelope.Payload, &state); err != nil {
		return ErrBadSnapshot
	}
	// Check the checksum against the canonical encoding of the decoded state,
	// so harmless JSON reformatting still restores while any semantic change
	// is rejected.
	canonical, err := json.Marshal(state)
	if err != nil {
		return ErrBadSnapshot
	}
	digest := sha256.Sum256(canonical)
	if !bytes.Equal(checksum, digest[:]) {
		return ErrBadSnapshot
	}
	keywords, variants, refs, ok := decodeSnapshot(state)
	if !ok {
		return ErrBadSnapshot
	}

	s.mu.Lock()
	s.keywords = keywords
	s.variants = variants
	s.refs = refs
	s.mu.Unlock()
	return nil
}
