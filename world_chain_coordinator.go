// Package task advances a server wide world quest chain through contribution
// thresholds, keeps per stage participation and restores both after a restart.
package task

import "errors"

// errNotImplemented marks behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Stage states.
const (
	StageLocked    = "locked"
	StageOpen      = "open"
	StageCompleted = "completed"
	StageFailed    = "failed"
)

// Chain states.
const (
	ChainRunning   = "running"
	ChainCompleted = "completed"
)

// Errors returned by the coordinator.
var (
	ErrBlankArg         = errors.New("argument is required")
	ErrBadChain         = errors.New("chain definition is invalid")
	ErrDuplicateChain   = errors.New("chain id already defined")
	ErrUnknownChain     = errors.New("chain is not defined")
	ErrStageOutOfRange  = errors.New("stage index is out of range")
	ErrWindowClosed     = errors.New("stage window is not open")
	ErrBadAmount        = errors.New("contribution amount must be positive")
	ErrEventConflict    = errors.New("contribution event was already counted")
	ErrStageSettled     = errors.New("stage is already settled")
	ErrChainCompleted   = errors.New("chain is already completed")
	ErrChainLimit       = errors.New("chain limit reached")
	ErrParticipantLimit = errors.New("participant limit reached for this stage")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

// StageSpec defines one stage of a world chain.
type StageSpec struct {
	Index     int    `json:"index"`
	Threshold int64  `json:"threshold"`
	Duration  int64  `json:"duration"`
	Reward    string `json:"reward"`
}

// Chain is one world quest chain definition.
type Chain struct {
	ID     string      `json:"id"`
	Stages []StageSpec `json:"stages"`
}

// Options bounds the chain tables.
type Options struct {
	MaxChains       int
	MaxParticipants int
}

// StageProgress is the live contribution view of one stage.
type StageProgress struct {
	ChainID     string `json:"chain_id"`
	Stage       int    `json:"stage"`
	Contributed int64  `json:"contributed"`
	Threshold   int64  `json:"threshold"`
	Players     int    `json:"players"`
}

// StageState is the scheduling view of one stage.
type StageState struct {
	ChainID     string `json:"chain_id"`
	Stage       int    `json:"stage"`
	Status      string `json:"status"`
	Contributed int64  `json:"contributed"`
	Threshold   int64  `json:"threshold"`
	StartedAt   int64  `json:"started_at"`
	EndsAt      int64  `json:"ends_at"`
}

// ChainState is the full stage list of one chain.
type ChainState struct {
	ChainID string       `json:"chain_id"`
	Current int          `json:"current"`
	Status  string       `json:"status"`
	Stages  []StageState `json:"stages"`
}

// WorldChainCoordinator owns the chain definitions and the shared runtime state.
type WorldChainCoordinator struct {
	options Options
}

// NewWorldChainCoordinator builds a coordinator with the given caps.
func NewWorldChainCoordinator(options Options) *WorldChainCoordinator {
	return &WorldChainCoordinator{options: options}
}

// DefineChain registers one world chain definition.
func (c *WorldChainCoordinator) DefineChain(chain Chain) error {
	return errNotImplemented
}

// Join registers one player for the reward eligibility of the current stage.
func (c *WorldChainCoordinator) Join(chainID string, stage int, player string, at int64) error {
	return errNotImplemented
}

// Contribute reports one deduplicated contribution event to the current stage.
func (c *WorldChainCoordinator) Contribute(chainID, eventID, player string, amount int64, at int64) (StageProgress, error) {
	return StageProgress{}, errNotImplemented
}

// Advance opens, settles or fails the current stage at the given moment.
func (c *WorldChainCoordinator) Advance(chainID string, at int64) (StageState, error) {
	return StageState{}, errNotImplemented
}

// Eligible reports whether a player earns the reward of one stage.
func (c *WorldChainCoordinator) Eligible(chainID string, stage int, player string) bool {
	return false
}

// Progress reads the live progress of the current stage.
func (c *WorldChainCoordinator) Progress(chainID string) (StageProgress, error) {
	return StageProgress{}, errNotImplemented
}

// Participants lists the eligible players of one stage in ascending order.
func (c *WorldChainCoordinator) Participants(chainID string, stage int) []string {
	return nil
}

// State reads the full stage state of one chain.
func (c *WorldChainCoordinator) State(chainID string) (ChainState, bool) {
	return ChainState{}, false
}

// Awarded lists the players rewarded for one completed stage.
func (c *WorldChainCoordinator) Awarded(chainID string, stage int) []string {
	return nil
}

// Snapshot serialises the chain definitions, stage states and participants.
func (c *WorldChainCoordinator) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (c *WorldChainCoordinator) Restore(data []byte) error {
	return errNotImplemented
}
