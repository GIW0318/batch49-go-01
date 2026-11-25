// Package task swaps material variants at frame boundaries while an
// asynchronously compiled replacement is still in flight.
package task

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
)

// errNotImplemented is kept for compatibility with the original skeleton.
var errNotImplemented = errors.New("not implemented")

// Variant lifecycle states.
const (
	StateIdle      = "idle"
	StateQueued    = "queued"
	StateCompiling = "compiling"
	StateCompiled  = "compiled"
	StateReady     = "ready"
	StateFailed    = "failed"
	StateCancelled = "cancelled"
)

// Defaults applied when the configuration leaves a field at zero.
const (
	DefaultSwapLimit    = 2
	DefaultFailureLimit = 3
)

// Errors returned by the swapper.
var (
	ErrBlankVariant   = errors.New("variant key is required")
	ErrUnknownVariant = errors.New("variant is not tracked")
	ErrCancelTooLate  = errors.New("compile already started")
	ErrInvalidConfig  = errors.New("invalid swap configuration")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// VariantKey names one compiled material variant.
type VariantKey string

// Future is the ticket handed back for an asynchronous compile request.
type Future struct {
	ID  string     `json:"id"`
	Key VariantKey `json:"key"`
	Seq int64      `json:"seq"`
}

// MaterialHandle is the frame visible material instance of one variant.
type MaterialHandle struct {
	Key      VariantKey `json:"key"`
	Gen      uint64     `json:"gen"`
	Fallback bool       `json:"fallback"`
}

// SwapConfig bounds the work done per frame and the retries of one variant.
type SwapConfig struct {
	MaxSwapsPerFrame int
	FailureLimit     int
}

// SwapReport describes what one frame boundary did.
type SwapReport struct {
	Frame    int `json:"frame"`
	Swapped  int `json:"swapped"`
	Deferred int `json:"deferred"`
	Failed   int `json:"failed"`
}

type variantState struct {
	state    State
	gen      uint64
	handle   MaterialHandle
	params   map[string]float64
	failures int
	future   Future
}

type retiredHandle struct {
	handle    MaterialHandle
	swapFrame int
}

// VariantHotSwapper owns the compile queue, the handle table and the
// per variant parameters.
type VariantHotSwapper struct {
	mu sync.Mutex

	cfg SwapConfig

	frame            int
	nextSeq          int64
	failedSinceFrame int
	variants         map[VariantKey]*variantState
	compileQueue     []VariantKey
	swapQueue        []VariantKey
	retired          map[VariantKey][]retiredHandle
}

// NewVariantHotSwapper builds a swapper with the given bounds.
func NewVariantHotSwapper(cfg SwapConfig) *VariantHotSwapper {
	if cfg.MaxSwapsPerFrame <= 0 {
		cfg.MaxSwapsPerFrame = DefaultSwapLimit
	}
	if cfg.FailureLimit <= 0 {
		cfg.FailureLimit = DefaultFailureLimit
	}
	return &VariantHotSwapper{
		cfg:      cfg,
		nextSeq:  1,
		variants: make(map[VariantKey]*variantState),
		retired:  make(map[VariantKey][]retiredHandle),
	}
}

// Request queues an asynchronous compile and returns a shared future for the
// key.
func (s *VariantHotSwapper) Request(key VariantKey) (Future, error) {
	if isBlank(key) {
		return Future{}, ErrBlankVariant
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if v, ok := s.variants[key]; ok {
		switch v.state {
		case StateQueued, StateCompiling, StateCompiled, StateReady:
			return v.future, nil
		case StateCancelled, StateFailed:
			// A later request starts a fresh compile attempt for this key.
			v.state = StateQueued
			v.failures = 0
			v.future = s.newFutureLocked(key)
			v.handle.Fallback = false
			s.enqueueCompileLocked(key)
			return v.future, nil
		}
	}

	future := s.newFutureLocked(key)
	s.variants[key] = &variantState{
		state:  StateQueued,
		gen:    1,
		handle: MaterialHandle{Key: key, Gen: 1},
		params: make(map[string]float64),
		future: future,
	}
	s.enqueueCompileLocked(key)
	return future, nil
}

// Query reports the live handle and state of one variant.
func (s *VariantHotSwapper) Query(key VariantKey) (MaterialHandle, State) {
	s.mu.Lock()
	defer s.mu.Unlock()

	v, ok := s.variants[key]
	if !ok {
		return MaterialHandle{Key: key}, StateIdle
	}
	return v.handle, v.state
}

// SetParam writes one numeric parameter of a variant.
func (s *VariantHotSwapper) SetParam(key VariantKey, name string, value float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	v, ok := s.variants[key]
	if !ok {
		return ErrUnknownVariant
	}
	if v.params == nil {
		v.params = make(map[string]float64)
	}
	v.params[name] = value
	return nil
}

// Params returns a copy of the numeric parameters of a variant.
func (s *VariantHotSwapper) Params(key VariantKey) map[string]float64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	v, ok := s.variants[key]
	if !ok {
		return nil
	}
	out := make(map[string]float64, len(v.params))
	for name, value := range v.params {
		out[name] = value
	}
	return out
}

// StartCompile moves a queued variant into the compiling state.
func (s *VariantHotSwapper) StartCompile(key VariantKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	v, ok := s.variants[key]
	if !ok {
		return ErrUnknownVariant
	}
	if v.state != StateQueued {
		return ErrCancelTooLate
	}
	v.state = StateCompiling
	return nil
}

// CompleteCompile records the result of the asynchronous compile of a variant.
func (s *VariantHotSwapper) CompleteCompile(key VariantKey, ok bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	v, tracked := s.variants[key]
	if !tracked {
		return ErrUnknownVariant
	}
	if v.state != StateCompiling {
		return ErrCancelTooLate
	}

	s.removeCompileLocked(key)
	if ok {
		v.state = StateCompiled
		v.handle.Fallback = false
		s.enqueueSwapLocked(key)
		return nil
	}

	v.failures++
	if v.failures >= s.cfg.FailureLimit {
		v.state = StateFailed
		v.handle.Fallback = true
		s.failedSinceFrame++
		return nil
	}

	v.state = StateQueued
	s.enqueueCompileLocked(key)
	return nil
}

// Cancel drops a variant that has not started compiling yet.
func (s *VariantHotSwapper) Cancel(key VariantKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	v, ok := s.variants[key]
	if !ok {
		return ErrUnknownVariant
	}
	if v.state != StateQueued {
		return ErrCancelTooLate
	}
	v.state = StateCancelled
	s.removeCompileLocked(key)
	return nil
}

// AdvanceFrame applies pending handle swaps up to the configured limit.
func (s *VariantHotSwapper) AdvanceFrame() SwapReport {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.frame++
	report := SwapReport{
		Frame:  s.frame,
		Failed: s.failedSinceFrame,
	}
	s.failedSinceFrame = 0

	processed := 0
	for len(s.swapQueue) > 0 && processed < s.cfg.MaxSwapsPerFrame {
		key := s.swapQueue[0]
		s.swapQueue = s.swapQueue[1:]
		processed++

		v, ok := s.variants[key]
		if !ok {
			report.Failed++
			continue
		}
		if v.state != StateCompiled {
			// A terminal state won the race; the stale entry still consumed
			// this frame's work budget.
			report.Failed++
			continue
		}

		old := v.handle
		if old.Gen == 0 {
			old.Gen = v.gen
		}
		s.retired[key] = append(s.retired[key], retiredHandle{
			handle:    old,
			swapFrame: s.frame,
		})

		v.gen++
		v.handle = MaterialHandle{Key: key, Gen: v.gen}
		v.state = StateReady
		report.Swapped++
	}

	report.Deferred = len(s.swapQueue)
	s.releaseRetiredLocked()
	return report
}

// Generation reports the current handle generation of a variant.
func (s *VariantHotSwapper) Generation(key VariantKey) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	v, ok := s.variants[key]
	if !ok {
		return 0
	}
	return v.gen
}

// PendingCompiles counts the variants occupying a compile queue slot.
func (s *VariantHotSwapper) PendingCompiles() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.compileQueue)
}

type snapshotVariant struct {
	Key      VariantKey         `json:"key"`
	State    State              `json:"state"`
	Gen      uint64             `json:"gen"`
	Fallback bool               `json:"fallback"`
	Failures int                `json:"failures"`
	Future   Future             `json:"future"`
	Params   map[string]float64 `json:"params"`
}

type snapshotPayload struct {
	Version          int               `json:"version"`
	Config           SwapConfig        `json:"config"`
	Frame            int               `json:"frame"`
	NextSeq          int64             `json:"next_seq"`
	FailedSinceFrame int               `json:"failed_since_frame"`
	CompileQueue     []VariantKey      `json:"compile_queue"`
	SwapQueue        []VariantKey      `json:"swap_queue"`
	Variants         []snapshotVariant `json:"variants"`
}

const snapshotVersion = 1
const snapshotMagic = "variant-hot-swapper:1\n"

// Snapshot serialises the variant states, generations, parameters and failure
// counters.
func (s *VariantHotSwapper) Snapshot() ([]byte, error) {
	s.mu.Lock()

	keys := make([]string, 0, len(s.variants))
	for key := range s.variants {
		keys = append(keys, string(key))
	}
	sort.Strings(keys)

	payload := snapshotPayload{
		Version:          snapshotVersion,
		Config:           s.cfg,
		Frame:            s.frame,
		NextSeq:          s.nextSeq,
		FailedSinceFrame: s.failedSinceFrame,
		CompileQueue:     append([]VariantKey(nil), s.compileQueue...),
		SwapQueue:        append([]VariantKey(nil), s.swapQueue...),
		Variants:         make([]snapshotVariant, 0, len(keys)),
	}
	for _, name := range keys {
		key := VariantKey(name)
		v := s.variants[key]
		params := make(map[string]float64, len(v.params))
		for pname, value := range v.params {
			params[pname] = value
		}
		payload.Variants = append(payload.Variants, snapshotVariant{
			Key:      key,
			State:    v.state,
			Gen:      v.gen,
			Fallback: v.handle.Fallback,
			Failures: v.failures,
			Future:   v.future,
			Params:   params,
		})
	}
	s.mu.Unlock()

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	header := snapshotMagic + hex.EncodeToString(sum[:]) + "\n"
	out := make([]byte, 0, len(header)+len(body))
	out = append(out, header...)
	out = append(out, body...)
	return out, nil
}

// Restore replaces the whole runtime state with a snapshot, all or nothing.
func (s *VariantHotSwapper) Restore(data []byte) error {
	payload, err := decodeSnapshot(data)
	if err != nil {
		return ErrBadSnapshot
	}

	cfg := payload.Config
	if cfg.MaxSwapsPerFrame <= 0 {
		cfg.MaxSwapsPerFrame = DefaultSwapLimit
	}
	if cfg.FailureLimit <= 0 {
		cfg.FailureLimit = DefaultFailureLimit
	}

	variants := make(map[VariantKey]*variantState, len(payload.Variants))
	futureIDs := make(map[string]struct{}, len(payload.Variants))
	nextSeq := payload.NextSeq
	if nextSeq < 1 {
		return ErrBadSnapshot
	}
	for _, sv := range payload.Variants {
		if isBlank(sv.Key) || !validState(sv.State) || sv.Gen == 0 || sv.Failures < 0 {
			return ErrBadSnapshot
		}
		if _, exists := variants[sv.Key]; exists {
			return ErrBadSnapshot
		}
		if sv.Future.Key != sv.Key || sv.Future.ID == "" || sv.Future.Seq < 1 || sv.Future.Seq >= nextSeq {
			return ErrBadSnapshot
		}
		if _, exists := futureIDs[sv.Future.ID]; exists {
			return ErrBadSnapshot
		}
		futureIDs[sv.Future.ID] = struct{}{}
		if sv.State != StateFailed && sv.Fallback {
			return ErrBadSnapshot
		}
		params := make(map[string]float64, len(sv.Params))
		for name, value := range sv.Params {
			params[name] = value
		}
		variants[sv.Key] = &variantState{
			state:    sv.State,
			gen:      sv.Gen,
			handle:   MaterialHandle{Key: sv.Key, Gen: sv.Gen, Fallback: sv.Fallback},
			params:   params,
			failures: sv.Failures,
			future:   sv.Future,
		}
	}

	compileQueue, err := validateQueue(payload.CompileQueue, variants, func(v *variantState) bool {
		return v.state == StateQueued || v.state == StateCompiling
	})
	if err != nil {
		return ErrBadSnapshot
	}
	swapQueue, err := validateQueue(payload.SwapQueue, variants, func(v *variantState) bool {
		return v.state == StateCompiled
	})
	if err != nil {
		return ErrBadSnapshot
	}
	if payload.Frame < 0 || payload.FailedSinceFrame < 0 {
		return ErrBadSnapshot
	}

	s.mu.Lock()
	s.cfg = cfg
	s.frame = payload.Frame
	s.nextSeq = nextSeq
	s.failedSinceFrame = payload.FailedSinceFrame
	s.variants = variants
	s.compileQueue = compileQueue
	s.swapQueue = swapQueue
	s.retired = make(map[VariantKey][]retiredHandle)
	s.mu.Unlock()
	return nil
}

// State is the lifecycle state of one variant.
type State string

func (s *VariantHotSwapper) newFutureLocked(key VariantKey) Future {
	seq := s.nextSeq
	s.nextSeq++
	return Future{
		ID:  "variant-" + string(key) + "-" + itoa(seq),
		Key: key,
		Seq: seq,
	}
}

func (s *VariantHotSwapper) enqueueCompileLocked(key VariantKey) {
	if !containsKey(s.compileQueue, key) {
		s.compileQueue = append(s.compileQueue, key)
	}
}

func (s *VariantHotSwapper) enqueueSwapLocked(key VariantKey) {
	if !containsKey(s.swapQueue, key) {
		s.swapQueue = append(s.swapQueue, key)
	}
}

func (s *VariantHotSwapper) removeCompileLocked(key VariantKey) {
	s.compileQueue = removeKey(s.compileQueue, key)
}

func (s *VariantHotSwapper) releaseRetiredLocked() {
	for key, entries := range s.retired {
		kept := entries[:0]
		for _, entry := range entries {
			if s.frame <= entry.swapFrame+1 {
				kept = append(kept, entry)
			}
		}
		if len(kept) == 0 {
			delete(s.retired, key)
		} else {
			s.retired[key] = kept
		}
	}
}

func itoa(value int64) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var buf [24]byte
	i := len(buf)
	for value > 0 {
		i--
		buf[i] = byte('0' + value%10)
		value /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func isBlank(key VariantKey) bool {
	return strings.TrimSpace(string(key)) == ""
}

func containsKey(keys []VariantKey, key VariantKey) bool {
	for _, candidate := range keys {
		if candidate == key {
			return true
		}
	}
	return false
}

func removeKey(keys []VariantKey, key VariantKey) []VariantKey {
	for i, candidate := range keys {
		if candidate == key {
			copy(keys[i:], keys[i+1:])
			return keys[:len(keys)-1]
		}
	}
	return keys
}

func validState(state State) bool {
	switch state {
	case StateIdle, StateQueued, StateCompiling, StateCompiled, StateReady, StateFailed, StateCancelled:
		return true
	default:
		return false
	}
}

func validateQueue(keys []VariantKey, variants map[VariantKey]*variantState, allowed func(*variantState) bool) ([]VariantKey, error) {
	out := make([]VariantKey, 0, len(keys))
	seen := make(map[VariantKey]struct{}, len(keys))
	for _, key := range keys {
		if _, duplicate := seen[key]; duplicate {
			return nil, ErrBadSnapshot
		}
		v, ok := variants[key]
		if !ok || !allowed(v) {
			return nil, ErrBadSnapshot
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	return out, nil
}

func decodeSnapshot(data []byte) (snapshotPayload, error) {
	var payload snapshotPayload
	if !bytes.HasPrefix(data, []byte(snapshotMagic)) {
		return payload, ErrBadSnapshot
	}
	rest := data[len(snapshotMagic):]
	split := bytes.IndexByte(rest, '\n')
	if split < 0 {
		return payload, ErrBadSnapshot
	}
	want, err := hex.DecodeString(string(rest[:split]))
	if err != nil || len(want) != sha256.Size {
		return payload, ErrBadSnapshot
	}
	body := rest[split+1:]
	sum := sha256.Sum256(body)
	if subtle.ConstantTimeCompare(want, sum[:]) != 1 {
		return payload, ErrBadSnapshot
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return payload, ErrBadSnapshot
	}
	if payload.Version != snapshotVersion {
		return payload, ErrBadSnapshot
	}
	return payload, nil
}
