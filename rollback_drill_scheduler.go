// Package task schedules rollback drills on a sample of hosts and keeps a
// coverage ledger of simulated versus expected steps.
package task

import (
	"errors"
	"sort"
	"strings"
	"time"
)

// Drill outcomes.
const (
	DrillPassed     = "passed"
	DrillFailed     = "failed"
	DrillIncomplete = "incomplete"
)

// Mismatch kinds produced by step comparison.
const (
	MismatchMissing = "missing"
	MismatchOrder   = "order"
	MismatchResult  = "result"
)

// Errors surfaced by the scheduler.
var (
	ErrBlankField     = errors.New("host, fleet, batch, step and image are required")
	ErrDuplicateHost  = errors.New("host is already enrolled")
	ErrDuplicateBatch = errors.New("batch is already registered")
	ErrUnknownHost    = errors.New("host is not enrolled")
	ErrUnknownBatch   = errors.New("batch is not registered")
	ErrUnknownFleet   = errors.New("fleet has no enrolled hosts")
	ErrUnknownImage   = errors.New("image is not part of the batch")
	ErrBadLimit       = errors.New("sample size must be positive")
	ErrHostOffBatch   = errors.New("host is no longer on the requested batch")
	ErrNoSimulation   = errors.New("no simulation recorded for this host and batch")
	ErrDrillTimeout   = errors.New("simulated drill exceeded the time budget")
)

// BatchSpec is the expected rollback step list of one patch batch.
type BatchSpec struct {
	ID     string   `json:"id"`
	Steps  []string `json:"steps"`
	Images []string `json:"images"`
}

// Host is one enrolled machine.
type Host struct {
	ID    string `json:"id"`
	Fleet string `json:"fleet"`
	Batch string `json:"batch"`
	Image string `json:"image"`
}

// Mismatch is one difference between expected and simulated steps.
type Mismatch struct {
	Kind string `json:"kind"`
	Step string `json:"step"`
	Want string `json:"want"`
	Got  string `json:"got"`
}

// DrillResult is the outcome of one simulated drill.
type DrillResult struct {
	Host       string        `json:"host"`
	Batch      string        `json:"batch"`
	Image      string        `json:"image"`
	Status     string        `json:"status"`
	Mismatches []Mismatch    `json:"mismatches"`
	Elapsed    time.Duration `json:"elapsed"`
	Input      []string      `json:"input"`
	At         time.Time     `json:"at"`
}

// Coverage is the per fleet drill ledger.
type Coverage struct {
	Fleet            string   `json:"fleet"`
	TotalHosts       int      `json:"total_hosts"`
	DrilledHosts     int      `json:"drilled_hosts"`
	UncoveredBatches []string `json:"uncovered_batches"`
	Gaps             int      `json:"gaps"`
}

// DrillFailure is one failed or incomplete drill kept for triage.
type DrillFailure struct {
	Host       string     `json:"host"`
	Batch      string     `json:"batch"`
	Image      string     `json:"image"`
	Category   string     `json:"category"`
	Mismatches []Mismatch `json:"mismatches"`
}

// Options injects the clock and the drill time budget.
type Options struct {
	Now         func() time.Time
	DrillBudget time.Duration
}

// RollbackDrillScheduler owns hosts, batch specs, simulations and results.
type RollbackDrillScheduler struct {
	now     func() time.Time
	budget  time.Duration
	hosts   map[string]Host
	batches map[string]BatchSpec
	sims    map[string]simulation
	results map[string]DrillResult
	cycles  map[string]map[string]bool
}

// simulation is one recorded sandbox run for a host and batch.
type simulation struct {
	steps   []string
	elapsed time.Duration
	at      time.Time
}

// bucket is one batch/image stratum used by sampling.
type bucket struct {
	batch string
	image string
	hosts []string
}

func ledgerKey(host, batch string) string {
	return host + "\x00" + batch
}

func blank(fields ...string) bool {
	for _, f := range fields {
		if strings.TrimSpace(f) == "" {
			return true
		}
	}
	return false
}

// NewRollbackDrillScheduler builds a scheduler; a nil clock means time.Now.
func NewRollbackDrillScheduler(opts Options) *RollbackDrillScheduler {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &RollbackDrillScheduler{
		now:     now,
		budget:  opts.DrillBudget,
		hosts:   map[string]Host{},
		batches: map[string]BatchSpec{},
		sims:    map[string]simulation{},
		results: map[string]DrillResult{},
		cycles:  map[string]map[string]bool{},
	}
}

// Enroll adds a host to a fleet.
func (s *RollbackDrillScheduler) Enroll(host, fleet string) error {
	if blank(host, fleet) {
		return ErrBlankField
	}
	if _, ok := s.hosts[host]; ok {
		return ErrDuplicateHost
	}
	s.hosts[host] = Host{ID: host, Fleet: fleet}
	return nil
}

// RegisterBatch stores the expected rollback steps of a batch.
func (s *RollbackDrillScheduler) RegisterBatch(spec BatchSpec) error {
	if blank(spec.ID) || len(spec.Steps) == 0 || len(spec.Images) == 0 {
		return ErrBlankField
	}
	for _, step := range spec.Steps {
		if blank(step) {
			return ErrBlankField
		}
	}
	for _, image := range spec.Images {
		if blank(image) {
			return ErrBlankField
		}
	}
	if _, ok := s.batches[spec.ID]; ok {
		return ErrDuplicateBatch
	}
	steps := append([]string(nil), spec.Steps...)
	images := append([]string(nil), spec.Images...)
	s.batches[spec.ID] = BatchSpec{ID: spec.ID, Steps: steps, Images: images}
	return nil
}

// Assign pins a host to one batch and image.
func (s *RollbackDrillScheduler) Assign(host, batch, image string) error {
	if blank(host, batch, image) {
		return ErrBlankField
	}
	h, ok := s.hosts[host]
	if !ok {
		return ErrUnknownHost
	}
	spec, ok := s.batches[batch]
	if !ok {
		return ErrUnknownBatch
	}
	found := false
	for _, img := range spec.Images {
		if img == image {
			found = true
			break
		}
	}
	if !found {
		return ErrUnknownImage
	}
	h.Batch = batch
	h.Image = image
	s.hosts[host] = h
	return nil
}

// Sample picks n hosts from a fleet, stratified by batch and image.
func (s *RollbackDrillScheduler) Sample(fleet string, n int) ([]string, error) {
	if blank(fleet) {
		return nil, ErrBlankField
	}
	if n <= 0 {
		return nil, ErrBadLimit
	}
	hosts := s.fleetHosts(fleet)
	if len(hosts) == 0 {
		return nil, ErrUnknownFleet
	}
	buckets := stratify(hosts)
	cycle, ok := s.cycles[fleet]
	if !ok {
		cycle = map[string]bool{}
		s.cycles[fleet] = cycle
	}
	picked := make([]string, 0, n)
	inCall := map[string]bool{}
	for len(picked) < n {
		progress := false
		for _, b := range buckets {
			for _, id := range b.hosts {
				if cycle[id] || inCall[id] {
					continue
				}
				picked = append(picked, id)
				inCall[id] = true
				cycle[id] = true
				progress = true
				break
			}
			if len(picked) == n {
				break
			}
		}
		if !progress {
			if len(inCall) == len(hosts) {
				// Every host was already picked in this call; n exceeds the
				// fleet size and duplicates within one call are not allowed.
				break
			}
			// The cycle is exhausted: start a fresh one.
			cycle = map[string]bool{}
			s.cycles[fleet] = cycle
		}
	}
	return picked, nil
}

// Drill compares a recorded simulation with the expected batch steps.
func (s *RollbackDrillScheduler) Drill(host string, batch string) (DrillResult, error) {
	if blank(host, batch) {
		return DrillResult{}, ErrBlankField
	}
	h, ok := s.hosts[host]
	if !ok {
		return DrillResult{}, ErrUnknownHost
	}
	spec, ok := s.batches[batch]
	if !ok {
		return DrillResult{}, ErrUnknownBatch
	}
	if h.Batch != batch {
		return DrillResult{}, ErrHostOffBatch
	}
	sim, ok := s.sims[ledgerKey(host, batch)]
	if !ok {
		return DrillResult{}, ErrNoSimulation
	}
	mismatches := compareSteps(spec.Steps, sim.steps)
	status := DrillPassed
	switch {
	case s.budget > 0 && sim.elapsed > s.budget:
		status = DrillIncomplete
	case len(mismatches) > 0:
		status = DrillFailed
	}
	result := DrillResult{
		Host:       host,
		Batch:      batch,
		Image:      h.Image,
		Status:     status,
		Mismatches: mismatches,
		Elapsed:    sim.elapsed,
		Input:      append([]string(nil), spec.Steps...),
		At:         s.now(),
	}
	s.results[ledgerKey(host, batch)] = result
	return result, nil
}

// Coverage reports drilled hosts, uncovered batches and gaps.
func (s *RollbackDrillScheduler) Coverage(fleet string) (Coverage, error) {
	if blank(fleet) {
		return Coverage{}, ErrBlankField
	}
	hosts := s.fleetHosts(fleet)
	if len(hosts) == 0 {
		return Coverage{}, ErrUnknownFleet
	}
	type combo struct {
		batch string
		image string
	}
	covered := map[combo]bool{}
	present := map[combo]bool{}
	drilled := 0
	for _, h := range hosts {
		if h.Batch == "" {
			continue
		}
		c := combo{batch: h.Batch, image: h.Image}
		present[c] = true
		result, ok := s.results[ledgerKey(h.ID, h.Batch)]
		if ok && result.Image == h.Image {
			drilled++
			covered[c] = true
		}
	}
	gaps := 0
	uncovered := []string{}
	seenBatch := map[string]bool{}
	combos := make([]combo, 0, len(present))
	for c := range present {
		combos = append(combos, c)
	}
	sort.Slice(combos, func(i, j int) bool {
		if combos[i].batch != combos[j].batch {
			return combos[i].batch < combos[j].batch
		}
		return combos[i].image < combos[j].image
	})
	for _, c := range combos {
		if covered[c] {
			continue
		}
		gaps++
		if !seenBatch[c.batch] {
			seenBatch[c.batch] = true
			uncovered = append(uncovered, c.batch)
		}
	}
	return Coverage{
		Fleet:            fleet,
		TotalHosts:       len(hosts),
		DrilledHosts:     drilled,
		UncoveredBatches: uncovered,
		Gaps:             gaps,
	}, nil
}

// Failures lists the failed or incomplete drills of a fleet.
func (s *RollbackDrillScheduler) Failures(fleet string) []DrillFailure {
	var out []DrillFailure
	for _, h := range s.fleetHosts(fleet) {
		if h.Batch == "" {
			continue
		}
		result, ok := s.results[ledgerKey(h.ID, h.Batch)]
		if !ok || result.Image != h.Image || result.Status == DrillPassed {
			continue
		}
		category := DrillIncomplete
		if len(result.Mismatches) > 0 {
			category = result.Mismatches[0].Kind
		}
		out = append(out, DrillFailure{
			Host:       h.ID,
			Batch:      result.Batch,
			Image:      result.Image,
			Category:   category,
			Mismatches: result.Mismatches,
		})
	}
	return out
}

// RecordSimulation stores what a sandboxed rollback would do.
func (s *RollbackDrillScheduler) RecordSimulation(host, batch string, steps []string, elapsed time.Duration) error {
	if blank(host, batch) || len(steps) == 0 {
		return ErrBlankField
	}
	for _, step := range steps {
		if blank(step) {
			return ErrBlankField
		}
	}
	if _, ok := s.hosts[host]; !ok {
		return ErrUnknownHost
	}
	if _, ok := s.batches[batch]; !ok {
		return ErrUnknownBatch
	}
	s.sims[ledgerKey(host, batch)] = simulation{
		steps:   append([]string(nil), steps...),
		elapsed: elapsed,
		at:      s.now(),
	}
	return nil
}

// Hosts lists the enrolled hosts of a fleet, sorted by id.
func (s *RollbackDrillScheduler) Hosts(fleet string) []Host {
	hosts := s.fleetHosts(fleet)
	if len(hosts) == 0 {
		return nil
	}
	return hosts
}

// fleetHosts returns the hosts of one fleet sorted by id.
func (s *RollbackDrillScheduler) fleetHosts(fleet string) []Host {
	var hosts []Host
	for _, h := range s.hosts {
		if h.Fleet == fleet {
			hosts = append(hosts, h)
		}
	}
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].ID < hosts[j].ID })
	return hosts
}

// stratify groups hosts into batch/image buckets sorted by batch then image;
// hosts inside a bucket are sorted by id.
func stratify(hosts []Host) []bucket {
	byKey := map[string]*bucket{}
	var keys []string
	for _, h := range hosts {
		key := h.Batch + "\x00" + h.Image
		b, ok := byKey[key]
		if !ok {
			b = &bucket{batch: h.Batch, image: h.Image}
			byKey[key] = b
			keys = append(keys, key)
		}
		b.hosts = append(b.hosts, h.ID)
	}
	buckets := make([]bucket, 0, len(keys))
	for _, key := range keys {
		buckets = append(buckets, *byKey[key])
	}
	sort.Slice(buckets, func(i, j int) bool {
		if buckets[i].batch != buckets[j].batch {
			return buckets[i].batch < buckets[j].batch
		}
		return buckets[i].image < buckets[j].image
	})
	return buckets
}

// compareSteps diffs the expected batch steps against the simulated ones.
func compareSteps(expected, actual []string) []Mismatch {
	inExpected := map[string]bool{}
	for _, step := range expected {
		inExpected[step] = true
	}
	var mismatches []Mismatch
	for i, want := range expected {
		if i >= len(actual) {
			mismatches = append(mismatches, Mismatch{Kind: MismatchMissing, Step: want, Want: want})
			continue
		}
		got := actual[i]
		if got == want {
			continue
		}
		kind := MismatchResult
		if inExpected[got] {
			kind = MismatchOrder
		}
		mismatches = append(mismatches, Mismatch{Kind: kind, Step: want, Want: want, Got: got})
	}
	for i := len(expected); i < len(actual); i++ {
		mismatches = append(mismatches, Mismatch{Kind: MismatchResult, Step: actual[i], Got: actual[i]})
	}
	return mismatches
}
