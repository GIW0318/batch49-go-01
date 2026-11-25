// Package task guards one WebSocket endpoint: origin checks, one time ticket
// authentication, message injection screening, per connection sequence and rate
// limits plus restart safe snapshots.
package task

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html"
	"io"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Connection states.
const (
	StateOpen          = "open"
	StateAuthenticated = "authenticated"
	StateClosing       = "closing"
	StateClosed        = "closed"
)

// Message kinds.
const (
	KindText   = "text"
	KindBinary = "binary"
)

// Errors returned by the guard.
var (
	ErrBadConfig        = errors.New("guard config is invalid")
	ErrBadConn          = errors.New("connection id is required")
	ErrUnknownConn      = errors.New("connection is not tracked")
	ErrConnOpen         = errors.New("connection is already open")
	ErrConnLimit        = errors.New("connection limit reached")
	ErrOriginDenied     = errors.New("origin is not allowed")
	ErrBadTicket        = errors.New("ticket is invalid")
	ErrTicketMismatch   = errors.New("ticket does not belong to this connection")
	ErrTicketExpired    = errors.New("ticket has expired")
	ErrTicketReused     = errors.New("ticket was already used")
	ErrAuthTimeout      = errors.New("authentication window elapsed")
	ErrNotAuthenticated = errors.New("connection is not authenticated")
	ErrConnClosed       = errors.New("connection is closed")
	ErrRevoked          = errors.New("subject is revoked")
	ErrMessageTooLarge  = errors.New("message exceeds the size limit")
	ErrHeaderInjection  = errors.New("message carries header injection characters")
	ErrControlChar      = errors.New("message carries control characters")
	ErrBadPayload       = errors.New("payload is not a json object")
	ErrTooDeep          = errors.New("payload nesting is too deep")
	ErrLongKey          = errors.New("payload key is too long")
	ErrReservedField    = errors.New("payload uses a reserved field")
	ErrBadSequence      = errors.New("message sequence is out of order")
	ErrRateLimited      = errors.New("message rate limit reached")
	ErrBadKind          = errors.New("message kind is invalid")
	ErrBadCode          = errors.New("close code must be positive")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

const (
	snapshotMagic   = "gsb-ws-auth-inject-guard"
	snapshotVersion = 1
)

var reservedPayloadKeys = map[string]struct{}{
	"__proto__":   {},
	"constructor": {},
	"prototype":   {},
	"$where":      {},
}

// Config bounds one guard instance. AllowedOrigins entries are normalised
// before matching; MaxDepth counts the top level object as depth one.
type Config struct {
	AllowedOrigins  []string
	MaxMessageBytes int
	MaxDepth        int
	MaxKeyLen       int
	AuthTimeoutMs   int64
	TicketTTL       int64
	MaxConns        int
	RateLimit       int
	RateWindowMs    int64
	Now             func() int64
}

// Ticket is one single use authentication ticket bound to a connection and its
// nonce.
type Ticket struct {
	Value     string
	ConnID    string
	Nonce     string
	Subject   string
	IssuedAt  int64
	ExpiresAt int64
}

// Session is the authenticated identity of one connection.
type Session struct {
	ConnID          string
	Subject         string
	AuthenticatedAt int64
}

// Message is one application message after screening.
type Message struct {
	Seq  uint64
	Kind string
	Text string
}

// GuardStats is the aggregate counter record.
type GuardStats struct {
	Open          int
	Authenticated int
	Rejected      uint64
}

type connState struct {
	ConnID          string `json:"conn_id"`
	Origin          string `json:"origin"`
	Nonce           string `json:"nonce"`
	Subject         string `json:"subject,omitempty"`
	State           string `json:"state"`
	OpenedAt        int64  `json:"opened_at"`
	AuthenticatedAt int64  `json:"authenticated_at,omitempty"`
	ClosedAt        int64  `json:"closed_at,omitempty"`
	CloseCode       int    `json:"close_code,omitempty"`
	NextSeq         uint64 `json:"next_seq"`
	RateWindowStart int64  `json:"rate_window_start,omitempty"`
	RateCount       int    `json:"rate_count,omitempty"`
}

type snapshotDocument struct {
	Magic    string          `json:"magic"`
	Version  int             `json:"version"`
	Checksum string          `json:"checksum"`
	Payload  json.RawMessage `json:"payload"`
}

type snapshotPayload struct {
	Connections     map[string]*connState `json:"connections"`
	UsedTickets     map[string]struct{}   `json:"used_tickets"`
	RevokedSubjects map[string]struct{}   `json:"revoked_subjects"`
	Rejected        uint64                `json:"rejected,omitempty"`
}

// WsAuthInjectGuard owns the connection table, the used ticket set and the
// revoked subject set.
type WsAuthInjectGuard struct {
	cfg            Config
	now            func() int64
	allowedOrigins map[string]struct{}

	mu              sync.Mutex
	conns           map[string]*connState
	nonces          map[string]struct{}
	usedTickets     map[string]struct{}
	revokedSubjects map[string]struct{}
	rejected        uint64
}

// NewWsAuthInjectGuard validates the configuration and builds an empty guard.
func NewWsAuthInjectGuard(cfg Config) (*WsAuthInjectGuard, error) {
	if cfg.MaxMessageBytes <= 0 ||
		cfg.MaxDepth <= 0 ||
		cfg.MaxKeyLen <= 0 ||
		cfg.AuthTimeoutMs <= 0 ||
		cfg.TicketTTL <= 0 ||
		cfg.MaxConns <= 0 ||
		cfg.RateLimit <= 0 ||
		cfg.RateWindowMs <= 0 ||
		len(cfg.AllowedOrigins) == 0 {
		return nil, ErrBadConfig
	}

	now := cfg.Now
	if now == nil {
		now = time.Now().UnixMilli
	}

	allowedOrigins := make(map[string]struct{}, len(cfg.AllowedOrigins))
	for _, origin := range cfg.AllowedOrigins {
		allowedOrigins[normalizeOrigin(origin)] = struct{}{}
	}

	return &WsAuthInjectGuard{
		cfg:             cfg,
		now:             now,
		allowedOrigins:  allowedOrigins,
		conns:           make(map[string]*connState),
		nonces:          make(map[string]struct{}),
		usedTickets:     make(map[string]struct{}),
		revokedSubjects: make(map[string]struct{}),
	}, nil
}

// Open registers one connection after the origin check and returns the one time
// authentication nonce.
func (g *WsAuthInjectGuard) Open(connID, origin string) (string, error) {
	if strings.TrimSpace(connID) == "" {
		return "", ErrBadConn
	}

	normalizedOrigin := normalizeOrigin(origin)
	if _, allowed := g.allowedOrigins[normalizedOrigin]; !allowed {
		g.mu.Lock()
		g.rejected++
		g.mu.Unlock()
		return "", ErrOriginDenied
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if _, exists := g.conns[connID]; exists {
		g.rejected++
		return "", ErrConnOpen
	}
	if g.activeConnectionsLocked() >= g.cfg.MaxConns {
		g.rejected++
		return "", ErrConnLimit
	}

	nonce, err := g.newNonceLocked()
	if err != nil {
		return "", err
	}

	openedAt := g.now()
	g.conns[connID] = &connState{
		ConnID:   connID,
		Origin:   normalizedOrigin,
		Nonce:    nonce,
		State:    StateOpen,
		OpenedAt: openedAt,
		NextSeq:  1,
	}

	return nonce, nil
}

// Authenticate consumes one ticket and moves the connection into the
// authenticated state.
func (g *WsAuthInjectGuard) Authenticate(connID string, ticket Ticket) (Session, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if strings.TrimSpace(ticket.Value) == "" {
		g.rejected++
		return Session{}, ErrBadTicket
	}
	if _, used := g.usedTickets[ticket.Value]; used {
		g.rejected++
		return Session{}, ErrTicketReused
	}
	if strings.TrimSpace(ticket.Subject) == "" {
		g.rejected++
		return Session{}, ErrBadTicket
	}

	conn, ok := g.conns[connID]
	if !ok {
		g.rejected++
		return Session{}, ErrUnknownConn
	}
	if ticket.ConnID != connID || ticket.Nonce == "" || ticket.Nonce != conn.Nonce {
		g.rejected++
		return Session{}, ErrTicketMismatch
	}

	now := g.now()
	if ticketIsExpired(ticket, now, g.cfg.TicketTTL) {
		g.rejected++
		return Session{}, ErrTicketExpired
	}
	if now-conn.OpenedAt > g.cfg.AuthTimeoutMs {
		conn.State = StateClosed
		conn.ClosedAt = now
		g.rejected++
		return Session{}, ErrAuthTimeout
	}
	if _, revoked := g.revokedSubjects[ticket.Subject]; revoked {
		conn.Subject = ticket.Subject
		conn.State = StateClosing
		g.rejected++
		return Session{}, ErrRevoked
	}
	if conn.State == StateClosed || conn.State == StateClosing {
		g.rejected++
		return Session{}, ErrConnClosed
	}

	g.usedTickets[ticket.Value] = struct{}{}
	conn.Subject = ticket.Subject
	conn.State = StateAuthenticated
	conn.AuthenticatedAt = now
	if conn.NextSeq == 0 {
		conn.NextSeq = 1
	}

	return Session{
		ConnID:          connID,
		Subject:         ticket.Subject,
		AuthenticatedAt: now,
	}, nil
}

// Inbound screens one client payload and enforces size, injection, shape,
// sequence and rate limits.
func (g *WsAuthInjectGuard) Inbound(connID string, seq uint64, payload []byte) (Message, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	conn, ok := g.conns[connID]
	if !ok {
		g.rejected++
		return Message{}, ErrUnknownConn
	}
	if err := g.usableConnectionLocked(conn); err != nil {
		g.rejected++
		return Message{}, err
	}
	if len(payload) > g.cfg.MaxMessageBytes {
		g.rejected++
		return Message{}, ErrMessageTooLarge
	}
	if err := screenRawBytes(payload); err != nil {
		g.rejected++
		return Message{}, err
	}
	if err := validateJSONObject(payload, g.cfg.MaxDepth, g.cfg.MaxKeyLen); err != nil {
		g.rejected++
		return Message{}, err
	}
	if seq != conn.NextSeq {
		now := g.now()
		conn.State = StateClosed
		conn.ClosedAt = now
		g.rejected++
		return Message{}, ErrBadSequence
	}
	if !conn.allowRateLocked(g.now(), g.cfg.RateLimit, g.cfg.RateWindowMs) {
		g.rejected++
		return Message{}, ErrRateLimited
	}

	conn.NextSeq++
	return Message{
		Seq:  seq,
		Kind: KindText,
		Text: string(payload),
	}, nil
}

// Outbound encodes one server message with markup escaping for text frames.
func (g *WsAuthInjectGuard) Outbound(connID string, msg Message) ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	conn, ok := g.conns[connID]
	if !ok {
		g.rejected++
		return nil, ErrUnknownConn
	}
	if err := g.usableConnectionLocked(conn); err != nil {
		g.rejected++
		return nil, err
	}

	switch msg.Kind {
	case KindText:
		return []byte(escapeMarkup(msg.Text)), nil
	case KindBinary:
		encoded := base64.StdEncoding.EncodeToString([]byte(msg.Text))
		return []byte(encoded), nil
	default:
		g.rejected++
		return nil, ErrBadKind
	}
}

// Close closes one connection. Closing twice is a no-op.
func (g *WsAuthInjectGuard) Close(connID string, code int) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	conn, ok := g.conns[connID]
	if !ok {
		return ErrUnknownConn
	}
	if conn.State == StateClosed {
		return nil
	}
	if code <= 0 {
		return ErrBadCode
	}

	conn.State = StateClosed
	conn.CloseCode = code
	conn.ClosedAt = g.now()
	return nil
}

// State reports the state of one connection.
func (g *WsAuthInjectGuard) State(connID string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	conn, ok := g.conns[connID]
	if !ok {
		return "", ErrUnknownConn
	}
	return conn.State, nil
}

// RevokeSubject moves every authenticated connection of one subject into the
// closing state and returns how many were affected.
func (g *WsAuthInjectGuard) RevokeSubject(subject string) int {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.revokedSubjects[subject] = struct{}{}
	affected := 0
	for _, conn := range g.conns {
		if conn.Subject == subject && conn.State == StateAuthenticated {
			conn.State = StateClosing
			affected++
		}
	}
	return affected
}

// Stats reports the aggregate counters.
func (g *WsAuthInjectGuard) Stats() GuardStats {
	g.mu.Lock()
	defer g.mu.Unlock()

	stats := GuardStats{Rejected: g.rejected}
	for _, conn := range g.conns {
		switch conn.State {
		case StateOpen:
			stats.Open++
		case StateAuthenticated:
			stats.Authenticated++
		}
	}
	return stats
}

// UsedTicket reports whether one ticket value was already consumed.
func (g *WsAuthInjectGuard) UsedTicket(value string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	_, used := g.usedTickets[value]
	return used
}

// Snapshot serialises the connection table, the used ticket set and the
// revoked subjects.
func (g *WsAuthInjectGuard) Snapshot() ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	payload := snapshotPayload{
		Connections:     g.conns,
		UsedTickets:     g.usedTickets,
		RevokedSubjects: g.revokedSubjects,
		Rejected:        g.rejected,
	}
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, ErrBadSnapshot
	}
	checksum := sha256.Sum256(rawPayload)
	envelope := snapshotDocument{
		Magic:    snapshotMagic,
		Version:  snapshotVersion,
		Checksum: hex.EncodeToString(checksum[:]),
		Payload:  rawPayload,
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		return nil, ErrBadSnapshot
	}
	return data, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *WsAuthInjectGuard) Restore(data []byte) error {
	var envelope snapshotDocument
	if err := json.Unmarshal(data, &envelope); err != nil {
		return ErrBadSnapshot
	}
	if envelope.Magic != snapshotMagic || envelope.Version != snapshotVersion || envelope.Checksum == "" {
		return ErrBadSnapshot
	}
	checksum := sha256.Sum256(envelope.Payload)
	if envelope.Checksum != hex.EncodeToString(checksum[:]) {
		return ErrBadSnapshot
	}

	var document snapshotPayload
	if err := json.Unmarshal(envelope.Payload, &document); err != nil {
		return ErrBadSnapshot
	}
	if err := validateSnapshot(&document); err != nil {
		return ErrBadSnapshot
	}
	if document.Connections == nil {
		document.Connections = make(map[string]*connState)
	}
	if document.UsedTickets == nil {
		document.UsedTickets = make(map[string]struct{})
	}
	if document.RevokedSubjects == nil {
		document.RevokedSubjects = make(map[string]struct{})
	}

	nonces := make(map[string]struct{}, len(document.Connections))
	for connID, conn := range document.Connections {
		conn.ConnID = connID
		if conn.NextSeq == 0 {
			conn.NextSeq = 1
		}
		nonces[conn.Nonce] = struct{}{}
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	g.conns = document.Connections
	g.nonces = nonces
	g.usedTickets = document.UsedTickets
	g.revokedSubjects = document.RevokedSubjects
	g.rejected = document.Rejected
	return nil
}

func normalizeOrigin(raw string) string {
	normalized := strings.ToLower(raw)
	normalized = strings.TrimRight(normalized, "/")

	parsed, err := url.Parse(normalized)
	if err != nil || parsed.Host == "" {
		return normalized
	}

	host := parsed.Hostname()
	port := parsed.Port()
	if (parsed.Scheme == "http" && port == "80") ||
		(parsed.Scheme == "https" && port == "443") {
		port = ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}

	origin := parsed.Scheme + "://" + host
	if port != "" {
		origin += ":" + port
	}
	if parsed.Path != "" {
		origin += parsed.EscapedPath()
	}
	if parsed.RawQuery != "" {
		origin += "?" + parsed.RawQuery
	}
	if parsed.Fragment != "" {
		origin += "#" + parsed.Fragment
	}
	return origin
}

func (g *WsAuthInjectGuard) activeConnectionsLocked() int {
	active := 0
	for _, conn := range g.conns {
		if conn.State != StateClosed {
			active++
		}
	}
	return active
}

func (g *WsAuthInjectGuard) newNonceLocked() (string, error) {
	var raw [24]byte
	for attempt := 0; attempt < 3; attempt++ {
		if _, err := rand.Read(raw[:]); err != nil {
			if attempt == 2 {
				return "", err
			}
			continue
		}
		nonce := hex.EncodeToString(raw[:])
		if _, exists := g.nonces[nonce]; !exists {
			g.nonces[nonce] = struct{}{}
			return nonce, nil
		}
	}
	return "", errors.New("unable to allocate a unique connection nonce")
}

func ticketIsExpired(ticket Ticket, now int64, ttl int64) bool {
	if ticket.ExpiresAt > 0 && now >= ticket.ExpiresAt {
		return true
	}
	if ticket.IssuedAt > 0 && ttl > 0 && now-ticket.IssuedAt >= ttl {
		return true
	}
	return false
}

func (g *WsAuthInjectGuard) usableConnectionLocked(conn *connState) error {
	if conn.Subject != "" {
		if _, revoked := g.revokedSubjects[conn.Subject]; revoked {
			return ErrRevoked
		}
	}
	switch conn.State {
	case StateClosed, StateClosing:
		return ErrConnClosed
	case StateAuthenticated:
		return nil
	default:
		return ErrNotAuthenticated
	}
}

func (conn *connState) allowRateLocked(now int64, limit int, window int64) bool {
	if conn.RateWindowStart == 0 {
		conn.RateWindowStart = now
	}
	if now >= conn.RateWindowStart && now-conn.RateWindowStart >= window {
		conn.RateWindowStart = now
		conn.RateCount = 0
	}
	if conn.RateCount >= limit {
		return false
	}
	conn.RateCount++
	return true
}

func screenRawBytes(payload []byte) error {
	for _, b := range payload {
		if b == '\r' || b == '\n' {
			return ErrHeaderInjection
		}
		if (b < 0x20 && b != '\t') || b == 0x7f {
			return ErrControlChar
		}
	}
	return nil
}

func validateJSONObject(payload []byte, maxDepth, maxKeyLen int) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()

	first, err := decoder.Token()
	if err != nil {
		return ErrBadPayload
	}
	firstDelim, ok := first.(json.Delim)
	if !ok || firstDelim != '{' {
		return ErrBadPayload
	}
	if err := consumeJSONObject(decoder, 1, maxDepth, maxKeyLen); err != nil {
		return err
	}

	extra, err := decoder.Token()
	if !errors.Is(err, io.EOF) {
		if err == nil && extra != nil {
			return ErrBadPayload
		}
		return ErrBadPayload
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder, depth, maxDepth, maxKeyLen int) error {
	token, err := decoder.Token()
	if err != nil {
		return ErrBadPayload
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}

	switch delim {
	case '{':
		if depth > maxDepth {
			return ErrTooDeep
		}
		return consumeJSONObject(decoder, depth, maxDepth, maxKeyLen)
	case '[':
		if depth > maxDepth {
			return ErrTooDeep
		}
		return consumeJSONArray(decoder, depth, maxDepth, maxKeyLen)
	default:
		return ErrBadPayload
	}
}

func consumeJSONObject(decoder *json.Decoder, depth, maxDepth, maxKeyLen int) error {
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return ErrBadPayload
		}
		key, ok := keyToken.(string)
		if !ok {
			return ErrBadPayload
		}
		if len(key) > maxKeyLen {
			return ErrLongKey
		}
		if _, reserved := reservedPayloadKeys[key]; reserved {
			return ErrReservedField
		}
		if err := consumeJSONValue(decoder, depth+1, maxDepth, maxKeyLen); err != nil {
			return err
		}
	}

	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return ErrBadPayload
	}
	return nil
}

func consumeJSONArray(decoder *json.Decoder, depth, maxDepth, maxKeyLen int) error {
	for decoder.More() {
		if err := consumeJSONValue(decoder, depth+1, maxDepth, maxKeyLen); err != nil {
			return err
		}
	}

	end, err := decoder.Token()
	if err != nil || end != json.Delim(']') {
		return ErrBadPayload
	}
	return nil
}

var markupReplacer = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	`"`, "&quot;",
	"'", "&#39;",
)

func escapeMarkup(text string) string {
	return markupReplacer.Replace(html.UnescapeString(text))
}

func validateSnapshot(document *snapshotPayload) error {
	for id, conn := range document.Connections {
		if conn == nil || strings.TrimSpace(id) == "" || strings.TrimSpace(conn.Nonce) == "" {
			return ErrBadSnapshot
		}
		if conn.ConnID != "" && conn.ConnID != id {
			return ErrBadSnapshot
		}
		switch conn.State {
		case StateOpen, StateAuthenticated, StateClosing, StateClosed:
		default:
			return ErrBadSnapshot
		}
		if conn.RateCount < 0 {
			return ErrBadSnapshot
		}
	}
	for ticket := range document.UsedTickets {
		if strings.TrimSpace(ticket) == "" {
			return ErrBadSnapshot
		}
	}
	return nil
}
