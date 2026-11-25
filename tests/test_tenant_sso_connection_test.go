package task

import (
	"errors"
	"testing"
	"time"

	impl "gsb/batch49-go-safety-a-23"
)

var ssoBase = time.Date(2026, 5, 6, 8, 30, 0, 0, time.UTC)

func ssoLimits() impl.SsoLimits {
	return impl.SsoLimits{
		MaxConnectionsPerTenant: 4,
		MaxDomainsPerConnection: 3,
		MaxBreakGlassPerDay:     2,
	}
}

func ssoConfig(domain string) impl.ConnectionConfig {
	return impl.ConnectionConfig{
		DisplayName: "Corp SSO",
		Domains:     []string{domain},
		Issuer:      "https://idp.corp.example",
		ClientID:    "client-1",
	}
}

func hasString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func enabledConnection(t *testing.T, svc *impl.TenantSsoConnection, tenant, domain string) impl.Connection {
	t.Helper()
	conn, err := svc.Create(tenant, ssoConfig(domain))
	if err != nil {
		t.Fatalf("create must succeed: %v", err)
	}
	if err := svc.VerifyDomain(tenant, conn.ID, domain, "dns-txt=gsb-verify"); err != nil {
		t.Fatalf("domain verification must succeed: %v", err)
	}
	if _, err := svc.Test(tenant, conn.ID); err != nil {
		t.Fatalf("connectivity test must succeed: %v", err)
	}
	if err := svc.Enable(tenant, conn.ID); err != nil {
		t.Fatalf("enable must succeed: %v", err)
	}
	return conn
}

func TestCreateStartsInDraft(t *testing.T) {
	svc := impl.NewTenantSsoConnection(ssoLimits())
	conn, err := svc.Create("tenant-1", ssoConfig("corp.example"))
	if err != nil {
		t.Fatalf("create must succeed: %v", err)
	}
	if conn.ID == "" {
		t.Fatalf("connection identifier must not be empty")
	}
	if conn.State != impl.StateDraft {
		t.Fatalf("new connection must be in draft state, got %q", conn.State)
	}
	if len(conn.Domains) != 1 || conn.Domains[0] != "corp.example" {
		t.Fatalf("configuration domains must be copied, got %v", conn.Domains)
	}
}

func TestCreateRejectsBlankTenant(t *testing.T) {
	svc := impl.NewTenantSsoConnection(ssoLimits())
	if _, err := svc.Create("", ssoConfig("corp.example")); !errors.Is(err, impl.ErrBlankTenant) {
		t.Fatalf("blank tenant must return ErrBlankTenant, got %v", err)
	}
}

func TestCreateEnforcesConnectionLimit(t *testing.T) {
	limits := ssoLimits()
	limits.MaxConnectionsPerTenant = 2
	svc := impl.NewTenantSsoConnection(limits)
	if _, err := svc.Create("tenant-1", ssoConfig("one.example")); err != nil {
		t.Fatalf("first connection must succeed: %v", err)
	}
	if _, err := svc.Create("tenant-1", ssoConfig("two.example")); err != nil {
		t.Fatalf("second connection must succeed: %v", err)
	}
	if _, err := svc.Create("tenant-1", ssoConfig("three.example")); !errors.Is(err, impl.ErrTenantLimit) {
		t.Fatalf("third connection must return ErrTenantLimit, got %v", err)
	}
}

func TestVerifyDomainMarksVerified(t *testing.T) {
	svc := impl.NewTenantSsoConnection(ssoLimits())
	conn, err := svc.Create("tenant-1", ssoConfig("corp.example"))
	if err != nil {
		t.Fatalf("create must succeed: %v", err)
	}
	if err := svc.VerifyDomain("tenant-1", conn.ID, "corp.example", "dns-txt=gsb-verify"); err != nil {
		t.Fatalf("domain verification must succeed: %v", err)
	}
	got, ok := svc.Get("tenant-1", conn.ID)
	if !ok {
		t.Fatalf("connection must be found after verification")
	}
	if !hasString(got.VerifiedDomains, "corp.example") {
		t.Fatalf("verified domain must be recorded, got %v", got.VerifiedDomains)
	}
}

func TestVerifyDomainRejectsBadProof(t *testing.T) {
	svc := impl.NewTenantSsoConnection(ssoLimits())
	conn, err := svc.Create("tenant-1", ssoConfig("corp.example"))
	if err != nil {
		t.Fatalf("create must succeed: %v", err)
	}
	if err := svc.VerifyDomain("tenant-1", conn.ID, "corp.example", ""); !errors.Is(err, impl.ErrBadProof) {
		t.Fatalf("blank proof must return ErrBadProof, got %v", err)
	}
}

func TestConnectivityTestMovesToTested(t *testing.T) {
	svc := impl.NewTenantSsoConnection(ssoLimits())
	conn, err := svc.Create("tenant-1", ssoConfig("corp.example"))
	if err != nil {
		t.Fatalf("create must succeed: %v", err)
	}
	if err := svc.VerifyDomain("tenant-1", conn.ID, "corp.example", "dns-txt=gsb-verify"); err != nil {
		t.Fatalf("domain verification must succeed: %v", err)
	}
	result, err := svc.Test("tenant-1", conn.ID)
	if err != nil {
		t.Fatalf("connectivity test must succeed: %v", err)
	}
	if !result.OK {
		t.Fatalf("connectivity test must report success")
	}
	if result.Checked < 1 {
		t.Fatalf("connectivity test must report at least one check, got %d", result.Checked)
	}
	got, ok := svc.Get("tenant-1", conn.ID)
	if !ok {
		t.Fatalf("connection must be found after the test")
	}
	if got.State != impl.StateTested {
		t.Fatalf("tested connection must move to tested state, got %q", got.State)
	}
}

func TestEnableAfterTested(t *testing.T) {
	svc := impl.NewTenantSsoConnection(ssoLimits())
	conn := enabledConnection(t, svc, "tenant-1", "corp.example")
	got, ok := svc.Get("tenant-1", conn.ID)
	if !ok {
		t.Fatalf("enabled connection must be found")
	}
	if got.State != impl.StateEnabled {
		t.Fatalf("connection must be enabled, got %q", got.State)
	}
}

func TestDisableFromEnabled(t *testing.T) {
	svc := impl.NewTenantSsoConnection(ssoLimits())
	conn := enabledConnection(t, svc, "tenant-1", "corp.example")
	if err := svc.Disable("tenant-1", conn.ID); err != nil {
		t.Fatalf("disable must succeed: %v", err)
	}
	got, ok := svc.Get("tenant-1", conn.ID)
	if !ok {
		t.Fatalf("disabled connection must still be found")
	}
	if got.State != impl.StateDisabled {
		t.Fatalf("connection must be disabled, got %q", got.State)
	}
}

func TestRolloutPercentBounds(t *testing.T) {
	svc := impl.NewTenantSsoConnection(ssoLimits())
	conn := enabledConnection(t, svc, "tenant-1", "corp.example")
	if err := svc.Rollout("tenant-1", conn.ID, 101); !errors.Is(err, impl.ErrBadRollout) {
		t.Fatalf("percent 101 must return ErrBadRollout, got %v", err)
	}
	if err := svc.Rollout("tenant-1", conn.ID, -1); !errors.Is(err, impl.ErrBadRollout) {
		t.Fatalf("percent -1 must return ErrBadRollout, got %v", err)
	}
}

func TestRolloutPersistsPercent(t *testing.T) {
	svc := impl.NewTenantSsoConnection(ssoLimits())
	conn := enabledConnection(t, svc, "tenant-1", "corp.example")
	if err := svc.Rollout("tenant-1", conn.ID, 25); err != nil {
		t.Fatalf("rollout 25 must succeed: %v", err)
	}
	got, ok := svc.Get("tenant-1", conn.ID)
	if !ok {
		t.Fatalf("connection must be found after rollout")
	}
	if got.RolloutPercent != 25 {
		t.Fatalf("rollout percent must persist, got %d", got.RolloutPercent)
	}
}

func TestSnapshotRestoreKeepsEnabledAndRollout(t *testing.T) {
	svc := impl.NewTenantSsoConnection(ssoLimits())
	conn := enabledConnection(t, svc, "tenant-1", "corp.example")
	if err := svc.Rollout("tenant-1", conn.ID, 25); err != nil {
		t.Fatalf("rollout must succeed: %v", err)
	}
	snap, err := svc.Snapshot()
	if err != nil {
		t.Fatalf("snapshot must succeed: %v", err)
	}
	restored := impl.NewTenantSsoConnection(ssoLimits())
	if err := restored.Restore(snap); err != nil {
		t.Fatalf("restore must succeed: %v", err)
	}
	got, ok := restored.Get("tenant-1", conn.ID)
	if !ok {
		t.Fatalf("restored connection must be found")
	}
	if got.State != impl.StateEnabled {
		t.Fatalf("restored state must stay enabled, got %q", got.State)
	}
	if got.RolloutPercent != 25 {
		t.Fatalf("restored rollout must stay 25, got %d", got.RolloutPercent)
	}
	if !hasString(got.VerifiedDomains, "corp.example") {
		t.Fatalf("restored verified domains must be kept, got %v", got.VerifiedDomains)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	svc := impl.NewTenantSsoConnection(ssoLimits())
	enabledConnection(t, svc, "tenant-1", "corp.example")
	if err := svc.Restore([]byte("not a snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must return ErrBadSnapshot, got %v", err)
	}
}

func TestUnknownConnectionRejected(t *testing.T) {
	svc := impl.NewTenantSsoConnection(ssoLimits())
	err := svc.VerifyDomain("tenant-1", "conn-missing", "corp.example", "dns-txt=gsb-verify")
	if !errors.Is(err, impl.ErrUnknownConnection) {
		t.Fatalf("unknown connection must return ErrUnknownConnection, got %v", err)
	}
}

func TestCreateRejectsDuplicateDomain(t *testing.T) {
	svc := impl.NewTenantSsoConnection(ssoLimits())
	if _, err := svc.Create("tenant-1", ssoConfig("corp.example")); err != nil {
		t.Fatalf("first create must succeed: %v", err)
	}
	if _, err := svc.Create("tenant-2", ssoConfig("corp.example")); !errors.Is(err, impl.ErrDomainTaken) {
		t.Fatalf("duplicate domain must return ErrDomainTaken, got %v", err)
	}
}

func TestRouteLoginUsesVerifiedDomain(t *testing.T) {
	svc := impl.NewTenantSsoConnection(ssoLimits())
	conn := enabledConnection(t, svc, "tenant-1", "corp.example")
	routed, err := svc.RouteLogin("tenant-1", "alice@corp.example")
	if err != nil {
		t.Fatalf("login routing must succeed: %v", err)
	}
	if routed.ID != conn.ID {
		t.Fatalf("login must route to the enabled connection, got %q want %q", routed.ID, conn.ID)
	}
}
