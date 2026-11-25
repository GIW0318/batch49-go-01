// Package task stabilises live caption partial results into rolling lines.
package task

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// Errors returned by the stabiliser.
var (
	ErrBadPartial     = errors.New("partial result is invalid")
	ErrUnknownSegment = errors.New("caption segment is unknown")
	ErrInvalidWindow  = errors.New("correction window is invalid")
	ErrSegmentLimit   = errors.New("caption segment limit reached")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// Defaults used when the configuration leaves a field at zero.
const (
	DefaultCorrectionWindow = 400 * time.Millisecond
	DefaultMaxLines         = 3
	DefaultMaxSegments      = 64
)

const (
	snapshotMagic   = "gsb-live-caption-stabilizer"
	snapshotVersion = 1
)

// Partial is one partial recognition result pushed by the engine.
type Partial struct {
	SegID string
	Seq   int
	Text  string
	Start time.Duration
	Now   time.Duration
}

// Line is one on-screen caption line.
type Line struct {
	SegID  string
	Seq    int
	Text   string
	Start  time.Duration
	Shown  time.Duration
	Frozen bool
}

// CaptionConfig bounds the rolling window and the correction window.
type CaptionConfig struct {
	CorrectionWindow time.Duration
	MaxLines         int
	MaxSegments      int
}

// CaptionStats reports counters of the stabiliser.
type CaptionStats struct {
	Received     int
	Applied      int
	DroppedStale int
	DroppedFinal int
	LateEdits    int
	Segments     int
	Frozen       int
	Evicted      int
}

type captionSegment struct {
	line      Line
	finalized bool
}

// LiveCaptionStabilizer keeps partial results stable on screen.
type LiveCaptionStabilizer struct {
	mu       sync.RWMutex
	cfg      CaptionConfig
	segments map[string]*captionSegment
	order    []string
	stats    CaptionStats
}

type snapshotLine struct {
	SegID     string        `json:"seg_id"`
	Seq       int           `json:"seq"`
	Text      string        `json:"text"`
	Start     time.Duration `json:"start_ns"`
	Shown     time.Duration `json:"shown_ns"`
	Frozen    bool          `json:"frozen"`
	Finalized bool          `json:"finalized"`
}

type snapshotPayload struct {
	Version          int            `json:"version"`
	CorrectionWindow time.Duration  `json:"correction_window_ns"`
	MaxLines         int            `json:"max_lines"`
	MaxSegments      int            `json:"max_segments"`
	Stats            CaptionStats   `json:"stats"`
	Segments         []snapshotLine `json:"segments"`
}

type snapshotEnvelope struct {
	Magic    string          `json:"magic"`
	Payload  json.RawMessage `json:"payload"`
	Checksum string          `json:"checksum_sha256"`
}

// NewLiveCaptionStabilizer builds a stabiliser with the given limits.
func NewLiveCaptionStabilizer(cfg CaptionConfig) *LiveCaptionStabilizer {
	if cfg.CorrectionWindow <= 0 {
		cfg.CorrectionWindow = DefaultCorrectionWindow
	}
	if cfg.MaxLines == 0 {
		cfg.MaxLines = DefaultMaxLines
	}
	if cfg.MaxSegments == 0 {
		cfg.MaxSegments = DefaultMaxSegments
	}
	if cfg.MaxLines < 0 {
		cfg.MaxLines = DefaultMaxLines
	}
	if cfg.MaxSegments < 0 {
		cfg.MaxSegments = DefaultMaxSegments
	}

	return &LiveCaptionStabilizer{
		cfg:      cfg,
		segments: make(map[string]*captionSegment),
	}
}

// Update applies one partial result to its segment.
func (s *LiveCaptionStabilizer) Update(p Partial) error {
	if strings.TrimSpace(p.SegID) == "" || p.Seq < 1 {
		return ErrBadPartial
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.stats.Received++

	segment, known := s.segments[p.SegID]
	if !known {
		if s.stats.Segments >= s.cfg.MaxSegments {
			return ErrSegmentLimit
		}

		segment = &captionSegment{
			line: Line{
				SegID: p.SegID,
				Seq:   p.Seq,
				Text:  p.Text,
				Start: p.Start,
				Shown: p.Now,
			},
		}
		s.segments[p.SegID] = segment
		s.order = append(s.order, p.SegID)
		s.stats.Segments++
		s.stats.Applied++
		if s.stats.Segments > s.cfg.MaxLines {
			s.stats.Evicted++
		}
		return nil
	}

	if segment.finalized {
		s.stats.DroppedFinal++
		return nil
	}

	if p.Seq <= segment.line.Seq {
		s.stats.DroppedStale++
		return nil
	}

	elapsed := p.Now - segment.line.Shown
	if segment.line.Frozen || elapsed > s.cfg.CorrectionWindow {
		s.stats.LateEdits++
		if !segment.line.Frozen {
			segment.line.Frozen = true
			s.stats.Frozen++
		}
		return nil
	}

	segment.line.Seq = p.Seq
	segment.line.Text = p.Text
	segment.line.Start = p.Start
	s.stats.Applied++
	return nil
}

// Finalize freezes one segment so later partial results cannot touch it.
func (s *LiveCaptionStabilizer) Finalize(segID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	segment, ok := s.segments[segID]
	if !ok {
		return ErrUnknownSegment
	}

	alreadyFrozen := segment.line.Frozen
	segment.finalized = true
	segment.line.Frozen = true
	if !alreadyFrozen {
		s.stats.Frozen++
	}
	return nil
}

// ForceFinalize freezes every unfrozen segment first shown at or before cutoff.
func (s *LiveCaptionStabilizer) ForceFinalize(cutoff time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, segID := range s.order {
		segment := s.segments[segID]
		if segment.line.Shown > cutoff || segment.finalized {
			continue
		}

		segment.finalized = true
		if !segment.line.Frozen {
			segment.line.Frozen = true
			s.stats.Frozen++
		}
	}
	return nil
}

// Rolling returns the live lines in display order.
func (s *LiveCaptionStabilizer) Rolling() []Line {
	s.mu.RLock()
	defer s.mu.RUnlock()

	start := 0
	if len(s.order) > s.cfg.MaxLines {
		start = len(s.order) - s.cfg.MaxLines
	}

	lines := make([]Line, 0, len(s.order)-start)
	for _, segID := range s.order[start:] {
		lines = append(lines, s.segments[segID].line)
	}
	return lines
}

// SetCorrectionWindow changes how long a revealed line may still be edited.
func (s *LiveCaptionStabilizer) SetCorrectionWindow(d time.Duration) error {
	if d < 0 {
		return fmt.Errorf("%w: %s", ErrInvalidWindow, d)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.CorrectionWindow = d
	return nil
}

// Pending returns the lines that are not frozen yet.
func (s *LiveCaptionStabilizer) Pending() []Line {
	s.mu.RLock()
	defer s.mu.RUnlock()

	pending := make([]Line, 0, len(s.order))
	for _, segID := range s.order {
		line := s.segments[segID].line
		if !line.Frozen {
			pending = append(pending, line)
		}
	}
	return pending
}

// Stats returns a copy of the counters.
func (s *LiveCaptionStabilizer) Stats() CaptionStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stats
}

// Reset drops every line and counter.
func (s *LiveCaptionStabilizer) Reset() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.segments = make(map[string]*captionSegment)
	s.order = nil
	s.stats = CaptionStats{}
	return nil
}

// Snapshot serialises the rolling window and counters.
func (s *LiveCaptionStabilizer) Snapshot() ([]byte, error) {
	s.mu.RLock()
	payload := snapshotPayload{
		Version:          snapshotVersion,
		CorrectionWindow: s.cfg.CorrectionWindow,
		MaxLines:         s.cfg.MaxLines,
		MaxSegments:      s.cfg.MaxSegments,
		Stats:            s.stats,
		Segments:         make([]snapshotLine, 0, len(s.order)),
	}

	for _, segID := range s.order {
		segment := s.segments[segID]
		payload.Segments = append(payload.Segments, snapshotLine{
			SegID:     segment.line.SegID,
			Seq:       segment.line.Seq,
			Text:      segment.line.Text,
			Start:     segment.line.Start,
			Shown:     segment.line.Shown,
			Frozen:    segment.line.Frozen,
			Finalized: segment.finalized,
		})
	}
	s.mu.RUnlock()

	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode caption snapshot: %w", err)
	}

	checksum := sha256.Sum256(rawPayload)
	envelope := snapshotEnvelope{
		Magic:    snapshotMagic,
		Payload:  rawPayload,
		Checksum: hex.EncodeToString(checksum[:]),
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("encode caption snapshot envelope: %w", err)
	}
	return data, nil
}

// Restore replaces the state with a snapshot, all or nothing.
func (s *LiveCaptionStabilizer) Restore(data []byte) error {
	payload, err := decodeSnapshot(data)
	if err != nil {
		return err
	}

	segments := make(map[string]*captionSegment, len(payload.Segments))
	order := make([]string, 0, len(payload.Segments))
	frozenCount := 0

	for _, item := range payload.Segments {
		if strings.TrimSpace(item.SegID) == "" || item.Seq < 1 {
			return fmt.Errorf("%w: invalid segment", ErrBadSnapshot)
		}
		if _, exists := segments[item.SegID]; exists {
			return fmt.Errorf("%w: duplicate segment %q", ErrBadSnapshot, item.SegID)
		}

		segment := &captionSegment{
			line: Line{
				SegID:  item.SegID,
				Seq:    item.Seq,
				Text:   item.Text,
				Start:  item.Start,
				Shown:  item.Shown,
				Frozen: item.Frozen,
			},
			finalized: item.Finalized,
		}
		segments[item.SegID] = segment
		order = append(order, item.SegID)
		if item.Frozen {
			frozenCount++
		}
	}

	if err := validateSnapshotStats(payload, frozenCount); err != nil {
		return err
	}

	s.mu.Lock()
	s.cfg = CaptionConfig{
		CorrectionWindow: payload.CorrectionWindow,
		MaxLines:         payload.MaxLines,
		MaxSegments:      payload.MaxSegments,
	}
	s.segments = segments
	s.order = order
	s.stats = payload.Stats
	s.mu.Unlock()
	return nil
}

func decodeSnapshot(data []byte) (snapshotPayload, error) {
	var payload snapshotPayload
	if len(data) == 0 {
		return payload, fmt.Errorf("%w: empty snapshot", ErrBadSnapshot)
	}

	var envelope snapshotEnvelope
	if err := unmarshalStrictSnapshot(data, &envelope); err != nil {
		return payload, fmt.Errorf("%w: invalid envelope: %v", ErrBadSnapshot, err)
	}
	if envelope.Magic != snapshotMagic {
		return payload, fmt.Errorf("%w: bad magic", ErrBadSnapshot)
	}

	expectedChecksum, err := hex.DecodeString(envelope.Checksum)
	if err != nil || len(expectedChecksum) != sha256.Size {
		return payload, fmt.Errorf("%w: bad checksum encoding", ErrBadSnapshot)
	}
	actualChecksum := sha256.Sum256(envelope.Payload)
	if subtle.ConstantTimeCompare(expectedChecksum, actualChecksum[:]) != 1 {
		return payload, fmt.Errorf("%w: checksum mismatch", ErrBadSnapshot)
	}

	if err := unmarshalStrictSnapshot(envelope.Payload, &payload); err != nil {
		return payload, fmt.Errorf("%w: invalid payload: %v", ErrBadSnapshot, err)
	}
	if payload.Version != snapshotVersion {
		return payload, fmt.Errorf("%w: unsupported version %d", ErrBadSnapshot, payload.Version)
	}
	if payload.CorrectionWindow < 0 {
		return payload, fmt.Errorf("%w: negative correction window", ErrBadSnapshot)
	}
	if payload.MaxLines <= 0 || payload.MaxSegments <= 0 {
		return payload, fmt.Errorf("%w: invalid limits", ErrBadSnapshot)
	}
	if len(payload.Segments) > payload.MaxSegments {
		return payload, fmt.Errorf("%w: too many segments", ErrBadSnapshot)
	}
	return payload, nil
}

func validateSnapshotStats(payload snapshotPayload, frozenCount int) error {
	stats := payload.Stats
	if stats.Received < 0 || stats.Applied < 0 || stats.DroppedStale < 0 ||
		stats.DroppedFinal < 0 || stats.LateEdits < 0 || stats.Segments < 0 ||
		stats.Frozen < 0 || stats.Evicted < 0 {
		return fmt.Errorf("%w: negative counter", ErrBadSnapshot)
	}
	if stats.Segments != len(payload.Segments) {
		return fmt.Errorf("%w: segment counter mismatch", ErrBadSnapshot)
	}
	if stats.Frozen != frozenCount {
		return fmt.Errorf("%w: frozen counter mismatch", ErrBadSnapshot)
	}
	expectedEvicted := len(payload.Segments) - payload.MaxLines
	if expectedEvicted < 0 {
		expectedEvicted = 0
	}
	if stats.Evicted != expectedEvicted {
		return fmt.Errorf("%w: evicted counter mismatch", ErrBadSnapshot)
	}
	return nil
}

func unmarshalStrictSnapshot(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}

	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}
