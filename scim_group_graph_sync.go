// Package task keeps the tenant SCIM group graph consistent while groups,
// members and nested groups arrive from an upstream directory.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Member reference kinds.
const (
	RefUser  = "user"
	RefGroup = "group"
)

// snapshotSchema tags the serialised payload format.
const snapshotSchema = "scim-group-graph-snapshot/v1"

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

// Limits bounds the graph tables. A limit of zero (or less) means
// "not capped" for that particular bound.
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
//
// A single RWMutex serialises all writes and lets readers observe either the
// old or the new complete state, never half an edge. All graph validation is
// performed against a candidate state before any committed map is touched.
type ScimGroupGraphSync struct {
	mu      sync.RWMutex
	limits  Limits
	tenants map[string]*tenantState
}

// tenantState groups all records that belong to one tenant.
type tenantState struct {
	groups map[string]*groupState
}

// groupState is the mutable stored record. The members set is the single
// source of truth for both direct membership and nesting edges.
type groupState struct {
	id          string
	displayName string
	members     map[Ref]struct{}
	version     int64
	lastDiff    *Diff
}

// NewScimGroupGraphSync builds a graph store with the given caps.
func NewScimGroupGraphSync(limits Limits) *ScimGroupGraphSync {
	return &ScimGroupGraphSync{
		limits:  limits,
		tenants: make(map[string]*tenantState),
	}
}

// UpsertGroup creates or updates one group record.
func (s *ScimGroupGraphSync) UpsertGroup(tenantID string, g Group) (Group, error) {
	if strings.TrimSpace(tenantID) == "" {
		return Group{}, ErrBlankTenant
	}
	if strings.TrimSpace(g.ID) == "" {
		return Group{}, ErrBlankGroupID
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.tenants == nil {
		s.tenants = make(map[string]*tenantState)
	}
	tenant := s.tenants[tenantID]
	if tenant == nil {
		tenant = &tenantState{groups: make(map[string]*groupState)}
		s.tenants[tenantID] = tenant
	}

	if current, ok := tenant.groups[g.ID]; ok {
		// Scalar fields are editable through upsert; membership and the
		// diff/version watermarks are owned by the member operations and
		// must survive a directory re-sync of the group record.
		current.displayName = g.DisplayName
		current.version++
		return groupView(current), nil
	}

	if s.limits.MaxGroupsPerTenant > 0 && len(tenant.groups) >= s.limits.MaxGroupsPerTenant {
		return Group{}, ErrGroupLimit
	}

	created := &groupState{
		id:          g.ID,
		displayName: g.DisplayName,
		members:     make(map[Ref]struct{}),
		version:     1,
	}
	tenant.groups[g.ID] = created
	return groupView(created), nil
}

// Group reads one group record.
func (s *ScimGroupGraphSync) Group(tenantID, groupID string) (Group, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	g, ok := s.groupLocked(tenantID, groupID)
	if !ok {
		return Group{}, false
	}
	return groupView(g), true
}

// DeleteGroup removes one group and unlinks it from its parents.
func (s *ScimGroupGraphSync) DeleteGroup(tenantID, groupID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tenant, ok := s.tenants[tenantID]
	if !ok {
		return ErrGroupNotFound
	}
	if _, ok := tenant.groups[groupID]; !ok {
		return ErrGroupNotFound
	}

	delete(tenant.groups, groupID)

	// Remove every incoming group:<id> edge in the same tenant. Referenced
	// child/parent groups are never deleted by this operation.
	stale := Ref{Type: RefGroup, ID: groupID}
	for _, g := range tenant.groups {
		if _, linked := g.members[stale]; linked {
			delete(g.members, stale)
			g.version++
			g.lastDiff = &Diff{
				GroupID: g.id,
				Removed: []Ref{stale},
				Version: g.version,
			}
		}
	}
	return nil
}

// SetMembers replaces the direct members of one group as a set.
func (s *ScimGroupGraphSync) SetMembers(tenantID, groupID string, refs []Ref) (Diff, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tenant, g, err := s.requireGroup(tenantID, groupID)
	if err != nil {
		return Diff{}, err
	}

	next := make(map[Ref]struct{}, len(refs))
	for _, ref := range refs {
		if !validRef(ref) {
			return Diff{}, ErrBadRef
		}
		next[ref] = struct{}{}
	}

	if err := checkGroupRefs(tenant, next); err != nil {
		return Diff{}, err
	}
	if s.limits.MaxMembersPerGroup > 0 && len(next) > s.limits.MaxMembersPerGroup {
		return Diff{}, ErrMemberLimit
	}
	if err := validateNesting(tenant, groupID, next, s.limits.MaxDepth); err != nil {
		return Diff{}, err
	}

	added, removed, kept := diffMemberSets(g.members, next)
	g.members = next
	g.version++

	diff := Diff{
		GroupID: groupID,
		Added:   added,
		Removed: removed,
		Kept:    kept,
		Version: g.version,
	}
	g.lastDiff = &diff
	return diff, nil
}

// AddMember appends one member written as "type:id".
func (s *ScimGroupGraphSync) AddMember(tenantID, groupID, refText string) error {
	ref, err := parseRef(refText)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tenant, g, err := s.requireGroup(tenantID, groupID)
	if err != nil {
		return err
	}

	if _, exists := g.members[ref]; exists {
		return ErrDuplicateMember
	}
	if ref.Type == RefGroup {
		if _, ok := tenant.groups[ref.ID]; !ok {
			return ErrGroupNotFound
		}
	}

	next := cloneRefSet(g.members)
	next[ref] = struct{}{}

	if s.limits.MaxMembersPerGroup > 0 && len(next) > s.limits.MaxMembersPerGroup {
		return ErrMemberLimit
	}
	if err := validateNesting(tenant, groupID, next, s.limits.MaxDepth); err != nil {
		return err
	}

	kept := len(g.members)
	g.members = next
	g.version++
	g.lastDiff = &Diff{
		GroupID: groupID,
		Added:   []Ref{ref},
		Kept:    kept,
		Version: g.version,
	}
	return nil
}

// RemoveMember drops one member written as "type:id".
func (s *ScimGroupGraphSync) RemoveMember(tenantID, groupID, refText string) error {
	ref, err := parseRef(refText)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	_, g, err := s.requireGroup(tenantID, groupID)
	if err != nil {
		return err
	}

	if _, exists := g.members[ref]; !exists {
		return ErrMemberNotFound
	}

	delete(g.members, ref)
	g.version++
	g.lastDiff = &Diff{
		GroupID: groupID,
		Removed: []Ref{ref},
		Version: g.version,
	}
	return nil
}

// Members lists the direct members of one group, ordered by type then id.
func (s *ScimGroupGraphSync) Members(tenantID, groupID string) ([]Ref, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	_, g, err := s.requireGroup(tenantID, groupID)
	if err != nil {
		return nil, err
	}
	return sortedRefs(g.members), nil
}

// EffectiveMembers lists the groups a user belongs to directly or through
// nested groups, ordered by group id.
func (s *ScimGroupGraphSync) EffectiveMembers(tenantID, userID string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tenant, ok := s.tenants[tenantID]
	if !ok {
		return []string{}, nil
	}

	userMember := Ref{Type: RefUser, ID: userID}

	// Reverse nesting index: child group -> parent groups referencing it.
	parentsOf := make(map[string][]string)
	matched := make(map[string]struct{})
	queue := make([]string, 0)

	for parentID, g := range tenant.groups {
		for ref := range g.members {
			if ref.Type == RefGroup {
				parentsOf[ref.ID] = append(parentsOf[ref.ID], parentID)
			}
		}
		if _, direct := g.members[userMember]; direct {
			if _, seen := matched[parentID]; !seen {
				matched[parentID] = struct{}{}
				queue = append(queue, parentID)
			}
		}
	}

	// Expand upwards through every ancestor. The visited set deduplicates
	// diamond paths and makes the walk robust even against weird graphs.
	for len(queue) > 0 {
		childID := queue[0]
		queue = queue[1:]
		for _, parentID := range parentsOf[childID] {
			if _, seen := matched[parentID]; seen {
				continue
			}
			matched[parentID] = struct{}{}
			queue = append(queue, parentID)
		}
	}

	out := make([]string, 0, len(matched))
	for groupID := range matched {
		out = append(out, groupID)
	}
	sort.Strings(out)
	return out, nil
}

// Depth reports how deep the nesting below one group goes. A group without
// nested groups is a leaf with depth 1.
func (s *ScimGroupGraphSync) Depth(tenantID, groupID string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tenant, ok := s.tenants[tenantID]
	if !ok {
		return 0, ErrGroupNotFound
	}
	if _, ok := tenant.groups[groupID]; !ok {
		return 0, ErrGroupNotFound
	}

	children := buildChildren(tenant, "", nil)
	depths, _ := computeDepths(children)
	return depths[groupID], nil
}

// snapshotFile is the serialised whole-store representation.
type snapshotFile struct {
	Schema  string                    `json:"schema"`
	Tenants map[string]snapshotTenant `json:"tenants"`
}

type snapshotTenant struct {
	Groups map[string]snapshotGroup `json:"groups"`
}

type snapshotGroup struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Members     []Ref  `json:"members"`
	Version     int64  `json:"version"`
	LastDiff    *Diff  `json:"last_diff,omitempty"`
}

// Snapshot serialises the whole graph with its versions and diff watermark.
func (s *ScimGroupGraphSync) Snapshot() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	file := snapshotFile{
		Schema:  snapshotSchema,
		Tenants: make(map[string]snapshotTenant, len(s.tenants)),
	}
	for tenantID, tenant := range s.tenants {
		st := snapshotTenant{Groups: make(map[string]snapshotGroup, len(tenant.groups))}
		for groupID, g := range tenant.groups {
			st.Groups[groupID] = snapshotGroup{
				ID:          g.id,
				DisplayName: g.displayName,
				Members:     sortedRefs(g.members),
				Version:     g.version,
				LastDiff:    g.lastDiff,
			}
		}
		file.Tenants[tenantID] = st
	}
	return json.Marshal(&file)
}

// Restore replaces the whole graph with a snapshot, all or nothing.
func (s *ScimGroupGraphSync) Restore(data []byte) error {
	var file snapshotFile
	if err := json.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}

	tenants, err := decodeSnapshot(&file)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrBadSnapshot, err)
	}

	s.mu.Lock()
	s.tenants = tenants
	s.mu.Unlock()
	return nil
}

// ---------------------------------------------------------------------------
// Small helpers (graph access assumes the caller serialises access).
// ---------------------------------------------------------------------------

func (s *ScimGroupGraphSync) groupLocked(tenantID, groupID string) (*groupState, bool) {
	tenant, ok := s.tenants[tenantID]
	if !ok {
		return nil, false
	}
	g, ok := tenant.groups[groupID]
	return g, ok
}

func (s *ScimGroupGraphSync) requireGroup(tenantID, groupID string) (*tenantState, *groupState, error) {
	tenant, ok := s.tenants[tenantID]
	if !ok {
		return nil, nil, ErrGroupNotFound
	}
	g, ok := tenant.groups[groupID]
	if !ok {
		return nil, nil, ErrGroupNotFound
	}
	return tenant, g, nil
}

func groupView(g *groupState) Group {
	return Group{
		ID:          g.id,
		DisplayName: g.displayName,
		Members:     sortedRefs(g.members),
		Version:     g.version,
	}
}

// parseRef parses the canonical "type:id" member reference form. Only the
// user: and group: prefixes with a non-empty id are accepted.
func parseRef(text string) (Ref, error) {
	colon := strings.IndexByte(text, ':')
	if colon <= 0 {
		return Ref{}, ErrBadRef
	}
	ref := Ref{Type: text[:colon], ID: text[colon+1:]}
	if (ref.Type != RefUser && ref.Type != RefGroup) || strings.TrimSpace(ref.ID) == "" {
		return Ref{}, ErrBadRef
	}
	return ref, nil
}

func validRef(ref Ref) bool {
	if ref.Type != RefUser && ref.Type != RefGroup {
		return false
	}
	return strings.TrimSpace(ref.ID) != ""
}

func cloneRefSet(in map[Ref]struct{}) map[Ref]struct{} {
	out := make(map[Ref]struct{}, len(in)+1)
	for ref := range in {
		out[ref] = struct{}{}
	}
	return out
}

func sortRefs(refs []Ref) {
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Type != refs[j].Type {
			return refs[i].Type < refs[j].Type
		}
		return refs[i].ID < refs[j].ID
	})
}

func sortedRefs(set map[Ref]struct{}) []Ref {
	out := make([]Ref, 0, len(set))
	for ref := range set {
		out = append(out, ref)
	}
	sortRefs(out)
	return out
}

// diffMemberSets compares old/new direct-member sets and returns
// audit-ready, deterministically ordered added/removed slices plus the
// number of kept (unchanged) references.
func diffMemberSets(old, next map[Ref]struct{}) (added []Ref, removed []Ref, kept int) {
	added = make([]Ref, 0)
	removed = make([]Ref, 0)
	for ref := range next {
		if _, ok := old[ref]; ok {
			kept++
		} else {
			added = append(added, ref)
		}
	}
	for ref := range old {
		if _, ok := next[ref]; !ok {
			removed = append(removed, ref)
		}
	}
	sortRefs(added)
	sortRefs(removed)
	return added, removed, kept
}

func checkGroupRefs(tenant *tenantState, members map[Ref]struct{}) error {
	for ref := range members {
		if ref.Type == RefGroup {
			if _, ok := tenant.groups[ref.ID]; !ok {
				return ErrGroupNotFound
			}
		}
	}
	return nil
}

// buildChildren builds the parent -> nested child ids adjacency. When
// overrideID is non-empty, overrideEdges replaces that group's committed
// member set so a candidate write can be validated before it lands.
func buildChildren(tenant *tenantState, overrideID string, overrideEdges map[Ref]struct{}) map[string][]string {
	children := make(map[string][]string, len(tenant.groups))
	for id, g := range tenant.groups {
		members := g.members
		if id == overrideID {
			members = overrideEdges
		}
		for ref := range members {
			if ref.Type == RefGroup {
				children[id] = append(children[id], ref.ID)
			}
		}
	}
	return children
}

// validateNesting rejects candidate edges that close a cycle or create a
// chain longer than maxDepth. It never mutates committed state.
func validateNesting(tenant *tenantState, overrideID string, overrideEdges map[Ref]struct{}, maxDepth int) error {
	children := buildChildren(tenant, overrideID, overrideEdges)

	if cycle := findCyclePath(children); cycle != nil {
		return fmt.Errorf("%w: %s", ErrCycle, strings.Join(cycle, " -> "))
	}

	if maxDepth > 0 {
		depths, nextChild := computeDepths(children)
		worstID := ""
		worstDepth := 0
		for _, id := range sortedGroupIDs(tenant.groups) {
			if depths[id] > worstDepth {
				worstDepth = depths[id]
				worstID = id
			}
		}
		if worstDepth > maxDepth {
			path := []string{worstID}
			current := worstID
			for nextChild[current] != "" {
				current = nextChild[current]
				path = append(path, current)
			}
			return fmt.Errorf("%w: path %s is depth %d, limit %d",
				ErrDepthExceeded, strings.Join(path, " -> "), worstDepth, maxDepth)
		}
	}
	return nil
}

func sortedGroupIDs(groups map[string]*groupState) []string {
	ids := make([]string, 0, len(groups))
	for id := range groups {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// findCyclePath returns one representative closed path (repeating the first
// node at the end) or nil when the adjacency is acyclic.
func findCyclePath(children map[string][]string) []string {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int, len(children))
	stack := make([]string, 0)

	var visit func(string) []string
	visit = func(node string) []string {
		color[node] = gray
		stack = append(stack, node)

		kids := append([]string(nil), children[node]...)
		sort.Strings(kids)
		for _, child := range kids {
			switch color[child] {
			case gray:
				start := 0
				for i, n := range stack {
					if n == child {
						start = i
						break
					}
				}
				return append(append([]string(nil), stack[start:]...), child)
			case white:
				if path := visit(child); path != nil {
					return path
				}
			}
		}

		stack = stack[:len(stack)-1]
		color[node] = black
		return nil
	}

	nodes := make([]string, 0, len(children))
	for node := range children {
		nodes = append(nodes, node)
	}
	sort.Strings(nodes)
	for _, node := range nodes {
		if color[node] == white {
			if path := visit(node); path != nil {
				return path
			}
		}
	}
	return nil
}

// computeDepths returns the longest leaf-counted nesting depth of every node
// and, for each node, the child used by that longest path. The graph must be
// acyclic (see findCyclePath).
func computeDepths(children map[string][]string) (map[string]int, map[string]string) {
	depth := make(map[string]int, len(children))
	nextChild := make(map[string]string, len(children))

	var visit func(string) int
	visit = func(node string) int {
		if d, ok := depth[node]; ok {
			return d
		}
		best := 1
		bestChild := ""

		kids := append([]string(nil), children[node]...)
		sort.Strings(kids)
		for _, child := range kids {
			candidate := visit(child) + 1
			if candidate > best || (candidate == best && (bestChild == "" || child < bestChild)) {
				best = candidate
				bestChild = child
			}
		}

		depth[node] = best
		nextChild[node] = bestChild
		return best
	}

	for node := range children {
		visit(node)
	}
	return depth, nextChild
}

// decodeSnapshot rebuilds and validates a whole tenant graph before Restore
// swaps it in. Any invariant violation is reported as a bad snapshot.
func decodeSnapshot(file *snapshotFile) (map[string]*tenantState, error) {
	tenants := make(map[string]*tenantState, len(file.Tenants))

	for tenantID, st := range file.Tenants {
		if strings.TrimSpace(tenantID) == "" {
			return nil, errors.New("snapshot has a blank tenant id")
		}
		tenant := &tenantState{groups: make(map[string]*groupState, len(st.Groups))}

		for groupID, sg := range st.Groups {
			if strings.TrimSpace(groupID) == "" {
				return nil, errors.New("snapshot has a blank group id")
			}
			members := make(map[Ref]struct{}, len(sg.Members))
			for _, ref := range sg.Members {
				if !validRef(ref) {
					return nil, fmt.Errorf("group %q contains an invalid member reference", groupID)
				}
				members[ref] = struct{}{}
			}
			version := sg.Version
			if version < 1 {
				version = 1
			}
			tenant.groups[groupID] = &groupState{
				id:          groupID,
				displayName: sg.DisplayName,
				members:     members,
				version:     version,
				lastDiff:    sg.LastDiff,
			}
		}

		for _, g := range tenant.groups {
			if err := checkGroupRefs(tenant, g.members); err != nil {
				return nil, fmt.Errorf("group %q: %w", g.id, err)
			}
		}
		if cycle := findCyclePath(buildChildren(tenant, "", nil)); cycle != nil {
			return nil, fmt.Errorf("nesting cycle: %s", strings.Join(cycle, " -> "))
		}

		tenants[tenantID] = tenant
	}
	return tenants, nil
}
