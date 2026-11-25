package task

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	impl "gsb/batch49-go-safety-c-08"
)

func base() time.Time { return time.Date(2026, 4, 10, 9, 0, 0, 0, time.UTC) }

func validDigest() impl.Digest {
	return impl.Digest{Algorithm: "sha256", Hex: strings.Repeat("a", 64)}
}

func releasePolicy() impl.Policy {
	return impl.Policy{Threshold: 2, Signers: []string{"alice", "bob", "carol"}, TrustRoot: "root-prod"}
}

func signingKey(id, signer string) impl.Key {
	return impl.Key{
		KeyID:     id,
		Signer:    signer,
		NotBefore: base().Add(-24 * time.Hour),
		NotAfter:  base().Add(24 * time.Hour),
		Chain:     []string{"leaf-" + id, "root-prod"},
	}
}

func sigFor(id string, at time.Time) impl.Signature {
	return impl.Signature{KeyID: id, At: at, LogIndex: 1, LogTime: at, Included: true, Value: "value-" + id}
}

func hasReason(reasons []string, want string) bool {
	for _, r := range reasons {
		if r == want {
			return true
		}
	}
	return false
}

func setupVerifier(t *testing.T, keys []impl.Key, p impl.Policy) *impl.SigVerifier {
	t.Helper()
	v := impl.NewSigVerifier()
	if v == nil {
		t.Fatalf("NewSigVerifier returned nil")
	}
	for _, k := range keys {
		if err := v.RegisterKey(k); err != nil {
			t.Fatalf("register key %s: %v", k.KeyID, err)
		}
	}
	if err := v.SetPolicy(p); err != nil {
		t.Fatalf("set policy: %v", err)
	}
	return v
}

func threeKeys() []impl.Key {
	return []impl.Key{signingKey("k-alice", "alice"), signingKey("k-bob", "bob"), signingKey("k-carol", "carol")}
}

func TestVerifyRequiresValidPolicy(t *testing.T) {
	v := impl.NewSigVerifier()
	if _, err := v.Verify(validDigest(), nil); !errors.Is(err, impl.ErrNoPolicy) {
		t.Fatalf("verify without a policy must fail with ErrNoPolicy, got %v", err)
	}
	badPolicies := []impl.Policy{
		{Threshold: 0, Signers: []string{"alice"}, TrustRoot: "root-prod"},
		{Threshold: 3, Signers: []string{"alice", "bob"}, TrustRoot: "root-prod"},
		{Threshold: 1, Signers: []string{"alice", "alice"}, TrustRoot: "root-prod"},
		{Threshold: 1, Signers: []string{"alice"}, TrustRoot: ""},
	}
	for i, p := range badPolicies {
		if err := v.SetPolicy(p); !errors.Is(err, impl.ErrBadPolicy) {
			t.Fatalf("bad policy %d must fail with ErrBadPolicy, got %v", i, err)
		}
	}
	if err := v.SetPolicy(releasePolicy()); err != nil {
		t.Fatalf("valid policy must be accepted, got %v", err)
	}
}

func TestVerifyRejectsMalformedDigest(t *testing.T) {
	v := setupVerifier(t, threeKeys(), releasePolicy())
	bad := []impl.Digest{
		{Algorithm: "sha1", Hex: strings.Repeat("a", 40)},
		{Algorithm: "sha256", Hex: strings.Repeat("a", 63)},
		{Algorithm: "sha256", Hex: strings.Repeat("A", 64)},
		{Algorithm: "sha256", Hex: strings.Repeat("z", 64)},
	}
	for i, d := range bad {
		if _, err := v.Verify(d, nil); !errors.Is(err, impl.ErrBadDigest) {
			t.Fatalf("malformed digest %d must fail with ErrBadDigest, got %v", i, err)
		}
	}
}

func TestVerifyAcceptsThresholdDistinctSigners(t *testing.T) {
	v := setupVerifier(t, threeKeys(), releasePolicy())
	verdict, err := v.Verify(validDigest(), []impl.Signature{
		sigFor("k-alice", base().Add(10*time.Minute)),
		sigFor("k-bob", base().Add(10*time.Minute)),
	})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !verdict.OK {
		t.Fatalf("two distinct signers must satisfy a 2-of-3 policy, got %+v", verdict)
	}
	if verdict.Accepted != 2 || verdict.Required != 2 {
		t.Fatalf("accepted=%d required=%d, want 2 and 2", verdict.Accepted, verdict.Required)
	}
	if !hasReason(verdict.Reasons, impl.ReasonOK) {
		t.Fatalf("reasons = %v, want the ok code", verdict.Reasons)
	}
	if len(verdict.Signers) != 2 || verdict.Signers[0] != "alice" || verdict.Signers[1] != "bob" {
		t.Fatalf("signers = %v, want [alice bob] sorted", verdict.Signers)
	}
	if got := v.Rounds(); got != 1 {
		t.Fatalf("round counter = %d, want 1", got)
	}
}

func TestVerifyFailsBelowThreshold(t *testing.T) {
	v := setupVerifier(t, threeKeys(), releasePolicy())
	verdict, err := v.Verify(validDigest(), []impl.Signature{sigFor("k-alice", base().Add(10*time.Minute))})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if verdict.OK {
		t.Fatalf("one signer must not satisfy a threshold of two, got %+v", verdict)
	}
	if verdict.Accepted != 1 || verdict.Required != 2 {
		t.Fatalf("accepted=%d required=%d, want 1 and 2", verdict.Accepted, verdict.Required)
	}
	if !hasReason(verdict.Reasons, impl.ReasonInsufficient) {
		t.Fatalf("reasons = %v, want the insufficient signer code", verdict.Reasons)
	}
	if len(verdict.Signers) != 1 || verdict.Signers[0] != "alice" {
		t.Fatalf("signers = %v, want [alice]", verdict.Signers)
	}
}

func TestVerifyCountsSignerOncePerMultipleKeys(t *testing.T) {
	keys := []impl.Key{signingKey("k-alice-1", "alice"), signingKey("k-alice-2", "alice"), signingKey("k-bob", "bob")}
	v := setupVerifier(t, keys, releasePolicy())
	verdict, err := v.Verify(validDigest(), []impl.Signature{
		sigFor("k-alice-1", base().Add(10*time.Minute)),
		sigFor("k-alice-2", base().Add(10*time.Minute)),
	})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if verdict.OK {
		t.Fatalf("two keys of one signer must not satisfy a two signer threshold, got %+v", verdict)
	}
	if verdict.Accepted != 1 {
		t.Fatalf("accepted = %d, want one distinct signer", verdict.Accepted)
	}
	if !hasReason(verdict.Reasons, impl.ReasonDuplicateSigner) {
		t.Fatalf("reasons = %v, want the duplicate signer code", verdict.Reasons)
	}
}

func TestVerifyRequiresTrustRootChain(t *testing.T) {
	keys := threeKeys()
	keys[1].Chain = []string{"leaf-k-bob", "rogue-root"}
	v := setupVerifier(t, keys, releasePolicy())
	verdict, err := v.Verify(validDigest(), []impl.Signature{
		sigFor("k-alice", base().Add(10*time.Minute)),
		sigFor("k-bob", base().Add(10*time.Minute)),
	})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if verdict.OK {
		t.Fatalf("a chain that misses the trust root must not count, got %+v", verdict)
	}
	if verdict.Accepted != 1 {
		t.Fatalf("accepted = %d, want only alice", verdict.Accepted)
	}
	if !hasReason(verdict.Reasons, impl.ReasonUntrustedChain) {
		t.Fatalf("reasons = %v, want the untrusted chain code", verdict.Reasons)
	}
}

func TestVerifyRequiresAuthorizedSigner(t *testing.T) {
	keys := append(threeKeys(), signingKey("k-mallory", "mallory"))
	v := setupVerifier(t, keys, releasePolicy())
	verdict, err := v.Verify(validDigest(), []impl.Signature{
		sigFor("k-alice", base().Add(10*time.Minute)),
		sigFor("k-mallory", base().Add(10*time.Minute)),
	})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if verdict.Accepted != 1 {
		t.Fatalf("accepted = %d, want only the authorized alice signature", verdict.Accepted)
	}
	if !hasReason(verdict.Reasons, impl.ReasonUnauthorizedUser) {
		t.Fatalf("reasons = %v, want the unauthorized signer code", verdict.Reasons)
	}
}

func TestVerifyIgnoresUnknownKey(t *testing.T) {
	v := setupVerifier(t, threeKeys(), releasePolicy())
	verdict, err := v.Verify(validDigest(), []impl.Signature{
		sigFor("k-alice", base().Add(10*time.Minute)),
		sigFor("k-404", base().Add(10*time.Minute)),
	})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if verdict.Accepted != 1 {
		t.Fatalf("accepted = %d, want only the registered key", verdict.Accepted)
	}
	if !hasReason(verdict.Reasons, impl.ReasonUnknownKey) {
		t.Fatalf("reasons = %v, want the unknown key code", verdict.Reasons)
	}
}

func TestVerifyChecksKeyValidityWindow(t *testing.T) {
	keys := threeKeys()
	keys[1].NotAfter = base()
	keys[2].NotBefore = base().Add(48 * time.Hour)
	v := setupVerifier(t, keys, releasePolicy())
	verdict, err := v.Verify(validDigest(), []impl.Signature{
		sigFor("k-alice", base().Add(10*time.Minute)),
		sigFor("k-bob", base().Add(10*time.Minute)),
		sigFor("k-carol", base().Add(10*time.Minute)),
	})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if verdict.Accepted != 1 {
		t.Fatalf("accepted = %d, want only the key inside its window", verdict.Accepted)
	}
	if !hasReason(verdict.Reasons, impl.ReasonKeyOutOfWindow) {
		t.Fatalf("reasons = %v, want the key window code", verdict.Reasons)
	}
}

func TestRevokedKeyStillCountsBeforeRevocation(t *testing.T) {
	v := setupVerifier(t, threeKeys(), releasePolicy())
	revokeAt := base().Add(time.Hour)
	if err := v.RevokeKey("k-bob", revokeAt); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	verdict, err := v.Verify(validDigest(), []impl.Signature{
		sigFor("k-alice", base().Add(10*time.Minute)),
		sigFor("k-bob", base().Add(10*time.Minute)),
	})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !verdict.OK {
		t.Fatalf("signatures made before the revocation instant must still count, got %+v", verdict)
	}
	if verdict.Accepted != 2 {
		t.Fatalf("accepted = %d, want 2", verdict.Accepted)
	}
	at, ok := v.Revoked("k-bob")
	if !ok || !at.Equal(revokeAt) {
		t.Fatalf("revoked(k-bob) = %v,%v want %v,true", at, ok, revokeAt)
	}
	records := v.Revocations()
	if len(records) != 1 || records[0].KeyID != "k-bob" {
		t.Fatalf("revocations = %+v, want one record for k-bob", records)
	}
}

func TestRevokeKeyValidationAndIdempotency(t *testing.T) {
	v := setupVerifier(t, threeKeys(), releasePolicy())
	if err := v.RevokeKey("k-404", base()); !errors.Is(err, impl.ErrNotFound) {
		t.Fatalf("revoking an unknown key must fail with ErrNotFound, got %v", err)
	}
	first := base()
	if err := v.RevokeKey("k-alice", first); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if err := v.RevokeKey("k-alice", base().Add(2*time.Hour)); err != nil {
		t.Fatalf("repeated revocation must be idempotent, got %v", err)
	}
	at, ok := v.Revoked("k-alice")
	if !ok || !at.Equal(first) {
		t.Fatalf("revocation instant = %v,%v want the earliest %v,true", at, ok, first)
	}
	if err := v.RevokeKey("k-alice", time.Time{}); !errors.Is(err, impl.ErrBadRevocation) {
		t.Fatalf("zero revocation instant must fail with ErrBadRevocation, got %v", err)
	}
	if got := len(v.Revocations()); got != 1 {
		t.Fatalf("revocation records = %d, want 1", got)
	}
}

func TestVerifyRequiresLogInclusionWhenPolicyAsks(t *testing.T) {
	policy := releasePolicy()
	policy.RequireLog = true
	v := setupVerifier(t, threeKeys(), policy)
	sigs := []impl.Signature{
		{KeyID: "k-alice", At: base().Add(10 * time.Minute), LogIndex: 7, LogTime: base().Add(10 * time.Minute), Included: true},
		{KeyID: "k-bob", At: base().Add(10 * time.Minute), LogIndex: 8, LogTime: base().Add(10 * time.Minute), Included: true},
	}
	verdict, err := v.Verify(validDigest(), sigs)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !verdict.OK || verdict.Accepted != 2 {
		t.Fatalf("signatures with a log inclusion proof must satisfy the policy, got %+v", verdict)
	}
	if len(v.Keys()) != 3 {
		t.Fatalf("key table lost entries during verification")
	}
}

func TestSnapshotRestoreKeepsRevocationsAndPolicy(t *testing.T) {
	v := setupVerifier(t, threeKeys(), releasePolicy())
	revokeAt := base().Add(time.Hour)
	if err := v.RevokeKey("k-bob", revokeAt); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	sigs := []impl.Signature{
		sigFor("k-alice", base().Add(10*time.Minute)),
		sigFor("k-bob", base().Add(10*time.Minute)),
	}
	if verdict, err := v.Verify(validDigest(), sigs); err != nil || !verdict.OK {
		t.Fatalf("baseline verify: %+v %v", verdict, err)
	}
	data, err := v.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewSigVerifier()
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	policy := restored.Policy()
	if policy.Threshold != 2 || policy.TrustRoot != "root-prod" || policy.RequireLog {
		t.Fatalf("restored policy = %+v, want the installed 2-of-3 policy", policy)
	}
	if len(policy.Signers) != 3 || policy.Signers[0] != "alice" {
		t.Fatalf("restored policy signers = %v, want [alice bob carol]", policy.Signers)
	}
	if len(restored.Keys()) != 3 {
		t.Fatalf("restored key table holds %d keys, want 3", len(restored.Keys()))
	}
	if at, ok := restored.Revoked("k-bob"); !ok || !at.Equal(revokeAt) {
		t.Fatalf("restored revocation = %v,%v want %v,true", at, ok, revokeAt)
	}
	if got := restored.Rounds(); got != 1 {
		t.Fatalf("rounds after restore = %d, want 1", got)
	}
	verdict, err := restored.Verify(validDigest(), sigs)
	if err != nil || !verdict.OK {
		t.Fatalf("verify after restore = %+v, %v", verdict, err)
	}
	if got := restored.Rounds(); got != 2 {
		t.Fatalf("rounds after one verification = %d, want 2", got)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	v := setupVerifier(t, threeKeys(), releasePolicy())
	if err := v.RevokeKey("k-bob", base().Add(time.Hour)); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if err := v.Restore([]byte("}{broken")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if err := v.Restore(nil); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("empty snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := v.Policy().Threshold; got != 2 {
		t.Fatalf("failed restore changed the policy threshold to %d", got)
	}
	if _, ok := v.Revoked("k-bob"); !ok {
		t.Fatalf("failed restore dropped the revocation record")
	}
	verdict, err := v.Verify(validDigest(), []impl.Signature{
		sigFor("k-alice", base().Add(10*time.Minute)),
		sigFor("k-bob", base().Add(10*time.Minute)),
	})
	if err != nil || !verdict.OK {
		t.Fatalf("live state must keep working after a rejected restore, got %+v %v", verdict, err)
	}
}

func TestConcurrentVerifiesSharePolicy(t *testing.T) {
	v := setupVerifier(t, threeKeys(), releasePolicy())
	sigs := []impl.Signature{
		sigFor("k-alice", base().Add(10*time.Minute)),
		sigFor("k-bob", base().Add(10*time.Minute)),
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	failures := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			verdict, err := v.Verify(validDigest(), sigs)
			mu.Lock()
			defer mu.Unlock()
			if err != nil || !verdict.OK || verdict.Accepted != 2 {
				failures++
			}
		}()
	}
	wg.Wait()
	if failures != 0 {
		t.Fatalf("%d concurrent verifications did not see the installed policy", failures)
	}
	if got := v.Rounds(); got != 8 {
		t.Fatalf("round counter = %d, want 8", got)
	}
}
