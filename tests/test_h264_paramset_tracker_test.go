package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch49-go-game-c-03"
)

// bitWriter assembles a raw RBSP with emulation prevention applied on finish.
type bitWriter struct {
	bytes []byte
	cur   byte
	n     uint
}

func (w *bitWriter) bit(v uint32) {
	w.cur = w.cur<<1 | byte(v&1)
	w.n++
	if w.n == 8 {
		w.bytes = append(w.bytes, w.cur)
		w.cur = 0
		w.n = 0
	}
}

func (w *bitWriter) u(n int, v uint32) {
	for i := n - 1; i >= 0; i-- {
		w.bit((v >> uint(i)) & 1)
	}
}

func (w *bitWriter) ue(v uint32) {
	code := v + 1
	lead := 0
	for x := code; x > 1; x >>= 1 {
		lead++
	}
	for i := 0; i < lead; i++ {
		w.bit(0)
	}
	for i := lead; i >= 0; i-- {
		w.bit((code >> uint(i)) & 1)
	}
}

func (w *bitWriter) finish() []byte {
	w.bit(1)
	for w.n != 0 {
		w.bit(0)
	}
	out := make([]byte, 0, len(w.bytes)+2)
	zeros := 0
	for _, b := range w.bytes {
		if zeros >= 2 && b <= 0x03 {
			out = append(out, 0x03)
			zeros = 0
		}
		out = append(out, b)
		if b == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}

type spsSpec struct {
	profileIDC     int
	levelIDC       int
	id             int
	numRefFrames   int
	widthMBs       int
	heightMapUnits int
	frameMBSOnly   bool
	cropLeft       uint32
	cropRight      uint32
	cropTop        uint32
	cropBottom     uint32
}

func buildSPS(p spsSpec) []byte {
	w := &bitWriter{}
	w.u(8, uint32(p.profileIDC))
	w.u(8, 0)
	w.u(8, uint32(p.levelIDC))
	w.ue(uint32(p.id))
	w.ue(0) // log2_max_frame_num_minus4
	w.ue(2) // pic_order_cnt_type
	w.ue(uint32(p.numRefFrames))
	w.u(1, 0) // gaps_in_frame_num_value_allowed_flag
	w.ue(uint32(p.widthMBs - 1))
	w.ue(uint32(p.heightMapUnits - 1))
	if p.frameMBSOnly {
		w.u(1, 1)
	} else {
		w.u(1, 0)
		w.u(1, 0) // mb_adaptive_frame_field_flag
	}
	w.u(1, 1) // direct_8x8_inference_flag
	if p.cropLeft+p.cropRight+p.cropTop+p.cropBottom > 0 {
		w.u(1, 1)
		w.ue(p.cropLeft)
		w.ue(p.cropRight)
		w.ue(p.cropTop)
		w.ue(p.cropBottom)
	} else {
		w.u(1, 0)
	}
	w.u(1, 0) // vui_parameters_present_flag
	return append([]byte{0x67}, w.finish()...)
}

func progressiveSPS(id, widthMBs, heightUnits int) []byte {
	return buildSPS(spsSpec{
		profileIDC:     66,
		levelIDC:       30,
		id:             id,
		numRefFrames:   2,
		widthMBs:       widthMBs,
		heightMapUnits: heightUnits,
		frameMBSOnly:   true,
	})
}

func buildPPS(ppsID, spsID int, entropy, deblock bool) []byte {
	w := &bitWriter{}
	w.ue(uint32(ppsID))
	w.ue(uint32(spsID))
	if entropy {
		w.u(1, 1)
	} else {
		w.u(1, 0)
	}
	w.u(1, 0) // bottom_field_pic_order_in_frame_present_flag
	w.ue(0)   // num_slice_groups_minus1
	w.ue(0)   // num_ref_idx_l0_default_active_minus1
	w.ue(0)   // num_ref_idx_l1_default_active_minus1
	w.u(1, 0) // weighted_pred_flag
	w.u(2, 0) // weighted_bipred_idc
	w.ue(0)   // pic_init_qp_minus26
	w.ue(0)   // pic_init_qs_minus26
	w.ue(0)   // chroma_qp_index_offset
	if deblock {
		w.u(1, 1)
	} else {
		w.u(1, 0)
	}
	w.u(1, 0) // constrained_intra_pred_flag
	w.u(1, 0) // redundant_pic_cnt_present_flag
	return append([]byte{0x68}, w.finish()...)
}

func TestParseSPSProgressiveDimensions(t *testing.T) {
	tr := impl.NewParamSetTracker()
	info, err := tr.ParseSPS(progressiveSPS(0, 20, 15))
	if err != nil {
		t.Fatalf("parse sps: %v", err)
	}
	if info.Width != 320 || info.Height != 240 {
		t.Fatalf("dimensions = %dx%d, want 320x240", info.Width, info.Height)
	}
	if info.ID != 0 || info.ProfileIDC != 66 || info.LevelIDC != 30 {
		t.Fatalf("ids = id %d profile %d level %d, want 0/66/30", info.ID, info.ProfileIDC, info.LevelIDC)
	}
	if !info.FrameMBSOnly {
		t.Fatalf("frame_mbs_only must be true for a progressive set")
	}
	if info.NumRefFrames != 2 {
		t.Fatalf("num ref frames = %d, want 2", info.NumRefFrames)
	}
}

func TestParseSPSAppliesCropping(t *testing.T) {
	tr := impl.NewParamSetTracker()
	spec := spsSpec{
		profileIDC:     66,
		levelIDC:       30,
		id:             1,
		numRefFrames:   1,
		widthMBs:       20,
		heightMapUnits: 15,
		frameMBSOnly:   true,
		cropBottom:     2,
	}
	info, err := tr.ParseSPS(buildSPS(spec))
	if err != nil {
		t.Fatalf("parse cropped sps: %v", err)
	}
	if info.Width != 320 || info.Height != 236 {
		t.Fatalf("cropped dimensions = %dx%d, want 320x236", info.Width, info.Height)
	}
}

func TestParseSPSRejectsTruncatedPayload(t *testing.T) {
	tr := impl.NewParamSetTracker()
	if _, err := tr.ParseSPS([]byte{0x67, 0x42}); !errors.Is(err, impl.ErrBadSPS) {
		t.Fatalf("truncated sps must report ErrBadSPS, got %v", err)
	}
}

func TestParseSPSRejectsWrongNALType(t *testing.T) {
	tr := impl.NewParamSetTracker()
	if _, err := tr.ParseSPS([]byte{0x68, 0x42, 0x00, 0x1e}); !errors.Is(err, impl.ErrBadSPS) {
		t.Fatalf("non sps nal must report ErrBadSPS, got %v", err)
	}
}

func TestParsePPSReadsIDsAndFlags(t *testing.T) {
	tr := impl.NewParamSetTracker()
	info, err := tr.ParsePPS(buildPPS(2, 1, true, true))
	if err != nil {
		t.Fatalf("parse pps: %v", err)
	}
	if info.ID != 2 || info.SPSID != 1 {
		t.Fatalf("pps ids = %d/%d, want 2/1", info.ID, info.SPSID)
	}
	if !info.EntropyCoding || !info.Deblocking {
		t.Fatalf("pps flags = entropy %v deblock %v, want both true", info.EntropyCoding, info.Deblocking)
	}
}

func TestParsePPSRejectsTruncatedPayload(t *testing.T) {
	tr := impl.NewParamSetTracker()
	if _, err := tr.ParsePPS([]byte{0x68, 0x80}); !errors.Is(err, impl.ErrBadPPS) {
		t.Fatalf("truncated pps must report ErrBadPPS, got %v", err)
	}
}

func TestActivateUnknownPPS(t *testing.T) {
	tr := impl.NewParamSetTracker()
	if err := tr.Activate(9); !errors.Is(err, impl.ErrUnknownPPS) {
		t.Fatalf("unknown pps must report ErrUnknownPPS, got %v", err)
	}
}

func TestActivateWithMissingSPS(t *testing.T) {
	tr := impl.NewParamSetTracker()
	if _, err := tr.ParsePPS(buildPPS(0, 5, false, true)); err != nil {
		t.Fatalf("parse pps: %v", err)
	}
	if err := tr.Activate(0); !errors.Is(err, impl.ErrUnknownSPS) {
		t.Fatalf("missing sps must report ErrUnknownSPS, got %v", err)
	}
}

func TestActivateMakesActiveSets(t *testing.T) {
	tr := impl.NewParamSetTracker()
	if _, err := tr.ParseSPS(progressiveSPS(0, 20, 15)); err != nil {
		t.Fatalf("parse sps: %v", err)
	}
	if _, err := tr.ParsePPS(buildPPS(0, 0, false, true)); err != nil {
		t.Fatalf("parse pps: %v", err)
	}
	if err := tr.Activate(0); err != nil {
		t.Fatalf("activate pps 0: %v", err)
	}
	active, ok := tr.ActiveSPS()
	if !ok {
		t.Fatalf("active sps must be present after activation")
	}
	if active.Width != 320 || active.Height != 240 {
		t.Fatalf("active dimensions = %dx%d, want 320x240", active.Width, active.Height)
	}
	pps, ok := tr.ActivePPS()
	if !ok || pps.ID != 0 || pps.SPSID != 0 {
		t.Fatalf("active pps = %+v ok=%v, want id 0 sps 0", pps, ok)
	}
	if w, h := tr.Dimensions(); w != 320 || h != 240 {
		t.Fatalf("Dimensions() = %dx%d, want 320x240", w, h)
	}
}

func TestParsedSetIsNotActiveUntilActivate(t *testing.T) {
	tr := impl.NewParamSetTracker()
	info, err := tr.ParseSPS(progressiveSPS(0, 30, 20))
	if err != nil {
		t.Fatalf("parse sps: %v", err)
	}
	if info.Width != 480 || info.Height != 320 {
		t.Fatalf("parsed dimensions = %dx%d, want 480x320", info.Width, info.Height)
	}
	if _, err := tr.ParsePPS(buildPPS(1, 0, false, true)); err != nil {
		t.Fatalf("parse pps: %v", err)
	}
	if _, ok := tr.ActiveSPS(); ok {
		t.Fatalf("set must not be active before Activate")
	}
	if err := tr.Activate(1); err != nil {
		t.Fatalf("activate pps 1: %v", err)
	}
	active, ok := tr.ActiveSPS()
	if !ok || active.Width != 480 {
		t.Fatalf("active after Activate = %+v ok=%v, want width 480", active, ok)
	}
}

func TestResolutionChangedAfterSPSChange(t *testing.T) {
	tr := impl.NewParamSetTracker()
	if _, err := tr.ParseSPS(progressiveSPS(0, 20, 15)); err != nil {
		t.Fatalf("parse first sps: %v", err)
	}
	if _, err := tr.ParsePPS(buildPPS(0, 0, false, true)); err != nil {
		t.Fatalf("parse pps: %v", err)
	}
	if err := tr.Activate(0); err != nil {
		t.Fatalf("first activate: %v", err)
	}
	if _, err := tr.ParseSPS(progressiveSPS(0, 40, 15)); err != nil {
		t.Fatalf("parse second sps: %v", err)
	}
	if err := tr.Activate(0); err != nil {
		t.Fatalf("second activate: %v", err)
	}
	if !tr.ResolutionChanged() {
		t.Fatalf("resolution change from 320x240 to 640x240 must be reported")
	}
	if w, h := tr.Dimensions(); w != 640 || h != 240 {
		t.Fatalf("new dimensions = %dx%d, want 640x240", w, h)
	}
}

func TestSPSVersionIncrementsOnContentChange(t *testing.T) {
	tr := impl.NewParamSetTracker()
	if _, err := tr.ParseSPS(progressiveSPS(0, 20, 15)); err != nil {
		t.Fatalf("parse first sps: %v", err)
	}
	if _, err := tr.ParseSPS(progressiveSPS(0, 40, 15)); err != nil {
		t.Fatalf("parse changed sps: %v", err)
	}
	version, ok := tr.SPSVersion(0)
	if !ok {
		t.Fatalf("sps 0 must be stored")
	}
	if version != 2 {
		t.Fatalf("sps version = %d, want 2 after one content change", version)
	}
}

func TestSnapshotRestoreKeepsActive(t *testing.T) {
	tr := impl.NewParamSetTracker()
	if _, err := tr.ParseSPS(progressiveSPS(0, 20, 15)); err != nil {
		t.Fatalf("parse sps: %v", err)
	}
	if _, err := tr.ParsePPS(buildPPS(0, 0, false, true)); err != nil {
		t.Fatalf("parse pps: %v", err)
	}
	if err := tr.Activate(0); err != nil {
		t.Fatalf("activate: %v", err)
	}
	data, err := tr.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	fresh := impl.NewParamSetTracker()
	if err := fresh.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	active, ok := fresh.ActiveSPS()
	if !ok || active.Width != 320 || active.Height != 240 {
		t.Fatalf("restored active sps = %+v ok=%v, want 320x240", active, ok)
	}
	if pps, ok := fresh.ActivePPS(); !ok || pps.ID != 0 {
		t.Fatalf("restored active pps = %+v ok=%v, want id 0", pps, ok)
	}
}

func TestRestoreRejectsCorruptPayload(t *testing.T) {
	tr := impl.NewParamSetTracker()
	if err := tr.Restore([]byte("broken")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt payload must report ErrBadSnapshot, got %v", err)
	}
}

func TestConcurrentParseDistinctSets(t *testing.T) {
	tr := impl.NewParamSetTracker()
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := tr.ParseSPS(progressiveSPS(i, i+1, 15)); err != nil {
				errs <- err
				return
			}
			if _, err := tr.ParsePPS(buildPPS(i, i, false, true)); err != nil {
				errs <- err
				return
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent parse failed: %v", err)
	}
	if err := tr.Activate(3); err != nil {
		t.Fatalf("activate pps 3: %v", err)
	}
	if w, _ := tr.Dimensions(); w != 64 {
		t.Fatalf("active width = %d, want 64", w)
	}
	if version, ok := tr.SPSVersion(3); !ok || version != 1 {
		t.Fatalf("sps 3 version = %d ok=%v, want 1", version, ok)
	}
}
