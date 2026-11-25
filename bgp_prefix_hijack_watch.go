// Package task watches BGP announcements against a learned baseline of
// legitimate prefix origins: it scores origin, prefix length, path shape and
// policy violations, and keeps per peer counters plus restart safe snapshots.
package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Verdict levels.
const (
	LevelClean      = "clean"
	LevelSuspected  = "suspected"
	LevelConfirmed  = "confirmed"
	LevelSuppressed = "suppressed"
)

// Reason codes attached to a verdict.
const (
	ReasonOriginMismatch = "origin_mismatch"
	ReasonMoreSpecific   = "more_specific"
	ReasonUncovered      = "uncovered_prefix"
	ReasonPathSpoofed    = "path_spoofed"
	ReasonPrivateAS      = "private_as"
	ReasonPathLoop       = "path_loop"
)

// Score weights and level thresholds.
const (
	ScoreOriginMismatch = 50
	ScoreMoreSpecific   = 30
	ScoreUncovered      = 20
	ScorePathSpoofed    = 40
	ScorePrivateAS      = 20
	ScorePathLoop       = 25

	SuspectedThreshold = 30
	ConfirmedThreshold = 70
)

// Errors returned by the watch.
var (
	ErrBadConfig        = errors.New("watch config is invalid")
	ErrBadPrefix        = errors.New("prefix is not a canonical cidr")
	ErrBadRoute         = errors.New("route is invalid")
	ErrOriginConflict   = errors.New("prefix already learned with another origin")
	ErrBaselineLimit    = errors.New("baseline limit reached")
	ErrBaselineNotFound = errors.New("baseline not found")
	ErrBadSuppression   = errors.New("suppression window is invalid")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

// Config bounds one watch instance.
type Config struct {
	MaxBaselines         int
	MaxHistory           int
	AllowMultipleOrigins bool
	Now                  func() int64
}

// Baseline is one learned legitimate prefix.
type Baseline struct {
	Prefix    string
	OriginAS  uint32
	MaxLength int
}

// Route is one observed announcement.
type Route struct {
	Prefix   string
	OriginAS uint32
	ASPath   []uint32
	PeerAS   uint32
	Seq      uint64
	At       int64
}

// Verdict is the hijack assessment of one announcement.
type Verdict struct {
	Prefix     string
	Level      string
	Reasons    []string
	Score      int
	Suppressed bool
}

// PeerStat is the per peer counter record.
type PeerStat struct {
	PeerAS       uint32
	Observations uint64
	Hijacks      uint64
}

// PrefixHijackWatch owns the baseline, the history, the suppression windows
// and the per peer counters.
type PrefixHijackWatch struct {
	cfg Config
}

// NewPrefixHijackWatch validates the configuration and builds an empty watch.
func NewPrefixHijackWatch(cfg Config) (*PrefixHijackWatch, error) {
	return nil, errNotImplemented
}

// Learn registers one legitimate baseline. Learning the same prefix and origin
// again is an idempotent update.
func (w *PrefixHijackWatch) Learn(b Baseline) error {
	return errNotImplemented
}

// RemoveBaseline withdraws one baseline, keeping its recorded history.
func (w *PrefixHijackWatch) RemoveBaseline(prefix string) error {
	return errNotImplemented
}

// BaselineFor reports the primary baseline of a prefix.
func (w *PrefixHijackWatch) BaselineFor(prefix string) (Baseline, bool) {
	return Baseline{}, false
}

// Observe scores one announcement and updates the history and the counters.
func (w *PrefixHijackWatch) Observe(route Route) (Verdict, error) {
	return Verdict{}, errNotImplemented
}

// History lists the non suppressed verdicts of one prefix in arrival order.
func (w *PrefixHijackWatch) History(prefix string) []Verdict {
	return nil
}

// Suppress silences one prefix until the given unix time, extending an
// existing window when the new deadline is later.
func (w *PrefixHijackWatch) Suppress(prefix string, until int64) error {
	return errNotImplemented
}

// Suppressed reports whether one prefix is inside a suppression window now.
func (w *PrefixHijackWatch) Suppressed(prefix string) bool {
	return false
}

// PeerStats reports the counters of one peer.
func (w *PrefixHijackWatch) PeerStats(peerAS uint32) PeerStat {
	return PeerStat{}
}

// TopOffenders lists the peers with hijack hits, worst first, ties broken by
// the smaller AS number.
func (w *PrefixHijackWatch) TopOffenders(n int) []uint32 {
	return nil
}

// Snapshot serialises the baseline, the history, the suppression windows and
// the peer counters.
func (w *PrefixHijackWatch) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (w *PrefixHijackWatch) Restore(data []byte) error {
	return errNotImplemented
}
