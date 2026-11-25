package task

import (
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	impl "gsb/batch49-go-safety-a-22"
)

var scorerBase = time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC)

func scorerConfig() impl.ScorerConfig {
	return impl.ScorerConfig{
		MaxSessionsPerUser: 4,
		MaxQueuePerSession: 8,
		MaxScore:           30,
		ChallengeThreshold: 20,
	}
}

func near(got, want float64) bool {
	return math.Abs(got-want) < 1e-9
}

func geoEvent(user string, weight float64) impl.Event {
	return impl.Event{UserID: user, Type: impl.EventLoginGeo, Weight: weight, At: scorerBase}
}

func TestObserveReturnsScoredSession(t *testing.T) {
	s := impl.NewSessionRiskScorer(scorerConfig())
	got, err := s.Observe("sess-1", geoEvent("user-1", 1.5))
	if err != nil {
		t.Fatalf("observe must succeed: %v", err)
	}
	if got.SessionID != "sess-1" {
		t.Fatalf("score must echo the session, got %q", got.SessionID)
	}
	if got.UserID != "user-1" {
		t.Fatalf("score must echo the user, got %q", got.UserID)
	}
	if got.Version != 1 {
		t.Fatalf("first observation must produce version 1, got %d", got.Version)
	}
	if got.UpdatedAt.IsZero() {
		t.Fatalf("score must carry an update timestamp")
	}
}

func TestObserveRejectsBlankSession(t *testing.T) {
	s := impl.NewSessionRiskScorer(scorerConfig())
	_, err := s.Observe("", geoEvent("user-1", 1.5))
	if !errors.Is(err, impl.ErrBlankSession) {
		t.Fatalf("blank session must return ErrBlankSession, got %v", err)
	}
}

func TestObserveRejectsUnknownEventType(t *testing.T) {
	s := impl.NewSessionRiskScorer(scorerConfig())
	_, err := s.Observe("sess-2", impl.Event{
		UserID: "user-1",
		Type:   "coffee_break",
		Weight: 2,
		At:     scorerBase,
	})
	if !errors.Is(err, impl.ErrBadEvent) {
		t.Fatalf("unknown event type must return ErrBadEvent, got %v", err)
	}
}

func TestScoreRejectsUnknownSession(t *testing.T) {
	s := impl.NewSessionRiskScorer(scorerConfig())
	if _, err := s.Score("sess-missing"); !errors.Is(err, impl.ErrUnknownSession) {
		t.Fatalf("unknown session must return ErrUnknownSession, got %v", err)
	}
}

func TestBaselineStartsAtInitialValue(t *testing.T) {
	s := impl.NewSessionRiskScorer(scorerConfig())
	got, err := s.Baseline("user-1", impl.EventLoginGeo, 2)
	if err != nil {
		t.Fatalf("baseline lookup must succeed: %v", err)
	}
	if !near(got, impl.InitialBaseline) {
		t.Fatalf("fresh baseline must be %v, got %v", impl.InitialBaseline, got)
	}
}

func TestBaselineFollowsEwma(t *testing.T) {
	s := impl.NewSessionRiskScorer(scorerConfig())
	for i := 0; i < 2; i++ {
		if _, err := s.Observe("sess-3", geoEvent("user-2", 5)); err != nil {
			t.Fatalf("observe %d must succeed: %v", i+1, err)
		}
	}
	got, err := s.Baseline("user-2", impl.EventLoginGeo, 2)
	if err != nil {
		t.Fatalf("baseline lookup must succeed: %v", err)
	}
	if !near(got, 2.44) {
		t.Fatalf("baseline after two weight-5 events must be 2.44, got %v", got)
	}
}

func TestDeviationRaisesScoreByFormula(t *testing.T) {
	s := impl.NewSessionRiskScorer(scorerConfig())
	got, err := s.Observe("sess-4", geoEvent("user-3", 9))
	if err != nil {
		t.Fatalf("observe must succeed: %v", err)
	}
	if !near(got.Value, 24) {
		t.Fatalf("weight 9 against baseline 1 must score 24, got %v", got.Value)
	}
	if got.Saturated {
		t.Fatalf("a score of 24 must not be saturated under a cap of 30")
	}
}

func TestScoreSaturatesAtMax(t *testing.T) {
	s := impl.NewSessionRiskScorer(scorerConfig())
	got, err := s.Observe("sess-5", geoEvent("user-4", 20))
	if err != nil {
		t.Fatalf("observe must succeed: %v", err)
	}
	if !near(got.Value, 30) {
		t.Fatalf("an extreme event must clamp at the 30 point cap, got %v", got.Value)
	}
	if !got.Saturated {
		t.Fatalf("a clamped score must be marked saturated")
	}
}

func TestConcurrentObserveKeepsVersionMonotonic(t *testing.T) {
	s := impl.NewSessionRiskScorer(scorerConfig())
	session := "sess-6"
	if _, err := s.Observe(session, geoEvent("user-5", 1.5)); err != nil {
		t.Fatalf("seed observe must succeed: %v", err)
	}
	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Observe(session, geoEvent("user-5", 1.5))
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent observe must succeed: %v", err)
		}
	}
	got, err := s.Score(session)
	if err != nil {
		t.Fatalf("score lookup must succeed: %v", err)
	}
	if got.Version != workers+1 {
		t.Fatalf("eight concurrent updates must land on version %d, got %d", workers+1, got.Version)
	}
}

func TestExplainReportsDeviationReason(t *testing.T) {
	s := impl.NewSessionRiskScorer(scorerConfig())
	if _, err := s.Observe("sess-7", geoEvent("user-6", 9)); err != nil {
		t.Fatalf("observe must succeed: %v", err)
	}
	reasons := s.Explain("sess-7")
	if len(reasons) == 0 {
		t.Fatalf("explain must return at least one reason")
	}
	found := false
	for _, reason := range reasons {
		if reason.Code == impl.ReasonBaselineDeviation && reason.Delta > 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("explain must report a positive baseline deviation, got %+v", reasons)
	}
}

func TestChallengeRejectsUnknownSession(t *testing.T) {
	s := impl.NewSessionRiskScorer(scorerConfig())
	if err := s.Challenge("sess-missing", impl.ChallengeStepUp); !errors.Is(err, impl.ErrUnknownSession) {
		t.Fatalf("challenging an unknown session must return ErrUnknownSession, got %v", err)
	}
}

func TestChallengeLatchesLevelAndID(t *testing.T) {
	s := impl.NewSessionRiskScorer(scorerConfig())
	if _, err := s.Observe("sess-8", geoEvent("user-7", 3)); err != nil {
		t.Fatalf("observe must succeed: %v", err)
	}
	if err := s.Challenge("sess-8", impl.ChallengeStepUp); err != nil {
		t.Fatalf("challenge must succeed: %v", err)
	}
	state, ok := s.ChallengeState("sess-8")
	if !ok {
		t.Fatalf("challenged session must expose a challenge state")
	}
	if !state.Latched {
		t.Fatalf("challenge must be latched")
	}
	if state.Level != impl.ChallengeStepUp {
		t.Fatalf("challenge level must be recorded, got %q", state.Level)
	}
	if state.ChallengeID == "" {
		t.Fatalf("challenge identifier must not be empty")
	}
}

func TestAckRejectsForeignChallenge(t *testing.T) {
	s := impl.NewSessionRiskScorer(scorerConfig())
	if _, err := s.Observe("sess-9", geoEvent("user-8", 3)); err != nil {
		t.Fatalf("observe must succeed: %v", err)
	}
	if err := s.Challenge("sess-9", impl.ChallengeBlock); err != nil {
		t.Fatalf("challenge must succeed: %v", err)
	}
	if err := s.Ack("sess-9", "challenge-from-elsewhere"); !errors.Is(err, impl.ErrBadChallenge) {
		t.Fatalf("acking a foreign challenge must return ErrBadChallenge, got %v", err)
	}
}

func TestSnapshotRestoreKeepsBaseline(t *testing.T) {
	s := impl.NewSessionRiskScorer(scorerConfig())
	for i := 0; i < 2; i++ {
		if _, err := s.Observe("sess-10", geoEvent("user-9", 5)); err != nil {
			t.Fatalf("observe %d must succeed: %v", i+1, err)
		}
	}
	before, err := s.Score("sess-10")
	if err != nil {
		t.Fatalf("score lookup must succeed: %v", err)
	}
	snap, err := s.Snapshot()
	if err != nil {
		t.Fatalf("snapshot must succeed: %v", err)
	}
	restored := impl.NewSessionRiskScorer(scorerConfig())
	if err := restored.Restore(snap); err != nil {
		t.Fatalf("restore must succeed: %v", err)
	}
	got, err := restored.Baseline("user-9", impl.EventLoginGeo, 2)
	if err != nil {
		t.Fatalf("baseline after restore must succeed: %v", err)
	}
	if !near(got, 2.44) {
		t.Fatalf("restored baseline must stay 2.44, got %v", got)
	}
	after, err := restored.Score("sess-10")
	if err != nil {
		t.Fatalf("score after restore must succeed: %v", err)
	}
	if !near(after.Value, before.Value) || after.Version != before.Version {
		t.Fatalf("restored score must match, got value=%v version=%d want value=%v version=%d",
			after.Value, after.Version, before.Value, before.Version)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	s := impl.NewSessionRiskScorer(scorerConfig())
	if err := s.Restore([]byte("}{ broken snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must return ErrBadSnapshot, got %v", err)
	}
}
