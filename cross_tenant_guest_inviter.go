// Package task runs cross tenant guest invitations: invite, accept, role
// mapping and seat accounting for one collaboration service.
package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Invitation states.
const (
	InvitationPending  = "pending"
	InvitationWaiting  = "waiting"
	InvitationRevoked  = "revoked"
	InvitationAccepted = "accepted"
)

// Member states.
const (
	MemberActive = "active"
)

// Roles that can be handed to a cross tenant guest.
const (
	RoleGuest  = "guest"
	RoleViewer = "viewer"
	RoleEditor = "editor"
	RoleMember = "member"
)

// HomeExternal marks a guest whose home tenant has not been recorded yet.
const HomeExternal = "external"

// Errors returned by the inviter.
var (
	ErrBlankField        = errors.New("tenant, email, role, actor or reason is required")
	ErrBadRole           = errors.New("role cannot be granted to a cross tenant guest")
	ErrUnknownInvitation = errors.New("unknown invitation")
	ErrUnknownMember     = errors.New("unknown member")
	ErrBlankIdentity     = errors.New("identity id is required")
	ErrAlreadyAccepted   = errors.New("invitation was already accepted")
	ErrRevoked           = errors.New("invitation was revoked")
	ErrInvitationLimit   = errors.New("pending invitation limit reached for this tenant")
	ErrBadSnapshot       = errors.New("snapshot payload is invalid")
)

// Invitation is one pending or accepted guest invitation.
type Invitation struct {
	ID     string `json:"id"`
	Tenant string `json:"tenant"`
	Email  string `json:"email"`
	Role   string `json:"role"`
	State  string `json:"state"`
	Seq    int64  `json:"seq"`
}

// Member is one guest that accepted an invitation.
type Member struct {
	ID         string `json:"id"`
	Tenant     string `json:"tenant"`
	Email      string `json:"email"`
	IdentityID string `json:"identity_id"`
	HomeTenant string `json:"home_tenant"`
	Role       string `json:"role"`
	State      string `json:"state"`
	Seq        int64  `json:"seq"`
}

// Limits bounds the inviter tables.
type Limits struct {
	MaxPendingInvitations int
	MaxSeatsPerTenant     int
}

// CrossTenantGuestInviter owns the invitation table and the member table.
type CrossTenantGuestInviter struct {
	limits Limits
}

// NewCrossTenantGuestInviter builds an inviter with the given caps.
func NewCrossTenantGuestInviter(limits Limits) *CrossTenantGuestInviter {
	return &CrossTenantGuestInviter{limits: limits}
}

// Invite creates or merges one invitation for an email address.
func (g *CrossTenantGuestInviter) Invite(fromTenant, email, role string) (Invitation, error) {
	return Invitation{}, errNotImplemented
}

// Accept turns a pending invitation into a member of the inviting tenant.
func (g *CrossTenantGuestInviter) Accept(invitationID, identityID string) (Member, error) {
	return Member{}, errNotImplemented
}

// Revoke cancels an invitation that was never accepted.
func (g *CrossTenantGuestInviter) Revoke(invitationID, actorID string) error {
	return errNotImplemented
}

// AssignRole changes the role of an accepted guest.
func (g *CrossTenantGuestInviter) AssignRole(memberID, role string) error {
	return errNotImplemented
}

// Remove takes a guest out of the tenant and frees the seat.
func (g *CrossTenantGuestInviter) Remove(memberID, actorID, reason string) error {
	return errNotImplemented
}

// Members lists the guests of one tenant ordered by member id.
func (g *CrossTenantGuestInviter) Members(tenantID string) []Member {
	return nil
}

// Invitations lists the invitations of one tenant ordered by sequence.
func (g *CrossTenantGuestInviter) Invitations(tenantID string) []Invitation {
	return nil
}

// ChangeHomeTenant records the real home tenant of a guest and remaps the role.
func (g *CrossTenantGuestInviter) ChangeHomeTenant(memberID, newHomeTenant string) error {
	return errNotImplemented
}

// SeatsUsed reports how many seats of one tenant are taken.
func (g *CrossTenantGuestInviter) SeatsUsed(tenantID string) int {
	return 0
}

// Snapshot serialises invitations, members and the sequence watermark.
func (g *CrossTenantGuestInviter) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// LoadSnapshot replaces the whole state with a snapshot, all or nothing.
func (g *CrossTenantGuestInviter) LoadSnapshot(data []byte) error {
	return errNotImplemented
}
