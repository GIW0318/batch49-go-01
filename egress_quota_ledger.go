// Package task keeps the egress quota ledger of one gateway: category aware
// admission, reserved versus settled bytes and the per tenant breaker.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Destination kinds. Management and audit traffic stays outside the breaker.
const (
	KindData       = "data"
	KindManagement = "management"
	KindAudit      = "audit"

	CategoryUnknown = "unknown"
	CategoryKnown   = "known"
)

// Verdict reasons.
const (
	ReasonAllowed       = "allowed"
	ReasonQuotaExceeded = "quota_exceeded"
	ReasonBreakerOpen   = "breaker_open"
)

const (
	snapshotFormatVersion = 1
	// defaultCooldown keeps a breaker open for one hour when Limits.Cooldown
	// was not configured.
	defaultCooldown = time.Hour
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
// breakers. Every adjudication is serialised through mu so concurrent
// reservations can never overshoot a quota and snapshots stay consistent.
type EgressQuotaLedger struct {
	limits Limits
	now    func() time.Time

	mu      sync.Mutex
	nextID  uint64
	tenants map[string]*tenantState
	tickets map[uint64]*ticketRecord
}

// tenantState is the isolated window, sample set, learned categories and
// breaker of one tenant.
type tenantState struct {
	knownUsed   int64
	exploreUsed int64
	reserved    int64
	rejected    int

	samples    []string
	categories map[string]string
	cooldown   time.Duration

	open      bool
	openedAt  time.Time
	recoverAt time.Time
	reason    string
}

// ticketRecord is the stored reservation. Removing it from the ledger ticket
// map invalidates the ticket after settle or release.
type ticketRecord struct {
	id       uint64
	tenant   string
	category string
	reserved int64
}

// NewEgressQuotaLedger builds a ledger backed by the wall clock.
func NewEgressQuotaLedger(limits Limits) *EgressQuotaLedger {
	return NewEgressQuotaLedgerWithClock(limits, time.Now)
}

// NewEgressQuotaLedgerWithClock builds a ledger with an injected clock.
func NewEgressQuotaLedgerWithClock(limits Limits, now func() time.Time) *EgressQuotaLedger {
	if now == nil {
		now = time.Now
	}
	return &EgressQuotaLedger{
		limits:  limits,
		now:     now,
		tenants: make(map[string]*tenantState),
		tickets: make(map[uint64]*ticketRecord),
	}
}

func blank(s string) bool {
	return strings.TrimSpace(s) == ""
}

func canonical(s string) string {
	return strings.TrimSpace(s)
}

func validKind(kind string) bool {
	switch kind {
	case KindData, KindManagement, KindAudit:
		return true
	default:
		return false
	}
}

// isUnknownClass reports whether a category is billed against the explore
// budget. Every other non empty category is a known category.
func isUnknownClass(category string) bool {
	return category == "" || category == CategoryUnknown
}

func (l *EgressQuotaLedger) clockNow() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

// initLocked makes the ledger usable even when a caller skips the
// constructor. Callers must hold l.mu.
func (l *EgressQuotaLedger) initLocked() {
	if l.tenants == nil {
		l.tenants = make(map[string]*tenantState)
	}
	if l.tickets == nil {
		l.tickets = make(map[uint64]*ticketRecord)
	}
}

// tenantLocked returns the isolated state of tenant, creating it on first
// use. Callers must hold l.mu.
func (l *EgressQuotaLedger) tenantLocked(tenant string) *tenantState {
	ts := l.tenants[tenant]
	if ts == nil {
		ts = &tenantState{categories: make(map[string]string)}
		l.tenants[tenant] = ts
	}
	if ts.categories == nil {
		ts.categories = make(map[string]string)
	}
	return ts
}

// effectiveCooldownLocked resolves the per tenant override, then the ledger
// wide Limits.Cooldown and finally the one hour default.
func (l *EgressQuotaLedger) effectiveCooldownLocked(ts *tenantState) time.Duration {
	if ts.cooldown > 0 {
		return ts.cooldown
	}
	if l.limits.Cooldown > 0 {
		return l.limits.Cooldown
	}
	return defaultCooldown
}

// recoverLocked closes an open breaker once the cooldown elapsed and clears
// the consecutive rejection streak. Recovery is evaluated lazily so injected
// clocks and plain getters observe the transition. Callers must hold l.mu.
func (l *EgressQuotaLedger) recoverLocked(ts *tenantState) {
	if !ts.open {
		return
	}
	if l.clockNow().Before(ts.recoverAt) {
		return
	}
	ts.open = false
	ts.rejected = 0
	ts.openedAt = time.Time{}
	ts.recoverAt = time.Time{}
	ts.reason = ""
}

// recordRejectionLocked counts one consecutive quota rejection and opens the
// whole tenant breaker at the threshold. The triggering call keeps its quota
// error. Callers must hold l.mu.
func (l *EgressQuotaLedger) recordRejectionLocked(ts *tenantState) {
	ts.rejected++
	if l.limits.RejectThreshold > 0 &&
		ts.rejected >= l.limits.RejectThreshold && !ts.open {
		ts.open = true
		ts.openedAt = l.clockNow()
		ts.recoverAt = ts.openedAt.Add(l.effectiveCooldownLocked(ts))
		ts.reason = ReasonQuotaExceeded
	}
}

// remainingLocked reports the category budget left after settled and
// reserved bytes. Callers must hold l.mu.
func (l *EgressQuotaLedger) remainingLocked(ts *tenantState, category string) int64 {
	if isUnknownClass(category) {
		return l.limits.ExploreQuota - ts.exploreUsed - ts.reserved
	}
	return l.limits.KnownQuota - ts.knownUsed - ts.reserved
}

// resolveCategory applies an explicit category first and a learned host
// mapping second, falling back to the unknown category.
func resolveCategory(dest Dest, learned map[string]string, host string) string {
	category := canonical(dest.Category)
	if !isUnknownClass(category) {
		return category
	}
	if mapped := learned[host]; mapped != "" {
		return mapped
	}
	return CategoryUnknown
}

// addSample retains unknown hosts in first appearance order, deduplicated.
// Once the bound is exceeded the oldest sample is evicted.
func (ts *tenantState) addSample(host string, maxSamples int) {
	if maxSamples <= 0 || host == "" {
		return
	}
	for _, existing := range ts.samples {
		if existing == host {
			return
		}
	}
	ts.samples = append(ts.samples, host)
	if len(ts.samples) > maxSamples {
		kept := ts.samples[len(ts.samples)-maxSamples:]
		ts.samples = append(make([]string, 0, maxSamples), kept...)
	}
}

// Check answers whether one destination would be admitted right now. It is a
// read only prediction for quota and reservations: it never creates a ticket
// nor moves settled usage. Observing an unknown host does retain its sample.
func (l *EgressQuotaLedger) Check(tenant string, dest Dest) (Verdict, error) {
	if blank(tenant) {
		return Verdict{}, ErrBlankTenant
	}
	if blank(dest.Host) {
		return Verdict{}, ErrBlankDestination
	}
	if !validKind(dest.Kind) {
		return Verdict{}, ErrBadCategory
	}

	tenantID := canonical(tenant)
	host := canonical(dest.Host)

	l.mu.Lock()
	defer l.mu.Unlock()
	l.initLocked()
	ts := l.tenantLocked(tenantID)
	l.recoverLocked(ts)

	category := resolveCategory(dest, ts.categories, host)
	if isUnknownClass(category) {
		ts.addSample(host, l.limits.MaxSamples)
	}

	remaining := l.remainingLocked(ts, category)
	if remaining < 0 {
		remaining = 0
	}

	// Health checks, audit uploads and other management channels stay
	// admitted while the data plane breaker is open. They never spend quota
	// and never feed the rejection streak.
	if dest.Kind == KindManagement || dest.Kind == KindAudit {
		return Verdict{
			Allowed:   true,
			Category:  category,
			Remaining: remaining,
			Reason:    ReasonAllowed,
		}, nil
	}

	if ts.open {
		return Verdict{
			Allowed:   false,
			Category:  category,
			Remaining: remaining,
			Reason:    ReasonBreakerOpen,
		}, nil
	}

	if remaining <= 0 {
		return Verdict{
			Allowed:   false,
			Category:  category,
			Remaining: 0,
			Reason:    ReasonQuotaExceeded,
		}, nil
	}

	return Verdict{
		Allowed:   true,
		Category:  category,
		Remaining: remaining,
		Reason:    ReasonAllowed,
	}, nil
}

// Reserve books an estimate against the known category budget. The check and
// the booking happen atomically, so an over quota estimate never leaves a
// partial reservation behind.
func (l *EgressQuotaLedger) Reserve(tenant string, est int64) (Ticket, error) {
	if blank(tenant) {
		return Ticket{}, ErrBlankTenant
	}
	if est <= 0 {
		return Ticket{}, ErrBadEstimate
	}

	tenantID := canonical(tenant)

	l.mu.Lock()
	defer l.mu.Unlock()
	l.initLocked()
	ts := l.tenantLocked(tenantID)
	l.recoverLocked(ts)

	if ts.open {
		return Ticket{}, ErrBreakerOpen
	}

	remaining := l.limits.KnownQuota - ts.knownUsed - ts.reserved
	if est > remaining {
		// Rejection counters and the breaker are the only mutation on the
		// denied path; Reserved is deliberately untouched.
		l.recordRejectionLocked(ts)
		return Ticket{}, ErrQuotaExceeded
	}

	l.nextID++
	ticket := Ticket{
		ID:       l.nextID,
		Tenant:   tenantID,
		Category: CategoryKnown,
		Reserved: est,
	}
	l.tickets[ticket.ID] = &ticketRecord{
		id:       ticket.ID,
		tenant:   ticket.Tenant,
		category: ticket.Category,
		reserved: ticket.Reserved,
	}
	ts.reserved += est
	// An admitted reservation breaks the consecutive rejection streak.
	ts.rejected = 0

	return ticket, nil
}

// lookupTicketLocked resolves and ownership checks a ticket. Callers must
// hold l.mu.
func (l *EgressQuotaLedger) lookupTicketLocked(t Ticket) (*ticketRecord, *tenantState, error) {
	rec, ok := l.tickets[t.ID]
	if !ok || rec.tenant != canonical(t.Tenant) {
		return nil, nil, ErrUnknownTicket
	}
	ts := l.tenantLocked(rec.tenant)
	return rec, ts, nil
}

// Settle closes a ticket with the bytes actually used. Spending below the
// estimate frees the difference, spending above it additionally records the
// overage. The ticket is invalid afterwards.
func (l *EgressQuotaLedger) Settle(t Ticket, used int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.initLocked()

	rec, ts, err := l.lookupTicketLocked(t)
	if err != nil {
		return err
	}
	if used < 0 {
		// Reject before touching any counter and keep the ticket valid.
		return ErrBadUsage
	}

	ts.reserved -= rec.reserved
	if ts.reserved < 0 {
		ts.reserved = 0
	}
	if isUnknownClass(rec.category) {
		ts.exploreUsed += used
	} else {
		ts.knownUsed += used
	}

	delete(l.tickets, rec.id)
	return nil
}

// Release closes a ticket without consuming anything and returns the whole
// reservation. The ticket is invalid afterwards, so a repeated release cannot
// hand the bytes back twice.
func (l *EgressQuotaLedger) Release(t Ticket) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.initLocked()

	rec, ts, err := l.lookupTicketLocked(t)
	if err != nil {
		return err
	}

	ts.reserved -= rec.reserved
	if ts.reserved < 0 {
		ts.reserved = 0
	}

	delete(l.tickets, rec.id)
	return nil
}

// Usage reports the rolling window usage of one tenant.
func (l *EgressQuotaLedger) Usage(tenant string) Usage {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.tenants == nil {
		return Usage{}
	}
	ts := l.tenants[canonical(tenant)]
	if ts == nil {
		return Usage{}
	}
	l.recoverLocked(ts)

	return Usage{
		Known:    ts.knownUsed,
		Explore:  ts.exploreUsed,
		Reserved: ts.reserved,
		Rejected: ts.rejected,
	}
}

// Breaker reports the breaker state of one tenant.
func (l *EgressQuotaLedger) Breaker(tenant string) BreakerState {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.tenants == nil {
		return BreakerState{}
	}
	ts := l.tenants[canonical(tenant)]
	if ts == nil {
		return BreakerState{}
	}
	l.recoverLocked(ts)

	return BreakerState{
		Open:        ts.open,
		Consecutive: ts.rejected,
		OpenedAt:    ts.openedAt,
		RecoverAt:   ts.recoverAt,
		Reason:      ts.reason,
	}
}

// Samples lists the unknown destination hosts seen for one tenant in first
// appearance order. The returned slice is a copy safe for callers to retain.
func (l *EgressQuotaLedger) Samples(tenant string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	empty := []string{}
	if l.tenants == nil {
		return empty
	}
	ts := l.tenants[canonical(tenant)]
	if ts == nil || len(ts.samples) == 0 {
		return empty
	}
	out := make([]string, len(ts.samples))
	copy(out, ts.samples)
	return out
}

// LearnCategory promotes a host into a known category. Historical explore
// usage is intentionally left untouched and already settled bills are not
// rewritten; only later admissions use the new price class.
func (l *EgressQuotaLedger) LearnCategory(tenant, host, category string) error {
	if blank(tenant) {
		return ErrBlankTenant
	}
	if blank(host) {
		return ErrBlankDestination
	}
	if blank(category) || isUnknownClass(canonical(category)) {
		return ErrBadCategory
	}

	tenantID := canonical(tenant)
	hostID := canonical(host)

	l.mu.Lock()
	defer l.mu.Unlock()
	l.initLocked()
	ts := l.tenantLocked(tenantID)
	ts.categories[hostID] = canonical(category)
	return nil
}

// SetCooldown adjusts the breaker recovery delay for one tenant. A zero or
// negative duration removes the override and falls back to Limits.Cooldown
// and then the one hour default.
func (l *EgressQuotaLedger) SetCooldown(tenant string, cooldown time.Duration) error {
	if blank(tenant) {
		return ErrBlankTenant
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	l.initLocked()
	ts := l.tenantLocked(canonical(tenant))
	if cooldown < 0 {
		cooldown = 0
	}
	ts.cooldown = cooldown
	if ts.open && !ts.openedAt.IsZero() {
		ts.recoverAt = ts.openedAt.Add(l.effectiveCooldownLocked(ts))
	}
	return nil
}

// ResetBreaker manually closes a tenant breaker and clears its consecutive
// rejection streak.
func (l *EgressQuotaLedger) ResetBreaker(tenant string) error {
	if blank(tenant) {
		return ErrBlankTenant
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	l.initLocked()
	ts := l.tenantLocked(canonical(tenant))
	ts.open = false
	ts.rejected = 0
	ts.openedAt = time.Time{}
	ts.recoverAt = time.Time{}
	ts.reason = ""
	return nil
}

// snapshotBreaker is the serialised breaker state.
type snapshotBreaker struct {
	Open      bool      `json:"open"`
	OpenedAt  time.Time `json:"opened_at"`
	RecoverAt time.Time `json:"recover_at"`
	Reason    string    `json:"reason"`
}

// snapshotTenant is the serialised state of one tenant.
type snapshotTenant struct {
	Tenant      string            `json:"tenant"`
	KnownUsed   int64             `json:"known_used"`
	ExploreUsed int64             `json:"explore_used"`
	Reserved    int64             `json:"reserved"`
	Rejected    int               `json:"rejected"`
	Cooldown    time.Duration     `json:"cooldown_ns"`
	Samples     []string          `json:"samples"`
	Categories  map[string]string `json:"categories"`
	Breaker     snapshotBreaker   `json:"breaker"`
}

// snapshotTicket is the serialised outstanding ticket.
type snapshotTicket struct {
	ID       uint64 `json:"id"`
	Tenant   string `json:"tenant"`
	Category string `json:"category"`
	Reserved int64  `json:"reserved"`
}

// snapshotData is the whole ledger envelope.
type snapshotData struct {
	Version      int              `json:"version"`
	NextTicketID uint64           `json:"next_ticket_id"`
	Tenants      []snapshotTenant `json:"tenants"`
	Tickets      []snapshotTicket `json:"tickets"`
}

// Snapshot serialises window usage, outstanding tickets, the ticket id
// watermark, sample order, learned categories and breaker state.
func (l *EgressQuotaLedger) Snapshot() ([]byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.initLocked()

	tenantNames := make([]string, 0, len(l.tenants))
	for name := range l.tenants {
		tenantNames = append(tenantNames, name)
	}
	sort.Strings(tenantNames)

	snap := snapshotData{
		Version:      snapshotFormatVersion,
		NextTicketID: l.nextID,
		Tenants:      make([]snapshotTenant, 0, len(tenantNames)),
		Tickets:      make([]snapshotTicket, 0, len(l.tickets)),
	}

	for _, name := range tenantNames {
		ts := l.tenants[name]
		st := snapshotTenant{
			Tenant:      name,
			KnownUsed:   ts.knownUsed,
			ExploreUsed: ts.exploreUsed,
			Reserved:    ts.reserved,
			Rejected:    ts.rejected,
			Cooldown:    ts.cooldown,
			Samples:     append([]string{}, ts.samples...),
			Categories:  make(map[string]string, len(ts.categories)),
			Breaker: snapshotBreaker{
				Open:      ts.open,
				OpenedAt:  ts.openedAt,
				RecoverAt: ts.recoverAt,
				Reason:    ts.reason,
			},
		}
		for host, category := range ts.categories {
			st.Categories[host] = category
		}
		snap.Tenants = append(snap.Tenants, st)
	}

	ticketIDs := make([]uint64, 0, len(l.tickets))
	for id := range l.tickets {
		ticketIDs = append(ticketIDs, id)
	}
	sort.Slice(ticketIDs, func(i, j int) bool { return ticketIDs[i] < ticketIDs[j] })
	for _, id := range ticketIDs {
		rec := l.tickets[id]
		snap.Tickets = append(snap.Tickets, snapshotTicket{
			ID:       rec.id,
			Tenant:   rec.tenant,
			Category: rec.category,
			Reserved: rec.reserved,
		})
	}

	payload, err := json.Marshal(snap)
	if err != nil {
		return nil, err
	}
	return payload, nil
}

// Restore replaces the whole state with a snapshot. The payload is parsed and
// validated before the live state is touched, so any failure leaves the
// current ledger untouched.
func (l *EgressQuotaLedger) Restore(payload []byte) error {
	var snap snapshotData
	if err := json.Unmarshal(payload, &snap); err != nil {
		return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	if snap.Version != snapshotFormatVersion {
		return fmt.Errorf("%w: unsupported snapshot version %d", ErrBadSnapshot, snap.Version)
	}

	newTenants := make(map[string]*tenantState, len(snap.Tenants))
	for _, st := range snap.Tenants {
		if blank(st.Tenant) {
			return fmt.Errorf("%w: tenant name is empty", ErrBadSnapshot)
		}
		name := canonical(st.Tenant)
		if _, duplicated := newTenants[name]; duplicated {
			return fmt.Errorf("%w: duplicate tenant %q", ErrBadSnapshot, name)
		}
		if st.KnownUsed < 0 || st.ExploreUsed < 0 || st.Reserved < 0 || st.Rejected < 0 {
			return fmt.Errorf("%w: negative tenant counter", ErrBadSnapshot)
		}
		if st.Cooldown < 0 {
			return fmt.Errorf("%w: negative cooldown", ErrBadSnapshot)
		}

		categories := make(map[string]string, len(st.Categories))
		for host, category := range st.Categories {
			if host == "" || category == "" {
				continue
			}
			categories[canonical(host)] = canonical(category)
		}

		ts := &tenantState{
			knownUsed:   st.KnownUsed,
			exploreUsed: st.ExploreUsed,
			rejected:    st.Rejected,
			samples:     normalizeSamples(st.Samples, l.limits.MaxSamples),
			categories:  categories,
			cooldown:    st.Cooldown,
			open:        st.Breaker.Open,
			openedAt:    st.Breaker.OpenedAt,
			recoverAt:   st.Breaker.RecoverAt,
			reason:      st.Breaker.Reason,
		}
		newTenants[name] = ts
	}

	newTickets := make(map[uint64]*ticketRecord, len(snap.Tickets))
	var maxTicketID uint64
	for _, st := range snap.Tickets {
		if st.ID == 0 {
			return fmt.Errorf("%w: ticket id must be positive", ErrBadSnapshot)
		}
		if _, duplicated := newTickets[st.ID]; duplicated {
			return fmt.Errorf("%w: duplicate ticket id %d", ErrBadSnapshot, st.ID)
		}
		if blank(st.Tenant) {
			return fmt.Errorf("%w: ticket %d has no tenant", ErrBadSnapshot, st.ID)
		}
		if st.Reserved <= 0 {
			return fmt.Errorf("%w: ticket %d reservation must be positive", ErrBadSnapshot, st.ID)
		}
		name := canonical(st.Tenant)
		if _, ok := newTenants[name]; !ok {
			newTenants[name] = &tenantState{categories: make(map[string]string)}
		}
		newTickets[st.ID] = &ticketRecord{
			id:       st.ID,
			tenant:   name,
			category: st.Category,
			reserved: st.Reserved,
		}
		if st.ID > maxTicketID {
			maxTicketID = st.ID
		}
	}

	// Rebuild the reservation counters from the outstanding tickets and
	// cross check them against the serialised totals.
	for name, ts := range newTenants {
		var reserved int64
		for _, rec := range newTickets {
			if rec.tenant != name {
				continue
			}
			reserved += rec.reserved
		}
		ts.reserved = reserved
	}
	for _, st := range snap.Tenants {
		name := canonical(st.Tenant)
		if ts := newTenants[name]; ts != nil && st.Reserved != ts.reserved {
			return fmt.Errorf("%w: reserved total does not match tickets for %q", ErrBadSnapshot, name)
		}
	}

	nextID := snap.NextTicketID
	if nextID < maxTicketID {
		nextID = maxTicketID
	}

	// Whole state swap under the ledger lock.
	l.mu.Lock()
	defer l.mu.Unlock()
	l.initLocked()
	l.tenants = newTenants
	l.tickets = newTickets
	l.nextID = nextID
	return nil
}

// normalizeSamples deduplicates in first appearance order and applies the
// oldest eviction bound on restore.
func normalizeSamples(in []string, maxSamples int) []string {
	ts := &tenantState{}
	for _, host := range in {
		if blank(host) {
			continue
		}
		ts.addSample(canonical(host), maxSamples)
	}
	if ts.samples == nil {
		return []string{}
	}
	return ts.samples
}
