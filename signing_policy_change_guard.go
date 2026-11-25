// Package task recalculates the blast radius of a signing policy change,
// gates relaxations behind approvals and activates changes namespace by
// namespace with rollback support.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

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
	for _, artifact := range artifacts {
		store[artifact.ID] = artifact
	}
	return &SigningPolicyChangeGuard{
		base:      clonePolicy(base),
		artifacts: store,
		changes:   make(map[string]*stagedChange),
		order:     make([]string, 0),
		scopes:    make(map[string]int),
	}
}

func changeIDFor(version int) string {
	return fmt.Sprintf("policy-v%d", version)
}

func clonePolicy(policy Policy) Policy {
	clone := policy
	if policy.AllowedAlgs != nil {
		clone.AllowedAlgs = append([]string(nil), policy.AllowedAlgs...)
	}
	return clone
}

func cloneImpact(impact Impact) Impact {
	clone := impact
	clone.Relaxations = append([]string(nil), impact.Relaxations...)
	clone.Restrictions = append([]string(nil), impact.Restrictions...)
	return clone
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func removeString(items []string, drop string) []string {
	if !containsString(items, drop) {
		return items
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if item != drop {
			out = append(out, item)
		}
	}
	return out
}

func signatureLive(expiresAt time.Time, now time.Time) bool {
	return expiresAt.IsZero() || expiresAt.After(now)
}

func algAllowed(allowedAlgs []string, alg string) bool {
	return containsString(allowedAlgs, alg)
}

// decideArtifact is the single verdict definition shared by impact analysis
// and runtime evaluation. An unsigned artifact is always rejected.
func decideArtifact(artifact Artifact, policy Policy, now time.Time) Decision {
	if policy.RequireSigned && !artifact.Signed {
		return DecisionDeny
	}
	if !artifact.Signed {
		return DecisionDeny
	}
	if !signatureLive(artifact.ExpiresAt, now) {
		return DecisionDeny
	}
	if !algAllowed(policy.AllowedAlgs, artifact.Alg) {
		return DecisionDeny
	}
	if artifact.KeyBits < policy.MinKeyBits {
		return DecisionDeny
	}
	return DecisionAllow
}

func (g *SigningPolicyChangeGuard) knownScopeLocked(namespace string) bool {
	for _, artifact := range g.artifacts {
		if artifact.Namespace == namespace {
			return true
		}
	}
	return false
}

func (g *SigningPolicyChangeGuard) policyByVersionLocked(version int) (Policy, bool) {
	if version == g.base.Version {
		return clonePolicy(g.base), true
	}
	if change, ok := g.changes[changeIDFor(version)]; ok {
		return clonePolicy(change.policy), true
	}
	return Policy{}, false
}

func (g *SigningPolicyChangeGuard) latestVersionLocked() int {
	latest := g.base.Version
	if len(g.order) > 0 {
		latest = g.changes[g.order[len(g.order)-1]].policy.Version
	}
	return latest
}

// Stage recomputes the impact of moving to next against the policy currently
// in force and stores the change without activating it.
func (g *SigningPolicyChangeGuard) Stage(next Policy) (Impact, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	changeID := changeIDFor(next.Version)
	if _, exists := g.changes[changeID]; exists {
		return Impact{}, ErrVersionExists
	}

	latest := g.latestVersionLocked()
	if next.Version != latest+1 {
		return Impact{}, ErrBadVersion
	}

	oldPolicy, ok := g.policyByVersionLocked(latest)
	if !ok {
		oldPolicy = clonePolicy(g.base)
	}
	nextPolicy := clonePolicy(next)

	ids := make([]string, 0, len(g.artifacts))
	for id := range g.artifacts {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	now := time.Now()
	impact := Impact{
		ChangeID:     changeID,
		FromVersion:  oldPolicy.Version,
		ToVersion:    nextPolicy.Version,
		Relaxations:  make([]string, 0),
		Restrictions: make([]string, 0),
	}
	for _, id := range ids {
		artifact := g.artifacts[id]
		from := decideArtifact(artifact, oldPolicy, now)
		to := decideArtifact(artifact, nextPolicy, now)
		switch {
		case from == DecisionDeny && to == DecisionAllow:
			impact.Relaxations = append(impact.Relaxations, id)
		case from == DecisionAllow && to == DecisionDeny:
			impact.Restrictions = append(impact.Restrictions, id)
		}
	}
	sort.Strings(impact.Relaxations)
	sort.Strings(impact.Restrictions)

	g.changes[changeID] = &stagedChange{
		policy: nextPolicy,
		impact: cloneImpact(impact),
	}
	g.order = append(g.order, changeID)

	return cloneImpact(impact), nil
}

// Approve records one approval token for a staged change. Tokens are bound to
// a single change and to their expiry time.
func (g *SigningPolicyChangeGuard) Approve(change string, token Approval) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	staged, ok := g.changes[change]
	if !ok {
		return ErrUnknownChange
	}
	if token.ChangeID != change || strings.TrimSpace(token.Approver) == "" {
		return ErrBadToken
	}
	if !token.ExpiresAt.After(time.Now()) {
		return ErrTokenExpired
	}
	for _, approval := range staged.approvals {
		if approval.Approver == token.Approver {
			return nil
		}
	}
	staged.approvals = append(staged.approvals, token)
	return nil
}

// Activate makes a change effective in one namespace. Relaxations need an
// approval first; activating the same namespace twice is idempotent.
func (g *SigningPolicyChangeGuard) Activate(change, scope string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	staged, ok := g.changes[change]
	if !ok {
		return ErrUnknownChange
	}
	if !g.knownScopeLocked(scope) {
		return ErrUnknownScope
	}
	if staged.impact.IsRelaxation() && len(staged.approvals) == 0 {
		return ErrNotApproved
	}

	if g.scopes[scope] == staged.policy.Version && containsString(staged.activeScope, scope) {
		return nil
	}

	// A newer activation supersedes whichever change previously owned the
	// namespace so that ActiveScopes only reports currently active scopes.
	if previousVersion, active := g.scopes[scope]; active && previousVersion != staged.policy.Version {
		if previous := g.changes[changeIDFor(previousVersion)]; previous != nil {
			previous.activeScope = removeString(previous.activeScope, scope)
		}
	}

	g.scopes[scope] = staged.policy.Version
	if !containsString(staged.activeScope, scope) {
		staged.activeScope = append(staged.activeScope, scope)
	}
	return nil
}

// Rollback withdraws every namespace activated by change and restores the
// previous version there, keeping the approval records.
func (g *SigningPolicyChangeGuard) Rollback(change string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	staged, ok := g.changes[change]
	if !ok {
		return ErrUnknownChange
	}
	if len(staged.activeScope) == 0 {
		return ErrNotActive
	}

	previousVersion := staged.impact.FromVersion
	scopes := append([]string(nil), staged.activeScope...)
	for _, scope := range scopes {
		currentVersion, active := g.scopes[scope]
		if active && currentVersion != staged.policy.Version {
			// The namespace was superseded by a later change; do not yank it
			// back from under the currently effective policy.
			continue
		}
		if previousVersion == g.base.Version {
			delete(g.scopes, scope)
		} else {
			g.scopes[scope] = previousVersion
			if previous, ok := g.changes[changeIDFor(previousVersion)]; ok {
				if !containsString(previous.activeScope, scope) {
					previous.activeScope = append(previous.activeScope, scope)
				}
			}
		}
	}
	staged.activeScope = nil
	return nil
}

// Effective returns the policy version in force for one namespace.
func (g *SigningPolicyChangeGuard) Effective(namespace string) (Policy, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if !g.knownScopeLocked(namespace) {
		return Policy{}, ErrUnknownScope
	}
	if version, active := g.scopes[namespace]; active {
		if policy, ok := g.policyByVersionLocked(version); ok {
			return policy, nil
		}
	}
	return clonePolicy(g.base), nil
}

// ImpactOf returns the impact computed when change was staged.
func (g *SigningPolicyChangeGuard) ImpactOf(change string) (Impact, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	staged, ok := g.changes[change]
	if !ok {
		return Impact{}, ErrUnknownChange
	}
	return cloneImpact(staged.impact), nil
}

// Evaluate decides one artifact against the effective policy of its namespace.
// The whole decision runs under the guard lock, so the policy version is
// pinned for the duration of the verdict and can never be half old/new.
func (g *SigningPolicyChangeGuard) Evaluate(id string) (Decision, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	artifact, ok := g.artifacts[id]
	if !ok {
		return "", ErrUnknownArtifact
	}

	policy := clonePolicy(g.base)
	if version, active := g.scopes[artifact.Namespace]; active {
		if resolved, ok := g.policyByVersionLocked(version); ok {
			policy = resolved
		}
	}

	return decideArtifact(artifact, policy, time.Now()), nil
}

// ChangeIDs lists the staged change IDs in ascending (stage/version) order.
func (g *SigningPolicyChangeGuard) ChangeIDs() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.order...)
}

// Approved lists the approvers recorded for one change in insertion order.
func (g *SigningPolicyChangeGuard) Approved(change string) []string {
	g.mu.Lock()
	defer g.mu.Unlock()

	staged, ok := g.changes[change]
	if !ok {
		return nil
	}
	approvers := make([]string, 0, len(staged.approvals))
	for _, approval := range staged.approvals {
		approvers = append(approvers, approval.Approver)
	}
	return approvers
}

// ActiveScopes lists the namespaces where change is currently active.
func (g *SigningPolicyChangeGuard) ActiveScopes(change string) []string {
	g.mu.Lock()
	defer g.mu.Unlock()

	staged, ok := g.changes[change]
	if !ok {
		return nil
	}
	return append([]string(nil), staged.activeScope...)
}

const snapshotFormat = "signing-policy-change-guard/v1"

type snapshotChange struct {
	ID          string     `json:"id"`
	Policy      Policy     `json:"policy"`
	Impact      Impact     `json:"impact"`
	Approvals   []Approval `json:"approvals"`
	ActiveScope []string   `json:"active_scope"`
}

type snapshotDocument struct {
	Format    string           `json:"format"`
	Base      Policy           `json:"base"`
	Artifacts []Artifact       `json:"artifacts"`
	Order     []string         `json:"order"`
	Changes   []snapshotChange `json:"changes"`
	Scopes    map[string]int   `json:"scopes"`
}

// Snapshot serialises the policy versions, approvals and activations.
func (g *SigningPolicyChangeGuard) Snapshot() ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	document := snapshotDocument{
		Format:    snapshotFormat,
		Base:      clonePolicy(g.base),
		Artifacts: make([]Artifact, 0, len(g.artifacts)),
		Order:     append([]string(nil), g.order...),
		Changes:   make([]snapshotChange, 0, len(g.order)),
		Scopes:    make(map[string]int, len(g.scopes)),
	}
	for _, artifact := range g.artifacts {
		document.Artifacts = append(document.Artifacts, artifact)
	}
	sort.Slice(document.Artifacts, func(i, j int) bool {
		return document.Artifacts[i].ID < document.Artifacts[j].ID
	})
	for scope, version := range g.scopes {
		document.Scopes[scope] = version
	}
	for _, id := range g.order {
		staged := g.changes[id]
		document.Changes = append(document.Changes, snapshotChange{
			ID:          id,
			Policy:      clonePolicy(staged.policy),
			Impact:      cloneImpact(staged.impact),
			Approvals:   append([]Approval(nil), staged.approvals...),
			ActiveScope: append([]string(nil), staged.activeScope...),
		})
	}

	data, err := json.Marshal(document)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Restore replaces the guard state from a snapshot. A corrupt payload leaves
// the guard untouched.
func (g *SigningPolicyChangeGuard) Restore(data []byte) error {
	var document snapshotDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	if document.Format != snapshotFormat {
		return fmt.Errorf("%w: unsupported snapshot format", ErrBadSnapshot)
	}

	artifacts := make(map[string]Artifact, len(document.Artifacts))
	for _, artifact := range document.Artifacts {
		if artifact.ID == "" {
			return fmt.Errorf("%w: artifact is missing an id", ErrBadSnapshot)
		}
		artifacts[artifact.ID] = artifact
	}

	changes := make(map[string]*stagedChange, len(document.Changes))
	order := make([]string, 0, len(document.Changes))
	for _, item := range document.Changes {
		if item.ID == "" {
			return fmt.Errorf("%w: change is missing an id", ErrBadSnapshot)
		}
		if _, duplicated := changes[item.ID]; duplicated {
			return fmt.Errorf("%w: duplicate change %q", ErrBadSnapshot, item.ID)
		}
		impact := item.Impact
		impact.ChangeID = item.ID
		changes[item.ID] = &stagedChange{
			policy:      clonePolicy(item.Policy),
			impact:      impact,
			approvals:   append([]Approval(nil), item.Approvals...),
			activeScope: append([]string(nil), item.ActiveScope...),
		}
		order = append(order, item.ID)
	}

	for _, id := range document.Order {
		if _, ok := changes[id]; !ok {
			return fmt.Errorf("%w: order references unknown change %q", ErrBadSnapshot, id)
		}
	}
	if len(document.Order) > 0 {
		order = append([]string(nil), document.Order...)
	}

	base := clonePolicy(document.Base)
	scopes := make(map[string]int, len(document.Scopes))
	for scope, version := range document.Scopes {
		if _, ok := func() (Policy, bool) {
			if version == base.Version {
				return base, true
			}
			policy, ok := changes[changeIDFor(version)]
			if !ok {
				return Policy{}, false
			}
			return policy.policy, true
		}(); !ok {
			return fmt.Errorf("%w: scope %q references unknown policy version %d", ErrBadSnapshot, scope, version)
		}
		scopes[scope] = version
	}

	// The payload is fully validated and independent of live state; only now
	// replace the guard contents under the lock.
	g.mu.Lock()
	g.base = base
	g.artifacts = artifacts
	g.changes = changes
	g.order = order
	g.scopes = scopes
	g.mu.Unlock()
	return nil
}
