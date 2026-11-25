// Package task gates trades with a sliding window counter, a counterparty
// concentration check and an account suspension reviewed by an operator.
package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

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

// TradeVelocityGuard owns the windows and the suspension table.
type TradeVelocityGuard struct {
	opts Options
}

// NewTradeVelocityGuard builds a guard with the given caps.
func NewTradeVelocityGuard(opts Options) *TradeVelocityGuard {
	return &TradeVelocityGuard{opts: opts}
}

// Allow decides whether one trade may proceed without consuming any quota.
func (g *TradeVelocityGuard) Allow(from, to string, amount int64, at int64) (Verdict, error) {
	return Verdict{}, errNotImplemented
}

// Record writes one completed trade into the sender window, idempotent by id.
func (g *TradeVelocityGuard) Record(from, to, tradeID string, amount int64, at int64) error {
	return errNotImplemented
}

// Suspend blocks one account until the given time.
func (g *TradeVelocityGuard) Suspend(account, reason string, until int64) error {
	return errNotImplemented
}

// Approve clears a suspension after review.
func (g *TradeVelocityGuard) Approve(account string) error {
	return errNotImplemented
}

// Window reads the sliding window of one sender.
func (g *TradeVelocityGuard) Window(account string, at int64) WindowStat {
	return WindowStat{}
}

// CounterpartShare is the permille share of one peer inside the window.
func (g *TradeVelocityGuard) CounterpartShare(account, peer string, at int64) int {
	return 0
}

// Status reads the review state of one account.
func (g *TradeVelocityGuard) Status(account string, at int64) AccountStatus {
	return AccountStatus{}
}

// Limits returns the effective options.
func (g *TradeVelocityGuard) Limits() Options {
	return Options{}
}

// Events returns a copy of the recorded trades of one account.
func (g *TradeVelocityGuard) Events(account string) []Event {
	return nil
}

// Snapshot serialises windows, suspensions and the trade id index.
func (g *TradeVelocityGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *TradeVelocityGuard) Restore(data []byte) error {
	return errNotImplemented
}
