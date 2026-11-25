// Package task keeps conservative world space bounds for skinned meshes up to
// date after bone animation, under a per frame recompute budget.
package task

import "errors"

// errNotImplemented marks behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Defaults applied when an option field is left at zero.
const (
	DefaultFrameBudget = 32
	DefaultCullMargin  = 0.5
	DefaultMaxMeshes   = 2048
)

// Errors returned by the updater.
var (
	ErrInvalidOptions = errors.New("bounds options are invalid")
	ErrEmptyMesh      = errors.New("mesh id is required")
	ErrDuplicateMesh  = errors.New("mesh is already bound")
	ErrInvalidBounds  = errors.New("bounds are not a valid box")
	ErrUnknownBone    = errors.New("bone is not part of the skeleton")
	ErrUnknownMesh    = errors.New("mesh is not bound")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// Vec3 is a world space position or extent.
type Vec3 struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

// AABB is an axis aligned box.
type AABB struct {
	Min Vec3 `json:"min"`
	Max Vec3 `json:"max"`
}

// BonePose is the local to world transform of one bone, translation and
// per axis scale only.
type BonePose struct {
	Offset Vec3 `json:"offset"`
	Scale  Vec3 `json:"scale"`
}

// InfluenceBox is one bone local box that contributes to a mesh.
type InfluenceBox struct {
	Bone  string `json:"bone"`
	Local AABB   `json:"local"`
}

// InfluenceMap lists the bone local boxes of one mesh and its skeleton.
type InfluenceMap struct {
	SkelID string         `json:"skel_id"`
	Boxes  []InfluenceBox `json:"boxes"`
}

// BoundsOptions bounds the updater.
type BoundsOptions struct {
	FrameBudget int     `json:"frame_budget"`
	CullMargin  float64 `json:"cull_margin"`
	MaxMeshes   int     `json:"max_meshes"`
}

// RecomputeReport summarises one recompute call.
type RecomputeReport struct {
	DirtyBones    int `json:"dirty_bones"`
	DeferredBones int `json:"deferred_bones"`
	Meshes        int `json:"meshes"`
	Skipped       int `json:"skipped"`
}

// SkinnedBoundsUpdater owns the mesh table, the bone poses and the dirty sets.
type SkinnedBoundsUpdater struct {
	opts BoundsOptions
}

// NewSkinnedBoundsUpdater builds an updater with normalised options.
func NewSkinnedBoundsUpdater(opts BoundsOptions) (*SkinnedBoundsUpdater, error) {
	return &SkinnedBoundsUpdater{opts: opts}, nil
}

// Bind attaches one mesh to a skeleton and its bone local boxes.
func (u *SkinnedBoundsUpdater) Bind(meshID string, bindBounds AABB, influence InfluenceMap) error {
	return errNotImplemented
}

// SetPose stores the current pose of one bone without touching the dirty set.
func (u *SkinnedBoundsUpdater) SetPose(skelID, bone string, pose BonePose) error {
	return errNotImplemented
}

// MarkBoneDirty flags one bone for the next recompute.
func (u *SkinnedBoundsUpdater) MarkBoneDirty(skelID, bone string) error {
	return errNotImplemented
}

// Recompute rebuilds the bounds of every mesh of one skeleton that depends on
// a dirty bone, up to the per frame budget.
func (u *SkinnedBoundsUpdater) Recompute(skelID string) RecomputeReport {
	return RecomputeReport{}
}

// Bounds reads the stored bounds of one mesh.
func (u *SkinnedBoundsUpdater) Bounds(meshID string) (AABB, bool) {
	return AABB{}, false
}

// CullBounds reads the bounds used by culling for one mesh.
func (u *SkinnedBoundsUpdater) CullBounds(meshID string) (AABB, bool) {
	return AABB{}, false
}

// StaleCount reports how many meshes carry stale bounds.
func (u *SkinnedBoundsUpdater) StaleCount() int {
	return 0
}

// BeginFrame resets the per frame recompute counters.
func (u *SkinnedBoundsUpdater) BeginFrame() {
}

// MeshCount reports how many meshes are bound.
func (u *SkinnedBoundsUpdater) MeshCount() int {
	return 0
}

// Snapshot serialises meshes, poses, dirty sets and stale flags.
func (u *SkinnedBoundsUpdater) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (u *SkinnedBoundsUpdater) Restore(data []byte) error {
	return errNotImplemented
}
