package task

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	impl "gsb/batch49-go-safety-c-07"
)

type clock struct {
	at time.Time
}

func (c *clock) now() time.Time { return c.at }

func newDetector(t *testing.T) (*impl.ConfusionDetector, *clock) {
	t.Helper()
	clk := &clock{at: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	d := impl.NewConfusionDetector(impl.Config{
		TypoDistance: 1,
		Scope:        "prod-registry",
		Now:          clk.now,
	})
	if d == nil {
		t.Fatalf("NewConfusionDetector returned nil")
	}
	return d, clk
}

func shadowBatch() []impl.Coordinate {
	return []impl.Coordinate{
		{Name: "@acme/left-pad", Source: impl.SourceInternal, Publisher: "acme", Version: "1.4.0"},
		{Name: "leftpad", Source: impl.SourcePublic, Publisher: "evil-mirror", Version: "1.4.0"},
	}
}

func collisionByClass(list []impl.Collision, class string) (impl.Collision, bool) {
	for _, c := range list {
		if c.Class == class {
			return c, true
		}
	}
	return impl.Collision{}, false
}

func TestNormalizeStripsScopeAndFoldsSeparators(t *testing.T) {
	d, _ := newDetector(t)
	cases := map[string]string{
		"@Scope/Left-Pad": "leftpad",
		"left_pad":        "leftpad",
		"LEFT.PAD":        "leftpad",
		"  leftpad  ":     "leftpad",
		"left--pad":       "leftpad",
		"l\u0435ftpad":    "leftpad",
	}
	for in, want := range cases {
		if got := d.Normalize(in); got != want {
			t.Fatalf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClassIDStableAcrossVariants(t *testing.T) {
	d, _ := newDetector(t)
	first := d.ClassID("Left-Pad")
	second := d.ClassID("@other/left_pad")
	if first != second {
		t.Fatalf("class id differs for equivalent names: %q vs %q", first, second)
	}
	if !strings.HasPrefix(first, "cc-") {
		t.Fatalf("class id = %q, want the cc- prefix", first)
	}
	if len(first) != 11 {
		t.Fatalf("class id = %q, want cc- plus eight hex digits", first)
	}
	if other := d.ClassID("leftpads"); other == first {
		t.Fatalf("different names must map to different class ids, both got %q", first)
	}
}

func TestScanDetectsNameShadowAcrossSources(t *testing.T) {
	d, _ := newDetector(t)
	list, err := d.Scan(shadowBatch())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	collision, ok := collisionByClass(list, "leftpad")
	if !ok {
		t.Fatalf("scan returned %+v, want one collision for class leftpad", list)
	}
	if collision.Kind != impl.KindNameShadow {
		t.Fatalf("kind = %q, want %q", collision.Kind, impl.KindNameShadow)
	}
	if !collision.Blocked {
		t.Fatalf("same name from internal and public sources must be blocked")
	}
	if collision.Suspicious {
		t.Fatalf("a name shadow is a hard collision, not a suspicion")
	}
	if collision.Severity != impl.SeverityHigh {
		t.Fatalf("severity = %q, want %q", collision.Severity, impl.SeverityHigh)
	}
	if collision.FirstRound != 1 || collision.LastRound != 1 {
		t.Fatalf("rounds = %d..%d, want 1..1", collision.FirstRound, collision.LastRound)
	}
	if len(collision.Members) != 2 {
		t.Fatalf("members = %+v, want two coordinates", collision.Members)
	}
	if collision.Members[0].Source != impl.SourceInternal || collision.Members[1].Source != impl.SourcePublic {
		t.Fatalf("members = %+v, want the internal source first", collision.Members)
	}
	if collision.Members[0].Normalized != "leftpad" {
		t.Fatalf("member normalized name = %q, want leftpad", collision.Members[0].Normalized)
	}
}

func TestScanGroupsSeparatorVariantsAndDedupes(t *testing.T) {
	d, _ := newDetector(t)
	coords := []impl.Coordinate{
		{Name: "left-pad", Source: impl.SourceInternal, Publisher: "acme", Version: "1.0.0"},
		{Name: "left_pad", Source: impl.SourceInternal, Publisher: "acme", Version: "1.0.0"},
		{Name: "@acme/LEFT.PAD", Source: impl.SourceInternal, Publisher: "acme", Version: "1.0.0"},
		{Name: "leftpad", Source: impl.SourcePublic, Publisher: "evil-mirror", Version: "2.0.0"},
	}
	list, err := d.Scan(coords)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("collisions = %+v, want exactly one class", list)
	}
	if len(list[0].Members) != 2 {
		t.Fatalf("members = %+v, want the three identical internal rows folded into one", list[0].Members)
	}
	if got := len(d.Collisions()); got != 1 {
		t.Fatalf("stored collisions = %d, want 1", got)
	}
	if d.Collisions()[0].ID != list[0].ID {
		t.Fatalf("stored collision id differs from the returned id")
	}
}

func TestScanRejectsBlankCoordinate(t *testing.T) {
	d, _ := newDetector(t)
	if _, err := d.Scan([]impl.Coordinate{{Name: "  ", Source: impl.SourceInternal}}); !errors.Is(err, impl.ErrBlankCoordinate) {
		t.Fatalf("blank name must fail with ErrBlankCoordinate, got %v", err)
	}
	if _, err := d.Scan([]impl.Coordinate{{Name: "leftpad", Source: "mirror"}}); !errors.Is(err, impl.ErrBadSource) {
		t.Fatalf("unknown source must fail with ErrBadSource, got %v", err)
	}
	if got := d.Rounds(); got != 0 {
		t.Fatalf("rejected scans must not advance rounds, got %d", got)
	}
	if got := len(d.Collisions()); got != 0 {
		t.Fatalf("rejected scans must not store collisions, got %d", got)
	}
	if _, err := d.Scan(shadowBatch()); err != nil {
		t.Fatalf("valid scan after rejections: %v", err)
	}
	if got := d.Rounds(); got != 1 {
		t.Fatalf("round counter = %d, want 1", got)
	}
}

func TestScanMarksTyposquatSuspicious(t *testing.T) {
	d, _ := newDetector(t)
	list, err := d.Scan([]impl.Coordinate{
		{Name: "leftpad", Source: impl.SourceInternal, Publisher: "acme", Version: "1.0.0"},
		{Name: "1eftpad", Source: impl.SourcePublic, Publisher: "evil-mirror", Version: "1.0.0"},
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("collisions = %+v, want one typo pair", list)
	}
	collision := list[0]
	if collision.Kind != impl.KindTyposquat {
		t.Fatalf("kind = %q, want %q", collision.Kind, impl.KindTyposquat)
	}
	if !collision.Suspicious {
		t.Fatalf("a one character substitution must be flagged as a suspicion")
	}
	if collision.Severity != impl.SeverityMedium {
		t.Fatalf("severity = %q, want %q", collision.Severity, impl.SeverityMedium)
	}
	if collision.Class != "leftpad" {
		t.Fatalf("class = %q, want the normalized name of the internal side leftpad", collision.Class)
	}
}

func TestScanCollisionIDsStableAcrossRounds(t *testing.T) {
	d, _ := newDetector(t)
	first, err := d.Scan(shadowBatch())
	if err != nil {
		t.Fatalf("first scan: %v", err)
	}
	second, err := d.Scan(shadowBatch())
	if err != nil {
		t.Fatalf("second scan: %v", err)
	}
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("collisions = %d then %d, want one class each round", len(first), len(second))
	}
	if first[0].ID != second[0].ID {
		t.Fatalf("class id drifted across rounds: %q then %q", first[0].ID, second[0].ID)
	}
	if second[0].FirstRound != 1 || second[0].LastRound != 2 {
		t.Fatalf("rounds = %d..%d, want 1..2", second[0].FirstRound, second[0].LastRound)
	}
	if got := d.Rounds(); got != 2 {
		t.Fatalf("round counter = %d, want 2", got)
	}
}

func TestAllowSuppressesBlockUntilTime(t *testing.T) {
	d, clk := newDetector(t)
	list, err := d.Scan(shadowBatch())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	until := clk.at.Add(2 * time.Hour)
	if err := d.Allow(list[0].ID, until); err != nil {
		t.Fatalf("allow: %v", err)
	}
	after, err := d.Scan(shadowBatch())
	if err != nil {
		t.Fatalf("scan after allow: %v", err)
	}
	collision := after[0]
	if collision.Blocked {
		t.Fatalf("an allowed class must not stay blocked")
	}
	if !collision.Allowed {
		t.Fatalf("collision must report the active exemption")
	}
	if !collision.AllowUntil.Equal(until) {
		t.Fatalf("allow until = %v, want %v", collision.AllowUntil, until)
	}
	allows := d.ActiveAllows(clk.at)
	if len(allows) != 1 {
		t.Fatalf("active allows = %+v, want one record", allows)
	}
	if allows[0].CollisionID != collision.ID || allows[0].Scope != "prod-registry" {
		t.Fatalf("allow record = %+v, want id %q under scope prod-registry", allows[0], collision.ID)
	}
}

func TestAllowRejectsBadRequests(t *testing.T) {
	d, clk := newDetector(t)
	list, err := d.Scan(shadowBatch())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if err := d.Allow("cc-deadbeef", clk.at.Add(time.Hour)); !errors.Is(err, impl.ErrNotFound) {
		t.Fatalf("allowing an unknown class must fail with ErrNotFound, got %v", err)
	}
	if err := d.Allow(list[0].ID, clk.at.Add(-time.Minute)); !errors.Is(err, impl.ErrBadUntil) {
		t.Fatalf("a deadline in the past must fail with ErrBadUntil, got %v", err)
	}
	if err := d.Allow(list[0].ID, clk.at); !errors.Is(err, impl.ErrBadUntil) {
		t.Fatalf("a deadline equal to now must fail with ErrBadUntil, got %v", err)
	}
	if got := d.ActiveAllows(clk.at); len(got) != 0 {
		t.Fatalf("rejected allows must not be recorded, got %+v", got)
	}
}

func TestActiveAllowsListsOnlyLiveRecords(t *testing.T) {
	d, clk := newDetector(t)
	list, err := d.Scan([]impl.Coordinate{
		{Name: "left-pad", Source: impl.SourceInternal, Publisher: "acme", Version: "1.0.0"},
		{Name: "leftpad", Source: impl.SourcePublic, Publisher: "evil-mirror", Version: "1.0.0"},
		{Name: "right-pad", Source: impl.SourceInternal, Publisher: "acme", Version: "1.0.0"},
		{Name: "rightpad", Source: impl.SourcePublic, Publisher: "evil-mirror", Version: "1.0.0"},
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("collisions = %d, want 2", len(list))
	}
	shortID, longID := list[0].ID, list[1].ID
	if err := d.Allow(shortID, clk.at.Add(time.Hour)); err != nil {
		t.Fatalf("allow short: %v", err)
	}
	if err := d.Allow(longID, clk.at.Add(3*time.Hour)); err != nil {
		t.Fatalf("allow long: %v", err)
	}
	if got := d.ActiveAllows(clk.at); len(got) != 2 {
		t.Fatalf("active allows now = %+v, want 2", got)
	}
	live := d.ActiveAllows(clk.at.Add(2 * time.Hour))
	if len(live) != 1 || live[0].CollisionID != longID {
		t.Fatalf("active allows after two hours = %+v, want only %q", live, longID)
	}
	if got := d.ActiveAllows(clk.at.Add(4 * time.Hour)); len(got) != 0 {
		t.Fatalf("active allows after four hours = %+v, want none", got)
	}
}

func TestRevokeAllowRestoresBlocking(t *testing.T) {
	d, clk := newDetector(t)
	list, err := d.Scan(shadowBatch())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if err := d.Allow(list[0].ID, clk.at.Add(time.Hour)); err != nil {
		t.Fatalf("allow: %v", err)
	}
	if err := d.RevokeAllow(list[0].ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if err := d.RevokeAllow(list[0].ID); !errors.Is(err, impl.ErrNotFound) {
		t.Fatalf("revoking twice must fail with ErrNotFound, got %v", err)
	}
	after, err := d.Scan(shadowBatch())
	if err != nil {
		t.Fatalf("scan after revoke: %v", err)
	}
	if !after[0].Blocked || after[0].Allowed {
		t.Fatalf("revoked class must be blocked again, got %+v", after[0])
	}
}

func TestExplainReturnsSortedEvidence(t *testing.T) {
	d, _ := newDetector(t)
	list, err := d.Scan(shadowBatch())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	evidence, err := d.Explain(list[0].ID)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if evidence.CollisionID != list[0].ID || evidence.Class != "leftpad" {
		t.Fatalf("evidence = %+v, want id %q and class leftpad", evidence, list[0].ID)
	}
	if evidence.Verdict != impl.VerdictBlocked {
		t.Fatalf("verdict = %q, want %q", evidence.Verdict, impl.VerdictBlocked)
	}
	if len(evidence.Rows) != 2 {
		t.Fatalf("rows = %+v, want two coordinates", evidence.Rows)
	}
	if evidence.Rows[0].Source != impl.SourceInternal || evidence.Rows[0].Publisher != "acme" {
		t.Fatalf("first row = %+v, want the internal acme coordinate", evidence.Rows[0])
	}
	if evidence.Rows[1].Source != impl.SourcePublic || evidence.Rows[1].Publisher != "evil-mirror" {
		t.Fatalf("second row = %+v, want the public evil-mirror coordinate", evidence.Rows[1])
	}
	if evidence.Rounds != 1 {
		t.Fatalf("evidence rounds = %d, want 1", evidence.Rounds)
	}
	if _, err := d.Explain("cc-deadbeef"); !errors.Is(err, impl.ErrNotFound) {
		t.Fatalf("explaining an unknown class must fail with ErrNotFound, got %v", err)
	}
}

func TestSnapshotRestoreKeepsAllowAndRounds(t *testing.T) {
	clk := &clock{at: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	cfg := impl.Config{TypoDistance: 1, Scope: "prod-registry", Now: clk.now}
	source := impl.NewConfusionDetector(cfg)
	list, err := source.Scan(shadowBatch())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	until := clk.at.Add(time.Hour)
	if err := source.Allow(list[0].ID, until); err != nil {
		t.Fatalf("allow: %v", err)
	}
	data, err := source.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewConfusionDetector(cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.Rounds(); got != 1 {
		t.Fatalf("rounds after restore = %d, want 1", got)
	}
	stored := restored.Collisions()
	if len(stored) != 1 || stored[0].ID != list[0].ID {
		t.Fatalf("restored collisions = %+v, want the original class", stored)
	}
	if allows := restored.ActiveAllows(clk.at); len(allows) != 1 || !allows[0].Until.Equal(until) {
		t.Fatalf("restored allows = %+v, want the original deadline %v", allows, until)
	}
	after, err := restored.Scan(shadowBatch())
	if err != nil {
		t.Fatalf("scan after restore: %v", err)
	}
	if after[0].Blocked {
		t.Fatalf("restored exemption must still suppress the block")
	}
	if got := restored.Rounds(); got != 2 {
		t.Fatalf("rounds after one more scan = %d, want 2", got)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	d, clk := newDetector(t)
	list, err := d.Scan(shadowBatch())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if err := d.Allow(list[0].ID, clk.at.Add(time.Hour)); err != nil {
		t.Fatalf("allow: %v", err)
	}
	if err := d.Restore([]byte("{broken")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if err := d.Restore(nil); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("empty snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := d.Rounds(); got != 1 {
		t.Fatalf("failed restore changed the round counter to %d", got)
	}
	if got := len(d.ActiveAllows(clk.at)); got != 1 {
		t.Fatalf("failed restore touched the allow records, got %d", got)
	}
	if got := len(d.Collisions()); got != 1 {
		t.Fatalf("failed restore touched the collision table, got %d", got)
	}
}

func TestConcurrentScansKeepOneClassAndCountRounds(t *testing.T) {
	d, _ := newDetector(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ids := make(map[string]struct{})
	failures := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			list, err := d.Scan(shadowBatch())
			mu.Lock()
			defer mu.Unlock()
			if err != nil || len(list) != 1 {
				failures++
				return
			}
			ids[list[0].ID] = struct{}{}
		}()
	}
	wg.Wait()
	if failures != 0 {
		t.Fatalf("%d concurrent scans failed to report exactly one collision", failures)
	}
	if len(ids) != 1 {
		t.Fatalf("concurrent scans reported %d distinct class ids, want 1", len(ids))
	}
	if got := d.Rounds(); got != 8 {
		t.Fatalf("round counter = %d, want 8", got)
	}
	if got := len(d.Collisions()); got != 1 {
		t.Fatalf("collision table holds %d classes, want 1", got)
	}
}
