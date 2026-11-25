// Package task decides whether a set of artifact signatures satisfies a
// release policy: distinct signer threshold, key windows, revocation instants
// and transparency log inclusion.
package task

import (
	"errors"
	"time"
)

// errNotImplemented is returned by behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Reason codes attached to a verification verdict.
const (
	ReasonOK                = "ok"
	ReasonInsufficient      = "insufficient_signers"
	ReasonUnknownKey        = "unknown_key"
	ReasonUntrustedChain    = "untrusted_chain"
	ReasonUnauthorizedUser  = "unauthorized_signer"
	ReasonKeyOutOfWindow    = "key_out_of_window"
	ReasonRevoked           = "key_revoked"
	ReasonDuplicateSigner   = "duplicate_signer"
	ReasonMissingLogProof   = "missing_log_proof"
	ReasonDeclaredAfterLog  = "declared_after_log"
)

// Errors returned by the verifier.
var (
	ErrBadDigest   = errors.New("digest must be sha256 with 64 lowercase hex characters")
	ErrNoPolicy    = errors.New("release policy is not set")
	ErrBadPolicy   = errors.New("release policy is invalid")
	ErrBadKey      = errors.New("key record is invalid")
	ErrDuplicateKey = errors.New("key id already registered")
	ErrNotFound    = errors.New("key not found")
	ErrBadRevocation = errors.New("revocation instant is required")
	ErrBadSnapshot = errors.New("snapshot payload is invalid")
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
type SigVerifier struct{}

// NewSigVerifier builds an empty verifier.
func NewSigVerifier() *SigVerifier {
	return &SigVerifier{}
}

// RegisterKey adds one signing key to the trust table.
func (v *SigVerifier) RegisterKey(k Key) error {
	return errNotImplemented
}

// SetPolicy installs the release policy.
func (v *SigVerifier) SetPolicy(p Policy) error {
	return errNotImplemented
}

// Policy reads the installed release policy.
func (v *SigVerifier) Policy() Policy {
	return Policy{}
}

// Verify grades the signatures against the policy.
func (v *SigVerifier) Verify(d Digest, sigs []Signature) (Verdict, error) {
	return Verdict{}, errNotImplemented
}

// RevokeKey records the instant from which a key must not count.
func (v *SigVerifier) RevokeKey(keyID string, at time.Time) error {
	return errNotImplemented
}

// Revocations lists the recorded revocations in recording order.
func (v *SigVerifier) Revocations() []Revocation {
	return nil
}

// Revoked reads the revocation instant of one key.
func (v *SigVerifier) Revoked(keyID string) (time.Time, bool) {
	return time.Time{}, false
}

// Keys lists the registered keys by key id.
func (v *SigVerifier) Keys() []Key {
	return nil
}

// Rounds reports how many verifications the verifier has completed.
func (v *SigVerifier) Rounds() int {
	return 0
}

// Snapshot serialises keys, the policy, revocations and the round counter.
func (v *SigVerifier) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole verifier state with a snapshot, all or nothing.
func (v *SigVerifier) Restore(data []byte) error {
	return errNotImplemented
}
