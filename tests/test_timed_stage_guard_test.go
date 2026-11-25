package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch49-go-game-a-06"
)

func mustStart(t *testing.T, g *impl.TimedStageGuard, quest, player string, at int64) impl.Run {
	t.Helper()
	run, err := g.Start(quest, player, at)
	if err != nil {
		t.Fatalf("Start(%q, %q, %d) return error: %v", quest, player, at, err)
	}
	return run
}

func TestNewGuardAppliesDefaults(t *testing.T) {
	g := impl.NewTimedStageGuard(impl.Options{})
	if g == nil {
		t.Fatal("NewTimedStageGuard returned nil")
	}
	if got := g.StageLimitMs(); got != 30000 {
		t.Fatalf("default stage limit = %d, want 30000", got)
	}
	if got := g.GraceMs(); got != 5000 {
		t.Fatalf("default grace = %d, want 5000", got)
	}
}

func TestStartBuildsIdentityAndDeadline(t *testing.T) {
	g := impl.NewTimedStageGuard(impl.Options{})
	run := mustStart(t, g, "quest-7", "alice", 1000)
	if run.ID != "quest-7#alice#1" {
		t.Fatalf("run id = %q, want quest-7#alice#1", run.ID)
	}
	if run.Stage != 0 {
		t.Fatalf("initial stage = %d, want 0", run.Stage)
	}
	if run.StartedAt != 1000 {
		t.Fatalf("started at = %d, want 1000", run.StartedAt)
	}
	if run.Deadline != 31000 {
		t.Fatalf("deadline = %d, want 31000", run.Deadline)
	}
	if run.GraceUsed {
		t.Fatal("fresh run must not report grace used")
	}
}

func TestStartGeneratesSequentialRunIDs(t *testing.T) {
	g := impl.NewTimedStageGuard(impl.Options{})
	first := mustStart(t, g, "quest-7", "alice", 0)
	second := mustStart(t, g, "quest-7", "bob", 0)
	if first.ID != "quest-7#alice#1" {
		t.Fatalf("first run id = %q, want quest-7#alice#1", first.ID)
	}
	if second.ID != "quest-7#bob#2" {
		t.Fatalf("second run id = %q, want quest-7#bob#2", second.ID)
	}
}

func TestEnterStageResetsStageClock(t *testing.T) {
	g := impl.NewTimedStageGuard(impl.Options{})
	run := mustStart(t, g, "quest-7", "alice", 1000)
	if err := g.EnterStage(run.ID, 1, 4000); err != nil {
		t.Fatalf("EnterStage(1, 4000) return error: %v", err)
	}
	deadline, err := g.Deadline(run.ID)
	if err != nil {
		t.Fatalf("Deadline return error: %v", err)
	}
	if deadline != 34000 {
		t.Fatalf("stage 1 deadline = %d, want 34000", deadline)
	}
	stage, err := g.StageIndex(run.ID)
	if err != nil {
		t.Fatalf("StageIndex return error: %v", err)
	}
	if stage != 1 {
		t.Fatalf("stage index = %d, want 1", stage)
	}
	spent, err := g.TotalSpent(run.ID)
	if err != nil {
		t.Fatalf("TotalSpent return error: %v", err)
	}
	if spent != 3000 {
		t.Fatalf("total spent = %d, want 3000", spent)
	}
}

func TestEnterStageRejectsSkippedStage(t *testing.T) {
	g := impl.NewTimedStageGuard(impl.Options{})
	run := mustStart(t, g, "quest-7", "alice", 0)
	if err := g.EnterStage(run.ID, 2, 1000); !errors.Is(err, impl.ErrStageOrder) {
		t.Fatalf("skipping to stage 2 error = %v, want ErrStageOrder", err)
	}
	if err := g.EnterStage(run.ID, 0, 2000); !errors.Is(err, impl.ErrStageOrder) {
		t.Fatalf("rewinding to stage 0 error = %v, want ErrStageOrder", err)
	}
	stage, err := g.StageIndex(run.ID)
	if err != nil {
		t.Fatalf("StageIndex return error: %v", err)
	}
	if stage != 0 {
		t.Fatalf("stage after rejected transitions = %d, want 0", stage)
	}
}

func TestTickReportsRemainingTime(t *testing.T) {
	g := impl.NewTimedStageGuard(impl.Options{})
	run := mustStart(t, g, "quest-7", "alice", 0)
	state, err := g.Tick(run.ID, 10000)
	if err != nil {
		t.Fatalf("Tick return error: %v", err)
	}
	if state.Stage != 0 {
		t.Fatalf("state stage = %d, want 0", state.Stage)
	}
	if state.RemainingMs != 20000 {
		t.Fatalf("remaining = %d, want 20000", state.RemainingMs)
	}
	if state.InGrace || state.Expired || state.RolledBack || state.Failed {
		t.Fatalf("unexpected flags before deadline: %+v", state)
	}
}

func TestTickAtDeadlineStillOnTime(t *testing.T) {
	g := impl.NewTimedStageGuard(impl.Options{})
	run := mustStart(t, g, "quest-7", "alice", 0)
	state, err := g.Tick(run.ID, 30000)
	if err != nil {
		t.Fatalf("Tick return error: %v", err)
	}
	if state.RemainingMs != 0 {
		t.Fatalf("remaining at deadline = %d, want 0", state.RemainingMs)
	}
	if state.InGrace || state.Expired {
		t.Fatalf("deadline itself must not start grace: %+v", state)
	}
}

func TestTickEntersGraceWindow(t *testing.T) {
	g := impl.NewTimedStageGuard(impl.Options{})
	run := mustStart(t, g, "quest-7", "alice", 0)
	state, err := g.Tick(run.ID, 34000)
	if err != nil {
		t.Fatalf("Tick return error: %v", err)
	}
	if !state.InGrace {
		t.Fatalf("expected grace window, got %+v", state)
	}
	if state.Expired || state.Failed || state.RolledBack {
		t.Fatalf("grace tick must not expire: %+v", state)
	}
	if state.RemainingMs != 1000 {
		t.Fatalf("grace remaining = %d, want 1000", state.RemainingMs)
	}
}

func TestTickExpiredRollsBackToCheckpoint(t *testing.T) {
	g := impl.NewTimedStageGuard(impl.Options{Checkpoints: map[int]bool{0: true}})
	run := mustStart(t, g, "quest-7", "alice", 0)
	if err := g.EnterStage(run.ID, 1, 1000); err != nil {
		t.Fatalf("EnterStage(1) return error: %v", err)
	}
	if err := g.EnterStage(run.ID, 2, 2000); err != nil {
		t.Fatalf("EnterStage(2) return error: %v", err)
	}
	state, err := g.Tick(run.ID, 40000)
	if err != nil {
		t.Fatalf("Tick return error: %v", err)
	}
	if !state.RolledBack {
		t.Fatalf("expected rollback, got %+v", state)
	}
	if state.Failed {
		t.Fatalf("rollback must not fail the chain: %+v", state)
	}
	if state.Stage != 0 {
		t.Fatalf("stage after rollback = %d, want 0", state.Stage)
	}
	if state.RemainingMs != 30000 {
		t.Fatalf("remaining after rollback = %d, want 30000", state.RemainingMs)
	}
}

func TestTickExpiredWithoutCheckpointFails(t *testing.T) {
	g := impl.NewTimedStageGuard(impl.Options{})
	run := mustStart(t, g, "quest-7", "alice", 0)
	state, err := g.Tick(run.ID, 40000)
	if err != nil {
		t.Fatalf("Tick return error: %v", err)
	}
	if !state.Expired || !state.Failed {
		t.Fatalf("expected expired failure, got %+v", state)
	}
	if state.RolledBack {
		t.Fatalf("no checkpoint means no rollback, got %+v", state)
	}
	if state.FailedStage != 0 {
		t.Fatalf("failed stage = %d, want 0", state.FailedStage)
	}
}

func TestExtendPushesDeadline(t *testing.T) {
	g := impl.NewTimedStageGuard(impl.Options{})
	run := mustStart(t, g, "quest-7", "alice", 0)
	deadline, err := g.Extend(run.ID, 10, 1000)
	if err != nil {
		t.Fatalf("Extend return error: %v", err)
	}
	if deadline != 40000 {
		t.Fatalf("extended deadline = %d, want 40000", deadline)
	}
	stored, err := g.Deadline(run.ID)
	if err != nil {
		t.Fatalf("Deadline return error: %v", err)
	}
	if stored != 40000 {
		t.Fatalf("stored deadline = %d, want 40000", stored)
	}
}

func TestRetryResetsStageDeadline(t *testing.T) {
	g := impl.NewTimedStageGuard(impl.Options{})
	run := mustStart(t, g, "quest-7", "alice", 0)
	retried, err := g.Retry(run.ID, 5000)
	if err != nil {
		t.Fatalf("Retry return error: %v", err)
	}
	if retried.Retries != 1 {
		t.Fatalf("retry count = %d, want 1", retried.Retries)
	}
	if retried.Stage != 0 {
		t.Fatalf("retry stage = %d, want 0", retried.Stage)
	}
	if retried.Deadline != 35000 {
		t.Fatalf("retry deadline = %d, want 35000", retried.Deadline)
	}
}

func TestTickAfterFailureStaysFailed(t *testing.T) {
	g := impl.NewTimedStageGuard(impl.Options{})
	run := mustStart(t, g, "quest-7", "alice", 0)
	if _, err := g.Tick(run.ID, 40000); err != nil {
		t.Fatalf("first Tick return error: %v", err)
	}
	state, err := g.Tick(run.ID, 41000)
	if err != nil {
		t.Fatalf("second Tick return error: %v", err)
	}
	if !state.Failed {
		t.Fatalf("failed chain must stay failed, got %+v", state)
	}
}

func TestSnapshotRestoreKeepsStageClock(t *testing.T) {
	g := impl.NewTimedStageGuard(impl.Options{})
	run := mustStart(t, g, "quest-7", "alice", 1000)
	if err := g.EnterStage(run.ID, 1, 4000); err != nil {
		t.Fatalf("EnterStage return error: %v", err)
	}
	if _, err := g.Tick(run.ID, 5000); err != nil {
		t.Fatalf("Tick return error: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot return error: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("Snapshot returned empty payload")
	}
	restored := impl.NewTimedStageGuard(impl.Options{})
	if err := restored.Restore(data); err != nil {
		t.Fatalf("Restore return error: %v", err)
	}
	stage, err := restored.StageIndex(run.ID)
	if err != nil {
		t.Fatalf("StageIndex after restore return error: %v", err)
	}
	if stage != 1 {
		t.Fatalf("restored stage = %d, want 1", stage)
	}
	deadline, err := restored.Deadline(run.ID)
	if err != nil {
		t.Fatalf("Deadline after restore return error: %v", err)
	}
	if deadline != 34000 {
		t.Fatalf("restored deadline = %d, want 34000", deadline)
	}
}

func TestConcurrentTicksAgreeOnRemaining(t *testing.T) {
	g := impl.NewTimedStageGuard(impl.Options{})
	run := mustStart(t, g, "quest-7", "alice", 0)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			state, err := g.Tick(run.ID, 10000)
			if err != nil {
				t.Errorf("concurrent Tick return error: %v", err)
				return
			}
			if state.RemainingMs != 20000 {
				t.Errorf("concurrent remaining = %d, want 20000", state.RemainingMs)
			}
			if state.Stage != 0 {
				t.Errorf("concurrent stage = %d, want 0", state.Stage)
			}
		}()
	}
	wg.Wait()
}
