// Package task keeps the egress quota ledger of one gateway: category aware
// admission, reserved versus settled bytes and the per tenant breaker.
package task

import (
	"errors"
	"time"
)

// errNotImplemented is returned by behaviour that is not wired up yet.
var errNotImplemented = errors.New("not implemented")

// Destination kinds. Management and audit traffic stays outside the breaker.
const (
	KindData       = "data"
	KindManagement = "management"
	KindAudit      = "audit"

	CategoryUnknown = "unknown"
)

// Verdict reasons.
const (
	ReasonAllowed       = "allowed"
	ReasonQuotaExceeded = "quota_exceeded"
	ReasonBreakerOpen   = "breaker_open"
)

// Errors reported by the ledger.
var (
	ErrBlankTenant      = errors.New("tenant is required")
	ErrBlankDestination = errors.New("destination host is required")
	ErrBadEstimate      = errors.New("reservation estimate must be positive")
	ErrQuotaExceeded    = errors.New("quota exceeded for the current window")
	ErrBreakerOpen      = errors.New("tenant egress breaker is open")
	ErrUnknownTicket    = errors.New("unknown reservation ticket")
	ErrBadUsage         = errors.New("settled usage is invalid")
	ErrBadCategory      = errors.New("category is invalid")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

// Dest is one outbound destination. An empty or unknown Category uses the
// smaller exploration budget.
type Dest struct {
	Host     string `json:"host"`
	Category string `json:"category"`
	Kind     string `json:"kind"`
}

// Verdict is the admission answer for one destination.
type Verdict struct {
	Allowed   bool   `json:"allowed"`
	Category  string `json:"category"`
	Cost      int64  `json:"cost"`
	Remaining int64  `json:"remaining"`
	Reason    string `json:"reason"`
}

// Ticket is one reservation that later settles or returns.
type Ticket struct {
	ID       uint64 `json:"id"`
	Tenant   string `json:"tenant"`
	Category string `json:"category"`
	Reserved int64  `json:"reserved"`
}

// Limits bounds the ledger.
type Limits struct {
	KnownQuota      int64
	ExploreQuota    int64
	MaxSamples      int
	RejectThreshold int
	Cooldown        time.Duration
}

// Usage is the rolling window picture of one tenant.
type Usage struct {
	Known    int64 `json:"known"`
	Explore  int64 `json:"explore"`
	Reserved int64 `json:"reserved"`
	Rejected int   `json:"rejected"`
}

// BreakerState is the circuit breaker view of one tenant.
type BreakerState struct {
	Open        bool      `json:"open"`
	Consecutive int       `json:"consecutive"`
	OpenedAt    time.Time `json:"opened_at"`
	RecoverAt   time.Time `json:"recover_at"`
	Reason      string    `json:"reason"`
}

// EgressQuotaLedger owns windows, reservations, samples, categories and
// breakers.
type EgressQuotaLedger struct {
	limits Limits
	now    func() time.Time
}

// NewEgressQuotaLedger builds a ledger backed by the wall clock.
func NewEgressQuotaLedger(limits Limits) *EgressQuotaLedger {
	return &EgressQuotaLedger{limits: limits, now: time.Now}
}

// NewEgressQuotaLedgerWithClock builds a ledger with an injected clock.
func NewEgressQuotaLedgerWithClock(limits Limits, now func() time.Time) *EgressQuotaLedger {
	if now == nil {
		now = time.Now
	}
	return &EgressQuotaLedger{limits: limits, now: now}
}

// Check answers whether one destination would be admitted right now.
func (l *EgressQuotaLedger) Check(tenant string, dest Dest) (Verdict, error) {
	return Verdict{}, errNotImplemented
}

// Reserve books an estimate against the known category budget.
func (l *EgressQuotaLedger) Reserve(tenant string, est int64) (Ticket, error) {
	return Ticket{}, errNotImplemented
}

// Settle closes a ticket with the bytes actually used and returns or charges
// the difference against the reservation.
func (l *EgressQuotaLedger) Settle(t Ticket, used int64) error {
	return errNotImplemented
}

// Release closes a ticket without consuming anything and returns the whole
// reservation.
func (l *EgressQuotaLedger) Release(t Ticket) error {
	return errNotImplemented
}

// Usage reports the rolling window usage of one tenant.
func (l *EgressQuotaLedger) Usage(tenant string) Usage {
	return Usage{}
}

// Breaker reports the breaker state of one tenant.
func (l *EgressQuotaLedger) Breaker(tenant string) BreakerState {
	return BreakerState{}
}

// Samples lists the unknown destination hosts seen for one tenant.
func (l *EgressQuotaLedger) Samples(tenant string) []string {
	return nil
}

// LearnCategory promotes a host into a known category.
func (l *EgressQuotaLedger) LearnCategory(tenant, host, category string) error {
	return errNotImplemented
}

// Snapshot serialises windows, tickets, categories and breakers.
func (l *EgressQuotaLedger) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (l *EgressQuotaLedger) Restore(data []byte) error {
	return errNotImplemented
}
