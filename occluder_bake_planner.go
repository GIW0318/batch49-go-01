// Package task bakes occluder candidates into per cell runtime data, filters
// unusable candidates and keeps a verifiable version hash per cell.
package task

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
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

// CellID identifies one cell of the bake grid.
type CellID struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// Triangle is one candidate occluder triangle in world space.
type Triangle struct {
	A Vec3 `json:"a"`
	B Vec3 `json:"b"`
	C Vec3 `json:"c"`
}

// MaterialInfo describes how one material takes part in occlusion.
type MaterialInfo struct {
	ID          string `json:"id"`
	Transparent bool   `json:"transparent"`
	Dynamic     bool   `json:"dynamic"`
}

// CellData is the baked result of one cell.
type CellData struct {
	Cell      CellID  `json:"cell"`
	Triangles int     `json:"triangles"`
	Coverage  float64 `json:"coverage"`
	Version   string  `json:"version"`
	Discarded bool    `json:"discarded"`
}

// BakeResult reports what one incremental bake pass produced.
type BakeResult struct {
	Baked     []CellID `json:"baked"`
	Discarded []CellID `json:"discarded"`
	Skipped   int      `json:"skipped"`
}

// BakeOptions bounds the bake grid and the batch sizes.
type BakeOptions struct {
	CellSize             float64 `json:"cell_size"`
	MinCoverage          float64 `json:"min_coverage"`
	MaxTrianglesPerBatch int     `json:"max_triangles_per_batch"`
	MaxCellsPerBake      int     `json:"max_cells_per_bake"`
}

// Errors returned by the planner.
var (
	ErrBlankScene      = errors.New("scene id is required")
	ErrBadMaterial     = errors.New("material id is required")
	ErrEmptyBatch      = errors.New("triangle batch is empty")
	ErrBatchTooLarge   = errors.New("triangle batch exceeds the cap")
	ErrBadTriangle     = errors.New("triangle coordinates are not finite")
	ErrUnknownCell     = errors.New("cell is unknown")
	ErrUnknownMaterial = errors.New("material was never submitted")
	ErrNotBaked        = errors.New("cell has not been baked")
	ErrCellDiscarded   = errors.New("cell was discarded")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

type triangleKey struct {
	A        Vec3
	B        Vec3
	C        Vec3
	Material string
}

type candidate struct {
	key  triangleKey
	poly []Vec3
}

type cellState struct {
	candidates map[triangleKey]*candidate
	baked      *CellData
	discarded  bool
	dirty      bool
}

// OccluderBakePlanner owns the candidate grid and the baked cell table.
type OccluderBakePlanner struct {
	mu        sync.RWMutex
	options   BakeOptions
	cells     map[CellID]*cellState
	materials map[string]MaterialInfo
}

// NewOccluderBakePlanner builds a planner for the given grid.
func NewOccluderBakePlanner(options BakeOptions) *OccluderBakePlanner {
	return &OccluderBakePlanner{
		options:   options,
		cells:     make(map[CellID]*cellState),
		materials: make(map[string]MaterialInfo),
	}
}

// Submit queues one triangle batch of a single material.
func (p *OccluderBakePlanner) Submit(sceneID string, tris []Triangle, mat MaterialInfo) error {
	if strings.TrimSpace(sceneID) == "" {
		return ErrBlankScene
	}
	if strings.TrimSpace(mat.ID) == "" {
		return ErrBadMaterial
	}
	if len(tris) == 0 {
		return ErrEmptyBatch
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if len(tris) > p.options.MaxTrianglesPerBatch {
		return ErrBatchTooLarge
	}
	for i := range tris {
		if !finiteTriangle(tris[i]) {
			return ErrBadTriangle
		}
	}

	p.materials[mat.ID] = mat
	if mat.Transparent || mat.Dynamic {
		return nil
	}

	if p.options.CellSize <= 0 || math.IsNaN(p.options.CellSize) || math.IsInf(p.options.CellSize, 0) {
		return nil
	}

	for i := range tris {
		tri := canonicalTriangle(tris[i])
		key := triangleKey{
			A:        tri.A,
			B:        tri.B,
			C:        tri.C,
			Material: mat.ID,
		}
		for _, cell := range cellsCoveredByTriangle(tri, p.options.CellSize) {
			poly := clipTriangleToCell(tri, p.cellRect(cell))
			if len(poly) < 3 || polygonArea(poly) <= 0 {
				continue
			}

			state := p.cells[cell]
			if state == nil {
				state = &cellState{candidates: make(map[triangleKey]*candidate)}
				p.cells[cell] = state
			}
			if _, exists := state.candidates[key]; exists {
				continue
			}

			state.candidates[key] = &candidate{key: key, poly: poly}
			state.dirty = true
			state.discarded = false
		}
	}
	return nil
}

// BakeCell bakes one cell from its current candidates.
func (p *OccluderBakePlanner) BakeCell(cell CellID) (CellData, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.bakeCellLocked(cell)
}

func (p *OccluderBakePlanner) bakeCellLocked(cell CellID) (CellData, error) {
	state := p.cells[cell]
	if state == nil {
		return CellData{}, ErrUnknownCell
	}

	coverage := p.coverageLocked(cell, state)
	if coverage < p.options.MinCoverage {
		state.baked = nil
		state.discarded = true
		state.dirty = false
		return CellData{
			Cell:      cell,
			Triangles: len(state.candidates),
			Coverage:  coverage,
			Discarded: true,
		}, nil
	}

	cands := sortedCandidates(state)
	data := CellData{
		Cell:      cell,
		Triangles: len(cands),
		Coverage:  coverage,
		Version:   hashCell(cell, cands),
	}
	state.baked = &data
	state.discarded = false
	state.dirty = false
	return data, nil
}

// BakePending bakes the pending cells within the per pass budget.
func (p *OccluderBakePlanner) BakePending(limit int) (BakeResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	pending := make([]CellID, 0)
	for cell, state := range p.cells {
		if state.dirty {
			pending = append(pending, cell)
		}
	}
	sortCellIDs(pending)

	budget := p.options.MaxCellsPerBake
	if budget < 0 {
		budget = 0
	}
	if limit <= 0 {
		budget = 0
	} else if limit < budget {
		budget = limit
	}
	if budget > len(pending) {
		budget = len(pending)
	}

	result := BakeResult{}
	for _, cell := range pending[:budget] {
		data, err := p.bakeCellLocked(cell)
		if err != nil {
			return result, err
		}
		if data.Discarded {
			result.Discarded = append(result.Discarded, cell)
		} else {
			result.Baked = append(result.Baked, cell)
		}
	}
	result.Skipped = len(pending) - budget
	return result, nil
}

// InvalidateByMaterial marks every cell using one material as dirty.
func (p *OccluderBakePlanner) InvalidateByMaterial(matID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, ok := p.materials[matID]; !ok {
		return ErrUnknownMaterial
	}
	for _, state := range p.cells {
		for key := range state.candidates {
			if key.Material == matID {
				state.dirty = true
				break
			}
		}
	}
	return nil
}

// Cells lists every cell that has candidates, ordered by X then Y.
func (p *OccluderBakePlanner) Cells() []CellID {
	p.mu.RLock()
	defer p.mu.RUnlock()

	cells := make([]CellID, 0, len(p.cells))
	for cell := range p.cells {
		cells = append(cells, cell)
	}
	sortCellIDs(cells)
	return cells
}

// DirtyCells lists the cells that need a rebake, ordered by X then Y.
func (p *OccluderBakePlanner) DirtyCells() []CellID {
	p.mu.RLock()
	defer p.mu.RUnlock()

	cells := make([]CellID, 0)
	for cell, state := range p.cells {
		if state.dirty {
			cells = append(cells, cell)
		}
	}
	sortCellIDs(cells)
	return cells
}

// DiscardedCells lists the cells dropped by the coverage gate.
func (p *OccluderBakePlanner) DiscardedCells() []CellID {
	p.mu.RLock()
	defer p.mu.RUnlock()

	cells := make([]CellID, 0)
	for cell, state := range p.cells {
		if state.discarded {
			cells = append(cells, cell)
		}
	}
	sortCellIDs(cells)
	return cells
}

// Coverage returns the projected coverage ratio of one cell.
func (p *OccluderBakePlanner) Coverage(cell CellID) (float64, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	state := p.cells[cell]
	if state == nil {
		return 0, ErrUnknownCell
	}
	return p.coverageLocked(cell, state), nil
}

// VersionHash returns the verifiable version hash of one baked cell.
func (p *OccluderBakePlanner) VersionHash(cell CellID) (string, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	state := p.cells[cell]
	if state == nil {
		return "", ErrUnknownCell
	}
	if state.discarded {
		return "", ErrCellDiscarded
	}
	if state.baked == nil {
		return "", ErrNotBaked
	}
	return state.baked.Version, nil
}

// CandidateCount returns how many candidates one cell holds.
func (p *OccluderBakePlanner) CandidateCount(cell CellID) int {
	p.mu.RLock()
	defer p.mu.RUnlock()

	state := p.cells[cell]
	if state == nil {
		return 0
	}
	return len(state.candidates)
}

type snapshotEnvelope struct {
	Magic    string          `json:"magic"`
	Checksum string          `json:"checksum"`
	Payload  json.RawMessage `json:"payload"`
}

type snapshotPayload struct {
	Options   BakeOptions    `json:"options"`
	Materials []string       `json:"materials"`
	Cells     []snapshotCell `json:"cells"`
}

type snapshotCell struct {
	ID         CellID              `json:"id"`
	Candidates []snapshotCandidate `json:"candidates"`
	Baked      *CellData           `json:"baked,omitempty"`
	Discarded  bool                `json:"discarded"`
	Dirty      bool                `json:"dirty"`
}

type snapshotCandidate struct {
	Material string   `json:"material"`
	Triangle Triangle `json:"triangle"`
}

const snapshotMagic = "gsb-occluder-bake-planner-v1"

// Snapshot serialises the whole planner state.
func (p *OccluderBakePlanner) Snapshot() ([]byte, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	materials := make([]string, 0, len(p.materials))
	for id := range p.materials {
		materials = append(materials, id)
	}
	sort.Strings(materials)

	cellIDs := make([]CellID, 0, len(p.cells))
	for cell := range p.cells {
		cellIDs = append(cellIDs, cell)
	}
	sortCellIDs(cellIDs)

	payload := snapshotPayload{
		Options:   p.options,
		Materials: materials,
		Cells:     make([]snapshotCell, 0, len(cellIDs)),
	}
	for _, cell := range cellIDs {
		state := p.cells[cell]
		sc := snapshotCell{
			ID:        cell,
			Discarded: state.discarded,
			Dirty:     state.dirty,
		}
		if state.baked != nil {
			baked := *state.baked
			sc.Baked = &baked
		}

		cands := sortedCandidates(state)
		sc.Candidates = make([]snapshotCandidate, 0, len(cands))
		for _, cand := range cands {
			sc.Candidates = append(sc.Candidates, snapshotCandidate{
				Material: cand.key.Material,
				Triangle: Triangle{A: cand.key.A, B: cand.key.B, C: cand.key.C},
			})
		}
		payload.Cells = append(payload.Cells, sc)
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(payloadBytes)
	envelope := snapshotEnvelope{
		Magic:    snapshotMagic,
		Checksum: hex.EncodeToString(sum[:]),
		Payload:  json.RawMessage(payloadBytes),
	}
	return json.Marshal(envelope)
}

// Restore replaces the whole planner state from a snapshot.
func (p *OccluderBakePlanner) Restore(data []byte) error {
	payload, err := decodeSnapshot(data)
	if err != nil {
		return err
	}
	cells, materials, err := buildRestoredState(payload)
	if err != nil {
		return err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.options = payload.Options
	p.cells = cells
	p.materials = materials
	return nil
}

func decodeSnapshot(data []byte) (*snapshotPayload, error) {
	var envelope snapshotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, ErrBadSnapshot
	}
	if envelope.Magic != snapshotMagic || len(envelope.Payload) == 0 {
		return nil, ErrBadSnapshot
	}
	sum := sha256.Sum256(envelope.Payload)
	if !strings.EqualFold(envelope.Checksum, hex.EncodeToString(sum[:])) {
		return nil, ErrBadSnapshot
	}

	var payload snapshotPayload
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return nil, ErrBadSnapshot
	}
	return &payload, nil
}

func buildRestoredState(payload *snapshotPayload) (map[CellID]*cellState, map[string]MaterialInfo, error) {
	options := payload.Options
	if math.IsNaN(options.CellSize) || math.IsInf(options.CellSize, 0) ||
		math.IsNaN(options.MinCoverage) || math.IsInf(options.MinCoverage, 0) {
		return nil, nil, ErrBadSnapshot
	}

	materials := make(map[string]MaterialInfo, len(payload.Materials))
	for _, id := range payload.Materials {
		if strings.TrimSpace(id) == "" {
			return nil, nil, ErrBadSnapshot
		}
		if _, exists := materials[id]; exists {
			return nil, nil, ErrBadSnapshot
		}
		materials[id] = MaterialInfo{ID: id}
	}

	cells := make(map[CellID]*cellState, len(payload.Cells))
	for _, sc := range payload.Cells {
		if _, exists := cells[sc.ID]; exists {
			return nil, nil, ErrBadSnapshot
		}
		if len(sc.Candidates) == 0 {
			return nil, nil, ErrBadSnapshot
		}
		if sc.Baked != nil && sc.Discarded {
			return nil, nil, ErrBadSnapshot
		}
		if sc.Baked != nil {
			if sc.Baked.Cell != sc.ID || sc.Baked.Version == "" ||
				!finiteNumber(sc.Baked.Coverage) {
				return nil, nil, ErrBadSnapshot
			}
			if !sc.Dirty && sc.Baked.Triangles != len(sc.Candidates) {
				return nil, nil, ErrBadSnapshot
			}
		}

		state := &cellState{
			candidates: make(map[triangleKey]*candidate, len(sc.Candidates)),
			discarded:  sc.Discarded,
			dirty:      sc.Dirty,
		}
		if sc.Baked != nil {
			baked := *sc.Baked
			state.baked = &baked
		}

		rect := cellRectFor(options.CellSize, sc.ID)
		for _, candidateRecord := range sc.Candidates {
			if _, ok := materials[candidateRecord.Material]; !ok {
				return nil, nil, ErrBadSnapshot
			}
			if !finiteTriangle(candidateRecord.Triangle) {
				return nil, nil, ErrBadSnapshot
			}
			tri := canonicalTriangle(candidateRecord.Triangle)
			key := triangleKey{
				A:        tri.A,
				B:        tri.B,
				C:        tri.C,
				Material: candidateRecord.Material,
			}
			if _, exists := state.candidates[key]; exists {
				return nil, nil, ErrBadSnapshot
			}
			poly := clipTriangleToCell(tri, rect)
			if options.CellSize <= 0 || len(poly) < 3 || polygonArea(poly) <= 0 {
				return nil, nil, ErrBadSnapshot
			}
			state.candidates[key] = &candidate{key: key, poly: poly}
		}

		if state.baked == nil && !state.discarded && !state.dirty {
			return nil, nil, ErrBadSnapshot
		}
		cells[sc.ID] = state
	}
	return cells, materials, nil
}

func finiteNumber(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

func finiteTriangle(t Triangle) bool {
	return finiteVec3(t.A) && finiteVec3(t.B) && finiteVec3(t.C)
}

func finiteVec3(v Vec3) bool {
	return finiteNumber(v.X) && finiteNumber(v.Y) && finiteNumber(v.Z)
}

func canonicalTriangle(t Triangle) Triangle {
	verts := []Vec3{normalizeVec3(t.A), normalizeVec3(t.B), normalizeVec3(t.C)}
	sort.Slice(verts, func(i, j int) bool {
		return compareVec3(verts[i], verts[j]) < 0
	})
	return Triangle{A: verts[0], B: verts[1], C: verts[2]}
}

func normalizeVec3(v Vec3) Vec3 {
	return Vec3{X: normalizeZero(v.X), Y: normalizeZero(v.Y), Z: normalizeZero(v.Z)}
}

func normalizeZero(v float64) float64 {
	if v == 0 {
		return 0
	}
	return v
}

func compareVec3(a, b Vec3) int {
	if a.X < b.X {
		return -1
	}
	if a.X > b.X {
		return 1
	}
	if a.Y < b.Y {
		return -1
	}
	if a.Y > b.Y {
		return 1
	}
	if a.Z < b.Z {
		return -1
	}
	if a.Z > b.Z {
		return 1
	}
	return 0
}

func compareTriangle(a, b Triangle) int {
	if cmp := compareVec3(a.A, b.A); cmp != 0 {
		return cmp
	}
	if cmp := compareVec3(a.B, b.B); cmp != 0 {
		return cmp
	}
	return compareVec3(a.C, b.C)
}

func sortedCandidates(state *cellState) []*candidate {
	cands := make([]*candidate, 0, len(state.candidates))
	for _, cand := range state.candidates {
		cands = append(cands, cand)
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].key.Material != cands[j].key.Material {
			return cands[i].key.Material < cands[j].key.Material
		}
		triI := Triangle{A: cands[i].key.A, B: cands[i].key.B, C: cands[i].key.C}
		triJ := Triangle{A: cands[j].key.A, B: cands[j].key.B, C: cands[j].key.C}
		return compareTriangle(triI, triJ) < 0
	})
	return cands
}

func sortCellIDs(cells []CellID) {
	sort.Slice(cells, func(i, j int) bool {
		if cells[i].X != cells[j].X {
			return cells[i].X < cells[j].X
		}
		return cells[i].Y < cells[j].Y
	})
}

func (p *OccluderBakePlanner) cellRect(cell CellID) [4]float64 {
	return cellRectFor(p.options.CellSize, cell)
}

func cellRectFor(size float64, cell CellID) [4]float64 {
	x0 := float64(cell.X) * size
	y0 := float64(cell.Y) * size
	return [4]float64{x0, x0 + size, y0, y0 + size}
}

func cellsCoveredByTriangle(t Triangle, size float64) []CellID {
	if size <= 0 || math.IsNaN(size) || math.IsInf(size, 0) {
		return nil
	}
	xs := []float64{t.A.X, t.B.X, t.C.X}
	ys := []float64{t.A.Y, t.B.Y, t.C.Y}
	minX, maxX := xs[0], xs[0]
	minY, maxY := ys[0], ys[0]
	for _, x := range xs[1:] {
		if x < minX {
			minX = x
		}
		if x > maxX {
			maxX = x
		}
	}
	for _, y := range ys[1:] {
		if y < minY {
			minY = y
		}
		if y > maxY {
			maxY = y
		}
	}
	if maxX <= minX || maxY <= minY {
		return nil
	}

	firstX := int(math.Floor(minX / size))
	lastX := int(math.Floor(math.Nextafter(maxX, math.Inf(-1)) / size))
	firstY := int(math.Floor(minY / size))
	lastY := int(math.Floor(math.Nextafter(maxY, math.Inf(-1)) / size))
	if lastX < firstX || lastY < firstY {
		return nil
	}

	cells := make([]CellID, 0, (lastX-firstX+1)*(lastY-firstY+1))
	for y := firstY; y <= lastY; y++ {
		for x := firstX; x <= lastX; x++ {
			cells = append(cells, CellID{X: x, Y: y})
		}
	}
	return cells
}

func clipTriangleToCell(t Triangle, rect [4]float64) []Vec3 {
	poly := []Vec3{normalizeVec3(t.A), normalizeVec3(t.B), normalizeVec3(t.C)}
	poly = clipHalfPlane(poly,
		func(v Vec3) bool { return v.X >= rect[0] },
		func(s, e Vec3) Vec3 { return interpolateAtX(s, e, rect[0]) })
	poly = clipHalfPlane(poly,
		func(v Vec3) bool { return v.X <= rect[1] },
		func(s, e Vec3) Vec3 { return interpolateAtX(s, e, rect[1]) })
	poly = clipHalfPlane(poly,
		func(v Vec3) bool { return v.Y >= rect[2] },
		func(s, e Vec3) Vec3 { return interpolateAtY(s, e, rect[2]) })
	poly = clipHalfPlane(poly,
		func(v Vec3) bool { return v.Y <= rect[3] },
		func(s, e Vec3) Vec3 { return interpolateAtY(s, e, rect[3]) })
	return cleanPolygon(poly)
}

func clipHalfPlane(poly []Vec3, inside func(Vec3) bool, intersect func(Vec3, Vec3) Vec3) []Vec3 {
	if len(poly) == 0 {
		return nil
	}
	out := make([]Vec3, 0, len(poly)+1)
	prev := poly[len(poly)-1]
	prevInside := inside(prev)
	for _, cur := range poly {
		curInside := inside(cur)
		if curInside {
			if !prevInside {
				out = append(out, intersect(prev, cur))
			}
			out = append(out, cur)
		} else if prevInside {
			out = append(out, intersect(prev, cur))
		}
		prev = cur
		prevInside = curInside
	}
	return out
}

func interpolateAtX(s, e Vec3, x float64) Vec3 {
	dx := e.X - s.X
	if dx == 0 {
		return Vec3{X: x, Y: s.Y, Z: s.Z}
	}
	t := (x - s.X) / dx
	return Vec3{
		X: x,
		Y: s.Y + t*(e.Y-s.Y),
		Z: s.Z + t*(e.Z-s.Z),
	}
}

func interpolateAtY(s, e Vec3, y float64) Vec3 {
	dy := e.Y - s.Y
	if dy == 0 {
		return Vec3{X: s.X, Y: y, Z: s.Z}
	}
	t := (y - s.Y) / dy
	return Vec3{
		X: s.X + t*(e.X-s.X),
		Y: y,
		Z: s.Z + t*(e.Z-s.Z),
	}
}

func cleanPolygon(poly []Vec3) []Vec3 {
	if len(poly) == 0 {
		return nil
	}
	out := make([]Vec3, 0, len(poly))
	for _, v := range poly {
		v = normalizeVec3(v)
		if len(out) == 0 || !sameVec3(out[len(out)-1], v) {
			out = append(out, v)
		}
	}
	if len(out) > 1 && sameVec3(out[0], out[len(out)-1]) {
		out = out[:len(out)-1]
	}
	if len(out) < 3 {
		return nil
	}
	return out
}

func sameVec3(a, b Vec3) bool {
	return a.X == b.X && a.Y == b.Y && a.Z == b.Z
}

func polygonArea(poly []Vec3) float64 {
	if len(poly) < 3 {
		return 0
	}
	sum := 0.0
	for i := range poly {
		j := (i + 1) % len(poly)
		sum += poly[i].X*poly[j].Y - poly[j].X*poly[i].Y
	}
	return math.Abs(sum) / 2
}

func (p *OccluderBakePlanner) coverageLocked(cell CellID, state *cellState) float64 {
	area := float64(p.options.CellSize) * p.options.CellSize
	if area <= 0 || math.IsNaN(area) || math.IsInf(area, 0) {
		return 0
	}
	cands := sortedCandidates(state)
	polys := make([][]Vec3, 0, len(cands))
	for _, cand := range cands {
		polys = append(polys, cand.poly)
	}
	covered := unionArea(polys, cell, p.options.CellSize)
	coverage := covered / area
	if coverage < 0 {
		return 0
	}
	if coverage > 1 {
		return 1
	}
	return coverage
}

type segment struct {
	a Vec3
	b Vec3
}

func unionArea(polys [][]Vec3, cell CellID, size float64) float64 {
	if len(polys) == 0 || size <= 0 {
		return 0
	}
	rect := cellRectFor(size, cell)
	xs := []float64{rect[0], rect[1]}
	segments := make([]segment, 0)
	for _, poly := range polys {
		for i := range poly {
			xs = append(xs, poly[i].X)
			segments = append(segments, segment{a: poly[i], b: poly[(i+1)%len(poly)]})
		}
	}

	for i := 0; i < len(segments); i++ {
		for j := i + 1; j < len(segments); j++ {
			if x, ok := segmentIntersectionX(segments[i], segments[j]); ok {
				xs = append(xs, x)
			}
		}
	}
	sort.Float64s(xs)
	xs = dedupeSortedFloats(xs, math.Max(1, math.Abs(size))*1e-12)

	total := 0.0
	for i := 0; i+1 < len(xs); i++ {
		left, right := xs[i], xs[i+1]
		width := right - left
		if width <= 0 {
			continue
		}
		mid := left + width/2
		intervals := make([][2]float64, 0, len(polys))
		for _, poly := range polys {
			lo, hi, ok := polygonIntervalAtX(poly, mid)
			if ok && hi > lo {
				intervals = append(intervals, [2]float64{lo, hi})
			}
		}
		if len(intervals) == 0 {
			continue
		}
		sort.Slice(intervals, func(i, j int) bool {
			if intervals[i][0] != intervals[j][0] {
				return intervals[i][0] < intervals[j][0]
			}
			return intervals[i][1] < intervals[j][1]
		})
		lo, hi := intervals[0][0], intervals[0][1]
		length := 0.0
		for _, interval := range intervals[1:] {
			if interval[0] > hi {
				length += hi - lo
				lo, hi = interval[0], interval[1]
			} else if interval[1] > hi {
				hi = interval[1]
			}
		}
		length += hi - lo
		total += width * length
	}
	return total
}

func dedupeSortedFloats(values []float64, tolerance float64) []float64 {
	if len(values) == 0 {
		return nil
	}
	out := []float64{values[0]}
	for _, v := range values[1:] {
		if v-out[len(out)-1] > tolerance {
			out = append(out, v)
		}
	}
	return out
}

func segmentIntersectionX(s1, s2 segment) (float64, bool) {
	rx := s1.b.X - s1.a.X
	ry := s1.b.Y - s1.a.Y
	sx := s2.b.X - s2.a.X
	sy := s2.b.Y - s2.a.Y
	denom := rx*sy - ry*sx
	if denom == 0 || math.IsNaN(denom) || math.IsInf(denom, 0) {
		return 0, false
	}
	qpx := s2.a.X - s1.a.X
	qpy := s2.a.Y - s1.a.Y
	t := (qpx*sy - qpy*sx) / denom
	u := (qpx*ry - qpy*rx) / denom
	if t < 0 || t > 1 || u < 0 || u > 1 {
		return 0, false
	}
	return s1.a.X + t*rx, true
}

func polygonIntervalAtX(poly []Vec3, x float64) (float64, float64, bool) {
	if len(poly) < 3 {
		return 0, 0, false
	}
	lo := math.Inf(1)
	hi := math.Inf(-1)
	for i := range poly {
		a := poly[i]
		b := poly[(i+1)%len(poly)]
		if (a.X <= x && b.X >= x) || (b.X <= x && a.X >= x) {
			if a.X == b.X {
				continue
			}
			t := (x - a.X) / (b.X - a.X)
			y := a.Y + t*(b.Y-a.Y)
			if y < lo {
				lo = y
			}
			if y > hi {
				hi = y
			}
		}
	}
	if math.IsInf(lo, 1) || math.IsInf(hi, -1) || hi <= lo {
		return 0, 0, false
	}
	return lo, hi, true
}

func hashCell(cell CellID, cands []*candidate) string {
	h := sha256.New()
	writeUint64(h, math.Float64bits(1))
	writeInt64(h, int64(cell.X))
	writeInt64(h, int64(cell.Y))
	writeUint64(h, uint64(len(cands)))
	for _, cand := range cands {
		writeString(h, cand.key.Material)
		writeVec3(h, cand.key.A)
		writeVec3(h, cand.key.B)
		writeVec3(h, cand.key.C)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func writeUint64(h hash.Hash, v uint64) {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], v)
	_, _ = h.Write(buf[:])
}

func writeInt64(h hash.Hash, v int64) {
	writeUint64(h, uint64(v))
}

func writeString(h hash.Hash, v string) {
	writeUint64(h, uint64(len(v)))
	_, _ = h.Write([]byte(v))
}

func writeVec3(h hash.Hash, v Vec3) {
	writeUint64(h, floatBits(v.X))
	writeUint64(h, floatBits(v.Y))
	writeUint64(h, floatBits(v.Z))
}

func floatBits(v float64) uint64 {
	return math.Float64bits(normalizeZero(v))
}
