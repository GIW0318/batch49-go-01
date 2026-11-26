// Package task replicates an identity directory across regions with monotonic
// versions, tombstones and a read only degradation for lagging regions.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"
	"sync"
)

// Change operations.
const (
	OpPut    = "put"
	OpDelete = "delete"
)

// Audit reasons.
const (
	ReasonApplied    = "applied"
	ReasonTombstone  = "tombstone"
	ReasonLocalWrite = "local_write"
)

// Errors returned by the replica stream.
var (
	errNotImplemented    = errors.New("not implemented")
	ErrEmptyRegion       = errors.New("region id is required")
	ErrUnknownRegion     = errors.New("region is not registered")
	ErrRegionExists      = errors.New("region is already registered")
	ErrBadBatch          = errors.New("change batch is empty")
	ErrBatchTooLarge     = errors.New("change batch exceeds the configured limit")
	ErrBadVersion        = errors.New("version epoch and counter must be positive")
	ErrVersionRegression = errors.New("change versions must strictly increase")
	ErrBadOp             = errors.New("change operation must be put or delete")
	ErrEmptyUser         = errors.New("user id is required")
	ErrRegionFull        = errors.New("region record table is full")
	ErrRegionReadOnly    = errors.New("region is read only while it lags behind")
	ErrTombstoneVersion  = errors.New("change version is not above the local tombstone")
	ErrUnknownRecord     = errors.New("identity record is not present in this region")
	ErrBadCheckpoint     = errors.New("checkpoint payload is invalid")
	ErrBadSnapshot       = errors.New("snapshot payload is invalid")
)

const (
	payloadMagic   = "gsb.batch49.identity-replica-stream"
	payloadSchema  = 1
	kindCheckpoint = "checkpoint"
	kindSnapshot   = "snapshot"

	// EnvDisableNewLogic is the rollback switch. Set it to 1/true to
	// restore the pre-implementation stub behaviour for output comparison.
	EnvDisableNewLogic = "IDENTITY_REPLICA_DISABLE_NEW_LOGIC"
)

var baselineLogicEnabled = newLogicDisabledByEnv()

// Version orders changes first by epoch and then by counter.
type Version struct {
	Epoch   int64 `json:"epoch"`
	Counter int64 `json:"counter"`
}

// Change is one replicated identity mutation.
type Change struct {
	ID      string  `json:"id"`
	Op      string  `json:"op"`
	Version Version `json:"version"`
	Origin  string  `json:"origin"`
	Name    string  `json:"name"`
}

// ApplyResult summarises one batch.
type ApplyResult struct {
	Region     string  `json:"region"`
	Applied    int     `json:"applied"`
	Duplicates int     `json:"duplicates"`
	Stale      int     `json:"stale"`
	Watermark  Version `json:"watermark"`
}

// Record is the identity view of one region.
type Record struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Version Version `json:"version"`
	Deleted bool    `json:"deleted"`
	Origin  string  `json:"origin"`
}

// AuditEntry is one applied change in a region log.
type AuditEntry struct {
	Region  string  `json:"region"`
	ID      string  `json:"id"`
	Op      string  `json:"op"`
	Version Version `json:"version"`
	Origin  string  `json:"origin"`
	Reason  string  `json:"reason"`
	Seq     int64   `json:"seq"`
}

// Limits bounds batches, tables and the audit log.
type Limits struct {
	MaxBatch   int
	MaxRecords int
	MaxAudit   int
	MaxLag     int64
}

type regionState struct {
	watermark Version
	records   map[string]Record
	audit     []AuditEntry
	auditHead int
	auditLen  int
	nextSeq   int64
	processed map[Version]struct{}
}

// IdentityReplicaStream owns the per region tables, watermarks and logs.
type IdentityReplicaStream struct {
	mu         sync.RWMutex
	limits     Limits
	regions    map[string]*regionState
	tombstones map[string]Version
	legacy     bool
}

type payloadEnvelope struct {
	Magic    string          `json:"magic"`
	Schema   int             `json:"schema"`
	Kind     string          `json:"kind"`
	Checksum string          `json:"checksum"`
	Payload  json.RawMessage `json:"payload"`
}

type regionPayload struct {
	Region     string             `json:"region"`
	Watermark  Version            `json:"watermark"`
	Records    map[string]Record  `json:"records"`
	Audit      []AuditEntry       `json:"audit"`
	NextSeq    int64              `json:"next_seq"`
	Processed  []Version          `json:"processed"`
	Tombstones map[string]Version `json:"tombstones,omitempty"`
}

type snapshotPayload struct {
	Regions    []regionPayload    `json:"regions"`
	Tombstones map[string]Version `json:"tombstones"`
}

// NewIdentityReplicaStream builds a stream with the given caps.
func NewIdentityReplicaStream(limits Limits) *IdentityReplicaStream {
	return &IdentityReplicaStream{
		limits:     limits,
		regions:    make(map[string]*regionState),
		tombstones: make(map[string]Version),
		legacy:     baselineLogicEnabled,
	}
}

func newLogicDisabledByEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvDisableNewLogic))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// CompareVersion returns -1, 0 or 1 for a lower, equal or higher a than b.
func CompareVersion(a, b Version) int {
	if baselineLogicEnabled {
		return 0
	}
	if a.Epoch < b.Epoch {
		return -1
	}
	if a.Epoch > b.Epoch {
		return 1
	}
	if a.Counter < b.Counter {
		return -1
	}
	if a.Counter > b.Counter {
		return 1
	}
	return 0
}

// RegisterRegion admits a region into the stream.
func (s *IdentityReplicaStream) RegisterRegion(regionID string) error {
	if s.legacy {
		return errNotImplemented
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if strings.TrimSpace(regionID) == "" {
		return ErrEmptyRegion
	}
	if s.regions == nil {
		s.regions = make(map[string]*regionState)
	}
	if _, ok := s.regions[regionID]; ok {
		return ErrRegionExists
	}
	s.regions[regionID] = newRegionState()
	if s.tombstones == nil {
		s.tombstones = make(map[string]Version)
	}
	return nil
}

// Apply replicates one batch into a region.
func (s *IdentityReplicaStream) Apply(regionID string, batch []Change) (ApplyResult, error) {
	if s.legacy {
		return ApplyResult{}, errNotImplemented
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.regionLocked(regionID)
	if err != nil {
		return ApplyResult{}, err
	}
	result := ApplyResult{Region: regionID, Watermark: state.watermark}
	if len(batch) == 0 {
		return result, ErrBadBatch
	}
	if len(batch) > s.limits.MaxBatch {
		return result, ErrBatchTooLarge
	}

	var maxVersion Version
	for i, change := range batch {
		if strings.TrimSpace(change.ID) == "" {
			return result, ErrEmptyUser
		}
		if change.Op != OpPut && change.Op != OpDelete {
			return result, ErrBadOp
		}
		if !validVersion(change.Version) {
			return result, ErrBadVersion
		}
		if i > 0 && CompareVersion(change.Version, batch[i-1].Version) <= 0 {
			return result, ErrVersionRegression
		}
		if i == 0 || CompareVersion(change.Version, maxVersion) > 0 {
			maxVersion = change.Version
		}
	}

	maxRecords := nonNegative(s.limits.MaxRecords)
	stagedRecords := make(map[string]Record, len(batch))
	stagedTombstones := make(map[string]Version)
	planned := make([]plannedChange, 0, len(batch))
	newRecords := 0

	for _, change := range batch {
		tombstone, hasTombstone := s.tombstones[change.ID]
		if batchTombstone, ok := stagedTombstones[change.ID]; ok &&
			(!hasTombstone || CompareVersion(batchTombstone, tombstone) > 0) {
			tombstone = batchTombstone
			hasTombstone = true
		}
		if hasTombstone {
			cmp := CompareVersion(change.Version, tombstone)
			if cmp < 0 || (cmp == 0 && change.Op != OpDelete) {
				return ApplyResult{Region: regionID, Watermark: state.watermark}, ErrTombstoneVersion
			}
		}

		if _, ok := state.processed[change.Version]; ok {
			result.Duplicates++
			continue
		}
		existing, existingStaged := stagedRecords[change.ID]
		if !existingStaged {
			existing, existingStaged = state.records[change.ID]
		}
		if existingStaged &&
			CompareVersion(change.Version, existing.Version) <= 0 {
			result.Stale++
			continue
		}

		_, inCurrent := state.records[change.ID]
		_, inStaged := stagedRecords[change.ID]
		if !inCurrent && !inStaged && len(state.records)+newRecords >= maxRecords {
			return ApplyResult{Region: regionID, Watermark: state.watermark}, ErrRegionFull
		}
		if !inCurrent && !inStaged {
			newRecords++
		}

		record := Record{
			ID:      change.ID,
			Name:    change.Name,
			Version: change.Version,
			Deleted: change.Op == OpDelete,
			Origin:  change.Origin,
		}
		if record.Deleted {
			record.Name = ""
		}
		stagedRecords[change.ID] = record

		reason := ReasonApplied
		if change.Op == OpDelete {
			reason = ReasonTombstone
			if current, ok := s.tombstones[change.ID]; !ok ||
				CompareVersion(change.Version, current) > 0 {
				stagedTombstones[change.ID] = change.Version
			}
		}

		planned = append(planned, plannedChange{
			record: record,
			entry: AuditEntry{
				Region:  regionID,
				ID:      change.ID,
				Op:      change.Op,
				Version: change.Version,
				Origin:  change.Origin,
				Reason:  reason,
			},
		})
		result.Applied++
	}

	for _, plan := range planned {
		state.records[plan.record.ID] = plan.record
		state.processed[plan.entry.Version] = struct{}{}
		appendAudit(state, plan.entry, s.limits.MaxAudit)
	}
	for id, version := range stagedTombstones {
		if current, ok := s.tombstones[id]; !ok || CompareVersion(version, current) > 0 {
			s.tombstones[id] = version
		}
	}
	if CompareVersion(maxVersion, state.watermark) > 0 {
		state.watermark = maxVersion
	}
	result.Watermark = state.watermark
	return result, nil
}

type plannedChange struct {
	record Record
	entry  AuditEntry
}

// Write performs one local mutation in a region.
func (s *IdentityReplicaStream) Write(regionID, userID, name string, version Version) error {
	if s.legacy {
		return errNotImplemented
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.regionLocked(regionID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(userID) == "" {
		return ErrEmptyUser
	}
	if !validVersion(version) {
		return ErrBadVersion
	}
	if s.readOnlyLocked(state) {
		return ErrRegionReadOnly
	}
	if tombstone, ok := s.tombstones[userID]; ok &&
		CompareVersion(version, tombstone) <= 0 {
		return ErrTombstoneVersion
	}
	if CompareVersion(version, state.watermark) <= 0 {
		return ErrVersionRegression
	}
	if _, exists := state.records[userID]; !exists &&
		len(state.records) >= nonNegative(s.limits.MaxRecords) {
		return ErrRegionFull
	}

	state.records[userID] = Record{
		ID:      userID,
		Name:    name,
		Version: version,
		Origin:  regionID,
	}
	state.watermark = version
	state.processed[version] = struct{}{}
	appendAudit(state, AuditEntry{
		Region:  regionID,
		ID:      userID,
		Op:      OpPut,
		Version: version,
		Origin:  regionID,
		Reason:  ReasonLocalWrite,
	}, s.limits.MaxAudit)
	return nil
}

// Watermark reports the highest version applied in a region.
func (s *IdentityReplicaStream) Watermark(regionID string) (Version, error) {
	if s.legacy {
		return Version{}, errNotImplemented
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	state, err := s.regionLocked(regionID)
	if err != nil {
		return Version{}, err
	}
	return state.watermark, nil
}

// Lag counts how far a region trails the global watermark.
func (s *IdentityReplicaStream) Lag(regionID string) (int64, error) {
	if s.legacy {
		return 0, errNotImplemented
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	state, err := s.regionLocked(regionID)
	if err != nil {
		return 0, err
	}
	return lagBetween(s.globalVersionLocked(), state.watermark), nil
}

// GlobalVersion reports the highest version known to any region.
func (s *IdentityReplicaStream) GlobalVersion() Version {
	if s.legacy {
		return Version{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.globalVersionLocked()
}

// Tombstone records a global delete marker for one identity.
func (s *IdentityReplicaStream) Tombstone(id string, version Version) error {
	if s.legacy {
		return errNotImplemented
	}
	if strings.TrimSpace(id) == "" {
		return ErrEmptyUser
	}
	if !validVersion(version) {
		return ErrBadVersion
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.tombstones == nil {
		s.tombstones = make(map[string]Version)
	}
	if current, ok := s.tombstones[id]; ok {
		cmp := CompareVersion(version, current)
		if cmp < 0 {
			return ErrVersionRegression
		}
		if cmp == 0 {
			return nil
		}
	}

	var highestRecord Version
	for _, state := range s.regions {
		record, ok := state.records[id]
		if ok && CompareVersion(record.Version, highestRecord) > 0 {
			highestRecord = record.Version
		}
	}
	if highestRecord != (Version{}) && CompareVersion(version, highestRecord) <= 0 {
		return ErrVersionRegression
	}

	s.tombstones[id] = version
	return nil
}

// Read returns the identity view of one region.
func (s *IdentityReplicaStream) Read(regionID, userID string) (Record, error) {
	if s.legacy {
		return Record{}, errNotImplemented
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	state, err := s.regionLocked(regionID)
	if err != nil {
		return Record{}, err
	}
	if strings.TrimSpace(userID) == "" {
		return Record{}, ErrEmptyUser
	}

	record, exists := state.records[userID]
	if tombstone, ok := s.tombstones[userID]; ok {
		if !exists || CompareVersion(tombstone, record.Version) >= 0 {
			deleted := Record{
				ID:      userID,
				Version: tombstone,
				Deleted: true,
			}
			if exists {
				deleted.Origin = record.Origin
			}
			return deleted, nil
		}
	}
	if !exists {
		return Record{}, ErrUnknownRecord
	}
	return record, nil
}

// ReadOnly reports whether a region has been degraded to read only.
func (s *IdentityReplicaStream) ReadOnly(regionID string) (bool, error) {
	if s.legacy {
		return false, errNotImplemented
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	state, err := s.regionLocked(regionID)
	if err != nil {
		return false, err
	}
	return s.readOnlyLocked(state), nil
}

// Audit returns the retained audit entries of one region, oldest first.
func (s *IdentityReplicaStream) Audit(regionID string) []AuditEntry {
	if s.legacy {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	state, err := s.regionLocked(regionID)
	if err != nil {
		return nil
	}
	audit := orderedAudit(state)
	if len(audit) == 0 {
		return nil
	}
	return audit
}

// Checkpoint serialises the state of one region.
func (s *IdentityReplicaStream) Checkpoint(regionID string) ([]byte, error) {
	if s.legacy {
		return nil, errNotImplemented
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	state, err := s.regionLocked(regionID)
	if err != nil {
		return nil, err
	}
	payload := regionPayload{
		Region:     regionID,
		Watermark:  state.watermark,
		Records:    cloneRecords(state.records),
		Audit:      orderedAudit(state),
		NextSeq:    state.nextSeq,
		Processed:  sortedVersions(state.processed),
		Tombstones: cloneTombstones(s.tombstones),
	}
	out, err := encodePayload(kindCheckpoint, payload)
	if err != nil {
		return nil, ErrBadCheckpoint
	}
	return out, nil
}

// Resume restores one region from a checkpoint.
func (s *IdentityReplicaStream) Resume(regionID string, cp []byte) error {
	if s.legacy {
		return errNotImplemented
	}
	s.mu.RLock()
	_, registered := s.regions[regionID]
	s.mu.RUnlock()
	if !registered {
		return ErrUnknownRegion
	}

	raw, err := decodePayload(cp, kindCheckpoint)
	if err != nil {
		return ErrBadCheckpoint
	}
	var payload regionPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ErrBadCheckpoint
	}
	if payload.Region != regionID {
		return ErrBadCheckpoint
	}
	state, ok := s.stateFromPayload(payload)
	if !ok {
		return ErrBadCheckpoint
	}
	tombstones, ok := validTombstones(payload.Tombstones)
	if !ok {
		return ErrBadCheckpoint
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.regions == nil {
		s.regions = make(map[string]*regionState)
	}
	if _, ok := s.regions[regionID]; !ok {
		return ErrUnknownRegion
	}

	merged := cloneTombstones(s.tombstones)
	for id, version := range tombstones {
		if current, exists := merged[id]; !exists ||
			CompareVersion(version, current) > 0 {
			merged[id] = version
		}
	}

	s.regions[regionID] = state
	s.tombstones = merged
	return nil
}

// Snapshot serialises every region.
func (s *IdentityReplicaStream) Snapshot() ([]byte, error) {
	if s.legacy {
		return nil, errNotImplemented
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	ids := make([]string, 0, len(s.regions))
	for id := range s.regions {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	regions := make([]regionPayload, 0, len(ids))
	for _, id := range ids {
		state := s.regions[id]
		regions = append(regions, regionPayload{
			Region:    id,
			Watermark: state.watermark,
			Records:   cloneRecords(state.records),
			Audit:     orderedAudit(state),
			NextSeq:   state.nextSeq,
			Processed: sortedVersions(state.processed),
		})
	}

	out, err := encodePayload(kindSnapshot, snapshotPayload{
		Regions:    regions,
		Tombstones: cloneTombstones(s.tombstones),
	})
	if err != nil {
		return nil, ErrBadSnapshot
	}
	return out, nil
}

// Restore replaces the whole stream with a snapshot, all or nothing.
func (s *IdentityReplicaStream) Restore(data []byte) error {
	if s.legacy {
		return errNotImplemented
	}
	raw, err := decodePayload(data, kindSnapshot)
	if err != nil {
		return ErrBadSnapshot
	}
	var payload snapshotPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ErrBadSnapshot
	}
	tombstones, ok := validTombstones(payload.Tombstones)
	if !ok {
		return ErrBadSnapshot
	}

	regions := make(map[string]*regionState, len(payload.Regions))
	for _, region := range payload.Regions {
		if strings.TrimSpace(region.Region) == "" {
			return ErrBadSnapshot
		}
		if _, exists := regions[region.Region]; exists {
			return ErrBadSnapshot
		}
		state, ok := s.stateFromPayload(region)
		if !ok {
			return ErrBadSnapshot
		}
		regions[region.Region] = state
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.regions = regions
	s.tombstones = tombstones
	return nil
}

func newRegionState() *regionState {
	return &regionState{
		records:   make(map[string]Record),
		processed: make(map[Version]struct{}),
	}
}

func cloneRecords(in map[string]Record) map[string]Record {
	out := make(map[string]Record, len(in))
	for id, record := range in {
		out[id] = record
	}
	return out
}

func cloneTombstones(in map[string]Version) map[string]Version {
	out := make(map[string]Version, len(in))
	for id, version := range in {
		out[id] = version
	}
	return out
}

func (s *IdentityReplicaStream) regionLocked(regionID string) (*regionState, error) {
	state, ok := s.regions[regionID]
	if !ok {
		return nil, ErrUnknownRegion
	}
	return state, nil
}

func (s *IdentityReplicaStream) globalVersionLocked() Version {
	var global Version
	for _, state := range s.regions {
		if CompareVersion(state.watermark, global) > 0 {
			global = state.watermark
		}
	}
	return global
}

func (s *IdentityReplicaStream) readOnlyLocked(state *regionState) bool {
	return lagBetween(s.globalVersionLocked(), state.watermark) > s.limits.MaxLag
}

func lagBetween(global, local Version) int64 {
	if CompareVersion(global, local) <= 0 {
		return 0
	}
	if local == (Version{}) && global.Epoch == 1 {
		return global.Counter - local.Counter
	}
	if global.Epoch == local.Epoch {
		return global.Counter - local.Counter
	}
	return int64(^uint64(0) >> 1)
}

func validVersion(version Version) bool {
	return version.Epoch > 0 && version.Counter > 0
}

func nonNegative(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func appendAudit(state *regionState, entry AuditEntry, maxAudit int) {
	state.nextSeq++
	entry.Seq = state.nextSeq

	maxAudit = nonNegative(maxAudit)
	if maxAudit == 0 {
		state.audit = nil
		state.auditHead = 0
		state.auditLen = 0
		return
	}
	if state.auditLen < maxAudit {
		state.audit = append(state.audit, entry)
		state.auditLen++
		return
	}
	state.audit[state.auditHead] = entry
	state.auditHead = (state.auditHead + 1) % maxAudit
}

func orderedAudit(state *regionState) []AuditEntry {
	if state.auditLen == 0 || len(state.audit) == 0 {
		return nil
	}
	out := make([]AuditEntry, state.auditLen)
	capacity := len(state.audit)
	if state.auditHead == 0 || state.auditHead+state.auditLen <= capacity {
		copy(out, state.audit[state.auditHead:state.auditHead+state.auditLen])
		return out
	}
	tail := capacity - state.auditHead
	copy(out, state.audit[state.auditHead:])
	copy(out[tail:], state.audit[:state.auditLen-tail])
	return out
}

func sortedVersions(versions map[Version]struct{}) []Version {
	out := make([]Version, 0, len(versions))
	for version := range versions {
		out = append(out, version)
	}
	sort.Slice(out, func(i, j int) bool {
		return CompareVersion(out[i], out[j]) < 0
	})
	return out
}

func encodePayload(kind string, payload any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	return json.Marshal(payloadEnvelope{
		Magic:    payloadMagic,
		Schema:   payloadSchema,
		Kind:     kind,
		Checksum: hex.EncodeToString(sum[:]),
		Payload:  body,
	})
}

func decodePayload(data []byte, kind string) ([]byte, error) {
	var envelope payloadEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, err
	}
	if envelope.Magic != payloadMagic ||
		envelope.Schema != payloadSchema ||
		envelope.Kind != kind ||
		len(envelope.Payload) == 0 {
		return nil, errors.New("invalid payload envelope")
	}
	sum := sha256.Sum256(envelope.Payload)
	if envelope.Checksum != hex.EncodeToString(sum[:]) {
		return nil, errors.New("payload checksum mismatch")
	}
	return envelope.Payload, nil
}

func (s *IdentityReplicaStream) stateFromPayload(payload regionPayload) (*regionState, bool) {
	if strings.TrimSpace(payload.Region) == "" {
		return nil, false
	}
	if payload.Watermark != (Version{}) && !validVersion(payload.Watermark) {
		return nil, false
	}
	if payload.Watermark == (Version{}) &&
		(payload.NextSeq != 0 ||
			len(payload.Records) != 0 ||
			len(payload.Audit) != 0 ||
			len(payload.Processed) != 0) {
		return nil, false
	}
	if len(payload.Records) > nonNegative(s.limits.MaxRecords) {
		return nil, false
	}
	if len(payload.Audit) > nonNegative(s.limits.MaxAudit) {
		return nil, false
	}

	state := newRegionState()
	state.watermark = payload.Watermark
	state.nextSeq = payload.NextSeq
	if state.nextSeq < 0 {
		return nil, false
	}

	for id, record := range payload.Records {
		if id != record.ID || strings.TrimSpace(record.ID) == "" {
			return nil, false
		}
		if !validVersion(record.Version) {
			return nil, false
		}
		if CompareVersion(record.Version, payload.Watermark) > 0 {
			return nil, false
		}
		state.records[id] = record
	}

	var previousSeq int64
	for _, entry := range payload.Audit {
		if entry.Region != payload.Region ||
			entry.Seq <= previousSeq ||
			entry.Seq <= 0 ||
			strings.TrimSpace(entry.ID) == "" ||
			!validVersion(entry.Version) ||
			CompareVersion(entry.Version, payload.Watermark) > 0 ||
			(entry.Op != OpPut && entry.Op != OpDelete) {
			return nil, false
		}
		switch entry.Reason {
		case ReasonApplied, ReasonTombstone, ReasonLocalWrite:
		default:
			return nil, false
		}
		previousSeq = entry.Seq
	}
	if state.nextSeq < previousSeq {
		return nil, false
	}
	state.audit = append([]AuditEntry(nil), payload.Audit...)
	state.auditHead = 0
	state.auditLen = len(state.audit)

	for _, version := range payload.Processed {
		if !validVersion(version) {
			return nil, false
		}
		if CompareVersion(version, payload.Watermark) > 0 {
			return nil, false
		}
		if _, exists := state.processed[version]; exists {
			return nil, false
		}
		state.processed[version] = struct{}{}
	}

	return state, true
}

func validTombstones(payload map[string]Version) (map[string]Version, bool) {
	out := make(map[string]Version, len(payload))
	for id, version := range payload {
		if strings.TrimSpace(id) == "" || !validVersion(version) {
			return nil, false
		}
		out[id] = version
	}
	return out, true
}
