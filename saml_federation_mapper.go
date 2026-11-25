// Package task maps SAML assertions from several identity providers onto local
// accounts, keeps the per provider attribute rules and tracks a NameID format
// migration that runs both identifiers side by side.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"
)

// NameIDFormat is the declared format of the subject identifier.
type NameIDFormat string

// Supported name identifier formats.
const (
	NameIDPersistent  NameIDFormat = "persistent"
	NameIDEmail       NameIDFormat = "email"
	NameIDTransient   NameIDFormat = "transient"
	NameIDUnspecified NameIDFormat = "unspecified"
)

// Multi value strategies accepted by an attribute rule.
const (
	MultiFirst    = "first"
	MultiJoin     = "join"
	MultiTruncate = "truncate"
)

const (
	snapshotVersion  = 1
	migrationOverlap = 24 * time.Hour
	emailIndexPrefix = "email:"
)

// Errors reported by the mapper.
var (
	ErrBlankArgument   = errors.New("tenant id, idp id, name id and user id are required")
	ErrUnknownIdP      = errors.New("identity provider is not configured for this tenant")
	ErrRuleLimit       = errors.New("attribute rule limit reached for this identity provider")
	ErrIdPLimit        = errors.New("identity provider limit reached for this tenant")
	ErrDuplicateRule   = errors.New("attribute rule name is duplicated")
	ErrBadRule         = errors.New("attribute rule is malformed")
	ErrMissingAttr     = errors.New("required assertion attribute is missing")
	ErrBadNameID       = errors.New("name identifier is not usable")
	ErrBadFormat       = errors.New("name identifier format is not supported")
	ErrBindingConflict = errors.New("name identifier is already bound to another account")
	ErrBindingLimit    = errors.New("binding limit reached for this identity provider")
	ErrBindingNotFound = errors.New("binding not found")
	ErrSameFormat      = errors.New("source and target formats must differ")
	ErrMigrationActive = errors.New("a migration is already running for this identity provider")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// AttrRule says how one local attribute is taken from the assertion.
type AttrRule struct {
	Name      string `json:"name"`
	Source    string `json:"source"`
	Required  bool   `json:"required"`
	Multi     string `json:"multi"`
	MaxValues int    `json:"max_values"`
	MaxLen    int    `json:"max_len"`
	Separator string `json:"separator"`
}

// Assertion is the subset of a SAML assertion the mapper consumes.
type Assertion struct {
	NameID     string              `json:"name_id"`
	Format     NameIDFormat        `json:"format"`
	Issuer     string              `json:"issuer"`
	Attributes map[string][]string `json:"attributes"`
}

// Identity is the local account resolved from one assertion.
type Identity struct {
	UserID     string            `json:"user_id"`
	TenantID   string            `json:"tenant_id"`
	IdPID      string            `json:"idp_id"`
	Email      string            `json:"email"`
	Created    bool              `json:"created"`
	Attributes map[string]string `json:"attributes"`
	Seq        int64             `json:"seq"`
}

// Binding ties one name identifier of one provider to one local account.
type Binding struct {
	TenantID string       `json:"tenant_id"`
	IdPID    string       `json:"idp_id"`
	NameID   string       `json:"name_id"`
	Format   NameIDFormat `json:"format"`
	UserID   string       `json:"user_id"`
	Seq      int64        `json:"seq"`
}

// MigrationPlan describes a name identifier format migration.
type MigrationPlan struct {
	TenantID     string       `json:"tenant_id"`
	IdPID        string       `json:"idp_id"`
	From         NameIDFormat `json:"from"`
	To           NameIDFormat `json:"to"`
	OverlapUntil time.Time    `json:"overlap_until"`
	Bindings     int          `json:"bindings"`
	Seq          int64        `json:"seq"`
}

// Limits bounds the mapper tables.
type Limits struct {
	MaxIdPsPerTenant  int
	MaxRulesPerIdP    int
	MaxBindingsPerIdP int
}

type accountState struct {
	UserID     string            `json:"user_id"`
	Email      string            `json:"email,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

type providerState struct {
	Rules      []AttrRule               `json:"rules"`
	Bindings   []Binding                `json:"bindings"`
	Accounts   map[string]*accountState `json:"accounts,omitempty"`
	Migration  *MigrationPlan           `json:"migration,omitempty"`
	nameIndex  map[string]string        `json:"-"`
	emailIndex map[string]string        `json:"-"`
}

type snapshotEnvelope struct {
	Version int                                  `json:"version"`
	Seq     int64                                `json:"seq"`
	Tenants map[string]map[string]*providerState `json:"tenants"`
}

// SamlFederationMapper owns the rules, the bindings and the migration state.
type SamlFederationMapper struct {
	mu      sync.RWMutex
	limits  Limits
	seq     int64
	tenants map[string]map[string]*providerState
}

// NewSamlFederationMapper builds a mapper with the given caps.
func NewSamlFederationMapper(limits Limits) *SamlFederationMapper {
	if limits.MaxIdPsPerTenant < 0 {
		limits.MaxIdPsPerTenant = 0
	}
	if limits.MaxRulesPerIdP < 0 {
		limits.MaxRulesPerIdP = 0
	}
	if limits.MaxBindingsPerIdP < 0 {
		limits.MaxBindingsPerIdP = 0
	}
	return &SamlFederationMapper{
		limits:  limits,
		tenants: make(map[string]map[string]*providerState),
	}
}

// Configure replaces the attribute rules of one provider.
func (m *SamlFederationMapper) Configure(tenantID, idpID string, rules []AttrRule) error {
	if isBlank(tenantID) || isBlank(idpID) {
		return ErrBlankArgument
	}

	stored, err := validateRules(rules, m.limits.MaxRulesPerIdP)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	tenant, ok := m.tenants[tenantID]
	if !ok {
		tenant = make(map[string]*providerState)
	}

	if provider, ok := tenant[idpID]; ok {
		provider.Rules = stored
		return nil
	}

	if len(tenant)+1 > m.limits.MaxIdPsPerTenant {
		return ErrIdPLimit
	}
	m.tenants[tenantID] = tenant
	tenant[idpID] = newProviderState(stored)
	return nil
}

// Attributes returns the rules of one provider in configuration order.
func (m *SamlFederationMapper) Attributes(tenantID, idpID string) ([]AttrRule, error) {
	if isBlank(tenantID) || isBlank(idpID) {
		return nil, ErrBlankArgument
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	provider, err := m.providerLocked(tenantID, idpID)
	if err != nil {
		return nil, err
	}
	rules := make([]AttrRule, len(provider.Rules))
	copy(rules, provider.Rules)
	return rules, nil
}

// Map turns one assertion into a local identity.
func (m *SamlFederationMapper) Map(tenantID, idpID string, a Assertion) (Identity, error) {
	if isBlank(tenantID) || isBlank(idpID) {
		return Identity{}, ErrBlankArgument
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	provider, err := m.providerLocked(tenantID, idpID)
	if err != nil {
		return Identity{}, err
	}
	provider.ensureIndexes()

	nameID, err := m.NormalizeNameID(a.Format, a.NameID)
	if err != nil {
		return Identity{}, err
	}

	attributes, email, err := m.mapAttributes(provider.Rules, a.Attributes)
	if err != nil {
		return Identity{}, err
	}

	userID, created, shouldBind, err := m.resolveIdentityLocked(provider, nameID, a.Format, email)
	if err != nil {
		return Identity{}, err
	}

	m.seq++
	seq := m.seq
	if created {
		userID = fmt.Sprintf("user-%d", seq)
	}
	if shouldBind {
		provider.Bindings = append(provider.Bindings, Binding{
			TenantID: tenantID,
			IdPID:    idpID,
			NameID:   nameID,
			Format:   a.Format,
			UserID:   userID,
			Seq:      seq,
		})
		provider.nameIndex[nameID] = userID
	}

	account := provider.Accounts[userID]
	if account == nil {
		account = &accountState{UserID: userID, Attributes: make(map[string]string)}
		provider.Accounts[userID] = account
	}
	if account.Attributes == nil {
		account.Attributes = make(map[string]string)
	}
	if email != "" {
		account.Email = email
		provider.emailIndex[emailKey(email)] = userID
	}
	for name, value := range attributes {
		account.Attributes[name] = value
	}

	return Identity{
		UserID:     userID,
		TenantID:   tenantID,
		IdPID:      idpID,
		Email:      email,
		Created:    created,
		Attributes: attributes,
		Seq:        seq,
	}, nil
}

// Bind ties one name identifier to one local account.
func (m *SamlFederationMapper) Bind(tenantID, idpID, nameID, userID string) error {
	if isBlank(tenantID) || isBlank(idpID) || isBlank(nameID) || isBlank(userID) {
		return ErrBlankArgument
	}
	key := bindingKey(nameID)

	m.mu.Lock()
	defer m.mu.Unlock()

	provider, err := m.providerLocked(tenantID, idpID)
	if err != nil {
		return err
	}
	provider.ensureIndexes()

	if existingUserID, ok := provider.nameIndex[key]; ok {
		if existingUserID != userID {
			return ErrBindingConflict
		}
		return nil
	}

	if len(provider.Bindings)+1 > m.limits.MaxBindingsPerIdP {
		return ErrBindingLimit
	}

	m.seq++
	provider.Bindings = append(provider.Bindings, Binding{
		TenantID: tenantID,
		IdPID:    idpID,
		NameID:   key,
		Format:   NameIDUnspecified,
		UserID:   userID,
		Seq:      m.seq,
	})
	provider.nameIndex[key] = userID
	return nil
}

// Unbind removes one name identifier binding.
func (m *SamlFederationMapper) Unbind(tenantID, idpID, nameID string) error {
	if isBlank(tenantID) || isBlank(idpID) || isBlank(nameID) {
		return ErrBlankArgument
	}
	key := bindingKey(nameID)

	m.mu.Lock()
	defer m.mu.Unlock()

	provider, ok := m.tenantProviderLocked(tenantID, idpID)
	if !ok {
		return ErrBindingNotFound
	}
	provider.ensureIndexes()

	if _, exists := provider.nameIndex[key]; !exists {
		return ErrBindingNotFound
	}

	delete(provider.nameIndex, key)
	for i, binding := range provider.Bindings {
		if binding.NameID == key {
			provider.Bindings = append(provider.Bindings[:i], provider.Bindings[i+1:]...)
			break
		}
	}
	return nil
}

// Bindings lists the bindings of one provider, oldest first.
func (m *SamlFederationMapper) Bindings(tenantID, idpID string) []Binding {
	m.mu.RLock()
	defer m.mu.RUnlock()

	provider, ok := m.tenantProviderLocked(tenantID, idpID)
	if !ok {
		return []Binding{}
	}
	bindings := make([]Binding, len(provider.Bindings))
	copy(bindings, provider.Bindings)
	return bindings
}

// Migrate opens a name identifier format migration for one provider.
func (m *SamlFederationMapper) Migrate(tenantID, idpID string, from, to NameIDFormat) (MigrationPlan, error) {
	return m.MigrateAt(tenantID, idpID, from, to, time.Now().UTC())
}

// MigrateAt opens a migration with a caller supplied issue time.
func (m *SamlFederationMapper) MigrateAt(tenantID, idpID string, from, to NameIDFormat, now time.Time) (MigrationPlan, error) {
	if isBlank(tenantID) || isBlank(idpID) {
		return MigrationPlan{}, ErrBlankArgument
	}
	if !isKnownFormat(from) || !isKnownFormat(to) {
		return MigrationPlan{}, ErrBadFormat
	}
	if from == to {
		return MigrationPlan{}, ErrSameFormat
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	provider, err := m.providerLocked(tenantID, idpID)
	if err != nil {
		return MigrationPlan{}, err
	}

	if provider.Migration != nil {
		if provider.Migration.To == to {
			return *provider.Migration, nil
		}
		return MigrationPlan{}, ErrMigrationActive
	}

	m.seq++
	plan := MigrationPlan{
		TenantID:     tenantID,
		IdPID:        idpID,
		From:         from,
		To:           to,
		OverlapUntil: now.Add(migrationOverlap),
		Bindings:     len(provider.Bindings),
		Seq:          m.seq,
	}
	provider.Migration = &plan
	return plan, nil
}

// Migration reports the running migration of one provider.
func (m *SamlFederationMapper) Migration(tenantID, idpID string) (MigrationPlan, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	provider, ok := m.tenantProviderLocked(tenantID, idpID)
	if !ok || provider.Migration == nil {
		return MigrationPlan{}, false
	}
	return *provider.Migration, true
}

// PickValue applies one rule to the raw assertion values.
func (m *SamlFederationMapper) PickValue(rule AttrRule, values []string) (string, error) {
	if len(values) == 0 {
		return "", ErrMissingAttr
	}

	strategy := rule.Multi
	if strategy == "" {
		strategy = MultiFirst
	}

	var value string
	switch strategy {
	case MultiFirst:
		value = values[0]
	case MultiJoin, MultiTruncate:
		selected := values
		if rule.MaxValues > 0 && len(selected) > rule.MaxValues {
			selected = selected[:rule.MaxValues]
		}
		value = strings.Join(selected, rule.Separator)
	default:
		return "", fmt.Errorf("%w: unsupported multi strategy %q", ErrBadRule, rule.Multi)
	}

	if rule.MaxLen > 0 && len(value) > rule.MaxLen {
		value = value[:rule.MaxLen]
	}
	return value, nil
}

// NormalizeNameID canonicalises one subject identifier for its format.
func (m *SamlFederationMapper) NormalizeNameID(format NameIDFormat, raw string) (string, error) {
	switch format {
	case NameIDEmail:
		normalized := strings.ToLower(removeWhitespace(raw))
		if strings.Count(normalized, "@") != 1 {
			return "", ErrBadNameID
		}
		local, domain, _ := strings.Cut(normalized, "@")
		if local == "" || domain == "" {
			return "", ErrBadNameID
		}
		return normalized, nil
	case NameIDPersistent, NameIDUnspecified:
		normalized := strings.TrimSpace(raw)
		if normalized == "" {
			return "", ErrBadNameID
		}
		return normalized, nil
	case NameIDTransient:
		return "", ErrBadNameID
	default:
		return "", ErrBadFormat
	}
}

// IdPCount reports how many providers one tenant has configured.
func (m *SamlFederationMapper) IdPCount(tenantID string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.tenants[tenantID])
}

// Snapshot serialises rules, bindings, migrations and the sequence.
func (m *SamlFederationMapper) Snapshot() ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	payload, err := json.Marshal(snapshotEnvelope{
		Version: snapshotVersion,
		Seq:     m.seq,
		Tenants: m.tenants,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	return payload, nil
}

// Restore replaces the whole mapper with a snapshot, all or nothing.
func (m *SamlFederationMapper) Restore(data []byte) error {
	var snapshot snapshotEnvelope
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	if err := validateSnapshot(&snapshot); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.seq = snapshot.Seq
	m.tenants = snapshot.Tenants
	return nil
}

func isBlank(value string) bool {
	return strings.TrimSpace(value) == ""
}

func removeWhitespace(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, value)
}

func bindingKey(nameID string) string {
	return strings.TrimSpace(nameID)
}

func emailKey(email string) string {
	return emailIndexPrefix + strings.ToLower(removeWhitespace(email))
}

func isKnownFormat(format NameIDFormat) bool {
	switch format {
	case NameIDPersistent, NameIDEmail, NameIDTransient, NameIDUnspecified:
		return true
	default:
		return false
	}
}

func validateRules(rules []AttrRule, maxRules int) ([]AttrRule, error) {
	if len(rules) > maxRules {
		return nil, ErrRuleLimit
	}

	stored := make([]AttrRule, len(rules))
	copy(stored, rules)
	names := make(map[string]struct{}, len(stored))
	for _, rule := range stored {
		switch rule.Multi {
		case "", MultiFirst, MultiJoin, MultiTruncate:
		default:
			return nil, ErrBadRule
		}
		if _, duplicated := names[rule.Name]; duplicated {
			return nil, ErrDuplicateRule
		}
		names[rule.Name] = struct{}{}
	}
	return stored, nil
}

func newProviderState(rules []AttrRule) *providerState {
	return &providerState{
		Rules:      append([]AttrRule{}, rules...),
		Bindings:   []Binding{},
		Accounts:   make(map[string]*accountState),
		nameIndex:  make(map[string]string),
		emailIndex: make(map[string]string),
	}
}

func (m *SamlFederationMapper) tenantProviderLocked(tenantID, idpID string) (*providerState, bool) {
	tenant, ok := m.tenants[tenantID]
	if !ok {
		return nil, false
	}
	provider, ok := tenant[idpID]
	return provider, ok && provider != nil
}

func (m *SamlFederationMapper) providerLocked(tenantID, idpID string) (*providerState, error) {
	provider, ok := m.tenantProviderLocked(tenantID, idpID)
	if !ok {
		return nil, ErrUnknownIdP
	}
	return provider, nil
}

func (p *providerState) ensureIndexes() {
	if p.nameIndex == nil {
		p.nameIndex = make(map[string]string)
	}
	if p.emailIndex == nil {
		p.emailIndex = make(map[string]string)
	}
	if p.Accounts == nil {
		p.Accounts = make(map[string]*accountState)
	}
}

func (m *SamlFederationMapper) mapAttributes(rules []AttrRule, raw map[string][]string) (map[string]string, string, error) {
	attributes := make(map[string]string, len(rules))
	email := ""

	for _, rule := range rules {
		value, err := m.PickValue(rule, raw[rule.Source])
		if err != nil {
			if errors.Is(err, ErrMissingAttr) && !rule.Required {
				continue
			}
			return nil, "", err
		}

		attributes[rule.Name] = value
		if email == "" && isEmailAttributeRule(rule) {
			email = value
		}
	}

	if explicitEmail, ok := attributes["email"]; ok && explicitEmail != "" {
		email = explicitEmail
	}
	return attributes, email, nil
}

func isEmailAttributeRule(rule AttrRule) bool {
	name := strings.ToLower(strings.TrimSpace(rule.Name))
	source := strings.ToLower(strings.TrimSpace(rule.Source))
	if name == "email" || name == "mail" {
		return true
	}
	if source == "email" || source == "mail" {
		return true
	}
	return strings.Contains(source, "emailaddress") || strings.HasSuffix(source, "/email")
}

func (m *SamlFederationMapper) resolveIdentityLocked(provider *providerState, nameID string, format NameIDFormat, email string) (userID string, created bool, shouldBind bool, err error) {
	if existingUserID, ok := provider.nameIndex[nameID]; ok {
		return existingUserID, false, false, nil
	}

	if format == NameIDEmail {
		for existingNameID, existingUserID := range provider.nameIndex {
			if strings.EqualFold(existingNameID, nameID) {
				provider.nameIndex[nameID] = existingUserID
				return existingUserID, false, false, nil
			}
		}
	}

	if email != "" {
		if existingUserID, ok := provider.emailIndex[emailKey(email)]; ok {
			if len(provider.Bindings)+1 > m.limits.MaxBindingsPerIdP {
				return "", false, false, ErrBindingLimit
			}
			return existingUserID, false, true, nil
		}
	}

	if len(provider.Bindings)+1 > m.limits.MaxBindingsPerIdP {
		return "", false, false, ErrBindingLimit
	}
	return "", true, true, nil
}

func validateSnapshot(snapshot *snapshotEnvelope) error {
	if snapshot.Version != snapshotVersion || snapshot.Tenants == nil || snapshot.Seq < 0 {
		return ErrBadSnapshot
	}

	var maxSeq int64
	for tenantID, providers := range snapshot.Tenants {
		if isBlank(tenantID) || providers == nil {
			return ErrBadSnapshot
		}
		for idpID, provider := range providers {
			if isBlank(idpID) || provider == nil {
				return ErrBadSnapshot
			}

			storedRules, err := validateRules(provider.Rules, len(provider.Rules))
			if err != nil {
				return ErrBadSnapshot
			}
			provider.Rules = storedRules
			if provider.Bindings == nil {
				provider.Bindings = []Binding{}
			}
			if provider.Accounts == nil {
				provider.Accounts = make(map[string]*accountState)
			}
			provider.nameIndex = make(map[string]string)
			provider.emailIndex = make(map[string]string)

			for i := range provider.Bindings {
				binding := &provider.Bindings[i]
				if binding.Seq <= 0 || isBlank(binding.NameID) || isBlank(binding.UserID) {
					return ErrBadSnapshot
				}
				if binding.TenantID == "" {
					binding.TenantID = tenantID
				} else if binding.TenantID != tenantID {
					return ErrBadSnapshot
				}
				if binding.IdPID == "" {
					binding.IdPID = idpID
				} else if binding.IdPID != idpID {
					return ErrBadSnapshot
				}

				key := bindingKey(binding.NameID)
				if _, exists := provider.nameIndex[key]; exists {
					return ErrBadSnapshot
				}
				provider.nameIndex[key] = binding.UserID
				if binding.Seq > maxSeq {
					maxSeq = binding.Seq
				}
			}

			for userID, account := range provider.Accounts {
				if account == nil || account.UserID != userID {
					return ErrBadSnapshot
				}
				if account.Attributes == nil {
					account.Attributes = make(map[string]string)
				}
				if account.Email != "" {
					key := emailKey(account.Email)
					if existingUserID, exists := provider.emailIndex[key]; exists && existingUserID != userID {
						return ErrBadSnapshot
					}
					provider.emailIndex[key] = userID
				}
			}

			if provider.Migration != nil {
				plan := provider.Migration
				if plan.TenantID == "" {
					plan.TenantID = tenantID
				} else if plan.TenantID != tenantID {
					return ErrBadSnapshot
				}
				if plan.IdPID == "" {
					plan.IdPID = idpID
				} else if plan.IdPID != idpID {
					return ErrBadSnapshot
				}
				if !isKnownFormat(plan.From) || !isKnownFormat(plan.To) || plan.From == plan.To ||
					plan.Seq <= 0 || plan.OverlapUntil.IsZero() {
					return ErrBadSnapshot
				}
				if plan.Seq > maxSeq {
					maxSeq = plan.Seq
				}
			}
		}
	}

	if snapshot.Seq < maxSeq {
		snapshot.Seq = maxSeq
	}
	return nil
}
