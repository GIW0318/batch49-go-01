// Package task composites post processing results through single channel masks
// and reports per effect statistics.
package task

import "errors"

// errNotImplemented marks behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Mask source kinds.
const (
	MaskFromDepth    = "depth"
	MaskFromMaterial = "material"
	MaskFromPainted  = "painted"
)

// Supported limits.
const (
	MaxFeatherPixels = 64
	MaxStrength      = 1.0
)

// Errors returned by the compositor.
var (
	ErrInvalidSize     = errors.New("width and height must be positive")
	ErrInvalidImage    = errors.New("image buffer does not match its dimensions")
	ErrInvalidMask     = errors.New("mask reference is invalid")
	ErrInvalidStrength = errors.New("strength must be within 0 and 1")
	ErrInvalidFeather  = errors.New("feather must be within 0 and MaxFeatherPixels")
	ErrUnknownEffect   = errors.New("effect is not declared")
	ErrDuplicateEffect = errors.New("effect is already declared")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// Vec4 is one linear colour sample.
type Vec4 struct {
	R float32 `json:"r"`
	G float32 `json:"g"`
	B float32 `json:"b"`
	A float32 `json:"a"`
}

// Image is a linear colour target.
type Image struct {
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Pixels []Vec4 `json:"pixels"`
}

// MaskRef is a single channel mask bound to one effect.
type MaskRef struct {
	ID     string    `json:"id"`
	Kind   string    `json:"kind"`
	Width  int       `json:"width"`
	Height int       `json:"height"`
	Data   []float32 `json:"data"`
}

// Stats describes one composite pass.
type Stats struct {
	EffectID  string  `json:"effect_id"`
	Pixels    int     `json:"pixels"`
	Written   int     `json:"written"`
	Feather   int     `json:"feather"`
	Strength  float64 `json:"strength"`
	Resampled bool    `json:"resampled"`
	MaskZero  bool    `json:"mask_zero"`
	Skipped   bool    `json:"skipped"`
}

// PostFxMaskCompositor owns the effect chain, the colour targets and the
// single channel masks.
type PostFxMaskCompositor struct {
	width  int
	height int
}

// NewPostFxMaskCompositor builds a compositor for one render target size.
func NewPostFxMaskCompositor(width, height int) (*PostFxMaskCompositor, error) {
	return &PostFxMaskCompositor{width: width, height: height}, nil
}

// SetSource replaces the untouched frame used as the blend base.
func (c *PostFxMaskCompositor) SetSource(img Image) error {
	return errNotImplemented
}

// DeclareEffect appends an effect to the declaration order.
func (c *PostFxMaskCompositor) DeclareEffect(effectID string) error {
	return errNotImplemented
}

// SubmitRendered stores the processed image for one effect.
func (c *PostFxMaskCompositor) SubmitRendered(effectID string, img Image) error {
	return errNotImplemented
}

// BindMask binds a single channel mask to one effect.
func (c *PostFxMaskCompositor) BindMask(effectID string, mask MaskRef) error {
	return errNotImplemented
}

// SetFeather sets the feather radius in pixels for one effect.
func (c *PostFxMaskCompositor) SetFeather(effectID string, px int) error {
	return errNotImplemented
}

// SetStrength sets the blend strength in the closed range 0..1.
func (c *PostFxMaskCompositor) SetStrength(effectID string, v float64) error {
	return errNotImplemented
}

// Composite blends one effect over the current output.
func (c *PostFxMaskCompositor) Composite(effectID string) (Stats, error) {
	return Stats{}, errNotImplemented
}

// CompositeAll blends every declared effect in declaration order.
func (c *PostFxMaskCompositor) CompositeAll() ([]Stats, error) {
	return nil, errNotImplemented
}

// EffectOrder lists the declared effect identifiers.
func (c *PostFxMaskCompositor) EffectOrder() []string {
	return nil
}

// Output returns the current colour target.
func (c *PostFxMaskCompositor) Output() Image {
	return Image{}
}

// MaskResolution reports the bound mask size of one effect.
func (c *PostFxMaskCompositor) MaskResolution(effectID string) (int, int) {
	return 0, 0
}

// Snapshot serialises the whole compositor state.
func (c *PostFxMaskCompositor) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (c *PostFxMaskCompositor) Restore(data []byte) error {
	return errNotImplemented
}
