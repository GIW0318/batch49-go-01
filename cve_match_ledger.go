// Package task matches normalized component coordinates against vulnerability
// records and explains every verdict it reaches.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

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

// wildcardFixed marks an affected range with no upper bound.
const wildcardFixed = "*"

// snapshotFormat tags snapshot payloads produced by this ledger.
const snapshotFormat = "cve-match-ledger-snapshot-v1"

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
	mu        sync.Mutex
	priority  []string
	records   map[string][]Record
	overrides map[string]map[string]string
	// muted identities are silenced in Match after an explicit Withdraw. A
	// full Load starts a fresh record set and clears the silences.
	muted map[string]bool
}

// snapshotEnvelope is the on-disk representation of a ledger.
type snapshotEnvelope struct {
	Format    string                       `json:"format"`
	Priority  []string                     `json:"priority,omitempty"`
	Records   []Record                     `json:"records"`
	Overrides map[string]map[string]string `json:"overrides,omitempty"`
	Muted     []string                     `json:"muted,omitempty"`
}

// NewCveMatchLedger builds a ledger with a source priority list, highest
// priority first.
func NewCveMatchLedger(priority []string) *CveMatchLedger {
	p := make([]string, len(priority))
	for i, s := range priority {
		p[i] = strings.TrimSpace(s)
	}
	return &CveMatchLedger{
		priority:  p,
		records:   make(map[string][]Record),
		overrides: make(map[string]map[string]string),
		muted:     make(map[string]bool),
	}
}

// ---------------------------------------------------------------------------
// Version comparison
// ---------------------------------------------------------------------------

func normalizeEcosystem(raw string) (string, bool) {
	eco := strings.ToLower(strings.TrimSpace(raw))
	switch eco {
	case EcosystemSemver, EcosystemDeb, EcosystemRpm:
		return eco, true
	default:
		return "", false
	}
}

func hasInternalWhitespace(s string) bool {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ' ', '\t', '\n', '\r', '\v', '\f':
			return true
		}
	}
	return false
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// normalizeDigits strips leading zeroes so numeric runs compare by length.
func normalizeDigits(s string) string {
	s = strings.TrimLeft(s, "0")
	if s == "" {
		return "0"
	}
	return s
}

// compareNumericStrings orders two non-empty digit strings numerically.
func compareNumericStrings(a, b string) int {
	a = normalizeDigits(a)
	b = normalizeDigits(b)
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}

type semverIdentifier struct {
	numeric bool
	value   string
}

type semverVersion struct {
	core   [3]string
	pre    []semverIdentifier
	hasPre bool
}

func parseSemver(raw string) (semverVersion, error) {
	var v semverVersion
	s := raw
	if i := strings.IndexByte(s, '+'); i >= 0 {
		// Build metadata is ignored entirely.
		s = s[:i]
	}
	core := s
	pre := ""
	if i := strings.IndexByte(s, '-'); i >= 0 {
		core = s[:i]
		pre = s[i+1:]
		v.hasPre = true
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return v, fmt.Errorf("%w: semver %q must have major.minor.patch", ErrBadVersion, raw)
	}
	for i, p := range parts {
		if !allDigits(p) {
			return v, fmt.Errorf("%w: semver segment %q is not numeric", ErrBadVersion, p)
		}
		v.core[i] = normalizeDigits(p)
	}
	if v.hasPre {
		if pre == "" {
			return v, fmt.Errorf("%w: empty prerelease in %q", ErrBadVersion, raw)
		}
		for _, id := range strings.Split(pre, ".") {
			if id == "" {
				return v, fmt.Errorf("%w: empty prerelease identifier in %q", ErrBadVersion, raw)
			}
			for _, r := range id {
				if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'z') &&
					!(r >= 'A' && r <= 'Z') && r != '-' {
					return v, fmt.Errorf("%w: bad prerelease identifier %q", ErrBadVersion, id)
				}
			}
			v.pre = append(v.pre, semverIdentifier{numeric: allDigits(id), value: id})
		}
	}
	return v, nil
}

func compareSemver(a, b semverVersion) int {
	for i := 0; i < 3; i++ {
		if c := compareNumericStrings(a.core[i], b.core[i]); c != 0 {
			return c
		}
	}
	switch {
	case !a.hasPre && !b.hasPre:
		return 0
	case a.hasPre && !b.hasPre:
		// A prerelease sorts below the matching final release.
		return -1
	case !a.hasPre && b.hasPre:
		return 1
	}
	n := len(a.pre)
	if len(b.pre) < n {
		n = len(b.pre)
	}
	for i := 0; i < n; i++ {
		ai, bi := a.pre[i], b.pre[i]
		switch {
		case ai.numeric && bi.numeric:
			if c := compareNumericStrings(ai.value, bi.value); c != 0 {
				return c
			}
		case ai.numeric != bi.numeric:
			// Numeric identifiers have lower precedence than alphanumeric.
			if ai.numeric {
				return -1
			}
			return 1
		default:
			if c := strings.Compare(ai.value, bi.value); c != 0 {
				return c
			}
		}
	}
	switch {
	case len(a.pre) < len(b.pre):
		return -1
	case len(a.pre) > len(b.pre):
		return 1
	default:
		return 0
	}
}

// debOrder is the Debian lexical order: tilde sorts before everything
// (including the end of a part), letters sort before other punctuation.
func debOrder(c byte) int {
	switch {
	case c == '~':
		return -1
	case c >= '0' && c <= '9':
		return 0
	case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
		return int(c)
	default:
		return int(c) + 256
	}
}

func isASCIIDigit(c byte) bool {
	return c >= '0' && c <= '9'
}

// compareDebChunk implements the alternating non-digit/digit Debian comparison.
func compareDebChunk(a, b string) int {
	ia, ib := 0, 0
	for ia < len(a) || ib < len(b) {
		// Leading non-digit runs compared under the modified lexical order.
		for (ia < len(a) && !isASCIIDigit(a[ia])) || (ib < len(b) && !isASCIIDigit(b[ib])) {
			ca, cb := 0, 0 // exhausted side behaves like the zero/end marker
			if ia < len(a) {
				ca = debOrder(a[ia])
			}
			if ib < len(b) {
				cb = debOrder(b[ib])
			}
			if ca < cb {
				return -1
			}
			if ca > cb {
				return 1
			}
			ia++
			ib++
		}
		// Digit runs: skip leading zeroes then compare numerically.
		for ia < len(a) && a[ia] == '0' {
			ia++
		}
		for ib < len(b) && b[ib] == '0' {
			ib++
		}
		firstDiff := 0
		for ia < len(a) && isASCIIDigit(a[ia]) && ib < len(b) && isASCIIDigit(b[ib]) {
			if firstDiff == 0 && a[ia] != b[ib] {
				if a[ia] < b[ib] {
					firstDiff = -1
				} else {
					firstDiff = 1
				}
			}
			ia++
			ib++
		}
		if ia < len(a) && isASCIIDigit(a[ia]) {
			return 1
		}
		if ib < len(b) && isASCIIDigit(b[ib]) {
			return -1
		}
		if firstDiff != 0 {
			return firstDiff
		}
	}
	return 0
}

// splitEpoch separates an optional numeric epoch prefix from a deb/rpm version.
func splitEpoch(raw string) (string, string, error) {
	if i := strings.IndexByte(raw, ':'); i >= 0 {
		epoch := raw[:i]
		rest := raw[i+1:]
		if !allDigits(epoch) || rest == "" {
			return "", "", fmt.Errorf("%w: bad epoch in %q", ErrBadVersion, raw)
		}
		return normalizeDigits(epoch), rest, nil
	}
	return "0", raw, nil
}

// splitRevision splits the upstream version from the trailing revision at the
// last hyphen. A missing revision is the empty string.
func splitRevision(rest string) (string, string) {
	if i := strings.LastIndexByte(rest, '-'); i >= 0 {
		return rest[:i], rest[i+1:]
	}
	return rest, ""
}

func compareDistro(a, b string) (int, error) {
	ea, ra, err := splitEpoch(a)
	if err != nil {
		return 0, err
	}
	eb, rb, err := splitEpoch(b)
	if err != nil {
		return 0, err
	}
	if c := compareNumericStrings(ea, eb); c != 0 {
		return c, nil
	}
	ua, revA := splitRevision(ra)
	ub, revB := splitRevision(rb)
	if c := compareDebChunk(ua, ub); c != 0 {
		return c, nil
	}
	if c := compareDebChunk(revA, revB); c != 0 {
		return c, nil
	}
	return 0, nil
}

// CompareVersions orders two versions of one ecosystem, returning -1, 0 or 1.
func CompareVersions(ecosystem, a, b string) (int, error) {
	eco, ok := normalizeEcosystem(ecosystem)
	if !ok {
		return 0, fmt.Errorf("%w: unknown ecosystem %q", ErrBadVersion, ecosystem)
	}
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" || hasInternalWhitespace(a) || hasInternalWhitespace(b) {
		return 0, fmt.Errorf("%w: empty or malformed version", ErrBadVersion)
	}
	switch eco {
	case EcosystemSemver:
		va, err := parseSemver(a)
		if err != nil {
			return 0, err
		}
		vb, err := parseSemver(b)
		if err != nil {
			return 0, err
		}
		return compareSemver(va, vb), nil
	case EcosystemDeb, EcosystemRpm:
		return compareDistro(a, b)
	default:
		return 0, fmt.Errorf("%w: unknown ecosystem %q", ErrBadVersion, ecosystem)
	}
}

func versionShapeOK(ecosystem, v string) error {
	if _, err := CompareVersions(ecosystem, v, v); err != nil {
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// Normalization
// ---------------------------------------------------------------------------

func normalizeQualifiers(q map[string]string) map[string]string {
	if len(q) == 0 {
		return nil
	}
	out := make(map[string]string, len(q))
	for k, v := range q {
		nk := strings.ToLower(strings.TrimSpace(k))
		nv := strings.TrimSpace(v)
		if nk == "" {
			continue
		}
		out[nk] = nv
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// NormalizeCoordinate lowercases and trims a coordinate and validates its
// required fields.
func NormalizeCoordinate(coord Coordinate) (Coordinate, error) {
	eco, ok := normalizeEcosystem(coord.Ecosystem)
	if !ok {
		return Coordinate{}, fmt.Errorf("%w: unknown ecosystem %q", ErrBadCoordinate, coord.Ecosystem)
	}
	nc := Coordinate{
		Vendor:     strings.ToLower(strings.TrimSpace(coord.Vendor)),
		Package:    strings.ToLower(strings.TrimSpace(coord.Package)),
		Version:    strings.TrimSpace(coord.Version),
		Ecosystem:  eco,
		Distro:     strings.ToLower(strings.TrimSpace(coord.Distro)),
		Qualifiers: normalizeQualifiers(coord.Qualifiers),
	}
	switch {
	case nc.Vendor == "":
		return Coordinate{}, fmt.Errorf("%w: vendor is required", ErrBadCoordinate)
	case nc.Package == "":
		return Coordinate{}, fmt.Errorf("%w: package is required", ErrBadCoordinate)
	case nc.Version == "":
		return Coordinate{}, fmt.Errorf("%w: version is required", ErrBadCoordinate)
	}
	return nc, nil
}

// coordinateKey renders a normalized coordinate as a stable map key.
func coordinateKey(nc Coordinate) string {
	b, err := json.Marshal(nc)
	if err != nil {
		// All fields are strings / string maps, so marshalling cannot fail.
		return ""
	}
	return string(b)
}

// identityKey identifies the vendor/package/ecosystem/distro tuple without a
// concrete version, used for withdrawal silencing.
func identityKey(vendor, pkg, eco, distro string) string {
	return strings.Join([]string{vendor, pkg, eco, distro}, "\x00")
}

// coordinateIdentityKey is the identity key of a normalized coordinate.
func coordinateIdentityKey(c Coordinate) string {
	return identityKey(c.Vendor, c.Package, c.Ecosystem, c.Distro)
}

// ---------------------------------------------------------------------------
// Record validation and loading
// ---------------------------------------------------------------------------

func validateRecord(in Record) (Record, error) {
	r := in
	// Isolate reference types from the caller's input so normalisation never
	// mutates a slice/map that is also read through the live record set.
	if in.Affected != nil {
		r.Affected = append([]AffectedRange(nil), in.Affected...)
	}
	if in.Unaffected != nil {
		r.Unaffected = append([]UnaffectedRange(nil), in.Unaffected...)
	}
	r.CVE = strings.TrimSpace(r.CVE)
	if r.CVE == "" {
		return r, fmt.Errorf("%w: cve id is required", ErrBadRecord)
	}
	r.Source = strings.TrimSpace(r.Source)
	r.Vendor = strings.ToLower(strings.TrimSpace(r.Vendor))
	r.Package = strings.ToLower(strings.TrimSpace(r.Package))
	if r.Package == "" {
		return r, fmt.Errorf("%w: package is required for %s", ErrBadRecord, r.CVE)
	}
	eco, ok := normalizeEcosystem(r.Ecosystem)
	if !ok {
		return r, fmt.Errorf("%w: unknown ecosystem %q for %s", ErrBadRecord, r.Ecosystem, r.CVE)
	}
	r.Ecosystem = eco
	r.Distro = strings.ToLower(strings.TrimSpace(r.Distro))
	r.Qualifiers = normalizeQualifiers(r.Qualifiers)

	for i := range r.Affected {
		intro := strings.TrimSpace(r.Affected[i].Introduced)
		fixed := strings.TrimSpace(r.Affected[i].Fixed)
		if err := versionShapeOK(eco, intro); err != nil {
			return r, fmt.Errorf("%w: bad introduced bound for %s: %v", ErrBadRecord, r.CVE, err)
		}
		r.Affected[i].Introduced = intro
		r.Affected[i].Fixed = fixed
		if fixed != wildcardFixed {
			if err := versionShapeOK(eco, fixed); err != nil {
				return r, fmt.Errorf("%w: bad fixed bound for %s: %v", ErrBadRecord, r.CVE, err)
			}
			cmp, err := CompareVersions(eco, intro, fixed)
			if err != nil {
				return r, fmt.Errorf("%w: %v", ErrBadRecord, err)
			}
			if cmp >= 0 {
				return r, fmt.Errorf("%w: interval [%s,%s) must be increasing for %s",
					ErrBadRecord, intro, fixed, r.CVE)
			}
		}
	}
	for i := range r.Unaffected {
		v := strings.TrimSpace(r.Unaffected[i].Version)
		if err := versionShapeOK(eco, v); err != nil {
			return r, fmt.Errorf("%w: bad unaffected version for %s: %v", ErrBadRecord, r.CVE, err)
		}
		r.Unaffected[i].Version = v
		r.Unaffected[i].Reason = strings.TrimSpace(r.Unaffected[i].Reason)
	}
	return r, nil
}

// buildRecords validates a whole batch and returns a fresh record set. Any
// invalid record aborts the batch before the live state is touched.
func buildRecords(records []Record) (map[string][]Record, error) {
	built := make(map[string][]Record)
	seenSource := make(map[string]bool)
	for _, rec := range records {
		nr, err := validateRecord(rec)
		if err != nil {
			return nil, err
		}
		uniq := nr.CVE + "\x00" + nr.Source
		if seenSource[uniq] {
			return nil, fmt.Errorf("%w: duplicate source %q for %s", ErrBadRecord, nr.Source, nr.CVE)
		}
		seenSource[uniq] = true
		built[nr.CVE] = append(built[nr.CVE], nr)
	}
	return built, nil
}

// Load replaces the whole record set atomically.
func (l *CveMatchLedger) Load(records []Record) error {
	built, err := buildRecords(records)
	if err != nil {
		return err
	}
	l.mu.Lock()
	l.records = built
	l.muted = make(map[string]bool)
	// Manual verdicts are deliberately retained across source syncs.
	l.mu.Unlock()
	return nil
}

// Records lists the known CVE ids in ascending order.
func (l *CveMatchLedger) Records() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	ids := make([]string, 0, len(l.records))
	for id := range l.records {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// ---------------------------------------------------------------------------
// Applicability, source selection and grading
// ---------------------------------------------------------------------------

// recordApplies reports whether a normalized record identity covers the
// normalized coordinate.
func recordApplies(r Record, c Coordinate) bool {
	if r.Vendor != c.Vendor || r.Package != c.Package || r.Ecosystem != c.Ecosystem {
		return false
	}
	// A distro qualified record only applies to that exact distro. A record
	// without a distro qualifier is generic and applies to any distro. An
	// empty distro coordinate therefore never matches a distro bound record.
	if r.Distro != "" && r.Distro != c.Distro {
		return false
	}
	for k, rv := range r.Qualifiers {
		cv, ok := c.Qualifiers[k]
		if !ok || cv != rv {
			return false
		}
	}
	return true
}

func (l *CveMatchLedger) priorityRank(source string) int {
	for i, s := range l.priority {
		if s == source {
			return i
		}
	}
	// Unlisted sources share the rank just below every listed source.
	return len(l.priority)
}

// selectWinner picks one applicable record by source priority, breaking ties
// with the newer update timestamp. Other applicable records are the shadowed
// losers, kept in load order.
func (l *CveMatchLedger) selectWinner(applicable []Record) (Record, []Record) {
	winnerIdx := 0
	for i := 1; i < len(applicable); i++ {
		cur := applicable[i]
		best := applicable[winnerIdx]
		rc := l.priorityRank(cur.Source)
		rb := l.priorityRank(best.Source)
		switch {
		case rc < rb:
			winnerIdx = i
		case rc == rb && cur.UpdatedAt > best.UpdatedAt:
			winnerIdx = i
		}
	}
	winner := applicable[winnerIdx]
	shadowed := make([]Record, 0, len(applicable)-1)
	for i, r := range applicable {
		if i != winnerIdx {
			shadowed = append(shadowed, r)
		}
	}
	return winner, shadowed
}

type grade struct {
	status      string
	reason      string
	comparisons []Comparison
}

func sign1(c int) int {
	switch {
	case c < 0:
		return -1
	case c > 0:
		return 1
	default:
		return 0
	}
}

// gradeRecord evaluates one selected record against the coordinate.
func gradeRecord(r Record, c Coordinate) (grade, error) {
	g := grade{comparisons: []Comparison{}}
	affected := false
	crossedFixed := false

	for _, ar := range r.Affected {
		cmpIntroRaw, err := CompareVersions(r.Ecosystem, c.Version, ar.Introduced)
		if err != nil {
			return g, err
		}
		cmpIntro := sign1(cmpIntroRaw)
		g.comparisons = append(g.comparisons, Comparison{Target: ar.Introduced, Cmp: cmpIntro})

		if ar.Fixed == wildcardFixed {
			// No upper bound: represent infinity so every concrete version is
			// strictly below it.
			g.comparisons = append(g.comparisons, Comparison{Target: wildcardFixed, Cmp: -1})
			if cmpIntro >= 0 {
				affected = true
			}
			continue
		}

		cmpFixedRaw, err := CompareVersions(r.Ecosystem, c.Version, ar.Fixed)
		if err != nil {
			return g, err
		}
		cmpFixed := sign1(cmpFixedRaw)
		g.comparisons = append(g.comparisons, Comparison{Target: ar.Fixed, Cmp: cmpFixed})
		if cmpFixed >= 0 {
			crossedFixed = true
		}
		if cmpIntro >= 0 && cmpFixed < 0 {
			affected = true
		}
	}

	// A backported fix at or below the installed version is a safe verdict
	// even when the version still sits inside an upstream affected interval.
	backport := false
	for _, ur := range r.Unaffected {
		if !ur.Backport {
			continue
		}
		cmpVRaw, err := CompareVersions(r.Ecosystem, c.Version, ur.Version)
		if err != nil {
			return g, err
		}
		if sign1(cmpVRaw) >= 0 {
			backport = true
			break
		}
	}

	switch {
	case affected && backport:
		// The coordinate sits in an affected interval but also reaches a
		// backported fix: the safe verdict wins.
		g.status = StatusNotAffected
		g.reason = ReasonBackportFix
	case affected:
		g.status = StatusAffected
		g.reason = ReasonAffectedRange
	case crossedFixed:
		g.status = StatusNotApplicable
		g.reason = ReasonAboveFixed
	default:
		g.status = StatusNotApplicable
		g.reason = ReasonBelowIntro
	}
	return g, nil
}

func (l *CveMatchLedger) applicableLocked(cve string, c Coordinate) []Record {
	out := make([]Record, 0)
	for _, r := range l.records[cve] {
		if recordApplies(r, c) {
			out = append(out, r)
		}
	}
	return out
}

// Match grades one coordinate against every applicable record.
func (l *CveMatchLedger) Match(coord Coordinate) ([]Match, error) {
	nc, err := NormalizeCoordinate(coord)
	if err != nil {
		return nil, err
	}
	if err := versionShapeOK(nc.Ecosystem, nc.Version); err != nil {
		return nil, err
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	// An explicit withdrawal silences fresh verdicts for the exact normalized
	// package identity it described until the next full Load.
	if l.muted[coordinateIdentityKey(nc)] {
		return []Match{}, nil
	}

	cves := make([]string, 0, len(l.records))
	for cve := range l.records {
		cves = append(cves, cve)
	}
	sort.Strings(cves)

	out := make([]Match, 0)
	key := coordinateKey(nc)
	for _, cve := range cves {
		applicable := l.applicableLocked(cve, nc)
		var winner Record
		hasWinner := len(applicable) > 0
		if hasWinner {
			winner, _ = l.selectWinner(applicable)
		}
		source := ""
		if hasWinner {
			source = winner.Source
		}

		if reasons, ok := l.overrides[cve]; ok {
			if _, overridden := reasons[key]; overridden {
				out = append(out, Match{CVE: cve, Status: StatusNotAffected, Reason: ReasonManualOverride, Source: source})
				continue
			}
		}
		if !hasWinner {
			continue
		}
		g, err := gradeRecord(winner, nc)
		if err != nil {
			return nil, err
		}
		if g.status == StatusAffected || g.status == StatusNotAffected {
			out = append(out, Match{CVE: cve, Status: g.status, Reason: g.reason, Source: source})
		}
	}
	return out, nil
}

// Explain reports the boundary comparisons behind one CVE verdict.
func (l *CveMatchLedger) Explain(cveID string, coord Coordinate) (Trace, error) {
	id := strings.TrimSpace(cveID)

	l.mu.Lock()
	defer l.mu.Unlock()

	records, known := l.records[id]
	if !known {
		return Trace{}, fmt.Errorf("%w: %q", ErrUnknownCVE, cveID)
	}
	nc, err := NormalizeCoordinate(coord)
	if err != nil {
		return Trace{}, err
	}
	if err := versionShapeOK(nc.Ecosystem, nc.Version); err != nil {
		return Trace{}, err
	}

	trace := Trace{CVE: id, Coordinate: nc, Comparisons: []Comparison{}, Shadowed: []Record{}}
	applicable := make([]Record, 0)
	for _, r := range records {
		if recordApplies(r, nc) {
			applicable = append(applicable, r)
		}
	}
	hasWinner := len(applicable) > 0
	var winner Record
	if hasWinner {
		var shadowed []Record
		winner, shadowed = l.selectWinner(applicable)
		trace.Source = winner.Source
		trace.Shadowed = shadowed
	}

	if reasons, ok := l.overrides[id]; ok {
		if _, overridden := reasons[coordinateKey(nc)]; overridden {
			trace.Status = StatusNotAffected
			trace.Reason = ReasonManualOverride
			if hasWinner {
				g, gerr := gradeRecord(winner, nc)
				if gerr != nil {
					return Trace{}, gerr
				}
				trace.Comparisons = g.comparisons
			}
			return trace, nil
		}
	}

	if !hasWinner {
		trace.Status = StatusNotApplicable
		return trace, nil
	}

	g, err := gradeRecord(winner, nc)
	if err != nil {
		return Trace{}, err
	}
	trace.Status = g.status
	trace.Reason = g.reason
	trace.Comparisons = g.comparisons
	return trace, nil
}

// Override records a manual, per coordinate verdict for one CVE.
func (l *CveMatchLedger) Override(cveID string, coord Coordinate, reason string) error {
	nc, err := NormalizeCoordinate(coord)
	if err != nil {
		return err
	}
	if strings.TrimSpace(reason) == "" {
		return ErrBadOverride
	}
	id := strings.TrimSpace(cveID)

	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.records[id]; !ok {
		return fmt.Errorf("%w: %q", ErrUnknownCVE, cveID)
	}
	if l.overrides[id] == nil {
		l.overrides[id] = make(map[string]string)
	}
	l.overrides[id][coordinateKey(nc)] = strings.TrimSpace(reason)
	return nil
}

// Withdraw removes one CVE from the record set.
func (l *CveMatchLedger) Withdraw(cveID string) error {
	id := strings.TrimSpace(cveID)
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.records[id]; !ok {
		return fmt.Errorf("%w: %q", ErrUnknownCVE, cveID)
	}
	for _, r := range l.records[id] {
		l.muted[identityKey(r.Vendor, r.Package, r.Ecosystem, r.Distro)] = true
	}
	delete(l.records, id)
	delete(l.overrides, id)
	return nil
}

// Snapshot serialises records, the source policy and every override.
func (l *CveMatchLedger) Snapshot() ([]byte, error) {
	l.mu.Lock()
	ids := make([]string, 0, len(l.records))
	for id := range l.records {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	recs := make([]Record, 0)
	for _, id := range ids {
		recs = append(recs, l.records[id]...)
	}
	priority := append([]string(nil), l.priority...)
	overrides := make(map[string]map[string]string, len(l.overrides))
	for cve, m := range l.overrides {
		cp := make(map[string]string, len(m))
		for k, v := range m {
			cp[k] = v
		}
		overrides[cve] = cp
	}
	muted := make([]string, 0, len(l.muted))
	for k := range l.muted {
		muted = append(muted, k)
	}
	sort.Strings(muted)
	l.mu.Unlock()

	env := snapshotEnvelope{
		Format:    snapshotFormat,
		Priority:  priority,
		Records:   recs,
		Overrides: overrides,
		Muted:     muted,
	}
	return json.Marshal(env)
}

// Restore replaces the whole ledger state with a snapshot, all or nothing.
func (l *CveMatchLedger) Restore(data []byte) error {
	if len(strings.TrimSpace(string(data))) == 0 {
		return fmt.Errorf("%w: empty payload", ErrBadSnapshot)
	}
	var env snapshotEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	if env.Format != snapshotFormat {
		return fmt.Errorf("%w: missing or unsupported snapshot format", ErrBadSnapshot)
	}
	built, err := buildRecords(env.Records)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	overrides := make(map[string]map[string]string, len(env.Overrides))
	for cve, m := range env.Overrides {
		cp := make(map[string]string, len(m))
		for k, reason := range m {
			if strings.TrimSpace(reason) == "" {
				return fmt.Errorf("%w: empty override reason for %s", ErrBadSnapshot, cve)
			}
			cp[k] = reason
		}
		overrides[cve] = cp
	}
	priority := append([]string(nil), env.Priority...)
	muted := make(map[string]bool, len(env.Muted))
	for _, k := range env.Muted {
		if strings.TrimSpace(k) != "" {
			muted[k] = true
		}
	}

	l.mu.Lock()
	l.records = built
	l.overrides = overrides
	l.priority = priority
	l.muted = muted
	l.mu.Unlock()
	return nil
}
