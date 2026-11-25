// Package task reconciles the real ACL of a managed tree against a baseline
// policy and turns the drift into an ordered repair plan.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Entry is one subject and permission bit pair.
type Entry struct {
	Subject string `json:"subject"`
	Bits    uint32 `json:"bits"`
}

// Baseline is the desired ACL policy for a managed tree.
type Baseline struct {
	Version string             `json:"version"`
	Rules   map[string][]Entry `json:"rules"`
	Default []Entry            `json:"default"`
}

// FileAcl is one file as reported by the tree source.
type FileAcl struct {
	Path        string    `json:"path"`
	Entries     []Entry   `json:"entries"`
	ContentHash string    `json:"content_hash"`
	MTime       time.Time `json:"mtime"`
	ReadOnly    bool      `json:"read_only"`
	Depth       int       `json:"depth"`
}

// TreeSnapshot is a generation pinned view of one managed tree.
type TreeSnapshot struct {
	Generation uint64    `json:"generation"`
	Files      []FileAcl `json:"files"`
}

// Tree supplies snapshots of a managed tree.
type Tree interface {
	Snapshot(root string) (TreeSnapshot, error)
}

// DriftKind classifies one deviation between actual and baseline ACL.
type DriftKind string

// Drift classes reported by the reconciler.
const (
	DriftOverGrant       DriftKind = "over_grant"
	DriftMissing         DriftKind = "missing"
	DriftExpiredTemp     DriftKind = "expired_temp"
	DriftInheritConflict DriftKind = "inherit_conflict"
	DriftUnfixable       DriftKind = "unfixable"
)

// Drift is one deviation found on one path.
type Drift struct {
	Path       string    `json:"path"`
	Kind       DriftKind `json:"kind"`
	Expected   []Entry   `json:"expected"`
	Actual     []Entry   `json:"actual"`
	Extra      []Entry   `json:"extra"`
	Missing    []Entry   `json:"missing"`
	Depth      int       `json:"depth"`
	ReadOnly   bool      `json:"read_only"`
	Generation uint64    `json:"generation"`
}

// FixStep is one idempotent repair action for a single path.
type FixStep struct {
	Path       string  `json:"path"`
	Depth      int     `json:"depth"`
	Batch      int     `json:"batch"`
	Add        []Entry `json:"add"`
	Remove     []Entry `json:"remove"`
	Key        string  `json:"key"`
	Generation uint64  `json:"generation"`
}

// Exemption keeps a path out of the drift report until a ticket expires.
type Exemption struct {
	Path   string    `json:"path"`
	Until  time.Time `json:"until"`
	Ticket string    `json:"ticket"`
}

// Shard is the result of one bounded scan slice.
type Shard struct {
	Scanned    int     `json:"scanned"`
	Remaining  int     `json:"remaining"`
	Done       bool    `json:"done"`
	Drifts     []Drift `json:"drifts"`
	Generation uint64  `json:"generation"`
}

// ScanStats summarises the most recent scan pass.
type ScanStats struct {
	Files      int    `json:"files"`
	Drifts     int    `json:"drifts"`
	Cached     int    `json:"cached"`
	Generation uint64 `json:"generation"`
}

// Limits caps how much work one reconciler does.
type Limits struct {
	MaxFiles  int `json:"max_files"`
	MaxDrifts int `json:"max_drifts"`
	MaxSteps  int `json:"max_steps"`
}

// Errors reported by the reconciler.
var (
	ErrBlankRoot       = errors.New("root is required")
	ErrBadBaseline     = errors.New("baseline version is required")
	ErrFileLimit       = errors.New("file limit reached for one scan")
	ErrDriftLimit      = errors.New("drift limit reached for one scan")
	ErrStepLimit       = errors.New("fix step limit reached")
	ErrBadBudget       = errors.New("scan budget must be positive")
	ErrBlankTicket     = errors.New("exemption ticket is required")
	ErrExemptPast      = errors.New("exemption must expire in the future")
	ErrStaleGeneration = errors.New("fix step was planned against an older snapshot")
	ErrUnknownRoot     = errors.New("root is not managed")
)

type cacheRecord struct {
	contentHash     string
	baselineVersion string
	drifts          []Drift
}

type shardState struct {
	root            string
	baselineVersion string
	baseline        Baseline
	generation      uint64
	files           []FileAcl
	next            int
	rawDrifts       []Drift
	cached          int
	done            bool
}

type stepAggregate struct {
	path       string
	depth      int
	generation uint64
	add        []Entry
	remove     []Entry
}

// AclBaselineReconciler compares actual ACLs with a baseline policy.
type AclBaselineReconciler struct {
	tree   Tree
	limits Limits
	now    func() time.Time

	mu              sync.RWMutex
	generation      uint64
	lastDrifts      []Drift
	lastDigest      string
	stats           ScanStats
	exemptions      []Exemption
	cache           map[string]cacheRecord
	shard           *shardState
	appliedStepKeys map[string]struct{}
}

// NewAclBaselineReconciler builds a reconciler over a tree source.
func NewAclBaselineReconciler(tree Tree, l Limits, now func() time.Time) *AclBaselineReconciler {
	if now == nil {
		now = time.Now
	}
	if l.MaxFiles < 0 {
		l.MaxFiles = 0
	}
	if l.MaxDrifts < 0 {
		l.MaxDrifts = 0
	}
	if l.MaxSteps < 0 {
		l.MaxSteps = 0
	}
	return &AclBaselineReconciler{
		tree:            tree,
		limits:          l,
		now:             now,
		cache:           make(map[string]cacheRecord),
		appliedStepKeys: make(map[string]struct{}),
	}
}

// Scan compares every file of one tree with the baseline.
func (r *AclBaselineReconciler) Scan(root string, baseline Baseline) ([]Drift, error) {
	if err := validateScanRequest(root, baseline); err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	snapshot, err := r.tree.Snapshot(root)
	if err != nil {
		return nil, err
	}
	snapshot = pinSnapshot(snapshot)
	if len(snapshot.Files) > r.limits.MaxFiles {
		return nil, ErrFileLimit
	}

	rawDrifts, cached := r.processFilesLocked(root, baseline, snapshot.Files, snapshot.Generation)
	report := r.activeReportLocked(rawDrifts, r.currentTime())
	if len(report) > r.limits.MaxDrifts {
		return nil, ErrDriftLimit
	}

	r.generation = snapshot.Generation
	r.lastDrifts = cloneDrifts(report)
	r.lastDigest = digestDrifts(report)
	r.stats = ScanStats{
		Files:      len(snapshot.Files),
		Drifts:     len(report),
		Cached:     cached,
		Generation: snapshot.Generation,
	}
	r.shard = nil

	return cloneDrifts(report), nil
}

// ScanShard compares at most budget files and reports whether the pass is done.
func (r *AclBaselineReconciler) ScanShard(root string, baseline Baseline, budget int) (Shard, error) {
	if err := validateScanRequest(root, baseline); err != nil {
		return Shard{}, err
	}
	if budget <= 0 {
		return Shard{}, ErrBadBudget
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.shard == nil || r.shard.root != root || r.shard.baselineVersion != baseline.Version {
		snapshot, err := r.tree.Snapshot(root)
		if err != nil {
			return Shard{}, err
		}
		snapshot = pinSnapshot(snapshot)
		if len(snapshot.Files) > r.limits.MaxFiles {
			return Shard{Generation: snapshot.Generation}, ErrFileLimit
		}

		r.shard = &shardState{
			root:            root,
			baselineVersion: baseline.Version,
			baseline:        cloneBaseline(baseline),
			generation:      snapshot.Generation,
			files:           snapshot.Files,
			rawDrifts:       make([]Drift, 0),
		}
		r.generation = snapshot.Generation
	}

	shard := r.shard
	if shard.done {
		report := r.activeReportLocked(shard.rawDrifts, r.currentTime())
		return Shard{
			Scanned:    0,
			Remaining:  0,
			Done:       true,
			Drifts:     cloneDrifts(report),
			Generation: shard.generation,
		}, nil
	}

	remainingFiles := len(shard.files) - shard.next
	scanned := budget
	if scanned > remainingFiles {
		scanned = remainingFiles
	}
	end := shard.next + scanned

	rawDrifts, cached := r.processFilesLocked(
		shard.root,
		shard.baseline,
		shard.files[shard.next:end],
		shard.generation,
	)
	shard.rawDrifts = append(shard.rawDrifts, rawDrifts...)
	shard.cached += cached
	shard.next = end

	report := r.activeReportLocked(shard.rawDrifts, r.currentTime())
	done := shard.next >= len(shard.files)
	response := Shard{
		Scanned:    scanned,
		Remaining:  len(shard.files) - shard.next,
		Done:       done,
		Drifts:     cloneDrifts(report),
		Generation: shard.generation,
	}

	r.generation = shard.generation
	r.stats = ScanStats{
		Files:      shard.next,
		Drifts:     len(report),
		Cached:     shard.cached,
		Generation: shard.generation,
	}

	if len(report) > r.limits.MaxDrifts {
		return response, ErrDriftLimit
	}

	if done {
		shard.done = true
		response.Remaining = 0
		r.lastDrifts = cloneDrifts(report)
		r.lastDigest = digestDrifts(report)
		r.stats.Files = len(shard.files)
	}

	return response, nil
}

// Classify returns the drift class that applies to one deviation.
func (r *AclBaselineReconciler) Classify(d Drift) DriftKind {
	if d.ReadOnly {
		return DriftUnfixable
	}
	return d.Kind
}

// Plan turns drifts into ordered idempotent repair steps.
func (r *AclBaselineReconciler) Plan(drifts []Drift) ([]FixStep, error) {
	now := r.currentTime()

	r.mu.RLock()
	currentGeneration := r.generation
	maxDrifts := r.limits.MaxDrifts
	maxSteps := r.limits.MaxSteps
	activeExemptions := make(map[string]bool, len(r.exemptions))
	for _, exemption := range r.exemptions {
		if now.Before(exemption.Until) {
			activeExemptions[exemption.Path] = true
		}
	}
	r.mu.RUnlock()

	if len(drifts) > maxDrifts {
		return nil, ErrDriftLimit
	}

	byPath := make(map[string]*stepAggregate)
	paths := make([]string, 0)
	for _, drift := range drifts {
		if drift.ReadOnly || drift.Kind == DriftUnfixable || activeExemptions[drift.Path] {
			continue
		}

		aggregate, ok := byPath[drift.Path]
		if !ok {
			aggregate = &stepAggregate{
				path:       drift.Path,
				depth:      drift.Depth,
				generation: drift.Generation,
			}
			byPath[drift.Path] = aggregate
			paths = append(paths, drift.Path)
		}

		if drift.Depth > aggregate.depth {
			aggregate.depth = drift.Depth
		}
		if drift.Generation > aggregate.generation {
			aggregate.generation = drift.Generation
		}

		remove, add := driftRepairEntries(drift)
		aggregate.remove = append(aggregate.remove, remove...)
		aggregate.add = append(aggregate.add, add...)
	}

	aggregates := make([]*stepAggregate, 0, len(paths))
	for _, path := range paths {
		aggregate := byPath[path]
		aggregate.remove = canonicalEntries(aggregate.remove)
		aggregate.add = canonicalEntries(aggregate.add)
		if aggregate.generation == 0 {
			aggregate.generation = currentGeneration
		}
		aggregates = append(aggregates, aggregate)
	}

	sort.Slice(aggregates, func(i, j int) bool {
		if aggregates[i].depth != aggregates[j].depth {
			return aggregates[i].depth < aggregates[j].depth
		}
		return aggregates[i].path < aggregates[j].path
	})

	if len(aggregates) > maxSteps {
		return nil, ErrStepLimit
	}

	depths := make([]int, 0)
	seenDepth := make(map[int]bool)
	for _, aggregate := range aggregates {
		if !seenDepth[aggregate.depth] {
			seenDepth[aggregate.depth] = true
			depths = append(depths, aggregate.depth)
		}
	}
	sort.Ints(depths)
	batchByDepth := make(map[int]int, len(depths))
	for batch, depth := range depths {
		batchByDepth[depth] = batch
	}

	steps := make([]FixStep, 0, len(aggregates))
	for _, aggregate := range aggregates {
		key := stableStepKey(
			aggregate.generation,
			aggregate.path,
			aggregate.depth,
			aggregate.remove,
			aggregate.add,
		)
		steps = append(steps, FixStep{
			Path:       aggregate.path,
			Depth:      aggregate.depth,
			Batch:      batchByDepth[aggregate.depth],
			Add:        aggregate.add,
			Remove:     aggregate.remove,
			Key:        key,
			Generation: aggregate.generation,
		})
	}

	return steps, nil
}

// ApplyStep applies one repair step idempotently.
func (r *AclBaselineReconciler) ApplyStep(step FixStep) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if step.Generation != r.generation {
		return ErrStaleGeneration
	}

	key := strings.TrimSpace(step.Key)
	if key == "" {
		remove := canonicalEntries(step.Remove)
		add := canonicalEntries(step.Add)
		key = stableStepKey(step.Generation, step.Path, step.Depth, remove, add)
	}
	if _, applied := r.appliedStepKeys[key]; applied {
		return nil
	}
	if r.appliedStepKeys == nil {
		r.appliedStepKeys = make(map[string]struct{})
	}

	r.appliedStepKeys[key] = struct{}{}
	return nil
}

// Exempt keeps a path out of the drift report until the ticket expires.
func (r *AclBaselineReconciler) Exempt(path string, until time.Time, ticket string) error {
	if strings.TrimSpace(ticket) == "" {
		return ErrBlankTicket
	}
	now := r.currentTime()
	if until.Before(now) {
		return ErrExemptPast
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.exemptions = append(r.exemptions, Exemption{
		Path:   path,
		Until:  until,
		Ticket: ticket,
	})
	return nil
}

// Exemptions lists the active exemption records.
func (r *AclBaselineReconciler) Exemptions() []Exemption {
	now := r.currentTime()

	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]Exemption, 0, len(r.exemptions))
	for _, exemption := range r.exemptions {
		if now.Before(exemption.Until) {
			result = append(result, exemption)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Path != result[j].Path {
			return result[i].Path < result[j].Path
		}
		if !result[i].Until.Equal(result[j].Until) {
			return result[i].Until.Before(result[j].Until)
		}
		return result[i].Ticket < result[j].Ticket
	})
	return result
}

// AppliedSteps counts the repair steps that took effect.
func (r *AclBaselineReconciler) AppliedSteps() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.appliedStepKeys)
}

// Digest fingerprints the most recent drift report.
func (r *AclBaselineReconciler) Digest() (string, error) {
	r.mu.RLock()
	digest := r.lastDigest
	drifts := r.lastDrifts
	r.mu.RUnlock()

	if digest == "" {
		digest = digestDrifts(drifts)
	}
	return digest, nil
}

// Generation returns the snapshot generation of the most recent scan.
func (r *AclBaselineReconciler) Generation() uint64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.generation
}

// Stats summarises the most recent scan pass.
func (r *AclBaselineReconciler) Stats() ScanStats {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.stats
}

func (r *AclBaselineReconciler) currentTime() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

func validateScanRequest(root string, baseline Baseline) error {
	if strings.TrimSpace(root) == "" {
		return ErrBlankRoot
	}
	if strings.TrimSpace(baseline.Version) == "" {
		return ErrBadBaseline
	}
	return nil
}

func pinSnapshot(snapshot TreeSnapshot) TreeSnapshot {
	pinned := snapshot
	pinned.Files = make([]FileAcl, len(snapshot.Files))
	for i, file := range snapshot.Files {
		file.Entries = cloneEntries(file.Entries)
		pinned.Files[i] = file
	}
	sort.SliceStable(pinned.Files, func(i, j int) bool {
		return pinned.Files[i].Path < pinned.Files[j].Path
	})
	return pinned
}

func cloneBaseline(baseline Baseline) Baseline {
	cloned := baseline
	cloned.Default = cloneEntries(baseline.Default)
	if baseline.Rules != nil {
		cloned.Rules = make(map[string][]Entry, len(baseline.Rules))
		for path, entries := range baseline.Rules {
			cloned.Rules[path] = cloneEntries(entries)
		}
	}
	return cloned
}

func cloneEntries(entries []Entry) []Entry {
	if entries == nil {
		return nil
	}
	cloned := make([]Entry, len(entries))
	copy(cloned, entries)
	return cloned
}

func cloneDrifts(drifts []Drift) []Drift {
	cloned := make([]Drift, len(drifts))
	for i, drift := range drifts {
		cloned[i] = drift
		cloned[i].Expected = cloneEntries(drift.Expected)
		cloned[i].Actual = cloneEntries(drift.Actual)
		cloned[i].Extra = cloneEntries(drift.Extra)
		cloned[i].Missing = cloneEntries(drift.Missing)
	}
	return cloned
}

func aggregateEntries(entries []Entry) map[string]uint32 {
	perSubject := make(map[string]uint32)
	for _, entry := range entries {
		perSubject[entry.Subject] |= entry.Bits
	}
	return perSubject
}

func canonicalEntries(entries []Entry) []Entry {
	perSubject := aggregateEntries(entries)
	canonical := make([]Entry, 0, len(perSubject))
	for subject, bits := range perSubject {
		canonical = append(canonical, Entry{Subject: subject, Bits: bits})
	}
	sort.Slice(canonical, func(i, j int) bool {
		return canonical[i].Subject < canonical[j].Subject
	})
	return canonical
}

func entriesFromMap(perSubject map[string]uint32) []Entry {
	entries := make([]Entry, 0, len(perSubject))
	for subject, bits := range perSubject {
		if bits != 0 {
			entries = append(entries, Entry{Subject: subject, Bits: bits})
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Subject < entries[j].Subject
	})
	return entries
}

func hasWildcard(subject string) bool {
	return strings.Contains(subject, "*")
}

func wildcardMatch(pattern, value string) bool {
	if !strings.Contains(pattern, "*") {
		return pattern == value
	}

	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == value
	}
	if !strings.HasPrefix(value, parts[0]) || !strings.HasSuffix(value, parts[len(parts)-1]) {
		return false
	}

	start := len(parts[0])
	end := len(value) - len(parts[len(parts)-1])
	if start > end {
		return false
	}
	middle := value[start:end]
	for _, part := range parts[1 : len(parts)-1] {
		if part == "" {
			continue
		}
		index := strings.Index(middle, part)
		if index < 0 {
			return false
		}
		middle = middle[index+len(part):]
	}
	return true
}

func baselineEntriesForPath(path string, baseline Baseline) []Entry {
	if baseline.Rules != nil {
		if entries, ok := baseline.Rules[path]; ok {
			return entries
		}
	}
	return baseline.Default
}

func compareFile(file FileAcl, baseline Baseline, generation uint64) []Drift {
	rules := baselineEntriesForPath(file.Path, baseline)
	actualBits := aggregateEntries(file.Entries)

	expectedBits := make(map[string]uint32)
	wildcardRules := make([]Entry, 0)
	for _, entry := range rules {
		if hasWildcard(entry.Subject) {
			wildcardRules = append(wildcardRules, entry)
			continue
		}
		expectedBits[entry.Subject] |= entry.Bits
	}

	for subject := range actualBits {
		bits := expectedBits[subject]
		for _, rule := range wildcardRules {
			if wildcardMatch(rule.Subject, subject) {
				bits |= rule.Bits
			}
		}
		expectedBits[subject] = bits
	}

	subjects := make(map[string]struct{})
	for subject := range actualBits {
		subjects[subject] = struct{}{}
	}
	for subject := range expectedBits {
		subjects[subject] = struct{}{}
	}
	orderedSubjects := make([]string, 0, len(subjects))
	for subject := range subjects {
		orderedSubjects = append(orderedSubjects, subject)
	}
	sort.Strings(orderedSubjects)

	extra := make([]Entry, 0)
	missing := make([]Entry, 0)
	for _, subject := range orderedSubjects {
		actual := actualBits[subject]
		expected := expectedBits[subject]
		if bits := actual &^ expected; bits != 0 {
			extra = append(extra, Entry{Subject: subject, Bits: bits})
		}
		if bits := expected &^ actual; bits != 0 {
			missing = append(missing, Entry{Subject: subject, Bits: bits})
		}
	}

	if len(extra) == 0 && len(missing) == 0 {
		return nil
	}

	base := Drift{
		Path:       file.Path,
		Expected:   entriesFromMap(expectedBits),
		Actual:     entriesFromMap(actualBits),
		Depth:      file.Depth,
		ReadOnly:   file.ReadOnly,
		Generation: generation,
	}

	if file.ReadOnly {
		drift := base
		drift.Kind = DriftUnfixable
		drift.Extra = extra
		drift.Missing = missing
		return []Drift{drift}
	}

	drifts := make([]Drift, 0, 2)
	if len(extra) > 0 {
		drift := base
		drift.Kind = DriftOverGrant
		drift.Extra = extra
		drifts = append(drifts, drift)
	}
	if len(missing) > 0 {
		drift := base
		drift.Kind = DriftMissing
		drift.Missing = missing
		drifts = append(drifts, drift)
	}
	return drifts
}

func (r *AclBaselineReconciler) processFilesLocked(
	root string,
	baseline Baseline,
	files []FileAcl,
	generation uint64,
) ([]Drift, int) {
	if r.cache == nil {
		r.cache = make(map[string]cacheRecord)
	}

	drifts := make([]Drift, 0)
	cached := 0
	for _, file := range files {
		cacheKey := root + "\x00" + file.Path
		var fileDrifts []Drift

		record, ok := r.cache[cacheKey]
		if ok && record.contentHash == file.ContentHash && record.baselineVersion == baseline.Version {
			cached++
			fileDrifts = cloneDrifts(record.drifts)
			for i := range fileDrifts {
				fileDrifts[i].Generation = generation
			}
		} else {
			fileDrifts = compareFile(file, baseline, generation)
			r.cache[cacheKey] = cacheRecord{
				contentHash:     file.ContentHash,
				baselineVersion: baseline.Version,
				drifts:          cloneDrifts(fileDrifts),
			}
		}

		drifts = append(drifts, fileDrifts...)
	}
	return drifts, cached
}

func (r *AclBaselineReconciler) activeReportLocked(drifts []Drift, now time.Time) []Drift {
	exemptPaths := make(map[string]struct{}, len(r.exemptions))
	for _, exemption := range r.exemptions {
		if now.Before(exemption.Until) {
			exemptPaths[exemption.Path] = struct{}{}
		}
	}
	report := make([]Drift, 0, len(drifts))
	for _, drift := range drifts {
		if _, exempted := exemptPaths[drift.Path]; exempted {
			continue
		}
		report = append(report, drift)
	}
	sortDrifts(report)
	return report
}

func driftKindOrder(kind DriftKind) int {
	switch kind {
	case DriftOverGrant:
		return 0
	case DriftMissing:
		return 1
	case DriftExpiredTemp:
		return 2
	case DriftInheritConflict:
		return 3
	case DriftUnfixable:
		return 4
	default:
		return 5
	}
}

func sortDrifts(drifts []Drift) {
	sort.SliceStable(drifts, func(i, j int) bool {
		if drifts[i].Path != drifts[j].Path {
			return drifts[i].Path < drifts[j].Path
		}
		leftOrder := driftKindOrder(drifts[i].Kind)
		rightOrder := driftKindOrder(drifts[j].Kind)
		if leftOrder != rightOrder {
			return leftOrder < rightOrder
		}
		return drifts[i].Kind < drifts[j].Kind
	})
}

func driftRepairEntries(drift Drift) ([]Entry, []Entry) {
	remove := canonicalEntries(drift.Extra)
	add := canonicalEntries(drift.Missing)
	if len(remove) > 0 || len(add) > 0 {
		return remove, add
	}
	if len(drift.Expected) == 0 && len(drift.Actual) == 0 {
		return remove, add
	}

	expected := aggregateEntries(drift.Expected)
	actual := aggregateEntries(drift.Actual)
	subjects := make(map[string]struct{})
	for subject := range expected {
		subjects[subject] = struct{}{}
	}
	for subject := range actual {
		subjects[subject] = struct{}{}
	}
	for subject := range subjects {
		if bits := actual[subject] &^ expected[subject]; bits != 0 {
			remove = append(remove, Entry{Subject: subject, Bits: bits})
		}
		if bits := expected[subject] &^ actual[subject]; bits != 0 {
			add = append(add, Entry{Subject: subject, Bits: bits})
		}
	}
	return canonicalEntries(remove), canonicalEntries(add)
}

func entrySetKey(entries []Entry) string {
	parts := make([]string, 0, len(entries))
	for _, entry := range entries {
		parts = append(parts, fmt.Sprintf("%s=%d", entry.Subject, entry.Bits))
	}
	return strings.Join(parts, ",")
}

func stableStepKey(generation uint64, path string, depth int, remove, add []Entry) string {
	source := strings.Join([]string{
		fmt.Sprintf("generation=%d", generation),
		fmt.Sprintf("path=%s", path),
		fmt.Sprintf("depth=%d", depth),
		"remove=" + entrySetKey(remove),
		"add=" + entrySetKey(add),
	}, "\n")
	sum := sha256.Sum256([]byte(source))
	return hex.EncodeToString(sum[:])
}

func canonicalDrifts(drifts []Drift) []Drift {
	canonical := make([]Drift, len(drifts))
	for i, drift := range drifts {
		copyDrift := drift
		copyDrift.Expected = canonicalEntries(drift.Expected)
		copyDrift.Actual = canonicalEntries(drift.Actual)
		copyDrift.Extra = canonicalEntries(drift.Extra)
		copyDrift.Missing = canonicalEntries(drift.Missing)
		canonical[i] = copyDrift
	}
	sortDrifts(canonical)
	return canonical
}

func digestDrifts(drifts []Drift) string {
	if drifts == nil {
		drifts = []Drift{}
	}
	canonical := canonicalDrifts(drifts)
	payload, err := json.Marshal(canonical)
	if err != nil {
		payload = []byte("[]")
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
