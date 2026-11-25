// Package task attributes reachable vulnerable components in an SBOM
// dependency graph back to the owners that pull them in.
package task

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"sync"
)

// Edge scopes.
const (
	ScopeProd     = "prod"
	ScopeDev      = "dev"
	ScopeOptional = "optional"
)

// PathSeparator joins component ids inside a rendered path key.
const PathSeparator = " -> "

// Errors returned by the attributor.
var (
	ErrBadGraph         = errors.New("dependency graph is invalid")
	ErrUnknownComponent = errors.New("unknown component")
	ErrNoPath           = errors.New("no path from any root")
	ErrBadLimit         = errors.New("path limit must be positive")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

// Component is one resolved package at one version. Its id is
// "<name>@<version>".
type Component struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ID renders the stable node key of the component.
func (c Component) ID() string {
	return c.Name + "@" + c.Version
}

// Edge is one dependency declaration between two component ids.
type Edge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Scope string `json:"scope"`
}

// Graph is a directed dependency multigraph plus its entry points.
type Graph struct {
	Components []Component `json:"components"`
	Edges      []Edge      `json:"edges"`
	Roots      []string    `json:"roots"`
}

// Path is one simple dependency path from a root to a component.
type Path struct {
	Nodes    []string `json:"nodes"`
	Complete bool     `json:"complete"`
}

// Owner is one component that declares a direct dependency on another one.
type Owner struct {
	ID string `json:"id"`
}

// PrunePolicy controls which dependency scopes are dropped from traversal.
type PrunePolicy struct {
	DropDev      bool `json:"drop_dev"`
	DropOptional bool `json:"drop_optional"`
}

const (
	policyDropDev      = 1
	policyDropOptional = 2
)

func policyIndex(policy PrunePolicy) int {
	index := 0
	if policy.DropDev {
		index |= policyDropDev
	}
	if policy.DropOptional {
		index |= policyDropOptional
	}
	return index
}

func scopeAllowed(scope string, policy int) bool {
	if policy&policyDropDev != 0 && scope == ScopeDev {
		return false
	}
	if policy&policyDropOptional != 0 && scope == ScopeOptional {
		return false
	}
	return true
}

// graphState is an immutable snapshot of a validated graph. Every policy
// variant gets its own sorted adjacency lists, so traversals never depend on
// map iteration order.
type graphState struct {
	components map[string]Component
	ids        []string
	roots      []string
	rootSet    map[string]bool
	edges      []Edge
	forward    [4]map[string][]string
	reverse    [4]map[string][]string
}

// ReachAttributor owns the loaded graph and the active prune policy.
type ReachAttributor struct {
	mu     sync.RWMutex
	state  *graphState
	policy PrunePolicy
}

// NewReachAttributor builds an attributor with no graph loaded.
func NewReachAttributor() *ReachAttributor {
	return &ReachAttributor{}
}

// Load validates and swaps in a whole dependency graph.
func (r *ReachAttributor) Load(graph Graph) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	state, err := buildGraphState(graph)
	if err != nil {
		return ErrBadGraph
	}
	r.state = state
	return nil
}

// SetPolicy replaces the prune policy used by later traversals.
func (r *ReachAttributor) SetPolicy(policy PrunePolicy) error {
	r.mu.Lock()
	r.policy = policy
	r.mu.Unlock()
	return nil
}

// Policy returns the active prune policy.
func (r *ReachAttributor) Policy() PrunePolicy {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.policy
}

// Components lists the loaded component ids in ascending order.
func (r *ReachAttributor) Components() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.state == nil {
		return []string{}
	}
	return append([]string(nil), r.state.ids...)
}

// Roots lists the entry point ids in ascending order.
func (r *ReachAttributor) Roots() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.state == nil {
		return []string{}
	}
	return append([]string(nil), r.state.roots...)
}

// Node reads one component by id.
func (r *ReachAttributor) Node(id string) (Component, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.state == nil {
		return Component{}, false
	}
	component, ok := r.state.components[id]
	return component, ok
}

// PathsTo enumerates simple dependency paths to one component, shortest
// first and then lexicographic, truncated at limit.
func (r *ReachAttributor) PathsTo(component string, limit int) ([]Path, error) {
	if limit <= 0 {
		return nil, ErrBadLimit
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.state == nil {
		return nil, ErrUnknownComponent
	}
	if _, ok := r.state.components[component]; !ok {
		return nil, ErrUnknownComponent
	}

	paths := r.state.enumeratePaths(component, policyIndex(r.policy), limit)
	if len(paths) == 0 {
		return []Path{}, nil
	}
	return paths, nil
}

// Introducers lists the distinct direct owners of one component.
func (r *ReachAttributor) Introducers(component string) []Owner {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.state == nil {
		return []Owner{}
	}
	if _, ok := r.state.components[component]; !ok {
		return []Owner{}
	}
	if r.state.rootSet[component] {
		return []Owner{}
	}

	parents := r.state.reverse[policyIndex(r.policy)][component]
	owners := make([]Owner, 0, len(parents))
	for _, parent := range parents {
		owners = append(owners, Owner{ID: parent})
	}
	return owners
}

// Shortest returns the minimal path to one component.
func (r *ReachAttributor) Shortest(component string) (Path, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.state == nil {
		return Path{}, ErrUnknownComponent
	}
	if _, ok := r.state.components[component]; !ok {
		return Path{}, ErrUnknownComponent
	}

	path, ok := r.state.shortestPath(component, policyIndex(r.policy))
	if !ok {
		return Path{}, ErrNoPath
	}
	return path, nil
}

type snapshotEnvelope struct {
	Format  string      `json:"format"`
	Version int         `json:"version"`
	Graph   Graph       `json:"graph"`
	Policy  PrunePolicy `json:"policy"`
}

const (
	snapshotFormat  = "sbom-reach-attributor"
	snapshotVersion = 1
)

// Snapshot serialises the graph and the prune policy.
func (r *ReachAttributor) Snapshot() ([]byte, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.state == nil {
		return nil, ErrBadGraph
	}

	components := make([]Component, 0, len(r.state.ids))
	for _, id := range r.state.ids {
		components = append(components, r.state.components[id])
	}
	edges := append([]Edge(nil), r.state.edges...)
	roots := append([]string(nil), r.state.roots...)
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		if edges[i].To != edges[j].To {
			return edges[i].To < edges[j].To
		}
		return edges[i].Scope < edges[j].Scope
	})

	return json.Marshal(snapshotEnvelope{
		Format:  snapshotFormat,
		Version: snapshotVersion,
		Graph: Graph{
			Components: components,
			Edges:      edges,
			Roots:      roots,
		},
		Policy: r.policy,
	})
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (r *ReachAttributor) Restore(data []byte) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return ErrBadSnapshot
	}

	var snapshot snapshotEnvelope
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return ErrBadSnapshot
	}
	if snapshot.Format != snapshotFormat || snapshot.Version != snapshotVersion {
		return ErrBadSnapshot
	}
	state, err := buildGraphState(snapshot.Graph)
	if err != nil {
		return ErrBadSnapshot
	}

	r.mu.Lock()
	r.state = state
	r.policy = snapshot.Policy
	r.mu.Unlock()
	return nil
}

func buildGraphState(graph Graph) (*graphState, error) {
	if len(graph.Components) == 0 || len(graph.Roots) == 0 {
		return nil, ErrBadGraph
	}

	components := make(map[string]Component, len(graph.Components))
	ids := make([]string, 0, len(graph.Components))
	for _, component := range graph.Components {
		if component.Name == "" || component.Version == "" {
			return nil, ErrBadGraph
		}
		id := component.ID()
		if _, exists := components[id]; exists {
			return nil, ErrBadGraph
		}
		components[id] = component
		ids = append(ids, id)
	}

	rootSet := make(map[string]bool, len(graph.Roots))
	roots := make([]string, 0, len(graph.Roots))
	for _, root := range graph.Roots {
		if _, exists := components[root]; !exists {
			return nil, ErrBadGraph
		}
		if !rootSet[root] {
			rootSet[root] = true
			roots = append(roots, root)
		}
	}

	for _, edge := range graph.Edges {
		if edge.From == edge.To {
			return nil, ErrBadGraph
		}
		if _, exists := components[edge.From]; !exists {
			return nil, ErrBadGraph
		}
		if _, exists := components[edge.To]; !exists {
			return nil, ErrBadGraph
		}
	}

	sort.Strings(ids)
	sort.Strings(roots)

	var forwardSets [4]map[string]map[string]bool
	var reverseSets [4]map[string]map[string]bool
	for policy := 0; policy < 4; policy++ {
		forwardSets[policy] = make(map[string]map[string]bool)
		reverseSets[policy] = make(map[string]map[string]bool)
	}
	for _, edge := range graph.Edges {
		for policy := 0; policy < 4; policy++ {
			if !scopeAllowed(edge.Scope, policy) {
				continue
			}
			addAdjacency(forwardSets[policy], edge.From, edge.To)
			addAdjacency(reverseSets[policy], edge.To, edge.From)
		}
	}

	state := &graphState{
		components: components,
		ids:        ids,
		roots:      roots,
		rootSet:    rootSet,
		edges:      append([]Edge(nil), graph.Edges...),
	}
	for policy := 0; policy < 4; policy++ {
		state.forward[policy] = make(map[string][]string, len(ids))
		state.reverse[policy] = make(map[string][]string, len(ids))
		for _, id := range ids {
			state.forward[policy][id] = sortedSet(forwardSets[policy][id])
			state.reverse[policy][id] = sortedSet(reverseSets[policy][id])
		}
	}
	return state, nil
}

func addAdjacency(sets map[string]map[string]bool, from, to string) {
	if sets[from] == nil {
		sets[from] = make(map[string]bool)
	}
	sets[from][to] = true
}

func sortedSet(set map[string]bool) []string {
	if len(set) == 0 {
		return []string{}
	}
	values := make([]string, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

// enumeratePaths performs a depth-first search over simple paths. It retains
// only the best limit paths seen so far, so memory is bounded by the caller's
// limit rather than by the total number of paths in the graph.
func (state *graphState) enumeratePaths(target string, policy, limit int) []Path {
	forward := state.forward[policy]
	distance := state.distancesTo(target, policy)

	result := make([]Path, 0, boundedCapacity(limit))
	overflow := false
	onPath := make(map[string]bool, len(state.components))
	current := make([]string, 0, len(state.components))

	var visit func(string)
	visit = func(node string) {
		if node == target {
			result, overflow = addBoundedPath(result, current, limit, overflow)
			return
		}

		remaining, reachable := distance[node]
		if !reachable {
			return
		}
		if overflow && len(result) >= limit {
			worst := result[len(result)-1]
			finalLength := len(current) + remaining
			if finalLength > len(worst.Nodes) {
				return
			}
			if finalLength == len(worst.Nodes) && prefixLexGreater(current, worst.Nodes) {
				return
			}
		}

		for _, next := range forward[node] {
			if onPath[next] {
				continue
			}
			onPath[next] = true
			current = append(current, next)
			visit(next)
			current = current[:len(current)-1]
			onPath[next] = false
		}
	}

	for _, root := range state.roots {
		onPath[root] = true
		current = append(current[:0], root)
		visit(root)
		onPath[root] = false
		current = current[:0]
	}

	complete := !overflow
	for i := range result {
		result[i].Complete = complete
	}
	return result
}

func boundedCapacity(limit int) int {
	if limit < 1024 {
		return limit
	}
	return 1024
}

func addBoundedPath(result []Path, nodes []string, limit int, overflow bool) ([]Path, bool) {
	if len(result) < limit {
		return insertPathNodes(result, nodes), overflow
	}

	overflow = true
	if nodesLess(nodes, result[len(result)-1].Nodes) {
		result = result[:len(result)-1]
		result = insertPathNodes(result, nodes)
	}
	return result, overflow
}

func insertPathNodes(sorted []Path, nodes []string) []Path {
	index := sort.Search(len(sorted), func(i int) bool {
		return nodesLess(nodes, sorted[i].Nodes)
	})
	sorted = append(sorted, Path{})
	copy(sorted[index+1:], sorted[index:])
	sorted[index] = Path{Nodes: append([]string(nil), nodes...)}
	return sorted
}

func nodesLess(left, right []string) bool {
	if len(left) != len(right) {
		return len(left) < len(right)
	}
	for i := range left {
		if left[i] != right[i] {
			return left[i] < right[i]
		}
	}
	return false
}

// prefixLexGreater reports whether prefix is lexicographically greater than
// the equally long prefix of full.
func prefixLexGreater(prefix, full []string) bool {
	length := len(prefix)
	if length > len(full) {
		length = len(full)
	}
	for i := 0; i < length; i++ {
		if prefix[i] != full[i] {
			return prefix[i] > full[i]
		}
	}
	return false
}

// distancesTo computes, for every node, the number of edges on a shortest
// path from that node to target under the active policy.
func (state *graphState) distancesTo(target string, policy int) map[string]int {
	distance := map[string]int{target: 0}
	queue := []string{target}
	for head := 0; head < len(queue); head++ {
		node := queue[head]
		for _, parent := range state.reverse[policy][node] {
			if _, seen := distance[parent]; seen {
				continue
			}
			distance[parent] = distance[node] + 1
			queue = append(queue, parent)
		}
	}
	return distance
}

func (state *graphState) shortestPath(target string, policy int) (Path, bool) {
	distance := state.distancesTo(target, policy)

	length := -1
	for _, root := range state.roots {
		rootDistance, reachable := distance[root]
		if !reachable {
			continue
		}
		if length == -1 || rootDistance < length {
			length = rootDistance
		}
	}
	if length == -1 {
		return Path{}, false
	}

	start := ""
	for _, root := range state.roots {
		if rootDistance, reachable := distance[root]; reachable && rootDistance == length {
			start = root
			break
		}
	}

	nodes := make([]string, 0, length+1)
	nodes = append(nodes, start)
	current := start
	for remaining := length; remaining > 0; remaining-- {
		next := ""
		for _, candidate := range state.forward[policy][current] {
			if candidateDistance, reachable := distance[candidate]; reachable && candidateDistance == remaining-1 {
				next = candidate
				break
			}
		}
		if next == "" {
			return Path{}, false
		}
		nodes = append(nodes, next)
		current = next
	}

	return Path{Nodes: nodes, Complete: true}, true
}
