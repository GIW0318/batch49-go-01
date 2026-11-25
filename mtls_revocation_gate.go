// Package task gates mTLS handshakes on a revocation ledger: serial and issuer
// revocations, revocation epochs propagated to terminating workers, session
// draining and restart safe snapshots.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

// Decision reasons.
const (
	ReasonOK        = "ok"
	ReasonRevoked   = "revoked"
	ReasonExpired   = "expired"
	ReasonStaleView = "stale_view"
)

// Errors returned by the gate.
var (
	ErrBadConfig       = errors.New("gate config is invalid")
	ErrBadRevocation   = errors.New("revocation record is invalid")
	ErrEpochFuture     = errors.New("worker epoch is ahead of the gate")
	ErrEpochRegression = errors.New("worker epoch would go backwards")
	ErrBadHandshake    = errors.New("handshake is invalid")
	ErrBadSession      = errors.New("session is invalid")
	ErrSessionLimit    = errors.New("session limit reached for this serial")
	ErrRevocationLimit = errors.New("revocation limit reached")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// Config bounds one gate. MaxLag is the largest epoch distance a worker may
// trail before handshakes fail closed, MaxRevoked caps the revoked serials and
// MaxSessionsPerSerial caps the live sessions of one certificate.
type Config struct {
	MaxLag               uint64
	MaxRevoked           int
	MaxSessionsPerSerial int
	Now                  func() int64
}

// Revocation is one revoked certificate serial.
type Revocation struct {
	Serial string
	Issuer string
	At     int64
	Reason string
}

// Handshake is one inbound client certificate to admit.
type Handshake struct {
	Serial   string
	Issuer   string
	WorkerID string
	NotAfter int64
}

// Decision is the admission result of one handshake.
type Decision struct {
	Allow  bool
	Reason string
	Epoch  uint64
}

// Session is one established mTLS session.
type Session struct {
	ID       string
	Serial   string
	WorkerID string
	At       int64
}

// MtlsRevocationGate owns the revocation ledger, the worker epoch views and the
// session table.
type MtlsRevocationGate struct {
	cfg         Config
	mu          sync.RWMutex
	epoch       uint64
	revoked     map[string]Revocation
	issuers     map[string]struct{}
	workers     map[string]uint64
	sessions    map[string]map[string]Session
	sessionByID map[string]string
}

const snapshotVersion = 1

type snapshotPayload struct {
	Version     int               `json:"version"`
	Epoch       uint64            `json:"epoch"`
	Revocations []Revocation      `json:"revocations,omitempty"`
	Issuers     []string          `json:"issuers,omitempty"`
	Workers     map[string]uint64 `json:"workers,omitempty"`
	Sessions    []Session         `json:"sessions,omitempty"`
}

type snapshotFile struct {
	Payload  []byte `json:"payload"`
	Checksum string `json:"checksum"`
}

// NewMtlsRevocationGate validates the configuration and builds an empty gate.
func NewMtlsRevocationGate(cfg Config) (*MtlsRevocationGate, error) {
	if cfg.MaxRevoked <= 0 || cfg.MaxSessionsPerSerial <= 0 {
		return nil, ErrBadConfig
	}
	if cfg.Now == nil {
		cfg.Now = func() int64 { return time.Now().UnixMilli() }
	}

	return &MtlsRevocationGate{
		cfg:         cfg,
		revoked:     make(map[string]Revocation),
		issuers:     make(map[string]struct{}),
		workers:     make(map[string]uint64),
		sessions:    make(map[string]map[string]Session),
		sessionByID: make(map[string]string),
	}, nil
}

// PublishRevocation revokes one serial and bumps the revocation epoch. A
// repeated serial is idempotent and does not bump again.
func (g *MtlsRevocationGate) PublishRevocation(rec Revocation) (uint64, error) {
	if strings.TrimSpace(rec.Serial) == "" || strings.TrimSpace(rec.Issuer) == "" {
		return g.Epoch(), ErrBadRevocation
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if _, exists := g.revoked[rec.Serial]; exists {
		return g.epoch, nil
	}
	if len(g.revoked) >= g.cfg.MaxRevoked {
		return g.epoch, ErrRevocationLimit
	}

	g.revoked[rec.Serial] = rec
	g.epoch++
	return g.epoch, nil
}

// RevokeIssuer revokes every certificate of one issuer and bumps the epoch. A
// repeated issuer is idempotent.
func (g *MtlsRevocationGate) RevokeIssuer(issuer string) (uint64, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if _, exists := g.issuers[issuer]; exists {
		return g.epoch, nil
	}

	g.issuers[issuer] = struct{}{}
	g.epoch++
	return g.epoch, nil
}

// Epoch reports the current revocation epoch.
func (g *MtlsRevocationGate) Epoch() uint64 {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.epoch
}

// Admit decides whether one client certificate may complete a handshake on the
// given worker.
func (g *MtlsRevocationGate) Admit(hs Handshake) (Decision, error) {
	if strings.TrimSpace(hs.WorkerID) == "" || hs.NotAfter <= 0 {
		return Decision{}, ErrBadHandshake
	}

	now := g.cfg.Now()

	g.mu.RLock()
	defer g.mu.RUnlock()

	decision := Decision{Epoch: g.epoch}
	if _, revoked := g.revoked[hs.Serial]; revoked {
		decision.Reason = ReasonRevoked
		return decision, nil
	}
	if _, revoked := g.issuers[hs.Issuer]; revoked {
		decision.Reason = ReasonRevoked
		return decision, nil
	}
	if hs.NotAfter <= now {
		decision.Reason = ReasonExpired
		return decision, nil
	}

	applied := g.workers[hs.WorkerID]
	if g.epoch-applied > g.cfg.MaxLag {
		decision.Reason = ReasonStaleView
		return decision, nil
	}

	decision.Allow = true
	decision.Reason = ReasonOK
	return decision, nil
}

// SyncWorker records that one worker now serves the given revocation epoch.
func (g *MtlsRevocationGate) SyncWorker(workerID string, epoch uint64) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if epoch > g.epoch {
		return ErrEpochFuture
	}
	if epoch < g.workers[workerID] {
		return ErrEpochRegression
	}

	g.workers[workerID] = epoch
	return nil
}

// AppliedEpoch reports the epoch one worker last acknowledged.
func (g *MtlsRevocationGate) AppliedEpoch(workerID string) uint64 {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.workers[workerID]
}

// RegisterSession records one established session of a certificate.
func (g *MtlsRevocationGate) RegisterSession(s Session) error {
	if strings.TrimSpace(s.ID) == "" || strings.TrimSpace(s.Serial) == "" {
		return ErrBadSession
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if existingSerial, exists := g.sessionByID[s.ID]; exists {
		if existingSerial != s.Serial {
			return ErrBadSession
		}
		return nil
	}

	sessions := g.sessions[s.Serial]
	if len(sessions) >= g.cfg.MaxSessionsPerSerial {
		return ErrSessionLimit
	}
	if sessions == nil {
		sessions = make(map[string]Session)
		g.sessions[s.Serial] = sessions
	}

	sessions[s.ID] = s
	g.sessionByID[s.ID] = s.Serial
	return nil
}

// SessionsFor lists the live session ids of one serial, sorted.
func (g *MtlsRevocationGate) SessionsFor(serial string) []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return sortedSessionIDs(g.sessions[serial])
}

// DrainSessions removes and returns the live session ids of one serial.
func (g *MtlsRevocationGate) DrainSessions(serial string) ([]string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	ids := sortedSessionIDs(g.sessions[serial])
	for _, id := range ids {
		delete(g.sessionByID, id)
	}
	delete(g.sessions, serial)
	return ids, nil
}

// Revoked reports whether one serial was revoked by name.
func (g *MtlsRevocationGate) Revoked(serial string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	_, revoked := g.revoked[serial]
	return revoked
}

// Snapshot serialises the revoked serials, the revoked issuers, the epoch, the
// worker views and the session table.
func (g *MtlsRevocationGate) Snapshot() ([]byte, error) {
	g.mu.RLock()
	payload := snapshotPayload{
		Version: snapshotVersion,
		Epoch:   g.epoch,
		Workers: make(map[string]uint64, len(g.workers)),
	}

	for _, rec := range g.revoked {
		payload.Revocations = append(payload.Revocations, rec)
	}
	sort.Slice(payload.Revocations, func(i, j int) bool {
		return payload.Revocations[i].Serial < payload.Revocations[j].Serial
	})

	for issuer := range g.issuers {
		payload.Issuers = append(payload.Issuers, issuer)
	}
	sort.Strings(payload.Issuers)

	for workerID, epoch := range g.workers {
		payload.Workers[workerID] = epoch
	}

	for _, sessions := range g.sessions {
		for _, session := range sessions {
			payload.Sessions = append(payload.Sessions, session)
		}
	}
	sort.Slice(payload.Sessions, func(i, j int) bool {
		return payload.Sessions[i].ID < payload.Sessions[j].ID
	})
	g.mu.RUnlock()

	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, ErrBadSnapshot
	}
	sum := sha256.Sum256(raw)

	data, err := json.Marshal(snapshotFile{
		Payload:  raw,
		Checksum: hex.EncodeToString(sum[:]),
	})
	if err != nil {
		return nil, ErrBadSnapshot
	}
	return data, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *MtlsRevocationGate) Restore(data []byte) error {
	payload, err := decodeSnapshot(data)
	if err != nil {
		return err
	}

	revoked := make(map[string]Revocation, len(payload.Revocations))
	for _, rec := range payload.Revocations {
		if strings.TrimSpace(rec.Serial) == "" || strings.TrimSpace(rec.Issuer) == "" {
			return ErrBadSnapshot
		}
		if _, exists := revoked[rec.Serial]; exists {
			return ErrBadSnapshot
		}
		revoked[rec.Serial] = rec
	}
	if len(revoked) > g.cfg.MaxRevoked {
		return ErrBadSnapshot
	}

	issuers := make(map[string]struct{}, len(payload.Issuers))
	for _, issuer := range payload.Issuers {
		if _, exists := issuers[issuer]; exists {
			return ErrBadSnapshot
		}
		issuers[issuer] = struct{}{}
	}

	workers := make(map[string]uint64, len(payload.Workers))
	for workerID, epoch := range payload.Workers {
		if epoch > payload.Epoch {
			return ErrBadSnapshot
		}
		workers[workerID] = epoch
	}

	sessions := make(map[string]map[string]Session)
	sessionByID := make(map[string]string, len(payload.Sessions))
	for _, session := range payload.Sessions {
		if strings.TrimSpace(session.ID) == "" || strings.TrimSpace(session.Serial) == "" {
			return ErrBadSnapshot
		}
		if existingSerial, exists := sessionByID[session.ID]; exists {
			if existingSerial != session.Serial {
				return ErrBadSnapshot
			}
			continue
		}

		group := sessions[session.Serial]
		if len(group) >= g.cfg.MaxSessionsPerSerial {
			return ErrBadSnapshot
		}
		if group == nil {
			group = make(map[string]Session)
			sessions[session.Serial] = group
		}
		group[session.ID] = session
		sessionByID[session.ID] = session.Serial
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if payload.Epoch < g.epoch {
		return ErrBadSnapshot
	}

	g.epoch = payload.Epoch
	g.revoked = revoked
	g.issuers = issuers
	g.workers = workers
	g.sessions = sessions
	g.sessionByID = sessionByID
	return nil
}

func sortedSessionIDs(sessions map[string]Session) []string {
	ids := make([]string, 0, len(sessions))
	for id := range sessions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func decodeSnapshot(data []byte) (snapshotPayload, error) {
	var file snapshotFile
	if err := json.Unmarshal(data, &file); err != nil {
		return snapshotPayload{}, ErrBadSnapshot
	}

	sum := sha256.Sum256(file.Payload)
	if file.Checksum != hex.EncodeToString(sum[:]) {
		return snapshotPayload{}, ErrBadSnapshot
	}

	var payload snapshotPayload
	if err := json.Unmarshal(file.Payload, &payload); err != nil {
		return snapshotPayload{}, ErrBadSnapshot
	}
	if payload.Version != snapshotVersion {
		return snapshotPayload{}, ErrBadSnapshot
	}

	return payload, nil
}
