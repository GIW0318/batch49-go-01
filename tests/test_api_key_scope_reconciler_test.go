package task

import (
	"errors"
	"testing"

	impl "gsb/batch49-go-safety-a-25"
)

func scopeLimits() impl.ScopeLimits {
	return impl.ScopeLimits{
		MaxKeysPerOwner: 4,
		MaxScopesPerKey: 4,
	}
}

func hasString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func setupReconciler(t *testing.T, keyScopes, ownerPerms []string) *impl.ApiKeyScopeReconciler {
	t.Helper()
	r := impl.NewApiKeyScopeReconciler(scopeLimits())
	if err := r.SetOwnerPermissions("owner-1", ownerPerms); err != nil {
		t.Fatalf("owner permissions must be accepted: %v", err)
	}
	if err := r.RegisterKey("owner-1", "key-1", keyScopes); err != nil {
		t.Fatalf("key registration must succeed: %v", err)
	}
	return r
}

func TestRegisterKeyStartsAtVersionOne(t *testing.T) {
	r := setupReconciler(t, []string{"repo:read"}, []string{"repo:read"})
	scopes, ok := r.KeyScopes("key-1")
	if !ok {
		t.Fatalf("registered key must be found")
	}
	if len(scopes) != 1 || scopes[0] != "repo:read" {
		t.Fatalf("issued scopes must be stored, got %v", scopes)
	}
	version, ok := r.Version("key-1")
	if !ok {
		t.Fatalf("registered key must expose a version")
	}
	if version != 1 {
		t.Fatalf("new key must start at version 1, got %d", version)
	}
}

func TestRegisterRejectsBlankKey(t *testing.T) {
	r := impl.NewApiKeyScopeReconciler(scopeLimits())
	if err := r.SetOwnerPermissions("owner-1", []string{"repo:read"}); err != nil {
		t.Fatalf("owner permissions must be accepted: %v", err)
	}
	if err := r.RegisterKey("owner-1", "", []string{"repo:read"}); !errors.Is(err, impl.ErrBlankKey) {
		t.Fatalf("blank key id must return ErrBlankKey, got %v", err)
	}
}

func TestRegisterRejectsBadScope(t *testing.T) {
	r := impl.NewApiKeyScopeReconciler(scopeLimits())
	if err := r.SetOwnerPermissions("owner-1", []string{"repo:read"}); err != nil {
		t.Fatalf("owner permissions must be accepted: %v", err)
	}
	if err := r.RegisterKey("owner-1", "key-1", []string{"repo-read"}); !errors.Is(err, impl.ErrBadScope) {
		t.Fatalf("scope without a colon must return ErrBadScope, got %v", err)
	}
}

func TestRegisterEnforcesKeyLimit(t *testing.T) {
	limits := scopeLimits()
	limits.MaxKeysPerOwner = 2
	r := impl.NewApiKeyScopeReconciler(limits)
	if err := r.SetOwnerPermissions("owner-1", []string{"repo:read"}); err != nil {
		t.Fatalf("owner permissions must be accepted: %v", err)
	}
	if err := r.RegisterKey("owner-1", "key-a", []string{"repo:read"}); err != nil {
		t.Fatalf("first key must be accepted: %v", err)
	}
	if err := r.RegisterKey("owner-1", "key-b", []string{"repo:read"}); err != nil {
		t.Fatalf("second key must be accepted: %v", err)
	}
	if err := r.RegisterKey("owner-1", "key-c", []string{"repo:read"}); !errors.Is(err, impl.ErrKeyLimit) {
		t.Fatalf("third key must return ErrKeyLimit, got %v", err)
	}
}

func TestRegisterEnforcesScopeLimit(t *testing.T) {
	limits := scopeLimits()
	limits.MaxScopesPerKey = 3
	perms := []string{"repo:read", "repo:write", "repo:delete", "repo:admin"}
	r := impl.NewApiKeyScopeReconciler(limits)
	if err := r.SetOwnerPermissions("owner-1", perms); err != nil {
		t.Fatalf("owner permissions must be accepted: %v", err)
	}
	err := r.RegisterKey("owner-1", "key-1", perms)
	if !errors.Is(err, impl.ErrScopeLimit) {
		t.Fatalf("four scopes must return ErrScopeLimit, got %v", err)
	}
}

func TestAuthorizeAllowsIntersectedScope(t *testing.T) {
	r := setupReconciler(t, []string{"repo:read"}, []string{"repo:read", "repo:write"})
	decision, err := r.Authorize("key-1", "repo", "read")
	if err != nil {
		t.Fatalf("authorize must succeed: %v", err)
	}
	if !decision.Allowed {
		t.Fatalf("a scope present on both sides must be allowed, reason=%q", decision.Reason)
	}
	if decision.Reason != impl.ReasonScopeOK {
		t.Fatalf("allowed decision must carry reason %q, got %q", impl.ReasonScopeOK, decision.Reason)
	}
	if decision.Version != 1 {
		t.Fatalf("decision must carry the key version, got %d", decision.Version)
	}
}

func TestAuthorizeDeniesUnlistedAction(t *testing.T) {
	r := setupReconciler(t, []string{"repo:read"}, []string{"repo:read", "repo:delete"})
	decision, err := r.Authorize("key-1", "repo", "delete")
	if err != nil {
		t.Fatalf("authorize must succeed: %v", err)
	}
	if decision.Allowed {
		t.Fatalf("an action missing from the key scopes must be denied")
	}
	if decision.Reason != impl.ReasonScopeMissing {
		t.Fatalf("denied decision must carry reason %q, got %q", impl.ReasonScopeMissing, decision.Reason)
	}
}

func TestAuthorizeRejectsUnknownKey(t *testing.T) {
	r := setupReconciler(t, []string{"repo:read"}, []string{"repo:read"})
	if _, err := r.Authorize("key-missing", "repo", "read"); !errors.Is(err, impl.ErrUnknownKey) {
		t.Fatalf("unknown key must return ErrUnknownKey, got %v", err)
	}
}

func TestImpactListsLostScopesWithoutChangingKey(t *testing.T) {
	r := setupReconciler(t, []string{"repo:read", "repo:write"}, []string{"repo:read", "repo:write"})
	if err := r.SetOwnerPermissions("owner-1", []string{"repo:read"}); err != nil {
		t.Fatalf("narrowing owner permissions must succeed: %v", err)
	}
	report, err := r.Impact("owner-1")
	if err != nil {
		t.Fatalf("impact must succeed: %v", err)
	}
	if report.OwnerID != "owner-1" {
		t.Fatalf("report must echo the owner, got %q", report.OwnerID)
	}
	if report.Keys != 1 {
		t.Fatalf("report must count one key, got %d", report.Keys)
	}
	if len(report.Entries) != 1 {
		t.Fatalf("report must contain one entry, got %d", len(report.Entries))
	}
	entry := report.Entries[0]
	if entry.KeyID != "key-1" {
		t.Fatalf("entry must reference the key, got %q", entry.KeyID)
	}
	if !hasString(entry.Lost, "repo:write") {
		t.Fatalf("entry must list the lost scope, got %v", entry.Lost)
	}
	if !hasString(entry.Kept, "repo:read") {
		t.Fatalf("entry must list the kept scope, got %v", entry.Kept)
	}
	scopes, ok := r.KeyScopes("key-1")
	if !ok || len(scopes) != 2 {
		t.Fatalf("impact must not modify the stored key scopes, got %v", scopes)
	}
	version, _ := r.Version("key-1")
	if version != 1 {
		t.Fatalf("impact must not bump the key version, got %d", version)
	}
}

func TestReconcileBuildsDiffWithoutApplying(t *testing.T) {
	r := setupReconciler(t, []string{"repo:read", "repo:write"}, []string{"repo:read", "repo:write"})
	if err := r.SetOwnerPermissions("owner-1", []string{"repo:read"}); err != nil {
		t.Fatalf("narrowing owner permissions must succeed: %v", err)
	}
	diff, err := r.Reconcile("key-1")
	if err != nil {
		t.Fatalf("reconcile must succeed: %v", err)
	}
	if diff.ID == "" {
		t.Fatalf("diff identifier must not be empty")
	}
	if diff.KeyID != "key-1" || diff.OwnerID != "owner-1" {
		t.Fatalf("diff must reference key and owner, got %q / %q", diff.KeyID, diff.OwnerID)
	}
	if len(diff.Before) != 2 {
		t.Fatalf("diff must carry both old scopes, got %v", diff.Before)
	}
	if len(diff.After) != 1 || diff.After[0] != "repo:read" {
		t.Fatalf("diff must narrow to the remaining scope, got %v", diff.After)
	}
	if !hasString(diff.Lost, "repo:write") {
		t.Fatalf("diff must record the lost scope, got %v", diff.Lost)
	}
	scopes, _ := r.KeyScopes("key-1")
	if len(scopes) != 2 {
		t.Fatalf("reconcile must not apply the diff, got %v", scopes)
	}
}

func TestApplyBumpsVersionAndShrinksScopes(t *testing.T) {
	r := setupReconciler(t, []string{"repo:read", "repo:write"}, []string{"repo:read", "repo:write"})
	if err := r.SetOwnerPermissions("owner-1", []string{"repo:read"}); err != nil {
		t.Fatalf("narrowing owner permissions must succeed: %v", err)
	}
	diff, err := r.Reconcile("key-1")
	if err != nil {
		t.Fatalf("reconcile must succeed: %v", err)
	}
	result, err := r.Apply(diff.ID)
	if err != nil {
		t.Fatalf("apply must succeed: %v", err)
	}
	if !result.Applied {
		t.Fatalf("applied result must be marked applied")
	}
	if result.Version != 2 {
		t.Fatalf("apply must bump the version to 2, got %d", result.Version)
	}
	if !hasString(result.Lost, "repo:write") {
		t.Fatalf("apply result must report the lost scope, got %v", result.Lost)
	}
	scopes, _ := r.KeyScopes("key-1")
	if len(scopes) != 1 || scopes[0] != "repo:read" {
		t.Fatalf("apply must shrink the stored scopes, got %v", scopes)
	}
	version, _ := r.Version("key-1")
	if version != 2 {
		t.Fatalf("key version must be 2 after apply, got %d", version)
	}
}

func TestApplyUnknownDiff(t *testing.T) {
	r := setupReconciler(t, []string{"repo:read"}, []string{"repo:read"})
	if _, err := r.Apply("diff-missing"); !errors.Is(err, impl.ErrUnknownDiff) {
		t.Fatalf("unknown diff must return ErrUnknownDiff, got %v", err)
	}
}

func TestPriorityBoundsAndDecision(t *testing.T) {
	r := setupReconciler(t, []string{"repo:read"}, []string{"repo:read"})
	if err := r.Priority("key-1", 9); !errors.Is(err, impl.ErrBadPriority) {
		t.Fatalf("priority 9 must return ErrBadPriority, got %v", err)
	}
	if err := r.Priority("key-1", 3); err != nil {
		t.Fatalf("priority 3 must be accepted: %v", err)
	}
	decision, err := r.Authorize("key-1", "repo", "read")
	if err != nil {
		t.Fatalf("authorize must succeed: %v", err)
	}
	if decision.Priority != 3 {
		t.Fatalf("decision must carry the stored priority 3, got %d", decision.Priority)
	}
}

func TestSnapshotRestoreKeepsVersion(t *testing.T) {
	r := setupReconciler(t, []string{"repo:read", "repo:write"}, []string{"repo:read", "repo:write"})
	if err := r.SetOwnerPermissions("owner-1", []string{"repo:read"}); err != nil {
		t.Fatalf("narrowing owner permissions must succeed: %v", err)
	}
	diff, err := r.Reconcile("key-1")
	if err != nil {
		t.Fatalf("reconcile must succeed: %v", err)
	}
	if _, err := r.Apply(diff.ID); err != nil {
		t.Fatalf("apply must succeed: %v", err)
	}
	snap, err := r.Snapshot()
	if err != nil {
		t.Fatalf("snapshot must succeed: %v", err)
	}
	restored := impl.NewApiKeyScopeReconciler(scopeLimits())
	if err := restored.Restore(snap); err != nil {
		t.Fatalf("restore must succeed: %v", err)
	}
	scopes, ok := restored.KeyScopes("key-1")
	if !ok || len(scopes) != 1 || scopes[0] != "repo:read" {
		t.Fatalf("restored scopes must stay narrowed, got %v", scopes)
	}
	version, ok := restored.Version("key-1")
	if !ok || version != 2 {
		t.Fatalf("restored version must stay 2, got %d ok=%v", version, ok)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	r := setupReconciler(t, []string{"repo:read"}, []string{"repo:read"})
	if err := r.Restore([]byte("::broken::")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must return ErrBadSnapshot, got %v", err)
	}
}
