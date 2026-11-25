// Package task routes a login email address to the tenant SSO connection that
// owns the email domain, with claims, verification and releases.
package task

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

// Domain claim states.
const (
	ClaimPending  = "claim_pending"
	ClaimActive   = "claim_active"
	ClaimDisputed = "claim_disputed"
	ClaimReleased = "claim_released"
)

// Errors returned by the router.
var (
	ErrBlankTenant  = errors.New("tenant id is required")
	ErrBlankDomain  = errors.New("domain is required")
	ErrBadEmail     = errors.New("email address is malformed")
	ErrUnknownClaim = errors.New("unknown domain claim")
	ErrBadProof     = errors.New("domain proof is invalid")
	ErrNoClaim      = errors.New("no claim for this tenant and domain")
	ErrDomainLimit  = errors.New("domain route table is full")
	ErrClaimLimit   = errors.New("claim limit reached for this tenant")
	ErrBadSnapshot  = errors.New("snapshot payload is invalid")
)

// DomainRouterLimits bounds one router instance. A non-positive limit means
// that the corresponding dimension is unbounded.
type DomainRouterLimits struct {
	MaxDomains         int
	MaxClaimsPerTenant int
	CacheTTL           time.Duration
}

// Route is the outcome of routing one email address.
type Route struct {
	Email         string
	Domain        string
	MatchedDomain string
	TenantID      string
	ClaimID       string
	Default       bool
	Cached        bool
}

// Claim is one tenant claim over one domain.
type Claim struct {
	ID         string
	TenantID   string
	Domain     string
	State      string
	Proof      string
	CreatedAt  time.Time
	VerifiedAt time.Time
}

type claimRecord struct {
	claim     Claim
	domainKey string
}

type cacheEntry struct {
	route     Route
	expiresAt time.Time
}

// SsoDomainRouter owns the global domain ownership table.
type SsoDomainRouter struct {
	mu sync.Mutex

	limits DomainRouterLimits
	claims []*claimRecord
	byID   map[string]*claimRecord
	// domains indexes every claim, including released history. Routing and
	// capacity ignore released records.
	domains map[string][]*claimRecord
	active  map[string]*claimRecord
	cache   map[string]cacheEntry
}

// NewSsoDomainRouter builds a router with the given limits.
func NewSsoDomainRouter(limits DomainRouterLimits) *SsoDomainRouter {
	return &SsoDomainRouter{
		limits:  limits,
		byID:    make(map[string]*claimRecord),
		domains: make(map[string][]*claimRecord),
		active:  make(map[string]*claimRecord),
		cache:   make(map[string]cacheEntry),
	}
}

// Route resolves an email address to a tenant connection.
func (r *SsoDomainRouter) Route(email string) (Route, error) {
	domainKey, err := emailDomain(email)
	if err != nil {
		return Route{}, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	if entry, ok := r.cache[domainKey]; ok {
		if now.Before(entry.expiresAt) {
			route := entry.route
			route.Email = email
			route.Cached = true
			return route, nil
		}
		delete(r.cache, domainKey)
	}

	route := Route{Email: email, Domain: domainKey, Default: true}
	if record := r.active[domainKey]; record != nil {
		route.Default = false
		route.MatchedDomain = domainKey
		route.TenantID = record.claim.TenantID
		route.ClaimID = record.claim.ID
	}

	if r.limits.CacheTTL > 0 {
		r.cache[domainKey] = cacheEntry{
			route:     route,
			expiresAt: now.Add(r.limits.CacheTTL),
		}
	}
	return route, nil
}

// Claim records a tenant claim over a domain.
func (r *SsoDomainRouter) Claim(tenantID, domain string) (Claim, error) {
	if strings.TrimSpace(tenantID) == "" {
		return Claim{}, ErrBlankTenant
	}
	if strings.TrimSpace(domain) == "" {
		return Claim{}, ErrBlankDomain
	}

	domainKey := canonicalDomain(domain)
	r.mu.Lock()
	defer r.mu.Unlock()

	// Repeating the same live claim is idempotent.
	if record := r.findTenantDomain(tenantID, domainKey); record != nil {
		return record.claim, nil
	}

	if limit := r.limits.MaxClaimsPerTenant; limit > 0 && r.tenantClaimCount(tenantID) >= limit {
		return Claim{}, ErrClaimLimit
	}
	if limit := r.limits.MaxDomains; limit > 0 && !r.domainOccupied(domainKey) && r.occupiedDomains() >= limit {
		return Claim{}, ErrDomainLimit
	}

	state := ClaimPending
	if r.active[domainKey] != nil {
		// Ownership is first-verifier-wins. A later claim never displaces an
		// already active tenant, it is frozen for audit/dispute handling.
		state = ClaimDisputed
	}

	record := &claimRecord{
		claim: Claim{
			ID:        r.newClaimID(),
			TenantID:  tenantID,
			Domain:    domain,
			State:     state,
			CreatedAt: time.Now().UTC(),
		},
		domainKey: domainKey,
	}
	r.claims = append(r.claims, record)
	r.byID[record.claim.ID] = record
	r.domains[domainKey] = append(r.domains[domainKey], record)
	r.invalidateLocked(domainKey)

	return record.claim, nil
}

// Verify activates a pending claim after proof of ownership.
func (r *SsoDomainRouter) Verify(claimID, proof string) error {
	if strings.TrimSpace(proof) == "" {
		return ErrBadProof
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	record := r.byID[claimID]
	if record == nil || record.claim.State == ClaimReleased {
		return ErrUnknownClaim
	}

	now := time.Now().UTC()
	switch record.claim.State {
	case ClaimActive:
		// Verification is idempotent for the winning claim.
		return nil
	case ClaimDisputed:
		if owner := r.active[record.domainKey]; owner != nil && owner != record {
			// The dispute is still owned by the first verifier.
			return nil
		}
	case ClaimPending:
		if owner := r.active[record.domainKey]; owner != nil && owner != record {
			record.claim.State = ClaimDisputed
			record.claim.Proof = proof
			record.claim.VerifiedAt = now
			r.invalidateLocked(record.domainKey)
			return nil
		}
	default:
		return ErrUnknownClaim
	}

	record.claim.State = ClaimActive
	record.claim.Proof = proof
	record.claim.VerifiedAt = now
	r.active[record.domainKey] = record

	for _, other := range r.domains[record.domainKey] {
		if other == record || other.claim.State == ClaimReleased {
			continue
		}
		if other.claim.State == ClaimActive || other.claim.State == ClaimPending {
			other.claim.State = ClaimDisputed
		}
	}
	r.invalidateLocked(record.domainKey)
	return nil
}

// Release drops the claim a tenant holds over a domain.
func (r *SsoDomainRouter) Release(tenantID, domain string) error {
	if strings.TrimSpace(tenantID) == "" {
		return ErrBlankTenant
	}
	if strings.TrimSpace(domain) == "" {
		return ErrBlankDomain
	}

	domainKey := canonicalDomain(domain)
	r.mu.Lock()
	defer r.mu.Unlock()

	released := false
	for _, record := range r.domains[domainKey] {
		if record.claim.TenantID != tenantID || record.claim.State == ClaimReleased {
			continue
		}
		record.claim.State = ClaimReleased
		released = true
	}
	if !released {
		return ErrNoClaim
	}

	if owner := r.active[domainKey]; owner != nil && owner.claim.State == ClaimReleased {
		delete(r.active, domainKey)
	}
	r.invalidateLocked(domainKey)
	return nil
}

// Invalidate drops any cached route for a domain.
func (r *SsoDomainRouter) Invalidate(domain string) error {
	if strings.TrimSpace(domain) == "" {
		return ErrBlankDomain
	}
	domainKey := canonicalDomain(domain)

	r.mu.Lock()
	defer r.mu.Unlock()
	r.invalidateLocked(domainKey)
	return nil
}

// Snapshot serialises the domain ownership table.
func (r *SsoDomainRouter) Snapshot() ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	claims := make([]Claim, 0, len(r.claims))
	for _, record := range r.claims {
		claims = append(claims, record.claim)
	}
	payload := snapshotPayload{Version: snapshotVersion, Claims: claims}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	envelope := snapshotEnvelope{
		Version:  snapshotVersion,
		Claims:   claims,
		Checksum: hex.EncodeToString(sum[:]),
	}
	return json.Marshal(envelope)
}

// Restore replaces the router state from a snapshot.
func (r *SsoDomainRouter) Restore(data []byte) error {
	if len(data) == 0 {
		return ErrBadSnapshot
	}

	var envelope snapshotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return ErrBadSnapshot
	}
	if envelope.Version != snapshotVersion || envelope.Checksum == "" {
		return ErrBadSnapshot
	}

	payload := snapshotPayload{Version: envelope.Version, Claims: envelope.Claims}
	body, err := json.Marshal(payload)
	if err != nil {
		return ErrBadSnapshot
	}
	sum := sha256.Sum256(body)
	if !strings.EqualFold(envelope.Checksum, hex.EncodeToString(sum[:])) {
		return ErrBadSnapshot
	}

	claims, byID, domains, active, err := rebuildState(envelope.Claims)
	if err != nil {
		return ErrBadSnapshot
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.claims = claims
	r.byID = byID
	r.domains = domains
	r.active = active
	r.cache = make(map[string]cacheEntry)
	return nil
}

// Lookup returns the active route of an exact domain.
func (r *SsoDomainRouter) Lookup(domain string) (Route, bool) {
	if strings.TrimSpace(domain) == "" {
		return Route{}, false
	}
	domainKey := canonicalDomain(domain)

	r.mu.Lock()
	defer r.mu.Unlock()
	record := r.active[domainKey]
	if record == nil {
		return Route{}, false
	}
	return Route{
		Domain:        domainKey,
		MatchedDomain: domainKey,
		TenantID:      record.claim.TenantID,
		ClaimID:       record.claim.ID,
	}, true
}

// Claims lists the claims of one tenant.
func (r *SsoDomainRouter) Claims(tenantID string) []Claim {
	r.mu.Lock()
	defer r.mu.Unlock()

	var claims []Claim
	for _, record := range r.claims {
		if record.claim.TenantID == tenantID {
			claims = append(claims, record.claim)
		}
	}
	return claims
}

// Disputes lists frozen claims.
func (r *SsoDomainRouter) Disputes() []Claim {
	r.mu.Lock()
	defer r.mu.Unlock()

	var claims []Claim
	for _, record := range r.claims {
		if record.claim.State == ClaimDisputed {
			claims = append(claims, record.claim)
		}
	}
	return claims
}

// Capacity returns current occupancy and the hard limit of the route table.
func (r *SsoDomainRouter) Capacity() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.occupiedDomains(), r.limits.MaxDomains
}

func (r *SsoDomainRouter) findTenantDomain(tenantID, domainKey string) *claimRecord {
	for _, record := range r.domains[domainKey] {
		if record.claim.TenantID == tenantID && record.claim.State != ClaimReleased {
			return record
		}
	}
	return nil
}

func (r *SsoDomainRouter) tenantClaimCount(tenantID string) int {
	count := 0
	for _, record := range r.claims {
		if record.claim.TenantID == tenantID && record.claim.State != ClaimReleased {
			count++
		}
	}
	return count
}

func (r *SsoDomainRouter) domainOccupied(domainKey string) bool {
	for _, record := range r.domains[domainKey] {
		if record.claim.State != ClaimReleased {
			return true
		}
	}
	return false
}

func (r *SsoDomainRouter) occupiedDomains() int {
	count := 0
	for domainKey := range r.domains {
		if r.domainOccupied(domainKey) {
			count++
		}
	}
	return count
}

func (r *SsoDomainRouter) invalidateLocked(domainKey string) {
	delete(r.cache, domainKey)
}

func (r *SsoDomainRouter) newClaimID() string {
	for {
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err == nil {
			id := "claim-" + hex.EncodeToString(raw[:])
			if _, exists := r.byID[id]; !exists {
				return id
			}
			continue
		}

		// crypto/rand is expected to work; this fallback still keeps IDs
		// unique within the process.
		for {
			id := "claim-" + hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
			if _, exists := r.byID[id]; !exists {
				return id
			}
		}
	}
}

func canonicalDomain(domain string) string {
	return strings.ToLower(strings.TrimSpace(domain))
}

func emailDomain(email string) (string, error) {
	trimmed := strings.TrimSpace(email)
	if strings.Count(trimmed, "@") != 1 {
		return "", ErrBadEmail
	}
	parts := strings.SplitN(trimmed, "@", 2)
	local := strings.TrimSpace(parts[0])
	domain := strings.TrimSpace(parts[1])
	if local == "" || domain == "" {
		return "", ErrBadEmail
	}
	return canonicalDomain(domain), nil
}

const snapshotVersion = 1

type snapshotPayload struct {
	Version int     `json:"version"`
	Claims  []Claim `json:"claims"`
}

type snapshotEnvelope struct {
	Version  int     `json:"version"`
	Claims   []Claim `json:"claims"`
	Checksum string  `json:"checksum"`
}

func rebuildState(claims []Claim) ([]*claimRecord, map[string]*claimRecord, map[string][]*claimRecord, map[string]*claimRecord, error) {
	records := make([]*claimRecord, 0, len(claims))
	byID := make(map[string]*claimRecord, len(claims))
	domains := make(map[string][]*claimRecord)
	active := make(map[string]*claimRecord)
	liveTenantDomains := make(map[string]bool)

	for _, claim := range claims {
		if strings.TrimSpace(claim.ID) == "" ||
			strings.TrimSpace(claim.TenantID) == "" ||
			strings.TrimSpace(claim.Domain) == "" ||
			claim.CreatedAt.IsZero() {
			return nil, nil, nil, nil, ErrBadSnapshot
		}
		switch claim.State {
		case ClaimPending, ClaimActive, ClaimDisputed, ClaimReleased:
		default:
			return nil, nil, nil, nil, ErrBadSnapshot
		}
		if _, duplicate := byID[claim.ID]; duplicate {
			return nil, nil, nil, nil, ErrBadSnapshot
		}

		domainKey := canonicalDomain(claim.Domain)
		record := &claimRecord{claim: claim, domainKey: domainKey}
		records = append(records, record)
		byID[claim.ID] = record
		domains[domainKey] = append(domains[domainKey], record)

		if claim.State == ClaimReleased {
			continue
		}
		liveKey := claim.TenantID + "\x00" + domainKey
		if liveTenantDomains[liveKey] {
			return nil, nil, nil, nil, ErrBadSnapshot
		}
		liveTenantDomains[liveKey] = true

		if claim.State == ClaimActive {
			if claim.VerifiedAt.IsZero() {
				return nil, nil, nil, nil, ErrBadSnapshot
			}
			if _, duplicate := active[domainKey]; duplicate {
				return nil, nil, nil, nil, ErrBadSnapshot
			}
			active[domainKey] = record
		}
	}
	return records, byID, domains, active, nil
}
