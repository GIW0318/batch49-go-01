package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch49-go-game-b-11"
)

func solidImage(w, h int, r, g, b, a float32) impl.Image {
	px := make([]impl.Vec4, w*h)
	for i := range px {
		px[i] = impl.Vec4{R: r, G: g, B: b, A: a}
	}
	return impl.Image{Width: w, Height: h, Pixels: px}
}

func mustCompositor(t *testing.T, w, h int) *impl.PostFxMaskCompositor {
	t.Helper()
	c, err := impl.NewPostFxMaskCompositor(w, h)
	if err != nil {
		t.Fatalf("new compositor %dx%d: %v", w, h, err)
	}
	if c == nil {
		t.Fatalf("compositor must not be nil")
	}
	return c
}

func mustBindMask(t *testing.T, c *impl.PostFxMaskCompositor, effectID string, mask impl.MaskRef) {
	t.Helper()
	if err := c.BindMask(effectID, mask); err != nil {
		t.Fatalf("bind mask to %q: %v", effectID, err)
	}
}

func TestNewRejectsNonPositiveSize(t *testing.T) {
	if _, err := impl.NewPostFxMaskCompositor(0, 4); !errors.Is(err, impl.ErrInvalidSize) {
		t.Fatalf("zero width must report ErrInvalidSize, got %v", err)
	}
	if _, err := impl.NewPostFxMaskCompositor(4, -2); !errors.Is(err, impl.ErrInvalidSize) {
		t.Fatalf("negative height must report ErrInvalidSize, got %v", err)
	}
}

func TestSetSourceRejectsBufferMismatch(t *testing.T) {
	c := mustCompositor(t, 4, 4)
	short := impl.Image{Width: 4, Height: 4, Pixels: make([]impl.Vec4, 15)}
	if err := c.SetSource(short); !errors.Is(err, impl.ErrInvalidImage) {
		t.Fatalf("short pixel buffer must report ErrInvalidImage, got %v", err)
	}
	if err := c.SetSource(solidImage(3, 3, 0, 0, 0, 1)); !errors.Is(err, impl.ErrInvalidImage) {
		t.Fatalf("foreign dimensions must report ErrInvalidImage, got %v", err)
	}
}

func TestDeclareEffectKeepsOrderAndRejectsDuplicate(t *testing.T) {
	c := mustCompositor(t, 4, 4)
	if err := c.DeclareEffect("bloom"); err != nil {
		t.Fatalf("declare bloom: %v", err)
	}
	if err := c.DeclareEffect("grade"); err != nil {
		t.Fatalf("declare grade: %v", err)
	}
	if err := c.DeclareEffect("bloom"); !errors.Is(err, impl.ErrDuplicateEffect) {
		t.Fatalf("re-declaring bloom must report ErrDuplicateEffect, got %v", err)
	}
	order := c.EffectOrder()
	if len(order) != 2 || order[0] != "bloom" || order[1] != "grade" {
		t.Fatalf("effect order = %v, want [bloom grade]", order)
	}
}

func TestCompositeRejectsUnknownEffect(t *testing.T) {
	c := mustCompositor(t, 4, 4)
	if err := c.SetSource(solidImage(4, 4, 0, 0, 0, 1)); err != nil {
		t.Fatalf("set source: %v", err)
	}
	if _, err := c.Composite("ghost"); !errors.Is(err, impl.ErrUnknownEffect) {
		t.Fatalf("unknown effect must report ErrUnknownEffect, got %v", err)
	}
}

func TestBindMaskValidatesReference(t *testing.T) {
	c := mustCompositor(t, 4, 4)
	if err := c.DeclareEffect("fog"); err != nil {
		t.Fatalf("declare fog: %v", err)
	}
	blank := impl.MaskRef{ID: "", Kind: impl.MaskFromPainted, Width: 2, Height: 2, Data: []float32{0, 0, 0, 0}}
	if err := c.BindMask("fog", blank); !errors.Is(err, impl.ErrInvalidMask) {
		t.Fatalf("empty mask id must report ErrInvalidMask, got %v", err)
	}
	short := impl.MaskRef{ID: "m1", Kind: impl.MaskFromDepth, Width: 2, Height: 2, Data: []float32{1}}
	if err := c.BindMask("fog", short); !errors.Is(err, impl.ErrInvalidMask) {
		t.Fatalf("short mask data must report ErrInvalidMask, got %v", err)
	}
}

func TestFeatherAndStrengthBounds(t *testing.T) {
	c := mustCompositor(t, 4, 4)
	if err := c.DeclareEffect("fog"); err != nil {
		t.Fatalf("declare fog: %v", err)
	}
	if err := c.SetFeather("fog", -1); !errors.Is(err, impl.ErrInvalidFeather) {
		t.Fatalf("negative feather must report ErrInvalidFeather, got %v", err)
	}
	if err := c.SetFeather("fog", impl.MaxFeatherPixels+1); !errors.Is(err, impl.ErrInvalidFeather) {
		t.Fatalf("oversized feather must report ErrInvalidFeather, got %v", err)
	}
	if err := c.SetStrength("fog", 1.5); !errors.Is(err, impl.ErrInvalidStrength) {
		t.Fatalf("strength above one must report ErrInvalidStrength, got %v", err)
	}
	if err := c.SetStrength("fog", -0.25); !errors.Is(err, impl.ErrInvalidStrength) {
		t.Fatalf("negative strength must report ErrInvalidStrength, got %v", err)
	}
}

func TestCompositeMixesPixelsByMask(t *testing.T) {
	c := mustCompositor(t, 2, 1)
	source := impl.Image{Width: 2, Height: 1, Pixels: []impl.Vec4{
		{R: 0, G: 0, B: 0, A: 1},
		{R: 1, G: 1, B: 1, A: 1},
	}}
	if err := c.SetSource(source); err != nil {
		t.Fatalf("set source: %v", err)
	}
	if err := c.DeclareEffect("fog"); err != nil {
		t.Fatalf("declare fog: %v", err)
	}
	rendered := impl.Image{Width: 2, Height: 1, Pixels: []impl.Vec4{
		{R: 1, G: 1, B: 1, A: 1},
		{R: 0, G: 0, B: 0, A: 1},
	}}
	if err := c.SubmitRendered("fog", rendered); err != nil {
		t.Fatalf("submit rendered: %v", err)
	}
	mask := impl.MaskRef{ID: "m1", Kind: impl.MaskFromPainted, Width: 2, Height: 1, Data: []float32{0, 1}}
	mustBindMask(t, c, "fog", mask)
	if err := c.SetStrength("fog", 1); err != nil {
		t.Fatalf("set strength: %v", err)
	}
	stats, err := c.Composite("fog")
	if err != nil {
		t.Fatalf("composite fog: %v", err)
	}
	if stats.Pixels != 2 {
		t.Fatalf("composited pixel count = %d, want 2", stats.Pixels)
	}
	out := c.Output()
	if len(out.Pixels) != 2 {
		t.Fatalf("output buffer has %d pixels, want 2", len(out.Pixels))
	}
	if out.Pixels[0] != (impl.Vec4{A: 1}) {
		t.Fatalf("mask 0 pixel must keep source, got %+v", out.Pixels[0])
	}
	want := impl.Vec4{R: 1, G: 1, B: 1, A: 1}
	if out.Pixels[1] != want {
		t.Fatalf("mask 1 pixel must take rendered colour, got %+v want %+v", out.Pixels[1], want)
	}
}

func TestCompositeResamplesMaskAtPixelCenters(t *testing.T) {
	c := mustCompositor(t, 4, 4)
	if err := c.SetSource(solidImage(4, 4, 0, 0, 0, 1)); err != nil {
		t.Fatalf("set source: %v", err)
	}
	if err := c.DeclareEffect("ao"); err != nil {
		t.Fatalf("declare ao: %v", err)
	}
	if err := c.SubmitRendered("ao", solidImage(4, 4, 1, 1, 1, 1)); err != nil {
		t.Fatalf("submit rendered: %v", err)
	}
	mask := impl.MaskRef{ID: "half", Kind: impl.MaskFromDepth, Width: 2, Height: 2, Data: []float32{0, 1, 1, 0}}
	mustBindMask(t, c, "ao", mask)
	if err := c.SetStrength("ao", 1); err != nil {
		t.Fatalf("set strength: %v", err)
	}
	stats, err := c.Composite("ao")
	if err != nil {
		t.Fatalf("composite ao: %v", err)
	}
	if !stats.Resampled {
		t.Fatalf("mask 2x2 on a 4x4 target must be reported as resampled")
	}
	out := c.Output()
	if len(out.Pixels) != 16 {
		t.Fatalf("output buffer has %d pixels, want 16", len(out.Pixels))
	}
	want := []float32{
		0, 0, 1, 1,
		0, 0, 1, 1,
		1, 1, 0, 0,
		1, 1, 0, 0,
	}
	for i, w := range want {
		if out.Pixels[i].R != w {
			t.Fatalf("pixel %d red = %v, want %v", i, out.Pixels[i].R, w)
		}
	}
}

func TestFeatherLeavesMaskInteriorUntouched(t *testing.T) {
	c := mustCompositor(t, 4, 4)
	if err := c.SetSource(solidImage(4, 4, 0, 0, 0, 1)); err != nil {
		t.Fatalf("set source: %v", err)
	}
	if err := c.DeclareEffect("grade"); err != nil {
		t.Fatalf("declare grade: %v", err)
	}
	rendered := solidImage(4, 4, 0.25, 0.5, 0.75, 1)
	if err := c.SubmitRendered("grade", rendered); err != nil {
		t.Fatalf("submit rendered: %v", err)
	}
	mask := impl.MaskRef{ID: "flat", Kind: impl.MaskFromPainted, Width: 4, Height: 4, Data: make([]float32, 16)}
	for i := range mask.Data {
		mask.Data[i] = 1
	}
	mustBindMask(t, c, "grade", mask)
	if err := c.SetFeather("grade", 3); err != nil {
		t.Fatalf("set feather: %v", err)
	}
	if err := c.SetStrength("grade", 1); err != nil {
		t.Fatalf("set strength: %v", err)
	}
	if _, err := c.Composite("grade"); err != nil {
		t.Fatalf("composite grade: %v", err)
	}
	out := c.Output()
	if len(out.Pixels) != 16 {
		t.Fatalf("output buffer has %d pixels, want 16", len(out.Pixels))
	}
	for i := range out.Pixels {
		if out.Pixels[i] != rendered.Pixels[i] {
			t.Fatalf("pixel %d changed to %+v by feather on a flat mask, want %+v", i, out.Pixels[i], rendered.Pixels[i])
		}
	}
}

func TestCompositeAllAppliesDeclarationOrder(t *testing.T) {
	c := mustCompositor(t, 1, 1)
	if err := c.SetSource(solidImage(1, 1, 0, 0, 0, 1)); err != nil {
		t.Fatalf("set source: %v", err)
	}
	for _, id := range []string{"first", "second"} {
		if err := c.DeclareEffect(id); err != nil {
			t.Fatalf("declare %s: %v", id, err)
		}
	}
	if err := c.SubmitRendered("first", solidImage(1, 1, 1, 1, 1, 1)); err != nil {
		t.Fatalf("submit first: %v", err)
	}
	if err := c.SubmitRendered("second", solidImage(1, 1, 0.5, 0.5, 0.5, 1)); err != nil {
		t.Fatalf("submit second: %v", err)
	}
	for _, id := range []string{"first", "second"} {
		mustBindMask(t, c, id, impl.MaskRef{ID: id + "-mask", Kind: impl.MaskFromPainted, Width: 1, Height: 1, Data: []float32{1}})
		if err := c.SetStrength(id, 1); err != nil {
			t.Fatalf("set strength on %s: %v", id, err)
		}
	}
	stats, err := c.CompositeAll()
	if err != nil {
		t.Fatalf("composite all: %v", err)
	}
	if len(stats) != 2 || stats[0].EffectID != "first" || stats[1].EffectID != "second" {
		t.Fatalf("composite stats = %+v, want first then second", stats)
	}
	out := c.Output()
	if len(out.Pixels) != 1 {
		t.Fatalf("output buffer has %d pixels, want 1", len(out.Pixels))
	}
	if out.Pixels[0].R != 0.5 {
		t.Fatalf("last effect must win on a full mask, red = %v want 0.5", out.Pixels[0].R)
	}
}

func TestStatsReportCountsAndStrength(t *testing.T) {
	c := mustCompositor(t, 4, 4)
	if err := c.SetSource(solidImage(4, 4, 0, 0, 0, 1)); err != nil {
		t.Fatalf("set source: %v", err)
	}
	if err := c.DeclareEffect("fog"); err != nil {
		t.Fatalf("declare fog: %v", err)
	}
	if err := c.SubmitRendered("fog", solidImage(4, 4, 1, 1, 1, 1)); err != nil {
		t.Fatalf("submit rendered: %v", err)
	}
	mustBindMask(t, c, "fog", impl.MaskRef{ID: "one", Kind: impl.MaskFromPainted, Width: 1, Height: 1, Data: []float32{1}})
	if err := c.SetStrength("fog", 0.75); err != nil {
		t.Fatalf("set strength: %v", err)
	}
	stats, err := c.Composite("fog")
	if err != nil {
		t.Fatalf("composite fog: %v", err)
	}
	if stats.EffectID != "fog" || stats.Pixels != 16 || stats.Written != 16 {
		t.Fatalf("stats identity/counts = %+v, want effect fog with 16/16 pixels", stats)
	}
	if stats.Strength != 0.75 || stats.Feather != 0 {
		t.Fatalf("stats strength/feather = %v/%d, want 0.75/0", stats.Strength, stats.Feather)
	}
	out := c.Output()
	if len(out.Pixels) != 16 {
		t.Fatalf("output buffer has %d pixels, want 16", len(out.Pixels))
	}
	if out.Pixels[0].R != 0.25 {
		t.Fatalf("blend at strength 0.75 = %v, want 0.25", out.Pixels[0].R)
	}
}

func TestMaskResolutionReflectsBinding(t *testing.T) {
	c := mustCompositor(t, 4, 4)
	if err := c.DeclareEffect("fog"); err != nil {
		t.Fatalf("declare fog: %v", err)
	}
	mask := impl.MaskRef{ID: "odd", Kind: impl.MaskFromMaterial, Width: 2, Height: 3, Data: make([]float32, 6)}
	mustBindMask(t, c, "fog", mask)
	w, h := c.MaskResolution("fog")
	if w != 2 || h != 3 {
		t.Fatalf("mask resolution = %dx%d, want 2x3", w, h)
	}
}

func TestSnapshotRestoreKeepsEffectsAndMasks(t *testing.T) {
	c := mustCompositor(t, 2, 1)
	if err := c.SetSource(solidImage(2, 1, 0, 0, 0, 1)); err != nil {
		t.Fatalf("set source: %v", err)
	}
	if err := c.DeclareEffect("fog"); err != nil {
		t.Fatalf("declare fog: %v", err)
	}
	if err := c.SubmitRendered("fog", solidImage(2, 1, 1, 1, 1, 1)); err != nil {
		t.Fatalf("submit rendered: %v", err)
	}
	mustBindMask(t, c, "fog", impl.MaskRef{ID: "m", Kind: impl.MaskFromPainted, Width: 2, Height: 1, Data: []float32{1, 1}})
	if err := c.SetStrength("fog", 0.5); err != nil {
		t.Fatalf("set strength: %v", err)
	}
	snap, err := c.Snapshot()
	if err != nil || len(snap) == 0 {
		t.Fatalf("snapshot err=%v len=%d, want a non empty payload", err, len(snap))
	}
	if err := c.SetStrength("fog", 1); err != nil {
		t.Fatalf("set strength after snapshot: %v", err)
	}
	if err := c.Restore(snap); err != nil {
		t.Fatalf("restore: %v", err)
	}
	stats, err := c.Composite("fog")
	if err != nil {
		t.Fatalf("composite after restore: %v", err)
	}
	if stats.Strength != 0.5 {
		t.Fatalf("restored strength = %v, want 0.5", stats.Strength)
	}
}

func TestRestoreRejectsCorruptPayload(t *testing.T) {
	c := mustCompositor(t, 2, 2)
	if err := c.Restore([]byte("not a snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must report ErrBadSnapshot, got %v", err)
	}
}

func TestCompositeUnderConcurrentSnapshot(t *testing.T) {
	c := mustCompositor(t, 2, 1)
	if err := c.SetSource(solidImage(2, 1, 0, 0, 0, 1)); err != nil {
		t.Fatalf("set source: %v", err)
	}
	if err := c.DeclareEffect("fog"); err != nil {
		t.Fatalf("declare fog: %v", err)
	}
	if err := c.SubmitRendered("fog", solidImage(2, 1, 1, 1, 1, 1)); err != nil {
		t.Fatalf("submit rendered: %v", err)
	}
	mustBindMask(t, c, "fog", impl.MaskRef{ID: "m", Kind: impl.MaskFromPainted, Width: 2, Height: 1, Data: []float32{1, 1}})
	errs := make(chan error, 16)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Composite("fog"); err != nil {
				errs <- err
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := c.Snapshot(); err != nil {
			errs <- err
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent composite/snapshot failed: %v", err)
	}
	out := c.Output()
	if len(out.Pixels) != 2 {
		t.Fatalf("output buffer has %d pixels after concurrent passes, want 2", len(out.Pixels))
	}
}
