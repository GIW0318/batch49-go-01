// Package task attributes reachable vulnerable components in an SBOM
// dependency graph back to the owners that pull them in.
package task

import (
	"errors"
	"sync"
)

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

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
	return ""
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

// ReachAttributor owns the loaded graph and the active prune policy.
type ReachAttributor struct {
	mu sync.Mutex
}

// NewReachAttributor builds an attributor with no graph loaded.
func NewReachAttributor() *ReachAttributor {
	return &ReachAttributor{}
}

// Load validates and swaps in a whole dependency graph.
func (r *ReachAttributor) Load(graph Graph) error {
	return errNotImplemented
}

// SetPolicy replaces the prune policy used by later traversals.
func (r *ReachAttributor) SetPolicy(policy PrunePolicy) error {
	return errNotImplemented
}

// Policy returns the active prune policy.
func (r *ReachAttributor) Policy() PrunePolicy {
	return PrunePolicy{}
}

// Components lists the loaded component ids in ascending order.
func (r *ReachAttributor) Components() []string {
	return nil
}

// Roots lists the entry point ids in ascending order.
func (r *ReachAttributor) Roots() []string {
	return nil
}

// Node reads one component by id.
func (r *ReachAttributor) Node(id string) (Component, bool) {
	return Component{}, false
}

// PathsTo enumerates simple dependency paths to one component, shortest
// first and then lexicographic, truncated at limit.
func (r *ReachAttributor) PathsTo(component string, limit int) ([]Path, error) {
	return nil, errNotImplemented
}

// Introducers lists the distinct direct owners of one component.
func (r *ReachAttributor) Introducers(component string) []Owner {
	return nil
}

// Shortest returns the minimal path to one component.
func (r *ReachAttributor) Shortest(component string) (Path, error) {
	return Path{}, errNotImplemented
}

// Snapshot serialises the graph and the prune policy.
func (r *ReachAttributor) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (r *ReachAttributor) Restore(data []byte) error {
	return errNotImplemented
}
