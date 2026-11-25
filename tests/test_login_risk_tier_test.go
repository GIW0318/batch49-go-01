package task

import (
	"errors"
	"testing"
	"time"

	impl "gsb/batch49-go-safety-a-29"
)

type fakeClock struct {
	t time.Time
}

func (c *fakeClock) now() time.Time {
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.t = c.t.Add(d)
}

func riskLimits() impl.Limits {
	return impl.Limits{MaxDevicesPerUser: 3, MaxTiersPerUser: 10}
}

func newRisk() (*impl.LoginRiskTier, *fakeClock) {
	clock := &fakeClock{t: time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)}
	return impl.NewLoginRiskTier(riskLimits(), clock.now), clock
}

func cleanEvent(user string) impl.LoginEvent {
	return impl.LoginEvent{UserID: user, DeviceID: "device-1", Region: "cn-east", Hour: 14}
}

func TestClassifyCleanEventIsLow(t *testing.T) {
	r, _ := newRisk()
	tier, err := r.Classify(cleanEvent("user-1"))
	if err != nil {
		t.Fatalf("classify must succeed, got %v", err)
	}
	if tier.ID == "" {
		t.Fatalf("tier id must not be empty")
	}
	if tier.Level != impl.TierLow {
		t.Fatalf("clean tier = %q, want %q", tier.Level, impl.TierLow)
	}
	if tier.Score != 0 {
		t.Fatalf("clean score = %d, want 0", tier.Score)
	}
	if tier.Reason != impl.ReasonClean {
		t.Fatalf("clean reason = %q, want %q", tier.Reason, impl.ReasonClean)
	}
	if tier.Seq <= 0 {
		t.Fatalf("tier seq = %d, want a positive number", tier.Seq)
	}
	tiers := r.Tiers("user-1")
	if len(tiers) != 1 || tiers[0].ID != tier.ID {
		t.Fatalf("tiers = %+v, want exactly the graded record", tiers)
	}
}

func TestClassifyRejectsBadEvents(t *testing.T) {
	r, _ := newRisk()
	blankUser := cleanEvent("   ")
	if _, err := r.Classify(blankUser); !errors.Is(err, impl.ErrBadEvent) {
		t.Fatalf("blank user must fail with ErrBadEvent, got %v", err)
	}
	badHour := cleanEvent("user-2")
	badHour.Hour = 24
	if _, err := r.Classify(badHour); !errors.Is(err, impl.ErrBadEvent) {
		t.Fatalf("hour 24 must fail with ErrBadEvent, got %v", err)
	}
	badRegion := cleanEvent("user-3")
	badRegion.Region = "   "
	if _, err := r.Classify(badRegion); !errors.Is(err, impl.ErrBadEvent) {
		t.Fatalf("blank region must fail with ErrBadEvent, got %v", err)
	}
	if got := len(r.Tiers("user-2")); got != 0 {
		t.Fatalf("rejected events must not be recorded, got %d", got)
	}
}

func TestClassifyBlockedRegionIsSevere(t *testing.T) {
	r, _ := newRisk()
	e := cleanEvent("user-4")
	e.Region = impl.RegionBlocked
	tier, err := r.Classify(e)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if tier.Level != impl.TierSevere {
		t.Fatalf("blocked region tier = %q, want %q", tier.Level, impl.TierSevere)
	}
	if tier.Reason != impl.ReasonHardRule {
		t.Fatalf("blocked region reason = %q, want %q", tier.Reason, impl.ReasonHardRule)
	}
}

func TestClassifySoftSignalsAccumulate(t *testing.T) {
	r, _ := newRisk()
	one := cleanEvent("user-5")
	one.NewDevice = true
	tier, err := r.Classify(one)
	if err != nil {
		t.Fatalf("classify one signal: %v", err)
	}
	if tier.Level != impl.TierMedium || tier.Score != 1 {
		t.Fatalf("one signal tier=%q score=%d, want medium/1", tier.Level, tier.Score)
	}
	if tier.Reason != impl.ReasonSoftSignals {
		t.Fatalf("one signal reason = %q, want %q", tier.Reason, impl.ReasonSoftSignals)
	}

	two := cleanEvent("user-6")
	two.NewDevice = true
	two.UnusualHour = true
	tier, err = r.Classify(two)
	if err != nil {
		t.Fatalf("classify two signals: %v", err)
	}
	if tier.Level != impl.TierHigh || tier.Score != 2 {
		t.Fatalf("two signals tier=%q score=%d, want high/2", tier.Level, tier.Score)
	}
}

func TestClassifyThreeSignalsSevere(t *testing.T) {
	r, _ := newRisk()
	e := cleanEvent("user-7")
	e.NewDevice = true
	e.UnusualHour = true
	e.NewNetwork = true
	tier, err := r.Classify(e)
	if err != nil {
		t.Fatalf("classify three signals: %v", err)
	}
	if tier.Level != impl.TierSevere || tier.Score != 3 {
		t.Fatalf("three signals tier=%q score=%d, want severe/3", tier.Level, tier.Score)
	}
}

func TestClassifyEnforcesRecordLimit(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)}
	r := impl.NewLoginRiskTier(impl.Limits{MaxDevicesPerUser: 3, MaxTiersPerUser: 3}, clock.now)
	seqs := make([]int64, 0, 5)
	for i := 0; i < 5; i++ {
		e := cleanEvent("user-8")
		e.Hour = 8 + i
		tier, err := r.Classify(e)
		if err != nil {
			t.Fatalf("classify %d: %v", i, err)
		}
		seqs = append(seqs, tier.Seq)
	}
	tiers := r.Tiers("user-8")
	if len(tiers) != 3 {
		t.Fatalf("retained tiers = %d, want the cap 3", len(tiers))
	}
	for i, want := range seqs[2:] {
		if tiers[i].Seq != want {
			t.Fatalf("retained tier %d has seq %d, want the newest window %d", i, tiers[i].Seq, want)
		}
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] <= seqs[i-1] {
			t.Fatalf("tier sequence must strictly increase, got %v", seqs)
		}
	}
}

func TestChallengeKindByTier(t *testing.T) {
	r, _ := newRisk()
	lowEvent := cleanEvent("user-9")
	lowTier, err := r.Classify(lowEvent)
	if err != nil {
		t.Fatalf("classify low: %v", err)
	}
	medEvent := cleanEvent("user-10")
	medEvent.NewDevice = true
	medTier, err := r.Classify(medEvent)
	if err != nil {
		t.Fatalf("classify medium: %v", err)
	}
	highEvent := cleanEvent("user-11")
	highEvent.NewDevice = true
	highEvent.UnusualHour = true
	highTier, err := r.Classify(highEvent)
	if err != nil {
		t.Fatalf("classify high: %v", err)
	}
	sevEvent := cleanEvent("user-12")
	sevEvent.Region = impl.RegionBlocked
	sevTier, err := r.Classify(sevEvent)
	if err != nil {
		t.Fatalf("classify severe: %v", err)
	}
	cases := []struct {
		tier impl.Tier
		kind string
	}{
		{lowTier, impl.ChallengeNone},
		{medTier, impl.ChallengeSMS},
		{highTier, impl.ChallengeTOTP},
		{sevTier, impl.ChallengeBlock},
	}
	for _, c := range cases {
		ch, err := r.Challenge(c.tier.ID, c.tier.UserID)
		if err != nil {
			t.Fatalf("challenge for %q: %v", c.tier.Level, err)
		}
		if ch.Kind != c.kind {
			t.Fatalf("challenge for %q = %q, want %q", c.tier.Level, ch.Kind, c.kind)
		}
		if ch.TierID != c.tier.ID || ch.UserID != c.tier.UserID {
			t.Fatalf("challenge carries tier=%q user=%q, want %q/%q", ch.TierID, ch.UserID, c.tier.ID, c.tier.UserID)
		}
	}
	if _, err := r.Challenge("tier-404", "user-9"); !errors.Is(err, impl.ErrUnknownTier) {
		t.Fatalf("unknown tier must fail with ErrUnknownTier, got %v", err)
	}
}

func TestVerifyAcceptsMatchingCode(t *testing.T) {
	r, _ := newRisk()
	e := cleanEvent("user-13")
	e.NewDevice = true
	tier, err := r.Classify(e)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	ch, err := r.Challenge(tier.ID, "user-13")
	if err != nil {
		t.Fatalf("challenge: %v", err)
	}
	if ch.Code == "" {
		t.Fatalf("challenge must carry a verification code")
	}
	dec, err := r.Verify(tier.ID, impl.Proof{Code: ch.Code})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if dec.Result != impl.DecisionAllow {
		t.Fatalf("decision = %q, want %q", dec.Result, impl.DecisionAllow)
	}
	if dec.Attempts != 0 || dec.Locked {
		t.Fatalf("decision = %+v, want no attempts and no lock", dec)
	}
}

func TestVerifyRejectsWrongCode(t *testing.T) {
	r, _ := newRisk()
	e := cleanEvent("user-14")
	e.NewDevice = true
	tier, err := r.Classify(e)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if _, err := r.Challenge(tier.ID, "user-14"); err != nil {
		t.Fatalf("challenge: %v", err)
	}
	dec, err := r.Verify(tier.ID, impl.Proof{Code: "000000-wrong"})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if dec.Result != impl.DecisionDeny {
		t.Fatalf("decision = %q, want %q", dec.Result, impl.DecisionDeny)
	}
	if dec.Reason != impl.ReasonCodeMismatch {
		t.Fatalf("deny reason = %q, want %q", dec.Reason, impl.ReasonCodeMismatch)
	}
	if dec.Attempts != 1 || dec.Locked {
		t.Fatalf("decision = %+v, want attempts 1 and no lock", dec)
	}
}

func TestVerifyLocksAfterMaxAttempts(t *testing.T) {
	r, _ := newRisk()
	e := cleanEvent("user-15")
	e.NewDevice = true
	tier, err := r.Classify(e)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	ch, err := r.Challenge(tier.ID, "user-15")
	if err != nil {
		t.Fatalf("challenge: %v", err)
	}
	for i := 1; i <= impl.MaxAttempts; i++ {
		dec, err := r.Verify(tier.ID, impl.Proof{Code: "bad-code"})
		if err != nil {
			t.Fatalf("wrong proof %d: %v", i, err)
		}
		if dec.Attempts != i {
			t.Fatalf("wrong proof %d recorded attempts %d", i, dec.Attempts)
		}
		if i == impl.MaxAttempts {
			if !dec.Locked {
				t.Fatalf("the last tolerated wrong proof must lock the challenge")
			}
			if dec.Reason != impl.ReasonLocked {
				t.Fatalf("lock reason = %q, want %q", dec.Reason, impl.ReasonLocked)
			}
		}
	}
	if _, err := r.Verify(tier.ID, impl.Proof{Code: ch.Code}); !errors.Is(err, impl.ErrTooManyAttempts) {
		t.Fatalf("verify after the lock must fail with ErrTooManyAttempts, got %v", err)
	}
}

func TestTrustDeviceValidation(t *testing.T) {
	r, _ := newRisk()
	if err := r.TrustDevice("user-16", "   ", time.Hour); !errors.Is(err, impl.ErrBlankDevice) {
		t.Fatalf("blank device must fail with ErrBlankDevice, got %v", err)
	}
	if err := r.TrustDevice("   ", "device-1", time.Hour); !errors.Is(err, impl.ErrBlankDevice) {
		t.Fatalf("blank user must fail with ErrBlankDevice, got %v", err)
	}
	if err := r.TrustDevice("user-16", "device-1", 0); !errors.Is(err, impl.ErrBadTTL) {
		t.Fatalf("zero ttl must fail with ErrBadTTL, got %v", err)
	}
	if r.Trusted("user-16", "device-1") {
		t.Fatalf("a device must not be trusted before TrustDevice")
	}
	if err := r.TrustDevice("user-16", "device-1", time.Hour); err != nil {
		t.Fatalf("trust device: %v", err)
	}
	if !r.Trusted("user-16", "device-1") {
		t.Fatalf("device must be trusted after TrustDevice")
	}
}

func TestTrustDeviceEnforcesCap(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)}
	r := impl.NewLoginRiskTier(impl.Limits{MaxDevicesPerUser: 2, MaxTiersPerUser: 10}, clock.now)
	for _, device := range []string{"device-1", "device-2"} {
		if err := r.TrustDevice("user-17", device, time.Hour); err != nil {
			t.Fatalf("trust %s: %v", device, err)
		}
	}
	if err := r.TrustDevice("user-17", "device-3", time.Hour); !errors.Is(err, impl.ErrDeviceLimit) {
		t.Fatalf("third trusted device must fail with ErrDeviceLimit, got %v", err)
	}
	if r.Trusted("user-17", "device-3") {
		t.Fatalf("rejected device must not be trusted")
	}
}

func TestTrustedDeviceExemptionAndExpiry(t *testing.T) {
	r, clock := newRisk()
	if err := r.TrustDevice("user-18", "device-t", time.Hour); err != nil {
		t.Fatalf("trust device: %v", err)
	}
	e := cleanEvent("user-18")
	e.DeviceID = "device-t"
	e.NewDevice = true
	tier, err := r.Classify(e)
	if err != nil {
		t.Fatalf("classify on a trusted device: %v", err)
	}
	if tier.Level != impl.TierLow {
		t.Fatalf("trusted device tier = %q, want %q", tier.Level, impl.TierLow)
	}
	clock.advance(2 * time.Hour)
	expired := cleanEvent("user-18")
	expired.DeviceID = "device-t"
	expired.NewDevice = true
	tier, err = r.Classify(expired)
	if err != nil {
		t.Fatalf("classify after the trust window: %v", err)
	}
	if tier.Level != impl.TierMedium {
		t.Fatalf("tier after the trust window = %q, want %q", tier.Level, impl.TierMedium)
	}
	if r.Trusted("user-18", "device-t") {
		t.Fatalf("device must not be trusted after its ttl elapsed")
	}
}

func TestOverrideRequiresActorAndReason(t *testing.T) {
	r, _ := newRisk()
	tier, err := r.Classify(cleanEvent("user-19"))
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if err := r.Override(tier.ID, "", "manual review"); !errors.Is(err, impl.ErrBlankField) {
		t.Fatalf("override without an actor must fail with ErrBlankField, got %v", err)
	}
	if err := r.Override(tier.ID, "ops-1", "  "); !errors.Is(err, impl.ErrBlankField) {
		t.Fatalf("override without a reason must fail with ErrBlankField, got %v", err)
	}
	if err := r.Override("tier-404", "ops-1", "manual review"); !errors.Is(err, impl.ErrUnknownTier) {
		t.Fatalf("override of an unknown tier must fail with ErrUnknownTier, got %v", err)
	}
	if err := r.Override(tier.ID, "ops-1", "manual review"); err != nil {
		t.Fatalf("override: %v", err)
	}
	tiers := r.Tiers("user-19")
	if len(tiers) != 1 {
		t.Fatalf("tiers = %d, want 1", len(tiers))
	}
	if !tiers[0].Overridden {
		t.Fatalf("overridden flag must be set after the override")
	}
	if tiers[0].Level != impl.TierMedium {
		t.Fatalf("overridden level = %q, want %q", tiers[0].Level, impl.TierMedium)
	}
	if err := r.Override(tier.ID, "ops-1", "manual review"); !errors.Is(err, impl.ErrAlreadyOverridden) {
		t.Fatalf("second override must fail with ErrAlreadyOverridden, got %v", err)
	}
}

func TestSnapshotRestoreKeepsTrustAndRecords(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)}
	limits := impl.Limits{MaxDevicesPerUser: 3, MaxTiersPerUser: 10}
	r := impl.NewLoginRiskTier(limits, clock.now)
	first, err := r.Classify(cleanEvent("user-20"))
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if err := r.TrustDevice("user-20", "device-1", time.Hour); err != nil {
		t.Fatalf("trust device: %v", err)
	}
	if err := r.LoadSnapshot([]byte("<<broken>>")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := len(r.Tiers("user-20")); got != 1 {
		t.Fatalf("state after a rejected snapshot changed, tiers = %d", got)
	}
	data, err := r.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewLoginRiskTier(limits, clock.now)
	if err := restored.LoadSnapshot(data); err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	tiers := restored.Tiers("user-20")
	if len(tiers) != 1 || tiers[0].ID != first.ID || tiers[0].Level != impl.TierLow {
		t.Fatalf("restored tiers = %+v, want the graded record", tiers)
	}
	if !restored.Trusted("user-20", "device-1") {
		t.Fatalf("trusted device must survive the restart")
	}
	e := cleanEvent("user-20")
	e.NewDevice = true
	next, err := restored.Classify(e)
	if err != nil {
		t.Fatalf("classify after restore: %v", err)
	}
	if next.Seq <= first.Seq {
		t.Fatalf("seq after restore = %d, must continue past %d", next.Seq, first.Seq)
	}
}
