// Package task guards a dependency lock graph: every entry carries an exact
// version and integrity digest, upgrades propagate along dependency edges and
// serialisation stays byte identical for the same graph.
package task

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Requirement is one exact dependency edge taken from release metadata.
type Requirement struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Release is one published version of a module together with its direct
// requirements.
type Release struct {
	Name     string        `json:"name"`
	Version  string        `json:"version"`
	Digest   string        `json:"digest"`
	Requires []Requirement `json:"requires"`
}

// Entry is one locked module coordinate.
type Entry struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
	Direct  bool   `json:"direct"`
}

// Change is one line of an upgrade plan.
type Change struct {
	Name       string `json:"name"`
	Action     string `json:"action"`
	OldVersion string `json:"old_version"`
	NewVersion string `json:"new_version"`
	OldDigest  string `json:"old_digest"`
	NewDigest  string `json:"new_digest"`
}

// Plan is the pending lock set change produced by Upgrade and consumed by
// Apply.
type Plan struct {
	Root    string   `json:"root"`
	Version string   `json:"version"`
	Changes []Change `json:"changes"`
}

// Problem is one integrity finding produced by Verify.
type Problem struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

// Change actions reported in a Plan.
const (
	ActionAdd     = "add"
	ActionUpgrade = "upgrade"
)

// Problem kinds reported by Verify.
const (
	ProblemUnsatisfied   = "unsatisfied_requirement"
	ProblemUnreachable   = "unreachable"
	ProblemMissingDigest = "missing_digest"
	ProblemUnlocked      = "unlocked"
)

// Errors returned by the guard.
var (
	ErrBadName         = errors.New("module name is required")
	ErrFloatingVersion = errors.New("version must be an exact three part version")
	ErrBadDigest       = errors.New("digest must be 64 lowercase hex characters")
	ErrUnknownRelease  = errors.New("release is not registered")
	ErrDigestMismatch  = errors.New("digest does not match the registered release")
	ErrDigestConflict  = errors.New("digest conflict for the same coordinate")
	ErrAlreadyPinned   = errors.New("module is already pinned directly")
	ErrNotLocked       = errors.New("module is not locked")
	ErrStalePlan       = errors.New("plan was computed from a stale lock state")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// LockGraphGuard owns the release catalogue and the current lock set.
type LockGraphGuard struct {
	mu       sync.Mutex
	releases map[string]map[string]Release
	locks    map[string]Entry
}

// NewLockGraphGuard builds an empty guard.
func NewLockGraphGuard() *LockGraphGuard {
	return &LockGraphGuard{
		releases: make(map[string]map[string]Release),
		locks:    make(map[string]Entry),
	}
}

func exactVersion(version string) bool {
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

func validDigest(digest string) bool {
	if len(digest) != 64 {
		return false
	}
	for i := 0; i < len(digest); i++ {
		c := digest[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func versionParts(version string) [3]int {
	var parts [3]int
	for i, value := range strings.Split(version, ".") {
		if i >= 3 {
			break
		}
		n, _ := strconv.Atoi(value)
		parts[i] = n
	}
	return parts
}

func compareVersions(a, b string) int {
	pa, pb := versionParts(a), versionParts(b)
	for i := range pa {
		if pa[i] < pb[i] {
			return -1
		}
		if pa[i] > pb[i] {
			return 1
		}
	}
	return 0
}

func cloneLocks(source map[string]Entry) map[string]Entry {
	clone := make(map[string]Entry, len(source))
	for name, entry := range source {
		clone[name] = entry
	}
	return clone
}

func sortedEntries(locks map[string]Entry) []Entry {
	entries := make([]Entry, 0, len(locks))
	for _, entry := range locks {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})
	return entries
}

func sortedRequirementCopies(requirements []Requirement) []Requirement {
	out := append([]Requirement(nil), requirements...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return compareVersions(out[i].Version, out[j].Version) < 0
	})
	return out
}

func releaseFor(guard *LockGraphGuard, name, version string) (Release, bool) {
	versions := guard.releases[name]
	release, ok := versions[version]
	return release, ok
}

func upgradeDigestConflictError(name, version string) error {
	return fmt.Errorf("%w: %s@%s: %w", ErrDigestConflict, name, version, ErrDigestMismatch)
}

func collectReachable(locks map[string]Entry, releases map[string]map[string]Release, extraRoots ...string) map[string]bool {
	reachable := make(map[string]bool)
	queue := make([]string, 0)

	enqueue := func(name string) {
		if _, locked := locks[name]; !locked {
			return
		}
		if !reachable[name] {
			reachable[name] = true
			queue = append(queue, name)
		}
	}

	for name, entry := range locks {
		if entry.Direct {
			enqueue(name)
		}
	}
	for _, root := range extraRoots {
		if root != "" {
			enqueue(root)
		}
	}

	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]

		entry, ok := locks[name]
		if !ok {
			continue
		}
		release, ok := releases[entry.Name][entry.Version]
		if !ok {
			continue
		}
		for _, requirement := range sortedRequirementCopies(release.Requires) {
			enqueue(requirement.Name)
		}
	}

	return reachable
}

// Register stores one published release. Version must be exactly three numeric
// parts and digest exactly 64 lowercase hex characters.
func (g *LockGraphGuard) Register(name, version, digest string, requires []Requirement) error {
	if name == "" {
		return ErrBadName
	}
	if !exactVersion(version) {
		return ErrFloatingVersion
	}
	if !validDigest(digest) {
		return ErrBadDigest
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	versions, ok := g.releases[name]
	if !ok {
		versions = make(map[string]Release)
		g.releases[name] = versions
	}
	if existing, ok := versions[version]; ok {
		if existing.Digest != digest {
			return fmt.Errorf("%w: %s@%s", ErrDigestConflict, name, version)
		}
		return nil
	}

	versions[version] = Release{
		Name:     name,
		Version:  version,
		Digest:   digest,
		Requires: append([]Requirement(nil), requires...),
	}
	return nil
}

// Releases lists the registered versions of one module in ascending order.
func (g *LockGraphGuard) Releases(name string) []string {
	g.mu.Lock()
	defer g.mu.Unlock()

	versions := g.releases[name]
	if len(versions) == 0 {
		return nil
	}
	out := make([]string, 0, len(versions))
	for version := range versions {
		out = append(out, version)
	}
	sort.Slice(out, func(i, j int) bool {
		return compareVersions(out[i], out[j]) < 0
	})
	return out
}

// Pin locks one module coordinate directly. The digest must equal the digest
// registered for that release.
func (g *LockGraphGuard) Pin(name, version, digest string) error {
	if name == "" {
		return ErrBadName
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	release, ok := releaseFor(g, name, version)
	if !ok {
		return fmt.Errorf("%w: %s@%s", ErrUnknownRelease, name, version)
	}
	if digest != release.Digest {
		return fmt.Errorf("%w: %s@%s", ErrDigestMismatch, name, version)
	}
	if existing, ok := g.locks[name]; ok && existing.Direct {
		return fmt.Errorf("%w: %s", ErrAlreadyPinned, name)
	}

	g.locks[name] = Entry{
		Name:    name,
		Version: version,
		Digest:  digest,
		Direct:  true,
	}
	return nil
}

// Unpin removes a direct pin; entries that stay reachable through other direct
// dependencies keep their lock.
func (g *LockGraphGuard) Unpin(name string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	entry, ok := g.locks[name]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotLocked, name)
	}
	if !entry.Direct {
		return nil
	}

	entry.Direct = false
	g.locks[name] = entry

	reachable := collectReachable(g.locks, g.releases)
	for lockedName := range g.locks {
		if !reachable[lockedName] {
			delete(g.locks, lockedName)
		}
	}
	return nil
}

// Locked returns the entry currently locked for name.
func (g *LockGraphGuard) Locked(name string) (Entry, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	entry, ok := g.locks[name]
	return entry, ok
}

// Entries lists the whole lock set ordered by module name.
func (g *LockGraphGuard) Entries() []Entry {
	g.mu.Lock()
	defer g.mu.Unlock()

	return sortedEntries(g.locks)
}

// Closure lists the modules reachable from root through locked dependency
// edges, root included, in ascending order.
func (g *LockGraphGuard) Closure(root string) ([]string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if _, ok := g.locks[root]; !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotLocked, root)
	}

	reachable := collectReachable(g.locks, g.releases, root)
	out := make([]string, 0, len(reachable))
	for name := range reachable {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

func (g *LockGraphGuard) resolveUpgrade(
	release Release,
	candidate map[string]Entry,
	visited map[string]bool,
) error {
	coordinate := release.Name + "\x00" + release.Version
	if visited[coordinate] {
		return nil
	}
	visited[coordinate] = true

	for _, requirement := range sortedRequirementCopies(release.Requires) {
		wanted, ok := releaseFor(g, requirement.Name, requirement.Version)
		if !ok {
			return fmt.Errorf("%w: required release %s@%s", ErrUnknownRelease, requirement.Name, requirement.Version)
		}

		if current, ok := candidate[requirement.Name]; ok &&
			current.Version == wanted.Version &&
			current.Digest != wanted.Digest {
			return upgradeDigestConflictError(requirement.Name, requirement.Version)
		}

		direct := false
		if current, ok := candidate[requirement.Name]; ok {
			direct = current.Direct
		}
		candidate[requirement.Name] = Entry{
			Name:    wanted.Name,
			Version: wanted.Version,
			Digest:  wanted.Digest,
			Direct:  direct,
		}

		if err := g.resolveUpgrade(wanted, candidate, visited); err != nil {
			return err
		}
	}
	return nil
}

func retainStillRequiredReleases(
	candidate, original map[string]Entry,
	releases map[string]map[string]Release,
	reachable map[string]bool,
) bool {
	reverted := make(map[string]bool)
	changed := false
	for _, parent := range sortedEntries(candidate) {
		if !reachable[parent.Name] {
			continue
		}
		versions := releases[parent.Name]
		release, ok := versions[parent.Version]
		if !ok {
			continue
		}

		for _, requirement := range sortedRequirementCopies(release.Requires) {
			old, existed := original[requirement.Name]
			if !existed || old.Version != requirement.Version || reverted[requirement.Name] {
				continue
			}
			if wanted, ok := releases[requirement.Name][requirement.Version]; ok && old.Digest != wanted.Digest {
				continue
			}

			current, present := candidate[requirement.Name]
			if present && current.Version == old.Version && current.Digest == old.Digest {
				continue
			}
			candidate[requirement.Name] = old
			reverted[requirement.Name] = true
			changed = true
		}
	}
	return changed
}

// Upgrade computes the lock set change that moves name to version, propagating
// through the dependency graph. It never mutates the lock set.
func (g *LockGraphGuard) Upgrade(name, version string) (Plan, error) {
	if name == "" {
		return Plan{}, ErrBadName
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	target, ok := releaseFor(g, name, version)
	if !ok {
		return Plan{}, fmt.Errorf("%w: %s@%s", ErrUnknownRelease, name, version)
	}

	original := cloneLocks(g.locks)
	candidate := cloneLocks(g.locks)

	rootDirect := false
	if old, ok := candidate[name]; ok {
		if old.Version == version && old.Digest != target.Digest {
			return Plan{}, upgradeDigestConflictError(name, version)
		}
		rootDirect = old.Direct
	}
	candidate[name] = Entry{
		Name:    name,
		Version: version,
		Digest:  target.Digest,
		Direct:  rootDirect,
	}

	if err := g.resolveUpgrade(target, candidate, make(map[string]bool)); err != nil {
		return Plan{}, err
	}
	for {
		reachable := collectReachable(candidate, g.releases, name)
		if !retainStillRequiredReleases(candidate, original, g.releases, reachable) {
			break
		}
	}

	reachable := collectReachable(candidate, g.releases, name)
	for lockedName := range candidate {
		if !reachable[lockedName] {
			delete(candidate, lockedName)
		}
	}

	changes := make([]Change, 0)
	for _, next := range sortedEntries(candidate) {
		old, existed := original[next.Name]
		if !existed {
			changes = append(changes, Change{
				Name:       next.Name,
				Action:     ActionAdd,
				NewVersion: next.Version,
				NewDigest:  next.Digest,
			})
			continue
		}
		if old.Version == next.Version && old.Digest == next.Digest {
			continue
		}
		changes = append(changes, Change{
			Name:       next.Name,
			Action:     ActionUpgrade,
			OldVersion: old.Version,
			NewVersion: next.Version,
			OldDigest:  old.Digest,
			NewDigest:  next.Digest,
		})
	}

	return Plan{Root: name, Version: version, Changes: changes}, nil
}

// Apply commits a plan. It fails with ErrStalePlan when the lock set moved
// since the plan was computed, and leaves the lock set untouched on error.
func (g *LockGraphGuard) Apply(plan Plan) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	seen := make(map[string]bool, len(plan.Changes))
	for _, change := range plan.Changes {
		if change.Name == "" || seen[change.Name] {
			return fmt.Errorf("%w: invalid plan change", ErrStalePlan)
		}
		seen[change.Name] = true

		current, exists := g.locks[change.Name]
		switch change.Action {
		case ActionAdd:
			if exists {
				return fmt.Errorf("%w: %s already exists", ErrStalePlan, change.Name)
			}
		case ActionUpgrade:
			if !exists ||
				current.Version != change.OldVersion ||
				current.Digest != change.OldDigest {
				return fmt.Errorf("%w: %s", ErrStalePlan, change.Name)
			}
		default:
			return fmt.Errorf("%w: unsupported action %q", ErrStalePlan, change.Action)
		}
	}

	for _, change := range plan.Changes {
		direct := false
		if old, existed := g.locks[change.Name]; existed {
			direct = old.Direct
		} else if change.Name == plan.Root {
			direct = true
		}
		g.locks[change.Name] = Entry{
			Name:    change.Name,
			Version: change.NewVersion,
			Digest:  change.NewDigest,
			Direct:  direct,
		}
	}

	if plan.Root != "" && plan.Version != "" {
		reachable := collectReachable(g.locks, g.releases, plan.Root)
		for name := range g.locks {
			if !reachable[name] {
				delete(g.locks, name)
			}
		}
	}
	return nil
}

// Verify reports integrity problems ordered by module name and kind.
func (g *LockGraphGuard) Verify() []Problem {
	g.mu.Lock()
	defer g.mu.Unlock()

	problems := make([]Problem, 0)
	reachable := collectReachable(g.locks, g.releases)

	for _, entry := range sortedEntries(g.locks) {
		if entry.Digest == "" {
			problems = append(problems, Problem{
				Name:   entry.Name,
				Kind:   ProblemMissingDigest,
				Detail: "digest is missing",
			})
		} else if !validDigest(entry.Digest) {
			problems = append(problems, Problem{
				Name:   entry.Name,
				Kind:   ProblemMissingDigest,
				Detail: "digest must be 64 lowercase hex characters",
			})
		}

		if !reachable[entry.Name] {
			problems = append(problems, Problem{
				Name:   entry.Name,
				Kind:   ProblemUnreachable,
				Detail: "entry is not reachable from a direct lock",
			})
		}

		release, ok := releaseFor(g, entry.Name, entry.Version)
		if !ok {
			continue
		}
		for _, requirement := range sortedRequirementCopies(release.Requires) {
			required, locked := g.locks[requirement.Name]
			if !locked {
				problems = append(problems, Problem{
					Name:   requirement.Name,
					Kind:   ProblemUnlocked,
					Detail: fmt.Sprintf("%s %s requires %s %s but it is not locked", entry.Name, entry.Version, requirement.Name, requirement.Version),
				})
				continue
			}
			if required.Version != requirement.Version {
				problems = append(problems, Problem{
					Name:   requirement.Name,
					Kind:   ProblemUnsatisfied,
					Detail: fmt.Sprintf("%s %s requires %s %s but version %s is locked", entry.Name, entry.Version, requirement.Name, requirement.Version, required.Version),
				})
			}
		}
	}

	sort.SliceStable(problems, func(i, j int) bool {
		if problems[i].Name != problems[j].Name {
			return problems[i].Name < problems[j].Name
		}
		if problems[i].Kind != problems[j].Kind {
			return problems[i].Kind < problems[j].Kind
		}
		return problems[i].Detail < problems[j].Detail
	})
	return problems
}

// Serialize writes the lock set as tab separated lines ordered by module name,
// so the same graph yields the same bytes for any insertion order.
func (g *LockGraphGuard) Serialize() ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	var builder strings.Builder
	for _, entry := range sortedEntries(g.locks) {
		builder.WriteString(entry.Name)
		builder.WriteByte('\t')
		builder.WriteString(entry.Version)
		builder.WriteByte('\t')
		builder.WriteString(entry.Digest)
		builder.WriteByte('\n')
	}
	return []byte(builder.String()), nil
}

// Restore only replaces the locked entries from serialized output. A corrupt
// payload leaves the guard untouched.
func (g *LockGraphGuard) Restore(data []byte) error {
	next := make(map[string]Entry)
	if len(data) > 0 {
		lines := strings.Split(string(data), "\n")
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}

		for _, rawLine := range lines {
			line := strings.TrimSuffix(rawLine, "\r")
			if line == "" {
				return fmt.Errorf("%w: empty line", ErrBadSnapshot)
			}
			fields := strings.Split(line, "\t")
			if len(fields) != 3 {
				return fmt.Errorf("%w: expected three tab separated fields", ErrBadSnapshot)
			}
			name, version, digest := fields[0], fields[1], fields[2]
			if name == "" || version == "" {
				return fmt.Errorf("%w: name and version are required", ErrBadSnapshot)
			}
			if _, duplicate := next[name]; duplicate {
				return fmt.Errorf("%w: duplicate entry %s", ErrBadSnapshot, name)
			}
			next[name] = Entry{Name: name, Version: version, Digest: digest}
		}
	}

	g.mu.Lock()
	g.locks = next
	g.mu.Unlock()
	return nil
}
