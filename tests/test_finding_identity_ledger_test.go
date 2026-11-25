package task

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	impl "gsb/batch49-go-safety-c-10"
)

func rawA() impl.RawFinding {
	return impl.RawFinding{
		Source:      "semgrep",
		RuleID:      "R-100",
		Path:        "pkg/handler.go",
		Line:        100,
		Fingerprint: "fp-alpha",
		Message:     "unchecked error",
		Severity:    "high",
	}
}

func rawB() impl.RawFinding {
	return impl.RawFinding{
		Source:      "semgrep",
		RuleID:      "R-200",
		Path:        "pkg/store.go",
		Line:        20,
		Fingerprint: "fp-beta",
		Message:     "weak cipher",
		Severity:    "medium",
	}
}

func newLedger(t *testing.T) *impl.FindingIdentityLedger {
	t.Helper()
	l := impl.NewFindingIdentityLedger(impl.Config{LineDrift: 20, FingerprintDrift: 2})
	if l == nil {
		t.Fatalf("NewFindingIdentityLedger returned nil")
	}
	return l
}

func findingWithOrigin(list []impl.Finding, origin string) (impl.Finding, bool) {
	for _, f := range list {
		for _, o := range f.Origins {
			if o == origin {
				return f, true
			}
		}
	}
	return impl.Finding{}, false
}

func eventsFor(events []impl.Event, findingID string) []impl.Event {
	out := make([]impl.Event, 0, len(events))
	for _, e := range events {
		if e.FindingID == findingID {
			out = append(out, e)
		}
	}
	return out
}

func TestIdentityJoinsRulePathLineFingerprint(t *testing.T) {
	l := newLedger(t)
	raw := rawA()
	want := "R-100|pkg/handler.go|100|fp-alpha"
	if got := l.Identity(raw); got != want {
		t.Fatalf("Identity = %q, want %q", got, want)
	}
	otherSource := raw
	otherSource.Source = "codeql"
	if got := l.Identity(otherSource); got != want {
		t.Fatalf("identity must ignore the scanner source, got %q", got)
	}
	drifted := raw
	drifted.Line = 101
	if got := l.Identity(drifted); got == want {
		t.Fatalf("a different line must change the raw identity")
	}
	otherFP := raw
	otherFP.Fingerprint = "fp-gamma"
	if got := l.Identity(otherFP); got == want {
		t.Fatalf("a different fingerprint must change the raw identity")
	}
	otherRule := raw
	otherRule.RuleID = "R-999"
	if got := l.Identity(otherRule); got == want {
		t.Fatalf("a different rule must change the raw identity")
	}
}

func TestIngestCreatesNewFindings(t *testing.T) {
	l := newLedger(t)
	events, err := l.Ingest("scan-1", []impl.RawFinding{rawA(), rawB()})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %+v, want one per raw finding", events)
	}
	for i, e := range events {
		if e.Kind != impl.KindNew {
			t.Fatalf("event %d kind = %q, want %q", i, e.Kind, impl.KindNew)
		}
		if e.ScanID != "scan-1" {
			t.Fatalf("event %d scan = %q, want scan-1", i, e.ScanID)
		}
	}
	if events[0].Seq >= events[1].Seq {
		t.Fatalf("event sequences must strictly increase, got %d then %d", events[0].Seq, events[1].Seq)
	}
	findings := l.Findings()
	if len(findings) != 2 {
		t.Fatalf("findings = %+v, want two entries", findings)
	}
	seen := map[string]struct{}{}
	for _, f := range findings {
		if !strings.HasPrefix(f.ID, "fi-") || len(f.ID) != 11 {
			t.Fatalf("finding id = %q, want fi- plus eight hex digits", f.ID)
		}
		seen[f.ID] = struct{}{}
		if f.State != impl.StateNew {
			t.Fatalf("new finding state = %q, want %q", f.State, impl.StateNew)
		}
		if f.Seen != 1 || f.FirstScan != "scan-1" {
			t.Fatalf("finding = %+v, want one sighting in scan-1", f)
		}
	}
	if len(seen) != 2 {
		t.Fatalf("finding ids are not unique: %v", seen)
	}
	if stats := l.Stats(); stats.New != 2 {
		t.Fatalf("stats = %+v, want two new findings", stats)
	}
	if got := l.Rounds(); got != 1 {
		t.Fatalf("round counter = %d, want 1", got)
	}
}

func TestIngestValidatesAtomically(t *testing.T) {
	l := newLedger(t)
	if _, err := l.Ingest("  ", []impl.RawFinding{rawA()}); !errors.Is(err, impl.ErrBlankScanID) {
		t.Fatalf("blank scan id must fail with ErrBlankScanID, got %v", err)
	}
	bad := []impl.RawFinding{rawA(), rawB()}
	bad[0].Source = ""
	bad[1].Line = 0
	if _, err := l.Ingest("scan-1", bad); !errors.Is(err, impl.ErrBadRaw) {
		t.Fatalf("a batch with an invalid row must fail with ErrBadRaw, got %v", err)
	}
	cases := []impl.RawFinding{rawA(), rawA(), rawA()}
	cases[0].Path = ""
	cases[1].Fingerprint = "   "
	cases[2].RuleID = ""
	for i, raw := range cases {
		if _, err := l.Ingest("scan-1", []impl.RawFinding{raw}); !errors.Is(err, impl.ErrBadRaw) {
			t.Fatalf("invalid raw %d must fail with ErrBadRaw, got %v", i, err)
		}
	}
	if got := len(l.Findings()); got != 0 {
		t.Fatalf("rejected batches stored %d findings, want 0", got)
	}
	if got := len(l.Events()); got != 0 {
		t.Fatalf("rejected batches stored %d events, want 0", got)
	}
	if got := l.Rounds(); got != 0 {
		t.Fatalf("rejected batches advanced the round counter to %d", got)
	}
}

func TestIngestRepeatedScanKeepsIdentity(t *testing.T) {
	l := newLedger(t)
	if _, err := l.Ingest("scan-1", []impl.RawFinding{rawA()}); err != nil {
		t.Fatalf("first scan: %v", err)
	}
	first := l.Findings()[0]
	if _, err := l.Ingest("scan-2", []impl.RawFinding{rawA()}); err != nil {
		t.Fatalf("second scan: %v", err)
	}
	findings := l.Findings()
	if len(findings) != 1 || findings[0].ID != first.ID {
		t.Fatalf("findings = %+v, want the original id %q", findings, first.ID)
	}
	if findings[0].State != impl.StateOpen {
		t.Fatalf("state after a repeated sighting = %q, want %q", findings[0].State, impl.StateOpen)
	}
	if findings[0].Seen != 2 || findings[0].LastScan != "scan-2" {
		t.Fatalf("finding = %+v, want two sightings and last scan scan-2", findings[0])
	}
	events := eventsFor(l.Events(), first.ID)
	if len(events) != 2 || events[1].Kind != impl.KindSeen {
		t.Fatalf("events = %+v, want new then seen", events)
	}
	if stats := l.Stats(); stats.Open != 1 {
		t.Fatalf("stats = %+v, want one open finding", stats)
	}
}

func TestIngestMarksAbsentFindingsFixed(t *testing.T) {
	l := newLedger(t)
	if _, err := l.Ingest("scan-1", []impl.RawFinding{rawA(), rawB()}); err != nil {
		t.Fatalf("first scan: %v", err)
	}
	var idA, idB string
	for _, f := range l.Findings() {
		switch f.RuleID {
		case "R-100":
			idA = f.ID
		case "R-200":
			idB = f.ID
		}
	}
	if idA == "" || idB == "" {
		t.Fatalf("first scan did not create both findings: %+v", l.Findings())
	}
	if _, err := l.Ingest("scan-2", []impl.RawFinding{rawA()}); err != nil {
		t.Fatalf("second scan: %v", err)
	}
	fixed, ok := l.Get(idB)
	if !ok {
		t.Fatalf("fixed finding must stay readable")
	}
	if fixed.State != impl.StateFixed {
		t.Fatalf("absent finding state = %q, want %q", fixed.State, impl.StateFixed)
	}
	open, _ := l.Get(idA)
	if open.State != impl.StateOpen {
		t.Fatalf("reported finding state = %q, want %q", open.State, impl.StateOpen)
	}
	events := eventsFor(l.Events(), idB)
	last := events[len(events)-1]
	if last.Kind != impl.KindFixed || last.ScanID != "scan-2" {
		t.Fatalf("last event for the absent finding = %+v, want a fixed event from scan-2", last)
	}
	if stats := l.Stats(); stats.Open != 1 || stats.Fixed != 1 {
		t.Fatalf("stats = %+v, want one open and one fixed finding", stats)
	}
}

func TestIngestRebindsLineDrift(t *testing.T) {
	l := newLedger(t)
	if _, err := l.Ingest("scan-1", []impl.RawFinding{rawA()}); err != nil {
		t.Fatalf("first scan: %v", err)
	}
	original := l.Findings()[0]
	drifted := rawA()
	drifted.Line = 108
	if _, err := l.Ingest("scan-2", []impl.RawFinding{drifted}); err != nil {
		t.Fatalf("second scan: %v", err)
	}
	findings := l.Findings()
	if len(findings) != 1 {
		t.Fatalf("findings = %+v, want the drifted line rebound to one entry", findings)
	}
	if findings[0].ID != original.ID {
		t.Fatalf("line drift created a new id %q, want %q", findings[0].ID, original.ID)
	}
	if findings[0].Line != 108 {
		t.Fatalf("line after rebinding = %d, want 108", findings[0].Line)
	}
	if findings[0].Seen != 2 {
		t.Fatalf("sightings after rebinding = %d, want 2", findings[0].Seen)
	}
}

func TestIngestBeyondLineToleranceCreatesNewFinding(t *testing.T) {
	l := newLedger(t)
	if _, err := l.Ingest("scan-1", []impl.RawFinding{rawA()}); err != nil {
		t.Fatalf("first scan: %v", err)
	}
	original := l.Findings()[0]
	far := rawA()
	far.Line = 300
	if _, err := l.Ingest("scan-2", []impl.RawFinding{far}); err != nil {
		t.Fatalf("second scan: %v", err)
	}
	findings := l.Findings()
	if len(findings) != 2 {
		t.Fatalf("findings = %+v, want a new entry for the distant line", findings)
	}
	old, ok := l.Get(original.ID)
	if !ok || old.State != impl.StateFixed {
		t.Fatalf("previous finding = %+v ok=%v, want fixed", old, ok)
	}
	var fresh impl.Finding
	for _, f := range findings {
		if f.ID != original.ID {
			fresh = f
		}
	}
	if fresh.ID == "" || fresh.State != impl.StateNew || fresh.Line != 300 {
		t.Fatalf("fresh finding = %+v, want a new entry at line 300", fresh)
	}
}

func TestResolveReturnsMatchOrNoMatch(t *testing.T) {
	l := newLedger(t)
	if _, err := l.Resolve(impl.RawFinding{}); !errors.Is(err, impl.ErrBadRaw) {
		t.Fatalf("resolving an invalid raw must fail with ErrBadRaw, got %v", err)
	}
	if _, err := l.Resolve(rawA()); !errors.Is(err, impl.ErrNoMatch) {
		t.Fatalf("resolving before any scan must fail with ErrNoMatch, got %v", err)
	}
	if _, err := l.Ingest("scan-1", []impl.RawFinding{rawA()}); err != nil {
		t.Fatalf("scan: %v", err)
	}
	stored := l.Findings()[0]
	match, err := l.Resolve(rawA())
	if err != nil {
		t.Fatalf("resolve exact: %v", err)
	}
	if match.ID != stored.ID {
		t.Fatalf("resolved id = %q, want %q", match.ID, stored.ID)
	}
	stranger := rawA()
	stranger.Fingerprint = "fp-completely-different"
	stranger.Line = 500
	if _, err := l.Resolve(stranger); !errors.Is(err, impl.ErrNoMatch) {
		t.Fatalf("resolving an unrelated raw must fail with ErrNoMatch, got %v", err)
	}
}

func TestIgnoreSuppressesFutureEvents(t *testing.T) {
	l := newLedger(t)
	if _, err := l.Ingest("scan-1", []impl.RawFinding{rawA()}); err != nil {
		t.Fatalf("first scan: %v", err)
	}
	id := l.Findings()[0].ID
	if err := l.Ignore(id); err != nil {
		t.Fatalf("ignore: %v", err)
	}
	item, _ := l.Get(id)
	if item.State != impl.StateIgnored {
		t.Fatalf("state after ignore = %q, want %q", item.State, impl.StateIgnored)
	}
	if _, err := l.Ingest("scan-2", []impl.RawFinding{rawB()}); err != nil {
		t.Fatalf("second scan: %v", err)
	}
	if kept, _ := l.Get(id); kept.State != impl.StateIgnored {
		t.Fatalf("ignored finding became %q after an absent round", kept.State)
	}
	if events := eventsFor(l.Events(), id); len(events) != 2 || events[1].Kind != impl.KindIgnored {
		t.Fatalf("ignored events = %+v, want new then ignored", events)
	}
	if _, err := l.Ingest("scan-3", []impl.RawFinding{rawA()}); err != nil {
		t.Fatalf("third scan: %v", err)
	}
	if item, _ := l.Get(id); item.State != impl.StateIgnored || item.Seen != 1 {
		t.Fatalf("ignored finding = %+v, want an untouched ignored entry", item)
	}
	if events := eventsFor(l.Events(), id); len(events) != 2 {
		t.Fatalf("ignored finding gained events: %+v", events)
	}
	if stats := l.Stats(); stats.Ignored != 1 {
		t.Fatalf("stats = %+v, want one ignored finding", stats)
	}
}

func TestIgnoreUnknownFindingFails(t *testing.T) {
	l := newLedger(t)
	if err := l.Ignore("fi-0000dead"); !errors.Is(err, impl.ErrNotFound) {
		t.Fatalf("ignoring an unknown finding must fail with ErrNotFound, got %v", err)
	}
	if _, err := l.Ingest("scan-1", []impl.RawFinding{rawA()}); err != nil {
		t.Fatalf("scan: %v", err)
	}
	id := l.Findings()[0].ID
	if err := l.Ignore(id); err != nil {
		t.Fatalf("ignore: %v", err)
	}
	if err := l.Ignore(id); err != nil {
		t.Fatalf("repeated ignore must be idempotent, got %v", err)
	}
	if got := len(eventsFor(l.Events(), id)); got != 2 {
		t.Fatalf("repeated ignore wrote %d events, want 2", got)
	}
}

func TestSplitPeelsSelectedOrigins(t *testing.T) {
	l := newLedger(t)
	other := rawA()
	other.Source = "codeql"
	if _, err := l.Ingest("scan-1", []impl.RawFinding{rawA(), other}); err != nil {
		t.Fatalf("scan: %v", err)
	}
	findings := l.Findings()
	if len(findings) != 1 {
		t.Fatalf("findings = %+v, want the two sources merged into one entry", findings)
	}
	merged := findings[0]
	if len(merged.Origins) != 2 || merged.Origins[0] != "codeql" || merged.Origins[1] != "semgrep" {
		t.Fatalf("origins = %v, want [codeql semgrep]", merged.Origins)
	}
	if err := l.Split(merged.ID, []string{"codeql"}); err != nil {
		t.Fatalf("split: %v", err)
	}
	after := l.Findings()
	if len(after) != 2 {
		t.Fatalf("findings after split = %+v, want two entries", after)
	}
	original, ok := l.Get(merged.ID)
	if !ok {
		t.Fatalf("original finding must stay readable after the split")
	}
	if len(original.Origins) != 1 || original.Origins[0] != "semgrep" {
		t.Fatalf("original origins = %v, want [semgrep]", original.Origins)
	}
	peeled, ok := findingWithOrigin(after, "codeql")
	if !ok {
		t.Fatalf("split did not keep the codeql origin on any finding")
	}
	if peeled.ID == merged.ID {
		t.Fatalf("split instance reused the original id %q", merged.ID)
	}
	if !strings.HasPrefix(peeled.ID, merged.ID+"-") {
		t.Fatalf("split instance id = %q, want a suffix of %q", peeled.ID, merged.ID)
	}
	if err := l.Split(merged.ID, []string{"semgrep"}); !errors.Is(err, impl.ErrBadSplit) {
		t.Fatalf("splitting every remaining origin must fail with ErrBadSplit, got %v", err)
	}
}

func TestSplitValidation(t *testing.T) {
	l := newLedger(t)
	other := rawA()
	other.Source = "codeql"
	if _, err := l.Ingest("scan-1", []impl.RawFinding{rawA(), other}); err != nil {
		t.Fatalf("scan: %v", err)
	}
	id := l.Findings()[0].ID
	if err := l.Split("fi-0000dead", []string{"codeql"}); !errors.Is(err, impl.ErrNotFound) {
		t.Fatalf("splitting an unknown finding must fail with ErrNotFound, got %v", err)
	}
	if err := l.Split(id, nil); !errors.Is(err, impl.ErrBadSplit) {
		t.Fatalf("splitting with an empty keep set must fail with ErrBadSplit, got %v", err)
	}
	if err := l.Split(id, []string{"nope"}); !errors.Is(err, impl.ErrBadSplit) {
		t.Fatalf("splitting an unknown origin must fail with ErrBadSplit, got %v", err)
	}
	if err := l.Split(id, []string{"semgrep", "codeql"}); !errors.Is(err, impl.ErrBadSplit) {
		t.Fatalf("splitting every origin must fail with ErrBadSplit, got %v", err)
	}
	if got := len(l.Findings()); got != 1 {
		t.Fatalf("failed splits changed the finding count to %d", got)
	}
}

func TestSnapshotRestoreKeepsStatesAndSequence(t *testing.T) {
	l := newLedger(t)
	if _, err := l.Ingest("scan-1", []impl.RawFinding{rawA(), rawB()}); err != nil {
		t.Fatalf("first scan: %v", err)
	}
	var idA, idB string
	for _, f := range l.Findings() {
		if f.RuleID == "R-100" {
			idA = f.ID
		} else {
			idB = f.ID
		}
	}
	if err := l.Ignore(idB); err != nil {
		t.Fatalf("ignore: %v", err)
	}
	lastSeq := l.Events()[len(l.Events())-1].Seq
	data, err := l.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := newLedger(t)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.Rounds(); got != 1 {
		t.Fatalf("rounds after restore = %d, want 1", got)
	}
	findings := restored.Findings()
	if len(findings) != 2 {
		t.Fatalf("restored findings = %+v, want two entries", findings)
	}
	if item, _ := restored.Get(idA); item.State != impl.StateOpen {
		t.Fatalf("restored open finding state = %q", item.State)
	}
	if item, _ := restored.Get(idB); item.State != impl.StateIgnored {
		t.Fatalf("restored ignored finding state = %q", item.State)
	}
	if _, err := restored.Ingest("scan-2", []impl.RawFinding{rawA()}); err != nil {
		t.Fatalf("scan after restore: %v", err)
	}
	if item, _ := restored.Get(idA); item.Seen != 2 {
		t.Fatalf("finding after restore = %+v, want two sightings", item)
	}
	events := restored.Events()
	if events[len(events)-1].Seq <= lastSeq {
		t.Fatalf("event sequence after restore = %d, must continue past %d", events[len(events)-1].Seq, lastSeq)
	}
	if got := len(restored.Findings()); got != 2 {
		t.Fatalf("restored ledger duplicated findings, got %d", got)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	l := newLedger(t)
	if _, err := l.Ingest("scan-1", []impl.RawFinding{rawA(), rawB()}); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if err := l.Restore([]byte("[]not-json")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if err := l.Restore(nil); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("empty snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := len(l.Findings()); got != 2 {
		t.Fatalf("failed restore changed the finding count to %d", got)
	}
	if got := len(l.Events()); got != 2 {
		t.Fatalf("failed restore changed the audit log to %d events", got)
	}
	if got := l.Rounds(); got != 1 {
		t.Fatalf("failed restore changed the round counter to %d", got)
	}
	if _, err := l.Ingest("scan-2", []impl.RawFinding{rawA()}); err != nil {
		t.Fatalf("ingest after rejected restore: %v", err)
	}
}

func TestConcurrentIngestKeepsOneFindingPerRaw(t *testing.T) {
	l := newLedger(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	failures := 0
	ids := make(map[string]struct{})
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			events, err := l.Ingest(fmt.Sprintf("scan-%d", i), []impl.RawFinding{rawA(), rawB()})
			mu.Lock()
			defer mu.Unlock()
			if err != nil || len(events) != 2 {
				failures++
				return
			}
			ids[events[0].FindingID] = struct{}{}
			ids[events[1].FindingID] = struct{}{}
		}(i)
	}
	wg.Wait()
	if failures != 0 {
		t.Fatalf("%d concurrent ingests failed to report two events", failures)
	}
	if len(ids) != 2 {
		t.Fatalf("concurrent ingests produced %d distinct finding ids, want 2", len(ids))
	}
	findings := l.Findings()
	if len(findings) != 2 {
		t.Fatalf("findings = %+v, want two entries", findings)
	}
	for _, f := range findings {
		if f.Seen != 6 {
			t.Fatalf("finding %q seen %d times, want 6", f.ID, f.Seen)
		}
	}
	if got := l.Rounds(); got != 6 {
		t.Fatalf("round counter = %d, want 6", got)
	}
}
