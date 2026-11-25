// Package task publishes service provider metadata versions for each tenant and
// rotates the signing certificates, keeping the old and the new certificate
// acceptable for a bounded overlap window.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Version states.
const (
	VersionDraft   = "draft"
	VersionActive  = "active"
	VersionRetired = "retired"
)

// snapshotFormat tags snapshot payloads produced by this rotator so that
// arbitrary JSON documents are not mistaken for valid state.
const snapshotFormat = "saml-sp-metadata-rotator/v1"

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

// certWindow keeps the certificates of a version that was just replaced
// acceptable until ExpiresAt. The deadline is an absolute instant so a
// restored snapshot keeps judging the overlap against its original end.
type certWindow struct {
	ExpiresAt time.Time `json:"expires_at"`
	Certs     []CertRef `json:"certs"`
}

// tenantState is the whole mutable state of one tenant.
type tenantState struct {
	entities   map[string]struct{}
	acsURLs    map[string]struct{}
	versions   []*Version
	byVersion  map[string]*Version
	activeID   string
	everActive map[string]struct{}
	windows    []certWindow
}

// SamlSpMetadataRotator owns the tenant allowlists, the published versions and
// the live certificate set. Every state transition and every read of the
// derived certificate union goes through the same mutex, so callers always
// observe a complete old or new active version.
type SamlSpMetadataRotator struct {
	opts Options

	mu      sync.Mutex
	seq     int64
	tenants map[string]*tenantState
}

// NewSamlSpMetadataRotator builds a rotator with the given options.
func NewSamlSpMetadataRotator(opts Options) *SamlSpMetadataRotator {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &SamlSpMetadataRotator{
		opts:    opts,
		tenants: make(map[string]*tenantState),
	}
}

func (r *SamlSpMetadataRotator) now() time.Time {
	if r.opts.Now != nil {
		return r.opts.Now()
	}
	return time.Now()
}

// canonicalTenant rejects blank tenant ids and normalises the map key.
func canonicalTenant(tenantID string) (string, error) {
	key := strings.TrimSpace(tenantID)
	if key == "" {
		return "", ErrBlankArgument
	}
	return key, nil
}

func newTenantState() *tenantState {
	return &tenantState{
		entities:   make(map[string]struct{}),
		acsURLs:    make(map[string]struct{}),
		byVersion:  make(map[string]*Version),
		everActive: make(map[string]struct{}),
	}
}

// dedupeSorted drops duplicate entries and returns a lexicographically sorted
// fresh slice.
func dedupeSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func sortedSetKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string(nil), values...)
}

func cloneCerts(certs []CertRef) []CertRef {
	if certs == nil {
		return nil
	}
	return append([]CertRef(nil), certs...)
}

func cloneVersion(version *Version) Version {
	return Version{
		TenantID:    version.TenantID,
		Version:     version.Version,
		EntityID:    version.EntityID,
		ACSURLs:     cloneStrings(version.ACSURLs),
		Certs:       cloneCerts(version.Certs),
		PublishedAt: version.PublishedAt,
		Status:      version.Status,
		Seq:         version.Seq,
	}
}

// SetAllowlist replaces the entity ids and ACS urls a tenant may publish.
func (r *SamlSpMetadataRotator) SetAllowlist(tenantID string, entityIDs, acsURLs []string) error {
	key, err := canonicalTenant(tenantID)
	if err != nil {
		return err
	}

	entities := dedupeSorted(entityIDs)
	acs := dedupeSorted(acsURLs)
	if len(entities) == 0 || len(acs) == 0 {
		return ErrBadAllowlist
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.tenants == nil {
		r.tenants = make(map[string]*tenantState)
	}
	state := newTenantState()
	for _, entityID := range entities {
		state.entities[entityID] = struct{}{}
	}
	for _, acsURL := range acs {
		state.acsURLs[acsURL] = struct{}{}
	}

	// Replacing an allowlist must preserve the published version table, the
	// active pointer and the overlap windows of the tenant.
	if existing := r.tenants[key]; existing != nil {
		existing.entities = state.entities
		existing.acsURLs = state.acsURLs
	} else {
		r.tenants[key] = state
	}
	return nil
}

// Allowlist returns the normalised allowlist of one tenant. The slices are
// independent copies: mutating them never affects internal state.
func (r *SamlSpMetadataRotator) Allowlist(tenantID string) ([]string, []string) {
	key := strings.TrimSpace(tenantID)

	r.mu.Lock()
	defer r.mu.Unlock()

	state := r.tenants[key]
	if state == nil {
		return []string{}, []string{}
	}
	return sortedSetKeys(state.entities), sortedSetKeys(state.acsURLs)
}

// Validate checks one metadata document on its own.
func (r *SamlSpMetadataRotator) Validate(md Metadata) error {
	if strings.TrimSpace(md.EntityID) == "" {
		return ErrBlankEntityID
	}
	if len(md.Certificates) == 0 {
		return ErrNoCertificate
	}
	now := r.now()
	for _, cert := range md.Certificates {
		// "not after the current instant" includes the exact boundary.
		if !now.Before(cert.NotAfter) {
			return ErrCertExpired
		}
	}
	return nil
}

// allowed reports whether the entity id and every ACS URL are on the tenant
// allowlist. A missing allowlist rejects everything.
func (ts *tenantState) allowed(md Metadata) bool {
	if ts == nil {
		return false
	}
	if _, ok := ts.entities[md.EntityID]; !ok {
		return false
	}
	for _, acsURL := range md.ACSURLs {
		if _, ok := ts.acsURLs[acsURL]; !ok {
			return false
		}
	}
	return true
}

// Publish stores one metadata document as the next version of a tenant.
func (r *SamlSpMetadataRotator) Publish(tenantID string, md Metadata) (Version, error) {
	key, err := canonicalTenant(tenantID)
	if err != nil {
		return Version{}, err
	}
	if err := r.Validate(md); err != nil {
		return Version{}, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	state := r.tenants[key]
	// The whole document is rejected when the entity id or any ACS URL is not
	// allowlisted; fields are never silently dropped.
	if !state.allowed(md) {
		return Version{}, ErrNotAllowed
	}
	if r.opts.MaxVersionsPerTenant > 0 && len(state.versions) >= r.opts.MaxVersionsPerTenant {
		return Version{}, ErrVersionLimit
	}

	// Version numbers are assigned only after every check passed, so rejected
	// publishes never consume a number.
	r.seq++
	version := &Version{
		TenantID:    key,
		Version:     fmt.Sprintf("v%d", len(state.versions)+1),
		EntityID:    md.EntityID,
		ACSURLs:     cloneStrings(md.ACSURLs),
		Certs:       cloneCerts(md.Certificates),
		PublishedAt: r.now(),
		Status:      VersionDraft,
		Seq:         r.seq,
	}
	state.versions = append(state.versions, version)
	state.byVersion[version.Version] = version

	return cloneVersion(version), nil
}

// Versions lists the published versions of a tenant, oldest first.
func (r *SamlSpMetadataRotator) Versions(tenantID string) []Version {
	key := strings.TrimSpace(tenantID)

	r.mu.Lock()
	defer r.mu.Unlock()

	state := r.tenants[key]
	if state == nil {
		return []Version{}
	}
	out := make([]Version, 0, len(state.versions))
	for _, version := range state.versions {
		out = append(out, cloneVersion(version))
	}
	return out
}

// Active returns the version a tenant currently serves.
func (r *SamlSpMetadataRotator) Active(tenantID string) (Version, bool) {
	key := strings.TrimSpace(tenantID)

	r.mu.Lock()
	defer r.mu.Unlock()

	state := r.tenants[key]
	if state == nil || state.activeID == "" {
		return Version{}, false
	}
	version := state.byVersion[state.activeID]
	if version == nil {
		return Version{}, false
	}
	return cloneVersion(version), true
}

// Activate makes one published version the live one.
func (r *SamlSpMetadataRotator) Activate(tenantID, version string) error {
	key, err := canonicalTenant(tenantID)
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	return r.switchLocked(key, version, false)
}

// Rollback makes a previously live version live again.
func (r *SamlSpMetadataRotator) Rollback(tenantID, version string) error {
	key, err := canonicalTenant(tenantID)
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	return r.switchLocked(key, version, true)
}

// pruneWindowsLocked drops overlap windows whose deadline has already been
// reached. Expired windows no longer contribute certificates.
func (ts *tenantState) pruneWindowsLocked(now time.Time) {
	kept := make([]certWindow, 0, len(ts.windows))
	for _, window := range ts.windows {
		if now.Before(window.ExpiresAt) {
			kept = append(kept, window)
		}
	}
	ts.windows = kept
}

// liveCertIDsLocked computes the unique certificate ids that would be
// acceptable immediately after switching to targetID: the target certificates,
// the current active certificates entering their overlap window and every
// still-running overlap window. Certificates past their own NotAfter do not
// count.
func (ts *tenantState) liveCertIDsLocked(target *Version, now time.Time) map[string]struct{} {
	ids := make(map[string]struct{})
	add := func(certs []CertRef) {
		for _, cert := range certs {
			if now.Before(cert.NotAfter) {
				ids[cert.ID] = struct{}{}
			}
		}
	}

	add(target.Certs)
	if ts.activeID != "" && ts.activeID != target.Version {
		if current := ts.byVersion[ts.activeID]; current != nil {
			add(current.Certs)
		}
	}
	for _, window := range ts.windows {
		if now.Before(window.ExpiresAt) {
			add(window.Certs)
		}
	}
	return ids
}

// switchLocked contains the shared Activate/Rollback transition. Every check
// runs before any mutation so a failed switch leaves the current active
// version and its certificates untouched.
func (r *SamlSpMetadataRotator) switchLocked(tenantKey, version string, rollback bool) error {
	state := r.tenants[tenantKey]
	var target *Version
	if state != nil {
		target = state.byVersion[version]
	}
	if target == nil {
		return ErrUnknownVersion
	}
	if rollback {
		if _, wasActive := state.everActive[version]; !wasActive {
			return ErrUnknownVersion
		}
	}

	now := r.now()

	// Never activate or roll back onto a document whose certificates are not
	// alive right now; rollback in particular must not silently renew them.
	for _, cert := range target.Certs {
		if !now.Before(cert.NotAfter) {
			return ErrCertExpired
		}
	}

	state.pruneWindowsLocked(now)
	union := state.liveCertIDsLocked(target, now)
	if r.opts.MaxCertsPerTenant > 0 && len(union) > r.opts.MaxCertsPerTenant {
		return ErrCertLimit
	}

	// All guards passed; commit the switch atomically.
	if state.activeID != "" && state.activeID != version {
		if current := state.byVersion[state.activeID]; current != nil {
			state.windows = append(state.windows, certWindow{
				ExpiresAt: now.Add(r.opts.CertOverlap),
				Certs:     cloneCerts(current.Certs),
			})
		}
	}

	for _, candidate := range state.versions {
		switch {
		case candidate.Version == version:
			candidate.Status = VersionActive
		case candidate.Status == VersionActive:
			// Exclusivity: only the target stays active.
			candidate.Status = VersionRetired
		}
	}

	state.activeID = version
	state.everActive[version] = struct{}{}
	return nil
}

// Certs lists the certificates a tenant accepts right now: the active
// version's own certificates plus the certificates replaced within the
// overlap window. Duplicate ids collapse to one entry and the result is
// sorted by certificate id.
func (r *SamlSpMetadataRotator) Certs(tenantID string) []CertRef {
	key := strings.TrimSpace(tenantID)

	r.mu.Lock()
	defer r.mu.Unlock()

	state := r.tenants[key]
	if state == nil {
		return []CertRef{}
	}

	now := r.now()
	state.pruneWindowsLocked(now)

	merged := make(map[string]CertRef)
	if state.activeID != "" {
		if active := state.byVersion[state.activeID]; active != nil {
			for _, cert := range active.Certs {
				if now.Before(cert.NotAfter) {
					merged[cert.ID] = cert
				}
			}
		}
	}
	// Overlap certificates only fill ids the active set does not already
	// carry, so a shared id is returned exactly once.
	for _, window := range state.windows {
		for _, cert := range window.Certs {
			if !now.Before(cert.NotAfter) {
				continue
			}
			if _, exists := merged[cert.ID]; !exists {
				merged[cert.ID] = cert
			}
		}
	}

	out := make([]CertRef, 0, len(merged))
	for _, cert := range merged {
		out = append(out, cert)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out
}

// snapshotWindow is the serialised overlap window.
type snapshotWindow struct {
	ExpiresAt time.Time `json:"expires_at"`
	Certs     []CertRef `json:"certs"`
}

// snapshotTenant is the serialised state of one tenant.
type snapshotTenant struct {
	Entities   []string         `json:"entities"`
	ACSURLs    []string         `json:"acs_urls"`
	ActiveID   string           `json:"active_id"`
	EverActive []string         `json:"ever_active"`
	Versions   []Version        `json:"versions"`
	Windows    []snapshotWindow `json:"overlap_windows"`
}

// snapshotFile is the whole rotator payload.
type snapshotFile struct {
	Format  string                    `json:"format"`
	Seq     int64                     `json:"seq"`
	Tenants map[string]snapshotTenant `json:"tenants"`
}

// Snapshot serialises versions, active pointers, allowlists, overlap deadlines
// and the publication sequence.
func (r *SamlSpMetadataRotator) Snapshot() ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	payload := snapshotFile{
		Format:  snapshotFormat,
		Seq:     r.seq,
		Tenants: make(map[string]snapshotTenant, len(r.tenants)),
	}
	for tenantID, state := range r.tenants {
		entry := snapshotTenant{
			Entities:   sortedSetKeys(state.entities),
			ACSURLs:    sortedSetKeys(state.acsURLs),
			ActiveID:   state.activeID,
			EverActive: sortedSetKeys(state.everActive),
			Versions:   make([]Version, 0, len(state.versions)),
			Windows:    make([]snapshotWindow, 0, len(state.windows)),
		}
		for _, version := range state.versions {
			entry.Versions = append(entry.Versions, cloneVersion(version))
		}
		for _, window := range state.windows {
			entry.Windows = append(entry.Windows, snapshotWindow{
				ExpiresAt: window.ExpiresAt,
				Certs:     cloneCerts(window.Certs),
			})
		}
		payload.Tenants[tenantID] = entry
	}
	return json.Marshal(payload)
}

// validateSnapshot performs referential checks on a decoded payload before it
// is allowed to replace live state.
func validateSnapshot(payload *snapshotFile) error {
	if payload.Format != snapshotFormat {
		return ErrBadSnapshot
	}
	if payload.Seq < 0 {
		return ErrBadSnapshot
	}
	for tenantID, tenant := range payload.Tenants {
		if strings.TrimSpace(tenantID) == "" {
			return ErrBadSnapshot
		}
		versionIDs := make(map[string]struct{}, len(tenant.Versions))
		for _, version := range tenant.Versions {
			if version.Version == "" || version.TenantID == "" {
				return ErrBadSnapshot
			}
			if _, duplicated := versionIDs[version.Version]; duplicated {
				return ErrBadSnapshot
			}
			versionIDs[version.Version] = struct{}{}
		}
		if tenant.ActiveID != "" {
			if _, exists := versionIDs[tenant.ActiveID]; !exists {
				return ErrBadSnapshot
			}
		}
		for _, versionID := range tenant.EverActive {
			if _, exists := versionIDs[versionID]; !exists {
				return ErrBadSnapshot
			}
		}
	}
	return nil
}

// Restore replaces the whole rotator with a snapshot, all or nothing. A
// corrupt payload returns ErrBadSnapshot without touching live versions or
// certificates.
func (r *SamlSpMetadataRotator) Restore(data []byte) error {
	var payload snapshotFile
	if err := json.Unmarshal(data, &payload); err != nil {
		return ErrBadSnapshot
	}
	if err := validateSnapshot(&payload); err != nil {
		return ErrBadSnapshot
	}

	// Build the replacement entirely before taking the lock so a semantic
	// failure can never leave the live rotator half replaced.
	nextTenants := make(map[string]*tenantState, len(payload.Tenants))
	for tenantID, tenant := range payload.Tenants {
		state := newTenantState()
		for _, entityID := range tenant.Entities {
			state.entities[entityID] = struct{}{}
		}
		for _, acsURL := range tenant.ACSURLs {
			state.acsURLs[acsURL] = struct{}{}
		}
		for _, versionID := range tenant.EverActive {
			state.everActive[versionID] = struct{}{}
		}
		state.activeID = tenant.ActiveID
		for index := range tenant.Versions {
			version := tenant.Versions[index]
			version.ACSURLs = cloneStrings(version.ACSURLs)
			version.Certs = cloneCerts(version.Certs)
			state.versions = append(state.versions, &version)
			state.byVersion[version.Version] = &version
		}
		for _, window := range tenant.Windows {
			state.windows = append(state.windows, certWindow{
				ExpiresAt: window.ExpiresAt,
				Certs:     cloneCerts(window.Certs),
			})
		}
		nextTenants[tenantID] = state
	}

	r.mu.Lock()
	r.tenants = nextTenants
	r.seq = payload.Seq
	r.mu.Unlock()
	return nil
}
