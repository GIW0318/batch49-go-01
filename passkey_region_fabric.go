// Package task keeps WebAuthn credential records consistent across a home
// region and its satellite replicas, and propagates revocations to them.
package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Credential lifecycle states.
const (
	CredentialActive      = "active"
	CredentialRevoked     = "revoked"
	CredentialQuarantined = "quarantined"
)

// Replica modes reported by Status.
const (
	ReplicaActive   = "active"
	ReplicaLagging  = "lagging"
	ReplicaReadOnly = "read_only"
)

// Errors returned by the fabric.
var (
	ErrBlankField           = errors.New("credential id, user id and rp id are required")
	ErrUnknownRegion        = errors.New("region is not configured")
	ErrNotHomeRegion        = errors.New("write is only accepted at the home region")
	ErrRPIDMismatch         = errors.New("rp id does not belong to the home region")
	ErrUserMismatch         = errors.New("assertion subject does not match the credential owner")
	ErrDuplicateCredential  = errors.New("credential id already registered")
	ErrCredentialNotFound   = errors.New("credential not found")
	ErrCredentialRevoked    = errors.New("credential has been revoked")
	ErrCredentialQuarantined = errors.New("credential is quarantined")
	ErrCounterReplay        = errors.New("signature counter did not advance")
	ErrRegionLagging        = errors.New("replica has not caught up with the credential epoch")
	ErrRegionReadOnly       = errors.New("replica is read only until it catches up")
	ErrCredentialLimit      = errors.New("replica credential capacity exceeded")
	ErrBadSnapshot          = errors.New("snapshot payload is invalid")
)

// FabricConfig describes the home region, its replicas and the safety caps.
type FabricConfig struct {
	HomeRegion              string
	Replicas                []string
	HomeRPID                string
	MaxCredentialsPerRegion int
	ReplayTolerance         int
	AckWindow               int64
}

// Credential is one WebAuthn public key credential record.
type Credential struct {
	ID             string `json:"id"`
	UserID         string `json:"user_id"`
	RPID           string `json:"rp_id"`
	AAGUID         string `json:"aaguid"`
	SignCount      uint32 `json:"sign_count"`
	BackupEligible bool   `json:"backup_eligible"`
	State          string `json:"state"`
	Epoch          int64  `json:"epoch"`
	Replays        int    `json:"replays"`
}

// ReplicaSet is the outcome of a home region registration.
type ReplicaSet struct {
	CredentialID string `json:"credential_id"`
	Epoch        int64  `json:"epoch"`
	Regions      []string `json:"regions"`
}

// Assertion is the signed assertion presented during a login.
type Assertion struct {
	CredentialID string `json:"credential_id"`
	UserID       string `json:"user_id"`
	RPID         string `json:"rp_id"`
	Origin       string `json:"origin"`
	SignCount    uint32 `json:"sign_count"`
	UserVerified bool   `json:"user_verified"`
}

// LoginGrant is returned when an assertion is accepted.
type LoginGrant struct {
	CredentialID string `json:"credential_id"`
	UserID       string `json:"user_id"`
	Region       string `json:"region"`
	Epoch        int64  `json:"epoch"`
	Seq          int64  `json:"seq"`
}

// RevocationStamp records one credential revocation in the home ledger.
type RevocationStamp struct {
	CredentialID string `json:"credential_id"`
	Reason       string `json:"reason"`
	Epoch        int64  `json:"epoch"`
}

// RegionStatus is the replication view of one region.
type RegionStatus struct {
	Region      string `json:"region"`
	Epoch       int64  `json:"epoch"`
	Records     int    `json:"records"`
	Revocations int    `json:"revocations"`
	Mode        string `json:"mode"`
}

// PasskeyRegionFabric owns the credential table, the revocation ledger and
// the per region replication watermarks.
type PasskeyRegionFabric struct {
	cfg   FabricConfig
	epoch int64
	seq   int64
}

// NewPasskeyRegionFabric builds an empty fabric for the configured regions.
func NewPasskeyRegionFabric(cfg FabricConfig) *PasskeyRegionFabric {
	return &PasskeyRegionFabric{cfg: cfg}
}

// Register stores a new credential at the home region.
func (f *PasskeyRegionFabric) Register(homeRegion string, cred Credential) (ReplicaSet, error) {
	return ReplicaSet{}, errNotImplemented
}

// Replicate copies pending credential records from the home region to one replica.
func (f *PasskeyRegionFabric) Replicate(fromRegion, toRegion string, limit int) (int, error) {
	return 0, errNotImplemented
}

// Authenticate checks one assertion in the given region.
func (f *PasskeyRegionFabric) Authenticate(region string, a Assertion) (LoginGrant, error) {
	return LoginGrant{}, errNotImplemented
}

// Revoke marks a credential revoked in the home revocation ledger.
func (f *PasskeyRegionFabric) Revoke(credentialID, reason string) (RevocationStamp, error) {
	return RevocationStamp{}, errNotImplemented
}

// Propagate delivers revocation tombstones up to the given epoch to a region.
func (f *PasskeyRegionFabric) Propagate(region string, upToEpoch int64) (int, error) {
	return 0, errNotImplemented
}

// Status reports the replication watermark and mode of one region.
func (f *PasskeyRegionFabric) Status(region string) RegionStatus {
	return RegionStatus{}
}

// Credential returns the credential record known at the home region.
func (f *PasskeyRegionFabric) Credential(credentialID string) (Credential, bool) {
	return Credential{}, false
}

// Regions lists the configured home region followed by its replicas.
func (f *PasskeyRegionFabric) Regions() []string {
	return nil
}

// Snapshot serialises the whole fabric state.
func (f *PasskeyRegionFabric) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole fabric state from a snapshot.
func (f *PasskeyRegionFabric) Restore(data []byte) error {
	return errNotImplemented
}
