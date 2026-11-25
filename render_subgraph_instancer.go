// Package task instantiates registered render sub-graphs into one frame graph
// and arbitrates the generations of cross-frame history resources.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
)

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

const (
	snapshotMagic   = "gsb-render-subgraph-instancer"
	snapshotVersion = 1
)

// SubGraphInstancer owns the sub-graph registry and the per graph instances.
//
// A single mutex serialises every state-changing submission (instantiation,
// binding, expansion and commit), so concurrent instantiation can never observe
// a duplicated or half-built instance.
type SubGraphInstancer struct {
	mu sync.Mutex

	options  InstancerOptions
	registry map[string]SubGraphDef

	generation  uint64
	knownGens   map[uint64]struct{}
	retiredGens map[uint64]struct{}

	nextInstanceID InstanceID
	nextHandleID   uint64

	instances map[InstanceID]*instanceState
	handles   map[uint64]HistoryHandle
	graphs    map[string]*graphState
}

type instanceState struct {
	id       InstanceID
	graph    string
	subGraph string
	// params holds the normalised effective values (defaults applied).
	params map[string]string
	// provided records which parameters were present in the request. Presence
	// matters on its own because an explicit "" must not merge with an omitted
	// parameter that only resolves to the default "".
	provided map[string]bool
	birthGen uint64
	history  map[string]HistoryHandle
}

type graphState struct {
	order []InstanceID
	dedup map[string]InstanceID

	passCount int
	nodes     []PassNode
	// ready reports a non-empty successful expansion that has not been
	// consumed by a commit yet.
	ready bool
}

// NewSubGraphInstancer builds an instancer with the given caps.
func NewSubGraphInstancer(options InstancerOptions) *SubGraphInstancer {
	s := &SubGraphInstancer{
		options:        options,
		registry:       make(map[string]SubGraphDef),
		knownGens:      make(map[uint64]struct{}),
		retiredGens:    make(map[uint64]struct{}),
		nextInstanceID: 1,
		nextHandleID:   1,
		instances:      make(map[InstanceID]*instanceState),
		handles:        make(map[uint64]HistoryHandle),
		graphs:         make(map[string]*graphState),
	}
	s.knownGens[0] = struct{}{}
	return s
}

func blank(value string) bool {
	return strings.TrimSpace(value) == ""
}

// initLocked makes the zero value usable as a convenience for callers that
// bypass NewSubGraphInstancer. The mutex itself is already usable as its zero
// value; everything else is initialised atomically under the lock.
func (s *SubGraphInstancer) initLocked() {
	if s.registry == nil {
		s.registry = make(map[string]SubGraphDef)
	}
	if s.knownGens == nil {
		s.knownGens = map[uint64]struct{}{0: {}}
	}
	if s.retiredGens == nil {
		s.retiredGens = make(map[uint64]struct{})
	}
	if s.instances == nil {
		s.instances = make(map[InstanceID]*instanceState)
	}
	if s.handles == nil {
		s.handles = make(map[uint64]HistoryHandle)
	}
	if s.graphs == nil {
		s.graphs = make(map[string]*graphState)
	}
	if s.nextInstanceID == 0 {
		s.nextInstanceID = 1
	}
	if s.nextHandleID == 0 {
		s.nextHandleID = 1
	}
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string(nil), values...)
}

func cloneNode(node PassNode) PassNode {
	node.Inputs = cloneStrings(node.Inputs)
	node.Outputs = cloneStrings(node.Outputs)
	return node
}

// instanceKey builds the collision-free deduplication identity of a request.
//
// It carries the sub-graph, the generation the request is born into, every
// declared parameter's presence bit and its effective value. Length prefixes
// keep arbitrary parameter values from colliding with the separators.
func instanceKey(subGraph string, generation uint64, decls []ParamDecl, params map[string]string, provided map[string]bool) string {
	var b strings.Builder
	part := func(value string) {
		b.WriteString(strconv.Itoa(len(value)))
		b.WriteByte(':')
		b.WriteString(value)
		b.WriteByte(';')
	}
	part(subGraph)
	part(strconv.FormatUint(generation, 10))
	for _, decl := range decls {
		part(decl.Name)
		if provided[decl.Name] {
			b.WriteByte('1')
		} else {
			b.WriteByte('0')
		}
		b.WriteByte(';')
		part(params[decl.Name])
	}
	return b.String()
}

// RegisterSubGraph stores one sub-graph definition under a unique name.
func (s *SubGraphInstancer) RegisterSubGraph(name string, def SubGraphDef) error {
	if blank(name) {
		return ErrBlankName
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.initLocked()
	if _, exists := s.registry[name]; exists {
		return ErrDuplicateSubGraph
	}
	if len(def.Passes) == 0 {
		return ErrEmptyDefinition
	}
	for _, pass := range def.Passes {
		if blank(pass.Name) {
			return ErrBadPass
		}
	}

	stored := SubGraphDef{
		Params: append([]ParamDecl(nil), def.Params...),
		Passes: make([]PassSpec, len(def.Passes)),
	}
	for i, pass := range def.Passes {
		stored.Passes[i] = PassSpec{
			Name:    pass.Name,
			History: pass.History,
			Inputs:  cloneStrings(pass.Inputs),
			Outputs: cloneStrings(pass.Outputs),
		}
	}
	s.registry[name] = stored
	return nil
}

// Instantiate binds one sub-graph into a graph with the given parameters.
func (s *SubGraphInstancer) Instantiate(graphID, subGraph string, params map[string]string) (InstanceID, error) {
	if blank(graphID) {
		return 0, ErrBlankGraph
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.initLocked()
	def, registered := s.registry[subGraph]
	if !registered {
		return 0, ErrUnknownSubGraph
	}

	declared := make(map[string]ParamDecl, len(def.Params))
	for _, decl := range def.Params {
		declared[decl.Name] = decl
	}

	// Presence based check: an explicitly supplied "" is still supplied.
	for _, decl := range def.Params {
		if decl.Required {
			if _, present := params[decl.Name]; !present {
				return 0, ErrMissingParam
			}
		}
	}

	provided := make(map[string]bool, len(params))
	effective := make(map[string]string, len(def.Params))
	for name, value := range params {
		if _, ok := declared[name]; !ok {
			return 0, ErrUnknownParam
		}
		provided[name] = true
		effective[name] = value
	}
	for _, decl := range def.Params {
		if !provided[decl.Name] {
			effective[decl.Name] = decl.Default
		}
	}

	key := instanceKey(subGraph, s.generation, def.Params, effective, provided)

	graph := s.graphs[graphID]
	if graph != nil {
		if existing, ok := graph.dedup[key]; ok {
			return existing, nil
		}
	}

	// Hard cap checked only for a genuinely new instance, after dedup.
	if s.options.MaxInstances > 0 && len(s.instances) >= s.options.MaxInstances {
		return 0, ErrInstanceLimit
	}

	id := s.nextInstanceID
	s.nextInstanceID++

	inst := &instanceState{
		id:       id,
		graph:    graphID,
		subGraph: subGraph,
		params:   effective,
		provided: provided,
		birthGen: s.generation,
		history:  make(map[string]HistoryHandle),
	}
	s.instances[id] = inst

	if graph == nil {
		graph = &graphState{dedup: make(map[string]InstanceID)}
		s.graphs[graphID] = graph
	}
	graph.order = append(graph.order, id)
	if graph.dedup == nil {
		graph.dedup = make(map[string]InstanceID)
	}
	graph.dedup[key] = id

	return id, nil
}

// BindHistory binds a history resource to one instance and returns its handle.
func (s *SubGraphInstancer) BindHistory(instanceID InstanceID, resource string) (HistoryHandle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.initLocked()
	inst, ok := s.instances[instanceID]
	if !ok {
		return HistoryHandle{}, ErrUnknownInstance
	}
	if blank(resource) {
		return HistoryHandle{}, ErrBadResource
	}

	if handle, ok := inst.history[resource]; ok {
		return handle, nil
	}

	handle := HistoryHandle{
		ID:         s.nextHandleID,
		Instance:   instanceID,
		Resource:   resource,
		Generation: s.generation,
	}
	s.nextHandleID++
	inst.history[resource] = handle
	s.handles[handle.ID] = handle
	return handle, nil
}

// Expand turns the instances of one graph into an ordered list of pass nodes.
func (s *SubGraphInstancer) Expand(graphID string) ([]PassNode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.initLocked()
	graph := s.graphs[graphID]
	if graph == nil || len(graph.order) == 0 {
		return []PassNode{}, nil
	}

	// Hard frame budget is a global gate so an over-budget request can never
	// publish a partial expansion.
	totalPasses := 0
	for _, id := range graph.order {
		inst := s.instances[id]
		def := s.registry[inst.subGraph]
		totalPasses += len(def.Passes)
	}
	if s.options.MaxPassesPerFrame > 0 && totalPasses > s.options.MaxPassesPerFrame {
		return nil, ErrPassBudget
	}

	// Build entirely off the published state. Any failure below simply discards
	// this local slice, leaving the previous successful expansion untouched.
	nodes := make([]PassNode, 0, totalPasses)
	ordinal := 0
	for _, id := range graph.order {
		inst := s.instances[id]
		def := s.registry[inst.subGraph]
		for _, pass := range def.Passes {
			if pass.History && len(inst.history) == 0 {
				return nil, ErrUnboundHistory
			}
			nodes = append(nodes, PassNode{
				Instance: id,
				SubGraph: inst.subGraph,
				Pass:     pass.Name,
				Ordinal:  ordinal,
				Inputs:   cloneStrings(pass.Inputs),
				Outputs:  cloneStrings(pass.Outputs),
			})
			ordinal++
		}
	}

	graph.nodes = nodes
	graph.passCount = len(nodes)
	graph.ready = true

	out := make([]PassNode, len(nodes))
	for i := range nodes {
		out[i] = cloneNode(nodes[i])
	}
	return out, nil
}

// CommitFrame publishes the expanded frame and advances the generation.
func (s *SubGraphInstancer) CommitFrame(graphID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.initLocked()
	graph := s.graphs[graphID]
	if graph == nil || !graph.ready || len(graph.nodes) == 0 {
		return ErrEmptyFrame
	}

	// Consume the pending expansion. A second commit without a fresh Expand is
	// therefore an empty frame.
	graph.ready = false

	s.generation++
	if s.knownGens == nil {
		s.knownGens = make(map[uint64]struct{})
	}
	s.knownGens[s.generation] = struct{}{}
	return nil
}

// RetireGeneration retires one history generation.
func (s *SubGraphInstancer) RetireGeneration(gen uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.initLocked()
	if gen == s.generation {
		return ErrGenerationActive
	}
	if _, known := s.knownGens[gen]; !known {
		return ErrUnknownGeneration
	}
	if s.retiredGens == nil {
		s.retiredGens = make(map[uint64]struct{})
	}
	s.retiredGens[gen] = struct{}{}
	return nil
}

// Instances returns the instances of a graph in creation order.
func (s *SubGraphInstancer) Instances(graphID string) []InstanceID {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.initLocked()
	graph := s.graphs[graphID]
	if graph == nil {
		return nil
	}
	return append([]InstanceID(nil), graph.order...)
}

// PassCount returns the pass count produced by the last successful expansion of
// one graph. A failed Expand leaves it at the previous successful value.
func (s *SubGraphInstancer) PassCount(graphID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.initLocked()
	graph := s.graphs[graphID]
	if graph == nil {
		return 0
	}
	return graph.passCount
}

// CurrentGeneration returns the generation new instances are born into.
func (s *SubGraphInstancer) CurrentGeneration() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.initLocked()
	return s.generation
}

// HandleAlive reports whether a history handle still refers to a live generation.
func (s *SubGraphInstancer) HandleAlive(h HistoryHandle) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.initLocked()
	stored, ok := s.handles[h.ID]
	if !ok || stored != h {
		return false
	}
	if _, retired := s.retiredGens[h.Generation]; retired {
		return false
	}
	if _, known := s.knownGens[h.Generation]; !known {
		return false
	}
	return true
}

// --- Snapshot / Restore -----------------------------------------------------

type snapshotEnvelope struct {
	Magic    string          `json:"magic"`
	Version  int             `json:"version"`
	Payload  json.RawMessage `json:"payload"`
	Checksum string          `json:"checksum"`
}

type snapshotData struct {
	Options        InstancerOptions       `json:"options"`
	Generation     uint64                 `json:"generation"`
	NextInstanceID InstanceID             `json:"next_instance_id"`
	NextHandleID   uint64                 `json:"next_handle_id"`
	KnownGens      []uint64               `json:"known_generations"`
	RetiredGens    []uint64               `json:"retired_generations"`
	Registry       map[string]SubGraphDef `json:"registry"`
	Instances      []instanceSnapshot     `json:"instances"`
	Graphs         []graphSnapshot        `json:"graphs"`
}

type instanceSnapshot struct {
	ID       InstanceID        `json:"id"`
	Graph    string            `json:"graph"`
	SubGraph string            `json:"sub_graph"`
	Params   map[string]string `json:"params"`
	Provided map[string]bool   `json:"provided"`
	BirthGen uint64            `json:"birth_generation"`
	History  []HistoryHandle   `json:"history"`
}

type graphSnapshot struct {
	ID        string       `json:"id"`
	Order     []InstanceID `json:"order"`
	PassCount int          `json:"pass_count"`
	Nodes     []PassNode   `json:"nodes"`
	Ready     bool         `json:"ready"`
}

// Snapshot serialises the whole instancer state.
func (s *SubGraphInstancer) Snapshot() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.initLocked()
	data := snapshotData{
		Options:        s.options,
		Generation:     s.generation,
		NextInstanceID: s.nextInstanceID,
		NextHandleID:   s.nextHandleID,
		Registry:       make(map[string]SubGraphDef, len(s.registry)),
	}

	for gen := range s.knownGens {
		data.KnownGens = append(data.KnownGens, gen)
	}
	for gen := range s.retiredGens {
		data.RetiredGens = append(data.RetiredGens, gen)
	}

	for name, def := range s.registry {
		data.Registry[name] = def
	}

	for id, inst := range s.instances {
		is := instanceSnapshot{
			ID:       id,
			Graph:    inst.graph,
			SubGraph: inst.subGraph,
			Params:   make(map[string]string, len(inst.params)),
			Provided: make(map[string]bool, len(inst.provided)),
			BirthGen: inst.birthGen,
		}
		for k, v := range inst.params {
			is.Params[k] = v
		}
		for k, v := range inst.provided {
			is.Provided[k] = v
		}
		for _, handle := range inst.history {
			is.History = append(is.History, handle)
		}
		data.Instances = append(data.Instances, is)
	}

	for id, graph := range s.graphs {
		gs := graphSnapshot{
			ID:        id,
			Order:     append([]InstanceID(nil), graph.order...),
			PassCount: graph.passCount,
			Ready:     graph.ready,
		}
		for _, node := range graph.nodes {
			gs.Nodes = append(gs.Nodes, cloneNode(node))
		}
		data.Graphs = append(data.Graphs, gs)
	}

	payload, err := json.Marshal(&data)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(payload)
	envelope := snapshotEnvelope{
		Magic:    snapshotMagic,
		Version:  snapshotVersion,
		Payload:  payload,
		Checksum: hex.EncodeToString(sum[:]),
	}
	return json.Marshal(&envelope)
}

// Restore replaces the whole instancer state from a snapshot.
func (s *SubGraphInstancer) Restore(raw []byte) error {
	restored, err := decodeSnapshot(raw)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.options = restored.options
	s.registry = restored.registry
	s.generation = restored.generation
	s.knownGens = restored.knownGens
	s.retiredGens = restored.retiredGens
	s.nextInstanceID = restored.nextInstanceID
	s.nextHandleID = restored.nextHandleID
	s.instances = restored.instances
	s.handles = restored.handles
	s.graphs = restored.graphs
	return nil
}

// decodeSnapshot parses and fully validates a payload into an independent,
// ready-to-publish state. It never touches the receiver, so a rejection leaves
// live memory untouched.
func decodeSnapshot(raw []byte) (*SubGraphInstancer, error) {
	if len(raw) == 0 {
		return nil, ErrBadSnapshot
	}

	var envelope snapshotEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, ErrBadSnapshot
	}
	if envelope.Magic != snapshotMagic || envelope.Version != snapshotVersion {
		return nil, ErrBadSnapshot
	}
	if len(envelope.Payload) == 0 || envelope.Checksum == "" {
		return nil, ErrBadSnapshot
	}
	sum := sha256.Sum256(envelope.Payload)
	if hex.EncodeToString(sum[:]) != envelope.Checksum {
		return nil, ErrBadSnapshot
	}

	var data snapshotData
	if err := json.Unmarshal(envelope.Payload, &data); err != nil {
		return nil, ErrBadSnapshot
	}

	c := NewSubGraphInstancer(data.Options)
	c.generation = data.Generation

	c.knownGens = make(map[uint64]struct{}, len(data.KnownGens)+1)
	c.knownGens[data.Generation] = struct{}{}
	for _, gen := range data.KnownGens {
		c.knownGens[gen] = struct{}{}
	}
	c.retiredGens = make(map[uint64]struct{}, len(data.RetiredGens))
	for _, gen := range data.RetiredGens {
		if _, known := c.knownGens[gen]; !known {
			return nil, ErrBadSnapshot
		}
		c.retiredGens[gen] = struct{}{}
	}

	c.registry = make(map[string]SubGraphDef, len(data.Registry))
	for name, def := range data.Registry {
		if blank(name) || len(def.Passes) == 0 {
			return nil, ErrBadSnapshot
		}
		for _, pass := range def.Passes {
			if blank(pass.Name) {
				return nil, ErrBadSnapshot
			}
		}
		c.registry[name] = def
	}

	var maxInstanceID InstanceID
	var maxHandleID uint64

	c.instances = make(map[InstanceID]*instanceState, len(data.Instances))
	c.handles = make(map[uint64]HistoryHandle)
	for _, is := range data.Instances {
		if is.ID == 0 {
			return nil, ErrBadSnapshot
		}
		if _, dup := c.instances[is.ID]; dup {
			return nil, ErrBadSnapshot
		}
		def, ok := c.registry[is.SubGraph]
		if !ok {
			return nil, ErrBadSnapshot
		}
		if is.Params == nil {
			is.Params = make(map[string]string)
		}
		if is.Provided == nil {
			is.Provided = make(map[string]bool)
		}

		inst := &instanceState{
			id:       is.ID,
			graph:    is.Graph,
			subGraph: is.SubGraph,
			params:   is.Params,
			provided: is.Provided,
			birthGen: is.BirthGen,
			history:  make(map[string]HistoryHandle),
		}

		for _, handle := range is.History {
			if handle.ID == 0 || handle.Instance != is.ID || handle.Resource == "" {
				return nil, ErrBadSnapshot
			}
			if existing, clash := c.handles[handle.ID]; clash && existing != handle {
				return nil, ErrBadSnapshot
			}
			if _, known := c.knownGens[handle.Generation]; !known {
				return nil, ErrBadSnapshot
			}
			inst.history[handle.Resource] = handle
			c.handles[handle.ID] = handle
			if handle.ID > maxHandleID {
				maxHandleID = handle.ID
			}
		}

		// Rebuild any defaulted values that an older payload might omit.
		for _, decl := range def.Params {
			if !inst.provided[decl.Name] {
				if _, present := inst.params[decl.Name]; !present {
					inst.params[decl.Name] = decl.Default
				}
			}
		}

		c.instances[is.ID] = inst
		if is.ID > maxInstanceID {
			maxInstanceID = is.ID
		}
	}

	c.graphs = make(map[string]*graphState, len(data.Graphs))
	for _, gs := range data.Graphs {
		if _, dup := c.graphs[gs.ID]; dup {
			return nil, ErrBadSnapshot
		}
		graph := &graphState{
			dedup:     make(map[string]InstanceID),
			passCount: gs.PassCount,
			nodes:     make([]PassNode, 0, len(gs.Nodes)),
			ready:     gs.Ready,
		}

		seen := make(map[InstanceID]struct{}, len(gs.Order))
		for _, id := range gs.Order {
			inst, ok := c.instances[id]
			if !ok || inst.graph != gs.ID {
				return nil, ErrBadSnapshot
			}
			if _, repeated := seen[id]; repeated {
				return nil, ErrBadSnapshot
			}
			seen[id] = struct{}{}
			graph.order = append(graph.order, id)

			def := c.registry[inst.subGraph]
			key := instanceKey(inst.subGraph, inst.birthGen, def.Params, inst.params, inst.provided)
			if other, exists := graph.dedup[key]; exists && other != id {
				return nil, ErrBadSnapshot
			}
			graph.dedup[key] = id
		}

		for _, node := range gs.Nodes {
			graph.nodes = append(graph.nodes, cloneNode(node))
		}
		c.graphs[gs.ID] = graph
	}

	c.nextInstanceID = data.NextInstanceID
	if c.nextInstanceID <= maxInstanceID {
		c.nextInstanceID = maxInstanceID + 1
	}
	c.nextHandleID = data.NextHandleID
	if c.nextHandleID <= maxHandleID {
		c.nextHandleID = maxHandleID + 1
	}

	return c, nil
}
