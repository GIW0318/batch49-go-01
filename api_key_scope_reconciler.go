// Package task reconciles stored API key scopes with the live permissions of
// their owners, and authorises requests against the narrowed scope set.
package task

import (
	"errors"
	"time"
)

// errNotImplemented marks behaviour that is not built yet.
var errNotImplemented = errors.New("not implemented")

// Decision reason codes.
const (
	ReasonScopeOK      = "scope_ok"
	ReasonScopeMissing = "scope_missing"
)

// Errors returned by the reconciler.
var (
	ErrBlankKey     = errors.New("key id is required")
	ErrBlankOwner   = errors.New("owner id is required")
	ErrUnknownKey   = errors.New("unknown api key")
	ErrUnknownOwner = errors.New("unknown owner")
	ErrUnknownDiff  = errors.New("unknown scope diff")
	ErrBadScope     = errors.New("scope must be resource:action")
	ErrKeyLimit     = errors.New("key limit reached for this owner")
	ErrScopeLimit   = errors.New("scope limit reached for this key")
	ErrBadPriority  = errors.New("priority must be between 0 and 5")
	ErrDiffStale    = errors.New("scope diff no longer matches the key version")
	ErrBadSnapshot  = errors.New("snapshot payload is invalid")
)

// ScopeLimits bounds one reconciler instance.
type ScopeLimits struct {
	MaxKeysPerOwner int
	MaxScopesPerKey int
}

// ImpactEntry reports the scope loss of one key.
type ImpactEntry struct {
	KeyID string
	Lost  []string
	Kept  []string
}

// Report is the impact of the current owner permissions on all owned keys.
type Report struct {
	OwnerID string
	Keys    int
	Entries []ImpactEntry
}

// Diff is a computed but not yet applied scope narrowing.
type Diff struct {
	ID        string
	KeyID     string
	OwnerID   string
	Before    []string
	After     []string
	Lost      []string
	Version   uint64
	CreatedAt time.Time
}

// Result is the outcome of applying a diff.
type Result struct {
	DiffID  string
	KeyID   string
	Applied bool
	Version uint64
	Lost    []string
}

// Decision is an authorisation answer for one request.
type Decision struct {
	Allowed  bool
	Reason   string
	Version  uint64
	Priority int
}

// ApiKeyScopeReconciler owns keys, owner permissions and pending diffs.
type ApiKeyScopeReconciler struct {
	limits ScopeLimits
}

// NewApiKeyScopeReconciler builds a reconciler with the given limits.
func NewApiKeyScopeReconciler(limits ScopeLimits) *ApiKeyScopeReconciler {
	return &ApiKeyScopeReconciler{limits: limits}
}

// RegisterKey stores a new key with its issued scopes.
func (r *ApiKeyScopeReconciler) RegisterKey(ownerID, keyID string, scopes []string) error {
	return errNotImplemented
}

// SetOwnerPermissions replaces the live permissions of an owner.
func (r *ApiKeyScopeReconciler) SetOwnerPermissions(ownerID string, perms []string) error {
	return errNotImplemented
}

// Impact reports which scopes the owner keys would lose, without changing keys.
func (r *ApiKeyScopeReconciler) Impact(ownerID string) (Report, error) {
	return Report{}, errNotImplemented
}

// Reconcile computes the narrowing diff of one key.
func (r *ApiKeyScopeReconciler) Reconcile(keyID string) (Diff, error) {
	return Diff{}, errNotImplemented
}

// Apply lands a previously computed diff.
func (r *ApiKeyScopeReconciler) Apply(diffID string) (Result, error) {
	return Result{}, errNotImplemented
}

// Authorize decides one request against the key and owner permissions.
func (r *ApiKeyScopeReconciler) Authorize(keyID, resource, action string) (Decision, error) {
	return Decision{}, errNotImplemented
}

// Priority sets the review priority of a key.
func (r *ApiKeyScopeReconciler) Priority(keyID string, level int) error {
	return errNotImplemented
}

// Snapshot serialises keys, permissions and versions.
func (r *ApiKeyScopeReconciler) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the reconciler state from a snapshot.
func (r *ApiKeyScopeReconciler) Restore(data []byte) error {
	return errNotImplemented
}

// KeyScopes returns the currently stored scopes of a key.
func (r *ApiKeyScopeReconciler) KeyScopes(keyID string) ([]string, bool) {
	return nil, false
}

// Version returns the current version of a key.
func (r *ApiKeyScopeReconciler) Version(keyID string) (uint64, bool) {
	return 0, false
}
