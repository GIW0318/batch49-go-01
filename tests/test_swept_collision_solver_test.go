package task

import (
	"errors"
	"math"
	"testing"

	impl "gsb/batch49-go-game-a-13"
)

func solverOptions() impl.Options {
	return impl.Options{
		Epsilon:      1e-6,
		MaxSubsteps:  16,
		MinThickness: 0.5,
		MaxBodies:    16,
	}
}

func TestAddBodyStoresBodyAndPosition(t *testing.T) {
	s := impl.NewSweptCollisionSolver(solverOptions())
	body := impl.Body{ID: "player", Pos: impl.Vec3{X: -3}, Half: impl.Vec3{X: 0.5, Y: 0.5, Z: 0.5}}
	if err := s.AddBody(body); err != nil {
		t.Fatalf("add body failed: %v", err)
	}
	got, ok := s.Body("player")
	if !ok {
		t.Fatalf("added body must be readable")
	}
	if got.ID != "player" || math.Abs(got.Pos.X-(-3)) > 1e-9 {
		t.Fatalf("stored body = %+v, want id player at x -3", got)
	}
	pos, ok := s.Position("player")
	if !ok || math.Abs(pos.X-(-3)) > 1e-9 {
		t.Fatalf("position = %+v ok=%v, want x -3", pos, ok)
	}
}

func TestAddBodyRejectsInvalidInput(t *testing.T) {
	s := impl.NewSweptCollisionSolver(solverOptions())
	if err := s.AddBody(impl.Body{ID: "  ", Half: impl.Vec3{X: 1, Y: 1, Z: 1}}); !errors.Is(err, impl.ErrBlankID) {
		t.Fatalf("blank id must be rejected, got %v", err)
	}
	if err := s.AddBody(impl.Body{ID: "flat", Half: impl.Vec3{X: 0, Y: 1, Z: 1}}); !errors.Is(err, impl.ErrBadBody) {
		t.Fatalf("zero half extent must be rejected, got %v", err)
	}
	if err := s.AddBody(impl.Body{ID: "negative", Half: impl.Vec3{X: 1, Y: -2, Z: 1}}); !errors.Is(err, impl.ErrBadBody) {
		t.Fatalf("negative half extent must be rejected, got %v", err)
	}
}

func TestAddBodyRejectsDuplicateID(t *testing.T) {
	s := impl.NewSweptCollisionSolver(solverOptions())
	body := impl.Body{ID: "crate", Half: impl.Vec3{X: 1, Y: 1, Z: 1}}
	if err := s.AddBody(body); err != nil {
		t.Fatalf("first insert failed: %v", err)
	}
	if err := s.AddBody(body); !errors.Is(err, impl.ErrDuplicateBody) {
		t.Fatalf("duplicate id must be rejected, got %v", err)
	}
}

func TestAddBodyEnforcesCapacity(t *testing.T) {
	opts := solverOptions()
	opts.MaxBodies = 2
	s := impl.NewSweptCollisionSolver(opts)
	for _, id := range []string{"a", "b"} {
		if err := s.AddBody(impl.Body{ID: id, Half: impl.Vec3{X: 1, Y: 1, Z: 1}}); err != nil {
			t.Fatalf("insert %s failed: %v", id, err)
		}
	}
	if err := s.AddBody(impl.Body{ID: "c", Half: impl.Vec3{X: 1, Y: 1, Z: 1}}); !errors.Is(err, impl.ErrBodyLimit) {
		t.Fatalf("third body must exceed the capacity, got %v", err)
	}
}

func TestSubstepsSplitsByHalfThickness(t *testing.T) {
	s := impl.NewSweptCollisionSolver(solverOptions())
	if got := s.Substeps(10, 4); got != 5 {
		t.Fatalf("Substeps(10, 4) = %d, want 5", got)
	}
	if got := s.Substeps(1, 4); got != 1 {
		t.Fatalf("Substeps(1, 4) = %d, want 1", got)
	}
	if got := s.Substeps(0, 4); got != 1 {
		t.Fatalf("Substeps(0, 4) = %d, want 1", got)
	}
}

func TestSubstepsHonorsCap(t *testing.T) {
	opts := solverOptions()
	opts.MaxSubsteps = 4
	s := impl.NewSweptCollisionSolver(opts)
	if got := s.Substeps(100, 2); got != 4 {
		t.Fatalf("Substeps(100, 2) = %d, want the cap 4", got)
	}
}

func TestSweepHitsStaticBox(t *testing.T) {
	s := impl.NewSweptCollisionSolver(solverOptions())
	wall := impl.Body{ID: "wall", Pos: impl.Vec3{}, Half: impl.Vec3{X: 1, Y: 1, Z: 1}, Static: true}
	if err := s.AddBody(wall); err != nil {
		t.Fatalf("add wall failed: %v", err)
	}
	from := impl.AABB{Min: impl.Vec3{X: -6, Y: -1, Z: -1}, Max: impl.Vec3{X: -5, Y: 1, Z: 1}}
	to := impl.AABB{Min: impl.Vec3{X: 4, Y: -1, Z: -1}, Max: impl.Vec3{X: 5, Y: 1, Z: 1}}
	hit, ok, err := s.Sweep(from, to, 100)
	if err != nil {
		t.Fatalf("sweep failed: %v", err)
	}
	if !ok {
		t.Fatalf("sweep must report the wall contact")
	}
	if hit.BodyID != "wall" {
		t.Fatalf("hit body = %q, want wall", hit.BodyID)
	}
	if math.Abs(hit.T-0.4) > 1e-6 {
		t.Fatalf("hit t = %f, want 0.4", hit.T)
	}
	if math.Abs(hit.Normal.X+1) > 1e-9 {
		t.Fatalf("hit normal = %+v, want -x", hit.Normal)
	}
}

func TestSweepMissesClearPath(t *testing.T) {
	s := impl.NewSweptCollisionSolver(solverOptions())
	wall := impl.Body{ID: "wall", Pos: impl.Vec3{}, Half: impl.Vec3{X: 1, Y: 1, Z: 1}, Static: true}
	if err := s.AddBody(wall); err != nil {
		t.Fatalf("add wall failed: %v", err)
	}
	from := impl.AABB{Min: impl.Vec3{X: -6, Y: 10, Z: -1}, Max: impl.Vec3{X: -5, Y: 12, Z: 1}}
	to := impl.AABB{Min: impl.Vec3{X: 4, Y: 10, Z: -1}, Max: impl.Vec3{X: 5, Y: 12, Z: 1}}
	_, ok, err := s.Sweep(from, to, 100)
	if err != nil {
		t.Fatalf("sweep failed: %v", err)
	}
	if ok {
		t.Fatalf("sweep above the wall must not report a contact")
	}
}

func TestSweepRejectsInvalidAABB(t *testing.T) {
	s := impl.NewSweptCollisionSolver(solverOptions())
	bad := impl.AABB{Min: impl.Vec3{X: 2}, Max: impl.Vec3{X: 1}}
	if _, _, err := s.Sweep(bad, bad, 100); !errors.Is(err, impl.ErrBadAABB) {
		t.Fatalf("inverted aabb must be rejected, got %v", err)
	}
}

func TestMoveAdvancesWithoutObstacle(t *testing.T) {
	s := impl.NewSweptCollisionSolver(solverOptions())
	if err := s.AddBody(impl.Body{ID: "player", Pos: impl.Vec3{X: -5}, Half: impl.Vec3{X: 0.5, Y: 0.5, Z: 0.5}}); err != nil {
		t.Fatalf("add body failed: %v", err)
	}
	res, err := s.Move("player", impl.Vec3{X: 2.5}, 100)
	if err != nil {
		t.Fatalf("move failed: %v", err)
	}
	if res.Blocked {
		t.Fatalf("free move must not be blocked")
	}
	if math.Abs(res.Pos.X-(-2.5)) > 1e-9 {
		t.Fatalf("pos x = %f, want -2.5", res.Pos.X)
	}
	if res.Substeps < 1 {
		t.Fatalf("substeps = %d, want at least 1", res.Substeps)
	}
}

func TestMoveFrameRateIndependent(t *testing.T) {
	one := impl.NewSweptCollisionSolver(solverOptions())
	if err := one.AddBody(impl.Body{ID: "a", Half: impl.Vec3{X: 0.5, Y: 0.5, Z: 0.5}}); err != nil {
		t.Fatalf("add body failed: %v", err)
	}
	if _, err := one.Move("a", impl.Vec3{X: 10}, 100); err != nil {
		t.Fatalf("single move failed: %v", err)
	}
	split := impl.NewSweptCollisionSolver(solverOptions())
	if err := split.AddBody(impl.Body{ID: "b", Half: impl.Vec3{X: 0.5, Y: 0.5, Z: 0.5}}); err != nil {
		t.Fatalf("add body failed: %v", err)
	}
	if _, err := split.Move("b", impl.Vec3{X: 5}, 50); err != nil {
		t.Fatalf("first half move failed: %v", err)
	}
	if _, err := split.Move("b", impl.Vec3{X: 5}, 50); err != nil {
		t.Fatalf("second half move failed: %v", err)
	}
	posA, _ := one.Position("a")
	posB, _ := split.Position("b")
	if math.Abs(posA.X-posB.X) > 1e-9 {
		t.Fatalf("100ms endpoint x = %f, two 50ms endpoint x = %f, want them equal", posA.X, posB.X)
	}
}

func TestMoveStopsAtWall(t *testing.T) {
	s := impl.NewSweptCollisionSolver(solverOptions())
	if err := s.AddBody(impl.Body{ID: "player", Pos: impl.Vec3{X: -5}, Half: impl.Vec3{X: 0.5, Y: 0.5, Z: 0.5}}); err != nil {
		t.Fatalf("add player failed: %v", err)
	}
	if err := s.AddBody(impl.Body{ID: "wall", Pos: impl.Vec3{}, Half: impl.Vec3{X: 1, Y: 1, Z: 1}, Static: true}); err != nil {
		t.Fatalf("add wall failed: %v", err)
	}
	res, err := s.Move("player", impl.Vec3{X: 10}, 100)
	if err != nil {
		t.Fatalf("move failed: %v", err)
	}
	if !res.Blocked {
		t.Fatalf("move into the wall must be blocked")
	}
	if res.Pos.X > -1.5 {
		t.Fatalf("pos x = %f, body must stop in front of the wall at -1.5", res.Pos.X)
	}
	if res.Pos.X < -1.51 {
		t.Fatalf("pos x = %f, body rolled back too far", res.Pos.X)
	}
	if math.Abs(res.Normal.X+1) > 1e-9 {
		t.Fatalf("block normal = %+v, want -x", res.Normal)
	}
}

func TestMoveRejectsUnknownStaticAndBadDelta(t *testing.T) {
	s := impl.NewSweptCollisionSolver(solverOptions())
	if _, err := s.Move("ghost", impl.Vec3{X: 1}, 100); !errors.Is(err, impl.ErrUnknownBody) {
		t.Fatalf("unknown body must be rejected, got %v", err)
	}
	if err := s.AddBody(impl.Body{ID: "wall", Half: impl.Vec3{X: 1, Y: 1, Z: 1}, Static: true}); err != nil {
		t.Fatalf("add static body failed: %v", err)
	}
	if _, err := s.Move("wall", impl.Vec3{X: 1}, 100); !errors.Is(err, impl.ErrStaticBody) {
		t.Fatalf("static body move must be rejected, got %v", err)
	}
	if err := s.AddBody(impl.Body{ID: "player", Half: impl.Vec3{X: 0.5, Y: 0.5, Z: 0.5}}); err != nil {
		t.Fatalf("add player failed: %v", err)
	}
	if _, err := s.Move("player", impl.Vec3{X: 1}, 0); !errors.Is(err, impl.ErrBadTime) {
		t.Fatalf("zero dt must be rejected, got %v", err)
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	s := impl.NewSweptCollisionSolver(solverOptions())
	if err := s.AddBody(impl.Body{ID: "player", Pos: impl.Vec3{X: -2}, Half: impl.Vec3{X: 0.5, Y: 0.5, Z: 0.5}}); err != nil {
		t.Fatalf("add body failed: %v", err)
	}
	if _, err := s.Move("player", impl.Vec3{X: 1.5}, 50); err != nil {
		t.Fatalf("move failed: %v", err)
	}
	wantPos, _ := s.Position("player")
	wantVel, _ := s.Velocity("player")
	snap, err := s.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	restored := impl.NewSweptCollisionSolver(solverOptions())
	if err := restored.Restore(snap); err != nil {
		t.Fatalf("restore failed: %v", err)
	}
	gotPos, ok := restored.Position("player")
	if !ok {
		t.Fatalf("restored body is missing")
	}
	if math.Abs(gotPos.X-wantPos.X) > 1e-9 {
		t.Fatalf("restored pos x = %f, want %f", gotPos.X, wantPos.X)
	}
	gotVel, ok := restored.Velocity("player")
	if !ok || math.Abs(gotVel.X-wantVel.X) > 1e-9 {
		t.Fatalf("restored velocity = %+v ok=%v, want %+v", gotVel, ok, wantVel)
	}
	if _, err := restored.Move("player", impl.Vec3{X: 1}, 50); err != nil {
		t.Fatalf("move after restore failed: %v", err)
	}
}

func TestRestoreRejectsBadSnapshot(t *testing.T) {
	s := impl.NewSweptCollisionSolver(solverOptions())
	if err := s.AddBody(impl.Body{ID: "player", Half: impl.Vec3{X: 0.5, Y: 0.5, Z: 0.5}}); err != nil {
		t.Fatalf("add body failed: %v", err)
	}
	if err := s.Restore([]byte("not a snapshot at all")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("broken snapshot must be rejected, got %v", err)
	}
	if _, ok := s.Body("player"); !ok {
		t.Fatalf("failed restore must keep the live state")
	}
}
