// Package task instantiates registered render sub-graphs into one frame graph
// and arbitrates the generations of cross-frame history resources.
package task

import "errors"

// errNotImplemented marks behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// InstanceID identifies one instantiated sub-graph inside a graph.
type InstanceID uint64

// ParamDecl declares one instantiation parameter of a sub-graph.
type ParamDecl struct {
	Name     string `json:"name"`
	Default  string `json:"default"`
	Required bool   `json:"required"`
}

// PassSpec is one pass inside a sub-graph definition.
type PassSpec struct {
	Name    string   `json:"name"`
	Inputs  []string `json:"inputs"`
	Outputs []string `json:"outputs"`
	History bool     `json:"history"`
}

// SubGraphDef is the registered definition of a reusable sub-graph.
type SubGraphDef struct {
	Passes []PassSpec  `json:"passes"`
	Params []ParamDecl `json:"params"`
}

// PassNode is one expanded pass of the frame graph.
type PassNode struct {
	Instance InstanceID `json:"instance"`
	SubGraph string     `json:"sub_graph"`
	Pass     string     `json:"pass"`
	Ordinal  int        `json:"ordinal"`
	Inputs   []string   `json:"inputs"`
	Outputs  []string   `json:"outputs"`
}

// HistoryHandle refers to a history resource bound to one instance.
type HistoryHandle struct {
	ID         uint64     `json:"id"`
	Instance   InstanceID `json:"instance"`
	Resource   string     `json:"resource"`
	Generation uint64     `json:"generation"`
}

// InstancerOptions bounds the instancer tables.
type InstancerOptions struct {
	MaxInstances      int `json:"max_instances"`
	MaxPassesPerFrame int `json:"max_passes_per_frame"`
	MaxGenerations    int `json:"max_generations"`
}

// Errors returned by the instancer.
var (
	ErrBlankGraph        = errors.New("graph id is required")
	ErrBlankName         = errors.New("sub graph name is required")
	ErrDuplicateSubGraph = errors.New("sub graph already registered")
	ErrEmptyDefinition   = errors.New("sub graph needs at least one pass")
	ErrBadPass           = errors.New("pass name is required")
	ErrUnknownSubGraph   = errors.New("sub graph is not registered")
	ErrMissingParam      = errors.New("required parameter is missing")
	ErrUnknownParam      = errors.New("parameter is not declared")
	ErrInstanceLimit     = errors.New("instance limit reached")
	ErrUnknownInstance   = errors.New("instance is unknown")
	ErrBadResource       = errors.New("history resource name is required")
	ErrUnboundHistory    = errors.New("history pass has no bound resource")
	ErrPassBudget        = errors.New("frame pass budget exceeded")
	ErrEmptyFrame        = errors.New("frame has no expanded passes")
	ErrGenerationActive  = errors.New("generation is still active")
	ErrUnknownGeneration = errors.New("generation was never issued")
	ErrBadSnapshot       = errors.New("snapshot payload is invalid")
)

// SubGraphInstancer owns the sub-graph registry and the per graph instances.
type SubGraphInstancer struct {
	options InstancerOptions
}

// NewSubGraphInstancer builds an instancer with the given caps.
func NewSubGraphInstancer(options InstancerOptions) *SubGraphInstancer {
	return &SubGraphInstancer{options: options}
}

// RegisterSubGraph stores one sub-graph definition under a unique name.
func (s *SubGraphInstancer) RegisterSubGraph(name string, def SubGraphDef) error {
	return errNotImplemented
}

// Instantiate binds one sub-graph into a graph with the given parameters.
func (s *SubGraphInstancer) Instantiate(graphID, subGraph string, params map[string]string) (InstanceID, error) {
	return 0, errNotImplemented
}

// BindHistory binds a history resource to one instance and returns its handle.
func (s *SubGraphInstancer) BindHistory(instanceID InstanceID, resource string) (HistoryHandle, error) {
	return HistoryHandle{}, errNotImplemented
}

// Expand turns the instances of one graph into an ordered list of pass nodes.
func (s *SubGraphInstancer) Expand(graphID string) ([]PassNode, error) {
	return nil, errNotImplemented
}

// CommitFrame publishes the expanded frame and advances the generation.
func (s *SubGraphInstancer) CommitFrame(graphID string) error {
	return errNotImplemented
}

// RetireGeneration retires one history generation.
func (s *SubGraphInstancer) RetireGeneration(gen uint64) error {
	return errNotImplemented
}

// Instances returns the instances of a graph in creation order.
func (s *SubGraphInstancer) Instances(graphID string) []InstanceID {
	return nil
}

// PassCount returns the committed pass count of one graph.
func (s *SubGraphInstancer) PassCount(graphID string) int {
	return 0
}

// CurrentGeneration returns the generation new instances are born into.
func (s *SubGraphInstancer) CurrentGeneration() uint64 {
	return 0
}

// HandleAlive reports whether a history handle still refers to a live generation.
func (s *SubGraphInstancer) HandleAlive(h HistoryHandle) bool {
	return false
}

// Snapshot serialises the whole instancer state.
func (s *SubGraphInstancer) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole instancer state from a snapshot.
func (s *SubGraphInstancer) Restore(data []byte) error {
	return errNotImplemented
}
