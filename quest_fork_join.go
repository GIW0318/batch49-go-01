// Package task runs a quest chain as a dependency graph: forks unlock several
// parallel branches and joins resolve by all or by a quorum of them.
package task

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
)

// Node states of one chain for one player.
const (
	NodePending  = "pending"
	NodeActive   = "active"
	NodeDone     = "done"
	NodeCanceled = "canceled"
)

// Errors returned by the chain runtime.
var (
	ErrBlankArg        = errors.New("argument is required")
	ErrBadChain        = errors.New("chain definition is invalid")
	ErrDuplicateChain  = errors.New("chain id already defined")
	ErrUnknownChain    = errors.New("chain is not defined")
	ErrUnknownNode     = errors.New("node is not defined")
	ErrCycle           = errors.New("chain definition contains a dependency cycle")
	ErrNodeNotReady    = errors.New("node dependencies are not satisfied")
	ErrAlreadyDone     = errors.New("node is already completed")
	ErrAlreadyCanceled = errors.New("node is already canceled")
	ErrNotJoin         = errors.New("node has no dependencies to resolve")
	ErrChainLimit      = errors.New("chain limit reached")
	ErrNodeLimit       = errors.New("node limit reached")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// NodeSpec defines one node of a fork join chain.
type NodeSpec struct {
	ID        string   `json:"id"`
	DependsOn []string `json:"depends_on"`
	Quorum    int      `json:"quorum"`
	Reward    string   `json:"reward"`
}

// Chain is one fork join chain definition.
type Chain struct {
	ID    string     `json:"id"`
	Nodes []NodeSpec `json:"nodes"`
}

// Options bounds the definition tables.
type Options struct {
	MaxChains int
	MaxNodes  int
}

// NodeState is the state of one node for one player.
type NodeState struct {
	NodeID string `json:"node_id"`
	Status string `json:"status"`
	Seq    int64  `json:"seq"`
}

// JoinResult is the quorum verdict of one dependency node.
type JoinResult struct {
	JoinID    string   `json:"join_id"`
	Resolved  bool     `json:"resolved"`
	Quorum    int      `json:"quorum"`
	Satisfied int      `json:"satisfied"`
	Rewards   []string `json:"rewards"`
}

// ChainState is the full per player view of one chain.
type ChainState struct {
	ChainID string       `json:"chain_id"`
	Nodes   []NodeState  `json:"nodes"`
	Joins   []JoinResult `json:"joins"`
}

type nodeDef struct {
	id              string
	dependsOn       []string
	quorum          int
	effectiveQuorum int
	reward          string
}

type chainDef struct {
	id    string
	nodes []*nodeDef
	byID  map[string]*nodeDef
}

type playerNodeState struct {
	status string
	seq    int64
}

type playerChainState struct {
	nodes   map[string]*playerNodeState
	nextSeq int64
	rewards []string
}

// ForkJoinChain owns the chain definitions and the per player runtime state.
type ForkJoinChain struct {
	mu         sync.Mutex
	options    Options
	chains     map[string]*chainDef
	chainOrder []string
	players    map[string]map[string]*playerChainState
}

// NewForkJoinChain builds a runtime with the given caps.
func NewForkJoinChain(options Options) *ForkJoinChain {
	return &ForkJoinChain{
		options: options,
		chains:  make(map[string]*chainDef),
		players: make(map[string]map[string]*playerChainState),
	}
}

// DefineChain registers one fork join chain definition.
func (c *ForkJoinChain) DefineChain(chain Chain) error {
	def, err := buildChainDef(chain, c.options.MaxNodes)
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.chains[def.id]; exists {
		return ErrDuplicateChain
	}
	if c.options.MaxChains > 0 && len(c.chains) >= c.options.MaxChains {
		return ErrChainLimit
	}
	c.chains[def.id] = def
	c.chainOrder = append(c.chainOrder, def.id)
	return nil
}

// Complete marks one node done for a player and grants its reward.
func (c *ForkJoinChain) Complete(player, chainID, nodeID string, at int64) (NodeState, error) {
	if strings.TrimSpace(player) == "" {
		return NodeState{}, ErrBlankArg
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	def, ok := c.chains[chainID]
	if !ok {
		return NodeState{}, ErrUnknownChain
	}
	node, ok := def.byID[nodeID]
	if !ok {
		return NodeState{}, ErrUnknownNode
	}

	state := c.ensureState(player, def)
	refreshStates(def, state)
	nodeState := state.nodes[nodeID]
	switch nodeState.status {
	case NodeDone:
		return NodeState{}, ErrAlreadyDone
	case NodeCanceled:
		return NodeState{}, ErrAlreadyCanceled
	}
	if !dependenciesSatisfied(node, state) {
		return NodeState{}, ErrNodeNotReady
	}

	nodeState.status = NodeDone
	nodeState.seq = state.nextSeq
	state.nextSeq++
	if node.reward != "" {
		state.rewards = append(state.rewards, node.reward)
	}
	refreshStates(def, state)

	return NodeState{NodeID: nodeID, Status: nodeState.status, Seq: nodeState.seq}, nil
}

// Cancel cancels one node and propagates the outcome downstream.
func (c *ForkJoinChain) Cancel(player, chainID, nodeID, reason string) error {
	if strings.TrimSpace(player) == "" || strings.TrimSpace(reason) == "" {
		return ErrBlankArg
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	def, ok := c.chains[chainID]
	if !ok {
		return ErrUnknownChain
	}
	if _, ok := def.byID[nodeID]; !ok {
		return ErrUnknownNode
	}

	state := c.ensureState(player, def)
	refreshStates(def, state)
	nodeState := state.nodes[nodeID]
	switch nodeState.status {
	case NodeDone:
		return ErrAlreadyDone
	case NodeCanceled:
		return ErrAlreadyCanceled
	}

	nodeState.status = NodeCanceled
	propagateCancellation(def, state)
	return nil
}

// Resolve reports the quorum verdict of one dependency node.
func (c *ForkJoinChain) Resolve(player, chainID, joinID string) (JoinResult, error) {
	if strings.TrimSpace(player) == "" {
		return JoinResult{}, ErrBlankArg
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	def, ok := c.chains[chainID]
	if !ok {
		return JoinResult{}, ErrUnknownChain
	}
	node, ok := def.byID[joinID]
	if !ok {
		return JoinResult{}, ErrUnknownNode
	}
	if len(node.dependsOn) == 0 {
		return JoinResult{}, ErrNotJoin
	}

	result := JoinResult{
		JoinID: joinID,
		Quorum: node.effectiveQuorum,
	}
	if state := c.existingState(player, chainID); state != nil {
		refreshStates(def, state)
		result.Satisfied = completedDependencies(node, state)
	}
	result.Resolved = result.Satisfied >= result.Quorum
	return result, nil
}

// State reads the full per player state of one chain.
func (c *ForkJoinChain) State(player, chainID string) ChainState {
	c.mu.Lock()
	defer c.mu.Unlock()

	def, ok := c.chains[chainID]
	if !ok {
		return ChainState{}
	}
	state := c.ensureState(player, def)
	refreshStates(def, state)

	chainState := ChainState{
		ChainID: chainID,
		Nodes:   make([]NodeState, 0, len(def.nodes)),
	}
	for _, node := range def.nodes {
		nodeState := state.nodes[node.id]
		chainState.Nodes = append(chainState.Nodes, NodeState{
			NodeID: node.id,
			Status: nodeState.status,
			Seq:    nodeState.seq,
		})
	}
	for _, node := range def.nodes {
		if len(node.dependsOn) == 0 {
			continue
		}
		satisfied := completedDependencies(node, state)
		chainState.Joins = append(chainState.Joins, JoinResult{
			JoinID:    node.id,
			Resolved:  satisfied >= node.effectiveQuorum,
			Quorum:    node.effectiveQuorum,
			Satisfied: satisfied,
		})
	}
	return chainState
}

// Rewards lists the reward ids a player earned in one chain.
func (c *ForkJoinChain) Rewards(player, chainID string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	state := c.existingState(player, chainID)
	if state == nil {
		return []string{}
	}
	rewards := make([]string, len(state.rewards))
	copy(rewards, state.rewards)
	return rewards
}

// Available lists the nodes a player can complete right now.
func (c *ForkJoinChain) Available(player, chainID string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	def, ok := c.chains[chainID]
	if !ok {
		return nil
	}
	state := c.ensureState(player, def)
	refreshStates(def, state)

	available := make([]string, 0)
	for _, node := range def.nodes {
		if state.nodes[node.id].status == NodeActive {
			available = append(available, node.id)
		}
	}
	return available
}

const snapshotVersion = 1

type snapshotDoc struct {
	Version int              `json:"version"`
	Chains  []Chain          `json:"chains"`
	Players []snapshotPlayer `json:"players"`
}

type snapshotPlayer struct {
	Player string                `json:"player"`
	Chains []snapshotPlayerChain `json:"chains"`
}

type snapshotPlayerChain struct {
	ChainID string         `json:"chain_id"`
	NextSeq int64          `json:"next_seq"`
	Nodes   []snapshotNode `json:"nodes"`
	Rewards []string       `json:"rewards"`
}

type snapshotNode struct {
	NodeID string `json:"node_id"`
	Status string `json:"status"`
	Seq    int64  `json:"seq"`
}

// Snapshot serialises the definitions and every per player state.
func (c *ForkJoinChain) Snapshot() ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	doc := snapshotDoc{
		Version: snapshotVersion,
		Chains:  make([]Chain, 0, len(c.chainOrder)),
		Players: make([]snapshotPlayer, 0, len(c.players)),
	}
	for _, chainID := range c.chainOrder {
		def := c.chains[chainID]
		chain := Chain{ID: def.id, Nodes: make([]NodeSpec, 0, len(def.nodes))}
		for _, node := range def.nodes {
			chain.Nodes = append(chain.Nodes, NodeSpec{
				ID:        node.id,
				DependsOn: append([]string(nil), node.dependsOn...),
				Quorum:    node.quorum,
				Reward:    node.reward,
			})
		}
		doc.Chains = append(doc.Chains, chain)
	}

	players := make([]string, 0, len(c.players))
	for player := range c.players {
		players = append(players, player)
	}
	sort.Strings(players)
	for _, player := range players {
		playerState := snapshotPlayer{
			Player: player,
			Chains: make([]snapshotPlayerChain, 0, len(c.players[player])),
		}
		for _, chainID := range c.chainOrder {
			state, ok := c.players[player][chainID]
			if !ok {
				continue
			}
			def := c.chains[chainID]
			chainState := snapshotPlayerChain{
				ChainID: chainID,
				NextSeq: state.nextSeq,
				Nodes:   make([]snapshotNode, 0, len(def.nodes)),
				Rewards: make([]string, len(state.rewards)),
			}
			copy(chainState.Rewards, state.rewards)
			for _, node := range def.nodes {
				nodeState := state.nodes[node.id]
				chainState.Nodes = append(chainState.Nodes, snapshotNode{
					NodeID: node.id,
					Status: nodeState.status,
					Seq:    nodeState.seq,
				})
			}
			playerState.Chains = append(playerState.Chains, chainState)
		}
		doc.Players = append(doc.Players, playerState)
	}

	return json.Marshal(doc)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (c *ForkJoinChain) Restore(data []byte) error {
	var doc snapshotDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return ErrBadSnapshot
	}
	if doc.Version != snapshotVersion {
		return ErrBadSnapshot
	}

	chains := make(map[string]*chainDef, len(doc.Chains))
	chainOrder := make([]string, 0, len(doc.Chains))
	for _, chain := range doc.Chains {
		def, err := buildChainDef(chain, c.options.MaxNodes)
		if err != nil {
			return ErrBadSnapshot
		}
		if _, exists := chains[def.id]; exists {
			return ErrBadSnapshot
		}
		chains[def.id] = def
		chainOrder = append(chainOrder, def.id)
	}
	if c.options.MaxChains > 0 && len(chains) > c.options.MaxChains {
		return ErrBadSnapshot
	}

	players := make(map[string]map[string]*playerChainState, len(doc.Players))
	for _, playerState := range doc.Players {
		if _, exists := players[playerState.Player]; exists {
			return ErrBadSnapshot
		}
		byChain := make(map[string]*playerChainState, len(playerState.Chains))
		for _, chainState := range playerState.Chains {
			def, ok := chains[chainState.ChainID]
			if !ok {
				return ErrBadSnapshot
			}
			if _, exists := byChain[chainState.ChainID]; exists {
				return ErrBadSnapshot
			}
			if chainState.NextSeq < 1 {
				return ErrBadSnapshot
			}
			if len(chainState.Nodes) != len(def.nodes) {
				return ErrBadSnapshot
			}

			state := &playerChainState{
				nodes:   make(map[string]*playerNodeState, len(def.nodes)),
				nextSeq: chainState.NextSeq,
				rewards: append([]string(nil), chainState.Rewards...),
			}
			doneAtSeq := make(map[int64]string, len(def.nodes))
			for _, nodeState := range chainState.Nodes {
				node, ok := def.byID[nodeState.NodeID]
				if !ok {
					return ErrBadSnapshot
				}
				if _, exists := state.nodes[node.id]; exists {
					return ErrBadSnapshot
				}
				switch nodeState.Status {
				case NodePending, NodeActive, NodeCanceled:
					if nodeState.Seq != 0 {
						return ErrBadSnapshot
					}
				case NodeDone:
					if nodeState.Seq < 1 {
						return ErrBadSnapshot
					}
					if _, exists := doneAtSeq[nodeState.Seq]; exists {
						return ErrBadSnapshot
					}
					doneAtSeq[nodeState.Seq] = node.id
				default:
					return ErrBadSnapshot
				}
				state.nodes[node.id] = &playerNodeState{
					status: nodeState.Status,
					seq:    nodeState.Seq,
				}
			}
			if state.nextSeq != int64(len(doneAtSeq))+1 {
				return ErrBadSnapshot
			}
			expectedRewards := make([]string, 0, len(doneAtSeq))
			for seq := int64(1); seq <= int64(len(doneAtSeq)); seq++ {
				nodeID, ok := doneAtSeq[seq]
				if !ok {
					return ErrBadSnapshot
				}
				if reward := def.byID[nodeID].reward; reward != "" {
					expectedRewards = append(expectedRewards, reward)
				}
			}
			if !sameStrings(expectedRewards, chainState.Rewards) {
				return ErrBadSnapshot
			}
			byChain[chainState.ChainID] = state
		}
		players[playerState.Player] = byChain
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.chains = chains
	c.chainOrder = chainOrder
	c.players = players
	return nil
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func buildChainDef(chain Chain, maxNodes int) (*chainDef, error) {
	if strings.TrimSpace(chain.ID) == "" {
		return nil, ErrBadChain
	}
	if len(chain.Nodes) == 0 {
		return nil, ErrBadChain
	}
	if maxNodes > 0 && len(chain.Nodes) > maxNodes {
		return nil, ErrNodeLimit
	}

	def := &chainDef{
		id:    chain.ID,
		nodes: make([]*nodeDef, 0, len(chain.Nodes)),
		byID:  make(map[string]*nodeDef, len(chain.Nodes)),
	}
	for _, spec := range chain.Nodes {
		if strings.TrimSpace(spec.ID) == "" {
			return nil, ErrBadChain
		}
		if _, exists := def.byID[spec.ID]; exists {
			return nil, ErrBadChain
		}
		if spec.Quorum < 0 {
			return nil, ErrBadChain
		}
		if spec.Quorum > 0 && len(spec.DependsOn) == 0 {
			return nil, ErrBadChain
		}
		if spec.Quorum > len(spec.DependsOn) {
			return nil, ErrBadChain
		}

		node := &nodeDef{
			id:        spec.ID,
			dependsOn: append([]string(nil), spec.DependsOn...),
			quorum:    spec.Quorum,
			reward:    spec.Reward,
		}
		if node.quorum == 0 {
			node.effectiveQuorum = len(node.dependsOn)
		} else {
			node.effectiveQuorum = node.quorum
		}
		def.nodes = append(def.nodes, node)
		def.byID[node.id] = node
	}

	for _, node := range def.nodes {
		for _, dependency := range node.dependsOn {
			if _, exists := def.byID[dependency]; !exists {
				return nil, ErrUnknownNode
			}
		}
	}
	if dependencyCycle(def) {
		return nil, ErrCycle
	}
	return def, nil
}

func dependencyCycle(def *chainDef) bool {
	const (
		unvisited = 0
		visiting  = 1
		visited   = 2
	)
	colors := make(map[string]int, len(def.nodes))
	var visit func(string) bool
	visit = func(nodeID string) bool {
		switch colors[nodeID] {
		case visiting:
			return true
		case visited:
			return false
		}
		colors[nodeID] = visiting
		for _, dependency := range def.byID[nodeID].dependsOn {
			if visit(dependency) {
				return true
			}
		}
		colors[nodeID] = visited
		return false
	}
	for _, node := range def.nodes {
		if visit(node.id) {
			return true
		}
	}
	return false
}

func (c *ForkJoinChain) existingState(player, chainID string) *playerChainState {
	byChain := c.players[player]
	if byChain == nil {
		return nil
	}
	return byChain[chainID]
}

func (c *ForkJoinChain) ensureState(player string, def *chainDef) *playerChainState {
	byChain := c.players[player]
	if byChain == nil {
		byChain = make(map[string]*playerChainState)
		c.players[player] = byChain
	}
	state := byChain[def.id]
	if state != nil {
		return state
	}

	state = &playerChainState{
		nodes:   make(map[string]*playerNodeState, len(def.nodes)),
		nextSeq: 1,
	}
	for _, node := range def.nodes {
		status := NodePending
		if len(node.dependsOn) == 0 {
			status = NodeActive
		}
		state.nodes[node.id] = &playerNodeState{status: status}
	}
	byChain[def.id] = state
	return state
}

func refreshStates(def *chainDef, state *playerChainState) {
	for _, node := range def.nodes {
		nodeState := state.nodes[node.id]
		if nodeState.status != NodePending {
			continue
		}
		if dependenciesSatisfied(node, state) {
			nodeState.status = NodeActive
		}
	}
}

func dependenciesSatisfied(node *nodeDef, state *playerChainState) bool {
	return completedDependencies(node, state) >= node.effectiveQuorum
}

func completedDependencies(node *nodeDef, state *playerChainState) int {
	done := 0
	for _, dependency := range node.dependsOn {
		dependencyState := state.nodes[dependency]
		if dependencyState != nil && dependencyState.status == NodeDone {
			done++
		}
	}
	return done
}

func propagateCancellation(def *chainDef, state *playerChainState) {
	for {
		changed := false
		for _, node := range def.nodes {
			nodeState := state.nodes[node.id]
			if nodeState.status == NodeDone || nodeState.status == NodeCanceled {
				continue
			}
			if len(node.dependsOn) == 0 {
				continue
			}

			canceled := 0
			for _, dependency := range node.dependsOn {
				dependencyState := state.nodes[dependency]
				if dependencyState != nil && dependencyState.status == NodeCanceled {
					canceled++
				}
			}
			if canceled == 0 {
				continue
			}
			remaining := len(node.dependsOn) - canceled
			if remaining < node.effectiveQuorum {
				nodeState.status = NodeCanceled
				changed = true
			}
		}
		if !changed {
			return
		}
	}
}
