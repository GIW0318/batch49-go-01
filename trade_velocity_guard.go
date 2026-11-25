// Package task gates trades with a sliding window counter, a counterparty
// concentration check and an account suspension reviewed by an operator.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
)

// Verdict codes returned by Allow.
const (
	VerdictAllowed       = "allowed"
	VerdictWindowCount   = "window_count"
	VerdictWindowAmount  = "window_amount"
	VerdictConcentration = "concentration"
	VerdictSuspended     = "suspended"
)

// Defaults used when Options leaves a field unset.
const (
	DefaultWindowSeconds    = 60
	DefaultMaxTrades        = 20
	DefaultMaxAmount        = 100000
	DefaultMaxSharePermille = 800
	DefaultSuspendSeconds   = 300
)

// Errors returned by the guard.
var (
	ErrBadTrade    = errors.New("invalid trade")
	ErrBadAccount  = errors.New("account is required")
	ErrBadReason   = errors.New("suspension reason is required")
	ErrBadUntil    = errors.New("suspension end must be in the future")
	ErrBadSnapshot = errors.New("snapshot payload is invalid")
)

// Options bounds the velocity guard.
type Options struct {
	WindowSeconds    int64
	MaxTrades        int
	MaxAmount        int64
	MaxSharePermille int
	SuspendSeconds   int64
}

// Verdict explains one Allow decision.
type Verdict struct {
	Allowed       bool   `json:"allowed"`
	Code          string `json:"code"`
	Trades        int    `json:"trades"`
	Amount        int64  `json:"amount"`
	SharePermille int    `json:"share_permille"`
	ResetAt       int64  `json:"reset_at"`
	Until         int64  `json:"until"`
}

// WindowStat is the per account sliding window snapshot.
type WindowStat struct {
	Trades  int              `json:"trades"`
	Amount  int64            `json:"amount"`
	Peers   map[string]int64 `json:"peers"`
	ResetAt int64            `json:"reset_at"`
}

// AccountStatus is the review state of one account.
type AccountStatus struct {
	Account   string `json:"account"`
	Suspended bool   `json:"suspended"`
	Reason    string `json:"reason"`
	Until     int64  `json:"until"`
}

// Event is one recorded outgoing trade.
type Event struct {
	TradeID string `json:"trade_id"`
	Peer    string `json:"peer"`
	Amount  int64  `json:"amount"`
	At      int64  `json:"at"`
}

type suspensionState struct {
	Reason string `json:"reason"`
	Until  int64  `json:"until"`
}

type snapshotData struct {
	Records     map[string][]Event         `json:"records"`
	Suspensions map[string]suspensionState `json:"suspensions"`
	TradeOwners map[string]string          `json:"trade_owners"`
}

// TradeVelocityGuard owns the windows and the suspension table.
type TradeVelocityGuard struct {
	mu          sync.Mutex
	opts        Options
	records     map[string][]Event
	suspensions map[string]suspensionState
	tradeOwners map[string]string
}

// NewTradeVelocityGuard builds a guard with the given caps.
func NewTradeVelocityGuard(opts Options) *TradeVelocityGuard {
	if opts.WindowSeconds <= 0 {
		opts.WindowSeconds = DefaultWindowSeconds
	}
	if opts.MaxTrades <= 0 {
		opts.MaxTrades = DefaultMaxTrades
	}
	if opts.MaxAmount <= 0 {
		opts.MaxAmount = DefaultMaxAmount
	}
	if opts.MaxSharePermille <= 0 {
		opts.MaxSharePermille = DefaultMaxSharePermille
	}
	if opts.SuspendSeconds <= 0 {
		opts.SuspendSeconds = DefaultSuspendSeconds
	}

	return &TradeVelocityGuard{
		opts:        opts,
		records:     make(map[string][]Event),
		suspensions: make(map[string]suspensionState),
		tradeOwners: make(map[string]string),
	}
}

// Allow decides whether one trade may proceed without consuming any quota.
func (g *TradeVelocityGuard) Allow(from, to string, amount int64, at int64) (Verdict, error) {
	if !validTrade(from, to, amount, at) {
		return Verdict{}, ErrBadTrade
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	stat := g.windowLocked(from, at)
	verdict := Verdict{
		Trades:  stat.Trades,
		Amount:  stat.Amount,
		ResetAt: stat.ResetAt,
	}

	if suspension, ok := g.suspensions[from]; ok && suspension.Until > at {
		verdict.Code = VerdictSuspended
		verdict.Until = suspension.Until
		return verdict, nil
	}

	projectedPeerAmount := stat.Peers[to] + amount
	sharePermille := 0
	incrementalPermille := 0
	if stat.Amount > 0 {
		sharePermille = int(projectedPeerAmount * 1000 / stat.Amount)
		incrementalPermille = int(amount * 1000 / stat.Amount)
	}
	verdict.SharePermille = sharePermille

	if stat.Trades+1 > g.opts.MaxTrades {
		verdict.Code = VerdictWindowCount
		return verdict, nil
	}
	if stat.Amount+amount > g.opts.MaxAmount {
		verdict.Code = VerdictWindowAmount
		return verdict, nil
	}
	if sharePermille > g.opts.MaxSharePermille &&
		incrementalPermille > g.opts.MaxSharePermille {
		until := at + g.opts.SuspendSeconds
		g.suspensions[from] = suspensionState{
			Reason: VerdictConcentration,
			Until:  until,
		}
		verdict.Code = VerdictConcentration
		verdict.Until = until
		return verdict, nil
	}

	verdict.Allowed = true
	verdict.Code = VerdictAllowed
	return verdict, nil
}

// Record writes one completed trade into the sender window, idempotent by id.
func (g *TradeVelocityGuard) Record(from, to, tradeID string, amount int64, at int64) error {
	if !validTrade(from, to, amount, at) || tradeID == "" {
		return ErrBadTrade
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if _, exists := g.tradeOwners[tradeID]; exists {
		return nil
	}

	g.tradeOwners[tradeID] = from
	g.records[from] = append(g.records[from], Event{
		TradeID: tradeID,
		Peer:    to,
		Amount:  amount,
		At:      at,
	})
	return nil
}

// Suspend blocks one account until the given time.
func (g *TradeVelocityGuard) Suspend(account, reason string, until int64) error {
	if account == "" {
		return ErrBadAccount
	}
	if reason == "" {
		return ErrBadReason
	}
	if until <= 0 {
		return ErrBadUntil
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.suspensions[account] = suspensionState{Reason: reason, Until: until}
	return nil
}

// Approve clears a suspension after review.
func (g *TradeVelocityGuard) Approve(account string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.suspensions, account)
	return nil
}

// Window reads the sliding window of one sender.
func (g *TradeVelocityGuard) Window(account string, at int64) WindowStat {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.windowLocked(account, at)
}

// CounterpartShare is the permille share of one peer inside the window.
func (g *TradeVelocityGuard) CounterpartShare(account, peer string, at int64) int {
	g.mu.Lock()
	defer g.mu.Unlock()

	stat := g.windowLocked(account, at)
	if stat.Amount <= 0 {
		return 0
	}
	return int(stat.Peers[peer] * 1000 / stat.Amount)
}

// Status reads the review state of one account.
func (g *TradeVelocityGuard) Status(account string, at int64) AccountStatus {
	g.mu.Lock()
	defer g.mu.Unlock()

	status := AccountStatus{Account: account}
	suspension, ok := g.suspensions[account]
	if !ok {
		return status
	}

	status.Reason = suspension.Reason
	status.Until = suspension.Until
	status.Suspended = suspension.Until > at
	return status
}

// Limits returns the effective options.
func (g *TradeVelocityGuard) Limits() Options {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.opts
}

// Events returns a copy of the recorded trades of one account.
func (g *TradeVelocityGuard) Events(account string) []Event {
	g.mu.Lock()
	events := make([]Event, len(g.records[account]))
	copy(events, g.records[account])
	g.mu.Unlock()

	sort.SliceStable(events, func(i, j int) bool {
		return events[i].At < events[j].At
	})
	return events
}

// Snapshot serialises windows, suspensions and the trade id index.
func (g *TradeVelocityGuard) Snapshot() ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	data, err := json.Marshal(snapshotData{
		Records:     g.records,
		Suspensions: g.suspensions,
		TradeOwners: g.tradeOwners,
	})
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *TradeVelocityGuard) Restore(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		return fmt.Errorf("%w: empty payload", ErrBadSnapshot)
	}

	var payload snapshotData
	if err := json.Unmarshal(data, &payload); err != nil {
		return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	if err := prepareSnapshot(&payload); err != nil {
		return err
	}

	g.mu.Lock()
	g.records = payload.Records
	g.suspensions = payload.Suspensions
	g.tradeOwners = payload.TradeOwners
	g.mu.Unlock()
	return nil
}

func validTrade(from, to string, amount int64, at int64) bool {
	return from != "" && to != "" && from != to && amount > 0 && at > 0
}

func (g *TradeVelocityGuard) windowLocked(account string, at int64) WindowStat {
	stat := WindowStat{Peers: make(map[string]int64)}
	windowSeconds := g.opts.WindowSeconds
	var earliest int64

	for _, event := range g.records[account] {
		if event.At > at || at-event.At >= windowSeconds {
			continue
		}

		stat.Trades++
		stat.Amount += event.Amount
		stat.Peers[event.Peer] += event.Amount
		if stat.Trades == 1 || event.At < earliest {
			earliest = event.At
		}
	}

	if stat.Trades > 0 {
		stat.ResetAt = earliest + windowSeconds
	}
	return stat
}

func prepareSnapshot(payload *snapshotData) error {
	if payload.Records == nil {
		payload.Records = make(map[string][]Event)
	}
	if payload.Suspensions == nil {
		payload.Suspensions = make(map[string]suspensionState)
	}
	if payload.TradeOwners == nil {
		payload.TradeOwners = make(map[string]string)
	}

	seenTradeIDs := make(map[string]struct{})
	for account, events := range payload.Records {
		if account == "" {
			return fmt.Errorf("%w: empty record account", ErrBadSnapshot)
		}

		for _, event := range events {
			if event.TradeID == "" || event.Peer == "" || event.Peer == account ||
				event.Amount <= 0 || event.At <= 0 {
				return fmt.Errorf("%w: invalid event", ErrBadSnapshot)
			}
			if _, duplicated := seenTradeIDs[event.TradeID]; duplicated {
				return fmt.Errorf("%w: duplicate trade id %s", ErrBadSnapshot, event.TradeID)
			}
			seenTradeIDs[event.TradeID] = struct{}{}

			if owner, ok := payload.TradeOwners[event.TradeID]; ok && owner != account {
				return fmt.Errorf("%w: trade owner mismatch", ErrBadSnapshot)
			}
			payload.TradeOwners[event.TradeID] = account
		}
	}

	for tradeID, owner := range payload.TradeOwners {
		if tradeID == "" || owner == "" {
			return fmt.Errorf("%w: invalid trade index", ErrBadSnapshot)
		}
	}
	for account, suspension := range payload.Suspensions {
		if account == "" || suspension.Reason == "" || suspension.Until <= 0 {
			return fmt.Errorf("%w: invalid suspension", ErrBadSnapshot)
		}
	}
	return nil
}
