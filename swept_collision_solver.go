// Package task performs continuous collision detection for fast moving
// bodies: swept box queries, substep splitting and rollback to the last legal
// position.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
)

// Solver anchors.
const (
	DefaultEpsilon      = 1e-6
	DefaultMaxSubsteps  = 16
	DefaultMinThickness = 0.5
	DefaultMaxBodies    = 1024
)

// Numerical tolerances. They only decide how near a floating point value may
// be to a mathematical boundary before it is treated as lying on it.
const (
	contactTieEpsilon = 1e-9
	slabEpsilon       = 1e-9
	substepRoundSlack = 1e-12
)

// Errors returned by the solver.
var (
	ErrBadOptions    = errors.New("options are out of range")
	ErrBlankID       = errors.New("body id is required")
	ErrBadBody       = errors.New("body half extents must be positive")
	ErrDuplicateBody = errors.New("body id already exists")
	ErrBodyLimit     = errors.New("body limit reached")
	ErrUnknownBody   = errors.New("unknown body")
	ErrStaticBody    = errors.New("static body cannot move")
	ErrBadTime       = errors.New("time step must be positive")
	ErrBadAABB       = errors.New("aabb min must not exceed max")
	ErrBadSnapshot   = errors.New("snapshot payload is invalid")
)

// Vec3 is a position, half extent, normal or delta in world units.
type Vec3 struct {
	X float64
	Y float64
	Z float64
}

// AABB is an axis aligned box.
type AABB struct {
	Min Vec3
	Max Vec3
}

// Body is one collidable box. Static bodies never move.
type Body struct {
	ID     string
	Pos    Vec3
	Half   Vec3
	Vel    Vec3
	Static bool
}

// Hit describes the first contact of a sweep.
type Hit struct {
	BodyID string
	T      float64
	Normal Vec3
	Point  Vec3
}

// MoveResult reports where a body ended up after one frame.
type MoveResult struct {
	ID       string
	Pos      Vec3
	Blocked  bool
	Substeps int
	Normal   Vec3
}

// Options bounds the solver.
type Options struct {
	Epsilon      float64
	MaxSubsteps  int
	MinThickness float64
	MaxBodies    int
}

type bodyState struct {
	ID      string
	Pos     Vec3
	Half    Vec3
	Vel     Vec3
	Static  bool
	PrevPos Vec3
	Blocked bool

	// moveMu serialises moves (and snapshot copies) of this one body. It is
	// always acquired before the solver wide mu, never afterwards.
	moveMu sync.Mutex
}

// colliderView is an immutable copy of a body used while doing lock free
// sweep math.
type colliderView struct {
	ID   string
	Pos  Vec3
	Half Vec3
}

// SweptCollisionSolver owns the body table and the sweep helpers.
type SweptCollisionSolver struct {
	mu     sync.RWMutex
	opts   Options
	bodies map[string]*bodyState
	order  []string
}

// NewSweptCollisionSolver builds a solver, filling defaults for zero options.
func NewSweptCollisionSolver(opts Options) *SweptCollisionSolver {
	if opts.Epsilon <= 0 || math.IsNaN(opts.Epsilon) {
		opts.Epsilon = DefaultEpsilon
	}
	if opts.MaxSubsteps <= 0 {
		opts.MaxSubsteps = DefaultMaxSubsteps
	}
	if opts.MinThickness <= 0 || math.IsNaN(opts.MinThickness) {
		opts.MinThickness = DefaultMinThickness
	}
	if opts.MaxBodies <= 0 {
		opts.MaxBodies = DefaultMaxBodies
	}
	return &SweptCollisionSolver{
		opts:   opts,
		bodies: make(map[string]*bodyState),
	}
}

// AddBody registers one body.
func (s *SweptCollisionSolver) AddBody(b Body) error {
	if strings.TrimSpace(b.ID) == "" {
		return ErrBlankID
	}
	if !(b.Half.X > 0 && b.Half.Y > 0 && b.Half.Z > 0) {
		return ErrBadBody
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.bodies[b.ID]; exists {
		return ErrDuplicateBody
	}
	if len(s.order) >= s.opts.MaxBodies {
		return ErrBodyLimit
	}

	state := &bodyState{
		ID:      b.ID,
		Pos:     b.Pos,
		Half:    b.Half,
		Vel:     b.Vel,
		Static:  b.Static,
		PrevPos: b.Pos,
		Blocked: false,
	}
	s.bodies[b.ID] = state
	s.order = append(s.order, b.ID)
	return nil
}

// RemoveBody drops one body from the table.
func (s *SweptCollisionSolver) RemoveBody(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.bodies[id]; !ok {
		return ErrUnknownBody
	}
	delete(s.bodies, id)
	for i, existing := range s.order {
		if existing == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	return nil
}

// Move advances one body by delta over dtMs milliseconds.
func (s *SweptCollisionSolver) Move(id string, delta Vec3, dtMs int) (MoveResult, error) {
	fail := func(err error) (MoveResult, error) {
		return MoveResult{ID: id}, err
	}

	s.mu.RLock()
	state, ok := s.bodies[id]
	static := ok && state.Static
	s.mu.RUnlock()

	if !ok {
		return fail(fmt.Errorf("%w: %q", ErrUnknownBody, id))
	}
	if static {
		return fail(fmt.Errorf("%w: %q", ErrStaticBody, id))
	}
	if dtMs <= 0 {
		return fail(fmt.Errorf("%w: dtMs=%d", ErrBadTime, dtMs))
	}

	// Same body moves are serialised. Different bodies use different locks.
	state.moveMu.Lock()
	defer state.moveMu.Unlock()

	eps := s.opts.Epsilon

	s.mu.RLock()
	if _, stillPresent := s.bodies[id]; !stillPresent {
		s.mu.RUnlock()
		return fail(fmt.Errorf("%w: %q", ErrUnknownBody, id))
	}
	start := state.Pos
	half := state.Half
	lastLegal := state.PrevPos
	s.mu.RUnlock()

	// Velocity is recorded in world units per second. Blocking never zeroes
	// it: the next frame retries with the same intended motion.
	dtSeconds := float64(dtMs) / 1000.0
	velocity := scaleVec(delta, 1.0/dtSeconds)

	distance := lengthVec(delta)
	thickness := 2.0 * minFloat3(half.X, half.Y, half.Z)
	substeps := s.Substeps(distance, thickness)

	// Static colliders are copied once; all intersection math runs without
	// the solver lock so queries and other bodies are not blocked.
	boxes := s.colliders(true)

	current := start
	blocked := false
	normal := Vec3{}
	used := 0
	contactFound := false

	for i := 0; i < substeps; i++ {
		used++
		f0 := float64(i) / float64(substeps)
		f1 := float64(i+1) / float64(substeps)

		// Anchor every endpoint to the frame start instead of accumulating
		// additions, so different frame rates land on the same point.
		p0 := addVec(start, scaleVec(delta, f0))
		p1 := addVec(start, scaleVec(delta, f1))

		from := aabbAt(p0, half)
		to := aabbAt(p1, half)
		hit, hitNow := sweepColliders(from, to, boxes)
		if !hitNow {
			current = p1
			continue
		}

		contactFound = true
		blocked = true
		normal = hit.Normal

		globalT := f0 + (f1-f0)*hit.T
		toi := addVec(start, scaleVec(delta, globalT))
		// Stop one epsilon onto the free side of the contact normal.
		current = addVec(toi, scaleVec(hit.Normal, eps))
		break
	}

	if !blocked {
		// Single rounding for the full frame displacement.
		current = addVec(start, delta)
	}

	// Safety net: if the settled box still penetrates a static box (degenerate
	// initial overlap or numerical trouble), revert to the last legal
	// checkpoint instead of shipping a half embedded body. Velocity is kept.
	if aabbOverlapsAny(aabbAt(current, half), boxes) {
		current = lastLegal
		blocked = true
		if !contactFound {
			normal = penetrationNormal(current, half, boxes)
		}
	}

	s.mu.Lock()
	state.Pos = current
	state.Vel = velocity
	state.Blocked = blocked
	if !aabbOverlapsAny(aabbAt(current, half), boxes) {
		// Every legally settled move advances the checkpoint.
		state.PrevPos = current
	}
	s.mu.Unlock()

	return MoveResult{
		ID:       id,
		Pos:      current,
		Blocked:  blocked,
		Substeps: used,
		Normal:   normal,
	}, nil
}

// Sweep returns the earliest contact between the swept box and a body.
func (s *SweptCollisionSolver) Sweep(from, to AABB, dtMs int) (Hit, bool, error) {
	if err := validateAABB(from); err != nil {
		return Hit{}, false, err
	}
	if err := validateAABB(to); err != nil {
		return Hit{}, false, err
	}
	if dtMs <= 0 {
		return Hit{}, false, fmt.Errorf("%w: dtMs=%d", ErrBadTime, dtMs)
	}

	boxes := s.colliders(false)
	hit, ok := sweepColliders(from, to, boxes)
	if !ok {
		return Hit{}, false, nil
	}
	return hit, true, nil
}

// Substeps returns how many substeps a move needs at the given thickness.
func (s *SweptCollisionSolver) Substeps(distance, minThickness float64) int {
	maxSubsteps := s.opts.MaxSubsteps
	if maxSubsteps <= 0 {
		maxSubsteps = DefaultMaxSubsteps
	}
	if distance <= 0 || math.IsNaN(distance) {
		return 1
	}
	if minThickness <= 0 || math.IsNaN(minThickness) {
		minThickness = s.opts.MinThickness
	}

	stepLength := minThickness * 0.5
	if stepLength <= 0 || math.IsNaN(stepLength) {
		return maxSubsteps
	}

	ratio := distance / stepLength
	if math.IsInf(ratio, 1) || ratio >= float64(maxSubsteps) {
		return maxSubsteps
	}
	// Pull slightly down before ceiling so an exact integer ratio computed
	// with a rounding error does not cost one extra substep.
	n := int(math.Ceil(ratio - substepRoundSlack*math.Max(1.0, math.Abs(ratio))))
	if n < 1 {
		return 1
	}
	if n > maxSubsteps {
		return maxSubsteps
	}
	return n
}

// Body returns a copy of one registered body.
func (s *SweptCollisionSolver) Body(id string) (Body, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state, ok := s.bodies[id]
	if !ok {
		return Body{}, false
	}
	return Body{
		ID:     state.ID,
		Pos:    state.Pos,
		Half:   state.Half,
		Vel:    state.Vel,
		Static: state.Static,
	}, true
}

// Position returns the current position of one body.
func (s *SweptCollisionSolver) Position(id string) (Vec3, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state, ok := s.bodies[id]
	if !ok {
		return Vec3{}, false
	}
	return state.Pos, true
}

// Velocity returns the last recorded velocity of one body.
func (s *SweptCollisionSolver) Velocity(id string) (Vec3, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state, ok := s.bodies[id]
	if !ok {
		return Vec3{}, false
	}
	return state.Vel, true
}

// Blocked reports whether the last move of a body was stopped this frame.
func (s *SweptCollisionSolver) Blocked(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state, ok := s.bodies[id]
	if !ok {
		return false
	}
	return state.Blocked
}

// snapshotBody is the serialisable form of one bodyState.
type snapshotBody struct {
	ID      string `json:"id"`
	Pos     Vec3   `json:"pos"`
	Half    Vec3   `json:"half"`
	Vel     Vec3   `json:"vel"`
	PrevPos Vec3   `json:"prevPos"`
	Static  bool   `json:"static"`
	Blocked bool   `json:"blocked"`
}

type snapshotEnvelope struct {
	Format  string         `json:"format"`
	Version int            `json:"version"`
	Bodies  []snapshotBody `json:"bodies"`
}

const snapshotFormatName = "gsb-swept-collision-solver"

// Snapshot serialises positions, velocities and the last legal positions.
func (s *SweptCollisionSolver) Snapshot() ([]byte, error) {
	// Stable pointer list first; bodies added later are simply not part of
	// this snapshot.
	s.mu.RLock()
	states := make([]*bodyState, 0, len(s.order))
	for _, id := range s.order {
		states = append(states, s.bodies[id])
	}
	s.mu.RUnlock()

	bodies := make([]snapshotBody, 0, len(states))
	for _, state := range states {
		// Serialised with this body's Move: a snapshot never observes a
		// half committed frame.
		state.moveMu.Lock()
		s.mu.RLock()
		bodies = append(bodies, snapshotBody{
			ID:      state.ID,
			Pos:     state.Pos,
			Half:    state.Half,
			Vel:     state.Vel,
			PrevPos: state.PrevPos,
			Static:  state.Static,
			Blocked: state.Blocked,
		})
		s.mu.RUnlock()
		state.moveMu.Unlock()
	}

	payload := snapshotEnvelope{
		Format:  snapshotFormatName,
		Version: 1,
		Bodies:  bodies,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	return data, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (s *SweptCollisionSolver) Restore(data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("%w: empty payload", ErrBadSnapshot)
	}

	var payload snapshotEnvelope
	if err := json.Unmarshal(data, &payload); err != nil {
		return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	if payload.Format != snapshotFormatName || payload.Version != 1 {
		return fmt.Errorf("%w: unrecognised format", ErrBadSnapshot)
	}

	// Build and validate the replacement off line so any failure leaves the
	// live solver untouched.
	next := make(map[string]*bodyState, len(payload.Bodies))
	order := make([]string, 0, len(payload.Bodies))
	for _, item := range payload.Bodies {
		if strings.TrimSpace(item.ID) == "" {
			return fmt.Errorf("%w: blank body id", ErrBadSnapshot)
		}
		if !(item.Half.X > 0 && item.Half.Y > 0 && item.Half.Z > 0) {
			return fmt.Errorf("%w: bad half extents for %q", ErrBadSnapshot, item.ID)
		}
		if _, dup := next[item.ID]; dup {
			return fmt.Errorf("%w: duplicate body %q", ErrBadSnapshot, item.ID)
		}
		state := &bodyState{
			ID:      item.ID,
			Pos:     item.Pos,
			Half:    item.Half,
			Vel:     item.Vel,
			Static:  item.Static,
			PrevPos: item.PrevPos,
			Blocked: item.Blocked,
		}
		if state.PrevPos == (Vec3{}) && state.Pos != (Vec3{}) {
			// Snapshots produced by this build always carry PrevPos; keep
			// older/hand crafted payloads usable.
			state.PrevPos = state.Pos
		}
		next[item.ID] = state
		order = append(order, item.ID)
	}

	maxBodies := s.opts.MaxBodies
	if maxBodies > 0 && len(order) > maxBodies {
		return fmt.Errorf("%w: %d bodies exceed limit %d", ErrBadSnapshot, len(order), maxBodies)
	}

	s.mu.Lock()
	s.bodies = next
	s.order = order
	s.mu.Unlock()
	return nil
}

// colliders copies the current body table for lock free sweep math.
func (s *SweptCollisionSolver) colliders(staticOnly bool) []colliderView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]colliderView, 0, len(s.order))
	for _, id := range s.order {
		state := s.bodies[id]
		if staticOnly && !state.Static {
			continue
		}
		out = append(out, colliderView{ID: state.ID, Pos: state.Pos, Half: state.Half})
	}
	return out
}

// sweepColliders casts the from box along its center displacement to the
// center of to and returns the globally earliest contact. Ties are resolved
// by lexicographic normal and then body id, never by map order.
func sweepColliders(from, to AABB, boxes []colliderView) (Hit, bool) {
	center := aabbCenter(from)
	half := aabbHalf(from)
	delta := subVec(aabbCenter(to), center)

	type candidate struct {
		hit Hit
		t   float64
	}
	candidates := make([]candidate, 0, len(boxes))
	earliest := math.Inf(1)

	for _, box := range boxes {
		hit, ok := sweepOneBox(center, half, delta, box)
		if !ok {
			continue
		}
		candidates = append(candidates, candidate{hit: hit, t: hit.T})
		if hit.T < earliest {
			earliest = hit.T
		}
	}

	if len(candidates) == 0 {
		return Hit{}, false
	}

	// Among every contact sitting in the earliest time window, pick the
	// lexicographically smallest normal. The explicit comparison keeps the
	// result independent of (randomised) body table iteration order.
	var best Hit
	haveBest := false
	for _, cand := range candidates {
		if math.Abs(cand.t-earliest) > contactTieEpsilon {
			continue
		}
		if !haveBest {
			best = cand.hit
			haveBest = true
			continue
		}
		if lessNormal(cand.hit.Normal, best.Normal) ||
			(cand.hit.Normal == best.Normal && cand.hit.BodyID < best.BodyID) {
			best = cand.hit
		}
	}
	if !haveBest {
		return Hit{}, false
	}
	return best, true
}

// sweepOneBox runs the slab test against one box expanded by the moving
// half extents.
func sweepOneBox(center, half, delta Vec3, box colliderView) (Hit, bool) {
	mover := [3]float64{center.X, center.Y, center.Z}
	move := [3]float64{delta.X, delta.Y, delta.Z}
	lo := [3]float64{
		box.Pos.X - box.Half.X - half.X,
		box.Pos.Y - box.Half.Y - half.Y,
		box.Pos.Z - box.Half.Z - half.Z,
	}
	hi := [3]float64{
		box.Pos.X + box.Half.X + half.X,
		box.Pos.Y + box.Half.Y + half.Y,
		box.Pos.Z + box.Half.Z + half.Z,
	}

	tNear := math.Inf(-1)
	tFar := math.Inf(1)
	normal := Vec3{}

	for axis := 0; axis < 3; axis++ {
		speed := move[axis]
		if speed == 0 {
			// Sweeping parallel to this slab: the center must stay inside
			// the expanded interval for contact to ever be possible.
			if mover[axis] < lo[axis] || mover[axis] > hi[axis] {
				return Hit{}, false
			}
			continue
		}

		tLow := (lo[axis] - mover[axis]) / speed
		tHigh := (hi[axis] - mover[axis]) / speed
		tEntry := math.Min(tLow, tHigh)
		tExit := math.Max(tLow, tHigh)

		entryNormal := axisNormal(axis, -signFloat(speed))
		if tEntry > tNear {
			tNear = tEntry
			normal = entryNormal
		} else if nearlyEqual(tEntry, tNear, slabEpsilon) && lessNormal(entryNormal, normal) {
			// Corner entry on this same box: keep the first normal in
			// lexicographic order.
			normal = entryNormal
		}

		if tExit < tFar {
			tFar = tExit
		}
		if tNear > tFar+slabEpsilon {
			return Hit{}, false
		}
	}

	if tFar < -slabEpsilon {
		return Hit{}, false
	}
	if tNear > 1.0+slabEpsilon {
		return Hit{}, false
	}

	initialOverlap := tNear < -slabEpsilon
	t := tNear
	if t < 0 {
		t = 0
	}
	if t > 1 {
		if t <= 1+slabEpsilon {
			t = 1
		} else {
			return Hit{}, false
		}
	}

	contactCenter := addVec(center, scaleVec(delta, t))
	if initialOverlap {
		normal = penetrationNormal(contactCenter, half, []colliderView{box})
	}

	return Hit{
		BodyID: box.ID,
		T:      t,
		Normal: normal,
		Point:  contactPoint(contactCenter, box),
	}, true
}

// contactPoint returns a deterministic representative contact point: the
// closest point on the static box to the moving center at time of impact.
func contactPoint(center Vec3, box colliderView) Vec3 {
	return Vec3{
		X: clampFloat(center.X, box.Pos.X-box.Half.X, box.Pos.X+box.Half.X),
		Y: clampFloat(center.Y, box.Pos.Y-box.Half.Y, box.Pos.Y+box.Half.Y),
		Z: clampFloat(center.Z, box.Pos.Z-box.Half.Z, box.Pos.Z+box.Half.Z),
	}
}

// penetrationNormal picks the axis of least penetration for overlapping
// boxes. It is only used for degenerate starts inside a collider.
func penetrationNormal(center, half Vec3, boxes []colliderView) Vec3 {
	bestNormal := Vec3{}
	bestDepth := math.Inf(1)
	found := false
	for _, box := range boxes {
		offsets := [3]float64{
			center.X - box.Pos.X,
			center.Y - box.Pos.Y,
			center.Z - box.Pos.Z,
		}
		overlaps := [3]float64{
			half.X + box.Half.X - math.Abs(offsets[0]),
			half.Y + box.Half.Y - math.Abs(offsets[1]),
			half.Z + box.Half.Z - math.Abs(offsets[2]),
		}
		for axis := 0; axis < 3; axis++ {
			if overlaps[axis] <= 0 {
				continue
			}
			sign := 1.0
			if offsets[axis] < 0 || (offsets[axis] == 0 && axis == 0) {
				sign = -1.0
			}
			depth := overlaps[axis]
			n := axisNormal(axis, sign)
			if depth < bestDepth-slabEpsilon ||
				(nearlyEqual(depth, bestDepth, slabEpsilon) && lessNormal(n, bestNormal)) {
				bestDepth = depth
				bestNormal = n
				found = true
			}
		}
	}
	if !found {
		return Vec3{X: -1}
	}
	return bestNormal
}

// aabbOverlapsAny reports a strict interior intersection; touching faces do
// not count as penetration.
func aabbOverlapsAny(a AABB, boxes []colliderView) bool {
	for _, box := range boxes {
		bMin := subVec(box.Pos, box.Half)
		bMax := addVec(box.Pos, box.Half)
		if a.Min.X < bMax.X && a.Max.X > bMin.X &&
			a.Min.Y < bMax.Y && a.Max.Y > bMin.Y &&
			a.Min.Z < bMax.Z && a.Max.Z > bMin.Z {
			return true
		}
	}
	return false
}

func validateAABB(a AABB) error {
	if a.Min.X > a.Max.X || a.Min.Y > a.Max.Y || a.Min.Z > a.Max.Z {
		return fmt.Errorf("%w: min=%+v max=%+v", ErrBadAABB, a.Min, a.Max)
	}
	return nil
}

func aabbAt(pos, half Vec3) AABB {
	return AABB{Min: subVec(pos, half), Max: addVec(pos, half)}
}

func aabbCenter(a AABB) Vec3 {
	return Vec3{
		X: (a.Min.X + a.Max.X) * 0.5,
		Y: (a.Min.Y + a.Max.Y) * 0.5,
		Z: (a.Min.Z + a.Max.Z) * 0.5,
	}
}

func aabbHalf(a AABB) Vec3 {
	return Vec3{
		X: (a.Max.X - a.Min.X) * 0.5,
		Y: (a.Max.Y - a.Min.Y) * 0.5,
		Z: (a.Max.Z - a.Min.Z) * 0.5,
	}
}

func addVec(a, b Vec3) Vec3 {
	return Vec3{X: a.X + b.X, Y: a.Y + b.Y, Z: a.Z + b.Z}
}

func subVec(a, b Vec3) Vec3 {
	return Vec3{X: a.X - b.X, Y: a.Y - b.Y, Z: a.Z - b.Z}
}

func scaleVec(a Vec3, s float64) Vec3 {
	return Vec3{X: a.X * s, Y: a.Y * s, Z: a.Z * s}
}

func lengthVec(a Vec3) float64 {
	return math.Sqrt(a.X*a.X + a.Y*a.Y + a.Z*a.Z)
}

func minFloat3(a, b, c float64) float64 {
	return math.Min(a, math.Min(b, c))
}

func signFloat(v float64) float64 {
	if v < 0 {
		return -1
	}
	if v > 0 {
		return 1
	}
	return 0
}

func axisNormal(axis int, sign float64) Vec3 {
	var n Vec3
	switch axis {
	case 0:
		n.X = sign
	case 1:
		n.Y = sign
	default:
		n.Z = sign
	}
	return n
}

// lessNormal defines the deterministic order: X, then Y, then Z.
func lessNormal(a, b Vec3) bool {
	if a.X != b.X {
		return a.X < b.X
	}
	if a.Y != b.Y {
		return a.Y < b.Y
	}
	return a.Z < b.Z
}

func nearlyEqual(a, b, tolerance float64) bool {
	return math.Abs(a-b) <= tolerance
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
