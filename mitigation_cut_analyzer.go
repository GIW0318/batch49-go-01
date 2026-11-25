// Package task computes which capability chains survive a set of deployed
// mitigations and which smallest mitigation set cuts every remaining chain.
package task

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
)

// State is one node in the capability graph.
type State string

// Edge is one atomic capability edge.
type Edge struct {
	ID   string `json:"id"`
	From State  `json:"from"`
	To   State  `json:"to"`
}

// Chain is one simple path from the source to the goal.
type Chain struct {
	Nodes []string `json:"nodes"`
	Edges []string `json:"edges"`
}

// Mitigation is one deployed countermeasure. Covers lists the edges it fully
// blocks, Partial lists the edges where it only removes a precondition, and
// Conflicts names the mitigations that may not be selected together with it.
type Mitigation struct {
	Name      string   `json:"name"`
	Cost      int      `json:"cost"`
	Covers    []string `json:"covers"`
	Partial   []string `json:"partial"`
	Conflicts []string `json:"conflicts"`
}

// DefaultMaxCandidates caps the candidate set accepted by MinCut.
const DefaultMaxCandidates = 12

// Errors returned by the analyzer.
var (
	ErrUnknownEdge       = errors.New("unknown edge")
	ErrNotCovered        = errors.New("edge is not covered by this mitigation")
	ErrUnknownState      = errors.New("unknown state")
	ErrUncuttable        = errors.New("no candidate set cuts every path")
	ErrCandidateLimit    = errors.New("too many candidates")
	ErrConflict          = errors.New("mitigations conflict with each other")
	ErrBadSnapshot       = errors.New("snapshot payload is invalid")
	ErrBadMitigation     = errors.New("mitigation definition is invalid")
	ErrUnknownMitigation = errors.New("unknown mitigation")
)

// MitigationCutAnalyzer owns the capability graph, the applied blocks and the
// queries over the residual chains.
type MitigationCutAnalyzer struct {
	mu            sync.RWMutex
	source        State
	goal          State
	edges         []Edge
	index         map[string]Edge
	adjacency     map[State][]Edge
	states        map[State]bool
	mitigations   map[string]Mitigation
	blocked       map[string]bool
	partial       map[string]map[string]bool
	maxCandidates int
}

// NewMitigationCutAnalyzer builds an analyzer for the given graph and the
// default source/goal pair, accepting up to DefaultMaxCandidates candidates.
func NewMitigationCutAnalyzer(source, goal State, edges []Edge) *MitigationCutAnalyzer {
	return NewMitigationCutAnalyzerWithLimit(source, goal, edges, DefaultMaxCandidates)
}

// NewMitigationCutAnalyzerWithLimit is NewMitigationCutAnalyzer with an explicit
// candidate cap for MinCut.
func NewMitigationCutAnalyzerWithLimit(source, goal State, edges []Edge, maxCandidates int) *MitigationCutAnalyzer {
	if maxCandidates <= 0 {
		maxCandidates = DefaultMaxCandidates
	}

	a := &MitigationCutAnalyzer{
		source:        source,
		goal:          goal,
		edges:         append([]Edge(nil), edges...),
		index:         make(map[string]Edge),
		adjacency:     make(map[State][]Edge),
		states:        make(map[State]bool),
		mitigations:   make(map[string]Mitigation),
		blocked:       make(map[string]bool),
		partial:       make(map[string]map[string]bool),
		maxCandidates: maxCandidates,
	}
	a.rebuildLocked()
	return a
}

// Source reports the configured default source state.
func (a *MitigationCutAnalyzer) Source() State { return a.source }

// Goal reports the configured default goal state.
func (a *MitigationCutAnalyzer) Goal() State { return a.goal }

// Edges lists the graph edges ordered by edge ID.
func (a *MitigationCutAnalyzer) Edges() []Edge {
	a.mu.RLock()
	defer a.mu.RUnlock()

	out := append([]Edge(nil), a.edges...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// AddEdge appends one edge to the graph.
func (a *MitigationCutAnalyzer) AddEdge(e Edge) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if e.ID == "" {
		return ErrBadMitigation
	}
	if _, exists := a.index[e.ID]; exists {
		return ErrBadMitigation
	}

	a.edges = append(a.edges, e)
	a.index[e.ID] = e
	a.adjacency[e.From] = append(a.adjacency[e.From], e)
	sort.Slice(a.adjacency[e.From], func(i, j int) bool {
		return a.adjacency[e.From][i].ID < a.adjacency[e.From][j].ID
	})
	a.states[e.From] = true
	a.states[e.To] = true
	return nil
}

// Register stores a mitigation definition so that later calls may refer to it
// by name only.
func (a *MitigationCutAnalyzer) Register(m Mitigation) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	m = normalizeMitigation(m)
	if m.Name == "" {
		return ErrBadMitigation
	}
	for _, id := range append(append([]string(nil), m.Covers...), m.Partial...) {
		if _, ok := a.index[id]; !ok {
			return ErrUnknownEdge
		}
	}
	a.mitigations[m.Name] = m
	return nil
}

// Mitigations lists the registered mitigation names in ascending order.
func (a *MitigationCutAnalyzer) Mitigations() []string {
	a.mu.RLock()
	defer a.mu.RUnlock()

	names := make([]string, 0, len(a.mitigations))
	for name := range a.mitigations {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Block applies one mitigation to the given edges. Blocks are additive: the
// same mitigation applied to different edges counts once per edge, and
// repeating the same edge is a no-op.
func (a *MitigationCutAnalyzer) Block(m Mitigation, edges []string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	m, err := a.resolveMitigationLocked(m, true)
	if err != nil {
		return err
	}

	covers := stringSet(m.Covers)
	partial := stringSet(m.Partial)

	// Validate the whole request before mutating any state.
	for _, id := range edges {
		if _, ok := a.index[id]; !ok {
			return ErrUnknownEdge
		}
		if !covers[id] && !partial[id] {
			return ErrNotCovered
		}
	}

	for _, id := range edges {
		if covers[id] {
			a.blocked[id] = true
			continue
		}
		if a.partial[id] == nil {
			a.partial[id] = make(map[string]bool)
		}
		a.partial[id][m.Name] = true
	}

	a.rememberMitigationLocked(m)
	return nil
}

// Apply applies a whole mitigation set. The call is atomic: on error no block
// from the set is kept.
func (a *MitigationCutAnalyzer) Apply(set []Mitigation) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	resolved := make([]Mitigation, 0, len(set))
	for _, m := range set {
		rm, err := a.resolveMitigationLocked(m, true)
		if err != nil {
			return err
		}
		resolved = append(resolved, rm)
	}

	// Conflicts are checked before touching the applied state.
	for i := range resolved {
		for j := i + 1; j < len(resolved); j++ {
			if resolved[i].Name == resolved[j].Name {
				continue
			}
			if namesContain(resolved[i].Conflicts, resolved[j].Name) ||
				namesContain(resolved[j].Conflicts, resolved[i].Name) {
				return ErrConflict
			}
		}
	}

	// Validate every edge before mutating any state.
	for _, m := range resolved {
		for _, id := range append(append([]string(nil), m.Covers...), m.Partial...) {
			if _, ok := a.index[id]; !ok {
				return ErrUnknownEdge
			}
		}
	}

	for _, m := range resolved {
		a.applyMitigationLocked(m)
		a.rememberMitigationLocked(m)
	}
	return nil
}

// Paths enumerates every simple path from from to goal over unblocked edges,
// ordered by the joined edge ID sequence.
func (a *MitigationCutAnalyzer) Paths(from, goal State) ([]Chain, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.pathsLocked(from, goal)
}

// Residual enumerates the chains still reaching goal from the default source
// after the applied blocks.
func (a *MitigationCutAnalyzer) Residual(goal State) ([]Chain, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.pathsLocked(a.source, goal)
}

// MinCut returns the smallest candidate set that cuts every path from from to
// goal. Equal size is broken by lower total cost, then by name order.
func (a *MitigationCutAnalyzer) MinCut(from, goal State, candidates []Mitigation) ([]Mitigation, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if len(candidates) > a.maxCandidates {
		return []Mitigation{}, ErrCandidateLimit
	}

	cands, err := a.canonicalCandidatesLocked(candidates)
	if err != nil {
		return []Mitigation{}, err
	}

	paths, err := a.pathsLocked(from, goal)
	if err != nil {
		return []Mitigation{}, err
	}
	if len(paths) == 0 {
		return []Mitigation{}, nil
	}

	coveredEdges := make([]map[string]bool, len(cands))
	for i, m := range cands {
		coveredEdges[i] = stringSet(m.Covers)
	}

	choices := make([][]int, len(paths))
	for pi, ch := range paths {
		onPath := stringSet(ch.Edges)
		for ci := range cands {
			for id := range coveredEdges[ci] {
				if onPath[id] {
					choices[pi] = append(choices[pi], ci)
					break
				}
			}
		}
		if len(choices[pi]) == 0 {
			return []Mitigation{}, ErrUncuttable
		}
	}

	conflicts := conflictMatrix(cands)

	selected := make([]bool, len(cands))
	selectedList := make([]int, 0, len(cands))
	bestSize := len(cands) + 1
	bestCost := 0
	bestNames := []string(nil)
	bestSelection := []int(nil)

	var search func()
	search = func() {
		allCovered := true
		chosenPath := -1
		var chosenOptions []int

		for pi := range paths {
			if pathIsCovered(pi, choices, selected) {
				continue
			}
			allCovered = false

			options := make([]int, 0, len(choices[pi]))
			for _, ci := range choices[pi] {
				if selected[ci] || conflictsWithSelected(ci, selectedList, conflicts) {
					continue
				}
				options = append(options, ci)
			}
			if len(options) == 0 {
				return
			}
			if chosenPath == -1 || len(options) < len(chosenOptions) {
				chosenPath = pi
				chosenOptions = options
			}
		}

		if allCovered {
			names := selectionNames(selectedList, cands)
			cost := selectionCost(selectedList, cands)
			if len(selectedList) < bestSize ||
				(len(selectedList) == bestSize && (cost < bestCost ||
					(cost == bestCost && stringSliceLess(names, bestNames)))) {
				bestSize = len(selectedList)
				bestCost = cost
				bestNames = append([]string(nil), names...)
				bestSelection = append([]int(nil), selectedList...)
			}
			return
		}

		// Any completion adds at least one mitigation.
		if len(selectedList) >= bestSize {
			return
		}

		for _, ci := range chosenOptions {
			selected[ci] = true
			selectedList = append(selectedList, ci)
			search()
			selectedList = selectedList[:len(selectedList)-1]
			selected[ci] = false
		}
	}
	search()

	if bestSelection == nil {
		return []Mitigation{}, ErrUncuttable
	}

	result := make([]Mitigation, 0, len(bestSelection))
	for _, ci := range bestSelection {
		result = append(result, cloneMitigation(cands[ci]))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

// Impact counts how many of the residual chains to the default goal are cut
// when m is applied on its own.
func (a *MitigationCutAnalyzer) Impact(m Mitigation) int {
	a.mu.RLock()
	defer a.mu.RUnlock()

	m, err := a.resolveMitigationLocked(m, false)
	if err != nil {
		return 0
	}
	paths, err := a.pathsLocked(a.source, a.goal)
	if err != nil {
		return 0
	}

	covers := stringSet(m.Covers)
	count := 0
	for _, ch := range paths {
		for _, id := range ch.Edges {
			if covers[id] {
				count++
				break
			}
		}
	}
	return count
}

// BlockedEdges lists the blocked edge IDs in ascending order.
func (a *MitigationCutAnalyzer) BlockedEdges() []string {
	a.mu.RLock()
	defer a.mu.RUnlock()

	out := make([]string, 0, len(a.blocked))
	for id := range a.blocked {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

type snapshotPayload struct {
	Version     int                 `json:"version"`
	Blocked     []string            `json:"blocked"`
	Partial     map[string][]string `json:"partial,omitempty"`
	Mitigations []Mitigation        `json:"mitigations"`
}

const snapshotVersion = 1

// Snapshot serialises the applied blocks and registered mitigations.
func (a *MitigationCutAnalyzer) Snapshot() ([]byte, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	p := snapshotPayload{
		Version: snapshotVersion,
		Blocked: make([]string, 0, len(a.blocked)),
		Partial: make(map[string][]string, len(a.partial)),
	}
	for id := range a.blocked {
		p.Blocked = append(p.Blocked, id)
	}
	sort.Strings(p.Blocked)

	for id, names := range a.partial {
		list := make([]string, 0, len(names))
		for name := range names {
			list = append(list, name)
		}
		sort.Strings(list)
		p.Partial[id] = list
	}

	names := make([]string, 0, len(a.mitigations))
	for name := range a.mitigations {
		names = append(names, name)
	}
	sort.Strings(names)
	p.Mitigations = make([]Mitigation, 0, len(names))
	for _, name := range names {
		p.Mitigations = append(p.Mitigations, cloneMitigation(a.mitigations[name]))
	}

	return json.Marshal(p)
}

// Restore replaces the current blocks and registered mitigations from a
// snapshot. A corrupt payload leaves the analyzer untouched.
func (a *MitigationCutAnalyzer) Restore(data []byte) error {
	if len(data) == 0 {
		return ErrBadSnapshot
	}

	var p snapshotPayload
	if err := json.Unmarshal(data, &p); err != nil || p.Version != snapshotVersion {
		return ErrBadSnapshot
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	blocked := make(map[string]bool, len(p.Blocked))
	for _, id := range p.Blocked {
		if _, ok := a.index[id]; !ok {
			return ErrBadSnapshot
		}
		blocked[id] = true
	}

	partial := make(map[string]map[string]bool, len(p.Partial))
	for id, names := range p.Partial {
		if _, ok := a.index[id]; !ok {
			return ErrBadSnapshot
		}
		set := make(map[string]bool, len(names))
		for _, name := range names {
			if name == "" {
				return ErrBadSnapshot
			}
			set[name] = true
		}
		partial[id] = set
	}

	mitigations := make(map[string]Mitigation, len(p.Mitigations))
	for _, m := range p.Mitigations {
		m = normalizeMitigation(m)
		if m.Name == "" {
			return ErrBadSnapshot
		}
		for _, id := range append(append([]string(nil), m.Covers...), m.Partial...) {
			if _, ok := a.index[id]; !ok {
				return ErrBadSnapshot
			}
		}
		mitigations[m.Name] = m
	}

	a.blocked = blocked
	a.partial = partial
	a.mitigations = mitigations
	return nil
}

// Reset drops every block, registration and edge.
func (a *MitigationCutAnalyzer) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.edges = nil
	a.index = make(map[string]Edge)
	a.adjacency = make(map[State][]Edge)
	a.states = make(map[State]bool)
	a.mitigations = make(map[string]Mitigation)
	a.blocked = make(map[string]bool)
	a.partial = make(map[string]map[string]bool)
}

func (a *MitigationCutAnalyzer) rebuildLocked() {
	a.index = make(map[string]Edge, len(a.edges))
	a.adjacency = make(map[State][]Edge, len(a.edges))
	a.states = make(map[State]bool, len(a.edges))

	unique := make([]Edge, 0, len(a.edges))
	for _, e := range a.edges {
		if _, exists := a.index[e.ID]; exists {
			continue
		}
		unique = append(unique, e)
		a.index[e.ID] = e
		a.adjacency[e.From] = append(a.adjacency[e.From], e)
		a.states[e.From] = true
		a.states[e.To] = true
	}
	a.edges = unique

	for from := range a.adjacency {
		sort.Slice(a.adjacency[from], func(i, j int) bool {
			return a.adjacency[from][i].ID < a.adjacency[from][j].ID
		})
	}
}

func (a *MitigationCutAnalyzer) pathsLocked(from, goal State) ([]Chain, error) {
	if !a.states[from] || !a.states[goal] {
		return nil, ErrUnknownState
	}

	result := make([]Chain, 0)
	nodes := []string{string(from)}
	edgePath := make([]string, 0)
	visited := map[State]bool{from: true}

	var walk func(State)
	walk = func(node State) {
		if node == goal {
			result = append(result, Chain{
				Nodes: append([]string(nil), nodes...),
				Edges: append([]string(nil), edgePath...),
			})
			return
		}

		for _, e := range a.adjacency[node] {
			if a.blocked[e.ID] || visited[e.To] {
				continue
			}
			visited[e.To] = true
			nodes = append(nodes, string(e.To))
			edgePath = append(edgePath, e.ID)

			walk(e.To)

			edgePath = edgePath[:len(edgePath)-1]
			nodes = nodes[:len(nodes)-1]
			delete(visited, e.To)
		}
	}
	walk(from)

	sort.Slice(result, func(i, j int) bool {
		return stringSliceLess(result[i].Edges, result[j].Edges)
	})
	return result, nil
}

func (a *MitigationCutAnalyzer) resolveMitigationLocked(m Mitigation, strict bool) (Mitigation, error) {
	if m.Name == "" {
		return Mitigation{}, ErrBadMitigation
	}
	if len(m.Covers) == 0 && len(m.Partial) == 0 && len(m.Conflicts) == 0 {
		registered, ok := a.mitigations[m.Name]
		if ok {
			return cloneMitigation(registered), nil
		}
		if strict {
			return Mitigation{}, ErrUnknownMitigation
		}
		return normalizeMitigation(m), nil
	}
	return normalizeMitigation(m), nil
}

func (a *MitigationCutAnalyzer) applyMitigationLocked(m Mitigation) {
	covers := stringSet(m.Covers)
	for id := range covers {
		a.blocked[id] = true
	}
	for _, id := range m.Partial {
		if covers[id] {
			continue
		}
		if a.partial[id] == nil {
			a.partial[id] = make(map[string]bool)
		}
		a.partial[id][m.Name] = true
	}
}

func (a *MitigationCutAnalyzer) rememberMitigationLocked(m Mitigation) {
	if m.Name == "" {
		return
	}
	normalized := normalizeMitigation(m)
	for _, id := range append(append([]string(nil), normalized.Covers...), normalized.Partial...) {
		if _, ok := a.index[id]; !ok {
			return
		}
	}
	a.mitigations[normalized.Name] = normalized
}

func (a *MitigationCutAnalyzer) canonicalCandidatesLocked(candidates []Mitigation) ([]Mitigation, error) {
	resolved := make([]Mitigation, 0, len(candidates))
	for _, m := range candidates {
		rm, err := a.resolveMitigationLocked(m, false)
		if err != nil {
			return nil, err
		}
		for _, id := range append(append([]string(nil), rm.Covers...), rm.Partial...) {
			if _, ok := a.index[id]; !ok {
				return nil, ErrUnknownEdge
			}
		}
		resolved = append(resolved, rm)
	}

	sort.Slice(resolved, func(i, j int) bool {
		if resolved[i].Name != resolved[j].Name {
			return resolved[i].Name < resolved[j].Name
		}
		if resolved[i].Cost != resolved[j].Cost {
			return resolved[i].Cost < resolved[j].Cost
		}
		return mitigationSignature(resolved[i]) < mitigationSignature(resolved[j])
	})

	unique := make([]Mitigation, 0, len(resolved))
	seen := make(map[string]bool, len(resolved))
	for _, m := range resolved {
		if seen[m.Name] {
			continue
		}
		seen[m.Name] = true
		unique = append(unique, m)
	}
	return unique, nil
}

func pathIsCovered(path int, choices [][]int, selected []bool) bool {
	for _, ci := range choices[path] {
		if selected[ci] {
			return true
		}
	}
	return false
}

func conflictsWithSelected(candidate int, selected []int, conflicts [][]bool) bool {
	for _, other := range selected {
		if conflicts[candidate][other] {
			return true
		}
	}
	return false
}

func conflictMatrix(cands []Mitigation) [][]bool {
	matrix := make([][]bool, len(cands))
	for i := range matrix {
		matrix[i] = make([]bool, len(cands))
	}
	for i := range cands {
		for j := i + 1; j < len(cands); j++ {
			if namesContain(cands[i].Conflicts, cands[j].Name) ||
				namesContain(cands[j].Conflicts, cands[i].Name) {
				matrix[i][j] = true
				matrix[j][i] = true
			}
		}
	}
	return matrix
}

func selectionNames(selection []int, cands []Mitigation) []string {
	names := make([]string, 0, len(selection))
	for _, ci := range selection {
		names = append(names, cands[ci].Name)
	}
	sort.Strings(names)
	return names
}

func selectionCost(selection []int, cands []Mitigation) int {
	cost := 0
	for _, ci := range selection {
		cost += cands[ci].Cost
	}
	return cost
}

func normalizeMitigation(m Mitigation) Mitigation {
	return Mitigation{
		Name:      m.Name,
		Cost:      m.Cost,
		Covers:    uniqueSorted(m.Covers),
		Partial:   uniqueSorted(m.Partial),
		Conflicts: uniqueSorted(m.Conflicts),
	}
}

func cloneMitigation(m Mitigation) Mitigation {
	return Mitigation{
		Name:      m.Name,
		Cost:      m.Cost,
		Covers:    append([]string(nil), m.Covers...),
		Partial:   append([]string(nil), m.Partial...),
		Conflicts: append([]string(nil), m.Conflicts...),
	}
}

func uniqueSorted(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	set := make(map[string]bool, len(values))
	for _, v := range values {
		set[v] = true
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func stringSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, v := range values {
		set[v] = true
	}
	return set
}

func namesContain(names []string, target string) bool {
	for _, name := range names {
		if name == target {
			return true
		}
	}
	return false
}

func stringSliceLess(a, b []string) bool {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return len(a) < len(b)
}

func mitigationSignature(m Mitigation) string {
	return strings.Join(m.Covers, "\x00") + "\x01" +
		strings.Join(m.Partial, "\x00") + "\x01" +
		strings.Join(m.Conflicts, "\x00")
}
