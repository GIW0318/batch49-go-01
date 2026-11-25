// Package task replicates an identity directory across regions with monotonic
// versions, tombstones and a read only degradation for lagging regions.
package task

import (
	"errors"
)

// errNotImplemented marks behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

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

// IdentityReplicaStream owns the per region tables, watermarks and logs.
type IdentityReplicaStream struct {
	limits Limits
}

// NewIdentityReplicaStream builds a stream with the given caps.
func NewIdentityReplicaStream(limits Limits) *IdentityReplicaStream {
	return &IdentityReplicaStream{limits: limits}
}

// CompareVersion returns -1, 0 or 1 for a lower, equal or higher a than b.
func CompareVersion(a, b Version) int {
	return 0
}

// RegisterRegion admits a region into the stream.
func (s *IdentityReplicaStream) RegisterRegion(regionID string) error {
	return errNotImplemented
}

// Apply replicates one batch into a region.
func (s *IdentityReplicaStream) Apply(regionID string, batch []Change) (ApplyResult, error) {
	return ApplyResult{}, errNotImplemented
}

// Write performs one local mutation in a region.
func (s *IdentityReplicaStream) Write(regionID, userID, name string, version Version) error {
	return errNotImplemented
}

// Watermark reports the highest version applied in a region.
func (s *IdentityReplicaStream) Watermark(regionID string) (Version, error) {
	return Version{}, errNotImplemented
}

// Lag counts how far a region trails the global watermark.
func (s *IdentityReplicaStream) Lag(regionID string) (int64, error) {
	return 0, errNotImplemented
}

// GlobalVersion reports the highest version known to any region.
func (s *IdentityReplicaStream) GlobalVersion() Version {
	return Version{}
}

// Tombstone records a global delete marker for one identity.
func (s *IdentityReplicaStream) Tombstone(id string, version Version) error {
	return errNotImplemented
}

// Read returns the identity view of one region.
func (s *IdentityReplicaStream) Read(regionID, userID string) (Record, error) {
	return Record{}, errNotImplemented
}

// ReadOnly reports whether a region has been degraded to read only.
func (s *IdentityReplicaStream) ReadOnly(regionID string) (bool, error) {
	return false, errNotImplemented
}

// Audit returns the retained audit entries of one region, oldest first.
func (s *IdentityReplicaStream) Audit(regionID string) []AuditEntry {
	return nil
}

// Checkpoint serialises the state of one region.
func (s *IdentityReplicaStream) Checkpoint(regionID string) ([]byte, error) {
	return nil, errNotImplemented
}

// Resume restores one region from a checkpoint.
func (s *IdentityReplicaStream) Resume(regionID string, cp []byte) error {
	return errNotImplemented
}

// Snapshot serialises every region.
func (s *IdentityReplicaStream) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole stream with a snapshot, all or nothing.
func (s *IdentityReplicaStream) Restore(data []byte) error {
	return errNotImplemented
}
