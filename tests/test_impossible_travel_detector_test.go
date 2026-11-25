package task

import (
	"errors"
	"math"
	"testing"
	"time"

	impl "gsb/batch49-go-safety-a-30"
)

const (
	beijingLat  = 39.9042
	beijingLon  = 116.4074
	shanghaiLat = 31.2304
	shanghaiLon = 121.4737
	xianLat     = 34.3416
	xianLon     = 108.9398
)

func travelConfig() impl.Config {
	return impl.Config{ThresholdKmh: 900, MaxEventsPerUser: 5, MaxVerdictsPerUser: 10, MaxKnownLocations: 5}
}

func newTravel(t *testing.T) *impl.ImpossibleTravelDetector {
	t.Helper()
	d, err := impl.NewImpossibleTravelDetector(travelConfig())
	if err != nil {
		t.Fatalf("build detector: %v", err)
	}
	if d == nil {
		t.Fatalf("detector must not be nil")
	}
	return d
}

func travelEvent(user, place string, lat, lon float64, at time.Time) impl.LoginEvent {
	return impl.LoginEvent{UserID: user, Place: place, Lat: lat, Lon: lon, Source: impl.SourceResidential, At: at}
}

func travelBase() time.Time {
	return time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
}

func TestNewRejectsBadConfig(t *testing.T) {
	bad := []impl.Config{
		{ThresholdKmh: 0, MaxEventsPerUser: 5, MaxVerdictsPerUser: 10, MaxKnownLocations: 5},
		{ThresholdKmh: 900, MaxEventsPerUser: 0, MaxVerdictsPerUser: 10, MaxKnownLocations: 5},
		{ThresholdKmh: 900, MaxEventsPerUser: 5, MaxVerdictsPerUser: 0, MaxKnownLocations: 5},
		{ThresholdKmh: 900, MaxEventsPerUser: 5, MaxVerdictsPerUser: 10, MaxKnownLocations: 0},
	}
	for i, cfg := range bad {
		if _, err := impl.NewImpossibleTravelDetector(cfg); !errors.Is(err, impl.ErrBadConfig) {
			t.Fatalf("bad config %d must fail with ErrBadConfig, got %v", i, err)
		}
	}
	d := newTravel(t)
	if _, err := d.Observe(travelEvent("user-1", "beijing", beijingLat, beijingLon, travelBase())); err != nil {
		t.Fatalf("a valid config must accept observations, got %v", err)
	}
}

func TestObserveRejectsBadInput(t *testing.T) {
	d := newTravel(t)
	base := travelBase()
	if _, err := d.Observe(travelEvent("   ", "beijing", beijingLat, beijingLon, base)); !errors.Is(err, impl.ErrBadEvent) {
		t.Fatalf("blank user must fail with ErrBadEvent, got %v", err)
	}
	if _, err := d.Observe(travelEvent("user-2", "  ", beijingLat, beijingLon, base)); !errors.Is(err, impl.ErrBadEvent) {
		t.Fatalf("blank place must fail with ErrBadEvent, got %v", err)
	}
	if _, err := d.Observe(travelEvent("user-2", "nowhere", 91, beijingLon, base)); !errors.Is(err, impl.ErrBadEvent) {
		t.Fatalf("latitude 91 must fail with ErrBadEvent, got %v", err)
	}
	if _, err := d.Observe(travelEvent("user-2", "nowhere", beijingLat, -181, base)); !errors.Is(err, impl.ErrBadEvent) {
		t.Fatalf("longitude -181 must fail with ErrBadEvent, got %v", err)
	}
	badSource := travelEvent("user-2", "beijing", beijingLat, beijingLon, base)
	badSource.Source = "tor-proxy"
	if _, err := d.Observe(badSource); !errors.Is(err, impl.ErrBadEvent) {
		t.Fatalf("unknown source must fail with ErrBadEvent, got %v", err)
	}
	if got := d.WindowSize("user-2"); got != 0 {
		t.Fatalf("rejected events must not enter the window, got %d", got)
	}
}

func TestFirstObservationHasNoTravel(t *testing.T) {
	d := newTravel(t)
	v, err := d.Observe(travelEvent("user-3", "beijing", beijingLat, beijingLon, travelBase()))
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if v.ID == "" {
		t.Fatalf("verdict id must not be empty")
	}
	if v.Flagged {
		t.Fatalf("the first observation must not be flagged")
	}
	if v.DistanceKm != 0 || v.SpeedKmh != 0 {
		t.Fatalf("first observation distance=%v speed=%v, want zero", v.DistanceKm, v.SpeedKmh)
	}
	if v.Reason != impl.ReasonClean {
		t.Fatalf("first observation reason = %q, want %q", v.Reason, impl.ReasonClean)
	}
	if v.Seq <= 0 {
		t.Fatalf("verdict seq = %d, want a positive number", v.Seq)
	}
	if got := d.WindowSize("user-3"); got != 1 {
		t.Fatalf("window size = %d, want 1", got)
	}
}

func TestSamePlaceNoTravel(t *testing.T) {
	d := newTravel(t)
	base := travelBase()
	if _, err := d.Observe(travelEvent("user-4", "beijing", beijingLat, beijingLon, base)); err != nil {
		t.Fatalf("first observe: %v", err)
	}
	v, err := d.Observe(travelEvent("user-4", "beijing", beijingLat, beijingLon, base.Add(5*time.Minute)))
	if err != nil {
		t.Fatalf("second observe: %v", err)
	}
	if v.Flagged {
		t.Fatalf("a move inside the same place must not be flagged")
	}
	if v.DistanceKm > 1 {
		t.Fatalf("distance for the same place = %v km, want about zero", v.DistanceKm)
	}
}

func TestFlagsFastLongMove(t *testing.T) {
	d := newTravel(t)
	base := travelBase()
	if _, err := d.Observe(travelEvent("user-5", "beijing", beijingLat, beijingLon, base)); err != nil {
		t.Fatalf("first observe: %v", err)
	}
	v, err := d.Observe(travelEvent("user-5", "shanghai", shanghaiLat, shanghaiLon, base.Add(30*time.Minute)))
	if err != nil {
		t.Fatalf("second observe: %v", err)
	}
	if !v.Flagged {
		t.Fatalf("beijing to shanghai in 30 minutes must be flagged")
	}
	if math.Abs(v.DistanceKm-1067) > 30 {
		t.Fatalf("beijing to shanghai distance = %v km, want about 1067", v.DistanceKm)
	}
	if v.SpeedKmh <= 900 {
		t.Fatalf("speed = %v km/h, want above the 900 threshold", v.SpeedKmh)
	}
	if v.Reason != impl.ReasonSpeedExceeded {
		t.Fatalf("flagged reason = %q, want %q", v.Reason, impl.ReasonSpeedExceeded)
	}
	if v.FromPlace != "beijing" || v.ToPlace != "shanghai" {
		t.Fatalf("verdict places = %q to %q, want beijing to shanghai", v.FromPlace, v.ToPlace)
	}
}

func TestSlowMoveNotFlagged(t *testing.T) {
	d := newTravel(t)
	base := travelBase()
	if _, err := d.Observe(travelEvent("user-6", "beijing", beijingLat, beijingLon, base)); err != nil {
		t.Fatalf("first observe: %v", err)
	}
	v, err := d.Observe(travelEvent("user-6", "xian", xianLat, xianLon, base.Add(120*time.Minute)))
	if err != nil {
		t.Fatalf("second observe: %v", err)
	}
	if v.Flagged {
		t.Fatalf("beijing to xian in two hours must not be flagged")
	}
	if math.Abs(v.DistanceKm-905) > 30 {
		t.Fatalf("beijing to xian distance = %v km, want about 905", v.DistanceKm)
	}
	if v.SpeedKmh >= 900 {
		t.Fatalf("speed = %v km/h, want below the 900 threshold", v.SpeedKmh)
	}
}

func TestVerdictRecordedForFlaggedMove(t *testing.T) {
	d := newTravel(t)
	base := travelBase()
	if _, err := d.Observe(travelEvent("user-7", "beijing", beijingLat, beijingLon, base)); err != nil {
		t.Fatalf("first observe: %v", err)
	}
	v, err := d.Observe(travelEvent("user-7", "shanghai", shanghaiLat, shanghaiLon, base.Add(20*time.Minute)))
	if err != nil {
		t.Fatalf("second observe: %v", err)
	}
	vs := d.Verdicts("user-7")
	if len(vs) != 2 {
		t.Fatalf("verdicts = %d, want one per observation", len(vs))
	}
	last := vs[len(vs)-1]
	if last.ID != v.ID || !last.Flagged {
		t.Fatalf("last verdict = %+v, want the flagged verdict %q", last, v.ID)
	}
	if last.Dismissed {
		t.Fatalf("a fresh verdict must not be dismissed")
	}
	if len(d.Verdicts("user-404")) != 0 {
		t.Fatalf("a user without observations must have no verdicts")
	}
}

func TestMarkKnownLocationValidation(t *testing.T) {
	d := newTravel(t)
	if err := d.MarkKnownLocation("user-8", "   "); !errors.Is(err, impl.ErrBlankPlace) {
		t.Fatalf("blank place must fail with ErrBlankPlace, got %v", err)
	}
	if err := d.MarkKnownLocation("   ", "beijing"); !errors.Is(err, impl.ErrBadEvent) {
		t.Fatalf("blank user must fail with ErrBadEvent, got %v", err)
	}
	if err := d.MarkKnownLocation("user-8", "xian"); err != nil {
		t.Fatalf("mark xian: %v", err)
	}
	if err := d.MarkKnownLocation("user-8", "beijing"); err != nil {
		t.Fatalf("mark beijing: %v", err)
	}
	if err := d.MarkKnownLocation("user-8", "beijing"); err != nil {
		t.Fatalf("marking the same place must be idempotent, got %v", err)
	}
	known := d.KnownLocations("user-8")
	if len(known) != 2 {
		t.Fatalf("known locations = %v, want 2 entries", known)
	}
	if known[0] != "beijing" || known[1] != "xian" {
		t.Fatalf("known locations = %v, want them sorted by name", known)
	}
}

func TestKnownPairNotFlagged(t *testing.T) {
	d := newTravel(t)
	if err := d.MarkKnownLocation("user-9", "beijing"); err != nil {
		t.Fatalf("mark beijing: %v", err)
	}
	if err := d.MarkKnownLocation("user-9", "shanghai"); err != nil {
		t.Fatalf("mark shanghai: %v", err)
	}
	base := travelBase()
	if _, err := d.Observe(travelEvent("user-9", "beijing", beijingLat, beijingLon, base)); err != nil {
		t.Fatalf("first observe: %v", err)
	}
	v, err := d.Observe(travelEvent("user-9", "shanghai", shanghaiLat, shanghaiLon, base.Add(30*time.Minute)))
	if err != nil {
		t.Fatalf("second observe: %v", err)
	}
	if v.Flagged {
		t.Fatalf("a whitelisted place pair must not be flagged")
	}
	if v.Reason != impl.ReasonKnownPair {
		t.Fatalf("reason = %q, want %q", v.Reason, impl.ReasonKnownPair)
	}
}

func TestKnownLocationLimit(t *testing.T) {
	d, err := impl.NewImpossibleTravelDetector(impl.Config{ThresholdKmh: 900, MaxEventsPerUser: 5, MaxVerdictsPerUser: 10, MaxKnownLocations: 2})
	if err != nil {
		t.Fatalf("build detector: %v", err)
	}
	for _, place := range []string{"beijing", "xian"} {
		if err := d.MarkKnownLocation("user-10", place); err != nil {
			t.Fatalf("mark %s: %v", place, err)
		}
	}
	if err := d.MarkKnownLocation("user-10", "shanghai"); !errors.Is(err, impl.ErrLocationLimit) {
		t.Fatalf("third known location must fail with ErrLocationLimit, got %v", err)
	}
	if got := len(d.KnownLocations("user-10")); got != 2 {
		t.Fatalf("known locations = %d, want the cap 2", got)
	}
}

func TestDismissRequiresActorAndReason(t *testing.T) {
	d := newTravel(t)
	base := travelBase()
	if _, err := d.Observe(travelEvent("user-11", "beijing", beijingLat, beijingLon, base)); err != nil {
		t.Fatalf("first observe: %v", err)
	}
	v, err := d.Observe(travelEvent("user-11", "shanghai", shanghaiLat, shanghaiLon, base.Add(15*time.Minute)))
	if err != nil {
		t.Fatalf("second observe: %v", err)
	}
	if !v.Flagged {
		t.Fatalf("fixture must produce a flagged verdict")
	}
	if err := d.Dismiss(v.ID, "", "known trip"); !errors.Is(err, impl.ErrBlankField) {
		t.Fatalf("dismiss without an actor must fail with ErrBlankField, got %v", err)
	}
	if err := d.Dismiss(v.ID, "ops-1", "  "); !errors.Is(err, impl.ErrBlankField) {
		t.Fatalf("dismiss without a reason must fail with ErrBlankField, got %v", err)
	}
	if err := d.Dismiss("verdict-404", "ops-1", "known trip"); !errors.Is(err, impl.ErrUnknownVerdict) {
		t.Fatalf("dismiss of an unknown verdict must fail with ErrUnknownVerdict, got %v", err)
	}
	if err := d.Dismiss(v.ID, "ops-1", "known trip"); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	vs := d.Verdicts("user-11")
	last := vs[len(vs)-1]
	if !last.Dismissed {
		t.Fatalf("verdict must be marked dismissed")
	}
	if err := d.Dismiss(v.ID, "ops-1", "known trip"); !errors.Is(err, impl.ErrAlreadyDismissed) {
		t.Fatalf("repeated dismiss must fail with ErrAlreadyDismissed, got %v", err)
	}
}

func TestVerdictCapKeepsNewest(t *testing.T) {
	d, err := impl.NewImpossibleTravelDetector(impl.Config{ThresholdKmh: 900, MaxEventsPerUser: 10, MaxVerdictsPerUser: 2, MaxKnownLocations: 5})
	if err != nil {
		t.Fatalf("build detector: %v", err)
	}
	base := travelBase()
	route := []struct {
		place string
		lat   float64
		lon   float64
	}{
		{"beijing", beijingLat, beijingLon},
		{"shanghai", shanghaiLat, shanghaiLon},
		{"beijing", beijingLat, beijingLon},
		{"shanghai", shanghaiLat, shanghaiLon},
	}
	var last impl.Verdict
	for i, stop := range route {
		v, err := d.Observe(travelEvent("user-12", stop.place, stop.lat, stop.lon, base.Add(time.Duration(i*30)*time.Minute)))
		if err != nil {
			t.Fatalf("observe %d: %v", i, err)
		}
		last = v
	}
	vs := d.Verdicts("user-12")
	if len(vs) != 2 {
		t.Fatalf("retained verdicts = %d, want the cap 2", len(vs))
	}
	if vs[len(vs)-1].ID != last.ID {
		t.Fatalf("newest retained verdict = %q, want %q", vs[len(vs)-1].ID, last.ID)
	}
}

func TestWindowSizeCapped(t *testing.T) {
	d, err := impl.NewImpossibleTravelDetector(impl.Config{ThresholdKmh: 900, MaxEventsPerUser: 2, MaxVerdictsPerUser: 10, MaxKnownLocations: 5})
	if err != nil {
		t.Fatalf("build detector: %v", err)
	}
	base := travelBase()
	for i := 0; i < 5; i++ {
		if _, err := d.Observe(travelEvent("user-13", "beijing", beijingLat, beijingLon, base.Add(time.Duration(i*10)*time.Minute))); err != nil {
			t.Fatalf("observe %d: %v", i, err)
		}
	}
	if got := d.WindowSize("user-13"); got != 2 {
		t.Fatalf("window size = %d, want the cap 2", got)
	}
}

func TestSnapshotRestoreKeepsWindow(t *testing.T) {
	limits := impl.Config{ThresholdKmh: 900, MaxEventsPerUser: 5, MaxVerdictsPerUser: 10, MaxKnownLocations: 5}
	d, err := impl.NewImpossibleTravelDetector(limits)
	if err != nil {
		t.Fatalf("build detector: %v", err)
	}
	base := travelBase()
	if _, err := d.Observe(travelEvent("user-14", "beijing", beijingLat, beijingLon, base)); err != nil {
		t.Fatalf("first observe: %v", err)
	}
	if _, err := d.Observe(travelEvent("user-14", "xian", xianLat, xianLon, base.Add(120*time.Minute))); err != nil {
		t.Fatalf("second observe: %v", err)
	}
	data, err := d.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored, err := impl.NewImpossibleTravelDetector(limits)
	if err != nil {
		t.Fatalf("build restored detector: %v", err)
	}
	if err := restored.LoadSnapshot(data); err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	if got := restored.WindowSize("user-14"); got != 2 {
		t.Fatalf("restored window size = %d, want 2", got)
	}
	v, err := restored.Observe(travelEvent("user-14", "shanghai", shanghaiLat, shanghaiLon, base.Add(150*time.Minute)))
	if err != nil {
		t.Fatalf("observe after restore: %v", err)
	}
	if !v.Flagged {
		t.Fatalf("xian to shanghai in 30 minutes must be flagged after the restore")
	}
	if v.FromPlace != "xian" || v.ToPlace != "shanghai" {
		t.Fatalf("restored window compared %q to %q, want xian to shanghai", v.FromPlace, v.ToPlace)
	}
}

func TestLoadSnapshotRejectsCorrupt(t *testing.T) {
	d := newTravel(t)
	if _, err := d.Observe(travelEvent("user-15", "beijing", beijingLat, beijingLon, travelBase())); err != nil {
		t.Fatalf("observe: %v", err)
	}
	if err := d.LoadSnapshot([]byte("}}}{{")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := d.WindowSize("user-15"); got != 1 {
		t.Fatalf("state after a rejected snapshot changed, window = %d", got)
	}
	if _, err := d.Observe(travelEvent("user-15", "xian", xianLat, xianLon, travelBase().Add(time.Hour))); err != nil {
		t.Fatalf("live state must keep working after a rejected snapshot, got %v", err)
	}
}
