// Package task advances a server wide world quest chain through contribution
// thresholds, keeps per stage participation and restores both after a restart.
package task

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
)

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

type stageRuntime struct {
	spec         StageSpec
	status       string
	contributed  int64
	startedAt    int64
	endsAt       int64
	participants map[string]struct{}
	awarded      map[string]struct{}
}

type chainRuntime struct {
	definition Chain
	current    int
	status     string
	stages     []stageRuntime
}

// WorldChainCoordinator owns the chain definitions and the shared runtime state.
type WorldChainCoordinator struct {
	mu      sync.RWMutex
	options Options
	chains  map[string]*chainRuntime
	events  map[string]struct{}
}

// NewWorldChainCoordinator builds a coordinator with the given caps.
func NewWorldChainCoordinator(options Options) *WorldChainCoordinator {
	return &WorldChainCoordinator{
		options: options,
		chains:  make(map[string]*chainRuntime),
		events:  make(map[string]struct{}),
	}
}

// DefineChain registers one world chain definition.
func (c *WorldChainCoordinator) DefineChain(chain Chain) error {
	if err := validateChain(chain); err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, ok := c.chains[chain.ID]; ok {
		return ErrDuplicateChain
	}
	if c.options.MaxChains > 0 && len(c.chains) >= c.options.MaxChains {
		return ErrChainLimit
	}

	stages := make([]stageRuntime, len(chain.Stages))
	for i, spec := range chain.Stages {
		stages[i] = stageRuntime{
			spec:         spec,
			status:       StageLocked,
			participants: make(map[string]struct{}),
			awarded:      make(map[string]struct{}),
		}
	}
	c.chains[chain.ID] = &chainRuntime{
		definition: cloneChain(chain),
		current:    0,
		status:     ChainRunning,
		stages:     stages,
	}
	return nil
}

// Join registers one player for the reward eligibility of the current stage.
func (c *WorldChainCoordinator) Join(chainID string, stage int, player string, at int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	chain, ok := c.chains[chainID]
	if !ok {
		return ErrUnknownChain
	}
	if stage < 0 || stage >= len(chain.stages) || stage != chain.current {
		return ErrStageOutOfRange
	}
	if strings.TrimSpace(player) == "" {
		return ErrBlankArg
	}

	current := &chain.stages[chain.current]
	if current.status != StageOpen {
		return ErrWindowClosed
	}
	if at < current.startedAt || at >= current.endsAt {
		return ErrWindowClosed
	}
	if _, exists := current.participants[player]; exists {
		return nil
	}
	if c.options.MaxParticipants > 0 && len(current.participants) >= c.options.MaxParticipants {
		return ErrParticipantLimit
	}
	current.participants[player] = struct{}{}
	return nil
}

// Contribute reports one deduplicated contribution event to the current stage.
func (c *WorldChainCoordinator) Contribute(chainID, eventID, player string, amount int64, at int64) (StageProgress, error) {
	if strings.TrimSpace(eventID) == "" || strings.TrimSpace(player) == "" {
		return StageProgress{}, ErrBlankArg
	}
	if amount <= 0 {
		return StageProgress{}, ErrBadAmount
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	chain, ok := c.chains[chainID]
	if !ok {
		return StageProgress{}, ErrUnknownChain
	}
	if _, exists := c.events[eventID]; exists {
		return StageProgress{}, ErrEventConflict
	}

	current := &chain.stages[chain.current]
	switch current.status {
	case StageLocked:
		return StageProgress{}, ErrWindowClosed
	case StageCompleted, StageFailed:
		return StageProgress{}, ErrStageSettled
	case StageOpen:
	default:
		return StageProgress{}, ErrWindowClosed
	}
	if at < current.startedAt || at >= current.endsAt {
		return StageProgress{}, ErrWindowClosed
	}
	if amount > int64(^uint64(0)>>1)-current.contributed {
		return StageProgress{}, ErrBadAmount
	}

	// Contribution and participation are committed together. This makes every
	// successfully counted contributor visible to the reward snapshot.
	if _, exists := current.participants[player]; !exists {
		if c.options.MaxParticipants > 0 && len(current.participants) >= c.options.MaxParticipants {
			return StageProgress{}, ErrParticipantLimit
		}
		current.participants[player] = struct{}{}
	}
	current.contributed += amount
	c.events[eventID] = struct{}{}

	return progressOf(chain), nil
}

// Advance opens, settles or fails the current stage at the given moment.
func (c *WorldChainCoordinator) Advance(chainID string, at int64) (StageState, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	chain, ok := c.chains[chainID]
	if !ok {
		return StageState{}, ErrUnknownChain
	}
	if chain.status == ChainCompleted {
		return StageState{}, ErrChainCompleted
	}

	current := &chain.stages[chain.current]
	settled := chain.current
	switch current.status {
	case StageLocked, StageFailed:
		openStage(current, at)
	case StageOpen:
		if current.contributed >= current.spec.Threshold {
			completeStage(chain)
			return stateOf(chain, settled), nil
		} else if at >= current.endsAt {
			current.status = StageFailed
			current.contributed = 0
		}
	case StageCompleted:
		return StageState{}, ErrChainCompleted
	}

	return stateOf(chain, settled), nil
}

// Eligible reports whether a player earns the reward of one stage.
func (c *WorldChainCoordinator) Eligible(chainID string, stage int, player string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	chain, ok := c.chains[chainID]
	if !ok || stage < 0 || stage >= len(chain.stages) || strings.TrimSpace(player) == "" {
		return false
	}
	_, ok = chain.stages[stage].participants[player]
	return ok
}

// Progress reads the live progress of the current stage.
func (c *WorldChainCoordinator) Progress(chainID string) (StageProgress, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	chain, ok := c.chains[chainID]
	if !ok {
		return StageProgress{}, ErrUnknownChain
	}
	return progressOf(chain), nil
}

// Participants lists the eligible players of one stage in ascending order.
func (c *WorldChainCoordinator) Participants(chainID string, stage int) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	chain, ok := c.chains[chainID]
	if !ok || stage < 0 || stage >= len(chain.stages) {
		return nil
	}
	return sortedKeys(chain.stages[stage].participants)
}

// State reads the full stage state of one chain.
func (c *WorldChainCoordinator) State(chainID string) (ChainState, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	chain, ok := c.chains[chainID]
	if !ok {
		return ChainState{}, false
	}

	stages := make([]StageState, len(chain.stages))
	for i := range chain.stages {
		stages[i] = stateOf(chain, i)
	}
	return ChainState{
		ChainID: chain.definition.ID,
		Current: chain.current,
		Status:  chain.status,
		Stages:  stages,
	}, true
}

// Awarded lists the players rewarded for one completed stage.
func (c *WorldChainCoordinator) Awarded(chainID string, stage int) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	chain, ok := c.chains[chainID]
	if !ok || stage < 0 || stage >= len(chain.stages) {
		return nil
	}
	return sortedKeys(chain.stages[stage].awarded)
}

type snapshotStage struct {
	Status       string   `json:"status"`
	Contributed  int64    `json:"contributed"`
	StartedAt    int64    `json:"started_at"`
	EndsAt       int64    `json:"ends_at"`
	Participants []string `json:"participants"`
	Awarded      []string `json:"awarded"`
}

type snapshotChain struct {
	Definition Chain           `json:"definition"`
	Current    int             `json:"current"`
	Status     string          `json:"status"`
	Stages     []snapshotStage `json:"stages"`
}

type snapshotPayload struct {
	Version int             `json:"version"`
	Chains  []snapshotChain `json:"chains"`
	Events  []string        `json:"events"`
}

const snapshotVersion = 1

// Snapshot serialises the chain definitions, stage states and participants.
func (c *WorldChainCoordinator) Snapshot() ([]byte, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	ids := make([]string, 0, len(c.chains))
	for id := range c.chains {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	payload := snapshotPayload{
		Version: snapshotVersion,
		Chains:  make([]snapshotChain, 0, len(ids)),
		Events:  sortedKeys(c.events),
	}
	for _, id := range ids {
		chain := c.chains[id]
		item := snapshotChain{
			Definition: cloneChain(chain.definition),
			Current:    chain.current,
			Status:     chain.status,
			Stages:     make([]snapshotStage, len(chain.stages)),
		}
		for i := range chain.stages {
			stage := &chain.stages[i]
			item.Stages[i] = snapshotStage{
				Status:       stage.status,
				Contributed:  stage.contributed,
				StartedAt:    stage.startedAt,
				EndsAt:       stage.endsAt,
				Participants: sortedKeys(stage.participants),
				Awarded:      sortedKeys(stage.awarded),
			}
		}
		payload.Chains = append(payload.Chains, item)
	}
	return json.Marshal(payload)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (c *WorldChainCoordinator) Restore(data []byte) error {
	var payload snapshotPayload
	if err := json.Unmarshal(data, &payload); err != nil || payload.Version != snapshotVersion {
		return ErrBadSnapshot
	}

	chains, err := buildRestoredChains(payload)
	if err != nil {
		return ErrBadSnapshot
	}
	events, err := buildRestoredEvents(payload.Events)
	if err != nil {
		return ErrBadSnapshot
	}

	c.mu.Lock()
	c.chains = chains
	c.events = events
	c.mu.Unlock()
	return nil
}

func validateChain(chain Chain) error {
	if strings.TrimSpace(chain.ID) == "" || len(chain.Stages) == 0 {
		return ErrBadChain
	}
	for i, stage := range chain.Stages {
		if stage.Index != i || stage.Threshold <= 0 || stage.Duration <= 0 {
			return ErrBadChain
		}
	}
	return nil
}

func cloneChain(chain Chain) Chain {
	clone := Chain{ID: chain.ID}
	if chain.Stages != nil {
		clone.Stages = append([]StageSpec(nil), chain.Stages...)
	}
	return clone
}

func openStage(stage *stageRuntime, at int64) {
	stage.status = StageOpen
	stage.contributed = 0
	stage.startedAt = at
	if stage.spec.Duration > 0 && at > int64(^uint64(0)>>1)-stage.spec.Duration {
		stage.endsAt = int64(^uint64(0) >> 1)
	} else {
		stage.endsAt = at + stage.spec.Duration
	}
}

func completeStage(chain *chainRuntime) {
	stage := &chain.stages[chain.current]
	stage.status = StageCompleted
	for player := range stage.participants {
		stage.awarded[player] = struct{}{}
	}
	if chain.current+1 < len(chain.stages) {
		chain.current++
		return
	}
	chain.status = ChainCompleted
}

func progressOf(chain *chainRuntime) StageProgress {
	stage := &chain.stages[chain.current]
	return StageProgress{
		ChainID:     chain.definition.ID,
		Stage:       chain.current,
		Contributed: stage.contributed,
		Threshold:   stage.spec.Threshold,
		Players:     len(stage.participants),
	}
}

func stateOf(chain *chainRuntime, index int) StageState {
	stage := &chain.stages[index]
	return StageState{
		ChainID:     chain.definition.ID,
		Stage:       index,
		Status:      stage.status,
		Contributed: stage.contributed,
		Threshold:   stage.spec.Threshold,
		StartedAt:   stage.startedAt,
		EndsAt:      stage.endsAt,
	}
}

func sortedKeys(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func buildRestoredChains(payload snapshotPayload) (map[string]*chainRuntime, error) {
	chains := make(map[string]*chainRuntime, len(payload.Chains))
	for _, item := range payload.Chains {
		if err := validateChain(item.Definition); err != nil {
			return nil, err
		}
		if _, exists := chains[item.Definition.ID]; exists {
			return nil, ErrBadSnapshot
		}
		if len(item.Stages) != len(item.Definition.Stages) {
			return nil, ErrBadSnapshot
		}
		if item.Current < 0 || item.Current >= len(item.Stages) {
			return nil, ErrBadSnapshot
		}
		if item.Status != ChainRunning && item.Status != ChainCompleted {
			return nil, ErrBadSnapshot
		}

		chain := &chainRuntime{
			definition: cloneChain(item.Definition),
			current:    item.Current,
			status:     item.Status,
			stages:     make([]stageRuntime, len(item.Stages)),
		}
		allCompleted := true
		for i, stageItem := range item.Stages {
			if !validStageStatus(stageItem.Status) || stageItem.Contributed < 0 {
				return nil, ErrBadSnapshot
			}
			if stageItem.Status == StageFailed && stageItem.Contributed != 0 {
				return nil, ErrBadSnapshot
			}
			participants, err := buildParticipantSet(stageItem.Participants)
			if err != nil {
				return nil, err
			}
			awarded, err := buildAwardedSet(stageItem.Awarded, participants)
			if err != nil {
				return nil, err
			}
			chain.stages[i] = stageRuntime{
				spec:         item.Definition.Stages[i],
				status:       stageItem.Status,
				contributed:  stageItem.Contributed,
				startedAt:    stageItem.StartedAt,
				endsAt:       stageItem.EndsAt,
				participants: participants,
				awarded:      awarded,
			}
			if stageItem.Status != StageCompleted {
				allCompleted = false
			}
		}
		if item.Status == ChainCompleted && !allCompleted {
			return nil, ErrBadSnapshot
		}
		if item.Status == ChainRunning && allCompleted {
			return nil, ErrBadSnapshot
		}
		chains[item.Definition.ID] = chain
	}
	return chains, nil
}

func buildRestoredEvents(values []string) (map[string]struct{}, error) {
	events := make(map[string]struct{}, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return nil, ErrBadSnapshot
		}
		if _, exists := events[value]; exists {
			return nil, ErrBadSnapshot
		}
		events[value] = struct{}{}
	}
	return events, nil
}

func buildParticipantSet(values []string) (map[string]struct{}, error) {
	participants := make(map[string]struct{}, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return nil, ErrBadSnapshot
		}
		if _, exists := participants[value]; exists {
			return nil, ErrBadSnapshot
		}
		participants[value] = struct{}{}
	}
	return participants, nil
}

func buildAwardedSet(values []string, participants map[string]struct{}) (map[string]struct{}, error) {
	awarded := make(map[string]struct{}, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return nil, ErrBadSnapshot
		}
		if _, exists := awarded[value]; exists {
			return nil, ErrBadSnapshot
		}
		if _, exists := participants[value]; !exists {
			return nil, ErrBadSnapshot
		}
		awarded[value] = struct{}{}
	}
	return awarded, nil
}

func validStageStatus(status string) bool {
	switch status {
	case StageLocked, StageOpen, StageCompleted, StageFailed:
		return true
	default:
		return false
	}
}
