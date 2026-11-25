package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch49-go-game-c-10"
)

func planConfig() impl.PlanConfig {
	return impl.PlanConfig{TotalRate: 3000, MinRate: 0, MaxRate: 10000, Tolerance: 0}
}

func stats(frame int64, complexity float64) impl.FrameStats {
	return impl.FrameStats{Frame: frame, Bytes: 100, Complexity: complexity}
}

func mustAdd(t *testing.T, p *impl.TwoPassPlanner, s impl.FrameStats) {
	t.Helper()
	if err := p.AddStats(s); err != nil {
		t.Fatalf("add frame %d: %v", s.Frame, err)
	}
}

func mustScene(t *testing.T, p *impl.TwoPassPlanner, frame int64) {
	t.Helper()
	if err := p.MarkScene(frame); err != nil {
		t.Fatalf("mark scene %d: %v", frame, err)
	}
}

func mustPlan(t *testing.T, p *impl.TwoPassPlanner, cfg impl.PlanConfig) impl.Allocation {
	t.Helper()
	a, err := p.Plan(cfg)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	return a
}

func TestAddStatsRejectsBadInput(t *testing.T) {
	p := impl.NewTwoPassPlanner()
	if err := p.AddStats(impl.FrameStats{Frame: -1, Complexity: 1}); !errors.Is(err, impl.ErrBadStats) {
		t.Fatalf("negative frame = %v, want ErrBadStats", err)
	}
	if err := p.AddStats(impl.FrameStats{Frame: 0, Complexity: -0.5}); !errors.Is(err, impl.ErrBadStats) {
		t.Fatalf("negative complexity = %v, want ErrBadStats", err)
	}
	if err := p.AddStats(impl.FrameStats{Frame: 0, Bytes: -1, Complexity: 1}); !errors.Is(err, impl.ErrBadStats) {
		t.Fatalf("negative bytes = %v, want ErrBadStats", err)
	}
	if st := p.Stats(); st.Frames != 0 {
		t.Fatalf("rejected stats changed the frame count to %d", st.Frames)
	}
}

func TestAddStatsRejectsDuplicateFrame(t *testing.T) {
	p := impl.NewTwoPassPlanner()
	mustAdd(t, p, stats(0, 1))
	if err := p.AddStats(stats(0, 2)); !errors.Is(err, impl.ErrDuplicateFrame) {
		t.Fatalf("duplicate frame = %v, want ErrDuplicateFrame", err)
	}
	if st := p.Stats(); st.Frames != 1 {
		t.Fatalf("frames after duplicate = %d, want 1", st.Frames)
	}
}

func TestAddStatsRejectsOutOfOrder(t *testing.T) {
	p := impl.NewTwoPassPlanner()
	mustAdd(t, p, stats(5, 1))
	if err := p.AddStats(stats(3, 1)); !errors.Is(err, impl.ErrOutOfOrder) {
		t.Fatalf("backwards frame = %v, want ErrOutOfOrder", err)
	}
	if st := p.Stats(); st.Frames != 1 {
		t.Fatalf("frames after rejected stat = %d, want 1", st.Frames)
	}
}

func TestSceneBoundariesSplitAtSceneStart(t *testing.T) {
	p := impl.NewTwoPassPlanner()
	if err := p.MarkScene(0); !errors.Is(err, impl.ErrBadScene) {
		t.Fatalf("marking frame 0 again = %v, want ErrBadScene", err)
	}
	mustScene(t, p, 5)
	if err := p.MarkScene(5); !errors.Is(err, impl.ErrBadScene) {
		t.Fatalf("duplicate scene boundary = %v, want ErrBadScene", err)
	}
	bounds := p.SceneBoundaries()
	if len(bounds) != 2 || bounds[0] != 0 || bounds[1] != 5 {
		t.Fatalf("scene boundaries = %v, want [0 5]", bounds)
	}
	if got := p.SceneCount(); got != 2 {
		t.Fatalf("scene count = %d, want 2", got)
	}
	if st := p.Stats(); st.Scenes != 2 {
		t.Fatalf("stats scenes = %d, want 2", st.Scenes)
	}
}

func TestSceneMinLengthMergesShortScene(t *testing.T) {
	p := impl.NewTwoPassPlanner()
	if err := p.SetSceneMinFrames(0); !errors.Is(err, impl.ErrBadConfig) {
		t.Fatalf("zero min scene length = %v, want ErrBadConfig", err)
	}
	if err := p.SetSceneMinFrames(3); err != nil {
		t.Fatalf("set min scene length: %v", err)
	}
	mustScene(t, p, 5)
	mustScene(t, p, 7)
	bounds := p.SceneBoundaries()
	if len(bounds) != 2 || bounds[0] != 0 || bounds[1] != 5 {
		t.Fatalf("scene boundaries = %v, want the 2 frame scene merged into [0 5]", bounds)
	}
	if got := p.SceneCount(); got != 2 {
		t.Fatalf("scene count = %d, want 2", got)
	}
}

func TestPlanSplitsBudgetByComplexity(t *testing.T) {
	p := impl.NewTwoPassPlanner()
	mustScene(t, p, 5)
	for i := int64(0); i < 5; i++ {
		mustAdd(t, p, stats(i, 2.0))
	}
	for i := int64(5); i < 10; i++ {
		mustAdd(t, p, stats(i, 1.0))
	}
	a := mustPlan(t, p, planConfig())
	if len(a.Scenes) != 2 {
		t.Fatalf("allocated scenes = %d, want 2", len(a.Scenes))
	}
	first, second := a.Scenes[0], a.Scenes[1]
	if first.StartFrame != 0 || first.EndFrame != 4 {
		t.Fatalf("first scene = %d..%d, want 0..4", first.StartFrame, first.EndFrame)
	}
	if second.StartFrame != 5 || second.EndFrame != 9 {
		t.Fatalf("second scene = %d..%d, want 5..9", second.StartFrame, second.EndFrame)
	}
	if first.Rate != 2000 || second.Rate != 1000 {
		t.Fatalf("rates = %d/%d, want 2000/1000 for the 2:1 complexity split", first.Rate, second.Rate)
	}
	if first.Clamped || second.Clamped {
		t.Fatalf("unclamped plan marked a scene clamped: %+v", a.Scenes)
	}
}

func TestPlanClampsToMaxRate(t *testing.T) {
	p := impl.NewTwoPassPlanner()
	for i := int64(0); i < 5; i++ {
		mustAdd(t, p, stats(i, 10.0))
	}
	cfg := planConfig()
	cfg.TotalRate = 1000
	cfg.MaxRate = 600
	a := mustPlan(t, p, cfg)
	if len(a.Scenes) != 1 {
		t.Fatalf("allocated scenes = %d, want 1", len(a.Scenes))
	}
	if a.Scenes[0].Rate != 600 {
		t.Fatalf("scene rate = %d, want the max bound 600", a.Scenes[0].Rate)
	}
	if !a.Scenes[0].Clamped {
		t.Fatalf("scene hitting the max bound must be marked clamped")
	}
}

func TestPlanClampsToMinRate(t *testing.T) {
	p := impl.NewTwoPassPlanner()
	for i := int64(0); i < 5; i++ {
		mustAdd(t, p, stats(i, 1.0))
	}
	cfg := planConfig()
	cfg.TotalRate = 1000
	cfg.MinRate = 2000
	cfg.MaxRate = 5000
	a := mustPlan(t, p, cfg)
	if len(a.Scenes) != 1 {
		t.Fatalf("allocated scenes = %d, want 1", len(a.Scenes))
	}
	if a.Scenes[0].Rate != 2000 {
		t.Fatalf("scene rate = %d, want the min bound 2000", a.Scenes[0].Rate)
	}
	if !a.Scenes[0].Clamped {
		t.Fatalf("scene hitting the min bound must be marked clamped")
	}
}

func TestPlanTotalWithinTolerance(t *testing.T) {
	p := impl.NewTwoPassPlanner()
	mustScene(t, p, 5)
	for i := int64(0); i < 5; i++ {
		mustAdd(t, p, stats(i, 3.0))
	}
	for i := int64(5); i < 10; i++ {
		mustAdd(t, p, stats(i, 1.0))
	}
	cfg := impl.PlanConfig{TotalRate: 4000, MinRate: 0, MaxRate: 10000, Tolerance: 50}
	a := mustPlan(t, p, cfg)
	var sum int64
	for _, scene := range a.Scenes {
		sum += scene.Rate
	}
	if sum != 4000 {
		t.Fatalf("allocated total = %d, want 4000 for the 3:1 split", sum)
	}
	if diff := sum - a.Total; diff < -a.Tolerance || diff > a.Tolerance {
		t.Fatalf("allocation diff %d exceeds the tolerance %d", diff, a.Tolerance)
	}
}

func TestPlanRejectsEmptyStats(t *testing.T) {
	p := impl.NewTwoPassPlanner()
	if _, err := p.Plan(planConfig()); !errors.Is(err, impl.ErrNoStats) {
		t.Fatalf("plan without stats = %v, want ErrNoStats", err)
	}
	mustAdd(t, p, stats(0, 1))
	cfg := planConfig()
	cfg.TotalRate = 0
	if _, err := p.Plan(cfg); !errors.Is(err, impl.ErrBadConfig) {
		t.Fatalf("zero total rate = %v, want ErrBadConfig", err)
	}
}

func TestValidateAcceptsPlan(t *testing.T) {
	p := impl.NewTwoPassPlanner()
	mustScene(t, p, 5)
	for i := int64(0); i < 5; i++ {
		mustAdd(t, p, stats(i, 2.0))
	}
	for i := int64(5); i < 10; i++ {
		mustAdd(t, p, stats(i, 2.0))
	}
	a := mustPlan(t, p, planConfig())
	if err := p.Validate(a); err != nil {
		t.Fatalf("a balanced plan must validate, got %v", err)
	}
}

func TestValidateRejectsOverBudget(t *testing.T) {
	p := impl.NewTwoPassPlanner()
	for i := int64(0); i < 4; i++ {
		mustAdd(t, p, stats(i, 1.0))
	}
	a := mustPlan(t, p, planConfig())
	a.Scenes[0].Rate += a.Total + 1
	if err := p.Validate(a); !errors.Is(err, impl.ErrBudgetMismatch) {
		t.Fatalf("over budget plan = %v, want ErrBudgetMismatch", err)
	}
}

func TestSnapshotRestoreKeepsScenesAndStats(t *testing.T) {
	p := impl.NewTwoPassPlanner()
	mustScene(t, p, 5)
	for i := int64(0); i < 5; i++ {
		mustAdd(t, p, stats(i, 2.0))
	}
	for i := int64(5); i < 10; i++ {
		mustAdd(t, p, stats(i, 1.0))
	}
	data, err := p.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewTwoPassPlanner()
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	bounds := restored.SceneBoundaries()
	if len(bounds) != 2 || bounds[1] != 5 {
		t.Fatalf("restored boundaries = %v, want [0 5]", bounds)
	}
	if st := restored.Stats(); st.Frames != 10 || st.Bytes != 1000 {
		t.Fatalf("restored stats = %+v, want 10 frames and 1000 bytes", st)
	}
	a := mustPlan(t, restored, planConfig())
	if len(a.Scenes) != 2 || a.Scenes[0].Rate != 2000 || a.Scenes[1].Rate != 1000 {
		t.Fatalf("restored plan = %+v, want the 2000/1000 split", a.Scenes)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	p := impl.NewTwoPassPlanner()
	mustScene(t, p, 5)
	for i := int64(0); i < 10; i++ {
		mustAdd(t, p, stats(i, 1.0))
	}
	if err := p.Restore([]byte("garbage")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot = %v, want ErrBadSnapshot", err)
	}
	if got := p.SceneCount(); got != 2 {
		t.Fatalf("failed restore changed the scene count to %d, want 2", got)
	}
	if st := p.Stats(); st.Frames != 10 {
		t.Fatalf("failed restore changed the frame count to %d, want 10", st.Frames)
	}
	if _, err := p.Plan(planConfig()); err != nil {
		t.Fatalf("live planner must keep working after a rejected restore, got %v", err)
	}
}

func TestConcurrentPlanAndBoundaries(t *testing.T) {
	p := impl.NewTwoPassPlanner()
	mustScene(t, p, 5)
	for i := int64(0); i < 5; i++ {
		mustAdd(t, p, stats(i, 2.0))
	}
	for i := int64(5); i < 10; i++ {
		mustAdd(t, p, stats(i, 1.0))
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				a, err := p.Plan(planConfig())
				if err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
					return
				}
				if len(a.Scenes) != 2 || a.Scenes[0].Rate != 2000 || a.Scenes[1].Rate != 1000 {
					mu.Lock()
					if firstErr == nil {
						firstErr = errors.New("plan snapshot became inconsistent")
					}
					mu.Unlock()
					return
				}
				if bounds := p.SceneBoundaries(); len(bounds) != 2 {
					mu.Lock()
					if firstErr == nil {
						firstErr = errors.New("scene boundary snapshot became inconsistent")
					}
					mu.Unlock()
					return
				}
				if st := p.Stats(); st.Frames != 10 {
					mu.Lock()
					if firstErr == nil {
						firstErr = errors.New("stats snapshot became inconsistent")
					}
					mu.Unlock()
					return
				}
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		t.Fatalf("concurrent planning failed: %v", firstErr)
	}
}
