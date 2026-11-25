package task

import (
	"errors"
	"testing"

	impl "gsb/batch49-go-game-a-04"
)

func testChain(id string) impl.Chain {
	return impl.Chain{ID: id, Stages: []impl.StageSpec{
		{Index: 0, Threshold: 100, Duration: 500, Reward: "reward-0"},
		{Index: 1, Threshold: 50, Duration: 300, Reward: "reward-1"},
	}}
}

func newCoordinator() *impl.WorldChainCoordinator {
	return impl.NewWorldChainCoordinator(impl.Options{MaxChains: 4, MaxParticipants: 16})
}

func mustDefine(t *testing.T, coord *impl.WorldChainCoordinator, chain impl.Chain) {
	t.Helper()
	if err := coord.DefineChain(chain); err != nil {
		t.Fatalf("define chain %s: %v", chain.ID, err)
	}
}

func mustAdvance(t *testing.T, coord *impl.WorldChainCoordinator, chainID string, at int64) impl.StageState {
	t.Helper()
	state, err := coord.Advance(chainID, at)
	if err != nil {
		t.Fatalf("advance %s at %d: %v", chainID, at, err)
	}
	return state
}

func TestDefineChainRejectsBadSpec(t *testing.T) {
	coord := newCoordinator()
	if err := coord.DefineChain(impl.Chain{ID: " ", Stages: []impl.StageSpec{{Index: 0, Threshold: 10, Duration: 10}}}); !errors.Is(err, impl.ErrBadChain) {
		t.Fatalf("blank chain id must return ErrBadChain, got %v", err)
	}
	if err := coord.DefineChain(impl.Chain{ID: "empty"}); !errors.Is(err, impl.ErrBadChain) {
		t.Fatalf("chain without stages must return ErrBadChain, got %v", err)
	}
	if err := coord.DefineChain(impl.Chain{ID: "bad-index", Stages: []impl.StageSpec{{Index: 1, Threshold: 10, Duration: 10}}}); !errors.Is(err, impl.ErrBadChain) {
		t.Fatalf("stage index mismatch must return ErrBadChain, got %v", err)
	}
	if err := coord.DefineChain(impl.Chain{ID: "zero-threshold", Stages: []impl.StageSpec{{Index: 0, Threshold: 0, Duration: 10}}}); !errors.Is(err, impl.ErrBadChain) {
		t.Fatalf("zero threshold must return ErrBadChain, got %v", err)
	}
	if err := coord.DefineChain(impl.Chain{ID: "zero-duration", Stages: []impl.StageSpec{{Index: 0, Threshold: 10, Duration: 0}}}); !errors.Is(err, impl.ErrBadChain) {
		t.Fatalf("zero duration must return ErrBadChain, got %v", err)
	}
}

func TestDefineChainRejectsDuplicate(t *testing.T) {
	coord := newCoordinator()
	mustDefine(t, coord, testChain("world-1"))
	if err := coord.DefineChain(testChain("world-1")); !errors.Is(err, impl.ErrDuplicateChain) {
		t.Fatalf("duplicate chain must return ErrDuplicateChain, got %v", err)
	}
	if _, ok := coord.State("world-1"); !ok {
		t.Fatalf("original chain must survive a duplicate define")
	}
}

func TestDefineChainStartsLocked(t *testing.T) {
	coord := newCoordinator()
	mustDefine(t, coord, testChain("world-1"))
	state, ok := coord.State("world-1")
	if !ok {
		t.Fatalf("defined chain must be readable")
	}
	if state.ChainID != "world-1" || state.Current != 0 {
		t.Fatalf("chain state = %+v, want current stage 0", state)
	}
	if state.Status != impl.ChainRunning {
		t.Fatalf("chain status = %q, want running", state.Status)
	}
	if len(state.Stages) != 2 {
		t.Fatalf("stage count = %d, want 2", len(state.Stages))
	}
	if state.Stages[0].Status != impl.StageLocked || state.Stages[1].Status != impl.StageLocked {
		t.Fatalf("fresh stages must be locked, got %+v", state.Stages)
	}
}

func TestAdvanceOpensFirstStage(t *testing.T) {
	coord := newCoordinator()
	mustDefine(t, coord, testChain("world-1"))
	if _, err := coord.Advance("missing", 1000); !errors.Is(err, impl.ErrUnknownChain) {
		t.Fatalf("unknown chain must return ErrUnknownChain, got %v", err)
	}
	state := mustAdvance(t, coord, "world-1", 1000)
	if state.Status != impl.StageOpen {
		t.Fatalf("stage status = %q, want open", state.Status)
	}
	if state.Stage != 0 || state.StartedAt != 1000 || state.EndsAt != 1500 {
		t.Fatalf("opened stage = %+v, want window 1000..1500", state)
	}
	if state.Contributed != 0 || state.Threshold != 100 {
		t.Fatalf("opened stage progress = %+v", state)
	}
}

func TestContributeBeforeOpenRejected(t *testing.T) {
	coord := newCoordinator()
	mustDefine(t, coord, testChain("world-1"))
	if _, err := coord.Contribute("world-1", "evt-1", "alice", 10, 100); !errors.Is(err, impl.ErrWindowClosed) {
		t.Fatalf("contribution on a locked stage must return ErrWindowClosed, got %v", err)
	}
	progress, err := coord.Progress("world-1")
	if err != nil {
		t.Fatalf("progress: %v", err)
	}
	if progress.Contributed != 0 {
		t.Fatalf("rejected contribution must not count, got %d", progress.Contributed)
	}
}

func TestContributeAccumulatesProgress(t *testing.T) {
	coord := newCoordinator()
	mustDefine(t, coord, testChain("world-1"))
	mustAdvance(t, coord, "world-1", 1000)
	first, err := coord.Contribute("world-1", "evt-1", "alice", 30, 1010)
	if err != nil {
		t.Fatalf("contribute: %v", err)
	}
	if first.Contributed != 30 || first.Threshold != 100 || first.Stage != 0 {
		t.Fatalf("first progress = %+v, want 30 of 100 on stage 0", first)
	}
	if first.Players != 1 {
		t.Fatalf("players after one contributor = %d, want 1", first.Players)
	}
	second, err := coord.Contribute("world-1", "evt-2", "bob", 25, 1020)
	if err != nil {
		t.Fatalf("second contribute: %v", err)
	}
	if second.Contributed != 55 {
		t.Fatalf("contributed = %d, want 55", second.Contributed)
	}
	if second.Players != 2 {
		t.Fatalf("players after two contributors = %d, want 2", second.Players)
	}
	progress, err := coord.Progress("world-1")
	if err != nil {
		t.Fatalf("progress: %v", err)
	}
	if progress.Contributed != 55 || progress.Players != 2 {
		t.Fatalf("progress = %+v, want 55 from 2 players", progress)
	}
}

func TestContributeRejectsBadAmount(t *testing.T) {
	coord := newCoordinator()
	mustDefine(t, coord, testChain("world-1"))
	mustAdvance(t, coord, "world-1", 1000)
	if _, err := coord.Contribute("world-1", "evt-1", "alice", 0, 1010); !errors.Is(err, impl.ErrBadAmount) {
		t.Fatalf("zero amount must return ErrBadAmount, got %v", err)
	}
	if _, err := coord.Contribute("world-1", "evt-2", "alice", -7, 1010); !errors.Is(err, impl.ErrBadAmount) {
		t.Fatalf("negative amount must return ErrBadAmount, got %v", err)
	}
	if _, err := coord.Contribute("world-1", "", "alice", 5, 1010); !errors.Is(err, impl.ErrBlankArg) {
		t.Fatalf("blank event id must return ErrBlankArg, got %v", err)
	}
	if _, err := coord.Contribute("world-1", "evt-3", " ", 5, 1010); !errors.Is(err, impl.ErrBlankArg) {
		t.Fatalf("blank player must return ErrBlankArg, got %v", err)
	}
}

func TestContributeAfterWindowRejected(t *testing.T) {
	coord := newCoordinator()
	mustDefine(t, coord, testChain("world-1"))
	mustAdvance(t, coord, "world-1", 0)
	if _, err := coord.Contribute("world-1", "evt-late", "alice", 10, 500); !errors.Is(err, impl.ErrWindowClosed) {
		t.Fatalf("contribution at the deadline must return ErrWindowClosed, got %v", err)
	}
	progress, err := coord.Progress("world-1")
	if err != nil {
		t.Fatalf("progress: %v", err)
	}
	if progress.Contributed != 0 {
		t.Fatalf("late contribution must not count, got %d", progress.Contributed)
	}
	if _, err := coord.Contribute("world-1", "evt-ok", "alice", 10, 499); err != nil {
		t.Fatalf("contribution inside the window must pass, got %v", err)
	}
}

func TestJoinRegistersParticipant(t *testing.T) {
	coord := newCoordinator()
	mustDefine(t, coord, testChain("world-1"))
	mustAdvance(t, coord, "world-1", 1000)
	if err := coord.Join("world-1", 0, "alice", 1100); err != nil {
		t.Fatalf("join: %v", err)
	}
	if !coord.Eligible("world-1", 0, "alice") {
		t.Fatalf("player joined inside the window must be eligible")
	}
	if coord.Eligible("world-1", 0, "bob") {
		t.Fatalf("player outside the stage must not be eligible")
	}
	participants := coord.Participants("world-1", 0)
	if len(participants) != 1 || participants[0] != "alice" {
		t.Fatalf("participants = %v, want [alice]", participants)
	}
}

func TestJoinRejectsUnknownChainAndStage(t *testing.T) {
	coord := newCoordinator()
	if err := coord.Join("missing", 0, "alice", 10); !errors.Is(err, impl.ErrUnknownChain) {
		t.Fatalf("unknown chain must return ErrUnknownChain, got %v", err)
	}
	mustDefine(t, coord, testChain("world-1"))
	mustAdvance(t, coord, "world-1", 0)
	if err := coord.Join("world-1", 5, "alice", 10); !errors.Is(err, impl.ErrStageOutOfRange) {
		t.Fatalf("stage 5 must return ErrStageOutOfRange, got %v", err)
	}
	if err := coord.Join("world-1", 1, "alice", 10); !errors.Is(err, impl.ErrStageOutOfRange) {
		t.Fatalf("future stage must return ErrStageOutOfRange, got %v", err)
	}
	if err := coord.Join("world-1", 0, " ", 10); !errors.Is(err, impl.ErrBlankArg) {
		t.Fatalf("blank player must return ErrBlankArg, got %v", err)
	}
}

func TestJoinBeforeOpenRejected(t *testing.T) {
	coord := newCoordinator()
	mustDefine(t, coord, testChain("world-1"))
	if err := coord.Join("world-1", 0, "alice", 100); !errors.Is(err, impl.ErrWindowClosed) {
		t.Fatalf("join on a locked stage must return ErrWindowClosed, got %v", err)
	}
	if got := len(coord.Participants("world-1", 0)); got != 0 {
		t.Fatalf("rejected join must not register anyone, got %d", got)
	}
}

func TestJoinTwiceKeepsOneEntry(t *testing.T) {
	coord := newCoordinator()
	mustDefine(t, coord, testChain("world-1"))
	mustAdvance(t, coord, "world-1", 1000)
	if err := coord.Join("world-1", 0, "alice", 1010); err != nil {
		t.Fatalf("first join: %v", err)
	}
	if err := coord.Join("world-1", 0, "alice", 1020); err != nil {
		t.Fatalf("second join: %v", err)
	}
	if got := len(coord.Participants("world-1", 0)); got != 1 {
		t.Fatalf("repeated join must not duplicate the player, got %d", got)
	}
	if err := coord.Join("world-1", 0, "bob", 1030); err != nil {
		t.Fatalf("join bob: %v", err)
	}
	participants := coord.Participants("world-1", 0)
	if len(participants) != 2 || participants[0] != "alice" || participants[1] != "bob" {
		t.Fatalf("participants = %v, want [alice bob]", participants)
	}
}

func TestAdvanceCompletesStageAtThreshold(t *testing.T) {
	coord := newCoordinator()
	mustDefine(t, coord, testChain("world-1"))
	mustAdvance(t, coord, "world-1", 0)
	if _, err := coord.Contribute("world-1", "evt-1", "alice", 60, 10); err != nil {
		t.Fatalf("contribute: %v", err)
	}
	if _, err := coord.Contribute("world-1", "evt-2", "bob", 40, 20); err != nil {
		t.Fatalf("contribute: %v", err)
	}
	settled := mustAdvance(t, coord, "world-1", 30)
	if settled.Status != impl.StageCompleted {
		t.Fatalf("stage status = %q, want completed", settled.Status)
	}
	if settled.Contributed != 100 {
		t.Fatalf("completed stage contributed = %d, want 100", settled.Contributed)
	}
	state, ok := coord.State("world-1")
	if !ok {
		t.Fatalf("state must be readable")
	}
	if state.Current != 1 {
		t.Fatalf("chain must advance to stage 1, got %d", state.Current)
	}
	if state.Stages[0].Status != impl.StageCompleted || state.Stages[1].Status != impl.StageLocked {
		t.Fatalf("stage states = %+v, want completed then locked", state.Stages)
	}
}

func TestAdvanceFailsStageAtDeadline(t *testing.T) {
	coord := newCoordinator()
	mustDefine(t, coord, testChain("world-1"))
	mustAdvance(t, coord, "world-1", 0)
	if _, err := coord.Contribute("world-1", "evt-1", "alice", 10, 10); err != nil {
		t.Fatalf("contribute: %v", err)
	}
	failed := mustAdvance(t, coord, "world-1", 500)
	if failed.Status != impl.StageFailed {
		t.Fatalf("stage status = %q, want failed", failed.Status)
	}
	if failed.Stage != 0 {
		t.Fatalf("failed stage index = %d, want 0", failed.Stage)
	}
	state, ok := coord.State("world-1")
	if !ok || state.Current != 0 {
		t.Fatalf("failed stage must stay current, state = %+v ok=%t", state, ok)
	}
	if state.Stages[0].Contributed != 0 {
		t.Fatalf("retry must start from zero, contributed = %d", state.Stages[0].Contributed)
	}
	retried := mustAdvance(t, coord, "world-1", 600)
	if retried.Status != impl.StageOpen || retried.StartedAt != 600 || retried.EndsAt != 1100 {
		t.Fatalf("retried stage = %+v, want a new window at 600", retried)
	}
}

func TestSnapshotRestoreKeepsParticipation(t *testing.T) {
	coord := newCoordinator()
	mustDefine(t, coord, testChain("world-1"))
	mustAdvance(t, coord, "world-1", 0)
	if err := coord.Join("world-1", 0, "alice", 10); err != nil {
		t.Fatalf("join: %v", err)
	}
	if _, err := coord.Contribute("world-1", "evt-1", "alice", 40, 20); err != nil {
		t.Fatalf("contribute: %v", err)
	}
	data, err := coord.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := newCoordinator()
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	progress, err := restored.Progress("world-1")
	if err != nil {
		t.Fatalf("restored progress: %v", err)
	}
	if progress.Contributed != 40 || progress.Stage != 0 {
		t.Fatalf("restored progress = %+v, want 40 on stage 0", progress)
	}
	if !restored.Eligible("world-1", 0, "alice") {
		t.Fatalf("restored participation must keep eligibility")
	}
	state, ok := restored.State("world-1")
	if !ok || state.Stages[0].Status != impl.StageOpen || state.Stages[0].EndsAt != 500 {
		t.Fatalf("restored stage state = %+v ok=%t", state, ok)
	}
	if err := restored.Restore([]byte("garbage")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("garbage restore must return ErrBadSnapshot, got %v", err)
	}
	if _, ok := restored.State("world-1"); !ok {
		t.Fatalf("failed restore must not drop the chain")
	}
}
