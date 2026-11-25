package task

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	impl "gsb/batch49-go-safety-a-02"
)

func attestationLimits() impl.Limits {
	return impl.Limits{MaxEntriesPerList: 8, MaxTrustAnchors: 4, MaxVersions: 3}
}

func anchor() string { return "CN=Corp Root CA" }

func attestedDevice() impl.Registration {
	return impl.Registration{
		ID:           "reg-bank-key",
		AAGUID:       "aaguid-bank-key",
		Conveyance:   impl.ConveyanceEnterprise,
		Chain:        []string{"CN=Device Leaf 22", "CN=Corp Issuing CA", "CN=Corp Root CA"},
		Attested:     true,
		UserVerified: true,
	}
}

func enterprisePolicy() impl.Policy {
	return impl.Policy{
		Conveyance:   impl.ConveyanceEnterprise,
		AllowAAGUIDs: []string{"aaguid-bank-key"},
		TrustAnchors: []string{anchor()},
		MinStrength:  impl.StrengthHigh,
	}
}

func hasCode(codes []string, want string) bool {
	for _, c := range codes {
		if c == want {
			return true
		}
	}
	return false
}

func TestSetPolicyRejectsBlankTenant(t *testing.T) {
	e := impl.NewAuthenticatorAttestationPolicy(attestationLimits())
	if _, err := e.SetPolicy("", enterprisePolicy()); !errors.Is(err, impl.ErrBlankTenant) {
		t.Fatalf("policy without a tenant = %v, want ErrBlankTenant", err)
	}
}

func TestSetPolicyRejectsUnknownConveyance(t *testing.T) {
	e := impl.NewAuthenticatorAttestationPolicy(attestationLimits())
	bad := enterprisePolicy()
	bad.Conveyance = "telepathic"
	if _, err := e.SetPolicy("tenant-1", bad); !errors.Is(err, impl.ErrBadConveyance) {
		t.Fatalf("unknown conveyance = %v, want ErrBadConveyance", err)
	}
	bad = enterprisePolicy()
	bad.Conveyance = ""
	if _, err := e.SetPolicy("tenant-1", bad); !errors.Is(err, impl.ErrBadConveyance) {
		t.Fatalf("empty conveyance = %v, want ErrBadConveyance", err)
	}
}

func TestSetPolicyRejectsUnknownStrength(t *testing.T) {
	e := impl.NewAuthenticatorAttestationPolicy(attestationLimits())
	bad := enterprisePolicy()
	bad.MinStrength = "ultimate"
	if _, err := e.SetPolicy("tenant-1", bad); !errors.Is(err, impl.ErrBadStrength) {
		t.Fatalf("unknown strength tier = %v, want ErrBadStrength", err)
	}
}

func TestSetPolicyBumpsGenerationAndReportsSizes(t *testing.T) {
	e := impl.NewAuthenticatorAttestationPolicy(attestationLimits())
	v1, err := e.SetPolicy("tenant-1", enterprisePolicy())
	if err != nil {
		t.Fatalf("first policy must install, got %v", err)
	}
	if v1.Generation != 1 || v1.Allow != 1 || v1.Deny != 0 || v1.Anchors != 1 {
		t.Fatalf("first version = %+v, want generation 1 with 1 allow and 1 anchor", v1)
	}
	p, err := e.Policy("tenant-1")
	if err != nil {
		t.Fatalf("policy must be readable, got %v", err)
	}
	if p.Conveyance != impl.ConveyanceEnterprise || p.MinStrength != impl.StrengthHigh {
		t.Fatalf("stored policy = %+v, want enterprise at high strength", p)
	}
	if len(p.AllowAAGUIDs) != 1 || p.AllowAAGUIDs[0] != "aaguid-bank-key" {
		t.Fatalf("stored allow list = %v", p.AllowAAGUIDs)
	}
	second := enterprisePolicy()
	second.DenyAAGUIDs = []string{"aaguid-lost-key"}
	v2, err := e.SetPolicy("tenant-1", second)
	if err != nil {
		t.Fatalf("second policy must install, got %v", err)
	}
	if v2.Generation != 2 {
		t.Fatalf("second generation = %d, want 2", v2.Generation)
	}
	versions := e.Versions("tenant-1")
	if len(versions) != 2 || versions[0].Generation != 1 || versions[1].Generation != 2 {
		t.Fatalf("version history = %+v, want generations 1 and 2", versions)
	}
}

func TestSetPolicyRejectsOversizedLists(t *testing.T) {
	e := impl.NewAuthenticatorAttestationPolicy(attestationLimits())
	wide := enterprisePolicy()
	wide.AllowAAGUIDs = nil
	for i := 0; i < 9; i++ {
		wide.AllowAAGUIDs = append(wide.AllowAAGUIDs, fmt.Sprintf("aaguid-%02d", i))
	}
	if _, err := e.SetPolicy("tenant-1", wide); !errors.Is(err, impl.ErrAAGUIDLimit) {
		t.Fatalf("nine aaguids with an eight entry cap = %v, want ErrAAGUIDLimit", err)
	}
	deep := enterprisePolicy()
	deep.TrustAnchors = nil
	for i := 0; i < 5; i++ {
		deep.TrustAnchors = append(deep.TrustAnchors, fmt.Sprintf("CN=Anchor %d", i))
	}
	if _, err := e.SetPolicy("tenant-1", deep); !errors.Is(err, impl.ErrTrustAnchorLimit) {
		t.Fatalf("five anchors with a four anchor cap = %v, want ErrTrustAnchorLimit", err)
	}
	if _, err := e.Policy("tenant-1"); !errors.Is(err, impl.ErrUnknownTenant) {
		t.Fatalf("rejected policy must not install, Policy = %v want ErrUnknownTenant", err)
	}
}

func TestSetPolicyEnforcesVersionCap(t *testing.T) {
	e := impl.NewAuthenticatorAttestationPolicy(attestationLimits())
	for i := 0; i < 3; i++ {
		if _, err := e.SetPolicy("tenant-1", enterprisePolicy()); err != nil {
			t.Fatalf("policy %d must install, got %v", i, err)
		}
	}
	if _, err := e.SetPolicy("tenant-1", enterprisePolicy()); !errors.Is(err, impl.ErrPolicyLimit) {
		t.Fatalf("fourth policy with a three version cap = %v, want ErrPolicyLimit", err)
	}
	p, err := e.Policy("tenant-1")
	if err != nil {
		t.Fatalf("policy must stay readable, got %v", err)
	}
	if p.Conveyance != impl.ConveyanceEnterprise {
		t.Fatalf("live policy changed after the rejected write: %+v", p)
	}
	if got := len(e.Versions("tenant-1")); got != 3 {
		t.Fatalf("stored versions = %d, want 3", got)
	}
}

func TestEvaluateRejectsUnknownTenantAndBlankRegistration(t *testing.T) {
	e := impl.NewAuthenticatorAttestationPolicy(attestationLimits())
	if _, err := e.Evaluate("tenant-1", attestedDevice()); !errors.Is(err, impl.ErrUnknownTenant) {
		t.Fatalf("evaluate without a policy = %v, want ErrUnknownTenant", err)
	}
	if _, err := e.SetPolicy("tenant-1", enterprisePolicy()); err != nil {
		t.Fatalf("policy must install, got %v", err)
	}
	blank := attestedDevice()
	blank.ID = "   "
	if _, err := e.Evaluate("tenant-1", blank); !errors.Is(err, impl.ErrBlankRegistration) {
		t.Fatalf("evaluate a registration without an id = %v, want ErrBlankRegistration", err)
	}
}

func TestEvaluateNoneConveyanceCapsStrength(t *testing.T) {
	e := impl.NewAuthenticatorAttestationPolicy(attestationLimits())
	p := impl.Policy{Conveyance: impl.ConveyanceNone, MinStrength: impl.StrengthLow}
	if _, err := e.SetPolicy("tenant-1", p); err != nil {
		t.Fatalf("none policy must install, got %v", err)
	}
	reg := impl.Registration{
		ID:         "reg-plain",
		AAGUID:     "aaguid-plain",
		Conveyance: impl.ConveyanceNone,
		Attested:   false,
	}
	d, err := e.Evaluate("tenant-1", reg)
	if err != nil {
		t.Fatalf("evaluate must succeed, got %v", err)
	}
	if !d.Accepted {
		t.Fatalf("none conveyance with a low floor must be accepted, got %+v", d)
	}
	if d.Strength != impl.StrengthLow {
		t.Fatalf("none conveyance strength = %q, want %q", d.Strength, impl.StrengthLow)
	}
	if d.Generation != 1 {
		t.Fatalf("decision generation = %d, want 1", d.Generation)
	}
	if d.Seq <= 0 {
		t.Fatalf("decision sequence = %d, want a positive number", d.Seq)
	}
}

func TestEvaluateRequiresAttestationWhenPolicySaysSo(t *testing.T) {
	e := impl.NewAuthenticatorAttestationPolicy(attestationLimits())
	p := enterprisePolicy()
	p.RequireAttestation = true
	if _, err := e.SetPolicy("tenant-1", p); err != nil {
		t.Fatalf("policy must install, got %v", err)
	}
	reg := attestedDevice()
	reg.Attested = false
	d, err := e.Evaluate("tenant-1", reg)
	if err != nil {
		t.Fatalf("evaluate must succeed, got %v", err)
	}
	if d.Accepted {
		t.Fatalf("unattested registration must be rejected, got %+v", d)
	}
	if !hasCode(d.Codes, impl.ReasonAttestationRequired) {
		t.Fatalf("decision codes = %v, want %q", d.Codes, impl.ReasonAttestationRequired)
	}
}

func TestEvaluateRejectsAAGUIDOutsideAllowList(t *testing.T) {
	e := impl.NewAuthenticatorAttestationPolicy(attestationLimits())
	p := impl.Policy{
		Conveyance:   impl.ConveyanceEnterprise,
		AllowAAGUIDs: []string{"aaguid-bank-key"},
		TrustAnchors: []string{anchor()},
		MinStrength:  impl.StrengthLow,
	}
	if _, err := e.SetPolicy("tenant-1", p); err != nil {
		t.Fatalf("policy must install, got %v", err)
	}
	reg := attestedDevice()
	reg.AAGUID = "aaguid-console-key"
	d, err := e.Evaluate("tenant-1", reg)
	if err != nil {
		t.Fatalf("evaluate must succeed, got %v", err)
	}
	if d.Accepted {
		t.Fatalf("aaguid outside the allow list must be rejected, got %+v", d)
	}
	if !hasCode(d.Codes, impl.ReasonNotAllowlisted) {
		t.Fatalf("decision codes = %v, want %q", d.Codes, impl.ReasonNotAllowlisted)
	}
}

func TestEvaluateEnterpriseChainAtAnchorIsHighStrength(t *testing.T) {
	e := impl.NewAuthenticatorAttestationPolicy(attestationLimits())
	p := impl.Policy{
		Conveyance:   impl.ConveyanceEnterprise,
		TrustAnchors: []string{anchor()},
		MinStrength:  impl.StrengthHigh,
	}
	if _, err := e.SetPolicy("tenant-1", p); err != nil {
		t.Fatalf("policy must install, got %v", err)
	}
	d, err := e.Evaluate("tenant-1", attestedDevice())
	if err != nil {
		t.Fatalf("evaluate must succeed, got %v", err)
	}
	if !d.Accepted {
		t.Fatalf("enterprise chain ending at a trusted anchor must be accepted, got %+v", d)
	}
	if d.Strength != impl.StrengthHigh {
		t.Fatalf("strength = %q, want %q", d.Strength, impl.StrengthHigh)
	}
	if !hasCode(d.Codes, impl.ReasonAllowlisted) && len(d.Codes) == 0 {
		t.Fatalf("accepted decision must carry at least one reason, got %v", d.Codes)
	}
}

func TestEvaluateCacheFollowsPolicyGeneration(t *testing.T) {
	e := impl.NewAuthenticatorAttestationPolicy(attestationLimits())
	open := impl.Policy{
		Conveyance:   impl.ConveyanceEnterprise,
		TrustAnchors: []string{anchor()},
		MinStrength:  impl.StrengthLow,
	}
	if _, err := e.SetPolicy("tenant-1", open); err != nil {
		t.Fatalf("policy must install, got %v", err)
	}
	first, err := e.Evaluate("tenant-1", attestedDevice())
	if err != nil {
		t.Fatalf("first evaluate must succeed, got %v", err)
	}
	if !first.Accepted || first.Generation != 1 {
		t.Fatalf("first decision = %+v, want accepted under generation 1", first)
	}
	denied := open
	denied.DenyAAGUIDs = []string{"aaguid-bank-key"}
	if _, err := e.SetPolicy("tenant-1", denied); err != nil {
		t.Fatalf("second policy must install, got %v", err)
	}
	revisited, err := e.Reevaluate("tenant-1")
	if err != nil {
		t.Fatalf("reevaluate must succeed, got %v", err)
	}
	if revisited != 1 {
		t.Fatalf("reevaluated credentials = %d, want 1", revisited)
	}
	second, err := e.Evaluate("tenant-1", attestedDevice())
	if err != nil {
		t.Fatalf("second evaluate must succeed, got %v", err)
	}
	if second.Accepted {
		t.Fatalf("stale cache survived the policy bump, got %+v", second)
	}
	if second.Generation != 2 {
		t.Fatalf("decision generation = %d, want 2", second.Generation)
	}
	if !hasCode(second.Codes, impl.ReasonDenylisted) {
		t.Fatalf("decision codes = %v, want %q", second.Codes, impl.ReasonDenylisted)
	}
	if second.ID == first.ID {
		t.Fatalf("a fresh evaluation must get a fresh decision id, both were %q", second.ID)
	}
}

func TestExplainListsReasonsForDecision(t *testing.T) {
	e := impl.NewAuthenticatorAttestationPolicy(attestationLimits())
	if _, err := e.SetPolicy("tenant-1", enterprisePolicy()); err != nil {
		t.Fatalf("policy must install, got %v", err)
	}
	d, err := e.Evaluate("tenant-1", attestedDevice())
	if err != nil {
		t.Fatalf("evaluate must succeed, got %v", err)
	}
	reasons := e.Explain(d.ID)
	if len(reasons) != len(d.Codes) {
		t.Fatalf("explained %d reasons for %d codes", len(reasons), len(d.Codes))
	}
	seen := map[string]bool{}
	for _, r := range reasons {
		seen[r.Code] = true
		if r.Detail == "" {
			t.Fatalf("reason %q must carry a detail", r.Code)
		}
	}
	for _, code := range d.Codes {
		if !seen[code] {
			t.Fatalf("code %q missing from the explanation", code)
		}
	}
	if got := e.Explain("decision-missing"); len(got) != 0 {
		t.Fatalf("unknown decision explained %d reasons, want 0", len(got))
	}
}

func TestSnapshotRestoreKeepsPolicyAndDecisions(t *testing.T) {
	e := impl.NewAuthenticatorAttestationPolicy(attestationLimits())
	if _, err := e.SetPolicy("tenant-1", enterprisePolicy()); err != nil {
		t.Fatalf("first policy must install, got %v", err)
	}
	if _, err := e.SetPolicy("tenant-1", enterprisePolicy()); err != nil {
		t.Fatalf("second policy must install, got %v", err)
	}
	d, err := e.Evaluate("tenant-1", attestedDevice())
	if err != nil {
		t.Fatalf("evaluate must succeed, got %v", err)
	}
	data, err := e.Snapshot()
	if err != nil {
		t.Fatalf("snapshot must succeed, got %v", err)
	}
	restored := impl.NewAuthenticatorAttestationPolicy(attestationLimits())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore must succeed, got %v", err)
	}
	p, err := restored.Policy("tenant-1")
	if err != nil {
		t.Fatalf("restored policy must be readable, got %v", err)
	}
	if p.Conveyance != impl.ConveyanceEnterprise {
		t.Fatalf("restored conveyance = %q", p.Conveyance)
	}
	got, ok := restored.Decision(d.ID)
	if !ok {
		t.Fatalf("restored decision %q must be readable", d.ID)
	}
	if got.Accepted != d.Accepted || got.Generation != d.Generation || got.RegistrationID != d.RegistrationID {
		t.Fatalf("restored decision = %+v, want %+v", got, d)
	}
	if len(restored.Versions("tenant-1")) != 2 {
		t.Fatalf("restored versions = %d, want 2", len(restored.Versions("tenant-1")))
	}
	if err := restored.Restore([]byte("<not a snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt restore = %v, want ErrBadSnapshot", err)
	}
	if _, ok := restored.Decision(d.ID); !ok {
		t.Fatalf("a rejected restore must not wipe the live decisions")
	}
	if _, err := restored.Evaluate("tenant-1", attestedDevice()); err != nil {
		t.Fatalf("evaluate after a rejected restore must work, got %v", err)
	}
}

func TestConcurrentSetPolicyKeepsGenerationsUnique(t *testing.T) {
	e := impl.NewAuthenticatorAttestationPolicy(impl.Limits{
		MaxEntriesPerList: 16,
		MaxTrustAnchors:   16,
		MaxVersions:       32,
	})
	const writers = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	generations := make([]int, 0, writers)
	var failures []error
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func(i int) {
			defer wg.Done()
			p := enterprisePolicy()
			p.DenyAAGUIDs = []string{fmt.Sprintf("aaguid-retired-%02d", i)}
			v, err := e.SetPolicy("tenant-1", p)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failures = append(failures, err)
				return
			}
			generations = append(generations, v.Generation)
		}(i)
	}
	wg.Wait()
	if len(failures) != 0 {
		t.Fatalf("concurrent policy writes failed: %v", failures)
	}
	seen := map[int]bool{}
	for _, g := range generations {
		if seen[g] {
			t.Fatalf("generation %d handed out twice", g)
		}
		seen[g] = true
	}
	if len(seen) != writers {
		t.Fatalf("distinct generations = %d, want %d", len(seen), writers)
	}
	for want := 1; want <= writers; want++ {
		if !seen[want] {
			t.Fatalf("generation %d missing from %v", want, generations)
		}
	}
	p, err := e.Policy("tenant-1")
	if err != nil {
		t.Fatalf("policy must be readable, got %v", err)
	}
	if len(p.DenyAAGUIDs) != 1 {
		t.Fatalf("last policy deny list = %v, want exactly one entry", p.DenyAAGUIDs)
	}
}
