// Package task detects impossible travel from login geography and time, with a
// bounded per user event window.
package task

import (
	"errors"
	"time"
)

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

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
	cfg Config
}

// NewImpossibleTravelDetector validates the config and builds a detector.
func NewImpossibleTravelDetector(cfg Config) (*ImpossibleTravelDetector, error) {
	return nil, errNotImplemented
}

// Observe records one located login and grades the move from the previous one.
func (d *ImpossibleTravelDetector) Observe(e LoginEvent) (Verdict, error) {
	return Verdict{}, errNotImplemented
}

// Verdicts lists the retained verdicts of one user, oldest first.
func (d *ImpossibleTravelDetector) Verdicts(userID string) []Verdict {
	return nil
}

// MarkKnownLocation adds one place to the per user whitelist.
func (d *ImpossibleTravelDetector) MarkKnownLocation(userID, place string) error {
	return errNotImplemented
}

// KnownLocations lists the whitelisted places of one user, sorted by name.
func (d *ImpossibleTravelDetector) KnownLocations(userID string) []string {
	return nil
}

// Dismiss clears a flagged verdict with an operator and a reason.
func (d *ImpossibleTravelDetector) Dismiss(verdictID, actorID, reason string) error {
	return errNotImplemented
}

// WindowSize reports how many events the user window currently holds.
func (d *ImpossibleTravelDetector) WindowSize(userID string) int {
	return 0
}

// DistanceKm returns the great circle distance between two located events.
func (d *ImpossibleTravelDetector) DistanceKm(a, b LoginEvent) (float64, error) {
	return 0, errNotImplemented
}

// Snapshot serialises windows, verdicts, whitelists and the sequence watermark.
func (d *ImpossibleTravelDetector) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// LoadSnapshot replaces the whole state with a snapshot, all or nothing.
func (d *ImpossibleTravelDetector) LoadSnapshot(data []byte) error {
	return errNotImplemented
}
