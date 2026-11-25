// Package task publishes service provider metadata versions for each tenant and
// rotates the signing certificates, keeping the old and the new certificate
// acceptable for a bounded overlap window.
package task

import (
	"errors"
	"time"
)

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Version states.
const (
	VersionDraft   = "draft"
	VersionActive  = "active"
	VersionRetired = "retired"
)

// Errors reported by the rotator.
var (
	ErrBlankArgument  = errors.New("tenant id must not be blank")
	ErrBlankEntityID  = errors.New("metadata entity id is required")
	ErrNoCertificate  = errors.New("metadata must carry at least one certificate")
	ErrCertExpired    = errors.New("certificate is already expired")
	ErrNotAllowed     = errors.New("entity id or acs url is not on the tenant allowlist")
	ErrBadAllowlist   = errors.New("allowlist must contain at least one entity id and one acs url")
	ErrUnknownVersion = errors.New("metadata version not found")
	ErrVersionLimit   = errors.New("metadata version limit reached for this tenant")
	ErrCertLimit      = errors.New("live certificate limit reached for this tenant")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// Options bounds one rotator and supplies its clock.
type Options struct {
	Now                  func() time.Time
	MaxVersionsPerTenant int
	MaxCertsPerTenant    int
	CertOverlap          time.Duration
}

// CertRef is one signing certificate known to a tenant.
type CertRef struct {
	ID       string    `json:"id"`
	NotAfter time.Time `json:"not_after"`
	PEM      string    `json:"pem"`
}

// Metadata is the service provider metadata document to publish.
type Metadata struct {
	EntityID             string    `json:"entity_id"`
	ACSURLs              []string  `json:"acs_urls"`
	Certificates         []CertRef `json:"certificates"`
	ValidUntil           time.Time `json:"valid_until"`
	WantAssertionsSigned bool      `json:"want_assertions_signed"`
}

// Version is one published metadata document.
type Version struct {
	TenantID    string    `json:"tenant_id"`
	Version     string    `json:"version"`
	EntityID    string    `json:"entity_id"`
	ACSURLs     []string  `json:"acs_urls"`
	Certs       []CertRef `json:"certs"`
	PublishedAt time.Time `json:"published_at"`
	Status      string    `json:"status"`
	Seq         int64     `json:"seq"`
}

// SamlSpMetadataRotator owns the tenant allowlists, the published versions and
// the live certificate set.
type SamlSpMetadataRotator struct {
	opts Options
}

// NewSamlSpMetadataRotator builds a rotator with the given options.
func NewSamlSpMetadataRotator(opts Options) *SamlSpMetadataRotator {
	return &SamlSpMetadataRotator{opts: opts}
}

// SetAllowlist replaces the entity ids and ACS urls a tenant may publish.
func (r *SamlSpMetadataRotator) SetAllowlist(tenantID string, entityIDs, acsURLs []string) error {
	return errNotImplemented
}

// Allowlist returns the normalised allowlist of one tenant.
func (r *SamlSpMetadataRotator) Allowlist(tenantID string) ([]string, []string) {
	return nil, nil
}

// Validate checks one metadata document on its own.
func (r *SamlSpMetadataRotator) Validate(md Metadata) error {
	return errNotImplemented
}

// Publish stores one metadata document as the next version of a tenant.
func (r *SamlSpMetadataRotator) Publish(tenantID string, md Metadata) (Version, error) {
	return Version{}, errNotImplemented
}

// Versions lists the published versions of a tenant, oldest first.
func (r *SamlSpMetadataRotator) Versions(tenantID string) []Version {
	return nil
}

// Active returns the version a tenant currently serves.
func (r *SamlSpMetadataRotator) Active(tenantID string) (Version, bool) {
	return Version{}, false
}

// Activate makes one published version the live one.
func (r *SamlSpMetadataRotator) Activate(tenantID, version string) error {
	return errNotImplemented
}

// Rollback makes a previously live version live again.
func (r *SamlSpMetadataRotator) Rollback(tenantID, version string) error {
	return errNotImplemented
}

// Certs lists the certificates a tenant accepts right now.
func (r *SamlSpMetadataRotator) Certs(tenantID string) []CertRef {
	return nil
}

// Snapshot serialises versions, active pointers, allowlists and the sequence.
func (r *SamlSpMetadataRotator) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole rotator with a snapshot, all or nothing.
func (r *SamlSpMetadataRotator) Restore(data []byte) error {
	return errNotImplemented
}
