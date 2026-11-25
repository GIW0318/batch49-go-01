// Package task keeps TLS session resumption state in two tracks, a session id
// cache and a ticket key ring, and only resumes when the bound handshake
// parameters of the first connection still match.
package task

import (
	"container/list"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Defaults applied to zero valued Config fields.
const (
	DefaultMaxSessions = 256
	DefaultMaxPerSNI   = 32
	DefaultTicketTTL   = time.Hour
)

// snapshotVersion is the only snapshot encoding produced/accepted.
const snapshotVersion = 1

// Errors returned by the cache.
var (
	ErrBadSession  = errors.New("session binding parameters are incomplete")
	ErrStaleTicket = errors.New("ticket belongs to a retired key generation")
	ErrBadSnapshot = errors.New("snapshot payload is invalid")
)

// TicketID is the opaque handle returned by Store.
type TicketID string

// Clock supplies the current time so expiry can be tested deterministically.
type Clock interface {
	Now() time.Time
}

// systemClock falls back to wall clock time when Config.Clock is nil.
type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// Session is one stored resumption record.
type Session struct {
	ID                string    `json:"id"`
	SNI               string    `json:"sni"`
	ALPN              string    `json:"alpn"`
	CipherFamily      string    `json:"cipher_family"`
	ClientCertSubject string    `json:"client_cert_subject"`
	Secret            []byte    `json:"secret"`
	IssuedAt          time.Time `json:"issued_at"`
	ExpiresAt         time.Time `json:"expires_at"`
	KeyGeneration     int       `json:"key_generation"`
}

// ClientHello carries the parameters offered on a resumption attempt.
type ClientHello struct {
	SNI               string `json:"sni"`
	ALPN              string `json:"alpn"`
	CipherFamily      string `json:"cipher_family"`
	ClientCertSubject string `json:"client_cert_subject"`
}

// Config bounds the cache.
type Config struct {
	MaxSessions int   `json:"max_sessions"`
	MaxPerSNI   int   `json:"max_per_sni"`
	Clock       Clock `json:"-"`
}

// Stats aggregates the counters of the cache.
type Stats struct {
	Sessions      int   `json:"sessions"`
	SNIs          int   `json:"snis"`
	Hits          int64 `json:"hits"`
	Misses        int64 `json:"misses"`
	Resumes       int64 `json:"resumes"`
	Degraded      int64 `json:"degraded"`
	Expired       int64 `json:"expired"`
	Evicted       int64 `json:"evicted"`
	Rotations     int64 `json:"rotations"`
	KeyGeneration int   `json:"key_generation"`
}

// record is one session and its membership in the global insertion order and
// the per-SNI least-recently-used order. List elements point back at the
// record so re-ticketing does not invalidate the orders.
type record struct {
	session Session
	ticket  TicketID
	global  *list.Element
	perSNI  *list.Element
}

// ResumeCache owns the two indexes, the key ring and the eviction order.
//
// Invariants while mu is held:
//   - every ticket in tickets has exactly one matching bySID entry;
//   - globalOrder contains every record exactly once, front = oldest insert;
//   - sniOrder[sni] contains exactly the records of that SNI, front = LRU;
//   - the key ring is generationsFor(currentGen) (at most current and previous).
type ResumeCache struct {
	cfg         Config
	clock       Clock
	maxSessions int
	maxPerSNI   int

	mu         sync.Mutex
	currentGen int
	nextNonce  uint64

	tickets     map[TicketID]*record
	bySID       map[string]TicketID
	globalOrder *list.List
	sniOrder    map[string]*list.List

	hits      int64
	misses    int64
	resumes   int64
	degraded  int64
	expired   int64
	evicted   int64
	rotations int64
}

// NewResumeCache builds a cache with the given configuration.
func NewResumeCache(cfg Config) *ResumeCache {
	clock := cfg.Clock
	if clock == nil {
		clock = systemClock{}
	}
	maxSessions := cfg.MaxSessions
	if maxSessions <= 0 {
		maxSessions = DefaultMaxSessions
	}
	maxPerSNI := cfg.MaxPerSNI
	if maxPerSNI <= 0 {
		maxPerSNI = DefaultMaxPerSNI
	}
	return &ResumeCache{
		cfg:         cfg,
		clock:       clock,
		maxSessions: maxSessions,
		maxPerSNI:   maxPerSNI,
		currentGen:  1,
		tickets:     make(map[TicketID]*record),
		bySID:       make(map[string]TicketID),
		globalOrder: list.New(),
		sniOrder:    make(map[string]*list.List),
	}
}

// Store writes one session into both tracks and mints a ticket for it.
func (c *ResumeCache) Store(sess Session) (TicketID, error) {
	// Validate before touching any state so a rejected session cannot leave
	// a half-written record in either track.
	if isBlank(sess.SNI) || isBlank(sess.ALPN) || isBlank(sess.CipherFamily) {
		return "", ErrBadSession
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.clock.Now()
	sess = cloneSession(sess)
	if sess.IssuedAt.IsZero() {
		sess.IssuedAt = now
	}
	if sess.ExpiresAt.IsZero() {
		sess.ExpiresAt = now.Add(DefaultTicketTTL)
	}
	sess.KeyGeneration = c.currentGen

	// Re-storing a known session id replaces its record, keeping the two
	// indexes one-to-one. A replacement is not an eviction.
	if oldTID, ok := c.bySID[sess.ID]; ok {
		if old, exists := c.tickets[oldTID]; exists {
			c.removeRecordLocked(old)
		} else {
			delete(c.bySID, sess.ID)
		}
	}

	// Per-SNI quota: evict the least recently used entry of this SNI only.
	if bucket := c.sniOrder[sess.SNI]; bucket != nil {
		for bucket.Len() >= c.maxPerSNI {
			oldest := bucket.Front().Value.(*record)
			c.removeRecordLocked(oldest)
			c.evicted++
		}
	}

	// Global capacity: evict the oldest insertion across every SNI.
	for c.globalOrder.Len() >= c.maxSessions {
		oldest := c.globalOrder.Front().Value.(*record)
		c.removeRecordLocked(oldest)
		c.evicted++
	}

	tid := c.mintTicketLocked()
	rec := &record{session: sess, ticket: tid}
	rec.global = c.globalOrder.PushBack(rec)

	bucket := c.sniOrder[sess.SNI]
	if bucket == nil {
		bucket = list.New()
		c.sniOrder[sess.SNI] = bucket
	}
	rec.perSNI = bucket.PushBack(rec)

	c.tickets[tid] = rec
	c.bySID[sess.ID] = tid
	return tid, nil
}

// Lookup finds a session by ticket id.
func (c *ResumeCache) Lookup(id TicketID) (Session, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	rec, ok := c.tickets[id]
	if !ok {
		c.misses++
		return Session{}, false
	}
	return c.readValidLocked(rec)
}

// LookupBySessionID finds a session through the session id track.
func (c *ResumeCache) LookupBySessionID(id string) (Session, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	tid, ok := c.bySID[id]
	if !ok {
		c.misses++
		return Session{}, false
	}
	rec, ok := c.tickets[tid]
	if !ok {
		// Repair a dangling session-id index rather than serving a ghost.
		delete(c.bySID, id)
		c.misses++
		return Session{}, false
	}
	return c.readValidLocked(rec)
}

// Resume checks the offered handshake parameters against a stored ticket.
func (c *ResumeCache) Resume(id TicketID, hello ClientHello) (Session, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	rec, ok := c.tickets[id]
	if !ok {
		c.misses++
		return Session{}, false
	}
	if c.retiredGenerationLocked(rec.session.KeyGeneration) {
		c.removeRecordLocked(rec)
		c.misses++
		return Session{}, false
	}
	if c.isExpiredLocked(rec.session.ExpiresAt) {
		c.removeRecordLocked(rec)
		c.expired++
		return Session{}, false
	}

	bound := rec.session
	if bound.SNI != hello.SNI ||
		bound.ALPN != hello.ALPN ||
		bound.CipherFamily != hello.CipherFamily ||
		bound.ClientCertSubject != hello.ClientCertSubject {
		// Decryptable ticket but changed bindings: silently fall back to a
		// full handshake. Count only the degradation.
		c.degraded++
		return Session{}, false
	}

	c.resumes++
	c.touchLocked(rec)
	return cloneSession(bound), true
}

// Renew mints a fresh ticket for a session that still uses the current key
// generation.
func (c *ResumeCache) Renew(id TicketID) (TicketID, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	rec, ok := c.tickets[id]
	if !ok {
		return "", fmt.Errorf("%w: unknown ticket", ErrStaleTicket)
	}
	if c.retiredGenerationLocked(rec.session.KeyGeneration) {
		gen := rec.session.KeyGeneration
		c.removeRecordLocked(rec)
		return "", fmt.Errorf("%w: generation %d retired", ErrStaleTicket, gen)
	}
	if rec.session.KeyGeneration != c.currentGen {
		return "", fmt.Errorf("%w: ticket generation %d is previous, current %d",
			ErrStaleTicket, rec.session.KeyGeneration, c.currentGen)
	}
	if c.isExpiredLocked(rec.session.ExpiresAt) {
		c.removeRecordLocked(rec)
		return "", fmt.Errorf("%w: ticket expired", ErrStaleTicket)
	}

	newID := c.mintTicketLocked()
	c.rekeyTicketLocked(rec, newID)
	return newID, nil
}

// RotateKey retires the oldest key generation.
func (c *ResumeCache) RotateKey() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.currentGen++
	c.rotations++

	// The ring keeps {current-1, current}; anything older is invalid and is
	// purged from both tracks immediately.
	oldestKept := c.currentGen - 1
	for elem := c.globalOrder.Front(); elem != nil; {
		next := elem.Next()
		rec := elem.Value.(*record)
		if rec.session.KeyGeneration < oldestKept {
			c.removeRecordLocked(rec)
		}
		elem = next
	}
	return nil
}

// InvalidateSNI drops every session of one server name and returns the count.
func (c *ResumeCache) InvalidateSNI(sni string) int {
	c.mu.Lock()
	defer c.mu.Unlock()

	bucket := c.sniOrder[sni]
	if bucket == nil || bucket.Len() == 0 {
		return 0
	}
	victims := make([]*record, 0, bucket.Len())
	for elem := bucket.Front(); elem != nil; elem = elem.Next() {
		victims = append(victims, elem.Value.(*record))
	}
	for _, rec := range victims {
		c.removeRecordLocked(rec)
	}
	return len(victims)
}

// PurgeExpired drops every session whose expiry already passed.
func (c *ResumeCache) PurgeExpired() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.clock.Now()
	removed := 0
	for elem := c.globalOrder.Front(); elem != nil; {
		next := elem.Next()
		rec := elem.Value.(*record)
		if !now.Before(rec.session.ExpiresAt) {
			c.removeRecordLocked(rec)
			removed++
		}
		elem = next
	}
	c.expired += int64(removed)
	return removed
}

// SNIs lists the server names that still hold sessions, in ascending order.
func (c *ResumeCache) SNIs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	out := make([]string, 0, len(c.sniOrder))
	for sni := range c.sniOrder {
		out = append(out, sni)
	}
	sort.Strings(out)
	return out
}

// KeyGenerations lists the key generations the ring still keeps.
func (c *ResumeCache) KeyGenerations() []int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int(nil), generationsFor(c.currentGen)...)
}

// Stats reads the aggregate counters.
func (c *ResumeCache) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()

	return Stats{
		Sessions:      c.globalOrder.Len(),
		SNIs:          len(c.sniOrder),
		Hits:          c.hits,
		Misses:        c.misses,
		Resumes:       c.resumes,
		Degraded:      c.degraded,
		Expired:       c.expired,
		Evicted:       c.evicted,
		Rotations:     c.rotations,
		KeyGeneration: c.currentGen,
	}
}

// Snapshot serialises both indexes, the key ring and the counters.
func (c *ResumeCache) Snapshot() ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	data := snapshotData{
		Version:      snapshotVersion,
		CurrentGen:   c.currentGen,
		Generations:  generationsFor(c.currentGen),
		NextNonce:    c.nextNonce,
		Tickets:      make(map[string]Session, len(c.tickets)),
		SessionIndex: make(map[string]string, len(c.bySID)),
		GlobalOrder:  make([]string, 0, c.globalOrder.Len()),
		SNIOrder:     make(map[string][]string, len(c.sniOrder)),
		Hits:         c.hits,
		Misses:       c.misses,
		Resumes:      c.resumes,
		Degraded:     c.degraded,
		Expired:      c.expired,
		Evicted:      c.evicted,
		Rotations:    c.rotations,
	}

	for tid, rec := range c.tickets {
		data.Tickets[string(tid)] = cloneSession(rec.session)
	}
	for sid, tid := range c.bySID {
		data.SessionIndex[sid] = string(tid)
	}
	for elem := c.globalOrder.Front(); elem != nil; elem = elem.Next() {
		rec := elem.Value.(*record)
		data.GlobalOrder = append(data.GlobalOrder, string(rec.ticket))
	}
	for sni, bucket := range c.sniOrder {
		ids := make([]string, 0, bucket.Len())
		for elem := bucket.Front(); elem != nil; elem = elem.Next() {
			rec := elem.Value.(*record)
			ids = append(ids, string(rec.ticket))
		}
		data.SNIOrder[sni] = ids
	}

	blob, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("encode snapshot: %w", err)
	}
	return blob, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (c *ResumeCache) Restore(payload []byte) error {
	state, err := restoreStateFromSnapshot(payload)
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.currentGen = state.currentGen
	c.nextNonce = state.nextNonce
	c.tickets = state.tickets
	c.bySID = state.bySID
	c.globalOrder = state.globalOrder
	c.sniOrder = state.sniOrder
	c.hits = state.hits
	c.misses = state.misses
	c.resumes = state.resumes
	c.degraded = state.degraded
	c.expired = state.expired
	c.evicted = state.evicted
	c.rotations = state.rotations
	return nil
}

// ---------------------------------------------------------------------------
// Internal helpers (callers hold c.mu unless noted).
// ---------------------------------------------------------------------------

// readValidLocked runs the shared generation/expiry/hit path for the two
// lookup tracks.
func (c *ResumeCache) readValidLocked(rec *record) (Session, bool) {
	if c.retiredGenerationLocked(rec.session.KeyGeneration) {
		c.removeRecordLocked(rec)
		c.misses++
		return Session{}, false
	}
	if c.isExpiredLocked(rec.session.ExpiresAt) {
		c.removeRecordLocked(rec)
		c.expired++
		return Session{}, false
	}
	c.hits++
	c.touchLocked(rec)
	return cloneSession(rec.session), true
}

// removeRecordLocked drops a record from the ticket track, the session-id
// track and both eviction orders.
func (c *ResumeCache) removeRecordLocked(rec *record) {
	c.globalOrder.Remove(rec.global)
	if rec.perSNI != nil {
		if bucket := c.sniOrder[rec.session.SNI]; bucket != nil {
			bucket.Remove(rec.perSNI)
			if bucket.Len() == 0 {
				delete(c.sniOrder, rec.session.SNI)
			}
		}
	}
	delete(c.tickets, rec.ticket)
	if tid, ok := c.bySID[rec.session.ID]; ok && tid == rec.ticket {
		delete(c.bySID, rec.session.ID)
	}
}

// rekeyTicketLocked moves a record to a freshly minted ticket id without
// changing its position in either order.
func (c *ResumeCache) rekeyTicketLocked(rec *record, newID TicketID) {
	delete(c.tickets, rec.ticket)
	rec.ticket = newID
	c.tickets[newID] = rec
	c.bySID[rec.session.ID] = newID
}

// touchLocked records recent use for per-SNI LRU only. The global order stays
// insertion ordered on purpose.
func (c *ResumeCache) touchLocked(rec *record) {
	if rec.perSNI == nil {
		return
	}
	if bucket := c.sniOrder[rec.session.SNI]; bucket != nil {
		bucket.MoveToBack(rec.perSNI)
	}
}

func (c *ResumeCache) isExpiredLocked(expiresAt time.Time) bool {
	return !c.clock.Now().Before(expiresAt)
}

// retiredGenerationLocked reports whether gen already fell out of the
// two-generation ring.
func (c *ResumeCache) retiredGenerationLocked(gen int) bool {
	return gen < oldestKeptGeneration(c.currentGen)
}

func oldestKeptGeneration(current int) int {
	if current <= 1 {
		return 1
	}
	return current - 1
}

// generationsFor returns the retained ring in ascending order.
func generationsFor(current int) []int {
	if current <= 1 {
		return []int{1}
	}
	return []int{current - 1, current}
}

// mintTicketLocked returns a unique opaque ticket id. The monotonic nonce
// guarantees uniqueness even if randomness were unavailable.
func (c *ResumeCache) mintTicketLocked() TicketID {
	for {
		c.nextNonce++
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err != nil {
			id := TicketID(fmt.Sprintf("ticket-%d-%d", time.Now().UnixNano(), c.nextNonce))
			if _, exists := c.tickets[id]; !exists {
				return id
			}
			continue
		}
		id := TicketID("ticket-" + hex.EncodeToString(raw[:]) +
			"-" + fmt.Sprintf("%d", c.nextNonce))
		if _, exists := c.tickets[id]; !exists {
			return id
		}
	}
}

func isBlank(value string) bool {
	return strings.TrimSpace(value) == ""
}

func cloneSession(sess Session) Session {
	if sess.Secret != nil {
		secret := make([]byte, len(sess.Secret))
		copy(secret, sess.Secret)
		sess.Secret = secret
	}
	return sess
}

// ---------------------------------------------------------------------------
// Snapshot encoding and all-or-nothing restore.
// ---------------------------------------------------------------------------

// snapshotData is the JSON envelope. It persists both indexes explicitly,
// the key ring, both orders (global insertion and per-SNI LRU) and every
// counter.
type snapshotData struct {
	Version      int                 `json:"version"`
	CurrentGen   int                 `json:"current_gen"`
	Generations  []int               `json:"generations"`
	NextNonce    uint64              `json:"next_nonce"`
	Tickets      map[string]Session  `json:"tickets"`
	SessionIndex map[string]string   `json:"session_index"`
	GlobalOrder  []string            `json:"global_order"`
	SNIOrder     map[string][]string `json:"sni_order"`

	Hits      int64 `json:"hits"`
	Misses    int64 `json:"misses"`
	Resumes   int64 `json:"resumes"`
	Degraded  int64 `json:"degraded"`
	Expired   int64 `json:"expired"`
	Evicted   int64 `json:"evicted"`
	Rotations int64 `json:"rotations"`
}

// restoreStateFromSnapshot parses and fully validates the payload, building a
// detached runtime state. It never touches the live cache, so any failure
// leaves current traffic state untouched.
func restoreStateFromSnapshot(payload []byte) (*ResumeCache, error) {
	var data snapshotData
	if err := json.Unmarshal(payload, &data); err != nil {
		return nil, fmt.Errorf("%w: decode: %v", ErrBadSnapshot, err)
	}
	if err := validateEnvelope(&data); err != nil {
		return nil, err
	}

	state := &ResumeCache{
		currentGen:  data.CurrentGen,
		nextNonce:   data.NextNonce,
		tickets:     make(map[TicketID]*record, len(data.Tickets)),
		bySID:       make(map[string]TicketID, len(data.SessionIndex)),
		globalOrder: list.New(),
		sniOrder:    make(map[string]*list.List),
		hits:        data.Hits,
		misses:      data.Misses,
		resumes:     data.Resumes,
		degraded:    data.Degraded,
		expired:     data.Expired,
		evicted:     data.Evicted,
		rotations:   data.Rotations,
	}

	ring := generationsFor(data.CurrentGen)

	for rawTID, sess := range data.Tickets {
		if rawTID == "" {
			return nil, badSnapshot("empty ticket id")
		}
		if isBlank(sess.SNI) || isBlank(sess.ALPN) || isBlank(sess.CipherFamily) {
			return nil, badSnapshot("session %q has incomplete bindings", rawTID)
		}
		if !containsInt(ring, sess.KeyGeneration) {
			return nil, badSnapshot("session %q key generation %d outside ring %v",
				rawTID, sess.KeyGeneration, ring)
		}
		tid := TicketID(rawTID)
		state.tickets[tid] = &record{
			session: cloneSession(sess),
			ticket:  tid,
		}
	}

	// Global insertion order must cover every ticket exactly once.
	if len(data.GlobalOrder) != len(state.tickets) {
		return nil, badSnapshot("global order length %d != tickets %d",
			len(data.GlobalOrder), len(state.tickets))
	}
	seenGlobal := make(map[TicketID]bool, len(state.tickets))
	for _, rawTID := range data.GlobalOrder {
		tid := TicketID(rawTID)
		rec, ok := state.tickets[tid]
		if !ok {
			return nil, badSnapshot("global order references missing ticket %q", rawTID)
		}
		if seenGlobal[tid] {
			return nil, badSnapshot("global order duplicates ticket %q", rawTID)
		}
		seenGlobal[tid] = true
		rec.global = state.globalOrder.PushBack(rec)
	}

	// Session-id index must be an exact bijection with the ticket track.
	if len(data.SessionIndex) != len(state.tickets) {
		return nil, badSnapshot("session index length %d != tickets %d",
			len(data.SessionIndex), len(state.tickets))
	}
	for sid, rawTID := range data.SessionIndex {
		tid := TicketID(rawTID)
		rec, ok := state.tickets[tid]
		if !ok {
			return nil, badSnapshot("session index %q references missing ticket", sid)
		}
		if rec.session.ID != sid {
			return nil, badSnapshot("session index key %q does not match record id %q",
				sid, rec.session.ID)
		}
		state.bySID[sid] = tid
	}
	for tid, rec := range state.tickets {
		mapped, ok := state.bySID[rec.session.ID]
		if !ok || mapped != tid {
			return nil, badSnapshot("ticket %q missing consistent session-id index", tid)
		}
	}

	// Per-SNI LRU orders must partition the ticket set by SNI exactly.
	expected := make(map[string]map[TicketID]bool)
	for tid, rec := range state.tickets {
		set := expected[rec.session.SNI]
		if set == nil {
			set = make(map[TicketID]bool)
			expected[rec.session.SNI] = set
		}
		set[tid] = true
	}
	if len(data.SNIOrder) != len(expected) {
		return nil, badSnapshot("sni order groups %d != sni count %d",
			len(data.SNIOrder), len(expected))
	}
	for sni, ids := range data.SNIOrder {
		want := expected[sni]
		if want == nil {
			return nil, badSnapshot("sni order has unknown server name %q", sni)
		}
		bucket := list.New()
		seen := make(map[TicketID]bool, len(ids))
		for _, rawTID := range ids {
			tid := TicketID(rawTID)
			rec, ok := state.tickets[tid]
			if !ok || rec.session.SNI != sni || !want[tid] {
				return nil, badSnapshot("sni order %q has foreign ticket %q", sni, rawTID)
			}
			if seen[tid] {
				return nil, badSnapshot("sni order %q duplicates ticket %q", sni, rawTID)
			}
			seen[tid] = true
			rec.perSNI = bucket.PushBack(rec)
		}
		if len(seen) != len(want) {
			return nil, badSnapshot("sni order %q is incomplete", sni)
		}
		state.sniOrder[sni] = bucket
	}

	return state, nil
}

func validateEnvelope(data *snapshotData) error {
	if data.Version != snapshotVersion {
		return badSnapshot("unsupported version %d", data.Version)
	}
	if data.CurrentGen < 1 {
		return badSnapshot("current generation %d invalid", data.CurrentGen)
	}
	if !equalInts(data.Generations, generationsFor(data.CurrentGen)) {
		return badSnapshot("generations %v inconsistent with current %d",
			data.Generations, data.CurrentGen)
	}
	if data.Hits < 0 || data.Misses < 0 || data.Resumes < 0 ||
		data.Degraded < 0 || data.Expired < 0 || data.Evicted < 0 ||
		data.Rotations < 0 {
		return badSnapshot("counters must not be negative")
	}
	return nil
}

func badSnapshot(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrBadSnapshot, fmt.Sprintf(format, args...))
}

func containsInt(values []int, target int) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
