package task

import (
	"errors"
	"testing"

	impl "gsb/batch49-go-game-b-13"
)

func mustUpdater(t *testing.T, budget int) *impl.SkinnedBoundsUpdater {
	t.Helper()
	u, err := impl.NewSkinnedBoundsUpdater(impl.BoundsOptions{FrameBudget: budget, CullMargin: 0.5, MaxMeshes: 16})
	if err != nil {
		t.Fatalf("new updater: %v", err)
	}
	if u == nil {
		t.Fatalf("updater must not be nil")
	}
	return u
}

func box(minX, minY, minZ, maxX, maxY, maxZ float64) impl.AABB {
	return impl.AABB{
		Min: impl.Vec3{X: minX, Y: minY, Z: minZ},
		Max: impl.Vec3{X: maxX, Y: maxY, Z: maxZ},
	}
}

func singleBoneInfluence(skel, bone string, local impl.AABB) impl.InfluenceMap {
	return impl.InfluenceMap{SkelID: skel, Boxes: []impl.InfluenceBox{{Bone: bone, Local: local}}}
}

func mustBind(t *testing.T, u *impl.SkinnedBoundsUpdater, mesh, skel, bone string, local impl.AABB) {
	t.Helper()
	if err := u.Bind(mesh, local, singleBoneInfluence(skel, bone, local)); err != nil {
		t.Fatalf("bind %s: %v", mesh, err)
	}
}

func TestNewRejectsInvalidOptions(t *testing.T) {
	if _, err := impl.NewSkinnedBoundsUpdater(impl.BoundsOptions{FrameBudget: 0, MaxMeshes: 4}); !errors.Is(err, impl.ErrInvalidOptions) {
		t.Fatalf("zero frame budget must report ErrInvalidOptions, got %v", err)
	}
	if _, err := impl.NewSkinnedBoundsUpdater(impl.BoundsOptions{FrameBudget: 4, CullMargin: -1, MaxMeshes: 4}); !errors.Is(err, impl.ErrInvalidOptions) {
		t.Fatalf("negative cull margin must report ErrInvalidOptions, got %v", err)
	}
	if _, err := impl.NewSkinnedBoundsUpdater(impl.BoundsOptions{FrameBudget: 4, MaxMeshes: 0}); !errors.Is(err, impl.ErrInvalidOptions) {
		t.Fatalf("zero mesh cap must report ErrInvalidOptions, got %v", err)
	}
}

func TestBindRejectsEmptyMesh(t *testing.T) {
	u := mustUpdater(t, 8)
	err := u.Bind("  ", box(0, 0, 0, 1, 1, 1), singleBoneInfluence("skel", "root", box(0, 0, 0, 1, 1, 1)))
	if !errors.Is(err, impl.ErrEmptyMesh) {
		t.Fatalf("blank mesh id must report ErrEmptyMesh, got %v", err)
	}
}

func TestBindRejectsInvalidBounds(t *testing.T) {
	u := mustUpdater(t, 8)
	err := u.Bind("mesh", box(2, 0, 0, 1, 1, 1), singleBoneInfluence("skel", "root", box(0, 0, 0, 1, 1, 1)))
	if !errors.Is(err, impl.ErrInvalidBounds) {
		t.Fatalf("inverted box must report ErrInvalidBounds, got %v", err)
	}
}

func TestBindRejectsDuplicateMesh(t *testing.T) {
	u := mustUpdater(t, 8)
	mustBind(t, u, "mesh", "skel", "root", box(0, 0, 0, 1, 1, 1))
	err := u.Bind("mesh", box(0, 0, 0, 2, 2, 2), singleBoneInfluence("skel", "root", box(0, 0, 0, 2, 2, 2)))
	if !errors.Is(err, impl.ErrDuplicateMesh) {
		t.Fatalf("duplicate mesh must report ErrDuplicateMesh, got %v", err)
	}
}

func TestBoundsRequiresBoundMesh(t *testing.T) {
	u := mustUpdater(t, 8)
	mustBind(t, u, "mesh", "skel", "root", box(0, 0, 0, 1, 1, 1))
	if got := u.MeshCount(); got != 1 {
		t.Fatalf("mesh count = %d, want 1", got)
	}
	if _, ok := u.Bounds("ghost"); ok {
		t.Fatalf("unknown mesh must not report bounds")
	}
}

func TestRecomputeWithIdentityPoseMatchesInfluenceBox(t *testing.T) {
	u := mustUpdater(t, 8)
	mustBind(t, u, "mesh", "skel", "root", box(0, 0, 0, 1, 1, 1))
	u.BeginFrame()
	if err := u.MarkBoneDirty("skel", "root"); err != nil {
		t.Fatalf("mark dirty: %v", err)
	}
	report := u.Recompute("skel")
	if report.Meshes != 1 {
		t.Fatalf("recomputed meshes = %d, want 1", report.Meshes)
	}
	got, ok := u.Bounds("mesh")
	want := box(0, 0, 0, 1, 1, 1)
	if !ok || got != want {
		t.Fatalf("identity bounds = %+v/%v, want %+v/true", got, ok, want)
	}
}

func TestRecomputeAppliesBoneTranslation(t *testing.T) {
	u := mustUpdater(t, 8)
	mustBind(t, u, "mesh", "skel", "root", box(0, 0, 0, 1, 1, 1))
	if err := u.SetPose("skel", "root", impl.BonePose{Offset: impl.Vec3{X: 10}, Scale: impl.Vec3{X: 1, Y: 1, Z: 1}}); err != nil {
		t.Fatalf("set pose: %v", err)
	}
	u.BeginFrame()
	if err := u.MarkBoneDirty("skel", "root"); err != nil {
		t.Fatalf("mark dirty: %v", err)
	}
	u.Recompute("skel")
	got, ok := u.Bounds("mesh")
	want := box(10, 0, 0, 11, 1, 1)
	if !ok || got != want {
		t.Fatalf("translated bounds = %+v/%v, want %+v/true", got, ok, want)
	}
}

func TestRecomputeUnionsEveryBone(t *testing.T) {
	u := mustUpdater(t, 8)
	influence := impl.InfluenceMap{SkelID: "skel", Boxes: []impl.InfluenceBox{
		{Bone: "arm", Local: box(0, 0, 0, 1, 1, 1)},
		{Bone: "leg", Local: box(2, 2, 2, 3, 3, 3)},
	}}
	if err := u.Bind("mesh", box(0, 0, 0, 3, 3, 3), influence); err != nil {
		t.Fatalf("bind: %v", err)
	}
	u.BeginFrame()
	if err := u.MarkBoneDirty("skel", "arm"); err != nil {
		t.Fatalf("mark arm: %v", err)
	}
	if err := u.MarkBoneDirty("skel", "leg"); err != nil {
		t.Fatalf("mark leg: %v", err)
	}
	u.Recompute("skel")
	got, ok := u.Bounds("mesh")
	want := box(0, 0, 0, 3, 3, 3)
	if !ok || got != want {
		t.Fatalf("union bounds = %+v/%v, want %+v/true", got, ok, want)
	}
}

func TestRepeatedDirtyMarksRecomputeOnce(t *testing.T) {
	u := mustUpdater(t, 8)
	mustBind(t, u, "mesh", "skel", "root", box(0, 0, 0, 1, 1, 1))
	u.BeginFrame()
	for i := 0; i < 3; i++ {
		if err := u.MarkBoneDirty("skel", "root"); err != nil {
			t.Fatalf("mark dirty %d: %v", i, err)
		}
	}
	report := u.Recompute("skel")
	if report.DirtyBones != 1 || report.Meshes != 1 {
		t.Fatalf("report = %+v, want one dirty bone and one mesh", report)
	}
}

func TestFrameBudgetDefersBonesAndMarksStale(t *testing.T) {
	u := mustUpdater(t, 1)
	infA := singleBoneInfluence("skel", "bone-a", box(0, 0, 0, 1, 1, 1))
	if err := u.Bind("mesh-a", box(0, 0, 0, 1, 1, 1), infA); err != nil {
		t.Fatalf("bind mesh-a: %v", err)
	}
	infB := singleBoneInfluence("skel", "bone-b", box(2, 2, 2, 3, 3, 3))
	if err := u.Bind("mesh-b", box(2, 2, 2, 3, 3, 3), infB); err != nil {
		t.Fatalf("bind mesh-b: %v", err)
	}
	if err := u.SetPose("skel", "bone-b", impl.BonePose{Offset: impl.Vec3{X: 5}, Scale: impl.Vec3{X: 1, Y: 1, Z: 1}}); err != nil {
		t.Fatalf("set pose: %v", err)
	}
	u.BeginFrame()
	if err := u.MarkBoneDirty("skel", "bone-a"); err != nil {
		t.Fatalf("mark bone-a: %v", err)
	}
	if err := u.MarkBoneDirty("skel", "bone-b"); err != nil {
		t.Fatalf("mark bone-b: %v", err)
	}
	report := u.Recompute("skel")
	if report.DirtyBones != 2 || report.DeferredBones != 1 {
		t.Fatalf("report = %+v, want 2 dirty and 1 deferred", report)
	}
	if got := u.StaleCount(); got != 1 {
		t.Fatalf("stale count = %d, want 1", got)
	}
	got, ok := u.Bounds("mesh-b")
	if !ok || got.Min.X != 2 {
		t.Fatalf("deferred mesh bounds = %+v, want the old min x 2", got)
	}
}

func TestDeferredBoneFinishesNextFrame(t *testing.T) {
	u := mustUpdater(t, 1)
	mustBind(t, u, "mesh-a", "skel", "bone-a", box(0, 0, 0, 1, 1, 1))
	mustBind(t, u, "mesh-b", "skel", "bone-b", box(2, 2, 2, 3, 3, 3))
	if err := u.SetPose("skel", "bone-b", impl.BonePose{Offset: impl.Vec3{X: 5}, Scale: impl.Vec3{X: 1, Y: 1, Z: 1}}); err != nil {
		t.Fatalf("set pose: %v", err)
	}
	u.BeginFrame()
	if err := u.MarkBoneDirty("skel", "bone-a"); err != nil {
		t.Fatalf("mark bone-a: %v", err)
	}
	if err := u.MarkBoneDirty("skel", "bone-b"); err != nil {
		t.Fatalf("mark bone-b: %v", err)
	}
	u.Recompute("skel")
	u.BeginFrame()
	report := u.Recompute("skel")
	if report.DirtyBones != 1 || report.DeferredBones != 0 {
		t.Fatalf("second frame report = %+v, want the deferred bone only", report)
	}
	if got := u.StaleCount(); got != 0 {
		t.Fatalf("stale count after the deferred bone = %d, want 0", got)
	}
	got, ok := u.Bounds("mesh-b")
	want := box(7, 2, 2, 8, 3, 3)
	if !ok || got != want {
		t.Fatalf("deferred bounds = %+v/%v, want %+v/true", got, ok, want)
	}
}

func TestMarkBoneDirtyRejectsUnknownBone(t *testing.T) {
	u := mustUpdater(t, 8)
	mustBind(t, u, "mesh", "skel", "root", box(0, 0, 0, 1, 1, 1))
	if err := u.MarkBoneDirty("skel", "ghost"); !errors.Is(err, impl.ErrUnknownBone) {
		t.Fatalf("unknown bone must report ErrUnknownBone, got %v", err)
	}
	if err := u.MarkBoneDirty("other-skel", "root"); !errors.Is(err, impl.ErrUnknownBone) {
		t.Fatalf("unknown skeleton must report ErrUnknownBone, got %v", err)
	}
}

func TestSnapshotRestoreKeepsBounds(t *testing.T) {
	u := mustUpdater(t, 8)
	mustBind(t, u, "mesh", "skel", "root", box(0, 0, 0, 1, 1, 1))
	u.BeginFrame()
	if err := u.MarkBoneDirty("skel", "root"); err != nil {
		t.Fatalf("mark dirty: %v", err)
	}
	u.Recompute("skel")
	snap, err := u.Snapshot()
	if err != nil || len(snap) == 0 {
		t.Fatalf("snapshot err=%v len=%d, want a non empty payload", err, len(snap))
	}
	if err := u.SetPose("skel", "root", impl.BonePose{Offset: impl.Vec3{X: 3}, Scale: impl.Vec3{X: 1, Y: 1, Z: 1}}); err != nil {
		t.Fatalf("set pose: %v", err)
	}
	u.BeginFrame()
	if err := u.MarkBoneDirty("skel", "root"); err != nil {
		t.Fatalf("mark dirty after snapshot: %v", err)
	}
	u.Recompute("skel")
	if err := u.Restore(snap); err != nil {
		t.Fatalf("restore: %v", err)
	}
	got, ok := u.Bounds("mesh")
	want := box(0, 0, 0, 1, 1, 1)
	if !ok || got != want {
		t.Fatalf("restored bounds = %+v/%v, want %+v/true", got, ok, want)
	}
}

func TestRestoreRejectsCorruptPayload(t *testing.T) {
	u := mustUpdater(t, 8)
	if err := u.Restore([]byte(`{"meshes":`)); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must report ErrBadSnapshot, got %v", err)
	}
}

func TestCullBoundsMatchesFreshBounds(t *testing.T) {
	u := mustUpdater(t, 8)
	mustBind(t, u, "mesh", "skel", "root", box(0, 0, 0, 1, 1, 1))
	u.BeginFrame()
	if err := u.MarkBoneDirty("skel", "root"); err != nil {
		t.Fatalf("mark dirty: %v", err)
	}
	u.Recompute("skel")
	cull, ok := u.CullBounds("mesh")
	if !ok {
		t.Fatalf("cull bounds of a fresh mesh must be available")
	}
	stored, _ := u.Bounds("mesh")
	if cull != stored {
		t.Fatalf("cull bounds of a fresh mesh = %+v, want %+v", cull, stored)
	}
}
