// Package task decides whether a set of artifact signatures satisfies a
// release policy: distinct signer threshold, key windows, revocation instants
// and transparency log inclusion.
package task

import (
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"
)

// Reason codes attached to a verification verdict.
const (
	ReasonOK               = "ok"
	ReasonInsufficient     = "insufficient_signers"
	ReasonUnknownKey       = "unknown_key"
	ReasonUntrustedChain   = "untrusted_chain"
	ReasonUnauthorizedUser = "unauthorized_signer"
	ReasonKeyOutOfWindow   = "key_out_of_window"
	ReasonRevoked          = "key_revoked"
	ReasonDuplicateSigner  = "duplicate_signer"
	ReasonMissingLogProof  = "missing_log_proof"
	ReasonDeclaredAfterLog = "declared_after_log"
)

// Errors returned by the verifier.
var (
	ErrBadDigest     = errors.New("digest must be sha256 with 64 lowercase hex characters")
	ErrNoPolicy      = errors.New("release policy is not set")
	ErrBadPolicy     = errors.New("release policy is invalid")
	ErrBadKey        = errors.New("key record is invalid")
	ErrDuplicateKey  = errors.New("key id already registered")
	ErrNotFound      = errors.New("key not found")
	ErrBadRevocation = errors.New("revocation instant is required")
	ErrBadSnapshot   = errors.New("snapshot payload is invalid")
)

// Digest identifies the signed artifact content.
type Digest struct {
	Algorithm string
	Hex       string
}

// Key is one registered signing key.
type Key struct {
	KeyID     string
	Signer    string
	NotBefore time.Time
	NotAfter  time.Time
	Chain     []string
}

// Signature is one parsed signature over a digest.
type Signature struct {
	KeyID    string
	Signer   string
	At       time.Time
	LogIndex int64
	LogTime  time.Time
	Included bool
	Value    string
}

// Policy is the release gate.
type Policy struct {
	Threshold  int
	Signers    []string
	TrustRoot  string
	RequireLog bool
}

// Verdict is the explainable outcome of one verification.
type Verdict struct {
	OK       bool
	Accepted int
	Required int
	Reasons  []string
	Signers  []string
}

// Revocation is one recorded key revocation instant.
type Revocation struct {
	KeyID string
	At    time.Time
	Seq   int64
}

// SigVerifier owns the key table, the release policy and revocation records.
type SigVerifier struct {
	mu          sync.Mutex
	keys        map[string]Key
	policy      Policy
	hasPolicy   bool
	revocations []Revocation
	revoked     map[string]time.Time
	nextSeq     int64
	rounds      int
}

const snapshotVersion = 1

type snapshot struct {
	Version     int          `json:"version"`
	Keys        []Key        `json:"keys"`
	Policy      Policy       `json:"policy"`
	HasPolicy   bool         `json:"has_policy"`
	Revocations []Revocation `json:"revocations"`
	Rounds      int          `json:"rounds"`
}

// NewSigVerifier builds an empty verifier.
func NewSigVerifier() *SigVerifier {
	return &SigVerifier{
		keys:    make(map[string]Key),
		revoked: make(map[string]time.Time),
		nextSeq: 1,
	}
}

// RegisterKey adds one signing key to the trust table.
func (v *SigVerifier) RegisterKey(k Key) error {
	if err := validateKey(k); err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.initLocked()
	if _, exists := v.keys[k.KeyID]; exists {
		return ErrDuplicateKey
	}
	v.keys[k.KeyID] = cloneKey(k)
	return nil
}

// SetPolicy installs the release policy.
func (v *SigVerifier) SetPolicy(p Policy) error {
	if err := validatePolicy(p); err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.initLocked()
	v.policy = clonePolicy(p)
	v.hasPolicy = true
	return nil
}

// Policy reads the installed release policy.
func (v *SigVerifier) Policy() Policy {
	v.mu.Lock()
	defer v.mu.Unlock()
	if !v.hasPolicy {
		return Policy{}
	}
	return clonePolicy(v.policy)
}

// Verify grades the signatures against the policy.
func (v *SigVerifier) Verify(d Digest, sigs []Signature) (Verdict, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.initLocked()

	if !v.hasPolicy {
		return Verdict{}, ErrNoPolicy
	}
	if !validDigest(d) {
		return Verdict{}, ErrBadDigest
	}

	required := v.policy.Threshold
	authorized := make(map[string]struct{}, len(v.policy.Signers))
	for _, signer := range v.policy.Signers {
		authorized[signer] = struct{}{}
	}

	reasons := make([]string, 0, len(sigs)+1)
	accepted := make(map[string]struct{})

	for _, sig := range sigs {
		key, ok := v.keys[sig.KeyID]
		if !ok {
			reasons = append(reasons, ReasonUnknownKey)
			continue
		}

		signer := key.Signer
		if _, ok := authorized[signer]; !ok {
			reasons = append(reasons, ReasonUnauthorizedUser)
			continue
		}
		if !chainReachesRoot(key.Chain, v.policy.TrustRoot) {
			reasons = append(reasons, ReasonUntrustedChain)
			continue
		}

		signTime := sig.At
		if !sig.LogTime.IsZero() {
			if sig.At.After(sig.LogTime) {
				reasons = append(reasons, ReasonDeclaredAfterLog)
				continue
			}
			if sig.LogTime.Before(sig.At) {
				signTime = sig.LogTime
			}
		}

		claimsLogEntry := sig.LogIndex != 0 || !sig.LogTime.IsZero() || sig.Included
		if sig.LogIndex <= 0 && (v.policy.RequireLog || claimsLogEntry) {
			reasons = append(reasons, ReasonMissingLogProof)
			continue
		}

		if signTime.Before(key.NotBefore) || signTime.After(key.NotAfter) {
			reasons = append(reasons, ReasonKeyOutOfWindow)
			continue
		}
		if revokedAt, ok := v.revoked[key.KeyID]; ok && !signTime.Before(revokedAt) {
			reasons = append(reasons, ReasonRevoked)
			continue
		}
		if v.policy.RequireLog && !sig.Included {
			reasons = append(reasons, ReasonMissingLogProof)
			continue
		}

		if _, duplicate := accepted[signer]; duplicate {
			reasons = append(reasons, ReasonDuplicateSigner)
			continue
		}
		accepted[signer] = struct{}{}
	}

	signers := make([]string, 0, len(accepted))
	for signer := range accepted {
		signers = append(signers, signer)
	}
	sort.Strings(signers)

	ok := len(accepted) >= required
	if ok {
		reasons = append(reasons, ReasonOK)
	} else {
		reasons = append(reasons, ReasonInsufficient)
	}
	sort.Strings(reasons)

	v.rounds++
	return Verdict{
		OK:       ok,
		Accepted: len(accepted),
		Required: required,
		Reasons:  reasons,
		Signers:  signers,
	}, nil
}

// RevokeKey records the instant from which a key must not count.
func (v *SigVerifier) RevokeKey(keyID string, at time.Time) error {
	if at.IsZero() {
		return ErrBadRevocation
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	v.initLocked()

	if _, ok := v.keys[keyID]; !ok {
		return ErrNotFound
	}
	if earliest, ok := v.revoked[keyID]; ok {
		if at.Before(earliest) {
			v.revoked[keyID] = at
			for i := range v.revocations {
				if v.revocations[i].KeyID == keyID {
					v.revocations[i].At = at
					break
				}
			}
		}
		return nil
	}

	v.revoked[keyID] = at
	v.revocations = append(v.revocations, Revocation{KeyID: keyID, At: at, Seq: v.nextSeq})
	v.nextSeq++
	return nil
}

// Revocations lists the recorded revocations in recording order.
func (v *SigVerifier) Revocations() []Revocation {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := make([]Revocation, len(v.revocations))
	copy(out, v.revocations)
	return out
}

// Revoked reads the revocation instant of one key.
func (v *SigVerifier) Revoked(keyID string) (time.Time, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	at, ok := v.revoked[keyID]
	return at, ok
}

// Keys lists the registered keys by key id.
func (v *SigVerifier) Keys() []Key {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := make([]Key, 0, len(v.keys))
	for _, k := range v.keys {
		out = append(out, cloneKey(k))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].KeyID < out[j].KeyID })
	return out
}

// Rounds reports how many verifications the verifier has completed.
func (v *SigVerifier) Rounds() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.rounds
}

// Snapshot serialises keys, the policy, revocations and the round counter.
func (v *SigVerifier) Snapshot() ([]byte, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	state := snapshot{
		Version:     snapshotVersion,
		Keys:        make([]Key, 0, len(v.keys)),
		Revocations: make([]Revocation, len(v.revocations)),
		Rounds:      v.rounds,
		HasPolicy:   v.hasPolicy,
	}
	for _, k := range v.keys {
		state.Keys = append(state.Keys, cloneKey(k))
	}
	sort.Slice(state.Keys, func(i, j int) bool { return state.Keys[i].KeyID < state.Keys[j].KeyID })
	if v.hasPolicy {
		state.Policy = clonePolicy(v.policy)
	}
	copy(state.Revocations, v.revocations)

	data, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Restore replaces the whole verifier state with a snapshot, all or nothing.
func (v *SigVerifier) Restore(data []byte) error {
	if len(data) == 0 {
		return ErrBadSnapshot
	}

	var state snapshot
	if err := json.Unmarshal(data, &state); err != nil {
		return ErrBadSnapshot
	}
	if state.Version != snapshotVersion || state.Rounds < 0 {
		return ErrBadSnapshot
	}

	keys := make(map[string]Key, len(state.Keys))
	for _, k := range state.Keys {
		if err := validateKey(k); err != nil {
			return ErrBadSnapshot
		}
		if _, exists := keys[k.KeyID]; exists {
			return ErrBadSnapshot
		}
		keys[k.KeyID] = cloneKey(k)
	}

	if state.HasPolicy {
		if err := validatePolicy(state.Policy); err != nil {
			return ErrBadSnapshot
		}
	} else if state.Policy.Threshold != 0 || len(state.Policy.Signers) != 0 ||
		state.Policy.TrustRoot != "" || state.Policy.RequireLog {
		return ErrBadSnapshot
	}

	revoked := make(map[string]time.Time, len(state.Revocations))
	revocations := make([]Revocation, 0, len(state.Revocations))
	seenSeq := make(map[int64]struct{}, len(state.Revocations))
	var maxSeq int64
	for _, r := range state.Revocations {
		if r.KeyID == "" || r.At.IsZero() || r.Seq <= 0 {
			return ErrBadSnapshot
		}
		if _, exists := revoked[r.KeyID]; exists {
			return ErrBadSnapshot
		}
		if _, exists := seenSeq[r.Seq]; exists {
			return ErrBadSnapshot
		}
		if _, exists := keys[r.KeyID]; !exists {
			return ErrBadSnapshot
		}
		revoked[r.KeyID] = r.At
		revocations = append(revocations, r)
		seenSeq[r.Seq] = struct{}{}
		if r.Seq > maxSeq {
			maxSeq = r.Seq
		}
	}
	sort.Slice(revocations, func(i, j int) bool { return revocations[i].Seq < revocations[j].Seq })

	nextSeq := maxSeq + 1
	if nextSeq < 1 {
		nextSeq = 1
	}
	var policy Policy
	if state.HasPolicy {
		policy = clonePolicy(state.Policy)
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	v.keys = keys
	v.policy = policy
	v.hasPolicy = state.HasPolicy
	v.revocations = revocations
	v.revoked = revoked
	v.nextSeq = nextSeq
	v.rounds = state.Rounds
	return nil
}

func (v *SigVerifier) initLocked() {
	if v.keys == nil {
		v.keys = make(map[string]Key)
	}
	if v.revoked == nil {
		v.revoked = make(map[string]time.Time)
	}
	if v.nextSeq < 1 {
		v.nextSeq = 1
	}
}

func validDigest(d Digest) bool {
	if d.Algorithm != "sha256" || len(d.Hex) != 64 {
		return false
	}
	for i := 0; i < len(d.Hex); i++ {
		c := d.Hex[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func validatePolicy(p Policy) error {
	if p.Threshold < 1 || p.Threshold > len(p.Signers) || p.TrustRoot == "" {
		return ErrBadPolicy
	}
	seen := make(map[string]struct{}, len(p.Signers))
	for _, signer := range p.Signers {
		if signer == "" {
			return ErrBadPolicy
		}
		if _, exists := seen[signer]; exists {
			return ErrBadPolicy
		}
		seen[signer] = struct{}{}
	}
	return nil
}

func validateKey(k Key) error {
	if k.KeyID == "" || k.Signer == "" {
		return ErrBadKey
	}
	return nil
}

func chainReachesRoot(chain []string, root string) bool {
	return len(chain) > 0 && chain[len(chain)-1] == root
}

func cloneKey(k Key) Key {
	k.Chain = append([]string(nil), k.Chain...)
	return k
}

func clonePolicy(p Policy) Policy {
	p.Signers = append([]string(nil), p.Signers...)
	return p
}
