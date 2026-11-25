// Package task runs evidence driven detection rules to a fixed point and
// keeps the provenance of every finding for later review.
package task

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
)

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

var errInconsistentState = errors.New("inconsistent engine state")

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

type evidenceRecord struct {
	Kind    string `json:"kind"`
	Subject string `json:"subject"`
	Round   int    `json:"round"`
	RuleID  string `json:"rule_id,omitempty"`
}

type findingRecord struct {
	Finding Finding `json:"finding"`
	Chain   Chain   `json:"chain"`
}

type targetState struct {
	evidence      []evidenceRecord
	evidenceIndex map[EvidenceRef]int
	findings      []findingRecord
	fired         map[string]bool
	last          Result
	hasLast       bool
}

type snapshotFinding struct {
	Finding Finding `json:"finding"`
	Chain   Chain   `json:"chain"`
}

type snapshotTarget struct {
	Evidence []evidenceRecord  `json:"evidence"`
	Findings []snapshotFinding `json:"findings"`
	Fired    []string          `json:"fired"`
	Last     Result            `json:"last"`
	HasLast  bool              `json:"has_last"`
}

type snapshotData struct {
	Version     int                       `json:"version"`
	Rules       []Rule                    `json:"rules"`
	NextFinding int64                     `json:"next_finding"`
	Targets     map[string]snapshotTarget `json:"targets"`
}

const snapshotVersion = 1

// EvidenceRuleEngine owns the rule registry and the per target ledgers.
type EvidenceRuleEngine struct {
	mu      sync.Mutex
	runMu   sync.Mutex
	rules   []Rule
	targets map[string]*targetState

	nextFinding int64
}

// NewEvidenceRuleEngine builds an empty engine.
func NewEvidenceRuleEngine() *EvidenceRuleEngine {
	return &EvidenceRuleEngine{targets: make(map[string]*targetState)}
}

// Register adds one rule after validating its id, produced evidence and
// dependency edges.
func (e *EvidenceRuleEngine) Register(rule Rule) error {
	if strings.TrimSpace(rule.ID) == "" {
		return ErrBlankRuleID
	}
	if len(rule.Produces) == 0 {
		return ErrBadRule
	}

	candidate := cloneRule(rule)

	e.mu.Lock()
	defer e.mu.Unlock()

	for _, existing := range e.rules {
		if existing.ID == candidate.ID {
			return ErrDuplicateRule
		}
	}

	all := make([]Rule, len(e.rules)+1)
	copy(all, e.rules)
	all[len(e.rules)] = candidate
	if rulesHaveCycle(all) {
		return ErrRuleCycle
	}

	e.rules = append(e.rules, candidate)
	return nil
}

// Rules lists the registered rule ids in registration order.
func (e *EvidenceRuleEngine) Rules() []string {
	e.mu.Lock()
	defer e.mu.Unlock()

	ids := make([]string, len(e.rules))
	for i := range e.rules {
		ids[i] = e.rules[i].ID
	}
	return ids
}

// Run evaluates rules round by round for one target.
func (e *EvidenceRuleEngine) Run(target Target, budget Budget) (Result, error) {
	if budget.MaxRounds <= 0 || budget.MaxFindings <= 0 {
		return Result{}, ErrBadBudget
	}
	if !e.runMu.TryLock() {
		return Result{}, ErrRunBusy
	}
	defer e.runMu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()

	subjects := make(map[string]struct{}, len(target.Subjects))
	for _, subject := range target.Subjects {
		subjects[subject] = struct{}{}
	}

	state := cloneTargetState(e.targets[target.ID])
	for _, item := range state.evidence {
		if _, ok := subjects[item.Subject]; !ok {
			return Result{}, ErrForeignSubject
		}
	}
	for _, seed := range target.Seeds {
		if _, ok := subjects[seed.Subject]; !ok {
			return Result{}, ErrForeignSubject
		}
		addEvidence(state, EvidenceRef{Kind: seed.Kind, Subject: seed.Subject}, 0, "")
	}

	rules := cloneRules(e.rules)
	nextFinding := e.nextFinding
	usedIDs := e.findingIDsLocked()

	result, err := evaluateRules(target.ID, rules, state, subjects, budget, &nextFinding, usedIDs)
	if err != nil {
		return Result{}, err
	}

	state.last = cloneResult(result)
	state.hasLast = true
	if e.targets == nil {
		e.targets = make(map[string]*targetState)
	}
	e.targets[target.ID] = state
	e.nextFinding = nextFinding

	return result, nil
}

// LastResult returns the most recent run summary of one target.
func (e *EvidenceRuleEngine) LastResult(targetID string) (Result, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	state, ok := e.targets[targetID]
	if !ok || !state.hasLast {
		return Result{}, false
	}
	return cloneResult(state.last), true
}

// Findings lists the findings recorded for one target, oldest first.
func (e *EvidenceRuleEngine) Findings(targetID string) []Finding {
	e.mu.Lock()
	defer e.mu.Unlock()

	state, ok := e.targets[targetID]
	if !ok {
		return nil
	}
	findings := make([]Finding, len(state.findings))
	for i := range state.findings {
		findings[i] = state.findings[i].Finding
	}
	return findings
}

// Evidence lists the evidence ledger of one target, first insertion first.
func (e *EvidenceRuleEngine) Evidence(targetID string) []EvidenceItem {
	e.mu.Lock()
	defer e.mu.Unlock()

	state, ok := e.targets[targetID]
	if !ok {
		return nil
	}
	items := make([]EvidenceItem, len(state.evidence))
	for i := range state.evidence {
		items[i] = EvidenceItem{
			Kind:    state.evidence[i].Kind,
			Subject: state.evidence[i].Subject,
			Round:   state.evidence[i].Round,
		}
	}
	return items
}

// Incomplete lists the registered rules that never fired for one target.
func (e *EvidenceRuleEngine) Incomplete(targetID string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()

	state, ok := e.targets[targetID]
	if !ok {
		return nil
	}
	return unfiredRules(e.rules, state.fired)
}

// Explain rebuilds the dependency chain of one finding.
func (e *EvidenceRuleEngine) Explain(findingID string) (Chain, error) {
	if findingID == "" {
		return Chain{}, ErrUnknownFinding
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	for _, state := range e.targets {
		for i := range state.findings {
			if state.findings[i].Finding.ID == findingID {
				return cloneChain(state.findings[i].Chain), nil
			}
		}
	}
	return Chain{}, ErrUnknownFinding
}

// Reset drops the ledger and findings of one target.
func (e *EvidenceRuleEngine) Reset(targetID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	delete(e.targets, targetID)
	return nil
}

// Snapshot serialises rules plus every target ledger and finding.
func (e *EvidenceRuleEngine) Snapshot() ([]byte, error) {
	e.runMu.Lock()
	defer e.runMu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()

	snapshot := snapshotData{
		Version:     snapshotVersion,
		Rules:       cloneRules(e.rules),
		NextFinding: e.nextFinding,
		Targets:     make(map[string]snapshotTarget, len(e.targets)),
	}
	for targetID, state := range e.targets {
		target := snapshotTarget{
			Evidence: append([]evidenceRecord(nil), state.evidence...),
			Findings: make([]snapshotFinding, len(state.findings)),
			Fired:    make([]string, 0, len(state.fired)),
			Last:     cloneResult(state.last),
			HasLast:  state.hasLast,
		}
		for i := range state.findings {
			target.Findings[i] = snapshotFinding{
				Finding: state.findings[i].Finding,
				Chain:   cloneChain(state.findings[i].Chain),
			}
		}
		for _, rule := range e.rules {
			if state.fired[rule.ID] {
				target.Fired = append(target.Fired, rule.ID)
			}
		}
		snapshot.Targets[targetID] = target
	}

	data, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Restore replaces the whole engine state with a snapshot, all or nothing.
func (e *EvidenceRuleEngine) Restore(data []byte) error {
	e.runMu.Lock()
	defer e.runMu.Unlock()

	snapshot, err := decodeSnapshot(data)
	if err != nil {
		return ErrBadSnapshot
	}
	targets, err := restoreTargets(snapshot)
	if err != nil {
		return ErrBadSnapshot
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	e.rules = cloneRules(snapshot.Rules)
	e.targets = targets
	e.nextFinding = snapshot.NextFinding
	return nil
}

func evaluateRules(
	targetID string,
	rules []Rule,
	state *targetState,
	subjects map[string]struct{},
	budget Budget,
	nextFinding *int64,
	usedIDs map[string]bool,
) (Result, error) {
	result := Result{TargetID: targetID}

	nextRound := 1
	for {
		if allRulesFired(rules, state.fired) {
			result.Converged = true
			break
		}
		if nextRound > budget.MaxRounds {
			if len(eligibleRules(rules, state)) == 0 {
				result.Converged = true
			}
			break
		}

		eligible := eligibleRules(rules, state)
		if len(eligible) == 0 {
			result.Converged = true
			break
		}

		round := nextRound
		result.Rounds = round
		for _, rule := range eligible {
			finding, err := fireRule(rule, round, state, subjects, nextFinding, usedIDs)
			if err != nil {
				return Result{}, err
			}
			result.Findings = append(result.Findings, finding)
			if len(result.Findings) >= budget.MaxFindings {
				result.Truncated = true
				break
			}
		}
		if result.Truncated {
			break
		}
		nextRound++
	}

	result.Incomplete = unfiredRules(rules, state.fired)
	return result, nil
}

func fireRule(
	rule Rule,
	round int,
	state *targetState,
	subjects map[string]struct{},
	nextFinding *int64,
	usedIDs map[string]bool,
) (Finding, error) {
	for _, produced := range rule.Produces {
		if _, ok := subjects[produced.Subject]; !ok {
			return Finding{}, ErrForeignSubject
		}
	}

	steps := make([]ChainStep, 0, len(rule.Requires))
	for _, required := range rule.Requires {
		index, ok := state.evidenceIndex[required]
		if !ok {
			return Finding{}, errInconsistentState
		}
		item := state.evidence[index]
		steps = append(steps, ChainStep{
			Kind:    item.Kind,
			Subject: item.Subject,
			RuleID:  item.RuleID,
			Round:   item.Round,
		})
	}

	finding := Finding{
		ID:       allocateFindingID(nextFinding, usedIDs),
		RuleID:   rule.ID,
		Severity: rule.Severity,
		Subject:  rule.Produces[0].Subject,
		Round:    round,
	}
	chain := Chain{
		FindingID: finding.ID,
		RuleID:    rule.ID,
		Steps:     steps,
	}

	for _, produced := range rule.Produces {
		addEvidence(state, produced, round, rule.ID)
	}
	state.fired[rule.ID] = true
	state.findings = append(state.findings, findingRecord{Finding: finding, Chain: chain})
	return finding, nil
}

func eligibleRules(rules []Rule, state *targetState) []Rule {
	eligible := make([]Rule, 0)
	for _, rule := range rules {
		if state.fired[rule.ID] {
			continue
		}
		if requirementsMet(rule, state.evidenceIndex) {
			eligible = append(eligible, rule)
		}
	}
	return eligible
}

func requirementsMet(rule Rule, evidence map[EvidenceRef]int) bool {
	for _, required := range rule.Requires {
		if _, ok := evidence[required]; !ok {
			return false
		}
	}
	return true
}

func allRulesFired(rules []Rule, fired map[string]bool) bool {
	for _, rule := range rules {
		if !fired[rule.ID] {
			return false
		}
	}
	return true
}

func unfiredRules(rules []Rule, fired map[string]bool) []string {
	incomplete := make([]string, 0)
	for _, rule := range rules {
		if !fired[rule.ID] {
			incomplete = append(incomplete, rule.ID)
		}
	}
	return incomplete
}

func addEvidence(state *targetState, ref EvidenceRef, round int, ruleID string) bool {
	if state.evidenceIndex == nil {
		state.evidenceIndex = make(map[EvidenceRef]int)
	}
	if _, ok := state.evidenceIndex[ref]; ok {
		return false
	}
	state.evidenceIndex[ref] = len(state.evidence)
	state.evidence = append(state.evidence, evidenceRecord{
		Kind:    ref.Kind,
		Subject: ref.Subject,
		Round:   round,
		RuleID:  ruleID,
	})
	return true
}

func allocateFindingID(next *int64, used map[string]bool) string {
	for {
		*next = *next + 1
		id := fmt.Sprintf("finding-%d", *next)
		if !used[id] {
			used[id] = true
			return id
		}
	}
}

func newTargetState() *targetState {
	return &targetState{
		evidenceIndex: make(map[EvidenceRef]int),
		fired:         make(map[string]bool),
	}
}

func cloneTargetState(source *targetState) *targetState {
	state := newTargetState()
	if source == nil {
		return state
	}

	state.evidence = append([]evidenceRecord(nil), source.evidence...)
	for i := range state.evidence {
		ref := EvidenceRef{Kind: state.evidence[i].Kind, Subject: state.evidence[i].Subject}
		state.evidenceIndex[ref] = i
	}
	state.findings = make([]findingRecord, len(source.findings))
	for i := range source.findings {
		state.findings[i] = findingRecord{
			Finding: source.findings[i].Finding,
			Chain:   cloneChain(source.findings[i].Chain),
		}
	}
	for id, fired := range source.fired {
		state.fired[id] = fired
	}
	state.last = cloneResult(source.last)
	state.hasLast = source.hasLast
	return state
}

func cloneRule(rule Rule) Rule {
	return Rule{
		ID:       rule.ID,
		Severity: rule.Severity,
		Requires: append([]EvidenceRef(nil), rule.Requires...),
		Produces: append([]EvidenceRef(nil), rule.Produces...),
	}
}

func cloneRules(rules []Rule) []Rule {
	cloned := make([]Rule, len(rules))
	for i := range rules {
		cloned[i] = cloneRule(rules[i])
	}
	return cloned
}

func cloneResult(result Result) Result {
	cloned := result
	cloned.Findings = append([]Finding(nil), result.Findings...)
	cloned.Incomplete = append([]string(nil), result.Incomplete...)
	return cloned
}

func cloneChain(chain Chain) Chain {
	cloned := chain
	cloned.Steps = append([]ChainStep(nil), chain.Steps...)
	return cloned
}

func rulesHaveCycle(rules []Rule) bool {
	producers := make(map[EvidenceRef][]int)
	for i := range rules {
		seen := make(map[EvidenceRef]bool)
		for _, produced := range rules[i].Produces {
			ref := EvidenceRef{Kind: produced.Kind, Subject: produced.Subject}
			if seen[ref] {
				continue
			}
			seen[ref] = true
			producers[ref] = append(producers[ref], i)
		}
	}

	edges := make([][]int, len(rules))
	for i := range rules {
		for _, required := range rules[i].Requires {
			ref := EvidenceRef{Kind: required.Kind, Subject: required.Subject}
			for _, producer := range producers[ref] {
				edges[producer] = append(edges[producer], i)
			}
		}
	}

	const (
		unvisited = iota
		visiting
		visited
	)
	colors := make([]int, len(rules))
	var visit func(int) bool
	visit = func(node int) bool {
		switch colors[node] {
		case visiting:
			return true
		case visited:
			return false
		}
		colors[node] = visiting
		for _, next := range edges[node] {
			if visit(next) {
				return true
			}
		}
		colors[node] = visited
		return false
	}

	for i := range rules {
		if visit(i) {
			return true
		}
	}
	return false
}

func (e *EvidenceRuleEngine) findingIDsLocked() map[string]bool {
	ids := make(map[string]bool)
	for _, state := range e.targets {
		for i := range state.findings {
			ids[state.findings[i].Finding.ID] = true
		}
	}
	return ids
}

func decodeSnapshot(data []byte) (snapshotData, error) {
	var snapshot snapshotData
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return snapshotData{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return snapshotData{}, ErrBadSnapshot
	}
	if snapshot.Version != snapshotVersion || snapshot.NextFinding < 0 {
		return snapshotData{}, ErrBadSnapshot
	}

	ids := make(map[string]bool, len(snapshot.Rules))
	for _, rule := range snapshot.Rules {
		if strings.TrimSpace(rule.ID) == "" || len(rule.Produces) == 0 || ids[rule.ID] {
			return snapshotData{}, ErrBadSnapshot
		}
		ids[rule.ID] = true
	}
	if rulesHaveCycle(snapshot.Rules) {
		return snapshotData{}, ErrBadSnapshot
	}
	return snapshot, nil
}

func restoreTargets(snapshot snapshotData) (map[string]*targetState, error) {
	ruleIndex := make(map[string]Rule, len(snapshot.Rules))
	for _, rule := range snapshot.Rules {
		ruleIndex[rule.ID] = rule
	}

	targets := make(map[string]*targetState, len(snapshot.Targets))
	usedFindingIDs := make(map[string]bool)
	for targetID, source := range snapshot.Targets {
		state := newTargetState()
		state.last = cloneResult(source.Last)
		state.hasLast = source.HasLast

		for _, item := range source.Evidence {
			if item.Round < 0 {
				return nil, ErrBadSnapshot
			}
			if item.RuleID != "" {
				if _, ok := ruleIndex[item.RuleID]; !ok {
					return nil, ErrBadSnapshot
				}
			}
			ref := EvidenceRef{Kind: item.Kind, Subject: item.Subject}
			if _, exists := state.evidenceIndex[ref]; exists {
				return nil, ErrBadSnapshot
			}
			state.evidenceIndex[ref] = len(state.evidence)
			state.evidence = append(state.evidence, item)
		}

		for _, ruleID := range source.Fired {
			if _, ok := ruleIndex[ruleID]; !ok || state.fired[ruleID] {
				return nil, ErrBadSnapshot
			}
			state.fired[ruleID] = true
		}

		for _, item := range source.Evidence {
			if item.RuleID != "" && !state.fired[item.RuleID] {
				return nil, ErrBadSnapshot
			}
		}

		findingCountByRule := make(map[string]int)
		for _, stored := range source.Findings {
			finding := stored.Finding
			chain := stored.Chain
			if finding.ID == "" || usedFindingIDs[finding.ID] {
				return nil, ErrBadSnapshot
			}
			usedFindingIDs[finding.ID] = true

			rule, ok := ruleIndex[finding.RuleID]
			if !ok || !state.fired[finding.RuleID] {
				return nil, ErrBadSnapshot
			}
			if chain.FindingID != finding.ID || chain.RuleID != finding.RuleID {
				return nil, ErrBadSnapshot
			}
			if finding.Subject != rule.Produces[0].Subject || finding.Severity != rule.Severity {
				return nil, ErrBadSnapshot
			}
			if finding.Round <= 0 || len(chain.Steps) != len(rule.Requires) {
				return nil, ErrBadSnapshot
			}
			for i, required := range rule.Requires {
				index, ok := state.evidenceIndex[EvidenceRef{Kind: required.Kind, Subject: required.Subject}]
				if !ok {
					return nil, ErrBadSnapshot
				}
				produced := state.evidence[index]
				step := chain.Steps[i]
				if step.Kind != required.Kind ||
					step.Subject != required.Subject ||
					step.RuleID != produced.RuleID ||
					step.Round != produced.Round {
					return nil, ErrBadSnapshot
				}
			}

			findingCountByRule[finding.RuleID]++
			state.findings = append(state.findings, findingRecord{
				Finding: finding,
				Chain:   cloneChain(chain),
			})
		}

		for ruleID := range state.fired {
			if findingCountByRule[ruleID] == 0 {
				return nil, ErrBadSnapshot
			}
		}
		if state.hasLast && state.last.TargetID != targetID {
			return nil, ErrBadSnapshot
		}

		targets[targetID] = state
	}
	return targets, nil
}
