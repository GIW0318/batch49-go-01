package task

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	impl "gsb/batch49-go-safety-a-17"
)

var campaignBase = time.Date(2026, 4, 6, 10, 0, 0, 0, time.UTC)

func campaignOptions() impl.Options {
	return impl.Options{GracePeriod: 72 * time.Hour, MaxAudience: 50, MaxBatch: 10}
}

func mustPlan(t *testing.T, c *impl.ConsentRenewalCampaign, tenant, from, to string) impl.Campaign {
	t.Helper()
	camp, err := c.PlanAt(tenant, from, to, campaignBase)
	if err != nil {
		t.Fatalf("plan %s -> %s: %v", from, to, err)
	}
	return camp
}

func TestPlanCreatesActiveCampaign(t *testing.T) {
	c := impl.NewConsentRenewalCampaign(campaignOptions())
	camp, err := c.PlanAt("tenant-1", "policy-2025.1", "policy-2026.1", campaignBase)
	if err != nil {
		t.Fatalf("plan must succeed, got %v", err)
	}
	if camp.ID == "" {
		t.Fatalf("campaign id must not be empty")
	}
	if camp.TenantID != "tenant-1" || camp.FromVersion != "policy-2025.1" || camp.ToVersion != "policy-2026.1" {
		t.Fatalf("campaign identity mismatch: %+v", camp)
	}
	if camp.Status != impl.CampaignActive {
		t.Fatalf("campaign status = %q, want %q", camp.Status, impl.CampaignActive)
	}
	if !camp.GraceUntil.Equal(campaignBase.Add(72 * time.Hour)) {
		t.Fatalf("grace deadline = %v, want %v", camp.GraceUntil, campaignBase.Add(72*time.Hour))
	}
	if camp.Seq <= 0 {
		t.Fatalf("campaign sequence = %d, want a positive number", camp.Seq)
	}
	if camp.Enrolled != 0 || camp.Accepted != 0 || camp.Exempted != 0 {
		t.Fatalf("fresh campaign counters must be zero, got %+v", camp)
	}
	stored, ok := c.Lookup(camp.ID)
	if !ok {
		t.Fatalf("planned campaign must be readable")
	}
	if stored.ToVersion != camp.ToVersion || !stored.GraceUntil.Equal(camp.GraceUntil) {
		t.Fatalf("stored campaign = %+v, want %+v", stored, camp)
	}
}

func TestPlanRejectsBadVersions(t *testing.T) {
	c := impl.NewConsentRenewalCampaign(campaignOptions())
	if _, err := c.PlanAt("   ", "policy-2025.1", "policy-2026.1", campaignBase); !errors.Is(err, impl.ErrBlankArgument) {
		t.Fatalf("blank tenant must fail with ErrBlankArgument, got %v", err)
	}
	if _, err := c.PlanAt("tenant-1", " ", "policy-2026.1", campaignBase); !errors.Is(err, impl.ErrBlankArgument) {
		t.Fatalf("blank source version must fail with ErrBlankArgument, got %v", err)
	}
	if _, err := c.PlanAt("tenant-1", "policy-2026.1", "", campaignBase); !errors.Is(err, impl.ErrBlankArgument) {
		t.Fatalf("blank target version must fail with ErrBlankArgument, got %v", err)
	}
	if _, err := c.PlanAt("tenant-1", "policy-2026.1", "policy-2026.1", campaignBase); !errors.Is(err, impl.ErrBadVersion) {
		t.Fatalf("identical versions must fail with ErrBadVersion, got %v", err)
	}
}

func TestEnrollDeduplicatesBatch(t *testing.T) {
	c := impl.NewConsentRenewalCampaign(campaignOptions())
	camp := mustPlan(t, c, "tenant-1", "policy-2025.1", "policy-2026.1")
	added, err := c.Enroll(camp.ID, []string{"user-1", "user-2", "user-1"})
	if err != nil {
		t.Fatalf("enrol: %v", err)
	}
	if added != 2 {
		t.Fatalf("first batch added %d users, want 2 distinct", added)
	}
	added, err = c.Enroll(camp.ID, []string{"user-2", "user-3"})
	if err != nil {
		t.Fatalf("second enrol: %v", err)
	}
	if added != 1 {
		t.Fatalf("second batch added %d users, want only user-3", added)
	}
	stats, err := c.Progress(camp.ID)
	if err != nil {
		t.Fatalf("progress: %v", err)
	}
	if stats.Enrolled != 3 {
		t.Fatalf("enrolled = %d, want 3", stats.Enrolled)
	}
	added, err = c.Enroll(camp.ID, []string{"user-1", "user-2", "user-3"})
	if err != nil {
		t.Fatalf("third enrol: %v", err)
	}
	if added != 0 {
		t.Fatalf("re-enrolling known users added %d, want 0", added)
	}
}

func TestEnrollRejectsEmptyAndOversizedBatch(t *testing.T) {
	c := impl.NewConsentRenewalCampaign(impl.Options{GracePeriod: time.Hour, MaxAudience: 50, MaxBatch: 3})
	camp := mustPlan(t, c, "tenant-1", "policy-2025.1", "policy-2026.1")
	if _, err := c.Enroll(camp.ID, nil); !errors.Is(err, impl.ErrEmptyBatch) {
		t.Fatalf("empty batch must fail with ErrEmptyBatch, got %v", err)
	}
	if _, err := c.Enroll(camp.ID, []string{"user-1", "user-2", "user-3", "user-4"}); !errors.Is(err, impl.ErrBatchLimit) {
		t.Fatalf("oversized batch must fail with ErrBatchLimit, got %v", err)
	}
	stats, err := c.Progress(camp.ID)
	if err != nil {
		t.Fatalf("progress: %v", err)
	}
	if stats.Enrolled != 0 {
		t.Fatalf("rejected batch must not enrol anyone, got %d", stats.Enrolled)
	}
	if _, err := c.Enroll(camp.ID, []string{"user-1", "user-2", "user-3"}); err != nil {
		t.Fatalf("a batch exactly at the cap must succeed, got %v", err)
	}
	if stats, err = c.Progress(camp.ID); err != nil || stats.Enrolled != 3 {
		t.Fatalf("enrolled = %d (err=%v), want 3", stats.Enrolled, err)
	}
}

func TestEnrollEnforcesAudienceLimit(t *testing.T) {
	c := impl.NewConsentRenewalCampaign(impl.Options{GracePeriod: time.Hour, MaxAudience: 3, MaxBatch: 10})
	camp := mustPlan(t, c, "tenant-1", "policy-2025.1", "policy-2026.1")
	if _, err := c.Enroll(camp.ID, []string{"user-1", "user-2"}); err != nil {
		t.Fatalf("first batch: %v", err)
	}
	if _, err := c.Enroll(camp.ID, []string{"user-3", "user-4"}); !errors.Is(err, impl.ErrAudienceLimit) {
		t.Fatalf("batch past the audience cap must fail with ErrAudienceLimit, got %v", err)
	}
	stats, err := c.Progress(camp.ID)
	if err != nil {
		t.Fatalf("progress: %v", err)
	}
	if stats.Enrolled != 2 {
		t.Fatalf("enrolled = %d, want 2 and no partial write", stats.Enrolled)
	}
	if _, err := c.Enroll(camp.ID, []string{"user-3"}); err != nil {
		t.Fatalf("enrolment up to the cap must succeed, got %v", err)
	}
	if _, err := c.Enroll(camp.ID, []string{"user-4"}); !errors.Is(err, impl.ErrAudienceLimit) {
		t.Fatalf("enrolment past the cap must fail with ErrAudienceLimit, got %v", err)
	}
}

func TestConcurrentEnrollRespectsAudienceLimit(t *testing.T) {
	c := impl.NewConsentRenewalCampaign(impl.Options{GracePeriod: time.Hour, MaxAudience: 10, MaxBatch: 1})
	camp := mustPlan(t, c, "tenant-1", "policy-2025.1", "policy-2026.1")
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, limited := 0, 0
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := c.Enroll(camp.ID, []string{fmt.Sprintf("user-%02d", i)})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, impl.ErrAudienceLimit):
				limited++
			}
		}(i)
	}
	wg.Wait()
	if ok != 10 {
		t.Fatalf("concurrent enrolment accepted %d users, want exactly the cap 10", ok)
	}
	if limited != 30 {
		t.Fatalf("concurrent enrolment reported %d audience limit errors, want 30", limited)
	}
	stats, err := c.Progress(camp.ID)
	if err != nil {
		t.Fatalf("progress: %v", err)
	}
	if stats.Enrolled != 10 {
		t.Fatalf("enrolled after the race = %d, want 10", stats.Enrolled)
	}
}

func TestAcceptRecordsReceipt(t *testing.T) {
	c := impl.NewConsentRenewalCampaign(campaignOptions())
	camp := mustPlan(t, c, "tenant-1", "policy-2025.1", "policy-2026.1")
	if _, err := c.Enroll(camp.ID, []string{"user-1", "user-2"}); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	receipt, err := c.AcceptAt(camp.ID, "user-1", "policy-2026.1", campaignBase.Add(time.Hour))
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if receipt.CampaignID != camp.ID || receipt.UserID != "user-1" || receipt.Version != "policy-2026.1" {
		t.Fatalf("receipt identity mismatch: %+v", receipt)
	}
	if !receipt.AcceptedAt.Equal(campaignBase.Add(time.Hour)) {
		t.Fatalf("receipt time = %v, want %v", receipt.AcceptedAt, campaignBase.Add(time.Hour))
	}
	if receipt.Seq <= 0 {
		t.Fatalf("receipt sequence = %d, want a positive number", receipt.Seq)
	}
	stats, err := c.Progress(camp.ID)
	if err != nil {
		t.Fatalf("progress: %v", err)
	}
	if stats.Accepted != 1 || stats.Enrolled != 2 {
		t.Fatalf("stats = %+v, want 1 accepted of 2 enrolled", stats)
	}
	if stats.CompletionBPS != 5000 {
		t.Fatalf("completion = %d bps, want 5000", stats.CompletionBPS)
	}
}

func TestAcceptRejectsVersionMismatch(t *testing.T) {
	c := impl.NewConsentRenewalCampaign(campaignOptions())
	camp := mustPlan(t, c, "tenant-1", "policy-2025.1", "policy-2026.1")
	if _, err := c.Enroll(camp.ID, []string{"user-1"}); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	if _, err := c.AcceptAt(camp.ID, "user-1", "policy-2025.1", campaignBase); !errors.Is(err, impl.ErrVersionMismatch) {
		t.Fatalf("older version must fail with ErrVersionMismatch, got %v", err)
	}
	if _, err := c.AcceptAt(camp.ID, "user-1", "policy-2027.1", campaignBase); !errors.Is(err, impl.ErrVersionMismatch) {
		t.Fatalf("unknown newer version must fail with ErrVersionMismatch, got %v", err)
	}
	stats, err := c.Progress(camp.ID)
	if err != nil {
		t.Fatalf("progress: %v", err)
	}
	if stats.Accepted != 0 {
		t.Fatalf("rejected acceptance must not count, got %d accepted", stats.Accepted)
	}
	if stats.CompletionBPS != 0 {
		t.Fatalf("completion = %d bps, want 0", stats.CompletionBPS)
	}
}

func TestAcceptRejectsNonEnrolled(t *testing.T) {
	c := impl.NewConsentRenewalCampaign(campaignOptions())
	camp := mustPlan(t, c, "tenant-1", "policy-2025.1", "policy-2026.1")
	if _, err := c.Enroll(camp.ID, []string{"user-1"}); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	if _, err := c.AcceptAt(camp.ID, "user-9", "policy-2026.1", campaignBase); !errors.Is(err, impl.ErrNotEnrolled) {
		t.Fatalf("non-enrolled user must fail with ErrNotEnrolled, got %v", err)
	}
	if _, err := c.AcceptAt("campaign-404", "user-1", "policy-2026.1", campaignBase); !errors.Is(err, impl.ErrCampaignNotFound) {
		t.Fatalf("unknown campaign must fail with ErrCampaignNotFound, got %v", err)
	}
	stats, err := c.Progress(camp.ID)
	if err != nil {
		t.Fatalf("progress: %v", err)
	}
	if stats.Accepted != 0 {
		t.Fatalf("rejected acceptance must not count, got %d accepted", stats.Accepted)
	}
}

func TestPauseBlocksAcceptThenResume(t *testing.T) {
	c := impl.NewConsentRenewalCampaign(campaignOptions())
	camp := mustPlan(t, c, "tenant-1", "policy-2025.1", "policy-2026.1")
	if _, err := c.Enroll(camp.ID, []string{"user-1", "user-2"}); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	if err := c.Pause(camp.ID); err != nil {
		t.Fatalf("pause: %v", err)
	}
	paused, ok := c.Lookup(camp.ID)
	if !ok || paused.Status != impl.CampaignPaused {
		t.Fatalf("campaign status after pause = %q ok=%v, want %q", paused.Status, ok, impl.CampaignPaused)
	}
	if _, err := c.AcceptAt(camp.ID, "user-1", "policy-2026.1", campaignBase); !errors.Is(err, impl.ErrPaused) {
		t.Fatalf("accept on a paused campaign must fail with ErrPaused, got %v", err)
	}
	if _, err := c.Enroll(camp.ID, []string{"user-3"}); !errors.Is(err, impl.ErrPaused) {
		t.Fatalf("enrol on a paused campaign must fail with ErrPaused, got %v", err)
	}
	if err := c.Pause(camp.ID); err != nil {
		t.Fatalf("pausing twice must be idempotent, got %v", err)
	}
	if err := c.Resume(camp.ID); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if err := c.Resume(camp.ID); err != nil {
		t.Fatalf("resuming twice must be idempotent, got %v", err)
	}
	if _, err := c.AcceptAt(camp.ID, "user-1", "policy-2026.1", campaignBase.Add(time.Minute)); err != nil {
		t.Fatalf("accept after resume must succeed, got %v", err)
	}
	stats, err := c.Progress(camp.ID)
	if err != nil {
		t.Fatalf("progress: %v", err)
	}
	if stats.Accepted != 1 {
		t.Fatalf("accepted after resume = %d, want 1", stats.Accepted)
	}
}

func TestProgressCompletionBasisPoints(t *testing.T) {
	c := impl.NewConsentRenewalCampaign(campaignOptions())
	camp := mustPlan(t, c, "tenant-1", "policy-2025.1", "policy-2026.1")
	if _, err := c.Enroll(camp.ID, []string{"user-1", "user-2", "user-3"}); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	for _, user := range []string{"user-1", "user-2"} {
		if _, err := c.AcceptAt(camp.ID, user, "policy-2026.1", campaignBase); err != nil {
			t.Fatalf("accept %s: %v", user, err)
		}
	}
	stats, err := c.Progress(camp.ID)
	if err != nil {
		t.Fatalf("progress: %v", err)
	}
	if stats.Enrolled != 3 || stats.Accepted != 2 {
		t.Fatalf("stats = %+v, want 2 accepted of 3 enrolled", stats)
	}
	if stats.CompletionBPS != 6666 {
		t.Fatalf("completion = %d bps, want 6666 (floor of 2/3)", stats.CompletionBPS)
	}
	if stats.Exempted != 0 {
		t.Fatalf("exempted = %d, want 0", stats.Exempted)
	}
}

func TestProgressEmptyCampaignIsZero(t *testing.T) {
	c := impl.NewConsentRenewalCampaign(campaignOptions())
	camp := mustPlan(t, c, "tenant-1", "policy-2025.1", "policy-2026.1")
	stats, err := c.Progress(camp.ID)
	if err != nil {
		t.Fatalf("progress on an empty campaign must not fail, got %v", err)
	}
	if stats.Enrolled != 0 || stats.Accepted != 0 || stats.Exempted != 0 {
		t.Fatalf("empty campaign stats = %+v, want zero counters", stats)
	}
	if stats.CompletionBPS != 0 {
		t.Fatalf("empty campaign completion = %d bps, want 0", stats.CompletionBPS)
	}
	other := mustPlan(t, c, "tenant-1", "policy-2026.1", "policy-2027.1")
	if _, err := c.Progress(other.ID); err != nil {
		t.Fatalf("the second campaign must be readable, got %v", err)
	}
}

func TestExemptRequiresEnrollmentAndReason(t *testing.T) {
	c := impl.NewConsentRenewalCampaign(campaignOptions())
	camp := mustPlan(t, c, "tenant-1", "policy-2025.1", "policy-2026.1")
	if _, err := c.Enroll(camp.ID, []string{"user-1", "user-2"}); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	if err := c.Exempt(camp.ID, "user-9", "service account"); !errors.Is(err, impl.ErrNotEnrolled) {
		t.Fatalf("exempting a non-enrolled user must fail with ErrNotEnrolled, got %v", err)
	}
	if err := c.Exempt(camp.ID, "user-1", "   "); !errors.Is(err, impl.ErrBadReason) {
		t.Fatalf("blank reason must fail with ErrBadReason, got %v", err)
	}
	if err := c.Exempt(camp.ID, "user-1", "legal hold"); err != nil {
		t.Fatalf("valid exemption must succeed, got %v", err)
	}
	if err := c.Exempt(camp.ID, "user-1", "legal hold"); err != nil {
		t.Fatalf("repeating the same exemption must be idempotent, got %v", err)
	}
	stats, err := c.Progress(camp.ID)
	if err != nil {
		t.Fatalf("progress: %v", err)
	}
	if stats.Exempted != 1 {
		t.Fatalf("exempted = %d, want 1", stats.Exempted)
	}
	if stats.Enrolled != 2 {
		t.Fatalf("enrolled = %d, want 2 including the exempt user", stats.Enrolled)
	}
}

func TestSnapshotRestoreKeepsProgressAndDeadline(t *testing.T) {
	opts := campaignOptions()
	c := impl.NewConsentRenewalCampaign(opts)
	camp := mustPlan(t, c, "tenant-1", "policy-2025.1", "policy-2026.1")
	if _, err := c.Enroll(camp.ID, []string{"user-1", "user-2"}); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	first, err := c.AcceptAt(camp.ID, "user-1", "policy-2026.1", campaignBase.Add(time.Hour))
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if err := c.Pause(camp.ID); err != nil {
		t.Fatalf("pause: %v", err)
	}
	data, err := c.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload must not be empty")
	}
	restored := impl.NewConsentRenewalCampaign(opts)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	stored, ok := restored.Lookup(camp.ID)
	if !ok {
		t.Fatalf("restored store lost the campaign")
	}
	if stored.Status != impl.CampaignPaused {
		t.Fatalf("restored status = %q, want %q", stored.Status, impl.CampaignPaused)
	}
	if !stored.GraceUntil.Equal(camp.GraceUntil) {
		t.Fatalf("restored grace deadline = %v, want %v", stored.GraceUntil, camp.GraceUntil)
	}
	stats, err := restored.Progress(camp.ID)
	if err != nil {
		t.Fatalf("progress after restore: %v", err)
	}
	if stats.Enrolled != 2 || stats.Accepted != 1 {
		t.Fatalf("restored stats = %+v, want 1 accepted of 2 enrolled", stats)
	}
	if err := restored.Resume(camp.ID); err != nil {
		t.Fatalf("resume after restore: %v", err)
	}
	second, err := restored.AcceptAt(camp.ID, "user-2", "policy-2026.1", campaignBase.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("accepting the second enrolled user after restore: %v", err)
	}
	if second.Seq <= first.Seq {
		t.Fatalf("receipt sequence after restore = %d, must continue past %d", second.Seq, first.Seq)
	}
	if stats, err = restored.Progress(camp.ID); err != nil || stats.Accepted != 2 {
		t.Fatalf("accepted after restore = %d (err=%v), want 2", stats.Accepted, err)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	c := impl.NewConsentRenewalCampaign(campaignOptions())
	camp := mustPlan(t, c, "tenant-1", "policy-2025.1", "policy-2026.1")
	if _, err := c.Enroll(camp.ID, []string{"user-1"}); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	if _, err := c.AcceptAt(camp.ID, "user-1", "policy-2026.1", campaignBase); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if err := c.Restore([]byte("{not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if _, ok := c.Lookup(camp.ID); !ok {
		t.Fatalf("rejected restore must not touch live campaigns")
	}
	stats, err := c.Progress(camp.ID)
	if err != nil {
		t.Fatalf("progress after a rejected restore: %v", err)
	}
	if stats.Enrolled != 1 || stats.Accepted != 1 {
		t.Fatalf("live stats after a rejected restore = %+v, want 1 of 1", stats)
	}
	if _, err := c.AcceptAt(camp.ID, "user-1", "policy-2026.1", campaignBase.Add(time.Minute)); err != nil {
		t.Fatalf("live campaign must keep working after a rejected restore, got %v", err)
	}
}
