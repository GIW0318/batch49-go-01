// Package task evaluates outbound egress destinations, issues short lived
// grants and re-evaluates every redirect hop.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Schemes accepted on a target.
const (
	SchemeHTTP  = "http"
	SchemeHTTPS = "https"
)

// How a rule matched a host.
type MatchKind string

// Match kinds ordered from strongest to weakest.
const (
	MatchExact    MatchKind = "exact"
	MatchSuffix   MatchKind = "suffix"
	MatchCategory MatchKind = "category"
	MatchNone     MatchKind = "none"
)

// Errors returned by the gate.
var (
	ErrBlankTenant     = errors.New("tenant is required")
	ErrBadTarget       = errors.New("target is invalid")
	ErrDenied          = errors.New("egress denied by policy")
	ErrGrantExpired    = errors.New("grant has expired")
	ErrGrantRevoked    = errors.New("grant is no longer valid")
	ErrTenantRevoked   = errors.New("tenant is revoked")
	ErrHopLimit        = errors.New("redirect hop limit reached")
	ErrMethodWidened   = errors.New("redirect widens the granted method set")
	ErrSchemeDowngrade = errors.New("https to http downgrade is not allowed")
	ErrGrantCap        = errors.New("live grant cap reached")
	ErrBadRule         = errors.New("egress rule is invalid")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// Target is one destination a client wants to reach.
type Target struct {
	Scheme string
	Host   string
	Port   int
	Path   string
	Method string
}

// EgressRequest asks for a grant for one tenant and destination.
type EgressRequest struct {
	Tenant string
	Target Target
}

// Grant is a short lived authorisation bound to tenant, destination and method
// set.
type Grant struct {
	ID        string
	Tenant    string
	Scheme    string
	Host      string
	Port      int
	Methods   []string
	ExpiresAt int64
	Hops      int
	CreatedAt int64
}

// Rule is one allow list entry. Pattern is an exact host, a "*.suffix"
// wildcard, or "#category" referring to Config.Categories.
type Rule struct {
	Tenant   string
	Pattern  string
	Ports    []int
	Methods  []string
	Priority int
}

// Config holds the immutable policy set and the limits of the gate.
type Config struct {
	Rules      []Rule
	Categories map[string][]string
	TTL        int64
	MaxHops    int
	MaxGrants  int
}

const (
	snapshotVersion = 1
	maxInt64        = int64(^uint64(0) >> 1)
	minInt64        = -maxInt64 - 1
)

type snapshotChain struct {
	ID            string `json:"id"`
	RootExpiresAt int64  `json:"root_expires_at"`
}

type snapshotPayload struct {
	Clock   int64           `json:"clock"`
	NextSeq uint64          `json:"next_seq"`
	Grants  []Grant         `json:"grants"`
	Chains  []snapshotChain `json:"chains"`
	Revoked []string        `json:"revoked"`
}

type snapshotEnvelope struct {
	Version int             `json:"version"`
	Payload json.RawMessage `json:"payload"`
	Digest  string          `json:"digest"`
}

// EgressGate owns the live grants and the logical clock.
type EgressGate struct {
	mu       sync.Mutex
	cfg      Config
	rulesErr error

	grants    map[string]Grant
	chains    map[string]int64
	expiredAt map[string]int64
	revoked   map[string]bool
	clock     int64
	nextSeq   uint64
}

// NewEgressGate builds a gate from the given config.
func NewEgressGate(cfg Config) *EgressGate {
	cfg = cloneConfig(cfg)
	return &EgressGate{
		cfg:       cfg,
		rulesErr:  validateRules(cfg.Rules, cfg.Categories),
		grants:    make(map[string]Grant),
		chains:    make(map[string]int64),
		expiredAt: make(map[string]int64),
		revoked:   make(map[string]bool),
	}
}

// Authorize evaluates the request and issues a grant when a rule allows it.
func (g *EgressGate) Authorize(req EgressRequest) (Grant, error) {
	tenant := strings.TrimSpace(req.Tenant)
	if tenant == "" {
		return Grant{}, ErrBlankTenant
	}
	target, err := normalizeTarget(req.Target)
	if err != nil {
		return Grant{}, err
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if g.rulesErr != nil {
		return Grant{}, g.rulesErr
	}
	if g.revoked[tenant] {
		return Grant{}, ErrTenantRevoked
	}

	g.purgeExpiredLocked()
	rule, _, ok := g.matchRuleLocked(tenant, target)
	if !ok || !methodAllowed(rule.Methods, target.Method) {
		return Grant{}, ErrDenied
	}
	if g.liveGrantCountLocked() >= g.cfg.MaxGrants {
		return Grant{}, ErrGrantCap
	}

	expiresAt := addInt64(g.clock, g.cfg.TTL)
	grant := Grant{
		ID:        g.nextGrantIDLocked(),
		Tenant:    tenant,
		Scheme:    target.Scheme,
		Host:      target.Host,
		Port:      target.Port,
		Methods:   sortedMethods(rule.Methods),
		ExpiresAt: expiresAt,
		Hops:      0,
		CreatedAt: g.clock,
	}
	g.grants[grant.ID] = grant
	g.chains[grant.ID] = expiresAt
	delete(g.expiredAt, grant.ID)
	return cloneGrant(grant), nil
}

// FollowRedirect re-evaluates the next hop under an existing grant.
func (g *EgressGate) FollowRedirect(grant Grant, next Target) (Grant, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	grant.ID = strings.TrimSpace(grant.ID)
	if grant.ID == "" {
		return Grant{}, ErrGrantRevoked
	}
	current, ok := g.grants[grant.ID]
	if !ok {
		if grant.Tenant != "" && g.revoked[strings.TrimSpace(grant.Tenant)] {
			return Grant{}, ErrGrantRevoked
		}
		if _, expired := g.expiredAt[grant.ID]; expired {
			return Grant{}, ErrGrantExpired
		}
		return Grant{}, ErrGrantRevoked
	}
	if g.revoked[current.Tenant] {
		g.deleteGrantLocked(current.ID)
		return Grant{}, ErrGrantRevoked
	}
	if !g.grantLiveLocked(current) {
		g.expireGrantLocked(current.ID)
		return Grant{}, ErrGrantExpired
	}
	if current.Hops >= g.cfg.MaxHops {
		return Grant{}, ErrHopLimit
	}

	next, err := normalizeTarget(next)
	if err != nil {
		return Grant{}, err
	}
	if current.Scheme == SchemeHTTPS && next.Scheme == SchemeHTTP {
		return Grant{}, ErrSchemeDowngrade
	}
	if !methodAllowed(current.Methods, next.Method) {
		return Grant{}, ErrMethodWidened
	}

	rule, _, ok := g.matchRuleLocked(current.Tenant, next)
	if !ok || !methodAllowed(rule.Methods, next.Method) {
		return Grant{}, ErrDenied
	}
	methods := intersectMethods(current.Methods, rule.Methods)
	if len(methods) == 0 && len(current.Methods) != 0 && len(rule.Methods) != 0 {
		return Grant{}, ErrDenied
	}

	rootExpiresAt, hasRoot := g.chains[current.ID]
	if !hasRoot {
		rootExpiresAt = current.ExpiresAt
	}
	nextGrant := Grant{
		ID:        g.nextGrantIDLocked(),
		Tenant:    current.Tenant,
		Scheme:    next.Scheme,
		Host:      next.Host,
		Port:      next.Port,
		Methods:   methods,
		ExpiresAt: addInt64(g.clock, g.cfg.TTL),
		Hops:      current.Hops + 1,
		CreatedAt: g.clock,
	}

	g.deleteGrantLocked(current.ID)
	g.grants[nextGrant.ID] = nextGrant
	g.chains[nextGrant.ID] = rootExpiresAt
	delete(g.expiredAt, nextGrant.ID)
	return cloneGrant(nextGrant), nil
}

// Revoke invalidates every live grant of the tenant and returns how many were
// dropped.
func (g *EgressGate) Revoke(tenant string) int {
	tenant = strings.TrimSpace(tenant)
	if tenant == "" {
		return 0
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	count := 0
	for id, grant := range g.grants {
		if grant.Tenant != tenant {
			continue
		}
		if g.grantLiveLocked(grant) {
			count++
		}
		g.deleteGrantLocked(id)
	}
	g.revoked[tenant] = true
	return count
}

// Snapshot serialises grants, revoked tenants and the logical clock.
func (g *EgressGate) Snapshot() ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.purgeExpiredLocked()

	ids := make([]string, 0, len(g.grants))
	for id, grant := range g.grants {
		if g.grantLiveLocked(grant) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)

	grants := make([]Grant, 0, len(ids))
	chains := make([]snapshotChain, 0, len(ids))
	for _, id := range ids {
		grant := cloneGrant(g.grants[id])
		grants = append(grants, grant)
		root, ok := g.chains[id]
		if !ok {
			root = grant.ExpiresAt
		}
		chains = append(chains, snapshotChain{ID: id, RootExpiresAt: root})
	}

	revoked := make([]string, 0, len(g.revoked))
	for tenant := range g.revoked {
		revoked = append(revoked, tenant)
	}
	sort.Strings(revoked)

	payload := snapshotPayload{
		Clock:   g.clock,
		NextSeq: g.nextSeq,
		Grants:  grants,
		Chains:  chains,
		Revoked: revoked,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	envelope := snapshotEnvelope{
		Version: snapshotVersion,
		Payload: raw,
		Digest:  hex.EncodeToString(sum[:]),
	}
	return json.Marshal(envelope)
}

// Restore replaces the live state from a snapshot.
func (g *EgressGate) Restore(data []byte) error {
	var envelope snapshotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return ErrBadSnapshot
	}
	if envelope.Version != snapshotVersion || len(envelope.Payload) == 0 {
		return ErrBadSnapshot
	}
	sum := sha256.Sum256(envelope.Payload)
	if envelope.Digest != hex.EncodeToString(sum[:]) {
		return ErrBadSnapshot
	}

	var payload snapshotPayload
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return ErrBadSnapshot
	}
	if payload.Clock < 0 {
		return ErrBadSnapshot
	}

	grants, chains, revoked, err := validateSnapshot(payload)
	if err != nil {
		return ErrBadSnapshot
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	g.grants = grants
	g.chains = chains
	g.revoked = revoked
	g.expiredAt = make(map[string]int64)
	g.clock = payload.Clock
	g.nextSeq = payload.NextSeq
	for id := range grants {
		if seq, ok := grantSequence(id); ok && seq > g.nextSeq {
			g.nextSeq = seq
		}
	}
	return nil
}

// MatchRule returns the strongest rule of the tenant matching host and port.
func (g *EgressGate) MatchRule(tenant string, target Target) (Rule, MatchKind, bool) {
	tenant = strings.TrimSpace(tenant)
	host := normalizeHost(target.Host)
	if tenant == "" || !validHost(host) || !validPort(target.Port) {
		return Rule{}, MatchNone, false
	}
	return g.matchRuleLocked(tenant, Target{Host: host, Port: target.Port})
}

// Active lists the live grants of one tenant ordered by grant id.
func (g *EgressGate) Active(tenant string) []Grant {
	tenant = strings.TrimSpace(tenant)

	g.mu.Lock()
	defer g.mu.Unlock()

	ids := make([]string, 0, len(g.grants))
	for id, grant := range g.grants {
		if grant.Tenant == tenant && g.grantLiveLocked(grant) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)

	grants := make([]Grant, 0, len(ids))
	for _, id := range ids {
		grants = append(grants, cloneGrant(g.grants[id]))
	}
	return grants
}

// Grant reads one live grant by id.
func (g *EgressGate) Grant(id string) (Grant, bool) {
	id = strings.TrimSpace(id)

	g.mu.Lock()
	defer g.mu.Unlock()

	grant, ok := g.grants[id]
	if !ok || !g.grantLiveLocked(grant) {
		return Grant{}, false
	}
	return cloneGrant(grant), true
}

// ValidateRules checks the configured rules before any authorisation happens.
func (g *EgressGate) ValidateRules() error {
	return g.rulesErr
}

// Advance moves the logical clock forward by the given milliseconds.
func (g *EgressGate) Advance(ms int64) {
	if ms <= 0 {
		return
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	g.clock = addInt64(g.clock, ms)
}

// Clock reports the current logical time in milliseconds.
func (g *EgressGate) Clock() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.clock
}

func (g *EgressGate) matchRuleLocked(tenant string, target Target) (Rule, MatchKind, bool) {
	host := normalizeHost(target.Host)
	var (
		selected Rule
		kind     MatchKind
		priority int
		found    bool
	)

	for _, rule := range g.cfg.Rules {
		if strings.TrimSpace(rule.Tenant) != tenant || !rulePortAllowed(rule, target.Port) {
			continue
		}
		candidate, ok := matchHost(g.cfg.Categories, rule.Pattern, host)
		if !ok {
			continue
		}
		if !found || matchRank(candidate) < matchRank(kind) ||
			(candidate == kind && rule.Priority > priority) {
			selected = rule
			kind = candidate
			priority = rule.Priority
			found = true
		}
	}
	if !found {
		return Rule{}, MatchNone, false
	}
	return cloneRule(selected), kind, true
}

func (g *EgressGate) purgeExpiredLocked() {
	ids := make([]string, 0, len(g.grants))
	for id := range g.grants {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		grant := g.grants[id]
		if !g.grantLiveLocked(grant) {
			g.expireGrantLocked(id)
		}
	}
}

func (g *EgressGate) liveGrantCountLocked() int {
	count := 0
	for _, grant := range g.grants {
		if g.grantLiveLocked(grant) {
			count++
		}
	}
	return count
}

func (g *EgressGate) grantLiveLocked(grant Grant) bool {
	if g.revoked[grant.Tenant] || grant.ExpiresAt <= g.clock {
		return false
	}
	if root, ok := g.chains[grant.ID]; ok && root <= g.clock {
		return false
	}
	return true
}

func (g *EgressGate) expireGrantLocked(id string) {
	grant, ok := g.grants[id]
	if !ok {
		return
	}
	expiresAt := grant.ExpiresAt
	if root, hasRoot := g.chains[id]; hasRoot && root <= g.clock {
		expiresAt = root
	}
	delete(g.grants, id)
	delete(g.chains, id)
	g.expiredAt[id] = expiresAt
}

func (g *EgressGate) deleteGrantLocked(id string) {
	delete(g.grants, id)
	delete(g.chains, id)
}

func (g *EgressGate) nextGrantIDLocked() string {
	g.nextSeq++
	return fmt.Sprintf("g-%016x", g.nextSeq)
}

func cloneConfig(cfg Config) Config {
	out := Config{
		TTL:       cfg.TTL,
		MaxHops:   cfg.MaxHops,
		MaxGrants: cfg.MaxGrants,
		Rules:     make([]Rule, len(cfg.Rules)),
	}
	for i, rule := range cfg.Rules {
		out.Rules[i] = cloneRule(rule)
	}
	if cfg.Categories != nil {
		out.Categories = make(map[string][]string, len(cfg.Categories))
		for name, members := range cfg.Categories {
			copied := make([]string, len(members))
			copy(copied, members)
			sort.Strings(copied)
			out.Categories[name] = copied
		}
	}
	return out
}

func cloneRule(rule Rule) Rule {
	out := rule
	if rule.Ports != nil {
		out.Ports = append([]int(nil), rule.Ports...)
	}
	if rule.Methods != nil {
		out.Methods = append([]string(nil), rule.Methods...)
	}
	return out
}

func cloneGrant(grant Grant) Grant {
	out := grant
	if grant.Methods != nil {
		out.Methods = append([]string(nil), grant.Methods...)
	}
	return out
}

func validateRules(rules []Rule, categories map[string][]string) error {
	for _, rule := range rules {
		tenant := strings.TrimSpace(rule.Tenant)
		if tenant == "" {
			return ErrBadRule
		}
		pattern := strings.TrimSpace(rule.Pattern)
		if pattern == "" {
			return ErrBadRule
		}
		if strings.HasPrefix(pattern, "#") {
			name := strings.TrimSpace(pattern[1:])
			if name == "" {
				return ErrBadRule
			}
			members, ok := categories[name]
			if !ok {
				return ErrBadRule
			}
			for _, member := range members {
				host := normalizeHost(member)
				if !validHost(host) || strings.Contains(host, "*") || strings.HasPrefix(host, ".") {
					return ErrBadRule
				}
			}
		} else if strings.HasPrefix(pattern, "*.") {
			suffix := normalizeHost(pattern[2:])
			if !validHost(suffix) || strings.Contains(suffix, "*") || strings.HasPrefix(suffix, ".") {
				return ErrBadRule
			}
		} else {
			host := normalizeHost(pattern)
			if strings.Contains(host, "*") || !validHost(host) || strings.HasPrefix(host, ".") {
				return ErrBadRule
			}
		}
		for _, port := range rule.Ports {
			if !validPort(port) {
				return ErrBadRule
			}
		}
		for _, method := range rule.Methods {
			if strings.TrimSpace(method) == "" {
				return ErrBadRule
			}
		}
	}
	return nil
}

func validateSnapshot(payload snapshotPayload) (map[string]Grant, map[string]int64, map[string]bool, error) {
	grants := make(map[string]Grant, len(payload.Grants))
	for _, grant := range payload.Grants {
		if grant.ID == "" || strings.TrimSpace(grant.Tenant) == "" || grant.Tenant != strings.TrimSpace(grant.Tenant) {
			return nil, nil, nil, ErrBadSnapshot
		}
		if _, exists := grants[grant.ID]; exists {
			return nil, nil, nil, ErrBadSnapshot
		}
		if grant.Scheme != SchemeHTTP && grant.Scheme != SchemeHTTPS {
			return nil, nil, nil, ErrBadSnapshot
		}
		if grant.Host != normalizeHost(grant.Host) || !validHost(grant.Host) ||
			strings.Contains(grant.Host, "*") {
			return nil, nil, nil, ErrBadSnapshot
		}
		if !validPort(grant.Port) || grant.ExpiresAt <= payload.Clock || grant.CreatedAt < 0 || grant.Hops < 0 {
			return nil, nil, nil, ErrBadSnapshot
		}
		if !validStoredMethods(grant.Methods) {
			return nil, nil, nil, ErrBadSnapshot
		}
		grants[grant.ID] = cloneGrant(grant)
	}

	revoked := make(map[string]bool, len(payload.Revoked))
	for _, tenant := range payload.Revoked {
		tenant = strings.TrimSpace(tenant)
		if tenant == "" {
			return nil, nil, nil, ErrBadSnapshot
		}
		if revoked[tenant] {
			return nil, nil, nil, ErrBadSnapshot
		}
		revoked[tenant] = true
	}
	for _, grant := range grants {
		if revoked[grant.Tenant] {
			return nil, nil, nil, ErrBadSnapshot
		}
	}

	chains := make(map[string]int64, len(payload.Chains))
	for _, chain := range payload.Chains {
		if _, ok := grants[chain.ID]; !ok || chain.RootExpiresAt <= payload.Clock {
			return nil, nil, nil, ErrBadSnapshot
		}
		if _, exists := chains[chain.ID]; exists {
			return nil, nil, nil, ErrBadSnapshot
		}
		chains[chain.ID] = chain.RootExpiresAt
	}
	for id, grant := range grants {
		if _, ok := chains[id]; !ok {
			chains[id] = grant.ExpiresAt
		}
	}
	return grants, chains, revoked, nil
}

func matchHost(categories map[string][]string, pattern, host string) (MatchKind, bool) {
	if strings.Contains(host, "*") {
		return MatchNone, false
	}
	pattern = strings.TrimSpace(pattern)
	switch {
	case strings.HasPrefix(pattern, "*."):
		suffix := normalizeHost(pattern[2:])
		return MatchSuffix, strings.HasSuffix(host, "."+suffix)
	case strings.HasPrefix(pattern, "#"):
		name := strings.TrimSpace(pattern[1:])
		for _, member := range categories[name] {
			member = normalizeHost(member)
			if host == member || strings.HasSuffix(host, "."+member) {
				return MatchCategory, true
			}
		}
		return MatchNone, false
	default:
		return MatchExact, host == normalizeHost(pattern)
	}
}

func matchRank(kind MatchKind) int {
	switch kind {
	case MatchExact:
		return 0
	case MatchSuffix:
		return 1
	case MatchCategory:
		return 2
	default:
		return 3
	}
}

func normalizeTarget(target Target) (Target, error) {
	target.Scheme = strings.ToLower(strings.TrimSpace(target.Scheme))
	if target.Scheme != SchemeHTTP && target.Scheme != SchemeHTTPS {
		return Target{}, ErrBadTarget
	}
	target.Host = normalizeHost(target.Host)
	if !validHost(target.Host) {
		return Target{}, ErrBadTarget
	}
	if !validPort(target.Port) {
		return Target{}, ErrBadTarget
	}
	target.Method = strings.TrimSpace(target.Method)
	if target.Method == "" {
		return Target{}, ErrBadTarget
	}
	return target, nil
}

func normalizeHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	return strings.TrimSuffix(host, ".")
}

func validHost(host string) bool {
	if host == "" || strings.HasPrefix(host, ".") {
		return false
	}
	return !strings.ContainsAny(host, " \t\r\n/\\?#@")
}

func validPort(port int) bool {
	return port >= 1 && port <= 65535
}

func rulePortAllowed(rule Rule, port int) bool {
	if len(rule.Ports) == 0 {
		return true
	}
	for _, allowed := range rule.Ports {
		if allowed == port {
			return true
		}
	}
	return false
}

func methodAllowed(methods []string, method string) bool {
	if len(methods) == 0 {
		return true
	}
	method = strings.TrimSpace(method)
	for _, allowed := range methods {
		if strings.TrimSpace(allowed) == method {
			return true
		}
	}
	return false
}

func sortedMethods(methods []string) []string {
	seen := make(map[string]struct{}, len(methods))
	out := make([]string, 0, len(methods))
	for _, method := range methods {
		method = strings.TrimSpace(method)
		if method == "" {
			continue
		}
		if _, ok := seen[method]; ok {
			continue
		}
		seen[method] = struct{}{}
		out = append(out, method)
	}
	sort.Strings(out)
	return out
}

func intersectMethods(left, right []string) []string {
	if len(left) == 0 {
		return sortedMethods(right)
	}
	if len(right) == 0 {
		return sortedMethods(left)
	}
	allowed := make(map[string]struct{}, len(left))
	for _, method := range sortedMethods(left) {
		allowed[method] = struct{}{}
	}
	out := make([]string, 0, len(right))
	for _, method := range sortedMethods(right) {
		if _, ok := allowed[method]; ok {
			out = append(out, method)
		}
	}
	return out
}

func validStoredMethods(methods []string) bool {
	canonical := sortedMethods(methods)
	if len(canonical) != len(methods) {
		return false
	}
	for i := range methods {
		if methods[i] != canonical[i] {
			return false
		}
	}
	return true
}

func addInt64(base, delta int64) int64 {
	if delta > 0 && base > maxInt64-delta {
		return maxInt64
	}
	if delta < 0 && base < minInt64-delta {
		return minInt64
	}
	return base + delta
}

func grantSequence(id string) (uint64, bool) {
	if !strings.HasPrefix(id, "g-") {
		return 0, false
	}
	seq, err := strconv.ParseUint(strings.TrimPrefix(id, "g-"), 16, 64)
	if err != nil {
		return 0, false
	}
	return seq, true
}
