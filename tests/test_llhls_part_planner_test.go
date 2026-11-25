package task

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	impl "gsb/batch49-go-game-c-07"
)

func plannerConfig() impl.PlannerConfig {
	return impl.PlannerConfig{
		PartTarget:     200 * time.Millisecond,
		WindowSegments: 6,
		MaxPartsPerSeg: 12,
	}
}

func part(index int, dur time.Duration, uri string, independent bool) impl.PartInfo {
	return impl.PartInfo{Index: index, Duration: dur, URI: uri, Independent: independent}
}

func mustPublish(t *testing.T, p *impl.LlhlsPartPlanner, seg uint64, info impl.PartInfo) {
	t.Helper()
	if err := p.PublishPart(seg, info); err != nil {
		t.Fatalf("publish seg %d part %d: %v", seg, info.Index, err)
	}
}

func TestPublishFirstPartAppearsInPlaylist(t *testing.T) {
	p := impl.NewLlhlsPartPlanner(plannerConfig())
	mustPublish(t, p, 3, part(0, 120*time.Millisecond, "live/3.0.m4s", true))
	m, err := p.Playlist()
	if err != nil {
		t.Fatalf("playlist: %v", err)
	}
	if m.Version != impl.ManifestVersion {
		t.Fatalf("manifest version = %d, want %d", m.Version, impl.ManifestVersion)
	}
	if m.TargetDuration != 200*time.Millisecond {
		t.Fatalf("target duration = %v, want the part target 200ms", m.TargetDuration)
	}
	if m.StartNumber != 3 {
		t.Fatalf("start number = %d, want the first published segment 3", m.StartNumber)
	}
	if len(m.Parts) != 1 {
		t.Fatalf("playlist parts = %d, want 1", len(m.Parts))
	}
	got := m.Parts[0]
	if got.Segment != 3 || got.Index != 0 || got.URI != "live/3.0.m4s" || got.Duration != 120*time.Millisecond {
		t.Fatalf("part row = %+v, want segment 3 index 0 uri live/3.0.m4s", got)
	}
	if !got.Independent {
		t.Fatalf("first part must be marked independent")
	}
}

func TestPublishPartRejectsOverlongDuration(t *testing.T) {
	p := impl.NewLlhlsPartPlanner(plannerConfig())
	if err := p.SetPartTarget(0); !errors.Is(err, impl.ErrInvalidPartTarget) {
		t.Fatalf("zero part target = %v, want ErrInvalidPartTarget", err)
	}
	if err := p.PublishPart(3, part(0, 250*time.Millisecond, "live/3.0.m4s", true)); !errors.Is(err, impl.ErrPartDuration) {
		t.Fatalf("overlong part = %v, want ErrPartDuration", err)
	}
	if err := p.PublishPart(3, part(0, 0, "live/3.0.m4s", true)); !errors.Is(err, impl.ErrPartDuration) {
		t.Fatalf("zero duration part = %v, want ErrPartDuration", err)
	}
	if err := p.PublishPart(3, part(0, 200*time.Millisecond, "live/3.0.m4s", true)); err != nil {
		t.Fatalf("part at the target must be accepted, got %v", err)
	}
}

func TestPublishPartRequiresContiguousIndex(t *testing.T) {
	p := impl.NewLlhlsPartPlanner(plannerConfig())
	if err := p.PublishPart(3, part(1, 100*time.Millisecond, "live/3.1.m4s", false)); !errors.Is(err, impl.ErrPartOrder) {
		t.Fatalf("first published index 1 = %v, want ErrPartOrder", err)
	}
	mustPublish(t, p, 3, part(0, 100*time.Millisecond, "live/3.0.m4s", true))
	if err := p.PublishPart(3, part(0, 100*time.Millisecond, "live/3.0.m4s", true)); !errors.Is(err, impl.ErrDuplicatePart) {
		t.Fatalf("republished index 0 = %v, want ErrDuplicatePart", err)
	}
	if err := p.PublishPart(3, part(2, 100*time.Millisecond, "live/3.2.m4s", false)); !errors.Is(err, impl.ErrPartOrder) {
		t.Fatalf("index 2 after index 0 = %v, want ErrPartOrder", err)
	}
	if got := len(p.Parts(3)); got != 1 {
		t.Fatalf("rejected parts changed the segment, got %d parts", got)
	}
}

func TestFirstPartMustBeIndependent(t *testing.T) {
	p := impl.NewLlhlsPartPlanner(plannerConfig())
	if err := p.PublishPart(4, part(0, 100*time.Millisecond, "live/4.0.m4s", false)); !errors.Is(err, impl.ErrNotIndependent) {
		t.Fatalf("non independent first part = %v, want ErrNotIndependent", err)
	}
	if got := len(p.Parts(4)); got != 0 {
		t.Fatalf("rejected first part must not be stored, got %d", got)
	}
}

func TestNonFirstPartMustNotClaimIndependent(t *testing.T) {
	p := impl.NewLlhlsPartPlanner(plannerConfig())
	mustPublish(t, p, 4, part(0, 100*time.Millisecond, "live/4.0.m4s", true))
	if err := p.PublishPart(4, part(1, 100*time.Millisecond, "live/4.1.m4s", true)); !errors.Is(err, impl.ErrBadIndependent) {
		t.Fatalf("independent non first part = %v, want ErrBadIndependent", err)
	}
	if got := len(p.Parts(4)); got != 1 {
		t.Fatalf("rejected part must not be stored, got %d parts", got)
	}
}

func TestCompleteSegmentClosesSegment(t *testing.T) {
	p := impl.NewLlhlsPartPlanner(plannerConfig())
	mustPublish(t, p, 5, part(0, 100*time.Millisecond, "live/5.0.m4s", true))
	if err := p.CompleteSegment(5); err != nil {
		t.Fatalf("complete segment: %v", err)
	}
	if err := p.PublishPart(5, part(1, 100*time.Millisecond, "live/5.1.m4s", false)); !errors.Is(err, impl.ErrSegmentClosed) {
		t.Fatalf("publish to a closed segment = %v, want ErrSegmentClosed", err)
	}
	if err := p.CompleteSegment(5); !errors.Is(err, impl.ErrSegmentClosed) {
		t.Fatalf("complete twice = %v, want ErrSegmentClosed", err)
	}
}

func TestCompleteEmptySegmentRejected(t *testing.T) {
	p := impl.NewLlhlsPartPlanner(plannerConfig())
	if err := p.CompleteSegment(9); !errors.Is(err, impl.ErrEmptySegment) {
		t.Fatalf("complete a segment without parts = %v, want ErrEmptySegment", err)
	}
	if got := len(p.Parts(9)); got != 0 {
		t.Fatalf("empty segment parts = %d, want 0", got)
	}
}

func TestWindowEvictsOldestSegments(t *testing.T) {
	cfg := plannerConfig()
	cfg.WindowSegments = 2
	p := impl.NewLlhlsPartPlanner(cfg)
	for seg := uint64(1); seg <= 3; seg++ {
		mustPublish(t, p, seg, part(0, 100*time.Millisecond, fmt.Sprintf("live/%d.0.m4s", seg), true))
		if err := p.CompleteSegment(seg); err != nil {
			t.Fatalf("complete segment %d: %v", seg, err)
		}
	}
	m, err := p.Playlist()
	if err != nil {
		t.Fatalf("playlist: %v", err)
	}
	if m.StartNumber != 2 {
		t.Fatalf("start number = %d, want 2 after the window slides", m.StartNumber)
	}
	for _, row := range m.Parts {
		if row.Segment < 2 {
			t.Fatalf("evicted segment %d still in playlist", row.Segment)
		}
	}
	if len(m.Parts) != 2 {
		t.Fatalf("playlist parts = %d, want 2", len(m.Parts))
	}
	if st := p.Stats(); st.Evicted != 1 || st.Segments != 2 {
		t.Fatalf("stats = %+v, want evicted 1 segments 2", st)
	}
}

func TestSetWindowRejectsOutOfRange(t *testing.T) {
	p := impl.NewLlhlsPartPlanner(plannerConfig())
	if err := p.SetWindow(0); !errors.Is(err, impl.ErrInvalidWindow) {
		t.Fatalf("window 0 = %v, want ErrInvalidWindow", err)
	}
	if err := p.SetWindow(impl.MaxWindowSegments + 1); !errors.Is(err, impl.ErrInvalidWindow) {
		t.Fatalf("window over the cap = %v, want ErrInvalidWindow", err)
	}
	if err := p.SetWindow(4); err != nil {
		t.Fatalf("window 4 must be accepted, got %v", err)
	}
}

func TestPartCountCapEnforced(t *testing.T) {
	cfg := plannerConfig()
	cfg.MaxPartsPerSeg = 4
	p := impl.NewLlhlsPartPlanner(cfg)
	for i := 0; i < 4; i++ {
		mustPublish(t, p, 6, part(i, 50*time.Millisecond, fmt.Sprintf("live/6.%d.m4s", i), i == 0))
	}
	err := p.PublishPart(6, part(4, 50*time.Millisecond, "live/6.4.m4s", false))
	if !errors.Is(err, impl.ErrTooManyParts) {
		t.Fatalf("fifth part with cap 4 = %v, want ErrTooManyParts", err)
	}
	if got := len(p.Parts(6)); got != 4 {
		t.Fatalf("parts after the rejected publish = %d, want 4", got)
	}
}

func TestPreloadHintFollowsNewestPart(t *testing.T) {
	p := impl.NewLlhlsPartPlanner(plannerConfig())
	mustPublish(t, p, 5, part(0, 100*time.Millisecond, "live/5.0.m4s", true))
	mustPublish(t, p, 5, part(1, 100*time.Millisecond, "live/5.1.m4s", false))
	hints := p.Preload()
	if len(hints) != 1 {
		t.Fatalf("preload hints = %d, want 1", len(hints))
	}
	if hints[0].URI != "live/5.2.m4s" || hints[0].PartIndex != 2 || hints[0].Type != impl.PreloadTypePart {
		t.Fatalf("preload hint = %+v, want the next part live/5.2.m4s", hints[0])
	}
	m, err := p.Playlist()
	if err != nil {
		t.Fatalf("playlist: %v", err)
	}
	if len(m.Preload) != 1 || m.Preload[0].URI != "live/5.2.m4s" {
		t.Fatalf("manifest preload = %+v, want the same hint", m.Preload)
	}
}

func TestBlockingReloadEmittedWhenDeclared(t *testing.T) {
	cfg := plannerConfig()
	cfg.HoldBack = 3 * time.Second
	p := impl.NewLlhlsPartPlanner(cfg)
	mustPublish(t, p, 7, part(0, 100*time.Millisecond, "live/7.0.m4s", true))
	m, err := p.Playlist()
	if err != nil {
		t.Fatalf("playlist: %v", err)
	}
	if m.Blocking == nil {
		t.Fatalf("hold-back declared but the manifest carries no blocking reload parameters")
	}
	if m.Blocking.HoldBack != 3*time.Second || m.Blocking.PartTarget != 200*time.Millisecond {
		t.Fatalf("blocking reload = %+v, want hold back 3s and part target 200ms", *m.Blocking)
	}
}

func TestSnapshotRestoreKeepsWindowAndStartNumber(t *testing.T) {
	cfg := plannerConfig()
	cfg.WindowSegments = 2
	p := impl.NewLlhlsPartPlanner(cfg)
	mustPublish(t, p, 2, part(0, 100*time.Millisecond, "live/2.0.m4s", true))
	mustPublish(t, p, 3, part(0, 100*time.Millisecond, "live/3.0.m4s", true))
	mustPublish(t, p, 3, part(1, 100*time.Millisecond, "live/3.1.m4s", false))
	data, err := p.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewLlhlsPartPlanner(cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	m, err := restored.Playlist()
	if err != nil {
		t.Fatalf("playlist after restore: %v", err)
	}
	if m.StartNumber != 2 || len(m.Parts) != 3 {
		t.Fatalf("restored manifest start=%d parts=%d, want start 2 and 3 parts", m.StartNumber, len(m.Parts))
	}
	if st := restored.Stats(); st.Segments != 2 || st.Parts != 3 {
		t.Fatalf("restored stats = %+v, want segments 2 parts 3", st)
	}
	mustPublish(t, restored, 3, part(2, 100*time.Millisecond, "live/3.2.m4s", false))
	if got := len(restored.Parts(3)); got != 3 {
		t.Fatalf("parts after restore and publish = %d, want 3", got)
	}
	if hints := restored.Preload(); len(hints) != 1 || hints[0].URI != "live/3.3.m4s" {
		t.Fatalf("preload after restore = %+v, want live/3.3.m4s", hints)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	p := impl.NewLlhlsPartPlanner(plannerConfig())
	mustPublish(t, p, 8, part(0, 100*time.Millisecond, "live/8.0.m4s", true))
	if err := p.Restore([]byte("not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot = %v, want ErrBadSnapshot", err)
	}
	if got := len(p.Parts(8)); got != 1 {
		t.Fatalf("failed restore must keep live state, got %d parts", got)
	}
	if _, err := p.Playlist(); err != nil {
		t.Fatalf("live planner must keep working after a rejected restore, got %v", err)
	}
}

func TestConcurrentPublishAndPlaylist(t *testing.T) {
	cfg := plannerConfig()
	cfg.WindowSegments = 8
	p := impl.NewLlhlsPartPlanner(cfg)
	for seg := uint64(100); seg < 108; seg++ {
		mustPublish(t, p, seg, part(0, 100*time.Millisecond, fmt.Sprintf("live/%d.0.m4s", seg), true))
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for seg := uint64(100); seg < 108; seg++ {
		wg.Add(1)
		go func(seg uint64) {
			defer wg.Done()
			info := part(1, 100*time.Millisecond, fmt.Sprintf("live/%d.1.m4s", seg), false)
			if err := p.PublishPart(seg, info); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
			}
		}(seg)
	}
	wg.Wait()
	if firstErr != nil {
		t.Fatalf("concurrent publish failed: %v", firstErr)
	}
	m, err := p.Playlist()
	if err != nil {
		t.Fatalf("playlist: %v", err)
	}
	if len(m.Parts) != 16 {
		t.Fatalf("playlist parts = %d, want 16", len(m.Parts))
	}
	if m.StartNumber != 100 {
		t.Fatalf("start number = %d, want 100", m.StartNumber)
	}
	if hints := p.Preload(); len(hints) != 1 || hints[0].URI != "live/107.2.m4s" {
		t.Fatalf("preload hint = %+v, want live/107.2.m4s", hints)
	}
}
