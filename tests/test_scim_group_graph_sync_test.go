package task

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	impl "gsb/batch49-go-safety-a-03"
)

func graphLimits() impl.Limits {
	return impl.Limits{MaxGroupsPerTenant: 12, MaxMembersPerGroup: 8, MaxDepth: 3}
}

func userRef(id string) impl.Ref  { return impl.Ref{Type: impl.RefUser, ID: id} }
func groupRef(id string) impl.Ref { return impl.Ref{Type: impl.RefGroup, ID: id} }

func upsertGroup(t *testing.T, s *impl.ScimGroupGraphSync, tenantID, groupID string) impl.Group {
	t.Helper()
	g, err := s.UpsertGroup(tenantID, impl.Group{ID: groupID, DisplayName: "team " + groupID})
	if err != nil {
		t.Fatalf("upsert %q must succeed, got %v", groupID, err)
	}
	return g
}

func TestUpsertGroupValidatesIdentity(t *testing.T) {
	s := impl.NewScimGroupGraphSync(graphLimits())
	if _, err := s.UpsertGroup("", impl.Group{ID: "g-1"}); !errors.Is(err, impl.ErrBlankTenant) {
		t.Fatalf("upsert without a tenant = %v, want ErrBlankTenant", err)
	}
	if _, err := s.UpsertGroup("tenant-1", impl.Group{ID: "  "}); !errors.Is(err, impl.ErrBlankGroupID) {
		t.Fatalf("upsert without a group id = %v, want ErrBlankGroupID", err)
	}
}

func TestUpsertGroupCreatesThenUpdates(t *testing.T) {
	s := impl.NewScimGroupGraphSync(graphLimits())
	created := upsertGroup(t, s, "tenant-1", "g-eng")
	if created.Version != 1 {
		t.Fatalf("fresh group version = %d, want 1", created.Version)
	}
	updated, err := s.UpsertGroup("tenant-1", impl.Group{ID: "g-eng", DisplayName: "engineering"})
	if err != nil {
		t.Fatalf("second upsert must succeed, got %v", err)
	}
	if updated.Version != 2 {
		t.Fatalf("updated group version = %d, want 2", updated.Version)
	}
	if updated.DisplayName != "engineering" {
		t.Fatalf("display name = %q, want engineering", updated.DisplayName)
	}
	stored, ok := s.Group("tenant-1", "g-eng")
	if !ok {
		t.Fatalf("stored group must be readable")
	}
	if stored.Version != 2 || stored.DisplayName != "engineering" {
		t.Fatalf("stored group = %+v, want version 2 named engineering", stored)
	}
	if _, ok := s.Group("tenant-1", "g-missing"); ok {
		t.Fatalf("unknown group must not resolve")
	}
	if err := s.DeleteGroup("tenant-1", "g-missing"); !errors.Is(err, impl.ErrGroupNotFound) {
		t.Fatalf("delete of an unknown group = %v, want ErrGroupNotFound", err)
	}
}

func TestGroupLimitPerTenant(t *testing.T) {
	s := impl.NewScimGroupGraphSync(graphLimits())
	for i := 0; i < 12; i++ {
		upsertGroup(t, s, "tenant-1", fmt.Sprintf("g-%02d", i))
	}
	if _, err := s.UpsertGroup("tenant-1", impl.Group{ID: "g-over"}); !errors.Is(err, impl.ErrGroupLimit) {
		t.Fatalf("thirteenth group = %v, want ErrGroupLimit", err)
	}
	upsertGroup(t, s, "tenant-2", "g-other-tenant")
}

func TestSetMembersComputesDiff(t *testing.T) {
	s := impl.NewScimGroupGraphSync(graphLimits())
	upsertGroup(t, s, "tenant-1", "g-eng")
	first, err := s.SetMembers("tenant-1", "g-eng", []impl.Ref{userRef("u-1"), userRef("u-2"), userRef("u-1")})
	if err != nil {
		t.Fatalf("first set members must succeed, got %v", err)
	}
	if len(first.Added) != 2 || len(first.Removed) != 0 || first.Kept != 0 {
		t.Fatalf("first diff = %+v, want 2 added, 0 removed, 0 kept", first)
	}
	if first.Version != 2 {
		t.Fatalf("first diff version = %d, want 2", first.Version)
	}
	second, err := s.SetMembers("tenant-1", "g-eng", []impl.Ref{userRef("u-2"), userRef("u-3")})
	if err != nil {
		t.Fatalf("second set members must succeed, got %v", err)
	}
	if len(second.Added) != 1 || second.Added[0].ID != "u-3" {
		t.Fatalf("second diff added = %+v, want exactly u-3", second.Added)
	}
	if len(second.Removed) != 1 || second.Removed[0].ID != "u-1" {
		t.Fatalf("second diff removed = %+v, want exactly u-1", second.Removed)
	}
	if second.Kept != 1 {
		t.Fatalf("second diff kept = %d, want 1", second.Kept)
	}
	members, err := s.Members("tenant-1", "g-eng")
	if err != nil {
		t.Fatalf("members must be readable, got %v", err)
	}
	if len(members) != 2 || members[0].ID != "u-2" || members[1].ID != "u-3" {
		t.Fatalf("members = %+v, want u-2 then u-3", members)
	}
}

func TestSetMembersRejectsOverCapWithoutPartialWrite(t *testing.T) {
	s := impl.NewScimGroupGraphSync(graphLimits())
	upsertGroup(t, s, "tenant-1", "g-eng")
	if _, err := s.SetMembers("tenant-1", "g-eng", []impl.Ref{userRef("u-keep")}); err != nil {
		t.Fatalf("baseline set members must succeed, got %v", err)
	}
	wide := make([]impl.Ref, 0, 9)
	for i := 0; i < 9; i++ {
		wide = append(wide, userRef(fmt.Sprintf("u-%02d", i)))
	}
	if _, err := s.SetMembers("tenant-1", "g-eng", wide); !errors.Is(err, impl.ErrMemberLimit) {
		t.Fatalf("nine members with an eight member cap = %v, want ErrMemberLimit", err)
	}
	members, err := s.Members("tenant-1", "g-eng")
	if err != nil {
		t.Fatalf("members must be readable, got %v", err)
	}
	if len(members) != 1 || members[0].ID != "u-keep" {
		t.Fatalf("rejected write must keep the old members, got %+v", members)
	}
	if _, err := s.SetMembers("tenant-1", "g-missing", []impl.Ref{userRef("u-1")}); !errors.Is(err, impl.ErrGroupNotFound) {
		t.Fatalf("set members on an unknown group = %v, want ErrGroupNotFound", err)
	}
}

func TestAddMemberValidation(t *testing.T) {
	s := impl.NewScimGroupGraphSync(graphLimits())
	upsertGroup(t, s, "tenant-1", "g-eng")
	if err := s.AddMember("tenant-1", "g-eng", "u-1"); !errors.Is(err, impl.ErrBadRef) {
		t.Fatalf("ref without a kind prefix = %v, want ErrBadRef", err)
	}
	if err := s.AddMember("tenant-1", "g-eng", "mail:ops"); !errors.Is(err, impl.ErrBadRef) {
		t.Fatalf("ref with an unknown kind = %v, want ErrBadRef", err)
	}
	if err := s.AddMember("tenant-1", "g-eng", "user:u-1"); err != nil {
		t.Fatalf("valid ref must be accepted, got %v", err)
	}
	if err := s.AddMember("tenant-1", "g-eng", "user:u-1"); !errors.Is(err, impl.ErrDuplicateMember) {
		t.Fatalf("duplicate member = %v, want ErrDuplicateMember", err)
	}
	if err := s.AddMember("tenant-1", "g-eng", "group:g-missing"); !errors.Is(err, impl.ErrGroupNotFound) {
		t.Fatalf("reference to an unknown group = %v, want ErrGroupNotFound", err)
	}
	if err := s.AddMember("tenant-1", "g-missing", "user:u-1"); !errors.Is(err, impl.ErrGroupNotFound) {
		t.Fatalf("add member on an unknown group = %v, want ErrGroupNotFound", err)
	}
}

func TestRemoveMemberMissing(t *testing.T) {
	s := impl.NewScimGroupGraphSync(graphLimits())
	upsertGroup(t, s, "tenant-1", "g-eng")
	if err := s.AddMember("tenant-1", "g-eng", "user:u-1"); err != nil {
		t.Fatalf("add member must succeed, got %v", err)
	}
	if err := s.AddMember("tenant-1", "g-eng", "user:u-2"); err != nil {
		t.Fatalf("add second member must succeed, got %v", err)
	}
	if err := s.RemoveMember("tenant-1", "g-eng", "user:u-1"); err != nil {
		t.Fatalf("remove member must succeed, got %v", err)
	}
	if err := s.RemoveMember("tenant-1", "g-eng", "user:u-1"); !errors.Is(err, impl.ErrMemberNotFound) {
		t.Fatalf("second remove = %v, want ErrMemberNotFound", err)
	}
	members, err := s.Members("tenant-1", "g-eng")
	if err != nil {
		t.Fatalf("members must be readable, got %v", err)
	}
	if len(members) != 1 || members[0].ID != "u-2" {
		t.Fatalf("members after removal = %+v, want only u-2", members)
	}
}

func TestCycleRejected(t *testing.T) {
	s := impl.NewScimGroupGraphSync(graphLimits())
	upsertGroup(t, s, "tenant-1", "g-parent")
	upsertGroup(t, s, "tenant-1", "g-child")
	if err := s.AddMember("tenant-1", "g-parent", "group:g-child"); err != nil {
		t.Fatalf("nesting a child group must be accepted, got %v", err)
	}
	if err := s.AddMember("tenant-1", "g-child", "group:g-parent"); !errors.Is(err, impl.ErrCycle) {
		t.Fatalf("closing the loop = %v, want ErrCycle", err)
	}
	if err := s.AddMember("tenant-1", "g-parent", "group:g-parent"); !errors.Is(err, impl.ErrCycle) {
		t.Fatalf("group nested into itself = %v, want ErrCycle", err)
	}
}

func TestDepthLimitRejected(t *testing.T) {
	s := impl.NewScimGroupGraphSync(graphLimits())
	upsertGroup(t, s, "tenant-1", "g-1")
	upsertGroup(t, s, "tenant-1", "g-2")
	upsertGroup(t, s, "tenant-1", "g-3")
	upsertGroup(t, s, "tenant-1", "g-4")
	if err := s.AddMember("tenant-1", "g-1", "group:g-2"); err != nil {
		t.Fatalf("first nesting must be accepted, got %v", err)
	}
	if err := s.AddMember("tenant-1", "g-2", "group:g-3"); err != nil {
		t.Fatalf("second nesting must be accepted, got %v", err)
	}
	depth, err := s.Depth("tenant-1", "g-1")
	if err != nil {
		t.Fatalf("depth must succeed, got %v", err)
	}
	if depth != 3 {
		t.Fatalf("depth of g-1 = %d, want 3", depth)
	}
	leaf, err := s.Depth("tenant-1", "g-3")
	if err != nil {
		t.Fatalf("depth must succeed, got %v", err)
	}
	if leaf != 1 {
		t.Fatalf("depth of g-3 = %d, want 1", leaf)
	}
	if err := s.AddMember("tenant-1", "g-3", "group:g-4"); !errors.Is(err, impl.ErrDepthExceeded) {
		t.Fatalf("nesting past the depth cap = %v, want ErrDepthExceeded", err)
	}
}

func TestDeleteGroupUnlinksReferences(t *testing.T) {
	s := impl.NewScimGroupGraphSync(graphLimits())
	upsertGroup(t, s, "tenant-1", "g-parent")
	upsertGroup(t, s, "tenant-1", "g-child")
	if err := s.AddMember("tenant-1", "g-parent", "group:g-child"); err != nil {
		t.Fatalf("nesting must be accepted, got %v", err)
	}
	if err := s.AddMember("tenant-1", "g-parent", "user:u-1"); err != nil {
		t.Fatalf("user membership must be accepted, got %v", err)
	}
	if err := s.DeleteGroup("tenant-1", "g-child"); err != nil {
		t.Fatalf("delete must succeed, got %v", err)
	}
	if _, ok := s.Group("tenant-1", "g-child"); ok {
		t.Fatalf("deleted group must not resolve")
	}
	if _, ok := s.Group("tenant-1", "g-parent"); !ok {
		t.Fatalf("parent group must survive the child delete")
	}
	members, err := s.Members("tenant-1", "g-parent")
	if err != nil {
		t.Fatalf("parent members must be readable, got %v", err)
	}
	if len(members) != 1 || members[0].ID != "u-1" {
		t.Fatalf("parent members after delete = %+v, want only u-1", members)
	}
}

func TestEffectiveMembersExpandsNesting(t *testing.T) {
	s := impl.NewScimGroupGraphSync(graphLimits())
	upsertGroup(t, s, "tenant-1", "g-child")
	upsertGroup(t, s, "tenant-1", "g-parent")
	if err := s.AddMember("tenant-1", "g-child", "user:u-1"); err != nil {
		t.Fatalf("child membership must be accepted, got %v", err)
	}
	if err := s.AddMember("tenant-1", "g-parent", "group:g-child"); err != nil {
		t.Fatalf("nesting must be accepted, got %v", err)
	}
	if err := s.AddMember("tenant-1", "g-parent", "user:u-2"); err != nil {
		t.Fatalf("parent membership must be accepted, got %v", err)
	}
	groups, err := s.EffectiveMembers("tenant-1", "u-1")
	if err != nil {
		t.Fatalf("effective members must succeed, got %v", err)
	}
	if len(groups) != 2 || groups[0] != "g-child" || groups[1] != "g-parent" {
		t.Fatalf("effective groups of u-1 = %v, want [g-child g-parent]", groups)
	}
	groups, err = s.EffectiveMembers("tenant-1", "u-2")
	if err != nil {
		t.Fatalf("effective members must succeed, got %v", err)
	}
	if len(groups) != 1 || groups[0] != "g-parent" {
		t.Fatalf("effective groups of u-2 = %v, want [g-parent]", groups)
	}
}

func TestTenantIsolation(t *testing.T) {
	s := impl.NewScimGroupGraphSync(graphLimits())
	upsertGroup(t, s, "tenant-a", "g-shared")
	upsertGroup(t, s, "tenant-b", "g-shared")
	if err := s.AddMember("tenant-a", "g-shared", "user:u-a"); err != nil {
		t.Fatalf("tenant a membership must be accepted, got %v", err)
	}
	if err := s.AddMember("tenant-b", "g-shared", "user:u-b"); err != nil {
		t.Fatalf("tenant b membership must be accepted, got %v", err)
	}
	membersA, err := s.Members("tenant-a", "g-shared")
	if err != nil {
		t.Fatalf("tenant a members must be readable, got %v", err)
	}
	if len(membersA) != 1 || membersA[0].ID != "u-a" {
		t.Fatalf("tenant a members = %+v, want only u-a", membersA)
	}
	groups, err := s.EffectiveMembers("tenant-a", "u-b")
	if err != nil {
		t.Fatalf("effective members must succeed, got %v", err)
	}
	if len(groups) != 0 {
		t.Fatalf("tenant b user leaked into tenant a: %v", groups)
	}
}

func TestMembersOrderedByTypeThenID(t *testing.T) {
	s := impl.NewScimGroupGraphSync(graphLimits())
	upsertGroup(t, s, "tenant-1", "g-eng")
	upsertGroup(t, s, "tenant-1", "g-nested")
	for _, ref := range []string{"user:u-9", "group:g-nested", "user:u-1"} {
		if err := s.AddMember("tenant-1", "g-eng", ref); err != nil {
			t.Fatalf("add %q must succeed, got %v", ref, err)
		}
	}
	members, err := s.Members("tenant-1", "g-eng")
	if err != nil {
		t.Fatalf("members must be readable, got %v", err)
	}
	if len(members) != 3 {
		t.Fatalf("member count = %d, want 3", len(members))
	}
	want := []string{"group:g-nested", "user:u-1", "user:u-9"}
	for i, ref := range members {
		if ref.Type+":"+ref.ID != want[i] {
			t.Fatalf("member %d = %s:%s, want %s", i, ref.Type, ref.ID, want[i])
		}
	}
}

func TestSnapshotRestoreKeepsGraph(t *testing.T) {
	s := impl.NewScimGroupGraphSync(graphLimits())
	upsertGroup(t, s, "tenant-1", "g-child")
	upsertGroup(t, s, "tenant-1", "g-parent")
	if err := s.AddMember("tenant-1", "g-child", "user:u-1"); err != nil {
		t.Fatalf("child membership must be accepted, got %v", err)
	}
	if err := s.AddMember("tenant-1", "g-parent", "group:g-child"); err != nil {
		t.Fatalf("nesting must be accepted, got %v", err)
	}
	data, err := s.Snapshot()
	if err != nil {
		t.Fatalf("snapshot must succeed, got %v", err)
	}
	restored := impl.NewScimGroupGraphSync(graphLimits())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore must succeed, got %v", err)
	}
	parent, ok := restored.Group("tenant-1", "g-parent")
	if !ok {
		t.Fatalf("restored parent must resolve")
	}
	if parent.Version != 2 {
		t.Fatalf("restored parent version = %d, want 2", parent.Version)
	}
	depth, err := restored.Depth("tenant-1", "g-parent")
	if err != nil {
		t.Fatalf("restored depth must succeed, got %v", err)
	}
	if depth != 2 {
		t.Fatalf("restored depth = %d, want 2", depth)
	}
	groups, err := restored.EffectiveMembers("tenant-1", "u-1")
	if err != nil {
		t.Fatalf("restored effective members must succeed, got %v", err)
	}
	if len(groups) != 2 || groups[0] != "g-child" || groups[1] != "g-parent" {
		t.Fatalf("restored effective groups = %v, want [g-child g-parent]", groups)
	}
	if err := restored.Restore([]byte("][ broken")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt restore = %v, want ErrBadSnapshot", err)
	}
	if _, ok := restored.Group("tenant-1", "g-parent"); !ok {
		t.Fatalf("rejected restore must not wipe the live graph")
	}
}

func TestConcurrentAddMemberKeepsAllRefs(t *testing.T) {
	s := impl.NewScimGroupGraphSync(impl.Limits{
		MaxGroupsPerTenant: 4,
		MaxMembersPerGroup: 32,
		MaxDepth:           3,
	})
	upsertGroup(t, s, "tenant-1", "g-eng")
	const workers = 20
	var wg sync.WaitGroup
	var mu sync.Mutex
	failures := 0
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			defer wg.Done()
			if err := s.AddMember("tenant-1", "g-eng", fmt.Sprintf("user:u-%02d", i)); err != nil {
				mu.Lock()
				failures++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if failures != 0 {
		t.Fatalf("%d concurrent member writes failed", failures)
	}
	members, err := s.Members("tenant-1", "g-eng")
	if err != nil {
		t.Fatalf("members must be readable, got %v", err)
	}
	if len(members) != workers {
		t.Fatalf("member count = %d, want %d", len(members), workers)
	}
	seen := map[string]bool{}
	for _, ref := range members {
		if seen[ref.ID] {
			t.Fatalf("member %q stored twice", ref.ID)
		}
		seen[ref.ID] = true
	}
}
