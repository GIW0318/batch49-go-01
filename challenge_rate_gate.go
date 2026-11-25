// Package task gates traffic by issuing proof of work challenges to new
// sources, verifying one time solutions and rate limiting admitted sources.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"
)

// Decision actions.
const (
	ActionAllow     = "allow"
	ActionChallenge = "challenge"
	ActionReject    = "reject"
)

// Errors returned by the gate.
var (
	ErrBlankSource      = errors.New("source address is required")
	ErrBadSource        = errors.New("source address is not a valid ip")
	ErrBadCIDR          = errors.New("prefix is not valid cidr")
	ErrUnknownChallenge = errors.New("challenge not found")
	ErrChallengeExpired = errors.New("challenge has expired")
	ErrReplay           = errors.New("challenge has already been solved")
	ErrBadSolution      = errors.New("proof of work does not meet the difficulty")
	ErrBlocked          = errors.New("source is blocked")
	ErrRateLimited      = errors.New("source exceeded its admission rate")
	ErrSourceLimit      = errors.New("tracked source capacity reached")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

const (
	snapshotVersion = 1
	digestHexLength = sha256.Size * 2
)

// GateConfig holds the challenge policy and the bucket limits.
type GateConfig struct {
	DifficultyBase  int
	DifficultyMax   int
	ChallengeTTL    time.Duration
	PassTTL         time.Duration
	MaxFailures     int
	BlockDuration   time.Duration
	PenaltyDecay    time.Duration
	RefillPerSecond float64
	Burst           int
	MaxSources      int
	IPv6PrefixBits  int
}

// Challenge is one proof of work request for a source prefix.
type Challenge struct {
	ID         string    `json:"id"`
	Prefix     string    `json:"prefix"`
	Difficulty int       `json:"difficulty"`
	ExpiresAt  time.Time `json:"expires_at"`
	Used       bool      `json:"used"`
}

// Pass is the admission token granted after a solved challenge.
type Pass struct {
	Token     string    `json:"token"`
	Prefix    string    `json:"prefix"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Decision is the admission answer for one source.
type Decision struct {
	Action      string        `json:"action"`
	Difficulty  int           `json:"difficulty"`
	ChallengeID string        `json:"challenge_id"`
	RetryAfter  time.Duration `json:"retry_after"`
}

// BucketState is the live rate limit view of one source prefix.
type BucketState struct {
	Prefix       string    `json:"prefix"`
	Tokens       float64   `json:"tokens"`
	Failures     int       `json:"failures"`
	BlockedUntil time.Time `json:"blocked_until"`
}

// GateStats reports the gate counters.
type GateStats struct {
	Sources    int `json:"sources"`
	Challenges int `json:"challenges"`
	Passes     int `json:"passes"`
	Allowed    int `json:"allowed"`
	Challenged int `json:"challenged"`
	Rejected   int `json:"rejected"`
	Blocked    int `json:"blocked"`
	Failures   int `json:"failures"`
	Evictions  int `json:"evictions"`
}

type sourceRecord struct {
	prefix           string
	tokens           float64
	lastRefill       time.Time
	lastSeen         time.Time
	failures         int
	lastFailure      time.Time
	blockedUntil     time.Time
	pass             *Pass
	activeChallenge  string
	challengePenalty int
}

type prefixRule struct {
	prefix netip.Prefix
	bits   int
}

// ChallengeRateGate owns the prefixes, challenges, passes and buckets.
type ChallengeRateGate struct {
	mu sync.Mutex

	cfg GateConfig
	now time.Time
	seq int64

	sources          map[string]*sourceRecord
	challenges       map[string]*Challenge
	activeChallenges map[string]string
	allowRules       []prefixRule
	denyRules        []prefixRule

	allowed        int
	challenged     int
	rejected       int
	blockedEvents  int
	failureEvents  int
	evictionEvents int
}

// NewChallengeRateGate builds an empty gate.
func NewChallengeRateGate(cfg GateConfig) *ChallengeRateGate {
	return &ChallengeRateGate{
		cfg:              cfg,
		now:              time.Now(),
		sources:          make(map[string]*sourceRecord),
		challenges:       make(map[string]*Challenge),
		activeChallenges: make(map[string]string),
	}
}

// SetNow fixes the clock used for challenges, blocks and refill.
func (g *ChallengeRateGate) SetNow(now time.Time) {
	g.mu.Lock()
	g.now = now
	g.mu.Unlock()
}

// Admit decides whether one source may proceed.
func (g *ChallengeRateGate) Admit(source string) (Decision, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	addr, prefix, err := normalizeSource(source, g.cfg.IPv6PrefixBits)
	if err != nil {
		return Decision{}, err
	}

	allowed, denied := g.matchPolicy(addr)
	if denied {
		return Decision{Action: ActionReject}, ErrBlocked
	}

	rec, err := g.ensureSource(prefix, g.now)
	if err != nil {
		return Decision{}, err
	}
	rec.lastSeen = g.now
	g.decayPenalty(rec, g.now)

	if g.isBlocked(rec, g.now) {
		return Decision{Action: ActionReject}, ErrBlocked
	}

	if rec.pass != nil {
		if g.now.Before(rec.pass.ExpiresAt) {
			return g.admitWithBucket(rec, g.now)
		}
		rec.pass = nil
	}

	// An allow-listed source skips the challenge, but not its bucket.
	if allowed {
		return g.admitWithBucket(rec, g.now)
	}

	ch := g.issueOrReuseChallenge(rec, g.now)
	g.challenged++
	return Decision{
		Action:      ActionChallenge,
		Difficulty:  ch.Difficulty,
		ChallengeID: ch.ID,
	}, nil
}

// Verify checks one proof of work and grants a pass on success.
func (g *ChallengeRateGate) Verify(challengeID, nonce, digest string) (Pass, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	ch, ok := g.challenges[challengeID]
	if !ok || challengeID == "" {
		return Pass{}, ErrUnknownChallenge
	}
	rec := g.sources[ch.Prefix]
	if rec == nil {
		return Pass{}, ErrUnknownChallenge
	}
	rec.lastSeen = g.now

	g.decayPenalty(rec, g.now)
	if g.isBlocked(rec, g.now) {
		return Pass{}, ErrBlocked
	}
	if !g.now.Before(ch.ExpiresAt) {
		return Pass{}, ErrChallengeExpired
	}
	if ch.Used {
		return Pass{}, ErrReplay
	}

	if len(digest) != digestHexLength || !isLowerOrUpperHex(digest) {
		return Pass{}, ErrBadSolution
	}

	want := sha256.Sum256([]byte(challengeID + ":" + nonce))
	wantHex := hex.EncodeToString(want[:])
	if !strings.EqualFold(digest, wantHex) || !meetsDifficulty(digest, ch.Difficulty) {
		g.recordFailure(rec, g.now)
		return Pass{}, ErrBadSolution
	}

	ch.Used = true
	if rec.activeChallenge == ch.ID {
		rec.activeChallenge = ""
	}
	if id := g.activeChallenges[ch.Prefix]; id == ch.ID {
		delete(g.activeChallenges, ch.Prefix)
	}

	pass := &Pass{
		Token:     g.nextToken("pass", ch.Prefix),
		Prefix:    ch.Prefix,
		ExpiresAt: g.now.Add(g.cfg.PassTTL),
	}
	rec.pass = pass
	rec.tokens = g.bucketCapacity()
	rec.lastRefill = g.now
	rec.lastSeen = g.now
	return *pass, nil
}

// AllowPrefix adds one prefix to the allow list.
func (g *ChallengeRateGate) AllowPrefix(cidr string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	rule, err := parsePrefixRule(cidr)
	if err != nil {
		return err
	}
	g.allowRules = append(g.allowRules, rule)
	return nil
}

// DenyPrefix adds one prefix to the deny list.
func (g *ChallengeRateGate) DenyPrefix(cidr string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	rule, err := parsePrefixRule(cidr)
	if err != nil {
		return err
	}
	g.denyRules = append(g.denyRules, rule)
	return nil
}

// Challenge returns a stored challenge by id.
func (g *ChallengeRateGate) Challenge(challengeID string) (Challenge, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	ch, ok := g.challenges[challengeID]
	if !ok {
		return Challenge{}, false
	}
	return *ch, true
}

// Bucket returns the live bucket of one source prefix.
func (g *ChallengeRateGate) Bucket(source string) (BucketState, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	_, prefix, err := normalizeSource(source, g.cfg.IPv6PrefixBits)
	if err != nil {
		return BucketState{}, false
	}
	rec, ok := g.sources[prefix]
	if !ok {
		return BucketState{}, false
	}

	g.decayPenalty(rec, g.now)
	g.refill(rec, g.now)
	blockedUntil := rec.blockedUntil
	if !blockedUntil.IsZero() && !g.now.Before(blockedUntil) {
		blockedUntil = time.Time{}
	}
	return BucketState{
		Prefix:       rec.prefix,
		Tokens:       rec.tokens,
		Failures:     rec.failures,
		BlockedUntil: blockedUntil,
	}, true
}

// Sweep drops expired challenges, passes and idle buckets.
func (g *ChallengeRateGate) Sweep() int {
	g.mu.Lock()
	defer g.mu.Unlock()

	removed := 0
	for id, ch := range g.challenges {
		if g.now.Before(ch.ExpiresAt) {
			continue
		}
		delete(g.challenges, id)
		removed++
		if g.activeChallenges[ch.Prefix] == id {
			delete(g.activeChallenges, ch.Prefix)
		}
		if rec := g.sources[ch.Prefix]; rec != nil && rec.activeChallenge == id {
			rec.activeChallenge = ""
		}
	}

	idleAfter := g.idleDuration()
	for prefix, rec := range g.sources {
		g.decayPenalty(rec, g.now)
		if rec.pass != nil && !g.now.Before(rec.pass.ExpiresAt) {
			rec.pass = nil
			removed++
		}
		if !rec.blockedUntil.IsZero() && !g.now.Before(rec.blockedUntil) {
			rec.blockedUntil = time.Time{}
		}
		if g.isBlocked(rec, g.now) || hasValidPass(rec, g.now) {
			continue
		}
		idle := rec.lastSeen.IsZero() || g.now.Sub(rec.lastSeen) >= idleAfter
		if !idle {
			continue
		}
		removed++
		removed += g.removeSource(prefix)
	}
	return removed
}

// Stats returns the current gate counters.
func (g *ChallengeRateGate) Stats() GateStats {
	g.mu.Lock()
	defer g.mu.Unlock()

	passes := 0
	for _, rec := range g.sources {
		if hasValidPass(rec, g.now) {
			passes++
		}
	}
	return GateStats{
		Sources:    len(g.sources),
		Challenges: len(g.challenges),
		Passes:     passes,
		Allowed:    g.allowed,
		Challenged: g.challenged,
		Rejected:   g.rejected,
		Blocked:    g.blockedEvents,
		Failures:   g.failureEvents,
		Evictions:  g.evictionEvents,
	}
}

// Snapshot serialises buckets, challenges, passes and counters.
func (g *ChallengeRateGate) Snapshot() ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	doc := snapshotDoc{
		Version:          snapshotVersion,
		Sequence:         g.seq,
		Allowed:          g.allowed,
		Challenged:       g.challenged,
		Rejected:         g.rejected,
		Blocked:          g.blockedEvents,
		Failures:         g.failureEvents,
		Evictions:        g.evictionEvents,
		Allow:            make([]string, 0, len(g.allowRules)),
		Deny:             make([]string, 0, len(g.denyRules)),
		Sources:          make([]snapshotSource, 0, len(g.sources)),
		Challenges:       make([]Challenge, 0, len(g.challenges)),
		ActiveChallenges: make(map[string]string, len(g.activeChallenges)),
	}

	for _, rule := range g.allowRules {
		doc.Allow = append(doc.Allow, rule.prefix.String())
	}
	for _, rule := range g.denyRules {
		doc.Deny = append(doc.Deny, rule.prefix.String())
	}
	for prefix, rec := range g.sources {
		ss := snapshotSource{
			Prefix:           prefix,
			Tokens:           rec.tokens,
			LastRefill:       rec.lastRefill,
			LastSeen:         rec.lastSeen,
			Failures:         rec.failures,
			LastFailure:      rec.lastFailure,
			BlockedUntil:     rec.blockedUntil,
			ActiveChallenge:  rec.activeChallenge,
			ChallengePenalty: rec.challengePenalty,
		}
		if rec.pass != nil {
			pass := *rec.pass
			ss.Pass = &pass
		}
		doc.Sources = append(doc.Sources, ss)
	}
	for _, ch := range g.challenges {
		doc.Challenges = append(doc.Challenges, *ch)
	}
	for prefix, id := range g.activeChallenges {
		doc.ActiveChallenges[prefix] = id
	}

	sort.Slice(doc.Allow, func(i, j int) bool { return doc.Allow[i] < doc.Allow[j] })
	sort.Slice(doc.Deny, func(i, j int) bool { return doc.Deny[i] < doc.Deny[j] })
	sort.Slice(doc.Sources, func(i, j int) bool { return doc.Sources[i].Prefix < doc.Sources[j].Prefix })
	sort.Slice(doc.Challenges, func(i, j int) bool { return doc.Challenges[i].ID < doc.Challenges[j].ID })

	return json.Marshal(doc)
}

// Restore replaces the whole gate state from a snapshot.
func (g *ChallengeRateGate) Restore(data []byte) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	var doc snapshotDoc
	if err := json.Unmarshal(data, &doc); err != nil || doc.Version != snapshotVersion {
		return ErrBadSnapshot
	}
	if doc.Sequence < 0 || doc.Allowed < 0 || doc.Challenged < 0 ||
		doc.Rejected < 0 || doc.Blocked < 0 || doc.Failures < 0 || doc.Evictions < 0 {
		return ErrBadSnapshot
	}

	allowRules := make([]prefixRule, 0, len(doc.Allow))
	for _, cidr := range doc.Allow {
		rule, err := parsePrefixRule(cidr)
		if err != nil {
			return ErrBadSnapshot
		}
		allowRules = append(allowRules, rule)
	}
	denyRules := make([]prefixRule, 0, len(doc.Deny))
	for _, cidr := range doc.Deny {
		rule, err := parsePrefixRule(cidr)
		if err != nil {
			return ErrBadSnapshot
		}
		denyRules = append(denyRules, rule)
	}

	sources := make(map[string]*sourceRecord, len(doc.Sources))
	passTokens := make(map[string]struct{}, len(doc.Sources))
	for _, ss := range doc.Sources {
		prefix, ok := validStoredSourcePrefix(ss.Prefix, g.cfg.IPv6PrefixBits)
		if !ok {
			return ErrBadSnapshot
		}
		if _, exists := sources[prefix]; exists {
			return ErrBadSnapshot
		}
		if math.IsNaN(ss.Tokens) || math.IsInf(ss.Tokens, 0) ||
			ss.Tokens < 0 || ss.Tokens > g.bucketCapacity() ||
			ss.Failures < 0 || ss.ChallengePenalty < -1 {
			return ErrBadSnapshot
		}
		rec := &sourceRecord{
			prefix:           prefix,
			tokens:           ss.Tokens,
			lastRefill:       ss.LastRefill,
			lastSeen:         ss.LastSeen,
			failures:         ss.Failures,
			lastFailure:      ss.LastFailure,
			blockedUntil:     ss.BlockedUntil,
			activeChallenge:  ss.ActiveChallenge,
			challengePenalty: ss.ChallengePenalty,
		}
		if ss.Pass != nil {
			if ss.Pass.Token == "" || ss.Pass.Prefix != prefix {
				return ErrBadSnapshot
			}
			if _, exists := passTokens[ss.Pass.Token]; exists {
				return ErrBadSnapshot
			}
			passTokens[ss.Pass.Token] = struct{}{}
			pass := *ss.Pass
			rec.pass = &pass
		}
		sources[prefix] = rec
	}
	if g.cfg.MaxSources > 0 && len(sources) > g.cfg.MaxSources {
		return ErrBadSnapshot
	}

	challenges := make(map[string]*Challenge, len(doc.Challenges))
	for _, ch := range doc.Challenges {
		if ch.ID == "" || ch.Difficulty < 0 {
			return ErrBadSnapshot
		}
		prefix, ok := validStoredSourcePrefix(ch.Prefix, g.cfg.IPv6PrefixBits)
		if !ok || sources[prefix] == nil {
			return ErrBadSnapshot
		}
		if _, exists := challenges[ch.ID]; exists {
			return ErrBadSnapshot
		}
		copyCh := ch
		copyCh.Prefix = prefix
		challenges[ch.ID] = &copyCh
	}

	activeChallenges := make(map[string]string, len(doc.ActiveChallenges))
	for prefix, id := range doc.ActiveChallenges {
		canonical, ok := validStoredSourcePrefix(prefix, g.cfg.IPv6PrefixBits)
		if !ok || id == "" {
			return ErrBadSnapshot
		}
		ch := challenges[id]
		if ch == nil || ch.Prefix != canonical || ch.Used {
			return ErrBadSnapshot
		}
		rec := sources[canonical]
		if rec == nil || rec.activeChallenge != id {
			return ErrBadSnapshot
		}
		activeChallenges[canonical] = id
	}
	for _, rec := range sources {
		if rec.activeChallenge == "" {
			continue
		}
		ch := challenges[rec.activeChallenge]
		if ch == nil || ch.Prefix != rec.prefix || ch.Used {
			return ErrBadSnapshot
		}
		if activeChallenges[rec.prefix] != rec.activeChallenge {
			return ErrBadSnapshot
		}
	}

	// Validation is complete. Swap only after every part has been checked.
	g.sources = sources
	g.challenges = challenges
	g.activeChallenges = activeChallenges
	g.allowRules = allowRules
	g.denyRules = denyRules
	g.seq = doc.Sequence
	g.allowed = doc.Allowed
	g.challenged = doc.Challenged
	g.rejected = doc.Rejected
	g.blockedEvents = doc.Blocked
	g.failureEvents = doc.Failures
	g.evictionEvents = doc.Evictions
	return nil
}

func normalizeSource(source string, ipv6Bits int) (netip.Addr, string, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return netip.Addr{}, "", ErrBlankSource
	}

	addr, err := netip.ParseAddr(source)
	if err != nil {
		return netip.Addr{}, "", ErrBadSource
	}
	if addr.Is4In6() {
		addr = addr.Unmap()
	} else if addr.Zone() != "" {
		addr = addr.WithZone("")
	}

	var prefix netip.Prefix
	switch {
	case addr.Is4():
		prefix = netip.PrefixFrom(addr, 32)
	case addr.Is6():
		bits := ipv6Bits
		if bits < 0 {
			bits = 0
		}
		if bits > 128 {
			bits = 128
		}
		prefix = netip.PrefixFrom(addr, bits)
	default:
		return netip.Addr{}, "", ErrBadSource
	}
	if !prefix.IsValid() {
		return netip.Addr{}, "", ErrBadSource
	}
	return addr, sourcePrefixString(prefix.Masked()), nil
}

func parsePrefixRule(cidr string) (prefixRule, error) {
	cidr = strings.TrimSpace(cidr)
	if cidr == "" {
		return prefixRule{}, ErrBadCIDR
	}
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		return prefixRule{}, ErrBadCIDR
	}
	prefix = prefix.Masked()

	addr := prefix.Addr()
	if addr.Is4In6() {
		if prefix.Bits() < 96 {
			return prefixRule{}, ErrBadCIDR
		}
		prefix = netip.PrefixFrom(addr.Unmap(), prefix.Bits()-96).Masked()
	}
	if !prefix.IsValid() {
		return prefixRule{}, ErrBadCIDR
	}
	return prefixRule{prefix: prefix, bits: prefix.Bits()}, nil
}

func validStoredSourcePrefix(prefix string, ipv6Bits int) (string, bool) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return "", false
	}
	var p netip.Prefix
	if strings.Contains(prefix, "/") {
		parsed, err := netip.ParsePrefix(prefix)
		if err != nil {
			return "", false
		}
		p = parsed
	} else {
		addr, err := netip.ParseAddr(prefix)
		if err != nil {
			return "", false
		}
		p = netip.PrefixFrom(addr.Unmap(), addr.Unmap().BitLen())
	}
	p = p.Masked()
	canonical := sourcePrefixString(p)
	if canonical != prefix {
		return "", false
	}
	switch {
	case p.Addr().Is4():
		if p.Bits() != 32 {
			return "", false
		}
	case p.Addr().Is4In6():
		return "", false
	case p.Addr().Is6():
		bits := ipv6Bits
		if bits < 0 {
			bits = 0
		}
		if bits > 128 {
			bits = 128
		}
		if p.Bits() != bits {
			return "", false
		}
	default:
		return "", false
	}
	return canonical, true
}

func sourcePrefixString(prefix netip.Prefix) string {
	if prefix.IsValid() && prefix.Bits() == prefix.Addr().BitLen() {
		return prefix.Addr().String()
	}
	return prefix.String()
}

func (g *ChallengeRateGate) matchPolicy(addr netip.Addr) (bool, bool) {
	allowBits := -1
	for _, rule := range g.allowRules {
		if rule.prefix.Contains(addr) {
			allowBits = maxInt(allowBits, rule.bits)
		}
	}
	denyBits := -1
	for _, rule := range g.denyRules {
		if rule.prefix.Contains(addr) {
			denyBits = maxInt(denyBits, rule.bits)
		}
	}

	if denyBits >= 0 && denyBits >= allowBits {
		return false, true
	}
	return allowBits >= 0, false
}

func (g *ChallengeRateGate) ensureSource(prefix string, now time.Time) (*sourceRecord, error) {
	if rec := g.sources[prefix]; rec != nil {
		return rec, nil
	}

	if g.cfg.MaxSources > 0 && len(g.sources) >= g.cfg.MaxSources {
		victim := g.evictionCandidate(now)
		if victim == "" {
			return nil, ErrSourceLimit
		}
		g.removeSource(victim)
		g.evictionEvents++
	}

	rec := &sourceRecord{
		prefix:           prefix,
		tokens:           g.bucketCapacity(),
		lastRefill:       now,
		lastSeen:         now,
		challengePenalty: -1,
	}
	g.sources[prefix] = rec
	return rec, nil
}

func (g *ChallengeRateGate) evictionCandidate(now time.Time) string {
	candidate := ""
	var candidateSeen time.Time
	for prefix, rec := range g.sources {
		if g.isBlocked(rec, now) || hasValidPass(rec, now) {
			continue
		}
		if candidate == "" || rec.lastSeen.Before(candidateSeen) ||
			(rec.lastSeen.Equal(candidateSeen) && prefix < candidate) {
			candidate = prefix
			candidateSeen = rec.lastSeen
		}
	}
	return candidate
}

func (g *ChallengeRateGate) removeSource(prefix string) int {
	delete(g.sources, prefix)
	delete(g.activeChallenges, prefix)

	removed := 0
	for id, ch := range g.challenges {
		if ch.Prefix == prefix {
			delete(g.challenges, id)
			removed++
		}
	}
	return removed
}

func (g *ChallengeRateGate) issueOrReuseChallenge(rec *sourceRecord, now time.Time) *Challenge {
	if rec.activeChallenge != "" {
		ch, ok := g.challenges[rec.activeChallenge]
		if ok && !ch.Used && now.Before(ch.ExpiresAt) &&
			rec.challengePenalty == rec.failures {
			return ch
		}
		if g.activeChallenges[rec.prefix] == rec.activeChallenge {
			delete(g.activeChallenges, rec.prefix)
		}
		rec.activeChallenge = ""
	}

	ch := &Challenge{
		ID:         g.nextToken("challenge", rec.prefix),
		Prefix:     rec.prefix,
		Difficulty: g.currentDifficulty(rec),
		ExpiresAt:  now.Add(g.cfg.ChallengeTTL),
	}
	g.challenges[ch.ID] = ch
	g.activeChallenges[rec.prefix] = ch.ID
	rec.activeChallenge = ch.ID
	rec.challengePenalty = rec.failures
	return ch
}

func (g *ChallengeRateGate) currentDifficulty(rec *sourceRecord) int {
	difficulty := g.cfg.DifficultyBase
	if difficulty < 0 {
		difficulty = 0
	}
	if g.cfg.DifficultyMax > 0 && difficulty > g.cfg.DifficultyMax {
		difficulty = g.cfg.DifficultyMax
	}
	if rec.failures > 0 && g.cfg.DifficultyMax > 0 {
		difficulty = minInt(difficulty+rec.failures, g.cfg.DifficultyMax)
	}
	if difficulty < 0 {
		return 0
	}
	return difficulty
}

func (g *ChallengeRateGate) nextToken(kind, prefix string) string {
	g.seq++
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d|%d", kind, prefix, g.seq, g.now.UnixNano())))
	return hex.EncodeToString(sum[:])
}

func (g *ChallengeRateGate) bucketCapacity() float64 {
	if g.cfg.Burst <= 0 {
		return 0
	}
	return float64(g.cfg.Burst)
}

func (g *ChallengeRateGate) refill(rec *sourceRecord, now time.Time) {
	capacity := g.bucketCapacity()
	if rec.tokens < 0 {
		rec.tokens = 0
	}
	if rec.tokens > capacity {
		rec.tokens = capacity
	}
	if g.cfg.RefillPerSecond <= 0 {
		if rec.lastRefill.IsZero() {
			rec.lastRefill = now
		}
		return
	}
	if rec.lastRefill.IsZero() {
		rec.lastRefill = now
		return
	}
	if !now.After(rec.lastRefill) {
		return
	}
	elapsed := now.Sub(rec.lastRefill).Seconds()
	rec.tokens = math.Min(capacity, rec.tokens+elapsed*g.cfg.RefillPerSecond)
	rec.lastRefill = now
	if rec.tokens < 0 {
		rec.tokens = 0
	}
}

func (g *ChallengeRateGate) admitWithBucket(rec *sourceRecord, now time.Time) (Decision, error) {
	g.refill(rec, now)
	if rec.tokens+1e-9 < 1 {
		retryAfter := g.retryAfter(rec)
		g.rejected++
		return Decision{Action: ActionReject, RetryAfter: retryAfter}, ErrRateLimited
	}
	rec.tokens -= 1
	if rec.tokens < 0 && rec.tokens > -1e-9 {
		rec.tokens = 0
	}
	g.allowed++
	return Decision{Action: ActionAllow}, nil
}

func (g *ChallengeRateGate) retryAfter(rec *sourceRecord) time.Duration {
	deficit := 1 - rec.tokens
	if deficit <= 0 {
		return time.Nanosecond
	}
	if g.cfg.RefillPerSecond <= 0 {
		return time.Duration(math.MaxInt64)
	}
	nanos := math.Ceil(deficit / g.cfg.RefillPerSecond * float64(time.Second))
	if nanos < 1 {
		return time.Nanosecond
	}
	if nanos >= float64(math.MaxInt64) {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(nanos)
}

func (g *ChallengeRateGate) recordFailure(rec *sourceRecord, now time.Time) {
	rec.failures++
	rec.lastFailure = now
	g.failureEvents++

	if g.cfg.MaxFailures <= 0 || rec.failures < g.cfg.MaxFailures {
		return
	}
	if g.cfg.BlockDuration <= 0 {
		return
	}
	rec.blockedUntil = now.Add(g.cfg.BlockDuration)
	g.blockedEvents++
	if g.activeChallenges[rec.prefix] == rec.activeChallenge {
		delete(g.activeChallenges, rec.prefix)
	}
	rec.activeChallenge = ""
	rec.challengePenalty = -1
}

func (g *ChallengeRateGate) isBlocked(rec *sourceRecord, now time.Time) bool {
	return !rec.blockedUntil.IsZero() && now.Before(rec.blockedUntil)
}

func (g *ChallengeRateGate) decayPenalty(rec *sourceRecord, now time.Time) {
	if rec.failures <= 0 || g.cfg.PenaltyDecay <= 0 || rec.lastFailure.IsZero() {
		return
	}
	if !now.After(rec.lastFailure) {
		return
	}
	steps := int(now.Sub(rec.lastFailure) / g.cfg.PenaltyDecay)
	if steps <= 0 {
		return
	}
	if steps >= rec.failures {
		rec.failures = 0
		rec.lastFailure = time.Time{}
		return
	}
	rec.failures -= steps
	rec.lastFailure = rec.lastFailure.Add(time.Duration(steps) * g.cfg.PenaltyDecay)
}

func (g *ChallengeRateGate) idleDuration() time.Duration {
	idle := g.cfg.PenaltyDecay
	if g.cfg.ChallengeTTL > idle {
		idle = g.cfg.ChallengeTTL
	}
	if g.cfg.PassTTL > idle {
		idle = g.cfg.PassTTL
	}
	if g.cfg.BlockDuration > idle {
		idle = g.cfg.BlockDuration
	}
	if idle <= 0 {
		idle = time.Hour
	}
	return idle
}

func hasValidPass(rec *sourceRecord, now time.Time) bool {
	return rec.pass != nil && now.Before(rec.pass.ExpiresAt)
}

func meetsDifficulty(digest string, difficulty int) bool {
	if difficulty <= 0 {
		return true
	}
	if difficulty > digestHexLength {
		return false
	}
	for i := 0; i < difficulty; i++ {
		if digest[i] != '0' {
			return false
		}
	}
	return true
}

func isLowerOrUpperHex(value string) bool {
	for i := 0; i < len(value); i++ {
		c := value[i]
		if !(c >= '0' && c <= '9') &&
			!(c >= 'a' && c <= 'f') &&
			!(c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

type snapshotDoc struct {
	Version          int               `json:"version"`
	Sequence         int64             `json:"sequence"`
	Allowed          int               `json:"allowed"`
	Challenged       int               `json:"challenged"`
	Rejected         int               `json:"rejected"`
	Blocked          int               `json:"blocked"`
	Failures         int               `json:"failures"`
	Evictions        int               `json:"evictions"`
	Allow            []string          `json:"allow"`
	Deny             []string          `json:"deny"`
	Sources          []snapshotSource  `json:"sources"`
	Challenges       []Challenge       `json:"challenges"`
	ActiveChallenges map[string]string `json:"active_challenges"`
}

type snapshotSource struct {
	Prefix           string    `json:"prefix"`
	Tokens           float64   `json:"tokens"`
	LastRefill       time.Time `json:"last_refill"`
	LastSeen         time.Time `json:"last_seen"`
	Failures         int       `json:"failures"`
	LastFailure      time.Time `json:"last_failure"`
	BlockedUntil     time.Time `json:"blocked_until"`
	Pass             *Pass     `json:"pass,omitempty"`
	ActiveChallenge  string    `json:"active_challenge,omitempty"`
	ChallengePenalty int       `json:"challenge_penalty"`
}
