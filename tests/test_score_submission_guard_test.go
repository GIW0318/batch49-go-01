package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch49-go-game-a-20"
)

func mustSubmit(t *testing.T, g *impl.ScoreGuard, board, player string, score, duration, at int64) impl.Verdict {
	t.Helper()
	v, err := g.Submit(board, player, score, duration, at)
	if err != nil {
		t.Fatalf("submit %s/%d: %v", player, score, err)
	}
	return v
}

func TestSubmitRejectsBadInput(t *testing.T) {
	g := impl.NewScoreGuard(impl.Options{})
	if _, err := g.Submit("   ", "alpha", 10, 60000, 0); !errors.Is(err, impl.ErrBlankKey) {
		t.Fatalf("blank board must fail with ErrBlankKey, got %v", err)
	}
	if _, err := g.Submit("season", "", 10, 60000, 0); !errors.Is(err, impl.ErrBlankKey) {
		t.Fatalf("blank player must fail with ErrBlankKey, got %v", err)
	}
	if _, err := g.Submit("season", "alpha", 10, 0, 0); !errors.Is(err, impl.ErrBadDuration) {
		t.Fatalf("zero duration must fail with ErrBadDuration, got %v", err)
	}
	if _, err := g.Submit("season", "alpha", -1, 60000, 0); !errors.Is(err, impl.ErrBadScore) {
		t.Fatalf("negative score must fail with ErrBadScore, got %v", err)
	}
	if got := len(g.Quarantine("season")); got != 0 {
		t.Fatalf("rejected submissions must not reach the quarantine, got %d", got)
	}
}

func TestSubmitAdmitsSmallGain(t *testing.T) {
	g := impl.NewScoreGuard(impl.Options{MaxSingleGain: 100})
	v := mustSubmit(t, g, "season", "alpha", 100, 60000, 1000)
	if v.Status != impl.StatusAdmitted || v.Reason != impl.ReasonOK {
		t.Fatalf("verdict = %+v, want admitted/ok", v)
	}
	if v.SubmissionID == "" {
		t.Fatalf("an admitted submission must get a non empty id")
	}
	if v.Score != 100 || v.Board != "season" || v.Player != "alpha" {
		t.Fatalf("verdict identity = %+v, want season/alpha/100", v)
	}
	score, ok := g.Effective("season", "alpha")
	if !ok || score != 100 {
		t.Fatalf("effective = %d ok=%v, want 100", score, ok)
	}
	stats := g.Stats("season")
	if stats.Submitted != 1 || stats.Admitted != 1 || stats.Quarantined != 0 {
		t.Fatalf("stats = %+v, want one submitted and admitted entry", stats)
	}
}

func TestSubmitQuarantinesSingleGain(t *testing.T) {
	g := impl.NewScoreGuard(impl.Options{MaxSingleGain: 100})
	mustSubmit(t, g, "season", "alpha", 100, 60000, 1000)
	v := mustSubmit(t, g, "season", "alpha", 250, 60000, 2000)
	if v.Status != impl.StatusQuarantined {
		t.Fatalf("verdict status = %q, want quarantined", v.Status)
	}
	if v.Reason != impl.ReasonSingleGain {
		t.Fatalf("verdict reason = %q, want %q", v.Reason, impl.ReasonSingleGain)
	}
	if got := len(g.Quarantine("season")); got != 1 {
		t.Fatalf("quarantine len = %d, want 1", got)
	}
	score, ok := g.Effective("season", "alpha")
	if !ok || score != 100 {
		t.Fatalf("effective after quarantine = %d ok=%v, want the previous 100", score, ok)
	}
	stats := g.Stats("season")
	if stats.Quarantined != 1 || stats.Admitted != 1 {
		t.Fatalf("stats = %+v, want one pending and one admitted", stats)
	}
}

func TestSubmitQuarantinesWindowGain(t *testing.T) {
	g := impl.NewScoreGuard(impl.Options{WindowMs: 60000, WindowGain: 50})
	mustSubmit(t, g, "season", "alpha", 100, 60000, 0)
	v := mustSubmit(t, g, "season", "alpha", 160, 60000, 1000)
	if v.Status != impl.StatusQuarantined || v.Reason != impl.ReasonWindowGain {
		t.Fatalf("verdict = %+v, want quarantined for the window gain", v)
	}
	score, ok := g.Effective("season", "alpha")
	if !ok || score != 100 {
		t.Fatalf("effective after the window quarantine = %d ok=%v, want 100", score, ok)
	}
}

func TestSubmitQuarantinesShortDuration(t *testing.T) {
	g := impl.NewScoreGuard(impl.Options{MaxScorePerMinute: 6000})
	v := mustSubmit(t, g, "season", "alpha", 150, 1000, 0)
	if v.Status != impl.StatusQuarantined || v.Reason != impl.ReasonDuration {
		t.Fatalf("verdict = %+v, want quarantined for the round length", v)
	}
	if got := len(g.Quarantine("season")); got != 1 {
		t.Fatalf("quarantine len = %d, want 1", got)
	}
}

func TestQuarantineRespectsLimit(t *testing.T) {
	g := impl.NewScoreGuard(impl.Options{MaxSingleGain: 1, QuarantineLimit: 2})
	first := mustSubmit(t, g, "season", "alpha", 100, 60000, 1000)
	second := mustSubmit(t, g, "season", "alpha", 200, 60000, 2000)
	third := mustSubmit(t, g, "season", "alpha", 300, 60000, 3000)
	if got := len(g.Quarantine("season")); got != 2 {
		t.Fatalf("quarantine len = %d, want the cap 2", got)
	}
	record, ok := g.Submission("season", first.SubmissionID)
	if !ok {
		t.Fatalf("the oldest ledger record must stay readable")
	}
	if record.Status != impl.StatusDropped {
		t.Fatalf("oldest record status = %q, want %q after the cap overflow", record.Status, impl.StatusDropped)
	}
	for _, want := range []string{second.SubmissionID, third.SubmissionID} {
		if _, ok := g.Submission("season", want); !ok {
			t.Fatalf("pending record %s must stay readable", want)
		}
	}
	stats := g.Stats("season")
	if stats.Dropped != 1 || stats.Quarantined != 2 {
		t.Fatalf("stats = %+v, want one dropped and two pending", stats)
	}
}

func TestReleaseAdmitsQuarantined(t *testing.T) {
	g := impl.NewScoreGuard(impl.Options{MaxSingleGain: 1})
	v := mustSubmit(t, g, "season", "alpha", 300, 60000, 1000)
	if err := g.Release("season", v.SubmissionID); err != nil {
		t.Fatalf("release: %v", err)
	}
	if got := len(g.Quarantine("season")); got != 0 {
		t.Fatalf("quarantine len after release = %d, want 0", got)
	}
	record, ok := g.Submission("season", v.SubmissionID)
	if !ok || record.Status != impl.StatusReleased {
		t.Fatalf("released record = %+v ok=%v, want status %q", record, ok, impl.StatusReleased)
	}
	if _, ok := g.Effective("season", "alpha"); !ok {
		t.Fatalf("a released submission must show up as effective")
	}
	if stats := g.Stats("season"); stats.Released != 1 {
		t.Fatalf("stats released = %d, want 1", stats.Released)
	}
	if err := g.Release("season", v.SubmissionID); !errors.Is(err, impl.ErrBadState) {
		t.Fatalf("releasing the same submission twice must fail with ErrBadState, got %v", err)
	}
}

func TestReleaseUnknownSubmissionFails(t *testing.T) {
	g := impl.NewScoreGuard(impl.Options{})
	if err := g.Release("season", "sub-404"); !errors.Is(err, impl.ErrUnknownSubmission) {
		t.Fatalf("releasing an unknown id must fail with ErrUnknownSubmission, got %v", err)
	}
	if err := g.Reject("season", "sub-404", "manual"); !errors.Is(err, impl.ErrUnknownSubmission) {
		t.Fatalf("rejecting an unknown id must fail with ErrUnknownSubmission, got %v", err)
	}
}

func TestRejectRemovesFromQuarantine(t *testing.T) {
	g := impl.NewScoreGuard(impl.Options{MaxSingleGain: 1})
	first := mustSubmit(t, g, "season", "alpha", 300, 60000, 1000)
	mustSubmit(t, g, "season", "bravo", 400, 60000, 2000)
	if err := g.Reject("season", first.SubmissionID, "   "); !errors.Is(err, impl.ErrBadReason) {
		t.Fatalf("blank reason must fail with ErrBadReason, got %v", err)
	}
	if got := len(g.Quarantine("season")); got != 2 {
		t.Fatalf("a rejected reason must not remove the entry, quarantine=%d", got)
	}
	if err := g.Reject("season", first.SubmissionID, "manual review"); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if got := len(g.Quarantine("season")); got != 1 {
		t.Fatalf("quarantine len after reject = %d, want 1", got)
	}
	record, ok := g.Submission("season", first.SubmissionID)
	if !ok || record.Status != impl.StatusRejected {
		t.Fatalf("rejected record = %+v ok=%v, want status %q", record, ok, impl.StatusRejected)
	}
	if _, ok := g.Effective("season", "alpha"); ok {
		t.Fatalf("a rejected submission must not be effective")
	}
	if err := g.Reject("season", first.SubmissionID, "again"); !errors.Is(err, impl.ErrBadState) {
		t.Fatalf("rejecting a closed submission must fail with ErrBadState, got %v", err)
	}
}

func TestDuplicateSubmitIsIdempotent(t *testing.T) {
	g := impl.NewScoreGuard(impl.Options{MaxSingleGain: 100})
	first := mustSubmit(t, g, "season", "alpha", 100, 60000, 1000)
	second := mustSubmit(t, g, "season", "alpha", 100, 60000, 1000)
	if second.Status != impl.StatusDuplicate {
		t.Fatalf("repeated submission status = %q, want %q", second.Status, impl.StatusDuplicate)
	}
	if second.SubmissionID != first.SubmissionID {
		t.Fatalf("repeated submission id = %q, want the original %q", second.SubmissionID, first.SubmissionID)
	}
	stats := g.Stats("season")
	if stats.Submitted != 1 || stats.Admitted != 1 || stats.Duplicates != 1 {
		t.Fatalf("stats = %+v, want one submitted, one admitted and one duplicate", stats)
	}
	score, ok := g.Effective("season", "alpha")
	if !ok || score != 100 {
		t.Fatalf("effective after the duplicate = %d ok=%v, want 100", score, ok)
	}
}

func TestEffectiveKeepsBestAdmitted(t *testing.T) {
	g := impl.NewScoreGuard(impl.Options{MaxSingleGain: 100})
	mustSubmit(t, g, "season", "alpha", 100, 60000, 1000)
	mustSubmit(t, g, "season", "alpha", 150, 60000, 2000)
	mustSubmit(t, g, "season", "alpha", 120, 60000, 3000)
	score, ok := g.Effective("season", "alpha")
	if !ok || score != 150 {
		t.Fatalf("effective = %d ok=%v, want the best admitted 150", score, ok)
	}
	list := g.EffectiveAll("season")
	if len(list) != 1 || list[0].Player != "alpha" || list[0].Score != 150 {
		t.Fatalf("effective list = %+v, want one alpha entry at 150", list)
	}
	if stats := g.Stats("season"); stats.Admitted != 3 {
		t.Fatalf("stats admitted = %d, want 3", stats.Admitted)
	}
}

func TestWatermarkTracksBest(t *testing.T) {
	g := impl.NewScoreGuard(impl.Options{})
	if _, ok := g.Watermark("season"); ok {
		t.Fatalf("an empty board must not report a watermark")
	}
	mustSubmit(t, g, "season", "alpha", 100, 60000, 1000)
	if got, ok := g.Watermark("season"); !ok || got != 100 {
		t.Fatalf("watermark = %d ok=%v, want 100", got, ok)
	}
	mustSubmit(t, g, "season", "alpha", 250, 60000, 2000)
	if got, ok := g.Watermark("season"); !ok || got != 250 {
		t.Fatalf("watermark after the higher score = %d ok=%v, want 250", got, ok)
	}
}

func TestConcurrentSubmitsSerialize(t *testing.T) {
	g := impl.NewScoreGuard(impl.Options{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	failures := make([]error, 0, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := g.Submit("season", "alpha", int64(100*(i+1)), 60000, 5000)
			if err != nil {
				mu.Lock()
				failures = append(failures, err)
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if len(failures) != 0 {
		t.Fatalf("concurrent submits failed: %v", failures)
	}
	score, ok := g.Effective("season", "alpha")
	if !ok || score != 800 {
		t.Fatalf("effective after concurrent submits = %d ok=%v, want 800", score, ok)
	}
	if stats := g.Stats("season"); stats.Admitted != 8 {
		t.Fatalf("stats admitted = %d, want 8", stats.Admitted)
	}
	if got, ok := g.Watermark("season"); !ok || got != 800 {
		t.Fatalf("watermark = %d ok=%v, want 800", got, ok)
	}
}

func TestSnapshotRestoreKeepsLedger(t *testing.T) {
	g := impl.NewScoreGuard(impl.Options{MaxSingleGain: 100})
	mustSubmit(t, g, "season", "alpha", 100, 60000, 1000)
	pending := mustSubmit(t, g, "season", "alpha", 300, 60000, 2000)
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewScoreGuard(impl.Options{MaxSingleGain: 100})
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	score, ok := restored.Effective("season", "alpha")
	if !ok || score != 100 {
		t.Fatalf("restored effective = %d ok=%v, want 100", score, ok)
	}
	queue := restored.Quarantine("season")
	if len(queue) != 1 || queue[0].Score != 300 || queue[0].Status != impl.StatusQuarantined {
		t.Fatalf("restored quarantine = %+v, want the pending 300", queue)
	}
	record, ok := restored.Submission("season", pending.SubmissionID)
	if !ok || record.Status != impl.StatusQuarantined {
		t.Fatalf("restored ledger record = %+v ok=%v, want pending", record, ok)
	}
	if err := restored.Release("season", pending.SubmissionID); err != nil {
		t.Fatalf("release after restore: %v", err)
	}
	if stats := restored.Stats("season"); stats.Quarantined != 0 || stats.Released != 1 {
		t.Fatalf("restored stats after release = %+v, want no pending and one released", stats)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	g := impl.NewScoreGuard(impl.Options{MaxSingleGain: 100})
	mustSubmit(t, g, "season", "alpha", 100, 60000, 1000)
	mustSubmit(t, g, "season", "alpha", 300, 60000, 2000)
	if err := g.Restore([]byte("{broken-payload")); !errors.Is(err, impl.ErrBadPayload) {
		t.Fatalf("corrupt snapshot must fail with ErrBadPayload, got %v", err)
	}
	score, ok := g.Effective("season", "alpha")
	if !ok || score != 100 {
		t.Fatalf("live effective after a rejected restore = %d ok=%v, want 100", score, ok)
	}
	if got := len(g.Quarantine("season")); got != 1 {
		t.Fatalf("live quarantine len after a rejected restore = %d, want 1", got)
	}
}
