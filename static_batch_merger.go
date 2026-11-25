// Package task merges static sub-meshes into batches per group, bakes world
// transforms into the vertices and keeps the sub-mesh to range mapping exact.
package task

import "errors"

// errNotImplemented marks behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Vec3 is a three component vector.
type Vec3 struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

// Transform is an affine world transform: a 3x3 matrix plus a translation.
type Transform struct {
	M [3][3]float64 `json:"m"`
	T Vec3          `json:"t"`
}

// VertexLayout describes the vertex stream of a sub-mesh.
type VertexLayout struct {
	HasTangent   bool `json:"has_tangent"`
	HasBitangent bool `json:"has_bitangent"`
	Stride       int  `json:"stride"`
}

// SubMesh is one drawable piece registered for merging.
type SubMesh struct {
	ID         string       `json:"id"`
	MaterialID string       `json:"material_id"`
	Layout     VertexLayout `json:"layout"`
	Positions  []Vec3       `json:"positions"`
	Normals    []Vec3       `json:"normals"`
	Tangents   []Vec3       `json:"tangents"`
	Indices    []uint32     `json:"indices"`
}

// SubRange is the slot one sub-mesh occupies inside a batch.
type SubRange struct {
	SubMeshID   string `json:"sub_mesh_id"`
	VertexStart int    `json:"vertex_start"`
	VertexCount int    `json:"vertex_count"`
	IndexStart  int    `json:"index_start"`
	IndexCount  int    `json:"index_count"`
}

// Batch is one merged mesh ready for the renderer.
type Batch struct {
	GroupKey    string       `json:"group_key"`
	BatchID     string       `json:"batch_id"`
	MaterialID  string       `json:"material_id"`
	Layout      VertexLayout `json:"layout"`
	Vertices    []Vec3       `json:"vertices"`
	Normals     []Vec3       `json:"normals"`
	Indices     []uint32     `json:"indices"`
	VertexCount int          `json:"vertex_count"`
	IndexCount  int          `json:"index_count"`
	IndexWidth  int          `json:"index_width"`
	Ranges      []SubRange   `json:"ranges"`
}

// BatchRef is the batch plus range a sub-mesh was merged into.
type BatchRef struct {
	BatchID     string `json:"batch_id"`
	GroupKey    string `json:"group_key"`
	MaterialID  string `json:"material_id"`
	VertexStart int    `json:"vertex_start"`
	VertexCount int    `json:"vertex_count"`
	IndexStart  int    `json:"index_start"`
	IndexCount  int    `json:"index_count"`
	Retired     bool   `json:"retired"`
}

// BuildReport summarises everything merged so far.
type BuildReport struct {
	Groups    int `json:"groups"`
	Batches   int `json:"batches"`
	SubMeshes int `json:"sub_meshes"`
	Vertices  int `json:"vertices"`
	Indices   int `json:"indices"`
}

// MergerOptions bounds one batch.
type MergerOptions struct {
	MaxVerticesPerBatch int `json:"max_vertices_per_batch"`
}

// Errors returned by the merger.
var (
	ErrBlankGroup      = errors.New("group key is required")
	ErrBadSubMesh      = errors.New("sub mesh id is required")
	ErrBadGeometry     = errors.New("sub mesh geometry is inconsistent")
	ErrDuplicateSubMesh = errors.New("sub mesh id already added")
	ErrSubMeshBound    = errors.New("sub mesh already belongs to another group")
	ErrMixedGroup      = errors.New("group mixes materials or vertex layouts")
	ErrUnknownGroup    = errors.New("group is unknown")
	ErrEmptyGroup      = errors.New("group has no sub meshes")
	ErrBatchTooLarge   = errors.New("batch vertex limit exceeded")
	ErrUnknownSubMesh  = errors.New("sub mesh is unknown")
	ErrNotBuilt        = errors.New("group has not been built")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// StaticBatchMerger owns the group tables and the built batches.
type StaticBatchMerger struct {
	options MergerOptions
}

// NewStaticBatchMerger builds a merger with the given caps.
func NewStaticBatchMerger(options MergerOptions) *StaticBatchMerger {
	return &StaticBatchMerger{options: options}
}

// AddSubMesh queues one sub-mesh into a group under a world transform.
func (m *StaticBatchMerger) AddSubMesh(groupKey string, mesh SubMesh, world Transform) error {
	return errNotImplemented
}

// Build merges every queued sub-mesh of one group into a single batch.
func (m *StaticBatchMerger) Build(groupKey string) (Batch, error) {
	return Batch{}, errNotImplemented
}

// RemoveSubMesh drops one sub-mesh and re-packs the surviving ranges.
func (m *StaticBatchMerger) RemoveSubMesh(groupKey, subMeshID string) error {
	return errNotImplemented
}

// BatchOf returns the batch and range a sub-mesh was merged into.
func (m *StaticBatchMerger) BatchOf(subMeshID string) (BatchRef, error) {
	return BatchRef{}, errNotImplemented
}

// Report summarises the merged groups.
func (m *StaticBatchMerger) Report() BuildReport {
	return BuildReport{}
}

// Groups lists the known group keys in lexical order.
func (m *StaticBatchMerger) Groups() []string {
	return nil
}

// SubMeshCount returns how many sub-meshes one group holds.
func (m *StaticBatchMerger) SubMeshCount(groupKey string) int {
	return 0
}

// TotalVertices returns the summed vertex count of one group.
func (m *StaticBatchMerger) TotalVertices(groupKey string) int {
	return 0
}

// IndexWidth returns the index width of the built batch of one group.
func (m *StaticBatchMerger) IndexWidth(groupKey string) int {
	return 0
}

// Snapshot serialises the whole merger state.
func (m *StaticBatchMerger) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole merger state from a snapshot.
func (m *StaticBatchMerger) Restore(data []byte) error {
	return errNotImplemented
}
