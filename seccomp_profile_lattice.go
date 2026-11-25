// Package task composes a seccomp profile for a workload out of a platform
// baseline and the declarations shipped with the workload itself, always
// narrowing the allowed call set and never widening it.
package task

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// MaxRules is the maximum number of rules accepted in one profile.
const MaxRules = 4096

// Action is the seccomp default action of a profile.
type Action string

const (
	ActionAllow Action = "allow"
	ActionErrno Action = "errno"
	ActionTrap  Action = "trap"
	ActionKill  Action = "kill"
)

// Argument filter operators understood by the lattice.
const (
	OpEq = "eq"
	OpNe = "ne"
)

// ArgFilter constrains one syscall argument.
type ArgFilter struct {
	Index uint32
	Op    string
	Value uint64
}

// Rule is one allowed call together with its argument conditions. An empty
// Args slice means the call is allowed unconditionally.
type Rule struct {
	Name   string
	Number int
	Args   []ArgFilter
}

// Profile is the effective seccomp profile of one workload.
type Profile struct {
	Default Action
	Arch    string
	Rules   []Rule
}

// Diff describes how one profile relates to another one.
type Diff struct {
	Narrowed       bool
	Added          []string
	Removed        []string
	DefaultChanged bool
	OldDefault     Action
	NewDefault     Action
}

var (
	ErrEmptyAllowSet    = errors.New("allowed call set is empty")
	ErrBadDefaultAction = errors.New("default action is not allow, errno, trap or kill")
	ErrDuplicateRule    = errors.New("duplicate call name in profile")
	ErrBadRuleName      = errors.New("call name is required")
	ErrBadArgOp         = errors.New("argument operator must be eq or ne")
	ErrArgConflict      = errors.New("argument conditions do not intersect")
	ErrWidenAttempt     = errors.New("change would widen the profile")
	ErrUnknownSyscall   = errors.New("call name has no number on the target architecture")
	ErrUnknownArch      = errors.New("target architecture is not supported")
	ErrRuleLimit        = errors.New("profile contains more than 4096 rules")
	ErrTooManyRules     = ErrRuleLimit
)

var syscallNumbers = map[string]map[string]int{
	"amd64": {
		"read":        0,
		"write":       1,
		"openat":      257,
		"personality": 135,
	},
	"arm64": {
		"read":   63,
		"write":  64,
		"openat": 56,
	},
}

// SeccompProfileLattice is stateless: every method reads its arguments only.
type SeccompProfileLattice struct{}

// NewSeccompProfileLattice returns a lattice usable from several goroutines.
func NewSeccompProfileLattice() *SeccompProfileLattice {
	return &SeccompProfileLattice{}
}

// Compose merges the platform baseline with a workload declaration.
func (l *SeccompProfileLattice) Compose(base, overlay Profile) (Profile, error) {
	if err := validateProfile(base); err != nil {
		return Profile{}, fmt.Errorf("base profile: %w", err)
	}
	if err := validateProfile(overlay); err != nil {
		return Profile{}, fmt.Errorf("overlay profile: %w", err)
	}

	baseRules := ruleByName(base.Rules)
	names := sortedRuleNames(overlay.Rules)

	for _, name := range names {
		if _, ok := baseRules[name]; !ok {
			return Profile{}, fmt.Errorf("%w: %s", ErrWidenAttempt, name)
		}
	}

	composed := make([]Rule, 0, len(names))
	for _, overlayRule := range overlay.Rules {
		name := overlayRule.Name
		baseRule := baseRules[name]
		args, err := intersectArgFilters(baseRule.Args, overlayRule.Args)
		if err != nil {
			return Profile{}, fmt.Errorf("rule %q: %w", name, err)
		}

		composed = append(composed, Rule{
			Name:   name,
			Number: baseRule.Number,
			Args:   args,
		})
	}

	arch := overlay.Arch
	if arch == "" {
		arch = base.Arch
	}

	return Profile{
		Default: stricterAction(base.Default, overlay.Default),
		Arch:    arch,
		Rules:   composed,
	}, nil
}

// Narrow checks that next is a narrowing of prev and reports the difference.
func (l *SeccompProfileLattice) Narrow(prev, next Profile) (Diff, error) {
	if err := validateProfile(prev); err != nil {
		return Diff{}, fmt.Errorf("previous profile: %w", err)
	}
	if err := validateProfile(next); err != nil {
		return Diff{}, fmt.Errorf("next profile: %w", err)
	}

	prevNames := nameSet(prev.Rules)
	nextNames := nameSet(next.Rules)
	diff := Diff{
		OldDefault:     prev.Default,
		NewDefault:     next.Default,
		DefaultChanged: prev.Default != next.Default,
		Added:          sortedMissingNames(nextNames, prevNames),
		Removed:        sortedMissingNames(prevNames, nextNames),
	}
	sort.Strings(diff.Added)
	sort.Strings(diff.Removed)

	widened := len(diff.Added) > 0
	if actionRank(next.Default) < actionRank(prev.Default) {
		widened = true
	}

	prevRules := ruleByName(prev.Rules)
	nextRules := ruleByName(next.Rules)
	argNarrowed := false
	for name := range nextNames {
		prevRule, ok := prevRules[name]
		if !ok {
			continue
		}
		if !argsNarrow(prevRule.Args, nextRules[name].Args) {
			widened = true
		}
		if !argsNarrow(nextRules[name].Args, prevRule.Args) {
			argNarrowed = true
		}
	}

	diff.Narrowed = !widened && (len(diff.Removed) > 0 ||
		actionRank(next.Default) > actionRank(prev.Default) ||
		argNarrowed)

	if widened {
		return diff, ErrWidenAttempt
	}
	return diff, nil
}

// Allowed reports whether a call name is inside the allow set.
func (l *SeccompProfileLattice) Allowed(p Profile, name string) (bool, error) {
	if name == "" {
		return false, ErrBadRuleName
	}
	for i := range p.Rules {
		if p.Rules[i].Name == name {
			return true, nil
		}
	}
	return false, nil
}

// Translate rewrites a profile for another architecture.
func (l *SeccompProfileLattice) Translate(p Profile, arch string) (Profile, error) {
	numbers, ok := syscallNumbers[arch]
	if !ok {
		return Profile{}, fmt.Errorf("%w: %q", ErrUnknownArch, arch)
	}
	if err := validateProfile(p); err != nil {
		return Profile{}, err
	}

	translated := make([]Rule, 0, len(p.Rules))
	for _, sourceRule := range p.Rules {
		name := sourceRule.Name
		number, ok := numbers[name]
		if !ok {
			return Profile{}, fmt.Errorf("%w: %s on %s", ErrUnknownSyscall, name, arch)
		}
		translated = append(translated, Rule{
			Name:   name,
			Number: number,
			Args:   cloneArgFilters(sourceRule.Args),
		})
	}

	return Profile{
		Default: p.Default,
		Arch:    arch,
		Rules:   translated,
	}, nil
}

// DefaultAction returns the default action of a profile.
func (l *SeccompProfileLattice) DefaultAction(p Profile) (Action, error) {
	if !knownAction(p.Default) {
		return "", ErrBadDefaultAction
	}
	return p.Default, nil
}

// Validate rejects profiles that would fail open.
func (l *SeccompProfileLattice) Validate(p Profile) error {
	return validateProfile(p)
}

// Canonical renders a profile as a deterministic string.
func (l *SeccompProfileLattice) Canonical(p Profile) (string, error) {
	if err := validateProfile(p); err != nil {
		return "", err
	}

	var builder strings.Builder
	builder.WriteString("seccomp-profile-lattice/v1\n")
	fmt.Fprintf(&builder, "default=%s\n", p.Default)
	fmt.Fprintf(&builder, "arch=%s\n", p.Arch)

	for _, rule := range sortedRules(p.Rules) {
		fmt.Fprintf(&builder, "rule=%s number=%d\n", rule.Name, rule.Number)
		args := cloneArgFilters(rule.Args)
		sort.SliceStable(args, func(i, j int) bool {
			return args[i].Index < args[j].Index
		})
		for _, arg := range args {
			fmt.Fprintf(&builder, "arg index=%d op=%s value=%d\n", arg.Index, arg.Op, arg.Value)
		}
	}

	return builder.String(), nil
}

// Names returns the allow set as a sorted slice.
func (l *SeccompProfileLattice) Names(p Profile) ([]string, error) {
	names := make([]string, 0, len(p.Rules))
	for i := range p.Rules {
		if p.Rules[i].Name == "" {
			return nil, ErrBadRuleName
		}
		names = append(names, p.Rules[i].Name)
	}
	sort.Strings(names)
	return names, nil
}

// Lookup returns the rule of one call name.
func (l *SeccompProfileLattice) Lookup(p Profile, name string) (Rule, bool, error) {
	if name == "" {
		return Rule{}, false, ErrBadRuleName
	}
	for i := range p.Rules {
		if p.Rules[i].Name == name {
			rule := p.Rules[i]
			rule.Args = cloneArgFilters(rule.Args)
			return rule, true, nil
		}
	}
	return Rule{}, false, nil
}

func validateProfile(p Profile) error {
	if len(p.Rules) > MaxRules {
		return fmt.Errorf("%w: got %d", ErrRuleLimit, len(p.Rules))
	}
	if len(p.Rules) == 0 {
		return ErrEmptyAllowSet
	}
	if !knownAction(p.Default) {
		return ErrBadDefaultAction
	}

	seen := make(map[string]struct{}, len(p.Rules))
	for i := range p.Rules {
		rule := &p.Rules[i]
		if rule.Name == "" {
			return ErrBadRuleName
		}
		if _, duplicate := seen[rule.Name]; duplicate {
			return fmt.Errorf("%w: %s", ErrDuplicateRule, rule.Name)
		}
		seen[rule.Name] = struct{}{}

		for _, arg := range rule.Args {
			if arg.Op != OpEq && arg.Op != OpNe {
				return fmt.Errorf("%w: %q", ErrBadArgOp, arg.Op)
			}
		}
	}
	return nil
}

func knownAction(action Action) bool {
	switch action {
	case ActionAllow, ActionErrno, ActionTrap, ActionKill:
		return true
	default:
		return false
	}
}

func actionRank(action Action) int {
	switch action {
	case ActionAllow:
		return 0
	case ActionErrno:
		return 1
	case ActionTrap:
		return 2
	case ActionKill:
		return 3
	default:
		return -1
	}
}

func stricterAction(left, right Action) Action {
	if actionRank(right) > actionRank(left) {
		return right
	}
	return left
}

func ruleByName(rules []Rule) map[string]Rule {
	byName := make(map[string]Rule, len(rules))
	for _, rule := range rules {
		byName[rule.Name] = rule
	}
	return byName
}

func nameSet(rules []Rule) map[string]struct{} {
	names := make(map[string]struct{}, len(rules))
	for _, rule := range rules {
		names[rule.Name] = struct{}{}
	}
	return names
}

func sortedRuleNames(rules []Rule) []string {
	names := make([]string, 0, len(rules))
	for _, rule := range rules {
		names = append(names, rule.Name)
	}
	sort.Strings(names)
	return names
}

func sortedRules(rules []Rule) []Rule {
	out := cloneRules(rules)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}

func cloneRules(rules []Rule) []Rule {
	out := make([]Rule, len(rules))
	for i := range rules {
		out[i] = rules[i]
		out[i].Args = cloneArgFilters(rules[i].Args)
	}
	return out
}

func cloneArgFilters(args []ArgFilter) []ArgFilter {
	if args == nil {
		return nil
	}
	out := make([]ArgFilter, len(args))
	copy(out, args)
	return out
}

func sortedMissingNames(want, have map[string]struct{}) []string {
	missing := make([]string, 0)
	for name := range want {
		if _, ok := have[name]; !ok {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}

func intersectArgFilters(left, right []ArgFilter) ([]ArgFilter, error) {
	merged := make([]ArgFilter, 0, len(left)+len(right))
	seen := make(map[ArgFilter]struct{}, len(left)+len(right))

	for _, arg := range left {
		if _, ok := seen[arg]; ok {
			continue
		}
		seen[arg] = struct{}{}
		merged = append(merged, arg)
	}
	for _, arg := range right {
		if _, ok := seen[arg]; ok {
			continue
		}
		seen[arg] = struct{}{}
		merged = append(merged, arg)
	}

	eqValues := make(map[uint32]uint64)
	neValues := make(map[uint32]map[uint64]struct{})
	for _, arg := range merged {
		switch arg.Op {
		case OpEq:
			if value, ok := eqValues[arg.Index]; ok && value != arg.Value {
				return nil, ErrArgConflict
			}
			eqValues[arg.Index] = arg.Value
			if values := neValues[arg.Index]; values != nil {
				if _, conflicts := values[arg.Value]; conflicts {
					return nil, ErrArgConflict
				}
			}
		case OpNe:
			if value, ok := eqValues[arg.Index]; ok && value == arg.Value {
				return nil, ErrArgConflict
			}
			if neValues[arg.Index] == nil {
				neValues[arg.Index] = make(map[uint64]struct{})
			}
			neValues[arg.Index][arg.Value] = struct{}{}
		}
	}

	if len(merged) == 0 {
		return nil, nil
	}
	sort.SliceStable(merged, func(i, j int) bool {
		return merged[i].Index < merged[j].Index
	})
	return merged, nil
}

func argsNarrow(prev, next []ArgFilter) bool {
	for _, required := range prev {
		if !nextImpliesFilter(required, next) {
			return false
		}
	}
	return true
}

func nextImpliesFilter(required ArgFilter, next []ArgFilter) bool {
	switch required.Op {
	case OpEq:
		for _, candidate := range next {
			if candidate.Index == required.Index &&
				candidate.Op == OpEq &&
				candidate.Value == required.Value {
				return true
			}
		}
		return false
	case OpNe:
		for _, candidate := range next {
			if candidate.Index != required.Index {
				continue
			}
			if candidate.Op == OpNe && candidate.Value == required.Value {
				return true
			}
			if candidate.Op == OpEq && candidate.Value != required.Value {
				return true
			}
		}
		return false
	default:
		return false
	}
}
