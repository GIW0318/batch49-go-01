// Package task grades WebAuthn registration attempts against the per tenant
// authenticator attestation policy before a credential reaches the ledger.
package task

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// errNotImplemented is returned by the rollback mode of the engine.
var errNotImplemented = errors.New("not implemented")

// policyEngineMode is a build-time rollback switch. It defaults to the new
// implementation; linking with
// -ldflags "-X gsb/batch49-go-safety-a-02.policyEngineMode=legacy"
// restores the pre-change stub behaviour byte for byte.
var policyEngineMode = "new"

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
	ReasonAllowlisted         = "reason_allowlisted"
	ReasonDenylisted          = "reason_denylisted"
	ReasonNotAllowlisted      = "reason_not_allowlisted"
	ReasonConveyanceNone      = "reason_conveyance_none"
	ReasonUntrustedChain      = "reason_untrusted_chain"
	ReasonAttestationRequired = "reason_attestation_required"
	ReasonUserNotVerified     = "reason_user_not_verified"
	ReasonTrustedChain        = "reason_trusted_chain"
	ReasonBelowMinStrength    = "reason_below_min_strength"
	ReasonStrengthCapped      = "reason_strength_capped"
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

// Limits bounds the policy tables.
type Limits struct {
	MaxEntriesPerList int
	MaxTrustAnchors   int
	MaxVersions       int
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

type decisionRecord struct {
	Decision Decision `json:"decision"`
	Reasons  []Reason `json:"reasons"`
}

type ledgerRecord struct {
	Registration Registration `json:"registration"`
	DecisionID   string       `json:"decision_id"`
	Strength     string       `json:"strength"`
	Generation   int          `json:"generation"`
}

type tenantState struct {
	policy         Policy
	versions       []Version
	nextGeneration int
	nextSeq        int64
	ledger         map[string]ledgerRecord
}

// AuthenticatorAttestationPolicy owns the policies, their versions and the
// evaluation cache.
type AuthenticatorAttestationPolicy struct {
	mu             sync.RWMutex
	limits         Limits
	tenants        map[string]*tenantState
	decisions      map[string]decisionRecord
	nextDecisionID int64
}

// NewAuthenticatorAttestationPolicy builds a policy engine with the given caps.
func NewAuthenticatorAttestationPolicy(limits Limits) *AuthenticatorAttestationPolicy {
	return &AuthenticatorAttestationPolicy{
		limits:         limits,
		tenants:        make(map[string]*tenantState),
		decisions:      make(map[string]decisionRecord),
		nextDecisionID: 1,
	}
}

func newLogicEnabled() bool {
	return policyEngineMode != "legacy"
}

func cloneStrings(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func clonePolicy(in Policy) Policy {
	out := in
	out.AllowAAGUIDs = cloneStrings(in.AllowAAGUIDs)
	out.DenyAAGUIDs = cloneStrings(in.DenyAAGUIDs)
	out.TrustAnchors = cloneStrings(in.TrustAnchors)
	return out
}

func cloneDecision(in Decision) Decision {
	out := in
	out.Codes = cloneStrings(in.Codes)
	return out
}

func cloneReasons(in []Reason) []Reason {
	if in == nil {
		return nil
	}
	out := make([]Reason, len(in))
	copy(out, in)
	return out
}

func cloneRegistration(in Registration) Registration {
	out := in
	out.Chain = cloneStrings(in.Chain)
	return out
}

func validConveyance(v string) bool {
	switch v {
	case ConveyanceNone, ConveyanceIndirect, ConveyanceEnterprise:
		return true
	}
	return false
}

func validStrength(v string) bool {
	switch v {
	case StrengthLow, StrengthMedium, StrengthHigh:
		return true
	}
	return false
}

func strengthRank(v string) int {
	switch v {
	case StrengthHigh:
		return 3
	case StrengthMedium:
		return 2
	case StrengthLow:
		return 1
	}
	return 0
}

func lowerStrength(a, b string) string {
	if strengthRank(a) <= strengthRank(b) {
		return a
	}
	return b
}

func containsString(haystack []string, needle string) bool {
	for _, item := range haystack {
		if item == needle {
			return true
		}
	}
	return false
}

func (e *AuthenticatorAttestationPolicy) validatePolicyValues(tenantID string, p Policy) error {
	if strings.TrimSpace(tenantID) == "" {
		return ErrBlankTenant
	}
	if !validConveyance(p.Conveyance) {
		return ErrBadConveyance
	}
	if !validStrength(p.MinStrength) {
		return ErrBadStrength
	}
	return nil
}

func (e *AuthenticatorAttestationPolicy) validatePolicyLimits(p Policy) error {
	maxEntries := e.limits.MaxEntriesPerList
	if maxEntries > 0 && len(p.AllowAAGUIDs)+len(p.DenyAAGUIDs) > maxEntries {
		return ErrAAGUIDLimit
	}
	if maxAnchors := e.limits.MaxTrustAnchors; maxAnchors > 0 && len(p.TrustAnchors) > maxAnchors {
		return ErrTrustAnchorLimit
	}
	return nil
}

// SetPolicy installs a new policy revision and returns the stored version.
func (e *AuthenticatorAttestationPolicy) SetPolicy(tenantID string, p Policy) (Version, error) {
	if !newLogicEnabled() {
		return Version{}, errNotImplemented
	}
	if err := e.validatePolicyValues(tenantID, p); err != nil {
		return Version{}, err
	}
	if err := e.validatePolicyLimits(p); err != nil {
		return Version{}, err
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	if e.tenants == nil {
		e.tenants = make(map[string]*tenantState)
	}
	ts := e.tenants[tenantID]
	if maxVersions := e.limits.MaxVersions; maxVersions > 0 && ts != nil && len(ts.versions) >= maxVersions {
		return Version{}, ErrPolicyLimit
	}
	if ts == nil {
		ts = &tenantState{
			nextGeneration: 1,
			nextSeq:        1,
			ledger:         make(map[string]ledgerRecord),
		}
		e.tenants[tenantID] = ts
	}

	stored := clonePolicy(p)
	stored.TenantID = tenantID
	generation := ts.nextGeneration
	version := Version{
		TenantID:   tenantID,
		Generation: generation,
		Allow:      len(stored.AllowAAGUIDs),
		Deny:       len(stored.DenyAAGUIDs),
		Anchors:    len(stored.TrustAnchors),
	}

	ts.policy = stored
	ts.versions = append(ts.versions, version)
	ts.nextGeneration++
	return version, nil
}

// Policy reads the current policy of one tenant.
func (e *AuthenticatorAttestationPolicy) Policy(tenantID string) (Policy, error) {
	if !newLogicEnabled() {
		return Policy{}, errNotImplemented
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	ts, ok := e.tenants[tenantID]
	if !ok {
		return Policy{}, ErrUnknownTenant
	}
	return clonePolicy(ts.policy), nil
}

// Versions lists the stored policy revisions of one tenant, oldest first.
func (e *AuthenticatorAttestationPolicy) Versions(tenantID string) []Version {
	if !newLogicEnabled() {
		return nil
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	ts, ok := e.tenants[tenantID]
	if !ok {
		return nil
	}
	out := make([]Version, len(ts.versions))
	copy(out, ts.versions)
	return out
}

type gradeResult struct {
	Accepted bool
	Strength string
	Codes    []string
	Details  []string
}

func (g *gradeResult) add(code, detail string) {
	g.Codes = append(g.Codes, code)
	g.Details = append(g.Details, detail)
}

func (e *AuthenticatorAttestationPolicy) gradeRegistration(p Policy, reg Registration) gradeResult {
	g := gradeResult{Accepted: true, Strength: StrengthLow}

	if containsString(p.DenyAAGUIDs, reg.AAGUID) {
		g.add(ReasonDenylisted, "authenticator AAGUID is present on the tenant deny list")
		g.Accepted = false
		return g
	}
	if len(p.AllowAAGUIDs) > 0 && !containsString(p.AllowAAGUIDs, reg.AAGUID) {
		g.add(ReasonNotAllowlisted, "authenticator AAGUID is absent from the tenant allow list")
		g.Accepted = false
		return g
	}
	if len(p.AllowAAGUIDs) > 0 {
		g.add(ReasonAllowlisted, "authenticator AAGUID is present on the tenant allow list")
	}

	attestationMissing := reg.Conveyance == ConveyanceNone || !reg.Attested
	if reg.Conveyance == ConveyanceNone {
		g.add(ReasonConveyanceNone, "attestation conveyance is none, so strength is capped at low")
		g.Strength = StrengthLow
	} else if !reg.Attested {
		g.add(ReasonUntrustedChain, "registration carries no attestation statement")
		g.Strength = StrengthLow
	} else if !validConveyance(reg.Conveyance) {
		g.add(ReasonUntrustedChain, "registration conveyance cannot carry a trusted attestation chain")
		g.Strength = StrengthLow
	} else if !chainEndsAtAnchor(reg.Chain, p.TrustAnchors) {
		g.add(ReasonUntrustedChain, "attestation chain does not terminate at a configured trust anchor")
		g.Strength = StrengthLow
		attestationMissing = true
	} else {
		switch reg.Conveyance {
		case ConveyanceEnterprise:
			g.Strength = StrengthHigh
		case ConveyanceIndirect:
			g.Strength = StrengthMedium
		default:
			g.Strength = StrengthLow
		}
		g.add(ReasonTrustedChain, "attestation chain terminates at a configured trust anchor")
	}

	if p.RequireAttestation && attestationMissing {
		g.add(ReasonAttestationRequired, "tenant policy requires an attested chain anchored in the configured trust roots")
		g.Accepted = false
		return g
	}

	if strengthRank(g.Strength) < strengthRank(p.MinStrength) {
		g.add(ReasonBelowMinStrength, "resolved authenticator strength is below the tenant minimum")
		g.Accepted = false
	}
	return g
}

func chainEndsAtAnchor(chain, anchors []string) bool {
	if len(chain) == 0 || len(anchors) == 0 {
		return false
	}
	return containsString(anchors, chain[len(chain)-1])
}

func (e *AuthenticatorAttestationPolicy) recordDecisionLocked(
	ts *tenantState,
	tenantID string,
	reg Registration,
	generation int,
	g gradeResult,
) Decision {
	if ts.nextSeq < 1 {
		ts.nextSeq = 1
	}
	id := fmt.Sprintf("decision-%d", e.nextDecisionID)
	e.nextDecisionID++

	d := Decision{
		ID:             id,
		TenantID:       tenantID,
		RegistrationID: reg.ID,
		Accepted:       g.Accepted,
		Strength:       g.Strength,
		Generation:     generation,
		Codes:          cloneStrings(g.Codes),
		Seq:            ts.nextSeq,
	}
	ts.nextSeq++

	reasons := make([]Reason, len(g.Codes))
	for i, code := range g.Codes {
		reasons[i] = Reason{Code: code, Detail: g.Details[i]}
	}
	e.decisions[id] = decisionRecord{Decision: cloneDecision(d), Reasons: cloneReasons(reasons)}
	return d
}

func ledgerKey(reg Registration) string {
	return strings.TrimSpace(reg.ID)
}

// Evaluate grades one registration against the tenant policy.
func (e *AuthenticatorAttestationPolicy) Evaluate(tenantID string, reg Registration) (Decision, error) {
	if !newLogicEnabled() {
		return Decision{}, errNotImplemented
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	ts, ok := e.tenants[tenantID]
	if !ok {
		return Decision{}, ErrUnknownTenant
	}
	if strings.TrimSpace(reg.ID) == "" {
		return Decision{}, ErrBlankRegistration
	}
	generation := ts.nextGeneration - 1
	result := e.gradeRegistration(ts.policy, reg)
	d := e.recordDecisionLocked(ts, tenantID, reg, generation, result)

	key := ledgerKey(reg)
	if d.Accepted {
		ts.ledger[key] = ledgerRecord{
			Registration: cloneRegistration(reg),
			DecisionID:   d.ID,
			Strength:     d.Strength,
			Generation:   d.Generation,
		}
	} else {
		// A credential rejected by the live policy never remains in the
		// accepted-credential ledger.
		delete(ts.ledger, key)
	}
	return cloneDecision(d), nil
}

// Decision reads one cached decision by id.
func (e *AuthenticatorAttestationPolicy) Decision(decisionID string) (Decision, bool) {
	if !newLogicEnabled() {
		return Decision{}, false
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	rec, ok := e.decisions[decisionID]
	if !ok {
		return Decision{}, false
	}
	return cloneDecision(rec.Decision), true
}

// Explain returns the ordered reasons behind one cached decision.
func (e *AuthenticatorAttestationPolicy) Explain(decisionID string) []Reason {
	if !newLogicEnabled() {
		return nil
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	rec, ok := e.decisions[decisionID]
	if !ok {
		return nil
	}
	return cloneReasons(rec.Reasons)
}

// Reevaluate re-grades the cached registrations of one tenant and reports how
// many credentials were revisited.
func (e *AuthenticatorAttestationPolicy) Reevaluate(tenantID string) (int, error) {
	if !newLogicEnabled() {
		return 0, errNotImplemented
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	ts, ok := e.tenants[tenantID]
	if !ok {
		return 0, ErrUnknownTenant
	}
	generation := ts.nextGeneration - 1

	keys := make([]string, 0, len(ts.ledger))
	for key := range ts.ledger {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	revisited := 0
	for _, key := range keys {
		entry, stillPresent := ts.ledger[key]
		if !stillPresent {
			continue
		}
		revisited++

		result := e.gradeRegistration(ts.policy, entry.Registration)
		if strengthRank(result.Strength) > strengthRank(entry.Strength) {
			result.add(ReasonStrengthCapped, "existing credential strength cannot be raised by a newer policy")
		}
		result.Strength = lowerStrength(entry.Strength, result.Strength)

		if result.Accepted && strengthRank(result.Strength) < strengthRank(ts.policy.MinStrength) {
			result.add(ReasonBelowMinStrength, "existing credential strength is below the tenant minimum")
			result.Accepted = false
		}

		d := e.recordDecisionLocked(ts, tenantID, entry.Registration, generation, result)
		if d.Accepted {
			updated := entry
			updated.DecisionID = d.ID
			updated.Strength = d.Strength
			updated.Generation = d.Generation
			ts.ledger[key] = updated
		} else {
			delete(ts.ledger, key)
		}
	}
	return revisited, nil
}

const snapshotMagic = "gsb/authenticator-attestation-policy/v1"

type snapshotPayload struct {
	Policies       map[string]Policy                  `json:"policies"`
	Versions       map[string][]Version               `json:"versions"`
	Ledgers        map[string]map[string]ledgerRecord `json:"ledgers"`
	Decisions      map[string]decisionRecord          `json:"decisions"`
	NextDecisionID int64                              `json:"next_decision_id"`
}

type snapshotEnvelope struct {
	Magic   string          `json:"magic"`
	SHA256  string          `json:"sha256"`
	Payload json.RawMessage `json:"payload"`
}

// Snapshot serialises the policies, the versions and the decision cache.
func (e *AuthenticatorAttestationPolicy) Snapshot() ([]byte, error) {
	if !newLogicEnabled() {
		return nil, errNotImplemented
	}

	e.mu.RLock()
	defer e.mu.RUnlock()

	payload := snapshotPayload{
		Policies:       make(map[string]Policy, len(e.tenants)),
		Versions:       make(map[string][]Version, len(e.tenants)),
		Ledgers:        make(map[string]map[string]ledgerRecord, len(e.tenants)),
		Decisions:      make(map[string]decisionRecord, len(e.decisions)),
		NextDecisionID: e.nextDecisionID,
	}
	for tenantID, ts := range e.tenants {
		payload.Policies[tenantID] = clonePolicy(ts.policy)
		versions := make([]Version, len(ts.versions))
		copy(versions, ts.versions)
		payload.Versions[tenantID] = versions
		ledgerCopy := make(map[string]ledgerRecord, len(ts.ledger))
		for key, entry := range ts.ledger {
			entry.Registration = cloneRegistration(entry.Registration)
			ledgerCopy[key] = entry
		}
		payload.Ledgers[tenantID] = ledgerCopy
	}
	for id, rec := range e.decisions {
		payload.Decisions[id] = decisionRecord{
			Decision: cloneDecision(rec.Decision),
			Reasons:  cloneReasons(rec.Reasons),
		}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	env := snapshotEnvelope{
		Magic:   snapshotMagic,
		SHA256:  hex.EncodeToString(sum[:]),
		Payload: body,
	}
	return json.Marshal(env)
}

func validateSnapshotPayload(payload snapshotPayload) error {
	if payload.Policies == nil || payload.Versions == nil || payload.Ledgers == nil || payload.Decisions == nil {
		return ErrBadSnapshot
	}
	if payload.NextDecisionID < 1 {
		return ErrBadSnapshot
	}
	for tenantID, p := range payload.Policies {
		if strings.TrimSpace(tenantID) == "" || p.TenantID != tenantID {
			return ErrBadSnapshot
		}
		if !validConveyance(p.Conveyance) || !validStrength(p.MinStrength) {
			return ErrBadSnapshot
		}
		versions, ok := payload.Versions[tenantID]
		if !ok || len(versions) == 0 {
			return ErrBadSnapshot
		}
		for i, v := range versions {
			if v.TenantID != tenantID || v.Generation != i+1 || v.Allow < 0 || v.Deny < 0 || v.Anchors < 0 {
				return ErrBadSnapshot
			}
		}
		last := versions[len(versions)-1]
		if last.Allow != len(p.AllowAAGUIDs) || last.Deny != len(p.DenyAAGUIDs) || last.Anchors != len(p.TrustAnchors) {
			return ErrBadSnapshot
		}
	}
	for tenantID := range payload.Versions {
		if _, ok := payload.Policies[tenantID]; !ok {
			return ErrBadSnapshot
		}
	}
	for tenantID := range payload.Ledgers {
		if _, ok := payload.Policies[tenantID]; !ok {
			return ErrBadSnapshot
		}
	}

	var maxDecisionID int64
	for id, rec := range payload.Decisions {
		if id == "" || rec.Decision.ID != id || len(rec.Decision.Codes) != len(rec.Reasons) {
			return ErrBadSnapshot
		}
		if rec.Decision.RegistrationID == "" || rec.Decision.TenantID == "" || rec.Decision.Seq < 1 {
			return ErrBadSnapshot
		}
		if !validStrength(rec.Decision.Strength) {
			return ErrBadSnapshot
		}
		if _, ok := payload.Policies[rec.Decision.TenantID]; !ok {
			return ErrBadSnapshot
		}
		if rec.Decision.Generation < 1 || rec.Decision.Generation > len(payload.Versions[rec.Decision.TenantID]) {
			return ErrBadSnapshot
		}
		for _, reason := range rec.Reasons {
			if reason.Code == "" || reason.Detail == "" {
				return ErrBadSnapshot
			}
		}
		if !strings.HasPrefix(id, "decision-") {
			return ErrBadSnapshot
		}
		n, err := strconv.ParseInt(strings.TrimPrefix(id, "decision-"), 10, 64)
		if err != nil || n < 1 {
			return ErrBadSnapshot
		}
		if n > maxDecisionID {
			maxDecisionID = n
		}
	}
	if payload.NextDecisionID <= maxDecisionID {
		return ErrBadSnapshot
	}
	for tenantID, ledger := range payload.Ledgers {
		for key, entry := range ledger {
			if key != ledgerKey(entry.Registration) || strings.TrimSpace(key) == "" {
				return ErrBadSnapshot
			}
			if !validStrength(entry.Strength) {
				return ErrBadSnapshot
			}
			if entry.Generation < 1 || entry.Generation > len(payload.Versions[tenantID]) {
				return ErrBadSnapshot
			}
			if rec, ok := payload.Decisions[entry.DecisionID]; !ok || !rec.Decision.Accepted {
				return ErrBadSnapshot
			}
		}
	}
	return nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (e *AuthenticatorAttestationPolicy) Restore(data []byte) error {
	if !newLogicEnabled() {
		return errNotImplemented
	}

	var env snapshotEnvelope
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&env); err != nil {
		return ErrBadSnapshot
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return ErrBadSnapshot
	}
	if env.Magic != snapshotMagic {
		return ErrBadSnapshot
	}
	sum := sha256.Sum256(env.Payload)
	if !strings.EqualFold(env.SHA256, hex.EncodeToString(sum[:])) {
		return ErrBadSnapshot
	}

	var payload snapshotPayload
	payloadDecoder := json.NewDecoder(bytes.NewReader(env.Payload))
	payloadDecoder.DisallowUnknownFields()
	if err := payloadDecoder.Decode(&payload); err != nil {
		return ErrBadSnapshot
	}
	if err := ensureJSONEOF(payloadDecoder); err != nil {
		return ErrBadSnapshot
	}
	if err := validateSnapshotPayload(payload); err != nil {
		return ErrBadSnapshot
	}

	tenants := make(map[string]*tenantState, len(payload.Policies))
	for tenantID, p := range payload.Policies {
		versions := make([]Version, len(payload.Versions[tenantID]))
		copy(versions, payload.Versions[tenantID])
		ledger := make(map[string]ledgerRecord, len(payload.Ledgers[tenantID]))
		for key, entry := range payload.Ledgers[tenantID] {
			entry.Registration = cloneRegistration(entry.Registration)
			ledger[key] = entry
		}
		tenants[tenantID] = &tenantState{
			policy:         clonePolicy(p),
			versions:       versions,
			nextGeneration: len(versions) + 1,
			nextSeq:        1,
			ledger:         ledger,
		}
	}

	decisions := make(map[string]decisionRecord, len(payload.Decisions))
	for id, rec := range payload.Decisions {
		decisions[id] = decisionRecord{
			Decision: cloneDecision(rec.Decision),
			Reasons:  cloneReasons(rec.Reasons),
		}
		if rec.Decision.Seq >= tenants[rec.Decision.TenantID].nextSeq {
			tenants[rec.Decision.TenantID].nextSeq = rec.Decision.Seq + 1
		}
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	e.tenants = tenants
	e.decisions = decisions
	e.nextDecisionID = payload.NextDecisionID
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if err == nil {
		return ErrBadSnapshot
	}
	if !errors.Is(err, io.EOF) {
		return ErrBadSnapshot
	}
	return nil
}
