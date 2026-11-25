package task

import (
	"errors"
	"math"
	"testing"

	impl "gsb/batch49-go-game-b-15"
)

func baseConfig() impl.DitherConfig {
	return impl.DitherConfig{TargetBits: 1, TileSize: 2, Strength: 1, MaxError: 0.25}
}

func mustDither(t *testing.T, cfg impl.DitherConfig) *impl.BitDepthDither {
	t.Helper()
	d, err := impl.NewBitDepthDither(cfg)
	if err != nil {
		t.Fatalf("new dither: %v", err)
	}
	if d == nil {
		t.Fatalf("dither must not be nil")
	}
	return d
}

func flatFrame(w, h, channels int, value float32) impl.Frame {
	data := make([]float32, w*h*channels)
	for i := range data {
		data[i] = value
	}
	return impl.Frame{Width: w, Height: h, Channels: channels, Data: data}
}

func TestNewRejectsZeroBitDepth(t *testing.T) {
	cfg := baseConfig()
	cfg.TargetBits = 0
	if _, err := impl.NewBitDepthDither(cfg); !errors.Is(err, impl.ErrInvalidBitDepth) {
		t.Fatalf("zero target bits must report ErrInvalidBitDepth, got %v", err)
	}
}

func TestNewRejectsOversizedBitDepth(t *testing.T) {
	cfg := baseConfig()
	cfg.TargetBits = 17
	if _, err := impl.NewBitDepthDither(cfg); !errors.Is(err, impl.ErrInvalidBitDepth) {
		t.Fatalf("target bits above 16 must report ErrInvalidBitDepth, got %v", err)
	}
}

func TestConfigureRejectsTileSize(t *testing.T) {
	d := mustDither(t, baseConfig())
	cfg := baseConfig()
	cfg.TileSize = 3
	if err := d.Configure(cfg); !errors.Is(err, impl.ErrInvalidTileSize) {
		t.Fatalf("non power of two tile must report ErrInvalidTileSize, got %v", err)
	}
	cfg.TileSize = impl.MaxTileSize * 2
	if err := d.Configure(cfg); !errors.Is(err, impl.ErrInvalidTileSize) {
		t.Fatalf("oversized tile must report ErrInvalidTileSize, got %v", err)
	}
}

func TestSetStrengthBounds(t *testing.T) {
	d := mustDither(t, baseConfig())
	if err := d.SetStrength(1.5); !errors.Is(err, impl.ErrInvalidStrength) {
		t.Fatalf("strength above one must report ErrInvalidStrength, got %v", err)
	}
	if err := d.SetStrength(-0.1); !errors.Is(err, impl.ErrInvalidStrength) {
		t.Fatalf("negative strength must report ErrInvalidStrength, got %v", err)
	}
	if err := d.SetStrength(math.NaN()); !errors.Is(err, impl.ErrInvalidStrength) {
		t.Fatalf("NaN strength must report ErrInvalidStrength, got %v", err)
	}
}

func TestConfigureRejectsNegativeErrorBudget(t *testing.T) {
	d := mustDither(t, baseConfig())
	cfg := baseConfig()
	cfg.MaxError = -0.5
	if err := d.Configure(cfg); !errors.Is(err, impl.ErrInvalidMaxError) {
		t.Fatalf("negative max error must report ErrInvalidMaxError, got %v", err)
	}
}

func TestProcessFrameRejectsChannelOutOfRange(t *testing.T) {
	cfg := baseConfig()
	cfg.DiffuseChannels = []int{0, 3}
	d := mustDither(t, cfg)
	if _, err := d.ProcessFrame(flatFrame(2, 2, 3, 0.5), 0); !errors.Is(err, impl.ErrInvalidChannel) {
		t.Fatalf("diffusion channel 3 on a 3 channel frame must report ErrInvalidChannel, got %v", err)
	}
}

func TestProcessFrameRejectsBufferMismatch(t *testing.T) {
	d := mustDither(t, baseConfig())
	frame := impl.Frame{Width: 2, Height: 2, Channels: 3, Data: make([]float32, 11)}
	if _, err := d.ProcessFrame(frame, 0); !errors.Is(err, impl.ErrInvalidFrame) {
		t.Fatalf("short buffer must report ErrInvalidFrame, got %v", err)
	}
}

func TestProcessFrameRejectsUnsupportedChannels(t *testing.T) {
	d := mustDither(t, baseConfig())
	if _, err := d.ProcessFrame(flatFrame(2, 2, 2, 0.5), 0); !errors.Is(err, impl.ErrInvalidFrame) {
		t.Fatalf("two channel frame must report ErrInvalidFrame, got %v", err)
	}
}

func TestStrengthZeroQuantisesWithoutDither(t *testing.T) {
	d := mustDither(t, baseConfig())
	if err := d.SetStrength(0); err != nil {
		t.Fatalf("set strength zero: %v", err)
	}
	frame := impl.Frame{Width: 2, Height: 1, Channels: 3, Data: []float32{
		0.4, 0.4, 0.4,
		0.6, 0.6, 0.6,
	}}
	out, err := d.ProcessFrame(frame, 0)
	if err != nil {
		t.Fatalf("process frame: %v", err)
	}
	if len(out.Data) != 6 {
		t.Fatalf("output buffer has %d samples, want 6", len(out.Data))
	}
	if out.Data[0] != 0 || out.Data[3] != 1 {
		t.Fatalf("pure quantisation = %v / %v, want 0 / 1", out.Data[0], out.Data[3])
	}
	if stats := d.Stats(); !stats.Skipped {
		t.Fatalf("stats must report the strength zero bypass, got %+v", stats)
	}
}

func TestOrderedDitherFollowsBayerTile(t *testing.T) {
	cfg := baseConfig()
	d := mustDither(t, cfg)
	out, err := d.ProcessFrame(flatFrame(2, 2, 3, 0.5), 0)
	if err != nil {
		t.Fatalf("process frame: %v", err)
	}
	want := []float32{0, 1, 1, 0}
	for i, w := range want {
		if got := out.Data[i*3]; got != w {
			t.Fatalf("dither sample %d = %v, want %v (Bayer 2x2 [0 2; 3 1])", i, got, w)
		}
	}
}

func TestPhaseAdvanceFollowsFrameSequence(t *testing.T) {
	d := mustDither(t, baseConfig())
	if got := d.PhaseFor(5); got != 1 {
		t.Fatalf("phase for sequence 5 = %d, want 1 with a 2x2 tile", got)
	}
	if _, err := d.ProcessFrame(flatFrame(2, 2, 3, 0.5), 5); err != nil {
		t.Fatalf("process frame: %v", err)
	}
	if got := d.Stats().Phase; got != 1 {
		t.Fatalf("stats phase = %d, want 1", got)
	}
}

func TestDiffusedChannelCountAndAlpha(t *testing.T) {
	cfg := baseConfig()
	cfg.DiffuseChannels = []int{0, 1, 2}
	d := mustDither(t, cfg)
	frame := impl.Frame{Width: 1, Height: 1, Channels: 4, Data: []float32{0.4, 0.6, 0.5, 1.0}}
	out, err := d.ProcessFrame(frame, 0)
	if err != nil {
		t.Fatalf("process frame: %v", err)
	}
	if len(out.Data) != 4 {
		t.Fatalf("output buffer has %d samples, want 4", len(out.Data))
	}
	if got := d.Stats().DiffusedChannels; got != 3 {
		t.Fatalf("diffused channel count = %d, want 3", got)
	}
	if out.Data[3] != 1 {
		t.Fatalf("alpha output = %v, want the fully opaque value 1", out.Data[3])
	}
}

func TestErrorDiffusionStaysWithinBudget(t *testing.T) {
	cfg := impl.DitherConfig{TargetBits: 4, TileSize: 4, Strength: 0.5, DiffuseChannels: []int{0}, MaxError: 0.1}
	d := mustDither(t, cfg)
	out, err := d.ProcessFrame(flatFrame(4, 4, 3, 0.53), 0)
	if err != nil {
		t.Fatalf("process frame: %v", err)
	}
	stats := d.Stats()
	if stats.Frames != 1 || stats.Pixels != 16 || stats.Tiles != 1 {
		t.Fatalf("stats counters = %+v, want 1 frame, 16 pixels, 1 tile", stats)
	}
	if stats.MaxAbsError > cfg.MaxError+1e-6 {
		t.Fatalf("max diffused error = %v, want at most %v", stats.MaxAbsError, cfg.MaxError)
	}
	for i, v := range out.Data {
		if v < 0 || v > 1 {
			t.Fatalf("output sample %d = %v, want it inside 0..1", i, v)
		}
	}
}

func TestSnapshotRestoreKeepsConfig(t *testing.T) {
	d := mustDither(t, baseConfig())
	if err := d.SetStrength(0.3); err != nil {
		t.Fatalf("set strength: %v", err)
	}
	if _, err := d.ProcessFrame(flatFrame(2, 2, 3, 0.5), 2); err != nil {
		t.Fatalf("process frame: %v", err)
	}
	snap, err := d.Snapshot()
	if err != nil || len(snap) == 0 {
		t.Fatalf("snapshot err=%v len=%d, want a non empty payload", err, len(snap))
	}
	if err := d.SetStrength(1); err != nil {
		t.Fatalf("set strength after snapshot: %v", err)
	}
	if err := d.Restore(snap); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := d.CurrentConfig().Strength; got != 0.3 {
		t.Fatalf("restored strength = %v, want 0.3", got)
	}
	if got := d.PhaseFor(2); got != 0 {
		t.Fatalf("phase for sequence 2 = %d, want 0", got)
	}
}

func TestRestoreRejectsCorruptPayload(t *testing.T) {
	d := mustDither(t, baseConfig())
	if err := d.Restore([]byte("dither")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must report ErrBadSnapshot, got %v", err)
	}
}
