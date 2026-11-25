package task

import (
	"errors"
	"testing"

	impl "gsb/batch49-go-game-b-09"
)

func newStager() *impl.TextureUploadStager {
	return impl.NewTextureUploadStager(impl.StageConfig{StagingBudget: 4096, RetireWindowMs: 100})
}

func submitPage(t *testing.T, s *impl.TextureUploadStager, texID string, level, bytes int) impl.PageTicket {
	t.Helper()
	ticket, err := s.SubmitPage(impl.PageRequest{TexID: texID, Level: level, Bytes: bytes})
	if err != nil {
		t.Fatalf("submit %s level %d: %v", texID, level, err)
	}
	return ticket
}

func residentLevels(t *testing.T, s *impl.TextureUploadStager, texID string) []int {
	t.Helper()
	levels, err := s.Resident(texID)
	if err != nil {
		t.Fatalf("resident %s: %v", texID, err)
	}
	return levels
}

func TestSubmitReturnsIncreasingTickets(t *testing.T) {
	s := newStager()
	first := submitPage(t, s, "rock", 0, 60)
	second := submitPage(t, s, "rock", 1, 40)
	if first.ID == 0 || second.ID <= first.ID {
		t.Fatalf("ticket ids = %d and %d, want increasing positive ids", first.ID, second.ID)
	}
	if first.TexID != "rock" || first.Level != 0 || second.Level != 1 {
		t.Fatalf("ticket identity mismatch: %+v %+v", first, second)
	}
	if got := s.State("rock", 0); got != impl.PageQueued {
		t.Fatalf("state after submit = %q, want %q", got, impl.PageQueued)
	}
	if got := s.StagedBytes(); got != 100 {
		t.Fatalf("staged bytes = %d, want 100", got)
	}
}

func TestSubmitRejectsInvalidRequest(t *testing.T) {
	s := newStager()
	for _, texID := range []string{"", "   "} {
		if _, err := s.SubmitPage(impl.PageRequest{TexID: texID, Level: 0, Bytes: 10}); !errors.Is(err, impl.ErrBlankTexture) {
			t.Fatalf("blank texture %q must fail with ErrBlankTexture, got %v", texID, err)
		}
	}
	if _, err := s.SubmitPage(impl.PageRequest{TexID: "rock", Level: -1, Bytes: 10}); !errors.Is(err, impl.ErrInvalidLevel) {
		t.Fatalf("negative level must fail with ErrInvalidLevel, got %v", err)
	}
	if _, err := s.SubmitPage(impl.PageRequest{TexID: "rock", Level: 0, Bytes: 0}); !errors.Is(err, impl.ErrInvalidBytes) {
		t.Fatalf("zero byte page must fail with ErrInvalidBytes, got %v", err)
	}
	if got := s.StagedBytes(); got != 0 {
		t.Fatalf("rejected requests must not reserve staging, got %d bytes", got)
	}
}

func TestSubmitRejectsDuplicatePage(t *testing.T) {
	s := newStager()
	submitPage(t, s, "rock", 0, 60)
	if _, err := s.SubmitPage(impl.PageRequest{TexID: "rock", Level: 0, Bytes: 60}); !errors.Is(err, impl.ErrDuplicatePage) {
		t.Fatalf("duplicate page must fail with ErrDuplicatePage, got %v", err)
	}
	if got := s.StagedBytes(); got != 60 {
		t.Fatalf("duplicate submit must not double the staging, got %d bytes", got)
	}
}

func TestUploadStepRespectsByteBudget(t *testing.T) {
	s := newStager()
	submitPage(t, s, "rock", 0, 60)
	submitPage(t, s, "rock", 1, 40)
	report, err := s.UploadStep(100)
	if err != nil {
		t.Fatalf("upload step: %v", err)
	}
	if report.Bytes != 100 || report.Uploaded != 2 || report.Blocked != 0 || report.Partial {
		t.Fatalf("report = %+v, want 100 bytes and two uploaded pages", report)
	}
	levels := residentLevels(t, s, "rock")
	if len(levels) != 2 || levels[0] != 0 || levels[1] != 1 {
		t.Fatalf("resident levels = %v, want [0 1]", levels)
	}
	empty, err := s.UploadStep(100)
	if err != nil {
		t.Fatalf("idle upload step: %v", err)
	}
	if empty.Bytes != 0 || empty.Uploaded != 0 {
		t.Fatalf("idle report = %+v, want no work", empty)
	}
}

func TestUploadHonoursParentFirst(t *testing.T) {
	s := newStager()
	submitPage(t, s, "rock", 2, 30)
	submitPage(t, s, "rock", 1, 30)
	submitPage(t, s, "rock", 0, 30)
	report, err := s.UploadStep(1000)
	if err != nil {
		t.Fatalf("upload step: %v", err)
	}
	if report.Uploaded != 3 {
		t.Fatalf("uploaded pages = %d, want 3 once the parents are in place", report.Uploaded)
	}
	levels := residentLevels(t, s, "rock")
	if len(levels) != 3 || levels[0] != 0 || levels[1] != 1 || levels[2] != 2 {
		t.Fatalf("resident levels = %v, want [0 1 2]", levels)
	}
}

func TestChildBlockedWithoutParent(t *testing.T) {
	s := newStager()
	submitPage(t, s, "rock", 1, 30)
	report, err := s.UploadStep(1000)
	if err != nil {
		t.Fatalf("upload step: %v", err)
	}
	if report.Uploaded != 0 || report.Blocked != 1 {
		t.Fatalf("report = %+v, want one blocked page", report)
	}
	if got := s.State("rock", 1); got != impl.PageQueued {
		t.Fatalf("child state = %q, want %q", got, impl.PageQueued)
	}
	if got := s.StagedBytes(); got != 30 {
		t.Fatalf("blocked page must keep its staging bytes, got %d", got)
	}
}

func TestPartialUploadKeepsPageUploading(t *testing.T) {
	s := newStager()
	ticket := submitPage(t, s, "rock", 0, 60)
	report, err := s.UploadStep(30)
	if err != nil {
		t.Fatalf("partial upload step: %v", err)
	}
	if report.Bytes != 30 || report.Uploaded != 0 || !report.Partial {
		t.Fatalf("partial report = %+v, want 30 bytes and a partial page", report)
	}
	if got := s.State("rock", 0); got != impl.PageUploading {
		t.Fatalf("state after a partial step = %q, want %q", got, impl.PageUploading)
	}
	if err := s.Cancel(ticket); !errors.Is(err, impl.ErrUploadInProgress) {
		t.Fatalf("cancel of an uploading page must fail with ErrUploadInProgress, got %v", err)
	}
	done, err := s.UploadStep(100)
	if err != nil {
		t.Fatalf("finishing upload step: %v", err)
	}
	if done.Uploaded != 1 {
		t.Fatalf("uploaded pages = %d, want 1 after finishing", done.Uploaded)
	}
	if got := s.State("rock", 0); got != impl.PageResident {
		t.Fatalf("state after a full upload = %q, want %q", got, impl.PageResident)
	}
}

func TestCancelQueuedReleasesStaging(t *testing.T) {
	s := newStager()
	ticket := submitPage(t, s, "rock", 0, 60)
	if err := s.Cancel(ticket); err != nil {
		t.Fatalf("cancel a queued page: %v", err)
	}
	if got := s.State("rock", 0); got != impl.PageCancelled {
		t.Fatalf("state after cancel = %q, want %q", got, impl.PageCancelled)
	}
	if got := s.StagedBytes(); got != 0 {
		t.Fatalf("staging after cancel = %d, want 0", got)
	}
}

func TestEvictMovesPageToRetirement(t *testing.T) {
	s := newStager()
	submitPage(t, s, "rock", 0, 60)
	if _, err := s.UploadStep(1000); err != nil {
		t.Fatalf("upload step: %v", err)
	}
	if err := s.Evict("rock", 0); err != nil {
		t.Fatalf("evict: %v", err)
	}
	if got := s.State("rock", 0); got != impl.PageRetired {
		t.Fatalf("state after evict = %q, want %q", got, impl.PageRetired)
	}
	if levels := residentLevels(t, s, "rock"); len(levels) != 0 {
		t.Fatalf("resident levels after evict = %v, want none", levels)
	}
}

func TestRetirementWindowExpiry(t *testing.T) {
	s := newStager()
	submitPage(t, s, "rock", 0, 60)
	if _, err := s.UploadStep(1000); err != nil {
		t.Fatalf("upload step: %v", err)
	}
	if err := s.Evict("rock", 0); err != nil {
		t.Fatalf("evict: %v", err)
	}
	s.Tick(50)
	if got := s.State("rock", 0); got != impl.PageRetired {
		t.Fatalf("state inside the retirement window = %q, want %q", got, impl.PageRetired)
	}
	s.Tick(60)
	if got := s.State("rock", 0); got != impl.PageFree {
		t.Fatalf("state after the retirement window = %q, want %q", got, impl.PageFree)
	}
	if levels := residentLevels(t, s, "rock"); len(levels) != 0 {
		t.Fatalf("resident levels after expiry = %v, want none", levels)
	}
}

func TestReRequestDuringRetirementReusesPage(t *testing.T) {
	s := newStager()
	submitPage(t, s, "rock", 0, 60)
	if _, err := s.UploadStep(1000); err != nil {
		t.Fatalf("upload step: %v", err)
	}
	if err := s.Evict("rock", 0); err != nil {
		t.Fatalf("evict: %v", err)
	}
	s.Tick(10)
	submitPage(t, s, "rock", 0, 60)
	if got := s.State("rock", 0); got != impl.PageResident {
		t.Fatalf("state after a re-request inside the window = %q, want %q", got, impl.PageResident)
	}
	if got := s.StagedBytes(); got != 0 {
		t.Fatalf("a reused page must not take staging bytes, got %d", got)
	}
	if levels := residentLevels(t, s, "rock"); len(levels) != 1 || levels[0] != 0 {
		t.Fatalf("resident levels after reuse = %v, want [0]", levels)
	}
}

func TestEvictUnknownPage(t *testing.T) {
	s := newStager()
	if err := s.Evict("rock", 3); !errors.Is(err, impl.ErrNotResident) {
		t.Fatalf("evict of an unknown texture must fail with ErrNotResident, got %v", err)
	}
	submitPage(t, s, "rock", 0, 60)
	if err := s.Evict("rock", 0); !errors.Is(err, impl.ErrNotResident) {
		t.Fatalf("evict of a queued page must fail with ErrNotResident, got %v", err)
	}
	if got := s.State("rock", 0); got != impl.PageQueued {
		t.Fatalf("a rejected evict must leave the page queued, got %q", got)
	}
}

func TestDestroyTextureFreesResidentPages(t *testing.T) {
	s := newStager()
	submitPage(t, s, "rock", 0, 30)
	submitPage(t, s, "rock", 1, 30)
	if _, err := s.UploadStep(1000); err != nil {
		t.Fatalf("upload step: %v", err)
	}
	if err := s.DestroyTexture("rock"); err != nil {
		t.Fatalf("destroy texture: %v", err)
	}
	for _, level := range []int{0, 1} {
		if got := s.State("rock", level); got != impl.PageFree {
			t.Fatalf("state of level %d after destroy = %q, want %q", level, got, impl.PageFree)
		}
	}
	if _, err := s.Resident("rock"); !errors.Is(err, impl.ErrNoTexture) {
		t.Fatalf("resident of a destroyed texture must fail with ErrNoTexture, got %v", err)
	}
}

func TestSnapshotRestoreKeepsQueuedPages(t *testing.T) {
	s := newStager()
	submitPage(t, s, "rock", 0, 60)
	submitPage(t, s, "sand", 0, 30)
	if report, err := s.UploadStep(60); err != nil || report.Uploaded != 1 {
		t.Fatalf("upload step = %+v err %v, want one uploaded page", report, err)
	}
	snap, err := s.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snap) == 0 {
		t.Fatalf("snapshot payload must not be empty")
	}
	fresh := newStager()
	if err := fresh.Restore(snap); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := fresh.State("rock", 0); got != impl.PageResident {
		t.Fatalf("restored state of rock = %q, want %q", got, impl.PageResident)
	}
	if got := fresh.State("sand", 0); got != impl.PageQueued {
		t.Fatalf("restored state of sand = %q, want %q", got, impl.PageQueued)
	}
	if levels := residentLevels(t, fresh, "rock"); len(levels) != 1 || levels[0] != 0 {
		t.Fatalf("restored resident levels = %v, want [0]", levels)
	}
	if got := fresh.StagedBytes(); got != 30 {
		t.Fatalf("restored staging bytes = %d, want 30", got)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	s := newStager()
	submitPage(t, s, "rock", 0, 60)
	if err := s.Restore([]byte("stager-state: broken")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt payload must fail with ErrBadSnapshot, got %v", err)
	}
	if got := s.State("rock", 0); got != impl.PageQueued {
		t.Fatalf("a rejected restore must keep the page table, state = %q", got)
	}
	if got := s.StagedBytes(); got != 60 {
		t.Fatalf("a rejected restore must keep the staging bytes, got %d", got)
	}
}
