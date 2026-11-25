// Package task keeps the tenant SCIM group graph consistent while groups,
// members and nested groups arrive from an upstream directory.
package task

import (
	"errors"
)

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Member reference kinds.
const (
	RefUser  = "user"
	RefGroup = "group"
)

// Errors returned by the group graph.
var (
	ErrBlankTenant     = errors.New("tenant id is required")
	ErrBlankGroupID    = errors.New("group id is required")
	ErrBadRef          = errors.New("member reference is invalid")
	ErrGroupNotFound   = errors.New("group not found")
	ErrGroupLimit      = errors.New("group limit reached for tenant")
	ErrMemberLimit     = errors.New("member limit reached for group")
	ErrDuplicateMember = errors.New("member already present")
	ErrMemberNotFound  = errors.New("member not present")
	ErrCycle           = errors.New("group nesting would create a cycle")
	ErrDepthExceeded   = errors.New("group nesting exceeds the depth limit")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// Limits bounds the graph tables.
type Limits struct {
	MaxGroupsPerTenant int
	MaxMembersPerGroup int
	MaxDepth           int
}

// Ref points at one user or one group.
type Ref struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// Group is one SCIM group with its direct members.
type Group struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Members     []Ref  `json:"members"`
	Version     int64  `json:"version"`
}

// Diff reports what one member replacement changed.
type Diff struct {
	GroupID string `json:"group_id"`
	Added   []Ref  `json:"added"`
	Removed []Ref  `json:"removed"`
	Kept    int    `json:"kept"`
	Version int64  `json:"version"`
}

// ScimGroupGraphSync owns the group graph of every tenant.
type ScimGroupGraphSync struct {
	limits Limits
}

// NewScimGroupGraphSync builds a graph store with the given caps.
func NewScimGroupGraphSync(limits Limits) *ScimGroupGraphSync {
	return &ScimGroupGraphSync{limits: limits}
}

// UpsertGroup creates or updates one group record.
func (s *ScimGroupGraphSync) UpsertGroup(tenantID string, g Group) (Group, error) {
	return Group{}, errNotImplemented
}

// Group reads one group record.
func (s *ScimGroupGraphSync) Group(tenantID, groupID string) (Group, bool) {
	return Group{}, false
}

// DeleteGroup removes one group and unlinks it from its parents.
func (s *ScimGroupGraphSync) DeleteGroup(tenantID, groupID string) error {
	return errNotImplemented
}

// SetMembers replaces the direct members of one group as a set.
func (s *ScimGroupGraphSync) SetMembers(tenantID, groupID string, refs []Ref) (Diff, error) {
	return Diff{}, errNotImplemented
}

// AddMember appends one member written as "type:id".
func (s *ScimGroupGraphSync) AddMember(tenantID, groupID, ref string) error {
	return errNotImplemented
}

// RemoveMember drops one member written as "type:id".
func (s *ScimGroupGraphSync) RemoveMember(tenantID, groupID, ref string) error {
	return errNotImplemented
}

// Members lists the direct members of one group, ordered by type then id.
func (s *ScimGroupGraphSync) Members(tenantID, groupID string) ([]Ref, error) {
	return nil, errNotImplemented
}

// EffectiveMembers lists the groups a user belongs to directly or through
// nested groups, ordered by group id.
func (s *ScimGroupGraphSync) EffectiveMembers(tenantID, userID string) ([]string, error) {
	return nil, errNotImplemented
}

// Depth reports how deep the nesting below one group goes.
func (s *ScimGroupGraphSync) Depth(tenantID, groupID string) (int, error) {
	return 0, errNotImplemented
}

// Snapshot serialises the whole graph with its versions.
func (s *ScimGroupGraphSync) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole graph with a snapshot, all or nothing.
func (s *ScimGroupGraphSync) Restore(data []byte) error {
	return errNotImplemented
}
