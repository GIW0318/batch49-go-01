// Package task validates a TLS certificate chain against pinned trust
// anchors and keeps a bounded cache of revocation status lookups.
package task

import (
	"container/list"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const snapshotFormatVersion = 1

// Revocation status values.
const (
	StatusGood    = "good"
	StatusRevoked = "revoked"
	StatusUnknown = "unknown"
)

// Errors returned by the verifier.
var (
	ErrBlankField       = errors.New("required field is empty")
	ErrEmptyChain       = errors.New("certificate chain is empty")
	ErrChainTooLong     = errors.New("certificate chain exceeds the configured length")
	ErrHostMismatch     = errors.New("leaf certificate does not cover the requested host")
	ErrCertNotYetValid  = errors.New("certificate is not valid yet")
	ErrCertExpired      = errors.New("certificate has expired")
	ErrLeafIsCA         = errors.New("leaf certificate must not be a CA")
	ErrChainBroken      = errors.New("issuer and subject do not chain")
	ErrNotCA            = errors.New("certificate acting as an issuer is not a CA")
	ErrPathTooLong      = errors.New("path length constraint violated")
	ErrNameConstraint   = errors.New("name constraint violated")
	ErrKeyUsage         = errors.New("leaf certificate lacks server authentication usage")
	ErrUntrustedChain   = errors.New("chain does not terminate at a trust anchor")
	ErrDuplicateAnchor  = errors.New("trust anchor subject already pinned")
	ErrRevoked          = errors.New("certificate is revoked")
	ErrStatusUnknown    = errors.New("revocation status is unknown")
	ErrStatusStale      = errors.New("cached revocation status is too old")
	ErrRevokedPermanent = errors.New("revoked status cannot be overwritten")
	ErrCacheFull        = errors.New("revocation cache holds only revocations")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

// ChainConfig bounds the chain length and the revocation cache.
type ChainConfig struct {
	MaxChainLen     int
	MaxCacheEntries int
	StatusTTL       time.Duration
	MaxStaleness    time.Duration
}

// CertInfo is the parsed view of one X.509 certificate.
type CertInfo struct {
	Subject            string    `json:"subject"`
	Issuer             string    `json:"issuer"`
	Serial             string    `json:"serial"`
	IsCA               bool      `json:"is_ca"`
	PathLen            int       `json:"path_len"`
	PathLenSet         bool      `json:"path_len_set"`
	DNSNames           []string  `json:"dns_names"`
	NameConstraints    []string  `json:"name_constraints"`
	NotBefore          time.Time `json:"not_before"`
	NotAfter           time.Time `json:"not_after"`
	KeyUsageServerAuth bool      `json:"key_usage_server_auth"`
}

// StatusEntry is one cached revocation lookup.
type StatusEntry struct {
	Issuer     string    `json:"issuer"`
	Serial     string    `json:"serial"`
	Status     string    `json:"status"`
	Source     string    `json:"source"`
	ObservedAt time.Time `json:"observed_at"`
	LastUsed   int64     `json:"last_used"`
}

// Verdict explains one successful chain validation.
type Verdict struct {
	Trusted    bool     `json:"trusted"`
	LeafSerial string   `json:"leaf_serial"`
	ChainLen   int      `json:"chain_len"`
	Revocation string   `json:"revocation"`
	Reasons    []string `json:"reasons"`
}

// CacheStats reports the revocation cache counters.
type CacheStats struct {
	Entries   int `json:"entries"`
	Hits      int `json:"hits"`
	Misses    int `json:"misses"`
	Evictions int `json:"evictions"`
	Stale     int `json:"stale"`
}

type statusKey struct {
	Issuer string `json:"issuer"`
	Serial string `json:"serial"`
}

type snapshotData struct {
	Version int           `json:"version"`
	Seq     int64         `json:"seq"`
	Anchors []CertInfo    `json:"anchors"`
	Entries []StatusEntry `json:"entries"`
	LRU     []statusKey   `json:"lru"`
	Stats   CacheStats    `json:"stats"`
}

// ChainRevocationCache owns the pinned anchors and the status cache.
type ChainRevocationCache struct {
	mu          sync.Mutex
	cfg         ChainConfig
	now         time.Time
	seq         int64
	anchors     []CertInfo
	anchorIndex map[string]CertInfo
	entries     map[statusKey]*StatusEntry
	lru         *list.List
	nodes       map[statusKey]*list.Element
	stats       CacheStats
}

// NewChainRevocationCache builds an empty verifier.
func NewChainRevocationCache(cfg ChainConfig) *ChainRevocationCache {
	return &ChainRevocationCache{
		cfg:         cfg,
		now:         time.Now(),
		anchorIndex: make(map[string]CertInfo),
		entries:     make(map[statusKey]*StatusEntry),
		lru:         list.New(),
		nodes:       make(map[statusKey]*list.Element),
	}
}

// SetNow fixes the clock used for validity and status freshness.
func (c *ChainRevocationCache) SetNow(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

// AddTrustAnchor pins one CA certificate as a trust anchor.
func (c *ChainRevocationCache) AddTrustAnchor(cert CertInfo) error {
	if strings.TrimSpace(cert.Subject) == "" {
		return ErrBlankField
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.anchorIndex[cert.Subject]; exists {
		return ErrDuplicateAnchor
	}
	if !cert.IsCA {
		return ErrNotCA
	}
	if err := checkCertificateValidity(cert, c.now); err != nil {
		return err
	}

	stored := cloneCertInfo(cert)
	c.anchors = append(c.anchors, stored)
	c.anchorIndex[cert.Subject] = stored
	return nil
}

// Verify validates a leaf first chain for the given host.
func (c *ChainRevocationCache) Verify(chain []CertInfo, host string) (Verdict, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(chain) == 0 {
		return Verdict{}, ErrEmptyChain
	}
	if len(chain) > c.cfg.MaxChainLen {
		return Verdict{}, ErrChainTooLong
	}

	leaf := chain[0]
	if !hostMatchesSAN(host, leaf.DNSNames) {
		return Verdict{}, ErrHostMismatch
	}

	for _, cert := range chain {
		if err := checkCertificateValidity(cert, c.now); err != nil {
			return Verdict{}, err
		}
	}

	if leaf.IsCA {
		return Verdict{}, ErrLeafIsCA
	}
	if !leaf.KeyUsageServerAuth {
		return Verdict{}, ErrKeyUsage
	}

	for i := 0; i+1 < len(chain); i++ {
		if chain[i].Issuer != chain[i+1].Subject {
			return Verdict{}, ErrChainBroken
		}
	}
	for i := 1; i < len(chain); i++ {
		if !chain[i].IsCA {
			return Verdict{}, ErrNotCA
		}
	}

	for i := range chain {
		if err := checkSubordinatePathLen(chain, i); err != nil {
			return Verdict{}, err
		}
	}

	anchor, trusted := c.anchorIndex[chain[len(chain)-1].Issuer]
	if !trusted {
		return Verdict{}, ErrUntrustedChain
	}
	if !anchor.IsCA {
		return Verdict{}, ErrNotCA
	}
	if err := checkCertificateValidity(anchor, c.now); err != nil {
		return Verdict{}, err
	}
	if anchor.PathLenSet && countCACerts(chain) > anchor.PathLen {
		return Verdict{}, ErrPathTooLong
	}
	if !leafNamesConstrained(leaf.DNSNames, anchor.NameConstraints) {
		return Verdict{}, ErrNameConstraint
	}

	revocation, err := c.verifyLeafRevocation(leaf)
	if err != nil {
		return Verdict{}, err
	}

	return Verdict{
		Trusted:    true,
		LeafSerial: leaf.Serial,
		ChainLen:   len(chain) + 1,
		Revocation: revocation,
	}, nil
}

// RecordStatus stores one revocation lookup result.
func (c *ChainRevocationCache) RecordStatus(issuer, serial, status, source string) error {
	if strings.TrimSpace(issuer) == "" || strings.TrimSpace(serial) == "" {
		return ErrBlankField
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	key := statusKey{Issuer: issuer, Serial: serial}
	if existing, ok := c.entries[key]; ok {
		if existing.Status == StatusRevoked {
			if status == StatusRevoked {
				return nil
			}
			return ErrRevokedPermanent
		}

		existing.Status = status
		existing.Source = source
		existing.ObservedAt = c.now
		c.touchLocked(key)
		return nil
	}

	for len(c.entries) >= c.cfg.MaxCacheEntries {
		if !c.evictLRULocked() {
			return ErrCacheFull
		}
	}

	c.seq++
	entry := &StatusEntry{
		Issuer:     issuer,
		Serial:     serial,
		Status:     status,
		Source:     source,
		ObservedAt: c.now,
		LastUsed:   c.seq,
	}
	c.entries[key] = entry
	c.nodes[key] = c.lru.PushBack(key)
	c.stats.Entries = len(c.entries)
	return nil
}

// Status returns the cached entry for one issuer and serial pair.
func (c *ChainRevocationCache) Status(issuer, serial string) (StatusEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := statusKey{Issuer: issuer, Serial: serial}
	element, ok := c.nodes[key]
	if !ok {
		c.stats.Misses++
		return StatusEntry{}, false
	}

	c.stats.Hits++
	c.lru.MoveToBack(element)
	c.seq++
	c.entries[key].LastUsed = c.seq
	return *c.entries[key], true
}

// Prune drops status entries that are no longer usable.
func (c *ChainRevocationCache) Prune() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	removed := 0
	for key, entry := range c.entries {
		if statusEntryExpired(entry, c.cfg, c.now) {
			c.removeLocked(key)
			removed++
		}
	}
	c.stats.Entries = len(c.entries)
	return removed
}

// Stats returns the current cache counters.
func (c *ChainRevocationCache) Stats() CacheStats {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.stats.Entries = len(c.entries)
	return c.stats
}

// Anchors lists the pinned anchor subjects in insertion order.
func (c *ChainRevocationCache) Anchors() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	var subjects []string
	for _, anchor := range c.anchors {
		subjects = append(subjects, anchor.Subject)
	}
	return subjects
}

// Snapshot serialises the anchors and the status cache.
func (c *ChainRevocationCache) Snapshot() ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	data := snapshotData{
		Version: snapshotFormatVersion,
		Seq:     c.seq,
		Anchors: make([]CertInfo, 0, len(c.anchors)),
		Entries: make([]StatusEntry, 0, len(c.entries)),
		LRU:     make([]statusKey, 0, c.lru.Len()),
		Stats:   c.stats,
	}
	data.Stats.Entries = len(c.entries)

	for _, anchor := range c.anchors {
		data.Anchors = append(data.Anchors, cloneCertInfo(anchor))
	}
	for element := c.lru.Front(); element != nil; element = element.Next() {
		key := element.Value.(statusKey)
		data.LRU = append(data.LRU, key)
		data.Entries = append(data.Entries, *c.entries[key])
	}

	payload, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	return payload, nil
}

// Restore replaces the whole verifier state from a snapshot.
func (c *ChainRevocationCache) Restore(data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(data) == 0 {
		return fmt.Errorf("%w: empty snapshot", ErrBadSnapshot)
	}

	var snapshot snapshotData
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	if err := validateSnapshot(&snapshot); err != nil {
		return err
	}

	anchors := make([]CertInfo, 0, len(snapshot.Anchors))
	anchorIndex := make(map[string]CertInfo, len(snapshot.Anchors))
	for _, anchor := range snapshot.Anchors {
		stored := cloneCertInfo(anchor)
		anchors = append(anchors, stored)
		anchorIndex[stored.Subject] = stored
	}

	entries := make(map[statusKey]*StatusEntry, len(snapshot.Entries))
	nodes := make(map[statusKey]*list.Element, len(snapshot.Entries))
	lru := list.New()
	seq := snapshot.Seq
	entryByKey := make(map[statusKey]StatusEntry, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		entryByKey[statusKey{Issuer: entry.Issuer, Serial: entry.Serial}] = entry
	}

	for _, key := range snapshot.LRU {
		entry, ok := entryByKey[key]
		if !ok {
			return fmt.Errorf("%w: LRU entry is missing", ErrBadSnapshot)
		}

		copied := entry
		keyCopy := key
		entries[keyCopy] = &copied
		nodes[keyCopy] = lru.PushBack(keyCopy)
		if copied.LastUsed > seq {
			seq = copied.LastUsed
		}
	}

	stats := snapshot.Stats
	stats.Entries = len(entries)

	c.seq = seq
	c.anchors = anchors
	c.anchorIndex = anchorIndex
	c.entries = entries
	c.lru = lru
	c.nodes = nodes
	c.stats = stats
	return nil
}

func (c *ChainRevocationCache) verifyLeafRevocation(leaf CertInfo) (string, error) {
	entry, found := c.lookupForVerifyLocked(leaf.Issuer, leaf.Serial)
	if !found {
		return "", ErrStatusUnknown
	}

	switch entry.Status {
	case StatusRevoked:
		return StatusRevoked, ErrRevoked
	case StatusGood:
		age := c.now.Sub(entry.ObservedAt)
		if age > c.cfg.StatusTTL {
			c.stats.Stale++
		}
		if age > c.cfg.StatusTTL+c.cfg.MaxStaleness {
			return "", ErrStatusStale
		}
		return StatusGood, nil
	default:
		return entry.Status, ErrStatusUnknown
	}
}

func (c *ChainRevocationCache) lookupForVerifyLocked(issuer, serial string) (StatusEntry, bool) {
	key := statusKey{Issuer: issuer, Serial: serial}
	element, ok := c.nodes[key]
	if !ok {
		c.stats.Misses++
		return StatusEntry{}, false
	}

	c.stats.Hits++
	c.lru.MoveToBack(element)
	c.seq++
	c.entries[key].LastUsed = c.seq
	return *c.entries[key], true
}

func (c *ChainRevocationCache) touchLocked(key statusKey) {
	element := c.nodes[key]
	c.lru.MoveToBack(element)
	c.seq++
	c.entries[key].LastUsed = c.seq
}

func (c *ChainRevocationCache) evictLRULocked() bool {
	for element := c.lru.Front(); element != nil; element = element.Next() {
		key := element.Value.(statusKey)
		if c.entries[key].Status == StatusRevoked {
			continue
		}

		c.removeLocked(key)
		c.stats.Evictions++
		c.stats.Entries = len(c.entries)
		return true
	}
	return false
}

func (c *ChainRevocationCache) removeLocked(key statusKey) {
	if element, ok := c.nodes[key]; ok {
		c.lru.Remove(element)
		delete(c.nodes, key)
	}
	delete(c.entries, key)
}

func checkCertificateValidity(cert CertInfo, now time.Time) error {
	if now.Before(cert.NotBefore) {
		return ErrCertNotYetValid
	}
	if now.After(cert.NotAfter) {
		return ErrCertExpired
	}
	return nil
}

func checkSubordinatePathLen(chain []CertInfo, index int) error {
	cert := chain[index]
	if !cert.PathLenSet {
		return nil
	}

	subordinateCAs := 0
	for i := 0; i < index; i++ {
		if chain[i].IsCA {
			subordinateCAs++
		}
	}
	if subordinateCAs > cert.PathLen {
		return ErrPathTooLong
	}
	return nil
}

func countCACerts(chain []CertInfo) int {
	count := 0
	for _, cert := range chain {
		if cert.IsCA {
			count++
		}
	}
	return count
}

func hostMatchesSAN(host string, dnsNames []string) bool {
	normalizedHost := normalizeDNSName(host)
	if normalizedHost == "" {
		return false
	}

	for _, rawName := range dnsNames {
		name := normalizeDNSName(rawName)
		if name == "" {
			continue
		}
		if name == normalizedHost || wildcardMatches(name, normalizedHost) {
			return true
		}
	}
	return false
}

func wildcardMatches(pattern, host string) bool {
	if len(pattern) < 3 || pattern[:2] != "*." {
		return false
	}

	parent := pattern[2:]
	suffix := "." + parent
	if parent == "" || host == parent {
		return false
	}
	if len(host) <= len(suffix) || host[len(host)-len(suffix):] != suffix {
		return false
	}

	firstLabel := host[:len(host)-len(suffix)]
	return firstLabel != "" && !containsByte(firstLabel, '.')
}

func leafNamesConstrained(dnsNames, constraints []string) bool {
	if len(constraints) == 0 {
		return true
	}

	for _, name := range dnsNames {
		if !dnsNameMatchesAnyConstraint(name, constraints) {
			return false
		}
	}
	return true
}

func dnsNameMatchesAnyConstraint(name string, constraints []string) bool {
	normalizedName := normalizeDNSName(name)
	if normalizedName == "" {
		return false
	}

	for _, rawConstraint := range constraints {
		constraint := normalizeDNSName(rawConstraint)
		if constraint == "" {
			continue
		}

		if constraint[0] == '.' {
			base := constraint[1:]
			if base != "" && dnsNameHasSuffix(normalizedName, base) {
				return true
			}
			continue
		}

		if normalizedName == constraint || dnsNameHasSuffix(normalizedName, constraint) {
			return true
		}
	}
	return false
}

func dnsNameHasSuffix(name, parent string) bool {
	suffix := "." + parent
	return len(name) > len(suffix) && name[len(name)-len(suffix):] == suffix
}

func normalizeDNSName(value string) string {
	for len(value) > 0 && value[len(value)-1] == '.' {
		value = value[:len(value)-1]
	}
	return toLowerASCII(value)
}

func toLowerASCII(value string) string {
	lowered := make([]byte, len(value))
	for i := 0; i < len(value); i++ {
		b := value[i]
		if b >= 'A' && b <= 'Z' {
			b += 'a' - 'A'
		}
		lowered[i] = b
	}
	return string(lowered)
}

func containsByte(value string, target byte) bool {
	for i := 0; i < len(value); i++ {
		if value[i] == target {
			return true
		}
	}
	return false
}

func cloneCertInfo(cert CertInfo) CertInfo {
	if cert.DNSNames != nil {
		cert.DNSNames = append([]string(nil), cert.DNSNames...)
	}
	if cert.NameConstraints != nil {
		cert.NameConstraints = append([]string(nil), cert.NameConstraints...)
	}
	return cert
}

func statusEntryExpired(entry *StatusEntry, cfg ChainConfig, now time.Time) bool {
	if entry.Status == StatusRevoked {
		return false
	}

	ttl := cfg.StatusTTL
	if entry.Status == StatusGood {
		ttl += cfg.MaxStaleness
	}
	return now.After(entry.ObservedAt.Add(ttl))
}

func validateSnapshot(snapshot *snapshotData) error {
	if snapshot.Version != snapshotFormatVersion {
		return fmt.Errorf("%w: unsupported version", ErrBadSnapshot)
	}
	if snapshot.Seq < 0 {
		return fmt.Errorf("%w: negative sequence", ErrBadSnapshot)
	}
	if snapshot.Stats.Hits < 0 || snapshot.Stats.Misses < 0 ||
		snapshot.Stats.Evictions < 0 || snapshot.Stats.Stale < 0 ||
		snapshot.Stats.Entries < 0 {
		return fmt.Errorf("%w: negative counter", ErrBadSnapshot)
	}
	if snapshot.Stats.Entries != len(snapshot.Entries) {
		return fmt.Errorf("%w: entry count mismatch", ErrBadSnapshot)
	}
	if len(snapshot.LRU) != len(snapshot.Entries) {
		return fmt.Errorf("%w: LRU length mismatch", ErrBadSnapshot)
	}

	anchors := make(map[string]struct{}, len(snapshot.Anchors))
	for _, anchor := range snapshot.Anchors {
		if strings.TrimSpace(anchor.Subject) == "" {
			return fmt.Errorf("%w: blank anchor subject", ErrBadSnapshot)
		}
		if !anchor.IsCA {
			return fmt.Errorf("%w: anchor is not a CA", ErrBadSnapshot)
		}
		if _, exists := anchors[anchor.Subject]; exists {
			return fmt.Errorf("%w: duplicate anchor", ErrBadSnapshot)
		}
		anchors[anchor.Subject] = struct{}{}
	}

	entries := make(map[statusKey]struct{}, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		key := statusKey{Issuer: entry.Issuer, Serial: entry.Serial}
		if strings.TrimSpace(entry.Issuer) == "" || strings.TrimSpace(entry.Serial) == "" {
			return fmt.Errorf("%w: blank status key", ErrBadSnapshot)
		}
		if _, exists := entries[key]; exists {
			return fmt.Errorf("%w: duplicate status entry", ErrBadSnapshot)
		}
		entries[key] = struct{}{}
	}

	lruSeen := make(map[statusKey]struct{}, len(snapshot.LRU))
	for _, key := range snapshot.LRU {
		if _, exists := entries[key]; !exists {
			return fmt.Errorf("%w: LRU references missing entry", ErrBadSnapshot)
		}
		if _, seen := lruSeen[key]; seen {
			return fmt.Errorf("%w: duplicate LRU key", ErrBadSnapshot)
		}
		lruSeen[key] = struct{}{}
	}
	return nil
}
