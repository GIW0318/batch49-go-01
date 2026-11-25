// Package task runs evidence driven detection rules to a fixed point and
// keeps the provenance of every finding for later review.
package task

import (
	"errors"
	"sync"
)

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Finding severities.
const (
	SeverityLow      = "low"
	SeverityMedium   = "medium"
	SeverityHigh     = "high"
	SeverityCritical = "critical"
)

// Errors returned by the engine.
var (
	ErrBlankRuleID    = errors.New("rule id is required")
	ErrDuplicateRule  = errors.New("rule already registered")
	ErrBadRule        = errors.New("rule declares no produced evidence")
	ErrRuleCycle      = errors.New("rule dependency cycle")
	ErrBadBudget      = errors.New("budget must be positive")
	ErrRunBusy        = errors.New("engine is already running")
	ErrForeignSubject = errors.New("evidence subject is outside the target")
	ErrUnknownFinding = errors.New("finding not found")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// EvidenceRef names one piece of evidence by kind and owning subject.
type EvidenceRef struct {
	Kind    string `json:"kind"`
	Subject string `json:"subject"`
}

// Rule declares the evidence it needs and the evidence it produces.
type Rule struct {
	ID       string        `json:"id"`
	Severity string        `json:"severity"`
	Requires []EvidenceRef `json:"requires"`
	Produces []EvidenceRef `json:"produces"`
}

// Target is one scan scope: the subjects that may appear in this scan plus
// the evidence already collected before the first round.
type Target struct {
	ID       string        `json:"id"`
	Subjects []string      `json:"subjects"`
	Seeds    []EvidenceRef `json:"seeds"`
}

// Budget bounds one run.
type Budget struct {
	MaxRounds   int `json:"max_rounds"`
	MaxFindings int `json:"max_findings"`
}

// EvidenceItem is one stored piece of evidence together with the round that
// first produced it.
type EvidenceItem struct {
	Kind    string `json:"kind"`
	Subject string `json:"subject"`
	Round   int    `json:"round"`
}

// Finding is one rule hit raised during a run.
type Finding struct {
	ID       string `json:"id"`
	RuleID   string `json:"rule_id"`
	Severity string `json:"severity"`
	Subject  string `json:"subject"`
	Round    int    `json:"round"`
}

// ChainStep is one required evidence item with the rule and round that
// produced it.
type ChainStep struct {
	Kind    string `json:"kind"`
	Subject string `json:"subject"`
	RuleID  string `json:"rule_id"`
	Round   int    `json:"round"`
}

// Chain explains why one finding was raised.
type Chain struct {
	FindingID string      `json:"finding_id"`
	RuleID    string      `json:"rule_id"`
	Steps     []ChainStep `json:"steps"`
}

// Result reports what one run of the engine did.
type Result struct {
	TargetID   string    `json:"target_id"`
	Rounds     int       `json:"rounds"`
	Findings   []Finding `json:"findings"`
	Incomplete []string  `json:"incomplete"`
	Converged  bool      `json:"converged"`
	Truncated  bool      `json:"truncated"`
}

// EvidenceRuleEngine owns the rule registry and the per target ledgers.
type EvidenceRuleEngine struct {
	mu sync.Mutex
}

// NewEvidenceRuleEngine builds an empty engine.
func NewEvidenceRuleEngine() *EvidenceRuleEngine {
	return &EvidenceRuleEngine{}
}

// Register adds one rule after validating its id, produced evidence and
// dependency edges.
func (e *EvidenceRuleEngine) Register(rule Rule) error {
	return errNotImplemented
}

// Rules lists the registered rule ids in registration order.
func (e *EvidenceRuleEngine) Rules() []string {
	return nil
}

// Run evaluates rules round by round for one target.
func (e *EvidenceRuleEngine) Run(target Target, budget Budget) (Result, error) {
	return Result{}, errNotImplemented
}

// LastResult returns the most recent run summary of one target.
func (e *EvidenceRuleEngine) LastResult(targetID string) (Result, bool) {
	return Result{}, false
}

// Findings lists the findings recorded for one target, oldest first.
func (e *EvidenceRuleEngine) Findings(targetID string) []Finding {
	return nil
}

// Evidence lists the evidence ledger of one target, first insertion first.
func (e *EvidenceRuleEngine) Evidence(targetID string) []EvidenceItem {
	return nil
}

// Incomplete lists the registered rules that never fired for one target.
func (e *EvidenceRuleEngine) Incomplete(targetID string) []string {
	return nil
}

// Explain rebuilds the dependency chain of one finding.
func (e *EvidenceRuleEngine) Explain(findingID string) (Chain, error) {
	return Chain{}, errNotImplemented
}

// Reset drops the ledger and findings of one target.
func (e *EvidenceRuleEngine) Reset(targetID string) error {
	return errNotImplemented
}

// Snapshot serialises rules plus every target ledger and finding.
func (e *EvidenceRuleEngine) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole engine state with a snapshot, all or nothing.
func (e *EvidenceRuleEngine) Restore(data []byte) error {
	return errNotImplemented
}
