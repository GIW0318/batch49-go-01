// Package task closes the confirmation loop of high risk transactions at the
// authenticator: what was confirmed is what gets redeemed, once.
package task

import (
	"errors"
	"time"
)

// errNotImplemented is returned by behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Confirmation tiers chosen from the transaction amount.
const (
	TierExempt = "exempt"
	TierSingle = "single"
	TierDual   = "dual"
)

// Challenge states.
const (
	ChallengeOpen       = "open"
	ChallengeClosed     = "closed"
	ChallengeSuperseded = "superseded"
)

// Ticket states.
const (
	TicketReady = "ready"
	TicketUsed  = "used"
	TicketStale = "stale"
)

// Decision reasons.
const (
	ReasonConfirmed = "confirmed"
	ReasonExempt    = "small_amount_exempt"
)

// Errors reported by the guard.
var (
	ErrBlankID           = errors.New("identifier is required")
	ErrBadAmount         = errors.New("amount must be positive")
	ErrBadTransaction    = errors.New("transaction is missing payee, account or channel")
	ErrChallengeNotFound = errors.New("challenge not found")
	ErrChallengeClosed   = errors.New("challenge is already closed")
	ErrChallengeExpired  = errors.New("challenge has expired")
	ErrChallengeStale    = errors.New("challenge was replaced by a newer preparation")
	ErrChallengeLimit    = errors.New("pending challenge limit reached for this user")
	ErrBadProof          = errors.New("proof is incomplete")
	ErrAttemptLimit      = errors.New("confirmation attempts exhausted")
	ErrNotConfirmed      = errors.New("confirmation quorum not reached yet")
	ErrTicketNotFound    = errors.New("ticket not found")
	ErrTicketUsed        = errors.New("ticket was already redeemed")
	ErrTicketExpired     = errors.New("ticket has expired")
	ErrTicketStale       = errors.New("ticket no longer matches the prepared transaction")
	ErrOpMismatch        = errors.New("ticket is bound to another operation")
	ErrChannelMismatch   = errors.New("ticket is bound to another channel")
	ErrBadSnapshot       = errors.New("snapshot payload is invalid")
)

// Transaction is the operation waiting for confirmation.
type Transaction struct {
	UserID       string `json:"user_id"`
	AmountCents  int64  `json:"amount_cents"`
	Currency     string `json:"currency"`
	Payee        string `json:"payee"`
	PayeeAccount string `json:"payee_account"`
	Channel      string `json:"channel"`
}

// Proof is what one principal submits to confirm a challenge.
type Proof struct {
	Signature string `json:"signature"`
	DeviceID  string `json:"device_id"`
}

// Challenge is one prepared confirmation request.
type Challenge struct {
	ID         string    `json:"id"`
	OpID       string    `json:"op_id"`
	UserID     string    `json:"user_id"`
	Digest     string    `json:"digest"`
	Tier       string    `json:"tier"`
	Required   int       `json:"required"`
	Confirmed  int       `json:"confirmed"`
	Attempts   int       `json:"attempts"`
	Principals []string  `json:"principals"`
	State      string    `json:"state"`
	TicketID   string    `json:"ticket_id"`
	ExpiresAt  time.Time `json:"expires_at"`
	Seq        int64     `json:"seq"`
}

// Ticket is the single use right to redeem one confirmed operation.
type Ticket struct {
	ID        string    `json:"id"`
	OpID      string    `json:"op_id"`
	UserID    string    `json:"user_id"`
	Digest    string    `json:"digest"`
	Channel   string    `json:"channel"`
	Reason    string    `json:"reason"`
	State     string    `json:"state"`
	ExpiresAt time.Time `json:"expires_at"`
	Seq       int64     `json:"seq"`
}

// Decision is the outcome of a redemption.
type Decision struct {
	OpID     string `json:"op_id"`
	TicketID string `json:"ticket_id"`
	Approved bool   `json:"approved"`
	Reason   string `json:"reason"`
	Seq      int64  `json:"seq"`
}

// Options bounds the guard and supplies the clock used for expiry.
type Options struct {
	SmallLimitCents      int64
	LargeLimitCents      int64
	ChallengeTTL         time.Duration
	TicketTTL            time.Duration
	MaxPendingChallenges int
	MaxConfirmAttempts   int
	Now                  func() time.Time
}

// TransactionConfirmationGuard owns the challenge table, the ticket table and
// the counters.
type TransactionConfirmationGuard struct {
	opts Options
}

// NewTransactionConfirmationGuard builds a guard with the given options.
func NewTransactionConfirmationGuard(opts Options) *TransactionConfirmationGuard {
	return &TransactionConfirmationGuard{opts: opts}
}

// Prepare canonicalises the transaction, picks the tier and registers a
// challenge, or a ready ticket for an exempt amount.
func (g *TransactionConfirmationGuard) Prepare(opID string, tx Transaction) (Challenge, error) {
	return Challenge{}, errNotImplemented
}

// Confirm records one principal's confirmation and issues the ticket once the
// quorum is reached.
func (g *TransactionConfirmationGuard) Confirm(challengeID, principalID string, proof Proof) (Ticket, error) {
	return Ticket{}, errNotImplemented
}

// Redeem consumes a ticket on the channel it was issued for.
func (g *TransactionConfirmationGuard) Redeem(ticketID, opID string) (Decision, error) {
	return Decision{}, errNotImplemented
}

// RedeemOn consumes a ticket and checks the calling channel against the one the
// ticket was bound to.
func (g *TransactionConfirmationGuard) RedeemOn(ticketID, opID, channel string) (Decision, error) {
	return Decision{}, errNotImplemented
}

// Cancel closes an open challenge.
func (g *TransactionConfirmationGuard) Cancel(challengeID string) error {
	return errNotImplemented
}

// Pending lists the open challenges of one user, oldest first.
func (g *TransactionConfirmationGuard) Pending(userID string) []Challenge {
	return nil
}

// LookupChallenge reads one challenge record.
func (g *TransactionConfirmationGuard) LookupChallenge(challengeID string) (Challenge, bool) {
	return Challenge{}, false
}

// LookupTicket reads one ticket record.
func (g *TransactionConfirmationGuard) LookupTicket(ticketID string) (Ticket, bool) {
	return Ticket{}, false
}

// Snapshot serialises challenges, tickets, digests, expiry deadlines and the
// monotonic counters.
func (g *TransactionConfirmationGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *TransactionConfirmationGuard) Restore(data []byte) error {
	return errNotImplemented
}
