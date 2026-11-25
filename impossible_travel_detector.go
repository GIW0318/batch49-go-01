// Package task detects impossible travel from login geography and time, with a
// bounded per user event window.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

// Login source types.
const (
	SourceResidential = "residential"
	SourceDatacenter  = "datacenter"
	SourceVPN         = "vpn"
)

// Reasons attached to a verdict.
const (
	ReasonSpeedExceeded = "reason_speed_exceeded"
	ReasonKnownPair     = "reason_known_pair"
	ReasonClean         = "reason_clean"
)

// Errors returned by the detector.
var (
	ErrBadConfig        = errors.New("threshold and caps must all be positive")
	ErrBadEvent         = errors.New("user, place, coordinates or source are invalid")
	ErrBlankPlace       = errors.New("place name is required")
	ErrLocationLimit    = errors.New("known location limit reached for this user")
	ErrUnknownVerdict   = errors.New("unknown verdict")
	ErrBlankField       = errors.New("actor and reason are required")
	ErrAlreadyDismissed = errors.New("verdict was already dismissed")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

// LoginEvent is one located login.
type LoginEvent struct {
	UserID string    `json:"user_id"`
	Place  string    `json:"place"`
	Lat    float64   `json:"lat"`
	Lon    float64   `json:"lon"`
	Source string    `json:"source"`
	At     time.Time `json:"at"`
}

// Verdict is one travel judgement between two adjacent events.
type Verdict struct {
	ID         string  `json:"id"`
	UserID     string  `json:"user_id"`
	FromPlace  string  `json:"from_place"`
	ToPlace    string  `json:"to_place"`
	Source     string  `json:"source"`
	DistanceKm float64 `json:"distance_km"`
	SpeedKmh   float64 `json:"speed_kmh"`
	Flagged    bool    `json:"flagged"`
	Dismissed  bool    `json:"dismissed"`
	Reason     string  `json:"reason"`
	Seq        int64   `json:"seq"`
}

// Config bounds the detector behaviour and tables.
type Config struct {
	ThresholdKmh       float64
	MaxEventsPerUser   int
	MaxVerdictsPerUser int
	MaxKnownLocations  int
}

// ImpossibleTravelDetector owns the event windows, verdicts and whitelists.
type ImpossibleTravelDetector struct {
	mu       sync.Mutex
	cfg      Config
	windows  map[string][]LoginEvent
	verdicts map[string][]Verdict
	known    map[string]map[string]struct{}
	byID     map[string]string
	seq      int64
}

// NewImpossibleTravelDetector validates the config and builds a detector.
func NewImpossibleTravelDetector(cfg Config) (*ImpossibleTravelDetector, error) {
	if cfg.ThresholdKmh <= 0 || math.IsNaN(cfg.ThresholdKmh) || math.IsInf(cfg.ThresholdKmh, 0) ||
		cfg.MaxEventsPerUser <= 0 || cfg.MaxVerdictsPerUser <= 0 || cfg.MaxKnownLocations <= 0 {
		return nil, ErrBadConfig
	}
	return &ImpossibleTravelDetector{
		cfg:      cfg,
		windows:  make(map[string][]LoginEvent),
		verdicts: make(map[string][]Verdict),
		known:    make(map[string]map[string]struct{}),
		byID:     make(map[string]string),
	}, nil
}

// Observe records one located login and grades the move from the previous one.
func (d *ImpossibleTravelDetector) Observe(e LoginEvent) (Verdict, error) {
	if err := validateEvent(e); err != nil {
		return Verdict{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	win := d.windows[e.UserID]
	idx := sort.Search(len(win), func(i int) bool { return win[i].At.After(e.At) })
	win = append(win, LoginEvent{})
	copy(win[idx+1:], win[idx:])
	win[idx] = e
	if overflow := len(win) - d.cfg.MaxEventsPerUser; overflow > 0 {
		win = append([]LoginEvent(nil), win[overflow:]...)
		idx -= overflow
	}
	d.windows[e.UserID] = win

	d.seq++
	v := Verdict{
		ID:      fmt.Sprintf("verdict-%d", d.seq),
		UserID:  e.UserID,
		ToPlace: e.Place,
		Source:  e.Source,
		Reason:  ReasonClean,
		Seq:     d.seq,
	}

	if e.Source == SourceResidential && idx > 0 {
		prev, found := residentialPredecessor(win, idx)
		if found {
			v.FromPlace = prev.Place
			v.DistanceKm = haversineKm(prev.Lat, prev.Lon, e.Lat, e.Lon)
			if hours := e.At.Sub(prev.At).Hours(); hours > 0 {
				v.SpeedKmh = v.DistanceKm / hours
			}
			switch {
			case d.isKnownPair(e.UserID, prev.Place, e.Place):
				v.Reason = ReasonKnownPair
			case v.SpeedKmh > d.cfg.ThresholdKmh:
				v.Flagged = true
				v.Reason = ReasonSpeedExceeded
			}
		}
	}

	vs := append(d.verdicts[e.UserID], v)
	if overflow := len(vs) - d.cfg.MaxVerdictsPerUser; overflow > 0 {
		for _, old := range vs[:overflow] {
			delete(d.byID, old.ID)
		}
		vs = append([]Verdict(nil), vs[overflow:]...)
	}
	d.verdicts[e.UserID] = vs
	d.byID[v.ID] = e.UserID
	return v, nil
}

// Verdicts lists the retained verdicts of one user, oldest first.
func (d *ImpossibleTravelDetector) Verdicts(userID string) []Verdict {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]Verdict(nil), d.verdicts[userID]...)
}

// MarkKnownLocation adds one place to the per user whitelist.
func (d *ImpossibleTravelDetector) MarkKnownLocation(userID, place string) error {
	if strings.TrimSpace(userID) == "" {
		return ErrBadEvent
	}
	if strings.TrimSpace(place) == "" {
		return ErrBlankPlace
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	set, ok := d.known[userID]
	if !ok {
		set = make(map[string]struct{})
		d.known[userID] = set
	}
	if _, dup := set[place]; dup {
		return nil
	}
	if len(set) >= d.cfg.MaxKnownLocations {
		return ErrLocationLimit
	}
	set[place] = struct{}{}
	return nil
}

// KnownLocations lists the whitelisted places of one user, sorted by name.
func (d *ImpossibleTravelDetector) KnownLocations(userID string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	set := d.known[userID]
	out := make([]string, 0, len(set))
	for place := range set {
		out = append(out, place)
	}
	sort.Strings(out)
	return out
}

// Dismiss clears a flagged verdict with an operator and a reason.
func (d *ImpossibleTravelDetector) Dismiss(verdictID, actorID, reason string) error {
	if strings.TrimSpace(actorID) == "" || strings.TrimSpace(reason) == "" {
		return ErrBlankField
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	userID, ok := d.byID[verdictID]
	if !ok {
		return ErrUnknownVerdict
	}
	vs := d.verdicts[userID]
	for i := range vs {
		if vs[i].ID == verdictID {
			if vs[i].Dismissed {
				return ErrAlreadyDismissed
			}
			vs[i].Dismissed = true
			d.verdicts[userID] = vs
			return nil
		}
	}
	return ErrUnknownVerdict
}

// WindowSize reports how many events the user window currently holds.
func (d *ImpossibleTravelDetector) WindowSize(userID string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.windows[userID])
}

// DistanceKm returns the great circle distance between two located events.
func (d *ImpossibleTravelDetector) DistanceKm(a, b LoginEvent) (float64, error) {
	if !validCoords(a.Lat, a.Lon) || !validCoords(b.Lat, b.Lon) {
		return 0, ErrBadEvent
	}
	return haversineKm(a.Lat, a.Lon, b.Lat, b.Lon), nil
}

// snapshotState is the serialised form of the whole detector state.
type snapshotState struct {
	Seq      int64                   `json:"seq"`
	Windows  map[string][]LoginEvent `json:"windows"`
	Verdicts map[string][]Verdict    `json:"verdicts"`
	Known    map[string][]string     `json:"known"`
}

// Snapshot serialises windows, verdicts, whitelists and the sequence watermark.
func (d *ImpossibleTravelDetector) Snapshot() ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	snap := snapshotState{
		Seq:      d.seq,
		Windows:  make(map[string][]LoginEvent, len(d.windows)),
		Verdicts: make(map[string][]Verdict, len(d.verdicts)),
		Known:    make(map[string][]string, len(d.known)),
	}
	for userID, win := range d.windows {
		snap.Windows[userID] = append([]LoginEvent(nil), win...)
	}
	for userID, vs := range d.verdicts {
		snap.Verdicts[userID] = append([]Verdict(nil), vs...)
	}
	for userID, set := range d.known {
		places := make([]string, 0, len(set))
		for place := range set {
			places = append(places, place)
		}
		sort.Strings(places)
		snap.Known[userID] = places
	}
	return json.Marshal(snap)
}

// LoadSnapshot replaces the whole state with a snapshot, all or nothing.
func (d *ImpossibleTravelDetector) LoadSnapshot(data []byte) error {
	var snap snapshotState
	if err := json.Unmarshal(data, &snap); err != nil {
		return ErrBadSnapshot
	}

	windows := make(map[string][]LoginEvent, len(snap.Windows))
	for userID, evs := range snap.Windows {
		if strings.TrimSpace(userID) == "" {
			return ErrBadSnapshot
		}
		for _, e := range evs {
			if e.UserID != userID || validateEvent(e) != nil {
				return ErrBadSnapshot
			}
		}
		sorted := append([]LoginEvent(nil), evs...)
		sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].At.Before(sorted[j].At) })
		if overflow := len(sorted) - d.cfg.MaxEventsPerUser; overflow > 0 {
			sorted = sorted[overflow:]
		}
		windows[userID] = sorted
	}

	verdicts := make(map[string][]Verdict, len(snap.Verdicts))
	byID := make(map[string]string)
	var maxSeq int64
	for userID, vs := range snap.Verdicts {
		if strings.TrimSpace(userID) == "" {
			return ErrBadSnapshot
		}
		if overflow := len(vs) - d.cfg.MaxVerdictsPerUser; overflow > 0 {
			vs = vs[overflow:]
		}
		kept := make([]Verdict, 0, len(vs))
		for _, v := range vs {
			if v.ID == "" || v.UserID != userID || v.Seq <= 0 {
				return ErrBadSnapshot
			}
			if _, dup := byID[v.ID]; dup {
				return ErrBadSnapshot
			}
			byID[v.ID] = userID
			if v.Seq > maxSeq {
				maxSeq = v.Seq
			}
			kept = append(kept, v)
		}
		verdicts[userID] = kept
	}

	known := make(map[string]map[string]struct{}, len(snap.Known))
	for userID, places := range snap.Known {
		if strings.TrimSpace(userID) == "" || len(places) > d.cfg.MaxKnownLocations {
			return ErrBadSnapshot
		}
		set := make(map[string]struct{}, len(places))
		for _, place := range places {
			if strings.TrimSpace(place) == "" {
				return ErrBadSnapshot
			}
			set[place] = struct{}{}
		}
		known[userID] = set
	}

	seq := snap.Seq
	if seq < maxSeq {
		seq = maxSeq
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	d.windows = windows
	d.verdicts = verdicts
	d.known = known
	d.byID = byID
	d.seq = seq
	return nil
}

// isKnownPair reports whether both places are whitelisted for the user.
// Callers must hold d.mu.
func (d *ImpossibleTravelDetector) isKnownPair(userID, a, b string) bool {
	set := d.known[userID]
	if set == nil {
		return false
	}
	_, okA := set[a]
	_, okB := set[b]
	return okA && okB
}

// residentialPredecessor finds the newest residential event before position
// idx in a time sorted window.
func residentialPredecessor(win []LoginEvent, idx int) (LoginEvent, bool) {
	for i := idx - 1; i >= 0; i-- {
		if win[i].Source == SourceResidential {
			return win[i], true
		}
	}
	return LoginEvent{}, false
}

func validateEvent(e LoginEvent) error {
	if strings.TrimSpace(e.UserID) == "" || strings.TrimSpace(e.Place) == "" {
		return ErrBadEvent
	}
	if !validCoords(e.Lat, e.Lon) {
		return ErrBadEvent
	}
	switch e.Source {
	case SourceResidential, SourceDatacenter, SourceVPN:
		return nil
	}
	return ErrBadEvent
}

func validCoords(lat, lon float64) bool {
	if math.IsNaN(lat) || math.IsNaN(lon) {
		return false
	}
	return lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180
}

const earthRadiusKm = 6371.0

// haversineKm is the great circle distance between two coordinates.
func haversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	deg := math.Pi / 180
	dLat := (lat2 - lat1) * deg
	dLon := (lon2 - lon1) * deg
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*deg)*math.Cos(lat2*deg)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * earthRadiusKm * math.Asin(math.Sqrt(a))
}
