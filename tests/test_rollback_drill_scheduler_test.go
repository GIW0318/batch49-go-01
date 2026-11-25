package task

import (
	"errors"
	"testing"
	"time"

	impl "gsb/batch49-go-safety-e-09"
)

func day(n int) time.Time {
	return time.Date(2026, 6, 8, 7, 0, 0, 0, time.UTC).Add(time.Duration(n) * 24 * time.Hour)
}

func schedulerAt(t time.Time) *impl.RollbackDrillScheduler {
	return impl.NewRollbackDrillScheduler(impl.Options{Now: func() time.Time { return t }, DrillBudget: time.Minute})
}

func batchSpec(id string) impl.BatchSpec {
	return impl.BatchSpec{ID: id, Steps: []string{"stop-service", "restore-files", "start-service"}, Images: []string{"img-1"}}
}

func fleetWithHosts(t *testing.T) *impl.RollbackDrillScheduler {
	t.Helper()
	s := schedulerAt(day(1))
	if err := s.RegisterBatch(batchSpec("batch-a")); err != nil {
		t.Fatalf("register batch-a: %v", err)
	}
	if err := s.RegisterBatch(batchSpec("batch-b")); err != nil {
		t.Fatalf("register batch-b: %v", err)
	}
	for _, host := range []string{"host-1", "host-2", "host-3", "host-4"} {
		if err := s.Enroll(host, "fleet-1"); err != nil {
			t.Fatalf("enroll %s: %v", host, err)
		}
	}
	assignments := []struct {
		host  string
		batch string
	}{
		{"host-1", "batch-a"},
		{"host-2", "batch-a"},
		{"host-3", "batch-b"},
		{"host-4", "batch-b"},
	}
	for _, a := range assignments {
		if err := s.Assign(a.host, a.batch, "img-1"); err != nil {
			t.Fatalf("assign %s: %v", a.host, err)
		}
	}
	return s
}

func TestEnrollRejectsBlank(t *testing.T) {
	s := schedulerAt(day(1))
	if err := s.Enroll("", "fleet-1"); !errors.Is(err, impl.ErrBlankField) {
		t.Fatalf("blank host must fail with ErrBlankField, got %v", err)
	}
	if err := s.Enroll("host-1", "   "); !errors.Is(err, impl.ErrBlankField) {
		t.Fatalf("blank fleet must fail with ErrBlankField, got %v", err)
	}
	if err := s.Enroll("host-1", "fleet-1"); err != nil {
		t.Fatalf("enroll host: %v", err)
	}
}

func TestEnrollRejectsDuplicate(t *testing.T) {
	s := schedulerAt(day(1))
	if err := s.Enroll("host-1", "fleet-1"); err != nil {
		t.Fatalf("enroll host: %v", err)
	}
	if err := s.Enroll("host-1", "fleet-2"); !errors.Is(err, impl.ErrDuplicateHost) {
		t.Fatalf("duplicate host must fail with ErrDuplicateHost, got %v", err)
	}
	if got := len(s.Hosts("fleet-1")); got != 1 {
		t.Fatalf("fleet-1 host count = %d, want 1", got)
	}
}

func TestRegisterBatchRejectsBlank(t *testing.T) {
	s := schedulerAt(day(1))
	if err := s.RegisterBatch(impl.BatchSpec{ID: "", Steps: []string{"a"}, Images: []string{"img-1"}}); !errors.Is(err, impl.ErrBlankField) {
		t.Fatalf("blank batch id must fail with ErrBlankField, got %v", err)
	}
	if err := s.RegisterBatch(impl.BatchSpec{ID: "batch-1", Images: []string{"img-1"}}); !errors.Is(err, impl.ErrBlankField) {
		t.Fatalf("a batch without steps must fail with ErrBlankField, got %v", err)
	}
	if err := s.RegisterBatch(impl.BatchSpec{ID: "batch-1", Steps: []string{"a"}}); !errors.Is(err, impl.ErrBlankField) {
		t.Fatalf("a batch without images must fail with ErrBlankField, got %v", err)
	}
	if err := s.RegisterBatch(impl.BatchSpec{ID: "batch-1", Steps: []string{"a", " "}, Images: []string{"img-1"}}); !errors.Is(err, impl.ErrBlankField) {
		t.Fatalf("blank step must fail with ErrBlankField, got %v", err)
	}
	if err := s.RegisterBatch(impl.BatchSpec{ID: "batch-1", Steps: []string{"a"}, Images: []string{""}}); !errors.Is(err, impl.ErrBlankField) {
		t.Fatalf("blank image must fail with ErrBlankField, got %v", err)
	}
}

func TestRegisterBatchRejectsDuplicate(t *testing.T) {
	s := schedulerAt(day(1))
	batch := batchSpec("batch-1")
	if err := s.RegisterBatch(batch); err != nil {
		t.Fatalf("register batch: %v", err)
	}
	if err := s.RegisterBatch(batch); !errors.Is(err, impl.ErrDuplicateBatch) {
		t.Fatalf("duplicate batch must fail with ErrDuplicateBatch, got %v", err)
	}
}

func TestAssignRejectsUnknownHost(t *testing.T) {
	s := schedulerAt(day(1))
	if err := s.RegisterBatch(batchSpec("batch-1")); err != nil {
		t.Fatalf("register batch: %v", err)
	}
	if err := s.Assign("ghost", "batch-1", "img-1"); !errors.Is(err, impl.ErrUnknownHost) {
		t.Fatalf("assigning an unknown host must fail with ErrUnknownHost, got %v", err)
	}
}

func TestAssignRejectsUnknownImage(t *testing.T) {
	s := schedulerAt(day(1))
	if err := s.Enroll("host-1", "fleet-1"); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if err := s.RegisterBatch(batchSpec("batch-1")); err != nil {
		t.Fatalf("register batch: %v", err)
	}
	if err := s.Assign("host-1", "batch-1", "img-9"); !errors.Is(err, impl.ErrUnknownImage) {
		t.Fatalf("assigning an image outside the batch must fail with ErrUnknownImage, got %v", err)
	}
}

func TestSampleRequiresKnownFleet(t *testing.T) {
	s := schedulerAt(day(1))
	if _, err := s.Sample("ghost-fleet", 2); !errors.Is(err, impl.ErrUnknownFleet) {
		t.Fatalf("unknown fleet must fail with ErrUnknownFleet, got %v", err)
	}
}

func TestSampleStratifiesAndRotates(t *testing.T) {
	s := fleetWithHosts(t)
	first, err := s.Sample("fleet-1", 2)
	if err != nil {
		t.Fatalf("first sample: %v", err)
	}
	if len(first) != 2 || first[0] != "host-1" || first[1] != "host-3" {
		t.Fatalf("first sample = %v, want one host from each batch [host-1 host-3]", first)
	}
	second, err := s.Sample("fleet-1", 2)
	if err != nil {
		t.Fatalf("second sample: %v", err)
	}
	if len(second) != 2 || second[0] != "host-2" || second[1] != "host-4" {
		t.Fatalf("second sample = %v, want the remaining hosts [host-2 host-4]", second)
	}
	third, err := s.Sample("fleet-1", 2)
	if err != nil {
		t.Fatalf("third sample: %v", err)
	}
	if len(third) != 2 || third[0] != "host-1" || third[1] != "host-3" {
		t.Fatalf("third sample = %v, want a fresh cycle starting again", third)
	}
}

func TestRecordSimulationUnknownHost(t *testing.T) {
	s := schedulerAt(day(1))
	if err := s.RegisterBatch(batchSpec("batch-1")); err != nil {
		t.Fatalf("register batch: %v", err)
	}
	if err := s.RecordSimulation("ghost", "batch-1", []string{"a"}, time.Second); !errors.Is(err, impl.ErrUnknownHost) {
		t.Fatalf("simulation on an unknown host must fail with ErrUnknownHost, got %v", err)
	}
}

func TestDrillWithoutSimulation(t *testing.T) {
	s := schedulerAt(day(1))
	if err := s.Enroll("host-1", "fleet-1"); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if err := s.RegisterBatch(batchSpec("batch-1")); err != nil {
		t.Fatalf("register batch: %v", err)
	}
	if err := s.Assign("host-1", "batch-1", "img-1"); err != nil {
		t.Fatalf("assign: %v", err)
	}
	if _, err := s.Drill("host-1", "batch-1"); !errors.Is(err, impl.ErrNoSimulation) {
		t.Fatalf("drill without a simulation must fail with ErrNoSimulation, got %v", err)
	}
}

func TestDrillMissingStepsReportsMissing(t *testing.T) {
	s := schedulerAt(day(1))
	if err := s.Enroll("host-1", "fleet-1"); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if err := s.RegisterBatch(batchSpec("batch-1")); err != nil {
		t.Fatalf("register batch: %v", err)
	}
	if err := s.Assign("host-1", "batch-1", "img-1"); err != nil {
		t.Fatalf("assign: %v", err)
	}
	if err := s.RecordSimulation("host-1", "batch-1", []string{"stop-service", "restore-files"}, time.Second); err != nil {
		t.Fatalf("record simulation: %v", err)
	}
	result, err := s.Drill("host-1", "batch-1")
	if err != nil {
		t.Fatalf("drill: %v", err)
	}
	if result.Status != impl.DrillFailed {
		t.Fatalf("status = %q, want failed", result.Status)
	}
	if len(result.Mismatches) != 1 {
		t.Fatalf("mismatches = %+v, want one missing step", result.Mismatches)
	}
	if result.Mismatches[0].Kind != impl.MismatchMissing || result.Mismatches[0].Step != "start-service" {
		t.Fatalf("mismatch = %+v, want missing start-service", result.Mismatches[0])
	}
}

func TestDrillOrderMismatchDistinguished(t *testing.T) {
	s := schedulerAt(day(1))
	if err := s.Enroll("host-1", "fleet-1"); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if err := s.RegisterBatch(batchSpec("batch-1")); err != nil {
		t.Fatalf("register batch: %v", err)
	}
	if err := s.Assign("host-1", "batch-1", "img-1"); err != nil {
		t.Fatalf("assign: %v", err)
	}
	if err := s.RecordSimulation("host-1", "batch-1", []string{"stop-service", "start-service", "restore-files"}, time.Second); err != nil {
		t.Fatalf("record simulation: %v", err)
	}
	result, err := s.Drill("host-1", "batch-1")
	if err != nil {
		t.Fatalf("drill: %v", err)
	}
	if result.Status != impl.DrillFailed || len(result.Mismatches) == 0 {
		t.Fatalf("result = %+v, want a failed drill with mismatches", result)
	}
	if result.Mismatches[0].Kind != impl.MismatchOrder {
		t.Fatalf("first mismatch = %+v, want an order mismatch", result.Mismatches[0])
	}
}

func TestDrillPassedResultRecorded(t *testing.T) {
	s := schedulerAt(day(1))
	if err := s.Enroll("host-1", "fleet-1"); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if err := s.RegisterBatch(batchSpec("batch-1")); err != nil {
		t.Fatalf("register batch: %v", err)
	}
	if err := s.Assign("host-1", "batch-1", "img-1"); err != nil {
		t.Fatalf("assign: %v", err)
	}
	expected := batchSpec("batch-1").Steps
	if err := s.RecordSimulation("host-1", "batch-1", expected, 2*time.Second); err != nil {
		t.Fatalf("record simulation: %v", err)
	}
	result, err := s.Drill("host-1", "batch-1")
	if err != nil {
		t.Fatalf("drill: %v", err)
	}
	if result.Status != impl.DrillPassed || len(result.Mismatches) != 0 {
		t.Fatalf("result = %+v, want passed without mismatches", result)
	}
	if len(result.Input) != len(expected) {
		t.Fatalf("result input = %v, want the reproducible expected steps", result.Input)
	}
	coverage, err := s.Coverage("fleet-1")
	if err != nil {
		t.Fatalf("coverage: %v", err)
	}
	if coverage.DrilledHosts != 1 || coverage.Gaps != 0 || len(coverage.UncoveredBatches) != 0 {
		t.Fatalf("coverage = %+v, want the only batch covered", coverage)
	}
}

func TestCoverageCountsGaps(t *testing.T) {
	s := fleetWithHosts(t)
	if err := s.RecordSimulation("host-1", "batch-a", batchSpec("batch-a").Steps, time.Second); err != nil {
		t.Fatalf("record simulation: %v", err)
	}
	if _, err := s.Drill("host-1", "batch-a"); err != nil {
		t.Fatalf("drill: %v", err)
	}
	coverage, err := s.Coverage("fleet-1")
	if err != nil {
		t.Fatalf("coverage: %v", err)
	}
	if coverage.TotalHosts != 4 || coverage.DrilledHosts != 1 {
		t.Fatalf("coverage = %+v, want 1 of 4 hosts drilled", coverage)
	}
	if len(coverage.UncoveredBatches) != 1 || coverage.UncoveredBatches[0] != "batch-b" {
		t.Fatalf("uncovered batches = %v, want [batch-b]", coverage.UncoveredBatches)
	}
	if coverage.Gaps != 1 {
		t.Fatalf("gaps = %d, want 1 uncovered batch/image pair", coverage.Gaps)
	}
}

func TestFailuresListsFailedDrills(t *testing.T) {
	s := fleetWithHosts(t)
	if err := s.RecordSimulation("host-3", "batch-b", []string{"stop-service"}, time.Second); err != nil {
		t.Fatalf("record simulation: %v", err)
	}
	if _, err := s.Drill("host-3", "batch-b"); err != nil {
		t.Fatalf("drill: %v", err)
	}
	failures := s.Failures("fleet-1")
	if len(failures) != 1 {
		t.Fatalf("failures = %+v, want one recorded failure", failures)
	}
	if failures[0].Host != "host-3" || failures[0].Batch != "batch-b" {
		t.Fatalf("failure = %+v, want host-3 on batch-b", failures[0])
	}
	if failures[0].Category != impl.MismatchMissing {
		t.Fatalf("failure category = %q, want %q", failures[0].Category, impl.MismatchMissing)
	}
	if len(s.Failures("ghost-fleet")) != 0 {
		t.Fatalf("unknown fleet must have no failures")
	}
}
