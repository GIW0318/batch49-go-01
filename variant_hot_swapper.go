// Package task swaps material variants at frame boundaries while an
// asynchronously compiled replacement is still in flight.
package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
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

// VariantHotSwapper owns the compile queue, the handle table and the
// per variant parameters.
type VariantHotSwapper struct{}

// NewVariantHotSwapper builds a swapper with the given bounds.
func NewVariantHotSwapper(cfg SwapConfig) *VariantHotSwapper {
	return &VariantHotSwapper{}
}

// Request queues an asynchronous compile and returns a shared future for the
// key.
func (s *VariantHotSwapper) Request(key VariantKey) (Future, error) {
	return Future{}, errNotImplemented
}

// Query reports the live handle and state of one variant.
func (s *VariantHotSwapper) Query(key VariantKey) (MaterialHandle, State) {
	return MaterialHandle{}, StateIdle
}

// SetParam writes one numeric parameter of a variant.
func (s *VariantHotSwapper) SetParam(key VariantKey, name string, value float64) error {
	return errNotImplemented
}

// Params returns a copy of the numeric parameters of a variant.
func (s *VariantHotSwapper) Params(key VariantKey) map[string]float64 {
	return nil
}

// StartCompile moves a queued variant into the compiling state.
func (s *VariantHotSwapper) StartCompile(key VariantKey) error {
	return errNotImplemented
}

// CompleteCompile records the result of the asynchronous compile of a variant.
func (s *VariantHotSwapper) CompleteCompile(key VariantKey, ok bool) error {
	return errNotImplemented
}

// Cancel drops a variant that has not started compiling yet.
func (s *VariantHotSwapper) Cancel(key VariantKey) error {
	return errNotImplemented
}

// AdvanceFrame applies pending handle swaps up to the configured limit.
func (s *VariantHotSwapper) AdvanceFrame() SwapReport {
	return SwapReport{}
}

// Generation reports the current handle generation of a variant.
func (s *VariantHotSwapper) Generation(key VariantKey) uint64 {
	return 0
}

// PendingCompiles counts the variants still waiting for a compile slot.
func (s *VariantHotSwapper) PendingCompiles() int {
	return 0
}

// Snapshot serialises the variant states, generations, parameters and failure
// counters.
func (s *VariantHotSwapper) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole runtime state with a snapshot, all or nothing.
func (s *VariantHotSwapper) Restore(data []byte) error {
	return errNotImplemented
}

// State is the lifecycle state of one variant.
type State string
