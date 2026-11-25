package task

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	impl "gsb/batch49-go-game-a-29"
)

func rankLadder() []impl.RankRule {
	return []impl.RankRule{
		{Name: "novice", MinContribution: 0, MinTenureSeconds: 0},
		{Name: "veteran", MinContribution: 1000, MinTenureSeconds: 100},
		{Name: "master", MinContribution: 5000, MinTenureSeconds: 1000},
	}
}

func guildOptions() impl.Options {
	return impl.Options{
		Ranks:         rankLadder(),
		KindWeights:   map[string]int{"quest": 100, "pvp": 150, "donate": 200},
		WeekSeconds:   100,
		ActivityFloor: 100,
		DecayPermille: 500,
		MaxMembers:    10,
	}
}

func seedGuild(t *testing.T, l *impl.StandingLedger) {
	t.Helper()
	if err := l.AddMember("g1", "leader", 10); err != nil {
		t.Fatalf("add leader: %v", err)
	}
	if err := l.AddMember("g1", "alice", 20); err != nil {
		t.Fatalf("add alice: %v", err)
	}
}

func TestAddMemberCreatesGuildAndRanks(t *testing.T) {
	l := impl.NewStandingLedger(guildOptions())
	if err := l.AddMember("g1", "leader", 10); err != nil {
		t.Fatalf("add leader: %v", err)
	}
	guild, ok := l.Guild("g1")
	if !ok || guild.ID != "g1" || guild.Master != "leader" || guild.Members != 1 {
		t.Fatalf("guild header = %+v ok=%v, want leader with one member", guild, ok)
	}
	leader := l.Standing("g1", "leader")
	if leader.Rank != "master" || leader.Contribution != 0 || leader.GuildID != "g1" {
		t.Fatalf("leader standing = %+v, want master with zero contribution", leader)
	}
	if err := l.AddMember("g1", "alice", 20); err != nil {
		t.Fatalf("add alice: %v", err)
	}
	if got := l.Standing("g1", "alice"); got.Rank != "novice" || got.Member != "alice" {
		t.Fatalf("second member standing = %+v, want novice", got)
	}
	if err := l.AddMember("g1", "alice", 30); !errors.Is(err, impl.ErrMemberExists) {
		t.Fatalf("duplicate member = %v, want ErrMemberExists", err)
	}
	if err := l.AddMember("", "bob", 30); !errors.Is(err, impl.ErrBadGuild) {
		t.Fatalf("blank guild = %v, want ErrBadGuild", err)
	}
	if err := l.AddMember("g1", "  ", 30); !errors.Is(err, impl.ErrBadMember) {
		t.Fatalf("blank member = %v, want ErrBadMember", err)
	}
	if err := l.AddMember("g1", "bob", 0); !errors.Is(err, impl.ErrBadTime) {
		t.Fatalf("zero join time = %v, want ErrBadTime", err)
	}
	tight := guildOptions()
	tight.MaxMembers = 2
	small := impl.NewStandingLedger(tight)
	if err := small.AddMember("g2", "a", 10); err != nil {
		t.Fatalf("add a: %v", err)
	}
	if err := small.AddMember("g2", "b", 20); err != nil {
		t.Fatalf("add b: %v", err)
	}
	if err := small.AddMember("g2", "c", 30); !errors.Is(err, impl.ErrGuildFull) {
		t.Fatalf("third member = %v, want ErrGuildFull", err)
	}
}

func TestMembersAndGuildInfo(t *testing.T) {
	l := impl.NewStandingLedger(guildOptions())
	for _, m := range []struct {
		name string
		at   int64
	}{{"leader", 10}, {"zed", 20}, {"amy", 30}} {
		if err := l.AddMember("g1", m.name, m.at); err != nil {
			t.Fatalf("add %s: %v", m.name, err)
		}
	}
	members := l.Members("g1")
	if len(members) != 3 {
		t.Fatalf("members = %d, want 3", len(members))
	}
	want := []string{"amy", "leader", "zed"}
	for i, name := range want {
		if members[i].Member != name {
			t.Fatalf("members[%d] = %s, want %s", i, members[i].Member, name)
		}
	}
	guild, ok := l.Guild("g1")
	if !ok || guild.Members != 3 || guild.Master != "leader" {
		t.Fatalf("guild header = %+v ok=%v", guild, ok)
	}
	if _, ok := l.Guild("missing"); ok {
		t.Fatalf("unknown guild must not be found")
	}
	if got := len(l.Members("missing")); got != 0 {
		t.Fatalf("unknown guild members = %d, want 0", got)
	}
	ranks := l.Ranks()
	if len(ranks) != 3 || ranks[0].Name != "novice" || ranks[2].Name != "master" {
		t.Fatalf("ranks = %+v", ranks)
	}
	ranks[0].MinContribution = 777
	if got := l.Ranks()[0].MinContribution; got != 0 {
		t.Fatalf("Ranks must return a copy, got %d", got)
	}
}

func TestContributeWeights(t *testing.T) {
	l := impl.NewStandingLedger(guildOptions())
	seedGuild(t, l)
	if err := l.Contribute("g1", "alice", "quest", "e1", 100, 30); err != nil {
		t.Fatalf("quest contribution: %v", err)
	}
	if got := l.Standing("g1", "alice").Contribution; got != 100 {
		t.Fatalf("after quest = %d, want 100", got)
	}
	if err := l.Contribute("g1", "alice", "pvp", "e2", 100, 31); err != nil {
		t.Fatalf("pvp contribution: %v", err)
	}
	if got := l.Standing("g1", "alice").Contribution; got != 250 {
		t.Fatalf("after pvp = %d, want 250", got)
	}
	if err := l.Contribute("g1", "alice", "donate", "e3", 200, 32); err != nil {
		t.Fatalf("donate contribution: %v", err)
	}
	standing := l.Standing("g1", "alice")
	if standing.Contribution != 650 || standing.Rank != "novice" {
		t.Fatalf("standing after donate = %+v, want 650 novice", standing)
	}
	if err := l.Contribute("g1", "leader", "quest", "e4", 50, 33); err != nil {
		t.Fatalf("leader contribution: %v", err)
	}
	if got := l.Standing("g1", "leader").Contribution; got != 50 {
		t.Fatalf("leader contribution = %d, want 50", got)
	}
}

func TestContributeValidation(t *testing.T) {
	l := impl.NewStandingLedger(guildOptions())
	seedGuild(t, l)
	if err := l.Contribute("missing", "alice", "quest", "e1", 10, 30); !errors.Is(err, impl.ErrUnknownGuild) {
		t.Fatalf("unknown guild = %v, want ErrUnknownGuild", err)
	}
	if err := l.Contribute("g1", "nobody", "quest", "e2", 10, 30); !errors.Is(err, impl.ErrMemberNotFound) {
		t.Fatalf("unknown member = %v, want ErrMemberNotFound", err)
	}
	if err := l.Contribute("g1", "alice", "fishing", "e3", 10, 30); !errors.Is(err, impl.ErrBadKind) {
		t.Fatalf("unknown kind = %v, want ErrBadKind", err)
	}
	if err := l.Contribute("g1", "alice", "quest", "e4", 0, 30); !errors.Is(err, impl.ErrBadAmount) {
		t.Fatalf("zero amount = %v, want ErrBadAmount", err)
	}
	if err := l.Contribute("g1", "alice", "quest", "e5", -5, 30); !errors.Is(err, impl.ErrBadAmount) {
		t.Fatalf("negative amount = %v, want ErrBadAmount", err)
	}
	if err := l.Contribute("g1", "alice", "quest", "", 10, 30); !errors.Is(err, impl.ErrBadEvent) {
		t.Fatalf("blank event = %v, want ErrBadEvent", err)
	}
	if err := l.Contribute("g1", "alice", "quest", "e6", 10, 0); !errors.Is(err, impl.ErrBadTime) {
		t.Fatalf("zero time = %v, want ErrBadTime", err)
	}
	if got := l.Standing("g1", "alice").Contribution; got != 0 {
		t.Fatalf("rejected contributions changed standing to %d", got)
	}
}

func TestContributeIdempotentEventID(t *testing.T) {
	l := impl.NewStandingLedger(guildOptions())
	seedGuild(t, l)
	if err := l.Contribute("g1", "alice", "quest", "raid-1", 100, 30); err != nil {
		t.Fatalf("first contribution: %v", err)
	}
	if err := l.Contribute("g1", "alice", "donate", "raid-1", 900, 40); err != nil {
		t.Fatalf("duplicate contribution must be a no-op, got %v", err)
	}
	if got := l.Standing("g1", "alice").Contribution; got != 100 {
		t.Fatalf("duplicate event counted, contribution = %d", got)
	}
	if err := l.Contribute("g1", "alice", "quest", "raid-2", 50, 50); err != nil {
		t.Fatalf("second contribution: %v", err)
	}
	if got := l.Standing("g1", "alice").Contribution; got != 150 {
		t.Fatalf("distinct event not counted, contribution = %d", got)
	}
}

func TestPromoteContributionThreshold(t *testing.T) {
	l := impl.NewStandingLedger(guildOptions())
	seedGuild(t, l)
	if err := l.Contribute("g1", "alice", "quest", "e1", 999, 30); err != nil {
		t.Fatalf("contribution: %v", err)
	}
	miss, err := l.Promote("g1", "alice", 500)
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if miss.Changed || miss.From != "novice" || miss.To != "novice" || miss.Contribution != 999 {
		t.Fatalf("premature promotion = %+v", miss)
	}
	if err := l.Contribute("g1", "alice", "quest", "e2", 1, 40); err != nil {
		t.Fatalf("contribution: %v", err)
	}
	hit, err := l.Promote("g1", "alice", 500)
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if !hit.Changed || hit.From != "novice" || hit.To != "veteran" || hit.Contribution != 1000 {
		t.Fatalf("promotion = %+v, want novice to veteran at 1000", hit)
	}
	if hit.TenureSeconds != 480 {
		t.Fatalf("promotion tenure = %d, want 480", hit.TenureSeconds)
	}
	if got := l.Standing("g1", "alice").Rank; got != "veteran" {
		t.Fatalf("rank after promotion = %s, want veteran", got)
	}
}

func TestPromoteTenureThreshold(t *testing.T) {
	l := impl.NewStandingLedger(guildOptions())
	seedGuild(t, l)
	if err := l.Contribute("g1", "alice", "quest", "e1", 1000, 30); err != nil {
		t.Fatalf("contribution: %v", err)
	}
	early, err := l.Promote("g1", "alice", 100)
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if early.Changed || early.Contribution != 1000 || early.TenureSeconds != 80 {
		t.Fatalf("early promotion = %+v, want rejected at tenure 80", early)
	}
	onTime, err := l.Promote("g1", "alice", 120)
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if !onTime.Changed || onTime.To != "veteran" || onTime.TenureSeconds != 100 {
		t.Fatalf("on time promotion = %+v, want veteran at tenure 100", onTime)
	}
}

func TestPromoteOneStepAtATime(t *testing.T) {
	l := impl.NewStandingLedger(guildOptions())
	seedGuild(t, l)
	if err := l.Contribute("g1", "alice", "quest", "e1", 6000, 30); err != nil {
		t.Fatalf("contribution: %v", err)
	}
	first, err := l.Promote("g1", "alice", 2000)
	if err != nil || !first.Changed || first.To != "veteran" {
		t.Fatalf("first promote = %+v err=%v, want veteran", first, err)
	}
	second, err := l.Promote("g1", "alice", 2000)
	if err != nil || !second.Changed || second.To != "master" {
		t.Fatalf("second promote = %+v err=%v, want master", second, err)
	}
	third, err := l.Promote("g1", "alice", 2000)
	if err != nil {
		t.Fatalf("third promote: %v", err)
	}
	if third.Changed || third.Reason != impl.ReasonMaxRank || third.From != "master" || third.To != "master" {
		t.Fatalf("third promote = %+v, want max rank", third)
	}
	leader, err := l.Promote("g1", "leader", 2000)
	if err != nil {
		t.Fatalf("leader promote: %v", err)
	}
	if leader.Changed || leader.Reason != impl.ReasonMaxRank {
		t.Fatalf("leader promote = %+v, want max rank", leader)
	}
}

func TestWeeklyDecayInactiveMember(t *testing.T) {
	l := impl.NewStandingLedger(guildOptions())
	seedGuild(t, l)
	if err := l.Contribute("g1", "alice", "quest", "e1", 1000, 20); err != nil {
		t.Fatalf("contribution: %v", err)
	}
	changes, err := l.WeeklyDecay("g1", 1)
	if err != nil {
		t.Fatalf("decay: %v", err)
	}
	if len(changes) != 0 {
		t.Fatalf("novice member must not be demoted, changes = %+v", changes)
	}
	if got := l.Standing("g1", "alice").Contribution; got != 500 {
		t.Fatalf("decayed contribution = %d, want 500", got)
	}
	if _, err := l.WeeklyDecay("missing", 1); !errors.Is(err, impl.ErrUnknownGuild) {
		t.Fatalf("unknown guild decay = %v, want ErrUnknownGuild", err)
	}
	if _, err := l.WeeklyDecay("g1", 0); !errors.Is(err, impl.ErrBadWeek) {
		t.Fatalf("week zero = %v, want ErrBadWeek", err)
	}
}

func TestWeeklyDecayDemotesBelowRank(t *testing.T) {
	l := impl.NewStandingLedger(guildOptions())
	seedGuild(t, l)
	if err := l.Contribute("g1", "alice", "quest", "e1", 1000, 20); err != nil {
		t.Fatalf("contribution: %v", err)
	}
	if _, err := l.Promote("g1", "alice", 200); err != nil {
		t.Fatalf("promote: %v", err)
	}
	changes, err := l.WeeklyDecay("g1", 2)
	if err != nil {
		t.Fatalf("decay: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("changes = %+v, want exactly one demotion", changes)
	}
	change := changes[0]
	if change.Member != "alice" || !change.Changed || change.From != "veteran" || change.To != "novice" ||
		change.Reason != impl.ReasonDemoted || change.Contribution != 500 {
		t.Fatalf("demotion = %+v, want veteran to novice at 500", change)
	}
	standing := l.Standing("g1", "alice")
	if standing.Rank != "novice" || standing.Contribution != 500 {
		t.Fatalf("standing after demotion = %+v", standing)
	}
}

func TestWeeklyDecayMasterExempt(t *testing.T) {
	l := impl.NewStandingLedger(guildOptions())
	seedGuild(t, l)
	if err := l.Contribute("g1", "leader", "quest", "e1", 5000, 20); err != nil {
		t.Fatalf("leader contribution: %v", err)
	}
	changes, err := l.WeeklyDecay("g1", 3)
	if err != nil {
		t.Fatalf("decay: %v", err)
	}
	if len(changes) != 0 {
		t.Fatalf("master must not decay, changes = %+v", changes)
	}
	standing := l.Standing("g1", "leader")
	if standing.Contribution != 5000 || standing.Rank != "master" {
		t.Fatalf("master standing = %+v, want contribution 5000", standing)
	}
}

func TestWeeklyDecayActiveMemberKeepsStanding(t *testing.T) {
	opts := guildOptions()
	l := impl.NewStandingLedger(opts)
	if err := l.AddMember("g1", "leader", 10); err != nil {
		t.Fatalf("add leader: %v", err)
	}
	if err := l.AddMember("g1", "alice", 110); err != nil {
		t.Fatalf("add alice: %v", err)
	}
	if err := l.Contribute("g1", "alice", "quest", "e1", 500, 120); err != nil {
		t.Fatalf("contribution: %v", err)
	}
	changes, err := l.WeeklyDecay("g1", 1)
	if err != nil {
		t.Fatalf("decay: %v", err)
	}
	if len(changes) != 0 {
		t.Fatalf("active member must not change rank, changes = %+v", changes)
	}
	if got := l.Standing("g1", "alice").Contribution; got != 500 {
		t.Fatalf("active contribution = %d, want 500", got)
	}
}

func TestLeaveAndRejoinMember(t *testing.T) {
	l := impl.NewStandingLedger(guildOptions())
	if err := l.AddMember("g1", "leader", 10); err != nil {
		t.Fatalf("add leader: %v", err)
	}
	if err := l.AddMember("g1", "alice", 20); err != nil {
		t.Fatalf("add alice: %v", err)
	}
	if err := l.AddMember("g1", "bob", 30); err != nil {
		t.Fatalf("add bob: %v", err)
	}
	if err := l.Contribute("g1", "alice", "quest", "e1", 300, 30); err != nil {
		t.Fatalf("contribution: %v", err)
	}
	if err := l.Leave("g1", "alice"); err != nil {
		t.Fatalf("leave: %v", err)
	}
	if got := len(l.Members("g1")); got != 2 {
		t.Fatalf("members after leave = %d, want 2", got)
	}
	if got := l.Standing("g1", "alice"); got.Contribution != 0 || got.Rank != "" {
		t.Fatalf("standing after leave = %+v, want zero", got)
	}
	if err := l.Contribute("g1", "alice", "quest", "e2", 10, 40); !errors.Is(err, impl.ErrMemberNotFound) {
		t.Fatalf("contribute after leave = %v, want ErrMemberNotFound", err)
	}
	if err := l.Leave("g1", "alice"); !errors.Is(err, impl.ErrMemberNotFound) {
		t.Fatalf("second leave = %v, want ErrMemberNotFound", err)
	}
	if err := l.Leave("missing", "alice"); !errors.Is(err, impl.ErrUnknownGuild) {
		t.Fatalf("leave from unknown guild = %v, want ErrUnknownGuild", err)
	}
	if err := l.AddMember("g1", "alice", 500); err != nil {
		t.Fatalf("rejoin: %v", err)
	}
	rejoined := l.Standing("g1", "alice")
	if rejoined.Contribution != 0 || rejoined.Rank != "novice" {
		t.Fatalf("rejoined standing = %+v, want fresh novice", rejoined)
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	l := impl.NewStandingLedger(guildOptions())
	seedGuild(t, l)
	if err := l.Contribute("g1", "alice", "quest", "e1", 1000, 20); err != nil {
		t.Fatalf("contribution: %v", err)
	}
	if _, err := l.Promote("g1", "alice", 200); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if _, err := l.WeeklyDecay("g1", 2); err != nil {
		t.Fatalf("decay: %v", err)
	}
	data, err := l.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := impl.NewStandingLedger(guildOptions())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	members := restored.Members("g1")
	if len(members) != 2 || members[0].Member != "alice" || members[1].Member != "leader" {
		t.Fatalf("restored members = %+v", members)
	}
	if got := restored.Standing("g1", "alice"); got.Contribution != 500 || got.Rank != "novice" {
		t.Fatalf("restored alice = %+v, want 500 novice", got)
	}
	guild, ok := restored.Guild("g1")
	if !ok || guild.Master != "leader" {
		t.Fatalf("restored guild = %+v ok=%v", guild, ok)
	}
	if err := restored.Contribute("g1", "alice", "quest", "e1", 500, 300); err != nil {
		t.Fatalf("duplicate after restore: %v", err)
	}
	if got := restored.Standing("g1", "alice").Contribution; got != 500 {
		t.Fatalf("restored event index lost, contribution = %d", got)
	}
	if err := restored.Restore([]byte("broken")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("bad payload = %v, want ErrBadSnapshot", err)
	}
	if got := len(restored.Members("g1")); got != 2 {
		t.Fatalf("failed restore changed members to %d", got)
	}
	if err := restored.Restore(nil); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("nil payload = %v, want ErrBadSnapshot", err)
	}
}

func TestConcurrentContribute(t *testing.T) {
	l := impl.NewStandingLedger(guildOptions())
	seedGuild(t, l)
	var wg sync.WaitGroup
	errs := make(chan error, 128)
	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				id := fmt.Sprintf("w%d-%d", worker, i)
				if err := l.Contribute("g1", "alice", "quest", id, 10, 30); err != nil {
					errs <- err
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent contribution failed: %v", err)
	}
	if got := l.Standing("g1", "alice").Contribution; got != 600 {
		t.Fatalf("contribution after concurrent writes = %d, want 600", got)
	}
}
