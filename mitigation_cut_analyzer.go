// Package task computes which capability chains survive a set of deployed
// mitigations and which smallest mitigation set cuts every remaining chain.
package task

import (
	"errors"
	"sync"
)

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

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
	mu            sync.Mutex
	source        State
	goal          State
	edges         []Edge
	index         map[string]Edge
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
	return &MitigationCutAnalyzer{
		source:        source,
		goal:          goal,
		edges:         append([]Edge(nil), edges...),
		maxCandidates: maxCandidates,
	}
}

// Source reports the configured default source state.
func (a *MitigationCutAnalyzer) Source() State { return a.source }

// Goal reports the configured default goal state.
func (a *MitigationCutAnalyzer) Goal() State { return a.goal }

// Edges lists the graph edges ordered by edge ID.
func (a *MitigationCutAnalyzer) Edges() []Edge { return nil }

// AddEdge appends one edge to the graph.
func (a *MitigationCutAnalyzer) AddEdge(e Edge) error { return errNotImplemented }

// Register stores a mitigation definition so that later calls may refer to it
// by name only.
func (a *MitigationCutAnalyzer) Register(m Mitigation) error { return errNotImplemented }

// Mitigations lists the registered mitigation names in ascending order.
func (a *MitigationCutAnalyzer) Mitigations() []string { return nil }

// Block applies one mitigation to the given edges. Blocks are additive: the
// same mitigation applied to different edges counts once per edge, and
// repeating the same edge is a no-op.
func (a *MitigationCutAnalyzer) Block(m Mitigation, edges []string) error {
	return errNotImplemented
}

// Apply applies a whole mitigation set. The call is atomic: on error no block
// from the set is kept.
func (a *MitigationCutAnalyzer) Apply(set []Mitigation) error { return errNotImplemented }

// Paths enumerates every simple path from from to goal over unblocked edges,
// ordered by the joined edge ID sequence.
func (a *MitigationCutAnalyzer) Paths(from, goal State) ([]Chain, error) {
	return nil, errNotImplemented
}

// Residual enumerates the chains still reaching goal from the default source
// after the applied blocks.
func (a *MitigationCutAnalyzer) Residual(goal State) ([]Chain, error) { return nil, errNotImplemented }

// MinCut returns the smallest candidate set that cuts every path from from to
// goal. Equal size is broken by lower total cost, then by name order.
func (a *MitigationCutAnalyzer) MinCut(from, goal State, candidates []Mitigation) ([]Mitigation, error) {
	return nil, errNotImplemented
}

// Impact counts how many of the residual chains to the default goal are cut
// when m is applied on its own.
func (a *MitigationCutAnalyzer) Impact(m Mitigation) int { return 0 }

// BlockedEdges lists the blocked edge IDs in ascending order.
func (a *MitigationCutAnalyzer) BlockedEdges() []string { return nil }

// Snapshot serialises the applied blocks and registered mitigations.
func (a *MitigationCutAnalyzer) Snapshot() ([]byte, error) { return nil, errNotImplemented }

// Restore replaces the current blocks and registered mitigations from a
// snapshot. A corrupt payload leaves the analyzer untouched.
func (a *MitigationCutAnalyzer) Restore(data []byte) error { return errNotImplemented }

// Reset drops every block, registration and edge.
func (a *MitigationCutAnalyzer) Reset() {}
