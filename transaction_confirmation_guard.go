// Package task closes the confirmation loop of high risk transactions at the
// authenticator: what was confirmed is what gets redeemed, once.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

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

const snapshotVersion = 1

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

type storedChallenge struct {
	Challenge
	Channel string `json:"channel"`
}

type snapshotData struct {
	Version    int               `json:"version"`
	Seq        int64             `json:"seq"`
	Challenges []storedChallenge `json:"challenges"`
	Tickets    []Ticket          `json:"tickets"`
}

// TransactionConfirmationGuard owns the challenge table, the ticket table and
// the counters.
type TransactionConfirmationGuard struct {
	opts Options
	now  func() time.Time

	mu          sync.Mutex
	seq         int64
	challenges  map[string]*storedChallenge
	tickets     map[string]*Ticket
	opChallenge map[string]string
	opTickets   map[string][]string
}

// NewTransactionConfirmationGuard builds a guard with the given options.
func NewTransactionConfirmationGuard(opts Options) *TransactionConfirmationGuard {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &TransactionConfirmationGuard{
		opts:        opts,
		now:         now,
		challenges:  make(map[string]*storedChallenge),
		tickets:     make(map[string]*Ticket),
		opChallenge: make(map[string]string),
		opTickets:   make(map[string][]string),
	}
}

// Prepare canonicalises the transaction, picks the tier and registers a
// challenge, or a ready ticket for an exempt amount.
func (g *TransactionConfirmationGuard) Prepare(opID string, tx Transaction) (Challenge, error) {
	opID = strings.TrimSpace(opID)
	if opID == "" || strings.TrimSpace(tx.UserID) == "" {
		return Challenge{}, ErrBlankID
	}
	if tx.AmountCents <= 0 {
		return Challenge{}, ErrBadAmount
	}

	tx = normalizeTransaction(tx)
	if tx.Payee == "" || tx.PayeeAccount == "" || tx.Channel == "" {
		return Challenge{}, ErrBadTransaction
	}

	tier, required := tierForAmount(tx.AmountCents, g.opts)
	digest := transactionDigest(tx)

	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()

	oldID := g.opChallenge[opID]
	old := g.challenges[oldID]

	if tier != TierExempt {
		otherPending := 0
		for id, challenge := range g.challenges {
			if challenge.UserID != tx.UserID || challenge.State != ChallengeOpen {
				continue
			}
			if old != nil && id == old.ID {
				continue
			}
			otherPending++
		}
		if otherPending >= g.opts.MaxPendingChallenges {
			return Challenge{}, ErrChallengeLimit
		}
	}

	if old != nil && old.State == ChallengeOpen {
		old.State = ChallengeSuperseded
	}
	g.invalidateChangedTicketsLocked(opID, digest)

	seq := g.nextSeqLocked()
	challenge := Challenge{
		ID:         fmt.Sprintf("challenge-%d", seq),
		OpID:       opID,
		UserID:     tx.UserID,
		Digest:     digest,
		Tier:       tier,
		Required:   required,
		Principals: []string{},
		State:      ChallengeOpen,
		ExpiresAt:  now.Add(g.opts.ChallengeTTL),
		Seq:        seq,
	}
	record := &storedChallenge{Challenge: challenge, Channel: tx.Channel}
	g.challenges[challenge.ID] = record
	g.opChallenge[opID] = challenge.ID

	if tier == TierExempt {
		g.issueTicketLocked(record, ReasonExempt, now)
	}

	return record.copy(), nil
}

// Confirm records one principal's confirmation and issues the ticket once the
// quorum is reached.
func (g *TransactionConfirmationGuard) Confirm(challengeID, principalID string, proof Proof) (Ticket, error) {
	challengeID = strings.TrimSpace(challengeID)
	principalID = strings.TrimSpace(principalID)

	g.mu.Lock()
	defer g.mu.Unlock()

	record, ok := g.challenges[challengeID]
	if !ok {
		return Ticket{}, ErrChallengeNotFound
	}
	if principalID == "" {
		return Ticket{}, ErrBlankID
	}

	challenge := &record.Challenge
	switch challenge.State {
	case ChallengeSuperseded:
		return Ticket{}, ErrChallengeStale
	case ChallengeClosed:
		return Ticket{}, ErrChallengeClosed
	}

	now := g.now()
	if isExpired(challenge.ExpiresAt, now) {
		return Ticket{}, ErrChallengeExpired
	}
	if challenge.Attempts >= g.opts.MaxConfirmAttempts {
		return Ticket{}, ErrAttemptLimit
	}
	if strings.TrimSpace(proof.Signature) == "" || strings.TrimSpace(proof.DeviceID) == "" {
		challenge.Attempts++
		return Ticket{}, ErrBadProof
	}

	for _, existing := range challenge.Principals {
		if existing == principalID {
			return Ticket{}, ErrNotConfirmed
		}
	}

	challenge.Principals = append(challenge.Principals, principalID)
	challenge.Confirmed = len(challenge.Principals)
	if challenge.Confirmed < challenge.Required {
		return Ticket{}, ErrNotConfirmed
	}

	return g.issueTicketLocked(record, ReasonConfirmed, now), nil
}

// Redeem consumes a ticket without imposing a caller-channel check.
func (g *TransactionConfirmationGuard) Redeem(ticketID, opID string) (Decision, error) {
	return g.redeem(ticketID, opID, "", false)
}

// RedeemOn consumes a ticket and checks the calling channel against the one the
// ticket was bound to.
func (g *TransactionConfirmationGuard) RedeemOn(ticketID, opID, channel string) (Decision, error) {
	return g.redeem(ticketID, opID, strings.TrimSpace(channel), true)
}

// Cancel closes an open challenge.
func (g *TransactionConfirmationGuard) Cancel(challengeID string) error {
	challengeID = strings.TrimSpace(challengeID)

	g.mu.Lock()
	defer g.mu.Unlock()

	record, ok := g.challenges[challengeID]
	if !ok {
		return ErrChallengeNotFound
	}
	if record.State != ChallengeOpen {
		return ErrChallengeClosed
	}

	record.State = ChallengeClosed
	return nil
}

// Pending lists the open challenges of one user, oldest first.
func (g *TransactionConfirmationGuard) Pending(userID string) []Challenge {
	userID = strings.TrimSpace(userID)

	g.mu.Lock()
	defer g.mu.Unlock()

	pending := make([]Challenge, 0)
	for _, record := range g.challenges {
		if record.UserID == userID && record.State == ChallengeOpen {
			pending = append(pending, record.copy())
		}
	}
	sort.Slice(pending, func(i, j int) bool {
		return pending[i].Seq < pending[j].Seq
	})
	return pending
}

// LookupChallenge reads one challenge record.
func (g *TransactionConfirmationGuard) LookupChallenge(challengeID string) (Challenge, bool) {
	challengeID = strings.TrimSpace(challengeID)

	g.mu.Lock()
	defer g.mu.Unlock()

	record, ok := g.challenges[challengeID]
	if !ok {
		return Challenge{}, false
	}
	return record.copy(), true
}

// LookupTicket reads one ticket record.
func (g *TransactionConfirmationGuard) LookupTicket(ticketID string) (Ticket, bool) {
	ticketID = strings.TrimSpace(ticketID)

	g.mu.Lock()
	defer g.mu.Unlock()

	ticket, ok := g.tickets[ticketID]
	if !ok {
		return Ticket{}, false
	}
	return *ticket, true
}

// Snapshot serialises challenges, tickets, digests, expiry deadlines and the
// monotonic counters.
func (g *TransactionConfirmationGuard) Snapshot() ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	data := snapshotData{
		Version:    snapshotVersion,
		Seq:        g.seq,
		Challenges: make([]storedChallenge, 0, len(g.challenges)),
		Tickets:    make([]Ticket, 0, len(g.tickets)),
	}
	for _, record := range g.challenges {
		data.Challenges = append(data.Challenges, *record)
	}
	for _, ticket := range g.tickets {
		data.Tickets = append(data.Tickets, *ticket)
	}
	sort.Slice(data.Challenges, func(i, j int) bool {
		return data.Challenges[i].Seq < data.Challenges[j].Seq
	})
	sort.Slice(data.Tickets, func(i, j int) bool {
		return data.Tickets[i].Seq < data.Tickets[j].Seq
	})

	payload, err := json.Marshal(&data)
	if err != nil {
		return nil, fmt.Errorf("encode snapshot: %w", err)
	}
	return payload, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *TransactionConfirmationGuard) Restore(data []byte) error {
	state, err := buildRestoredState(data)
	if err != nil {
		return err
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	g.seq = state.seq
	g.challenges = state.challenges
	g.tickets = state.tickets
	g.opChallenge = state.opChallenge
	g.opTickets = state.opTickets
	return nil
}

type restoredState struct {
	seq         int64
	challenges  map[string]*storedChallenge
	tickets     map[string]*Ticket
	opChallenge map[string]string
	opTickets   map[string][]string
}

func buildRestoredState(data []byte) (restoredState, error) {
	var snapshot snapshotData
	if len(strings.TrimSpace(string(data))) == 0 {
		return restoredState{}, fmt.Errorf("%w: empty payload", ErrBadSnapshot)
	}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return restoredState{}, fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	if snapshot.Version != snapshotVersion || snapshot.Seq < 0 {
		return restoredState{}, fmt.Errorf("%w: unsupported snapshot envelope", ErrBadSnapshot)
	}

	state := restoredState{
		seq:         snapshot.Seq,
		challenges:  make(map[string]*storedChallenge, len(snapshot.Challenges)),
		tickets:     make(map[string]*Ticket, len(snapshot.Tickets)),
		opChallenge: make(map[string]string),
		opTickets:   make(map[string][]string),
	}

	for i := range snapshot.Challenges {
		record := snapshot.Challenges[i]
		record.Principals = append([]string(nil), record.Principals...)
		if err := validateChallengeSnapshot(&record); err != nil {
			return restoredState{}, err
		}
		if _, exists := state.challenges[record.ID]; exists {
			return restoredState{}, fmt.Errorf("%w: duplicate challenge %q", ErrBadSnapshot, record.ID)
		}
		state.challenges[record.ID] = &record
		if record.Seq > state.seq {
			state.seq = record.Seq
		}

		currentID := state.opChallenge[record.OpID]
		current, exists := state.challenges[currentID]
		if !exists || record.Seq > current.Seq {
			state.opChallenge[record.OpID] = record.ID
		}
	}

	for i := range snapshot.Tickets {
		ticket := snapshot.Tickets[i]
		if err := validateTicketSnapshot(&ticket); err != nil {
			return restoredState{}, err
		}
		if _, exists := state.tickets[ticket.ID]; exists {
			return restoredState{}, fmt.Errorf("%w: duplicate ticket %q", ErrBadSnapshot, ticket.ID)
		}
		state.tickets[ticket.ID] = &ticket
		state.opTickets[ticket.OpID] = append(state.opTickets[ticket.OpID], ticket.ID)
		if ticket.Seq > state.seq {
			state.seq = ticket.Seq
		}
	}

	for _, record := range state.challenges {
		if record.TicketID == "" {
			continue
		}
		ticket, ok := state.tickets[record.TicketID]
		if !ok {
			return restoredState{}, fmt.Errorf("%w: challenge %q references missing ticket %q", ErrBadSnapshot, record.ID, record.TicketID)
		}
		if ticket.OpID != record.OpID || ticket.Digest != record.Digest {
			return restoredState{}, fmt.Errorf("%w: challenge/ticket binding mismatch for %q", ErrBadSnapshot, record.ID)
		}
	}

	return state, nil
}

func validateChallengeSnapshot(record *storedChallenge) error {
	if record.ID == "" || record.OpID == "" || record.UserID == "" || record.Channel == "" {
		return fmt.Errorf("%w: challenge is missing identifiers", ErrBadSnapshot)
	}
	if record.Seq <= 0 || record.ExpiresAt.IsZero() {
		return fmt.Errorf("%w: challenge %q has invalid sequence or expiry", ErrBadSnapshot, record.ID)
	}
	if !validDigest(record.Digest) {
		return fmt.Errorf("%w: challenge %q has invalid digest", ErrBadSnapshot, record.ID)
	}
	if record.Tier != TierExempt && record.Tier != TierSingle && record.Tier != TierDual {
		return fmt.Errorf("%w: challenge %q has invalid tier", ErrBadSnapshot, record.ID)
	}
	if record.State != ChallengeOpen && record.State != ChallengeClosed && record.State != ChallengeSuperseded {
		return fmt.Errorf("%w: challenge %q has invalid state", ErrBadSnapshot, record.ID)
	}
	if record.Required < 0 || record.Confirmed < 0 || record.Attempts < 0 || record.Confirmed > record.Required {
		return fmt.Errorf("%w: challenge %q has invalid counters", ErrBadSnapshot, record.ID)
	}
	if len(record.Principals) != record.Confirmed {
		return fmt.Errorf("%w: challenge %q principal count does not match confirmed count", ErrBadSnapshot, record.ID)
	}
	seen := make(map[string]struct{}, len(record.Principals))
	for _, principal := range record.Principals {
		if principal == "" {
			return fmt.Errorf("%w: challenge %q has an empty principal", ErrBadSnapshot, record.ID)
		}
		if _, duplicate := seen[principal]; duplicate {
			return fmt.Errorf("%w: challenge %q has duplicate principal %q", ErrBadSnapshot, record.ID, principal)
		}
		seen[principal] = struct{}{}
	}
	return nil
}

func validateTicketSnapshot(ticket *Ticket) error {
	if ticket.ID == "" || ticket.OpID == "" || ticket.UserID == "" || ticket.Channel == "" {
		return fmt.Errorf("%w: ticket is missing identifiers", ErrBadSnapshot)
	}
	if ticket.Seq <= 0 || ticket.ExpiresAt.IsZero() {
		return fmt.Errorf("%w: ticket %q has invalid sequence or expiry", ErrBadSnapshot, ticket.ID)
	}
	if !validDigest(ticket.Digest) {
		return fmt.Errorf("%w: ticket %q has invalid digest", ErrBadSnapshot, ticket.ID)
	}
	if ticket.State != TicketReady && ticket.State != TicketUsed && ticket.State != TicketStale {
		return fmt.Errorf("%w: ticket %q has invalid state", ErrBadSnapshot, ticket.ID)
	}
	return nil
}

func validDigest(digest string) bool {
	if len(digest) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(digest)
	return err == nil
}

func (g *TransactionConfirmationGuard) redeem(ticketID, opID, channel string, checkChannel bool) (Decision, error) {
	ticketID = strings.TrimSpace(ticketID)
	opID = strings.TrimSpace(opID)

	g.mu.Lock()
	defer g.mu.Unlock()

	ticket, ok := g.tickets[ticketID]
	if !ok {
		return Decision{}, ErrTicketNotFound
	}
	if ticket.OpID != opID {
		return Decision{}, ErrOpMismatch
	}
	if ticket.State == TicketUsed {
		return Decision{}, ErrTicketUsed
	}
	if ticket.State == TicketStale {
		return Decision{}, ErrTicketStale
	}
	if isExpired(ticket.ExpiresAt, g.now()) {
		return Decision{}, ErrTicketExpired
	}
	if checkChannel && channel != ticket.Channel {
		return Decision{}, ErrChannelMismatch
	}
	if currentID := g.opChallenge[ticket.OpID]; currentID != "" {
		if current := g.challenges[currentID]; current != nil && current.Digest != ticket.Digest {
			ticket.State = TicketStale
			return Decision{}, ErrTicketStale
		}
	}

	ticket.State = TicketUsed
	return Decision{
		OpID:     ticket.OpID,
		TicketID: ticket.ID,
		Approved: true,
		Reason:   ticket.Reason,
		Seq:      ticket.Seq,
	}, nil
}

func (g *TransactionConfirmationGuard) nextSeqLocked() int64 {
	g.seq++
	return g.seq
}

func (g *TransactionConfirmationGuard) issueTicketLocked(record *storedChallenge, reason string, now time.Time) Ticket {
	seq := g.nextSeqLocked()
	ticket := Ticket{
		ID:        fmt.Sprintf("ticket-%d", seq),
		OpID:      record.OpID,
		UserID:    record.UserID,
		Digest:    record.Digest,
		Channel:   record.Channel,
		Reason:    reason,
		State:     TicketReady,
		ExpiresAt: now.Add(g.opts.TicketTTL),
		Seq:       seq,
	}
	g.tickets[ticket.ID] = &ticket
	g.opTickets[ticket.OpID] = append(g.opTickets[ticket.OpID], ticket.ID)
	record.TicketID = ticket.ID
	record.State = ChallengeClosed
	return ticket
}

func (g *TransactionConfirmationGuard) invalidateChangedTicketsLocked(opID, digest string) {
	for _, ticketID := range g.opTickets[opID] {
		ticket := g.tickets[ticketID]
		if ticket == nil || ticket.State != TicketReady {
			continue
		}
		if ticket.Digest != digest {
			ticket.State = TicketStale
		}
	}
}

func (r *storedChallenge) copy() Challenge {
	return cloneChallenge(r.Challenge)
}

func cloneChallenge(challenge Challenge) Challenge {
	challenge.Principals = append([]string(nil), challenge.Principals...)
	return challenge
}

func normalizeTransaction(tx Transaction) Transaction {
	tx.UserID = strings.TrimSpace(tx.UserID)
	tx.Currency = strings.TrimSpace(tx.Currency)
	tx.Payee = strings.TrimSpace(tx.Payee)
	tx.PayeeAccount = strings.TrimSpace(tx.PayeeAccount)
	tx.Channel = strings.TrimSpace(tx.Channel)
	return tx
}

func transactionDigest(tx Transaction) string {
	canonical, err := json.Marshal([5]any{
		tx.UserID,
		tx.AmountCents,
		tx.Currency,
		tx.Payee,
		tx.PayeeAccount,
	})
	if err != nil {
		panic(fmt.Sprintf("canonical transaction encoding failed: %v", err))
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

func tierForAmount(amount int64, opts Options) (string, int) {
	switch {
	case amount < opts.SmallLimitCents:
		return TierExempt, 0
	case amount < opts.LargeLimitCents:
		return TierSingle, 1
	default:
		return TierDual, 2
	}
}

func isExpired(deadline, now time.Time) bool {
	return !now.Before(deadline)
}
