// Package task solves the smallest safe upgrade target for vulnerable installs.
package task

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
)

// Reasons attached to a solved target.
const (
	ReasonUpgrade     = "upgrade"
	ReasonAlreadySafe = "already_safe"
	ReasonNoFix       = "no_fix"
)

// Errors returned by the solver.
var (
	ErrBadRange         = errors.New("affected range is invalid")
	ErrBadVersion       = errors.New("version string is invalid")
	ErrUnknownComponent = errors.New("component has no ranges or release list")
	ErrBadInstalled     = errors.New("installed version is invalid")
	ErrNoFix            = errors.New("no safe released version satisfies the constraints")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

// Range is one CVE affected range. Introduced is inclusive and Fixed is
// exclusive; an empty Fixed means the component is still unfixed.
type Range struct {
	Component  string `json:"component"`
	CVE        string `json:"cve"`
	Introduced string `json:"introduced,omitempty"`
	Fixed      string `json:"fixed,omitempty"`
}

// Installed is one deployed version of a component.
type Installed struct {
	Component string `json:"component"`
	Version   string `json:"version"`
}

// Target is the recommended upgrade of one installed version.
type Target struct {
	Component  string `json:"component"`
	From       string `json:"from"`
	To         string `json:"to"`
	Moved      bool   `json:"moved"`
	CrossMajor bool   `json:"cross_major"`
	Reason     string `json:"reason"`
}

// Risk describes what stays open when no safe released version exists.
type Risk struct {
	Component string   `json:"component"`
	Installed string   `json:"installed"`
	Highest   string   `json:"highest"`
	Residual  []string `json:"residual,omitempty"`
	Reason    string   `json:"reason"`
}

type version struct {
	text  string
	parts [3]string
}

type snapshotPayload struct {
	Schema        int                 `json:"schema"`
	Ranges        []Range             `json:"ranges"`
	Releases      map[string][]string `json:"releases"`
	Compatibility map[string]string   `json:"compatibility,omitempty"`
}

// UpgradeTargetSolver owns the affected ranges, the release lists and the
// declared compatibility limits.
type UpgradeTargetSolver struct {
	mu       sync.RWMutex
	ranges   map[string][]Range
	releases map[string]map[string]version
	limits   map[string]version
}

// NewUpgradeTargetSolver builds an empty solver.
func NewUpgradeTargetSolver() *UpgradeTargetSolver {
	return &UpgradeTargetSolver{
		ranges:   make(map[string][]Range),
		releases: make(map[string]map[string]version),
		limits:   make(map[string]version),
	}
}

// AddRange records one affected range of a component.
func (s *UpgradeTargetSolver) AddRange(r Range) error {
	r.Component = strings.TrimSpace(r.Component)
	r.CVE = strings.TrimSpace(r.CVE)
	if r.Component == "" || r.CVE == "" {
		return ErrBadRange
	}

	introduced, err := parseVersion(r.Introduced)
	if err != nil {
		return ErrBadVersion
	}
	if r.Fixed != "" {
		fixed, err := parseVersion(r.Fixed)
		if err != nil {
			return ErrBadVersion
		}
		if compareVersions(introduced, fixed) > 0 {
			return ErrBadRange
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureLocked()
	s.ranges[r.Component] = append(s.ranges[r.Component], r)
	return nil
}

// AddReleased records one published version of a component.
func (s *UpgradeTargetSolver) AddReleased(component, published string) error {
	component = strings.TrimSpace(component)
	if component == "" {
		return ErrBadRange
	}
	v, err := parseVersion(published)
	if err != nil {
		return ErrBadVersion
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureLocked()
	if s.releases[component] == nil {
		s.releases[component] = make(map[string]version)
	}
	if _, exists := s.releases[component][v.text]; !exists {
		s.releases[component][v.text] = v
	}
	return nil
}

// Solve returns the smallest safe upgrade target of one installed version.
func (s *UpgradeTargetSolver) Solve(installed Installed) (Target, error) {
	component := strings.TrimSpace(installed.Component)
	from, err := parseVersion(installed.Version)
	if err != nil {
		return Target{}, ErrBadInstalled
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	ranges, okRanges := s.ranges[component]
	releases, okReleases := s.releases[component]
	if !okRanges || !okReleases || len(ranges) == 0 || len(releases) == 0 {
		return Target{}, ErrUnknownComponent
	}

	if !isAffected(ranges, from) {
		return Target{
			Component: component,
			From:      installed.Version,
			To:        installed.Version,
			Moved:     false,
			Reason:    ReasonAlreadySafe,
		}, nil
	}

	candidates := s.candidatesLocked(component, from, ranges, releases)
	if len(candidates) == 0 {
		return Target{
			Component: component,
			From:      installed.Version,
			Reason:    ReasonNoFix,
		}, ErrNoFix
	}

	toText := candidates[0]
	to, _ := parseVersion(toText)
	return Target{
		Component:  component,
		From:       installed.Version,
		To:         toText,
		Moved:      true,
		CrossMajor: to.parts[0] != from.parts[0],
		Reason:     ReasonUpgrade,
	}, nil
}

// Candidates lists every usable upgrade version of one installed version.
func (s *UpgradeTargetSolver) Candidates(installed Installed) []string {
	component := strings.TrimSpace(installed.Component)
	from, err := parseVersion(installed.Version)
	if err != nil {
		return nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	ranges, okRanges := s.ranges[component]
	releases, okReleases := s.releases[component]
	if !okRanges || !okReleases || len(ranges) == 0 || len(releases) == 0 {
		return nil
	}
	return s.candidatesLocked(component, from, ranges, releases)
}

// NoFix reports the residual risk of an installed version without a safe target.
func (s *UpgradeTargetSolver) NoFix(installed Installed) (Risk, bool) {
	component := strings.TrimSpace(installed.Component)
	from, err := parseVersion(installed.Version)
	if err != nil {
		return Risk{}, false
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	ranges, okRanges := s.ranges[component]
	releases, okReleases := s.releases[component]
	if !okRanges || !okReleases || len(ranges) == 0 || len(releases) == 0 {
		return Risk{}, false
	}
	if !isAffected(ranges, from) {
		return Risk{}, false
	}
	if len(s.candidatesLocked(component, from, ranges, releases)) != 0 {
		return Risk{}, false
	}

	highest, ok := highestAvailable(releases, s.limits[component])
	residual := []string(nil)
	if ok {
		residual = affectedCVEs(ranges, highest)
	} else {
		residual = affectedCVEs(ranges, from)
	}
	if len(residual) == 0 {
		residual = nil
	}
	highestText := ""
	if ok {
		highestText = highest.text
	}
	return Risk{
		Component: component,
		Installed: installed.Version,
		Highest:   highestText,
		Residual:  residual,
		Reason:    ReasonNoFix,
	}, true
}

// Compatibility records the highest version a component may be upgraded to.
func (s *UpgradeTargetSolver) Compatibility(component, maxVersion string) error {
	component = strings.TrimSpace(component)
	if component == "" {
		return ErrBadRange
	}
	max, err := parseVersion(maxVersion)
	if err != nil {
		return ErrBadVersion
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureLocked()
	current, exists := s.limits[component]
	if !exists || compareVersions(max, current) < 0 {
		s.limits[component] = max
	}
	return nil
}

// Components lists the components with recorded ranges or releases.
func (s *UpgradeTargetSolver) Components() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	seen := make(map[string]struct{}, len(s.ranges)+len(s.releases))
	for component, ranges := range s.ranges {
		if len(ranges) != 0 {
			seen[component] = struct{}{}
		}
	}
	for component, releases := range s.releases {
		if len(releases) != 0 {
			seen[component] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for component := range seen {
		out = append(out, component)
	}
	sort.Strings(out)
	return out
}

// Ranges returns a copy of the affected ranges of one component.
func (s *UpgradeTargetSolver) Ranges(component string) []Range {
	component = strings.TrimSpace(component)
	s.mu.RLock()
	defer s.mu.RUnlock()

	ranges := s.ranges[component]
	if len(ranges) == 0 {
		return nil
	}
	return append([]Range(nil), ranges...)
}

// Released returns the published versions of one component, ascending.
func (s *UpgradeTargetSolver) Released(component string) []string {
	component = strings.TrimSpace(component)
	s.mu.RLock()
	defer s.mu.RUnlock()
	return releasedLocked(s.releases[component])
}

// Snapshot serialises ranges, release lists and compatibility limits.
func (s *UpgradeTargetSolver) Snapshot() ([]byte, error) {
	s.mu.RLock()
	payload := snapshotPayload{
		Schema:        1,
		Ranges:        make([]Range, 0),
		Releases:      make(map[string][]string, len(s.releases)),
		Compatibility: make(map[string]string, len(s.limits)),
	}

	components := make([]string, 0, len(s.ranges))
	for component, ranges := range s.ranges {
		if len(ranges) != 0 {
			components = append(components, component)
		}
	}
	sort.Strings(components)
	for _, component := range components {
		payload.Ranges = append(payload.Ranges, s.ranges[component]...)
	}

	for component, releases := range s.releases {
		if len(releases) != 0 {
			payload.Releases[component] = releasedLocked(releases)
		}
	}
	for component, limit := range s.limits {
		payload.Compatibility[component] = limit.text
	}
	s.mu.RUnlock()

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (s *UpgradeTargetSolver) Restore(data []byte) error {
	var payload snapshotPayload
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return badSnapshot(err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return badSnapshot(err)
	}
	if payload.Schema != 1 {
		return badSnapshot(errors.New("unsupported snapshot schema"))
	}

	ranges := make(map[string][]Range)
	for _, r := range payload.Ranges {
		r.Component = strings.TrimSpace(r.Component)
		r.CVE = strings.TrimSpace(r.CVE)
		if r.Component == "" || r.CVE == "" {
			return badSnapshot(errors.New("range has an empty component or CVE"))
		}
		introduced, err := parseVersion(r.Introduced)
		if err != nil {
			return badSnapshot(err)
		}
		if r.Fixed != "" {
			fixed, err := parseVersion(r.Fixed)
			if err != nil {
				return badSnapshot(err)
			}
			if compareVersions(introduced, fixed) > 0 {
				return badSnapshot(errors.New("range is inverted"))
			}
		}
		ranges[r.Component] = append(ranges[r.Component], r)
	}

	releases := make(map[string]map[string]version)
	for rawComponent, versions := range payload.Releases {
		component := strings.TrimSpace(rawComponent)
		if component == "" {
			return badSnapshot(errors.New("release list has an empty component"))
		}
		if releases[component] == nil {
			releases[component] = make(map[string]version)
		}
		for _, rawVersion := range versions {
			v, err := parseVersion(rawVersion)
			if err != nil {
				return badSnapshot(err)
			}
			releases[component][v.text] = v
		}
	}

	limits := make(map[string]version)
	for rawComponent, rawLimit := range payload.Compatibility {
		component := strings.TrimSpace(rawComponent)
		if component == "" {
			return badSnapshot(errors.New("compatibility entry has an empty component"))
		}
		limit, err := parseVersion(rawLimit)
		if err != nil {
			return badSnapshot(err)
		}
		current, exists := limits[component]
		if !exists || compareVersions(limit, current) < 0 {
			limits[component] = limit
		}
	}

	s.mu.Lock()
	s.ranges = ranges
	s.releases = releases
	s.limits = limits
	s.mu.Unlock()
	return nil
}

func (s *UpgradeTargetSolver) ensureLocked() {
	if s.ranges == nil {
		s.ranges = make(map[string][]Range)
	}
	if s.releases == nil {
		s.releases = make(map[string]map[string]version)
	}
	if s.limits == nil {
		s.limits = make(map[string]version)
	}
}

func (s *UpgradeTargetSolver) candidatesLocked(component string, installed version, ranges []Range, releases map[string]version) []string {
	candidates := make([]version, 0, len(releases))
	limit, hasLimit := s.limits[component]
	for _, released := range releases {
		if compareVersions(released, installed) <= 0 {
			continue
		}
		if hasLimit && compareVersions(released, limit) > 0 {
			continue
		}
		if isAffected(ranges, released) {
			continue
		}
		candidates = append(candidates, released)
	}
	sort.Slice(candidates, func(i, j int) bool {
		return compareVersions(candidates[i], candidates[j]) < 0
	})
	out := make([]string, len(candidates))
	for i, candidate := range candidates {
		out[i] = candidate.text
	}
	return out
}

func releasedLocked(releases map[string]version) []string {
	if len(releases) == 0 {
		return nil
	}
	versions := make([]version, 0, len(releases))
	for _, v := range releases {
		versions = append(versions, v)
	}
	sort.Slice(versions, func(i, j int) bool {
		return compareVersions(versions[i], versions[j]) < 0
	})
	out := make([]string, len(versions))
	for i, v := range versions {
		out[i] = v.text
	}
	return out
}

func highestAvailable(releases map[string]version, limit version) (version, bool) {
	var highest version
	found := false
	for _, released := range releases {
		if limit.text != "" && compareVersions(released, limit) > 0 {
			continue
		}
		if !found || compareVersions(released, highest) > 0 {
			highest = released
			found = true
		}
	}
	return highest, found
}

func isAffected(ranges []Range, candidate version) bool {
	return len(affectedCVEs(ranges, candidate)) != 0
}

func affectedCVEs(ranges []Range, candidate version) []string {
	seen := make(map[string]struct{})
	for _, r := range ranges {
		introduced, err := parseVersion(r.Introduced)
		if err != nil || compareVersions(candidate, introduced) < 0 {
			continue
		}
		if r.Fixed != "" {
			fixed, err := parseVersion(r.Fixed)
			if err != nil || compareVersions(candidate, fixed) >= 0 {
				continue
			}
		}
		seen[r.CVE] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for cve := range seen {
		out = append(out, cve)
	}
	sort.Strings(out)
	return out
}

func parseVersion(raw string) (version, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return version{}, ErrBadVersion
	}
	var normalized [3]string
	for i, part := range parts {
		if part == "" {
			return version{}, ErrBadVersion
		}
		for j := 0; j < len(part); j++ {
			if part[j] < '0' || part[j] > '9' {
				return version{}, ErrBadVersion
			}
		}
		trimmed := strings.TrimLeft(part, "0")
		if trimmed == "" {
			trimmed = "0"
		}
		normalized[i] = trimmed
	}
	return version{
		text:  strings.Join(normalized[:], "."),
		parts: normalized,
	}, nil
}

func compareVersions(a, b version) int {
	for i := 0; i < 3; i++ {
		if len(a.parts[i]) < len(b.parts[i]) {
			return -1
		}
		if len(a.parts[i]) > len(b.parts[i]) {
			return 1
		}
		if a.parts[i] < b.parts[i] {
			return -1
		}
		if a.parts[i] > b.parts[i] {
			return 1
		}
	}
	return 0
}

func badSnapshot(err error) error {
	return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
}
