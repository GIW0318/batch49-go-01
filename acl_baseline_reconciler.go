// Package task reconciles the real ACL of a managed tree against a baseline
// policy and turns the drift into an ordered repair plan.
package task

import (
	"errors"
	"time"
)

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

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

// AclBaselineReconciler compares actual ACLs with a baseline policy.
type AclBaselineReconciler struct {
	tree   Tree
	limits Limits
	now    func() time.Time
}

// NewAclBaselineReconciler builds a reconciler over a tree source.
func NewAclBaselineReconciler(tree Tree, l Limits, now func() time.Time) *AclBaselineReconciler {
	return &AclBaselineReconciler{tree: tree, limits: l, now: now}
}

// Scan compares every file of one tree with the baseline.
func (r *AclBaselineReconciler) Scan(root string, baseline Baseline) ([]Drift, error) {
	return nil, errNotImplemented
}

// ScanShard compares at most budget files and reports whether the pass is done.
func (r *AclBaselineReconciler) ScanShard(root string, baseline Baseline, budget int) (Shard, error) {
	return Shard{}, errNotImplemented
}

// Classify returns the drift class that applies to one deviation.
func (r *AclBaselineReconciler) Classify(d Drift) DriftKind {
	return ""
}

// Plan turns drifts into ordered idempotent repair steps.
func (r *AclBaselineReconciler) Plan(drifts []Drift) ([]FixStep, error) {
	return nil, errNotImplemented
}

// ApplyStep applies one repair step idempotently.
func (r *AclBaselineReconciler) ApplyStep(step FixStep) error {
	return errNotImplemented
}

// Exempt keeps a path out of the drift report until the ticket expires.
func (r *AclBaselineReconciler) Exempt(path string, until time.Time, ticket string) error {
	return errNotImplemented
}

// Exemptions lists the active exemption records.
func (r *AclBaselineReconciler) Exemptions() []Exemption {
	return nil
}

// AppliedSteps counts the repair steps that took effect.
func (r *AclBaselineReconciler) AppliedSteps() int {
	return 0
}

// Digest fingerprints the most recent drift report.
func (r *AclBaselineReconciler) Digest() (string, error) {
	return "", errNotImplemented
}

// Generation returns the snapshot generation of the most recent scan.
func (r *AclBaselineReconciler) Generation() uint64 {
	return 0
}

// Stats summarises the most recent scan pass.
func (r *AclBaselineReconciler) Stats() ScanStats {
	return ScanStats{}
}
