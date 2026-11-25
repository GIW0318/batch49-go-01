// Package task compiles WAF rule templates with typed value slots, escapes
// untrusted values per slot class and rejects template injection attempts.
package task

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"
)

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

const snapshotVersion = 1

const (
	literalStep           = -1
	unresolvedPlaceholder = -2
)

// WafTemplateEscaper owns compiled rules and their activation state.
type WafTemplateEscaper struct {
	mu     sync.RWMutex
	limits Limits
	rules  map[string]*storedRule
}

type storedRule struct {
	rule CompiledRule
	tmpl *compiledTemplate
}

type compiledTemplate struct {
	slots []Slot
	keys  []string
	steps []renderStep
}

type renderStep struct {
	text string
	slot int
	name string
}

type snapshotEnvelope struct {
	Version int            `json:"version"`
	Rules   []CompiledRule `json:"rules"`
}

// NewWafTemplateEscaper builds an escaper with the given caps.
func NewWafTemplateEscaper(limits Limits) *WafTemplateEscaper {
	return &WafTemplateEscaper{
		limits: limits,
		rules:  make(map[string]*storedRule),
	}
}

// Compile validates a template and stores it in the inactive state.
func (e *WafTemplateEscaper) Compile(t Template) (CompiledRule, error) {
	rule, tmpl, err := buildTemplate(t, e.limits)
	if err != nil {
		return CompiledRule{}, err
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	if _, exists := e.rules[t.ID]; exists {
		return CompiledRule{}, ErrDuplicateRule
	}
	if len(e.rules)+1 > e.limits.MaxRules {
		return CompiledRule{}, fmt.Errorf("%w: rule count %d exceeds %d", ErrLimitExceeded, len(e.rules)+1, e.limits.MaxRules)
	}

	e.rules[t.ID] = &storedRule{rule: cloneRule(rule), tmpl: tmpl}
	return cloneRule(rule), nil
}

// Activate marks a compiled rule as usable by Render.
func (e *WafTemplateEscaper) Activate(ruleID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	stored, ok := e.rules[ruleID]
	if !ok {
		return ErrUnknownRule
	}
	if stored.rule.Active {
		return ErrAlreadyActive
	}
	stored.rule.Active = true
	return nil
}

// Deactivate removes a rule from service without dropping its build.
func (e *WafTemplateEscaper) Deactivate(ruleID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	stored, ok := e.rules[ruleID]
	if !ok {
		return ErrUnknownRule
	}
	if !stored.rule.Active {
		return ErrNotActive
	}
	stored.rule.Active = false
	return nil
}

// Rule returns a copy of one compiled rule.
func (e *WafTemplateEscaper) Rule(ruleID string) (CompiledRule, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	stored, ok := e.rules[ruleID]
	if !ok {
		return CompiledRule{}, false
	}
	return cloneRule(stored.rule), true
}

// Rules returns every compiled rule ordered by id.
func (e *WafTemplateEscaper) Rules() []CompiledRule {
	e.mu.RLock()
	defer e.mu.RUnlock()

	ids := make([]string, 0, len(e.rules))
	for id := range e.rules {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	rules := make([]CompiledRule, 0, len(ids))
	for _, id := range ids {
		rules = append(rules, cloneRule(e.rules[id].rule))
	}
	return rules
}

// Render substitutes escaped values into an active rule.
func (e *WafTemplateEscaper) Render(ruleID string, values map[string]string) (string, error) {
	e.mu.RLock()
	stored, ok := e.rules[ruleID]
	if !ok {
		e.mu.RUnlock()
		return "", ErrUnknownRule
	}
	if !stored.rule.Active {
		e.mu.RUnlock()
		return "", ErrRuleNotActive
	}
	tmpl := stored.tmpl
	maxRenderedBytes := e.limits.MaxRenderedBytes
	e.mu.RUnlock()

	return renderCompiled(tmpl, values, maxRenderedBytes)
}

// Preview validates and renders a template without registering it.
func (e *WafTemplateEscaper) Preview(t Template, values map[string]string) (string, error) {
	_, tmpl, err := buildTemplate(t, e.limits)
	if err != nil {
		return "", err
	}
	return renderCompiled(tmpl, values, e.limits.MaxRenderedBytes)
}

// Canonicalize decodes exactly one layer of percent escapes.
func (e *WafTemplateEscaper) Canonicalize(raw string) (Canonical, error) {
	return canonicalize(raw)
}

// Snapshot serialises compiled rules and activation state.
func (e *WafTemplateEscaper) Snapshot() ([]byte, error) {
	e.mu.RLock()
	ids := make([]string, 0, len(e.rules))
	for id := range e.rules {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	payload := snapshotEnvelope{
		Version: snapshotVersion,
		Rules:   make([]CompiledRule, 0, len(ids)),
	}
	for _, id := range ids {
		payload.Rules = append(payload.Rules, cloneRule(e.rules[id].rule))
	}
	e.mu.RUnlock()

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode waf template snapshot: %w", err)
	}
	return data, nil
}

// Restore replaces the whole registry from a snapshot.
func (e *WafTemplateEscaper) Restore(data []byte) error {
	var payload snapshotEnvelope
	if err := json.Unmarshal(data, &payload); err != nil {
		return fmt.Errorf("%w: decode snapshot: %v", ErrBadSnapshot, err)
	}
	if payload.Version != snapshotVersion {
		return fmt.Errorf("%w: unsupported version %d", ErrBadSnapshot, payload.Version)
	}
	if len(payload.Rules) > e.limits.MaxRules {
		return fmt.Errorf("%w: %w: rule count %d exceeds %d", ErrBadSnapshot, ErrLimitExceeded, len(payload.Rules), e.limits.MaxRules)
	}

	next := make(map[string]*storedRule, len(payload.Rules))
	seenIDs := make(map[string]struct{}, len(payload.Rules))

	for _, saved := range payload.Rules {
		if strings.TrimSpace(saved.ID) == "" {
			return fmt.Errorf("%w: blank rule id", ErrBadSnapshot)
		}
		if _, duplicated := seenIDs[saved.ID]; duplicated {
			return fmt.Errorf("%w: duplicate rule id %q", ErrBadSnapshot, saved.ID)
		}
		seenIDs[saved.ID] = struct{}{}

		rule, tmpl, err := buildTemplate(Template{
			ID:     saved.ID,
			Source: saved.Source,
			Slots:  saved.Slots,
		}, e.limits)
		if err != nil {
			return fmt.Errorf("%w: rule %q: %w", ErrBadSnapshot, saved.ID, err)
		}
		if saved.Digest == "" || saved.Digest != rule.Digest {
			return fmt.Errorf("%w: rule %q digest mismatch", ErrBadSnapshot, saved.ID)
		}

		rule.Active = saved.Active
		if saved.Generation < 0 {
			return fmt.Errorf("%w: rule %q has negative generation", ErrBadSnapshot, saved.ID)
		}
		if saved.Generation == 0 {
			rule.Generation = 1
		} else {
			rule.Generation = saved.Generation
		}

		next[saved.ID] = &storedRule{rule: rule, tmpl: tmpl}
	}

	e.mu.Lock()
	e.rules = next
	e.mu.Unlock()
	return nil
}

func buildTemplate(t Template, limits Limits) (CompiledRule, *compiledTemplate, error) {
	if strings.TrimSpace(t.ID) == "" {
		return CompiledRule{}, nil, ErrBlankRuleID
	}
	if t.Source == "" {
		return CompiledRule{}, nil, fmt.Errorf("%w: source is empty", ErrBadTemplate)
	}
	if len(t.Source) > limits.MaxTemplateBytes {
		return CompiledRule{}, nil, fmt.Errorf("%w: source is %d bytes, limit %d", ErrLimitExceeded, len(t.Source), limits.MaxTemplateBytes)
	}
	if len(t.Slots) > limits.MaxSlots {
		return CompiledRule{}, nil, fmt.Errorf("%w: slot count %d exceeds %d", ErrLimitExceeded, len(t.Slots), limits.MaxSlots)
	}

	tmpl, err := parseTemplate(t.Source)
	if err != nil {
		return CompiledRule{}, nil, err
	}

	slots := cloneSlots(t.Slots)
	keys := make([]string, len(slots))
	indexByKey := make(map[string]int, len(slots))

	for i, slot := range slots {
		key := normalizeSlotName(slot.Name)
		keys[i] = key
		if _, duplicated := indexByKey[key]; duplicated {
			return CompiledRule{}, nil, fmt.Errorf("slot %q: %w", key, ErrDuplicateSlot)
		}
		if !supportedSlotClass(slot.Class) {
			return CompiledRule{}, nil, fmt.Errorf("slot %q: %w: %q", key, ErrBadSlotClass, slot.Class)
		}
		indexByKey[key] = i
	}

	used := make([]bool, len(slots))
	for stepIndex := range tmpl.steps {
		step := &tmpl.steps[stepIndex]
		if step.slot != unresolvedPlaceholder {
			continue
		}

		key := normalizeSlotName(step.name)
		slotIndex, declared := indexByKey[key]
		if !declared {
			return CompiledRule{}, nil, fmt.Errorf("slot %q: %w", key, ErrUndeclaredSlot)
		}

		step.slot = slotIndex
		step.name = ""
		used[slotIndex] = true
	}

	for i := range slots {
		if !used[i] {
			return CompiledRule{}, nil, fmt.Errorf("slot %q: %w", keys[i], ErrUnusedSlot)
		}
	}

	tmpl.slots = slots
	tmpl.keys = keys
	digest := digestTemplate(t.ID, t.Source, slots, keys)
	rule := CompiledRule{
		ID:         t.ID,
		Source:     t.Source,
		Slots:      cloneSlots(slots),
		Digest:     digest,
		Active:     false,
		Generation: 1,
	}
	return rule, tmpl, nil
}

func parseTemplate(source string) (*compiledTemplate, error) {
	tmpl := &compiledTemplate{}
	var literal strings.Builder

	flushLiteral := func() {
		if literal.Len() == 0 {
			return
		}
		tmpl.steps = append(tmpl.steps, renderStep{
			text: literal.String(),
			slot: literalStep,
		})
		literal.Reset()
	}

	for i := 0; i < len(source); {
		if source[i] != '%' {
			literal.WriteByte(source[i])
			i++
			continue
		}

		if i+1 >= len(source) {
			return nil, fmt.Errorf("%w: trailing percent at offset %d", ErrBadTemplate, i)
		}

		if source[i+1] == '%' {
			literal.WriteByte('%')
			i += 2
			continue
		}

		if source[i+1] != '{' {
			return nil, fmt.Errorf("%w: invalid percent sequence at offset %d", ErrBadTemplate, i)
		}

		closingOffset := strings.IndexByte(source[i+2:], '}')
		if closingOffset < 0 {
			return nil, fmt.Errorf("%w: unclosed placeholder at offset %d", ErrBadTemplate, i)
		}
		closing := i + 2 + closingOffset
		name := source[i+2 : closing]

		flushLiteral()
		tmpl.steps = append(tmpl.steps, renderStep{
			name: name,
			slot: unresolvedPlaceholder,
		})
		i = closing + 1
	}

	flushLiteral()

	var literalParts strings.Builder
	for _, step := range tmpl.steps {
		if step.slot == literalStep {
			literalParts.WriteString(step.text)
		}
	}
	if err := checkLiteralBalance(literalParts.String()); err != nil {
		return nil, err
	}

	return tmpl, nil
}

func checkLiteralBalance(literal string) error {
	parenthesisDepth := 0
	var quote byte

	for i := 0; i < len(literal); i++ {
		c := literal[i]

		// A backslash protects the following character from balance checks.
		if c == '\\' && i+1 < len(literal) {
			i++
			continue
		}

		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}

		switch c {
		case '"', '\'':
			quote = c
		case '(':
			parenthesisDepth++
		case ')':
			parenthesisDepth--
			if parenthesisDepth < 0 {
				return fmt.Errorf("%w: parenthesis depth became negative", ErrBadTemplate)
			}
		}
	}

	if quote != 0 || parenthesisDepth != 0 {
		return fmt.Errorf("%w: unbalanced quote or parenthesis", ErrBadTemplate)
	}
	return nil
}

func renderCompiled(tmpl *compiledTemplate, values map[string]string, maxRenderedBytes int) (string, error) {
	escapedValues := make([]string, len(tmpl.slots))

	for i, slot := range tmpl.slots {
		key := tmpl.keys[i]
		rawValue, exists := lookupValue(values, slot.Name, key)
		if !exists {
			return "", fmt.Errorf("slot %q: %w", key, ErrMissingValue)
		}

		canonical, err := canonicalize(rawValue)
		if err != nil {
			return "", fmt.Errorf("slot %q: %w", key, err)
		}

		escaped, err := escapeSlotValue(slot, key, canonical.Value)
		if err != nil {
			return "", err
		}
		escapedValues[i] = escaped
	}

	var rendered strings.Builder
	for _, step := range tmpl.steps {
		if step.slot == literalStep {
			rendered.WriteString(step.text)
			continue
		}
		if step.slot >= 0 {
			rendered.WriteString(escapedValues[step.slot])
		}
	}

	if rendered.Len() > maxRenderedBytes {
		return "", fmt.Errorf("%w: output is %d bytes, limit %d", ErrRenderTooLong, rendered.Len(), maxRenderedBytes)
	}
	return rendered.String(), nil
}

func canonicalize(raw string) (Canonical, error) {
	if strings.IndexByte(raw, '%') < 0 {
		return Canonical{Value: raw, Layers: 0, Suspicious: false}, nil
	}

	var decoded strings.Builder
	decodedAny := false

	for i := 0; i < len(raw); {
		if raw[i] != '%' {
			decoded.WriteByte(raw[i])
			i++
			continue
		}

		if i+2 >= len(raw) {
			return Canonical{}, fmt.Errorf("%w: truncated escape at offset %d", ErrBadEscape, i)
		}
		high, highOK := hexNibble(raw[i+1])
		low, lowOK := hexNibble(raw[i+2])
		if !highOK || !lowOK {
			return Canonical{}, fmt.Errorf("%w: invalid hex escape at offset %d", ErrBadEscape, i)
		}

		decoded.WriteByte(byte(high<<4 | low))
		decodedAny = true
		i += 3
	}

	value := decoded.String()
	layers := 0
	if decodedAny {
		layers = 1
	}
	return Canonical{
		Value:      value,
		Layers:     layers,
		Suspicious: containsPercentHex(value),
	}, nil
}

func containsPercentHex(value string) bool {
	for i := 0; i+2 < len(value); i++ {
		if value[i] != '%' {
			continue
		}
		if _, highOK := hexNibble(value[i+1]); !highOK {
			continue
		}
		if _, lowOK := hexNibble(value[i+2]); lowOK {
			return true
		}
	}
	return false
}

func hexNibble(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	default:
		return 0, false
	}
}

func escapeSlotValue(slot Slot, key, value string) (string, error) {
	if hasControlCharacter(value) {
		return "", fmt.Errorf("slot %q: %w: control character", key, ErrInjectionBlocked)
	}

	switch slot.Class {
	case SlotLiteral:
		return escapeLiteral(value), nil
	case SlotRegex:
		return regexp.QuoteMeta(value), nil
	case SlotHeader:
		return normalizeHeader(value, key)
	case SlotNumber:
		return normalizeNumber(value, key)
	default:
		return "", fmt.Errorf("slot %q: %w: %q", key, ErrBadSlotClass, slot.Class)
	}
}

func escapeLiteral(value string) string {
	var escaped strings.Builder
	for i := 0; i < len(value); i++ {
		switch value[i] {
		case '\\':
			escaped.WriteString(`\\`)
		case '"':
			escaped.WriteString(`\"`)
		default:
			escaped.WriteByte(value[i])
		}
	}
	return escaped.String()
}

func normalizeHeader(value, key string) (string, error) {
	if value == "" {
		return "", fmt.Errorf("slot %q: %w: empty header name", key, ErrInjectionBlocked)
	}

	for i := 0; i < len(value); i++ {
		c := value[i]
		letter := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		digit := c >= '0' && c <= '9'
		if !letter && !digit && c != '-' {
			return "", fmt.Errorf("slot %q: %w: invalid header name", key, ErrInjectionBlocked)
		}
	}
	return strings.ToLower(value), nil
}

func normalizeNumber(value, key string) (string, error) {
	if value == "" {
		return "", fmt.Errorf("slot %q: %w: empty number", key, ErrInjectionBlocked)
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return "", fmt.Errorf("slot %q: %w: unsigned decimal integer required", key, ErrInjectionBlocked)
		}
	}

	normalized := strings.TrimLeft(value, "0")
	if normalized == "" {
		return "0", nil
	}
	return normalized, nil
}

func hasControlCharacter(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] < 0x20 || value[i] == 0x7f {
			return true
		}
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func lookupValue(values map[string]string, originalName, key string) (string, bool) {
	if value, ok := values[originalName]; ok {
		return value, true
	}
	if value, ok := values[key]; ok {
		return value, true
	}

	bestKey := ""
	found := false
	for name := range values {
		if normalizeSlotName(name) != key {
			continue
		}
		if !found || name < bestKey {
			bestKey = name
			found = true
		}
	}
	if !found {
		return "", false
	}
	return values[bestKey], true
}

func normalizeSlotName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func supportedSlotClass(class SlotClass) bool {
	switch class {
	case SlotLiteral, SlotRegex, SlotHeader, SlotNumber:
		return true
	default:
		return false
	}
}

func digestTemplate(id, source string, slots []Slot, keys []string) string {
	hash := sha256.New()
	writeDigestString := func(value string) {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(value)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte(value))
	}

	writeDigestString(id)
	writeDigestString(source)
	for i, slot := range slots {
		writeDigestString(slot.Name)
		writeDigestString(keys[i])
		writeDigestString(string(slot.Class))
	}

	return hex.EncodeToString(hash.Sum(nil))
}

func cloneRule(rule CompiledRule) CompiledRule {
	rule.Slots = cloneSlots(rule.Slots)
	return rule
}

func cloneSlots(slots []Slot) []Slot {
	if slots == nil {
		return nil
	}
	cloned := make([]Slot, len(slots))
	copy(cloned, slots)
	return cloned
}
