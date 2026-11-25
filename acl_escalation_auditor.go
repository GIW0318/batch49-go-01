// Package task audits file access control entries for privilege escalation
// paths and computes the smallest repair that removes them.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Access is a set of permission bits used for file and directory access.
type Access uint8

// Permission bits.
const (
	AccessNone    Access = 0
	AccessRead    Access = 1 << 0
	AccessWrite   Access = 1 << 1
	AccessExecute Access = 1 << 2
	AccessFull    Access = AccessRead | AccessWrite | AccessExecute
)

// AceKind identifies the class of an access control entry.
type AceKind string

// Entry classes. The default_* classes describe entries inherited by children
// that are created later, they do not affect the directory itself.
const (
	AceUser         AceKind = "user"
	AceGroup        AceKind = "group"
	AceMask         AceKind = "mask"
	AceOther        AceKind = "other"
	AceDefaultUser  AceKind = "default_user"
	AceDefaultGroup AceKind = "default_group"
	AceDefaultOther AceKind = "default_other"
)

// Severity ranks a finding.
type Severity string

// Finding severities.
const (
	SeverityCritical Severity = "critical"
	SeverityHigh     Severity = "high"
	SeverityMedium   Severity = "medium"
)

// Reason codes attached to a finding.
const (
	ReasonDirectWrite   = "direct_write"
	ReasonGroupWrite    = "group_write"
	ReasonOtherWrite    = "other_write"
	ReasonAncestorWrite = "ancestor_write"
	ReasonDefaultWrite  = "default_write"
)

// Ace is one access control entry. ID is a uid, a gid or zero for other and
// mask entries.
type Ace struct {
	Kind AceKind `json:"kind"`
	ID   uint32  `json:"id"`
	Bits Access  `json:"bits"`
}

// Node is one file or directory in the audited tree.
type Node struct {
	Path           string `json:"path"`
	Owner          uint32 `json:"owner"`
	Group          uint32 `json:"group"`
	OwnerBits      Access `json:"owner_bits"`
	GroupBits      Access `json:"group_bits"`
	OtherBits      Access `json:"other_bits"`
	SetUID         bool   `json:"setuid"`
	SetGID         bool   `json:"setgid"`
	Entries        []Ace  `json:"entries"`
	DefaultEntries []Ace  `json:"default_entries"`
}

// Principal is an account whose effective access is evaluated.
type Principal struct {
	ID         string   `json:"id"`
	UID        uint32   `json:"uid"`
	GID        uint32   `json:"gid"`
	Groups     []uint32 `json:"groups"`
	Privileged bool     `json:"privileged"`
}

// Finding is one escalation path attributed to one principal.
type Finding struct {
	ID          string   `json:"id"`
	PrincipalID string   `json:"principal_id"`
	Path        string   `json:"path"`
	Reason      string   `json:"reason"`
	Severity    Severity `json:"severity"`
	ViaGroup    uint32   `json:"via_group"`
	Chain       []string `json:"chain"`
	Seq         int64    `json:"seq"`
}

// AceChange removes bits from one entry of one node.
type AceChange struct {
	Path   string  `json:"path"`
	Kind   AceKind `json:"kind"`
	ID     uint32  `json:"id"`
	Remove Access  `json:"remove"`
}

// RepairPlan is an all or nothing set of entry changes bound to one state.
type RepairPlan struct {
	Changes []AceChange `json:"changes"`
	Digest  string      `json:"digest"`
}

// ApplyResult reports what a successful repair did.
type ApplyResult struct {
	Applied   int   `json:"applied"`
	Remaining int   `json:"remaining"`
	Seq       int64 `json:"seq"`
}

// Limits bounds the live tables of the auditor.
type Limits struct {
	MaxPrincipals int
	MaxNodes      int
	MaxFindings   int
}

// Errors returned by the auditor.
var (
	ErrBlankPrincipal     = errors.New("principal id is required")
	ErrDuplicatePrincipal = errors.New("principal already registered")
	ErrUnknownPrincipal   = errors.New("unknown principal")
	ErrPathInvalid        = errors.New("node path must be absolute and clean")
	ErrDuplicateNode      = errors.New("node already registered")
	ErrNodeNotFound       = errors.New("node not found")
	ErrLimitExceeded      = errors.New("limit exceeded")
	ErrPlanStale          = errors.New("repair plan no longer matches current state")
	ErrApplyRejected      = errors.New("repair plan cannot be applied")
	ErrResidualEscalation = errors.New("escalation paths remain after repair")
	ErrBadSnapshot        = errors.New("snapshot payload is invalid")
	ErrApplyBusy          = errors.New("another repair is being applied")
)

// AclEscalationAuditor owns the principal table, the node tree and the last
// audit result.
type AclEscalationAuditor struct {
	mu         sync.RWMutex
	limits     Limits
	principals map[string]Principal
	nodes      map[string]Node
	seq        int64
	findings   []Finding
}

// NewAclEscalationAuditor builds an auditor with the given caps.
func NewAclEscalationAuditor(limits Limits) *AclEscalationAuditor {
	return &AclEscalationAuditor{
		limits:     limits,
		principals: make(map[string]Principal),
		nodes:      make(map[string]Node),
	}
}

// RegisterPrincipal adds an account to the evaluated set.
func (a *AclEscalationAuditor) RegisterPrincipal(p Principal) error {
	id := strings.TrimSpace(p.ID)
	if id == "" {
		return ErrBlankPrincipal
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if _, ok := a.principals[id]; ok {
		return ErrDuplicatePrincipal
	}
	if a.limits.MaxPrincipals > 0 && len(a.principals) >= a.limits.MaxPrincipals {
		return ErrLimitExceeded
	}

	p.ID = id
	a.principals[id] = copyPrincipal(p)
	a.seq++
	return nil
}

// AddNode registers one file or directory.
func (a *AclEscalationAuditor) AddNode(n Node) error {
	if !validNodePath(n.Path) {
		return ErrPathInvalid
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if _, ok := a.nodes[n.Path]; ok {
		return ErrDuplicateNode
	}
	if a.limits.MaxNodes > 0 && len(a.nodes) >= a.limits.MaxNodes {
		return ErrLimitExceeded
	}

	a.nodes[n.Path] = copyNode(n)
	a.seq++
	return nil
}

// Node returns a copy of one registered node.
func (a *AclEscalationAuditor) Node(p string) (Node, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	n, ok := a.nodes[p]
	if !ok {
		return Node{}, false
	}
	return copyNode(n), true
}

// EffectiveAccess resolves the permission bits a principal holds on a path.
func (a *AclEscalationAuditor) EffectiveAccess(principalID, nodePath string) (Access, error) {
	id := strings.TrimSpace(principalID)

	a.mu.RLock()
	defer a.mu.RUnlock()

	p, ok := a.principals[id]
	if !ok {
		return AccessNone, ErrUnknownPrincipal
	}
	n, ok := a.nodes[nodePath]
	if !ok {
		return AccessNone, ErrNodeNotFound
	}
	return effectiveAccess(n, p), nil
}

// Scan evaluates the tree and records findings for one principal, or for every
// non privileged principal when principalID is empty.
func (a *AclEscalationAuditor) Scan(principalID string) ([]Finding, error) {
	id := strings.TrimSpace(principalID)

	a.mu.Lock()
	defer a.mu.Unlock()

	var principals []Principal
	if id == "" {
		for _, p := range a.principals {
			if !p.Privileged {
				principals = append(principals, copyPrincipal(p))
			}
		}
		sort.Slice(principals, func(i, j int) bool { return principals[i].ID < principals[j].ID })
	} else {
		p, ok := a.principals[id]
		if !ok {
			return nil, ErrUnknownPrincipal
		}
		principals = []Principal{copyPrincipal(p)}
	}

	findings := computeFindings(principals, a.nodes, a.seq)
	if a.limits.MaxFindings > 0 && len(findings) > a.limits.MaxFindings {
		return nil, ErrLimitExceeded
	}
	a.findings = copyFindings(findings)
	return copyFindings(findings), nil
}

// Findings returns the findings recorded by the most recent scan.
func (a *AclEscalationAuditor) Findings() []Finding {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return copyFindings(a.findings)
}

// PlanRepair builds the smallest repair that clears every recorded finding.
func (a *AclEscalationAuditor) PlanRepair() (RepairPlan, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	changes := make(map[string]AceChange)
	for _, f := range a.findings {
		p, ok := a.principals[f.PrincipalID]
		if !ok || p.Privileged {
			continue
		}
		n, ok := a.nodes[f.Path]
		if !ok || p.UID == n.Owner {
			continue
		}
		for _, c := range collectWriteChanges(n, p) {
			changes[changeKey(c)] = c
		}
	}

	result := make([]AceChange, 0, len(changes))
	for _, c := range changes {
		result = append(result, c)
	}
	sortChanges(result)

	digest, err := stateDigest(a.principals, a.nodes, a.seq)
	if err != nil {
		return RepairPlan{}, err
	}
	return RepairPlan{Changes: result, Digest: digest}, nil
}

// Apply validates and applies a repair plan atomically.
func (a *AclEscalationAuditor) Apply(plan RepairPlan) (ApplyResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	digest, err := stateDigest(a.principals, a.nodes, a.seq)
	if err != nil {
		return ApplyResult{}, err
	}
	if plan.Digest == "" || digest != plan.Digest {
		return ApplyResult{}, ErrPlanStale
	}

	next := cloneNodes(a.nodes)
	applied := 0
	for _, c := range plan.Changes {
		if c.Remove != AccessWrite {
			return ApplyResult{Seq: a.seq}, ErrApplyRejected
		}
		n, ok := next[c.Path]
		if !ok {
			return ApplyResult{Seq: a.seq}, ErrApplyRejected
		}
		updated, changed := applyAceChange(n, c)
		if !changed {
			return ApplyResult{Seq: a.seq}, ErrApplyRejected
		}
		next[c.Path] = updated
		applied++
	}

	var principals []Principal
	for _, p := range a.principals {
		if !p.Privileged {
			principals = append(principals, copyPrincipal(p))
		}
	}
	sort.Slice(principals, func(i, j int) bool { return principals[i].ID < principals[j].ID })

	residual := computeFindings(principals, next, a.seq)
	if len(residual) > 0 {
		return ApplyResult{Applied: applied, Remaining: len(residual), Seq: a.seq}, ErrResidualEscalation
	}

	a.nodes = next
	a.findings = nil
	a.seq++
	return ApplyResult{Applied: applied, Remaining: 0, Seq: a.seq}, nil
}

// Verify rescans every principal and reports whether escalations remain.
func (a *AclEscalationAuditor) Verify() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	var principals []Principal
	for _, p := range a.principals {
		if !p.Privileged {
			principals = append(principals, copyPrincipal(p))
		}
	}
	sort.Slice(principals, func(i, j int) bool { return principals[i].ID < principals[j].ID })

	findings := computeFindings(principals, a.nodes, a.seq)
	a.findings = copyFindings(findings)
	if len(findings) != 0 {
		return ErrResidualEscalation
	}
	return nil
}

const snapshotVersion = 1

type snapshotState struct {
	Version    int         `json:"version"`
	Seq        int64       `json:"seq"`
	Principals []Principal `json:"principals"`
	Nodes      []Node      `json:"nodes"`
}

// Snapshot serialises principals, nodes and the sequence counter.
func (a *AclEscalationAuditor) Snapshot() ([]byte, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	s := snapshotState{Version: snapshotVersion, Seq: a.seq}
	for _, p := range a.principals {
		s.Principals = append(s.Principals, copyPrincipal(p))
	}
	for _, n := range a.nodes {
		s.Nodes = append(s.Nodes, copyNode(n))
	}
	sort.Slice(s.Principals, func(i, j int) bool { return s.Principals[i].ID < s.Principals[j].ID })
	sort.Slice(s.Nodes, func(i, j int) bool { return s.Nodes[i].Path < s.Nodes[j].Path })

	return json.Marshal(s)
}

// Restore replaces the whole state from a snapshot.
func (a *AclEscalationAuditor) Restore(data []byte) error {
	var s snapshotState
	if err := json.Unmarshal(data, &s); err != nil {
		return ErrBadSnapshot
	}
	if s.Version != snapshotVersion || s.Seq < 0 {
		return ErrBadSnapshot
	}

	principals := make(map[string]Principal, len(s.Principals))
	for _, p := range s.Principals {
		id := strings.TrimSpace(p.ID)
		if id == "" {
			return ErrBadSnapshot
		}
		if _, ok := principals[id]; ok {
			return ErrBadSnapshot
		}
		if a.limits.MaxPrincipals > 0 && len(principals) >= a.limits.MaxPrincipals {
			return ErrBadSnapshot
		}
		p.ID = id
		principals[id] = copyPrincipal(p)
	}

	nodes := make(map[string]Node, len(s.Nodes))
	for _, n := range s.Nodes {
		if !validNodePath(n.Path) {
			return ErrBadSnapshot
		}
		if _, ok := nodes[n.Path]; ok {
			return ErrBadSnapshot
		}
		if a.limits.MaxNodes > 0 && len(nodes) >= a.limits.MaxNodes {
			return ErrBadSnapshot
		}
		nodes[n.Path] = copyNode(n)
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	nextSeq := a.seq
	if s.Seq > nextSeq {
		nextSeq = s.Seq
	}
	nextSeq++

	a.principals = principals
	a.nodes = nodes
	a.seq = nextSeq
	a.findings = nil
	return nil
}

func copyPrincipal(p Principal) Principal {
	p.Groups = append([]uint32(nil), p.Groups...)
	sort.Slice(p.Groups, func(i, j int) bool { return p.Groups[i] < p.Groups[j] })
	if len(p.Groups) > 0 {
		dst := p.Groups[:1]
		for _, g := range p.Groups[1:] {
			if g != dst[len(dst)-1] {
				dst = append(dst, g)
			}
		}
		p.Groups = dst
	}
	return p
}

func copyNode(n Node) Node {
	n.Entries = append([]Ace(nil), n.Entries...)
	n.DefaultEntries = append([]Ace(nil), n.DefaultEntries...)
	return n
}

func copyFindings(in []Finding) []Finding {
	if len(in) == 0 {
		return nil
	}
	out := make([]Finding, len(in))
	for i, f := range in {
		out[i] = f
		out[i].Chain = append([]string(nil), f.Chain...)
	}
	return out
}

func validNodePath(p string) bool {
	if p == "" || !strings.HasPrefix(p, "/") {
		return false
	}
	if p != path.Clean(p) {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return false
		}
	}
	return true
}

func nodeMask(n Node) Access {
	mask := AccessFull
	found := false
	for _, e := range n.Entries {
		if e.Kind == AceMask {
			mask &= e.Bits
			found = true
		}
	}
	if !found {
		return AccessFull
	}
	return mask
}

func groupMatches(p Principal, gid uint32) bool {
	if p.GID == gid {
		return true
	}
	for _, g := range p.Groups {
		if g == gid {
			return true
		}
	}
	return false
}

func effectiveAccess(n Node, p Principal) Access {
	mask := nodeMask(n)
	if p.UID == n.Owner {
		bits := n.OwnerBits
		for _, e := range n.Entries {
			if e.Kind == AceUser && e.ID == p.UID {
				bits |= e.Bits & mask
			}
		}
		return bits
	}

	var bits Access
	for _, e := range n.Entries {
		switch e.Kind {
		case AceUser:
			if e.ID == p.UID {
				bits |= e.Bits & mask
			}
		case AceGroup:
			if groupMatches(p, e.ID) {
				bits |= e.Bits & mask
			}
		case AceOther:
			bits |= e.Bits
		}
	}
	if groupMatches(p, n.Group) {
		bits |= n.GroupBits & mask
	}
	bits |= n.OtherBits
	return bits
}

func computeFindings(principals []Principal, nodes map[string]Node, seq int64) []Finding {
	paths := make([]string, 0, len(nodes))
	for p := range nodes {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	directories := make(map[string]bool)
	for _, p := range paths {
		for parent := path.Dir(p); parent != "."; parent = path.Dir(parent) {
			if _, ok := nodes[parent]; ok {
				directories[parent] = true
			}
			if parent == "/" {
				break
			}
		}
	}

	targets := make([]string, 0)
	for _, p := range paths {
		if isPrivilegedTarget(nodes[p], directories[p]) {
			targets = append(targets, p)
		}
	}
	sort.Strings(targets)

	out := make(map[string]Finding)
	for _, p := range principals {
		if p.Privileged {
			continue
		}
		for _, nodePath := range paths {
			n := nodes[nodePath]
			if p.UID == n.Owner || !isPrivilegedTarget(n, directories[nodePath]) {
				continue
			}
			if effectiveAccess(n, p)&AccessWrite == 0 {
				continue
			}
			named, group, via, other := currentAttribution(n, p)
			if !named && !group && !other {
				continue
			}
			reason := ReasonDirectWrite
			if named {
				via = 0
			} else if group {
				reason = ReasonGroupWrite
			} else if other {
				reason = ReasonOtherWrite
			}
			addFinding(out, p, nodePath, reason, SeverityCritical, via, []string{nodePath}, seq)
		}

		for _, target := range targets {
			for _, ancestor := range ancestorPaths(target, nodes) {
				an := nodes[ancestor]
				if p.UID == an.Owner {
					continue
				}
				bits := effectiveAccess(an, p)
				if bits&AccessWrite != 0 && bits&AccessExecute != 0 {
					named, group, via, other := currentAttribution(an, p)
					if named || group || other {
						reason := ReasonAncestorWrite
						if named {
							via = 0
						} else if group {
							reason = ReasonGroupWrite
						} else if other {
							reason = ReasonOtherWrite
						}
						chain := buildChain(ancestor, target, nodes)
						key := findingKey(p.ID, ancestor)
						if _, ok := out[key]; !ok {
							addFinding(out, p, ancestor, reason, SeverityHigh, via, chain, seq)
						}
					}
				}

				named, group, via, other := defaultAttribution(an, p)
				if named || group || other {
					reason := ReasonDefaultWrite
					if named {
						via = 0
					} else if group {
						reason = ReasonGroupWrite
					} else if other {
						reason = ReasonOtherWrite
					}
					key := findingKey(p.ID, ancestor)
					if _, ok := out[key]; !ok {
						chain := buildChain(ancestor, target, nodes)
						addFinding(out, p, ancestor, reason, SeverityHigh, via, chain, seq)
					}
				}
			}
		}
	}

	findings := make([]Finding, 0, len(out))
	for _, f := range out {
		findings = append(findings, f)
	}
	sortFindings(findings)
	return findings
}

func isPrivilegedTarget(n Node, directory bool) bool {
	if n.SetUID || n.SetGID {
		return true
	}
	if directory || n.Owner != 0 {
		return false
	}
	if (n.OwnerBits|n.GroupBits|n.OtherBits)&AccessExecute != 0 {
		return true
	}
	for _, e := range n.Entries {
		if e.Bits&AccessExecute != 0 {
			return true
		}
	}
	return false
}

func currentAttribution(n Node, p Principal) (named bool, group bool, via uint32, other bool) {
	mask := nodeMask(n)
	for _, e := range n.Entries {
		if e.Kind == AceUser && e.ID == p.UID && e.Bits&mask&AccessWrite != 0 {
			named = true
		}
		if e.Kind == AceGroup && groupMatches(p, e.ID) && e.Bits&mask&AccessWrite != 0 {
			if !group || e.ID < via {
				via = e.ID
			}
			group = true
		}
		if e.Kind == AceOther && e.Bits&AccessWrite != 0 {
			other = true
		}
	}
	if groupMatches(p, n.Group) && n.GroupBits&mask&AccessWrite != 0 {
		if !group || n.Group < via {
			via = n.Group
		}
		group = true
	}
	if n.OtherBits&AccessWrite != 0 {
		other = true
	}
	return named, group, via, other
}

func defaultAttribution(n Node, p Principal) (named bool, group bool, via uint32, other bool) {
	for _, e := range n.DefaultEntries {
		switch e.Kind {
		case AceDefaultUser:
			if e.ID == p.UID && e.Bits&AccessWrite != 0 {
				named = true
			}
		case AceDefaultGroup:
			if groupMatches(p, e.ID) && e.Bits&AccessWrite != 0 {
				if !group || e.ID < via {
					via = e.ID
				}
				group = true
			}
		case AceDefaultOther:
			if e.Bits&AccessWrite != 0 {
				other = true
			}
		}
	}
	return named, group, via, other
}

func ancestorPaths(target string, nodes map[string]Node) []string {
	var result []string
	for parent := path.Dir(target); parent != "."; parent = path.Dir(parent) {
		if _, ok := nodes[parent]; ok {
			result = append(result, parent)
		}
		if parent == "/" {
			break
		}
	}
	return result
}

func buildChain(ancestor, target string, nodes map[string]Node) []string {
	var reverse []string
	for cur := target; ; cur = path.Dir(cur) {
		if _, ok := nodes[cur]; ok && isWithin(cur, ancestor) {
			reverse = append(reverse, cur)
		}
		if cur == ancestor || cur == "/" {
			break
		}
	}
	chain := make([]string, 0, len(reverse))
	for i := len(reverse) - 1; i >= 0; i-- {
		chain = append(chain, reverse[i])
	}
	return chain
}

func isWithin(p, ancestor string) bool {
	if ancestor == "/" {
		return strings.HasPrefix(p, "/")
	}
	return p == ancestor || strings.HasPrefix(p, ancestor+"/")
}

func findingKey(principalID, nodePath string) string {
	return principalID + "\x00" + nodePath
}

func addFinding(out map[string]Finding, p Principal, nodePath, reason string, severity Severity, via uint32, chain []string, seq int64) {
	key := findingKey(p.ID, nodePath)
	if _, ok := out[key]; ok {
		return
	}
	out[key] = Finding{
		ID:          findingID(p.ID, nodePath, reason, via),
		PrincipalID: p.ID,
		Path:        nodePath,
		Reason:      reason,
		Severity:    severity,
		ViaGroup:    via,
		Chain:       append([]string(nil), chain...),
		Seq:         seq,
	}
}

func findingID(principalID, nodePath, reason string, via uint32) string {
	sum := sha256.Sum256([]byte(principalID + "\x00" + nodePath + "\x00" + reason + "\x00" + strconv.FormatUint(uint64(via), 10)))
	return "F" + hex.EncodeToString(sum[:8])
}

func severityRank(s Severity) int {
	switch s {
	case SeverityCritical:
		return 3
	case SeverityHigh:
		return 2
	case SeverityMedium:
		return 1
	default:
		return 0
	}
}

func sortFindings(findings []Finding) {
	sort.SliceStable(findings, func(i, j int) bool {
		ri, rj := severityRank(findings[i].Severity), severityRank(findings[j].Severity)
		if ri != rj {
			return ri > rj
		}
		if findings[i].Path != findings[j].Path {
			return findings[i].Path < findings[j].Path
		}
		if findings[i].PrincipalID != findings[j].PrincipalID {
			return findings[i].PrincipalID < findings[j].PrincipalID
		}
		return findings[i].ID < findings[j].ID
	})
}

func collectWriteChanges(n Node, p Principal) []AceChange {
	mask := nodeMask(n)
	out := make(map[string]AceChange)
	add := func(kind AceKind, id uint32) {
		c := AceChange{Path: n.Path, Kind: kind, ID: id, Remove: AccessWrite}
		out[changeKey(c)] = c
	}

	for _, e := range n.Entries {
		switch e.Kind {
		case AceUser:
			if e.ID == p.UID && e.Bits&mask&AccessWrite != 0 {
				add(AceUser, e.ID)
			}
		case AceGroup:
			if groupMatches(p, e.ID) && e.Bits&mask&AccessWrite != 0 {
				add(AceGroup, e.ID)
			}
		case AceOther:
			if e.Bits&AccessWrite != 0 {
				add(AceOther, e.ID)
			}
		}
	}
	for _, e := range n.DefaultEntries {
		switch e.Kind {
		case AceDefaultUser:
			if e.ID == p.UID && e.Bits&AccessWrite != 0 {
				add(AceDefaultUser, e.ID)
			}
		case AceDefaultGroup:
			if groupMatches(p, e.ID) && e.Bits&AccessWrite != 0 {
				add(AceDefaultGroup, e.ID)
			}
		case AceDefaultOther:
			if e.Bits&AccessWrite != 0 {
				add(AceDefaultOther, e.ID)
			}
		}
	}
	if groupMatches(p, n.Group) && n.GroupBits&mask&AccessWrite != 0 {
		add(AceGroup, n.Group)
	}
	if n.OtherBits&AccessWrite != 0 {
		add(AceOther, 0)
	}

	result := make([]AceChange, 0, len(out))
	for _, c := range out {
		result = append(result, c)
	}
	sortChanges(result)
	return result
}

func applyAceChange(n Node, c AceChange) (Node, bool) {
	n = copyNode(n)
	changed := false
	removeFrom := func(entries []Ace, kind AceKind, id uint32) {
		for i := range entries {
			if entries[i].Kind == kind && entries[i].ID == id && entries[i].Bits&c.Remove != 0 {
				entries[i].Bits &^= c.Remove
				changed = true
			}
		}
	}

	switch c.Kind {
	case AceUser:
		removeFrom(n.Entries, AceUser, c.ID)
	case AceGroup:
		if n.Group == c.ID && n.GroupBits&c.Remove != 0 {
			n.GroupBits &^= c.Remove
			changed = true
		}
		removeFrom(n.Entries, AceGroup, c.ID)
	case AceOther:
		if n.OtherBits&c.Remove != 0 {
			n.OtherBits &^= c.Remove
			changed = true
		}
		removeFrom(n.Entries, AceOther, c.ID)
	case AceDefaultUser:
		removeFrom(n.DefaultEntries, AceDefaultUser, c.ID)
	case AceDefaultGroup:
		removeFrom(n.DefaultEntries, AceDefaultGroup, c.ID)
	case AceDefaultOther:
		removeFrom(n.DefaultEntries, AceDefaultOther, c.ID)
	default:
		return n, false
	}

	return n, changed
}

func changeKey(c AceChange) string {
	return c.Path + "\x00" + string(c.Kind) + "\x00" + strconv.FormatUint(uint64(c.ID), 10)
}

func sortChanges(changes []AceChange) {
	sort.SliceStable(changes, func(i, j int) bool {
		if changes[i].Path != changes[j].Path {
			return changes[i].Path < changes[j].Path
		}
		if changes[i].Kind != changes[j].Kind {
			return changes[i].Kind < changes[j].Kind
		}
		return changes[i].ID < changes[j].ID
	})
}

func cloneNodes(in map[string]Node) map[string]Node {
	out := make(map[string]Node, len(in))
	for p, n := range in {
		out[p] = copyNode(n)
	}
	return out
}

func stateDigest(principals map[string]Principal, nodes map[string]Node, seq int64) (string, error) {
	ps := make([]Principal, 0, len(principals))
	for _, p := range principals {
		ps = append(ps, copyPrincipal(p))
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].ID < ps[j].ID })

	ns := make([]Node, 0, len(nodes))
	for _, n := range nodes {
		ns = append(ns, copyNode(n))
	}
	sort.Slice(ns, func(i, j int) bool { return ns[i].Path < ns[j].Path })

	payload, err := json.Marshal(struct {
		Seq        int64       `json:"seq"`
		Principals []Principal `json:"principals"`
		Nodes      []Node      `json:"nodes"`
	}{Seq: seq, Principals: ps, Nodes: ns})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}
