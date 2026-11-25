package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch49-go-safety-a-26"
)

func leakLimits() impl.Limits {
	return impl.Limits{MaxIncidents: 8, MaxReplacements: 4}
}

func TestReportCreatesSuspectedIncident(t *testing.T) {
	r := impl.NewApiKeyLeakResponder(leakLimits())
	inc, err := r.Report("key-1", "pastebin", impl.ConfidenceHigh, "paste-9911")
	if err != nil {
		t.Fatalf("report must succeed, got %v", err)
	}
	if inc.ID == "" {
		t.Fatalf("incident id must not be empty")
	}
	if inc.KeyID != "key-1" {
		t.Fatalf("incident key = %q, want key-1", inc.KeyID)
	}
	if inc.State != impl.IncidentSuspected {
		t.Fatalf("incident state = %q, want %q", inc.State, impl.IncidentSuspected)
	}
	if inc.Confidence != impl.ConfidenceHigh {
		t.Fatalf("incident confidence = %q, want %q", inc.Confidence, impl.ConfidenceHigh)
	}
	if inc.Source != "pastebin" {
		t.Fatalf("incident source = %q, want pastebin", inc.Source)
	}
	if inc.Seq <= 0 {
		t.Fatalf("incident seq = %d, want a positive number", inc.Seq)
	}
	if len(inc.Evidence) != 1 || inc.Evidence[0] != "paste-9911" {
		t.Fatalf("incident evidence = %v, want the reported evidence", inc.Evidence)
	}
	state, err := r.IncidentState(inc.ID)
	if err != nil {
		t.Fatalf("incident state: %v", err)
	}
	if state != impl.IncidentSuspected {
		t.Fatalf("read back state = %q, want %q", state, impl.IncidentSuspected)
	}
	incs := r.Incidents("key-1")
	if len(incs) != 1 || incs[0].ID != inc.ID {
		t.Fatalf("incidents = %+v, want exactly the reported incident", incs)
	}
}

func TestReportRejectsBlankKeyOrBadConfidence(t *testing.T) {
	r := impl.NewApiKeyLeakResponder(leakLimits())
	if _, err := r.Report("   ", "pastebin", impl.ConfidenceHigh, "e-1"); !errors.Is(err, impl.ErrBlankKeyID) {
		t.Fatalf("blank key id must fail with ErrBlankKeyID, got %v", err)
	}
	if _, err := r.Report("key-1", "  ", impl.ConfidenceHigh, "e-1"); !errors.Is(err, impl.ErrBlankKeyID) {
		t.Fatalf("blank source must fail with ErrBlankKeyID, got %v", err)
	}
	if _, err := r.Report("key-1", "pastebin", impl.Confidence("certain"), "e-1"); !errors.Is(err, impl.ErrBadConfidence) {
		t.Fatalf("unknown confidence must fail with ErrBadConfidence, got %v", err)
	}
	if got := len(r.Incidents("key-1")); got != 0 {
		t.Fatalf("rejected reports must not create incidents, got %d", got)
	}
}

func TestTriageLowConfidenceMonitors(t *testing.T) {
	r := impl.NewApiKeyLeakResponder(leakLimits())
	inc, err := r.Report("key-low", "internal-log", impl.ConfidenceLow, "scan-31")
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	act, err := r.Triage(inc.ID)
	if err != nil {
		t.Fatalf("triage: %v", err)
	}
	if act.Kind != impl.ActionMonitor {
		t.Fatalf("triage kind = %q, want %q", act.Kind, impl.ActionMonitor)
	}
	if act.NotifyOwner {
		t.Fatalf("low confidence must not notify the key owner")
	}
	if act.IncidentID != inc.ID {
		t.Fatalf("triage incident = %q, want %q", act.IncidentID, inc.ID)
	}
	state, err := r.IncidentState(inc.ID)
	if err != nil {
		t.Fatalf("incident state: %v", err)
	}
	if state != impl.IncidentObserving {
		t.Fatalf("state after low triage = %q, want %q", state, impl.IncidentObserving)
	}
}

func TestTriageEscalationByConfidence(t *testing.T) {
	r := impl.NewApiKeyLeakResponder(leakLimits())
	med, err := r.Report("key-med", "ticket", impl.ConfidenceMedium, "ticket-77")
	if err != nil {
		t.Fatalf("report medium: %v", err)
	}
	actMed, err := r.Triage(med.ID)
	if err != nil {
		t.Fatalf("triage medium: %v", err)
	}
	if actMed.Kind != impl.ActionEscalate || !actMed.NotifyOwner {
		t.Fatalf("medium triage = %+v, want escalate with owner notification", actMed)
	}
	medState, err := r.IncidentState(med.ID)
	if err != nil {
		t.Fatalf("medium state: %v", err)
	}
	if medState != impl.IncidentObserving {
		t.Fatalf("medium state = %q, want %q", medState, impl.IncidentObserving)
	}

	high, err := r.Report("key-high", "pastebin", impl.ConfidenceHigh, "paste-88")
	if err != nil {
		t.Fatalf("report high: %v", err)
	}
	actHigh, err := r.Triage(high.ID)
	if err != nil {
		t.Fatalf("triage high: %v", err)
	}
	if actHigh.Kind != impl.ActionShadowRevoke || !actHigh.NotifyOwner {
		t.Fatalf("high triage = %+v, want shadow revoke with owner notification", actHigh)
	}
	highState, err := r.IncidentState(high.ID)
	if err != nil {
		t.Fatalf("high state: %v", err)
	}
	if highState != impl.IncidentShadowRevoked {
		t.Fatalf("high state = %q, want %q", highState, impl.IncidentShadowRevoked)
	}
}

func TestTriageUnknownIncident(t *testing.T) {
	r := impl.NewApiKeyLeakResponder(leakLimits())
	if _, err := r.Triage("inc-404"); !errors.Is(err, impl.ErrUnknownIncident) {
		t.Fatalf("unknown incident must fail with ErrUnknownIncident, got %v", err)
	}
	if _, err := r.Triage(""); !errors.Is(err, impl.ErrUnknownIncident) {
		t.Fatalf("empty incident id must fail with ErrUnknownIncident, got %v", err)
	}
}

func TestShadowRevokeIdempotentAndStateChecked(t *testing.T) {
	r := impl.NewApiKeyLeakResponder(leakLimits())
	inc, err := r.Report("key-shadow", "pastebin", impl.ConfidenceHigh, "paste-12")
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if err := r.ShadowRevoke(inc.ID); err != nil {
		t.Fatalf("first shadow revoke: %v", err)
	}
	if err := r.ShadowRevoke(inc.ID); err != nil {
		t.Fatalf("repeated shadow revoke must be idempotent, got %v", err)
	}
	state, err := r.IncidentState(inc.ID)
	if err != nil {
		t.Fatalf("incident state: %v", err)
	}
	if state != impl.IncidentShadowRevoked {
		t.Fatalf("state = %q, want %q", state, impl.IncidentShadowRevoked)
	}
	if err := r.HardRevoke(inc.ID, "operator-1"); err != nil {
		t.Fatalf("hard revoke after shadow: %v", err)
	}
	if err := r.ShadowRevoke(inc.ID); !errors.Is(err, impl.ErrInvalidState) {
		t.Fatalf("shadow revoke on a hard revoked incident must fail with ErrInvalidState, got %v", err)
	}
	if _, err := r.Triage(inc.ID); !errors.Is(err, impl.ErrInvalidState) {
		t.Fatalf("triage of a closed incident must fail with ErrInvalidState, got %v", err)
	}
}

func TestHardRevokeGates(t *testing.T) {
	r := impl.NewApiKeyLeakResponder(leakLimits())
	inc, err := r.Report("key-gate", "pastebin", impl.ConfidenceHigh, "paste-13")
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if err := r.HardRevoke(inc.ID, "   "); !errors.Is(err, impl.ErrBlankActor) {
		t.Fatalf("hard revoke without an operator must fail with ErrBlankActor, got %v", err)
	}
	if err := r.HardRevoke(inc.ID, "operator-1"); !errors.Is(err, impl.ErrInvalidState) {
		t.Fatalf("hard revoke from the suspected state must fail with ErrInvalidState, got %v", err)
	}
	state, err := r.IncidentState(inc.ID)
	if err != nil {
		t.Fatalf("incident state: %v", err)
	}
	if state != impl.IncidentSuspected {
		t.Fatalf("state after rejected hard revokes = %q, want %q", state, impl.IncidentSuspected)
	}
	if _, err := r.Triage("missing"); !errors.Is(err, impl.ErrUnknownIncident) {
		t.Fatalf("triage of a missing incident must fail with ErrUnknownIncident, got %v", err)
	}
}

func TestHardRevokeAfterShadowTransitions(t *testing.T) {
	r := impl.NewApiKeyLeakResponder(leakLimits())
	inc, err := r.Report("key-hard", "vendor-report", impl.ConfidenceHigh, "vendor-5")
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if err := r.ShadowRevoke(inc.ID); err != nil {
		t.Fatalf("shadow revoke: %v", err)
	}
	if err := r.HardRevoke(inc.ID, "operator-2"); err != nil {
		t.Fatalf("hard revoke: %v", err)
	}
	state, err := r.IncidentState(inc.ID)
	if err != nil {
		t.Fatalf("incident state: %v", err)
	}
	if state != impl.IncidentHardRevoked {
		t.Fatalf("state = %q, want %q", state, impl.IncidentHardRevoked)
	}
	incs := r.Incidents("key-hard")
	if len(incs) != 1 {
		t.Fatalf("incidents = %d, want 1", len(incs))
	}
	if incs[0].Actor != "operator-2" {
		t.Fatalf("incident actor = %q, want operator-2", incs[0].Actor)
	}
}

func TestRestoreRequiresRevokedState(t *testing.T) {
	r := impl.NewApiKeyLeakResponder(leakLimits())
	inc, err := r.Report("key-restore", "false-alarm", impl.ConfidenceHigh, "alert-3")
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if err := r.Restore(inc.ID, "operator-3"); !errors.Is(err, impl.ErrInvalidState) {
		t.Fatalf("restore of a suspected incident must fail with ErrInvalidState, got %v", err)
	}
	if err := r.ShadowRevoke(inc.ID); err != nil {
		t.Fatalf("shadow revoke: %v", err)
	}
	if err := r.HardRevoke(inc.ID, "operator-2"); err != nil {
		t.Fatalf("hard revoke: %v", err)
	}
	if err := r.Restore(inc.ID, ""); !errors.Is(err, impl.ErrBlankActor) {
		t.Fatalf("restore without an operator must fail with ErrBlankActor, got %v", err)
	}
	if err := r.Restore(inc.ID, "operator-3"); err != nil {
		t.Fatalf("restore: %v", err)
	}
	state, err := r.IncidentState(inc.ID)
	if err != nil {
		t.Fatalf("incident state: %v", err)
	}
	if state != impl.IncidentRestored {
		t.Fatalf("state = %q, want %q", state, impl.IncidentRestored)
	}
	incs := r.Incidents("key-restore")
	if len(incs) != 1 || incs[0].Actor != "operator-3" {
		t.Fatalf("incident after restore = %+v, want actor operator-3", incs)
	}
	if got := len(incs[0].Evidence); got != 1 {
		t.Fatalf("restore must keep the evidence trail, got %d entries", got)
	}
}

func TestPrepareReplacementInactive(t *testing.T) {
	r := impl.NewApiKeyLeakResponder(leakLimits())
	inc, err := r.Report("key-repl", "pastebin", impl.ConfidenceHigh, "paste-21")
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if _, err := r.PrepareReplacement(inc.ID); !errors.Is(err, impl.ErrInvalidState) {
		t.Fatalf("replacement before a revoke must fail with ErrInvalidState, got %v", err)
	}
	if err := r.ShadowRevoke(inc.ID); err != nil {
		t.Fatalf("shadow revoke: %v", err)
	}
	key, err := r.PrepareReplacement(inc.ID)
	if err != nil {
		t.Fatalf("prepare replacement: %v", err)
	}
	if key.ID == "" {
		t.Fatalf("replacement id must not be empty")
	}
	if key.Active {
		t.Fatalf("replacement key must be provisioned inactive")
	}
	if key.KeyID != "key-repl" || key.IncidentID != inc.ID {
		t.Fatalf("replacement targets key=%q incident=%q, want key-repl/%q", key.KeyID, key.IncidentID, inc.ID)
	}
	if key.Seq <= 0 {
		t.Fatalf("replacement seq = %d, want a positive number", key.Seq)
	}
	reps := r.Replacements("key-repl")
	if len(reps) != 1 || reps[0].ID != key.ID {
		t.Fatalf("replacements = %+v, want exactly the issued key", reps)
	}
}

func TestPrepareReplacementConcurrentSingleIdempotent(t *testing.T) {
	r := impl.NewApiKeyLeakResponder(leakLimits())
	inc, err := r.Report("key-race", "pastebin", impl.ConfidenceHigh, "paste-22")
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if err := r.ShadowRevoke(inc.ID); err != nil {
		t.Fatalf("shadow revoke: %v", err)
	}
	const workers = 8
	ids := make([]string, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key, err := r.PrepareReplacement(inc.ID)
			ids[i], errs[i] = key.ID, err
		}(i)
	}
	wg.Wait()
	for i := 0; i < workers; i++ {
		if errs[i] != nil {
			t.Fatalf("concurrent prepare %d failed: %v", i, errs[i])
		}
		if ids[i] != ids[0] {
			t.Fatalf("concurrent prepare returned different keys: %v", ids)
		}
	}
	if got := len(r.Replacements("key-race")); got != 1 {
		t.Fatalf("replacements after the race = %d, want 1", got)
	}
}

func TestIncidentCapEnforced(t *testing.T) {
	r := impl.NewApiKeyLeakResponder(impl.Limits{MaxIncidents: 1, MaxReplacements: 4})
	if _, err := r.Report("key-cap-a", "pastebin", impl.ConfidenceHigh, "e-1"); err != nil {
		t.Fatalf("first report: %v", err)
	}
	if _, err := r.Report("key-cap-b", "pastebin", impl.ConfidenceHigh, "e-2"); !errors.Is(err, impl.ErrIncidentCap) {
		t.Fatalf("second incident must fail with ErrIncidentCap, got %v", err)
	}
	if got := len(r.Incidents("key-cap-b")); got != 0 {
		t.Fatalf("rejected incident leaked into the table, got %d records", got)
	}
	if got := len(r.Incidents("key-cap-a")); got != 1 {
		t.Fatalf("first incident count = %d, want 1", got)
	}
}

func TestReplacementCapEnforced(t *testing.T) {
	r := impl.NewApiKeyLeakResponder(impl.Limits{MaxIncidents: 8, MaxReplacements: 1})
	first, err := r.Report("key-first", "pastebin", impl.ConfidenceHigh, "e-1")
	if err != nil {
		t.Fatalf("first report: %v", err)
	}
	if err := r.ShadowRevoke(first.ID); err != nil {
		t.Fatalf("shadow revoke first: %v", err)
	}
	if _, err := r.PrepareReplacement(first.ID); err != nil {
		t.Fatalf("first replacement: %v", err)
	}
	second, err := r.Report("key-second", "pastebin", impl.ConfidenceHigh, "e-2")
	if err != nil {
		t.Fatalf("second report: %v", err)
	}
	if err := r.ShadowRevoke(second.ID); err != nil {
		t.Fatalf("shadow revoke second: %v", err)
	}
	if _, err := r.PrepareReplacement(second.ID); !errors.Is(err, impl.ErrReplacementCap) {
		t.Fatalf("second replacement must fail with ErrReplacementCap, got %v", err)
	}
	if got := len(r.Replacements("key-second")); got != 0 {
		t.Fatalf("rejected replacement leaked into the table, got %d records", got)
	}
}

func TestSnapshotRestoreKeepsStates(t *testing.T) {
	limits := impl.Limits{MaxIncidents: 8, MaxReplacements: 4}
	r := impl.NewApiKeyLeakResponder(limits)
	inc, err := r.Report("key-snap", "ticket", impl.ConfidenceHigh, "ticket-9")
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if err := r.ShadowRevoke(inc.ID); err != nil {
		t.Fatalf("shadow revoke: %v", err)
	}
	key, err := r.PrepareReplacement(inc.ID)
	if err != nil {
		t.Fatalf("prepare replacement: %v", err)
	}
	data, err := r.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewApiKeyLeakResponder(limits)
	if err := restored.LoadSnapshot(data); err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	state, err := restored.IncidentState(inc.ID)
	if err != nil {
		t.Fatalf("restored incident state: %v", err)
	}
	if state != impl.IncidentShadowRevoked {
		t.Fatalf("restored state = %q, want %q", state, impl.IncidentShadowRevoked)
	}
	reps := restored.Replacements("key-snap")
	if len(reps) != 1 || reps[0].ID != key.ID || reps[0].Active {
		t.Fatalf("restored replacements = %+v, want the inactive key %q", reps, key.ID)
	}
	next, err := restored.Report("key-snap-2", "ticket", impl.ConfidenceMedium, "ticket-10")
	if err != nil {
		t.Fatalf("report after restore: %v", err)
	}
	if next.Seq <= inc.Seq {
		t.Fatalf("seq after restore = %d, must continue past %d", next.Seq, inc.Seq)
	}
	if next.Seq <= key.Seq {
		t.Fatalf("seq after restore = %d, must continue past the replacement seq %d", next.Seq, key.Seq)
	}
}

func TestLoadSnapshotRejectsCorrupt(t *testing.T) {
	r := impl.NewApiKeyLeakResponder(leakLimits())
	inc, err := r.Report("key-corrupt", "pastebin", impl.ConfidenceHigh, "e-1")
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if err := r.ShadowRevoke(inc.ID); err != nil {
		t.Fatalf("shadow revoke: %v", err)
	}
	if err := r.LoadSnapshot([]byte("{not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	state, err := r.IncidentState(inc.ID)
	if err != nil {
		t.Fatalf("incident state after a rejected snapshot: %v", err)
	}
	if state != impl.IncidentShadowRevoked {
		t.Fatalf("state after a rejected snapshot = %q, want %q", state, impl.IncidentShadowRevoked)
	}
	if _, err := r.Report("key-corrupt-2", "ticket", impl.ConfidenceLow, "e-2"); err != nil {
		t.Fatalf("live state must keep working after a rejected snapshot, got %v", err)
	}
}
