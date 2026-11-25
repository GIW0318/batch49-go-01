package task

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	impl "gsb/batch49-go-game-c-14"
)

func durMs(v int) time.Duration { return time.Duration(v) * time.Millisecond }

func groupConfig() impl.Config {
	return impl.Config{MaxMembers: 8, MaxDriftMs: 100, CatchupStepMs: 100, StartTimeoutMs: 5000}
}

func mustJoin(t *testing.T, g *impl.GroupPlaybackSync, dev string) impl.MemberID {
	t.Helper()
	id, err := g.Join(dev, impl.Caps{BufferMs: 3000})
	if err != nil {
		t.Fatalf("Join(%q) failed: %v", dev, err)
	}
	return id
}

func memberOf(t *testing.T, g *impl.GroupPlaybackSync, dev string) impl.Member {
	t.Helper()
	for _, m := range g.Members() {
		if m.DeviceID == dev {
			return m
		}
	}
	t.Fatalf("device %q is not in the group", dev)
	return impl.Member{}
}

func readyGroup(t *testing.T, g *impl.GroupPlaybackSync, devs ...string) []impl.MemberID {
	t.Helper()
	ids := make([]impl.MemberID, 0, len(devs))
	for _, dev := range devs {
		ids = append(ids, mustJoin(t, g, dev))
	}
	for _, dev := range devs {
		if err := g.ReportOffset(dev, durMs(20)); err != nil {
			t.Fatalf("ReportOffset(%q) failed: %v", dev, err)
		}
	}
	if !g.Ready() {
		t.Fatalf("group must be ready after every member reports a small offset")
	}
	return ids
}

func TestFreshGroupIsIdle(t *testing.T) {
	g := impl.NewGroupPlaybackSync(groupConfig())
	if g.Started() {
		t.Fatalf("a fresh group must not be started")
	}
	if g.Ready() {
		t.Fatalf("a fresh group without members must not be ready")
	}
	if got := g.GroupNow(); got != 0 {
		t.Fatalf("GroupNow() = %d, want 0", got)
	}
	if got := len(g.Members()); got != 0 {
		t.Fatalf("member count = %d, want 0", got)
	}
	if _, ok := g.Leader(); ok {
		t.Fatalf("a fresh group must not have a leader")
	}
	if got := g.Missing(); len(got) != 0 {
		t.Fatalf("missing = %v, want none", got)
	}
	rep := g.CatchupStep()
	if len(rep.Entries) != 0 || rep.Timeline != 0 {
		t.Fatalf("catch up before start = %+v, want an empty report at timeline 0", rep)
	}
	if err := g.Advance(durMs(100)); !errors.Is(err, impl.ErrNotStarted) {
		t.Fatalf("advancing an idle group must fail with ErrNotStarted, got %v", err)
	}
}

func TestJoinElectsLeaderInOrder(t *testing.T) {
	g := impl.NewGroupPlaybackSync(groupConfig())
	first := mustJoin(t, g, "dev-a")
	second := mustJoin(t, g, "dev-b")
	third := mustJoin(t, g, "dev-c")
	if first == second || second == third || first == third {
		t.Fatalf("member identifiers must be distinct: %q %q %q", first, second, third)
	}
	if leader, ok := g.Leader(); !ok || leader != first {
		t.Fatalf("leader = %q ok=%v, want the first joined member %q", leader, ok, first)
	}
	members := g.Members()
	if len(members) != 3 {
		t.Fatalf("member count = %d, want 3", len(members))
	}
	wantDevices := []string{"dev-a", "dev-b", "dev-c"}
	for i, want := range wantDevices {
		if members[i].DeviceID != want {
			t.Fatalf("member %d device = %q, want %q in join order", i, members[i].DeviceID, want)
		}
		if members[i].Ready || members[i].Paused {
			t.Fatalf("member %d must join unready and playing", i)
		}
	}
	if !members[0].Leader || members[1].Leader || members[2].Leader {
		t.Fatalf("exactly the first member must be the leader, got %+v", members)
	}
	missing := g.Missing()
	if len(missing) != 3 || missing[0] != first || missing[1] != second || missing[2] != third {
		t.Fatalf("missing = %v, want all three members in join order", missing)
	}
}

func TestJoinRejectsBadDevice(t *testing.T) {
	g := impl.NewGroupPlaybackSync(groupConfig())
	if _, err := g.Join("   ", impl.Caps{}); !errors.Is(err, impl.ErrBadDevice) {
		t.Fatalf("blank device must fail with ErrBadDevice, got %v", err)
	}
	if _, err := g.Join("", impl.Caps{}); !errors.Is(err, impl.ErrBadDevice) {
		t.Fatalf("empty device must fail with ErrBadDevice, got %v", err)
	}
	mustJoin(t, g, "dev-a")
	if _, err := g.Join("dev-a", impl.Caps{}); !errors.Is(err, impl.ErrDuplicateDevice) {
		t.Fatalf("duplicate device must fail with ErrDuplicateDevice, got %v", err)
	}
	if got := len(g.Members()); got != 1 {
		t.Fatalf("member count = %d, want 1 after the rejected joins", got)
	}
}

func TestJoinEnforcesCapacity(t *testing.T) {
	cfg := groupConfig()
	cfg.MaxMembers = 2
	g := impl.NewGroupPlaybackSync(cfg)
	mustJoin(t, g, "dev-a")
	mustJoin(t, g, "dev-b")
	if _, err := g.Join("dev-c", impl.Caps{}); !errors.Is(err, impl.ErrGroupFull) {
		t.Fatalf("third member must fail with ErrGroupFull, got %v", err)
	}
	if got := len(g.Members()); got != 2 {
		t.Fatalf("member count = %d, want the cap 2", got)
	}
}

func TestReportOffsetWithinLimitMarksReady(t *testing.T) {
	g := impl.NewGroupPlaybackSync(groupConfig())
	first := mustJoin(t, g, "dev-a")
	mustJoin(t, g, "dev-b")
	if g.Ready() {
		t.Fatalf("the group must not be ready before any report")
	}
	if err := g.ReportOffset("dev-a", durMs(30)); err != nil {
		t.Fatalf("ReportOffset(dev-a) failed: %v", err)
	}
	if m := memberOf(t, g, "dev-a"); m.DriftMs != 30 || !m.Ready || m.Paused {
		t.Fatalf("dev-a after a small report = %+v, want drift 30 and ready", m)
	}
	if g.Ready() {
		t.Fatalf("the group must stay unready while dev-b has not reported")
	}
	if err := g.ReportOffset("dev-b", durMs(-30)); err != nil {
		t.Fatalf("ReportOffset(dev-b) failed: %v", err)
	}
	if !g.Ready() {
		t.Fatalf("the group must be ready once every member is inside the drift limit")
	}
	if got := g.Missing(); len(got) != 0 {
		t.Fatalf("missing = %v, want none", got)
	}
	if m := memberOf(t, g, "dev-b"); m.DriftMs != -30 || !m.Ready {
		t.Fatalf("dev-b after a negative report = %+v, want drift -30 and ready", m)
	}
	if leader, _ := g.Leader(); leader != first {
		t.Fatalf("reports must not move the leadership")
	}
}

func TestReportOffsetBeyondLimitPausesMember(t *testing.T) {
	g := impl.NewGroupPlaybackSync(groupConfig())
	first := mustJoin(t, g, "dev-a")
	mustJoin(t, g, "dev-b")
	if err := g.ReportOffset("dev-a", durMs(250)); err != nil {
		t.Fatalf("ReportOffset(dev-a) failed: %v", err)
	}
	if m := memberOf(t, g, "dev-a"); !m.Paused || m.Ready || m.DriftMs != 250 {
		t.Fatalf("dev-a = %+v, want paused with drift 250", m)
	}
	if err := g.ReportOffset("dev-b", durMs(10)); err != nil {
		t.Fatalf("ReportOffset(dev-b) failed: %v", err)
	}
	if g.Ready() {
		t.Fatalf("a paused member must keep the group unready")
	}
	missing := g.Missing()
	if len(missing) != 1 || missing[0] != first {
		t.Fatalf("missing = %v, want only the paused member", missing)
	}
	if err := g.ReportOffset("dev-x", durMs(10)); !errors.Is(err, impl.ErrMemberNotFound) {
		t.Fatalf("reporting for an unknown device must fail with ErrMemberNotFound, got %v", err)
	}
}

func TestStartBeginsTimeline(t *testing.T) {
	g := impl.NewGroupPlaybackSync(groupConfig())
	readyGroup(t, g, "dev-a", "dev-b")
	if err := g.Start(1000); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if !g.Started() {
		t.Fatalf("Started must report true after Start")
	}
	if got := g.GroupNow(); got != 1000 {
		t.Fatalf("GroupNow() = %d, want the pinned 1000", got)
	}
	for _, dev := range []string{"dev-a", "dev-b"} {
		if m := memberOf(t, g, dev); m.Position != 1000 {
			t.Fatalf("member %s position = %d, want the start position 1000", dev, m.Position)
		}
	}
	if err := g.Advance(durMs(500)); err != nil {
		t.Fatalf("Advance failed: %v", err)
	}
	if got := g.GroupNow(); got != 1500 {
		t.Fatalf("GroupNow() = %d, want 1500 after the advance", got)
	}
	for _, dev := range []string{"dev-a", "dev-b"} {
		if m := memberOf(t, g, dev); m.Position != 1500 {
			t.Fatalf("member %s position = %d, want 1500", dev, m.Position)
		}
	}
}

func TestStartTwiceRejected(t *testing.T) {
	g := impl.NewGroupPlaybackSync(groupConfig())
	readyGroup(t, g, "dev-a")
	if err := g.Start(0); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if err := g.Start(1000); !errors.Is(err, impl.ErrAlreadyStarted) {
		t.Fatalf("a second Start must fail with ErrAlreadyStarted, got %v", err)
	}
	if got := g.GroupNow(); got != 0 {
		t.Fatalf("GroupNow() = %d, want the first start position kept", got)
	}
}

func TestStartRejectsBackwardTime(t *testing.T) {
	g := impl.NewGroupPlaybackSync(groupConfig())
	readyGroup(t, g, "dev-a")
	if err := g.Start(-5); !errors.Is(err, impl.ErrBadStartTime) {
		t.Fatalf("a start behind the timeline must fail with ErrBadStartTime, got %v", err)
	}
	if g.Started() {
		t.Fatalf("a rejected start must not mark the group started")
	}
	if got := g.GroupNow(); got != 0 {
		t.Fatalf("GroupNow() = %d, want 0 after the rejected start", got)
	}
}

func TestAdvanceMovesOnlyPlayingMembers(t *testing.T) {
	g := impl.NewGroupPlaybackSync(groupConfig())
	readyGroup(t, g, "dev-a", "dev-b", "dev-c")
	if err := g.Start(0); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if err := g.Advance(durMs(500)); err != nil {
		t.Fatalf("Advance failed: %v", err)
	}
	for _, dev := range []string{"dev-a", "dev-b", "dev-c"} {
		if m := memberOf(t, g, dev); m.Position != 500 {
			t.Fatalf("member %s position = %d, want 500", dev, m.Position)
		}
	}
	if err := g.ReportOffset("dev-a", durMs(200)); err != nil {
		t.Fatalf("ReportOffset(dev-a) failed: %v", err)
	}
	if err := g.Advance(durMs(300)); err != nil {
		t.Fatalf("Advance failed: %v", err)
	}
	if got := g.GroupNow(); got != 800 {
		t.Fatalf("GroupNow() = %d, want 800", got)
	}
	if m := memberOf(t, g, "dev-a"); m.Position != 500 || !m.Paused {
		t.Fatalf("paused dev-a = %+v, want the position frozen at 500", m)
	}
	for _, dev := range []string{"dev-b", "dev-c"} {
		if m := memberOf(t, g, dev); m.Position != 800 {
			t.Fatalf("member %s position = %d, want 800", dev, m.Position)
		}
	}
	if err := g.Advance(0); !errors.Is(err, impl.ErrBadAdvance) {
		t.Fatalf("zero advance must fail with ErrBadAdvance, got %v", err)
	}
}

func TestCatchupStepIsBoundedPerFrame(t *testing.T) {
	g := impl.NewGroupPlaybackSync(groupConfig())
	ids := readyGroup(t, g, "dev-a", "dev-b")
	first := ids[0]
	if err := g.Start(0); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if err := g.Advance(durMs(500)); err != nil {
		t.Fatalf("Advance failed: %v", err)
	}
	if err := g.ReportOffset("dev-a", durMs(200)); err != nil {
		t.Fatalf("ReportOffset(dev-a) failed: %v", err)
	}
	if err := g.Advance(durMs(300)); err != nil {
		t.Fatalf("Advance failed: %v", err)
	}
	steps := []struct {
		advance   int
		remaining int
		done      bool
	}{
		{100, 200, false},
		{100, 100, false},
		{100, 0, true},
	}
	for i, want := range steps {
		rep := g.CatchupStep()
		if rep.Timeline != 800 {
			t.Fatalf("step %d report timeline = %d, want 800", i, rep.Timeline)
		}
		if len(rep.Entries) != 1 {
			t.Fatalf("step %d entries = %d, want only the lagging member", i, len(rep.Entries))
		}
		e := rep.Entries[0]
		if e.Member != first || e.DeviceID != "dev-a" {
			t.Fatalf("step %d entry = %+v, want dev-a", i, e)
		}
		if e.AdvanceMs != want.advance || e.RemainingMs != want.remaining || e.Done != want.done {
			t.Fatalf("step %d entry = %+v, want advance %d remaining %d done %v", i, e, want.advance, want.remaining, want.done)
		}
	}
	if m := memberOf(t, g, "dev-a"); m.Position != 800 || m.Paused || !m.Ready {
		t.Fatalf("caught up dev-a = %+v, want position 800 and resumed", m)
	}
	if rep := g.CatchupStep(); len(rep.Entries) != 0 {
		t.Fatalf("a synced group must report no catch up work, got %+v", rep)
	}
	if m := memberOf(t, g, "dev-b"); m.Position != 800 {
		t.Fatalf("the healthy member must stay on the timeline, got position %d", m.Position)
	}
}

func TestLeaveReelectsLeaderKeepingTimeline(t *testing.T) {
	g := impl.NewGroupPlaybackSync(groupConfig())
	readyGroup(t, g, "dev-a", "dev-b", "dev-c")
	if err := g.Start(0); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if err := g.Advance(durMs(500)); err != nil {
		t.Fatalf("Advance failed: %v", err)
	}
	if err := g.Leave("dev-a"); err != nil {
		t.Fatalf("Leave(dev-a) failed: %v", err)
	}
	leader, ok := g.Leader()
	if !ok {
		t.Fatalf("a non empty group must keep a leader")
	}
	if m := memberOf(t, g, string(leader)); m.DeviceID != "dev-b" {
		t.Fatalf("new leader = %q, want dev-b", m.DeviceID)
	}
	if got := g.GroupNow(); got != 500 {
		t.Fatalf("GroupNow() = %d, want the timeline to survive the leader change", got)
	}
	if err := g.Leave("dev-x"); !errors.Is(err, impl.ErrMemberNotFound) {
		t.Fatalf("leaving an unknown device must fail with ErrMemberNotFound, got %v", err)
	}
	if err := g.Leave("dev-b"); err != nil {
		t.Fatalf("Leave(dev-b) failed: %v", err)
	}
	if err := g.Leave("dev-c"); err != nil {
		t.Fatalf("Leave(dev-c) failed: %v", err)
	}
	if g.Started() {
		t.Fatalf("an empty group must return to the idle state")
	}
	if got := g.GroupNow(); got != 0 {
		t.Fatalf("GroupNow() = %d, want 0 for an empty group", got)
	}
	if _, ok := g.Leader(); ok {
		t.Fatalf("an empty group must not keep a leader")
	}
}

func TestSnapshotRestoreKeepsGroupState(t *testing.T) {
	g := impl.NewGroupPlaybackSync(groupConfig())
	readyGroup(t, g, "dev-a", "dev-b", "dev-c")
	if err := g.Start(1000); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if err := g.Advance(durMs(500)); err != nil {
		t.Fatalf("Advance failed: %v", err)
	}
	if err := g.ReportOffset("dev-b", durMs(200)); err != nil {
		t.Fatalf("ReportOffset(dev-b) failed: %v", err)
	}
	if err := g.Advance(durMs(400)); err != nil {
		t.Fatalf("Advance failed: %v", err)
	}
	if rep := g.CatchupStep(); len(rep.Entries) != 1 || rep.Entries[0].AdvanceMs != 100 {
		t.Fatalf("catch up before the snapshot = %+v, want one 100ms step", rep)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	cfg := groupConfig()
	cfg.MaxMembers = 1
	restored := impl.NewGroupPlaybackSync(cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("Restore failed: %v", err)
	}
	if !restored.Started() {
		t.Fatalf("restored group must stay started")
	}
	if got := restored.GroupNow(); got != 1900 {
		t.Fatalf("restored GroupNow() = %d, want 1900", got)
	}
	if got := len(restored.Members()); got != 3 {
		t.Fatalf("restored member count = %d, want 3", got)
	}
	if leader, ok := restored.Leader(); !ok {
		t.Fatalf("restored group must keep a leader")
	} else if m := memberOf(t, restored, string(leader)); m.DeviceID != "dev-a" {
		t.Fatalf("restored leader = %q, want dev-a", m.DeviceID)
	}
	m := memberOf(t, restored, "dev-b")
	if !m.Paused || m.Position != 1600 || m.DriftMs != 200 {
		t.Fatalf("restored dev-b = %+v, want paused at 1600 with drift 200", m)
	}
	rep := restored.CatchupStep()
	if len(rep.Entries) != 1 || rep.Entries[0].AdvanceMs != 100 || rep.Entries[0].RemainingMs != 200 {
		t.Fatalf("restored catch up = %+v, want the saved progress to continue", rep)
	}
	if m := memberOf(t, restored, "dev-b"); m.Position != 1700 {
		t.Fatalf("restored dev-b position = %d, want 1700 after one more step", m.Position)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	g := impl.NewGroupPlaybackSync(groupConfig())
	readyGroup(t, g, "dev-a", "dev-b")
	if err := g.Start(0); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if err := g.Advance(durMs(200)); err != nil {
		t.Fatalf("Advance failed: %v", err)
	}
	if err := g.Restore([]byte("###not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if !g.Started() || g.GroupNow() != 200 {
		t.Fatalf("a rejected restore must not touch the live group, started=%v now=%d", g.Started(), g.GroupNow())
	}
	if got := len(g.Members()); got != 2 {
		t.Fatalf("member count = %d, want the live table kept", got)
	}
	if err := g.Advance(durMs(100)); err != nil {
		t.Fatalf("the live group must keep advancing, got %v", err)
	}
	if got := g.GroupNow(); got != 300 {
		t.Fatalf("GroupNow() = %d, want 300", got)
	}
}

func TestConcurrentReportOffset(t *testing.T) {
	g := impl.NewGroupPlaybackSync(groupConfig())
	devs := []string{"dev-0", "dev-1", "dev-2", "dev-3", "dev-4", "dev-5", "dev-6", "dev-7"}
	for _, dev := range devs {
		mustJoin(t, g, dev)
	}
	var wg sync.WaitGroup
	var failures int64
	for _, dev := range devs {
		wg.Add(1)
		go func(dev string) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				if err := g.ReportOffset(dev, durMs(30)); err != nil {
					atomic.AddInt64(&failures, 1)
					return
				}
				if _, ok := g.Leader(); !ok {
					atomic.AddInt64(&failures, 1)
					return
				}
			}
		}(dev)
	}
	wg.Wait()
	if failures != 0 {
		t.Fatalf("%d workers observed a failing report or a missing leader", failures)
	}
	if !g.Ready() {
		t.Fatalf("the group must be ready after every device reported")
	}
	for _, dev := range devs {
		if m := memberOf(t, g, dev); m.DriftMs != 30 || !m.Ready || m.Paused {
			t.Fatalf("member %s = %+v, want drift 30 and ready", dev, m)
		}
	}
	if got := g.Missing(); len(got) != 0 {
		t.Fatalf("missing = %v, want none", got)
	}
}
