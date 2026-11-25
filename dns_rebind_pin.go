// Package task pins the address set of every outbound session to the first
// resolution of its host and re-checks later resolutions for DNS rebinding.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"
)

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

	snapshotVersion = 1
)

// Errors returned by the pin table.
var (
	ErrBadConfig      = errors.New("rebind pin config is invalid")
	ErrBlankSession   = errors.New("session id is required")
	ErrBlankHost      = errors.New("host is required")
	ErrUnknownSession = errors.New("session has no pin record")
	ErrPinLimit       = errors.New("session pin limit reached")
	ErrBlockedAddr    = errors.New("address family is not allowed")
	ErrBadAddress     = errors.New("address set is empty or unparsable")
	ErrRebindDetected = errors.New("resolution no longer matches the pin")
	ErrLookupFailed   = errors.New("host lookup failed")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
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

type snapshotData struct {
	Version int                          `json:"version"`
	Pins    map[string]map[string]Pinned `json:"pins"`
}

// RebindPin owns the per session pin table.
type RebindPin struct {
	cfg PinConfig

	// Lock serialises mutations and every verification. In particular, two
	// verifications for one session can never observe or publish a partial
	// intersection.
	mu   sync.RWMutex
	pins map[string]map[string]Pinned
}

// NewRebindPin builds a pin table from the given configuration.
func NewRebindPin(cfg PinConfig) *RebindPin {
	if cfg.Now == nil {
		cfg.Now = func() int64 { return time.Now().UnixMilli() }
	}
	return &RebindPin{
		cfg:  cfg,
		pins: make(map[string]map[string]Pinned),
	}
}

// Resolve pins the first resolution of host for the session and refreshes an
// expired pin afterwards.
func (p *RebindPin) Resolve(sessionID, host string) (Pinned, error) {
	if isBlank(sessionID) {
		return Pinned{}, ErrBlankSession
	}
	if isBlank(host) {
		return Pinned{}, ErrBlankHost
	}

	now := p.nowMillis()
	p.mu.Lock()
	sessionPins := p.pins[sessionID]
	existing, exists := sessionPins[host]
	if exists && existing.ExpiresAt > now {
		p.mu.Unlock()
		return clonePinned(existing), nil
	}
	if !exists && len(sessionPins) >= p.cfg.MaxPinsPerSession {
		p.mu.Unlock()
		return Pinned{}, ErrPinLimit
	}
	p.mu.Unlock()

	if p.cfg.Lookup == nil {
		return Pinned{}, fmt.Errorf("%w: lookup function is not configured", ErrLookupFailed)
	}
	resolved, resolverTTL, err := p.cfg.Lookup(host)
	if err != nil {
		return Pinned{}, fmt.Errorf("%w: %w", ErrLookupFailed, err)
	}

	addrs, err := normalizeAddrSlice(resolved)
	if err != nil {
		return Pinned{}, err
	}
	if reason, blocked := firstBlockedClass(addrs); reason != "" {
		return Pinned{}, fmt.Errorf("%w: %s (%s)", ErrBlockedAddr, reason, strings.Join(blocked, ","))
	}

	ttl := resolverTTL
	if p.cfg.MinTTLMillis > ttl {
		ttl = p.cfg.MinTTLMillis
	}
	if ttl < 0 {
		ttl = 0
	}
	resolvedAt := p.nowMillis()

	p.mu.Lock()
	defer p.mu.Unlock()

	sessionPins = p.pins[sessionID]
	if sessionPins == nil {
		sessionPins = make(map[string]Pinned)
		p.pins[sessionID] = sessionPins
	}

	current, exists := sessionPins[host]
	if exists && current.ExpiresAt > p.nowMillis() {
		return clonePinned(current), nil
	}
	if !exists && len(sessionPins) >= p.cfg.MaxPinsPerSession {
		return Pinned{}, ErrPinLimit
	}

	gen := int64(1)
	firstAt := resolvedAt
	if exists {
		gen = current.Gen + 1
		firstAt = current.FirstAt
	}

	pin := Pinned{
		SessionID: sessionID,
		Host:      host,
		Addrs:     addrs,
		FirstAt:   firstAt,
		TTLMillis: ttl,
		ExpiresAt: resolvedAt + ttl,
		Gen:       gen,
	}
	sessionPins[host] = pin
	return clonePinned(pin), nil
}

// Verify re-checks a fresh resolution against the pinned address set.
func (p *RebindPin) Verify(sessionID string, addrs []Addr) (Verdict, error) {
	if isBlank(sessionID) {
		return Verdict{}, ErrBlankSession
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	sessionPins := p.pins[sessionID]
	if len(sessionPins) == 0 {
		return Verdict{SessionID: sessionID}, ErrUnknownSession
	}

	normalized, err := normalizeAddrSlice(addrs)
	if err != nil {
		return Verdict{SessionID: sessionID, Reason: ReasonInvalid}, err
	}
	if reason, blocked := firstBlockedClass(normalized); reason != "" {
		return Verdict{
			SessionID: sessionID,
			Allowed:   []string{},
			Rejected:  blocked,
			Reason:    reason,
		}, ErrBlockedAddr
	}

	hosts := make([]string, 0, len(sessionPins))
	for host := range sessionPins {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)

	var matchedPin Pinned
	var allowed []string
	matched := false
	for _, host := range hosts {
		pin := sessionPins[host]
		candidate := intersectOrdered(normalized, pin.Addrs)
		if len(candidate) == 0 {
			continue
		}
		if !matched || len(candidate) > len(allowed) {
			matchedPin = pin
			allowed = candidate
			matched = true
		}
	}

	if !matched {
		rebindPin := sessionPins[hosts[0]]
		return Verdict{
			SessionID: sessionID,
			Host:      rebindPin.Host,
			Gen:       rebindPin.Gen,
			Allowed:   []string{},
			Rejected:  append([]string{}, normalized...),
			Rebound:   true,
			Reason:    ReasonRebinding,
		}, ErrRebindDetected
	}

	allowedSet := make(map[string]struct{}, len(allowed))
	for _, addr := range allowed {
		allowedSet[addr] = struct{}{}
	}
	rejected := make([]string, 0, len(normalized)-len(allowed))
	for _, addr := range normalized {
		if _, ok := allowedSet[addr]; !ok {
			rejected = append(rejected, addr)
		}
	}

	return Verdict{
		SessionID: sessionID,
		Host:      matchedPin.Host,
		Gen:       matchedPin.Gen,
		Allowed:   allowed,
		Rejected:  rejected,
		Rebound:   false,
		Reason:    ReasonPinOK,
	}, nil
}

// Release drops every pin of one session.
func (p *RebindPin) Release(sessionID string) {
	p.mu.Lock()
	delete(p.pins, sessionID)
	p.mu.Unlock()
}

// PinnedFor reads one pin record.
func (p *RebindPin) PinnedFor(sessionID, host string) (Pinned, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	pin, ok := p.pins[sessionID][host]
	if !ok {
		return Pinned{}, false
	}
	return clonePinned(pin), true
}

// PinCount reports how many hosts are pinned for one session.
func (p *RebindPin) PinCount(sessionID string) int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.pins[sessionID])
}

// Generations reports the resolution generation of one pinned host.
func (p *RebindPin) Generations(sessionID, host string) int64 {
	pin, ok := p.PinnedFor(sessionID, host)
	if !ok {
		return 0
	}
	return pin.Gen
}

// Classify labels one address literal with its family reason code.
func (p *RebindPin) Classify(ip string) string {
	return classifyAddress(ip)
}

// Sessions lists the sessions that currently hold at least one pin, sorted.
func (p *RebindPin) Sessions() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	sessions := make([]string, 0, len(p.pins))
	for sessionID, sessionPins := range p.pins {
		if len(sessionPins) > 0 {
			sessions = append(sessions, sessionID)
		}
	}
	sort.Strings(sessions)
	return sessions
}

// Snapshot serialises the pin table and the generation counters.
func (p *RebindPin) Snapshot() ([]byte, error) {
	p.mu.RLock()
	snapshot := snapshotData{
		Version: snapshotVersion,
		Pins:    make(map[string]map[string]Pinned, len(p.pins)),
	}
	for sessionID, sessionPins := range p.pins {
		if len(sessionPins) == 0 {
			continue
		}
		pinsCopy := make(map[string]Pinned, len(sessionPins))
		for host, pin := range sessionPins {
			pinsCopy[host] = clonePinned(pin)
		}
		snapshot.Pins[sessionID] = pinsCopy
	}
	p.mu.RUnlock()

	data, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Restore replaces the whole pin table with a snapshot, all or nothing.
func (p *RebindPin) Restore(data []byte) error {
	restored, err := decodeSnapshot(data)
	if err != nil {
		return err
	}

	p.mu.Lock()
	p.pins = restored
	p.mu.Unlock()
	return nil
}

func (p *RebindPin) nowMillis() int64 {
	if p.cfg.Now != nil {
		return p.cfg.Now()
	}
	return time.Now().UnixMilli()
}

func isBlank(value string) bool {
	return strings.TrimSpace(value) == ""
}

func clonePinned(pin Pinned) Pinned {
	if pin.Addrs != nil {
		pin.Addrs = append([]string(nil), pin.Addrs...)
	}
	return pin
}

func normalizeAddrSlice(addrs []Addr) ([]string, error) {
	literals := make([]string, len(addrs))
	for i, addr := range addrs {
		literals[i] = addr.IP
	}
	return normalizeLiterals(literals)
}

func normalizeLiterals(literals []string) ([]string, error) {
	if len(literals) == 0 {
		return nil, ErrBadAddress
	}

	normalized := make([]string, 0, len(literals))
	seen := make(map[string]struct{}, len(literals))
	for _, literal := range literals {
		addr, err := netip.ParseAddr(literal)
		if err != nil {
			return nil, ErrBadAddress
		}
		key := addr.Unmap().String()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, key)
	}
	if len(normalized) == 0 {
		return nil, ErrBadAddress
	}
	return normalized, nil
}

func classifyAddress(literal string) string {
	addr, err := netip.ParseAddr(literal)
	if err != nil {
		return ReasonInvalid
	}
	addr = addr.Unmap()

	switch {
	case addr.IsLoopback():
		return ReasonLoopback
	case addr.IsLinkLocalUnicast():
		return ReasonLinkLocal
	case isCGNAT(addr):
		return ReasonCGNAT
	case addr.IsPrivate():
		return ReasonPrivate
	default:
		return ReasonPublic
	}
}

func isCGNAT(addr netip.Addr) bool {
	if !addr.Is4() {
		return false
	}
	octets := addr.As4()
	return octets[0] == 100 && octets[1] >= 64 && octets[1] <= 127
}

func firstBlockedClass(addrs []string) (string, []string) {
	var reason string
	blocked := make([]string, 0)
	for _, addr := range addrs {
		class := classifyAddress(addr)
		if class == ReasonPublic {
			continue
		}
		blocked = append(blocked, addr)
		if reason == "" {
			reason = class
		}
	}
	return reason, blocked
}

func intersectOrdered(requestAddrs []string, pinnedAddrs []string) []string {
	pinned := make(map[string]struct{}, len(pinnedAddrs))
	for _, addr := range pinnedAddrs {
		pinned[addr] = struct{}{}
	}

	allowed := make([]string, 0, len(requestAddrs))
	for _, addr := range requestAddrs {
		if _, ok := pinned[addr]; ok {
			allowed = append(allowed, addr)
		}
	}
	return allowed
}

func decodeSnapshot(data []byte) (map[string]map[string]Pinned, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, ErrBadSnapshot
	}

	var snapshot snapshotData
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	if snapshot.Version != snapshotVersion || snapshot.Pins == nil {
		return nil, ErrBadSnapshot
	}

	restored := make(map[string]map[string]Pinned, len(snapshot.Pins))
	for sessionID, sessionPins := range snapshot.Pins {
		if isBlank(sessionID) || sessionPins == nil {
			return nil, ErrBadSnapshot
		}

		pins := make(map[string]Pinned, len(sessionPins))
		for host, pin := range sessionPins {
			if isBlank(host) || pin.SessionID != sessionID || pin.Host != host || pin.Gen < 1 || pin.TTLMillis < 0 {
				return nil, ErrBadSnapshot
			}

			addrs, err := normalizeLiterals(pin.Addrs)
			if err != nil {
				return nil, ErrBadSnapshot
			}
			if reason, _ := firstBlockedClass(addrs); reason != "" {
				return nil, ErrBadSnapshot
			}

			pin.Addrs = addrs
			pins[host] = pin
		}
		if len(pins) == 0 {
			return nil, ErrBadSnapshot
		}
		restored[sessionID] = pins
	}
	return restored, nil
}
