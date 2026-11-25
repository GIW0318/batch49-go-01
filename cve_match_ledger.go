// Package task matches normalized component coordinates against vulnerability
// records and explains every verdict it reaches.
package task

import (
	"errors"
	"sync"
)

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Match statuses.
const (
	StatusAffected      = "affected"
	StatusNotAffected   = "not_affected"
	StatusNotApplicable = "not_applicable"
)

// Reasons attached to a match or to an explanation trace.
const (
	ReasonAffectedRange  = "affected_range"
	ReasonBackportFix    = "backport_fix"
	ReasonManualOverride = "manual_override"
	ReasonAboveFixed     = "above_fixed"
	ReasonBelowIntro     = "below_introduced"
)

// Version ecosystems understood by the ledger.
const (
	EcosystemSemver = "semver"
	EcosystemDeb    = "deb"
	EcosystemRpm    = "rpm"
)

// Errors returned by the ledger.
var (
	ErrBadCoordinate = errors.New("coordinate is invalid")
	ErrBadVersion    = errors.New("version is malformed")
	ErrBadRecord     = errors.New("vulnerability record is invalid")
	ErrBadOverride   = errors.New("override reason is required")
	ErrUnknownCVE    = errors.New("unknown vulnerability record")
	ErrBadSnapshot   = errors.New("snapshot payload is invalid")
)

// Coordinate is one normalized component identity plus a concrete version.
type Coordinate struct {
	Vendor     string            `json:"vendor"`
	Package    string            `json:"package"`
	Version    string            `json:"version"`
	Ecosystem  string            `json:"ecosystem"`
	Distro     string            `json:"distro,omitempty"`
	Qualifiers map[string]string `json:"qualifiers,omitempty"`
}

// AffectedRange is a half open interval [Introduced, Fixed). A Fixed value of
// "*" means the record carries no upper bound of its own.
type AffectedRange struct {
	Introduced string `json:"introduced"`
	Fixed      string `json:"fixed"`
}

// UnaffectedRange records a version that is known to carry a backported fix.
type UnaffectedRange struct {
	Version  string `json:"version"`
	Backport bool   `json:"backport"`
	Reason   string `json:"reason,omitempty"`
}

// Record is one source specific vulnerability statement about one package.
type Record struct {
	CVE        string            `json:"cve"`
	Source     string            `json:"source"`
	UpdatedAt  int64             `json:"updated_at"`
	Vendor     string            `json:"vendor"`
	Package    string            `json:"package"`
	Ecosystem  string            `json:"ecosystem"`
	Distro     string            `json:"distro,omitempty"`
	Qualifiers map[string]string `json:"qualifiers,omitempty"`
	Affected   []AffectedRange   `json:"affected"`
	Unaffected []UnaffectedRange `json:"unaffected,omitempty"`
}

// Match is one verdict for one CVE.
type Match struct {
	CVE    string `json:"cve"`
	Status string `json:"status"`
	Reason string `json:"reason"`
	Source string `json:"source"`
}

// Comparison is one boundary comparison made while grading a coordinate.
type Comparison struct {
	Target string `json:"target"`
	Cmp    int    `json:"cmp"`
}

// Trace explains how one CVE was graded for one coordinate.
type Trace struct {
	CVE         string       `json:"cve"`
	Coordinate  Coordinate   `json:"coordinate"`
	Status      string       `json:"status"`
	Reason      string       `json:"reason"`
	Source      string       `json:"source"`
	Comparisons []Comparison `json:"comparisons"`
	Shadowed    []Record     `json:"shadowed"`
}

// CveMatchLedger owns the record set, the source policy and the overrides.
type CveMatchLedger struct {
	mu sync.Mutex
}

// NewCveMatchLedger builds a ledger with a source priority list, highest
// priority first.
func NewCveMatchLedger(priority []string) *CveMatchLedger {
	return &CveMatchLedger{}
}

// CompareVersions orders two versions of one ecosystem, returning -1, 0 or 1.
func CompareVersions(ecosystem, a, b string) (int, error) {
	return 0, errNotImplemented
}

// NormalizeCoordinate lowercases and trims a coordinate and validates its
// required fields.
func NormalizeCoordinate(coord Coordinate) (Coordinate, error) {
	return Coordinate{}, errNotImplemented
}

// Load replaces the whole record set atomically.
func (l *CveMatchLedger) Load(records []Record) error {
	return errNotImplemented
}

// Records lists the known CVE ids in ascending order.
func (l *CveMatchLedger) Records() []string {
	return nil
}

// Match grades one coordinate against every applicable record.
func (l *CveMatchLedger) Match(coord Coordinate) ([]Match, error) {
	return nil, errNotImplemented
}

// Explain reports the boundary comparisons behind one CVE verdict.
func (l *CveMatchLedger) Explain(cveID string, coord Coordinate) (Trace, error) {
	return Trace{}, errNotImplemented
}

// Override records a manual, per coordinate verdict for one CVE.
func (l *CveMatchLedger) Override(cveID string, coord Coordinate, reason string) error {
	return errNotImplemented
}

// Withdraw removes one CVE from the record set.
func (l *CveMatchLedger) Withdraw(cveID string) error {
	return errNotImplemented
}

// Snapshot serialises records, the source policy and every override.
func (l *CveMatchLedger) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole ledger state with a snapshot, all or nothing.
func (l *CveMatchLedger) Restore(data []byte) error {
	return errNotImplemented
}
