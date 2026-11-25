// Package task runs cross tenant guest invitations: invite, accept, role
// mapping and seat accounting for one collaboration service.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

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

const snapshotFormatVersion = 1

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

type snapshotData struct {
	FormatVersion int               `json:"format_version"`
	Seq           int64             `json:"seq"`
	Invitations   []Invitation      `json:"invitations"`
	Members       []Member          `json:"members"`
	Identities    map[string]string `json:"identities"`
}

// CrossTenantGuestInviter owns the invitation table and the member table.
type CrossTenantGuestInviter struct {
	mu                sync.Mutex
	limits            Limits
	seq               int64
	invitations       map[string]Invitation
	members           map[string]Member
	invitationByEmail map[string]string
	identityByEmail   map[string]string
}

// NewCrossTenantGuestInviter builds an inviter with the given caps.
func NewCrossTenantGuestInviter(limits Limits) *CrossTenantGuestInviter {
	g := &CrossTenantGuestInviter{limits: limits}
	g.initLocked()
	return g
}

func (g *CrossTenantGuestInviter) initLocked() {
	if g.invitations == nil {
		g.invitations = make(map[string]Invitation)
	}
	if g.members == nil {
		g.members = make(map[string]Member)
	}
	if g.invitationByEmail == nil {
		g.invitationByEmail = make(map[string]string)
	}
	if g.identityByEmail == nil {
		g.identityByEmail = make(map[string]string)
	}
}

func blank(value string) bool {
	return strings.TrimSpace(value) == ""
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func invitationEmailKey(tenant, email string) string {
	return tenant + "\x00" + email
}

func grantableRole(role string) bool {
	switch role {
	case RoleGuest, RoleViewer, RoleEditor:
		return true
	default:
		return false
	}
}

func validInvitationState(state string) bool {
	switch state {
	case InvitationPending, InvitationWaiting, InvitationRevoked, InvitationAccepted:
		return true
	default:
		return false
	}
}

func (g *CrossTenantGuestInviter) outstandingInvitationsLocked(tenant string) int {
	count := 0
	for _, invitation := range g.invitations {
		if invitation.Tenant == tenant &&
			(invitation.State == InvitationPending || invitation.State == InvitationWaiting) {
			count++
		}
	}
	return count
}

func (g *CrossTenantGuestInviter) seatsUsedLocked(tenant string) int {
	used := 0
	for _, invitation := range g.invitations {
		if invitation.Tenant == tenant && invitation.State == InvitationPending {
			used++
		}
	}
	for _, member := range g.members {
		if member.Tenant == tenant && member.State == MemberActive {
			used++
		}
	}
	return used
}

func (g *CrossTenantGuestInviter) promoteWaitingLocked(tenant string) {
	waiting := make([]Invitation, 0)
	for _, invitation := range g.invitations {
		if invitation.Tenant == tenant && invitation.State == InvitationWaiting {
			waiting = append(waiting, invitation)
		}
	}
	sort.Slice(waiting, func(i, j int) bool {
		if waiting[i].Seq == waiting[j].Seq {
			return waiting[i].ID < waiting[j].ID
		}
		return waiting[i].Seq < waiting[j].Seq
	})

	for _, invitation := range waiting {
		if g.limits.MaxSeatsPerTenant > 0 &&
			g.seatsUsedLocked(tenant) >= g.limits.MaxSeatsPerTenant {
			return
		}
		invitation.State = InvitationPending
		g.invitations[invitation.ID] = invitation
	}
}

// Invite creates or merges one invitation for an email address.
func (g *CrossTenantGuestInviter) Invite(fromTenant, email, role string) (Invitation, error) {
	tenant := strings.TrimSpace(fromTenant)
	email = normalizeEmail(email)
	role = strings.TrimSpace(role)

	if tenant == "" || email == "" || role == "" {
		return Invitation{}, ErrBlankField
	}
	if !grantableRole(role) {
		return Invitation{}, ErrBadRole
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()

	key := invitationEmailKey(tenant, email)
	if id, ok := g.invitationByEmail[key]; ok {
		if invitation, exists := g.invitations[id]; exists {
			return invitation, nil
		}
		delete(g.invitationByEmail, key)
	}

	if g.limits.MaxPendingInvitations > 0 &&
		g.outstandingInvitationsLocked(tenant) >= g.limits.MaxPendingInvitations {
		return Invitation{}, ErrInvitationLimit
	}

	state := InvitationPending
	if g.limits.MaxSeatsPerTenant > 0 &&
		g.seatsUsedLocked(tenant) >= g.limits.MaxSeatsPerTenant {
		state = InvitationWaiting
	}

	g.seq++
	invitation := Invitation{
		ID:     fmt.Sprintf("inv-%d", g.seq),
		Tenant: tenant,
		Email:  email,
		Role:   role,
		State:  state,
		Seq:    g.seq,
	}
	g.invitations[invitation.ID] = invitation
	g.invitationByEmail[key] = invitation.ID
	return invitation, nil
}

// Accept turns a pending invitation into a member of the inviting tenant.
func (g *CrossTenantGuestInviter) Accept(invitationID, identityID string) (Member, error) {
	invitationID = strings.TrimSpace(invitationID)
	identityID = strings.TrimSpace(identityID)

	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()

	invitation, ok := g.invitations[invitationID]
	if !ok {
		return Member{}, ErrUnknownInvitation
	}
	if identityID == "" {
		return Member{}, ErrBlankIdentity
	}
	if invitation.State == InvitationAccepted {
		return Member{}, ErrAlreadyAccepted
	}
	if invitation.State == InvitationRevoked {
		return Member{}, ErrRevoked
	}
	if invitation.State != InvitationPending && invitation.State != InvitationWaiting {
		return Member{}, fmt.Errorf("%w: invalid invitation state", ErrBadSnapshot)
	}
	if invitation.State == InvitationWaiting &&
		g.limits.MaxSeatsPerTenant > 0 &&
		g.seatsUsedLocked(invitation.Tenant) >= g.limits.MaxSeatsPerTenant {
		return Member{}, fmt.Errorf("%w: no seat is available", ErrInvitationLimit)
	}

	resolvedIdentity := identityID
	if existingIdentity, ok := g.identityByEmail[invitation.Email]; ok && existingIdentity != "" {
		resolvedIdentity = existingIdentity
	}

	g.seq++
	member := Member{
		ID:         fmt.Sprintf("member-%d", g.seq),
		Tenant:     invitation.Tenant,
		Email:      invitation.Email,
		IdentityID: resolvedIdentity,
		HomeTenant: HomeExternal,
		Role:       invitation.Role,
		State:      MemberActive,
		Seq:        g.seq,
	}
	g.members[member.ID] = member
	g.identityByEmail[member.Email] = resolvedIdentity

	invitation.State = InvitationAccepted
	g.invitations[invitation.ID] = invitation
	g.promoteWaitingLocked(invitation.Tenant)

	return member, nil
}

// Revoke cancels an invitation that was never accepted.
func (g *CrossTenantGuestInviter) Revoke(invitationID, actorID string) error {
	if blank(actorID) {
		return ErrBlankField
	}
	invitationID = strings.TrimSpace(invitationID)

	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()

	invitation, ok := g.invitations[invitationID]
	if !ok {
		return ErrUnknownInvitation
	}
	if invitation.State == InvitationPending || invitation.State == InvitationWaiting {
		invitation.State = InvitationRevoked
		g.invitations[invitation.ID] = invitation
		g.promoteWaitingLocked(invitation.Tenant)
	}
	return nil
}

// AssignRole changes the role of an accepted guest.
func (g *CrossTenantGuestInviter) AssignRole(memberID, role string) error {
	role = strings.TrimSpace(role)
	if role == "" {
		return ErrBlankField
	}
	if !grantableRole(role) {
		return ErrBadRole
	}
	memberID = strings.TrimSpace(memberID)

	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()

	member, ok := g.members[memberID]
	if !ok {
		return ErrUnknownMember
	}
	member.Role = role
	g.members[member.ID] = member
	return nil
}

// Remove takes a guest out of the tenant and frees the seat.
func (g *CrossTenantGuestInviter) Remove(memberID, actorID, reason string) error {
	if blank(actorID) || blank(reason) {
		return ErrBlankField
	}
	memberID = strings.TrimSpace(memberID)

	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()

	member, ok := g.members[memberID]
	if !ok {
		return ErrUnknownMember
	}
	delete(g.members, member.ID)
	g.promoteWaitingLocked(member.Tenant)
	return nil
}

// Members lists the guests of one tenant ordered by member id.
func (g *CrossTenantGuestInviter) Members(tenantID string) []Member {
	g.mu.Lock()
	defer g.mu.Unlock()

	members := make([]Member, 0)
	for _, member := range g.members {
		if member.Tenant == tenantID {
			members = append(members, member)
		}
	}
	sort.Slice(members, func(i, j int) bool {
		return members[i].ID < members[j].ID
	})
	return members
}

// Invitations lists the invitations of one tenant ordered by sequence.
func (g *CrossTenantGuestInviter) Invitations(tenantID string) []Invitation {
	g.mu.Lock()
	defer g.mu.Unlock()

	invitations := make([]Invitation, 0)
	for _, invitation := range g.invitations {
		if invitation.Tenant == tenantID {
			invitations = append(invitations, invitation)
		}
	}
	sort.Slice(invitations, func(i, j int) bool {
		if invitations[i].Seq == invitations[j].Seq {
			return invitations[i].ID < invitations[j].ID
		}
		return invitations[i].Seq < invitations[j].Seq
	})
	return invitations
}

// ChangeHomeTenant records the real home tenant of a guest and remaps the role.
func (g *CrossTenantGuestInviter) ChangeHomeTenant(memberID, newHomeTenant string) error {
	memberID = strings.TrimSpace(memberID)
	newHomeTenant = strings.TrimSpace(newHomeTenant)
	if newHomeTenant == "" {
		return ErrBlankField
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()

	member, ok := g.members[memberID]
	if !ok {
		return ErrUnknownMember
	}
	member.HomeTenant = newHomeTenant
	if newHomeTenant == member.Tenant {
		member.Role = RoleMember
	} else {
		member.Role = RoleGuest
	}
	g.members[member.ID] = member
	return nil
}

// SeatsUsed reports how many seats of one tenant are taken.
func (g *CrossTenantGuestInviter) SeatsUsed(tenantID string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.seatsUsedLocked(tenantID)
}

// Snapshot serialises invitations, members and the sequence watermark.
func (g *CrossTenantGuestInviter) Snapshot() ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()

	data := snapshotData{
		FormatVersion: snapshotFormatVersion,
		Seq:           g.seq,
		Invitations:   make([]Invitation, 0, len(g.invitations)),
		Members:       make([]Member, 0, len(g.members)),
		Identities:    make(map[string]string, len(g.identityByEmail)),
	}
	for _, invitation := range g.invitations {
		data.Invitations = append(data.Invitations, invitation)
	}
	for _, member := range g.members {
		data.Members = append(data.Members, member)
	}
	for email, identityID := range g.identityByEmail {
		data.Identities[email] = identityID
	}

	return json.Marshal(data)
}

// LoadSnapshot replaces the whole state with a snapshot, all or nothing.
func (g *CrossTenantGuestInviter) LoadSnapshot(data []byte) error {
	if len(strings.TrimSpace(string(data))) == 0 {
		return ErrBadSnapshot
	}

	var snapshot snapshotData
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}

	next, err := snapshotStateFromData(snapshot, g.limits)
	if err != nil {
		return err
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.seq = next.seq
	g.invitations = next.invitations
	g.members = next.members
	g.invitationByEmail = next.invitationByEmail
	g.identityByEmail = next.identityByEmail
	return nil
}

func snapshotStateFromData(data snapshotData, limits Limits) (*CrossTenantGuestInviter, error) {
	if data.FormatVersion != snapshotFormatVersion {
		return nil, fmt.Errorf("%w: unsupported format version", ErrBadSnapshot)
	}
	if data.Seq < 0 {
		return nil, fmt.Errorf("%w: negative sequence", ErrBadSnapshot)
	}

	state := &CrossTenantGuestInviter{limits: limits}
	state.initLocked()
	state.seq = data.Seq

	tenants := make(map[string]struct{})
	invitationIDs := make(map[string]struct{})
	invitationKeys := make(map[string]struct{})
	memberIDs := make(map[string]struct{})
	memberKeys := make(map[string]struct{})

	for _, invitation := range data.Invitations {
		invitation.ID = strings.TrimSpace(invitation.ID)
		invitation.Tenant = strings.TrimSpace(invitation.Tenant)
		invitation.Email = normalizeEmail(invitation.Email)
		invitation.Role = strings.TrimSpace(invitation.Role)
		invitation.State = strings.TrimSpace(invitation.State)

		if invitation.ID == "" || invitation.Tenant == "" || invitation.Email == "" ||
			invitation.Role == "" || invitation.State == "" || invitation.Seq <= 0 {
			return nil, fmt.Errorf("%w: incomplete invitation", ErrBadSnapshot)
		}
		if !grantableRole(invitation.Role) || !validInvitationState(invitation.State) {
			return nil, fmt.Errorf("%w: invalid invitation role or state", ErrBadSnapshot)
		}
		if _, exists := invitationIDs[invitation.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate invitation id", ErrBadSnapshot)
		}
		key := invitationEmailKey(invitation.Tenant, invitation.Email)
		if _, exists := invitationKeys[key]; exists {
			return nil, fmt.Errorf("%w: duplicate invitation email", ErrBadSnapshot)
		}

		invitationIDs[invitation.ID] = struct{}{}
		invitationKeys[key] = struct{}{}
		state.invitations[invitation.ID] = invitation
		state.invitationByEmail[key] = invitation.ID
		tenants[invitation.Tenant] = struct{}{}
		if invitation.Seq > state.seq {
			state.seq = invitation.Seq
		}
	}

	for _, member := range data.Members {
		member.ID = strings.TrimSpace(member.ID)
		member.Tenant = strings.TrimSpace(member.Tenant)
		member.Email = normalizeEmail(member.Email)
		member.IdentityID = strings.TrimSpace(member.IdentityID)
		member.HomeTenant = strings.TrimSpace(member.HomeTenant)
		member.Role = strings.TrimSpace(member.Role)
		member.State = strings.TrimSpace(member.State)

		if member.ID == "" || member.Tenant == "" || member.Email == "" ||
			member.IdentityID == "" || member.Role == "" || member.State == "" || member.Seq <= 0 {
			return nil, fmt.Errorf("%w: incomplete member", ErrBadSnapshot)
		}
		switch member.Role {
		case RoleGuest, RoleViewer, RoleEditor, RoleMember:
		default:
			return nil, fmt.Errorf("%w: invalid member role", ErrBadSnapshot)
		}
		if member.State != MemberActive {
			return nil, fmt.Errorf("%w: invalid member state", ErrBadSnapshot)
		}
		if _, exists := memberIDs[member.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate member id", ErrBadSnapshot)
		}
		key := invitationEmailKey(member.Tenant, member.Email)
		if _, exists := memberKeys[key]; exists {
			return nil, fmt.Errorf("%w: duplicate member email", ErrBadSnapshot)
		}

		memberIDs[member.ID] = struct{}{}
		memberKeys[key] = struct{}{}
		if member.HomeTenant == "" {
			member.HomeTenant = HomeExternal
		}
		state.members[member.ID] = member
		tenants[member.Tenant] = struct{}{}
		if member.Seq > state.seq {
			state.seq = member.Seq
		}
	}

	for email, identityID := range data.Identities {
		email = normalizeEmail(email)
		identityID = strings.TrimSpace(identityID)
		if email == "" || identityID == "" {
			return nil, fmt.Errorf("%w: invalid identity index", ErrBadSnapshot)
		}
		state.identityByEmail[email] = identityID
	}

	for _, member := range state.members {
		identityID, known := state.identityByEmail[member.Email]
		if !known {
			state.identityByEmail[member.Email] = member.IdentityID
			continue
		}
		if identityID != member.IdentityID {
			return nil, fmt.Errorf("%w: identity does not match email index", ErrBadSnapshot)
		}
	}

	for tenant := range tenants {
		if limits.MaxPendingInvitations > 0 &&
			state.outstandingInvitationsLocked(tenant) > limits.MaxPendingInvitations {
			return nil, fmt.Errorf("%w: invitation limit exceeded", ErrBadSnapshot)
		}
		used := state.seatsUsedLocked(tenant)
		if limits.MaxSeatsPerTenant > 0 && used > limits.MaxSeatsPerTenant {
			return nil, fmt.Errorf("%w: seat limit exceeded", ErrBadSnapshot)
		}
		if limits.MaxSeatsPerTenant > 0 {
			for _, invitation := range state.invitations {
				if invitation.Tenant == tenant && invitation.State == InvitationWaiting &&
					used < limits.MaxSeatsPerTenant {
					return nil, fmt.Errorf("%w: waiting queue is not next in seat order", ErrBadSnapshot)
				}
			}
		}
	}

	return state, nil
}
