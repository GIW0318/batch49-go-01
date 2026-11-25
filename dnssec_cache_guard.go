// Package task validates DNSSEC signed answers, rejects out of bailiwick
// data and keeps a bounded cache of validated lookups.
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

// Errors returned by the validating cache.
var (
	ErrBlankQuery         = errors.New("query name and type are required")
	ErrNotAbsolute        = errors.New("query name must be fully qualified")
	ErrUnsupportedClass   = errors.New("only the IN class is supported")
	ErrIDMismatch         = errors.New("response transaction id does not match the query")
	ErrCaseMismatch       = errors.New("response question name does not echo the query case")
	ErrBailiwickViolation = errors.New("record owner is outside the answering zone")
	ErrSignatureMissing   = errors.New("rrset has no signature")
	ErrSignatureInvalid   = errors.New("rrset signature is not valid")
	ErrSignatureExpired   = errors.New("rrset signature is outside its validity window")
	ErrKeyUnknown         = errors.New("signing key is not known for the signer zone")
	ErrNoTrustAnchor      = errors.New("signer zone has no trust anchor")
	ErrDenialInvalid      = errors.New("denial of existence has no valid proof")
	ErrCnameLoop          = errors.New("cname chain loops or exceeds the depth limit")
	ErrBadDelegation      = errors.New("delegation cannot be learned from this zone")
	ErrBadSnapshot        = errors.New("snapshot payload is invalid")
)

var errNotImplemented = errors.New("not implemented")

const snapshotVersion = 1

// ResolverConfig bounds the cache and the accepted TTLs.
type ResolverConfig struct {
	MaxCacheEntries int
	MinTTL          uint32
	MaxTTL          uint32
	MaxCNAME        int
}

// Query is one recursive lookup request.
type Query struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Class    string `json:"class"`
	DO       bool   `json:"do"`
	ID       uint16 `json:"id"`
	CaseName string `json:"case_name"`
}

// RR is one resource record inside an rrset.
type RR struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	TTL   uint32 `json:"ttl"`
	RData string `json:"rdata"`
}

// Rrsig is the signature that covers one rrset.
type Rrsig struct {
	TypeCovered string    `json:"type_covered"`
	SignerName  string    `json:"signer_name"`
	KeyTag      int       `json:"key_tag"`
	Inception   time.Time `json:"inception"`
	Expiration  time.Time `json:"expiration"`
	Labels      int       `json:"labels"`
	Valid       bool      `json:"valid"`
}

// RRset is one owner and type with its records and signatures.
type RRset struct {
	Name    string  `json:"name"`
	Type    string  `json:"type"`
	Class   string  `json:"class"`
	TTL     uint32  `json:"ttl"`
	Records []RR    `json:"records"`
	Sigs    []Rrsig `json:"sigs"`
}

// DnskeyRecord is one zone signing key registered locally.
type DnskeyRecord struct {
	Zone      string `json:"zone"`
	KeyTag    int    `json:"key_tag"`
	Flags     int    `json:"flags"`
	Algorithm string `json:"algorithm"`
	PublicKey string `json:"public_key"`
}

// DsRecord is one delegation signer record from the parent zone.
type DsRecord struct {
	Zone      string `json:"zone"`
	KeyTag    int    `json:"key_tag"`
	Algorithm string `json:"algorithm"`
	Digest    string `json:"digest"`
}

// Response is the upstream answer handed to the validator.
type Response struct {
	ID           uint16  `json:"id"`
	QuestionName string  `json:"question_name"`
	Zone         string  `json:"zone"`
	Answer       []RRset `json:"answer"`
	Authority    []RRset `json:"authority"`
	Additional   []RRset `json:"additional"`
}

// Answer is one validated lookup result.
type Answer struct {
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	Class     string    `json:"class"`
	RData     []string  `json:"rdata"`
	TTL       uint32    `json:"ttl"`
	Secure    bool      `json:"secure"`
	Denial    bool      `json:"denial"`
	ExpiresAt time.Time `json:"expires_at"`
}

// CacheStats reports lookup and poisoning counters.
type CacheStats struct {
	Entries  int `json:"entries"`
	Hits     int `json:"hits"`
	Misses   int `json:"misses"`
	Poisoned int `json:"poisoned"`
	Denials  int `json:"denials"`
}

type cacheKey struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Class string `json:"class"`
	DO    bool   `json:"do"`
}

type cacheEntry struct {
	Key    cacheKey `json:"key"`
	Answer Answer   `json:"answer"`
}

type rrsetKey struct {
	owner string
	typ   string
}

type snapshotData struct {
	Version     int                             `json:"version"`
	Anchors     map[string]DsRecord             `json:"anchors"`
	Keys        map[string]map[int]DnskeyRecord `json:"keys"`
	Delegations map[string][]DsRecord           `json:"delegations"`
	Entries     []cacheEntry                    `json:"entries"`
	Stats       CacheStats                      `json:"stats"`
}

// DnssecCacheGuard owns the anchors, the zone keys and the answer cache.
type DnssecCacheGuard struct {
	cfg         ResolverConfig
	mu          sync.Mutex
	now         time.Time
	anchors     map[string]DsRecord
	keys        map[string]map[int]DnskeyRecord
	delegations map[string][]DsRecord
	cache       map[cacheKey]*list.Element
	lru         *list.List
	stats       CacheStats
}

// NewDnssecCacheGuard builds an empty validating cache.
func NewDnssecCacheGuard(cfg ResolverConfig) *DnssecCacheGuard {
	return &DnssecCacheGuard{
		cfg:         cfg,
		now:         time.Now().UTC(),
		anchors:     make(map[string]DsRecord),
		keys:        make(map[string]map[int]DnskeyRecord),
		delegations: make(map[string][]DsRecord),
		cache:       make(map[cacheKey]*list.Element),
		lru:         list.New(),
	}
}

func (g *DnssecCacheGuard) initLocked() {
	if g.now.IsZero() {
		g.now = time.Now().UTC()
	}
	if g.anchors == nil {
		g.anchors = make(map[string]DsRecord)
	}
	if g.keys == nil {
		g.keys = make(map[string]map[int]DnskeyRecord)
	}
	if g.delegations == nil {
		g.delegations = make(map[string][]DsRecord)
	}
	if g.cache == nil {
		g.cache = make(map[cacheKey]*list.Element)
	}
	if g.lru == nil {
		g.lru = list.New()
	}
}

// SetNow fixes the clock used for signatures, TTLs and cache expiry.
func (g *DnssecCacheGuard) SetNow(now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()
	g.now = now
}

// SetTrustAnchor pins the DS record of one zone.
func (g *DnssecCacheGuard) SetTrustAnchor(zone string, ds DsRecord) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()

	z := canonicalName(zone)
	if z == "" {
		return errors.New("trust anchor zone is required")
	}
	ds.Zone = z
	g.anchors[z] = ds
	return nil
}

// AddZoneKey registers one DNSKEY for a zone.
func (g *DnssecCacheGuard) AddZoneKey(zone string, key DnskeyRecord) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()

	z := canonicalName(zone)
	if z == "" {
		return errors.New("DNSKEY zone is required")
	}
	key.Zone = z
	if g.keys[z] == nil {
		g.keys[z] = make(map[int]DnskeyRecord)
	}
	g.keys[z][key.KeyTag] = key
	return nil
}

// LearnDelegation stores DS records for a child zone learned from its parent.
func (g *DnssecCacheGuard) LearnDelegation(zone string, ds []DsRecord) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()

	child := canonicalName(zone)
	if child == "" || child == "." {
		return ErrBadDelegation
	}
	parent := g.coveringZoneLocked(child)
	if parent == "" {
		return ErrBadDelegation
	}

	stored := make([]DsRecord, 0, len(ds))
	for _, record := range ds {
		record.Zone = child
		stored = append(stored, record)
	}
	g.delegations[child] = append(g.delegations[child], stored...)
	return nil
}

// Resolve validates one upstream response and caches the result.
func (g *DnssecCacheGuard) Resolve(q Query, resp Response) (Answer, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()

	if err := validateQuery(q); err != nil {
		return Answer{}, err
	}
	if resp.ID != q.ID {
		return Answer{}, ErrIDMismatch
	}
	if q.CaseName != "" && resp.QuestionName != q.CaseName {
		return Answer{}, ErrCaseMismatch
	}

	zone := canonicalName(resp.Zone)
	if !withinZone(q.Name, zone) {
		return Answer{}, ErrBailiwickViolation
	}

	authority := g.filterBailiwickLocked(resp.Authority, zone)
	g.filterBailiwickLocked(resp.Additional, zone)
	if err := ensureAnswerBailiwick(resp.Answer, zone); err != nil {
		return Answer{}, err
	}

	key := newCacheKey(q)
	answer, err := g.buildAnswerLocked(q, resp.Answer, authority, zone)
	if err != nil {
		return Answer{}, err
	}

	g.putLocked(key, cacheEntry{Key: key, Answer: answer})
	if answer.Denial {
		g.stats.Denials++
	}
	return cloneAnswer(answer), nil
}

// CacheGet returns a live cached answer for one query.
func (g *DnssecCacheGuard) CacheGet(q Query) (Answer, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()

	if err := validateQuery(q); err != nil {
		g.stats.Misses++
		return Answer{}, false
	}

	key := newCacheKey(q)
	element, ok := g.cache[key]
	if !ok {
		g.stats.Misses++
		return Answer{}, false
	}

	entry, ok := element.Value.(*cacheEntry)
	if !ok || entry == nil {
		g.lru.Remove(element)
		delete(g.cache, key)
		g.stats.Misses++
		return Answer{}, false
	}
	if !entry.Answer.ExpiresAt.After(g.now) {
		g.removeElementLocked(element)
		g.stats.Misses++
		return Answer{}, false
	}

	g.lru.MoveToFront(element)
	g.stats.Hits++
	return cloneAnswer(entry.Answer), true
}

// Purge drops cached answers under one zone.
func (g *DnssecCacheGuard) Purge(zone string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()

	root := canonicalName(zone)
	if root == "" {
		return 0
	}

	removed := 0
	for key, element := range g.cache {
		if withinZone(key.Name, root) {
			g.removeElementLocked(element)
			delete(g.cache, key)
			removed++
		}
	}
	return removed
}

// Stats returns the current cache counters.
func (g *DnssecCacheGuard) Stats() CacheStats {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()

	stats := g.stats
	stats.Entries = g.lru.Len()
	return stats
}

// Snapshot serialises anchors, zone keys and cached answers.
func (g *DnssecCacheGuard) Snapshot() ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()

	entries := make([]cacheEntry, 0, g.lru.Len())
	for element := g.lru.Front(); element != nil; element = element.Next() {
		entry, ok := element.Value.(*cacheEntry)
		if !ok || entry == nil {
			continue
		}
		entries = append(entries, cacheEntry{
			Key:    entry.Key,
			Answer: cloneAnswer(entry.Answer),
		})
	}

	keys := make(map[string]map[int]DnskeyRecord, len(g.keys))
	for zone, zoneKeys := range g.keys {
		copied := make(map[int]DnskeyRecord, len(zoneKeys))
		for tag, key := range zoneKeys {
			copied[tag] = key
		}
		keys[zone] = copied
	}

	anchors := make(map[string]DsRecord, len(g.anchors))
	for zone, ds := range g.anchors {
		anchors[zone] = ds
	}
	delegations := make(map[string][]DsRecord, len(g.delegations))
	for zone, records := range g.delegations {
		delegations[zone] = append([]DsRecord(nil), records...)
	}

	stats := g.stats
	stats.Entries = len(entries)
	payload := snapshotData{
		Version:     snapshotVersion,
		Anchors:     anchors,
		Keys:        keys,
		Delegations: delegations,
		Entries:     entries,
		Stats:       stats,
	}
	return json.Marshal(payload)
}

// Restore replaces the whole guard state from a snapshot.
func (g *DnssecCacheGuard) Restore(data []byte) error {
	var payload snapshotData
	if err := json.Unmarshal(data, &payload); err != nil {
		return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	if payload.Version != snapshotVersion || payload.Stats.Hits < 0 ||
		payload.Stats.Misses < 0 || payload.Stats.Poisoned < 0 ||
		payload.Stats.Denials < 0 || payload.Stats.Entries < 0 {
		return fmt.Errorf("%w: unsupported or inconsistent snapshot", ErrBadSnapshot)
	}
	if payload.Stats.Entries != len(payload.Entries) {
		return fmt.Errorf("%w: entry count does not match entries", ErrBadSnapshot)
	}

	anchors := make(map[string]DsRecord, len(payload.Anchors))
	for zone, ds := range payload.Anchors {
		z := canonicalName(zone)
		if z == "" {
			return fmt.Errorf("%w: empty anchor zone", ErrBadSnapshot)
		}
		ds.Zone = z
		anchors[z] = ds
	}

	keys := make(map[string]map[int]DnskeyRecord, len(payload.Keys))
	for zone, zoneKeys := range payload.Keys {
		z := canonicalName(zone)
		if z == "" {
			return fmt.Errorf("%w: empty key zone", ErrBadSnapshot)
		}
		keys[z] = make(map[int]DnskeyRecord, len(zoneKeys))
		for tag, key := range zoneKeys {
			key.Zone = z
			key.KeyTag = tag
			keys[z][tag] = key
		}
	}

	delegations := make(map[string][]DsRecord, len(payload.Delegations))
	for zone, records := range payload.Delegations {
		z := canonicalName(zone)
		if z == "" {
			return fmt.Errorf("%w: empty delegation zone", ErrBadSnapshot)
		}
		stored := make([]DsRecord, 0, len(records))
		for _, ds := range records {
			ds.Zone = z
			stored = append(stored, ds)
		}
		delegations[z] = stored
	}

	lru := list.New()
	cache := make(map[cacheKey]*list.Element, len(payload.Entries))
	seen := make(map[cacheKey]struct{}, len(payload.Entries))
	for i := range payload.Entries {
		entry := payload.Entries[i]
		entry.Key = normalizeCacheKey(entry.Key)
		if _, duplicate := seen[entry.Key]; duplicate {
			return fmt.Errorf("%w: duplicate cache key", ErrBadSnapshot)
		}
		seen[entry.Key] = struct{}{}
		stored := &cacheEntry{Key: entry.Key, Answer: cloneAnswer(entry.Answer)}
		cache[entry.Key] = lru.PushBack(stored)
	}

	stats := payload.Stats
	stats.Entries = lru.Len()

	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()
	g.anchors = anchors
	g.keys = keys
	g.delegations = delegations
	g.cache = cache
	g.lru = lru
	g.stats = stats

	if g.cfg.MaxCacheEntries > 0 {
		for g.lru.Len() > g.cfg.MaxCacheEntries {
			oldest := g.lru.Back()
			if oldest == nil {
				break
			}
			g.removeElementLocked(oldest)
		}
		g.stats.Entries = g.lru.Len()
	}
	return nil
}

func (g *DnssecCacheGuard) removeElementLocked(element *list.Element) {
	if element == nil {
		return
	}
	if entry, ok := element.Value.(*cacheEntry); ok && entry != nil {
		delete(g.cache, entry.Key)
	}
	g.lru.Remove(element)
}

func (g *DnssecCacheGuard) putLocked(key cacheKey, entry cacheEntry) {
	if g.cache == nil {
		g.initLocked()
	}
	if element, ok := g.cache[key]; ok {
		stored, _ := element.Value.(*cacheEntry)
		if stored == nil {
			stored = &cacheEntry{Key: key}
			element.Value = stored
		}
		stored.Answer = cloneAnswer(entry.Answer)
		stored.Key = key
		g.lru.MoveToFront(element)
		return
	}

	stored := &cacheEntry{Key: key, Answer: cloneAnswer(entry.Answer)}
	element := g.lru.PushFront(stored)
	g.cache[key] = element

	if g.cfg.MaxCacheEntries > 0 {
		for g.lru.Len() > g.cfg.MaxCacheEntries {
			oldest := g.lru.Back()
			if oldest == nil {
				break
			}
			g.removeElementLocked(oldest)
		}
	}
}

func validateQuery(q Query) error {
	if q.Name == "" || q.Type == "" {
		return ErrBlankQuery
	}
	if !strings.HasSuffix(q.Name, ".") {
		return ErrNotAbsolute
	}
	if !strings.EqualFold(q.Class, "IN") {
		return ErrUnsupportedClass
	}
	return nil
}

func newCacheKey(q Query) cacheKey {
	return cacheKey{
		Name:  canonicalName(q.Name),
		Type:  strings.ToUpper(strings.TrimSpace(q.Type)),
		Class: "IN",
		DO:    q.DO,
	}
}

func normalizeCacheKey(key cacheKey) cacheKey {
	return cacheKey{
		Name:  canonicalName(key.Name),
		Type:  strings.ToUpper(strings.TrimSpace(key.Type)),
		Class: strings.ToUpper(strings.TrimSpace(key.Class)),
		DO:    key.DO,
	}
}

func cloneAnswer(answer Answer) Answer {
	if answer.RData != nil {
		answer.RData = append([]string(nil), answer.RData...)
	}
	return answer
}

// canonicalName lower-cases an owner and makes non-empty, relative fixture
// names absolute for comparison. Query validation happens before this helper.
func canonicalName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" || name == "." || strings.HasSuffix(name, ".") {
		return name
	}
	return name + "."
}

func withinZone(name, zone string) bool {
	n := canonicalName(name)
	z := canonicalName(zone)
	if n == "" || z == "" {
		return false
	}
	if z == "." {
		return strings.HasSuffix(n, ".")
	}
	return n == z || strings.HasSuffix(n, "."+z)
}

func dnsLabels(name string) int {
	n := canonicalName(name)
	if n == "" || n == "." {
		return 0
	}
	trimmed := strings.TrimSuffix(n, ".")
	if trimmed == "" {
		return 0
	}
	return strings.Count(trimmed, ".") + 1
}

func (g *DnssecCacheGuard) coveringZoneLocked(child string) string {
	best := ""
	for zone := range g.anchors {
		if zone != child && withinZone(child, zone) && len(zone) > len(best) {
			best = zone
		}
	}
	for zone := range g.delegations {
		if zone != child && withinZone(child, zone) && len(zone) > len(best) {
			best = zone
		}
	}
	return best
}

func (g *DnssecCacheGuard) filterBailiwickLocked(sets []RRset, zone string) []RRset {
	kept := make([]RRset, 0, len(sets))
	for _, set := range sets {
		outside := !withinZone(set.Name, zone)
		if !outside {
			for _, record := range set.Records {
				if strings.TrimSpace(record.Name) != "" && !withinZone(record.Name, zone) {
					outside = true
					break
				}
			}
		}
		if outside {
			g.stats.Poisoned++
			continue
		}
		kept = append(kept, set)
	}
	return kept
}

func ensureAnswerBailiwick(sets []RRset, zone string) error {
	for _, set := range sets {
		if !withinZone(set.Name, zone) {
			return ErrBailiwickViolation
		}
		for _, record := range set.Records {
			if strings.TrimSpace(record.Name) != "" && !withinZone(record.Name, zone) {
				return ErrBailiwickViolation
			}
		}
	}
	return nil
}

func (g *DnssecCacheGuard) buildAnswerLocked(q Query, answers []RRset, authority []RRset, zone string) (Answer, error) {
	queryType := strings.ToUpper(strings.TrimSpace(q.Type))
	indexed := make(map[rrsetKey][]RRset)
	for _, set := range answers {
		key := rrsetKey{owner: canonicalName(set.Name), typ: strings.ToUpper(strings.TrimSpace(set.Type))}
		indexed[key] = append(indexed[key], set)
	}

	current := canonicalName(q.Name)
	visited := map[string]struct{}{current: {}}
	chain := make([]RRset, 0, 2)
	hops := 0

	var final RRset
	for {
		if matches := indexed[rrsetKey{owner: current, typ: queryType}]; len(matches) > 0 {
			final = pickRRset(matches)
			chain = append(chain, final)
			break
		}

		cnames := indexed[rrsetKey{owner: current, typ: "CNAME"}]
		if len(cnames) == 0 {
			return g.resolveDenialLocked(q, authority)
		}

		if hops >= g.cfg.MaxCNAME {
			return Answer{}, ErrCnameLoop
		}
		cname := pickRRset(cnames)
		chain = append(chain, cname)
		target := cnameTarget(cname)
		if target == "" {
			return Answer{}, ErrCnameLoop
		}
		if !withinZone(target, zone) {
			g.stats.Poisoned++
			return Answer{}, ErrBailiwickViolation
		}
		if _, seen := visited[target]; seen {
			return Answer{}, ErrCnameLoop
		}
		visited[target] = struct{}{}
		current = target
		hops++
	}

	ttl := minimumTTL(chain...)
	earliestExpiry := time.Time{}
	secure := false
	if q.DO {
		var err error
		earliestExpiry, err = g.validateChainSignaturesLocked(chain)
		if err != nil {
			return Answer{}, err
		}
		secure = true
	}

	ttl = clampTTL(ttl, g.cfg)
	if q.DO {
		ttl = capTTLToExpiry(ttl, earliestExpiry, g.now)
	}

	rdata := make([]string, 0, len(final.Records))
	for _, record := range final.Records {
		rdata = append(rdata, record.RData)
	}

	return Answer{
		Name:      canonicalName(q.Name),
		Type:      queryType,
		Class:     "IN",
		RData:     rdata,
		TTL:       ttl,
		Secure:    secure,
		Denial:    false,
		ExpiresAt: g.now.Add(time.Duration(ttl) * time.Second),
	}, nil
}

func pickRRset(sets []RRset) RRset {
	for _, set := range sets {
		if len(set.Records) > 0 {
			return set
		}
	}
	return sets[0]
}

func cnameTarget(set RRset) string {
	for _, record := range set.Records {
		if strings.TrimSpace(record.RData) != "" {
			return canonicalName(record.RData)
		}
	}
	return ""
}

func minimumTTL(sets ...RRset) uint32 {
	found := false
	var ttl uint32
	for _, set := range sets {
		if !found || set.TTL < ttl {
			ttl = set.TTL
			found = true
		}
		for _, record := range set.Records {
			if !found || record.TTL < ttl {
				ttl = record.TTL
				found = true
			}
		}
	}
	return ttl
}

func clampTTL(ttl uint32, cfg ResolverConfig) uint32 {
	if ttl < cfg.MinTTL {
		ttl = cfg.MinTTL
	}
	if cfg.MaxTTL > 0 && ttl > cfg.MaxTTL {
		ttl = cfg.MaxTTL
	}
	return ttl
}

func capTTLToExpiry(ttl uint32, expires time.Time, now time.Time) uint32 {
	if expires.IsZero() {
		return ttl
	}
	remaining := expires.Sub(now)
	if remaining <= 0 {
		return 0
	}
	seconds := uint32(remaining / time.Second)
	if seconds < ttl {
		return seconds
	}
	return ttl
}

func (g *DnssecCacheGuard) validateChainSignaturesLocked(chain []RRset) (time.Time, error) {
	earliest := time.Time{}
	for _, set := range chain {
		expiration, err := g.validateRRsetSignaturesLocked(set)
		if err != nil {
			return time.Time{}, err
		}
		if earliest.IsZero() || expiration.Before(earliest) {
			earliest = expiration
		}
	}
	return earliest, nil
}

func (g *DnssecCacheGuard) validateRRsetSignaturesLocked(set RRset) (time.Time, error) {
	matching := make([]Rrsig, 0, len(set.Sigs))
	for _, sig := range set.Sigs {
		if strings.EqualFold(sig.TypeCovered, set.Type) {
			matching = append(matching, sig)
		}
	}
	if len(matching) == 0 {
		return time.Time{}, ErrSignatureMissing
	}

	var firstErr error
	for _, sig := range matching {
		expiration, err := g.validateSignatureLocked(set, sig)
		if err == nil {
			return expiration, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return time.Time{}, firstErr
}

func (g *DnssecCacheGuard) validateSignatureLocked(set RRset, sig Rrsig) (time.Time, error) {
	if !sig.Valid {
		return time.Time{}, ErrSignatureInvalid
	}

	signer := canonicalName(sig.SignerName)
	dsRecords, trusted := g.trustedDSLocked(signer)
	if !trusted || !withinZone(set.Name, signer) {
		return time.Time{}, ErrNoTrustAnchor
	}

	zoneKeys := g.keys[signer]
	key, known := zoneKeys[sig.KeyTag]
	if !known {
		return time.Time{}, ErrKeyUnknown
	}

	dsMatched := false
	for _, ds := range dsRecords {
		if ds.KeyTag == key.KeyTag && strings.EqualFold(ds.Algorithm, key.Algorithm) {
			dsMatched = true
			break
		}
	}
	if !dsMatched {
		return time.Time{}, ErrKeyUnknown
	}

	if g.now.Before(sig.Inception) || g.now.After(sig.Expiration) {
		return time.Time{}, ErrSignatureExpired
	}
	if sig.Labels != dnsLabels(set.Name) {
		return time.Time{}, ErrSignatureInvalid
	}
	return sig.Expiration, nil
}

func (g *DnssecCacheGuard) trustedDSLocked(signer string) ([]DsRecord, bool) {
	if ds, ok := g.anchors[signer]; ok {
		return []DsRecord{ds}, true
	}
	if records, ok := g.delegations[signer]; ok && len(records) > 0 {
		return records, true
	}
	return nil, false
}

func (g *DnssecCacheGuard) resolveDenialLocked(q Query, authority []RRset) (Answer, error) {
	var soa RRset
	haveSOA := false
	proofs := make([]RRset, 0)
	for _, set := range authority {
		switch strings.ToUpper(strings.TrimSpace(set.Type)) {
		case "SOA":
			if !haveSOA {
				soa = set
				haveSOA = true
			}
		case "NSEC", "NSEC3":
			proofs = append(proofs, set)
		}
	}

	if !haveSOA || len(proofs) == 0 {
		return Answer{}, ErrDenialInvalid
	}

	ttl := minimumTTL(soa)
	for _, proof := range proofs {
		proofTTL := minimumTTL(proof)
		if proofTTL < ttl {
			ttl = proofTTL
		}
	}

	earliestExpiry := time.Time{}
	secure := false
	if q.DO {
		var sigErr error
		validProofs := 0
		for _, proof := range proofs {
			expiration, err := g.validateRRsetSignaturesLocked(proof)
			if err == nil {
				validProofs++
				if earliestExpiry.IsZero() || expiration.Before(earliestExpiry) {
					earliestExpiry = expiration
				}
				continue
			}
			if sigErr == nil {
				sigErr = err
			}
		}
		if validProofs == 0 {
			if errors.Is(sigErr, ErrSignatureMissing) {
				return Answer{}, ErrDenialInvalid
			}
			if sigErr == nil {
				sigErr = ErrDenialInvalid
			}
			return Answer{}, sigErr
		}
		secure = true
	}

	ttl = clampTTL(ttl, g.cfg)
	if q.DO {
		ttl = capTTLToExpiry(ttl, earliestExpiry, g.now)
	}

	return Answer{
		Name:      canonicalName(q.Name),
		Type:      strings.ToUpper(strings.TrimSpace(q.Type)),
		Class:     "IN",
		RData:     []string{},
		TTL:       ttl,
		Secure:    secure,
		Denial:    true,
		ExpiresAt: g.now.Add(time.Duration(ttl) * time.Second),
	}, nil
}
