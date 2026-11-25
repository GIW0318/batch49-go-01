// Package task blends post processing volumes into one set of settings for
// the current camera position.
package task

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"sort"
	"strings"
	"sync"
)

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

const (
	blobFormat  = "gsb-postfx-volume-blender"
	blobVersion = 1
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

type volumeEntry struct {
	volume Volume
	target float64
	blend  float64
}

// PostFxVolumeBlender owns the volume table, the camera position and the
// per volume weight cache.
type PostFxVolumeBlender struct {
	mu      sync.Mutex
	cfg     BlendConfig
	volumes map[VolumeID]*volumeEntry
	nextID  VolumeID
	camera  Vec3
	frame   int
}

// DefaultPostFxSettings is the setting set used when no volume has weight.
func DefaultPostFxSettings() PostFxSettings {
	return PostFxSettings{Saturation: 1, Exposure: 0, Bloom: false, Tonemap: TonemapNeutral}
}

// NewPostFxVolumeBlender builds a blender with the given bounds.
func NewPostFxVolumeBlender(cfg BlendConfig) *PostFxVolumeBlender {
	if math.IsNaN(cfg.MaxWeightDelta) || cfg.MaxWeightDelta < 0 {
		cfg.MaxWeightDelta = 0
	}
	return &PostFxVolumeBlender{
		cfg:     cfg,
		volumes: make(map[VolumeID]*volumeEntry),
		nextID:  1,
	}
}

// AddVolume registers one volume and returns its identifier.
func (b *PostFxVolumeBlender) AddVolume(v Volume) (VolumeID, error) {
	if err := validateVolume(v); err != nil {
		return 0, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.cfg.MaxVolumes > 0 && len(b.volumes) >= b.cfg.MaxVolumes {
		return 0, ErrVolumeLimit
	}
	id := b.nextID
	b.nextID++
	v.ID = id
	b.volumes[id] = &volumeEntry{
		volume: v,
		target: targetWeight(v, b.camera),
	}
	return id, nil
}

// UpdateVolume replaces the shape and settings of a registered volume.
func (b *PostFxVolumeBlender) UpdateVolume(id VolumeID, v Volume) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	e, ok := b.volumes[id]
	if !ok {
		return ErrVolumeNotFound
	}
	if err := validateVolume(v); err != nil {
		return err
	}
	v.ID = id
	e.volume = v
	e.target = targetWeight(v, b.camera)
	if !v.Enabled {
		e.target = 0
		e.blend = 0
	}
	return nil
}

// RemoveVolume drops a volume from the table.
func (b *PostFxVolumeBlender) RemoveVolume(id VolumeID) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if _, ok := b.volumes[id]; !ok {
		return ErrVolumeNotFound
	}
	delete(b.volumes, id)

	for _, e := range b.volumes {
		e.target = targetWeight(e.volume, b.camera)
		if !e.volume.Enabled {
			e.target = 0
			e.blend = 0
		}
	}
	return nil
}

// SetEnabled turns a registered volume on or off.
func (b *PostFxVolumeBlender) SetEnabled(id VolumeID, enabled bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	e, ok := b.volumes[id]
	if !ok {
		return ErrVolumeNotFound
	}
	e.volume.Enabled = enabled
	if !enabled {
		e.target = 0
		e.blend = 0
	} else {
		e.target = targetWeight(e.volume, b.camera)
	}
	return nil
}

// SetCamera moves the blend camera, snapping the ramp on a teleport.
func (b *PostFxVolumeBlender) SetCamera(pos Vec3, teleported bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.camera = pos
	for _, e := range b.volumes {
		e.target = targetWeight(e.volume, b.camera)
		if !e.volume.Enabled {
			e.target = 0
			e.blend = 0
			continue
		}
		if teleported {
			e.blend = e.target
		}
	}
	return nil
}

// AdvanceFrame moves every blend weight one ramp step toward its target and
// returns the new frame number.
func (b *PostFxVolumeBlender) AdvanceFrame() int {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.frame++
	step := b.cfg.MaxWeightDelta
	if math.IsNaN(step) || step < 0 {
		step = 0
	}
	for _, e := range b.volumes {
		e.target = targetWeight(e.volume, b.camera)
		if !e.volume.Enabled {
			e.target = 0
			e.blend = 0
			continue
		}
		switch {
		case e.blend < e.target:
			e.blend += step
			if e.blend > e.target || math.IsInf(e.blend, 0) {
				e.blend = e.target
			}
		case e.blend > e.target:
			e.blend -= step
			if e.blend < e.target || math.IsInf(e.blend, 0) {
				e.blend = e.target
			}
		}
		e.blend = clampUnit(e.blend)
	}
	return b.frame
}

// Active reports the blended settings that apply to the current frame.
func (b *PostFxVolumeBlender) Active() PostFxSettings {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.activeLocked()
}

func (b *PostFxVolumeBlender) activeLocked() PostFxSettings {
	ids := b.sortedIDsLocked()

	var total float64
	var saturation float64
	var exposure float64
	var best *volumeEntry
	var bestID VolumeID
	for _, id := range ids {
		e := b.volumes[id]
		w := e.blend
		if w <= 0 {
			continue
		}
		total += w
		saturation += e.volume.Settings.Saturation * w
		exposure += e.volume.Settings.Exposure * w
		if best == nil || w > best.blend || (w == best.blend && id < bestID) {
			best = e
			bestID = id
		}
	}

	if best == nil || total <= 0 {
		return DefaultPostFxSettings()
	}
	return PostFxSettings{
		Saturation: clamp(saturation/total, SaturationMin, SaturationMax),
		Exposure:   clamp(exposure/total, ExposureMin, ExposureMax),
		Bloom:      best.volume.Settings.Bloom,
		Tonemap:    best.volume.Settings.Tonemap,
	}
}

// Weight reports the geometric target weight of one volume.
func (b *PostFxVolumeBlender) Weight(id VolumeID) float64 {
	b.mu.Lock()
	defer b.mu.Unlock()

	e, ok := b.volumes[id]
	if !ok {
		return 0
	}
	return targetWeight(e.volume, b.camera)
}

// BlendWeight reports the ramped weight used by the current blend.
func (b *PostFxVolumeBlender) BlendWeight(id VolumeID) float64 {
	b.mu.Lock()
	defer b.mu.Unlock()

	e, ok := b.volumes[id]
	if !ok {
		return 0
	}
	return e.blend
}

// VolumeCount reports how many volumes are registered.
func (b *PostFxVolumeBlender) VolumeCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.volumes)
}

// Params reads one registered volume.
func (b *PostFxVolumeBlender) Params(id VolumeID) (Volume, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	e, ok := b.volumes[id]
	if !ok {
		return Volume{}, false
	}
	return e.volume, true
}

func (b *PostFxVolumeBlender) sortedIDsLocked() []VolumeID {
	ids := make([]VolumeID, 0, len(b.volumes))
	for id := range b.volumes {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

type blobVolume struct {
	Volume Volume  `json:"volume"`
	Target float64 `json:"target"`
	Blend  float64 `json:"blend"`
}

type blobState struct {
	Version int          `json:"version"`
	Frame   int          `json:"frame"`
	NextID  VolumeID     `json:"next_id"`
	Camera  Vec3         `json:"camera"`
	Volumes []blobVolume `json:"volumes"`
}

type blobEnvelope struct {
	Format   string `json:"format"`
	Checksum string `json:"checksum"`
	Payload  []byte `json:"payload"`
}

// Snapshot serialises the volume table, the enable flags, the weights and the
// frame number.
func (b *PostFxVolumeBlender) Snapshot() ([]byte, error) {
	b.mu.Lock()
	ids := b.sortedIDsLocked()
	state := blobState{
		Version: blobVersion,
		Frame:   b.frame,
		NextID:  b.nextID,
		Camera:  b.camera,
		Volumes: make([]blobVolume, 0, len(ids)),
	}
	for _, id := range ids {
		e := b.volumes[id]
		state.Volumes = append(state.Volumes, blobVolume{
			Volume: e.volume,
			Target: e.target,
			Blend:  e.blend,
		})
	}
	b.mu.Unlock()

	payload, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(payload)
	envelope := blobEnvelope{
		Format:   blobFormat,
		Checksum: hex.EncodeToString(sum[:]),
		Payload:  payload,
	}
	return json.Marshal(envelope)
}

// Restore replaces the whole blender state with a snapshot, all or nothing.
func (b *PostFxVolumeBlender) Restore(data []byte) error {
	var envelope blobEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return ErrBadSnapshot
	}
	if envelope.Format != blobFormat || envelope.Checksum == "" || len(envelope.Payload) == 0 {
		return ErrBadSnapshot
	}
	sum := sha256.Sum256(envelope.Payload)
	if !strings.EqualFold(envelope.Checksum, hex.EncodeToString(sum[:])) {
		return ErrBadSnapshot
	}

	var state blobState
	dec := json.NewDecoder(bytes.NewReader(envelope.Payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&state); err != nil {
		return ErrBadSnapshot
	}
	if err := ensureEOF(dec); err != nil {
		return ErrBadSnapshot
	}
	if state.Version != blobVersion || state.Frame < 0 || state.NextID < 1 {
		return ErrBadSnapshot
	}
	if !finiteVec(state.Camera) {
		return ErrBadSnapshot
	}

	volumes := make(map[VolumeID]*volumeEntry, len(state.Volumes))
	maxID := VolumeID(0)
	for _, item := range state.Volumes {
		v := item.Volume
		if err := validateVolume(v); err != nil {
			return ErrBadSnapshot
		}
		if v.ID < 1 {
			return ErrBadSnapshot
		}
		if _, exists := volumes[v.ID]; exists {
			return ErrBadSnapshot
		}
		if !finite01(item.Target) || !finite01(item.Blend) {
			return ErrBadSnapshot
		}
		if !v.Enabled && (item.Target != 0 || item.Blend != 0) {
			return ErrBadSnapshot
		}
		if v.ID > maxID {
			maxID = v.ID
		}
		volumes[v.ID] = &volumeEntry{
			volume: v,
			target: item.Target,
			blend:  item.Blend,
		}
	}
	if maxID >= state.NextID {
		return ErrBadSnapshot
	}

	b.mu.Lock()
	if b.cfg.MaxVolumes > 0 && len(volumes) > b.cfg.MaxVolumes {
		b.mu.Unlock()
		return ErrBadSnapshot
	}
	b.volumes = volumes
	b.nextID = state.NextID
	b.frame = state.Frame
	b.camera = state.Camera
	b.mu.Unlock()
	return nil
}

func validateVolume(v Volume) error {
	if strings.TrimSpace(v.Name) == "" {
		return ErrInvalidVolume
	}
	if !finite(v.Inner) || v.Inner < 0 {
		return ErrInvalidVolume
	}
	if !finite(v.Outer) || v.Outer <= v.Inner {
		return ErrInvalidVolume
	}
	if !finiteVec(v.Center) || !finite(v.Settings.Saturation) || !finite(v.Settings.Exposure) {
		return ErrInvalidVolume
	}
	return nil
}

func targetWeight(v Volume, camera Vec3) float64 {
	if !v.Enabled {
		return 0
	}
	dx := camera.X - v.Center.X
	dy := camera.Y - v.Center.Y
	dz := camera.Z - v.Center.Z
	d := math.Sqrt(dx*dx + dy*dy + dz*dz)
	if d <= v.Inner {
		return 1
	}
	if d >= v.Outer {
		return 0
	}
	w := 1 - (d-v.Inner)/(v.Outer-v.Inner)
	return clampUnit(w)
}

func finite(x float64) bool {
	return !math.IsNaN(x) && !math.IsInf(x, 0)
}

func finiteVec(v Vec3) bool {
	return finite(v.X) && finite(v.Y) && finite(v.Z)
}

func finite01(x float64) bool {
	return finite(x) && x >= 0 && x <= 1
}

func clamp(x, lo, hi float64) float64 {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	if math.IsNaN(x) {
		return lo
	}
	return x
}

func clampUnit(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	if math.IsNaN(x) {
		return 0
	}
	return x
}

func ensureEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return err
	}
	return nil
}
