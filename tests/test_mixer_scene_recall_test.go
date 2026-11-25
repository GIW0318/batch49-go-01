package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch49-go-game-c-01"
)

func newMixer(t *testing.T) *impl.MixerSceneRecall {
	t.Helper()
	m := impl.NewMixerSceneRecall()
	for _, id := range []string{"t1", "t2"} {
		if err := m.AddTrack(id); err != nil {
			t.Fatalf("add track %q must succeed, got %v", id, err)
		}
	}
	return m
}

func TestAddTrackAndRejectDuplicate(t *testing.T) {
	m := impl.NewMixerSceneRecall()
	if err := m.AddTrack("drums"); err != nil {
		t.Fatalf("first registration must succeed, got %v", err)
	}
	if err := m.AddTrack("drums"); !errors.Is(err, impl.ErrDuplicateTrack) {
		t.Fatalf("duplicate registration must report ErrDuplicateTrack, got %v", err)
	}
}

func TestSaveAssignsMonotonicVersions(t *testing.T) {
	m := newMixer(t)
	if err := m.SetParam("t1", impl.ParamVolume, 1); err != nil {
		t.Fatalf("set volume: %v", err)
	}
	v1, err := m.Save("verse")
	if err != nil {
		t.Fatalf("first save: %v", err)
	}
	if v1.Number != 1 || v1.Name != "verse" {
		t.Fatalf("first version = %+v, want number 1 name verse", v1)
	}
	v2, err := m.Save("chorus")
	if err != nil {
		t.Fatalf("second save: %v", err)
	}
	if v2.Number != 2 {
		t.Fatalf("second version number = %d, want 2", v2.Number)
	}
}

func TestSaveRejectsBlankName(t *testing.T) {
	m := newMixer(t)
	if _, err := m.Save("   "); !errors.Is(err, impl.ErrBlankName) {
		t.Fatalf("blank scene name must report ErrBlankName, got %v", err)
	}
	if _, err := m.Save(""); !errors.Is(err, impl.ErrBlankName) {
		t.Fatalf("empty scene name must report ErrBlankName, got %v", err)
	}
}

func TestVersionLookup(t *testing.T) {
	m := newMixer(t)
	if _, err := m.Save("intro"); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, ok := m.Version("intro")
	if !ok {
		t.Fatalf("saved scene must be found")
	}
	if got.Number != 1 || got.Name != "intro" {
		t.Fatalf("version lookup = %+v, want number 1 name intro", got)
	}
	if _, ok := m.Version("missing"); ok {
		t.Fatalf("unknown scene must not be found")
	}
}

func TestSetParamRejectsUnknownTrackAndParam(t *testing.T) {
	m := newMixer(t)
	if err := m.SetParam("ghost", impl.ParamVolume, 0.5); !errors.Is(err, impl.ErrUnknownTrack) {
		t.Fatalf("unknown track must report ErrUnknownTrack, got %v", err)
	}
	if err := m.SetParam("t1", "drive", 0.5); !errors.Is(err, impl.ErrUnknownParam) {
		t.Fatalf("unknown parameter must report ErrUnknownParam, got %v", err)
	}
}

func TestSetParamEnforcesRange(t *testing.T) {
	m := newMixer(t)
	if err := m.SetParam("t1", impl.ParamVolume, 1.5); !errors.Is(err, impl.ErrOutOfRange) {
		t.Fatalf("volume above 1 must report ErrOutOfRange, got %v", err)
	}
	if err := m.SetParam("t1", impl.ParamPan, -1.5); !errors.Is(err, impl.ErrOutOfRange) {
		t.Fatalf("pan below -1 must report ErrOutOfRange, got %v", err)
	}
	if err := m.SetParam("t1", impl.ParamPan, -0.5); err != nil {
		t.Fatalf("pan -0.5 must be accepted, got %v", err)
	}
	if got := m.Value("t1", impl.ParamPan); got != -0.5 {
		t.Fatalf("pan value = %v, want -0.5", got)
	}
}

func TestRecallRampInterpolatesHalfway(t *testing.T) {
	m := newMixer(t)
	if err := m.SetParam("t1", impl.ParamVolume, 1); err != nil {
		t.Fatalf("set volume: %v", err)
	}
	if _, err := m.Save("loud"); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := m.SetParam("t1", impl.ParamVolume, 0); err != nil {
		t.Fatalf("reset volume: %v", err)
	}
	if err := m.SetRamp(100); err != nil {
		t.Fatalf("set ramp: %v", err)
	}
	report, err := m.Recall("loud", nil)
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if report.Scene != "loud" || report.Version != 1 {
		t.Fatalf("report = %+v, want scene loud version 1", report)
	}
	m.Advance(50)
	got := m.Value("t1", impl.ParamVolume)
	if got < 0.48 || got > 0.52 {
		t.Fatalf("halfway volume = %v, want about 0.5", got)
	}
}

func TestRecallCompletesAtTarget(t *testing.T) {
	m := newMixer(t)
	if err := m.SetParam("t1", impl.ParamVolume, 0.75); err != nil {
		t.Fatalf("set volume: %v", err)
	}
	if _, err := m.Save("mid"); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := m.SetParam("t1", impl.ParamVolume, 0); err != nil {
		t.Fatalf("reset volume: %v", err)
	}
	if err := m.SetRamp(80); err != nil {
		t.Fatalf("set ramp: %v", err)
	}
	if _, err := m.Recall("mid", nil); err != nil {
		t.Fatalf("recall: %v", err)
	}
	m.Advance(200)
	if got := m.Value("t1", impl.ParamVolume); got != 0.75 {
		t.Fatalf("finished volume = %v, want 0.75", got)
	}
	m.Advance(200)
	if got := m.Value("t1", impl.ParamVolume); got != 0.75 {
		t.Fatalf("volume must stay at target, got %v", got)
	}
}

func TestRecallPartialTouchesOnlyListedFields(t *testing.T) {
	m := newMixer(t)
	if err := m.SetParam("t1", impl.ParamVolume, 1); err != nil {
		t.Fatalf("set volume: %v", err)
	}
	if err := m.SetParam("t1", impl.ParamPan, 0.5); err != nil {
		t.Fatalf("set pan: %v", err)
	}
	if _, err := m.Save("wide"); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := m.SetParam("t1", impl.ParamVolume, 0); err != nil {
		t.Fatalf("reset volume: %v", err)
	}
	if err := m.SetParam("t1", impl.ParamPan, 0); err != nil {
		t.Fatalf("reset pan: %v", err)
	}
	if err := m.SetRamp(20); err != nil {
		t.Fatalf("set ramp: %v", err)
	}
	report, err := m.Recall("wide", []string{impl.ParamVolume})
	if err != nil {
		t.Fatalf("partial recall: %v", err)
	}
	if len(report.Fields) != 1 || report.Fields[0] != impl.ParamVolume {
		t.Fatalf("report fields = %v, want [volume]", report.Fields)
	}
	m.Advance(100)
	if got := m.Value("t1", impl.ParamVolume); got != 1 {
		t.Fatalf("recalled volume = %v, want 1", got)
	}
	if got := m.Value("t1", impl.ParamPan); got != 0 {
		t.Fatalf("unlisted pan must keep its value, got %v", got)
	}
}

func TestRecallUnknownScene(t *testing.T) {
	m := newMixer(t)
	if _, err := m.Recall("never-saved", nil); !errors.Is(err, impl.ErrUnknownScene) {
		t.Fatalf("unknown scene must report ErrUnknownScene, got %v", err)
	}
}

func TestRecallDisabledTrackTakesTargetImmediately(t *testing.T) {
	m := newMixer(t)
	if err := m.SetParam("t1", impl.ParamVolume, 1); err != nil {
		t.Fatalf("set t1 volume: %v", err)
	}
	if err := m.SetParam("t2", impl.ParamVolume, 1); err != nil {
		t.Fatalf("set t2 volume: %v", err)
	}
	if _, err := m.Save("both-loud"); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := m.SetParam("t1", impl.ParamVolume, 0); err != nil {
		t.Fatalf("reset t1: %v", err)
	}
	if err := m.SetParam("t2", impl.ParamVolume, 0); err != nil {
		t.Fatalf("reset t2: %v", err)
	}
	if err := m.SetTrackEnabled("t1", false); err != nil {
		t.Fatalf("disable t1: %v", err)
	}
	if err := m.SetRamp(100); err != nil {
		t.Fatalf("set ramp: %v", err)
	}
	report, err := m.Recall("both-loud", nil)
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(report.Jumped) != 1 || report.Jumped[0] != "t1" {
		t.Fatalf("jumped tracks = %v, want [t1]", report.Jumped)
	}
	if got := m.Value("t1", impl.ParamVolume); got != 1 {
		t.Fatalf("disabled track volume = %v, want immediate target 1", got)
	}
	m.Advance(10)
	if got := m.Value("t2", impl.ParamVolume); got >= 1 || got <= 0 {
		t.Fatalf("enabled track must ramp, got %v", got)
	}
}

func TestSnapshotRestoreKeepsScenes(t *testing.T) {
	m := newMixer(t)
	if err := m.SetParam("t2", impl.ParamSend, 0.5); err != nil {
		t.Fatalf("set send: %v", err)
	}
	if _, err := m.Save("sends"); err != nil {
		t.Fatalf("save: %v", err)
	}
	data, err := m.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	fresh := impl.NewMixerSceneRecall()
	if err := fresh.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	scenes := fresh.Scenes()
	if len(scenes) != 1 || scenes[0] != "sends" {
		t.Fatalf("scenes after restore = %v, want [sends]", scenes)
	}
	got, ok := fresh.Version("sends")
	if !ok || got.Number != 1 {
		t.Fatalf("version after restore = %+v ok=%v, want number 1", got, ok)
	}
}

func TestRestoreRejectsCorruptPayload(t *testing.T) {
	m := newMixer(t)
	if err := m.Restore([]byte("not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt payload must report ErrBadSnapshot, got %v", err)
	}
}

func TestDirtyClearedBySave(t *testing.T) {
	m := newMixer(t)
	if err := m.SetParam("t1", impl.ParamVolume, 0.25); err != nil {
		t.Fatalf("set volume: %v", err)
	}
	if !m.Dirty() {
		t.Fatalf("dirty must be true after a parameter change")
	}
	if _, err := m.Save("clean"); err != nil {
		t.Fatalf("save: %v", err)
	}
	if m.Dirty() {
		t.Fatalf("dirty must be false after a save")
	}
}

func TestConcurrentSetParamKeepsEveryValue(t *testing.T) {
	m := newMixer(t)
	type write struct {
		track string
		param string
		value float64
	}
	writes := []write{
		{"t1", impl.ParamVolume, 0.25},
		{"t1", impl.ParamPan, -0.25},
		{"t1", impl.ParamSend, 0.75},
		{"t1", impl.ParamFX, 1},
		{"t2", impl.ParamVolume, 0.5},
		{"t2", impl.ParamPan, 0.5},
		{"t2", impl.ParamSend, 1},
		{"t2", impl.ParamFX, 0},
	}
	var wg sync.WaitGroup
	errs := make([]error, len(writes))
	for i := range writes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w := writes[i]
			errs[i] = m.SetParam(w.track, w.param, w.value)
		}(i)
	}
	wg.Wait()
	for i, w := range writes {
		if errs[i] != nil {
			t.Fatalf("write %d (%s/%s) failed: %v", i, w.track, w.param, errs[i])
		}
	}
	for _, w := range writes {
		if got := m.Value(w.track, w.param); got != w.value {
			t.Fatalf("%s/%s = %v, want %v", w.track, w.param, got, w.value)
		}
	}
}
