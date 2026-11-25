package task

import (
	"errors"
	"testing"

	impl "gsb/batch49-go-safety-a-28"
)

func guestLimits() impl.Limits {
	return impl.Limits{MaxPendingInvitations: 5, MaxSeatsPerTenant: 10}
}

func TestInviteCreatesPendingInvitation(t *testing.T) {
	g := impl.NewCrossTenantGuestInviter(guestLimits())
	inv, err := g.Invite("tenant-a", "  Alice@Example.COM ", impl.RoleEditor)
	if err != nil {
		t.Fatalf("invite must succeed, got %v", err)
	}
	if inv.ID == "" {
		t.Fatalf("invitation id must not be empty")
	}
	if inv.Email != "alice@example.com" {
		t.Fatalf("invitation email = %q, want the trimmed lower case form", inv.Email)
	}
	if inv.Tenant != "tenant-a" || inv.Role != impl.RoleEditor {
		t.Fatalf("invitation tenant=%q role=%q, want tenant-a/editor", inv.Tenant, inv.Role)
	}
	if inv.State != impl.InvitationPending {
		t.Fatalf("invitation state = %q, want %q", inv.State, impl.InvitationPending)
	}
	if inv.Seq <= 0 {
		t.Fatalf("invitation seq = %d, want a positive number", inv.Seq)
	}
	if got := g.SeatsUsed("tenant-a"); got != 1 {
		t.Fatalf("seats used = %d, want 1", got)
	}
	list := g.Invitations("tenant-a")
	if len(list) != 1 || list[0].ID != inv.ID {
		t.Fatalf("invitations = %+v, want exactly the new invitation", list)
	}
}

func TestInviteRejectsBlankFields(t *testing.T) {
	g := impl.NewCrossTenantGuestInviter(guestLimits())
	if _, err := g.Invite("   ", "alice@example.com", impl.RoleGuest); !errors.Is(err, impl.ErrBlankField) {
		t.Fatalf("blank tenant must fail with ErrBlankField, got %v", err)
	}
	if _, err := g.Invite("tenant-a", "  ", impl.RoleGuest); !errors.Is(err, impl.ErrBlankField) {
		t.Fatalf("blank email must fail with ErrBlankField, got %v", err)
	}
	if _, err := g.Invite("tenant-a", "alice@example.com", ""); !errors.Is(err, impl.ErrBlankField) {
		t.Fatalf("blank role must fail with ErrBlankField, got %v", err)
	}
	if got := g.SeatsUsed("tenant-a"); got != 0 {
		t.Fatalf("rejected invitations must not take seats, got %d", got)
	}
}

func TestInviteRejectsUnknownRole(t *testing.T) {
	g := impl.NewCrossTenantGuestInviter(guestLimits())
	if _, err := g.Invite("tenant-a", "alice@example.com", "admin"); !errors.Is(err, impl.ErrBadRole) {
		t.Fatalf("admin must fail with ErrBadRole, got %v", err)
	}
	if _, err := g.Invite("tenant-a", "alice@example.com", impl.RoleMember); !errors.Is(err, impl.ErrBadRole) {
		t.Fatalf("internal member role must fail with ErrBadRole, got %v", err)
	}
	if got := len(g.Invitations("tenant-a")); got != 0 {
		t.Fatalf("rejected roles must not create invitations, got %d", got)
	}
}

func TestInviteDeduplicatesSameEmail(t *testing.T) {
	g := impl.NewCrossTenantGuestInviter(guestLimits())
	first, err := g.Invite("tenant-a", "alice@example.com", impl.RoleEditor)
	if err != nil {
		t.Fatalf("first invite: %v", err)
	}
	second, err := g.Invite("tenant-a", "ALICE@example.com ", impl.RoleViewer)
	if err != nil {
		t.Fatalf("second invite: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("duplicate invite id = %q, want the merged invitation %q", second.ID, first.ID)
	}
	if second.Role != impl.RoleEditor {
		t.Fatalf("merged invitation role = %q, want the first role editor", second.Role)
	}
	if got := g.SeatsUsed("tenant-a"); got != 1 {
		t.Fatalf("seats used = %d, want 1 for one address", got)
	}
	if got := len(g.Invitations("tenant-a")); got != 1 {
		t.Fatalf("invitations = %d, want 1 for one address", got)
	}
}

func TestInviteEnforcesPendingLimit(t *testing.T) {
	g := impl.NewCrossTenantGuestInviter(impl.Limits{MaxPendingInvitations: 2, MaxSeatsPerTenant: 10})
	for _, email := range []string{"a@example.com", "b@example.com"} {
		if _, err := g.Invite("tenant-b", email, impl.RoleGuest); err != nil {
			t.Fatalf("invite %s: %v", email, err)
		}
	}
	_, err := g.Invite("tenant-b", "c@example.com", impl.RoleGuest)
	if !errors.Is(err, impl.ErrInvitationLimit) {
		t.Fatalf("third pending invitation must fail with ErrInvitationLimit, got %v", err)
	}
	if got := len(g.Invitations("tenant-b")); got != 2 {
		t.Fatalf("invitations = %d, want the cap 2", got)
	}
	if _, err := g.Invite("tenant-c", "c@example.com", impl.RoleGuest); err != nil {
		t.Fatalf("another tenant must not be affected by the cap, got %v", err)
	}
}

func TestAcceptRequiresKnownInvitation(t *testing.T) {
	g := impl.NewCrossTenantGuestInviter(guestLimits())
	if _, err := g.Accept("inv-404", "identity-1"); !errors.Is(err, impl.ErrUnknownInvitation) {
		t.Fatalf("unknown invitation must fail with ErrUnknownInvitation, got %v", err)
	}
	if _, err := g.Accept("", "identity-1"); !errors.Is(err, impl.ErrUnknownInvitation) {
		t.Fatalf("empty invitation id must fail with ErrUnknownInvitation, got %v", err)
	}
}

func TestAcceptRejectsBlankIdentity(t *testing.T) {
	g := impl.NewCrossTenantGuestInviter(guestLimits())
	inv, err := g.Invite("tenant-d", "dave@example.com", impl.RoleGuest)
	if err != nil {
		t.Fatalf("invite: %v", err)
	}
	if _, err := g.Accept(inv.ID, "   "); !errors.Is(err, impl.ErrBlankIdentity) {
		t.Fatalf("blank identity must fail with ErrBlankIdentity, got %v", err)
	}
	if got := len(g.Members("tenant-d")); got != 0 {
		t.Fatalf("rejected accept must not create members, got %d", got)
	}
}

func TestAcceptAddsMemberWithInvitedRole(t *testing.T) {
	g := impl.NewCrossTenantGuestInviter(guestLimits())
	inv, err := g.Invite("tenant-e", "erin@example.com", impl.RoleEditor)
	if err != nil {
		t.Fatalf("invite: %v", err)
	}
	m, err := g.Accept(inv.ID, "identity-erin")
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if m.ID == "" {
		t.Fatalf("member id must not be empty")
	}
	if m.Tenant != "tenant-e" || m.Role != impl.RoleEditor {
		t.Fatalf("member tenant=%q role=%q, want tenant-e/editor", m.Tenant, m.Role)
	}
	if m.Email != "erin@example.com" || m.IdentityID != "identity-erin" {
		t.Fatalf("member email=%q identity=%q, want the invited address and identity", m.Email, m.IdentityID)
	}
	if m.HomeTenant != impl.HomeExternal {
		t.Fatalf("member home tenant = %q, want %q before it is recorded", m.HomeTenant, impl.HomeExternal)
	}
	if m.State != impl.MemberActive {
		t.Fatalf("member state = %q, want %q", m.State, impl.MemberActive)
	}
	if m.Seq <= 0 {
		t.Fatalf("member seq = %d, want a positive number", m.Seq)
	}
	members := g.Members("tenant-e")
	if len(members) != 1 || members[0].ID != m.ID {
		t.Fatalf("members = %+v, want exactly the accepted member", members)
	}
	if got := g.SeatsUsed("tenant-e"); got != 1 {
		t.Fatalf("seats used after accept = %d, want 1", got)
	}
	list := g.Invitations("tenant-e")
	if len(list) != 1 || list[0].State != impl.InvitationAccepted {
		t.Fatalf("invitations = %+v, want the accepted invitation", list)
	}
}

func TestAcceptTwiceRejected(t *testing.T) {
	g := impl.NewCrossTenantGuestInviter(guestLimits())
	inv, err := g.Invite("tenant-f", "frank@example.com", impl.RoleViewer)
	if err != nil {
		t.Fatalf("invite: %v", err)
	}
	if _, err := g.Accept(inv.ID, "identity-frank"); err != nil {
		t.Fatalf("first accept: %v", err)
	}
	if _, err := g.Accept(inv.ID, "identity-frank"); !errors.Is(err, impl.ErrAlreadyAccepted) {
		t.Fatalf("second accept must fail with ErrAlreadyAccepted, got %v", err)
	}
	if got := len(g.Members("tenant-f")); got != 1 {
		t.Fatalf("members = %d, want 1 after the duplicate accept", got)
	}
}

func TestRevokePendingInvitationFreesSeat(t *testing.T) {
	g := impl.NewCrossTenantGuestInviter(guestLimits())
	inv, err := g.Invite("tenant-g", "grace@example.com", impl.RoleGuest)
	if err != nil {
		t.Fatalf("invite: %v", err)
	}
	if err := g.Revoke(inv.ID, "  "); !errors.Is(err, impl.ErrBlankField) {
		t.Fatalf("revoke without an actor must fail with ErrBlankField, got %v", err)
	}
	if got := g.SeatsUsed("tenant-g"); got != 1 {
		t.Fatalf("rejected revoke must keep the seat, got %d", got)
	}
	if err := g.Revoke(inv.ID, "ops-1"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if got := g.SeatsUsed("tenant-g"); got != 0 {
		t.Fatalf("seats used after revoke = %d, want 0", got)
	}
	list := g.Invitations("tenant-g")
	if len(list) != 1 || list[0].State != impl.InvitationRevoked {
		t.Fatalf("invitations = %+v, want the revoked invitation", list)
	}
	if _, err := g.Accept(inv.ID, "identity-grace"); !errors.Is(err, impl.ErrRevoked) {
		t.Fatalf("accepting a revoked invitation must fail with ErrRevoked, got %v", err)
	}
	if err := g.Revoke("inv-404", "ops-1"); !errors.Is(err, impl.ErrUnknownInvitation) {
		t.Fatalf("revoking an unknown invitation must fail with ErrUnknownInvitation, got %v", err)
	}
}

func TestAssignRoleUpdatesMember(t *testing.T) {
	g := impl.NewCrossTenantGuestInviter(guestLimits())
	inv, err := g.Invite("tenant-h", "henry@example.com", impl.RoleGuest)
	if err != nil {
		t.Fatalf("invite: %v", err)
	}
	m, err := g.Accept(inv.ID, "identity-henry")
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if err := g.AssignRole(m.ID, impl.RoleViewer); err != nil {
		t.Fatalf("assign role: %v", err)
	}
	members := g.Members("tenant-h")
	if len(members) != 1 || members[0].Role != impl.RoleViewer {
		t.Fatalf("members = %+v, want role viewer", members)
	}
	if err := g.AssignRole(m.ID, "admin"); !errors.Is(err, impl.ErrBadRole) {
		t.Fatalf("admin role must fail with ErrBadRole, got %v", err)
	}
	if err := g.AssignRole("member-404", impl.RoleGuest); !errors.Is(err, impl.ErrUnknownMember) {
		t.Fatalf("unknown member must fail with ErrUnknownMember, got %v", err)
	}
}

func TestRemoveRequiresReasonAndActor(t *testing.T) {
	g := impl.NewCrossTenantGuestInviter(guestLimits())
	inv, err := g.Invite("tenant-i", "iris@example.com", impl.RoleEditor)
	if err != nil {
		t.Fatalf("invite: %v", err)
	}
	m, err := g.Accept(inv.ID, "identity-iris")
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if err := g.Remove(m.ID, "", "offboard"); !errors.Is(err, impl.ErrBlankField) {
		t.Fatalf("remove without an actor must fail with ErrBlankField, got %v", err)
	}
	if err := g.Remove(m.ID, "ops-2", "   "); !errors.Is(err, impl.ErrBlankField) {
		t.Fatalf("remove without a reason must fail with ErrBlankField, got %v", err)
	}
	if got := len(g.Members("tenant-i")); got != 1 {
		t.Fatalf("rejected removals must keep the member, got %d", got)
	}
	if err := g.Remove(m.ID, "ops-2", "offboard"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if got := len(g.Members("tenant-i")); got != 0 {
		t.Fatalf("members after removal = %d, want 0", got)
	}
	if got := g.SeatsUsed("tenant-i"); got != 0 {
		t.Fatalf("seats used after removal = %d, want 0", got)
	}
	if err := g.Remove(m.ID, "ops-2", "offboard"); !errors.Is(err, impl.ErrUnknownMember) {
		t.Fatalf("repeated removal must fail with ErrUnknownMember, got %v", err)
	}
}

func TestMembersSortedByID(t *testing.T) {
	g := impl.NewCrossTenantGuestInviter(guestLimits())
	ids := make([]string, 0, 2)
	for _, email := range []string{"zoe@example.com", "amy@example.com"} {
		inv, err := g.Invite("tenant-j", email, impl.RoleGuest)
		if err != nil {
			t.Fatalf("invite %s: %v", email, err)
		}
		m, err := g.Accept(inv.ID, "identity-"+email)
		if err != nil {
			t.Fatalf("accept %s: %v", email, err)
		}
		ids = append(ids, m.ID)
	}
	members := g.Members("tenant-j")
	if len(members) != 2 {
		t.Fatalf("members = %d, want 2", len(members))
	}
	if members[0].ID > members[1].ID {
		t.Fatalf("members are not sorted by id: %q then %q", members[0].ID, members[1].ID)
	}
	again := g.Members("tenant-j")
	if len(again) != 2 || again[0].ID != members[0].ID || again[1].ID != members[1].ID {
		t.Fatalf("member order is not stable across calls: %+v then %+v", members, again)
	}
}

func TestChangeHomeTenantRemapsGuestRole(t *testing.T) {
	g := impl.NewCrossTenantGuestInviter(guestLimits())
	inv, err := g.Invite("tenant-k", "kate@example.com", impl.RoleGuest)
	if err != nil {
		t.Fatalf("invite: %v", err)
	}
	m, err := g.Accept(inv.ID, "identity-kate")
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if err := g.ChangeHomeTenant(m.ID, "tenant-k"); err != nil {
		t.Fatalf("change home tenant into the tenant: %v", err)
	}
	members := g.Members("tenant-k")
	if len(members) != 1 {
		t.Fatalf("members = %d, want 1", len(members))
	}
	if members[0].Role != impl.RoleMember {
		t.Fatalf("role after the home tenant became internal = %q, want %q", members[0].Role, impl.RoleMember)
	}
	if members[0].HomeTenant != "tenant-k" {
		t.Fatalf("home tenant = %q, want tenant-k", members[0].HomeTenant)
	}
	if err := g.ChangeHomeTenant(m.ID, "tenant-z"); err != nil {
		t.Fatalf("change home tenant back out: %v", err)
	}
	members = g.Members("tenant-k")
	if len(members) != 1 || members[0].Role != impl.RoleGuest {
		t.Fatalf("role after leaving the tenant = %q, want %q", members[0].Role, impl.RoleGuest)
	}
	if err := g.ChangeHomeTenant("member-404", "tenant-k"); !errors.Is(err, impl.ErrUnknownMember) {
		t.Fatalf("unknown member must fail with ErrUnknownMember, got %v", err)
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	limits := impl.Limits{MaxPendingInvitations: 5, MaxSeatsPerTenant: 10}
	g := impl.NewCrossTenantGuestInviter(limits)
	accepted, err := g.Invite("tenant-m", "mia@example.com", impl.RoleEditor)
	if err != nil {
		t.Fatalf("invite accepted: %v", err)
	}
	m, err := g.Accept(accepted.ID, "identity-mia")
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	pending, err := g.Invite("tenant-m", "noah@example.com", impl.RoleGuest)
	if err != nil {
		t.Fatalf("invite pending: %v", err)
	}
	if got := g.SeatsUsed("tenant-m"); got != 2 {
		t.Fatalf("seats used = %d, want 2", got)
	}
	if err := g.LoadSnapshot([]byte("not json at all")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := len(g.Members("tenant-m")); got != 1 {
		t.Fatalf("state after a rejected snapshot changed, members = %d", got)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewCrossTenantGuestInviter(limits)
	if err := restored.LoadSnapshot(data); err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	members := restored.Members("tenant-m")
	if len(members) != 1 || members[0].ID != m.ID || members[0].Role != impl.RoleEditor {
		t.Fatalf("restored members = %+v, want the accepted member", members)
	}
	list := restored.Invitations("tenant-m")
	if len(list) != 2 {
		t.Fatalf("restored invitations = %d, want 2", len(list))
	}
	if restored.SeatsUsed("tenant-m") != 2 {
		t.Fatalf("restored seats = %d, want 2", restored.SeatsUsed("tenant-m"))
	}
	next, err := restored.Invite("tenant-m", "owen@example.com", impl.RoleGuest)
	if err != nil {
		t.Fatalf("invite after restore: %v", err)
	}
	if next.Seq <= pending.Seq || next.Seq <= accepted.Seq {
		t.Fatalf("seq after restore = %d, must continue past %d and %d", next.Seq, pending.Seq, accepted.Seq)
	}
}
