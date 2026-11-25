// Package task keeps WebAuthn credential records consistent across a home
// region and its satellite replicas, and propagates revocations to them.
package task

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
)

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
	mu      sync.Mutex
	cfg     FabricConfig
	epoch   int64
	seq     int64
	creds   map[string]Credential
	tombs   map[string]RevocationStamp
	replica map[string]*replicaState
}

// replicaState is the per replica view: copied records, delivered
// tombstones and the replication watermark.
type replicaState struct {
	watermark int64
	records   map[string]Credential
	tombs     map[string]RevocationStamp
}

// NewPasskeyRegionFabric builds an empty fabric for the configured regions.
func NewPasskeyRegionFabric(cfg FabricConfig) *PasskeyRegionFabric {
	f := &PasskeyRegionFabric{
		cfg:     cfg,
		creds:   make(map[string]Credential),
		tombs:   make(map[string]RevocationStamp),
		replica: make(map[string]*replicaState),
	}
	for _, region := range cfg.Replicas {
		f.replica[region] = newReplicaState()
	}
	return f
}

func newReplicaState() *replicaState {
	return &replicaState{
		records: make(map[string]Credential),
		tombs:   make(map[string]RevocationStamp),
	}
}

func (f *PasskeyRegionFabric) isReplica(region string) bool {
	_, ok := f.replica[region]
	return ok
}

func (f *PasskeyRegionFabric) isRegion(region string) bool {
	return region == f.cfg.HomeRegion || f.isReplica(region)
}

// Register stores a new credential at the home region.
func (f *PasskeyRegionFabric) Register(homeRegion string, cred Credential) (ReplicaSet, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if homeRegion != f.cfg.HomeRegion {
		return ReplicaSet{}, ErrNotHomeRegion
	}
	if strings.TrimSpace(cred.ID) == "" || strings.TrimSpace(cred.UserID) == "" || strings.TrimSpace(cred.RPID) == "" {
		return ReplicaSet{}, ErrBlankField
	}
	if cred.RPID != f.cfg.HomeRPID {
		return ReplicaSet{}, ErrRPIDMismatch
	}
	if _, exists := f.creds[cred.ID]; exists {
		return ReplicaSet{}, ErrDuplicateCredential
	}
	f.epoch++
	cred.State = CredentialActive
	cred.Epoch = f.epoch
	cred.Replays = 0
	f.creds[cred.ID] = cred
	return ReplicaSet{
		CredentialID: cred.ID,
		Epoch:        cred.Epoch,
		Regions:      []string{f.cfg.HomeRegion},
	}, nil
}

// Replicate copies pending credential records from the home region to one replica.
func (f *PasskeyRegionFabric) Replicate(fromRegion, toRegion string, limit int) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if fromRegion != f.cfg.HomeRegion {
		return 0, ErrNotHomeRegion
	}
	target, ok := f.replica[toRegion]
	if !ok {
		return 0, ErrUnknownRegion
	}
	pending := make([]Credential, 0, len(f.creds))
	for _, cred := range f.creds {
		if cred.Epoch > target.watermark {
			if _, present := target.records[cred.ID]; !present {
				pending = append(pending, cred)
			}
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].Epoch < pending[j].Epoch })
	if limit > 0 && len(pending) > limit {
		pending = pending[:limit]
	}
	if len(pending) == 0 {
		return 0, nil
	}
	if f.cfg.MaxCredentialsPerRegion > 0 && len(target.records)+len(pending) > f.cfg.MaxCredentialsPerRegion {
		return 0, ErrCredentialLimit
	}
	for _, cred := range pending {
		target.records[cred.ID] = cred
	}
	target.watermark = pending[len(pending)-1].Epoch
	return len(pending), nil
}

// Authenticate checks one assertion in the given region.
func (f *PasskeyRegionFabric) Authenticate(region string, a Assertion) (LoginGrant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if !f.isRegion(region) {
		return LoginGrant{}, ErrUnknownRegion
	}
	cred, ok := f.creds[a.CredentialID]
	if !ok {
		return LoginGrant{}, ErrCredentialNotFound
	}
	if a.UserID != cred.UserID {
		return LoginGrant{}, ErrUserMismatch
	}
	if a.RPID != cred.RPID {
		return LoginGrant{}, ErrRPIDMismatch
	}
	if _, revoked := f.tombs[a.CredentialID]; revoked {
		return LoginGrant{}, ErrCredentialRevoked
	}
	if cred.State == CredentialQuarantined {
		return LoginGrant{}, ErrCredentialQuarantined
	}
	if replica, isReplica := f.replica[region]; isReplica {
		if f.epoch-replica.watermark > f.cfg.AckWindow {
			return LoginGrant{}, ErrRegionReadOnly
		}
		if replica.watermark < cred.Epoch {
			return LoginGrant{}, ErrRegionLagging
		}
		if _, revoked := replica.tombs[a.CredentialID]; revoked {
			return LoginGrant{}, ErrCredentialRevoked
		}
	}
	if a.SignCount <= cred.SignCount {
		cred.Replays++
		if f.cfg.ReplayTolerance > 0 && cred.Replays >= f.cfg.ReplayTolerance {
			cred.State = CredentialQuarantined
		}
		f.creds[cred.ID] = cred
		return LoginGrant{}, ErrCounterReplay
	}
	cred.SignCount = a.SignCount
	f.creds[cred.ID] = cred
	f.seq++
	return LoginGrant{
		CredentialID: cred.ID,
		UserID:       cred.UserID,
		Region:       region,
		Epoch:        cred.Epoch,
		Seq:          f.seq,
	}, nil
}

// Revoke marks a credential revoked in the home revocation ledger.
func (f *PasskeyRegionFabric) Revoke(credentialID, reason string) (RevocationStamp, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	cred, ok := f.creds[credentialID]
	if !ok {
		return RevocationStamp{}, ErrCredentialNotFound
	}
	if stamp, exists := f.tombs[credentialID]; exists {
		return stamp, nil
	}
	f.epoch++
	stamp := RevocationStamp{
		CredentialID: credentialID,
		Reason:       reason,
		Epoch:        f.epoch,
	}
	f.tombs[credentialID] = stamp
	cred.State = CredentialRevoked
	f.creds[credentialID] = cred
	return stamp, nil
}

// Propagate delivers revocation tombstones up to the given epoch to a region.
func (f *PasskeyRegionFabric) Propagate(region string, upToEpoch int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if region == f.cfg.HomeRegion {
		return 0, nil
	}
	target, ok := f.replica[region]
	if !ok {
		return 0, ErrUnknownRegion
	}
	if upToEpoch > f.epoch {
		upToEpoch = f.epoch
	}
	pending := make([]RevocationStamp, 0, len(f.tombs))
	for _, stamp := range f.tombs {
		if stamp.Epoch <= upToEpoch {
			if _, delivered := target.tombs[stamp.CredentialID]; !delivered {
				pending = append(pending, stamp)
			}
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].Epoch < pending[j].Epoch })
	for _, stamp := range pending {
		target.tombs[stamp.CredentialID] = stamp
	}
	return len(pending), nil
}

// Status reports the replication watermark and mode of one region.
func (f *PasskeyRegionFabric) Status(region string) RegionStatus {
	f.mu.Lock()
	defer f.mu.Unlock()

	if region == f.cfg.HomeRegion {
		return RegionStatus{
			Region:      region,
			Epoch:       f.epoch,
			Records:     len(f.creds),
			Revocations: len(f.tombs),
			Mode:        ReplicaActive,
		}
	}
	replica, ok := f.replica[region]
	if !ok {
		return RegionStatus{Region: region}
	}
	return RegionStatus{
		Region:      region,
		Epoch:       replica.watermark,
		Records:     len(replica.records),
		Revocations: len(replica.tombs),
		Mode:        replicaMode(f.epoch-replica.watermark, f.cfg.AckWindow),
	}
}

func replicaMode(lag, ackWindow int64) string {
	switch {
	case lag <= 0:
		return ReplicaActive
	case lag <= ackWindow:
		return ReplicaLagging
	default:
		return ReplicaReadOnly
	}
}

// Credential returns the credential record known at the home region.
func (f *PasskeyRegionFabric) Credential(credentialID string) (Credential, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	cred, ok := f.creds[credentialID]
	return cred, ok
}

// Regions lists the configured home region followed by its replicas.
func (f *PasskeyRegionFabric) Regions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	regions := make([]string, 0, len(f.cfg.Replicas)+1)
	regions = append(regions, f.cfg.HomeRegion)
	regions = append(regions, f.cfg.Replicas...)
	return regions
}

// snapshotState is the serialisable whole-fabric state.
type snapshotState struct {
	Epoch    int64                       `json:"epoch"`
	Seq      int64                       `json:"seq"`
	Creds    map[string]Credential       `json:"credentials"`
	Tombs    map[string]RevocationStamp  `json:"tombstones"`
	Replicas map[string]snapshotReplica  `json:"replicas"`
}

type snapshotReplica struct {
	Watermark int64                       `json:"watermark"`
	Records   map[string]Credential       `json:"records"`
	Tombs     map[string]RevocationStamp  `json:"tombstones"`
}

// Snapshot serialises the whole fabric state.
func (f *PasskeyRegionFabric) Snapshot() ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	state := snapshotState{
		Epoch:    f.epoch,
		Seq:      f.seq,
		Creds:    make(map[string]Credential, len(f.creds)),
		Tombs:    make(map[string]RevocationStamp, len(f.tombs)),
		Replicas: make(map[string]snapshotReplica, len(f.replica)),
	}
	for id, cred := range f.creds {
		state.Creds[id] = cred
	}
	for id, stamp := range f.tombs {
		state.Tombs[id] = stamp
	}
	for region, replica := range f.replica {
		snap := snapshotReplica{
			Watermark: replica.watermark,
			Records:   make(map[string]Credential, len(replica.records)),
			Tombs:     make(map[string]RevocationStamp, len(replica.tombs)),
		}
		for id, cred := range replica.records {
			snap.Records[id] = cred
		}
		for id, stamp := range replica.tombs {
			snap.Tombs[id] = stamp
		}
		state.Replicas[region] = snap
	}
	return json.Marshal(state)
}

// Restore replaces the whole fabric state from a snapshot.
func (f *PasskeyRegionFabric) Restore(data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	var state snapshotState
	if err := json.Unmarshal(data, &state); err != nil {
		return ErrBadSnapshot
	}
	if err := validateSnapshot(&state); err != nil {
		return err
	}

	creds := make(map[string]Credential, len(state.Creds))
	for id, cred := range state.Creds {
		creds[id] = cred
	}
	tombs := make(map[string]RevocationStamp, len(state.Tombs))
	for id, stamp := range state.Tombs {
		tombs[id] = stamp
	}
	replicas := make(map[string]*replicaState, len(f.cfg.Replicas))
	for _, region := range f.cfg.Replicas {
		replicas[region] = newReplicaState()
	}
	for region, snap := range state.Replicas {
		replica, ok := replicas[region]
		if !ok {
			replica = newReplicaState()
			replicas[region] = replica
		}
		replica.watermark = snap.Watermark
		for id, cred := range snap.Records {
			replica.records[id] = cred
		}
		for id, stamp := range snap.Tombs {
			replica.tombs[id] = stamp
		}
	}

	epoch := state.Epoch
	if f.epoch > epoch {
		epoch = f.epoch
	}
	seq := state.Seq
	if f.seq > seq {
		seq = f.seq
	}

	f.creds = creds
	f.tombs = tombs
	f.replica = replicas
	f.epoch = epoch
	f.seq = seq
	return nil
}

func validateSnapshot(state *snapshotState) error {
	if state.Epoch < 0 || state.Seq < 0 {
		return ErrBadSnapshot
	}
	for _, cred := range state.Creds {
		if cred.Epoch < 1 || cred.Epoch > state.Epoch {
			return ErrBadSnapshot
		}
	}
	for _, stamp := range state.Tombs {
		if stamp.Epoch < 1 || stamp.Epoch > state.Epoch {
			return ErrBadSnapshot
		}
	}
	for _, replica := range state.Replicas {
		if replica.Watermark < 0 || replica.Watermark > state.Epoch {
			return ErrBadSnapshot
		}
		for _, cred := range replica.Records {
			if cred.Epoch < 1 || cred.Epoch > replica.Watermark {
				return ErrBadSnapshot
			}
		}
		for _, stamp := range replica.Tombs {
			if stamp.Epoch < 1 || stamp.Epoch > state.Epoch {
				return ErrBadSnapshot
			}
		}
	}
	return nil
}
