// Package task composites post processing results through single channel masks
// and reports per effect statistics.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"
)

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

	// MaxTargetDimension caps either edge of a colour target.
	MaxTargetDimension = 8192
	// MaxTargetPixels caps the total colour target resolution.
	MaxTargetPixels = 4096 * 4096
	// MaxEffects caps the declaration chain and the stats table.
	MaxEffects = 256
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

	errTooManyEffects = errors.New("effect limit reached")
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

// effectState is the per declaration configuration and retained resources.
type effectState struct {
	ID       string
	Feather  int
	Strength float64
	Mask     *MaskRef
	Rendered *Image
}

// PostFxMaskCompositor owns the effect chain, the colour targets and the
// single channel masks.
type PostFxMaskCompositor struct {
	mu      sync.RWMutex
	width   int
	height  int
	source  []Vec4
	output  []Vec4
	order   []string
	effects map[string]*effectState
	stats   []Stats
}

// NewPostFxMaskCompositor builds a compositor for one render target size.
func NewPostFxMaskCompositor(width, height int) (*PostFxMaskCompositor, error) {
	if width <= 0 || height <= 0 {
		return nil, ErrInvalidSize
	}
	if width > MaxTargetDimension || height > MaxTargetDimension || width > MaxTargetPixels/height {
		return nil, fmt.Errorf("%w: %dx%d exceeds the hard target limit", ErrInvalidSize, width, height)
	}
	pixelCount := width * height
	return &PostFxMaskCompositor{
		width:   width,
		height:  height,
		source:  make([]Vec4, pixelCount),
		output:  make([]Vec4, pixelCount),
		order:   make([]string, 0, 8),
		effects: make(map[string]*effectState),
		stats:   make([]Stats, 0, 8),
	}, nil
}

func (c *PostFxMaskCompositor) pixelCount() int { return c.width * c.height }

// validateImageLocked checks an image against the fixed colour target.
func (c *PostFxMaskCompositor) validateImageLocked(img Image) error {
	if img.Width != c.width || img.Height != c.height || len(img.Pixels) != c.pixelCount() {
		return ErrInvalidImage
	}
	return nil
}

// SetSource replaces the untouched frame used as the blend base.
func (c *PostFxMaskCompositor) SetSource(img Image) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.validateImageLocked(img); err != nil {
		return err
	}
	c.source = cloneVec4(img.Pixels)
	c.output = cloneVec4(img.Pixels)
	return nil
}

// DeclareEffect appends an effect to the declaration order.
func (c *PostFxMaskCompositor) DeclareEffect(effectID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.effects[effectID]; exists {
		return ErrDuplicateEffect
	}
	if len(c.order) >= MaxEffects {
		return fmt.Errorf("%w: at most %d effects", errTooManyEffects, MaxEffects)
	}
	state := &effectState{ID: effectID, Strength: 1.0}
	c.effects[effectID] = state
	c.order = append(c.order, effectID)
	c.stats = append(c.stats, Stats{EffectID: effectID, Strength: 1.0})
	return nil
}

// SubmitRendered stores the processed image for one effect.
func (c *PostFxMaskCompositor) SubmitRendered(effectID string, img Image) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	effect, err := c.effectLocked(effectID)
	if err != nil {
		return err
	}
	if err := c.validateImageLocked(img); err != nil {
		return err
	}
	effect.Rendered = &Image{
		Width:  img.Width,
		Height: img.Height,
		Pixels: cloneVec4(img.Pixels),
	}
	return nil
}

// BindMask binds a single channel mask to one effect. The bound mask is
// retained across frames so a culled material keeps compositing with its
// previous mask instead of collapsing to full strength.
func (c *PostFxMaskCompositor) BindMask(effectID string, mask MaskRef) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	effect, err := c.effectLocked(effectID)
	if err != nil {
		return err
	}
	if mask.ID == "" || mask.Width <= 0 || mask.Height <= 0 ||
		len(mask.Data) != mask.Width*mask.Height {
		return ErrInvalidMask
	}
	effect.Mask = &MaskRef{
		ID:     mask.ID,
		Kind:   mask.Kind,
		Width:  mask.Width,
		Height: mask.Height,
		Data:   cloneFloat32(mask.Data),
	}
	return nil
}

// SetFeather sets the feather radius in pixels for one effect.
func (c *PostFxMaskCompositor) SetFeather(effectID string, px int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	effect, err := c.effectLocked(effectID)
	if err != nil {
		return err
	}
	if px < 0 || px > MaxFeatherPixels {
		return ErrInvalidFeather
	}
	effect.Feather = px
	return nil
}

// SetStrength sets the blend strength in the closed range 0..1.
func (c *PostFxMaskCompositor) SetStrength(effectID string, v float64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	effect, err := c.effectLocked(effectID)
	if err != nil {
		return err
	}
	if math.IsNaN(v) || v < 0 || v > MaxStrength {
		return ErrInvalidStrength
	}
	effect.Strength = v
	return nil
}

func (c *PostFxMaskCompositor) effectLocked(effectID string) (*effectState, error) {
	effect, ok := c.effects[effectID]
	if !ok {
		return nil, ErrUnknownEffect
	}
	return effect, nil
}

// Composite blends one effect over the current output.
func (c *PostFxMaskCompositor) Composite(effectID string) (Stats, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	effect, err := c.effectLocked(effectID)
	if err != nil {
		return Stats{EffectID: effectID}, err
	}
	stats := c.compositeEffectLocked(effect)
	c.storeStatsLocked(stats)
	return stats, nil
}

// CompositeAll blends every declared effect in declaration order. A fresh
// frame starts from the untouched source before the chain is applied.
func (c *PostFxMaskCompositor) CompositeAll() ([]Stats, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.output = cloneVec4(c.source)
	results := make([]Stats, 0, len(c.order))
	for _, id := range c.order {
		effect := c.effects[id]
		if effect == nil {
			return nil, ErrUnknownEffect
		}
		stats := c.compositeEffectLocked(effect)
		results = append(results, stats)
	}
	c.stats = append([]Stats(nil), results...)
	return results, nil
}

// EffectOrder lists the declared effect identifiers.
func (c *PostFxMaskCompositor) EffectOrder() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]string(nil), c.order...)
}

// Output returns a copy of the current colour target.
func (c *PostFxMaskCompositor) Output() Image {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return Image{Width: c.width, Height: c.height, Pixels: cloneVec4(c.output)}
}

// MaskResolution reports the bound mask size of one effect.
func (c *PostFxMaskCompositor) MaskResolution(effectID string) (int, int) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	effect, ok := c.effects[effectID]
	if !ok || effect.Mask == nil {
		return 0, 0
	}
	return effect.Mask.Width, effect.Mask.Height
}

// sampleMaskLocked returns the mask resampled onto the colour target using
// pixel-centre nearest neighbour sampling.
func (c *PostFxMaskCompositor) sampleMaskLocked(effect *effectState) (mask []float32, resampled bool, allZero bool) {
	n := c.pixelCount()
	mask = make([]float32, n)
	if effect.Mask == nil {
		for i := range mask {
			mask[i] = 1
		}
		return mask, false, false
	}

	ref := effect.Mask
	mw, mh := ref.Width, ref.Height
	resampled = mw != c.width || mh != c.height
	allZero = true

	for y := 0; y < c.height; y++ {
		sy := (2*y + 1) * mh / (2 * c.height)
		if sy < 0 {
			sy = 0
		} else if sy >= mh {
			sy = mh - 1
		}
		rowBase := sy * mw
		for x := 0; x < c.width; x++ {
			sx := (2*x + 1) * mw / (2 * c.width)
			if sx < 0 {
				sx = 0
			} else if sx >= mw {
				sx = mw - 1
			}
			v := ref.Data[rowBase+sx]
			if math.IsNaN(float64(v)) {
				v = 0
			} else if v < 0 {
				v = 0
			} else if v > 1 {
				v = 1
			}
			mask[y*c.width+x] = v
			if v != 0 {
				allZero = false
			}
		}
	}
	return mask, resampled, allZero
}

// featherMask applies the radius only to edge pixels where the mask value
// changes. Image borders and flat interiors keep their exact value.
func (c *PostFxMaskCompositor) featherMask(mask []float32, radius int) {
	if radius <= 0 {
		return
	}
	w, h := c.width, c.height
	original := append([]float32(nil), mask...)
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			idx := y*w + x
			if !isEdgePixel(original, w, h, x, y) {
				continue
			}
			mask[idx] = windowAverage(original, w, h, x, y, radius)
		}
	}
}

func isEdgePixel(mask []float32, w, h, x, y int) bool {
	v := mask[y*w+x]
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			if dx == 0 && dy == 0 {
				continue
			}
			nx, ny := x+dx, y+dy
			if nx < 0 || ny < 0 || nx >= w || ny >= h {
				continue
			}
			if mask[ny*w+nx] != v {
				return true
			}
		}
	}
	return false
}

func windowAverage(mask []float32, w, h, cx, cy, radius int) float32 {
	x0 := cx - radius
	if x0 < 0 {
		x0 = 0
	}
	x1 := cx + radius
	if x1 >= w {
		x1 = w - 1
	}
	y0 := cy - radius
	if y0 < 0 {
		y0 = 0
	}
	y1 := cy + radius
	if y1 >= h {
		y1 = h - 1
	}
	var sum float64
	var count int
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			sum += float64(mask[y*w+x])
			count++
		}
	}
	if count == 0 {
		return mask[cy*w+cx]
	}
	return float32(sum / float64(count))
}

// compositeEffectLocked blends one effect onto c.output and returns its stats.
func (c *PostFxMaskCompositor) compositeEffectLocked(effect *effectState) Stats {
	stats := Stats{
		EffectID: effect.ID,
		Feather:  effect.Feather,
		Strength: effect.Strength,
		Pixels:   c.pixelCount(),
	}

	if effect.Strength <= 0 {
		stats.Skipped = true
		return stats
	}

	mask, resampled, allZero := c.sampleMaskLocked(effect)
	stats.Resampled = resampled
	stats.MaskZero = allZero
	if allZero {
		return stats
	}

	c.featherMask(mask, effect.Feather)

	rendered := c.output
	if effect.Rendered != nil {
		rendered = effect.Rendered.Pixels
	}

	strength := effect.Strength
	pixels := c.pixelCount()
	for i := 0; i < pixels; i++ {
		m := float64(mask[i]) * strength
		if m <= 0 {
			continue
		}
		if m > 1 {
			m = 1
		}
		base := c.output[i]
		over := rendered[i]
		c.output[i] = Vec4{
			R: blendFloat32(base.R, over.R, m),
			G: blendFloat32(base.G, over.G, m),
			B: blendFloat32(base.B, over.B, m),
			A: blendFloat32(base.A, over.A, m),
		}
		stats.Written++
	}
	return stats
}

func blendFloat32(base, over float32, m float64) float32 {
	return float32(float64(base)*(1-m) + float64(over)*m)
}

func (c *PostFxMaskCompositor) storeStatsLocked(stats Stats) {
	for i := range c.stats {
		if c.stats[i].EffectID == stats.EffectID {
			c.stats[i] = stats
			return
		}
	}
	if len(c.stats) < MaxEffects {
		c.stats = append(c.stats, stats)
	}
}

// snapshotDTO is the serialisable compositor state.
type snapshotDTO struct {
	Magic   string      `json:"magic"`
	Version int         `json:"version"`
	Width   int         `json:"width"`
	Height  int         `json:"height"`
	Source  []Vec4      `json:"source"`
	Output  []Vec4      `json:"output"`
	Order   []string    `json:"order"`
	Effects []effectDTO `json:"effects"`
}

type effectDTO struct {
	ID       string   `json:"id"`
	Feather  int      `json:"feather"`
	Strength float64  `json:"strength"`
	Mask     *MaskRef `json:"mask,omitempty"`
	Rendered *Image   `json:"rendered,omitempty"`
}

type snapshotEnvelope struct {
	Body     json.RawMessage `json:"body"`
	Checksum string          `json:"checksum"`
}

const snapshotMagic = "postfx-mask-compositor"
const snapshotVersion = 1

// Snapshot serialises the whole compositor state.
func (c *PostFxMaskCompositor) Snapshot() ([]byte, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	dto := snapshotDTO{
		Magic:   snapshotMagic,
		Version: snapshotVersion,
		Width:   c.width,
		Height:  c.height,
		Source:  cloneVec4(c.source),
		Output:  cloneVec4(c.output),
		Order:   append([]string(nil), c.order...),
		Effects: make([]effectDTO, 0, len(c.order)),
	}
	for _, id := range c.order {
		effect := c.effects[id]
		if effect == nil {
			continue
		}
		entry := effectDTO{ID: effect.ID, Feather: effect.Feather, Strength: effect.Strength}
		if effect.Mask != nil {
			mask := *effect.Mask
			mask.Data = cloneFloat32(effect.Mask.Data)
			entry.Mask = &mask
		}
		if effect.Rendered != nil {
			img := *effect.Rendered
			img.Pixels = cloneVec4(effect.Rendered.Pixels)
			entry.Rendered = &img
		}
		dto.Effects = append(dto.Effects, entry)
	}

	body, err := json.Marshal(dto)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	envelope := snapshotEnvelope{Body: body, Checksum: hex.EncodeToString(sum[:])}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	return payload, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (c *PostFxMaskCompositor) Restore(data []byte) error {
	dto, err := decodeSnapshot(data)
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	effects := make(map[string]*effectState, len(dto.Effects))
	order := make([]string, 0, len(dto.Order))
	stats := make([]Stats, 0, len(dto.Effects))
	for _, entry := range dto.Effects {
		if entry.ID == "" {
			return ErrBadSnapshot
		}
		if _, dup := effects[entry.ID]; dup {
			return ErrBadSnapshot
		}
		if entry.Feather < 0 || entry.Feather > MaxFeatherPixels {
			return ErrBadSnapshot
		}
		if math.IsNaN(entry.Strength) || entry.Strength < 0 || entry.Strength > MaxStrength {
			return ErrBadSnapshot
		}
		state := &effectState{ID: entry.ID, Feather: entry.Feather, Strength: entry.Strength}
		if entry.Mask != nil {
			mask := entry.Mask
			if mask.ID == "" || mask.Width <= 0 || mask.Height <= 0 ||
				len(mask.Data) != mask.Width*mask.Height {
				return ErrBadSnapshot
			}
			state.Mask = &MaskRef{
				ID:     mask.ID,
				Kind:   mask.Kind,
				Width:  mask.Width,
				Height: mask.Height,
				Data:   cloneFloat32(mask.Data),
			}
		}
		if entry.Rendered != nil {
			img := entry.Rendered
			if img.Width != dto.Width || img.Height != dto.Height ||
				len(img.Pixels) != dto.Width*dto.Height {
				return ErrBadSnapshot
			}
			state.Rendered = &Image{Width: img.Width, Height: img.Height, Pixels: cloneVec4(img.Pixels)}
		}
		effects[entry.ID] = state
		order = append(order, entry.ID)
		stats = append(stats, Stats{EffectID: entry.ID, Feather: entry.Feather, Strength: entry.Strength})
	}

	// The declared order must match the effect entries exactly.
	if len(order) != len(dto.Order) {
		return ErrBadSnapshot
	}
	for i, id := range dto.Order {
		if order[i] != id {
			return ErrBadSnapshot
		}
	}

	n := dto.Width * dto.Height
	source := make([]Vec4, n)
	output := make([]Vec4, n)
	if len(dto.Source) != n || len(dto.Output) != n {
		return ErrBadSnapshot
	}
	copy(source, dto.Source)
	copy(output, dto.Output)

	c.width = dto.Width
	c.height = dto.Height
	c.source = source
	c.output = output
	c.order = order
	c.effects = effects
	c.stats = stats
	return nil
}

func decodeSnapshot(data []byte) (snapshotDTO, error) {
	var envelope snapshotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return snapshotDTO{}, ErrBadSnapshot
	}
	if len(envelope.Body) == 0 || envelope.Checksum == "" {
		return snapshotDTO{}, ErrBadSnapshot
	}
	sum := sha256.Sum256(envelope.Body)
	if hex.EncodeToString(sum[:]) != envelope.Checksum {
		return snapshotDTO{}, ErrBadSnapshot
	}
	var dto snapshotDTO
	if err := json.Unmarshal(envelope.Body, &dto); err != nil {
		return snapshotDTO{}, ErrBadSnapshot
	}
	if dto.Magic != snapshotMagic || dto.Version != snapshotVersion {
		return snapshotDTO{}, ErrBadSnapshot
	}
	if dto.Width <= 0 || dto.Height <= 0 ||
		dto.Width > MaxTargetDimension || dto.Height > MaxTargetDimension ||
		dto.Width > MaxTargetPixels/dto.Height {
		return snapshotDTO{}, ErrBadSnapshot
	}
	n := dto.Width * dto.Height
	if len(dto.Source) != n || len(dto.Output) != n {
		return snapshotDTO{}, ErrBadSnapshot
	}
	if len(dto.Order) > MaxEffects || len(dto.Effects) != len(dto.Order) {
		return snapshotDTO{}, ErrBadSnapshot
	}
	seen := make(map[string]bool, len(dto.Order))
	for _, id := range dto.Order {
		if id == "" || seen[id] {
			return snapshotDTO{}, ErrBadSnapshot
		}
		seen[id] = true
	}
	for _, entry := range dto.Effects {
		if entry.ID == "" || !seen[entry.ID] {
			return snapshotDTO{}, ErrBadSnapshot
		}
	}
	return dto, nil
}

func cloneVec4(in []Vec4) []Vec4 {
	if in == nil {
		return nil
	}
	out := make([]Vec4, len(in))
	copy(out, in)
	return out
}

func cloneFloat32(in []float32) []float32 {
	if in == nil {
		return nil
	}
	out := make([]float32, len(in))
	copy(out, in)
	return out
}
