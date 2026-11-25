// Package task merges static sub-meshes into batches per group, bakes world
// transforms into the vertices and keeps the sub-mesh to range mapping exact.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
)

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

	// Tangents and Bitangents carry the re-orthonormalised tangent space when
	// the source meshes provide a tangent stream. They stay nil otherwise.
	Tangents   []Vec3 `json:"tangents,omitempty"`
	Bitangents []Vec3 `json:"bitangents,omitempty"`
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
	ErrBlankGroup       = errors.New("group key is required")
	ErrBadSubMesh       = errors.New("sub mesh id is required")
	ErrBadGeometry      = errors.New("sub mesh geometry is inconsistent")
	ErrDuplicateSubMesh = errors.New("sub mesh id already added")
	ErrSubMeshBound     = errors.New("sub mesh already belongs to another group")
	ErrMixedGroup       = errors.New("group mixes materials or vertex layouts")
	ErrUnknownGroup     = errors.New("group is unknown")
	ErrEmptyGroup       = errors.New("group has no sub meshes")
	ErrBatchTooLarge    = errors.New("batch vertex limit exceeded")
	ErrUnknownSubMesh   = errors.New("sub mesh is unknown")
	ErrNotBuilt         = errors.New("group has not been built")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

const (
	// indexWidth16 is the default index width.
	indexWidth16 = 16
	// indexWidth32 is used once a batch has more vertices than a uint16
	// index can address.
	indexWidth32 = 32
	// uint16AddressableVertices is the strict vertex threshold: batches with
	// more than 65535 vertices switch to 32 bit indices.
	uint16AddressableVertices = 65535

	snapshotMagic   = "gsb/batch49-go-game-b-02/static-batch-merger/v1"
	snapshotVersion = 1
)

// StaticBatchMerger owns the group tables and the built batches.
//
// All state lives behind st and is guarded by mu. Writers hold the exclusive
// lock across the whole bake and commit, so concurrent readers either observe
// the previous complete batch or the next complete batch, never half of one.
type StaticBatchMerger struct {
	mu sync.RWMutex
	st *mergerState
}

// mergerState is the serialisable whole state of a merger.
type mergerState struct {
	Options     MergerOptions           `json:"options"`
	GroupOrder  []string                `json:"group_order"`
	Groups      map[string]*groupState  `json:"groups"`
	Bindings    map[string]string       `json:"bindings"`
	ActiveRefs  map[string]BatchRef     `json:"active_refs"`
	LastRefs    map[string]BatchRef     `json:"last_refs"`
	RetiredRefs map[string]BatchRef     `json:"retired_refs"`
	Registry    map[string]*batchRecord `json:"registry"`
	BatchSeq    int                     `json:"batch_seq"`
}

// groupState stores one group in deterministic insertion order.
type groupState struct {
	Key      string                    `json:"key"`
	Material string                    `json:"material"`
	Layout   VertexLayout              `json:"layout"`
	Order    []string                  `json:"order"`
	Subs     map[string]*storedSubMesh `json:"subs"`

	Built      bool   `json:"built"`
	BatchID    string `json:"batch_id"`
	IndexWidth int    `json:"index_width"`
}

// storedSubMesh pairs the immutable source geometry with its bake transform.
type storedSubMesh struct {
	Mesh  SubMesh   `json:"mesh"`
	World Transform `json:"world"`
}

// batchRecord keeps built batches in the registry, including retired ones.
type batchRecord struct {
	Batch   Batch `json:"batch"`
	Retired bool  `json:"retired"`
}

func newMergerState(options MergerOptions) *mergerState {
	return &mergerState{
		Options:     options,
		Groups:      make(map[string]*groupState),
		Bindings:    make(map[string]string),
		ActiveRefs:  make(map[string]BatchRef),
		LastRefs:    make(map[string]BatchRef),
		RetiredRefs: make(map[string]BatchRef),
		Registry:    make(map[string]*batchRecord),
	}
}

// NewStaticBatchMerger builds a merger with the given caps.
func NewStaticBatchMerger(options MergerOptions) *StaticBatchMerger {
	return &StaticBatchMerger{st: newMergerState(options)}
}

// AddSubMesh queues one sub-mesh into a group under a world transform.
func (m *StaticBatchMerger) AddSubMesh(groupKey string, mesh SubMesh, world Transform) error {
	if strings.TrimSpace(groupKey) == "" {
		return ErrBlankGroup
	}
	if strings.TrimSpace(mesh.ID) == "" {
		return ErrBadSubMesh
	}
	if err := validateGeometry(mesh); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.st

	// One sub-mesh id can only ever belong to one group at a time.
	if boundGroup, bound := st.Bindings[mesh.ID]; bound {
		if boundGroup == groupKey {
			return ErrDuplicateSubMesh
		}
		return ErrSubMeshBound
	}

	group, exists := st.Groups[groupKey]
	if exists && len(group.Order) > 0 {
		if mesh.MaterialID != group.Material || mesh.Layout != group.Layout {
			return ErrMixedGroup
		}
	}

	// Commit point: every check above has passed, so the rejected paths can
	// never have mutated any table.
	if !exists {
		group = &groupState{
			Key:   groupKey,
			Subs:  make(map[string]*storedSubMesh),
			Order: []string{},
		}
		st.Groups[groupKey] = group
		st.GroupOrder = append(st.GroupOrder, groupKey)
	}
	if len(group.Order) == 0 {
		// A fresh group or a group whose batch was retired adopts the
		// material/layout of its new first member.
		group.Material = mesh.MaterialID
		group.Layout = mesh.Layout
		group.Built = false
		group.BatchID = ""
		group.IndexWidth = 0
	}

	group.Subs[mesh.ID] = &storedSubMesh{Mesh: mesh, World: world}
	group.Order = append(group.Order, mesh.ID)
	st.Bindings[mesh.ID] = groupKey
	return nil
}

// validateGeometry enforces length/index consistency of one source mesh.
func validateGeometry(mesh SubMesh) error {
	vertexCount := len(mesh.Positions)
	if len(mesh.Normals) != vertexCount {
		return ErrBadGeometry
	}
	// The tangent stream is mandatory for tangent-enabled layouts; when the
	// layout does not declare tangents, a nil stream is normal but a partially
	// sized provided stream is still inconsistent.
	if mesh.Layout.HasTangent || len(mesh.Tangents) > 0 {
		if len(mesh.Tangents) != vertexCount {
			return ErrBadGeometry
		}
	}
	if len(mesh.Indices)%3 != 0 {
		return ErrBadGeometry
	}
	for _, index := range mesh.Indices {
		if uint64(index) >= uint64(vertexCount) {
			return ErrBadGeometry
		}
	}
	return nil
}

// Build merges every queued sub-mesh of one group into a single batch.
func (m *StaticBatchMerger) Build(groupKey string) (Batch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	st := m.st
	group, ok := st.Groups[groupKey]
	if !ok {
		return Batch{}, ErrUnknownGroup
	}
	if len(group.Order) == 0 {
		return Batch{}, ErrEmptyGroup
	}

	totalVertices, totalIndices := groupTotals(group)
	if st.Options.MaxVerticesPerBatch > 0 && totalVertices > st.Options.MaxVerticesPerBatch {
		// Hard cap: reject without truncating and without committing any
		// built state (index width / refs stay at their previous value).
		return Batch{}, ErrBatchTooLarge
	}

	batch := bakeGroup(group, totalVertices, totalIndices)
	if group.BatchID == "" {
		st.BatchSeq++
		group.BatchID = fmt.Sprintf("batch-%06d", st.BatchSeq)
	}
	batch.BatchID = group.BatchID

	commitBatch(st, group, batch)
	group.Built = true
	group.IndexWidth = batch.IndexWidth
	return batch, nil
}

// groupTotals sums source vertex and index counts in insertion order.
func groupTotals(group *groupState) (int, int) {
	totalVertices, totalIndices := 0, 0
	for _, subMeshID := range group.Order {
		stored := group.Subs[subMeshID]
		totalVertices += len(stored.Mesh.Positions)
		totalIndices += len(stored.Mesh.Indices)
	}
	return totalVertices, totalIndices
}

// commitBatch publishes one fully baked batch and its sub-mesh refs.
func commitBatch(st *mergerState, group *groupState, batch Batch) {
	st.Registry[batch.BatchID] = &batchRecord{Batch: batch, Retired: false}
	for _, rng := range batch.Ranges {
		ref := BatchRef{
			BatchID:     batch.BatchID,
			GroupKey:    batch.GroupKey,
			MaterialID:  batch.MaterialID,
			VertexStart: rng.VertexStart,
			VertexCount: rng.VertexCount,
			IndexStart:  rng.IndexStart,
			IndexCount:  rng.IndexCount,
		}
		st.ActiveRefs[rng.SubMeshID] = ref
		st.LastRefs[rng.SubMeshID] = ref
		// A fresh build supersedes any retired tombstone for this id.
		delete(st.RetiredRefs, rng.SubMeshID)
	}
}

// rebuildCurrentLocked re-bakes the current members of a built group after a
// removal, reusing the batch id so ranges compact in place. Removing vertices
// can never trip the vertex cap that the previously committed batch passed.
func rebuildCurrentLocked(st *mergerState, group *groupState) {
	if group.BatchID == "" || len(group.Order) == 0 {
		return
	}
	totalVertices, totalIndices := groupTotals(group)
	batch := bakeGroup(group, totalVertices, totalIndices)
	batch.BatchID = group.BatchID
	commitBatch(st, group, batch)
	group.Built = true
	group.IndexWidth = batch.IndexWidth
}

// bakeGroup produces the merged batch with baked transforms and rebased
// indices. It is a pure function of the group's stored meshes and order.
func bakeGroup(group *groupState, totalVertices, totalIndices int) Batch {
	width := indexWidth16
	if totalVertices > uint16AddressableVertices {
		width = indexWidth32
	}

	batch := Batch{
		GroupKey:    group.Key,
		MaterialID:  group.Material,
		Layout:      group.Layout,
		IndexWidth:  width,
		VertexCount: totalVertices,
		IndexCount:  totalIndices,
		Vertices:    make([]Vec3, 0, totalVertices),
		Normals:     make([]Vec3, 0, totalVertices),
		Indices:     make([]uint32, 0, totalIndices),
		Ranges:      make([]SubRange, 0, len(group.Order)),
	}

	wantTangents := group.Layout.HasTangent
	if !wantTangents {
		for _, subMeshID := range group.Order {
			mesh := group.Subs[subMeshID].Mesh
			if len(mesh.Tangents) == len(mesh.Positions) && len(mesh.Positions) > 0 {
				wantTangents = true
				break
			}
		}
	}
	if wantTangents {
		batch.Tangents = make([]Vec3, 0, totalVertices)
		batch.Bitangents = make([]Vec3, 0, totalVertices)
	}

	for _, subMeshID := range group.Order {
		stored := group.Subs[subMeshID]
		mesh := stored.Mesh
		world := stored.World
		vertexCount := len(mesh.Positions)

		vertexStart := len(batch.Vertices)
		indexStart := len(batch.Indices)

		normalMatrix := inverseTranspose3(world.M)

		for vertexIndex := 0; vertexIndex < vertexCount; vertexIndex++ {
			batch.Vertices = append(batch.Vertices, transformPoint(mesh.Positions[vertexIndex], world))

			normal := normalizeOrZero(mulMatVec3(normalMatrix, mesh.Normals[vertexIndex]))
			batch.Normals = append(batch.Normals, normal)

			if wantTangents {
				var sourceTangent Vec3
				if len(mesh.Tangents) == vertexCount {
					// Tangents are directions: upper 3x3 only, then the
					// mandatory Gram-Schmidt correction against the baked
					// normal removes non-uniform-scale skew.
					sourceTangent = normalizeOrZero(mulMatVec3(world.M, mesh.Tangents[vertexIndex]))
				}
				tangent := orthogonalizeTangent(sourceTangent, normal)
				bitangent := normalizeOrZero(crossV3(tangent, normal))
				batch.Tangents = append(batch.Tangents, tangent)
				batch.Bitangents = append(batch.Bitangents, bitangent)
			}
		}

		// Every index is rebuilt against the global vertex base; no old
		// buffer or truncated index value is ever reused.
		for _, index := range mesh.Indices {
			batch.Indices = append(batch.Indices, uint32(vertexStart)+index)
		}

		batch.Ranges = append(batch.Ranges, SubRange{
			SubMeshID:   subMeshID,
			VertexStart: vertexStart,
			VertexCount: vertexCount,
			IndexStart:  indexStart,
			IndexCount:  len(mesh.Indices),
		})
	}

	return batch
}

// RemoveSubMesh drops one sub-mesh and re-packs the surviving ranges.
func (m *StaticBatchMerger) RemoveSubMesh(groupKey, subMeshID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	st := m.st
	group, ok := st.Groups[groupKey]
	if !ok {
		return ErrUnknownGroup
	}
	if _, member := group.Subs[subMeshID]; !member {
		return ErrUnknownSubMesh
	}

	delete(group.Subs, subMeshID)
	delete(st.Bindings, subMeshID)
	delete(st.ActiveRefs, subMeshID)
	if index := indexOfString(group.Order, subMeshID); index >= 0 {
		group.Order = append(group.Order[:index], group.Order[index+1:]...)
	}

	if len(group.Order) == 0 {
		retireGroupBatch(st, group)
		return nil
	}

	// Compact surviving ranges immediately so BatchOf observes the new
	// intervals even before an explicit rebuild.
	if group.Built {
		rebuildCurrentLocked(st, group)
	}
	return nil
}

// retireGroupBatch moves the group's current batch generation into the
// retired state while retaining its registry record and tombstone refs.
func retireGroupBatch(st *mergerState, group *groupState) {
	batchID := group.BatchID
	if group.Built && batchID != "" {
		if record := st.Registry[batchID]; record != nil {
			record.Retired = true
		}
		// Every id whose last committed ref belonged to this batch generation
		// becomes queryable as a retired ref, unless the id has since been
		// re-bound to live geometry.
		for id, lastRef := range st.LastRefs {
			if lastRef.BatchID != batchID {
				continue
			}
			if _, bound := st.Bindings[id]; bound {
				continue
			}
			retiredRef := lastRef
			retiredRef.Retired = true
			st.RetiredRefs[id] = retiredRef
		}
	}
	group.Built = false
	group.IndexWidth = 0
	group.BatchID = ""
}

// BatchOf returns the batch and range a sub-mesh was merged into.
func (m *StaticBatchMerger) BatchOf(subMeshID string) (BatchRef, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	st := m.st
	if ref, ok := st.ActiveRefs[subMeshID]; ok {
		return ref, nil
	}
	if _, bound := st.Bindings[subMeshID]; bound {
		// Currently registered but no build covering it has been committed.
		return BatchRef{}, ErrNotBuilt
	}
	if ref, ok := st.RetiredRefs[subMeshID]; ok {
		return ref, nil
	}
	return BatchRef{}, ErrUnknownSubMesh
}

// Report summarises the merged groups.
func (m *StaticBatchMerger) Report() BuildReport {
	m.mu.RLock()
	defer m.mu.RUnlock()

	st := m.st
	report := BuildReport{Groups: len(st.Groups)}
	for _, group := range st.Groups {
		report.SubMeshes += len(group.Order)
	}
	for _, record := range st.Registry {
		if record == nil || record.Retired {
			continue
		}
		report.Batches++
		report.Vertices += record.Batch.VertexCount
		report.Indices += record.Batch.IndexCount
	}
	return report
}

// Groups lists the known group keys in lexical order.
func (m *StaticBatchMerger) Groups() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	keys := append([]string(nil), m.st.GroupOrder...)
	sort.Strings(keys)
	return keys
}

// SubMeshCount returns how many sub-meshes one group holds.
func (m *StaticBatchMerger) SubMeshCount(groupKey string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if group, ok := m.st.Groups[groupKey]; ok {
		return len(group.Order)
	}
	return 0
}

// TotalVertices returns the summed vertex count of one group.
func (m *StaticBatchMerger) TotalVertices(groupKey string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	group, ok := m.st.Groups[groupKey]
	if !ok {
		return 0
	}
	total := 0
	for _, subMeshID := range group.Order {
		total += len(group.Subs[subMeshID].Mesh.Positions)
	}
	return total
}

// IndexWidth returns the index width of the built batch of one group.
func (m *StaticBatchMerger) IndexWidth(groupKey string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if group, ok := m.st.Groups[groupKey]; ok && group.Built {
		return group.IndexWidth
	}
	return 0
}

// snapshotEnvelope wraps the serialised state with integrity metadata.
type snapshotEnvelope struct {
	Magic    string          `json:"magic"`
	Version  int             `json:"version"`
	Checksum string          `json:"checksum"`
	Payload  json.RawMessage `json:"payload"`
}

// Snapshot serialises the whole merger state.
func (m *StaticBatchMerger) Snapshot() ([]byte, error) {
	m.mu.RLock()
	payload, err := json.Marshal(m.st)
	m.mu.RUnlock()
	if err != nil {
		return nil, err
	}

	digest := sha256.Sum256(payload)
	envelope := snapshotEnvelope{
		Magic:    snapshotMagic,
		Version:  snapshotVersion,
		Checksum: hex.EncodeToString(digest[:]),
		Payload:  payload,
	}
	return json.Marshal(envelope)
}

// Restore replaces the whole merger state from a snapshot. A corrupt payload
// yields ErrBadSnapshot and leaves the in-memory state untouched.
func (m *StaticBatchMerger) Restore(data []byte) error {
	next, err := decodeSnapshot(data)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}

	m.mu.Lock()
	// Limits are constructor configuration of the running process; a
	// snapshot restores tables and batch mappings, not the local hard caps.
	if m.st != nil {
		next.Options = m.st.Options
	}
	m.st = next
	m.mu.Unlock()
	return nil
}

// decodeSnapshot parses and validates a snapshot into a fresh state. It never
// touches the live merger, so a failure cannot mutate memory.
func decodeSnapshot(data []byte) (*mergerState, error) {
	if len(data) == 0 {
		return nil, errors.New("empty payload")
	}

	var envelope snapshotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("invalid envelope: %w", err)
	}
	if envelope.Magic != snapshotMagic {
		return nil, errors.New("bad magic")
	}
	if envelope.Version != snapshotVersion {
		return nil, fmt.Errorf("unsupported version %d", envelope.Version)
	}
	if len(envelope.Payload) == 0 {
		return nil, errors.New("missing payload")
	}
	digest := sha256.Sum256(envelope.Payload)
	if envelope.Checksum != hex.EncodeToString(digest[:]) {
		return nil, errors.New("checksum mismatch")
	}

	state := newMergerState(MergerOptions{})
	if err := json.Unmarshal(envelope.Payload, state); err != nil {
		return nil, fmt.Errorf("invalid state: %w", err)
	}
	if err := validateSnapshotState(state); err != nil {
		return nil, err
	}
	return state, nil
}

// validateSnapshotState checks the cross-table invariants the runtime relies
// on and normalises nil maps.
func validateSnapshotState(state *mergerState) error {
	if state.Groups == nil {
		return errors.New("missing groups table")
	}
	if state.Bindings == nil {
		state.Bindings = make(map[string]string)
	}
	if state.ActiveRefs == nil {
		state.ActiveRefs = make(map[string]BatchRef)
	}
	if state.LastRefs == nil {
		state.LastRefs = make(map[string]BatchRef)
	}
	if state.RetiredRefs == nil {
		state.RetiredRefs = make(map[string]BatchRef)
	}
	if state.Registry == nil {
		state.Registry = make(map[string]*batchRecord)
	}

	for groupKey, group := range state.Groups {
		if group == nil {
			return fmt.Errorf("nil group %q", groupKey)
		}
		if group.Subs == nil {
			group.Subs = make(map[string]*storedSubMesh)
		}
		seen := make(map[string]bool, len(group.Order))
		for _, subMeshID := range group.Order {
			if seen[subMeshID] {
				return fmt.Errorf("duplicate order entry %q", subMeshID)
			}
			seen[subMeshID] = true
			stored := group.Subs[subMeshID]
			if stored == nil {
				return fmt.Errorf("missing stored mesh %q", subMeshID)
			}
			if err := validateGeometry(stored.Mesh); err != nil {
				return fmt.Errorf("stored mesh %q invalid: %w", subMeshID, err)
			}
			if boundGroup, ok := state.Bindings[subMeshID]; !ok || boundGroup != groupKey {
				return fmt.Errorf("binding mismatch for %q", subMeshID)
			}
		}
	}
	for subMeshID, groupKey := range state.Bindings {
		group, ok := state.Groups[groupKey]
		if !ok {
			return fmt.Errorf("binding %q points to missing group %q", subMeshID, groupKey)
		}
		if _, member := group.Subs[subMeshID]; !member {
			return fmt.Errorf("binding %q not present in group %q", subMeshID, groupKey)
		}
	}
	return nil
}

// indexOfString returns the position of value in values or -1.
func indexOfString(values []string, value string) int {
	for i, candidate := range values {
		if candidate == value {
			return i
		}
	}
	return -1
}

// --- vector / matrix helpers ---------------------------------------------

func mulMatVec3(matrix [3][3]float64, v Vec3) Vec3 {
	return Vec3{
		X: matrix[0][0]*v.X + matrix[0][1]*v.Y + matrix[0][2]*v.Z,
		Y: matrix[1][0]*v.X + matrix[1][1]*v.Y + matrix[1][2]*v.Z,
		Z: matrix[2][0]*v.X + matrix[2][1]*v.Y + matrix[2][2]*v.Z,
	}
}

func transformPoint(point Vec3, transform Transform) Vec3 {
	transformed := mulMatVec3(transform.M, point)
	transformed.X += transform.T.X
	transformed.Y += transform.T.Y
	transformed.Z += transform.T.Z
	return transformed
}

func dotV3(a, b Vec3) float64 {
	return a.X*b.X + a.Y*b.Y + a.Z*b.Z
}

func crossV3(a, b Vec3) Vec3 {
	return Vec3{
		X: a.Y*b.Z - a.Z*b.Y,
		Y: a.Z*b.X - a.X*b.Z,
		Z: a.X*b.Y - a.Y*b.X,
	}
}

func lengthSquaredV3(v Vec3) float64 {
	return dotV3(v, v)
}

// normalizeOrZero normalises non-degenerate vectors and returns the original
// (typically zero) vector for a zero-length input instead of NaN.
func normalizeOrZero(v Vec3) Vec3 {
	length := math.Sqrt(lengthSquaredV3(v))
	if length < 1e-15 {
		return v
	}
	scale := 1.0 / length
	return Vec3{X: v.X * scale, Y: v.Y * scale, Z: v.Z * scale}
}

// orthogonalizeTangent runs Gram-Schmidt against the baked normal and returns
// a unit tangent perpendicular to it. Degenerate inputs fall back to a stable
// axis choice so the result never contains NaN.
func orthogonalizeTangent(tangent, normal Vec3) Vec3 {
	d := dotV3(tangent, normal)
	orthogonal := Vec3{
		X: tangent.X - normal.X*d,
		Y: tangent.Y - normal.Y*d,
		Z: tangent.Z - normal.Z*d,
	}
	if lengthSquaredV3(orthogonal) < 1e-20 {
		reference := Vec3{X: 0, Y: 1, Z: 0}
		if math.Abs(normal.Y) > 0.9 {
			reference = Vec3{X: 1, Y: 0, Z: 0}
		}
		orthogonal = crossV3(reference, normal)
		if lengthSquaredV3(orthogonal) < 1e-20 {
			orthogonal = crossV3(normal, Vec3{X: 1, Y: 0, Z: 0})
		}
	}
	return normalizeOrZero(orthogonal)
}

// inverseTranspose3 returns (M^-1)^T used for normal transformation.
// Singular matrices fall back to M itself (best effort) rather than NaN.
func inverseTranspose3(m [3][3]float64) [3][3]float64 {
	inv := inverse3(m)
	return [3][3]float64{
		{inv[0][0], inv[1][0], inv[2][0]},
		{inv[0][1], inv[1][1], inv[2][1]},
		{inv[0][2], inv[1][2], inv[2][2]},
	}
}

// inverse3 computes the inverse of a 3x3 matrix.
func inverse3(m [3][3]float64) [3][3]float64 {
	a, b, c := m[0][0], m[0][1], m[0][2]
	d, e, f := m[1][0], m[1][1], m[1][2]
	g, h, i := m[2][0], m[2][1], m[2][2]

	eiFH := e*i - f*h
	diFG := d*i - f*g
	dhEG := d*h - e*g
	det := a*eiFH - b*diFG + c*dhEG
	if math.Abs(det) < 1e-15 {
		return m
	}
	invDet := 1.0 / det

	return [3][3]float64{
		{
			eiFH * invDet,
			(c*h - b*i) * invDet,
			(b*f - c*e) * invDet,
		},
		{
			(f*g - d*i) * invDet,
			(a*i - c*g) * invDet,
			(c*d - a*f) * invDet,
		},
		{
			dhEG * invDet,
			(b*g - a*h) * invDet,
			(a*e - b*d) * invDet,
		},
	}
}
