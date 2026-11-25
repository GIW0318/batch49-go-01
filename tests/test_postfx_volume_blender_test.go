package task

import (
	"errors"
	"testing"

	impl "gsb/batch49-go-game-b-10"
)

func newBlender() *impl.PostFxVolumeBlender {
	return impl.NewPostFxVolumeBlender(impl.BlendConfig{MaxWeightDelta: 0.2, MaxVolumes: 8})
}

func testVolume(name string, center impl.Vec3, sat, exp float64, bloom bool, tone string) impl.Volume {
	return impl.Volume{
		Name:     name,
		Center:   center,
		Inner:    1,
		Outer:    5,
		Enabled:  true,
		Settings: impl.PostFxSettings{Saturation: sat, Exposure: exp, Bloom: bloom, Tonemap: tone},
	}
}

func addVolume(t *testing.T, b *impl.PostFxVolumeBlender, v impl.Volume) impl.VolumeID {
	t.Helper()
	id, err := b.AddVolume(v)
	if err != nil {
		t.Fatalf("add volume %s: %v", v.Name, err)
	}
	return id
}

func closeTo(t *testing.T, got, want float64, what string) {
	t.Helper()
	diff := got - want
	if diff < 0 {
		diff = -diff
	}
	if diff > 1e-9 {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

func TestAddVolumeAssignsSequentialIDs(t *testing.T) {
	b := newBlender()
	first := addVolume(t, b, testVolume("snow", impl.Vec3{X: 0, Y: 0, Z: 0}, 1.2, 0.3, false, impl.TonemapAces))
	second := addVolume(t, b, testVolume("cave", impl.Vec3{X: 10, Y: 0, Z: 0}, 0.8, 0.1, true, impl.TonemapNeutral))
	if first == 0 || second <= first {
		t.Fatalf("volume ids = %d and %d, want increasing positive ids", first, second)
	}
	if got := b.VolumeCount(); got != 2 {
		t.Fatalf("volume count = %d, want 2", got)
	}
	v, ok := b.Params(first)
	if !ok {
		t.Fatalf("Params(%d) must resolve", first)
	}
	if v.Name != "snow" || v.Settings.Saturation != 1.2 {
		t.Fatalf("params = %+v, want the snow volume", v)
	}
}

func TestAddVolumeRejectsInvalidShape(t *testing.T) {
	b := newBlender()
	blank := testVolume("  ", impl.Vec3{}, 1, 0, false, impl.TonemapNeutral)
	if _, err := b.AddVolume(blank); !errors.Is(err, impl.ErrInvalidVolume) {
		t.Fatalf("blank name must fail with ErrInvalidVolume, got %v", err)
	}
	negInner := testVolume("bad", impl.Vec3{}, 1, 0, false, impl.TonemapNeutral)
	negInner.Inner = -1
	if _, err := b.AddVolume(negInner); !errors.Is(err, impl.ErrInvalidVolume) {
		t.Fatalf("negative inner radius must fail with ErrInvalidVolume, got %v", err)
	}
	flat := testVolume("flat", impl.Vec3{}, 1, 0, false, impl.TonemapNeutral)
	flat.Outer = flat.Inner
	if _, err := b.AddVolume(flat); !errors.Is(err, impl.ErrInvalidVolume) {
		t.Fatalf("outer equal to inner must fail with ErrInvalidVolume, got %v", err)
	}
	if got := b.VolumeCount(); got != 0 {
		t.Fatalf("rejected volumes must not register, count = %d", got)
	}
}

func TestUpdateVolumeChangesSettings(t *testing.T) {
	b := newBlender()
	id := addVolume(t, b, testVolume("snow", impl.Vec3{}, 1.2, 0.3, false, impl.TonemapAces))
	updated := testVolume("updated", impl.Vec3{X: 1, Y: 0, Z: 0}, 1.6, 1.2, true, impl.TonemapReinhard)
	if err := b.UpdateVolume(id, updated); err != nil {
		t.Fatalf("update volume: %v", err)
	}
	v, ok := b.Params(id)
	if !ok {
		t.Fatalf("updated volume must stay registered")
	}
	if v.Name != "updated" || v.Settings.Saturation != 1.6 || v.Settings.Exposure != 1.2 || !v.Settings.Bloom {
		t.Fatalf("params after update = %+v", v)
	}
	if err := b.UpdateVolume(99, updated); !errors.Is(err, impl.ErrVolumeNotFound) {
		t.Fatalf("update of an unknown volume must fail with ErrVolumeNotFound, got %v", err)
	}
}

func TestCameraInsideCoreFullWeight(t *testing.T) {
	b := newBlender()
	id := addVolume(t, b, testVolume("snow", impl.Vec3{}, 1.5, 0.5, false, impl.TonemapAces))
	if err := b.SetCamera(impl.Vec3{X: 0.5, Y: 0, Z: 0}, true); err != nil {
		t.Fatalf("set camera: %v", err)
	}
	closeTo(t, b.Weight(id), 1, "weight inside the core")
	closeTo(t, b.BlendWeight(id), 1, "blend weight after a teleport")
}

func TestCameraOutsideOuterZeroWeight(t *testing.T) {
	b := newBlender()
	id := addVolume(t, b, testVolume("snow", impl.Vec3{}, 1.5, 0.5, false, impl.TonemapAces))
	if err := b.SetCamera(impl.Vec3{X: 0, Y: 9, Z: 0}, true); err != nil {
		t.Fatalf("set camera: %v", err)
	}
	closeTo(t, b.Weight(id), 0, "weight outside the outer radius")
	closeTo(t, b.BlendWeight(id), 0, "blend weight outside the outer radius")
}

func TestCameraInFalloffRing(t *testing.T) {
	b := newBlender()
	id := addVolume(t, b, testVolume("snow", impl.Vec3{}, 1.5, 0.5, false, impl.TonemapAces))
	if err := b.SetCamera(impl.Vec3{X: 0, Y: 2, Z: 0}, true); err != nil {
		t.Fatalf("set camera: %v", err)
	}
	closeTo(t, b.Weight(id), 0.75, "weight at distance 2 of a 1..5 volume")
}

func TestRampLimitsWeightGrowth(t *testing.T) {
	b := newBlender()
	id := addVolume(t, b, testVolume("snow", impl.Vec3{}, 1.5, 0.5, false, impl.TonemapAces))
	if err := b.SetCamera(impl.Vec3{}, false); err != nil {
		t.Fatalf("set camera: %v", err)
	}
	closeTo(t, b.BlendWeight(id), 0, "blend weight before the first frame")
	if frame := b.AdvanceFrame(); frame != 1 {
		t.Fatalf("frame number = %d, want 1", frame)
	}
	closeTo(t, b.BlendWeight(id), 0.2, "blend weight after one ramp step")
	if frame := b.AdvanceFrame(); frame != 2 {
		t.Fatalf("frame number = %d, want 2", frame)
	}
	closeTo(t, b.BlendWeight(id), 0.4, "blend weight after two ramp steps")
	for i := 0; i < 3; i++ {
		b.AdvanceFrame()
	}
	closeTo(t, b.BlendWeight(id), 1, "blend weight once the ramp reached the target")
}

func TestTeleportSnapsBlendWeight(t *testing.T) {
	b := newBlender()
	id := addVolume(t, b, testVolume("snow", impl.Vec3{}, 1.5, 0.5, false, impl.TonemapAces))
	if err := b.SetCamera(impl.Vec3{X: 0, Y: 9, Z: 0}, true); err != nil {
		t.Fatalf("set camera far away: %v", err)
	}
	closeTo(t, b.BlendWeight(id), 0, "blend weight far away")
	if err := b.SetCamera(impl.Vec3{}, true); err != nil {
		t.Fatalf("teleport into the volume: %v", err)
	}
	closeTo(t, b.BlendWeight(id), 1, "blend weight after a teleport into the volume")
}

func TestRemoveVolumeDropsContribution(t *testing.T) {
	b := newBlender()
	main := addVolume(t, b, testVolume("snow", impl.Vec3{}, 1.5, 0.5, false, impl.TonemapAces))
	side := addVolume(t, b, testVolume("cave", impl.Vec3{X: 0, Y: 3, Z: 0}, 0.5, 1.0, true, impl.TonemapReinhard))
	if err := b.SetCamera(impl.Vec3{}, true); err != nil {
		t.Fatalf("set camera: %v", err)
	}
	closeTo(t, b.Active().Saturation, 1.75/1.5, "blended saturation with both volumes")
	if err := b.RemoveVolume(side); err != nil {
		t.Fatalf("remove volume: %v", err)
	}
	if got := b.VolumeCount(); got != 1 {
		t.Fatalf("volume count after removal = %d, want 1", got)
	}
	if _, ok := b.Params(side); ok {
		t.Fatalf("removed volume must disappear from the table")
	}
	closeTo(t, b.Active().Saturation, 1.5, "blended saturation with the main volume only")
	if err := b.RemoveVolume(side); !errors.Is(err, impl.ErrVolumeNotFound) {
		t.Fatalf("second removal must fail with ErrVolumeNotFound, got %v", err)
	}
	if b.Weight(main) != 1 {
		t.Fatalf("main volume weight = %v, want 1", b.Weight(main))
	}
}

func TestActiveWithoutVolumesIsDefault(t *testing.T) {
	b := newBlender()
	if got := b.VolumeCount(); got != 0 {
		t.Fatalf("fresh blender volume count = %d, want 0", got)
	}
	if got, want := b.Active(), impl.DefaultPostFxSettings(); got != want {
		t.Fatalf("active without volumes = %+v, want %+v", got, want)
	}
	if err := b.SetCamera(impl.Vec3{X: 3, Y: 4, Z: 0}, true); err != nil {
		t.Fatalf("set camera: %v", err)
	}
	if got, want := b.Active(), impl.DefaultPostFxSettings(); got != want {
		t.Fatalf("active after moving without volumes = %+v, want %+v", got, want)
	}
}

func TestBlendNumericWeightedAverage(t *testing.T) {
	b := newBlender()
	addVolume(t, b, testVolume("snow", impl.Vec3{}, 1.5, 0.5, false, impl.TonemapAces))
	addVolume(t, b, testVolume("cave", impl.Vec3{X: 0, Y: 3, Z: 0}, 0.5, 1.0, true, impl.TonemapReinhard))
	if err := b.SetCamera(impl.Vec3{}, true); err != nil {
		t.Fatalf("set camera: %v", err)
	}
	active := b.Active()
	closeTo(t, active.Saturation, 1.75/1.5, "weighted saturation")
	closeTo(t, active.Exposure, 1.0/1.5, "weighted exposure")
}

func TestBlendBooleanFromHeaviestVolume(t *testing.T) {
	b := newBlender()
	addVolume(t, b, testVolume("snow", impl.Vec3{}, 1.5, 0.5, false, impl.TonemapAces))
	addVolume(t, b, testVolume("cave", impl.Vec3{X: 0, Y: 3, Z: 0}, 0.5, 1.0, true, impl.TonemapReinhard))
	if err := b.SetCamera(impl.Vec3{}, true); err != nil {
		t.Fatalf("set camera: %v", err)
	}
	if b.Active().Bloom {
		t.Fatalf("bloom must follow the heaviest volume, which keeps it disabled")
	}
	if err := b.SetCamera(impl.Vec3{X: 0, Y: 3, Z: 0}, true); err != nil {
		t.Fatalf("move onto the cave volume: %v", err)
	}
	if !b.Active().Bloom {
		t.Fatalf("bloom must be on while the heavier volume enables it")
	}
}

func TestBlendEnumFromHeaviestVolume(t *testing.T) {
	b := newBlender()
	addVolume(t, b, testVolume("snow", impl.Vec3{}, 1.5, 0.5, false, impl.TonemapAces))
	addVolume(t, b, testVolume("cave", impl.Vec3{X: 0, Y: 3, Z: 0}, 0.5, 1.0, true, impl.TonemapReinhard))
	if err := b.SetCamera(impl.Vec3{}, true); err != nil {
		t.Fatalf("set camera: %v", err)
	}
	if got := b.Active().Tonemap; got != impl.TonemapAces {
		t.Fatalf("tonemap = %q, want %q from the heaviest volume", got, impl.TonemapAces)
	}
	if err := b.SetCamera(impl.Vec3{X: 0, Y: 3, Z: 0}, true); err != nil {
		t.Fatalf("move onto the cave volume: %v", err)
	}
	if got := b.Active().Tonemap; got != impl.TonemapReinhard {
		t.Fatalf("tonemap = %q, want %q from the heaviest volume", got, impl.TonemapReinhard)
	}
}

func TestSnapshotRestoreKeepsWeights(t *testing.T) {
	b := newBlender()
	id := addVolume(t, b, testVolume("snow", impl.Vec3{}, 1.5, 0.5, false, impl.TonemapAces))
	if err := b.SetCamera(impl.Vec3{}, false); err != nil {
		t.Fatalf("set camera: %v", err)
	}
	if frame := b.AdvanceFrame(); frame != 1 {
		t.Fatalf("frame before snapshot = %d, want 1", frame)
	}
	snap, err := b.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snap) == 0 {
		t.Fatalf("snapshot payload must not be empty")
	}
	fresh := newBlender()
	if err := fresh.Restore(snap); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := fresh.VolumeCount(); got != 1 {
		t.Fatalf("volume count after restore = %d, want 1", got)
	}
	closeTo(t, fresh.Weight(id), 1, "weight after restore")
	closeTo(t, fresh.BlendWeight(id), 0.2, "blend weight after restore")
	if frame := fresh.AdvanceFrame(); frame != 2 {
		t.Fatalf("frame after restore = %d, want 2", frame)
	}
	closeTo(t, fresh.BlendWeight(id), 0.4, "blend weight after one frame past the restore")
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	b := newBlender()
	id := addVolume(t, b, testVolume("snow", impl.Vec3{}, 1.5, 0.5, false, impl.TonemapAces))
	if err := b.SetCamera(impl.Vec3{}, true); err != nil {
		t.Fatalf("set camera: %v", err)
	}
	if err := b.Restore([]byte("volume-table: broken")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt payload must fail with ErrBadSnapshot, got %v", err)
	}
	if got := b.VolumeCount(); got != 1 {
		t.Fatalf("a rejected restore must keep the volume table, count = %d", got)
	}
	closeTo(t, b.Weight(id), 1, "weight after a rejected restore")
	if got := b.Active().Saturation; got != 1.5 {
		t.Fatalf("active saturation after a rejected restore = %v, want 1.5", got)
	}
}
