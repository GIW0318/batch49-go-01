// Package task resolves layered rule package scopes for the scanning platform.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"
)

// Value kinds understood by the resolver.
const (
	KindNum  = "num"
	KindBool = "bool"
	KindEnum = "enum"
)

// Numeric bounds: BoundMax means a smaller number is stricter, BoundMin means a
// larger number is stricter.
const (
	BoundMax = "max"
	BoundMin = "min"
)

// Layer actions reported by Explain and Match.
const (
	ActionDeclare = "declare"
	ActionInherit = "inherit"
	ActionNarrow  = "narrow"
)

// Errors returned by the resolver.
var (
	ErrBadPackage       = errors.New("rule package definition is invalid")
	ErrDuplicatePackage = errors.New("rule package is already defined")
	ErrUnknownPackage   = errors.New("rule package is not defined")
	ErrUnknownKey       = errors.New("rule key is not defined in the package chain")
	ErrBadValue         = errors.New("rule value is invalid")
	ErrKindConflict     = errors.New("rule value kind conflicts with the parent layer")
	ErrWiden            = errors.New("override would widen the parent constraint")
	ErrConflict         = errors.New("override conflicts with the parent constraint")
	ErrNoMatch          = errors.New("pattern matched no rule key")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

// Value is one declared constraint inside a package layer.
type Value struct {
	Kind  string   `json:"kind"`
	Num   int64    `json:"num,omitempty"`
	Bound string   `json:"bound,omitempty"`
	Bool  bool     `json:"bool,omitempty"`
	Set   []string `json:"set,omitempty"`
}

// Package is one layer of the rule package tree. Parent is the package name of
// the layer this package inherits from; an empty Parent marks a root package.
type Package struct {
	Name   string           `json:"name"`
	Parent string           `json:"parent,omitempty"`
	Values map[string]Value `json:"values,omitempty"`
}

// Layer is one entry of the resolution trace of a single key.
type Layer struct {
	Package string `json:"package"`
	Key     string `json:"key"`
	Value   Value  `json:"value"`
	Action  string `json:"action"`
}

// Effective is the resolved view of one package.
type Effective struct {
	Package string           `json:"package"`
	Chain   []string         `json:"chain"`
	Values  map[string]Value `json:"values"`
}

// Match is one rule key selected by a wildcard pattern.
type Match struct {
	Key    string  `json:"key"`
	Value  Value   `json:"value"`
	Layers []Layer `json:"layers"`
}

// RuleScopeResolver owns the package tree and the cache of resolved snapshots.
type RuleScopeResolver struct {
	mu       sync.RWMutex
	packages map[string]Package
	cache    map[string]Effective
}

// NewRuleScopeResolver builds an empty resolver.
func NewRuleScopeResolver() *RuleScopeResolver {
	return &RuleScopeResolver{
		packages: map[string]Package{},
		cache:    map[string]Effective{},
	}
}

// Define registers one rule package layer.
func (r *RuleScopeResolver) Define(pkg Package) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if strings.TrimSpace(pkg.Name) == "" {
		return ErrBadPackage
	}
	if r.packages == nil {
		r.packages = map[string]Package{}
	}
	if _, ok := r.packages[pkg.Name]; ok {
		return ErrDuplicatePackage
	}
	if pkg.Parent != "" {
		if _, ok := r.packages[pkg.Parent]; !ok {
			return ErrUnknownPackage
		}
	}

	normalized, err := normalizePackage(pkg)
	if err != nil {
		return err
	}

	// Install only for the duration of validation. If resolution finds a kind
	// conflict or an empty enum intersection, the declaration is rolled back.
	r.packages[normalized.Name] = normalized
	if _, err := resolveState(r.packages, normalized.Name); err != nil {
		delete(r.packages, normalized.Name)
		return err
	}
	return nil
}

// Resolve returns the effective constraints of one package.
func (r *RuleScopeResolver) Resolve(name string) (Effective, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	resolved, err := resolveState(r.packages, name)
	if err != nil {
		return Effective{}, err
	}
	return copyEffective(resolved.effective), nil
}

// Override narrows one key of an already defined package.
func (r *RuleScopeResolver) Override(name, key string, v Value) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	pkg, ok := r.packages[name]
	if !ok {
		return ErrUnknownPackage
	}
	resolved, err := resolveState(r.packages, name)
	if err != nil {
		return err
	}
	current, ok := resolved.effective.Values[key]
	if !ok {
		return ErrUnknownKey
	}

	normalized, err := normalizeValue(v)
	if err != nil {
		return err
	}
	if err := validateNarrower(current, resolved.sources[key], key, normalized); err != nil {
		return err
	}

	updated := clonePackage(pkg)
	if updated.Values == nil {
		updated.Values = map[string]Value{}
	}
	updated.Values[key] = normalized

	// Validate the whole tree against the prospective change before committing,
	// so a narrowing that would break a descendant is rejected atomically.
	trial := make(map[string]Package, len(r.packages))
	for pkgName, pkgValue := range r.packages {
		trial[pkgName] = pkgValue
	}
	trial[name] = updated
	for _, pkgName := range sortedPackageNames(trial) {
		if _, err := resolveState(trial, pkgName); err != nil {
			return err
		}
	}

	r.packages[name] = updated
	return nil
}

// Explain returns the per layer trace of one resolved key.
func (r *RuleScopeResolver) Explain(name, key string) []Layer {
	r.mu.RLock()
	defer r.mu.RUnlock()

	resolved, err := resolveState(r.packages, name)
	if err != nil {
		return nil
	}
	layers, ok := resolved.traces[key]
	if !ok {
		return nil
	}
	return copyLayers(layers)
}

// Digest returns the stable digest of the resolved package.
func (r *RuleScopeResolver) Digest(name string) (string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	resolved, err := resolveState(r.packages, name)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(resolved.effective)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// Chain returns the ancestor chain of a package, root first.
func (r *RuleScopeResolver) Chain(name string) ([]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	chain, err := chainState(r.packages, name)
	if err != nil {
		return nil, err
	}
	return append([]string(nil), chain...), nil
}

// Keys returns every resolved key of a package in lexicographic order.
func (r *RuleScopeResolver) Keys(name string) ([]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	resolved, err := resolveState(r.packages, name)
	if err != nil {
		return nil, err
	}
	return sortedValueKeys(resolved.effective.Values), nil
}

// Match resolves every key selected by a wildcard field path.
func (r *RuleScopeResolver) Match(name, pattern string) ([]Match, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	resolved, err := resolveState(r.packages, name)
	if err != nil {
		return nil, err
	}

	keys := sortedValueKeys(resolved.effective.Values)
	matches := make([]Match, 0)
	for _, key := range keys {
		selected, matchErr := path.Match(pattern, key)
		if matchErr != nil {
			return nil, ErrNoMatch
		}
		if !selected {
			continue
		}
		matches = append(matches, Match{
			Key:    key,
			Value:  cloneValue(resolved.effective.Values[key]),
			Layers: copyLayers(resolved.traces[key]),
		})
	}
	if len(matches) == 0 {
		return nil, ErrNoMatch
	}
	return matches, nil
}

// Packages lists every defined package name in lexicographic order.
func (r *RuleScopeResolver) Packages() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.packages))
	for name := range r.packages {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

const snapshotVersion = 1

type snapshotPayload struct {
	Version  int       `json:"version"`
	Packages []Package `json:"packages"`
}

type snapshotEnvelope struct {
	Digest  string `json:"digest"`
	Payload []byte `json:"payload"`
}

// Snapshot serialises the package tree and the resolved cache.
func (r *RuleScopeResolver) Snapshot() ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	names := sortedPackageNames(r.packages)
	packages := make([]Package, 0, len(names))
	for _, name := range names {
		pkg := r.packages[name]
		packages = append(packages, Package{
			Name:   pkg.Name,
			Parent: pkg.Parent,
			Values: cloneValueMap(pkg.Values),
		})
	}

	payload, err := json.Marshal(snapshotPayload{
		Version:  snapshotVersion,
		Packages: packages,
	})
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(payload)
	return json.Marshal(snapshotEnvelope{
		Digest:  hex.EncodeToString(sum[:]),
		Payload: payload,
	})
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (r *RuleScopeResolver) Restore(data []byte) error {
	var envelope snapshotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return ErrBadSnapshot
	}
	if len(envelope.Payload) == 0 {
		return ErrBadSnapshot
	}
	sum := sha256.Sum256(envelope.Payload)
	if envelope.Digest != hex.EncodeToString(sum[:]) {
		return ErrBadSnapshot
	}

	var payload snapshotPayload
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return ErrBadSnapshot
	}
	if payload.Version != snapshotVersion {
		return ErrBadSnapshot
	}

	packages, err := rebuildPackages(payload.Packages)
	if err != nil {
		return ErrBadSnapshot
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.packages = packages
	r.cache = map[string]Effective{}
	return nil
}

type resolvedPackage struct {
	effective Effective
	traces    map[string][]Layer
	sources   map[string]string
}

func normalizePackage(pkg Package) (Package, error) {
	normalized := Package{
		Name:   pkg.Name,
		Parent: pkg.Parent,
		Values: make(map[string]Value, len(pkg.Values)),
	}
	for key, value := range pkg.Values {
		value, err := normalizeValue(value)
		if err != nil {
			return Package{}, err
		}
		normalized.Values[key] = value
	}
	return normalized, nil
}

func normalizeValue(value Value) (Value, error) {
	switch value.Kind {
	case KindNum:
		if value.Bound != BoundMax && value.Bound != BoundMin {
			return Value{}, ErrBadValue
		}
		return Value{Kind: KindNum, Num: value.Num, Bound: value.Bound}, nil
	case KindBool:
		return Value{Kind: KindBool, Bool: value.Bool}, nil
	case KindEnum:
		if len(value.Set) == 0 {
			return Value{}, ErrBadValue
		}
		seen := make(map[string]struct{}, len(value.Set))
		set := make([]string, 0, len(value.Set))
		for _, item := range value.Set {
			if _, ok := seen[item]; ok {
				continue
			}
			seen[item] = struct{}{}
			set = append(set, item)
		}
		sort.Strings(set)
		return Value{Kind: KindEnum, Set: set}, nil
	default:
		return Value{}, ErrBadValue
	}
}

func resolveState(packages map[string]Package, name string) (resolvedPackage, error) {
	chain, err := chainState(packages, name)
	if err != nil {
		return resolvedPackage{}, err
	}

	keys := make(map[string]struct{})
	for _, pkgName := range chain {
		for key := range packages[pkgName].Values {
			keys[key] = struct{}{}
		}
	}

	effective := Effective{
		Package: name,
		Chain:   append([]string(nil), chain...),
		Values:  make(map[string]Value, len(keys)),
	}
	traces := make(map[string][]Layer, len(keys))
	sources := make(map[string]string, len(keys))

	for _, key := range sortedKeys(keys) {
		var current Value
		var declared bool
		var source string
		trace := make([]Layer, 0, len(chain))

		for _, pkgName := range chain {
			value, ok := packages[pkgName].Values[key]
			if !ok {
				trace = append(trace, Layer{
					Package: pkgName,
					Key:     key,
					Value:   cloneValue(current),
					Action:  ActionInherit,
				})
				continue
			}
			if !declared {
				current = cloneValue(value)
				source = pkgName
				declared = true
				trace = append(trace, Layer{
					Package: pkgName,
					Key:     key,
					Value:   cloneValue(current),
					Action:  ActionDeclare,
				})
				continue
			}

			narrowed, tightened, err := mergeValue(current, value)
			if err != nil {
				return resolvedPackage{}, err
			}
			current = narrowed
			if tightened {
				source = pkgName
			}
			trace = append(trace, Layer{
				Package: pkgName,
				Key:     key,
				Value:   cloneValue(current),
				Action:  ActionNarrow,
			})
		}

		effective.Values[key] = current
		traces[key] = trace
		sources[key] = source
	}

	return resolvedPackage{
		effective: effective,
		traces:    traces,
		sources:   sources,
	}, nil
}

func mergeValue(current, next Value) (Value, bool, error) {
	if current.Kind != next.Kind {
		return Value{}, false, ErrKindConflict
	}

	switch current.Kind {
	case KindNum:
		if current.Bound != next.Bound {
			return Value{}, false, ErrConflict
		}
		if current.Bound == BoundMax {
			if next.Num < current.Num {
				return cloneValue(next), true, nil
			}
			return cloneValue(current), false, nil
		}
		if next.Num > current.Num {
			return cloneValue(next), true, nil
		}
		return cloneValue(current), false, nil
	case KindBool:
		narrowed := current.Bool && next.Bool
		return Value{Kind: KindBool, Bool: narrowed}, narrowed != current.Bool, nil
	case KindEnum:
		intersection := intersect(current.Set, next.Set)
		if len(intersection) == 0 {
			return Value{}, false, ErrConflict
		}
		return Value{Kind: KindEnum, Set: intersection}, len(intersection) != len(current.Set), nil
	default:
		return Value{}, false, ErrBadValue
	}
}

func validateNarrower(current Value, source, key string, next Value) error {
	if current.Kind != next.Kind {
		return fmt.Errorf("%w: key %q conflicts with layer %q", ErrKindConflict, key, source)
	}
	if source == "" {
		source = "unknown"
	}

	switch current.Kind {
	case KindNum:
		if current.Bound != next.Bound {
			return fmt.Errorf("%w: key %q in layer %q has incompatible numeric bounds", ErrConflict, key, source)
		}
		if current.Bound == BoundMax && next.Num > current.Num {
			return fmt.Errorf("%w: layer %q constraint on key %q would be widened", ErrWiden, source, key)
		}
		if current.Bound == BoundMin && next.Num < current.Num {
			return fmt.Errorf("%w: layer %q constraint on key %q would be widened", ErrWiden, source, key)
		}
	case KindBool:
		if !current.Bool && next.Bool {
			return fmt.Errorf("%w: layer %q constraint on key %q would be widened", ErrWiden, source, key)
		}
	case KindEnum:
		intersection := intersect(current.Set, next.Set)
		if len(intersection) == 0 {
			return fmt.Errorf("%w: key %q conflicts with layer %q", ErrConflict, key, source)
		}
		if len(intersection) != len(next.Set) {
			return fmt.Errorf("%w: layer %q constraint on key %q would be widened", ErrWiden, source, key)
		}
	default:
		return ErrBadValue
	}
	return nil
}

func chainState(packages map[string]Package, name string) ([]string, error) {
	if _, ok := packages[name]; !ok {
		return nil, ErrUnknownPackage
	}

	reversed := make([]string, 0, 4)
	seen := make(map[string]struct{}, 4)
	for current := name; current != ""; {
		if _, ok := seen[current]; ok {
			return nil, ErrUnknownPackage
		}
		seen[current] = struct{}{}

		pkg, ok := packages[current]
		if !ok {
			return nil, ErrUnknownPackage
		}
		reversed = append(reversed, current)
		current = pkg.Parent
	}

	chain := make([]string, len(reversed))
	for i := range reversed {
		chain[len(reversed)-1-i] = reversed[i]
	}
	return chain, nil
}

func rebuildPackages(input []Package) (map[string]Package, error) {
	type candidate struct {
		pkg  Package
		name string
	}

	seen := make(map[string]struct{}, len(input))
	pending := make([]candidate, 0, len(input))
	for _, pkg := range input {
		if strings.TrimSpace(pkg.Name) == "" {
			return nil, ErrBadPackage
		}
		if _, ok := seen[pkg.Name]; ok {
			return nil, ErrDuplicatePackage
		}
		seen[pkg.Name] = struct{}{}

		normalized, err := normalizePackage(pkg)
		if err != nil {
			return nil, err
		}
		pending = append(pending, candidate{pkg: normalized, name: normalized.Name})
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].name < pending[j].name })

	packages := make(map[string]Package, len(pending))
	for len(pending) > 0 {
		progress := false
		for i := 0; i < len(pending); {
			pkg := pending[i].pkg
			if pkg.Parent != "" {
				if _, ok := packages[pkg.Parent]; !ok {
					i++
					continue
				}
			}

			packages[pkg.Name] = pkg
			if _, err := resolveState(packages, pkg.Name); err != nil {
				return nil, err
			}
			pending = append(pending[:i], pending[i+1:]...)
			progress = true
		}
		if !progress {
			return nil, ErrBadPackage
		}
	}
	return packages, nil
}

func intersect(left, right []string) []string {
	rightSet := make(map[string]struct{}, len(right))
	for _, item := range right {
		rightSet[item] = struct{}{}
	}
	result := make([]string, 0, len(left))
	for _, item := range left {
		if _, ok := rightSet[item]; ok {
			result = append(result, item)
		}
	}
	return result
}

func sortedKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedValueKeys(values map[string]Value) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedPackageNames(packages map[string]Package) []string {
	names := make([]string, 0, len(packages))
	for name := range packages {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func cloneValue(value Value) Value {
	cloned := value
	if value.Set != nil {
		cloned.Set = append([]string(nil), value.Set...)
	}
	return cloned
}

func cloneValueMap(values map[string]Value) map[string]Value {
	if values == nil {
		return nil
	}
	cloned := make(map[string]Value, len(values))
	for key, value := range values {
		cloned[key] = cloneValue(value)
	}
	return cloned
}

func clonePackage(pkg Package) Package {
	return Package{
		Name:   pkg.Name,
		Parent: pkg.Parent,
		Values: cloneValueMap(pkg.Values),
	}
}

func copyEffective(effective Effective) Effective {
	return Effective{
		Package: effective.Package,
		Chain:   append([]string(nil), effective.Chain...),
		Values:  cloneValueMap(effective.Values),
	}
}

func copyLayers(layers []Layer) []Layer {
	if layers == nil {
		return nil
	}
	cloned := make([]Layer, len(layers))
	for i, layer := range layers {
		cloned[i] = layer
		cloned[i].Value = cloneValue(layer.Value)
	}
	return cloned
}
