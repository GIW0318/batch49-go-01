// Package task compiles network policies into a per workload allow matrix and
// evaluates live flows against that matrix.
package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Transport protocols accepted inside a policy rule.
const (
	ProtocolTCP = "tcp"
	ProtocolUDP = "udp"
)

// Traffic directions carried by a flow and by a matrix entry.
const (
	DirectionIngress = "ingress"
	DirectionEgress  = "egress"
)

// Decisions returned by Evaluate.
const (
	DecisionAllow = "allow"
	DecisionDeny  = "deny"
)

// Reason codes attached to a decision.
const (
	ReasonDefaultAllow = "default_allow"
	ReasonRuleMatch    = "rule_match"
	ReasonNoRule       = "no_rule"
	ReasonUnknown      = "unknown_workload"
)

// Kinds of a matrix diff record.
const (
	ChangeAdded   = "added"
	ChangeRemoved = "removed"
)

// Errors returned by the compiler.
var (
	ErrBlankPolicy       = errors.New("policy name is required")
	ErrDuplicatePolicy   = errors.New("duplicate policy name")
	ErrBlankWorkload     = errors.New("workload id is required")
	ErrDuplicateWorkload = errors.New("duplicate workload id")
	ErrInvalidPolicy     = errors.New("policy is invalid")
	ErrBadSnapshot       = errors.New("snapshot payload is invalid")
)

// Decision is the verdict for one evaluated flow.
type Decision string

// Reason explains one decision: which policy and which rule produced it.
type Reason struct {
	Code   string
	Policy string
	Rule   int
}

// PortRef is a policy port reference. It is either numeric (Port, optionally
// with EndPort for a range) or a named port resolved from the workload.
type PortRef struct {
	Name     string
	Port     int
	EndPort  int
	Protocol string
}

// Peer is one ingress or egress rule source or destination.
type Peer struct {
	NamespaceSelector map[string]string
	PodSelector       map[string]string
	IPBlock           string
	Except            []string
	Ports             []PortRef
}

// Policy selects workloads in one namespace and carries ingress and egress
// peers.
type Policy struct {
	Name      string
	Namespace string
	Selector  map[string]string
	Ingress   []Peer
	Egress    []Peer
}

// Workload is one addressable workload with its labels and named ports.
type Workload struct {
	ID        string
	Namespace string
	Labels    map[string]string
	Ports     map[string]int
}

// Flow is one live connection to evaluate against the compiled matrix.
type Flow struct {
	Direction     string
	PeerIP        string
	PeerNamespace string
	PeerLabels    map[string]string
	Port          int
	Protocol      string
}

// Entry is one normalised allowed rule in the compiled matrix.
type Entry struct {
	WorkloadID string
	Direction  string
	Policy     string
	Rule       int
	Peer       string
	Protocol   string
	PortStart  int
	PortEnd    int
}

// Diagnostic is one compile time finding that did not abort the compile.
type Diagnostic struct {
	Policy string
	Code   string
	Detail string
}

// Matrix is the deterministic compiled result for a set of policies.
type Matrix struct {
	Workloads   []string
	Entries     []Entry
	Diagnostics []Diagnostic
}

// Change is one entry level difference between two matrices.
type Change struct {
	WorkloadID string
	Direction  string
	Kind       string
	Policy     string
	Rule       int
	Protocol   string
	PortStart  int
	PortEnd    int
}

// NetPolCompiler owns the compiled matrix.
type NetPolCompiler struct {
}

// NewNetPolCompiler builds an empty compiler.
func NewNetPolCompiler() *NetPolCompiler {
	return &NetPolCompiler{}
}

// Compile normalises the policies against the workloads into a matrix.
func (c *NetPolCompiler) Compile(policies []Policy, workloads []Workload) (*Matrix, error) {
	return nil, errNotImplemented
}

// Evaluate decides one flow for one workload.
func (c *NetPolCompiler) Evaluate(workloadID string, flow Flow) (Decision, Reason) {
	return DecisionDeny, Reason{}
}

// Diff reports entry level differences between two matrices.
func Diff(old, next *Matrix) []Change {
	return nil
}

// Snapshot serialises the compiled matrix.
func (c *NetPolCompiler) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the compiled matrix from a snapshot.
func (c *NetPolCompiler) Restore(data []byte) error {
	return errNotImplemented
}

// WorkloadIDs lists the compiled workload ids in ascending order.
func (c *NetPolCompiler) WorkloadIDs() []string {
	return nil
}

// Entries returns the matrix entries of one workload.
func (c *NetPolCompiler) Entries(workloadID string) []Entry {
	return nil
}

// Diagnostics returns the compile time findings of the last compile.
func (c *NetPolCompiler) Diagnostics() []Diagnostic {
	return nil
}

// PolicyNames returns the accepted policy names of the last compile.
func (c *NetPolCompiler) PolicyNames() []string {
	return nil
}

// ValidatePolicies checks policy names and port references without compiling.
func ValidatePolicies(policies []Policy) error {
	return errNotImplemented
}

// SelectorMatches reports whether every selector pair is present in labels.
func SelectorMatches(selector, labels map[string]string) bool {
	return false
}
