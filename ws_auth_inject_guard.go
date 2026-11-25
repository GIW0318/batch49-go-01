// Package task guards one WebSocket endpoint: origin checks, one time ticket
// authentication, message injection screening, per connection sequence and rate
// limits plus restart safe snapshots.
package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

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

// WsAuthInjectGuard owns the connection table, the used ticket set and the
// revoked subject set.
type WsAuthInjectGuard struct {
	cfg Config
}

// NewWsAuthInjectGuard validates the configuration and builds an empty guard.
func NewWsAuthInjectGuard(cfg Config) (*WsAuthInjectGuard, error) {
	return nil, errNotImplemented
}

// Open registers one connection after the origin check and returns the one time
// authentication nonce.
func (g *WsAuthInjectGuard) Open(connID, origin string) (string, error) {
	return "", errNotImplemented
}

// Authenticate consumes one ticket and moves the connection into the
// authenticated state.
func (g *WsAuthInjectGuard) Authenticate(connID string, t Ticket) (Session, error) {
	return Session{}, errNotImplemented
}

// Inbound screens one client payload and enforces size, injection, shape,
// sequence and rate limits.
func (g *WsAuthInjectGuard) Inbound(connID string, seq uint64, payload []byte) (Message, error) {
	return Message{}, errNotImplemented
}

// Outbound encodes one server message with markup escaping for text frames.
func (g *WsAuthInjectGuard) Outbound(connID string, msg Message) ([]byte, error) {
	return nil, errNotImplemented
}

// Close closes one connection. Closing twice is a no-op.
func (g *WsAuthInjectGuard) Close(connID string, code int) error {
	return errNotImplemented
}

// State reports the state of one connection.
func (g *WsAuthInjectGuard) State(connID string) (string, error) {
	return "", errNotImplemented
}

// RevokeSubject moves every authenticated connection of one subject into the
// closing state and returns how many were affected.
func (g *WsAuthInjectGuard) RevokeSubject(subject string) int {
	return 0
}

// Stats reports the aggregate counters.
func (g *WsAuthInjectGuard) Stats() GuardStats {
	return GuardStats{}
}

// UsedTicket reports whether one ticket value was already consumed.
func (g *WsAuthInjectGuard) UsedTicket(value string) bool {
	return false
}

// Snapshot serialises the connection table, the used ticket set and the
// revoked subjects.
func (g *WsAuthInjectGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *WsAuthInjectGuard) Restore(data []byte) error {
	return errNotImplemented
}
