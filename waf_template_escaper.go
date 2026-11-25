// Package task compiles WAF rule templates with typed value slots, escapes
// untrusted values per slot class and rejects template injection attempts.
package task

import "errors"

// errNotImplemented marks behaviour that is still missing.
var errNotImplemented = errors.New("not implemented")

// SlotClass selects the escaping applied to one slot value.
type SlotClass string

// Supported slot classes.
const (
	SlotLiteral SlotClass = "literal"
	SlotRegex   SlotClass = "regex"
	SlotHeader  SlotClass = "header"
	SlotNumber  SlotClass = "number"
)

// Slot declares one placeholder and how its value must be escaped.
type Slot struct {
	Name  string    `json:"name"`
	Class SlotClass `json:"class"`
}

// Template is a rule source with %{name} placeholders and a slot declaration.
type Template struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Slots  []Slot `json:"slots"`
}

// CompiledRule is a validated template held in the registry.
type CompiledRule struct {
	ID         string `json:"id"`
	Source     string `json:"source"`
	Slots      []Slot `json:"slots"`
	Digest     string `json:"digest"`
	Active     bool   `json:"active"`
	Generation int    `json:"generation"`
}

// Canonical is the one layer percent decoding result of an untrusted value.
type Canonical struct {
	Value      string `json:"value"`
	Layers     int    `json:"layers"`
	Suspicious bool   `json:"suspicious"`
}

// Limits bounds the registry and rendered output.
type Limits struct {
	MaxRules         int
	MaxTemplateBytes int
	MaxRenderedBytes int
	MaxSlots         int
}

// Errors returned by the compiler.
var (
	ErrBlankRuleID      = errors.New("rule id is required")
	ErrDuplicateRule    = errors.New("rule id already compiled")
	ErrUnknownRule      = errors.New("unknown rule")
	ErrBadTemplate      = errors.New("rule source is malformed")
	ErrUndeclaredSlot   = errors.New("placeholder has no slot declaration")
	ErrUnusedSlot       = errors.New("declared slot is never used")
	ErrDuplicateSlot    = errors.New("slot name is duplicated")
	ErrBadSlotClass     = errors.New("slot class is not supported")
	ErrMissingValue     = errors.New("slot value is missing")
	ErrInjectionBlocked = errors.New("value cannot be escaped safely")
	ErrRenderTooLong    = errors.New("rendered rule exceeds the length budget")
	ErrRuleNotActive    = errors.New("rule is not active")
	ErrAlreadyActive    = errors.New("rule is already active")
	ErrNotActive        = errors.New("rule is not active")
	ErrBadEscape        = errors.New("percent escape is malformed")
	ErrLimitExceeded    = errors.New("limit exceeded")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

// WafTemplateEscaper owns compiled rules and their activation state.
type WafTemplateEscaper struct {
	limits Limits
}

// NewWafTemplateEscaper builds an escaper with the given caps.
func NewWafTemplateEscaper(limits Limits) *WafTemplateEscaper {
	return &WafTemplateEscaper{limits: limits}
}

// Compile validates a template and stores it in the inactive state.
func (e *WafTemplateEscaper) Compile(t Template) (CompiledRule, error) {
	return CompiledRule{}, errNotImplemented
}

// Activate marks a compiled rule as usable by Render.
func (e *WafTemplateEscaper) Activate(ruleID string) error {
	return errNotImplemented
}

// Deactivate removes a rule from service without dropping its build.
func (e *WafTemplateEscaper) Deactivate(ruleID string) error {
	return errNotImplemented
}

// Rule returns a copy of one compiled rule.
func (e *WafTemplateEscaper) Rule(ruleID string) (CompiledRule, bool) {
	return CompiledRule{}, false
}

// Rules returns every compiled rule ordered by id.
func (e *WafTemplateEscaper) Rules() []CompiledRule {
	return nil
}

// Render substitutes escaped values into an active rule.
func (e *WafTemplateEscaper) Render(ruleID string, values map[string]string) (string, error) {
	return "", errNotImplemented
}

// Preview validates and renders a template without registering it.
func (e *WafTemplateEscaper) Preview(t Template, values map[string]string) (string, error) {
	return "", errNotImplemented
}

// Canonicalize decodes exactly one layer of percent escapes.
func (e *WafTemplateEscaper) Canonicalize(raw string) (Canonical, error) {
	return Canonical{}, errNotImplemented
}

// Snapshot serialises compiled rules and activation state.
func (e *WafTemplateEscaper) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole registry from a snapshot.
func (e *WafTemplateEscaper) Restore(data []byte) error {
	return errNotImplemented
}
