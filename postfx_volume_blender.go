// Package task blends post processing volumes into one set of settings for
// the current camera position.
package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Declared parameter ranges of the blended output.
const (
	SaturationMin = 0.0
	SaturationMax = 2.0
	ExposureMin   = -3.0
	ExposureMax   = 3.0
)

// Tonemap operators understood by the blend.
const (
	TonemapNeutral  = "neutral"
	TonemapAces     = "aces"
	TonemapReinhard = "reinhard"
)

// Errors returned by the blender.
var (
	ErrInvalidVolume  = errors.New("volume shape is invalid")
	ErrVolumeNotFound = errors.New("volume is not registered")
	ErrVolumeLimit    = errors.New("volume limit reached")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// Vec3 is a position in world space.
type Vec3 struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

// VolumeID identifies one registered volume.
type VolumeID int

// PostFxSettings is one complete set of post processing parameters.
type PostFxSettings struct {
	Saturation float64 `json:"saturation"`
	Exposure   float64 `json:"exposure"`
	Bloom      bool    `json:"bloom"`
	Tonemap    string  `json:"tonemap"`
}

// Volume is one post processing volume placed in the world.
type Volume struct {
	ID       VolumeID       `json:"id"`
	Name     string         `json:"name"`
	Center   Vec3           `json:"center"`
	Inner    float64        `json:"inner"`
	Outer    float64        `json:"outer"`
	Priority int            `json:"priority"`
	Enabled  bool           `json:"enabled"`
	Settings PostFxSettings `json:"settings"`
}

// BlendConfig bounds the weight ramp and the volume table.
type BlendConfig struct {
	MaxWeightDelta float64
	MaxVolumes     int
}

// PostFxVolumeBlender owns the volume table, the camera position and the
// per volume weight cache.
type PostFxVolumeBlender struct{}

// DefaultPostFxSettings is the setting set used when no volume has weight.
func DefaultPostFxSettings() PostFxSettings {
	return PostFxSettings{Saturation: 1, Exposure: 0, Bloom: false, Tonemap: TonemapNeutral}
}

// NewPostFxVolumeBlender builds a blender with the given bounds.
func NewPostFxVolumeBlender(cfg BlendConfig) *PostFxVolumeBlender {
	return &PostFxVolumeBlender{}
}

// AddVolume registers one volume and returns its identifier.
func (b *PostFxVolumeBlender) AddVolume(v Volume) (VolumeID, error) {
	return 0, errNotImplemented
}

// UpdateVolume replaces the shape and settings of a registered volume.
func (b *PostFxVolumeBlender) UpdateVolume(id VolumeID, v Volume) error {
	return errNotImplemented
}

// RemoveVolume drops a volume from the table.
func (b *PostFxVolumeBlender) RemoveVolume(id VolumeID) error {
	return errNotImplemented
}

// SetEnabled turns a registered volume on or off.
func (b *PostFxVolumeBlender) SetEnabled(id VolumeID, enabled bool) error {
	return errNotImplemented
}

// SetCamera moves the blend camera, snapping the ramp on a teleport.
func (b *PostFxVolumeBlender) SetCamera(pos Vec3, teleported bool) error {
	return errNotImplemented
}

// AdvanceFrame moves every blend weight one ramp step toward its target and
// returns the new frame number.
func (b *PostFxVolumeBlender) AdvanceFrame() int {
	return 0
}

// Active reports the blended settings that apply to the current frame.
func (b *PostFxVolumeBlender) Active() PostFxSettings {
	return PostFxSettings{}
}

// Weight reports the geometric target weight of one volume.
func (b *PostFxVolumeBlender) Weight(id VolumeID) float64 {
	return 0
}

// BlendWeight reports the ramped weight used by the current blend.
func (b *PostFxVolumeBlender) BlendWeight(id VolumeID) float64 {
	return 0
}

// VolumeCount reports how many volumes are registered.
func (b *PostFxVolumeBlender) VolumeCount() int {
	return 0
}

// Params reads one registered volume.
func (b *PostFxVolumeBlender) Params(id VolumeID) (Volume, bool) {
	return Volume{}, false
}

// Snapshot serialises the volume table, the enable flags, the weights and the
// frame number.
func (b *PostFxVolumeBlender) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole blender state with a snapshot, all or nothing.
func (b *PostFxVolumeBlender) Restore(data []byte) error {
	return errNotImplemented
}
