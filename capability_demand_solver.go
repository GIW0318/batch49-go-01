// Package task derives the smallest capability set a workload needs from a
// recorded access trace and compares it with the profile the workload runs
// with today.
package task

import "errors"

// errNotImplemented marks behaviour that still has to be implemented.
var errNotImplemented = errors.New("not implemented")

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
	config SolverConfig
}

// NewCapabilityDemandSolver returns a solver with an empty trace.
func NewCapabilityDemandSolver(cfg SolverConfig) *CapabilityDemandSolver {
	return &CapabilityDemandSolver{config: cfg}
}

// AddAccess appends one access to the trace.
func (s *CapabilityDemandSolver) AddAccess(a Access) error {
	return errNotImplemented
}

// Solve returns the minimal capability set that covers the whole trace.
func (s *CapabilityDemandSolver) Solve() (Demand, error) {
	return Demand{}, errNotImplemented
}

// Diff compares a solved demand with the profile in use.
func (s *CapabilityDemandSolver) Diff(demand Demand, current Profile) (Diff, error) {
	return Diff{}, errNotImplemented
}

// Explain returns the accesses that justify one capability.
func (s *CapabilityDemandSolver) Explain(cap string) []Access {
	return nil
}

// Uncovered returns the accesses that map to no capability.
func (s *CapabilityDemandSolver) Uncovered() []Access {
	return nil
}

// Snapshot serialises the recorded trace.
func (s *CapabilityDemandSolver) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore rebuilds the trace from a snapshot produced by Snapshot.
func (s *CapabilityDemandSolver) Restore(payload []byte) error {
	return errNotImplemented
}

// Count reports how many accesses are recorded.
func (s *CapabilityDemandSolver) Count() int {
	return 0
}

// Accesses returns the recorded trace in insertion order.
func (s *CapabilityDemandSolver) Accesses() []Access {
	return nil
}

// Reset drops the recorded trace.
func (s *CapabilityDemandSolver) Reset() error {
	return errNotImplemented
}
