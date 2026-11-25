package task

import (
	"errors"
	"testing"
	"time"

	impl "gsb/batch49-go-safety-e-03"
)

func allDayWindow() impl.Window {
	return impl.Window{
		Zone:  "UTC",
		Days:  []time.Weekday{time.Sunday, time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday, time.Saturday},
		Start: 0,
		End:   24 * time.Hour,
	}
}

func mondayWindow() impl.Window {
	return impl.Window{
		Zone:  "UTC",
		Days:  []time.Weekday{time.Monday},
		Start: 9 * time.Hour,
		End:   18 * time.Hour,
	}
}

func mustDeclare(t *testing.T, p *impl.ScanWindowPlanner, group string, w impl.Window) {
	t.Helper()
	if err := p.Declare(group, w); err != nil {
		t.Fatalf("declare group %s: %v", group, err)
	}
}

func busyPlanner(t *testing.T) *impl.ScanWindowPlanner {
	t.Helper()
	p := impl.NewScanWindowPlanner(impl.Limits{PerTargetConcurrency: 1, MaxThrottles: 2, MaxBackoff: time.Minute})
	if p == nil {
		t.Fatalf("constructor returned nil")
	}
	mustDeclare(t, p, "batch-night", allDayWindow())
	return p
}

func TestDeclareRejectsInvertedWindow(t *testing.T) {
	p := impl.NewScanWindowPlanner(impl.Limits{PerTargetConcurrency: 1, MaxThrottles: 2, MaxBackoff: time.Minute})
	err := p.Declare("g1", impl.Window{Zone: "UTC", Days: []time.Weekday{time.Monday}, Start: 18 * time.Hour, End: 9 * time.Hour})
	if !errors.Is(err, impl.ErrBadWindow) {
		t.Fatalf("inverted window must report ErrBadWindow, got %v", err)
	}
	if got := len(p.Groups()); got != 0 {
		t.Fatalf("rejected window must not be declared, group count = %d", got)
	}
}

func TestDeclareRejectsUnknownZone(t *testing.T) {
	p := impl.NewScanWindowPlanner(impl.Limits{PerTargetConcurrency: 1, MaxThrottles: 2, MaxBackoff: time.Minute})
	err := p.Declare("g1", impl.Window{Zone: "Mars/Olympus", Days: []time.Weekday{time.Monday}, Start: 0, End: time.Hour})
	if !errors.Is(err, impl.ErrBadWindow) {
		t.Fatalf("unknown time zone must report ErrBadWindow, got %v", err)
	}
}

func TestDeclareRejectsDuplicateGroup(t *testing.T) {
	p := busyPlanner(t)
	err := p.Declare("batch-night", allDayWindow())
	if !errors.Is(err, impl.ErrDuplicateGroup) {
		t.Fatalf("redeclaring a group must report ErrDuplicateGroup, got %v", err)
	}
}

func TestSubmitRejectsUnknownGroup(t *testing.T) {
	p := busyPlanner(t)
	_, err := p.Submit(impl.Task{ID: "t-1", Group: "no-such-group", Target: "host-1"})
	if !errors.Is(err, impl.ErrUnknownGroup) {
		t.Fatalf("submit to an undeclared group must report ErrUnknownGroup, got %v", err)
	}
}

func TestSubmitRejectsDuplicateTaskID(t *testing.T) {
	p := busyPlanner(t)
	if _, err := p.Submit(impl.Task{ID: "t-1", Group: "batch-night", Target: "host-1"}); err != nil {
		t.Fatalf("first submit: %v", err)
	}
	_, err := p.Submit(impl.Task{ID: "t-1", Group: "batch-night", Target: "host-2"})
	if !errors.Is(err, impl.ErrDuplicateTask) {
		t.Fatalf("duplicate task id must report ErrDuplicateTask, got %v", err)
	}
	if got := p.Pending("batch-night"); got != 1 {
		t.Fatalf("duplicate submit must not enqueue, pending = %d", got)
	}
}

func TestInWindowUsesDeclaredZone(t *testing.T) {
	p := busyPlanner(t)
	mustDeclare(t, p, "office-hours", mondayWindow())
	monday := time.Date(2026, time.November, 16, 10, 0, 0, 0, time.UTC)
	if monday.Weekday() != time.Monday {
		t.Fatalf("test fixture date is not a Monday: %v", monday.Weekday())
	}
	open, err := p.InWindow("office-hours", monday)
	if err != nil {
		t.Fatalf("in window: %v", err)
	}
	if !open {
		t.Fatalf("Monday 10:00 UTC must be inside the 09:00-18:00 window")
	}
	late, err := p.InWindow("office-hours", time.Date(2026, time.November, 16, 19, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("in window: %v", err)
	}
	if late {
		t.Fatalf("Monday 19:00 UTC must be outside the 09:00-18:00 window")
	}
	if _, err := p.InWindow("ghost-group", monday); !errors.Is(err, impl.ErrUnknownGroup) {
		t.Fatalf("window of an undeclared group must report ErrUnknownGroup, got %v", err)
	}
}

func TestNextSkipsTasksOutsideWindow(t *testing.T) {
	p := busyPlanner(t)
	mustDeclare(t, p, "office-hours", mondayWindow())
	if _, err := p.Submit(impl.Task{ID: "t-1", Group: "office-hours", Target: "host-1"}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	tuesday := time.Date(2026, time.November, 17, 10, 0, 0, 0, time.UTC)
	if task, ok := p.Next(tuesday); ok {
		t.Fatalf("Tuesday must not dispatch, got %+v", task)
	}
	monday := time.Date(2026, time.November, 16, 10, 0, 0, 0, time.UTC)
	task, ok := p.Next(monday)
	if !ok || task.ID != "t-1" {
		t.Fatalf("Monday inside window must dispatch t-1, got %+v ok=%v", task, ok)
	}
}

func TestNextHonorsPerTargetGate(t *testing.T) {
	p := busyPlanner(t)
	for _, task := range []impl.Task{
		{ID: "a-1", Group: "batch-night", Target: "host-1"},
		{ID: "a-2", Group: "batch-night", Target: "host-1"},
		{ID: "b-1", Group: "batch-night", Target: "host-2"},
	} {
		if _, err := p.Submit(task); err != nil {
			t.Fatalf("submit %s: %v", task.ID, err)
		}
	}
	now := time.Date(2026, time.November, 16, 10, 0, 0, 0, time.UTC)
	first, ok := p.Next(now)
	if !ok || first.ID != "a-1" {
		t.Fatalf("first dispatch = %+v ok=%v, want a-1", first, ok)
	}
	second, ok := p.Next(now)
	if !ok || second.ID != "b-1" {
		t.Fatalf("second dispatch = %+v ok=%v, want b-1 because host-1 is gated", second, ok)
	}
	if third, ok := p.Next(now); ok {
		t.Fatalf("no further task may be dispatched, got %+v", third)
	}
}

func TestNextKeepsSubmissionOrder(t *testing.T) {
	p := busyPlanner(t)
	for _, task := range []impl.Task{
		{ID: "c-1", Group: "batch-night", Target: "host-1"},
		{ID: "c-2", Group: "batch-night", Target: "host-2"},
		{ID: "c-3", Group: "batch-night", Target: "host-3"},
	} {
		if _, err := p.Submit(task); err != nil {
			t.Fatalf("submit %s: %v", task.ID, err)
		}
	}
	now := time.Date(2026, time.November, 16, 10, 0, 0, 0, time.UTC)
	for _, want := range []string{"c-1", "c-2", "c-3"} {
		task, ok := p.Next(now)
		if !ok || task.ID != want {
			t.Fatalf("dispatch = %+v ok=%v, want %s", task, ok, want)
		}
	}
}

func TestReportDoneReleasesSlotAndPending(t *testing.T) {
	p := busyPlanner(t)
	for _, task := range []impl.Task{
		{ID: "a-1", Group: "batch-night", Target: "host-1"},
		{ID: "a-2", Group: "batch-night", Target: "host-1"},
	} {
		if _, err := p.Submit(task); err != nil {
			t.Fatalf("submit %s: %v", task.ID, err)
		}
	}
	now := time.Date(2026, time.November, 16, 10, 0, 0, 0, time.UTC)
	if task, ok := p.Next(now); !ok || task.ID != "a-1" {
		t.Fatalf("first dispatch = %+v ok=%v, want a-1", task, ok)
	}
	if got := p.Pending("batch-night"); got != 2 {
		t.Fatalf("pending while a-1 runs = %d, want 2", got)
	}
	if err := p.Report("a-1", impl.Outcome{Kind: impl.OutcomeDone, Progress: 100, At: now}); err != nil {
		t.Fatalf("report done: %v", err)
	}
	if got := p.Pending("batch-night"); got != 1 {
		t.Fatalf("pending after done = %d, want 1", got)
	}
	if task, ok := p.Next(now); !ok || task.ID != "a-2" {
		t.Fatalf("released gate must dispatch a-2, got %+v ok=%v", task, ok)
	}
}

func TestThrottleBackoffPreservesQueuePosition(t *testing.T) {
	p := busyPlanner(t)
	for _, task := range []impl.Task{
		{ID: "q-1", Group: "batch-night", Target: "host-1"},
		{ID: "q-2", Group: "batch-night", Target: "host-2"},
		{ID: "q-3", Group: "batch-night", Target: "host-3"},
	} {
		if _, err := p.Submit(task); err != nil {
			t.Fatalf("submit %s: %v", task.ID, err)
		}
	}
	t0 := time.Date(2026, time.November, 16, 10, 0, 0, 0, time.UTC)
	if task, ok := p.Next(t0); !ok || task.ID != "q-1" {
		t.Fatalf("first dispatch = %+v ok=%v, want q-1", task, ok)
	}
	if err := p.Report("q-1", impl.Outcome{Kind: impl.OutcomeThrottled, RetryAfter: 30 * time.Second, Progress: 40, At: t0}); err != nil {
		t.Fatalf("report throttled: %v", err)
	}
	if task, ok := p.Next(t0.Add(10 * time.Second)); !ok || task.ID != "q-2" {
		t.Fatalf("task in backoff must be skipped, got %+v ok=%v, want q-2", task, ok)
	}
	if err := p.Report("q-2", impl.Outcome{Kind: impl.OutcomeDone, Progress: 100, At: t0.Add(11 * time.Second)}); err != nil {
		t.Fatalf("report done: %v", err)
	}
	resumed, ok := p.Next(t0.Add(40 * time.Second))
	if !ok || resumed.ID != "q-1" {
		t.Fatalf("after backoff the original head must come first, got %+v ok=%v", resumed, ok)
	}
	if resumed.Progress != 40 {
		t.Fatalf("resumed progress = %d, want the reported 40", resumed.Progress)
	}
	if task, ok := p.Next(t0.Add(41 * time.Second)); !ok || task.ID != "q-3" {
		t.Fatalf("tail task must follow, got %+v ok=%v, want q-3", task, ok)
	}
}

func TestThrottleLimitParksTask(t *testing.T) {
	p := impl.NewScanWindowPlanner(impl.Limits{PerTargetConcurrency: 1, MaxThrottles: 1, MaxBackoff: time.Minute})
	mustDeclare(t, p, "batch-night", allDayWindow())
	for _, task := range []impl.Task{
		{ID: "p-1", Group: "batch-night", Target: "host-1"},
		{ID: "p-2", Group: "batch-night", Target: "host-1"},
	} {
		if _, err := p.Submit(task); err != nil {
			t.Fatalf("submit %s: %v", task.ID, err)
		}
	}
	t0 := time.Date(2026, time.November, 16, 10, 0, 0, 0, time.UTC)
	if task, ok := p.Next(t0); !ok || task.ID != "p-1" {
		t.Fatalf("first dispatch = %+v ok=%v, want p-1", task, ok)
	}
	if err := p.Report("p-1", impl.Outcome{Kind: impl.OutcomeThrottled, RetryAfter: 5 * time.Second, At: t0}); err != nil {
		t.Fatalf("first throttle: %v", err)
	}
	if task, ok := p.Next(t0.Add(10 * time.Second)); !ok || task.ID != "p-1" {
		t.Fatalf("second dispatch = %+v ok=%v, want p-1 again", task, ok)
	}
	if err := p.Report("p-1", impl.Outcome{Kind: impl.OutcomeThrottled, RetryAfter: 5 * time.Second, At: t0.Add(10 * time.Second)}); err != nil {
		t.Fatalf("second throttle: %v", err)
	}
	state, err := p.State("p-1")
	if err != nil {
		t.Fatalf("state of parked task: %v", err)
	}
	if state.State != impl.StateParked {
		t.Fatalf("task state = %q, want %q", state.State, impl.StateParked)
	}
	if state.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", state.Attempts)
	}
	if got := p.Pending("batch-night"); got != 1 {
		t.Fatalf("parked task must not be pending, got %d", got)
	}
	if task, ok := p.Next(t0.Add(20 * time.Second)); !ok || task.ID != "p-2" {
		t.Fatalf("parked task must release the gate, got %+v ok=%v, want p-2", task, ok)
	}
}

func TestReportRejectsUnknownTask(t *testing.T) {
	p := busyPlanner(t)
	now := time.Date(2026, time.November, 16, 10, 0, 0, 0, time.UTC)
	if err := p.Report("ghost", impl.Outcome{Kind: impl.OutcomeDone, At: now}); !errors.Is(err, impl.ErrUnknownTask) {
		t.Fatalf("report of an unknown task must report ErrUnknownTask, got %v", err)
	}
}

func TestReportRejectsUnknownOutcomeKind(t *testing.T) {
	p := busyPlanner(t)
	now := time.Date(2026, time.November, 16, 10, 0, 0, 0, time.UTC)
	if _, err := p.Submit(impl.Task{ID: "o-1", Group: "batch-night", Target: "host-1"}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if err := p.Report("o-1", impl.Outcome{Kind: "vibes", At: now}); !errors.Is(err, impl.ErrBadOutcome) {
		t.Fatalf("unknown outcome kind must report ErrBadOutcome, got %v", err)
	}
}

func TestSnapshotRestoreKeepsPendingAndOrder(t *testing.T) {
	p := busyPlanner(t)
	for _, task := range []impl.Task{
		{ID: "s-1", Group: "batch-night", Target: "host-1"},
		{ID: "s-2", Group: "batch-night", Target: "host-2"},
	} {
		if _, err := p.Submit(task); err != nil {
			t.Fatalf("submit %s: %v", task.ID, err)
		}
	}
	data, err := p.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	fresh := impl.NewScanWindowPlanner(impl.Limits{PerTargetConcurrency: 1, MaxThrottles: 2, MaxBackoff: time.Minute})
	if err := fresh.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := fresh.Pending("batch-night"); got != 2 {
		t.Fatalf("restored pending = %d, want 2", got)
	}
	now := time.Date(2026, time.November, 16, 10, 0, 0, 0, time.UTC)
	if task, ok := fresh.Next(now); !ok || task.ID != "s-1" {
		t.Fatalf("restored queue must dispatch s-1 first, got %+v ok=%v", task, ok)
	}
	if err := fresh.Restore([]byte("broken")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must report ErrBadSnapshot, got %v", err)
	}
	if got := fresh.Pending("batch-night"); got != 2 {
		t.Fatalf("rejected restore must not disturb live state, pending = %d", got)
	}
}
