package task

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	impl "gsb/batch49-go-safety-a-01"
)

var campaignClock = time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)

func campaignLimits() impl.Limits {
	return impl.Limits{
		MaxActiveCampaignsPerTenant: 2,
		MaxCredentialsPerUser:       4,
		MaxOverlapHours:             72,
	}
}

func newCredential(id, rpID string) impl.Credential {
	return impl.Credential{
		ID:           id,
		RPID:         rpID,
		PublicKey:    "pk-" + id,
		SignCount:    7,
		RegisteredAt: campaignClock,
	}
}

// campaignFixture plans and starts one campaign, failing the test when the
// baseline setup does not work.
func campaignFixture(t *testing.T) (*impl.RpIdMigrationCampaign, impl.Campaign) {
	t.Helper()
	c := impl.NewRpIdMigrationCampaign(campaignLimits())
	c.SetNow(campaignClock)
	cp, err := c.Plan("tenant-1", "login.old.example", "login.new.example")
	if err != nil {
		t.Fatalf("plan must succeed, got %v", err)
	}
	if err := c.Start(cp.ID); err != nil {
		t.Fatalf("start must succeed, got %v", err)
	}
	return c, cp
}

func TestPlanRejectsBlankTenant(t *testing.T) {
	c := impl.NewRpIdMigrationCampaign(campaignLimits())
	c.SetNow(campaignClock)
	if _, err := c.Plan("", "login.old.example", "login.new.example"); !errors.Is(err, impl.ErrBlankTenant) {
		t.Fatalf("plan with a blank tenant = %v, want ErrBlankTenant", err)
	}
	if _, err := c.Plan("tenant-1", "login.old.example", "  "); !errors.Is(err, impl.ErrBlankTenant) {
		t.Fatalf("plan with a blank target domain = %v, want ErrBlankTenant", err)
	}
}

func TestPlanRejectsSameDomain(t *testing.T) {
	c := impl.NewRpIdMigrationCampaign(campaignLimits())
	c.SetNow(campaignClock)
	if _, err := c.Plan("tenant-1", "login.same.example", "login.same.example"); !errors.Is(err, impl.ErrDomainUnchanged) {
		t.Fatalf("plan moving a domain onto itself = %v, want ErrDomainUnchanged", err)
	}
}

func TestPlanBuildsPlannedCampaign(t *testing.T) {
	c := impl.NewRpIdMigrationCampaign(campaignLimits())
	c.SetNow(campaignClock)
	cp, err := c.Plan("tenant-1", "login.old.example", "login.new.example")
	if err != nil {
		t.Fatalf("plan must succeed, got %v", err)
	}
	if cp.ID == "" {
		t.Fatalf("planned campaign must carry an id")
	}
	if cp.State != impl.CampaignPlanned {
		t.Fatalf("new campaign state = %q, want %q", cp.State, impl.CampaignPlanned)
	}
	if cp.TenantID != "tenant-1" || cp.FromDomain != "login.old.example" || cp.ToDomain != "login.new.example" {
		t.Fatalf("campaign fields not stored: %+v", cp)
	}
	if !cp.StartedAt.Equal(campaignClock) {
		t.Fatalf("campaign start = %v, want %v", cp.StartedAt, campaignClock)
	}
	stored, ok := c.Campaign(cp.ID)
	if !ok {
		t.Fatalf("planned campaign must be readable by id")
	}
	if stored.State != impl.CampaignPlanned {
		t.Fatalf("stored state = %q, want %q", stored.State, impl.CampaignPlanned)
	}
	if _, ok := c.Campaign("campaign-missing"); ok {
		t.Fatalf("unknown campaign must not resolve")
	}
}

func TestStartOnlyOnceAndRecordRequiresActive(t *testing.T) {
	c := impl.NewRpIdMigrationCampaign(campaignLimits())
	c.SetNow(campaignClock)
	cp, err := c.Plan("tenant-1", "login.old.example", "login.new.example")
	if err != nil {
		t.Fatalf("plan must succeed, got %v", err)
	}
	if _, err := c.Record(cp.ID, "user-1", newCredential("new-1", "login.new.example")); !errors.Is(err, impl.ErrCampaignNotActive) {
		t.Fatalf("record before start = %v, want ErrCampaignNotActive", err)
	}
	if err := c.Start(cp.ID); err != nil {
		t.Fatalf("first start must succeed, got %v", err)
	}
	if err := c.Start(cp.ID); !errors.Is(err, impl.ErrCampaignNotActive) {
		t.Fatalf("second start = %v, want ErrCampaignNotActive", err)
	}
	if err := c.Start("campaign-missing"); !errors.Is(err, impl.ErrCampaignNotFound) {
		t.Fatalf("start of unknown campaign = %v, want ErrCampaignNotFound", err)
	}
}

func TestActiveCampaignLimitPerTenant(t *testing.T) {
	c := impl.NewRpIdMigrationCampaign(campaignLimits())
	c.SetNow(campaignClock)
	for i := 0; i < 2; i++ {
		cp, err := c.Plan("tenant-1", fmt.Sprintf("login.o%d.example", i), fmt.Sprintf("login.n%d.example", i))
		if err != nil {
			t.Fatalf("plan %d must succeed, got %v", i, err)
		}
		if err := c.Start(cp.ID); err != nil {
			t.Fatalf("start %d must succeed, got %v", i, err)
		}
	}
	third, err := c.Plan("tenant-1", "login.third-old.example", "login.third-new.example")
	if err != nil {
		t.Fatalf("planning a third campaign must be allowed, got %v", err)
	}
	if err := c.Start(third.ID); !errors.Is(err, impl.ErrActiveCampaignLimit) {
		t.Fatalf("third concurrent start = %v, want ErrActiveCampaignLimit", err)
	}
	if st, ok := c.Campaign(third.ID); !ok || st.State != impl.CampaignPlanned {
		t.Fatalf("rejected campaign must stay planned, got %+v ok=%v", st, ok)
	}
}

func TestRecordRejectsForeignRPID(t *testing.T) {
	c, cp := campaignFixture(t)
	if _, err := c.Record(cp.ID, "user-1", newCredential("old-1", "login.old.example")); !errors.Is(err, impl.ErrCredentialDomain) {
		t.Fatalf("recording an old domain credential = %v, want ErrCredentialDomain", err)
	}
	prog, err := c.Status(cp.ID, "user-1")
	if err != nil {
		t.Fatalf("status must succeed, got %v", err)
	}
	if len(prog.Migrated) != 0 {
		t.Fatalf("rejected credential must not be stored, migrated = %+v", prog.Migrated)
	}
	if _, err := c.Record(cp.ID, "", newCredential("new-1", "login.new.example")); !errors.Is(err, impl.ErrBlankUser) {
		t.Fatalf("record without a user = %v, want ErrBlankUser", err)
	}
}

func TestRecordRejectsDuplicateCredential(t *testing.T) {
	c, cp := campaignFixture(t)
	cred := newCredential("new-1", "login.new.example")
	if _, err := c.Record(cp.ID, "user-1", cred); err != nil {
		t.Fatalf("first record must succeed, got %v", err)
	}
	if _, err := c.Record(cp.ID, "user-1", cred); !errors.Is(err, impl.ErrDuplicateCredential) {
		t.Fatalf("duplicate record = %v, want ErrDuplicateCredential", err)
	}
	prog, err := c.Status(cp.ID, "user-1")
	if err != nil {
		t.Fatalf("status must succeed, got %v", err)
	}
	if len(prog.Migrated) != 1 {
		t.Fatalf("duplicate must not grow the list, migrated = %d", len(prog.Migrated))
	}
}

func TestRecordRespectsCredentialCap(t *testing.T) {
	c, cp := campaignFixture(t)
	for i := 0; i < 4; i++ {
		if _, err := c.Record(cp.ID, "user-1", newCredential(fmt.Sprintf("new-%d", i), "login.new.example")); err != nil {
			t.Fatalf("record %d must succeed, got %v", i, err)
		}
	}
	if _, err := c.Record(cp.ID, "user-1", newCredential("new-over", "login.new.example")); !errors.Is(err, impl.ErrCredentialLimit) {
		t.Fatalf("fifth credential = %v, want ErrCredentialLimit", err)
	}
	prog, err := c.Status(cp.ID, "user-1")
	if err != nil {
		t.Fatalf("status must succeed, got %v", err)
	}
	if len(prog.Migrated) != 4 {
		t.Fatalf("stored credentials = %d, want 4", len(prog.Migrated))
	}
}

func TestStatusTracksMigratedAndRemaining(t *testing.T) {
	c, cp := campaignFixture(t)
	legacy := []impl.Credential{
		newCredential("old-1", "login.old.example"),
		newCredential("old-2", "login.old.example"),
		newCredential("old-3", "login.old.example"),
	}
	if err := c.ImportLegacy(cp.ID, "user-1", legacy); err != nil {
		t.Fatalf("legacy import must succeed, got %v", err)
	}
	if _, err := c.Record(cp.ID, "user-1", newCredential("new-1", "login.new.example")); err != nil {
		t.Fatalf("record must succeed, got %v", err)
	}
	prog, err := c.Status(cp.ID, "user-1")
	if err != nil {
		t.Fatalf("status must succeed, got %v", err)
	}
	if len(prog.Migrated) != 1 || prog.Remaining != 2 {
		t.Fatalf("after one migration migrated=%d remaining=%d, want 1 and 2", len(prog.Migrated), prog.Remaining)
	}
	if prog.State != impl.ProgressPartial {
		t.Fatalf("partial state = %q, want %q", prog.State, impl.ProgressPartial)
	}
	if _, err := c.Record(cp.ID, "user-1", newCredential("new-2", "login.new.example")); err != nil {
		t.Fatalf("second record must succeed, got %v", err)
	}
	if _, err := c.Record(cp.ID, "user-1", newCredential("new-3", "login.new.example")); err != nil {
		t.Fatalf("third record must succeed, got %v", err)
	}
	prog, err = c.Status(cp.ID, "user-1")
	if err != nil {
		t.Fatalf("status must succeed, got %v", err)
	}
	if prog.Remaining != 0 || prog.State != impl.ProgressMigrated {
		t.Fatalf("fully migrated user remaining=%d state=%q, want 0 and %q", prog.Remaining, prog.State, impl.ProgressMigrated)
	}
	if prog.Seq <= 0 {
		t.Fatalf("progress sequence = %d, want a positive number", prog.Seq)
	}
	verdict, err := c.LoginPolicy(cp.ID, "user-1", "login.new.example", "login.new.example")
	if err != nil {
		t.Fatalf("login policy must succeed, got %v", err)
	}
	if !verdict.Allowed {
		t.Fatalf("new domain login = %+v, want it allowed on the new rp id", verdict)
	}
	if err := c.ImportLegacy(cp.ID, "user-1", []impl.Credential{newCredential("old-x", "login.new.example")}); !errors.Is(err, impl.ErrCredentialDomain) {
		t.Fatalf("legacy import of a new domain credential = %v, want ErrCredentialDomain", err)
	}
}

func TestSetDeadlineRejectsBeforeStartAndBeyondCap(t *testing.T) {
	c, cp := campaignFixture(t)
	if err := c.SetDeadline(cp.ID, campaignClock.Add(-time.Hour)); !errors.Is(err, impl.ErrDeadlineInvalid) {
		t.Fatalf("deadline before start = %v, want ErrDeadlineInvalid", err)
	}
	if err := c.SetDeadline(cp.ID, campaignClock.Add(100*time.Hour)); !errors.Is(err, impl.ErrDeadlineTooLong) {
		t.Fatalf("deadline beyond the overlap cap = %v, want ErrDeadlineTooLong", err)
	}
	want := campaignClock.Add(48 * time.Hour)
	if err := c.SetDeadline(cp.ID, want); err != nil {
		t.Fatalf("valid deadline must be accepted, got %v", err)
	}
	stored, ok := c.Campaign(cp.ID)
	if !ok {
		t.Fatalf("campaign must exist after setting the deadline")
	}
	if !stored.Deadline.Equal(want) {
		t.Fatalf("stored deadline = %v, want %v", stored.Deadline, want)
	}
	if err := c.SetDeadline("campaign-missing", want); !errors.Is(err, impl.ErrCampaignNotFound) {
		t.Fatalf("deadline on an unknown campaign = %v, want ErrCampaignNotFound", err)
	}
}

func TestSweepClosesCampaignAfterDeadline(t *testing.T) {
	c, cp := campaignFixture(t)
	if err := c.SetDeadline(cp.ID, campaignClock.Add(48*time.Hour)); err != nil {
		t.Fatalf("deadline must be accepted, got %v", err)
	}
	closed, err := c.Sweep(campaignClock.Add(47 * time.Hour))
	if err != nil {
		t.Fatalf("early sweep must succeed, got %v", err)
	}
	if closed != 0 {
		t.Fatalf("early sweep closed %d campaigns, want 0", closed)
	}
	closed, err = c.Sweep(campaignClock.Add(49 * time.Hour))
	if err != nil {
		t.Fatalf("late sweep must succeed, got %v", err)
	}
	if closed != 1 {
		t.Fatalf("late sweep closed %d campaigns, want 1", closed)
	}
	stored, ok := c.Campaign(cp.ID)
	if !ok || stored.State != impl.CampaignClosed {
		t.Fatalf("campaign after sweep = %+v ok=%v, want %q", stored, ok, impl.CampaignClosed)
	}
	if _, err := c.Record(cp.ID, "user-1", newCredential("new-1", "login.new.example")); !errors.Is(err, impl.ErrCampaignNotActive) {
		t.Fatalf("record after close = %v, want ErrCampaignNotActive", err)
	}
}

func TestRollbackMarksCampaignRolledBack(t *testing.T) {
	c, cp := campaignFixture(t)
	if err := c.ImportLegacy(cp.ID, "user-1", []impl.Credential{newCredential("old-1", "login.old.example")}); err != nil {
		t.Fatalf("legacy import must succeed, got %v", err)
	}
	if _, err := c.Record(cp.ID, "user-1", newCredential("new-1", "login.new.example")); err != nil {
		t.Fatalf("record must succeed, got %v", err)
	}
	if err := c.Rollback(cp.ID); err != nil {
		t.Fatalf("rollback must succeed, got %v", err)
	}
	stored, ok := c.Campaign(cp.ID)
	if !ok || stored.State != impl.CampaignRolledBack {
		t.Fatalf("campaign after rollback = %+v ok=%v, want %q", stored, ok, impl.CampaignRolledBack)
	}
	if _, err := c.Record(cp.ID, "user-1", newCredential("new-2", "login.new.example")); !errors.Is(err, impl.ErrCampaignNotActive) {
		t.Fatalf("record after rollback = %v, want ErrCampaignNotActive", err)
	}
	if err := c.Rollback(cp.ID); !errors.Is(err, impl.ErrCampaignNotActive) {
		t.Fatalf("second rollback = %v, want ErrCampaignNotActive", err)
	}
}

func TestSnapshotRestoreKeepsProgressDeadlineAndState(t *testing.T) {
	c, cp := campaignFixture(t)
	if err := c.ImportLegacy(cp.ID, "user-1", []impl.Credential{newCredential("old-1", "login.old.example")}); err != nil {
		t.Fatalf("legacy import must succeed, got %v", err)
	}
	if _, err := c.Record(cp.ID, "user-1", newCredential("new-1", "login.new.example")); err != nil {
		t.Fatalf("record must succeed, got %v", err)
	}
	want := campaignClock.Add(50 * time.Hour)
	if err := c.SetDeadline(cp.ID, want); err != nil {
		t.Fatalf("deadline must be accepted, got %v", err)
	}
	data, err := c.Snapshot()
	if err != nil {
		t.Fatalf("snapshot must succeed, got %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload must not be empty")
	}
	restored := impl.NewRpIdMigrationCampaign(campaignLimits())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore must succeed, got %v", err)
	}
	stored, ok := restored.Campaign(cp.ID)
	if !ok || stored.State != impl.CampaignActive || !stored.Deadline.Equal(want) {
		t.Fatalf("restored campaign = %+v ok=%v, want active with deadline %v", stored, ok, want)
	}
	prog, err := restored.Status(cp.ID, "user-1")
	if err != nil {
		t.Fatalf("restored status must succeed, got %v", err)
	}
	if len(prog.Migrated) != 1 || len(prog.Legacy) != 1 {
		t.Fatalf("restored progress migrated=%d legacy=%d, want 1 and 1", len(prog.Migrated), len(prog.Legacy))
	}
	if !prog.Deadline.Equal(want) {
		t.Fatalf("restored per user deadline = %v, want %v", prog.Deadline, want)
	}
	if _, err := restored.Record(cp.ID, "user-1", newCredential("new-2", "login.new.example")); err != nil {
		t.Fatalf("record after restore must succeed, got %v", err)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	c, cp := campaignFixture(t)
	if err := c.Restore([]byte("{this is not a snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt restore = %v, want ErrBadSnapshot", err)
	}
	stored, ok := c.Campaign(cp.ID)
	if !ok || stored.State != impl.CampaignActive {
		t.Fatalf("live state must survive a rejected restore, got %+v ok=%v", stored, ok)
	}
}

func TestConcurrentRecordHonoursCredentialCap(t *testing.T) {
	c, cp := campaignFixture(t)
	const workers = 32
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := 0
	limited := 0
	errs := 0
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			defer wg.Done()
			_, err := c.Record(cp.ID, "user-1", newCredential(fmt.Sprintf("new-%02d", i), "login.new.example"))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				accepted++
			case errors.Is(err, impl.ErrCredentialLimit):
				limited++
			default:
				errs++
			}
		}(i)
	}
	wg.Wait()
	if accepted != 4 || limited != workers-4 || errs != 0 {
		t.Fatalf("concurrent record accepted=%d limited=%d other=%d, want 4/%d/0", accepted, limited, errs, workers-4)
	}
	prog, err := c.Status(cp.ID, "user-1")
	if err != nil {
		t.Fatalf("status must succeed, got %v", err)
	}
	if len(prog.Migrated) != 4 {
		t.Fatalf("stored credentials = %d, want the cap of 4", len(prog.Migrated))
	}
}
