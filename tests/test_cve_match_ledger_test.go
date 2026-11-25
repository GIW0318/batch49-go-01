package task

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	impl "gsb/batch49-go-safety-c-02"
)

func semverCoord(version string) impl.Coordinate {
	return impl.Coordinate{
		Vendor:    "acme",
		Package:   "libfoo",
		Version:   version,
		Ecosystem: impl.EcosystemSemver,
	}
}

func vulnRecord(cve, source string, updated int64, fixed string) impl.Record {
	return impl.Record{
		CVE:       cve,
		Source:    source,
		UpdatedAt: updated,
		Vendor:    "acme",
		Package:   "libfoo",
		Ecosystem: impl.EcosystemSemver,
		Affected:  []impl.AffectedRange{{Introduced: "1.0.0", Fixed: fixed}},
	}
}

func TestCompareSemverOrdersAndRejectsMalformed(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.2.3", "1.2.4", -1},
		{"1.2.4", "1.2.3", 1},
		{"1.2.3", "1.2.3", 0},
		{"1.2.3-rc.1", "1.2.3", -1},
		{"1.10.0", "1.9.9", 1},
		{"1.2.3+build.5", "1.2.3", 0},
	}
	for _, c := range cases {
		got, err := impl.CompareVersions(impl.EcosystemSemver, c.a, c.b)
		if err != nil {
			t.Fatalf("compare %q vs %q: %v", c.a, c.b, err)
		}
		if got != c.want {
			t.Fatalf("compare %q vs %q = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	if _, err := impl.CompareVersions(impl.EcosystemSemver, "1.2", "1.2.3"); !errors.Is(err, impl.ErrBadVersion) {
		t.Fatalf("two segment semver must fail with ErrBadVersion, got %v", err)
	}
	if _, err := impl.CompareVersions("maven", "1.0.0", "1.0.0"); !errors.Is(err, impl.ErrBadVersion) {
		t.Fatalf("unknown ecosystem must fail with ErrBadVersion, got %v", err)
	}
}

func TestCompareDebEpochAndRevision(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1:1.0-2", "1:1.0-1", 1},
		{"2:0.1-1", "1:9.9-9", 1},
		{"1.0-1", "1.0-1", 0},
		{"1.0-10", "1.0-9", 1},
	}
	for _, c := range cases {
		got, err := impl.CompareVersions(impl.EcosystemDeb, c.a, c.b)
		if err != nil {
			t.Fatalf("compare %q vs %q: %v", c.a, c.b, err)
		}
		if got != c.want {
			t.Fatalf("compare %q vs %q = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestCompareTildeSortsBeforeRelease(t *testing.T) {
	cases := []struct {
		ecosystem, a, b string
		want            int
	}{
		{impl.EcosystemDeb, "1.0~rc1", "1.0", -1},
		{impl.EcosystemDeb, "1.0", "1.0~rc1", 1},
		{impl.EcosystemRpm, "2.0~beta", "2.0", -1},
	}
	for _, c := range cases {
		got, err := impl.CompareVersions(c.ecosystem, c.a, c.b)
		if err != nil {
			t.Fatalf("compare %q vs %q: %v", c.a, c.b, err)
		}
		if got != c.want {
			t.Fatalf("compare %q vs %q = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestNormalizeCoordinateLowercases(t *testing.T) {
	out, err := impl.NormalizeCoordinate(impl.Coordinate{
		Vendor:    "  Acme  ",
		Package:   " LibFoo ",
		Version:   " 1.2.3 ",
		Ecosystem: " SEMVER ",
		Distro:    " Ubuntu-22.04 ",
	})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if out.Vendor != "acme" || out.Package != "libfoo" || out.Version != "1.2.3" {
		t.Fatalf("normalized identity = %+v, want acme/libfoo/1.2.3", out)
	}
	if out.Ecosystem != impl.EcosystemSemver || out.Distro != "ubuntu-22.04" {
		t.Fatalf("normalized ecosystem/distro = %q/%q", out.Ecosystem, out.Distro)
	}
	if _, err := impl.NormalizeCoordinate(impl.Coordinate{Package: "libfoo", Version: "1.0.0", Ecosystem: impl.EcosystemSemver}); !errors.Is(err, impl.ErrBadCoordinate) {
		t.Fatalf("missing package must fail with ErrBadCoordinate, got %v", err)
	}
	if _, err := impl.NormalizeCoordinate(impl.Coordinate{Package: "libfoo", Version: "1.0.0", Ecosystem: "maven"}); !errors.Is(err, impl.ErrBadCoordinate) {
		t.Fatalf("unknown ecosystem must fail with ErrBadCoordinate, got %v", err)
	}
}

func TestLoadRejectsBadRecords(t *testing.T) {
	l := impl.NewCveMatchLedger(nil)
	blankCVE := vulnRecord("", "vendor", 10, "2.0.0")
	if err := l.Load([]impl.Record{blankCVE}); !errors.Is(err, impl.ErrBadRecord) {
		t.Fatalf("blank CVE must fail with ErrBadRecord, got %v", err)
	}
	blankPackage := vulnRecord("CVE-2026-1001", "vendor", 10, "2.0.0")
	blankPackage.Package = "   "
	if err := l.Load([]impl.Record{blankPackage}); !errors.Is(err, impl.ErrBadRecord) {
		t.Fatalf("blank package must fail with ErrBadRecord, got %v", err)
	}
	inverted := vulnRecord("CVE-2026-1001", "vendor", 10, "1.0.0")
	if err := l.Load([]impl.Record{inverted}); !errors.Is(err, impl.ErrBadRecord) {
		t.Fatalf("fixed equal to introduced must fail with ErrBadRecord, got %v", err)
	}
	duplicate := []impl.Record{
		vulnRecord("CVE-2026-1001", "vendor", 10, "2.0.0"),
		vulnRecord("CVE-2026-1001", "vendor", 20, "3.0.0"),
	}
	if err := l.Load(duplicate); !errors.Is(err, impl.ErrBadRecord) {
		t.Fatalf("duplicate CVE and source must fail with ErrBadRecord, got %v", err)
	}
	if got := l.Records(); len(got) != 0 {
		t.Fatalf("rejected loads must not store records, got %v", got)
	}
	if err := l.Load([]impl.Record{vulnRecord("CVE-2026-1001", "vendor", 10, "2.0.0")}); err != nil {
		t.Fatalf("load good record: %v", err)
	}
	if got := l.Records(); len(got) != 1 || got[0] != "CVE-2026-1001" {
		t.Fatalf("records = %v, want [CVE-2026-1001]", got)
	}
}

func TestMatchAffectedRangeHalfOpen(t *testing.T) {
	l := impl.NewCveMatchLedger([]string{"vendor", "nvd", "distro"})
	if err := l.Load([]impl.Record{vulnRecord("CVE-2026-1001", "vendor", 10, "2.0.0")}); err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, version := range []string{"1.0.0", "1.5.0", "1.99.9"} {
		matches, err := l.Match(semverCoord(version))
		if err != nil {
			t.Fatalf("match %s: %v", version, err)
		}
		if len(matches) != 1 {
			t.Fatalf("match %s = %d entries, want 1", version, len(matches))
		}
		if matches[0].Status != impl.StatusAffected || matches[0].Reason != impl.ReasonAffectedRange {
			t.Fatalf("match %s = %+v, want affected/affected_range", version, matches[0])
		}
		if matches[0].Source != "vendor" {
			t.Fatalf("match %s source = %q, want vendor", version, matches[0].Source)
		}
	}
	for _, version := range []string{"2.0.0", "0.9.0", "2.1.0"} {
		matches, err := l.Match(semverCoord(version))
		if err != nil {
			t.Fatalf("match %s: %v", version, err)
		}
		if len(matches) != 0 {
			t.Fatalf("match %s = %+v, want no verdict outside [1.0.0,2.0.0)", version, matches)
		}
	}
}

func TestMatchMultipleRanges(t *testing.T) {
	rec := vulnRecord("CVE-2026-1001", "vendor", 10, "1.5.0")
	rec.Affected = append(rec.Affected, impl.AffectedRange{Introduced: "2.0.0", Fixed: "2.3.0"})
	l := impl.NewCveMatchLedger([]string{"vendor", "nvd", "distro"})
	if err := l.Load([]impl.Record{rec}); err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, version := range []string{"1.2.0", "2.1.0"} {
		matches, err := l.Match(semverCoord(version))
		if err != nil {
			t.Fatalf("match %s: %v", version, err)
		}
		if len(matches) != 1 || matches[0].Status != impl.StatusAffected {
			t.Fatalf("match %s = %+v, want one affected verdict", version, matches)
		}
	}
	for _, version := range []string{"1.7.0", "2.3.0"} {
		matches, err := l.Match(semverCoord(version))
		if err != nil {
			t.Fatalf("match %s: %v", version, err)
		}
		if len(matches) != 0 {
			t.Fatalf("match %s = %+v, want no verdict in the gap or at the fixed bound", version, matches)
		}
	}
}

func TestMatchFiltersByPackageIdentity(t *testing.T) {
	otherVendor := vulnRecord("CVE-2026-2002", "vendor", 10, "2.0.0")
	otherVendor.Vendor = "globex"
	otherPackage := vulnRecord("CVE-2026-3003", "vendor", 10, "2.0.0")
	otherPackage.Package = "libbar"
	l := impl.NewCveMatchLedger([]string{"vendor", "nvd", "distro"})
	if err := l.Load([]impl.Record{vulnRecord("CVE-2026-1001", "vendor", 10, "2.0.0"), otherVendor, otherPackage}); err != nil {
		t.Fatalf("load: %v", err)
	}
	matches, err := l.Match(impl.Coordinate{Vendor: " ACME ", Package: "LibFoo", Version: "1.5.0", Ecosystem: impl.EcosystemSemver})
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if len(matches) != 1 || matches[0].CVE != "CVE-2026-1001" {
		t.Fatalf("matches = %+v, want only CVE-2026-1001", matches)
	}
	if _, err := l.Match(impl.Coordinate{Package: "libfoo", Version: "1.5.0", Ecosystem: impl.EcosystemSemver}); !errors.Is(err, impl.ErrBadCoordinate) {
		t.Fatalf("blank vendor must fail with ErrBadCoordinate, got %v", err)
	}
	if _, err := l.Match(impl.Coordinate{Vendor: "acme", Package: "libfoo", Version: " ", Ecosystem: impl.EcosystemSemver}); !errors.Is(err, impl.ErrBadCoordinate) {
		t.Fatalf("blank version must fail with ErrBadCoordinate, got %v", err)
	}
}

func TestSourcePriorityResolvesConflict(t *testing.T) {
	vendor := vulnRecord("CVE-2026-9009", "vendor", 50, "1.5.0")
	distro := vulnRecord("CVE-2026-9009", "nvd", 900, "2.0.0")
	vendorFirst := impl.NewCveMatchLedger([]string{"vendor", "nvd"})
	if err := vendorFirst.Load([]impl.Record{vendor, distro}); err != nil {
		t.Fatalf("load: %v", err)
	}
	matches, err := vendorFirst.Match(semverCoord("1.7.0"))
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("matches = %+v, want the vendor record to win so 1.7.0 is already fixed", matches)
	}
	trace, err := vendorFirst.Explain("CVE-2026-9009", semverCoord("1.7.0"))
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if trace.Status != impl.StatusNotApplicable || trace.Reason != impl.ReasonAboveFixed {
		t.Fatalf("trace = %+v, want not_applicable/above_fixed", trace)
	}
	if trace.Source != "vendor" {
		t.Fatalf("trace source = %q, want the winning vendor record", trace.Source)
	}
	if len(trace.Shadowed) != 1 || trace.Shadowed[0].Source != "nvd" {
		t.Fatalf("shadowed records = %+v, want the masked nvd record", trace.Shadowed)
	}
	nvdFirst := impl.NewCveMatchLedger([]string{"nvd", "vendor"})
	if err := nvdFirst.Load([]impl.Record{vendor, distro}); err != nil {
		t.Fatalf("load: %v", err)
	}
	matches, err = nvdFirst.Match(semverCoord("1.7.0"))
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if len(matches) != 1 || matches[0].Source != "nvd" || matches[0].Status != impl.StatusAffected {
		t.Fatalf("matches = %+v, want the nvd record to win and report affected", matches)
	}
}

func TestExplainReportsComparisons(t *testing.T) {
	l := impl.NewCveMatchLedger([]string{"vendor", "nvd", "distro"})
	if err := l.Load([]impl.Record{vulnRecord("CVE-2026-1001", "vendor", 10, "2.0.0")}); err != nil {
		t.Fatalf("load: %v", err)
	}
	trace, err := l.Explain("CVE-2026-1001", semverCoord("1.7.0"))
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if trace.CVE != "CVE-2026-1001" || trace.Status != impl.StatusAffected || trace.Reason != impl.ReasonAffectedRange {
		t.Fatalf("trace header = %+v, want affected/affected_range for CVE-2026-1001", trace)
	}
	if len(trace.Comparisons) != 2 {
		t.Fatalf("comparisons = %+v, want the introduced and fixed boundaries", trace.Comparisons)
	}
	if trace.Comparisons[0].Target != "1.0.0" || trace.Comparisons[0].Cmp != 1 {
		t.Fatalf("first comparison = %+v, want 1.0.0 with cmp 1", trace.Comparisons[0])
	}
	if trace.Comparisons[1].Target != "2.0.0" || trace.Comparisons[1].Cmp != -1 {
		t.Fatalf("second comparison = %+v, want 2.0.0 with cmp -1", trace.Comparisons[1])
	}
	if _, err := l.Explain("CVE-404", semverCoord("1.7.0")); !errors.Is(err, impl.ErrUnknownCVE) {
		t.Fatalf("unknown CVE must fail with ErrUnknownCVE, got %v", err)
	}
}

func TestOverrideForcesNotAffected(t *testing.T) {
	l := impl.NewCveMatchLedger([]string{"vendor", "nvd", "distro"})
	if err := l.Load([]impl.Record{vulnRecord("CVE-2026-1001", "vendor", 10, "2.0.0")}); err != nil {
		t.Fatalf("load: %v", err)
	}
	coord := semverCoord("1.5.0")
	if err := l.Override("CVE-2026-1001", coord, "confirmed false positive"); err != nil {
		t.Fatalf("override: %v", err)
	}
	matches, err := l.Match(coord)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if len(matches) != 1 || matches[0].Status != impl.StatusNotAffected || matches[0].Reason != impl.ReasonManualOverride {
		t.Fatalf("matches = %+v, want not_affected/manual_override", matches)
	}
	trace, err := l.Explain("CVE-2026-1001", coord)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if trace.Status != impl.StatusNotAffected || trace.Reason != impl.ReasonManualOverride {
		t.Fatalf("trace = %+v, want the override verdict", trace)
	}
	if err := l.Override("CVE-2026-1001", coord, "   "); !errors.Is(err, impl.ErrBadOverride) {
		t.Fatalf("blank override reason must fail with ErrBadOverride, got %v", err)
	}
	if err := l.Override("CVE-404", coord, "nope"); !errors.Is(err, impl.ErrUnknownCVE) {
		t.Fatalf("override of unknown CVE must fail with ErrUnknownCVE, got %v", err)
	}
}

func TestWithdrawRemovesCVE(t *testing.T) {
	other := vulnRecord("CVE-2026-2002", "vendor", 10, "3.0.0")
	l := impl.NewCveMatchLedger([]string{"vendor", "nvd", "distro"})
	if err := l.Load([]impl.Record{vulnRecord("CVE-2026-1001", "vendor", 10, "2.0.0"), other}); err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := l.Withdraw("CVE-2026-1001"); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	if got := l.Records(); len(got) != 1 || got[0] != "CVE-2026-2002" {
		t.Fatalf("records after withdraw = %v, want [CVE-2026-2002]", got)
	}
	matches, err := l.Match(semverCoord("1.5.0"))
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("withdrawn CVE must not match, got %+v", matches)
	}
	if _, err := l.Explain("CVE-2026-1001", semverCoord("1.5.0")); !errors.Is(err, impl.ErrUnknownCVE) {
		t.Fatalf("withdrawn CVE must be unknown, got %v", err)
	}
	if err := l.Withdraw("CVE-2026-1001"); !errors.Is(err, impl.ErrUnknownCVE) {
		t.Fatalf("withdrawing twice must fail with ErrUnknownCVE, got %v", err)
	}
}

func TestConcurrentMatchAndLoadIsAtomic(t *testing.T) {
	affectedSet := []impl.Record{vulnRecord("CVE-2026-1001", "vendor", 10, "2.0.0")}
	fixedSet := []impl.Record{vulnRecord("CVE-2026-1001", "vendor", 20, "1.2.0")}
	l := impl.NewCveMatchLedger([]string{"vendor", "nvd", "distro"})
	if err := l.Load(affectedSet); err != nil {
		t.Fatalf("initial load: %v", err)
	}
	const readers = 6
	const iterations = 40
	errCh := make(chan error, readers*iterations)
	var wg sync.WaitGroup
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				matches, err := l.Match(semverCoord("1.5.0"))
				if err != nil {
					errCh <- fmt.Errorf("match during load: %w", err)
					return
				}
				switch len(matches) {
				case 0:
				case 1:
					if matches[0].Status != impl.StatusAffected || matches[0].CVE != "CVE-2026-1001" {
						errCh <- fmt.Errorf("torn match entry: %+v", matches[0])
						return
					}
				default:
					errCh <- fmt.Errorf("match returned %d entries for one record set", len(matches))
					return
				}
			}
		}()
	}
	for i := 0; i < 24; i++ {
		var err error
		if i%2 == 0 {
			err = l.Load(fixedSet)
		} else {
			err = l.Load(affectedSet)
		}
		if err != nil {
			t.Fatalf("load %d: %v", i, err)
		}
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent match observed a torn state: %v", err)
	}
}

func TestSnapshotRestoreKeepsRecordsAndOverrides(t *testing.T) {
	l := impl.NewCveMatchLedger([]string{"vendor", "nvd", "distro"})
	if err := l.Load([]impl.Record{vulnRecord("CVE-2026-1001", "vendor", 10, "2.0.0")}); err != nil {
		t.Fatalf("load: %v", err)
	}
	coord := semverCoord("1.5.0")
	if err := l.Override("CVE-2026-1001", coord, "vendor confirmed custom build"); err != nil {
		t.Fatalf("override: %v", err)
	}
	data, err := l.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewCveMatchLedger([]string{"vendor", "nvd", "distro"})
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.Records(); len(got) != 1 || got[0] != "CVE-2026-1001" {
		t.Fatalf("restored records = %v, want [CVE-2026-1001]", got)
	}
	matches, err := restored.Match(coord)
	if err != nil {
		t.Fatalf("match after restore: %v", err)
	}
	if len(matches) != 1 || matches[0].Status != impl.StatusNotAffected || matches[0].Reason != impl.ReasonManualOverride {
		t.Fatalf("matches after restore = %+v, want the override to survive", matches)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	l := impl.NewCveMatchLedger([]string{"vendor", "nvd", "distro"})
	if err := l.Load([]impl.Record{vulnRecord("CVE-2026-1001", "vendor", 10, "2.0.0")}); err != nil {
		t.Fatalf("load: %v", err)
	}
	coord := semverCoord("1.5.0")
	if err := l.Override("CVE-2026-1001", coord, "keep me"); err != nil {
		t.Fatalf("override: %v", err)
	}
	if err := l.Restore([]byte("not-json-at-all")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := l.Records(); len(got) != 1 {
		t.Fatalf("records after rejected restore = %v, want the live set", got)
	}
	matches, err := l.Match(coord)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if len(matches) != 1 || matches[0].Reason != impl.ReasonManualOverride {
		t.Fatalf("override must survive a rejected restore, got %+v", matches)
	}
}
