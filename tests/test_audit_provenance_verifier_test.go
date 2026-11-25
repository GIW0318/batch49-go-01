package task

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	impl "gsb/batch49-go-safety-d-06"
)

func tuning() impl.VerifierConfig {
	return impl.VerifierConfig{
		GapWindow:        3,
		MaxCredit:        4,
		MaxTimestampSkew: 2 * time.Second,
		MaxGapsPerSource: 8,
	}
}

func receiver(t *testing.T) *impl.AuditProvenanceVerifier {
	t.Helper()
	v := impl.NewAuditProvenanceVerifier(tuning())
	if err := v.Register(impl.Source{ID: "src-a", BootID: "boot-1", Key: "key-a"}); err != nil {
		t.Fatalf("register src-a: %v", err)
	}
	return v
}

func ev(seq uint64, digest string) impl.Event {
	return impl.Event{
		ID:     fmt.Sprintf("ev-%d-%s", seq, digest),
		Source: "src-a",
		BootID: "boot-1",
		Seq:    seq,
		At:     time.Unix(int64(seq), 0).UTC(),
		Signer: "key-a",
		Digest: digest,
	}
}

func mustAccept(t *testing.T, v *impl.AuditProvenanceVerifier, seq uint64, digest string) {
	t.Helper()
	verdict, reason, err := v.Verify(ev(seq, digest))
	if err != nil {
		t.Fatalf("verify seq %d: %v", seq, err)
	}
	if verdict != impl.VerdictAccept {
		t.Fatalf("verify seq %d verdict = %q, want %q", seq, verdict, impl.VerdictAccept)
	}
	if reason != impl.ReasonNone {
		t.Fatalf("verify seq %d reason = %q, want empty", seq, reason)
	}
}

func mustCredit(t *testing.T, v *impl.AuditProvenanceVerifier, want int) {
	t.Helper()
	got, err := v.Credit("src-a")
	if err != nil {
		t.Fatalf("credit: %v", err)
	}
	if got != want {
		t.Fatalf("credit = %d, want %d", got, want)
	}
}

func TestRegisterRejectsBlankAndDuplicateSource(t *testing.T) {
	v := impl.NewAuditProvenanceVerifier(tuning())
	if err := v.Register(impl.Source{ID: "", BootID: "boot-1", Key: "key-a"}); !errors.Is(err, impl.ErrBadSource) {
		t.Fatalf("blank source id err = %v, want ErrBadSource", err)
	}
	if err := v.Register(impl.Source{ID: "src-a", BootID: "", Key: "key-a"}); !errors.Is(err, impl.ErrBadSource) {
		t.Fatalf("blank boot id err = %v, want ErrBadSource", err)
	}
	if err := v.Register(impl.Source{ID: "src-a", BootID: "boot-1", Key: "key-a"}); err != nil {
		t.Fatalf("register src-a: %v", err)
	}
	if err := v.Register(impl.Source{ID: "src-a", BootID: "boot-2", Key: "key-a"}); !errors.Is(err, impl.ErrSourceExists) {
		t.Fatalf("duplicate register err = %v, want ErrSourceExists", err)
	}
	mustCredit(t, v, 4)
}

func TestUnsetConfigFallsBackToDefaults(t *testing.T) {
	v := impl.NewAuditProvenanceVerifier(impl.VerifierConfig{})
	if err := v.Register(impl.Source{ID: "src-a", BootID: "boot-1", Key: "key-a"}); err != nil {
		t.Fatalf("register src-a: %v", err)
	}
	credit, err := v.Credit("src-a")
	if err != nil {
		t.Fatalf("credit: %v", err)
	}
	if credit != impl.DefaultCredit {
		t.Fatalf("default credit = %d, want %d", credit, impl.DefaultCredit)
	}
	mustAccept(t, v, 1, "d1")
	verdict, reason, err := v.Verify(ev(5, "d5"))
	if err != nil {
		t.Fatalf("verify seq 5: %v", err)
	}
	if verdict != impl.VerdictGapPending {
		t.Fatalf("seq 5 verdict = %q, want %q with default window %d", verdict, impl.VerdictGapPending, impl.DefaultGapWindow)
	}
	if reason != impl.ReasonGapWithinWindow {
		t.Fatalf("seq 5 reason = %q, want %q", reason, impl.ReasonGapWithinWindow)
	}
	verdict, reason, err = v.Verify(ev(10, "d10"))
	if err != nil {
		t.Fatalf("verify seq 10: %v", err)
	}
	if verdict != impl.VerdictGap {
		t.Fatalf("seq 10 verdict = %q, want %q", verdict, impl.VerdictGap)
	}
	if reason != impl.ReasonGapBeyondWindow {
		t.Fatalf("seq 10 reason = %q, want %q", reason, impl.ReasonGapBeyondWindow)
	}
}

func TestUnknownSourceIsRejected(t *testing.T) {
	v := impl.NewAuditProvenanceVerifier(tuning())
	verdict, reason, err := v.Verify(ev(1, "d1"))
	if !errors.Is(err, impl.ErrUnknownSource) {
		t.Fatalf("verify unregistered err = %v, want ErrUnknownSource", err)
	}
	if verdict != impl.VerdictSourceMismatch {
		t.Fatalf("verdict = %q, want %q", verdict, impl.VerdictSourceMismatch)
	}
	if reason != impl.ReasonUnknownSource {
		t.Fatalf("reason = %q, want %q", reason, impl.ReasonUnknownSource)
	}
	if _, err := v.Watermark("src-a"); !errors.Is(err, impl.ErrUnknownSource) {
		t.Fatalf("watermark err = %v, want ErrUnknownSource", err)
	}
	if _, err := v.Credit("src-a"); !errors.Is(err, impl.ErrUnknownSource) {
		t.Fatalf("credit err = %v, want ErrUnknownSource", err)
	}
}

func TestSignatureMismatchKeepsWatermark(t *testing.T) {
	v := receiver(t)
	mustAccept(t, v, 1, "d1")
	forged := ev(2, "d2")
	forged.Signer = "key-b"
	verdict, reason, err := v.Verify(forged)
	if err != nil {
		t.Fatalf("verify forged event: %v", err)
	}
	if verdict != impl.VerdictSignatureMismatch {
		t.Fatalf("verdict = %q, want %q", verdict, impl.VerdictSignatureMismatch)
	}
	if reason != impl.ReasonSignatureMismatch {
		t.Fatalf("reason = %q, want %q", reason, impl.ReasonSignatureMismatch)
	}
	wm, err := v.Watermark("src-a")
	if err != nil {
		t.Fatalf("watermark: %v", err)
	}
	if wm != 1 {
		t.Fatalf("watermark = %d, want 1 after a forged event", wm)
	}
	mustCredit(t, v, 4)
	if gaps := v.Gaps("src-a"); len(gaps) != 0 {
		t.Fatalf("gaps after forged event = %+v, want none", gaps)
	}
}

func TestFirstEventAdvancesWatermark(t *testing.T) {
	v := receiver(t)
	verdict, reason, err := v.Verify(ev(1, "d1"))
	if err != nil {
		t.Fatalf("verify first event: %v", err)
	}
	if verdict != impl.VerdictAccept || reason != impl.ReasonNone {
		t.Fatalf("first event = (%q,%q), want (%q,empty)", verdict, reason, impl.VerdictAccept)
	}
	wm, err := v.Watermark("src-a")
	if err != nil {
		t.Fatalf("watermark: %v", err)
	}
	if wm != 1 {
		t.Fatalf("watermark = %d, want 1", wm)
	}
	isolated, why := v.Isolated("src-a")
	if isolated || why != "" {
		t.Fatalf("source isolated=%v reason=%q, want false", isolated, why)
	}
	mustCredit(t, v, 4)
}

func TestDuplicateWithSameDigestIsIdempotent(t *testing.T) {
	v := receiver(t)
	mustAccept(t, v, 1, "d1")
	verdict, reason, err := v.Verify(ev(1, "d1"))
	if err != nil {
		t.Fatalf("verify replay: %v", err)
	}
	if verdict != impl.VerdictDuplicate {
		t.Fatalf("replay verdict = %q, want %q", verdict, impl.VerdictDuplicate)
	}
	if reason != impl.ReasonDuplicateSeq {
		t.Fatalf("replay reason = %q, want %q", reason, impl.ReasonDuplicateSeq)
	}
	verdict, _, err = v.Verify(ev(1, "d1"))
	if err != nil || verdict != impl.VerdictDuplicate {
		t.Fatalf("second replay = (%q,%v), want duplicate without error", verdict, err)
	}
	wm, err := v.Watermark("src-a")
	if err != nil {
		t.Fatalf("watermark: %v", err)
	}
	if wm != 1 {
		t.Fatalf("watermark = %d, want 1", wm)
	}
	mustCredit(t, v, 4)
	if gaps := v.Gaps("src-a"); len(gaps) != 0 {
		t.Fatalf("gaps = %+v, want none", gaps)
	}
}

func TestDuplicateWithConflictingDigestCostsCredit(t *testing.T) {
	v := receiver(t)
	mustAccept(t, v, 1, "d1")
	verdict, reason, err := v.Verify(ev(1, "d9"))
	if err != nil {
		t.Fatalf("verify conflicting duplicate: %v", err)
	}
	if verdict != impl.VerdictDuplicate {
		t.Fatalf("verdict = %q, want %q", verdict, impl.VerdictDuplicate)
	}
	if reason != impl.ReasonDuplicateConflict {
		t.Fatalf("reason = %q, want %q", reason, impl.ReasonDuplicateConflict)
	}
	mustCredit(t, v, 3)
	wm, err := v.Watermark("src-a")
	if err != nil {
		t.Fatalf("watermark: %v", err)
	}
	if wm != 1 {
		t.Fatalf("watermark = %d, want 1", wm)
	}
}

func TestOutOfOrderSequenceIsReported(t *testing.T) {
	v := receiver(t)
	mustAccept(t, v, 1, "d1")
	mustAccept(t, v, 2, "d2")
	mustAccept(t, v, 3, "d3")
	verdict, reason, err := v.Verify(ev(2, "d2-old"))
	if err != nil {
		t.Fatalf("verify late event: %v", err)
	}
	if verdict != impl.VerdictOutOfOrder {
		t.Fatalf("verdict = %q, want %q", verdict, impl.VerdictOutOfOrder)
	}
	if reason != impl.ReasonBelowWatermark {
		t.Fatalf("reason = %q, want %q", reason, impl.ReasonBelowWatermark)
	}
	wm, err := v.Watermark("src-a")
	if err != nil {
		t.Fatalf("watermark: %v", err)
	}
	if wm != 3 {
		t.Fatalf("watermark = %d, want 3", wm)
	}
	mustCredit(t, v, 4)
}

func TestGapInsideWindowIsPending(t *testing.T) {
	v := receiver(t)
	mustAccept(t, v, 1, "d1")
	verdict, reason, err := v.Verify(ev(4, "d4"))
	if err != nil {
		t.Fatalf("verify seq 4: %v", err)
	}
	if verdict != impl.VerdictGapPending {
		t.Fatalf("verdict = %q, want %q", verdict, impl.VerdictGapPending)
	}
	if reason != impl.ReasonGapWithinWindow {
		t.Fatalf("reason = %q, want %q", reason, impl.ReasonGapWithinWindow)
	}
	wm, err := v.Watermark("src-a")
	if err != nil {
		t.Fatalf("watermark: %v", err)
	}
	if wm != 4 {
		t.Fatalf("watermark = %d, want 4", wm)
	}
	mustCredit(t, v, 4)
	gaps := v.Gaps("src-a")
	if len(gaps) != 1 {
		t.Fatalf("gaps = %+v, want exactly one", gaps)
	}
	if gaps[0].Source != "src-a" || gaps[0].From != 2 || gaps[0].To != 3 {
		t.Fatalf("gap = %+v, want src-a [2,3]", gaps[0])
	}
	mustAccept(t, v, 5, "d5")
}

func TestGapBeyondWindowDeductsCredit(t *testing.T) {
	v := receiver(t)
	mustAccept(t, v, 1, "d1")
	verdict, reason, err := v.Verify(ev(9, "d9"))
	if err != nil {
		t.Fatalf("verify seq 9: %v", err)
	}
	if verdict != impl.VerdictGap {
		t.Fatalf("verdict = %q, want %q", verdict, impl.VerdictGap)
	}
	if reason != impl.ReasonGapBeyondWindow {
		t.Fatalf("reason = %q, want %q", reason, impl.ReasonGapBeyondWindow)
	}
	wm, err := v.Watermark("src-a")
	if err != nil {
		t.Fatalf("watermark: %v", err)
	}
	if wm != 9 {
		t.Fatalf("watermark = %d, want 9", wm)
	}
	mustCredit(t, v, 3)
	gaps := v.Gaps("src-a")
	if len(gaps) != 1 || gaps[0].From != 2 || gaps[0].To != 8 {
		t.Fatalf("gaps = %+v, want one gap [2,8]", gaps)
	}
}

func TestCreditExhaustionIsolatesSource(t *testing.T) {
	v := impl.NewAuditProvenanceVerifier(impl.VerifierConfig{GapWindow: 3, MaxCredit: 2})
	if err := v.Register(impl.Source{ID: "src-a", BootID: "boot-1", Key: "key-a"}); err != nil {
		t.Fatalf("register src-a: %v", err)
	}
	mustAccept(t, v, 1, "d1")
	verdict, _, err := v.Verify(ev(9, "d9"))
	if err != nil || verdict != impl.VerdictGap {
		t.Fatalf("first gap = (%q,%v), want gap without error", verdict, err)
	}
	mustCredit(t, v, 1)
	verdict, reason, err := v.Verify(ev(9, "d9-again"))
	if err != nil {
		t.Fatalf("second conflict: %v", err)
	}
	if verdict != impl.VerdictDuplicate || reason != impl.ReasonDuplicateConflict {
		t.Fatalf("second conflict = (%q,%q), want duplicate/duplicate_conflict", verdict, reason)
	}
	mustCredit(t, v, 0)
	isolated, why := v.Isolated("src-a")
	if !isolated {
		t.Fatalf("source must be isolated once credit reaches zero")
	}
	if why != string(impl.ReasonCreditExhausted) {
		t.Fatalf("isolation reason = %q, want %q", why, impl.ReasonCreditExhausted)
	}
	if gaps := v.Gaps("src-a"); len(gaps) != 1 {
		t.Fatalf("gaps after isolation = %+v, want the earlier gap kept", gaps)
	}
	verdict, reason, err = v.Verify(ev(10, "d10"))
	if !errors.Is(err, impl.ErrSourceIsolated) {
		t.Fatalf("verify isolated source err = %v, want ErrSourceIsolated", err)
	}
	if verdict != impl.VerdictIsolated || reason != impl.ReasonCreditExhausted {
		t.Fatalf("isolated verdict = (%q,%q), want isolated/credit_exhausted", verdict, reason)
	}
}

func TestManualIsolationQuarantinesSource(t *testing.T) {
	v := receiver(t)
	if err := v.Isolate("src-x", "compromised"); !errors.Is(err, impl.ErrUnknownSource) {
		t.Fatalf("isolate unknown source err = %v, want ErrUnknownSource", err)
	}
	if err := v.Isolate("src-a", "compromised"); err != nil {
		t.Fatalf("isolate src-a: %v", err)
	}
	isolated, why := v.Isolated("src-a")
	if !isolated || why != "compromised" {
		t.Fatalf("isolated=%v reason=%q, want true/compromised", isolated, why)
	}
	verdict, reason, err := v.Verify(ev(1, "d1"))
	if !errors.Is(err, impl.ErrSourceIsolated) {
		t.Fatalf("verify quarantined err = %v, want ErrSourceIsolated", err)
	}
	if verdict != impl.VerdictIsolated || reason != impl.ReasonManualIsolation {
		t.Fatalf("quarantined verdict = (%q,%q), want isolated/manual_isolation", verdict, reason)
	}
}

func TestSnapshotRestoresWatermarkCreditAndGaps(t *testing.T) {
	v := receiver(t)
	mustAccept(t, v, 1, "d1")
	if verdict, _, err := v.Verify(ev(5, "d5")); err != nil || verdict != impl.VerdictGapPending {
		t.Fatalf("seq 5 = (%q,%v), want gap_pending", verdict, err)
	}
	if verdict, _, err := v.Verify(ev(5, "d5-alt")); err != nil || verdict != impl.VerdictDuplicate {
		t.Fatalf("conflict at 5 = (%q,%v), want duplicate", verdict, err)
	}
	mustCredit(t, v, 3)
	payload, err := v.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(payload) == 0 {
		t.Fatalf("snapshot payload is empty")
	}
	restored := impl.NewAuditProvenanceVerifier(tuning())
	if err := restored.Restore(payload); err != nil {
		t.Fatalf("restore: %v", err)
	}
	wm, err := restored.Watermark("src-a")
	if err != nil {
		t.Fatalf("restored watermark: %v", err)
	}
	if wm != 5 {
		t.Fatalf("restored watermark = %d, want 5", wm)
	}
	credit, err := restored.Credit("src-a")
	if err != nil {
		t.Fatalf("restored credit: %v", err)
	}
	if credit != 3 {
		t.Fatalf("restored credit = %d, want 3", credit)
	}
	gaps := restored.Gaps("src-a")
	if len(gaps) != 1 || gaps[0].From != 2 || gaps[0].To != 4 {
		t.Fatalf("restored gaps = %+v, want one gap [2,4]", gaps)
	}
	mustAccept(t, restored, 6, "d6")
	if err := restored.Restore([]byte("not a snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("restore garbage err = %v, want ErrBadSnapshot", err)
	}
}

func TestConcurrentReplayAcceptsExactlyOnce(t *testing.T) {
	v := receiver(t)
	var mu sync.Mutex
	accepted, duplicates := 0, 0
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			verdict, _, err := v.Verify(ev(1, "d1"))
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				t.Errorf("concurrent verify: %v", err)
				return
			}
			switch verdict {
			case impl.VerdictAccept:
				accepted++
			case impl.VerdictDuplicate:
				duplicates++
			}
		}()
	}
	wg.Wait()
	if accepted != 1 {
		t.Fatalf("accepted = %d, want exactly 1", accepted)
	}
	if duplicates != 63 {
		t.Fatalf("duplicates = %d, want 63", duplicates)
	}
	wm, err := v.Watermark("src-a")
	if err != nil {
		t.Fatalf("watermark: %v", err)
	}
	if wm != 1 {
		t.Fatalf("watermark = %d, want 1", wm)
	}
}

func TestOwnerIsRecordedForAcceptedEvent(t *testing.T) {
	v := receiver(t)
	mustAccept(t, v, 1, "d1")
	owner, ok := v.Owner(ev(1, "d1").ID)
	if !ok {
		t.Fatalf("owner of accepted event not recorded")
	}
	if owner != "src-a" {
		t.Fatalf("owner = %q, want src-a", owner)
	}
	if owner, ok := v.Owner("ev-missing"); ok || owner != "" {
		t.Fatalf("owner of unknown event = (%q,%v), want empty/false", owner, ok)
	}
}
