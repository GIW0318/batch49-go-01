// Package task maps nightly build artefacts onto a component release timeline
// and decides whether the attributed version falls inside a CVE range.
package task

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

// Basis values recorded on an attribution.
const (
	BasisDeclared       = "declared"
	BasisTime           = "time"
	BasisCommit         = "commit"
	BasisBeforeTimeline = "before_timeline"
	BasisAfterTimeline  = "after_timeline"
)

// Confidence anchors used by the attributor.
const (
	ConfidenceDeclared      = 100
	ConfidenceTime          = 90
	ConfidenceCommit        = 70
	ConfidenceAfterTimeline = 60
	ConfidenceUnlocated     = 10
)

// Reason codes attached to a CVE verdict.
const (
	ReasonInRange       = "in_range"
	ReasonOutsideRanges = "outside_ranges"
	ReasonUnlocated     = "unlocated"
)

// Errors surfaced by the attributor.
var (
	ErrBlankField       = errors.New("component, version and build id are required")
	ErrDuplicateRelease = errors.New("release already registered for this component")
	ErrDuplicateCommit  = errors.New("commit is already bound to a release")
	ErrUnknownComponent = errors.New("unknown component")
	ErrUnknownVersion   = errors.New("unknown release version")
	ErrUnknownCommit    = errors.New("unknown commit for this component")
	ErrTimelineConflict = errors.New("declared version conflicts with the release timeline")
	ErrBadRange         = errors.New("cve range endpoints are invalid")
	ErrUnknownID        = errors.New("unknown attribution id")
)

// Release is one tag on a component timeline.
type Release struct {
	Component  string    `json:"component"`
	Version    string    `json:"version"`
	At         time.Time `json:"at"`
	Prerelease bool      `json:"prerelease"`
	Commit     string    `json:"commit"`
	Seq        int       `json:"seq"`
}

// BuildRef is the artefact handed in by the build system.
type BuildRef struct {
	ID           string    `json:"id"`
	Component    string    `json:"component"`
	Version      string    `json:"version"`
	BuiltAt      time.Time `json:"built_at"`
	Commit       string    `json:"commit"`
	ClockTrusted bool      `json:"clock_trusted"`
}

// Attribution is the located release and the evidence behind it.
type Attribution struct {
	ID         string `json:"id"`
	Component  string `json:"component"`
	Version    string `json:"version"`
	Basis      string `json:"basis"`
	Confidence int    `json:"confidence"`
	Outside    bool   `json:"outside"`
}

// Range is one CVE affected interval. Fixed may stay empty.
type Range struct {
	Introduced string `json:"introduced"`
	Fixed      string `json:"fixed"`
}

// Verdict is the CVE answer for one build.
type Verdict struct {
	ID         string `json:"id"`
	Affected   bool   `json:"affected"`
	Confidence int    `json:"confidence"`
	Basis      string `json:"basis"`
	Introduced string `json:"introduced"`
	Fixed      string `json:"fixed"`
	Reason     string `json:"reason"`
}

// Step is one line of the explanation trail.
type Step struct {
	Ordinal int    `json:"ordinal"`
	Action  string `json:"action"`
	Detail  string `json:"detail"`
}

// BuildVersionAttributor holds the per component timelines and commit index.
type BuildVersionAttributor struct {
	mu sync.RWMutex

	nextSeq int

	releases map[string][]*releaseEntry
	versions map[string]map[string]*releaseEntry
	commits  map[string]map[string]map[string]*releaseEntry

	attributions map[string]Attribution
	explanations map[string][]Step
}

type releaseEntry struct {
	release Release
}

type parsedVersion struct {
	core  []string
	pre   []preIdentifier
	isPre bool
}

type preIdentifier struct {
	text    string
	numeric bool
	number  string
}

type timeLocation int

const (
	locateBefore timeLocation = iota
	locateAtOrAfter
)

// NewBuildVersionAttributor builds an empty attributor.
func NewBuildVersionAttributor() *BuildVersionAttributor {
	return &BuildVersionAttributor{
		releases:     make(map[string][]*releaseEntry),
		versions:     make(map[string]map[string]*releaseEntry),
		commits:      make(map[string]map[string]map[string]*releaseEntry),
		attributions: make(map[string]Attribution),
		explanations: make(map[string][]Step),
	}
}

// RegisterRelease appends one tag to a component timeline.
func (a *BuildVersionAttributor) RegisterRelease(component, version string, at time.Time) error {
	component = strings.TrimSpace(component)
	version = strings.TrimSpace(version)
	if component == "" || version == "" {
		return ErrBlankField
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	a.initLocked()

	if current := a.versions[component][version]; current != nil {
		return ErrDuplicateRelease
	}

	parsed, err := parseVersion(version)
	entry := &releaseEntry{
		release: Release{
			Component:  component,
			Version:    version,
			At:         at.UTC(),
			Prerelease: err == nil && parsed.isPre,
			Seq:        a.nextSequenceLocked(),
		},
	}

	a.releases[component] = append(a.releases[component], entry)
	if a.versions[component] == nil {
		a.versions[component] = make(map[string]*releaseEntry)
	}
	a.versions[component][version] = entry
	return nil
}

// BindCommit maps a source commit to an already registered release.
func (a *BuildVersionAttributor) BindCommit(component, version, commit string) error {
	component = strings.TrimSpace(component)
	version = strings.TrimSpace(version)
	commit = strings.TrimSpace(commit)
	if component == "" || version == "" || commit == "" {
		return ErrBlankField
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	a.initLocked()

	release := a.versions[component][version]
	if release == nil {
		if len(a.releases[component]) == 0 {
			return ErrUnknownComponent
		}
		return ErrUnknownVersion
	}

	if a.commits[component] == nil {
		a.commits[component] = make(map[string]map[string]*releaseEntry)
	}
	bindings := a.commits[component][commit]
	if bindings == nil {
		bindings = make(map[string]*releaseEntry)
		a.commits[component][commit] = bindings
	}

	for boundVersion := range bindings {
		if boundVersion != version {
			return ErrDuplicateCommit
		}
	}

	bindings[version] = release
	release.release.Commit = commit
	return nil
}

// Attribute locates the release a build belongs to.
func (a *BuildVersionAttributor) Attribute(b BuildRef) (Attribution, error) {
	id := strings.TrimSpace(b.ID)
	if id == "" {
		return Attribution{}, ErrBlankField
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	a.initLocked()

	if stored, ok := a.attributions[id]; ok {
		return stored, nil
	}

	attribution, err := a.attributeLocked(b, id)
	if err != nil {
		return Attribution{}, err
	}
	a.storeAttributionLocked(attribution)
	return attribution, nil
}

// Affected answers whether the build version lies in any CVE range.
func (a *BuildVersionAttributor) Affected(b BuildRef, ranges []Range) (Verdict, error) {
	attribution, err := a.Attribute(b)
	if err != nil {
		return Verdict{}, err
	}

	parsedRanges, err := parseRanges(ranges)
	if err != nil {
		return Verdict{}, err
	}

	verdict := Verdict{
		ID:         attribution.ID,
		Confidence: attribution.Confidence,
		Basis:      attribution.Basis,
	}

	if attribution.Version == "" {
		verdict.Affected = true
		verdict.Confidence = ConfidenceUnlocated
		verdict.Reason = ReasonUnlocated
		return verdict, nil
	}

	version, err := parseVersion(attribution.Version)
	if err != nil {
		return Verdict{}, ErrBadRange
	}

	for _, candidate := range parsedRanges {
		if compareVersions(version, candidate.introduced) < 0 {
			continue
		}
		if candidate.hasFixed && compareVersions(version, candidate.fixed) >= 0 {
			continue
		}
		verdict.Affected = true
		verdict.Introduced = candidate.introducedRaw
		verdict.Fixed = candidate.fixedRaw
		verdict.Reason = ReasonInRange
		return verdict, nil
	}

	verdict.Reason = ReasonOutsideRanges
	return verdict, nil
}

// Timeline returns one component timeline, oldest release first.
func (a *BuildVersionAttributor) Timeline(component string) []Release {
	component = strings.TrimSpace(component)

	a.mu.RLock()
	defer a.mu.RUnlock()

	entries := a.releases[component]
	if len(entries) == 0 {
		return nil
	}
	timeline := make([]Release, len(entries))
	for i, entry := range entries {
		timeline[i] = entry.release
	}
	sort.Slice(timeline, func(i, j int) bool {
		return releaseLess(timeline[i], timeline[j])
	})
	return timeline
}

// Latest returns the newest release of a component.
func (a *BuildVersionAttributor) Latest(component string) (Release, bool) {
	component = strings.TrimSpace(component)

	a.mu.RLock()
	defer a.mu.RUnlock()

	entry := latestEntry(a.releases[component])
	if entry == nil {
		return Release{}, false
	}
	return entry.release, true
}

// Components lists every component with at least one release, sorted.
func (a *BuildVersionAttributor) Components() []string {
	a.mu.RLock()
	defer a.mu.RUnlock()

	components := make([]string, 0, len(a.releases))
	for component, releases := range a.releases {
		if len(releases) > 0 {
			components = append(components, component)
		}
	}
	sort.Strings(components)
	return components
}

// Lookup reads a stored attribution by build id.
func (a *BuildVersionAttributor) Lookup(id string) (Attribution, bool) {
	id = strings.TrimSpace(id)

	a.mu.RLock()
	defer a.mu.RUnlock()

	attribution, ok := a.attributions[id]
	return attribution, ok
}

// Explain returns the three explanation steps of a stored attribution.
func (a *BuildVersionAttributor) Explain(id string) []Step {
	id = strings.TrimSpace(id)

	a.mu.RLock()
	defer a.mu.RUnlock()

	steps := a.explanations[id]
	if len(steps) == 0 {
		return nil
	}
	return append([]Step(nil), steps...)
}

func (a *BuildVersionAttributor) initLocked() {
	if a.releases == nil {
		a.releases = make(map[string][]*releaseEntry)
	}
	if a.versions == nil {
		a.versions = make(map[string]map[string]*releaseEntry)
	}
	if a.commits == nil {
		a.commits = make(map[string]map[string]map[string]*releaseEntry)
	}
	if a.attributions == nil {
		a.attributions = make(map[string]Attribution)
	}
	if a.explanations == nil {
		a.explanations = make(map[string][]Step)
	}
}

func (a *BuildVersionAttributor) nextSequenceLocked() int {
	a.nextSeq++
	return a.nextSeq
}

func (a *BuildVersionAttributor) attributeLocked(b BuildRef, id string) (Attribution, error) {
	component := strings.TrimSpace(b.Component)
	declared := strings.TrimSpace(b.Version)
	if component == "" {
		return Attribution{}, ErrBlankField
	}

	releases := a.releases[component]
	if len(releases) == 0 {
		return Attribution{}, ErrUnknownComponent
	}

	timeUsable := clockIsVerifiable(b)

	if declared != "" {
		release := a.versions[component][declared]
		if release == nil {
			return Attribution{}, ErrUnknownVersion
		}
		if timeUsable {
			located, location := locateByTime(releases, b.BuiltAt)
			if location == locateBefore || located == nil || located.release.Version != declared {
				return Attribution{}, ErrTimelineConflict
			}
		}
		return Attribution{
			ID:         id,
			Component:  component,
			Version:    declared,
			Basis:      BasisDeclared,
			Confidence: ConfidenceDeclared,
		}, nil
	}

	if timeUsable {
		located, location := locateByTime(releases, b.BuiltAt)
		if location == locateBefore {
			return Attribution{
				ID:         id,
				Component:  component,
				Basis:      BasisBeforeTimeline,
				Confidence: 0,
				Outside:    true,
			}, nil
		}

		attribution := Attribution{
			ID:         id,
			Component:  component,
			Version:    located.release.Version,
			Basis:      BasisTime,
			Confidence: ConfidenceTime,
		}
		if b.BuiltAt.After(latestTime(releases)) {
			attribution.Basis = BasisAfterTimeline
			attribution.Confidence = ConfidenceAfterTimeline
			attribution.Outside = true
		}
		return attribution, nil
	}

	commit := strings.TrimSpace(b.Commit)
	if commit == "" {
		return Attribution{}, ErrUnknownCommit
	}
	bindings := a.commits[component][commit]
	if len(bindings) == 0 {
		return Attribution{}, ErrUnknownCommit
	}

	var located *releaseEntry
	for _, release := range bindings {
		if located != nil && located.release.Version != release.release.Version {
			return Attribution{}, ErrDuplicateCommit
		}
		located = release
	}
	if located == nil {
		return Attribution{}, ErrUnknownCommit
	}

	return Attribution{
		ID:         id,
		Component:  component,
		Version:    located.release.Version,
		Basis:      BasisCommit,
		Confidence: ConfidenceCommit,
	}, nil
}

func (a *BuildVersionAttributor) storeAttributionLocked(attribution Attribution) {
	a.attributions[attribution.ID] = attribution

	boundary := "inside_timeline"
	if attribution.Outside {
		boundary = attribution.Basis
	}
	a.explanations[attribution.ID] = []Step{
		{Ordinal: 1, Action: "basis", Detail: attribution.Basis},
		{Ordinal: 2, Action: "version", Detail: attribution.Version},
		{Ordinal: 3, Action: "boundary", Detail: boundary},
	}
}

func clockIsVerifiable(b BuildRef) bool {
	if !b.ClockTrusted {
		return false
	}
	location := b.BuiltAt.Location()
	if location == nil || location == time.Local {
		return false
	}
	name := location.String()
	return name != "" && name != "Local"
}

func releaseLess(left, right Release) bool {
	if left.At.Equal(right.At) {
		return left.Seq < right.Seq
	}
	return left.At.Before(right.At)
}

func latestTime(releases []*releaseEntry) time.Time {
	var latest time.Time
	for i, release := range releases {
		if i == 0 || release.release.At.After(latest) {
			latest = release.release.At
		}
	}
	return latest
}

func latestEntry(releases []*releaseEntry) *releaseEntry {
	if len(releases) == 0 {
		return nil
	}
	latest := latestTime(releases)
	return preferredEntryAt(releases, latest)
}

func preferredEntryAt(releases []*releaseEntry, at time.Time) *releaseEntry {
	var formal *releaseEntry
	var any *releaseEntry
	for _, release := range releases {
		if !release.release.At.Equal(at) {
			continue
		}
		if any == nil || release.release.Seq > any.release.Seq {
			any = release
		}
		if release.release.Prerelease {
			continue
		}
		if formal == nil || release.release.Seq > formal.release.Seq {
			formal = release
		}
	}
	if formal != nil {
		return formal
	}
	return any
}

func locateByTime(releases []*releaseEntry, builtAt time.Time) (*releaseEntry, timeLocation) {
	if len(releases) == 0 {
		return nil, locateBefore
	}

	earliest := releases[0].release.At
	for _, release := range releases[1:] {
		if release.release.At.Before(earliest) {
			earliest = release.release.At
		}
	}
	if builtAt.Before(earliest) {
		return nil, locateBefore
	}

	var candidateTime time.Time
	found := false
	for _, release := range releases {
		if release.release.At.After(builtAt) {
			continue
		}
		if !found || release.release.At.After(candidateTime) {
			candidateTime = release.release.At
			found = true
		}
	}
	if !found {
		return nil, locateBefore
	}
	return preferredEntryAt(releases, candidateTime), locateAtOrAfter
}

func parseVersion(raw string) (parsedVersion, error) {
	var parsed parsedVersion
	value := strings.TrimSpace(raw)
	if value == "" {
		return parsed, ErrBadRange
	}

	if plus := strings.IndexByte(value, '+'); plus >= 0 {
		if plus == len(value)-1 {
			return parsed, ErrBadRange
		}
		value = value[:plus]
	}

	core := value
	pre := ""
	if dash := strings.IndexByte(value, '-'); dash >= 0 {
		core = value[:dash]
		pre = value[dash+1:]
		if pre == "" {
			return parsed, ErrBadRange
		}
		parsed.isPre = true
	}

	fields := strings.Split(core, ".")
	if len(fields) == 0 {
		return parsed, ErrBadRange
	}
	parsed.core = make([]string, len(fields))
	for i, field := range fields {
		if field == "" || !allDigits(field) {
			return parsed, ErrBadRange
		}
		parsed.core[i] = trimLeadingZeros(field)
	}

	if !parsed.isPre {
		return parsed, nil
	}

	identifiers := strings.Split(pre, ".")
	parsed.pre = make([]preIdentifier, len(identifiers))
	for i, identifier := range identifiers {
		if identifier == "" {
			return parsed, ErrBadRange
		}
		item := preIdentifier{text: identifier}
		if allDigits(identifier) {
			item.numeric = true
			item.number = trimLeadingZeros(identifier)
		}
		parsed.pre[i] = item
	}
	return parsed, nil
}

func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func trimLeadingZeros(value string) string {
	value = strings.TrimLeft(value, "0")
	if value == "" {
		return "0"
	}
	return value
}

func compareNumeric(left, right string) int {
	left = trimLeadingZeros(left)
	right = trimLeadingZeros(right)
	if len(left) < len(right) {
		return -1
	}
	if len(left) > len(right) {
		return 1
	}
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}

func compareVersions(left, right parsedVersion) int {
	coreLength := len(left.core)
	if len(right.core) > coreLength {
		coreLength = len(right.core)
	}
	for i := 0; i < coreLength; i++ {
		leftField := "0"
		if i < len(left.core) {
			leftField = left.core[i]
		}
		rightField := "0"
		if i < len(right.core) {
			rightField = right.core[i]
		}
		if comparison := compareNumeric(leftField, rightField); comparison != 0 {
			return comparison
		}
	}

	if !left.isPre && !right.isPre {
		return 0
	}
	if !left.isPre {
		return 1
	}
	if !right.isPre {
		return -1
	}

	preLength := len(left.pre)
	if len(right.pre) < preLength {
		preLength = len(right.pre)
	}
	for i := 0; i < preLength; i++ {
		comparison := comparePreIdentifier(left.pre[i], right.pre[i])
		if comparison != 0 {
			return comparison
		}
	}
	if len(left.pre) < len(right.pre) {
		return -1
	}
	if len(left.pre) > len(right.pre) {
		return 1
	}
	return 0
}

func comparePreIdentifier(left, right preIdentifier) int {
	if left.numeric && right.numeric {
		return compareNumeric(left.number, right.number)
	}
	if left.numeric {
		return -1
	}
	if right.numeric {
		return 1
	}
	if left.text < right.text {
		return -1
	}
	if left.text > right.text {
		return 1
	}
	return 0
}

type parsedRange struct {
	introduced    parsedVersion
	fixed         parsedVersion
	introducedRaw string
	fixedRaw      string
	hasFixed      bool
}

func parseRanges(ranges []Range) ([]parsedRange, error) {
	parsed := make([]parsedRange, 0, len(ranges))
	for _, current := range ranges {
		introducedRaw := strings.TrimSpace(current.Introduced)
		if introducedRaw == "" {
			return nil, ErrBadRange
		}
		introduced, err := parseVersion(introducedRaw)
		if err != nil {
			return nil, ErrBadRange
		}

		fixedRaw := strings.TrimSpace(current.Fixed)
		item := parsedRange{
			introduced:    introduced,
			introducedRaw: introducedRaw,
			fixedRaw:      fixedRaw,
		}
		if fixedRaw != "" {
			fixed, err := parseVersion(fixedRaw)
			if err != nil {
				return nil, ErrBadRange
			}
			if compareVersions(introduced, fixed) > 0 {
				return nil, ErrBadRange
			}
			item.fixed = fixed
			item.hasFixed = true
		}
		parsed = append(parsed, item)
	}
	return parsed, nil
}
