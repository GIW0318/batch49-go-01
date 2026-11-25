// Package task reconciles stored API key scopes with the live permissions of
// their owners, and authorises requests against the narrowed scope set.
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

// errKeyExists rejects re-registration of an existing key id.
var errKeyExists = errors.New("api key already registered")

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

// keyState is the stored record of one API key.
type keyState struct {
	ownerID  string
	scopes   []string
	version  uint64
	priority int
}

// ApiKeyScopeReconciler owns keys, owner permissions and pending diffs.
type ApiKeyScopeReconciler struct {
	limits      ScopeLimits
	mu          sync.RWMutex
	owners      map[string]map[string]struct{}
	keys        map[string]*keyState
	diffs       map[string]Diff
	diffCounter uint64
}

// NewApiKeyScopeReconciler builds a reconciler with the given limits.
func NewApiKeyScopeReconciler(limits ScopeLimits) *ApiKeyScopeReconciler {
	return &ApiKeyScopeReconciler{
		limits: limits,
		owners: make(map[string]map[string]struct{}),
		keys:   make(map[string]*keyState),
		diffs:  make(map[string]Diff),
	}
}

// validScope reports whether scope is exactly "resource:action".
func validScope(scope string) bool {
	resource, action, found := strings.Cut(scope, ":")
	return found && resource != "" && action != "" && !strings.Contains(action, ":")
}

// normalizeScopes validates, dedupes and sorts scope strings.
func normalizeScopes(scopes []string) ([]string, error) {
	seen := make(map[string]struct{}, len(scopes))
	out := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		if !validScope(scope) {
			return nil, ErrBadScope
		}
		if _, dup := seen[scope]; dup {
			continue
		}
		seen[scope] = struct{}{}
		out = append(out, scope)
	}
	sort.Strings(out)
	return out, nil
}

// intersect returns the sorted scopes present in both lists.
func intersect(scopes []string, perms map[string]struct{}) []string {
	out := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		if _, ok := perms[scope]; ok {
			out = append(out, scope)
		}
	}
	return out
}

// subtract returns the sorted scopes present in before but not in after.
func subtract(before, after []string) []string {
	kept := make(map[string]struct{}, len(after))
	for _, scope := range after {
		kept[scope] = struct{}{}
	}
	out := make([]string, 0, len(before))
	for _, scope := range before {
		if _, ok := kept[scope]; !ok {
			out = append(out, scope)
		}
	}
	return out
}

// cloneStrings copies a string slice so callers cannot mutate stored state.
func cloneStrings(in []string) []string {
	out := make([]string, len(in))
	copy(out, in)
	return out
}

// RegisterKey stores a new key with its issued scopes.
func (r *ApiKeyScopeReconciler) RegisterKey(ownerID, keyID string, scopes []string) error {
	if keyID == "" {
		return ErrBlankKey
	}
	if ownerID == "" {
		return ErrBlankOwner
	}
	normalized, err := normalizeScopes(scopes)
	if err != nil {
		return err
	}
	if r.limits.MaxScopesPerKey > 0 && len(normalized) > r.limits.MaxScopesPerKey {
		return ErrScopeLimit
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.owners[ownerID]; !ok {
		return ErrUnknownOwner
	}
	if _, ok := r.keys[keyID]; ok {
		return errKeyExists
	}
	if r.limits.MaxKeysPerOwner > 0 {
		count := 0
		for _, key := range r.keys {
			if key.ownerID == ownerID {
				count++
			}
		}
		if count >= r.limits.MaxKeysPerOwner {
			return ErrKeyLimit
		}
	}
	r.keys[keyID] = &keyState{
		ownerID: ownerID,
		scopes:  normalized,
		version: 1,
	}
	return nil
}

// SetOwnerPermissions replaces the live permissions of an owner.
func (r *ApiKeyScopeReconciler) SetOwnerPermissions(ownerID string, perms []string) error {
	if ownerID == "" {
		return ErrBlankOwner
	}
	normalized, err := normalizeScopes(perms)
	if err != nil {
		return err
	}
	set := make(map[string]struct{}, len(normalized))
	for _, perm := range normalized {
		set[perm] = struct{}{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.owners[ownerID] = set
	return nil
}

// Impact reports which scopes the owner keys would lose, without changing keys.
func (r *ApiKeyScopeReconciler) Impact(ownerID string) (Report, error) {
	if ownerID == "" {
		return Report{}, ErrBlankOwner
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	perms, ok := r.owners[ownerID]
	if !ok {
		return Report{}, ErrUnknownOwner
	}
	report := Report{OwnerID: ownerID, Entries: []ImpactEntry{}}
	keyIDs := make([]string, 0, len(r.keys))
	for keyID, key := range r.keys {
		if key.ownerID == ownerID {
			keyIDs = append(keyIDs, keyID)
		}
	}
	sort.Strings(keyIDs)
	for _, keyID := range keyIDs {
		key := r.keys[keyID]
		kept := intersect(key.scopes, perms)
		report.Entries = append(report.Entries, ImpactEntry{
			KeyID: keyID,
			Lost:  subtract(key.scopes, kept),
			Kept:  kept,
		})
	}
	report.Keys = len(report.Entries)
	return report, nil
}

// Reconcile computes the narrowing diff of one key.
func (r *ApiKeyScopeReconciler) Reconcile(keyID string) (Diff, error) {
	if keyID == "" {
		return Diff{}, ErrBlankKey
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key, ok := r.keys[keyID]
	if !ok {
		return Diff{}, ErrUnknownKey
	}
	perms := r.owners[key.ownerID]
	after := intersect(key.scopes, perms)
	r.diffCounter++
	diff := Diff{
		ID:        fmt.Sprintf("diff-%d", r.diffCounter),
		KeyID:     keyID,
		OwnerID:   key.ownerID,
		Before:    cloneStrings(key.scopes),
		After:     after,
		Lost:      subtract(key.scopes, after),
		Version:   key.version,
		CreatedAt: time.Now(),
	}
	r.diffs[diff.ID] = diff
	return diff, nil
}

// Apply lands a previously computed diff.
func (r *ApiKeyScopeReconciler) Apply(diffID string) (Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	diff, ok := r.diffs[diffID]
	if !ok {
		return Result{}, ErrUnknownDiff
	}
	key, ok := r.keys[diff.KeyID]
	if !ok {
		return Result{}, ErrUnknownKey
	}
	if diff.Version != key.version {
		return Result{}, ErrDiffStale
	}
	// Recompute against the owner permissions live at apply time so a
	// permission change between reconcile and apply is honoured.
	perms := r.owners[key.ownerID]
	after := intersect(key.scopes, perms)
	lost := subtract(key.scopes, after)
	key.scopes = after
	key.version++
	return Result{
		DiffID:  diffID,
		KeyID:   diff.KeyID,
		Applied: true,
		Version: key.version,
		Lost:    lost,
	}, nil
}

// Authorize decides one request against the key and owner permissions.
func (r *ApiKeyScopeReconciler) Authorize(keyID, resource, action string) (Decision, error) {
	if keyID == "" {
		return Decision{}, ErrBlankKey
	}
	scope := resource + ":" + action
	for {
		r.mu.RLock()
		key, ok := r.keys[keyID]
		if !ok {
			r.mu.RUnlock()
			return Decision{}, ErrUnknownKey
		}
		version := key.version
		priority := key.priority
		allowed := false
		if _, ok := r.owners[key.ownerID][scope]; ok {
			for _, stored := range key.scopes {
				if stored == scope {
					allowed = true
					break
				}
			}
		}
		r.mu.RUnlock()

		// Re-check the version before landing the decision: if a reconcile
		// applied while we computed, redo the check on the newer snapshot.
		r.mu.RLock()
		current, ok := r.keys[keyID]
		stillCurrent := ok && current.version == version
		r.mu.RUnlock()
		if !stillCurrent {
			if !ok {
				return Decision{}, ErrUnknownKey
			}
			continue
		}

		decision := Decision{Allowed: allowed, Version: version, Priority: priority}
		if allowed {
			decision.Reason = ReasonScopeOK
		} else {
			decision.Reason = ReasonScopeMissing
		}
		return decision, nil
	}
}

// Priority sets the review priority of a key.
func (r *ApiKeyScopeReconciler) Priority(keyID string, level int) error {
	if level < 0 || level > 5 {
		return ErrBadPriority
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key, ok := r.keys[keyID]
	if !ok {
		return ErrUnknownKey
	}
	key.priority = level
	return nil
}

// snapshotFile is the on-disk layout of a reconciler snapshot.
type snapshotFile struct {
	Format      int                 `json:"format"`
	Owners      map[string][]string `json:"owners"`
	Keys        []snapshotKey       `json:"keys"`
	DiffCounter uint64              `json:"diffCounter"`
}

// snapshotKey is the serialised form of one key.
type snapshotKey struct {
	ID       string   `json:"id"`
	OwnerID  string   `json:"owner"`
	Scopes   []string `json:"scopes"`
	Version  uint64   `json:"version"`
	Priority int      `json:"priority"`
}

// Snapshot serialises keys, permissions and versions.
func (r *ApiKeyScopeReconciler) Snapshot() ([]byte, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	file := snapshotFile{
		Format:      1,
		Owners:      make(map[string][]string, len(r.owners)),
		Keys:        make([]snapshotKey, 0, len(r.keys)),
		DiffCounter: r.diffCounter,
	}
	for ownerID, perms := range r.owners {
		list := make([]string, 0, len(perms))
		for perm := range perms {
			list = append(list, perm)
		}
		sort.Strings(list)
		file.Owners[ownerID] = list
	}
	keyIDs := make([]string, 0, len(r.keys))
	for keyID := range r.keys {
		keyIDs = append(keyIDs, keyID)
	}
	sort.Strings(keyIDs)
	for _, keyID := range keyIDs {
		key := r.keys[keyID]
		file.Keys = append(file.Keys, snapshotKey{
			ID:       keyID,
			OwnerID:  key.ownerID,
			Scopes:   cloneStrings(key.scopes),
			Version:  key.version,
			Priority: key.priority,
		})
	}
	return json.Marshal(file)
}

// Restore replaces the reconciler state from a snapshot.
func (r *ApiKeyScopeReconciler) Restore(data []byte) error {
	var file snapshotFile
	if err := json.Unmarshal(data, &file); err != nil {
		return ErrBadSnapshot
	}
	if file.Format != 1 || file.Owners == nil || file.Keys == nil {
		return ErrBadSnapshot
	}

	owners := make(map[string]map[string]struct{}, len(file.Owners))
	for ownerID, perms := range file.Owners {
		if ownerID == "" {
			return ErrBadSnapshot
		}
		normalized, err := normalizeScopes(perms)
		if err != nil {
			return ErrBadSnapshot
		}
		set := make(map[string]struct{}, len(normalized))
		for _, perm := range normalized {
			set[perm] = struct{}{}
		}
		owners[ownerID] = set
	}

	keys := make(map[string]*keyState, len(file.Keys))
	for _, entry := range file.Keys {
		if entry.ID == "" || entry.OwnerID == "" || entry.Version == 0 {
			return ErrBadSnapshot
		}
		if entry.Priority < 0 || entry.Priority > 5 {
			return ErrBadSnapshot
		}
		if _, ok := owners[entry.OwnerID]; !ok {
			return ErrBadSnapshot
		}
		if _, dup := keys[entry.ID]; dup {
			return ErrBadSnapshot
		}
		normalized, err := normalizeScopes(entry.Scopes)
		if err != nil {
			return ErrBadSnapshot
		}
		keys[entry.ID] = &keyState{
			ownerID:  entry.OwnerID,
			scopes:   normalized,
			version:  entry.Version,
			priority: entry.Priority,
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.owners = owners
	r.keys = keys
	r.diffs = make(map[string]Diff)
	r.diffCounter = file.DiffCounter
	return nil
}

// KeyScopes returns the currently stored scopes of a key.
func (r *ApiKeyScopeReconciler) KeyScopes(keyID string) ([]string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	key, ok := r.keys[keyID]
	if !ok {
		return nil, false
	}
	return cloneStrings(key.scopes), true
}

// Version returns the current version of a key.
func (r *ApiKeyScopeReconciler) Version(keyID string) (uint64, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	key, ok := r.keys[keyID]
	if !ok {
		return 0, false
	}
	return key.version, true
}
