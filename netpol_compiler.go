// Package task compiles network policies into a per workload allow matrix and
// evaluates live flows against that matrix.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
)

// Transport protocols accepted inside a policy rule.
const (
	ProtocolTCP = "tcp"
	ProtocolUDP = "udp"
)

// Traffic directions carried by a flow and by a matrix entry.
const (
	DirectionIngress = "ingress"
	DirectionEgress  = "egress"
)

// Decisions returned by Evaluate.
const (
	DecisionAllow = "allow"
	DecisionDeny  = "deny"
)

// Reason codes attached to a decision.
const (
	ReasonDefaultAllow = "default_allow"
	ReasonRuleMatch    = "rule_match"
	ReasonNoRule       = "no_rule"
	ReasonUnknown      = "unknown_workload"
)

// Kinds of a matrix diff record.
const (
	ChangeAdded   = "added"
	ChangeRemoved = "removed"
)

// Errors returned by the compiler.
var (
	ErrBlankPolicy       = errors.New("policy name is required")
	ErrDuplicatePolicy   = errors.New("duplicate policy name")
	ErrBlankWorkload     = errors.New("workload id is required")
	ErrDuplicateWorkload = errors.New("duplicate workload id")
	ErrInvalidPolicy     = errors.New("policy is invalid")
	ErrBadSnapshot       = errors.New("snapshot payload is invalid")
)

// Decision is the verdict for one evaluated flow.
type Decision string

// Reason explains one decision: which policy and which rule produced it.
type Reason struct {
	Code   string
	Policy string
	Rule   int
}

// PortRef is a policy port reference. It is either numeric (Port, optionally
// with EndPort for a range) or a named port resolved from the workload.
type PortRef struct {
	Name     string
	Port     int
	EndPort  int
	Protocol string
}

// Peer is one ingress or egress rule source or destination.
type Peer struct {
	NamespaceSelector map[string]string
	PodSelector       map[string]string
	IPBlock           string
	Except            []string
	Ports             []PortRef
}

// Policy selects workloads in one namespace and carries ingress and egress
// peers.
type Policy struct {
	Name      string
	Namespace string
	Selector  map[string]string
	Ingress   []Peer
	Egress    []Peer
}

// Workload is one addressable workload with its labels and named ports.
type Workload struct {
	ID        string
	Namespace string
	Labels    map[string]string
	Ports     map[string]int
}

// Flow is one live connection to evaluate against the compiled matrix.
type Flow struct {
	Direction     string
	PeerIP        string
	PeerNamespace string
	PeerLabels    map[string]string
	Port          int
	Protocol      string
}

// Entry is one normalised allowed rule in the compiled matrix.
type Entry struct {
	WorkloadID string
	Direction  string
	Policy     string
	Rule       int
	Peer       string
	Protocol   string
	PortStart  int
	PortEnd    int
}

// Diagnostic is one compile time finding that did not abort the compile.
type Diagnostic struct {
	Policy string
	Code   string
	Detail string
}

// Matrix is the deterministic compiled result for a set of policies.
type Matrix struct {
	Workloads   []string
	Entries     []Entry
	Diagnostics []Diagnostic
}

// Change is one entry level difference between two matrices.
type Change struct {
	WorkloadID string
	Direction  string
	Kind       string
	Policy     string
	Rule       int
	Protocol   string
	PortStart  int
	PortEnd    int
}

// NetPolCompiler owns the compiled matrix.
type NetPolCompiler struct {
	mu    sync.RWMutex
	state *compiledState
}

type peerKind string

const (
	peerKindSelector peerKind = "selector"
	peerKindIPBlock  peerKind = "ipblock"
)

// compiledState is immutable after it is published. Compile and Restore build
// a complete new state and swap it in one step, so readers never observe a
// partially updated matrix.
type compiledState struct {
	matrix      Matrix
	entries     []compiledEntry
	byWorkload  map[string][]compiledEntry
	selected    map[string]bool
	workloads   map[string]struct{}
	policyNames []string
}

type compiledEntry struct {
	Entry
	kind              peerKind
	ipBlock           *net.IPNet
	except            []*net.IPNet
	namespaceSelector map[string]string
	podSelector       map[string]string
}

const (
	snapshotVersion = 1

	diagnosticUnresolvedNamedPort = "unresolved_named_port"
	diagnosticInvalidIPBlock      = "invalid_ip_block"
	diagnosticInvalidExcept       = "invalid_except"
)

// NewNetPolCompiler builds an empty compiler.
func NewNetPolCompiler() *NetPolCompiler {
	return &NetPolCompiler{}
}

// Compile normalises the policies against the workloads into a matrix.
func (c *NetPolCompiler) Compile(policies []Policy, workloads []Workload) (*Matrix, error) {
	if err := ValidatePolicies(policies); err != nil {
		c.invalidate()
		return nil, err
	}
	if err := validateWorkloads(workloads); err != nil {
		c.invalidate()
		return nil, err
	}

	pols := append([]Policy(nil), policies...)
	sort.Slice(pols, func(i, j int) bool {
		if pols[i].Name != pols[j].Name {
			return pols[i].Name < pols[j].Name
		}
		return pols[i].Namespace < pols[j].Namespace
	})

	ws := append([]Workload(nil), workloads...)
	sort.Slice(ws, func(i, j int) bool { return ws[i].ID < ws[j].ID })

	ids := make([]string, 0, len(ws))
	workloadSet := make(map[string]struct{}, len(ws))
	for _, w := range ws {
		ids = append(ids, w.ID)
		workloadSet[w.ID] = struct{}{}
	}

	selected := make(map[string]bool, len(ws))
	var compiled []compiledEntry
	var diagnostics []Diagnostic
	for _, w := range ws {
		for _, p := range pols {
			if !policySelectsWorkload(p, w) {
				continue
			}
			selected[w.ID] = true
			compiled = append(compiled, compileDirection(w, p, DirectionIngress, p.Ingress, &diagnostics)...)
			compiled = append(compiled, compileDirection(w, p, DirectionEgress, p.Egress, &diagnostics)...)
		}
	}

	sort.Slice(compiled, func(i, j int) bool { return lessEntry(compiled[i].Entry, compiled[j].Entry) })
	compiled = dedupeCompiled(compiled)
	sortDiagnostics(diagnostics)

	entries := make([]Entry, len(compiled))
	for i, ce := range compiled {
		entries[i] = ce.Entry
	}
	matrix := Matrix{
		Workloads:   append([]string(nil), ids...),
		Entries:     entries,
		Diagnostics: cloneDiagnostics(diagnostics),
	}
	policyNames := make([]string, len(pols))
	for i, p := range pols {
		policyNames[i] = p.Name
	}
	state := &compiledState{
		matrix:      matrix,
		entries:     compiled,
		byWorkload:  groupByWorkload(compiled),
		selected:    selected,
		workloads:   workloadSet,
		policyNames: policyNames,
	}
	c.publish(state)
	return cloneMatrix(&matrix), nil
}

// Evaluate decides one flow for one workload.
func (c *NetPolCompiler) Evaluate(workloadID string, flow Flow) (Decision, Reason) {
	state := c.load()
	if state == nil {
		return DecisionDeny, Reason{Code: ReasonUnknown}
	}
	if _, ok := state.workloads[workloadID]; !ok {
		return DecisionDeny, Reason{Code: ReasonUnknown}
	}
	if !state.selected[workloadID] {
		return DecisionAllow, Reason{Code: ReasonDefaultAllow}
	}

	direction := normalizeDirection(flow.Direction)
	for i := range state.byWorkload[workloadID] {
		entry := &state.byWorkload[workloadID][i]
		if entry.Direction != direction {
			continue
		}
		if entry.matches(flow) {
			return DecisionAllow, Reason{
				Code:   ReasonRuleMatch,
				Policy: entry.Policy,
				Rule:   entry.Rule,
			}
		}
	}
	return DecisionDeny, Reason{Code: ReasonNoRule}
}

// Diff reports entry level differences between two matrices.
func Diff(old, next *Matrix) []Change {
	oldCounts := make(map[Entry]int)
	if old != nil {
		for _, entry := range old.Entries {
			oldCounts[entry]++
		}
	}

	var changes []Change
	if next != nil {
		for _, entry := range next.Entries {
			if oldCounts[entry] > 0 {
				oldCounts[entry]--
				continue
			}
			changes = append(changes, changeFromEntry(entry, ChangeAdded))
		}
	}
	if old != nil {
		for _, entry := range old.Entries {
			if oldCounts[entry] == 0 {
				continue
			}
			oldCounts[entry]--
			changes = append(changes, changeFromEntry(entry, ChangeRemoved))
		}
	}
	sort.SliceStable(changes, func(i, j int) bool { return lessChange(changes[i], changes[j]) })
	return changes
}

// Snapshot serialises the compiled matrix.
func (c *NetPolCompiler) Snapshot() ([]byte, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	doc := snapshotDoc{Version: snapshotVersion}
	if c.state != nil {
		state := c.state
		doc.Workloads = append([]string(nil), state.matrix.Workloads...)
		doc.Diagnostics = cloneDiagnostics(state.matrix.Diagnostics)
		doc.PolicyNames = append([]string(nil), state.policyNames...)
		doc.Selected = selectedIDs(state.selected)
		doc.Entries = make([]snapshotEntry, 0, len(state.entries))
		for _, entry := range state.entries {
			snap := snapshotEntry{
				Entry:             entry.Entry,
				Kind:              string(entry.kind),
				NamespaceSelector: cloneMap(entry.namespaceSelector),
				PodSelector:       cloneMap(entry.podSelector),
			}
			if entry.ipBlock != nil {
				snap.IPBlock = entry.ipBlock.String()
			}
			for _, except := range entry.except {
				snap.Except = append(snap.Except, except.String())
			}
			doc.Entries = append(doc.Entries, snap)
		}
	}
	return json.Marshal(doc)
}

// Restore replaces the compiled matrix from a snapshot.
func (c *NetPolCompiler) Restore(data []byte) error {
	var doc snapshotDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	state, err := stateFromSnapshot(doc)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	c.publish(state)
	return nil
}

// WorkloadIDs lists the compiled workload ids in ascending order.
func (c *NetPolCompiler) WorkloadIDs() []string {
	state := c.load()
	if state == nil {
		return nil
	}
	return append([]string(nil), state.matrix.Workloads...)
}

// Entries returns the matrix entries of one workload.
func (c *NetPolCompiler) Entries(workloadID string) []Entry {
	state := c.load()
	if state == nil {
		return nil
	}
	compiled := state.byWorkload[workloadID]
	entries := make([]Entry, len(compiled))
	for i, entry := range compiled {
		entries[i] = entry.Entry
	}
	return entries
}

// Diagnostics returns the compile time findings of the last compile.
func (c *NetPolCompiler) Diagnostics() []Diagnostic {
	state := c.load()
	if state == nil {
		return nil
	}
	return cloneDiagnostics(state.matrix.Diagnostics)
}

// PolicyNames returns the accepted policy names of the last compile.
func (c *NetPolCompiler) PolicyNames() []string {
	state := c.load()
	if state == nil {
		return nil
	}
	return append([]string(nil), state.policyNames...)
}

// ValidatePolicies checks policy names and port references without compiling.
func ValidatePolicies(policies []Policy) error {
	seen := make(map[string]struct{}, len(policies))
	for _, policy := range policies {
		name := strings.TrimSpace(policy.Name)
		if name == "" {
			return fmt.Errorf("%w", ErrBlankPolicy)
		}
		if _, ok := seen[policy.Name]; ok {
			return fmt.Errorf("%w: %s", ErrDuplicatePolicy, policy.Name)
		}
		seen[policy.Name] = struct{}{}

		for _, peer := range policy.Ingress {
			for _, port := range peer.Ports {
				if !validPortRef(port) {
					return fmt.Errorf("%w: policy %s has invalid port range %d-%d", ErrInvalidPolicy, policy.Name, port.Port, port.EndPort)
				}
			}
		}
		for _, peer := range policy.Egress {
			for _, port := range peer.Ports {
				if !validPortRef(port) {
					return fmt.Errorf("%w: policy %s has invalid port range %d-%d", ErrInvalidPolicy, policy.Name, port.Port, port.EndPort)
				}
			}
		}
	}
	return nil
}

// SelectorMatches reports whether every selector pair is present in labels.
func SelectorMatches(selector, labels map[string]string) bool {
	for key, want := range selector {
		if got, ok := labels[key]; !ok || got != want {
			return false
		}
	}
	return true
}

func (c *NetPolCompiler) load() *compiledState {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	state := c.state
	c.mu.RUnlock()
	return state
}

func (c *NetPolCompiler) publish(state *compiledState) {
	c.mu.Lock()
	c.state = state
	c.mu.Unlock()
}

func (c *NetPolCompiler) invalidate() {
	c.publish(nil)
}

func validateWorkloads(workloads []Workload) error {
	seen := make(map[string]struct{}, len(workloads))
	for _, workload := range workloads {
		if strings.TrimSpace(workload.ID) == "" {
			return fmt.Errorf("%w", ErrBlankWorkload)
		}
		if _, ok := seen[workload.ID]; ok {
			return fmt.Errorf("%w: %s", ErrDuplicateWorkload, workload.ID)
		}
		seen[workload.ID] = struct{}{}
	}
	return nil
}

func validPortRef(port PortRef) bool {
	if port.Port < 0 || port.EndPort < 0 {
		return false
	}
	if port.EndPort != 0 && port.EndPort < port.Port {
		return false
	}
	return true
}

func policySelectsWorkload(policy Policy, workload Workload) bool {
	if policy.Namespace != "" && policy.Namespace != workload.Namespace {
		return false
	}
	return SelectorMatches(policy.Selector, workload.Labels)
}

func compileDirection(workload Workload, policy Policy, direction string, peers []Peer, diagnostics *[]Diagnostic) []compiledEntry {
	var entries []compiledEntry
	for ruleIndex, peer := range peers {
		entries = append(entries, compilePeer(workload, policy, direction, ruleIndex, peer, diagnostics)...)
	}
	return entries
}

func compilePeer(workload Workload, policy Policy, direction string, ruleIndex int, peer Peer, diagnostics *[]Diagnostic) []compiledEntry {
	base := compiledEntry{
		Entry: Entry{
			WorkloadID: workload.ID,
			Direction:  direction,
			Policy:     policy.Name,
			Rule:       ruleIndex,
		},
	}

	if strings.TrimSpace(peer.IPBlock) != "" {
		block, err := parseCIDR(peer.IPBlock)
		if err != nil {
			*diagnostics = append(*diagnostics, Diagnostic{
				Policy: policy.Name,
				Code:   diagnosticInvalidIPBlock,
				Detail: fmt.Sprintf("workload %q %s rule %d: %v", workload.ID, direction, ruleIndex, err),
			})
			return nil
		}
		base.kind = peerKindIPBlock
		base.ipBlock = block

		exceptStrings := make([]string, 0, len(peer.Except))
		for _, rawExcept := range peer.Except {
			except, err := parseCIDR(rawExcept)
			if err != nil {
				*diagnostics = append(*diagnostics, Diagnostic{
					Policy: policy.Name,
					Code:   diagnosticInvalidExcept,
					Detail: fmt.Sprintf("workload %q %s rule %d: %v", workload.ID, direction, ruleIndex, err),
				})
				return nil
			}
			base.except = append(base.except, except)
			exceptStrings = append(exceptStrings, except.String())
		}
		sort.Strings(exceptStrings)
		base.Peer = "ip:" + block.String()
		if len(exceptStrings) > 0 {
			base.Peer += " except " + strings.Join(exceptStrings, ",")
		}
	} else {
		base.kind = peerKindSelector
		base.namespaceSelector = cloneMap(peer.NamespaceSelector)
		base.podSelector = cloneMap(peer.PodSelector)
		base.Peer = selectorPeerKey(base.namespaceSelector, base.podSelector)
	}

	if len(peer.Ports) == 0 {
		base.PortStart = 0
		base.PortEnd = 65535
		return []compiledEntry{base}
	}

	entries := make([]compiledEntry, 0, len(peer.Ports))
	for _, port := range peer.Ports {
		start, end, ok := resolvePort(port, workload)
		if !ok {
			*diagnostics = append(*diagnostics, Diagnostic{
				Policy: policy.Name,
				Code:   diagnosticUnresolvedNamedPort,
				Detail: fmt.Sprintf("workload %q %s rule %d: named port %q has no mapping", workload.ID, direction, ruleIndex, port.Name),
			})
			continue
		}
		entry := base
		entry.Protocol = normalizeProtocol(port.Protocol)
		entry.PortStart = start
		entry.PortEnd = end
		entries = append(entries, entry)
	}
	return entries
}

func resolvePort(port PortRef, workload Workload) (int, int, bool) {
	if strings.TrimSpace(port.Name) != "" {
		mapped, ok := workload.Ports[port.Name]
		if !ok {
			return 0, 0, false
		}
		end := mapped
		if port.EndPort > mapped {
			end = port.EndPort
		}
		return mapped, end, true
	}
	end := port.EndPort
	if end == 0 {
		end = port.Port
	}
	return port.Port, end, true
}

func (entry *compiledEntry) matches(flow Flow) bool {
	if !protocolMatches(entry.Protocol, flow.Protocol) {
		return false
	}
	if flow.Port < entry.PortStart || flow.Port > entry.PortEnd {
		return false
	}

	if entry.kind == peerKindIPBlock {
		ip := net.ParseIP(strings.TrimSpace(flow.PeerIP))
		if ip == nil || entry.ipBlock == nil || !entry.ipBlock.Contains(ip) {
			return false
		}
		for _, except := range entry.except {
			if except.Contains(ip) {
				return false
			}
		}
		return true
	}

	if !namespaceSelectorMatches(entry.namespaceSelector, flow.PeerNamespace) {
		return false
	}
	return SelectorMatches(entry.podSelector, flow.PeerLabels)
}

func protocolMatches(ruleProtocol, flowProtocol string) bool {
	if ruleProtocol == "" {
		return true
	}
	return normalizeProtocol(ruleProtocol) == normalizeProtocol(flowProtocol)
}

func normalizeProtocol(protocol string) string {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	if protocol == "" {
		return ProtocolTCP
	}
	return protocol
}

func normalizeDirection(direction string) string {
	return strings.ToLower(strings.TrimSpace(direction))
}

func namespaceSelectorMatches(selector map[string]string, namespace string) bool {
	if len(selector) == 0 {
		return true
	}
	if namespace == "" {
		return false
	}

	identityKey := false
	for key, value := range selector {
		switch key {
		case "kubernetes.io/metadata.name", "namespace", "name":
			identityKey = true
			if value != namespace {
				return false
			}
		}
	}
	if identityKey {
		return true
	}

	// The flow carries only the peer namespace name, not the full namespace
	// label set. Treat an explicit selector value that names the namespace as
	// the available identity signal.
	for _, value := range selector {
		if value == namespace {
			return true
		}
	}
	return false
}

func parseCIDR(raw string) (*net.IPNet, error) {
	_, network, err := net.ParseCIDR(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("invalid CIDR %q", raw)
	}
	return network, nil
}

func selectorPeerKey(namespaceSelector, podSelector map[string]string) string {
	return "ns{" + canonicalSelector(namespaceSelector) + "};pod{" + canonicalSelector(podSelector) + "}"
}

func canonicalSelector(selector map[string]string) string {
	if len(selector) == 0 {
		return ""
	}
	keys := make([]string, 0, len(selector))
	for key := range selector {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+selector[key])
	}
	return strings.Join(parts, ",")
}

func lessEntry(a, b Entry) bool {
	if a.WorkloadID != b.WorkloadID {
		return a.WorkloadID < b.WorkloadID
	}
	if a.Direction != b.Direction {
		return a.Direction < b.Direction
	}
	if a.Policy != b.Policy {
		return a.Policy < b.Policy
	}
	if a.Rule != b.Rule {
		return a.Rule < b.Rule
	}
	if a.Peer != b.Peer {
		return a.Peer < b.Peer
	}
	if a.Protocol != b.Protocol {
		return a.Protocol < b.Protocol
	}
	if a.PortStart != b.PortStart {
		return a.PortStart < b.PortStart
	}
	return a.PortEnd < b.PortEnd
}

func lessChange(a, b Change) bool {
	if a.WorkloadID != b.WorkloadID {
		return a.WorkloadID < b.WorkloadID
	}
	if a.Direction != b.Direction {
		return a.Direction < b.Direction
	}
	if a.Kind != b.Kind {
		return a.Kind < b.Kind
	}
	if a.Policy != b.Policy {
		return a.Policy < b.Policy
	}
	if a.Rule != b.Rule {
		return a.Rule < b.Rule
	}
	if a.Protocol != b.Protocol {
		return a.Protocol < b.Protocol
	}
	if a.PortStart != b.PortStart {
		return a.PortStart < b.PortStart
	}
	return a.PortEnd < b.PortEnd
}

func dedupeCompiled(entries []compiledEntry) []compiledEntry {
	if len(entries) < 2 {
		return entries
	}
	out := entries[:1]
	for _, entry := range entries[1:] {
		if entry.Entry == out[len(out)-1].Entry {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func groupByWorkload(entries []compiledEntry) map[string][]compiledEntry {
	grouped := make(map[string][]compiledEntry)
	for _, entry := range entries {
		grouped[entry.WorkloadID] = append(grouped[entry.WorkloadID], entry)
	}
	return grouped
}

func changeFromEntry(entry Entry, kind string) Change {
	return Change{
		WorkloadID: entry.WorkloadID,
		Direction:  entry.Direction,
		Kind:       kind,
		Policy:     entry.Policy,
		Rule:       entry.Rule,
		Protocol:   entry.Protocol,
		PortStart:  entry.PortStart,
		PortEnd:    entry.PortEnd,
	}
}

func sortDiagnostics(diagnostics []Diagnostic) {
	sort.SliceStable(diagnostics, func(i, j int) bool {
		if diagnostics[i].Policy != diagnostics[j].Policy {
			return diagnostics[i].Policy < diagnostics[j].Policy
		}
		if diagnostics[i].Code != diagnostics[j].Code {
			return diagnostics[i].Code < diagnostics[j].Code
		}
		return diagnostics[i].Detail < diagnostics[j].Detail
	})
}

func cloneMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func cloneDiagnostics(source []Diagnostic) []Diagnostic {
	return append([]Diagnostic(nil), source...)
}

func cloneMatrix(matrix *Matrix) *Matrix {
	if matrix == nil {
		return nil
	}
	return &Matrix{
		Workloads:   append([]string(nil), matrix.Workloads...),
		Entries:     append([]Entry(nil), matrix.Entries...),
		Diagnostics: cloneDiagnostics(matrix.Diagnostics),
	}
}

func selectedIDs(selected map[string]bool) []string {
	ids := make([]string, 0, len(selected))
	for id := range selected {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

type snapshotDoc struct {
	Version     int             `json:"version"`
	Workloads   []string        `json:"workloads"`
	Entries     []snapshotEntry `json:"entries"`
	Diagnostics []Diagnostic    `json:"diagnostics"`
	Selected    []string        `json:"selected"`
	PolicyNames []string        `json:"policyNames"`
}

type snapshotEntry struct {
	Entry
	Kind              string            `json:"kind"`
	IPBlock           string            `json:"ipBlock,omitempty"`
	Except            []string          `json:"except,omitempty"`
	NamespaceSelector map[string]string `json:"namespaceSelector,omitempty"`
	PodSelector       map[string]string `json:"podSelector,omitempty"`
}

func stateFromSnapshot(doc snapshotDoc) (*compiledState, error) {
	if doc.Version != snapshotVersion {
		return nil, fmt.Errorf("unsupported version %d", doc.Version)
	}

	workloads := append([]string(nil), doc.Workloads...)
	sort.Strings(workloads)
	workloadSet := make(map[string]struct{}, len(workloads))
	for i, id := range workloads {
		if strings.TrimSpace(id) == "" {
			return nil, errors.New("blank workload id")
		}
		if i > 0 && workloads[i-1] == id {
			return nil, fmt.Errorf("duplicate workload id %q", id)
		}
		workloadSet[id] = struct{}{}
	}

	policyNames := append([]string(nil), doc.PolicyNames...)
	sort.Strings(policyNames)
	for i, name := range policyNames {
		if strings.TrimSpace(name) == "" {
			return nil, errors.New("blank policy name")
		}
		if i > 0 && policyNames[i-1] == name {
			return nil, fmt.Errorf("duplicate policy name %q", name)
		}
	}

	selected := make(map[string]bool, len(doc.Selected))
	selectedList := append([]string(nil), doc.Selected...)
	sort.Strings(selectedList)
	for i, id := range selectedList {
		if _, ok := workloadSet[id]; !ok {
			return nil, fmt.Errorf("selected unknown workload %q", id)
		}
		if i > 0 && selectedList[i-1] == id {
			return nil, fmt.Errorf("duplicate selected workload %q", id)
		}
		selected[id] = true
	}

	compiled := make([]compiledEntry, 0, len(doc.Entries))
	for _, snap := range doc.Entries {
		entry, err := compiledEntryFromSnapshot(snap, workloadSet)
		if err != nil {
			return nil, err
		}
		compiled = append(compiled, entry)
	}
	sort.Slice(compiled, func(i, j int) bool { return lessEntry(compiled[i].Entry, compiled[j].Entry) })

	entries := make([]Entry, len(compiled))
	for i, entry := range compiled {
		entries[i] = entry.Entry
	}
	diagnostics := cloneDiagnostics(doc.Diagnostics)
	matrix := Matrix{
		Workloads:   workloads,
		Entries:     entries,
		Diagnostics: diagnostics,
	}
	return &compiledState{
		matrix:      matrix,
		entries:     compiled,
		byWorkload:  groupByWorkload(compiled),
		selected:    selected,
		workloads:   workloadSet,
		policyNames: policyNames,
	}, nil
}

func compiledEntryFromSnapshot(snap snapshotEntry, workloads map[string]struct{}) (compiledEntry, error) {
	entry := compiledEntry{Entry: snap.Entry}
	if _, ok := workloads[entry.WorkloadID]; !ok {
		return compiledEntry{}, fmt.Errorf("entry references unknown workload %q", entry.WorkloadID)
	}
	if entry.Direction != DirectionIngress && entry.Direction != DirectionEgress {
		return compiledEntry{}, fmt.Errorf("invalid direction %q", entry.Direction)
	}
	if entry.Rule < 0 {
		return compiledEntry{}, fmt.Errorf("invalid rule index %d", entry.Rule)
	}
	if entry.PortStart < 0 || entry.PortEnd < entry.PortStart {
		return compiledEntry{}, fmt.Errorf("invalid port range %d-%d", entry.PortStart, entry.PortEnd)
	}

	switch peerKind(snap.Kind) {
	case peerKindIPBlock:
		block, err := parseCIDR(snap.IPBlock)
		if err != nil {
			return compiledEntry{}, err
		}
		entry.kind = peerKindIPBlock
		entry.ipBlock = block
		for _, rawExcept := range snap.Except {
			except, err := parseCIDR(rawExcept)
			if err != nil {
				return compiledEntry{}, err
			}
			entry.except = append(entry.except, except)
		}
	case peerKindSelector:
		entry.kind = peerKindSelector
		entry.namespaceSelector = cloneMap(snap.NamespaceSelector)
		entry.podSelector = cloneMap(snap.PodSelector)
	default:
		return compiledEntry{}, fmt.Errorf("invalid peer kind %q", snap.Kind)
	}
	return entry, nil
}
