package task

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	impl "gsb/batch49-go-safety-c-04"
)

func linearHosts() []impl.Host {
	return []impl.Host{
		{ID: "h-a2", Service: "svc-a", Window: impl.Window{StartMinute: 0, EndMinute: 1439}, BudgetMinutes: 60},
		{ID: "h-a1", Service: "svc-a", Window: impl.Window{StartMinute: 540, EndMinute: 1020}, BudgetMinutes: 30},
		{ID: "h-b1", Service: "svc-b", Window: impl.Window{StartMinute: 0, EndMinute: 1439}, BudgetMinutes: 60},
		{ID: "h-c1", Service: "svc-c", Window: impl.Window{StartMinute: 0, EndMinute: 1439}, BudgetMinutes: 60},
	}
}

func linearDeps() []impl.DepEdge {
	return []impl.DepEdge{
		{Service: "svc-b", DependsOn: "svc-a"},
		{Service: "svc-c", DependsOn: "svc-b"},
	}
}

func plannedRollout(t *testing.T, policy impl.RolloutPolicy) *impl.PatchRollout {
	t.Helper()
	p := impl.NewPatchRollout(policy)
	if _, err := p.Plan(linearHosts(), linearDeps()); err != nil {
		t.Fatalf("plan: %v", err)
	}
	return p
}

func reportAllOK(t *testing.T, p *impl.PatchRollout, hosts ...string) {
	t.Helper()
	for _, hostID := range hosts {
		if err := p.ReportHost(hostID, impl.HostResult{OK: true, DurationMinutes: 5}); err != nil {
			t.Fatalf("report %s: %v", hostID, err)
		}
	}
}

func TestPlanRejectsInvalidInput(t *testing.T) {
	p := impl.NewPatchRollout(impl.RolloutPolicy{})
	if _, err := p.Plan(nil, nil); !errors.Is(err, impl.ErrEmptyPlan) {
		t.Fatalf("empty host list must fail with ErrEmptyPlan, got %v", err)
	}
	duplicate := []impl.Host{
		{ID: "h-1", Service: "svc-a", Window: impl.Window{StartMinute: 0, EndMinute: 10}},
		{ID: "h-1", Service: "svc-b", Window: impl.Window{StartMinute: 0, EndMinute: 10}},
	}
	if _, err := p.Plan(duplicate, nil); !errors.Is(err, impl.ErrBadPlan) {
		t.Fatalf("duplicate host id must fail with ErrBadPlan, got %v", err)
	}
	unknownDep := []impl.Host{{ID: "h-1", Service: "svc-a", Window: impl.Window{StartMinute: 0, EndMinute: 10}}}
	if _, err := p.Plan(unknownDep, []impl.DepEdge{{Service: "svc-a", DependsOn: "svc-z"}}); !errors.Is(err, impl.ErrBadPlan) {
		t.Fatalf("unknown dependency service must fail with ErrBadPlan, got %v", err)
	}
	noService := []impl.Host{{ID: "h-1", Window: impl.Window{StartMinute: 0, EndMinute: 10}}}
	if _, err := p.Plan(noService, nil); !errors.Is(err, impl.ErrBadPlan) {
		t.Fatalf("host without a service must fail with ErrBadPlan, got %v", err)
	}
	if got := p.Batches(); len(got) != 0 {
		t.Fatalf("rejected plans must not be stored, got %+v", got)
	}
}

func TestPlanLayersServicesByDependency(t *testing.T) {
	p := plannedRollout(t, impl.RolloutPolicy{})
	batches := p.Batches()
	if len(batches) != 3 {
		t.Fatalf("batches = %d, want 3 dependency layers", len(batches))
	}
	if batches[0].ID != "batch-1" || batches[1].ID != "batch-2" || batches[2].ID != "batch-3" {
		t.Fatalf("batch ids = %q,%q,%q", batches[0].ID, batches[1].ID, batches[2].ID)
	}
	if len(batches[0].Services) != 1 || batches[0].Services[0] != "svc-a" {
		t.Fatalf("first batch services = %v, want [svc-a]", batches[0].Services)
	}
	if len(batches[0].Hosts) != 2 || batches[0].Hosts[0] != "h-a1" || batches[0].Hosts[1] != "h-a2" {
		t.Fatalf("first batch hosts = %v, want [h-a1 h-a2]", batches[0].Hosts)
	}
	if len(batches[1].DependsOn) != 1 || batches[1].DependsOn[0] != "svc-a" {
		t.Fatalf("second batch deps = %v, want [svc-a]", batches[1].DependsOn)
	}
	if len(batches[2].DependsOn) != 1 || batches[2].DependsOn[0] != "svc-b" {
		t.Fatalf("third batch deps = %v, want [svc-b]", batches[2].DependsOn)
	}
	batch, ok := p.Batch("batch-2")
	if !ok || batch.ID != "batch-2" || len(batch.Hosts) != 1 || batch.Hosts[0] != "h-b1" {
		t.Fatalf("batch-2 = %+v ok=%v, want the svc-b layer", batch, ok)
	}
	if _, ok := p.Batch("batch-9"); ok {
		t.Fatalf("unknown batch id must not resolve")
	}
}

func TestStartRequiresPlanAndKnownBatch(t *testing.T) {
	p := impl.NewPatchRollout(impl.RolloutPolicy{})
	if err := p.Start("batch-1"); !errors.Is(err, impl.ErrNoPlan) {
		t.Fatalf("start without a plan must fail with ErrNoPlan, got %v", err)
	}
	p = plannedRollout(t, impl.RolloutPolicy{})
	if err := p.Start("batch-9"); !errors.Is(err, impl.ErrUnknownBatch) {
		t.Fatalf("unknown batch must fail with ErrUnknownBatch, got %v", err)
	}
	if err := p.Start(""); !errors.Is(err, impl.ErrUnknownBatch) {
		t.Fatalf("empty batch id must fail with ErrUnknownBatch, got %v", err)
	}
}

func TestStartBlockedByPendingDeps(t *testing.T) {
	p := plannedRollout(t, impl.RolloutPolicy{})
	if err := p.Start("batch-2"); !errors.Is(err, impl.ErrDepsPending) {
		t.Fatalf("starting a dependent batch early must fail with ErrDepsPending, got %v", err)
	}
	if err := p.Start("batch-1"); err != nil {
		t.Fatalf("start first batch: %v", err)
	}
	if err := p.Start("batch-1"); !errors.Is(err, impl.ErrBatchStarted) {
		t.Fatalf("starting twice must fail with ErrBatchStarted, got %v", err)
	}
	if err := p.Start("batch-2"); !errors.Is(err, impl.ErrDepsPending) {
		t.Fatalf("dependent batch must stay blocked until the first gate passes, got %v", err)
	}
	reportAllOK(t, p, "h-a1", "h-a2")
	gate, err := p.Gate("batch-1")
	if err != nil {
		t.Fatalf("gate batch-1: %v", err)
	}
	if !gate.Passed {
		t.Fatalf("gate batch-1 = %+v, want passed", gate)
	}
	if err := p.Start("batch-2"); err != nil {
		t.Fatalf("start batch-2 after the gate: %v", err)
	}
}

func TestReportRequiresActiveBatchAndKnownHost(t *testing.T) {
	p := plannedRollout(t, impl.RolloutPolicy{})
	if err := p.ReportHost("h-a1", impl.HostResult{OK: true}); !errors.Is(err, impl.ErrBatchNotActive) {
		t.Fatalf("report before start must fail with ErrBatchNotActive, got %v", err)
	}
	if err := p.Start("batch-1"); err != nil {
		t.Fatalf("start batch-1: %v", err)
	}
	if err := p.ReportHost("ghost", impl.HostResult{OK: true}); !errors.Is(err, impl.ErrUnknownHost) {
		t.Fatalf("unknown host must fail with ErrUnknownHost, got %v", err)
	}
	if err := p.ReportHost("h-a1", impl.HostResult{OK: true, DurationMinutes: 5}); err != nil {
		t.Fatalf("report h-a1: %v", err)
	}
	if status, ok := p.HostStatus("h-a1"); !ok || status != impl.HostOK {
		t.Fatalf("h-a1 status = %q ok=%v, want %q", status, ok, impl.HostOK)
	}
	if _, ok := p.HostStatus("ghost"); ok {
		t.Fatalf("unknown host must not have a status")
	}
}

func TestReportTwiceRejected(t *testing.T) {
	p := plannedRollout(t, impl.RolloutPolicy{})
	if err := p.Start("batch-1"); err != nil {
		t.Fatalf("start batch-1: %v", err)
	}
	if err := p.ReportHost("h-a1", impl.HostResult{OK: true}); err != nil {
		t.Fatalf("first report: %v", err)
	}
	if err := p.ReportHost("h-a1", impl.HostResult{OK: false}); !errors.Is(err, impl.ErrHostReported) {
		t.Fatalf("second report must fail with ErrHostReported, got %v", err)
	}
	if status, _ := p.HostStatus("h-a1"); status != impl.HostOK {
		t.Fatalf("rejected second report must not change the status, got %q", status)
	}
}

func TestReportEnforcesDowntimeBudget(t *testing.T) {
	p := plannedRollout(t, impl.RolloutPolicy{})
	if err := p.Start("batch-1"); err != nil {
		t.Fatalf("start batch-1: %v", err)
	}
	if err := p.ReportHost("h-a1", impl.HostResult{OK: true, DurationMinutes: 31}); !errors.Is(err, impl.ErrOverBudget) {
		t.Fatalf("downtime over the budget must fail with ErrOverBudget, got %v", err)
	}
	if status, _ := p.HostStatus("h-a1"); status != impl.HostActive {
		t.Fatalf("rejected report must leave the host active, got %q", status)
	}
	if err := p.ReportHost("h-a1", impl.HostResult{OK: true, DurationMinutes: 30}); err != nil {
		t.Fatalf("report within the budget: %v", err)
	}
}

func TestGateWaitsForCanary(t *testing.T) {
	hosts := []impl.Host{
		{ID: "h-c1", Service: "svc-a", Canary: true, Window: impl.Window{StartMinute: 0, EndMinute: 1439}, BudgetMinutes: 60},
		{ID: "h-a1", Service: "svc-a", Window: impl.Window{StartMinute: 0, EndMinute: 1439}, BudgetMinutes: 60},
	}
	p := impl.NewPatchRollout(impl.RolloutPolicy{MaxCanaryFailureRate: 0.5})
	if _, err := p.Plan(hosts, nil); err != nil {
		t.Fatalf("plan: %v", err)
	}
	if err := p.Start("batch-1"); err != nil {
		t.Fatalf("start batch-1: %v", err)
	}
	gate, err := p.Gate("batch-1")
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	if gate.Passed || gate.Reason != impl.GateWaiting || gate.WaitingCanary != 1 {
		t.Fatalf("gate = %+v, want waiting on one canary", gate)
	}
	if err := p.ReportHost("h-a1", impl.HostResult{OK: true}); !errors.Is(err, impl.ErrCanaryPending) {
		t.Fatalf("non canary report before the canary must fail with ErrCanaryPending, got %v", err)
	}
	reportAllOK(t, p, "h-c1")
	gate, err = p.Gate("batch-1")
	if err != nil {
		t.Fatalf("gate after canary: %v", err)
	}
	if gate.Passed || gate.Reason != impl.GateIncomplete {
		t.Fatalf("gate = %+v, want waiting on the remaining hosts", gate)
	}
	reportAllOK(t, p, "h-a1")
	gate, err = p.Gate("batch-1")
	if err != nil {
		t.Fatalf("gate after all hosts: %v", err)
	}
	if !gate.Passed || gate.Reason != impl.GatePassed {
		t.Fatalf("gate = %+v, want passed", gate)
	}
}

func TestGatePassesBatchOnSuccess(t *testing.T) {
	p := plannedRollout(t, impl.RolloutPolicy{})
	if err := p.Start("batch-1"); err != nil {
		t.Fatalf("start batch-1: %v", err)
	}
	reportAllOK(t, p, "h-a1", "h-a2")
	gate, err := p.Gate("batch-1")
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	if !gate.Passed || gate.Reported != 2 || gate.Total != 2 {
		t.Fatalf("gate = %+v, want 2 of 2 reported and passed", gate)
	}
	again, err := p.Gate("batch-1")
	if err != nil {
		t.Fatalf("second gate: %v", err)
	}
	if !again.Passed {
		t.Fatalf("second gate = %+v, want the finished batch to stay passed", again)
	}
	if err := p.Start("batch-2"); err != nil {
		t.Fatalf("start batch-2 after the pass: %v", err)
	}
}

func TestGateBlocksOnCanaryFailure(t *testing.T) {
	hosts := []impl.Host{
		{ID: "h-c1", Service: "svc-a", Canary: true, Window: impl.Window{StartMinute: 0, EndMinute: 1439}, BudgetMinutes: 60},
		{ID: "h-b1", Service: "svc-b", Window: impl.Window{StartMinute: 0, EndMinute: 1439}, BudgetMinutes: 60},
	}
	deps := []impl.DepEdge{{Service: "svc-b", DependsOn: "svc-a"}}
	p := impl.NewPatchRollout(impl.RolloutPolicy{MaxCanaryFailureRate: 0})
	if _, err := p.Plan(hosts, deps); err != nil {
		t.Fatalf("plan: %v", err)
	}
	if err := p.Start("batch-1"); err != nil {
		t.Fatalf("start batch-1: %v", err)
	}
	if err := p.ReportHost("h-c1", impl.HostResult{OK: false, DurationMinutes: 3}); err != nil {
		t.Fatalf("report failing canary: %v", err)
	}
	gate, err := p.Gate("batch-1")
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	if gate.Passed || gate.Reason != impl.GateFailed {
		t.Fatalf("gate = %+v, want a failed canary gate", gate)
	}
	if len(gate.Quarantined) != 1 || gate.Quarantined[0] != "h-c1" {
		t.Fatalf("gate quarantine = %v, want [h-c1]", gate.Quarantined)
	}
	if status, _ := p.HostStatus("h-c1"); status != impl.HostQuarantined {
		t.Fatalf("failed canary status = %q, want %q", status, impl.HostQuarantined)
	}
	if got := p.Quarantined(); len(got) != 1 || got[0] != "h-c1" {
		t.Fatalf("quarantine = %v, want [h-c1]", got)
	}
	if err := p.Start("batch-2"); !errors.Is(err, impl.ErrDepsPending) {
		t.Fatalf("blocked batch must keep its dependents pending, got %v", err)
	}
}

func TestAbortRequiresReasonAndClosesBatch(t *testing.T) {
	p := plannedRollout(t, impl.RolloutPolicy{})
	if err := p.Abort("batch-1", "   "); !errors.Is(err, impl.ErrBadReason) {
		t.Fatalf("blank abort reason must fail with ErrBadReason, got %v", err)
	}
	if err := p.Abort("batch-1", "rollback window"); err != nil {
		t.Fatalf("abort batch-1: %v", err)
	}
	if err := p.Start("batch-1"); !errors.Is(err, impl.ErrBatchAborted) {
		t.Fatalf("starting an aborted batch must fail with ErrBatchAborted, got %v", err)
	}
	if err := p.ReportHost("h-a1", impl.HostResult{OK: true}); !errors.Is(err, impl.ErrBatchAborted) {
		t.Fatalf("reporting into an aborted batch must fail with ErrBatchAborted, got %v", err)
	}
	if err := p.Abort("batch-9", "nope"); !errors.Is(err, impl.ErrUnknownBatch) {
		t.Fatalf("aborting an unknown batch must fail with ErrUnknownBatch, got %v", err)
	}
}

func TestBeginHostEnforcesWindow(t *testing.T) {
	p := plannedRollout(t, impl.RolloutPolicy{})
	if err := p.Start("batch-1"); err != nil {
		t.Fatalf("start batch-1: %v", err)
	}
	if err := p.BeginHost("h-a1", 539); !errors.Is(err, impl.ErrOutsideWindow) {
		t.Fatalf("minute before the window must fail with ErrOutsideWindow, got %v", err)
	}
	if err := p.BeginHost("h-a1", 1020); !errors.Is(err, impl.ErrOutsideWindow) {
		t.Fatalf("the exclusive window end must fail with ErrOutsideWindow, got %v", err)
	}
	if err := p.BeginHost("h-a1", 540); err != nil {
		t.Fatalf("window start must be accepted: %v", err)
	}
	if err := p.BeginHost("h-a1", 1019); err != nil {
		t.Fatalf("minute inside the window must be accepted: %v", err)
	}
	if err := p.BeginHost("ghost", 600); !errors.Is(err, impl.ErrUnknownHost) {
		t.Fatalf("unknown host must fail with ErrUnknownHost, got %v", err)
	}
}

func TestConcurrentReportsAcceptOne(t *testing.T) {
	hosts := []impl.Host{{ID: "h-a1", Service: "svc-a", Window: impl.Window{StartMinute: 0, EndMinute: 1439}, BudgetMinutes: 60}}
	p := impl.NewPatchRollout(impl.RolloutPolicy{})
	if _, err := p.Plan(hosts, nil); err != nil {
		t.Fatalf("plan: %v", err)
	}
	if err := p.Start("batch-1"); err != nil {
		t.Fatalf("start batch-1: %v", err)
	}
	const workers = 12
	var wg sync.WaitGroup
	errs := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = p.ReportHost("h-a1", impl.HostResult{OK: true, DurationMinutes: 2, Note: fmt.Sprintf("worker-%d", i)})
		}(i)
	}
	wg.Wait()
	accepted, rejected := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, impl.ErrHostReported):
			rejected++
		default:
			t.Fatalf("unexpected report error: %v", err)
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted reports = %d, want exactly 1", accepted)
	}
	if rejected != workers-1 {
		t.Fatalf("rejected reports = %d, want %d", rejected, workers-1)
	}
	if status, _ := p.HostStatus("h-a1"); status != impl.HostOK {
		t.Fatalf("host status after the race = %q, want %q", status, impl.HostOK)
	}
}

func TestSnapshotRestoreKeepsProgress(t *testing.T) {
	p := plannedRollout(t, impl.RolloutPolicy{})
	if err := p.Start("batch-1"); err != nil {
		t.Fatalf("start batch-1: %v", err)
	}
	reportAllOK(t, p, "h-a1", "h-a2")
	if gate, err := p.Gate("batch-1"); err != nil || !gate.Passed {
		t.Fatalf("gate batch-1 = %+v (%v), want passed", gate, err)
	}
	data, err := p.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewPatchRollout(impl.RolloutPolicy{MaxCanaryFailureRate: 0})
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.Batches(); len(got) != 3 {
		t.Fatalf("restored batches = %d, want 3", len(got))
	}
	if status, ok := restored.HostStatus("h-a1"); !ok || status != impl.HostOK {
		t.Fatalf("restored host status = %q ok=%v, want %q", status, ok, impl.HostOK)
	}
	if err := restored.Start("batch-2"); err != nil {
		t.Fatalf("start batch-2 after restore: %v", err)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	p := plannedRollout(t, impl.RolloutPolicy{})
	if err := p.Start("batch-1"); err != nil {
		t.Fatalf("start batch-1: %v", err)
	}
	reportAllOK(t, p, "h-a1", "h-a2")
	if gate, err := p.Gate("batch-1"); err != nil || !gate.Passed {
		t.Fatalf("gate batch-1 = %+v (%v), want passed", gate, err)
	}
	if err := p.Restore([]byte("batch-plan-{broken")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := p.Batches(); len(got) != 3 {
		t.Fatalf("batches after rejected restore = %d, want 3", len(got))
	}
	if status, _ := p.HostStatus("h-a2"); status != impl.HostOK {
		t.Fatalf("host status after rejected restore = %q, want %q", status, impl.HostOK)
	}
	if err := p.Start("batch-2"); err != nil {
		t.Fatalf("progress must survive a rejected restore, got %v", err)
	}
}
