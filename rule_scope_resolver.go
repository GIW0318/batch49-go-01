// Package task resolves layered rule package scopes for the scanning platform.
package task

import "errors"

// errNotImplemented marks behaviour that is not built yet.
var errNotImplemented = errors.New("not implemented")

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
	packages map[string]Package
	cache    map[string]Effective
}

// NewRuleScopeResolver builds an empty resolver.
func NewRuleScopeResolver() *RuleScopeResolver {
	return &RuleScopeResolver{packages: map[string]Package{}, cache: map[string]Effective{}}
}

// Define registers one rule package layer.
func (r *RuleScopeResolver) Define(pkg Package) error {
	return errNotImplemented
}

// Resolve returns the effective constraints of one package.
func (r *RuleScopeResolver) Resolve(name string) (Effective, error) {
	return Effective{}, errNotImplemented
}

// Override narrows one key of an already defined package.
func (r *RuleScopeResolver) Override(name, key string, v Value) error {
	return errNotImplemented
}

// Explain returns the per layer trace of one resolved key.
func (r *RuleScopeResolver) Explain(name, key string) []Layer {
	return nil
}

// Digest returns the stable digest of the resolved package.
func (r *RuleScopeResolver) Digest(name string) (string, error) {
	return "", errNotImplemented
}

// Chain returns the ancestor chain of a package, root first.
func (r *RuleScopeResolver) Chain(name string) ([]string, error) {
	return nil, errNotImplemented
}

// Keys returns every resolved key of a package in lexicographic order.
func (r *RuleScopeResolver) Keys(name string) ([]string, error) {
	return nil, errNotImplemented
}

// Match resolves every key selected by a wildcard field path.
func (r *RuleScopeResolver) Match(name, pattern string) ([]Match, error) {
	return nil, errNotImplemented
}

// Packages lists every defined package name in lexicographic order.
func (r *RuleScopeResolver) Packages() []string {
	return nil
}

// Snapshot serialises the package tree and the resolved cache.
func (r *RuleScopeResolver) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (r *RuleScopeResolver) Restore(data []byte) error {
	return errNotImplemented
}
