// Package task stores named mixer scenes, ramps parameters between values and
// recalls scenes partially without losing unsaved edits.
package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Parameter names accepted by SetParam and Recall.
const (
	ParamVolume = "volume"
	ParamPan    = "pan"
	ParamSend   = "send"
	ParamFX     = "fx"
)

// Errors returned by the scene engine.
var (
	ErrUnknownTrack   = errors.New("unknown track")
	ErrUnknownParam   = errors.New("unknown parameter")
	ErrOutOfRange     = errors.New("parameter value out of range")
	ErrUnknownScene   = errors.New("unknown scene")
	ErrBlankName      = errors.New("scene name is required")
	ErrDuplicateTrack = errors.New("track already registered")
	ErrBadRamp        = errors.New("ramp duration out of range")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// Version identifies one saved revision of a scene.
type Version struct {
	Number int
	Name   string
}

// RecallReport describes what one Recall call touched.
type RecallReport struct {
	Scene   string
	Version int
	Fields  []string
	Jumped  []string
}

// MixerSceneRecall owns the track values, named scenes and running ramps.
type MixerSceneRecall struct {
	rampMs      int
	nextVersion int
	dirty       bool
}

// NewMixerSceneRecall builds an empty engine with no tracks and a zero ramp.
func NewMixerSceneRecall() *MixerSceneRecall {
	return &MixerSceneRecall{}
}

// AddTrack registers one track id so parameters can be set on it.
func (m *MixerSceneRecall) AddTrack(trackID string) error {
	return errNotImplemented
}

// SetTrackEnabled toggles whether a track follows parameter ramps.
func (m *MixerSceneRecall) SetTrackEnabled(trackID string, enabled bool) error {
	return errNotImplemented
}

// SetRamp sets the ramp length in milliseconds used by later recalls.
func (m *MixerSceneRecall) SetRamp(ms int) error {
	return errNotImplemented
}

// SetParam writes one parameter target on a registered track.
func (m *MixerSceneRecall) SetParam(trackID, param string, v float64) error {
	return errNotImplemented
}

// Value reads the current (possibly interpolated) parameter value.
func (m *MixerSceneRecall) Value(trackID, param string) float64 {
	return 0
}

// Save stores the target values of every track under a name.
func (m *MixerSceneRecall) Save(name string) (Version, error) {
	return Version{}, errNotImplemented
}

// Recall restores a saved scene, optionally limited to the listed fields.
func (m *MixerSceneRecall) Recall(name string, fields []string) (RecallReport, error) {
	return RecallReport{}, errNotImplemented
}

// Advance moves every running ramp forward by the given milliseconds.
func (m *MixerSceneRecall) Advance(ms int) {
}

// Dirty reports whether parameters changed since the last save.
func (m *MixerSceneRecall) Dirty() bool {
	return false
}

// Version looks up the latest saved revision of a scene.
func (m *MixerSceneRecall) Version(name string) (Version, bool) {
	return Version{}, false
}

// Scenes lists saved scene names in ascending order.
func (m *MixerSceneRecall) Scenes() []string {
	return nil
}

// Snapshot serialises scenes, versions, targets and running ramps.
func (m *MixerSceneRecall) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole engine state from a snapshot, all or nothing.
func (m *MixerSceneRecall) Restore(data []byte) error {
	return errNotImplemented
}
