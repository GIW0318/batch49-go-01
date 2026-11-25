package task

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	impl "gsb/batch49-go-safety-e-07"
)

func day(n int) time.Time {
	return time.Date(2026, 3, 2, 8, 0, 0, 0, time.UTC).Add(time.Duration(n) * 24 * time.Hour)
}

func statement(id string, revision int, status string, at time.Time) impl.Statement {
	return impl.Statement{
		ID:          id,
		Upstream:    "up-a",
		Component:   "openssl",
		CVE:         "CVE-2026-1001",
		Status:      status,
		Revision:    revision,
		AffectsFrom: "1.0.0",
		AffectsTo:   "1.5.0",
		DeclaredAt:  at,
	}
}

func readyPropagator(t *testing.T) *impl.VexStatePropagator {
	t.Helper()
	p := impl.NewVexStatePropagator()
	if err := p.RegisterConsumer("consumer-1"); err != nil {
		t.Fatalf("register consumer: %v", err)
	}
	if err := p.RegisterComponent("openssl", "1.2.0"); err != nil {
		t.Fatalf("register component: %v", err)
	}
	return p
}

func TestRegisterConsumerRejectsBlank(t *testing.T) {
	p := impl.NewVexStatePropagator()
	if err := p.RegisterConsumer(""); !errors.Is(err, impl.ErrBlankField) {
		t.Fatalf("blank consumer must fail with ErrBlankField, got %v", err)
	}
	if err := p.RegisterConsumer("   "); !errors.Is(err, impl.ErrBlankField) {
		t.Fatalf("whitespace consumer must fail with ErrBlankField, got %v", err)
	}
	if err := p.RegisterConsumer("consumer-1"); err != nil {
		t.Fatalf("register consumer: %v", err)
	}
	if err := p.RegisterConsumer("consumer-1"); err != nil {
		t.Fatalf("registering the same consumer again must be idempotent, got %v", err)
	}
}

func TestRegisterComponentRejectsBlank(t *testing.T) {
	p := impl.NewVexStatePropagator()
	if err := p.RegisterComponent("", "1.0.0"); !errors.Is(err, impl.ErrBlankField) {
		t.Fatalf("blank component must fail with ErrBlankField, got %v", err)
	}
	if err := p.RegisterComponent("openssl", ""); !errors.Is(err, impl.ErrBlankField) {
		t.Fatalf("blank version must fail with ErrBlankField, got %v", err)
	}
	if err := p.RegisterComponent("openssl", "1.0.0"); err != nil {
		t.Fatalf("register component: %v", err)
	}
}

func TestPublishRejectsBadStatus(t *testing.T) {
	p := impl.NewVexStatePropagator()
	st := statement("s-1", 1, "probably_fine", day(0))
	if err := p.Publish(st, day(0)); !errors.Is(err, impl.ErrBadStatus) {
		t.Fatalf("unknown status must fail with ErrBadStatus, got %v", err)
	}
	blank := statement("s-2", 1, impl.StatusAffected, day(0))
	blank.Upstream = "  "
	if err := p.Publish(blank, day(0)); !errors.Is(err, impl.ErrBlankField) {
		t.Fatalf("blank upstream must fail with ErrBlankField, got %v", err)
	}
}

func TestPublishRejectsBadRevision(t *testing.T) {
	p := impl.NewVexStatePropagator()
	if err := p.Publish(statement("s-1", 0, impl.StatusAffected, day(0)), day(0)); !errors.Is(err, impl.ErrBadRevision) {
		t.Fatalf("revision zero must fail with ErrBadRevision, got %v", err)
	}
	if err := p.Publish(statement("s-2", -3, impl.StatusAffected, day(0)), day(0)); !errors.Is(err, impl.ErrBadRevision) {
		t.Fatalf("negative revision must fail with ErrBadRevision, got %v", err)
	}
}

func TestPublishRejectsBadRange(t *testing.T) {
	p := impl.NewVexStatePropagator()
	bad := statement("s-1", 1, impl.StatusAffected, day(0))
	bad.AffectsFrom = "1.x"
	if err := p.Publish(bad, day(0)); !errors.Is(err, impl.ErrBadRange) {
		t.Fatalf("bad lower endpoint must fail with ErrBadRange, got %v", err)
	}
	bad = statement("s-2", 1, impl.StatusAffected, day(0))
	bad.AffectsTo = "later"
	if err := p.Publish(bad, day(0)); !errors.Is(err, impl.ErrBadRange) {
		t.Fatalf("bad upper endpoint must fail with ErrBadRange, got %v", err)
	}
}

func TestStateForUnknownConsumer(t *testing.T) {
	p := impl.NewVexStatePropagator()
	if _, err := p.StateFor("nobody", "openssl", "CVE-2026-1001"); !errors.Is(err, impl.ErrUnknownConsumer) {
		t.Fatalf("unknown consumer must fail with ErrUnknownConsumer, got %v", err)
	}
}

func TestStateForUnknownStatement(t *testing.T) {
	p := readyPropagator(t)
	if _, err := p.StateFor("consumer-1", "openssl", "CVE-2026-9999"); !errors.Is(err, impl.ErrUnknownStatement) {
		t.Fatalf("missing statement must fail with ErrUnknownStatement, got %v", err)
	}
	if _, err := p.StateFor("consumer-1", "ghost", "CVE-2026-1001"); !errors.Is(err, impl.ErrUnknownComponent) {
		t.Fatalf("unknown component must fail with ErrUnknownComponent, got %v", err)
	}
}

func TestStateForLatestRevisionWins(t *testing.T) {
	p := readyPropagator(t)
	if err := p.Publish(statement("s-1", 1, impl.StatusAffected, day(0)), day(0)); err != nil {
		t.Fatalf("publish revision 1: %v", err)
	}
	if err := p.Publish(statement("s-2", 2, impl.StatusNotAffected, day(1)), day(1)); err != nil {
		t.Fatalf("publish revision 2: %v", err)
	}
	state, err := p.StateFor("consumer-1", "openssl", "CVE-2026-1001")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if state.Revision != 2 || state.Status != impl.StatusNotAffected {
		t.Fatalf("state = %+v, want revision 2 not_affected", state)
	}
	if state.UpstreamStatus != impl.StatusNotAffected {
		t.Fatalf("upstream status = %q, want not_affected", state.UpstreamStatus)
	}
	if state.Basis != impl.BasisUpstream {
		t.Fatalf("basis = %q, want %q", state.Basis, impl.BasisUpstream)
	}
	if state.Acked {
		t.Fatalf("state must not be acked before any acknowledgement")
	}
}

func TestStateForOutsideVersionRange(t *testing.T) {
	p := impl.NewVexStatePropagator()
	if err := p.RegisterConsumer("consumer-1"); err != nil {
		t.Fatalf("register consumer: %v", err)
	}
	if err := p.RegisterComponent("openssl", "2.5.0"); err != nil {
		t.Fatalf("register component: %v", err)
	}
	if err := p.Publish(statement("s-1", 1, impl.StatusAffected, day(0)), day(0)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	state, err := p.StateFor("consumer-1", "openssl", "CVE-2026-1001")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if state.Status != impl.StatusNotAffected || state.Basis != impl.BasisOutsideRange {
		t.Fatalf("state = %+v, want not_affected outside_range", state)
	}
	if state.UpstreamStatus != impl.StatusAffected {
		t.Fatalf("the upstream conclusion must stay visible, got %q", state.UpstreamStatus)
	}
}

func TestStateForExpiredFallsBack(t *testing.T) {
	p := readyPropagator(t)
	st := statement("s-1", 1, impl.StatusAffected, day(0))
	st.ExpiresAt = day(2)
	if err := p.Publish(st, day(0)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := p.Ack("consumer-1", day(3)); err != nil {
		t.Fatalf("ack: %v", err)
	}
	state, err := p.StateFor("consumer-1", "openssl", "CVE-2026-1001")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if state.Status != impl.StatusUnderInvestigation || state.Basis != impl.BasisExpired {
		t.Fatalf("state = %+v, want under_investigation expired", state)
	}
}

func TestRetractOverridesConclusion(t *testing.T) {
	p := readyPropagator(t)
	if err := p.Publish(statement("s-1", 1, impl.StatusAffected, day(0)), day(0)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := p.Retract(statement("r-1", 1, impl.StatusRetracted, day(1)), day(1)); err != nil {
		t.Fatalf("retract: %v", err)
	}
	state, err := p.StateFor("consumer-1", "openssl", "CVE-2026-1001")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if state.Status != impl.StatusRetracted || state.Basis != impl.BasisRetracted {
		t.Fatalf("state = %+v, want retracted/retracted", state)
	}
	if state.Revision != 1 {
		t.Fatalf("retraction state revision = %d, want 1", state.Revision)
	}
	history := p.History("consumer-1", "openssl", "CVE-2026-1001")
	if len(history) != 2 {
		t.Fatalf("history length = %d, want the statement plus the retraction", len(history))
	}
	if history[0].Kind != impl.RecordStatement || history[0].Valid {
		t.Fatalf("first record = %+v, want an invalidated statement", history[0])
	}
	if history[1].Kind != impl.RecordRetraction || history[1].Status != impl.StatusRetracted {
		t.Fatalf("second record = %+v, want the retraction record", history[1])
	}
}

func TestRetractIsIdempotent(t *testing.T) {
	p := readyPropagator(t)
	if err := p.Publish(statement("s-1", 1, impl.StatusAffected, day(0)), day(0)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	ret := statement("r-1", 1, impl.StatusRetracted, day(1))
	if err := p.Retract(ret, day(1)); err != nil {
		t.Fatalf("first retract: %v", err)
	}
	if err := p.Retract(ret, day(1)); err != nil {
		t.Fatalf("second retract must be idempotent, got %v", err)
	}
	history := p.History("consumer-1", "openssl", "CVE-2026-1001")
	if len(history) != 2 {
		t.Fatalf("history length = %d, want exactly one retraction record", len(history))
	}
}

func TestPendingCountsUnackedKeys(t *testing.T) {
	p := readyPropagator(t)
	if err := p.Publish(statement("s-1", 1, impl.StatusAffected, day(0)), day(0)); err != nil {
		t.Fatalf("publish first key: %v", err)
	}
	second := statement("s-2", 1, impl.StatusNotAffected, day(1))
	second.CVE = "CVE-2026-2002"
	if err := p.Publish(second, day(1)); err != nil {
		t.Fatalf("publish second key: %v", err)
	}
	if got := p.Pending("consumer-1"); got != 2 {
		t.Fatalf("pending before ack = %d, want 2", got)
	}
	if err := p.Ack("consumer-1", day(0)); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if got := p.Pending("consumer-1"); got != 1 {
		t.Fatalf("pending after acking day 0 = %d, want 1", got)
	}
	if err := p.Ack("consumer-1", day(5)); err != nil {
		t.Fatalf("ack all: %v", err)
	}
	if got := p.Pending("consumer-1"); got != 0 {
		t.Fatalf("pending after acking everything = %d, want 0", got)
	}
	if err := p.Ack("consumer-1", day(3)); err != nil {
		t.Fatalf("ack watermark must be monotonic, got %v", err)
	}
	if got := p.Pending("consumer-1"); got != 0 {
		t.Fatalf("an older ack must not resurrect pending work, got %d", got)
	}
}

func TestConsumersAndHistoryOrdered(t *testing.T) {
	p := impl.NewVexStatePropagator()
	for _, consumer := range []string{"consumer-b", "consumer-a"} {
		if err := p.RegisterConsumer(consumer); err != nil {
			t.Fatalf("register %s: %v", consumer, err)
		}
	}
	if err := p.RegisterComponent("openssl", "1.2.0"); err != nil {
		t.Fatalf("register component: %v", err)
	}
	if err := p.Publish(statement("s-1", 1, impl.StatusUnderInvestigation, day(0)), day(0)); err != nil {
		t.Fatalf("publish revision 1: %v", err)
	}
	if err := p.Publish(statement("s-2", 2, impl.StatusAffected, day(1)), day(1)); err != nil {
		t.Fatalf("publish revision 2: %v", err)
	}
	consumers := p.Consumers("openssl", "CVE-2026-1001")
	if len(consumers) != 2 || consumers[0] != "consumer-a" || consumers[1] != "consumer-b" {
		t.Fatalf("consumers = %v, want [consumer-a consumer-b]", consumers)
	}
	history := p.History("consumer-a", "openssl", "CVE-2026-1001")
	if len(history) != 2 {
		t.Fatalf("history length = %d, want 2", len(history))
	}
	if history[0].Revision != 1 || history[1].Revision != 2 {
		t.Fatalf("history revisions = %d,%d, want 1,2", history[0].Revision, history[1].Revision)
	}
	for _, record := range history {
		if !record.Valid {
			t.Fatalf("record %+v must stay valid when nothing was retracted", record)
		}
	}
}

func TestConcurrentPublishKeepsOnePending(t *testing.T) {
	p := readyPropagator(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	failures := 0
	for i := 1; i <= 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			st := statement(fmt.Sprintf("s-%02d", i), i, impl.StatusAffected, day(0).Add(time.Duration(i)*time.Minute))
			if err := p.Publish(st, st.DeclaredAt); err != nil {
				mu.Lock()
				failures++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if failures != 0 {
		t.Fatalf("%d concurrent publishes failed", failures)
	}
	if got := p.Pending("consumer-1"); got != 1 {
		t.Fatalf("pending = %d, want one pending key", got)
	}
	state, err := p.StateFor("consumer-1", "openssl", "CVE-2026-1001")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if state.Revision != 10 || state.Status != impl.StatusAffected {
		t.Fatalf("state = %+v, want revision 10 affected", state)
	}
}
