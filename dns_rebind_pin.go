// Package task pins the address set of every outbound session to the first
// resolution of its host and re-checks later resolutions for DNS rebinding.
package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Address family classification and verdict reason codes.
const (
	ReasonPinOK     = "pin_ok"
	ReasonRebinding = "rebind_detected"
	ReasonPublic    = "addr_public"
	ReasonPrivate   = "addr_private"
	ReasonLoopback  = "addr_loopback"
	ReasonLinkLocal = "addr_linklocal"
	ReasonCGNAT     = "addr_cgnat"
	ReasonInvalid   = "addr_invalid"
)

// Errors returned by the pin table.
var (
	ErrBadConfig     = errors.New("rebind pin config is invalid")
	ErrBlankSession  = errors.New("session id is required")
	ErrBlankHost     = errors.New("host is required")
	ErrUnknownSession = errors.New("session has no pin record")
	ErrPinLimit      = errors.New("session pin limit reached")
	ErrBlockedAddr   = errors.New("address family is not allowed")
	ErrBadAddress    = errors.New("address set is empty or unparsable")
	ErrRebindDetected = errors.New("resolution no longer matches the pin")
	ErrLookupFailed  = errors.New("host lookup failed")
	ErrBadSnapshot   = errors.New("snapshot payload is invalid")
)

// Addr is one resolved address of a host.
type Addr struct {
	IP string `json:"ip"`
}

// PinConfig bounds one pin table. Lookup is the resolver the table drives and
// returns the address set together with its time to live in milliseconds.
type PinConfig struct {
	MinTTLMillis      int64
	MaxPinsPerSession int
	Lookup            func(host string) ([]Addr, int64, error)
	Now               func() int64
}

// Pinned is the pin record kept per session and host.
type Pinned struct {
	SessionID string   `json:"session_id"`
	Host      string   `json:"host"`
	Addrs     []string `json:"addrs"`
	FirstAt   int64    `json:"first_at"`
	TTLMillis int64    `json:"ttl_millis"`
	ExpiresAt int64    `json:"expires_at"`
	Gen       int64    `json:"gen"`
}

// Verdict is the outcome of re-checking one resolution.
type Verdict struct {
	SessionID string   `json:"session_id"`
	Host      string   `json:"host"`
	Allowed   []string `json:"allowed"`
	Rejected  []string `json:"rejected"`
	Rebound   bool     `json:"rebound"`
	Reason    string   `json:"reason"`
	Gen       int64    `json:"gen"`
}

// RebindPin owns the per session pin table.
type RebindPin struct {
	cfg PinConfig
}

// NewRebindPin builds a pin table from the given configuration.
func NewRebindPin(cfg PinConfig) *RebindPin {
	return &RebindPin{cfg: cfg}
}

// Resolve pins the first resolution of host for the session and refreshes an
// expired pin afterwards.
func (p *RebindPin) Resolve(sessionID, host string) (Pinned, error) {
	return Pinned{}, errNotImplemented
}

// Verify re-checks a fresh resolution against the pinned address set.
func (p *RebindPin) Verify(sessionID string, addrs []Addr) (Verdict, error) {
	return Verdict{}, errNotImplemented
}

// Release drops every pin of one session.
func (p *RebindPin) Release(sessionID string) {
}

// PinnedFor reads one pin record.
func (p *RebindPin) PinnedFor(sessionID, host string) (Pinned, bool) {
	return Pinned{}, false
}

// PinCount reports how many hosts are pinned for one session.
func (p *RebindPin) PinCount(sessionID string) int {
	return 0
}

// Generations reports the resolution generation of one pinned host.
func (p *RebindPin) Generations(sessionID, host string) int64 {
	return 0
}

// Classify labels one address literal with its family reason code.
func (p *RebindPin) Classify(ip string) string {
	return ""
}

// Sessions lists the sessions that currently hold at least one pin, sorted.
func (p *RebindPin) Sessions() []string {
	return nil
}

// Snapshot serialises the pin table and the generation counters.
func (p *RebindPin) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole pin table with a snapshot, all or nothing.
func (p *RebindPin) Restore(data []byte) error {
	return errNotImplemented
}
