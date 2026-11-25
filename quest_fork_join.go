// Package task runs a quest chain as a dependency graph: forks unlock several
// parallel branches and joins resolve by all or by a quorum of them.
package task

import "errors"

// errNotImplemented marks behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

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

// ForkJoinChain owns the chain definitions and the per player runtime state.
type ForkJoinChain struct {
	options Options
}

// NewForkJoinChain builds a runtime with the given caps.
func NewForkJoinChain(options Options) *ForkJoinChain {
	return &ForkJoinChain{options: options}
}

// DefineChain registers one fork join chain definition.
func (c *ForkJoinChain) DefineChain(chain Chain) error {
	return errNotImplemented
}

// Complete marks one node done for a player and grants its reward.
func (c *ForkJoinChain) Complete(player, chainID, nodeID string, at int64) (NodeState, error) {
	return NodeState{}, errNotImplemented
}

// Cancel cancels one node and propagates the outcome downstream.
func (c *ForkJoinChain) Cancel(player, chainID, nodeID, reason string) error {
	return errNotImplemented
}

// Resolve reports the quorum verdict of one dependency node.
func (c *ForkJoinChain) Resolve(player, chainID, joinID string) (JoinResult, error) {
	return JoinResult{}, errNotImplemented
}

// State reads the full per player state of one chain.
func (c *ForkJoinChain) State(player, chainID string) ChainState {
	return ChainState{}
}

// Rewards lists the reward ids a player earned in one chain.
func (c *ForkJoinChain) Rewards(player, chainID string) []string {
	return nil
}

// Available lists the nodes a player can complete right now.
func (c *ForkJoinChain) Available(player, chainID string) []string {
	return nil
}

// Snapshot serialises the definitions and every per player state.
func (c *ForkJoinChain) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (c *ForkJoinChain) Restore(data []byte) error {
	return errNotImplemented
}
