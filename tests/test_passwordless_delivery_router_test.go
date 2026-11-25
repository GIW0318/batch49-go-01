package task

import (
	"errors"
	"testing"
	"time"

	impl "gsb/batch49-go-safety-a-21"
)

var routeBase = time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC)

func routerConfig() impl.RouterConfig {
	return impl.RouterConfig{
		MaxAttemptsPerPlan: 3,
		MaxSendsPerWindow:  10,
		Window:             time.Hour,
		MaxChannels:        3,
	}
}

func hasString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func TestRouteFollowsUserPreference(t *testing.T) {
	r := impl.NewPasswordlessDeliveryRouter(routerConfig())
	p, err := r.Route("user-1", impl.DeliveryRequest{
		PreferOrder: []string{impl.ChannelSmsCode, impl.ChannelEmailLink},
		Now:         routeBase,
	})
	if err != nil {
		t.Fatalf("route must succeed: %v", err)
	}
	if p.Channel != impl.ChannelSmsCode {
		t.Fatalf("first candidate must follow the user preference, got %q", p.Channel)
	}
	if p.Status != impl.PlanPending {
		t.Fatalf("new plan must be pending, got %q", p.Status)
	}
	if p.Attempt != 1 {
		t.Fatalf("first attempt must be 1, got %d", p.Attempt)
	}
	if p.ID == "" || p.DeliveryID == "" {
		t.Fatalf("plan and delivery identifiers must not be empty")
	}
	pref := r.Preference("user-1")
	if len(pref) != 2 || pref[0] != impl.ChannelSmsCode {
		t.Fatalf("preference must be remembered, got %v", pref)
	}
}

func TestRouteRejectsBlankUser(t *testing.T) {
	r := impl.NewPasswordlessDeliveryRouter(routerConfig())
	if _, err := r.Route("", impl.DeliveryRequest{Now: routeBase}); !errors.Is(err, impl.ErrBlankUser) {
		t.Fatalf("blank user must be rejected with ErrBlankUser, got %v", err)
	}
}

func TestRouteRejectsUnknownChannel(t *testing.T) {
	r := impl.NewPasswordlessDeliveryRouter(routerConfig())
	_, err := r.Route("user-2", impl.DeliveryRequest{
		PreferOrder: []string{"pigeon_post"},
		Now:         routeBase,
	})
	if !errors.Is(err, impl.ErrBadChannel) {
		t.Fatalf("unknown channel must be rejected with ErrBadChannel, got %v", err)
	}
}

func TestDeliveredPlanIsFinal(t *testing.T) {
	r := impl.NewPasswordlessDeliveryRouter(routerConfig())
	p, err := r.Route("user-3", impl.DeliveryRequest{Now: routeBase})
	if err != nil {
		t.Fatalf("route must succeed: %v", err)
	}
	err = r.Report(p.ID, impl.DeliveryReport{
		PlanID:  p.ID,
		Channel: p.Channel,
		Outcome: impl.OutcomeDelivered,
		At:      routeBase.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("delivered report must be accepted: %v", err)
	}
	got, ok := r.Lookup(p.ID)
	if !ok {
		t.Fatalf("plan %q must be found after the report", p.ID)
	}
	if got.Status != impl.PlanDelivered {
		t.Fatalf("plan must be delivered, got %q", got.Status)
	}
	if _, err := r.Retry(p.ID); !errors.Is(err, impl.ErrAlreadyFinal) {
		t.Fatalf("retrying a delivered plan must return ErrAlreadyFinal, got %v", err)
	}
}

func TestHardFailDowngradesChannel(t *testing.T) {
	r := impl.NewPasswordlessDeliveryRouter(routerConfig())
	p, err := r.Route("user-4", impl.DeliveryRequest{
		PreferOrder: []string{impl.ChannelEmailLink, impl.ChannelEmailCode},
		Now:         routeBase,
	})
	if err != nil {
		t.Fatalf("route must succeed: %v", err)
	}
	err = r.Report(p.ID, impl.DeliveryReport{
		PlanID:  p.ID,
		Channel: p.Channel,
		Outcome: impl.OutcomeHardFail,
		Reason:  "mailbox_rejected",
		At:      routeBase.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("hard failure report must be accepted: %v", err)
	}
	next, err := r.Retry(p.ID)
	if err != nil {
		t.Fatalf("retry after a hard failure must succeed: %v", err)
	}
	if next.Channel != impl.ChannelEmailCode {
		t.Fatalf("retry must downgrade to the next channel, got %q", next.Channel)
	}
	if next.Attempt != 2 {
		t.Fatalf("downgraded attempt must be 2, got %d", next.Attempt)
	}
	if next.ID != p.ID {
		t.Fatalf("retry must keep the plan identifier, got %q want %q", next.ID, p.ID)
	}
}

func TestSuppressedChannelIsSkipped(t *testing.T) {
	r := impl.NewPasswordlessDeliveryRouter(routerConfig())
	if err := r.Suppress(impl.ChannelEmailLink, "hard_bounce", routeBase.Add(time.Hour)); err != nil {
		t.Fatalf("suppress must succeed: %v", err)
	}
	p, err := r.Route("user-5", impl.DeliveryRequest{
		PreferOrder: []string{impl.ChannelEmailLink, impl.ChannelEmailCode},
		Now:         routeBase.Add(10 * time.Minute),
	})
	if err != nil {
		t.Fatalf("route must succeed: %v", err)
	}
	if p.Channel != impl.ChannelEmailCode {
		t.Fatalf("suppressed channel must be skipped, got %q", p.Channel)
	}
}

func TestSuppressRejectsUnknownChannel(t *testing.T) {
	r := impl.NewPasswordlessDeliveryRouter(routerConfig())
	err := r.Suppress("pigeon_post", "manual", routeBase.Add(time.Hour))
	if !errors.Is(err, impl.ErrBadChannel) {
		t.Fatalf("suppressing an unknown channel must return ErrBadChannel, got %v", err)
	}
}

func TestHealthScoreFollowsDeliveredReport(t *testing.T) {
	r := impl.NewPasswordlessDeliveryRouter(routerConfig())
	p, err := r.Route("user-6", impl.DeliveryRequest{
		PreferOrder: []string{impl.ChannelEmailCode},
		Now:         routeBase,
	})
	if err != nil {
		t.Fatalf("route must succeed: %v", err)
	}
	err = r.Report(p.ID, impl.DeliveryReport{
		PlanID:  p.ID,
		Channel: impl.ChannelEmailCode,
		Outcome: impl.OutcomeDelivered,
		At:      routeBase.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("delivered report must be accepted: %v", err)
	}
	h, err := r.Health(impl.ChannelEmailCode)
	if err != nil {
		t.Fatalf("health lookup must succeed: %v", err)
	}
	if h.Score != impl.DefaultHealthScore+impl.HealthDeliveredDelta {
		t.Fatalf("health score must grow by the delivered delta, got %d", h.Score)
	}
	if h.SoftFailures != 0 || h.HardFailures != 0 {
		t.Fatalf("failure counters must stay zero, got soft=%d hard=%d", h.SoftFailures, h.HardFailures)
	}
}

func TestAttemptBudgetExhausted(t *testing.T) {
	cfg := routerConfig()
	cfg.MaxAttemptsPerPlan = 2
	r := impl.NewPasswordlessDeliveryRouter(cfg)
	p, err := r.Route("user-7", impl.DeliveryRequest{
		PreferOrder: []string{impl.ChannelEmailLink, impl.ChannelEmailCode, impl.ChannelSmsCode},
		Now:         routeBase,
	})
	if err != nil {
		t.Fatalf("route must succeed: %v", err)
	}
	for i := 0; i < 2; i++ {
		err = r.Report(p.ID, impl.DeliveryReport{
			PlanID:  p.ID,
			Channel: p.Channel,
			Outcome: impl.OutcomeHardFail,
			At:      routeBase.Add(time.Duration(i+1) * time.Minute),
		})
		if err != nil {
			t.Fatalf("hard failure report %d must be accepted: %v", i+1, err)
		}
		if i == 0 {
			next, retryErr := r.Retry(p.ID)
			if retryErr != nil {
				t.Fatalf("first retry must succeed: %v", retryErr)
			}
			p = next
		}
	}
	if _, err := r.Retry(p.ID); !errors.Is(err, impl.ErrAttemptsSpent) {
		t.Fatalf("retry past the budget must return ErrAttemptsSpent, got %v", err)
	}
	got, ok := r.Lookup(p.ID)
	if !ok {
		t.Fatalf("exhausted plan must still be found")
	}
	if got.Status != impl.PlanExhausted {
		t.Fatalf("exhausted plan must be marked exhausted, got %q", got.Status)
	}
}

func TestRateLimitPerUserWindow(t *testing.T) {
	cfg := routerConfig()
	cfg.MaxSendsPerWindow = 2
	cfg.Window = time.Hour
	r := impl.NewPasswordlessDeliveryRouter(cfg)
	for i := 0; i < 2; i++ {
		if _, err := r.Route("user-8", impl.DeliveryRequest{
			PreferOrder: []string{impl.ChannelEmailLink},
			Now:         routeBase.Add(time.Duration(i) * 10 * time.Minute),
		}); err != nil {
			t.Fatalf("send %d inside the window must succeed: %v", i+1, err)
		}
	}
	_, err := r.Route("user-8", impl.DeliveryRequest{
		PreferOrder: []string{impl.ChannelEmailLink},
		Now:         routeBase.Add(20 * time.Minute),
	})
	if !errors.Is(err, impl.ErrRateLimited) {
		t.Fatalf("third send inside the window must be rate limited, got %v", err)
	}
	if _, err := r.Route("user-9", impl.DeliveryRequest{
		PreferOrder: []string{impl.ChannelEmailLink},
		Now:         routeBase.Add(20 * time.Minute),
	}); err != nil {
		t.Fatalf("another user must not inherit the limit: %v", err)
	}
}

func TestSnapshotKeepsSuppression(t *testing.T) {
	cfg := routerConfig()
	r := impl.NewPasswordlessDeliveryRouter(cfg)
	if err := r.Suppress(impl.ChannelEmailLink, "bounce", routeBase.Add(time.Hour)); err != nil {
		t.Fatalf("suppress must succeed: %v", err)
	}
	snap, err := r.Snapshot()
	if err != nil {
		t.Fatalf("snapshot must succeed: %v", err)
	}
	if len(snap) == 0 {
		t.Fatalf("snapshot payload must not be empty")
	}
	restored := impl.NewPasswordlessDeliveryRouter(cfg)
	if err := restored.Restore(snap); err != nil {
		t.Fatalf("restore must succeed: %v", err)
	}
	p, err := restored.Route("user-10", impl.DeliveryRequest{
		PreferOrder: []string{impl.ChannelEmailLink, impl.ChannelEmailCode},
		Now:         routeBase.Add(30 * time.Minute),
	})
	if err != nil {
		t.Fatalf("route after restore must succeed: %v", err)
	}
	if p.Channel != impl.ChannelEmailCode {
		t.Fatalf("restored suppression must still skip the channel, got %q", p.Channel)
	}
}

func TestRestoreRejectsBadSnapshot(t *testing.T) {
	r := impl.NewPasswordlessDeliveryRouter(routerConfig())
	if err := r.Suppress(impl.ChannelEmailLink, "bounce", routeBase.Add(time.Hour)); err != nil {
		t.Fatalf("suppress must succeed: %v", err)
	}
	if err := r.Restore([]byte("definitely-not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must return ErrBadSnapshot, got %v", err)
	}
	h, err := r.Health(impl.ChannelEmailLink)
	if err != nil {
		t.Fatalf("health after a failed restore must still work: %v", err)
	}
	if h.SuppressedUntil.IsZero() {
		t.Fatalf("failed restore must not wipe the live suppression deadline")
	}
}

func TestUnknownPlanRejected(t *testing.T) {
	r := impl.NewPasswordlessDeliveryRouter(routerConfig())
	if _, ok := r.Lookup("plan-missing"); ok {
		t.Fatalf("unknown plan must not be found")
	}
	err := r.Report("plan-missing", impl.DeliveryReport{
		Outcome: impl.OutcomeDelivered,
		At:      routeBase,
	})
	if !errors.Is(err, impl.ErrUnknownPlan) {
		t.Fatalf("report on an unknown plan must return ErrUnknownPlan, got %v", err)
	}
}

func TestMalformedReportRejected(t *testing.T) {
	r := impl.NewPasswordlessDeliveryRouter(routerConfig())
	p, err := r.Route("user-11", impl.DeliveryRequest{Now: routeBase})
	if err != nil {
		t.Fatalf("route must succeed: %v", err)
	}
	err = r.Report(p.ID, impl.DeliveryReport{
		PlanID:  p.ID,
		Channel: p.Channel,
		Outcome: "exploded",
		At:      routeBase.Add(time.Minute),
	})
	if !errors.Is(err, impl.ErrBadReport) {
		t.Fatalf("unknown outcome must return ErrBadReport, got %v", err)
	}
}

func TestHealthBaseline(t *testing.T) {
	r := impl.NewPasswordlessDeliveryRouter(routerConfig())
	h, err := r.Health(impl.ChannelEmailCode)
	if err != nil {
		t.Fatalf("health lookup must succeed: %v", err)
	}
	if h.ChannelID != impl.ChannelEmailCode {
		t.Fatalf("health must echo the channel, got %q", h.ChannelID)
	}
	if h.Score != impl.DefaultHealthScore {
		t.Fatalf("fresh channel must start at %d, got %d", impl.DefaultHealthScore, h.Score)
	}
	if !h.SuppressedUntil.IsZero() {
		t.Fatalf("fresh channel must not be suppressed, got %v", h.SuppressedUntil)
	}
}
