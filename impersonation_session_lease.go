// Package task governs time limited administrator impersonation sessions with
// user consent, read only or writable modes and immediate revocation.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Mode is the privilege level written into a consent and its lease.
type Mode string

// Supported impersonation modes.
const (
	ModeReadOnly  Mode = "read_only"
	ModeReadWrite Mode = "read_write"
)

// Consent states.
const (
	ConsentPending = "pending"
	ConsentGranted = "granted"
)

// Lease states.
const (
	LeaseActive  = "active"
	LeaseRevoked = "revoked"
)

// Envelope actions and reasons.
const (
	ActionRevoke     = "revoke"
	ReasonAuthorized = "authorized"
	ReasonReadOnly   = "read_only"
)

// Errors returned by the lease manager.
var (
	ErrBlankAdmin      = errors.New("admin id is required")
	ErrBlankUser       = errors.New("user id is required")
	ErrBlankScope      = errors.New("scope is required")
	ErrBadMode         = errors.New("mode must be read_only or read_write")
	ErrBadAction       = errors.New("action is required")
	ErrBadReason       = errors.New("a non blank reason is required")
	ErrBadActor        = errors.New("actor id is required")
	ErrUnknownConsent  = errors.New("unknown consent")
	ErrConsentMismatch = errors.New("consent belongs to another user")
	ErrConsentUsed     = errors.New("consent is already granted")
	ErrConsentExpired  = errors.New("consent has expired")
	ErrUserLeaseLimit  = errors.New("too many live leases for this user")
	ErrAdminLeaseLimit = errors.New("too many live leases held by this admin")
	ErrUnknownLease    = errors.New("unknown lease")
	ErrLeaseRevoked    = errors.New("lease is revoked")
	ErrLeaseExpired    = errors.New("lease has expired")
	ErrLeaseBusy       = errors.New("lease already has a live session")
	ErrUnknownSession  = errors.New("unknown impersonation session")
	ErrReadOnlyLease   = errors.New("lease is read only")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// Limits bounds lease lifetime, consent lifetime and the live tables. Now
// supplies the clock and may be replaced by a deterministic one in tests.
type Limits struct {
	LeaseTTL          time.Duration
	ConsentTTL        time.Duration
	MaxLeasesPerUser  int
	MaxLeasesPerAdmin int
	MaxEnvelopes      int
	Now               func() time.Time
}

// Consent is one user approval for an admin to act on their behalf.
type Consent struct {
	ID        string    `json:"id"`
	AdminID   string    `json:"admin_id"`
	UserID    string    `json:"user_id"`
	Scope     string    `json:"scope"`
	Mode      Mode      `json:"mode"`
	State     string    `json:"state"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Lease is the time limited authority derived from a granted consent.
type Lease struct {
	ID        string        `json:"id"`
	ConsentID string        `json:"consent_id"`
	AdminID   string        `json:"admin_id"`
	UserID    string        `json:"user_id"`
	Scope     string        `json:"scope"`
	Mode      Mode          `json:"mode"`
	State     string        `json:"state"`
	IssuedAt  time.Time     `json:"issued_at"`
	ExpiresAt time.Time     `json:"expires_at"`
	Remaining time.Duration `json:"remaining"`
}

// Session is one browsing context opened on a lease.
type Session struct {
	ID        string    `json:"id"`
	LeaseID   string    `json:"lease_id"`
	AdminID   string    `json:"admin_id"`
	UserID    string    `json:"user_id"`
	Mode      Mode      `json:"mode"`
	StartedAt time.Time `json:"started_at"`
}

// Envelope records one authorised or refused operation of a lease.
type Envelope struct {
	ID        string    `json:"id"`
	LeaseID   string    `json:"lease_id"`
	SessionID string    `json:"session_id"`
	Action    string    `json:"action"`
	Mode      Mode      `json:"mode"`
	Allowed   bool      `json:"allowed"`
	Reason    string    `json:"reason"`
	At        time.Time `json:"at"`
	Seq       int64     `json:"seq"`
}

// ImpersonationSessionLease owns consents, leases, sessions and envelopes.
type ImpersonationSessionLease struct {
	mu     sync.Mutex
	limits Limits

	consents map[string]Consent
	leases   map[string]*leaseRecord
	sessions map[string]Session
	// envelopes are kept oldest first for each lease.
	envelopes map[string][]Envelope

	nextConsentID  int64
	nextLeaseID    int64
	nextSessionID  int64
	nextEnvelopeID int64
}

type leaseRecord struct {
	Lease
	NextSeq int64 `json:"next_seq"`
}

const snapshotVersion = 1

type snapshotState struct {
	Version        int                   `json:"version"`
	Consents       []Consent             `json:"consents"`
	Leases         []leaseRecord         `json:"leases"`
	Sessions       []Session             `json:"sessions"`
	Envelopes      map[string][]Envelope `json:"envelopes"`
	NextConsentID  int64                 `json:"next_consent_id"`
	NextLeaseID    int64                 `json:"next_lease_id"`
	NextSessionID  int64                 `json:"next_session_id"`
	NextEnvelopeID int64                 `json:"next_envelope_id"`
}

// NewImpersonationSessionLease builds a manager with the given caps.
func NewImpersonationSessionLease(limits Limits) *ImpersonationSessionLease {
	return &ImpersonationSessionLease{
		limits:    limits,
		consents:  make(map[string]Consent),
		leases:    make(map[string]*leaseRecord),
		sessions:  make(map[string]Session),
		envelopes: make(map[string][]Envelope),
	}
}

// RequestConsent asks a user to approve one administrator action.
func (m *ImpersonationSessionLease) RequestConsent(adminID, userID, scope string, mode Mode) (Consent, error) {
	if strings.TrimSpace(adminID) == "" {
		return Consent{}, ErrBlankAdmin
	}
	if strings.TrimSpace(userID) == "" {
		return Consent{}, ErrBlankUser
	}
	if strings.TrimSpace(scope) == "" {
		return Consent{}, ErrBlankScope
	}
	if mode != ModeReadOnly && mode != ModeReadWrite {
		return Consent{}, ErrBadMode
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now()
	consent := Consent{
		ID:        m.newConsentID(),
		AdminID:   adminID,
		UserID:    userID,
		Scope:     scope,
		Mode:      mode,
		State:     ConsentPending,
		ExpiresAt: now.Add(m.limits.ConsentTTL),
	}
	m.consents[consent.ID] = consent
	return consent, nil
}

// Grant turns a pending consent into an active lease for the named user.
func (m *ImpersonationSessionLease) Grant(consentID, userID string) (Lease, error) {
	if strings.TrimSpace(consentID) == "" {
		return Lease{}, ErrUnknownConsent
	}
	if strings.TrimSpace(userID) == "" {
		return Lease{}, ErrBlankUser
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	consent, ok := m.consents[consentID]
	if !ok {
		return Lease{}, ErrUnknownConsent
	}
	if consent.UserID != userID {
		return Lease{}, ErrConsentMismatch
	}
	if consent.State == ConsentGranted {
		return Lease{}, ErrConsentUsed
	}

	now := m.now()
	if !now.Before(consent.ExpiresAt) {
		return Lease{}, ErrConsentExpired
	}

	userLeases, adminLeases := m.liveLeaseCounts(now, consent.UserID, consent.AdminID)
	if m.limits.MaxLeasesPerUser > 0 && userLeases >= m.limits.MaxLeasesPerUser {
		return Lease{}, ErrUserLeaseLimit
	}
	if m.limits.MaxLeasesPerAdmin > 0 && adminLeases >= m.limits.MaxLeasesPerAdmin {
		return Lease{}, ErrAdminLeaseLimit
	}

	lease := Lease{
		ID:        m.newLeaseID(),
		ConsentID: consent.ID,
		AdminID:   consent.AdminID,
		UserID:    consent.UserID,
		Scope:     consent.Scope,
		Mode:      consent.Mode,
		State:     LeaseActive,
		IssuedAt:  now,
		ExpiresAt: now.Add(m.limits.LeaseTTL),
		Remaining: m.limits.LeaseTTL,
	}
	if lease.Remaining < 0 {
		lease.Remaining = 0
	}
	m.leases[lease.ID] = &leaseRecord{Lease: lease, NextSeq: 1}
	consent.State = ConsentGranted
	m.consents[consent.ID] = consent
	return lease, nil
}

// Begin opens one session on an active lease.
func (m *ImpersonationSessionLease) Begin(leaseID string) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	record, ok := m.leases[leaseID]
	if !ok {
		return Session{}, ErrUnknownLease
	}
	if record.State == LeaseRevoked {
		return Session{}, ErrLeaseRevoked
	}
	now := m.now()
	if !now.Before(record.ExpiresAt) {
		return Session{}, ErrLeaseExpired
	}
	if m.activeSessionForLease(leaseID) != "" {
		return Session{}, ErrLeaseBusy
	}

	session := Session{
		ID:        m.newSessionID(),
		LeaseID:   record.ID,
		AdminID:   record.AdminID,
		UserID:    record.UserID,
		Mode:      record.Mode,
		StartedAt: now,
	}
	m.sessions[session.ID] = session
	return session, nil
}

// Authorize admits or refuses one action inside a session.
func (m *ImpersonationSessionLease) Authorize(sessionID, action string) error {
	if strings.TrimSpace(action) == "" {
		return ErrBadAction
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	session, ok := m.sessions[sessionID]
	if !ok {
		return ErrUnknownSession
	}
	record, ok := m.leases[session.LeaseID]
	if !ok {
		return ErrUnknownLease
	}
	if record.State == LeaseRevoked {
		return ErrLeaseRevoked
	}
	now := m.now()
	if !now.Before(record.ExpiresAt) {
		return ErrLeaseExpired
	}

	if record.Mode == ModeReadOnly && isWriteAction(action) {
		m.recordEnvelope(record, session.ID, action, false, ReasonReadOnly, now)
		return ErrReadOnlyLease
	}
	m.recordEnvelope(record, session.ID, action, true, ReasonAuthorized, now)
	return nil
}

// Revoke stops a lease immediately and records the actor and reason.
func (m *ImpersonationSessionLease) Revoke(leaseID, actorID, reason string) error {
	if strings.TrimSpace(actorID) == "" {
		return ErrBadActor
	}
	if strings.TrimSpace(reason) == "" {
		return ErrBadReason
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	record, ok := m.leases[leaseID]
	if !ok {
		return ErrUnknownLease
	}
	if record.State == LeaseRevoked {
		return ErrLeaseRevoked
	}

	now := m.now()
	record.State = LeaseRevoked
	m.recordEnvelope(record, m.activeSessionForLease(leaseID), ActionRevoke, false, reason, now)
	return nil
}

// Active lists the live leases of one user, ordered by lease id.
func (m *ImpersonationSessionLease) Active(userID string) []Lease {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now()
	active := make([]Lease, 0)
	for _, record := range m.leases {
		if record.UserID != userID || record.State != LeaseActive || !now.Before(record.ExpiresAt) {
			continue
		}
		active = append(active, m.leaseView(record, now))
	}
	sort.Slice(active, func(i, j int) bool {
		return active[i].ID < active[j].ID
	})
	return active
}

// LeaseState reads one lease with its remaining lifetime.
func (m *ImpersonationSessionLease) LeaseState(leaseID string) (Lease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	record, ok := m.leases[leaseID]
	if !ok {
		return Lease{}, ErrUnknownLease
	}
	return m.leaseView(record, m.now()), nil
}

// ConsentState reads one consent.
func (m *ImpersonationSessionLease) ConsentState(consentID string) (Consent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	consent, ok := m.consents[consentID]
	if !ok {
		return Consent{}, ErrUnknownConsent
	}
	return consent, nil
}

// Envelopes returns the retained envelopes of one lease, oldest first.
func (m *ImpersonationSessionLease) Envelopes(leaseID string) []Envelope {
	m.mu.Lock()
	defer m.mu.Unlock()

	envelopes := m.envelopes[leaseID]
	if len(envelopes) == 0 {
		return []Envelope{}
	}
	return append([]Envelope(nil), envelopes...)
}

// EndSession closes one live session and frees the lease.
func (m *ImpersonationSessionLease) EndSession(sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.sessions[sessionID]; !ok {
		return ErrUnknownSession
	}
	delete(m.sessions, sessionID)
	return nil
}

// Snapshot serialises consents, leases, sessions and envelopes.
func (m *ImpersonationSessionLease) Snapshot() ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	state := snapshotState{
		Version:        snapshotVersion,
		Consents:       make([]Consent, 0, len(m.consents)),
		Leases:         make([]leaseRecord, 0, len(m.leases)),
		Sessions:       make([]Session, 0, len(m.sessions)),
		Envelopes:      make(map[string][]Envelope, len(m.envelopes)),
		NextConsentID:  m.nextConsentID,
		NextLeaseID:    m.nextLeaseID,
		NextSessionID:  m.nextSessionID,
		NextEnvelopeID: m.nextEnvelopeID,
	}
	for _, consent := range m.consents {
		state.Consents = append(state.Consents, consent)
	}
	sort.Slice(state.Consents, func(i, j int) bool {
		return state.Consents[i].ID < state.Consents[j].ID
	})
	for _, record := range m.leases {
		state.Leases = append(state.Leases, *record)
	}
	sort.Slice(state.Leases, func(i, j int) bool {
		return state.Leases[i].ID < state.Leases[j].ID
	})
	for _, session := range m.sessions {
		state.Sessions = append(state.Sessions, session)
	}
	sort.Slice(state.Sessions, func(i, j int) bool {
		return state.Sessions[i].ID < state.Sessions[j].ID
	})
	for leaseID, envelopes := range m.envelopes {
		state.Envelopes[leaseID] = append([]Envelope(nil), envelopes...)
	}
	return json.Marshal(state)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (m *ImpersonationSessionLease) Restore(data []byte) error {
	var state snapshotState
	if err := json.Unmarshal(data, &state); err != nil || state.Version != snapshotVersion {
		return ErrBadSnapshot
	}

	consents := make(map[string]Consent, len(state.Consents))
	consentIDs := make([]string, 0, len(state.Consents))
	for _, consent := range state.Consents {
		if consent.ID == "" {
			return ErrBadSnapshot
		}
		if _, exists := consents[consent.ID]; exists {
			return ErrBadSnapshot
		}
		if consent.State != ConsentPending && consent.State != ConsentGranted {
			return ErrBadSnapshot
		}
		if consent.Mode != ModeReadOnly && consent.Mode != ModeReadWrite {
			return ErrBadSnapshot
		}
		consents[consent.ID] = consent
		consentIDs = append(consentIDs, consent.ID)
	}

	leases := make(map[string]*leaseRecord, len(state.Leases))
	leaseIDs := make([]string, 0, len(state.Leases))
	for _, snapshotLease := range state.Leases {
		if snapshotLease.ID == "" {
			return ErrBadSnapshot
		}
		if _, exists := leases[snapshotLease.ID]; exists {
			return ErrBadSnapshot
		}
		if snapshotLease.State != LeaseActive && snapshotLease.State != LeaseRevoked {
			return ErrBadSnapshot
		}
		if snapshotLease.Mode != ModeReadOnly && snapshotLease.Mode != ModeReadWrite {
			return ErrBadSnapshot
		}
		record := &leaseRecord{Lease: snapshotLease.Lease, NextSeq: snapshotLease.NextSeq}
		leases[record.ID] = record
		leaseIDs = append(leaseIDs, record.ID)
	}

	sessions := make(map[string]Session, len(state.Sessions))
	sessionIDs := make([]string, 0, len(state.Sessions))
	for _, session := range state.Sessions {
		if session.ID == "" {
			return ErrBadSnapshot
		}
		if _, exists := sessions[session.ID]; exists {
			return ErrBadSnapshot
		}
		if _, exists := leases[session.LeaseID]; !exists {
			return ErrBadSnapshot
		}
		sessions[session.ID] = session
		sessionIDs = append(sessionIDs, session.ID)
	}
	for leaseID, record := range leases {
		for _, session := range sessions {
			if session.LeaseID == leaseID {
				if record.activeSessions(sessions) > 1 {
					return ErrBadSnapshot
				}
				break
			}
		}
	}

	envelopes := make(map[string][]Envelope, len(state.Envelopes))
	envelopeIDs := make([]string, 0)
	for leaseID, saved := range state.Envelopes {
		if _, exists := leases[leaseID]; !exists {
			return ErrBadSnapshot
		}
		retained := append([]Envelope(nil), saved...)
		var previousSeq int64
		for _, envelope := range retained {
			if envelope.ID == "" || envelope.LeaseID != leaseID || envelope.Seq <= previousSeq {
				return ErrBadSnapshot
			}
			previousSeq = envelope.Seq
			envelopeIDs = append(envelopeIDs, envelope.ID)
		}
		envelopes[leaseID] = retained
		if record := leases[leaseID]; record.NextSeq <= previousSeq {
			record.NextSeq = previousSeq + 1
		}
	}
	for _, record := range leases {
		if record.NextSeq <= 0 {
			record.NextSeq = 1
		}
	}

	nextConsentID := maxInt64(state.NextConsentID, nextIDFromStrings(consentIDs, "consent-"))
	nextLeaseID := maxInt64(state.NextLeaseID, nextIDFromStrings(leaseIDs, "lease-"))
	nextSessionID := maxInt64(state.NextSessionID, nextIDFromStrings(sessionIDs, "session-"))
	nextEnvelopeID := maxInt64(state.NextEnvelopeID, nextIDFromStrings(envelopeIDs, "envelope-"))

	m.mu.Lock()
	defer m.mu.Unlock()
	m.consents = consents
	m.leases = leases
	m.sessions = sessions
	m.envelopes = envelopes
	m.nextConsentID = nextConsentID
	m.nextLeaseID = nextLeaseID
	m.nextSessionID = nextSessionID
	m.nextEnvelopeID = nextEnvelopeID
	return nil
}

func (m *ImpersonationSessionLease) now() time.Time {
	if m.limits.Now != nil {
		return m.limits.Now()
	}
	return time.Now()
}

func (m *ImpersonationSessionLease) newConsentID() string {
	m.nextConsentID++
	return fmt.Sprintf("consent-%016d", m.nextConsentID)
}

func (m *ImpersonationSessionLease) newLeaseID() string {
	m.nextLeaseID++
	return fmt.Sprintf("lease-%016d", m.nextLeaseID)
}

func (m *ImpersonationSessionLease) newSessionID() string {
	m.nextSessionID++
	return fmt.Sprintf("session-%016d", m.nextSessionID)
}

func (m *ImpersonationSessionLease) newEnvelopeID() string {
	m.nextEnvelopeID++
	return fmt.Sprintf("envelope-%016d", m.nextEnvelopeID)
}

func (m *ImpersonationSessionLease) liveLeaseCounts(now time.Time, userID, adminID string) (int, int) {
	var users, admins int
	for _, record := range m.leases {
		if record.State != LeaseActive || !now.Before(record.ExpiresAt) {
			continue
		}
		if record.UserID == userID {
			users++
		}
		if record.AdminID == adminID {
			admins++
		}
	}
	return users, admins
}

func (m *ImpersonationSessionLease) activeSessionForLease(leaseID string) string {
	for _, session := range m.sessions {
		if session.LeaseID == leaseID {
			return session.ID
		}
	}
	return ""
}

func (m *ImpersonationSessionLease) leaseView(record *leaseRecord, now time.Time) Lease {
	lease := record.Lease
	lease.Remaining = remaining(record.ExpiresAt, now)
	return lease
}

func (m *ImpersonationSessionLease) recordEnvelope(record *leaseRecord, sessionID, action string, allowed bool, reason string, now time.Time) {
	seq := record.NextSeq
	if seq <= 0 {
		seq = 1
	}
	record.NextSeq = seq + 1

	envelope := Envelope{
		ID:        m.newEnvelopeID(),
		LeaseID:   record.ID,
		SessionID: sessionID,
		Action:    action,
		Mode:      record.Mode,
		Allowed:   allowed,
		Reason:    reason,
		At:        now,
		Seq:       seq,
	}
	envelopes := append(m.envelopes[record.ID], envelope)
	if cap := m.limits.MaxEnvelopes; cap > 0 && len(envelopes) > cap {
		envelopes = append([]Envelope(nil), envelopes[len(envelopes)-cap:]...)
	}
	m.envelopes[record.ID] = envelopes
}

func remaining(expiresAt, now time.Time) time.Duration {
	if !now.Before(expiresAt) {
		return 0
	}
	return expiresAt.Sub(now)
}

func isWriteAction(action string) bool {
	action = strings.ToLower(strings.TrimSpace(action))
	if strings.HasPrefix(action, "write") {
		return true
	}
	verb := action
	if index := strings.IndexAny(verb, ":./ "); index >= 0 {
		verb = verb[:index]
	}
	switch verb {
	case "create", "delete", "modify", "mutate", "patch", "post", "put", "update":
		return true
	default:
		return false
	}
}

func (record *leaseRecord) activeSessions(sessions map[string]Session) int {
	count := 0
	for _, session := range sessions {
		if session.LeaseID == record.ID {
			count++
		}
	}
	return count
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func nextIDFromStrings(ids []string, prefix string) int64 {
	var next int64
	for _, id := range ids {
		if !strings.HasPrefix(id, prefix) {
			continue
		}
		value, err := strconv.ParseInt(strings.TrimPrefix(id, prefix), 10, 64)
		if err == nil && value >= next {
			next = value + 1
		}
	}
	return next
}
