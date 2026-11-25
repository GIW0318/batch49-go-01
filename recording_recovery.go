// Package task rebuilds a playable index from recording segments left behind by
// an interrupted recorder.
package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Stop reasons reported by a rebuild.
const (
	StopComplete = "complete"
	StopCorrupt  = "corrupt"
)

// Defaults used when the matching config field is zero.
const (
	DefaultMaxSegments = 64
	DefaultMaxFillMs   = 60000
)

// Config bounds one recovery run.
type Config struct {
	MaxSegments int
	MaxFillMs   int64
}

// ScanCursor is the position of one scan over a recording directory.
type ScanCursor struct {
	Root    string
	Last    string
	Scanned int
	Bytes   int64
}

// SegmentInfo is what a rebuild learned about one segment.
type SegmentInfo struct {
	Name         string
	Frames       int
	VideoFrames  int
	AudioFrames  int
	StartMs      int64
	DurationMs   int64
	Truncated    bool
	MissingTracks string
}

// Report is the rebuilt index plus the repair bookkeeping.
type Report struct {
	Segments        []SegmentInfo
	TotalMs         int64
	FilledMs        int64
	Repaired        int
	DiscardedFrames int
	StopReason      string
	Cursor          ScanCursor
}

// Errors reported by the recovery.
var (
	ErrScanRoot         = errors.New("scan root is not a readable directory")
	ErrBadCursor        = errors.New("scan cursor is invalid")
	ErrTooManySegments  = errors.New("segment count exceeds the configured cap")
	ErrGapTooLarge      = errors.New("timestamp gap exceeds the configured cap")
	ErrNothingToResume  = errors.New("no segment after the cursor")
	ErrBadReport        = errors.New("report is internally inconsistent")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

// RecordingRecovery owns the last rebuilt index.
type RecordingRecovery struct {
	cfg Config
}

// NewRecordingRecovery builds a recovery with the given caps.
func NewRecordingRecovery(cfg Config) *RecordingRecovery {
	return &RecordingRecovery{cfg: cfg}
}

// Scan lists the segment files under path and returns a cursor.
func (r *RecordingRecovery) Scan(path string) (ScanCursor, error) {
	return ScanCursor{}, errNotImplemented
}

// Rebuild reconstructs the playable index from the cursor root.
func (r *RecordingRecovery) Rebuild(cur ScanCursor) (Report, error) {
	return Report{}, errNotImplemented
}

// Resume continues a partial rebuild after the cursor's last segment.
func (r *RecordingRecovery) Resume(cur ScanCursor) (Report, error) {
	return Report{}, errNotImplemented
}

// Validate checks that a report is internally consistent.
func (r *RecordingRecovery) Validate(report Report) error {
	return errNotImplemented
}

// LastReport returns the newest report held by the recovery.
func (r *RecordingRecovery) LastReport() Report {
	return Report{}
}

// Names returns the segment names of the newest report.
func (r *RecordingRecovery) Names() []string {
	return nil
}

// SegmentCount returns the number of segments in the newest report.
func (r *RecordingRecovery) SegmentCount() int {
	return 0
}

// Snapshot serialises the newest report and its cursor.
func (r *RecordingRecovery) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (r *RecordingRecovery) Restore(data []byte) error {
	return errNotImplemented
}
