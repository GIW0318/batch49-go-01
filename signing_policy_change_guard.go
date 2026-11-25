// Package task recalculates the blast radius of a signing policy change,
// gates relaxations behind approvals and activates changes namespace by
// namespace with rollback support.
package task

import (
	"errors"
	"sync"
	"time"
)

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Policy is one immutable signing policy version.
type Policy struct {
	Version       int      `json:"version"`
	RequireSigned bool     `json:"require_signed"`
	AllowedAlgs   []string `json:"allowed_algs"`
	MinKeyBits    int      `json:"min_key_bits"`
}

// Artifact is one stored artifact together with the signature it carries.
type Artifact struct {
	ID        string    `json:"id"`
	Namespace string    `json:"namespace"`
	Signed    bool      `json:"signed"`
	Alg       string    `json:"alg"`
	KeyBits   int       `json:"key_bits"`
	SignedAt  time.Time `json:"signed_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Decision is the verdict of evaluating one artifact under the effective
// policy of its namespace.
type Decision string

// Decision values returned by Evaluate.
const (
	DecisionAllow Decision = "allow"
	DecisionDeny  Decision = "deny"
)

// Impact is the recomputed blast radius of one staged change.
type Impact struct {
	ChangeID     string   `json:"change_id"`
	FromVersion  int      `json:"from_version"`
	ToVersion    int      `json:"to_version"`
	Relaxations  []string `json:"relaxations"`
	Restrictions []string `json:"restrictions"`
}

// IsRelaxation reports whether the change allows at least one artifact that
// used to be rejected.
func (i Impact) IsRelaxation() bool { return len(i.Relaxations) > 0 }

// Approval is one approval token bound to a single change.
type Approval struct {
	ChangeID  string    `json:"change_id"`
	Approver  string    `json:"approver"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Errors returned by the guard.
var (
	ErrBadVersion      = errors.New("next policy version must be the current version plus one")
	ErrVersionExists   = errors.New("policy version is already staged or active")
	ErrBadPolicy       = errors.New("policy definition is invalid")
	ErrUnknownChange   = errors.New("unknown change")
	ErrBadToken        = errors.New("approval token does not match the change")
	ErrTokenExpired    = errors.New("approval token has expired")
	ErrNotApproved     = errors.New("relaxation requires an approval token")
	ErrUnknownScope    = errors.New("unknown namespace scope")
	ErrUnknownArtifact = errors.New("unknown artifact")
	ErrNotActive       = errors.New("change is not active in any namespace")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

type stagedChange struct {
	policy      Policy
	impact      Impact
	approvals   []Approval
	activeScope []string
}

// SigningPolicyChangeGuard owns the policy versions, the staged changes and
// the per namespace activations.
type SigningPolicyChangeGuard struct {
	mu        sync.Mutex
	base      Policy
	artifacts map[string]Artifact
	changes   map[string]*stagedChange
	order     []string
	scopes    map[string]int
}

// NewSigningPolicyChangeGuard seeds the guard with the base policy and the
// stored artifacts.
func NewSigningPolicyChangeGuard(base Policy, artifacts []Artifact) *SigningPolicyChangeGuard {
	store := make(map[string]Artifact, len(artifacts))
	for _, a := range artifacts {
		store[a.ID] = a
	}
	return &SigningPolicyChangeGuard{
		base:      base,
		artifacts: store,
		changes:   make(map[string]*stagedChange),
	}
}

// Stage recomputes the impact of moving to next against the policy currently
// in force and stores the change without activating it.
func (g *SigningPolicyChangeGuard) Stage(next Policy) (Impact, error) {
	return Impact{}, errNotImplemented
}

// Approve records one approval token for a staged change. Tokens are bound to
// a single change and to their expiry time.
func (g *SigningPolicyChangeGuard) Approve(change string, token Approval) error {
	return errNotImplemented
}

// Activate makes a change effective in one namespace. Relaxations need an
// approval first; activating the same namespace twice is idempotent.
func (g *SigningPolicyChangeGuard) Activate(change, scope string) error {
	return errNotImplemented
}

// Rollback withdraws every namespace activated by change and restores the
// previous version there, keeping the approval records.
func (g *SigningPolicyChangeGuard) Rollback(change string) error { return errNotImplemented }

// Effective returns the policy version in force for one namespace.
func (g *SigningPolicyChangeGuard) Effective(namespace string) (Policy, error) {
	return Policy{}, errNotImplemented
}

// ImpactOf returns the impact computed when change was staged.
func (g *SigningPolicyChangeGuard) ImpactOf(change string) (Impact, error) {
	return Impact{}, errNotImplemented
}

// Evaluate decides one artifact against the effective policy of its namespace.
func (g *SigningPolicyChangeGuard) Evaluate(id string) (Decision, error) {
	return "", errNotImplemented
}

// ChangeIDs lists the staged change IDs in ascending order.
func (g *SigningPolicyChangeGuard) ChangeIDs() []string { return nil }

// Approved lists the approvers recorded for one change in insertion order.
func (g *SigningPolicyChangeGuard) Approved(change string) []string { return nil }

// ActiveScopes lists the namespaces where change is currently active.
func (g *SigningPolicyChangeGuard) ActiveScopes(change string) []string { return nil }

// Snapshot serialises the policy versions, approvals and activations.
func (g *SigningPolicyChangeGuard) Snapshot() ([]byte, error) { return nil, errNotImplemented }

// Restore replaces the guard state from a snapshot. A corrupt payload leaves
// the guard untouched.
func (g *SigningPolicyChangeGuard) Restore(data []byte) error { return errNotImplemented }
