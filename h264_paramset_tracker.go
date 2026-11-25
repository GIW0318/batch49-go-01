// Package task parses H.264 SPS and PPS parameter sets and tracks which set is
// currently active for downstream decoders.
package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Errors returned by the tracker.
var (
	ErrBadSPS      = errors.New("sps payload is invalid")
	ErrBadPPS      = errors.New("pps payload is invalid")
	ErrUnknownPPS  = errors.New("pps is not known")
	ErrUnknownSPS  = errors.New("sps is not known")
	ErrBadSnapshot = errors.New("snapshot payload is invalid")
)

// SPSInfo is the decoded subset of one sequence parameter set.
type SPSInfo struct {
	ID           int
	ProfileIDC   int
	LevelIDC     int
	Width        int
	Height       int
	FrameMBSOnly bool
	NumRefFrames int
	Version      int
}

// PPSInfo is the decoded subset of one picture parameter set.
type PPSInfo struct {
	ID            int
	SPSID         int
	EntropyCoding bool
	Deblocking    bool
	Version       int
}

// ParamSetTracker owns the parsed sets and the activation state.
type ParamSetTracker struct {
	lastWidth  int
	lastHeight int
}

// NewParamSetTracker builds an empty tracker with no active set.
func NewParamSetTracker() *ParamSetTracker {
	return &ParamSetTracker{}
}

// ParseSPS decodes one SPS NAL unit and stores it under its id.
func (t *ParamSetTracker) ParseSPS(nal []byte) (SPSInfo, error) {
	return SPSInfo{}, errNotImplemented
}

// ParsePPS decodes one PPS NAL unit and stores it under its id.
func (t *ParamSetTracker) ParsePPS(nal []byte) (PPSInfo, error) {
	return PPSInfo{}, errNotImplemented
}

// Activate makes the SPS referenced by the given PPS the current parameter set.
func (t *ParamSetTracker) Activate(ppsID int) error {
	return errNotImplemented
}

// ActiveSPS returns the currently active sequence parameter set.
func (t *ParamSetTracker) ActiveSPS() (SPSInfo, bool) {
	return SPSInfo{}, false
}

// ActivePPS returns the currently active picture parameter set.
func (t *ParamSetTracker) ActivePPS() (PPSInfo, bool) {
	return PPSInfo{}, false
}

// ResolutionChanged reports whether the last activation changed the frame size.
func (t *ParamSetTracker) ResolutionChanged() bool {
	return false
}

// SPSVersion reports the stored revision of one SPS id.
func (t *ParamSetTracker) SPSVersion(id int) (int, bool) {
	return 0, false
}

// PPSVersion reports the stored revision of one PPS id.
func (t *ParamSetTracker) PPSVersion(id int) (int, bool) {
	return 0, false
}

// Dimensions returns the active frame size, or zeroes when nothing is active.
func (t *ParamSetTracker) Dimensions() (int, int) {
	return 0, 0
}

// Reset clears every parsed set and the activation state.
func (t *ParamSetTracker) Reset() error {
	return errNotImplemented
}

// Snapshot serialises the parsed sets, versions and activation state.
func (t *ParamSetTracker) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole tracker state from a snapshot, all or nothing.
func (t *ParamSetTracker) Restore(data []byte) error {
	return errNotImplemented
}
