// Package task composes a seccomp profile for a workload out of a platform
// baseline and the declarations shipped with the workload itself, always
// narrowing the allowed call set and never widening it.
package task

import "errors"

// errNotImplemented marks behaviour that still has to be implemented.
var errNotImplemented = errors.New("not implemented")

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
)

// SeccompProfileLattice is stateless: every method reads its arguments only.
type SeccompProfileLattice struct{}

// NewSeccompProfileLattice returns a lattice usable from several goroutines.
func NewSeccompProfileLattice() *SeccompProfileLattice {
	return &SeccompProfileLattice{}
}

// Compose merges the platform baseline with a workload declaration.
func (l *SeccompProfileLattice) Compose(base, overlay Profile) (Profile, error) {
	return Profile{}, errNotImplemented
}

// Narrow checks that next is a narrowing of prev and reports the difference.
func (l *SeccompProfileLattice) Narrow(prev, next Profile) (Diff, error) {
	return Diff{}, errNotImplemented
}

// Allowed reports whether a call name is inside the allow set.
func (l *SeccompProfileLattice) Allowed(p Profile, name string) (bool, error) {
	return false, errNotImplemented
}

// Translate rewrites a profile for another architecture.
func (l *SeccompProfileLattice) Translate(p Profile, arch string) (Profile, error) {
	return Profile{}, errNotImplemented
}

// DefaultAction returns the default action of a profile.
func (l *SeccompProfileLattice) DefaultAction(p Profile) (Action, error) {
	return "", errNotImplemented
}

// Validate rejects profiles that would fail open.
func (l *SeccompProfileLattice) Validate(p Profile) error {
	return errNotImplemented
}

// Canonical renders a profile as a deterministic string.
func (l *SeccompProfileLattice) Canonical(p Profile) (string, error) {
	return "", errNotImplemented
}

// Names returns the allow set as a sorted slice.
func (l *SeccompProfileLattice) Names(p Profile) ([]string, error) {
	return nil, errNotImplemented
}

// Lookup returns the rule of one call name.
func (l *SeccompProfileLattice) Lookup(p Profile, name string) (Rule, bool, error) {
	return Rule{}, false, errNotImplemented
}
