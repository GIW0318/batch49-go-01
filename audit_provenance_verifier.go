// Package task verifies that audit events pushed by several collection
// sources form one monotonic sequence per source and survive a receiver
// restart without losing their provenance.
package task

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"
)

// Verdict is the outcome of verifying a single audit event.
type Verdict string

// Reason explains a verdict and is what the audit console groups by.
type Reason string

const (
	VerdictAccept            Verdict = "accept"
	VerdictDuplicate         Verdict = "duplicate"
	VerdictOutOfOrder        Verdict = "out_of_order"
	VerdictGapPending        Verdict = "gap_pending"
	VerdictGap               Verdict = "gap"
	VerdictSourceMismatch    Verdict = "source_mismatch"
	VerdictSignatureMismatch Verdict = "signature_mismatch"
	VerdictIsolated          Verdict = "isolated"
)

const (
	ReasonNone              Reason = ""
	ReasonDuplicateSeq      Reason = "duplicate_seq"
	ReasonDuplicateConflict Reason = "duplicate_conflict"
	ReasonBelowWatermark    Reason = "below_watermark"
	ReasonGapWithinWindow   Reason = "gap_within_window"
	ReasonGapBeyondWindow   Reason = "gap_beyond_window"
	ReasonUnknownSource     Reason = "unknown_source"
	ReasonOwnerMismatch     Reason = "owner_mismatch"
	ReasonSignatureMismatch Reason = "signature_mismatch"
	ReasonCreditExhausted   Reason = "credit_exhausted"
	ReasonManualIsolation   Reason = "manual_isolation"
)

// Defaults applied by the constructor when a configuration field is left
// unset.
const (
	DefaultGapWindow uint64 = 3
	DefaultCredit    int    = 5

	snapshotVersion = 1
)

var (
	ErrBadSource      = errors.New("source id and boot id are required")
	ErrSourceExists   = errors.New("source already registered")
	ErrUnknownSource  = errors.New("source is not registered")
	ErrSourceIsolated = errors.New("source is isolated")
	ErrBadEvent       = errors.New("event id, digest and source are required")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// VerifierConfig carries the tuning knobs of one receiver.
type VerifierConfig struct {
	GapWindow        uint64
	MaxCredit        int
	MaxTimestampSkew time.Duration
	MaxGapsPerSource int
}

// Source is one registered collector together with the boot it belongs to.
type Source struct {
	ID     string
	BootID string
	Key    string
}

// Event is one audit record pushed by a source.
type Event struct {
	ID     string
	Source string
	BootID string
	Seq    uint64
	At     time.Time
	Signer string
	Digest string
}

// Gap is a run of sequence numbers that never arrived.
type Gap struct {
	Source string
	From   uint64
	To     uint64
}

// BootBaseline is the retained sequence baseline for one collector boot.
type BootBaseline struct {
	BootID    string
	Watermark uint64
	Current   bool
	Gaps      []Gap
}

// AuditProvenanceVerifier keeps one high watermark, one credit balance and one
// gap ledger per registered source.
type AuditProvenanceVerifier struct {
	mu     sync.RWMutex
	config VerifierConfig
	state  *verifierState
}

type verifierState struct {
	sources map[string]*sourceState
	owners  map[string]string
}

type sourceState struct {
	source          Source
	currentBootID   string
	boots           map[string]*bootState
	bootOrder       []string
	credit          int
	isolated        bool
	isolationReason Reason
	isolationDetail string
	gaps            []storedGap
}

type bootState struct {
	id         string
	watermark  uint64
	lastDigest string
	seen       bool
}

type storedGap struct {
	bootID string
	gap    Gap
}

type snapshotData struct {
	Version int                       `json:"version"`
	Sources map[string]sourceSnapshot `json:"sources"`
	Owners  map[string]string         `json:"owners"`
}

type sourceSnapshot struct {
	ID              string                  `json:"id"`
	BootID          string                  `json:"boot_id"`
	Key             string                  `json:"key"`
	CurrentBootID   string                  `json:"current_boot_id"`
	Credit          int                     `json:"credit"`
	Isolated        bool                    `json:"isolated"`
	IsolationReason string                  `json:"isolation_reason"`
	IsolationDetail string                  `json:"isolation_detail"`
	BootOrder       []string                `json:"boot_order"`
	Boots           map[string]bootSnapshot `json:"boots"`
	Gaps            []gapSnapshot           `json:"gaps"`
}

type bootSnapshot struct {
	ID         string `json:"id"`
	Watermark  uint64 `json:"watermark"`
	LastDigest string `json:"last_digest"`
	Seen       bool   `json:"seen"`
}

type gapSnapshot struct {
	BootID string `json:"boot_id"`
	Source string `json:"source"`
	From   uint64 `json:"from"`
	To     uint64 `json:"to"`
}

// NewAuditProvenanceVerifier returns a receiver with no source registered.
func NewAuditProvenanceVerifier(cfg VerifierConfig) *AuditProvenanceVerifier {
	if cfg.GapWindow == 0 {
		cfg.GapWindow = DefaultGapWindow
	}
	if cfg.MaxCredit <= 0 {
		cfg.MaxCredit = DefaultCredit
	}
	return &AuditProvenanceVerifier{
		config: cfg,
		state:  newVerifierState(),
	}
}

func newVerifierState() *verifierState {
	return &verifierState{
		sources: make(map[string]*sourceState),
		owners:  make(map[string]string),
	}
}

// Register adds a source that is allowed to report events.
func (v *AuditProvenanceVerifier) Register(src Source) error {
	if src.ID == "" || src.BootID == "" {
		return ErrBadSource
	}

	v.mu.Lock()
	defer v.mu.Unlock()

	if _, exists := v.state.sources[src.ID]; exists {
		return ErrSourceExists
	}

	v.state.sources[src.ID] = &sourceState{
		source:        src,
		currentBootID: src.BootID,
		boots: map[string]*bootState{
			src.BootID: {id: src.BootID},
		},
		bootOrder: []string{src.BootID},
		credit:    v.config.MaxCredit,
	}
	return nil
}

// Verify checks one event against the state of its source and advances the
// source watermark when the event is fresh.
func (v *AuditProvenanceVerifier) Verify(ev Event) (Verdict, Reason, error) {
	if ev.ID == "" || ev.Source == "" || ev.Digest == "" {
		return "", ReasonNone, ErrBadEvent
	}

	v.mu.Lock()
	defer v.mu.Unlock()

	src, ok := v.state.sources[ev.Source]
	if !ok {
		return VerdictSourceMismatch, ReasonUnknownSource, ErrUnknownSource
	}

	if src.isolated {
		reason := src.isolationReason
		if reason == "" {
			reason = ReasonManualIsolation
		}
		return VerdictIsolated, reason, ErrSourceIsolated
	}

	if ev.Signer != src.source.Key {
		return VerdictSignatureMismatch, ReasonSignatureMismatch, nil
	}

	if owner, owned := v.state.owners[ev.ID]; owned && owner != ev.Source {
		return VerdictSourceMismatch, ReasonOwnerMismatch, nil
	}

	boot := v.bootForEvent(src, ev.BootID)

	if boot.seen {
		switch {
		case ev.Seq < boot.watermark:
			return VerdictOutOfOrder, ReasonBelowWatermark, nil
		case ev.Seq == boot.watermark:
			if ev.Digest == boot.lastDigest {
				return VerdictDuplicate, ReasonDuplicateSeq, nil
			}
			v.deductCredit(src)
			return VerdictDuplicate, ReasonDuplicateConflict, nil
		}
	}

	verdict := VerdictAccept
	reason := ReasonNone

	if ev.Seq > boot.watermark && ev.Seq-boot.watermark > 1 {
		gap := storedGap{
			bootID: boot.id,
			gap: Gap{
				Source: ev.Source,
				From:   boot.watermark + 1,
				To:     ev.Seq - 1,
			},
		}
		src.gaps = append(src.gaps, gap)
		capGaps(src, v.config.MaxGapsPerSource)

		missing := ev.Seq - boot.watermark - 1
		if missing <= v.config.GapWindow {
			verdict = VerdictGapPending
			reason = ReasonGapWithinWindow
		} else {
			verdict = VerdictGap
			reason = ReasonGapBeyondWindow
			v.deductCredit(src)
		}
	}

	boot.watermark = ev.Seq
	boot.lastDigest = ev.Digest
	boot.seen = true

	if _, owned := v.state.owners[ev.ID]; !owned {
		v.state.owners[ev.ID] = ev.Source
	}

	return verdict, reason, nil
}

func (v *AuditProvenanceVerifier) bootForEvent(src *sourceState, bootID string) *bootState {
	if bootID == "" {
		bootID = src.currentBootID
	}

	boot, ok := src.boots[bootID]
	if ok {
		return boot
	}

	boot = &bootState{id: bootID}
	src.boots[bootID] = boot
	src.bootOrder = append(src.bootOrder, bootID)
	src.currentBootID = bootID
	return boot
}

func (v *AuditProvenanceVerifier) deductCredit(src *sourceState) {
	if src.credit > 0 {
		src.credit--
	}
	if src.credit == 0 {
		src.isolated = true
		src.isolationReason = ReasonCreditExhausted
		src.isolationDetail = string(ReasonCreditExhausted)
	}
}

func capGaps(src *sourceState, maxGaps int) {
	if maxGaps <= 0 || len(src.gaps) <= maxGaps {
		return
	}
	kept := make([]storedGap, maxGaps)
	copy(kept, src.gaps[len(src.gaps)-maxGaps:])
	src.gaps = kept
}

// Watermark reports the highest sequence number accepted for the current
// boot baseline of a source.
func (v *AuditProvenanceVerifier) Watermark(src string) (uint64, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()

	state, ok := v.state.sources[src]
	if !ok {
		return 0, ErrUnknownSource
	}
	return state.boots[state.currentBootID].watermark, nil
}

// BootWatermarks returns retained watermarks for all observed collector boots.
func (v *AuditProvenanceVerifier) BootWatermarks(src string) ([]BootBaseline, error) {
	return v.Baselines(src)
}

// Baselines exports current and archived boot watermarks and their gaps.
func (v *AuditProvenanceVerifier) Baselines(src string) ([]BootBaseline, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()

	state, ok := v.state.sources[src]
	if !ok {
		return nil, ErrUnknownSource
	}

	result := make([]BootBaseline, 0, len(state.bootOrder))
	for _, bootID := range state.bootOrder {
		boot := state.boots[bootID]
		result = append(result, BootBaseline{
			BootID:    bootID,
			Watermark: boot.watermark,
			Current:   bootID == state.currentBootID,
			Gaps:      v.gapsForBootLocked(state, bootID),
		})
	}
	return result, nil
}

// Gaps returns the recorded gaps of a source ordered by their first number.
func (v *AuditProvenanceVerifier) Gaps(src string) []Gap {
	v.mu.RLock()
	defer v.mu.RUnlock()

	state, ok := v.state.sources[src]
	if !ok {
		return nil
	}
	return v.gapsForBootLocked(state, "")
}

func (v *AuditProvenanceVerifier) gapsForBootLocked(state *sourceState, bootID string) []Gap {
	gaps := make([]Gap, 0, len(state.gaps))
	for _, recorded := range state.gaps {
		if bootID != "" && recorded.bootID != bootID {
			continue
		}
		gaps = append(gaps, recorded.gap)
	}
	sort.SliceStable(gaps, func(i, j int) bool {
		if gaps[i].From != gaps[j].From {
			return gaps[i].From < gaps[j].From
		}
		return gaps[i].To < gaps[j].To
	})
	return gaps
}

// Isolate puts a source into quarantine with a human readable reason.
func (v *AuditProvenanceVerifier) Isolate(src string, reason string) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	state, ok := v.state.sources[src]
	if !ok {
		return ErrUnknownSource
	}
	state.isolated = true
	state.isolationReason = ReasonManualIsolation
	state.isolationDetail = reason
	return nil
}

// Isolated reports whether a source is quarantined and why.
func (v *AuditProvenanceVerifier) Isolated(src string) (bool, string) {
	v.mu.RLock()
	defer v.mu.RUnlock()

	state, ok := v.state.sources[src]
	if !ok || !state.isolated {
		return false, ""
	}
	if state.isolationDetail != "" {
		return true, state.isolationDetail
	}
	if state.isolationReason != "" {
		return true, string(state.isolationReason)
	}
	return true, string(ReasonManualIsolation)
}

// Credit reports how many trust points a source still has.
func (v *AuditProvenanceVerifier) Credit(src string) (int, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()

	state, ok := v.state.sources[src]
	if !ok {
		return 0, ErrUnknownSource
	}
	return state.credit, nil
}

// Owner reports which source registered an event identifier first.
func (v *AuditProvenanceVerifier) Owner(eventID string) (string, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()

	owner, ok := v.state.owners[eventID]
	return owner, ok
}

// Snapshot serialises watermarks, credits, gaps and isolation state.
func (v *AuditProvenanceVerifier) Snapshot() ([]byte, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()

	payload := snapshotData{
		Version: snapshotVersion,
		Sources: make(map[string]sourceSnapshot, len(v.state.sources)),
		Owners:  make(map[string]string, len(v.state.owners)),
	}

	for eventID, owner := range v.state.owners {
		payload.Owners[eventID] = owner
	}

	for id, state := range v.state.sources {
		dto := sourceSnapshot{
			ID:              state.source.ID,
			BootID:          state.source.BootID,
			Key:             state.source.Key,
			CurrentBootID:   state.currentBootID,
			Credit:          state.credit,
			Isolated:        state.isolated,
			IsolationReason: string(state.isolationReason),
			IsolationDetail: state.isolationDetail,
			BootOrder:       append([]string(nil), state.bootOrder...),
			Boots:           make(map[string]bootSnapshot, len(state.boots)),
			Gaps:            make([]gapSnapshot, 0, len(state.gaps)),
		}
		for bootID, boot := range state.boots {
			dto.Boots[bootID] = bootSnapshot{
				ID:         boot.id,
				Watermark:  boot.watermark,
				LastDigest: boot.lastDigest,
				Seen:       boot.seen,
			}
		}
		for _, recorded := range state.gaps {
			dto.Gaps = append(dto.Gaps, gapSnapshot{
				BootID: recorded.bootID,
				Source: recorded.gap.Source,
				From:   recorded.gap.From,
				To:     recorded.gap.To,
			})
		}
		payload.Sources[id] = dto
	}

	return json.Marshal(payload)
}

// Restore rebuilds the receiver from a snapshot produced by Snapshot.
func (v *AuditProvenanceVerifier) Restore(payload []byte) error {
	state, err := stateFromSnapshot(payload, v.config.MaxGapsPerSource)
	if err != nil {
		return err
	}

	v.mu.Lock()
	v.state = state
	v.mu.Unlock()
	return nil
}

func stateFromSnapshot(payload []byte, maxGaps int) (*verifierState, error) {
	if len(bytes.TrimSpace(payload)) == 0 {
		return nil, fmt.Errorf("%w: empty payload", ErrBadSnapshot)
	}

	var snapshot snapshotData
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&snapshot); err != nil {
		return nil, fmt.Errorf("%w: decode: %v", ErrBadSnapshot, err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("%w: trailing data", ErrBadSnapshot)
		}
		return nil, fmt.Errorf("%w: decode: %v", ErrBadSnapshot, err)
	}

	if snapshot.Version != snapshotVersion {
		return nil, fmt.Errorf("%w: unsupported version %d", ErrBadSnapshot, snapshot.Version)
	}
	if snapshot.Sources == nil {
		return nil, fmt.Errorf("%w: missing state maps", ErrBadSnapshot)
	}
	if snapshot.Owners == nil {
		snapshot.Owners = make(map[string]string)
	}

	state := newVerifierState()
	for id, dto := range snapshot.Sources {
		if id == "" {
			return nil, fmt.Errorf("%w: empty source id", ErrBadSnapshot)
		}
		if dto.ID != "" && dto.ID != id {
			return nil, fmt.Errorf("%w: source id mismatch", ErrBadSnapshot)
		}
		if dto.BootID == "" {
			return nil, fmt.Errorf("%w: source %q missing registered boot", ErrBadSnapshot, id)
		}
		if dto.Credit < 0 {
			return nil, fmt.Errorf("%w: source %q has negative credit", ErrBadSnapshot, id)
		}
		if len(dto.Boots) == 0 {
			return nil, fmt.Errorf("%w: source %q has no boot state", ErrBadSnapshot, id)
		}

		currentBootID := dto.CurrentBootID
		if currentBootID == "" {
			currentBootID = dto.BootID
		}
		if _, ok := dto.Boots[currentBootID]; !ok {
			return nil, fmt.Errorf("%w: source %q points at missing current boot", ErrBadSnapshot, id)
		}
		if _, ok := dto.Boots[dto.BootID]; !ok {
			return nil, fmt.Errorf("%w: source %q missing registered boot state", ErrBadSnapshot, id)
		}

		sourceState := &sourceState{
			source: Source{
				ID:     id,
				BootID: dto.BootID,
				Key:    dto.Key,
			},
			currentBootID:   currentBootID,
			boots:           make(map[string]*bootState, len(dto.Boots)),
			credit:          dto.Credit,
			isolated:        dto.Isolated,
			isolationReason: Reason(dto.IsolationReason),
			isolationDetail: dto.IsolationDetail,
		}

		for bootID, bootDTO := range dto.Boots {
			if bootID == "" {
				return nil, fmt.Errorf("%w: source %q has empty boot id", ErrBadSnapshot, id)
			}
			if bootDTO.ID != "" && bootDTO.ID != bootID {
				return nil, fmt.Errorf("%w: source %q boot id mismatch", ErrBadSnapshot, id)
			}
			sourceState.boots[bootID] = &bootState{
				id:         bootID,
				watermark:  bootDTO.Watermark,
				lastDigest: bootDTO.LastDigest,
				seen:       bootDTO.Seen || bootDTO.Watermark > 0,
			}
		}

		seenBootOrder := make(map[string]bool, len(dto.BootOrder))
		for _, bootID := range dto.BootOrder {
			if _, ok := sourceState.boots[bootID]; !ok || seenBootOrder[bootID] {
				return nil, fmt.Errorf("%w: source %q has invalid boot order", ErrBadSnapshot, id)
			}
			sourceState.bootOrder = append(sourceState.bootOrder, bootID)
			seenBootOrder[bootID] = true
		}
		for bootID := range sourceState.boots {
			if !seenBootOrder[bootID] {
				sourceState.bootOrder = append(sourceState.bootOrder, bootID)
			}
		}

		for _, gapDTO := range dto.Gaps {
			if gapDTO.Source != "" && gapDTO.Source != id {
				return nil, fmt.Errorf("%w: gap source mismatch", ErrBadSnapshot)
			}
			if gapDTO.From == 0 || gapDTO.From > gapDTO.To {
				return nil, fmt.Errorf("%w: invalid gap range", ErrBadSnapshot)
			}
			if _, ok := sourceState.boots[gapDTO.BootID]; !ok {
				return nil, fmt.Errorf("%w: gap references missing boot", ErrBadSnapshot)
			}
			sourceState.gaps = append(sourceState.gaps, storedGap{
				bootID: gapDTO.BootID,
				gap: Gap{
					Source: id,
					From:   gapDTO.From,
					To:     gapDTO.To,
				},
			})
		}
		capGaps(sourceState, maxGaps)

		if sourceState.credit == 0 && !sourceState.isolated {
			sourceState.isolated = true
			sourceState.isolationReason = ReasonCreditExhausted
			sourceState.isolationDetail = string(ReasonCreditExhausted)
		}
		if sourceState.isolated && sourceState.isolationReason == "" {
			sourceState.isolationReason = ReasonManualIsolation
		}
		state.sources[id] = sourceState
	}

	for eventID, owner := range snapshot.Owners {
		if eventID == "" || owner == "" {
			return nil, fmt.Errorf("%w: invalid owner entry", ErrBadSnapshot)
		}
		if _, ok := state.sources[owner]; !ok {
			return nil, fmt.Errorf("%w: owner references missing source", ErrBadSnapshot)
		}
		state.owners[eventID] = owner
	}

	return state, nil
}
