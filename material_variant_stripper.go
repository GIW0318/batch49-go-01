// Package task resolves material keyword sets into ordered variant keys,
// enforces exclusive keyword groups and strips variants nobody references.
package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

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

// VariantStripper owns the keyword table, the resolved variants and the
// per variant reference counters.
type VariantStripper struct{}

// NewVariantStripper builds an empty stripper.
func NewVariantStripper() *VariantStripper {
	return &VariantStripper{}
}

// DeclareKeyword registers one keyword with its exclusive group and the
// keywords it depends on.
func (s *VariantStripper) DeclareKeyword(name, group string, deps []string) error {
	return errNotImplemented
}

// SetDependencies replaces the dependency list of an already declared keyword.
func (s *VariantStripper) SetDependencies(name string, deps []string) error {
	return errNotImplemented
}

// Group reports the exclusive group of a keyword.
func (s *VariantStripper) Group(name string) string {
	return ""
}

// Resolve turns a keyword set into a canonical variant key and registers it.
func (s *VariantStripper) Resolve(keywords map[string]bool) (VariantKey, error) {
	return "", errNotImplemented
}

// Strip keeps the variants referenced by the given materials and recounts the
// reference table from scratch.
func (s *VariantStripper) Strip(refs []MaterialRef) (VariantSet, error) {
	return VariantSet{}, errNotImplemented
}

// RefCount reports how many materials reference one variant.
func (s *VariantStripper) RefCount(key VariantKey) int {
	return 0
}

// Variants lists every registered variant key in ascending order.
func (s *VariantStripper) Variants() []VariantKey {
	return nil
}

// DefaultVariant is the variant used when no keyword is set.
func (s *VariantStripper) DefaultVariant() VariantKey {
	return BaseVariant
}

// Explain reports the explicit, closure and disabled keywords of a variant.
func (s *VariantStripper) Explain(key VariantKey) (VariantInfo, error) {
	return VariantInfo{}, errNotImplemented
}

// Validate checks that a variant key is registered and correctly ordered.
func (s *VariantStripper) Validate(key VariantKey) error {
	return errNotImplemented
}

// Snapshot serialises the keyword table, the resolved variants and the
// reference counters.
func (s *VariantStripper) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole stripper state with a snapshot, all or nothing.
func (s *VariantStripper) Restore(data []byte) error {
	return errNotImplemented
}
