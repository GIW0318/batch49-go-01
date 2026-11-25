// Package task manages the lifecycle of tenant level SSO connections:
// drafting, domain proof, connectivity test, enablement and rollouts.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// Connection lifecycle states.
const (
	StateDraft         = "draft"
	StateVerifyPending = "verify_pending"
	StateTested        = "tested"
	StateEnabled       = "enabled"
	StateDisabled      = "disabled"
)

const snapshotVersion = 1

// Errors returned by the service.
var (
	ErrBlankTenant       = errors.New("tenant id is required")
	ErrBlankDomain       = errors.New("domain is required")
	ErrDomainLimit       = errors.New("domain limit reached for this connection")
	ErrTenantLimit       = errors.New("connection limit reached for this tenant")
	ErrUnknownConnection = errors.New("unknown sso connection")
	ErrUnknownDomain     = errors.New("domain is not attached to this connection")
	ErrDomainTaken       = errors.New("domain is already attached to another connection")
	ErrBadProof          = errors.New("domain verification proof is invalid")
	ErrDomainUnverified  = errors.New("domain is not verified")
	ErrBadState          = errors.New("connection state does not allow this action")
	ErrBadRollout        = errors.New("rollout percent must be between 0 and 100")
	ErrBreakGlassDenied  = errors.New("break glass login denied")
	ErrBadSnapshot       = errors.New("snapshot payload is invalid")
)

// SsoLimits bounds one service instance.
type SsoLimits struct {
	MaxConnectionsPerTenant int
	MaxDomainsPerConnection int
	MaxBreakGlassPerDay     int
}

// ConnectionConfig is the submitted SSO configuration.
type ConnectionConfig struct {
	DisplayName string
	Domains     []string
	Issuer      string
	ClientID    string
}

// Connection is one tenant SSO connection.
type Connection struct {
	ID              string    `json:"id"`
	TenantID        string    `json:"tenant_id"`
	DisplayName     string    `json:"display_name"`
	State           string    `json:"state"`
	Domains         []string  `json:"domains"`
	VerifiedDomains []string  `json:"verified_domains"`
	RolloutPercent  int       `json:"rollout_percent"`
	Enforced        bool      `json:"enforced"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// TestResult is the outcome of a connectivity test.
type TestResult struct {
	OK       bool     `json:"ok"`
	Checked  int      `json:"checked"`
	Warnings []string `json:"warnings"`
}

// Alert is a retained operational warning.
type Alert struct {
	At       time.Time `json:"at"`
	TenantID string    `json:"tenant_id"`
	Kind     string    `json:"kind"`
	Message  string    `json:"message"`
}

type domainOwner struct {
	TenantID string `json:"tenant_id"`
	ConnID   string `json:"conn_id"`
}

type connRecord struct {
	Connection
	Seq        uint64    `json:"seq"`
	Issuer     string    `json:"issuer,omitempty"`
	ClientID   string    `json:"client_id,omitempty"`
	Tested     bool      `json:"tested"`
	RolloutSet bool      `json:"rollout_set"`
	CreatedAt  time.Time `json:"created_at"`
	TestedAt   time.Time `json:"tested_at,omitempty"`
}

type snapshotData struct {
	Version        int                    `json:"version"`
	Seq            uint64                 `json:"seq"`
	Connections    []connRecord           `json:"connections"`
	Enforced       map[string]bool        `json:"enforced"`
	BreakGlassUses map[string][]time.Time `json:"break_glass_uses,omitempty"`
	Alerts         []Alert                `json:"alerts,omitempty"`
}

// TenantSsoConnection owns every tenant connection.
type TenantSsoConnection struct {
	limits SsoLimits

	mu             sync.RWMutex
	seq            uint64
	conns          map[string]map[string]*connRecord
	domains        map[string]domainOwner
	enforced       map[string]bool
	breakGlassUses map[string][]time.Time
	alerts         []Alert
}

// NewTenantSsoConnection builds a service with the given limits.
func NewTenantSsoConnection(limits SsoLimits) *TenantSsoConnection {
	return &TenantSsoConnection{
		limits:         limits,
		conns:          make(map[string]map[string]*connRecord),
		domains:        make(map[string]domainOwner),
		enforced:       make(map[string]bool),
		breakGlassUses: make(map[string][]time.Time),
	}
}

// Create registers a new draft connection for a tenant.
func (t *TenantSsoConnection) Create(tenantID string, cfg ConnectionConfig) (Connection, error) {
	if isBlank(tenantID) {
		return Connection{}, ErrBlankTenant
	}
	if len(cfg.Domains) == 0 {
		return Connection{}, ErrBlankDomain
	}
	for _, domain := range cfg.Domains {
		if isBlank(domain) {
			return Connection{}, ErrBlankDomain
		}
	}
	if len(cfg.Domains) > t.limits.MaxDomainsPerConnection {
		return Connection{}, fmt.Errorf("%w: configured %d, limit %d",
			ErrDomainLimit, len(cfg.Domains), t.limits.MaxDomainsPerConnection)
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	tenantConns := t.conns[tenantID]
	if len(tenantConns) >= t.limits.MaxConnectionsPerTenant {
		return Connection{}, fmt.Errorf("%w: tenant %s has %d, limit %d",
			ErrTenantLimit, tenantID, len(tenantConns), t.limits.MaxConnectionsPerTenant)
	}
	for _, domain := range cfg.Domains {
		key := domainKey(domain)
		if owner, taken := t.domains[key]; taken {
			return Connection{}, fmt.Errorf("%w: %s is used by %s/%s",
				ErrDomainTaken, domain, owner.TenantID, owner.ConnID)
		}
	}

	t.seq++
	now := time.Now().UTC()
	record := &connRecord{
		Connection: Connection{
			ID:              fmt.Sprintf("conn-%d", t.seq),
			TenantID:        tenantID,
			DisplayName:     cfg.DisplayName,
			State:           StateDraft,
			Domains:         cloneStrings(cfg.Domains),
			VerifiedDomains: []string{},
			RolloutPercent:  0,
			Enforced:        t.enforced[tenantID],
			UpdatedAt:       now,
		},
		Seq:       t.seq,
		Issuer:    cfg.Issuer,
		ClientID:  cfg.ClientID,
		CreatedAt: now,
	}

	if tenantConns == nil {
		tenantConns = make(map[string]*connRecord)
		t.conns[tenantID] = tenantConns
	}
	tenantConns[record.ID] = record
	for _, domain := range record.Domains {
		t.domains[domainKey(domain)] = domainOwner{TenantID: tenantID, ConnID: record.ID}
	}

	return t.dtoLocked(record), nil
}

// VerifyDomain proves ownership of one domain of the connection.
func (t *TenantSsoConnection) VerifyDomain(tenantID, connID, domain, proof string) error {
	if isBlank(tenantID) {
		return ErrBlankTenant
	}
	if isBlank(proof) {
		return ErrBadProof
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	record, err := t.recordLocked(tenantID, connID)
	if err != nil {
		return err
	}

	key := domainKey(domain)
	configuredDomain := ""
	for _, candidate := range record.Domains {
		if domainKey(candidate) == key {
			configuredDomain = candidate
			break
		}
	}
	if configuredDomain == "" {
		return fmt.Errorf("%w: %s is not configured on %s", ErrUnknownDomain, domain, connID)
	}

	for _, verified := range record.VerifiedDomains {
		if domainKey(verified) == key {
			return nil
		}
	}

	record.VerifiedDomains = append(record.VerifiedDomains, configuredDomain)
	touchUpdatedAt(record)
	if record.State == StateDraft {
		record.State = StateVerifyPending
	}
	return nil
}

// Test runs the connectivity test for a verified connection.
func (t *TenantSsoConnection) Test(tenantID, connID string) (TestResult, error) {
	if isBlank(tenantID) {
		return TestResult{}, ErrBlankTenant
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	record, err := t.recordLocked(tenantID, connID)
	if err != nil {
		return TestResult{}, err
	}
	if !allDomainsVerified(record) {
		return TestResult{}, fmt.Errorf("%w: %s has unverified domains", ErrDomainUnverified, connID)
	}

	result := evaluateConnection(record)
	now := touchUpdatedAt(record)
	record.Tested = true
	record.TestedAt = now
	if record.State == StateDraft || record.State == StateVerifyPending {
		record.State = StateTested
	}
	return result, nil
}

// Enable turns a tested connection on.
func (t *TenantSsoConnection) Enable(tenantID, connID string) error {
	if isBlank(tenantID) {
		return ErrBlankTenant
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	record, err := t.recordLocked(tenantID, connID)
	if err != nil {
		return err
	}
	if record.State == StateEnabled {
		return nil
	}
	if !allDomainsVerified(record) {
		return fmt.Errorf("%w: %s", ErrDomainUnverified, connID)
	}
	if !record.Tested {
		return fmt.Errorf("%w: %s has not passed connectivity test", ErrBadState, connID)
	}
	if record.State != StateTested && record.State != StateDisabled {
		return fmt.Errorf("%w: cannot enable %s from %s", ErrBadState, connID, record.State)
	}

	record.State = StateEnabled
	touchUpdatedAt(record)
	return nil
}

// Disable turns a connection off without dropping its configuration.
func (t *TenantSsoConnection) Disable(tenantID, connID string) error {
	if isBlank(tenantID) {
		return ErrBlankTenant
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	record, err := t.recordLocked(tenantID, connID)
	if err != nil {
		return err
	}
	if record.State == StateDisabled {
		return nil
	}
	if record.State != StateEnabled {
		return fmt.Errorf("%w: cannot disable %s from %s", ErrBadState, connID, record.State)
	}

	record.State = StateDisabled
	touchUpdatedAt(record)
	return nil
}

// Rollout sets the rollout percentage of a connection.
func (t *TenantSsoConnection) Rollout(tenantID, connID string, percent int) error {
	if isBlank(tenantID) {
		return ErrBlankTenant
	}
	if percent < 0 || percent > 100 {
		return fmt.Errorf("%w: got %d", ErrBadRollout, percent)
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	record, err := t.recordLocked(tenantID, connID)
	if err != nil {
		return err
	}
	record.RolloutPercent = percent
	record.RolloutSet = true
	touchUpdatedAt(record)
	return nil
}

// Snapshot serialises every connection of every tenant.
func (t *TenantSsoConnection) Snapshot() ([]byte, error) {
	t.mu.RLock()
	snapshot := snapshotData{
		Version:        snapshotVersion,
		Seq:            t.seq,
		Connections:    make([]connRecord, 0),
		Enforced:       make(map[string]bool, len(t.enforced)),
		BreakGlassUses: make(map[string][]time.Time, len(t.breakGlassUses)),
		Alerts:         append([]Alert(nil), t.alerts...),
	}
	for tenantID, on := range t.enforced {
		snapshot.Enforced[tenantID] = on
	}
	for tenantID, uses := range t.breakGlassUses {
		snapshot.BreakGlassUses[tenantID] = append([]time.Time(nil), uses...)
	}
	for _, tenantConns := range t.conns {
		for _, record := range tenantConns {
			copyRecord := *record
			copyRecord.Domains = cloneStrings(record.Domains)
			copyRecord.VerifiedDomains = cloneStrings(record.VerifiedDomains)
			snapshot.Connections = append(snapshot.Connections, copyRecord)
		}
	}
	t.mu.RUnlock()

	sort.Slice(snapshot.Connections, func(i, j int) bool {
		left, right := snapshot.Connections[i], snapshot.Connections[j]
		if left.TenantID != right.TenantID {
			return left.TenantID < right.TenantID
		}
		return left.Seq < right.Seq
	})

	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode snapshot: %w", err)
	}
	return data, nil
}

// Restore replaces the service state from a snapshot. A bad snapshot never
// mutates the live service.
func (t *TenantSsoConnection) Restore(data []byte) error {
	next, err := decodeSnapshot(data)
	if err != nil {
		return err
	}

	t.mu.Lock()
	t.seq = next.seq
	t.conns = next.conns
	t.domains = next.domains
	t.enforced = next.enforced
	t.breakGlassUses = next.breakGlassUses
	t.alerts = next.alerts
	t.mu.Unlock()
	return nil
}

// Get returns one connection by tenant and identifier.
func (t *TenantSsoConnection) Get(tenantID, connID string) (Connection, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	record, err := t.recordLocked(tenantID, connID)
	if err != nil {
		return Connection{}, false
	}
	return t.dtoLocked(record), true
}

// RouteLogin picks the connection that should serve a tenant login. A canary
// miss falls back to the tenant's previous enabled connection when one exists.
func (t *TenantSsoConnection) RouteLogin(tenantID, email string) (Connection, error) {
	if isBlank(tenantID) {
		return Connection{}, ErrBlankTenant
	}
	emailDomain, err := emailDomainKey(email)
	if err != nil {
		return Connection{}, err
	}

	t.mu.RLock()
	defer t.mu.RUnlock()

	candidate := t.routeCandidateLocked(tenantID, emailDomain)
	if candidate == nil {
		return Connection{}, fmt.Errorf("%w: no enabled connection for %s", ErrUnknownDomain, emailDomain)
	}
	if rolloutHits(candidate, tenantID, email) {
		return t.dtoLocked(candidate), nil
	}
	if fallback := t.fallbackLocked(tenantID, candidate.ID, emailDomain); fallback != nil {
		return t.dtoLocked(fallback), nil
	}

	// With no older connection available, the only configured enabled
	// connection remains the safe fallback instead of failing the login.
	return t.dtoLocked(candidate), nil
}

// SetEnforceSSO turns mandatory SSO on or off for a tenant.
func (t *TenantSsoConnection) SetEnforceSSO(tenantID string, on bool) error {
	if isBlank(tenantID) {
		return ErrBlankTenant
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.enforced == nil {
		t.enforced = make(map[string]bool)
	}
	t.enforced[tenantID] = on
	for _, record := range t.conns[tenantID] {
		record.Enforced = on
	}
	return nil
}

// BreakGlass authenticates the emergency local administrator. The simulated
// directory has no separate credential store, so a non-blank secret proves
// that the entry point remains reachable. Every accepted use raises an alert.
func (t *TenantSsoConnection) BreakGlass(tenantID, password string) (bool, error) {
	if isBlank(tenantID) {
		return false, ErrBlankTenant
	}
	if isBlank(password) {
		return false, ErrBreakGlassDenied
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.breakGlassUses == nil {
		t.breakGlassUses = make(map[string][]time.Time)
	}

	now := time.Now().UTC()
	uses := t.breakGlassUses[tenantID]
	todayUses := uses[:0]
	for _, usedAt := range uses {
		if sameUTCDay(usedAt, now) {
			todayUses = append(todayUses, usedAt)
		}
	}
	if t.limits.MaxBreakGlassPerDay >= 0 && len(todayUses) >= t.limits.MaxBreakGlassPerDay {
		t.alerts = append(t.alerts, Alert{
			At:       now,
			TenantID: tenantID,
			Kind:     "break_glass_denied",
			Message:  "daily break glass quota exhausted",
		})
		return false, fmt.Errorf("%w: daily limit %d",
			ErrBreakGlassDenied, t.limits.MaxBreakGlassPerDay)
	}

	todayUses = append(todayUses, now)
	t.breakGlassUses[tenantID] = todayUses
	t.alerts = append(t.alerts, Alert{
		At:       now,
		TenantID: tenantID,
		Kind:     "break_glass_used",
		Message:  "emergency break glass login used",
	})
	return true, nil
}

// Alerts returns retained operational alerts for one tenant, oldest first.
func (t *TenantSsoConnection) Alerts(tenantID string) []Alert {
	t.mu.RLock()
	defer t.mu.RUnlock()

	var result []Alert
	for _, alert := range t.alerts {
		if alert.TenantID == tenantID {
			result = append(result, alert)
		}
	}
	return result
}

func (t *TenantSsoConnection) recordLocked(tenantID, connID string) (*connRecord, error) {
	if isBlank(tenantID) {
		return nil, ErrBlankTenant
	}
	tenantConns, ok := t.conns[tenantID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownConnection, connID)
	}
	record, ok := tenantConns[connID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownConnection, connID)
	}
	return record, nil
}

func (t *TenantSsoConnection) dtoLocked(record *connRecord) Connection {
	dto := record.Connection
	dto.Domains = cloneStrings(record.Domains)
	dto.VerifiedDomains = cloneStrings(record.VerifiedDomains)
	dto.Enforced = t.enforced[record.TenantID]
	return dto
}

func (t *TenantSsoConnection) routeCandidateLocked(tenantID, emailDomain string) *connRecord {
	var candidate *connRecord
	for _, record := range t.conns[tenantID] {
		if record.State != StateEnabled {
			continue
		}
		if verifiedDomainMatches(record, emailDomain) {
			return record
		}
	}
	return candidate
}

func (t *TenantSsoConnection) fallbackLocked(tenantID, candidateID, emailDomain string) *connRecord {
	var exactFallback, oldestFallback *connRecord
	for _, record := range t.conns[tenantID] {
		if record.ID == candidateID || record.State != StateEnabled {
			continue
		}
		if oldestFallback == nil || record.Seq < oldestFallback.Seq {
			oldestFallback = record
		}
		if exactFallback == nil && verifiedDomainMatches(record, emailDomain) {
			exactFallback = record
		}
	}
	if exactFallback != nil {
		return exactFallback
	}
	return oldestFallback
}

func decodeSnapshot(data []byte) (*TenantSsoConnection, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: empty payload", ErrBadSnapshot)
	}

	var snapshot snapshotData
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, fmt.Errorf("%w: decode: %v", ErrBadSnapshot, err)
	}
	if snapshot.Version != snapshotVersion {
		return nil, fmt.Errorf("%w: unsupported version %d", ErrBadSnapshot, snapshot.Version)
	}

	service := NewTenantSsoConnection(SsoLimits{})
	service.seq = snapshot.Seq
	if snapshot.Enforced != nil {
		service.enforced = snapshot.Enforced
	}
	if snapshot.BreakGlassUses != nil {
		service.breakGlassUses = snapshot.BreakGlassUses
	}
	service.alerts = append([]Alert(nil), snapshot.Alerts...)

	seenIDs := make(map[string]struct{}, len(snapshot.Connections))
	for index := range snapshot.Connections {
		record := snapshot.Connections[index]
		if err := validateSnapshotRecord(&record, seenIDs); err != nil {
			return nil, err
		}
		if record.Seq > service.seq {
			service.seq = record.Seq
		}

		tenantConns := service.conns[record.TenantID]
		if tenantConns == nil {
			tenantConns = make(map[string]*connRecord)
			service.conns[record.TenantID] = tenantConns
		}
		tenantConns[record.ID] = &record

		for _, domain := range record.Domains {
			key := domainKey(domain)
			owner := domainOwner{TenantID: record.TenantID, ConnID: record.ID}
			if existing, ok := service.domains[key]; ok && existing.ConnID != record.ID {
				return nil, fmt.Errorf("%w: duplicate domain %s", ErrBadSnapshot, key)
			}
			service.domains[key] = owner
		}
	}

	return service, nil
}

func validateSnapshotRecord(record *connRecord, seenIDs map[string]struct{}) error {
	if isBlank(record.ID) || isBlank(record.TenantID) {
		return fmt.Errorf("%w: connection identity is incomplete", ErrBadSnapshot)
	}
	if _, seen := seenIDs[record.ID]; seen {
		return fmt.Errorf("%w: duplicate connection id %s", ErrBadSnapshot, record.ID)
	}
	seenIDs[record.ID] = struct{}{}

	switch record.State {
	case StateDraft, StateVerifyPending, StateTested, StateEnabled, StateDisabled:
	default:
		return fmt.Errorf("%w: unknown state %q", ErrBadSnapshot, record.State)
	}
	if len(record.Domains) == 0 {
		return fmt.Errorf("%w: %s has no domains", ErrBadSnapshot, record.ID)
	}
	for _, domain := range record.Domains {
		if isBlank(domain) {
			return fmt.Errorf("%w: %s has a blank domain", ErrBadSnapshot, record.ID)
		}
	}
	if record.RolloutPercent < 0 || record.RolloutPercent > 100 {
		return fmt.Errorf("%w: %s has rollout %d", ErrBadSnapshot, record.ID, record.RolloutPercent)
	}

	for _, domain := range record.VerifiedDomains {
		if isBlank(domain) {
			return fmt.Errorf("%w: %s has a blank verified domain", ErrBadSnapshot, record.ID)
		}
		key := domainKey(domain)
		if !connectionHasDomain(record, key) {
			return fmt.Errorf("%w: verified domain %s is not configured", ErrBadSnapshot, key)
		}
	}

	switch record.State {
	case StateTested, StateEnabled:
		if !record.Tested || record.TestedAt.IsZero() {
			return fmt.Errorf("%w: %s is %s without a passed test", ErrBadSnapshot, record.ID, record.State)
		}
		if !allDomainsVerified(record) {
			return fmt.Errorf("%w: %s is %s with unverified domains", ErrBadSnapshot, record.ID, record.State)
		}
	case StateDisabled:
		if !record.Tested || record.TestedAt.IsZero() {
			return fmt.Errorf("%w: disabled %s is missing its test record", ErrBadSnapshot, record.ID)
		}
	}
	return nil
}

func evaluateConnection(record *connRecord) TestResult {
	warnings := []string{}
	checked := 0

	checked++
	if isBlank(record.DisplayName) {
		warnings = append(warnings, "display name is not set")
	}

	checked++
	switch {
	case isBlank(record.Issuer):
		warnings = append(warnings, "issuer is not configured")
	default:
		issuerURL, err := url.Parse(record.Issuer)
		if err != nil || issuerURL.Scheme == "" || issuerURL.Host == "" {
			warnings = append(warnings, "issuer is not a valid absolute URL")
		} else if issuerURL.Scheme != "https" {
			warnings = append(warnings, "issuer should use https")
		}
	}

	checked++
	if isBlank(record.ClientID) {
		warnings = append(warnings, "client id is not configured")
	}

	for range uniqueDomainKeys(record.Domains) {
		checked++
	}

	return TestResult{OK: true, Checked: checked, Warnings: warnings}
}

func rolloutHits(record *connRecord, tenantID, email string) bool {
	percent := 100
	if record.RolloutSet {
		percent = record.RolloutPercent
	}
	if percent >= 100 {
		return true
	}
	if percent <= 0 {
		return false
	}

	hash := fnv.New32a()
	_, _ = hash.Write([]byte(strings.ToLower(strings.TrimSpace(tenantID))))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(strings.ToLower(strings.TrimSpace(email))))
	return int(hash.Sum32()%100) < percent
}

func emailDomainKey(email string) (string, error) {
	at := strings.LastIndex(email, "@")
	if at <= 0 || at == len(email)-1 {
		return "", fmt.Errorf("%w: invalid login email %q", ErrUnknownDomain, email)
	}
	key := domainKey(email[at+1:])
	if key == "" {
		return "", fmt.Errorf("%w: invalid login email %q", ErrUnknownDomain, email)
	}
	return key, nil
}

func verifiedDomainMatches(record *connRecord, emailDomain string) bool {
	for _, verified := range record.VerifiedDomains {
		if domainKey(verified) == emailDomain {
			return true
		}
	}
	return false
}

func allDomainsVerified(record *connRecord) bool {
	configured := uniqueDomainKeys(record.Domains)
	if len(configured) == 0 {
		return false
	}
	verified := make(map[string]struct{}, len(record.VerifiedDomains))
	for _, domain := range record.VerifiedDomains {
		verified[domainKey(domain)] = struct{}{}
	}
	for domain := range configured {
		if _, ok := verified[domain]; !ok {
			return false
		}
	}
	return true
}

func uniqueDomainKeys(domains []string) map[string]struct{} {
	result := make(map[string]struct{}, len(domains))
	for _, domain := range domains {
		result[domainKey(domain)] = struct{}{}
	}
	return result
}

func connectionHasDomain(record *connRecord, key string) bool {
	for _, domain := range record.Domains {
		if domainKey(domain) == key {
			return true
		}
	}
	return false
}

func sameUTCDay(left, right time.Time) bool {
	leftUTC := left.UTC()
	rightUTC := right.UTC()
	return leftUTC.Year() == rightUTC.Year() &&
		leftUTC.Month() == rightUTC.Month() &&
		leftUTC.Day() == rightUTC.Day()
}

func touchUpdatedAt(record *connRecord) time.Time {
	now := time.Now().UTC()
	if !now.After(record.UpdatedAt) {
		now = record.UpdatedAt.Add(time.Nanosecond)
	}
	record.UpdatedAt = now
	return now
}

func isBlank(value string) bool {
	return strings.TrimSpace(value) == ""
}

func domainKey(domain string) string {
	return strings.ToLower(strings.TrimSpace(domain))
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string(nil), values...)
}
