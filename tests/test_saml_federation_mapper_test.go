package task

import (
	"errors"
	"sync"
	"testing"
	"time"

	impl "gsb/batch49-go-safety-a-18"
)

var mapperBase = time.Date(2026, 5, 8, 8, 30, 0, 0, time.UTC)

func mapperLimits() impl.Limits {
	return impl.Limits{MaxIdPsPerTenant: 4, MaxRulesPerIdP: 4, MaxBindingsPerIdP: 8}
}

func sampleRules() []impl.AttrRule {
	return []impl.AttrRule{
		{Name: "z_mail", Source: "mail", Required: true, Multi: impl.MultiFirst},
		{Name: "a_role", Source: "roles", Multi: impl.MultiJoin, MaxValues: 2, Separator: ","},
		{Name: "m_dept", Source: "dept"},
	}
}

func mustConfigure(t *testing.T, m *impl.SamlFederationMapper, tenant, idp string, rules []impl.AttrRule) {
	t.Helper()
	if err := m.Configure(tenant, idp, rules); err != nil {
		t.Fatalf("configure %s/%s: %v", tenant, idp, err)
	}
}

func TestConfigureStoresRulesInOrder(t *testing.T) {
	m := impl.NewSamlFederationMapper(mapperLimits())
	rules := sampleRules()
	if err := m.Configure("tenant-1", "idp-1", rules); err != nil {
		t.Fatalf("configure: %v", err)
	}
	got, err := m.Attributes("tenant-1", "idp-1")
	if err != nil {
		t.Fatalf("attributes: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("stored rules = %d, want 3", len(got))
	}
	want := []string{"z_mail", "a_role", "m_dept"}
	for i, name := range want {
		if got[i].Name != name {
			t.Fatalf("rule %d name = %q, want the configured order %v", i, got[i].Name, want)
		}
	}
	if got[0].Source != "mail" || !got[0].Required {
		t.Fatalf("first rule lost its source or required flag: %+v", got[0])
	}
	if got[1].MaxValues != 2 || got[1].Separator != "," || got[1].Multi != impl.MultiJoin {
		t.Fatalf("second rule lost its multi value settings: %+v", got[1])
	}
	if count := m.IdPCount("tenant-1"); count != 1 {
		t.Fatalf("provider count = %d, want 1", count)
	}
	if _, err := m.Attributes("tenant-1", "idp-404"); !errors.Is(err, impl.ErrUnknownIdP) {
		t.Fatalf("attributes of an unknown provider must fail with ErrUnknownIdP, got %v", err)
	}
}

func TestConfigureRejectsDuplicateRuleNames(t *testing.T) {
	m := impl.NewSamlFederationMapper(mapperLimits())
	mustConfigure(t, m, "tenant-1", "idp-1", sampleRules())
	dup := []impl.AttrRule{
		{Name: "email", Source: "mail", Multi: impl.MultiFirst},
		{Name: "email", Source: "mail_alt", Multi: impl.MultiFirst},
	}
	if err := m.Configure("tenant-1", "idp-1", dup); !errors.Is(err, impl.ErrDuplicateRule) {
		t.Fatalf("duplicate rule name must fail with ErrDuplicateRule, got %v", err)
	}
	got, err := m.Attributes("tenant-1", "idp-1")
	if err != nil {
		t.Fatalf("attributes: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("rejected configuration must leave the old rules, got %d", len(got))
	}
	malformed := []impl.AttrRule{{Name: "email", Source: "mail", Multi: "sorted"}}
	if err := m.Configure("tenant-1", "idp-2", malformed); !errors.Is(err, impl.ErrBadRule) {
		t.Fatalf("unknown multi strategy must fail with ErrBadRule, got %v", err)
	}
	if err := m.Configure("  ", "idp-2", sampleRules()); !errors.Is(err, impl.ErrBlankArgument) {
		t.Fatalf("blank tenant must fail with ErrBlankArgument, got %v", err)
	}
}

func TestConfigureEnforcesRuleCap(t *testing.T) {
	m := impl.NewSamlFederationMapper(impl.Limits{MaxIdPsPerTenant: 4, MaxRulesPerIdP: 2, MaxBindingsPerIdP: 8})
	rules := []impl.AttrRule{
		{Name: "email", Source: "mail", Multi: impl.MultiFirst},
		{Name: "dept", Source: "dept", Multi: impl.MultiFirst},
		{Name: "role", Source: "role", Multi: impl.MultiFirst},
	}
	if err := m.Configure("tenant-1", "idp-1", rules); !errors.Is(err, impl.ErrRuleLimit) {
		t.Fatalf("rule batch past the cap must fail with ErrRuleLimit, got %v", err)
	}
	if count := m.IdPCount("tenant-1"); count != 0 {
		t.Fatalf("rejected configuration must not register the provider, got %d", count)
	}
	mustConfigure(t, m, "tenant-1", "idp-1", rules[:2])
	if got, err := m.Attributes("tenant-1", "idp-1"); err != nil || len(got) != 2 {
		t.Fatalf("rules exactly at the cap must be stored, got %d (err=%v)", len(got), err)
	}
}

func TestConfigureEnforcesIdPLimit(t *testing.T) {
	m := impl.NewSamlFederationMapper(impl.Limits{MaxIdPsPerTenant: 2, MaxRulesPerIdP: 4, MaxBindingsPerIdP: 8})
	mustConfigure(t, m, "tenant-1", "idp-1", sampleRules())
	mustConfigure(t, m, "tenant-1", "idp-2", sampleRules())
	if err := m.Configure("tenant-1", "idp-3", sampleRules()); !errors.Is(err, impl.ErrIdPLimit) {
		t.Fatalf("third provider must fail with ErrIdPLimit, got %v", err)
	}
	if count := m.IdPCount("tenant-1"); count != 2 {
		t.Fatalf("provider count = %d, want the configured cap 2", count)
	}
	if _, err := m.Attributes("tenant-1", "idp-3"); !errors.Is(err, impl.ErrUnknownIdP) {
		t.Fatalf("rejected provider must stay unknown, got %v", err)
	}
	mustConfigure(t, m, "tenant-2", "idp-1", sampleRules())
	if count := m.IdPCount("tenant-2"); count != 1 {
		t.Fatalf("the provider cap is per tenant, got %d for tenant-2", count)
	}
}

func TestPickValueTakesFirstValue(t *testing.T) {
	m := impl.NewSamlFederationMapper(mapperLimits())
	rule := impl.AttrRule{Name: "email", Source: "mail", Required: true, Multi: impl.MultiFirst}
	value, err := m.PickValue(rule, []string{"a@example.test", "b@example.test"})
	if err != nil {
		t.Fatalf("pick first value: %v", err)
	}
	if value != "a@example.test" {
		t.Fatalf("picked value = %q, want the first value", value)
	}
	if _, err := m.PickValue(rule, nil); !errors.Is(err, impl.ErrMissingAttr) {
		t.Fatalf("missing values must fail with ErrMissingAttr, got %v", err)
	}
	if _, err := m.PickValue(rule, []string{}); !errors.Is(err, impl.ErrMissingAttr) {
		t.Fatalf("empty values must fail with ErrMissingAttr, got %v", err)
	}
	optional := impl.AttrRule{Name: "phone", Source: "phone", Multi: impl.MultiFirst}
	if _, err := m.PickValue(optional, nil); !errors.Is(err, impl.ErrMissingAttr) {
		t.Fatalf("an empty value list must still fail with ErrMissingAttr, got %v", err)
	}
}

func TestPickValueJoinAndTruncate(t *testing.T) {
	m := impl.NewSamlFederationMapper(mapperLimits())
	join := impl.AttrRule{Name: "role", Source: "roles", Multi: impl.MultiJoin, MaxValues: 2, Separator: "|"}
	value, err := m.PickValue(join, []string{"admin", "auditor", "viewer"})
	if err != nil {
		t.Fatalf("join values: %v", err)
	}
	if value != "admin|auditor" {
		t.Fatalf("joined value = %q, want admin|auditor", value)
	}
	cut := impl.AttrRule{Name: "role", Source: "roles", Multi: impl.MultiJoin, MaxValues: 2, Separator: ",", MaxLen: 8}
	value, err = m.PickValue(cut, []string{"alpha", "beta", "gamma"})
	if err != nil {
		t.Fatalf("truncated join: %v", err)
	}
	if value != "alpha,be" {
		t.Fatalf("truncated value = %q, want alpha,be", value)
	}
	single := impl.AttrRule{Name: "dept", Source: "dept", Multi: impl.MultiTruncate, MaxLen: 4}
	value, err = m.PickValue(single, []string{"engineering"})
	if err != nil {
		t.Fatalf("truncated single value: %v", err)
	}
	if value != "engi" {
		t.Fatalf("truncated single value = %q, want engi", value)
	}
}

func TestNormalizeNameIDFormats(t *testing.T) {
	m := impl.NewSamlFederationMapper(mapperLimits())
	value, err := m.NormalizeNameID(impl.NameIDEmail, "  Alice@Example.TEST  ")
	if err != nil {
		t.Fatalf("normalize email: %v", err)
	}
	if value != "alice@example.test" {
		t.Fatalf("normalized email = %q, want alice@example.test", value)
	}
	if _, err := m.NormalizeNameID(impl.NameIDEmail, "no-at-sign"); !errors.Is(err, impl.ErrBadNameID) {
		t.Fatalf("email without @ must fail with ErrBadNameID, got %v", err)
	}
	if _, err := m.NormalizeNameID(impl.NameIDEmail, "two@@example.test"); !errors.Is(err, impl.ErrBadNameID) {
		t.Fatalf("email with two @ must fail with ErrBadNameID, got %v", err)
	}
	value, err = m.NormalizeNameID(impl.NameIDPersistent, "  PN-42  ")
	if err != nil || value != "PN-42" {
		t.Fatalf("persistent id = %q (err=%v), want PN-42", value, err)
	}
	if _, err := m.NormalizeNameID(impl.NameIDTransient, "tn-1"); !errors.Is(err, impl.ErrBadNameID) {
		t.Fatalf("transient ids must not be bindable, got %v", err)
	}
	if _, err := m.NormalizeNameID(impl.NameIDFormat("phone"), "12345"); !errors.Is(err, impl.ErrBadFormat) {
		t.Fatalf("unknown format must fail with ErrBadFormat, got %v", err)
	}
}

func TestBindIdempotentAndConflictAndBlank(t *testing.T) {
	m := impl.NewSamlFederationMapper(mapperLimits())
	mustConfigure(t, m, "tenant-1", "idp-1", sampleRules())
	if err := m.Bind("tenant-1", "idp-1", "PN-1", "user-1"); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if err := m.Bind("tenant-1", "idp-1", "PN-1", "user-1"); err != nil {
		t.Fatalf("binding the same pair twice must be idempotent, got %v", err)
	}
	bindings := m.Bindings("tenant-1", "idp-1")
	if len(bindings) != 1 {
		t.Fatalf("binding count = %d, want 1", len(bindings))
	}
	if bindings[0].UserID != "user-1" || bindings[0].NameID != "PN-1" {
		t.Fatalf("stored binding = %+v, want PN-1 to user-1", bindings[0])
	}
	if err := m.Bind("tenant-1", "idp-1", "PN-1", "user-2"); !errors.Is(err, impl.ErrBindingConflict) {
		t.Fatalf("rebinding to another account must fail with ErrBindingConflict, got %v", err)
	}
	bindings = m.Bindings("tenant-1", "idp-1")
	if len(bindings) != 1 || bindings[0].UserID != "user-1" {
		t.Fatalf("conflicting bind must not touch the table, got %+v", bindings)
	}
	if err := m.Bind("   ", "idp-1", "PN-2", "user-1"); !errors.Is(err, impl.ErrBlankArgument) {
		t.Fatalf("blank tenant must fail with ErrBlankArgument, got %v", err)
	}
	if err := m.Bind("tenant-1", "idp-1", "  ", "user-1"); !errors.Is(err, impl.ErrBlankArgument) {
		t.Fatalf("blank name id must fail with ErrBlankArgument, got %v", err)
	}
	if err := m.Bind("tenant-1", "idp-404", "PN-2", "user-1"); !errors.Is(err, impl.ErrUnknownIdP) {
		t.Fatalf("binding on an unknown provider must fail with ErrUnknownIdP, got %v", err)
	}
}

func TestMapReturnsBoundIdentity(t *testing.T) {
	m := impl.NewSamlFederationMapper(mapperLimits())
	rules := []impl.AttrRule{
		{Name: "email", Source: "mail", Required: true, Multi: impl.MultiFirst},
		{Name: "dept", Source: "dept", Multi: impl.MultiFirst},
	}
	mustConfigure(t, m, "tenant-1", "idp-1", rules)
	if err := m.Bind("tenant-1", "idp-1", "PN-1", "user-1"); err != nil {
		t.Fatalf("bind: %v", err)
	}
	assertion := impl.Assertion{
		NameID: "PN-1",
		Format: impl.NameIDPersistent,
		Issuer: "https://idp.example.test",
		Attributes: map[string][]string{
			"mail": {"alice@example.test"},
			"dept": {"engineering"},
		},
	}
	identity, err := m.Map("tenant-1", "idp-1", assertion)
	if err != nil {
		t.Fatalf("map must succeed for a bound assertion, got %v", err)
	}
	if identity.UserID != "user-1" {
		t.Fatalf("mapped user = %q, want the bound account user-1", identity.UserID)
	}
	if identity.Created {
		t.Fatalf("a bound assertion must not create a new account")
	}
	if identity.TenantID != "tenant-1" || identity.IdPID != "idp-1" {
		t.Fatalf("mapped identity lost its scope: %+v", identity)
	}
	if identity.Email != "alice@example.test" {
		t.Fatalf("mapped email = %q, want alice@example.test", identity.Email)
	}
	if identity.Attributes["dept"] != "engineering" {
		t.Fatalf("mapped attributes = %+v, want dept=engineering", identity.Attributes)
	}
	if identity.Seq <= 0 {
		t.Fatalf("mapped sequence = %d, want a positive number", identity.Seq)
	}
	if _, err := m.Map("tenant-1", "idp-404", assertion); !errors.Is(err, impl.ErrUnknownIdP) {
		t.Fatalf("mapping through an unknown provider must fail with ErrUnknownIdP, got %v", err)
	}
	if _, err := m.Map("  ", "idp-1", assertion); !errors.Is(err, impl.ErrBlankArgument) {
		t.Fatalf("mapping with a blank tenant must fail with ErrBlankArgument, got %v", err)
	}
}

func TestMigrateRejectsBadFormats(t *testing.T) {
	m := impl.NewSamlFederationMapper(mapperLimits())
	mustConfigure(t, m, "tenant-1", "idp-1", sampleRules())
	if _, err := m.MigrateAt("tenant-1", "idp-1", impl.NameIDPersistent, impl.NameIDPersistent, mapperBase); !errors.Is(err, impl.ErrSameFormat) {
		t.Fatalf("identical formats must fail with ErrSameFormat, got %v", err)
	}
	if _, err := m.MigrateAt("tenant-1", "idp-1", impl.NameIDFormat("phone"), impl.NameIDEmail, mapperBase); !errors.Is(err, impl.ErrBadFormat) {
		t.Fatalf("unknown source format must fail with ErrBadFormat, got %v", err)
	}
	if _, err := m.MigrateAt("tenant-1", "idp-1", impl.NameIDPersistent, impl.NameIDFormat("upn"), mapperBase); !errors.Is(err, impl.ErrBadFormat) {
		t.Fatalf("unknown target format must fail with ErrBadFormat, got %v", err)
	}
	if _, ok := m.Migration("tenant-1", "idp-1"); ok {
		t.Fatalf("rejected migrations must not leave a plan behind")
	}
}

func TestMigrateReturnsOverlapPlan(t *testing.T) {
	m := impl.NewSamlFederationMapper(mapperLimits())
	mustConfigure(t, m, "tenant-1", "idp-1", sampleRules())
	if err := m.Bind("tenant-1", "idp-1", "PN-1", "user-1"); err != nil {
		t.Fatalf("bind PN-1: %v", err)
	}
	if err := m.Bind("tenant-1", "idp-1", "PN-2", "user-2"); err != nil {
		t.Fatalf("bind PN-2: %v", err)
	}
	plan, err := m.MigrateAt("tenant-1", "idp-1", impl.NameIDPersistent, impl.NameIDEmail, mapperBase)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if plan.From != impl.NameIDPersistent || plan.To != impl.NameIDEmail {
		t.Fatalf("plan formats = %s -> %s, want persistent -> email", plan.From, plan.To)
	}
	if !plan.OverlapUntil.After(mapperBase) {
		t.Fatalf("overlap deadline = %v, must be after the issue time %v", plan.OverlapUntil, mapperBase)
	}
	if plan.Bindings != 2 {
		t.Fatalf("plan binding count = %d, want the 2 existing bindings", plan.Bindings)
	}
	if plan.Seq <= 0 {
		t.Fatalf("plan sequence = %d, want a positive number", plan.Seq)
	}
	stored, ok := m.Migration("tenant-1", "idp-1")
	if !ok {
		t.Fatalf("planned migration must be readable")
	}
	if !stored.OverlapUntil.Equal(plan.OverlapUntil) || stored.To != plan.To {
		t.Fatalf("stored plan = %+v, want %+v", stored, plan)
	}
}

func TestConcurrentBindSameNameIDStoresOneBinding(t *testing.T) {
	m := impl.NewSamlFederationMapper(mapperLimits())
	mustConfigure(t, m, "tenant-1", "idp-1", sampleRules())
	var wg sync.WaitGroup
	var mu sync.Mutex
	failures := 0
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := m.Bind("tenant-1", "idp-1", "PN-race", "user-1"); err != nil {
				mu.Lock()
				failures++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if failures != 0 {
		t.Fatalf("concurrent binds of the same pair reported %d failures, want 0", failures)
	}
	bindings := m.Bindings("tenant-1", "idp-1")
	if len(bindings) != 1 {
		t.Fatalf("binding count after the race = %d, want 1", len(bindings))
	}
	if bindings[0].UserID != "user-1" {
		t.Fatalf("stored binding = %+v, want user-1", bindings[0])
	}
}

func TestSnapshotRestoreKeepsRulesAndBindings(t *testing.T) {
	limits := mapperLimits()
	m := impl.NewSamlFederationMapper(limits)
	mustConfigure(t, m, "tenant-1", "idp-1", sampleRules())
	if err := m.Bind("tenant-1", "idp-1", "PN-1", "user-1"); err != nil {
		t.Fatalf("bind PN-1: %v", err)
	}
	last := impl.Binding{}
	if err := m.Bind("tenant-1", "idp-1", "PN-2", "user-2"); err != nil {
		t.Fatalf("bind PN-2: %v", err)
	}
	for _, b := range m.Bindings("tenant-1", "idp-1") {
		if b.Seq > last.Seq {
			last = b
		}
	}
	plan, err := m.MigrateAt("tenant-1", "idp-1", impl.NameIDPersistent, impl.NameIDEmail, mapperBase)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	data, err := m.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload must not be empty")
	}
	restored := impl.NewSamlFederationMapper(limits)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	rules, err := restored.Attributes("tenant-1", "idp-1")
	if err != nil {
		t.Fatalf("attributes after restore: %v", err)
	}
	if len(rules) != 3 || rules[0].Name != "z_mail" || rules[2].Name != "m_dept" {
		t.Fatalf("restored rules = %+v, want the original order z_mail,a_role,m_dept", rules)
	}
	if rules[1].MaxValues != 2 || rules[1].Separator != "," {
		t.Fatalf("restored rule lost its multi value settings: %+v", rules[1])
	}
	bindings := restored.Bindings("tenant-1", "idp-1")
	if len(bindings) != 2 {
		t.Fatalf("restored bindings = %d, want 2", len(bindings))
	}
	users := map[string]string{}
	for _, b := range bindings {
		users[b.NameID] = b.UserID
	}
	if users["PN-1"] != "user-1" || users["PN-2"] != "user-2" {
		t.Fatalf("restored binding map = %+v, want PN-1->user-1 and PN-2->user-2", users)
	}
	stored, ok := restored.Migration("tenant-1", "idp-1")
	if !ok {
		t.Fatalf("restored mapper lost the migration")
	}
	if !stored.OverlapUntil.Equal(plan.OverlapUntil) {
		t.Fatalf("restored overlap = %v, want %v", stored.OverlapUntil, plan.OverlapUntil)
	}
	if err := restored.Bind("tenant-1", "idp-1", "PN-3", "user-3"); err != nil {
		t.Fatalf("bind after restore: %v", err)
	}
	fresh := impl.Binding{}
	for _, b := range restored.Bindings("tenant-1", "idp-1") {
		if b.NameID == "PN-3" {
			fresh = b
		}
	}
	if fresh.Seq <= last.Seq {
		t.Fatalf("binding sequence after restore = %d, must continue past %d", fresh.Seq, last.Seq)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	m := impl.NewSamlFederationMapper(mapperLimits())
	mustConfigure(t, m, "tenant-1", "idp-1", sampleRules())
	if err := m.Bind("tenant-1", "idp-1", "PN-1", "user-1"); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if err := m.Restore([]byte("not a snapshot at all")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if _, err := m.Attributes("tenant-1", "idp-1"); err != nil {
		t.Fatalf("rejected restore must not touch live rules, got %v", err)
	}
	bindings := m.Bindings("tenant-1", "idp-1")
	if len(bindings) != 1 || bindings[0].UserID != "user-1" {
		t.Fatalf("rejected restore must not touch live bindings, got %+v", bindings)
	}
	if err := m.Bind("tenant-1", "idp-1", "PN-2", "user-2"); err != nil {
		t.Fatalf("live mapper must keep working after a rejected restore, got %v", err)
	}
}

func TestBindingLimitAndSeqOrder(t *testing.T) {
	m := impl.NewSamlFederationMapper(impl.Limits{MaxIdPsPerTenant: 2, MaxRulesPerIdP: 4, MaxBindingsPerIdP: 2})
	mustConfigure(t, m, "tenant-1", "idp-1", sampleRules())
	if err := m.Bind("tenant-1", "idp-1", "PN-1", "user-1"); err != nil {
		t.Fatalf("bind PN-1: %v", err)
	}
	if err := m.Bind("tenant-1", "idp-1", "PN-2", "user-2"); err != nil {
		t.Fatalf("bind PN-2: %v", err)
	}
	if err := m.Bind("tenant-1", "idp-1", "PN-3", "user-3"); !errors.Is(err, impl.ErrBindingLimit) {
		t.Fatalf("binding past the cap must fail with ErrBindingLimit, got %v", err)
	}
	bindings := m.Bindings("tenant-1", "idp-1")
	if len(bindings) != 2 {
		t.Fatalf("binding count = %d, want the cap 2", len(bindings))
	}
	if bindings[0].Seq >= bindings[1].Seq {
		t.Fatalf("bindings must be listed oldest first, got seq %d then %d", bindings[0].Seq, bindings[1].Seq)
	}
	if err := m.Unbind("tenant-1", "idp-1", "PN-2"); err != nil {
		t.Fatalf("unbind must free a slot, got %v", err)
	}
	left := m.Bindings("tenant-1", "idp-1")
	if len(left) != 1 || left[0].NameID != "PN-1" {
		t.Fatalf("bindings after unbind = %+v, want only PN-1", left)
	}
	if err := m.Unbind("tenant-1", "idp-1", "PN-2"); !errors.Is(err, impl.ErrBindingNotFound) {
		t.Fatalf("unbinding a missing identifier must fail with ErrBindingNotFound, got %v", err)
	}
	if err := m.Bind("tenant-1", "idp-1", "PN-3", "user-3"); err != nil {
		t.Fatalf("binding after unbind must succeed, got %v", err)
	}
	after := m.Bindings("tenant-1", "idp-1")
	if len(after) != 2 || after[1].NameID != "PN-3" {
		t.Fatalf("bindings after reuse = %+v, want PN-1 and PN-3", after)
	}
	if got := m.Bindings("tenant-1", "idp-404"); len(got) != 0 {
		t.Fatalf("an unknown provider must have an empty binding list, got %d", len(got))
	}
}
