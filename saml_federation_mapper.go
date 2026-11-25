// Package task maps SAML assertions from several identity providers onto local
// accounts, keeps the per provider attribute rules and tracks a NameID format
// migration that runs both identifiers side by side.
package task

import (
	"errors"
	"time"
)

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

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

// SamlFederationMapper owns the rules, the bindings and the migration state.
type SamlFederationMapper struct {
	limits Limits
}

// NewSamlFederationMapper builds a mapper with the given caps.
func NewSamlFederationMapper(limits Limits) *SamlFederationMapper {
	return &SamlFederationMapper{limits: limits}
}

// Configure replaces the attribute rules of one provider.
func (m *SamlFederationMapper) Configure(tenantID, idpID string, rules []AttrRule) error {
	return errNotImplemented
}

// Attributes returns the rules of one provider in configuration order.
func (m *SamlFederationMapper) Attributes(tenantID, idpID string) ([]AttrRule, error) {
	return nil, errNotImplemented
}

// Map turns one assertion into a local identity.
func (m *SamlFederationMapper) Map(tenantID, idpID string, a Assertion) (Identity, error) {
	return Identity{}, errNotImplemented
}

// Bind ties one name identifier to one local account.
func (m *SamlFederationMapper) Bind(tenantID, idpID, nameID, userID string) error {
	return errNotImplemented
}

// Unbind removes one name identifier binding.
func (m *SamlFederationMapper) Unbind(tenantID, idpID, nameID string) error {
	return errNotImplemented
}

// Bindings lists the bindings of one provider, oldest first.
func (m *SamlFederationMapper) Bindings(tenantID, idpID string) []Binding {
	return nil
}

// Migrate opens a name identifier format migration for one provider.
func (m *SamlFederationMapper) Migrate(tenantID, idpID string, from, to NameIDFormat) (MigrationPlan, error) {
	return MigrationPlan{}, errNotImplemented
}

// MigrateAt opens a migration with a caller supplied issue time.
func (m *SamlFederationMapper) MigrateAt(tenantID, idpID string, from, to NameIDFormat, now time.Time) (MigrationPlan, error) {
	return MigrationPlan{}, errNotImplemented
}

// Migration reports the running migration of one provider.
func (m *SamlFederationMapper) Migration(tenantID, idpID string) (MigrationPlan, bool) {
	return MigrationPlan{}, false
}

// PickValue applies one rule to the raw assertion values.
func (m *SamlFederationMapper) PickValue(rule AttrRule, values []string) (string, error) {
	return "", errNotImplemented
}

// NormalizeNameID canonicalises one subject identifier for its format.
func (m *SamlFederationMapper) NormalizeNameID(format NameIDFormat, raw string) (string, error) {
	return "", errNotImplemented
}

// IdPCount reports how many providers one tenant has configured.
func (m *SamlFederationMapper) IdPCount(tenantID string) int {
	return 0
}

// Snapshot serialises rules, bindings, migrations and the sequence.
func (m *SamlFederationMapper) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole mapper with a snapshot, all or nothing.
func (m *SamlFederationMapper) Restore(data []byte) error {
	return errNotImplemented
}
