// Package task grades WebAuthn registration attempts against the per tenant
// authenticator attestation policy before a credential reaches the ledger.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// Attestation conveyance modes understood by the policy engine.
const (
	ConveyanceNone       = "none"
	ConveyanceIndirect   = "indirect"
	ConveyanceEnterprise = "enterprise"
)

// Authenticator strength tiers.
const (
	StrengthLow    = "low"
	StrengthMedium = "medium"
	StrengthHigh   = "high"
)

// Reason codes attached to a decision.
const (
	ReasonAllowlisted          = "reason_allowlisted"
	ReasonDenylisted           = "reason_denylisted"
	ReasonNotAllowlisted       = "reason_not_allowlisted"
	ReasonConveyanceNone       = "reason_conveyance_none"
	ReasonUntrustedChain       = "reason_untrusted_chain"
	ReasonAttestationRequired  = "reason_attestation_required"
	ReasonUserNotVerified      = "reason_user_not_verified"
	ReasonStrengthBelowMinimum = "reason_strength_below_minimum"
	ReasonPreviouslyRejected   = "reason_previously_rejected"
)

// Errors returned by the policy engine.
var (
	ErrBlankTenant       = errors.New("tenant id is required")
	ErrBadConveyance     = errors.New("unknown attestation conveyance")
	ErrBadStrength       = errors.New("unknown strength tier")
	ErrUnknownTenant     = errors.New("tenant has no policy")
	ErrPolicyLimit       = errors.New("policy version limit reached for tenant")
	ErrAAGUIDLimit       = errors.New("aaguid list limit reached")
	ErrTrustAnchorLimit  = errors.New("trust anchor limit reached")
	ErrBlankRegistration = errors.New("registration id is required")
	ErrBadSnapshot       = errors.New("snapshot payload is invalid")
)

const snapshotFormatV1 = "authenticator-attestation-policy-v1"

// Limits bounds the policy tables.
type Limits struct {
	MaxEntriesPerList int `json:"max_entries_per_list"`
	MaxTrustAnchors   int `json:"max_trust_anchors"`
	MaxVersions       int `json:"max_versions"`
}

// Policy is the attestation policy configured for one tenant.
type Policy struct {
	TenantID           string   `json:"tenant_id"`
	Conveyance         string   `json:"conveyance"`
	AllowAAGUIDs       []string `json:"allow_aaguids"`
	DenyAAGUIDs        []string `json:"deny_aaguids"`
	TrustAnchors       []string `json:"trust_anchors"`
	RequireAttestation bool     `json:"require_attestation"`
	MinStrength        string   `json:"min_strength"`
}

// Version describes one stored policy revision.
type Version struct {
	TenantID   string `json:"tenant_id"`
	Generation int    `json:"generation"`
	Allow      int    `json:"allow"`
	Deny       int    `json:"deny"`
	Anchors    int    `json:"anchors"`
}

// Registration is one WebAuthn registration presented by a client.
type Registration struct {
	ID           string   `json:"id"`
	AAGUID       string   `json:"aaguid"`
	Conveyance   string   `json:"conveyance"`
	Chain        []string `json:"chain"`
	Attested     bool     `json:"attested"`
	UserVerified bool     `json:"user_verified"`
}

// Decision is the graded outcome for one registration.
type Decision struct {
	ID             string   `json:"id"`
	TenantID       string   `json:"tenant_id"`
	RegistrationID string   `json:"registration_id"`
	Accepted       bool     `json:"accepted"`
	Strength       string   `json:"strength"`
	Generation     int      `json:"generation"`
	Codes          []string `json:"codes"`
	Seq            int64    `json:"seq"`
}

// Reason is one explanation line attached to a decision.
type Reason struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

type versionRecord struct {
	Version Version `json:"version"`
	Policy  Policy  `json:"policy"`
}

type credentialRecord struct {
	Registration Registration `json:"registration"`
	Strength     string       `json:"strength"`
	Accepted     bool         `json:"accepted"`
	Generation   int          `json:"generation"`
}

type evaluationResult struct {
	Accepted bool
	Strength string
	Codes    []string
	Reasons  []Reason
}

type cacheEntry struct {
	Fingerprint string           `json:"fingerprint"`
	Generation  int              `json:"generation"`
	Result      evaluationResult `json:"result"`
}

type tenantState struct {
	TenantID    string                       `json:"tenant_id"`
	Versions    []versionRecord              `json:"versions"`
	Seq         int64                        `json:"seq"`
	Credentials map[string]*credentialRecord `json:"credentials"`
	Cache       map[string]*cacheEntry       `json:"-"`
}

type decisionRecord struct {
	Decision Decision `json:"decision"`
	Reasons  []Reason `json:"reasons"`
}

type snapshotState struct {
	Limits          Limits                     `json:"limits"`
	DecisionCounter uint64                     `json:"decision_counter"`
	Tenants         map[string]*tenantState    `json:"tenants"`
	Decisions       map[string]*decisionRecord `json:"decisions"`
}

type snapshotEnvelope struct {
	Format   string          `json:"format"`
	Checksum string          `json:"checksum"`
	State    json.RawMessage `json:"state"`
}

// AuthenticatorAttestationPolicy owns the policies, their versions and the
// evaluation cache.
type AuthenticatorAttestationPolicy struct {
	mu              sync.RWMutex
	limits          Limits
	tenants         map[string]*tenantState
	decisions       map[string]*decisionRecord
	decisionCounter uint64
}

// NewAuthenticatorAttestationPolicy builds a policy engine with the given caps.
func NewAuthenticatorAttestationPolicy(limits Limits) *AuthenticatorAttestationPolicy {
	return &AuthenticatorAttestationPolicy{
		limits:    limits,
		tenants:   make(map[string]*tenantState),
		decisions: make(map[string]*decisionRecord),
	}
}

// SetPolicy installs a new policy revision and returns the stored version.
func (e *AuthenticatorAttestationPolicy) SetPolicy(tenantID string, p Policy) (Version, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return Version{}, ErrBlankTenant
	}
	if !validConveyance(p.Conveyance) {
		return Version{}, ErrBadConveyance
	}
	if !validStrength(p.MinStrength) {
		return Version{}, ErrBadStrength
	}

	p = clonePolicy(p)
	p.TenantID = tenantID

	e.mu.Lock()
	defer e.mu.Unlock()

	if len(p.AllowAAGUIDs)+len(p.DenyAAGUIDs) > e.limits.MaxEntriesPerList {
		return Version{}, ErrAAGUIDLimit
	}
	if len(p.TrustAnchors) > e.limits.MaxTrustAnchors {
		return Version{}, ErrTrustAnchorLimit
	}

	state, exists := e.tenants[tenantID]
	if !exists {
		state = &tenantState{
			TenantID:    tenantID,
			Versions:    make([]versionRecord, 0, 1),
			Credentials: make(map[string]*credentialRecord),
			Cache:       make(map[string]*cacheEntry),
		}
	}
	if len(state.Versions) >= e.limits.MaxVersions {
		return Version{}, ErrPolicyLimit
	}

	generation := len(state.Versions) + 1
	version := Version{
		TenantID:   tenantID,
		Generation: generation,
		Allow:      len(p.AllowAAGUIDs),
		Deny:       len(p.DenyAAGUIDs),
		Anchors:    len(p.TrustAnchors),
	}
	state.Versions = append(state.Versions, versionRecord{Version: version, Policy: p})
	state.Cache = make(map[string]*cacheEntry)
	if !exists {
		e.tenants[tenantID] = state
	}
	return version, nil
}

// Policy reads the current policy of one tenant.
func (e *AuthenticatorAttestationPolicy) Policy(tenantID string) (Policy, error) {
	tenantID = strings.TrimSpace(tenantID)
	e.mu.RLock()
	defer e.mu.RUnlock()

	state, ok := e.tenants[tenantID]
	if !ok || len(state.Versions) == 0 {
		return Policy{}, ErrUnknownTenant
	}
	return clonePolicy(state.Versions[len(state.Versions)-1].Policy), nil
}

// Versions lists the stored policy revisions of one tenant, oldest first.
func (e *AuthenticatorAttestationPolicy) Versions(tenantID string) []Version {
	tenantID = strings.TrimSpace(tenantID)
	e.mu.RLock()
	defer e.mu.RUnlock()

	state, ok := e.tenants[tenantID]
	if !ok {
		return []Version{}
	}
	versions := make([]Version, 0, len(state.Versions))
	for _, record := range state.Versions {
		versions = append(versions, record.Version)
	}
	return versions
}

// Evaluate grades one registration against the tenant policy.
func (e *AuthenticatorAttestationPolicy) Evaluate(tenantID string, reg Registration) (Decision, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return Decision{}, ErrUnknownTenant
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	state, ok := e.tenants[tenantID]
	if !ok || len(state.Versions) == 0 {
		return Decision{}, ErrUnknownTenant
	}
	if strings.TrimSpace(reg.ID) == "" {
		return Decision{}, ErrBlankRegistration
	}

	policyRecord := state.Versions[len(state.Versions)-1]
	policy := policyRecord.Policy
	cacheKey := strings.TrimSpace(reg.ID)
	fingerprint := registrationFingerprint(reg)

	var result evaluationResult
	if cached, hit := state.Cache[cacheKey]; hit &&
		cached.Generation == policyRecord.Version.Generation &&
		cached.Fingerprint == fingerprint {
		result = cached.Result.clone()
	} else {
		result = assessRegistration(policy, reg)
		if credential, ok := state.Credentials[cacheKey]; ok {
			result.Strength = weakerStrength(result.Strength, credential.Strength)
			enforceMinimumStrength(&result, policy.MinStrength)
		}
		state.Cache[cacheKey] = &cacheEntry{
			Fingerprint: fingerprint,
			Generation:  policyRecord.Version.Generation,
			Result:      result.clone(),
		}
	}

	e.decisionCounter++
	state.Seq++
	decision := Decision{
		ID:             fmt.Sprintf("decision-%d", e.decisionCounter),
		TenantID:       tenantID,
		RegistrationID: reg.ID,
		Accepted:       result.Accepted,
		Strength:       result.Strength,
		Generation:     policyRecord.Version.Generation,
		Codes:          append([]string(nil), result.Codes...),
		Seq:            state.Seq,
	}
	e.decisions[decision.ID] = &decisionRecord{
		Decision: cloneDecision(decision),
		Reasons:  append([]Reason(nil), result.Reasons...),
	}

	if decision.Accepted {
		credential, ok := state.Credentials[cacheKey]
		if !ok {
			credential = &credentialRecord{}
			state.Credentials[cacheKey] = credential
		}
		credential.Registration = cloneRegistration(reg)
		credential.Strength = decision.Strength
		credential.Accepted = true
		credential.Generation = decision.Generation
	} else if credential, ok := state.Credentials[cacheKey]; ok {
		credential.Accepted = false
		credential.Strength = result.Strength
		credential.Generation = decision.Generation
	}

	return decision, nil
}

// Decision reads one cached decision by id.
func (e *AuthenticatorAttestationPolicy) Decision(decisionID string) (Decision, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	record, ok := e.decisions[decisionID]
	if !ok {
		return Decision{}, false
	}
	return cloneDecision(record.Decision), true
}

// Explain returns the ordered reasons behind one cached decision.
func (e *AuthenticatorAttestationPolicy) Explain(decisionID string) []Reason {
	e.mu.RLock()
	defer e.mu.RUnlock()

	record, ok := e.decisions[decisionID]
	if !ok {
		return nil
	}
	return append([]Reason(nil), record.Reasons...)
}

// Reevaluate re-grades the cached registrations of one tenant and reports how
// many credentials were revisited.
func (e *AuthenticatorAttestationPolicy) Reevaluate(tenantID string) (int, error) {
	tenantID = strings.TrimSpace(tenantID)
	e.mu.Lock()
	defer e.mu.Unlock()

	state, ok := e.tenants[tenantID]
	if !ok || len(state.Versions) == 0 {
		return 0, ErrUnknownTenant
	}

	policyRecord := state.Versions[len(state.Versions)-1]
	policy := policyRecord.Policy
	count := 0
	for key, credential := range state.Credentials {
		if credential == nil {
			continue
		}
		count++
		wasAccepted := credential.Accepted
		result := assessRegistration(policy, credential.Registration)
		result.Strength = weakerStrength(result.Strength, credential.Strength)
		if result.Accepted && !wasAccepted {
			result.Accepted = false
			result.addReason(ReasonPreviouslyRejected)
		}
		enforceMinimumStrength(&result, policy.MinStrength)

		credential.Strength = result.Strength
		credential.Accepted = result.Accepted
		credential.Generation = policyRecord.Version.Generation
		state.Cache[key] = &cacheEntry{
			Fingerprint: registrationFingerprint(credential.Registration),
			Generation:  policyRecord.Version.Generation,
			Result:      result.clone(),
		}
	}
	return count, nil
}

// Snapshot serialises the policies, the versions and the decision cache.
func (e *AuthenticatorAttestationPolicy) Snapshot() ([]byte, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	state := snapshotState{
		Limits:          e.limits,
		DecisionCounter: e.decisionCounter,
		Tenants:         e.tenants,
		Decisions:       e.decisions,
	}
	stateBytes, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("marshal policy snapshot: %w", err)
	}
	checksum := sha256.Sum256(stateBytes)
	envelope := snapshotEnvelope{
		Format:   snapshotFormatV1,
		Checksum: hex.EncodeToString(checksum[:]),
		State:    stateBytes,
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("marshal snapshot envelope: %w", err)
	}
	return data, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (e *AuthenticatorAttestationPolicy) Restore(data []byte) error {
	var envelope snapshotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return fmt.Errorf("%w: decode snapshot envelope: %v", ErrBadSnapshot, err)
	}
	if envelope.Format != snapshotFormatV1 {
		return fmt.Errorf("%w: unsupported snapshot format", ErrBadSnapshot)
	}
	if len(envelope.State) == 0 || strings.TrimSpace(envelope.Checksum) == "" {
		return fmt.Errorf("%w: missing snapshot state", ErrBadSnapshot)
	}

	checksum := sha256.Sum256(envelope.State)
	if !strings.EqualFold(hex.EncodeToString(checksum[:]), strings.TrimSpace(envelope.Checksum)) {
		return fmt.Errorf("%w: snapshot checksum mismatch", ErrBadSnapshot)
	}

	var state snapshotState
	if err := json.Unmarshal(envelope.State, &state); err != nil {
		return fmt.Errorf("%w: decode snapshot state: %v", ErrBadSnapshot, err)
	}
	if err := validateSnapshotState(&state); err != nil {
		return err
	}

	if state.Tenants == nil {
		state.Tenants = make(map[string]*tenantState)
	}
	if state.Decisions == nil {
		state.Decisions = make(map[string]*decisionRecord)
	}
	for _, tenant := range state.Tenants {
		if tenant == nil {
			continue
		}
		tenant.Cache = make(map[string]*cacheEntry)
		if tenant.Credentials == nil {
			tenant.Credentials = make(map[string]*credentialRecord)
		}
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	e.limits = state.Limits
	e.tenants = state.Tenants
	e.decisions = state.Decisions
	e.decisionCounter = state.DecisionCounter
	return nil
}

func validConveyance(conveyance string) bool {
	switch conveyance {
	case ConveyanceNone, ConveyanceIndirect, ConveyanceEnterprise:
		return true
	default:
		return false
	}
}

func validStrength(strength string) bool {
	return strengthRank(strength) > 0
}

func strengthRank(strength string) int {
	switch strength {
	case StrengthLow:
		return 1
	case StrengthMedium:
		return 2
	case StrengthHigh:
		return 3
	default:
		return 0
	}
}

func strengthForRank(rank int) string {
	switch {
	case rank <= 1:
		return StrengthLow
	case rank == 2:
		return StrengthMedium
	default:
		return StrengthHigh
	}
}

func weakerStrength(left, right string) string {
	leftRank := strengthRank(left)
	rightRank := strengthRank(right)
	if leftRank == 0 {
		return right
	}
	if rightRank == 0 || leftRank < rightRank {
		return left
	}
	return right
}

func conveyanceRank(conveyance string) int {
	switch conveyance {
	case ConveyanceNone:
		return 1
	case ConveyanceIndirect:
		return 2
	case ConveyanceEnterprise:
		return 3
	default:
		return 1
	}
}

func clonePolicy(p Policy) Policy {
	p.AllowAAGUIDs = append([]string(nil), p.AllowAAGUIDs...)
	p.DenyAAGUIDs = append([]string(nil), p.DenyAAGUIDs...)
	p.TrustAnchors = append([]string(nil), p.TrustAnchors...)
	return p
}

func cloneRegistration(reg Registration) Registration {
	reg.Chain = append([]string(nil), reg.Chain...)
	return reg
}

func cloneDecision(decision Decision) Decision {
	decision.Codes = append([]string(nil), decision.Codes...)
	return decision
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func chainTrusted(chain, anchors []string) bool {
	trusted := make(map[string]struct{}, len(anchors))
	for _, anchor := range anchors {
		anchor = strings.TrimSpace(anchor)
		if anchor != "" {
			trusted[anchor] = struct{}{}
		}
	}
	if len(trusted) == 0 {
		return false
	}

	tail := ""
	for _, link := range chain {
		if trimmed := strings.TrimSpace(link); trimmed != "" {
			tail = trimmed
		}
	}
	_, ok := trusted[tail]
	return ok
}

func registrationFingerprint(reg Registration) string {
	data, err := json.Marshal(reg)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func reasonDetail(code string) string {
	switch code {
	case ReasonAllowlisted:
		return "The AAGUID is admitted by the tenant allow policy."
	case ReasonDenylisted:
		return "The AAGUID is explicitly denied by tenant policy."
	case ReasonNotAllowlisted:
		return "The AAGUID is absent from the tenant allow list."
	case ReasonConveyanceNone:
		return "None conveyance limits the registration to low strength."
	case ReasonUntrustedChain:
		return "The attestation chain does not reach a configured trust anchor."
	case ReasonAttestationRequired:
		return "Tenant policy requires verifiable attestation."
	case ReasonUserNotVerified:
		return "The registration did not assert user verification."
	case ReasonStrengthBelowMinimum:
		return "The assessed authenticator strength is below the tenant minimum."
	case ReasonPreviouslyRejected:
		return "The credential was previously rejected and cannot be automatically restored."
	default:
		return "The decision includes policy reason " + code + "."
	}
}

func validReasonCode(code string) bool {
	switch code {
	case ReasonAllowlisted, ReasonDenylisted, ReasonNotAllowlisted,
		ReasonConveyanceNone, ReasonUntrustedChain, ReasonAttestationRequired,
		ReasonUserNotVerified, ReasonStrengthBelowMinimum, ReasonPreviouslyRejected:
		return true
	default:
		return false
	}
}

func (result *evaluationResult) addReason(code string) {
	for _, existing := range result.Codes {
		if existing == code {
			return
		}
	}
	result.Codes = append(result.Codes, code)
	result.Reasons = append(result.Reasons, Reason{Code: code, Detail: reasonDetail(code)})
}

func (result evaluationResult) clone() evaluationResult {
	result.Codes = append([]string(nil), result.Codes...)
	result.Reasons = append([]Reason(nil), result.Reasons...)
	return result
}

func assessRegistration(policy Policy, reg Registration) evaluationResult {
	result := evaluationResult{Accepted: true, Strength: StrengthHigh}

	switch {
	case containsString(policy.DenyAAGUIDs, reg.AAGUID):
		result.addReason(ReasonDenylisted)
		result.Accepted = false
	case len(policy.AllowAAGUIDs) > 0 && !containsString(policy.AllowAAGUIDs, reg.AAGUID):
		result.addReason(ReasonNotAllowlisted)
		result.Accepted = false
	default:
		result.addReason(ReasonAllowlisted)
	}

	if !reg.UserVerified {
		result.addReason(ReasonUserNotVerified)
	}

	registrationConveyance := reg.Conveyance
	if registrationConveyance == "" {
		registrationConveyance = ConveyanceNone
	}
	effectiveRank := conveyanceRank(policy.Conveyance)
	if registrationRank := conveyanceRank(registrationConveyance); registrationRank < effectiveRank {
		effectiveRank = registrationRank
	}
	result.Strength = strengthForRank(effectiveRank)
	if effectiveRank == strengthRank(StrengthLow) {
		result.addReason(ReasonConveyanceNone)
	}

	trustedChain := chainTrusted(reg.Chain, policy.TrustAnchors)
	if !trustedChain {
		result.addReason(ReasonUntrustedChain)
		result.Strength = StrengthLow
	}

	missingAttestation := !reg.Attested || registrationConveyance == ConveyanceNone
	if missingAttestation {
		result.Strength = StrengthLow
	}
	if policy.RequireAttestation && (missingAttestation || !trustedChain) {
		result.addReason(ReasonAttestationRequired)
		result.Accepted = false
	}

	enforceMinimumStrength(&result, policy.MinStrength)
	return result
}

func enforceMinimumStrength(result *evaluationResult, minimum string) {
	if strengthRank(result.Strength) < strengthRank(minimum) {
		result.Accepted = false
		result.addReason(ReasonStrengthBelowMinimum)
	}
}

func validateSnapshotState(state *snapshotState) error {
	if state == nil {
		return fmt.Errorf("%w: empty state", ErrBadSnapshot)
	}
	if state.Tenants == nil || state.Decisions == nil {
		return fmt.Errorf("%w: missing state maps", ErrBadSnapshot)
	}
	for tenantID, tenant := range state.Tenants {
		if tenant == nil {
			return fmt.Errorf("%w: nil tenant state", ErrBadSnapshot)
		}
		if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(tenant.TenantID) == "" {
			return fmt.Errorf("%w: blank tenant id", ErrBadSnapshot)
		}
		if tenantID != tenant.TenantID {
			return fmt.Errorf("%w: tenant key mismatch", ErrBadSnapshot)
		}
		if len(tenant.Versions) == 0 {
			return fmt.Errorf("%w: tenant has no policy versions", ErrBadSnapshot)
		}
		if len(tenant.Versions) > state.Limits.MaxVersions {
			return fmt.Errorf("%w: tenant exceeds version limit", ErrBadSnapshot)
		}
		for index, record := range tenant.Versions {
			wantGeneration := index + 1
			if record.Version.Generation != wantGeneration {
				return fmt.Errorf("%w: non-contiguous version history", ErrBadSnapshot)
			}
			if record.Version.TenantID != tenantID || record.Policy.TenantID != tenantID {
				return fmt.Errorf("%w: tenant version mismatch", ErrBadSnapshot)
			}
			if record.Version.Allow != len(record.Policy.AllowAAGUIDs) ||
				record.Version.Deny != len(record.Policy.DenyAAGUIDs) ||
				record.Version.Anchors != len(record.Policy.TrustAnchors) {
				return fmt.Errorf("%w: version counts do not match policy", ErrBadSnapshot)
			}
			if len(record.Policy.AllowAAGUIDs)+len(record.Policy.DenyAAGUIDs) > state.Limits.MaxEntriesPerList {
				return fmt.Errorf("%w: policy exceeds aaguid limit", ErrBadSnapshot)
			}
			if len(record.Policy.TrustAnchors) > state.Limits.MaxTrustAnchors {
				return fmt.Errorf("%w: policy exceeds anchor limit", ErrBadSnapshot)
			}
			if !validConveyance(record.Policy.Conveyance) || !validStrength(record.Policy.MinStrength) {
				return fmt.Errorf("%w: invalid policy in snapshot", ErrBadSnapshot)
			}
		}
		if tenant.Seq < 0 {
			return fmt.Errorf("%w: negative tenant sequence", ErrBadSnapshot)
		}
		for key, credential := range tenant.Credentials {
			if credential == nil || strings.TrimSpace(credential.Registration.ID) == "" {
				return fmt.Errorf("%w: invalid credential record", ErrBadSnapshot)
			}
			if key != strings.TrimSpace(credential.Registration.ID) {
				return fmt.Errorf("%w: credential key mismatch", ErrBadSnapshot)
			}
			if strengthRank(credential.Strength) == 0 {
				return fmt.Errorf("%w: invalid credential strength", ErrBadSnapshot)
			}
			if credential.Generation < 0 || credential.Generation > len(tenant.Versions) {
				return fmt.Errorf("%w: invalid credential generation", ErrBadSnapshot)
			}
		}
	}

	for decisionID, record := range state.Decisions {
		if record == nil {
			return fmt.Errorf("%w: nil decision record", ErrBadSnapshot)
		}
		if record.Decision.ID != decisionID {
			return fmt.Errorf("%w: decision id mismatch", ErrBadSnapshot)
		}
		tenant, ok := state.Tenants[record.Decision.TenantID]
		if !ok {
			return fmt.Errorf("%w: decision references unknown tenant", ErrBadSnapshot)
		}
		if strings.TrimSpace(record.Decision.RegistrationID) == "" {
			return fmt.Errorf("%w: decision has blank registration", ErrBadSnapshot)
		}
		if strengthRank(record.Decision.Strength) == 0 {
			return fmt.Errorf("%w: decision has invalid strength", ErrBadSnapshot)
		}
		if record.Decision.Generation < 1 || record.Decision.Generation > len(tenant.Versions) {
			return fmt.Errorf("%w: decision has invalid generation", ErrBadSnapshot)
		}
		if record.Decision.Seq < 1 || record.Decision.Seq > tenant.Seq {
			return fmt.Errorf("%w: decision has invalid sequence", ErrBadSnapshot)
		}
		if len(record.Reasons) != len(record.Decision.Codes) {
			return fmt.Errorf("%w: decision reason count mismatch", ErrBadSnapshot)
		}
		for index, code := range record.Decision.Codes {
			if !validReasonCode(code) || record.Reasons[index].Code != code || record.Reasons[index].Detail == "" {
				return fmt.Errorf("%w: invalid decision reason", ErrBadSnapshot)
			}
		}
	}

	if maxID, ok := maxDecisionNumber(state.Decisions); ok && maxID > state.DecisionCounter {
		return fmt.Errorf("%w: decision counter below issued ids", ErrBadSnapshot)
	}
	return nil
}

func maxDecisionNumber(decisions map[string]*decisionRecord) (uint64, bool) {
	var max uint64
	found := false
	for decisionID := range decisions {
		const prefix = "decision-"
		if !strings.HasPrefix(decisionID, prefix) {
			continue
		}
		number, err := strconv.ParseUint(strings.TrimPrefix(decisionID, prefix), 10, 64)
		if err != nil {
			continue
		}
		if !found || number > max {
			max = number
			found = true
		}
	}
	return max, found
}
