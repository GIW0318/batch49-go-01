// Package task derives the smallest capability set a workload needs from a
// recorded access trace and compares it with the profile the workload runs
// with today.
package task

import (
	"bytes"
	"encoding/gob"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Access operations understood by the solver.
const (
	OpOpen    = "open"
	OpStat    = "stat"
	OpReaddir = "readdir"
	OpChmod   = "chmod"
	OpChown   = "chown"
	OpExec    = "exec"
)

// Open modes carried by an open access.
const (
	ModeRead      = "r"
	ModeWrite     = "w"
	ModeReadWrite = "rw"
	ModeDir       = "rdir"
)

// Capability names produced by the solver.
const (
	CapReadFile  = "cap_read_file"
	CapWriteFile = "cap_write_file"
	CapReadDir   = "cap_read_dir"
	CapStatFile  = "cap_stat_file"
	CapChmod     = "cap_chmod"
	CapChown     = "cap_chown"
	CapExec      = "cap_exec"
)

// Access is one recorded file operation of a workload.
type Access struct {
	Op     string
	Path   string
	Mode   string
	Denied bool
	Seq    uint64
}

// Demand is the capability set the trace asks for.
type Demand struct {
	Caps  []string
	ByCap map[string][]string
}

// Profile is the capability set a workload holds today.
type Profile struct {
	Caps []string
}

// Diff compares a demand with an existing profile.
type Diff struct {
	Missing []string
	Extra   []string
}

// SolverConfig carries the hard limit of one trace.
type SolverConfig struct {
	MaxAccesses int
}

var (
	ErrBadAccess       = errors.New("access needs an operation and a path")
	ErrAccessLimit     = errors.New("access trace limit reached")
	ErrUncoveredAccess = errors.New("trace contains an access that maps to no capability")
	ErrEmptyTrace      = errors.New("access trace is empty")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// CapabilityDemandSolver records accesses and answers capability questions.
type CapabilityDemandSolver struct {
	mu     sync.RWMutex
	config SolverConfig
	trace  []Access
}

type solverSnapshot struct {
	Magic         string
	FormatVersion int
	Config        SolverConfig
	Trace         []Access
}

const snapshotMagic = "gsb-batch49-go-safety-d-09-capability-demand-v1"
const snapshotVersion = 1

// NewCapabilityDemandSolver returns a solver with an empty trace.
func NewCapabilityDemandSolver(cfg SolverConfig) *CapabilityDemandSolver {
	return &CapabilityDemandSolver{
		config: cfg,
		trace:  make([]Access, 0),
	}
}

// AddAccess appends one access to the trace.
func (s *CapabilityDemandSolver) AddAccess(a Access) error {
	if a.Op == "" || a.Path == "" {
		return ErrBadAccess
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	limit := positiveLimit(s.config.MaxAccesses)
	if len(s.trace) >= limit {
		return fmt.Errorf("%w: max_accesses=%d", ErrAccessLimit, s.config.MaxAccesses)
	}

	s.trace = append(s.trace, a)
	return nil
}

// Solve returns the minimal capability set that covers the whole trace.
func (s *CapabilityDemandSolver) Solve() (Demand, error) {
	s.mu.RLock()
	traceLen := len(s.trace)
	evidence := buildEvidence(s.trace)
	s.mu.RUnlock()

	if traceLen == 0 {
		return Demand{}, ErrEmptyTrace
	}
	if len(evidence.uncovered) > 0 {
		return Demand{Caps: []string{}, ByCap: map[string][]string{}},
			fmt.Errorf("%w: %d access(es)", ErrUncoveredAccess, len(evidence.uncovered))
	}

	caps := make([]string, 0, len(evidence.pathsByCap))
	for capName := range evidence.pathsByCap {
		caps = append(caps, capName)
	}
	sort.Strings(caps)

	byCap := make(map[string][]string, len(caps))
	for _, capName := range caps {
		pathSet := evidence.pathsByCap[capName]
		paths := make([]string, 0, len(pathSet))
		for path := range pathSet {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		byCap[capName] = paths
	}

	return Demand{Caps: caps, ByCap: byCap}, nil
}

// Diff compares a solved demand with the profile in use.
func (s *CapabilityDemandSolver) Diff(demand Demand, current Profile) (Diff, error) {
	needed := normalizedStringSet(demand.Caps)
	held := normalizedStringSet(current.Caps)

	missing := make([]string, 0)
	for capName := range needed {
		if _, ok := held[capName]; !ok {
			missing = append(missing, capName)
		}
	}
	sort.Strings(missing)

	extra := make([]string, 0)
	for capName := range held {
		if _, ok := needed[capName]; !ok {
			extra = append(extra, capName)
		}
	}
	sort.Strings(extra)

	return Diff{Missing: missing, Extra: extra}, nil
}

// Explain returns the accesses that justify one capability.
func (s *CapabilityDemandSolver) Explain(cap string) []Access {
	s.mu.RLock()
	explained := buildEvidence(s.trace).explain[cap]
	s.mu.RUnlock()
	return copyAccesses(explained)
}

// Uncovered returns the accesses that map to no capability.
func (s *CapabilityDemandSolver) Uncovered() []Access {
	s.mu.RLock()
	uncovered := buildEvidence(s.trace).uncovered
	s.mu.RUnlock()
	return copyAccesses(uncovered)
}

// Snapshot serialises the recorded trace.
func (s *CapabilityDemandSolver) Snapshot() ([]byte, error) {
	s.mu.RLock()
	payload := solverSnapshot{
		Magic:         snapshotMagic,
		FormatVersion: snapshotVersion,
		Config:        s.config,
		Trace:         copyAccesses(s.trace),
	}
	s.mu.RUnlock()

	var buffer bytes.Buffer
	if err := gob.NewEncoder(&buffer).Encode(&payload); err != nil {
		return nil, fmt.Errorf("encode capability solver snapshot: %w", err)
	}
	return buffer.Bytes(), nil
}

// Restore rebuilds the trace from a snapshot produced by Snapshot.
func (s *CapabilityDemandSolver) Restore(payload []byte) error {
	var snapshot solverSnapshot
	if len(payload) == 0 {
		return fmt.Errorf("%w: empty payload", ErrBadSnapshot)
	}
	if err := gob.NewDecoder(bytes.NewReader(payload)).Decode(&snapshot); err != nil {
		return fmt.Errorf("%w: decode: %v", ErrBadSnapshot, err)
	}
	if snapshot.Magic != snapshotMagic || snapshot.FormatVersion != snapshotVersion {
		return fmt.Errorf("%w: unsupported snapshot format", ErrBadSnapshot)
	}
	if err := validateSnapshot(snapshot); err != nil {
		return err
	}

	trace := copyAccesses(snapshot.Trace)
	s.mu.Lock()
	s.config = snapshot.Config
	s.trace = trace
	s.mu.Unlock()
	return nil
}

// Count reports how many accesses are recorded.
func (s *CapabilityDemandSolver) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.trace)
}

// Accesses returns the recorded trace in insertion order.
func (s *CapabilityDemandSolver) Accesses() []Access {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return copyAccesses(s.trace)
}

// Reset drops the recorded trace.
func (s *CapabilityDemandSolver) Reset() error {
	s.mu.Lock()
	s.trace = make([]Access, 0)
	s.mu.Unlock()
	return nil
}

type evidence struct {
	pathsByCap map[string]map[string]struct{}
	explain    map[string][]Access
	uncovered  []Access
}

func buildEvidence(trace []Access) evidence {
	suppressedWriteEvidence := postconditionSuppressions(trace)
	result := evidence{
		pathsByCap: make(map[string]map[string]struct{}),
		explain:    make(map[string][]Access),
	}

	for i, access := range trace {
		if access.Denied {
			continue
		}

		caps, mappable := capsForAccess(access)
		if !mappable {
			result.uncovered = append(result.uncovered, access)
			continue
		}

		for _, capName := range caps {
			if suppressedWriteEvidence[i][capName] {
				continue
			}

			paths := result.pathsByCap[capName]
			if paths == nil {
				paths = make(map[string]struct{})
				result.pathsByCap[capName] = paths
			}
			paths[access.Path] = struct{}{}
			result.explain[capName] = append(result.explain[capName], access)
		}
	}

	return result
}

// postconditionSuppressions folds the specified write-then-chown sequence.
// A later allowed chown on the same path replaces the earlier write demand;
// raw trace records remain untouched and are still restored from snapshots.
func postconditionSuppressions(trace []Access) map[int]map[string]bool {
	suppressed := make(map[int]map[string]bool)
	pendingWrites := make(map[string][]int)

	for i, access := range trace {
		if access.Denied {
			continue
		}

		caps, mappable := capsForAccess(access)
		if !mappable {
			continue
		}

		if access.Op == OpChown {
			for _, writeIndex := range pendingWrites[access.Path] {
				if suppressed[writeIndex] == nil {
					suppressed[writeIndex] = make(map[string]bool)
				}
				suppressed[writeIndex][CapWriteFile] = true
			}
			delete(pendingWrites, access.Path)
		}

		for _, capName := range caps {
			if capName == CapWriteFile {
				pendingWrites[access.Path] = append(pendingWrites[access.Path], i)
				break
			}
		}
	}

	return suppressed
}

func capsForAccess(access Access) ([]string, bool) {
	var caps []string

	switch access.Op {
	case OpOpen:
		if access.Mode == ModeDir {
			caps = append(caps, CapReadDir)
			break
		}
		if strings.ContainsRune(access.Mode, 'r') {
			caps = append(caps, CapReadFile)
		}
		if strings.ContainsRune(access.Mode, 'w') {
			caps = append(caps, CapWriteFile)
		}
	case OpReaddir:
		caps = append(caps, CapReadDir)
	case OpStat:
		caps = append(caps, CapStatFile)
	case OpChmod:
		caps = append(caps, CapChmod)
	case OpChown:
		caps = append(caps, CapChown)
	case OpExec:
		caps = append(caps, CapExec)
	}

	sort.Strings(caps)
	return caps, len(caps) > 0
}

func normalizedStringSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

func copyAccesses(accesses []Access) []Access {
	if len(accesses) == 0 {
		return []Access{}
	}
	return append([]Access(nil), accesses...)
}

func positiveLimit(maxAccesses int) int {
	if maxAccesses < 0 {
		return 0
	}
	return maxAccesses
}

func validateSnapshot(snapshot solverSnapshot) error {
	limit := positiveLimit(snapshot.Config.MaxAccesses)
	if len(snapshot.Trace) > limit {
		return fmt.Errorf("%w: trace exceeds MaxAccesses", ErrBadSnapshot)
	}
	for _, access := range snapshot.Trace {
		if access.Op == "" || access.Path == "" {
			return fmt.Errorf("%w: access is missing op or path", ErrBadSnapshot)
		}
	}
	return nil
}
