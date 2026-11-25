// Package task performs continuous collision detection for fast moving
// bodies: swept box queries, substep splitting and rollback to the last legal
// position.
package task

import (
	"errors"
	"sync"
)

var errNotImplemented = errors.New("not implemented")

// Solver anchors.
const (
	DefaultEpsilon      = 1e-6
	DefaultMaxSubsteps  = 16
	DefaultMinThickness = 0.5
	DefaultMaxBodies    = 1024
)

// Errors returned by the solver.
var (
	ErrBadOptions  = errors.New("options are out of range")
	ErrBlankID     = errors.New("body id is required")
	ErrBadBody     = errors.New("body half extents must be positive")
	ErrDuplicateBody = errors.New("body id already exists")
	ErrBodyLimit   = errors.New("body limit reached")
	ErrUnknownBody = errors.New("unknown body")
	ErrStaticBody  = errors.New("static body cannot move")
	ErrBadTime     = errors.New("time step must be positive")
	ErrBadAABB     = errors.New("aabb min must not exceed max")
	ErrBadSnapshot = errors.New("snapshot payload is invalid")
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
	ID       string
	Pos      Vec3
	Half     Vec3
	Vel      Vec3
	Static   bool
	PrevPos  Vec3
	Blocked  bool
}

// SweptCollisionSolver owns the body table and the sweep helpers.
type SweptCollisionSolver struct {
	mu     sync.Mutex
	opts   Options
	bodies map[string]*bodyState
	order  []string
}

// NewSweptCollisionSolver builds a solver, filling defaults for zero options.
func NewSweptCollisionSolver(opts Options) *SweptCollisionSolver {
	return &SweptCollisionSolver{
		opts:   opts,
		bodies: make(map[string]*bodyState),
	}
}

// AddBody registers one body.
func (s *SweptCollisionSolver) AddBody(b Body) error {
	return errNotImplemented
}

// RemoveBody drops one body from the table.
func (s *SweptCollisionSolver) RemoveBody(id string) error {
	return errNotImplemented
}

// Move advances one body by delta over dtMs milliseconds.
func (s *SweptCollisionSolver) Move(id string, delta Vec3, dtMs int) (MoveResult, error) {
	return MoveResult{}, errNotImplemented
}

// Sweep returns the earliest contact between the swept box and a body.
func (s *SweptCollisionSolver) Sweep(from, to AABB, dtMs int) (Hit, bool, error) {
	return Hit{}, false, errNotImplemented
}

// Substeps returns how many substeps a move needs at the given thickness.
func (s *SweptCollisionSolver) Substeps(distance, minThickness float64) int {
	return 0
}

// Body returns a copy of one registered body.
func (s *SweptCollisionSolver) Body(id string) (Body, bool) {
	return Body{}, false
}

// Position returns the current position of one body.
func (s *SweptCollisionSolver) Position(id string) (Vec3, bool) {
	return Vec3{}, false
}

// Velocity returns the last recorded velocity of one body.
func (s *SweptCollisionSolver) Velocity(id string) (Vec3, bool) {
	return Vec3{}, false
}

// Blocked reports whether the last move of a body was stopped this frame.
func (s *SweptCollisionSolver) Blocked(id string) bool {
	return false
}

// Snapshot serialises positions, velocities and the last legal positions.
func (s *SweptCollisionSolver) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (s *SweptCollisionSolver) Restore(data []byte) error {
	return errNotImplemented
}
