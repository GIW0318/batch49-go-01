package task

import (
	"errors"
	"testing"

	impl "gsb/batch49-go-game-a-05"
)

func newRuntime() *impl.ForkJoinChain {
	return impl.NewForkJoinChain(impl.Options{MaxChains: 4, MaxNodes: 32})
}

func forkedChain() impl.Chain {
	return impl.Chain{ID: "fork-1", Nodes: []impl.NodeSpec{
		{ID: "root", Reward: "gold"},
		{ID: "left", DependsOn: []string{"root"}, Reward: "silver"},
		{ID: "right", DependsOn: []string{"root"}},
	}}
}

func quorumChain() impl.Chain {
	return impl.Chain{ID: "join-1", Nodes: []impl.NodeSpec{
		{ID: "a"},
		{ID: "b"},
		{ID: "c"},
		{ID: "join", DependsOn: []string{"a", "b", "c"}, Quorum: 2, Reward: "gold"},
	}}
}

func allOrNothingChain() impl.Chain {
	return impl.Chain{ID: "all-1", Nodes: []impl.NodeSpec{
		{ID: "a"},
		{ID: "b"},
		{ID: "join", DependsOn: []string{"a", "b"}},
	}}
}

func mustDefine(t *testing.T, runtime *impl.ForkJoinChain, chain impl.Chain) {
	t.Helper()
	if err := runtime.DefineChain(chain); err != nil {
		t.Fatalf("define chain %s: %v", chain.ID, err)
	}
}

func mustComplete(t *testing.T, runtime *impl.ForkJoinChain, player, chainID, nodeID string) impl.NodeState {
	t.Helper()
	state, err := runtime.Complete(player, chainID, nodeID, 100)
	if err != nil {
		t.Fatalf("complete %s for %s: %v", nodeID, player, err)
	}
	return state
}

func statusOf(state impl.ChainState, nodeID string) string {
	for _, node := range state.Nodes {
		if node.NodeID == nodeID {
			return node.Status
		}
	}
	return ""
}

func TestDefineChainRejectsBadSpec(t *testing.T) {
	runtime := newRuntime()
	if err := runtime.DefineChain(impl.Chain{ID: " ", Nodes: []impl.NodeSpec{{ID: "a"}}}); !errors.Is(err, impl.ErrBadChain) {
		t.Fatalf("blank chain id must return ErrBadChain, got %v", err)
	}
	if err := runtime.DefineChain(impl.Chain{ID: "empty"}); !errors.Is(err, impl.ErrBadChain) {
		t.Fatalf("chain without nodes must return ErrBadChain, got %v", err)
	}
	if err := runtime.DefineChain(impl.Chain{ID: "dup-node", Nodes: []impl.NodeSpec{{ID: "a"}, {ID: "a"}}}); !errors.Is(err, impl.ErrBadChain) {
		t.Fatalf("duplicate node id must return ErrBadChain, got %v", err)
	}
	if err := runtime.DefineChain(impl.Chain{ID: "blank-node", Nodes: []impl.NodeSpec{{ID: "a"}, {ID: " "}}}); !errors.Is(err, impl.ErrBadChain) {
		t.Fatalf("blank node id must return ErrBadChain, got %v", err)
	}
	if err := runtime.DefineChain(impl.Chain{ID: "quorum-high", Nodes: []impl.NodeSpec{{ID: "a"}, {ID: "j", DependsOn: []string{"a"}, Quorum: 2}}}); !errors.Is(err, impl.ErrBadChain) {
		t.Fatalf("quorum above the dependency count must return ErrBadChain, got %v", err)
	}
	if err := runtime.DefineChain(impl.Chain{ID: "quorum-alone", Nodes: []impl.NodeSpec{{ID: "j", Quorum: 1}}}); !errors.Is(err, impl.ErrBadChain) {
		t.Fatalf("quorum without dependencies must return ErrBadChain, got %v", err)
	}
}

func TestDefineChainRejectsUnknownDependency(t *testing.T) {
	runtime := newRuntime()
	if err := runtime.DefineChain(impl.Chain{ID: "dangling", Nodes: []impl.NodeSpec{{ID: "a", DependsOn: []string{"ghost"}}}}); !errors.Is(err, impl.ErrUnknownNode) {
		t.Fatalf("unknown dependency must return ErrUnknownNode, got %v", err)
	}
	mustDefine(t, runtime, forkedChain())
	if err := runtime.DefineChain(forkedChain()); !errors.Is(err, impl.ErrDuplicateChain) {
		t.Fatalf("duplicate chain must return ErrDuplicateChain, got %v", err)
	}
}

func TestStateInitializesRootsActive(t *testing.T) {
	runtime := newRuntime()
	mustDefine(t, runtime, forkedChain())
	state := runtime.State("alice", "fork-1")
	if state.ChainID != "fork-1" {
		t.Fatalf("state chain id = %q, want fork-1", state.ChainID)
	}
	if len(state.Nodes) != 3 {
		t.Fatalf("node count = %d, want 3", len(state.Nodes))
	}
	if got := statusOf(state, "root"); got != impl.NodeActive {
		t.Fatalf("root status = %q, want active", got)
	}
	if got := statusOf(state, "left"); got != impl.NodePending {
		t.Fatalf("left status = %q, want pending", got)
	}
	if got := statusOf(state, "right"); got != impl.NodePending {
		t.Fatalf("right status = %q, want pending", got)
	}
}

func TestCompleteRejectsUnknownNode(t *testing.T) {
	runtime := newRuntime()
	if _, err := runtime.Complete("alice", "missing", "root", 10); !errors.Is(err, impl.ErrUnknownChain) {
		t.Fatalf("unknown chain must return ErrUnknownChain, got %v", err)
	}
	mustDefine(t, runtime, forkedChain())
	if _, err := runtime.Complete("alice", "fork-1", "ghost", 10); !errors.Is(err, impl.ErrUnknownNode) {
		t.Fatalf("unknown node must return ErrUnknownNode, got %v", err)
	}
	if _, err := runtime.Complete(" ", "fork-1", "root", 10); !errors.Is(err, impl.ErrBlankArg) {
		t.Fatalf("blank player must return ErrBlankArg, got %v", err)
	}
}

func TestCompleteRequiresDependencies(t *testing.T) {
	runtime := newRuntime()
	mustDefine(t, runtime, forkedChain())
	if _, err := runtime.Complete("alice", "fork-1", "left", 10); !errors.Is(err, impl.ErrNodeNotReady) {
		t.Fatalf("child before its parent must return ErrNodeNotReady, got %v", err)
	}
	root := mustComplete(t, runtime, "alice", "fork-1", "root")
	if root.Status != impl.NodeDone || root.Seq != 1 {
		t.Fatalf("root completion = %+v, want done with seq 1", root)
	}
	available := runtime.Available("alice", "fork-1")
	if len(available) != 2 || available[0] != "left" || available[1] != "right" {
		t.Fatalf("available = %v, want [left right]", available)
	}
	left := mustComplete(t, runtime, "alice", "fork-1", "left")
	if left.Status != impl.NodeDone || left.Seq != 2 {
		t.Fatalf("left completion = %+v, want done with seq 2", left)
	}
}

func TestCompleteTwiceRejected(t *testing.T) {
	runtime := newRuntime()
	mustDefine(t, runtime, forkedChain())
	mustComplete(t, runtime, "alice", "fork-1", "root")
	if _, err := runtime.Complete("alice", "fork-1", "root", 200); !errors.Is(err, impl.ErrAlreadyDone) {
		t.Fatalf("second completion must return ErrAlreadyDone, got %v", err)
	}
}

func TestCompleteRecordsReward(t *testing.T) {
	runtime := newRuntime()
	mustDefine(t, runtime, forkedChain())
	mustComplete(t, runtime, "alice", "fork-1", "root")
	rewards := runtime.Rewards("alice", "fork-1")
	if len(rewards) != 1 || rewards[0] != "gold" {
		t.Fatalf("rewards = %v, want [gold]", rewards)
	}
	if got := runtime.Rewards("bob", "fork-1"); len(got) != 0 {
		t.Fatalf("another player must start without rewards, got %v", got)
	}
}

func TestRewardsKeepCompletionOrder(t *testing.T) {
	runtime := newRuntime()
	mustDefine(t, runtime, impl.Chain{ID: "two-roots", Nodes: []impl.NodeSpec{
		{ID: "gold-root", Reward: "gold"},
		{ID: "gem-root", Reward: "gem"},
	}})
	mustComplete(t, runtime, "alice", "two-roots", "gem-root")
	mustComplete(t, runtime, "alice", "two-roots", "gold-root")
	rewards := runtime.Rewards("alice", "two-roots")
	if len(rewards) != 2 || rewards[0] != "gem" || rewards[1] != "gold" {
		t.Fatalf("rewards = %v, want [gem gold] in completion order", rewards)
	}
}

func TestCancelLeafNode(t *testing.T) {
	runtime := newRuntime()
	mustDefine(t, runtime, forkedChain())
	if err := runtime.Cancel("alice", "fork-1", "right", "abandoned"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	state := runtime.State("alice", "fork-1")
	if got := statusOf(state, "right"); got != impl.NodeCanceled {
		t.Fatalf("right status = %q, want canceled", got)
	}
	if got := statusOf(state, "root"); got != impl.NodeActive {
		t.Fatalf("root must stay active, got %q", got)
	}
	if _, err := runtime.Complete("alice", "fork-1", "right", 100); !errors.Is(err, impl.ErrAlreadyCanceled) {
		t.Fatalf("completing a canceled node must return ErrAlreadyCanceled, got %v", err)
	}
	if err := runtime.Cancel("alice", "fork-1", "right", "abandoned"); !errors.Is(err, impl.ErrAlreadyCanceled) {
		t.Fatalf("second cancel must return ErrAlreadyCanceled, got %v", err)
	}
}

func TestCancelRejectsDoneNode(t *testing.T) {
	runtime := newRuntime()
	mustDefine(t, runtime, forkedChain())
	if err := runtime.Cancel("alice", "fork-1", "left", " "); !errors.Is(err, impl.ErrBlankArg) {
		t.Fatalf("blank cancel reason must return ErrBlankArg, got %v", err)
	}
	mustComplete(t, runtime, "alice", "fork-1", "root")
	if err := runtime.Cancel("alice", "fork-1", "root", "too late"); !errors.Is(err, impl.ErrAlreadyDone) {
		t.Fatalf("canceling a done node must return ErrAlreadyDone, got %v", err)
	}
}

func TestResolveQuorumSatisfied(t *testing.T) {
	runtime := newRuntime()
	mustDefine(t, runtime, quorumChain())
	mustComplete(t, runtime, "alice", "join-1", "a")
	mustComplete(t, runtime, "alice", "join-1", "b")
	result, err := runtime.Resolve("alice", "join-1", "join")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !result.Resolved {
		t.Fatalf("join with 2 of 3 branches must resolve, got %+v", result)
	}
	if result.Quorum != 2 || result.Satisfied != 2 || result.JoinID != "join" {
		t.Fatalf("join result = %+v, want quorum 2 satisfied 2", result)
	}
}

func TestResolveQuorumNotSatisfied(t *testing.T) {
	runtime := newRuntime()
	mustDefine(t, runtime, quorumChain())
	mustComplete(t, runtime, "alice", "join-1", "a")
	result, err := runtime.Resolve("alice", "join-1", "join")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if result.Resolved {
		t.Fatalf("join with 1 of 3 branches must not resolve, got %+v", result)
	}
	if result.Quorum != 2 || result.Satisfied != 1 {
		t.Fatalf("join result = %+v, want quorum 2 satisfied 1", result)
	}
	if _, err := runtime.Resolve("alice", "join-1", "a"); !errors.Is(err, impl.ErrNotJoin) {
		t.Fatalf("resolving a node without dependencies must return ErrNotJoin, got %v", err)
	}
}

func TestCompleteJoinRequiresQuorum(t *testing.T) {
	runtime := newRuntime()
	mustDefine(t, runtime, quorumChain())
	mustComplete(t, runtime, "alice", "join-1", "a")
	if _, err := runtime.Complete("alice", "join-1", "join", 100); !errors.Is(err, impl.ErrNodeNotReady) {
		t.Fatalf("join before quorum must return ErrNodeNotReady, got %v", err)
	}
	mustComplete(t, runtime, "alice", "join-1", "b")
	state := mustComplete(t, runtime, "alice", "join-1", "join")
	if state.Status != impl.NodeDone {
		t.Fatalf("join status = %q, want done", state.Status)
	}
	rewards := runtime.Rewards("alice", "join-1")
	if len(rewards) != 1 || rewards[0] != "gold" {
		t.Fatalf("rewards = %v, want [gold]", rewards)
	}
}

func TestQuorumZeroMeansAll(t *testing.T) {
	runtime := newRuntime()
	mustDefine(t, runtime, allOrNothingChain())
	mustComplete(t, runtime, "alice", "all-1", "a")
	partial, err := runtime.Resolve("alice", "all-1", "join")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if partial.Resolved {
		t.Fatalf("quorum zero must require every dependency, got %+v", partial)
	}
	if partial.Quorum != 2 || partial.Satisfied != 1 {
		t.Fatalf("effective quorum = %d satisfied = %d, want 2 and 1", partial.Quorum, partial.Satisfied)
	}
	mustComplete(t, runtime, "alice", "all-1", "b")
	full, err := runtime.Resolve("alice", "all-1", "join")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !full.Resolved || full.Satisfied != 2 || full.Quorum != 2 {
		t.Fatalf("full join = %+v, want resolved with 2 of 2", full)
	}
}

func TestSnapshotRestoreKeepsProgress(t *testing.T) {
	runtime := newRuntime()
	mustDefine(t, runtime, forkedChain())
	mustComplete(t, runtime, "alice", "fork-1", "root")
	data, err := runtime.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := newRuntime()
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	state := restored.State("alice", "fork-1")
	if got := statusOf(state, "root"); got != impl.NodeDone {
		t.Fatalf("restored root status = %q, want done", got)
	}
	if got := statusOf(state, "left"); got != impl.NodeActive {
		t.Fatalf("restored left status = %q, want active", got)
	}
	rewards := restored.Rewards("alice", "fork-1")
	if len(rewards) != 1 || rewards[0] != "gold" {
		t.Fatalf("restored rewards = %v, want [gold]", rewards)
	}
	left := mustComplete(t, restored, "alice", "fork-1", "left")
	if left.Seq != 2 {
		t.Fatalf("completion seq after restore = %d, want 2", left.Seq)
	}
	if err := restored.Restore([]byte("garbage")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("garbage restore must return ErrBadSnapshot, got %v", err)
	}
	if got := statusOf(restored.State("alice", "fork-1"), "root"); got != impl.NodeDone {
		t.Fatalf("failed restore must not drop progress, root = %q", got)
	}
}
