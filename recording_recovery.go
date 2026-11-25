// Package task rebuilds a playable index from recording segments left behind by
// an interrupted recorder.
package task

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

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
	Name          string
	Frames        int
	VideoFrames   int
	AudioFrames   int
	StartMs       int64
	DurationMs    int64
	Truncated     bool
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
	ErrScanRoot        = errors.New("scan root is not a readable directory")
	ErrBadCursor       = errors.New("scan cursor is invalid")
	ErrTooManySegments = errors.New("segment count exceeds the configured cap")
	ErrGapTooLarge     = errors.New("timestamp gap exceeds the configured cap")
	ErrNothingToResume = errors.New("no segment after the cursor")
	ErrBadReport       = errors.New("report is internally inconsistent")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// RecordingRecovery owns the last rebuilt index.
type RecordingRecovery struct {
	cfg  Config
	mu   sync.RWMutex
	last Report
}

// NewRecordingRecovery builds a recovery with the given caps.
func NewRecordingRecovery(cfg Config) *RecordingRecovery {
	if cfg.MaxSegments <= 0 {
		cfg.MaxSegments = DefaultMaxSegments
	}
	if cfg.MaxFillMs <= 0 {
		cfg.MaxFillMs = DefaultMaxFillMs
	}
	return &RecordingRecovery{cfg: cfg}
}

// listSegments returns the sorted .seg file names under root.
func listSegments(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, ErrScanRoot
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".seg") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}

// Scan lists the segment files under path and returns a cursor.
func (r *RecordingRecovery) Scan(path string) (ScanCursor, error) {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return ScanCursor{}, ErrScanRoot
	}
	names, err := listSegments(path)
	if err != nil {
		return ScanCursor{}, err
	}
	cur := ScanCursor{Root: path, Scanned: len(names)}
	for _, name := range names {
		fi, err := os.Stat(filepath.Join(path, name))
		if err != nil {
			return ScanCursor{}, ErrScanRoot
		}
		cur.Bytes += fi.Size()
	}
	if len(names) > 0 {
		cur.Last = names[len(names)-1]
	}
	return cur, nil
}

// frame is one parsed line of a segment file.
type frame struct {
	pts   int64
	dur   int64
	video bool
	audio bool
}

// parseSegment reads one segment file. An unterminated tail line is dropped
// and counted as discarded; a corrupt line stops the parse with the frames
// read so far kept.
func parseSegment(path string) (frames []frame, truncated bool, discarded int, corrupt bool, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, 0, false, err
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		truncated = true
		discarded = 1
		idx := bytes.LastIndexByte(data, '\n')
		if idx < 0 {
			data = nil
		} else {
			data = data[:idx+1]
		}
	}
	body := strings.TrimSuffix(string(data), "\n")
	if body == "" {
		return nil, truncated, discarded, false, nil
	}
	for _, line := range strings.Split(body, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return frames, truncated, discarded, true, nil
		}
		pts, perr := strconv.ParseInt(fields[0], 10, 64)
		dur, derr := strconv.ParseInt(fields[1], 10, 64)
		if perr != nil || derr != nil {
			return frames, truncated, discarded, true, nil
		}
		f := frame{pts: pts, dur: dur}
		switch fields[2] {
		case "v":
			f.video = true
		case "a":
			f.audio = true
		case "va":
			f.video, f.audio = true, true
		default:
			return frames, truncated, discarded, true, nil
		}
		frames = append(frames, f)
	}
	return frames, truncated, discarded, false, nil
}

// segmentBuilder accumulates the rebuilt index while segments are appended.
type segmentBuilder struct {
	segments  []SegmentInfo
	filledMs  int64
	repaired  int
	discarded int
	maxFillMs int64
}

// append parses one segment file and adds it to the index, filling any
// timestamp gap against the previous segment. It reports whether a corrupt
// line stopped the scan.
func (b *segmentBuilder) append(root, name string) (corrupt bool, err error) {
	frames, truncated, discarded, corrupt, err := parseSegment(filepath.Join(root, name))
	if err != nil {
		return false, err
	}
	b.discarded += discarded
	if len(frames) == 0 {
		return corrupt, nil
	}
	seg := SegmentInfo{Name: name, Truncated: truncated}
	var last frame
	for i, f := range frames {
		seg.Frames++
		if f.video {
			seg.VideoFrames++
		}
		if f.audio {
			seg.AudioFrames++
		}
		if i == 0 {
			seg.StartMs = f.pts
		}
		last = f
	}
	seg.DurationMs = last.pts + last.dur - seg.StartMs
	switch {
	case seg.VideoFrames == 0 && seg.AudioFrames == 0:
		seg.MissingTracks = "va"
	case seg.VideoFrames == 0:
		seg.MissingTracks = "v"
	case seg.AudioFrames == 0:
		seg.MissingTracks = "a"
	}
	if n := len(b.segments); n > 0 {
		prev := b.segments[n-1]
		prevEnd := prev.StartMs + prev.DurationMs
		if seg.StartMs > prevEnd {
			gap := seg.StartMs - prevEnd
			if gap > b.maxFillMs {
				return false, ErrGapTooLarge
			}
			b.filledMs += gap
			b.repaired++
		}
	}
	b.segments = append(b.segments, seg)
	return corrupt, nil
}

// report assembles the final report from the builder state.
func (b *segmentBuilder) report(stopReason string, cur ScanCursor) Report {
	var total int64
	for _, seg := range b.segments {
		total += seg.DurationMs
	}
	return Report{
		Segments:        b.segments,
		TotalMs:         total + b.filledMs,
		FilledMs:        b.filledMs,
		Repaired:        b.repaired,
		DiscardedFrames: b.discarded,
		StopReason:      stopReason,
		Cursor:          cur,
	}
}

// store replaces the live report atomically.
func (r *RecordingRecovery) store(rep Report) {
	r.mu.Lock()
	r.last = rep
	r.mu.Unlock()
}

// Rebuild reconstructs the playable index from the cursor root.
func (r *RecordingRecovery) Rebuild(cur ScanCursor) (Report, error) {
	if cur.Root == "" {
		return Report{}, ErrBadCursor
	}
	names, err := listSegments(cur.Root)
	if err != nil {
		return Report{}, err
	}
	if len(names) > r.cfg.MaxSegments {
		return Report{}, ErrTooManySegments
	}
	b := segmentBuilder{maxFillMs: r.cfg.MaxFillMs}
	stop := StopComplete
	for _, name := range names {
		corrupt, err := b.append(cur.Root, name)
		if err != nil {
			return Report{}, err
		}
		if corrupt {
			stop = StopCorrupt
			break
		}
	}
	fresh, err := r.Scan(cur.Root)
	if err != nil {
		return Report{}, err
	}
	rep := b.report(stop, fresh)
	r.store(rep)
	return rep, nil
}

// Resume continues a partial rebuild after the cursor's last segment.
func (r *RecordingRecovery) Resume(cur ScanCursor) (Report, error) {
	if cur.Root == "" {
		return Report{}, ErrBadCursor
	}
	names, err := listSegments(cur.Root)
	if err != nil {
		return Report{}, err
	}
	if len(names) > r.cfg.MaxSegments {
		return Report{}, ErrTooManySegments
	}
	var fresh []string
	for _, name := range names {
		if name > cur.Last {
			fresh = append(fresh, name)
		}
	}
	if len(fresh) == 0 {
		return Report{}, ErrNothingToResume
	}
	b := segmentBuilder{maxFillMs: r.cfg.MaxFillMs}
	r.mu.RLock()
	if r.last.Cursor.Root == cur.Root && r.last.Cursor.Last == cur.Last {
		b.segments = append(b.segments, r.last.Segments...)
		b.filledMs = r.last.FilledMs
		b.repaired = r.last.Repaired
		b.discarded = r.last.DiscardedFrames
	}
	r.mu.RUnlock()
	stop := StopComplete
	for _, name := range fresh {
		corrupt, err := b.append(cur.Root, name)
		if err != nil {
			return Report{}, err
		}
		if corrupt {
			stop = StopCorrupt
			break
		}
	}
	newCur, err := r.Scan(cur.Root)
	if err != nil {
		return Report{}, err
	}
	rep := b.report(stop, newCur)
	r.store(rep)
	return rep, nil
}

// Validate checks that a report is internally consistent.
func (r *RecordingRecovery) Validate(report Report) error {
	if report.FilledMs < 0 {
		return ErrBadReport
	}
	var total int64
	for _, seg := range report.Segments {
		if seg.Frames < 1 {
			return ErrBadReport
		}
		total += seg.DurationMs
	}
	if total+report.FilledMs != report.TotalMs {
		return ErrBadReport
	}
	return nil
}

// LastReport returns the newest report held by the recovery.
func (r *RecordingRecovery) LastReport() Report {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.last
}

// Names returns the segment names of the newest report.
func (r *RecordingRecovery) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, len(r.last.Segments))
	for i, seg := range r.last.Segments {
		names[i] = seg.Name
	}
	return names
}

// SegmentCount returns the number of segments in the newest report.
func (r *RecordingRecovery) SegmentCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.last.Segments)
}

// Snapshot serialises the newest report and its cursor.
func (r *RecordingRecovery) Snapshot() ([]byte, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return json.Marshal(r.last)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (r *RecordingRecovery) Restore(data []byte) error {
	var rep Report
	if err := json.Unmarshal(data, &rep); err != nil {
		return ErrBadSnapshot
	}
	r.mu.Lock()
	r.last = rep
	r.mu.Unlock()
	return nil
}
