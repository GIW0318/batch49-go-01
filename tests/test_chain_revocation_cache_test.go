package task

import (
	"errors"
	"reflect"
	"testing"
	"time"

	impl "gsb/batch49-go-fix-a-03"
)

var certBase = time.Date(2026, 5, 12, 8, 0, 0, 0, time.UTC)

func cacheConfig() impl.ChainConfig {
	return impl.ChainConfig{
		MaxChainLen:     4,
		MaxCacheEntries: 8,
		StatusTTL:       5 * time.Minute,
		MaxStaleness:    10 * time.Minute,
	}
}

func anchorCert() impl.CertInfo {
	return impl.CertInfo{
		Subject:    "CN=Root CA",
		Issuer:     "CN=Root CA",
		Serial:     "root-1",
		IsCA:       true,
		NotBefore:  certBase.Add(-24 * time.Hour),
		NotAfter:   certBase.Add(9000 * time.Hour),
	}
}

func intermediateCert() impl.CertInfo {
	return impl.CertInfo{
		Subject:    "CN=Inter CA",
		Issuer:     "CN=Root CA",
		Serial:     "inter-1",
		IsCA:       true,
		PathLen:    0,
		PathLenSet: true,
		NotBefore:  certBase.Add(-24 * time.Hour),
		NotAfter:   certBase.Add(9000 * time.Hour),
	}
}

func leafCert() impl.CertInfo {
	return impl.CertInfo{
		Subject:            "CN=api.example.com",
		Issuer:             "CN=Inter CA",
		Serial:             "leaf-1",
		DNSNames:           []string{"api.example.com", "*.svc.example.com"},
		NotBefore:          certBase.Add(-time.Hour),
		NotAfter:           certBase.Add(720 * time.Hour),
		KeyUsageServerAuth: true,
	}
}

func newVerifier() *impl.ChainRevocationCache {
	c := impl.NewChainRevocationCache(cacheConfig())
	c.SetNow(certBase)
	_ = c.AddTrustAnchor(anchorCert())
	return c
}

func verifierWithStatus(t *testing.T) *impl.ChainRevocationCache {
	t.Helper()
	c := newVerifier()
	if err := c.RecordStatus("CN=Inter CA", "leaf-1", impl.StatusGood, "ocsp"); err != nil {
		t.Fatalf("record good status: %v", err)
	}
	return c
}

func TestVerifyAcceptsValidChain(t *testing.T) {
	c := verifierWithStatus(t)
	v, err := c.Verify([]impl.CertInfo{leafCert(), intermediateCert()}, "api.example.com")
	if err != nil {
		t.Fatalf("valid chain must be accepted, got %v", err)
	}
	if !v.Trusted {
		t.Fatalf("verdict must be trusted: %+v", v)
	}
	if v.LeafSerial != "leaf-1" {
		t.Fatalf("leaf serial = %q, want leaf-1", v.LeafSerial)
	}
	if v.ChainLen != 3 {
		t.Fatalf("chain length = %d, want 3 including the anchor", v.ChainLen)
	}
	if v.Revocation != impl.StatusGood {
		t.Fatalf("revocation = %q, want %q", v.Revocation, impl.StatusGood)
	}
	if !reflect.DeepEqual(c.Anchors(), []string{"CN=Root CA"}) {
		t.Fatalf("anchors = %v, want the pinned root only", c.Anchors())
	}
}

func TestVerifyRejectsEmptyAndOverlongChains(t *testing.T) {
	c := verifierWithStatus(t)
	if _, err := c.Verify(nil, "api.example.com"); !errors.Is(err, impl.ErrEmptyChain) {
		t.Fatalf("empty chain must be rejected, got %v", err)
	}
	short := impl.NewChainRevocationCache(impl.ChainConfig{
		MaxChainLen:     1,
		MaxCacheEntries: 8,
		StatusTTL:       time.Minute,
		MaxStaleness:    time.Minute,
	})
	short.SetNow(certBase)
	if err := short.AddTrustAnchor(anchorCert()); err != nil {
		t.Fatalf("add anchor: %v", err)
	}
	if err := short.RecordStatus("CN=Inter CA", "leaf-1", impl.StatusGood, "ocsp"); err != nil {
		t.Fatalf("record status: %v", err)
	}
	if _, err := short.Verify([]impl.CertInfo{leafCert(), intermediateCert()}, "api.example.com"); !errors.Is(err, impl.ErrChainTooLong) {
		t.Fatalf("overlong chain must be rejected, got %v", err)
	}
}

func TestVerifyRejectsHostMismatch(t *testing.T) {
	c := verifierWithStatus(t)
	if _, err := c.Verify([]impl.CertInfo{leafCert(), intermediateCert()}, "api.other.example"); !errors.Is(err, impl.ErrHostMismatch) {
		t.Fatalf("host outside the SAN list must be rejected, got %v", err)
	}
}

func TestVerifyRejectsOutsideValidityWindow(t *testing.T) {
	c := verifierWithStatus(t)
	expired := leafCert()
	expired.NotAfter = certBase.Add(-time.Minute)
	if _, err := c.Verify([]impl.CertInfo{expired, intermediateCert()}, "api.example.com"); !errors.Is(err, impl.ErrCertExpired) {
		t.Fatalf("expired leaf must be rejected, got %v", err)
	}
	future := leafCert()
	future.NotBefore = certBase.Add(time.Hour)
	if _, err := c.Verify([]impl.CertInfo{future, intermediateCert()}, "api.example.com"); !errors.Is(err, impl.ErrCertNotYetValid) {
		t.Fatalf("not yet valid leaf must be rejected, got %v", err)
	}
}

func TestVerifyRejectsCALeaf(t *testing.T) {
	c := verifierWithStatus(t)
	leaf := leafCert()
	leaf.IsCA = true
	if _, err := c.Verify([]impl.CertInfo{leaf, intermediateCert()}, "api.example.com"); !errors.Is(err, impl.ErrLeafIsCA) {
		t.Fatalf("CA leaf must be rejected, got %v", err)
	}
}

func TestVerifyRejectsBrokenLink(t *testing.T) {
	c := verifierWithStatus(t)
	inter := intermediateCert()
	inter.Subject = "CN=Other CA"
	if _, err := c.Verify([]impl.CertInfo{leafCert(), inter}, "api.example.com"); !errors.Is(err, impl.ErrChainBroken) {
		t.Fatalf("issuer subject mismatch must break the chain, got %v", err)
	}
}

func TestVerifyRejectsNonCAIssuer(t *testing.T) {
	c := verifierWithStatus(t)
	inter := intermediateCert()
	inter.IsCA = false
	if _, err := c.Verify([]impl.CertInfo{leafCert(), inter}, "api.example.com"); !errors.Is(err, impl.ErrNotCA) {
		t.Fatalf("non CA issuer must be rejected, got %v", err)
	}
}

func TestVerifyEnforcesPathLength(t *testing.T) {
	c := verifierWithStatus(t)
	lower := impl.CertInfo{
		Subject:    "CN=Lower CA",
		Issuer:     "CN=Inter CA",
		Serial:     "lower-1",
		IsCA:       true,
		PathLen:    0,
		PathLenSet: true,
		NotBefore:  certBase.Add(-time.Hour),
		NotAfter:   certBase.Add(720 * time.Hour),
	}
	upper := impl.CertInfo{
		Subject:    "CN=Inter CA",
		Issuer:     "CN=Root CA",
		Serial:     "inter-1",
		IsCA:       true,
		PathLen:    0,
		PathLenSet: true,
		NotBefore:  certBase.Add(-24 * time.Hour),
		NotAfter:   certBase.Add(9000 * time.Hour),
	}
	leaf := leafCert()
	leaf.Issuer = "CN=Lower CA"
	if _, err := c.Verify([]impl.CertInfo{leaf, lower, upper}, "api.example.com"); !errors.Is(err, impl.ErrPathTooLong) {
		t.Fatalf("path length violation must be rejected, got %v", err)
	}
}

func TestVerifyEnforcesAnchorNameConstraints(t *testing.T) {
	c := impl.NewChainRevocationCache(cacheConfig())
	c.SetNow(certBase)
	anchor := anchorCert()
	anchor.NameConstraints = []string{"corp.example"}
	if err := c.AddTrustAnchor(anchor); err != nil {
		t.Fatalf("add constrained anchor: %v", err)
	}
	if err := c.RecordStatus("CN=Inter CA", "leaf-1", impl.StatusGood, "ocsp"); err != nil {
		t.Fatalf("record status: %v", err)
	}
	leaf := leafCert()
	leaf.DNSNames = []string{"api.public.example"}
	if _, err := c.Verify([]impl.CertInfo{leaf, intermediateCert()}, "api.public.example"); !errors.Is(err, impl.ErrNameConstraint) {
		t.Fatalf("leaf outside the anchor name constraints must be rejected, got %v", err)
	}
}

func TestVerifyRequiresServerAuthUsage(t *testing.T) {
	c := verifierWithStatus(t)
	leaf := leafCert()
	leaf.KeyUsageServerAuth = false
	if _, err := c.Verify([]impl.CertInfo{leaf, intermediateCert()}, "api.example.com"); !errors.Is(err, impl.ErrKeyUsage) {
		t.Fatalf("leaf without server auth usage must be rejected, got %v", err)
	}
}

func TestVerifyRejectsUntrustedChain(t *testing.T) {
	c := verifierWithStatus(t)
	leaf := leafCert()
	leaf.Issuer = "CN=Unknown CA"
	if _, err := c.Verify([]impl.CertInfo{leaf}, "api.example.com"); !errors.Is(err, impl.ErrUntrustedChain) {
		t.Fatalf("chain outside the pinned anchors must be rejected, got %v", err)
	}
}

func TestRevocationStatusRules(t *testing.T) {
	c := newVerifier()
	if _, err := c.Verify([]impl.CertInfo{leafCert(), intermediateCert()}, "api.example.com"); !errors.Is(err, impl.ErrStatusUnknown) {
		t.Fatalf("missing status must fail closed, got %v", err)
	}
	if err := c.RecordStatus("", "leaf-1", impl.StatusGood, "ocsp"); !errors.Is(err, impl.ErrBlankField) {
		t.Fatalf("blank issuer must be rejected, got %v", err)
	}
	if err := c.RecordStatus("CN=Inter CA", "leaf-1", impl.StatusRevoked, "ocsp"); err != nil {
		t.Fatalf("record revoked status: %v", err)
	}
	if _, err := c.Verify([]impl.CertInfo{leafCert(), intermediateCert()}, "api.example.com"); !errors.Is(err, impl.ErrRevoked) {
		t.Fatalf("revoked leaf must be rejected, got %v", err)
	}
	if err := c.RecordStatus("CN=Inter CA", "leaf-1", impl.StatusGood, "ocsp"); !errors.Is(err, impl.ErrRevokedPermanent) {
		t.Fatalf("revoked status must not be overwritten, got %v", err)
	}
	entry, ok := c.Status("CN=Inter CA", "leaf-1")
	if !ok || entry.Status != impl.StatusRevoked {
		t.Fatalf("cached entry = %+v (found=%v), want revoked", entry, ok)
	}
}

func TestRevocationStatusStaleness(t *testing.T) {
	c := verifierWithStatus(t)
	c.SetNow(certBase.Add(4 * time.Minute))
	if _, err := c.Verify([]impl.CertInfo{leafCert(), intermediateCert()}, "api.example.com"); err != nil {
		t.Fatalf("fresh status must still verify, got %v", err)
	}
	c.SetNow(certBase.Add(15*time.Minute + time.Second))
	if _, err := c.Verify([]impl.CertInfo{leafCert(), intermediateCert()}, "api.example.com"); !errors.Is(err, impl.ErrStatusStale) {
		t.Fatalf("status beyond ttl plus staleness must fail closed, got %v", err)
	}
}

func TestCacheCapacityEvictsLeastRecentlyUsed(t *testing.T) {
	cfg := cacheConfig()
	cfg.MaxCacheEntries = 2
	c := impl.NewChainRevocationCache(cfg)
	c.SetNow(certBase)
	if err := c.RecordStatus("CN=Inter CA", "leaf-1", impl.StatusGood, "ocsp"); err != nil {
		t.Fatalf("record leaf-1: %v", err)
	}
	if err := c.RecordStatus("CN=Inter CA", "leaf-2", impl.StatusGood, "ocsp"); err != nil {
		t.Fatalf("record leaf-2: %v", err)
	}
	if _, ok := c.Status("CN=Inter CA", "leaf-1"); !ok {
		t.Fatalf("leaf-1 must still be cached")
	}
	if err := c.RecordStatus("CN=Inter CA", "leaf-3", impl.StatusGood, "ocsp"); err != nil {
		t.Fatalf("record leaf-3: %v", err)
	}
	stats := c.Stats()
	if stats.Entries != 2 || stats.Evictions != 1 {
		t.Fatalf("stats = %+v, want 2 entries and 1 eviction", stats)
	}
	if _, ok := c.Status("CN=Inter CA", "leaf-2"); ok {
		t.Fatalf("least recently used entry must have been evicted")
	}
	if _, ok := c.Status("CN=Inter CA", "leaf-3"); !ok {
		t.Fatalf("new entry must be cached")
	}
}

func TestSnapshotRestoreKeepsAnchorsAndStatus(t *testing.T) {
	c := verifierWithStatus(t)
	if _, err := c.Verify([]impl.CertInfo{leafCert(), intermediateCert()}, "api.example.com"); err != nil {
		t.Fatalf("warm up verify: %v", err)
	}
	data, err := c.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := impl.NewChainRevocationCache(cacheConfig())
	restored.SetNow(certBase)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !reflect.DeepEqual(restored.Anchors(), c.Anchors()) {
		t.Fatalf("restored anchors = %v, want %v", restored.Anchors(), c.Anchors())
	}
	if restored.Stats() != c.Stats() {
		t.Fatalf("restored stats = %+v, want %+v", restored.Stats(), c.Stats())
	}
	if _, err := restored.Verify([]impl.CertInfo{leafCert(), intermediateCert()}, "api.example.com"); err != nil {
		t.Fatalf("restored verifier must accept the cached chain, got %v", err)
	}
	before := restored.Stats()
	if err := restored.Restore([]byte("not a snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must report ErrBadSnapshot, got %v", err)
	}
	if restored.Stats() != before {
		t.Fatalf("failed restore mutated the cache: %+v -> %+v", before, restored.Stats())
	}
}
