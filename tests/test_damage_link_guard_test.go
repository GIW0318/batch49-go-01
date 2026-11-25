package task

import (
	"errors"
	"testing"

	impl "gsb/batch49-go-game-a-11"
)

func testOptions() impl.Options {
	return impl.Options{
		MaxGroups:     4,
		MaxMembers:    6,
		TickSpan:      1000,
		TickCap:       50,
		DefaultHealth: 1000,
	}
}

func TestLinkKeepsMemberOrderAndPercent(t *testing.T) {
	g := impl.NewDamageLinkGuard(testOptions())
	if err := g.Link("raid", []string{"tank", "healer", "dps"}, 60); err != nil {
		t.Fatalf("link must succeed, got %v", err)
	}
	members := g.GroupMembers("raid")
	want := []string{"tank", "healer", "dps"}
	if len(members) != len(want) {
		t.Fatalf("members = %v, want %v", members, want)
	}
	for i := range want {
		if members[i] != want[i] {
			t.Fatalf("members = %v, want %v", members, want)
		}
	}
	pct, err := g.SharePct("raid")
	if err != nil {
		t.Fatalf("share percent lookup failed: %v", err)
	}
	if pct != 60 {
		t.Fatalf("share percent = %d, want 60", pct)
	}
}

func TestLinkRejectsBlankAndDuplicateMembers(t *testing.T) {
	g := impl.NewDamageLinkGuard(testOptions())
	if err := g.Link("   ", []string{"a"}, 50); !errors.Is(err, impl.ErrBlankGroup) {
		t.Fatalf("blank group id must be rejected, got %v", err)
	}
	if err := g.Link("raid", nil, 50); !errors.Is(err, impl.ErrBlankMember) {
		t.Fatalf("empty member list must be rejected, got %v", err)
	}
	if err := g.Link("raid", []string{"a", " "}, 50); !errors.Is(err, impl.ErrBlankMember) {
		t.Fatalf("blank member id must be rejected, got %v", err)
	}
	if err := g.Link("raid", []string{"a", "a"}, 50); !errors.Is(err, impl.ErrDuplicateMember) {
		t.Fatalf("duplicate member must be rejected, got %v", err)
	}
	if got := len(g.GroupMembers("raid")); got != 0 {
		t.Fatalf("rejected links must not create a group, got %d members", got)
	}
}

func TestLinkEnforcesGroupAndMemberLimits(t *testing.T) {
	g := impl.NewDamageLinkGuard(impl.Options{MaxGroups: 1, MaxMembers: 2, TickSpan: 1000, TickCap: 50, DefaultHealth: 1000})
	if err := g.Link("one", []string{"a", "b"}, 50); err != nil {
		t.Fatalf("first group must be accepted, got %v", err)
	}
	if err := g.Link("two", []string{"c", "d"}, 50); !errors.Is(err, impl.ErrGroupLimit) {
		t.Fatalf("second group must hit the group limit, got %v", err)
	}
	h := impl.NewDamageLinkGuard(impl.Options{MaxGroups: 4, MaxMembers: 2, TickSpan: 1000, TickCap: 50, DefaultHealth: 1000})
	if err := h.Link("big", []string{"a", "b", "c"}, 50); !errors.Is(err, impl.ErrMemberLimit) {
		t.Fatalf("three members must hit the member limit, got %v", err)
	}
}

func TestSettleSplitsEvenlyAcrossMembers(t *testing.T) {
	g := impl.NewDamageLinkGuard(testOptions())
	if err := g.Link("raid", []string{"tank", "healer", "dps", "support"}, 100); err != nil {
		t.Fatalf("link failed: %v", err)
	}
	shares, err := g.Settle(impl.Hit{Source: "boss", Target: "tank", GroupID: "raid", Amount: 200, At: 0})
	if err != nil {
		t.Fatalf("settle failed: %v", err)
	}
	if len(shares) != 4 {
		t.Fatalf("shares = %d, want 4", len(shares))
	}
	var total int64
	for _, s := range shares {
		if s.Amount != 50 {
			t.Fatalf("share for %s = %d, want 50", s.Member, s.Amount)
		}
		if s.Direct {
			t.Fatalf("share for %s must be linked, not direct", s.Member)
		}
		total += s.Amount
	}
	if total != 200 {
		t.Fatalf("total share = %d, want 200", total)
	}
	if hp := g.Health("tank"); hp != 950 {
		t.Fatalf("tank health = %d, want 950", hp)
	}
}

func TestSettleDistributesRemainderInJoinOrder(t *testing.T) {
	g := impl.NewDamageLinkGuard(testOptions())
	if err := g.Link("raid", []string{"first", "second", "third"}, 100); err != nil {
		t.Fatalf("link failed: %v", err)
	}
	shares, err := g.Settle(impl.Hit{Source: "boss", Target: "first", GroupID: "raid", Amount: 100, At: 0})
	if err != nil {
		t.Fatalf("settle failed: %v", err)
	}
	if len(shares) != 3 {
		t.Fatalf("shares = %d, want 3", len(shares))
	}
	want := []int64{34, 33, 33}
	for i, s := range shares {
		if s.Amount != want[i] {
			t.Fatalf("share %d = %d, want %d (remainder belongs to earlier members)", i, s.Amount, want[i])
		}
	}
}

func TestSettleHonorsSharePercentAndDirectPart(t *testing.T) {
	g := impl.NewDamageLinkGuard(testOptions())
	if err := g.Link("raid", []string{"tank", "dps"}, 25); err != nil {
		t.Fatalf("link failed: %v", err)
	}
	shares, err := g.Settle(impl.Hit{Source: "boss", Target: "tank", GroupID: "raid", Amount: 200, At: 0})
	if err != nil {
		t.Fatalf("settle failed: %v", err)
	}
	if len(shares) != 3 {
		t.Fatalf("shares = %d, want two linked shares plus one direct share", len(shares))
	}
	var linked, direct int64
	var directMember string
	for _, s := range shares {
		if s.Direct {
			direct += s.Amount
			directMember = s.Member
			continue
		}
		linked += s.Amount
	}
	if linked != 50 {
		t.Fatalf("linked amount = %d, want 50", linked)
	}
	if direct != 150 {
		t.Fatalf("direct amount = %d, want 150", direct)
	}
	if directMember != "tank" {
		t.Fatalf("direct part belongs to %q, want tank", directMember)
	}
	if hp := g.Health("tank"); hp != 825 {
		t.Fatalf("tank health = %d, want 825", hp)
	}
	if hp := g.Health("dps"); hp != 975 {
		t.Fatalf("dps health = %d, want 975", hp)
	}
}

func TestSettleRejectsBadInput(t *testing.T) {
	g := impl.NewDamageLinkGuard(testOptions())
	if _, err := g.Settle(impl.Hit{Target: "tank", GroupID: "missing", Amount: 10}); !errors.Is(err, impl.ErrUnknownGroup) {
		t.Fatalf("unknown group must be rejected, got %v", err)
	}
	if err := g.Link("raid", []string{"tank", "dps"}, 50); err != nil {
		t.Fatalf("link failed: %v", err)
	}
	if _, err := g.Settle(impl.Hit{Source: "boss", Target: "tank", GroupID: "raid", Amount: 0}); !errors.Is(err, impl.ErrBadAmount) {
		t.Fatalf("zero amount must be rejected, got %v", err)
	}
	if _, err := g.Settle(impl.Hit{Source: "boss", Target: "outsider", GroupID: "raid", Amount: 10}); !errors.Is(err, impl.ErrNotMember) {
		t.Fatalf("target outside the group must be rejected, got %v", err)
	}
}

func TestSettleOutputOrderedByJoinOrder(t *testing.T) {
	g := impl.NewDamageLinkGuard(testOptions())
	if err := g.Link("raid", []string{"zulu", "alpha", "mike"}, 100); err != nil {
		t.Fatalf("link failed: %v", err)
	}
	shares, err := g.Settle(impl.Hit{Source: "boss", Target: "zulu", GroupID: "raid", Amount: 60, At: 0})
	if err != nil {
		t.Fatalf("settle failed: %v", err)
	}
	want := []string{"zulu", "alpha", "mike"}
	for i, s := range shares {
		if s.Member != want[i] {
			t.Fatalf("share %d member = %q, want %q", i, s.Member, want[i])
		}
		if s.Order != i {
			t.Fatalf("share %d order = %d, want %d", i, s.Order, i)
		}
	}
}

func TestUnlinkRemovesMember(t *testing.T) {
	g := impl.NewDamageLinkGuard(testOptions())
	if err := g.Link("raid", []string{"a", "b", "c"}, 50); err != nil {
		t.Fatalf("link failed: %v", err)
	}
	if err := g.Unlink("raid", "b"); err != nil {
		t.Fatalf("unlink failed: %v", err)
	}
	members := g.GroupMembers("raid")
	if len(members) != 2 || members[0] != "a" || members[1] != "c" {
		t.Fatalf("members after unlink = %v, want [a c]", members)
	}
	if err := g.Unlink("raid", "b"); !errors.Is(err, impl.ErrNotMember) {
		t.Fatalf("removing an absent member must fail, got %v", err)
	}
	if err := g.Unlink("missing", "a"); !errors.Is(err, impl.ErrUnknownGroup) {
		t.Fatalf("unlinking from an unknown group must fail, got %v", err)
	}
}

func TestReflectCapsPerTick(t *testing.T) {
	g := impl.NewDamageLinkGuard(testOptions())
	got, err := g.Reflect("boss", "tank", 30, 100)
	if err != nil {
		t.Fatalf("first reflect failed: %v", err)
	}
	if got != 30 {
		t.Fatalf("first reflect = %d, want 30", got)
	}
	got, err = g.Reflect("boss", "tank", 30, 100)
	if err != nil {
		t.Fatalf("second reflect failed: %v", err)
	}
	if got != 20 {
		t.Fatalf("second reflect = %d, want 20", got)
	}
	got, err = g.Reflect("boss", "tank", 30, 100)
	if err != nil {
		t.Fatalf("third reflect failed: %v", err)
	}
	if got != 0 {
		t.Fatalf("third reflect = %d, want 0", got)
	}
	if used := g.ReflectedInTick("boss", 100); used != 50 {
		t.Fatalf("reflected in tick = %d, want 50", used)
	}
}

func TestTickResetClearsCap(t *testing.T) {
	g := impl.NewDamageLinkGuard(testOptions())
	if _, err := g.Reflect("boss", "tank", 50, 100); err != nil {
		t.Fatalf("reflect failed: %v", err)
	}
	if got, _ := g.Reflect("boss", "tank", 50, 100); got != 0 {
		t.Fatalf("cap must block the second reflect, got %d", got)
	}
	g.TickReset(1000)
	got, err := g.Reflect("boss", "tank", 30, 1000)
	if err != nil {
		t.Fatalf("reflect after tick reset failed: %v", err)
	}
	if got != 30 {
		t.Fatalf("reflect after tick reset = %d, want 30", got)
	}
}

func TestReflectRejectsBadAmountAndBlankIDs(t *testing.T) {
	g := impl.NewDamageLinkGuard(testOptions())
	if _, err := g.Reflect("boss", "tank", 0, 0); !errors.Is(err, impl.ErrBadAmount) {
		t.Fatalf("zero reflect amount must be rejected, got %v", err)
	}
	if _, err := g.Reflect("  ", "tank", 10, 0); !errors.Is(err, impl.ErrBlankMember) {
		t.Fatalf("blank source must be rejected, got %v", err)
	}
	if _, err := g.Reflect("boss", "", 10, 0); !errors.Is(err, impl.ErrBlankMember) {
		t.Fatalf("blank target must be rejected, got %v", err)
	}
}

func TestReflectDoesNotRouteThroughLink(t *testing.T) {
	g := impl.NewDamageLinkGuard(testOptions())
	if err := g.Link("raid", []string{"tank", "dps"}, 100); err != nil {
		t.Fatalf("link failed: %v", err)
	}
	got, err := g.Reflect("boss", "tank", 40, 100)
	if err != nil {
		t.Fatalf("reflect failed: %v", err)
	}
	if got != 40 {
		t.Fatalf("reflect = %d, want 40", got)
	}
	if hp := g.Health("boss"); hp != 960 {
		t.Fatalf("source health = %d, want 960", hp)
	}
	if hp := g.Health("tank"); hp != 1000 {
		t.Fatalf("target health = %d, want 1000", hp)
	}
	if total := g.ReflectedTotal("tank"); total != 40 {
		t.Fatalf("reflected total = %d, want 40", total)
	}
	if members := g.GroupMembers("raid"); len(members) != 2 {
		t.Fatalf("reflect must not touch the group, members = %v", members)
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	g := impl.NewDamageLinkGuard(testOptions())
	if err := g.Link("raid", []string{"a", "b", "c"}, 60); err != nil {
		t.Fatalf("link failed: %v", err)
	}
	if _, err := g.Settle(impl.Hit{Source: "boss", Target: "a", GroupID: "raid", Amount: 90, At: 0}); err != nil {
		t.Fatalf("settle failed: %v", err)
	}
	if _, err := g.Reflect("boss", "a", 25, 100); err != nil {
		t.Fatalf("reflect failed: %v", err)
	}
	snap, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	restored := impl.NewDamageLinkGuard(testOptions())
	if err := restored.Restore(snap); err != nil {
		t.Fatalf("restore failed: %v", err)
	}
	members := restored.GroupMembers("raid")
	if len(members) != 3 || members[0] != "a" || members[1] != "b" || members[2] != "c" {
		t.Fatalf("restored members = %v, want [a b c]", members)
	}
	if pct, _ := restored.SharePct("raid"); pct != 60 {
		t.Fatalf("restored share percent = %d, want 60", pct)
	}
	if hp := restored.Health("a"); hp != 946 {
		t.Fatalf("restored health of a = %d, want 946", hp)
	}
	if used := restored.ReflectedInTick("boss", 100); used != 25 {
		t.Fatalf("restored reflect tick = %d, want 25", used)
	}
	if total := restored.ReflectedTotal("a"); total != 25 {
		t.Fatalf("restored reflected total = %d, want 25", total)
	}
}

func TestRestoreRejectsBadSnapshot(t *testing.T) {
	g := impl.NewDamageLinkGuard(testOptions())
	if err := g.Link("raid", []string{"a", "b"}, 50); err != nil {
		t.Fatalf("link failed: %v", err)
	}
	if err := g.Restore([]byte("{ this is not a snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("broken snapshot must be rejected with ErrBadSnapshot, got %v", err)
	}
	if members := g.GroupMembers("raid"); len(members) != 2 {
		t.Fatalf("failed restore must keep the live state, members = %v", members)
	}
}
