package task

import (
	"errors"
	"testing"
	"time"

	impl "gsb/batch49-go-safety-a-19"
)

var rotatorBase = time.Date(2026, 6, 1, 7, 0, 0, 0, time.UTC)

type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.now = c.now.Add(d)
}

func rotatorOptions(clk *fakeClock) impl.Options {
	return impl.Options{
		Now:                  clk.Now,
		MaxVersionsPerTenant: 12,
		MaxCertsPerTenant:    6,
		CertOverlap:          30 * time.Minute,
	}
}

func rotatorCert(id string, notAfter time.Time) impl.CertRef {
	return impl.CertRef{ID: id, NotAfter: notAfter, PEM: "-----BEGIN CERTIFICATE-----\n" + id + "\n-----END CERTIFICATE-----"}
}

func rotatorMetadata(entity string, certs ...impl.CertRef) impl.Metadata {
	return impl.Metadata{
		EntityID:     entity,
		ACSURLs:      []string{"https://sp.example/acs"},
		Certificates: certs,
	}
}

func newRotator(t *testing.T, clk *fakeClock, opts impl.Options) *impl.SamlSpMetadataRotator {
	t.Helper()
	r := impl.NewSamlSpMetadataRotator(opts)
	if err := r.SetAllowlist("tenant-1", []string{"sp-a"}, []string{"https://sp.example/acs"}); err != nil {
		t.Fatalf("set allowlist: %v", err)
	}
	return r
}

func mustPublish(t *testing.T, r *impl.SamlSpMetadataRotator, tenant string, md impl.Metadata) impl.Version {
	t.Helper()
	v, err := r.Publish(tenant, md)
	if err != nil {
		t.Fatalf("publish %s: %v", tenant, err)
	}
	return v
}

func TestSetAllowlistNormalizesAndRejectsBadInput(t *testing.T) {
	clk := &fakeClock{now: rotatorBase}
	r := impl.NewSamlSpMetadataRotator(rotatorOptions(clk))
	if err := r.SetAllowlist("tenant-1", []string{"sp-b", "sp-a", "sp-a"}, []string{"https://sp.example/acs", "https://sp.example/acs"}); err != nil {
		t.Fatalf("set allowlist: %v", err)
	}
	entities, acs := r.Allowlist("tenant-1")
	if len(entities) != 2 || entities[0] != "sp-a" || entities[1] != "sp-b" {
		t.Fatalf("entity allowlist = %v, want the deduplicated sorted pair sp-a,sp-b", entities)
	}
	if len(acs) != 1 || acs[0] != "https://sp.example/acs" {
		t.Fatalf("acs allowlist = %v, want exactly one deduplicated url", acs)
	}
	if err := r.SetAllowlist("   ", []string{"sp-a"}, []string{"https://sp.example/acs"}); !errors.Is(err, impl.ErrBlankArgument) {
		t.Fatalf("blank tenant must fail with ErrBlankArgument, got %v", err)
	}
	if err := r.SetAllowlist("tenant-2", nil, nil); !errors.Is(err, impl.ErrBadAllowlist) {
		t.Fatalf("empty allowlist must fail with ErrBadAllowlist, got %v", err)
	}
	if err := r.SetAllowlist("tenant-2", []string{"sp-a"}, nil); !errors.Is(err, impl.ErrBadAllowlist) {
		t.Fatalf("an allowlist without acs urls must fail with ErrBadAllowlist, got %v", err)
	}
}

func TestPublishCreatesDraftVersion(t *testing.T) {
	clk := &fakeClock{now: rotatorBase}
	r := newRotator(t, clk, rotatorOptions(clk))
	cert := rotatorCert("cert-old", rotatorBase.Add(365*24*time.Hour))
	version := mustPublish(t, r, "tenant-1", rotatorMetadata("sp-a", cert))
	if version.Version != "v1" {
		t.Fatalf("first version id = %q, want v1", version.Version)
	}
	if version.Status != impl.VersionDraft {
		t.Fatalf("published status = %q, want %q", version.Status, impl.VersionDraft)
	}
	if !version.PublishedAt.Equal(rotatorBase) {
		t.Fatalf("published at = %v, want %v", version.PublishedAt, rotatorBase)
	}
	if version.Seq <= 0 {
		t.Fatalf("published sequence = %d, want a positive number", version.Seq)
	}
	if version.TenantID != "tenant-1" || version.EntityID != "sp-a" {
		t.Fatalf("published version lost its scope: %+v", version)
	}
	if len(version.Certs) != 1 || version.Certs[0].ID != "cert-old" {
		t.Fatalf("published certs = %+v, want exactly cert-old", version.Certs)
	}
	if got := r.Versions("tenant-1"); len(got) != 1 {
		t.Fatalf("stored versions = %d, want 1", len(got))
	}
	if _, ok := r.Active("tenant-1"); ok {
		t.Fatalf("a freshly published version must not be active yet")
	}
}

func TestPublishRejectsBlankTenant(t *testing.T) {
	clk := &fakeClock{now: rotatorBase}
	r := newRotator(t, clk, rotatorOptions(clk))
	cert := rotatorCert("cert-old", rotatorBase.Add(365*24*time.Hour))
	if _, err := r.Publish("   ", rotatorMetadata("sp-a", cert)); !errors.Is(err, impl.ErrBlankArgument) {
		t.Fatalf("blank tenant must fail with ErrBlankArgument, got %v", err)
	}
	if _, err := r.Publish("", rotatorMetadata("sp-a", cert)); !errors.Is(err, impl.ErrBlankArgument) {
		t.Fatalf("empty tenant must fail with ErrBlankArgument, got %v", err)
	}
	if got := r.Versions("tenant-1"); len(got) != 0 {
		t.Fatalf("rejected publishes must not store versions, got %d", len(got))
	}
}

func TestValidateRejectsBlankEntityAndEmptyCerts(t *testing.T) {
	clk := &fakeClock{now: rotatorBase}
	r := newRotator(t, clk, rotatorOptions(clk))
	if err := r.Validate(impl.Metadata{ACSURLs: []string{"https://sp.example/acs"}}); !errors.Is(err, impl.ErrBlankEntityID) {
		t.Fatalf("metadata without an entity id must fail with ErrBlankEntityID, got %v", err)
	}
	if err := r.Validate(impl.Metadata{EntityID: "sp-a"}); !errors.Is(err, impl.ErrNoCertificate) {
		t.Fatalf("metadata without certificates must fail with ErrNoCertificate, got %v", err)
	}
	cert := rotatorCert("cert-old", rotatorBase.Add(time.Hour))
	if err := r.Validate(rotatorMetadata("sp-a", cert)); err != nil {
		t.Fatalf("valid metadata must pass Validate, got %v", err)
	}
}

func TestValidateRejectsExpiredCertificate(t *testing.T) {
	clk := &fakeClock{now: rotatorBase}
	r := newRotator(t, clk, rotatorOptions(clk))
	expired := rotatorCert("cert-gone", rotatorBase.Add(-time.Hour))
	if err := r.Validate(rotatorMetadata("sp-a", expired)); !errors.Is(err, impl.ErrCertExpired) {
		t.Fatalf("expired certificate must fail with ErrCertExpired, got %v", err)
	}
	edge := rotatorCert("cert-edge", rotatorBase)
	if err := r.Validate(rotatorMetadata("sp-a", edge)); !errors.Is(err, impl.ErrCertExpired) {
		t.Fatalf("a certificate expiring exactly now must fail with ErrCertExpired, got %v", err)
	}
	fresh := rotatorCert("cert-ok", rotatorBase.Add(time.Minute))
	if err := r.Validate(rotatorMetadata("sp-a", fresh)); err != nil {
		t.Fatalf("a live certificate must pass Validate, got %v", err)
	}
}

func TestPublishEnforcesVersionLimit(t *testing.T) {
	clk := &fakeClock{now: rotatorBase}
	opts := rotatorOptions(clk)
	opts.MaxVersionsPerTenant = 2
	r := newRotator(t, clk, opts)
	cert := rotatorCert("cert-old", rotatorBase.Add(365*24*time.Hour))
	mustPublish(t, r, "tenant-1", rotatorMetadata("sp-a", cert))
	mustPublish(t, r, "tenant-1", rotatorMetadata("sp-a", cert))
	if _, err := r.Publish("tenant-1", rotatorMetadata("sp-a", cert)); !errors.Is(err, impl.ErrVersionLimit) {
		t.Fatalf("third version must fail with ErrVersionLimit, got %v", err)
	}
	if got := r.Versions("tenant-1"); len(got) != 2 {
		t.Fatalf("stored versions = %d, want the configured cap 2", len(got))
	}
	if got := r.Versions("tenant-2"); len(got) != 0 {
		t.Fatalf("the version cap is per tenant, got %d for tenant-2", len(got))
	}
}

func TestActivateMakesVersionActive(t *testing.T) {
	clk := &fakeClock{now: rotatorBase}
	r := newRotator(t, clk, rotatorOptions(clk))
	cert := rotatorCert("cert-old", rotatorBase.Add(365*24*time.Hour))
	mustPublish(t, r, "tenant-1", rotatorMetadata("sp-a", cert))
	if err := r.Activate("tenant-1", "v1"); err != nil {
		t.Fatalf("activate: %v", err)
	}
	active, ok := r.Active("tenant-1")
	if !ok {
		t.Fatalf("active version must be readable after activation")
	}
	if active.Version != "v1" || active.Status != impl.VersionActive {
		t.Fatalf("active version = %+v, want v1 with status %q", active, impl.VersionActive)
	}
	versions := r.Versions("tenant-1")
	if len(versions) != 1 || versions[0].Status != impl.VersionActive {
		t.Fatalf("stored versions = %+v, want v1 active", versions)
	}
}

func TestActivateUnknownVersion(t *testing.T) {
	clk := &fakeClock{now: rotatorBase}
	r := newRotator(t, clk, rotatorOptions(clk))
	cert := rotatorCert("cert-old", rotatorBase.Add(365*24*time.Hour))
	mustPublish(t, r, "tenant-1", rotatorMetadata("sp-a", cert))
	if err := r.Activate("tenant-1", "v99"); !errors.Is(err, impl.ErrUnknownVersion) {
		t.Fatalf("unknown version must fail with ErrUnknownVersion, got %v", err)
	}
	if err := r.Activate("tenant-1", ""); !errors.Is(err, impl.ErrUnknownVersion) {
		t.Fatalf("empty version must fail with ErrUnknownVersion, got %v", err)
	}
	if _, ok := r.Active("tenant-1"); ok {
		t.Fatalf("failed activation must not set an active version")
	}
}

func TestActivateRetiresPreviousVersion(t *testing.T) {
	clk := &fakeClock{now: rotatorBase}
	r := newRotator(t, clk, rotatorOptions(clk))
	oldCert := rotatorCert("cert-old", rotatorBase.Add(365*24*time.Hour))
	newCert := rotatorCert("cert-new", rotatorBase.Add(365*24*time.Hour))
	mustPublish(t, r, "tenant-1", rotatorMetadata("sp-a", oldCert))
	if err := r.Activate("tenant-1", "v1"); err != nil {
		t.Fatalf("activate v1: %v", err)
	}
	mustPublish(t, r, "tenant-1", rotatorMetadata("sp-a", newCert))
	if err := r.Activate("tenant-1", "v2"); err != nil {
		t.Fatalf("activate v2: %v", err)
	}
	active, ok := r.Active("tenant-1")
	if !ok || active.Version != "v2" {
		t.Fatalf("active version = %+v ok=%v, want v2", active, ok)
	}
	statuses := map[string]string{}
	for _, v := range r.Versions("tenant-1") {
		statuses[v.Version] = v.Status
	}
	if statuses["v1"] != impl.VersionRetired {
		t.Fatalf("previous version status = %q, want %q", statuses["v1"], impl.VersionRetired)
	}
	if statuses["v2"] != impl.VersionActive {
		t.Fatalf("new version status = %q, want %q", statuses["v2"], impl.VersionActive)
	}
}

func TestActivateEnforcesCertLimit(t *testing.T) {
	clk := &fakeClock{now: rotatorBase}
	opts := rotatorOptions(clk)
	opts.MaxCertsPerTenant = 2
	r := newRotator(t, clk, opts)
	mustPublish(t, r, "tenant-1", rotatorMetadata("sp-a",
		rotatorCert("cert-old-1", rotatorBase.Add(365*24*time.Hour)),
		rotatorCert("cert-old-2", rotatorBase.Add(365*24*time.Hour))))
	if err := r.Activate("tenant-1", "v1"); err != nil {
		t.Fatalf("activate v1: %v", err)
	}
	mustPublish(t, r, "tenant-1", rotatorMetadata("sp-a",
		rotatorCert("cert-new-1", rotatorBase.Add(365*24*time.Hour)),
		rotatorCert("cert-new-2", rotatorBase.Add(365*24*time.Hour))))
	if err := r.Activate("tenant-1", "v2"); !errors.Is(err, impl.ErrCertLimit) {
		t.Fatalf("activating past the certificate cap must fail with ErrCertLimit, got %v", err)
	}
	active, ok := r.Active("tenant-1")
	if !ok || active.Version != "v1" {
		t.Fatalf("failed activation must keep v1 active, got %+v ok=%v", active, ok)
	}
}

func TestCertsUnionDuringOverlap(t *testing.T) {
	clk := &fakeClock{now: rotatorBase}
	opts := rotatorOptions(clk)
	opts.MaxCertsPerTenant = 4
	r := newRotator(t, clk, opts)
	oldCert := rotatorCert("cert-old", rotatorBase.Add(365*24*time.Hour))
	newCert := rotatorCert("cert-new", rotatorBase.Add(365*24*time.Hour))
	mustPublish(t, r, "tenant-1", rotatorMetadata("sp-a", oldCert))
	if err := r.Activate("tenant-1", "v1"); err != nil {
		t.Fatalf("activate v1: %v", err)
	}
	clk.Advance(10 * time.Minute)
	mustPublish(t, r, "tenant-1", rotatorMetadata("sp-a", newCert))
	if err := r.Activate("tenant-1", "v2"); err != nil {
		t.Fatalf("activate v2: %v", err)
	}
	certs := r.Certs("tenant-1")
	if len(certs) != 2 {
		t.Fatalf("live certificates during the overlap = %d, want old and new", len(certs))
	}
	if certs[0].ID != "cert-new" || certs[1].ID != "cert-old" {
		t.Fatalf("live certificates = %+v, want the id sorted pair cert-new,cert-old", certs)
	}
	clk.Advance(10 * time.Minute)
	if again := r.Certs("tenant-1"); len(again) != 2 {
		t.Fatalf("live certificates inside the overlap = %d, want 2", len(again))
	}
	active, ok := r.Active("tenant-1")
	if !ok || len(active.Certs) != 1 || active.Certs[0].ID != "cert-new" {
		t.Fatalf("the active version must only carry its own certificate, got %+v ok=%v", active, ok)
	}
}

func TestRollbackRestoresRetiredVersion(t *testing.T) {
	clk := &fakeClock{now: rotatorBase}
	r := newRotator(t, clk, rotatorOptions(clk))
	oldCert := rotatorCert("cert-old", rotatorBase.Add(365*24*time.Hour))
	newCert := rotatorCert("cert-new", rotatorBase.Add(365*24*time.Hour))
	mustPublish(t, r, "tenant-1", rotatorMetadata("sp-a", oldCert))
	if err := r.Activate("tenant-1", "v1"); err != nil {
		t.Fatalf("activate v1: %v", err)
	}
	mustPublish(t, r, "tenant-1", rotatorMetadata("sp-a", newCert))
	if err := r.Activate("tenant-1", "v2"); err != nil {
		t.Fatalf("activate v2: %v", err)
	}
	if err := r.Rollback("tenant-1", "v1"); err != nil {
		t.Fatalf("rollback to v1: %v", err)
	}
	active, ok := r.Active("tenant-1")
	if !ok || active.Version != "v1" {
		t.Fatalf("active version after rollback = %+v ok=%v, want v1", active, ok)
	}
	statuses := map[string]string{}
	for _, v := range r.Versions("tenant-1") {
		statuses[v.Version] = v.Status
	}
	if statuses["v1"] != impl.VersionActive || statuses["v2"] != impl.VersionRetired {
		t.Fatalf("statuses after rollback = %+v, want v1 active and v2 retired", statuses)
	}
}

func TestRollbackRejectsDraftVersion(t *testing.T) {
	clk := &fakeClock{now: rotatorBase}
	r := newRotator(t, clk, rotatorOptions(clk))
	cert := rotatorCert("cert-old", rotatorBase.Add(365*24*time.Hour))
	mustPublish(t, r, "tenant-1", rotatorMetadata("sp-a", cert))
	if err := r.Activate("tenant-1", "v1"); err != nil {
		t.Fatalf("activate v1: %v", err)
	}
	mustPublish(t, r, "tenant-1", rotatorMetadata("sp-a", cert))
	if err := r.Rollback("tenant-1", "v2"); !errors.Is(err, impl.ErrUnknownVersion) {
		t.Fatalf("rolling back to a draft version must fail with ErrUnknownVersion, got %v", err)
	}
	if err := r.Rollback("tenant-1", "v404"); !errors.Is(err, impl.ErrUnknownVersion) {
		t.Fatalf("rolling back to an unknown version must fail with ErrUnknownVersion, got %v", err)
	}
	active, ok := r.Active("tenant-1")
	if !ok || active.Version != "v1" {
		t.Fatalf("rejected rollback must keep v1 active, got %+v ok=%v", active, ok)
	}
}

func TestSnapshotRestoreKeepsActiveAndCerts(t *testing.T) {
	clk := &fakeClock{now: rotatorBase}
	opts := rotatorOptions(clk)
	opts.MaxCertsPerTenant = 4
	opts.CertOverlap = time.Hour
	r := newRotator(t, clk, opts)
	oldCert := rotatorCert("cert-old", rotatorBase.Add(365*24*time.Hour))
	newCert := rotatorCert("cert-new", rotatorBase.Add(365*24*time.Hour))
	mustPublish(t, r, "tenant-1", rotatorMetadata("sp-a", oldCert))
	if err := r.Activate("tenant-1", "v1"); err != nil {
		t.Fatalf("activate v1: %v", err)
	}
	clk.Advance(10 * time.Minute)
	mustPublish(t, r, "tenant-1", rotatorMetadata("sp-a", newCert))
	if err := r.Activate("tenant-1", "v2"); err != nil {
		t.Fatalf("activate v2: %v", err)
	}
	data, err := r.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload must not be empty")
	}
	restored := impl.NewSamlSpMetadataRotator(opts)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	active, ok := restored.Active("tenant-1")
	if !ok || active.Version != "v2" {
		t.Fatalf("restored active version = %+v ok=%v, want v2", active, ok)
	}
	if got := restored.Versions("tenant-1"); len(got) != 2 {
		t.Fatalf("restored versions = %d, want 2", len(got))
	}
	if certs := restored.Certs("tenant-1"); len(certs) != 2 {
		t.Fatalf("restored live certificates = %d, want the overlap pair", len(certs))
	}
	entities, acs := restored.Allowlist("tenant-1")
	if len(entities) != 1 || entities[0] != "sp-a" || len(acs) != 1 {
		t.Fatalf("restored allowlist = %v / %v, want sp-a and one acs url", entities, acs)
	}
	clk.Advance(10 * time.Minute)
	if certs := restored.Certs("tenant-1"); len(certs) != 2 {
		t.Fatalf("restored overlap deadline was not kept, got %d live certificates", len(certs))
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	clk := &fakeClock{now: rotatorBase}
	r := newRotator(t, clk, rotatorOptions(clk))
	cert := rotatorCert("cert-old", rotatorBase.Add(365*24*time.Hour))
	mustPublish(t, r, "tenant-1", rotatorMetadata("sp-a", cert))
	if err := r.Activate("tenant-1", "v1"); err != nil {
		t.Fatalf("activate v1: %v", err)
	}
	if err := r.Restore([]byte("<<<broken>>>")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	active, ok := r.Active("tenant-1")
	if !ok || active.Version != "v1" {
		t.Fatalf("rejected restore must keep the live version, got %+v ok=%v", active, ok)
	}
	if got := len(r.Versions("tenant-1")); got != 1 {
		t.Fatalf("rejected restore must not touch the version table, got %d", got)
	}
	if certs := r.Certs("tenant-1"); len(certs) != 1 || certs[0].ID != "cert-old" {
		t.Fatalf("rejected restore must not touch live certificates, got %+v", certs)
	}
}
