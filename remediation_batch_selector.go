// Package task aggregates vulnerability findings with the fix actions that
// remove them, then packs actions into maintenance batches under a hard budget.
package task

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

// Finding is one open vulnerability mapped to a component.
type Finding struct {
	ID        string    `json:"id"`
	Component string    `json:"component"`
	Severity  string    `json:"severity"`
	DueAt     time.Time `json:"due_at"`
}

// Action is one executable fix step: one target version of one component.
type Action struct {
	ID            string   `json:"id"`
	Component     string   `json:"component"`
	TargetVersion string   `json:"target_version"`
	Prereq        string   `json:"prereq"`
	Cost          int      `json:"cost"`
	Fixes         []string `json:"fixes"`
}

// Budget is the hard cap of one maintenance window sequence.
type Budget struct {
	MaxCost    int `json:"max_cost"`
	MaxBatches int `json:"max_batches"`
}

// Batch is one maintenance window.
type Batch struct {
	Actions []string `json:"actions"`
	Cost    int      `json:"cost"`
	Covers  []string `json:"covers"`
}

// FixGroup is the per component aggregation of findings and available
// actions.
type FixGroup struct {
	Component      string   `json:"component"`
	Findings       []string `json:"findings"`
	Actions        []string `json:"actions"`
	TargetVersions []string `json:"target_versions"`
}

// BlockedFinding is one finding that Select could not cover, together with the
// constraint that blocked it.
type BlockedFinding struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// Reasons reported in BlockedFinding.
const (
	ReasonBudget     = "budget"
	ReasonBatchLimit = "batch_limit"
)

// Errors returned by the selector.
var (
	ErrBadFinding       = errors.New("finding id and component are required")
	ErrDuplicateFinding = errors.New("finding id is already registered")
	ErrBadAction        = errors.New("action id, component, target version and cost are required")
	ErrActionMismatch   = errors.New("action does not belong to this finding")
	ErrUnknownAction    = errors.New("unknown action")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

const snapshotVersion = 1

// RemediationBatchSelector owns the findings, the fix actions and the applied
// state.
type RemediationBatchSelector struct {
	mu        sync.Mutex
	findings  map[string]Finding
	actions   map[string]Action
	fixed     map[string]bool
	order     []string
	uncovered []BlockedFinding
}

// NewRemediationBatchSelector builds an empty selector.
func NewRemediationBatchSelector() *RemediationBatchSelector {
	return &RemediationBatchSelector{
		findings: make(map[string]Finding),
		actions:  make(map[string]Action),
		fixed:    make(map[string]bool),
	}
}

// Add registers one finding together with the actions that can remove it.
func (s *RemediationBatchSelector) Add(f Finding, actions []Action) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if strings.TrimSpace(f.ID) == "" || strings.TrimSpace(f.Component) == "" {
		return ErrBadFinding
	}
	if _, exists := s.findings[f.ID]; exists {
		return ErrDuplicateFinding
	}

	pending := make(map[string]Action, len(actions))
	for _, a := range actions {
		if err := validateAction(a); err != nil {
			return err
		}
		if a.Component != f.Component || !containsString(a.Fixes, f.ID) {
			return ErrActionMismatch
		}
		if existing, exists := s.actions[a.ID]; exists {
			if !sameAction(existing, a) {
				return actionDefinitionConflict(a.ID)
			}
			continue
		}
		if existing, exists := pending[a.ID]; exists {
			if !sameAction(existing, a) {
				return actionDefinitionConflict(a.ID)
			}
			continue
		}
		pending[a.ID] = a
	}

	// Commit only after every supplied action has been validated.
	s.findings[f.ID] = f
	s.order = append(s.order, f.ID)
	for id, a := range pending {
		s.actions[id] = a
	}
	return nil
}

// Group aggregates the registered findings and actions per component.
func (s *RemediationBatchSelector) Group() []FixGroup {
	s.mu.Lock()
	defer s.mu.Unlock()

	components := make(map[string]bool)
	findings := make(map[string][]string)
	actions := make(map[string][]string)
	versions := make(map[string]map[string]bool)

	for id, f := range s.findings {
		components[f.Component] = true
		findings[f.Component] = append(findings[f.Component], id)
	}
	for id, a := range s.actions {
		components[a.Component] = true
		actions[a.Component] = append(actions[a.Component], id)
		if versions[a.Component] == nil {
			versions[a.Component] = make(map[string]bool)
		}
		versions[a.Component][a.TargetVersion] = true
	}

	names := sortedKeys(components)
	groups := make([]FixGroup, 0, len(names))
	for _, component := range names {
		findingIDs := findings[component]
		sort.Strings(findingIDs)
		actionIDs := actions[component]
		sort.Strings(actionIDs)
		targetVersions := sortedKeys(versions[component])
		if findingIDs == nil {
			findingIDs = []string{}
		}
		if actionIDs == nil {
			actionIDs = []string{}
		}
		if targetVersions == nil {
			targetVersions = []string{}
		}
		groups = append(groups, FixGroup{
			Component:      component,
			Findings:       findingIDs,
			Actions:        actionIDs,
			TargetVersions: targetVersions,
		})
	}
	return groups
}

// Select packs actions into batches inside the budget. Findings that do not
// fit are reported through Uncovered instead of producing an error.
func (s *RemediationBatchSelector) Select(budget Budget) ([]Batch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	maxCost := budget.MaxCost
	if maxCost < 0 {
		maxCost = 0
	}
	maxBatches := budget.MaxBatches
	if maxBatches < 0 {
		maxBatches = 0
	}

	open := make(map[string]Finding)
	for id, f := range s.findings {
		if !s.fixed[id] {
			open[id] = f
		}
	}

	candidates := s.actionCandidates(open)
	sort.Slice(candidates, func(i, j int) bool {
		if len(candidates[i].fixes) != len(candidates[j].fixes) {
			return len(candidates[i].fixes) > len(candidates[j].fixes)
		}
		if !candidates[i].earliest.Equal(candidates[j].earliest) {
			return candidates[i].earliest.Before(candidates[j].earliest)
		}
		return candidates[i].action.ID < candidates[j].action.ID
	})

	// First pack compatible actions into as few windows as possible. A window
	// may not exceed the total cost cap on its own; actions that would push it
	// over are placed in a new window so the budget still has choices.
	windows := make([]*workingBatch, 0)
	for _, c := range candidates {
		placed := false
		for _, w := range windows {
			if w.hasConflict(c.action) {
				continue
			}
			if w.cost+c.action.Cost > maxCost {
				continue
			}
			w.add(c.action, c.fixes)
			placed = true
			break
		}
		if !placed {
			w := &workingBatch{fixes: make(map[string]bool)}
			w.add(c.action, c.fixes)
			windows = append(windows, w)
		}
	}

	batchCandidates := make([]batchCandidate, 0, len(windows))
	for _, w := range windows {
		actionIDs := make([]string, 0, len(w.actions))
		for _, a := range w.actions {
			actionIDs = append(actionIDs, a.ID)
		}
		sort.Strings(actionIDs)
		covers := sortedKeys(w.fixes)
		if len(covers) == 0 {
			continue
		}
		earliest := open[covers[0]].DueAt
		for _, id := range covers[1:] {
			if open[id].DueAt.Before(earliest) {
				earliest = open[id].DueAt
			}
		}
		batchCandidates = append(batchCandidates, batchCandidate{
			batch: Batch{
				Actions: actionIDs,
				Cost:    w.cost,
				Covers:  covers,
			},
			earliest:  earliest,
			signature: strings.Join(actionIDs, "\x00"),
		})
	}

	// Earlier windows are the ones with the widest coverage and the earliest
	// deadline. The signature keeps ties independent of registration order.
	sort.Slice(batchCandidates, func(i, j int) bool {
		ci := len(batchCandidates[i].batch.Covers)
		cj := len(batchCandidates[j].batch.Covers)
		if ci != cj {
			return ci > cj
		}
		if !batchCandidates[i].earliest.Equal(batchCandidates[j].earliest) {
			return batchCandidates[i].earliest.Before(batchCandidates[j].earliest)
		}
		return batchCandidates[i].signature < batchCandidates[j].signature
	})

	selected := make([]Batch, 0, len(batchCandidates))
	covered := make(map[string]bool)
	reasons := make(map[string]string)
	totalCost := 0

	for _, candidate := range batchCandidates {
		hasNewCoverage := false
		for _, id := range candidate.batch.Covers {
			if !covered[id] {
				hasNewCoverage = true
				break
			}
		}
		if !hasNewCoverage {
			// Another window already removes every finding here.
			continue
		}
		if len(selected) >= maxBatches {
			noteReasons(reasons, candidate.batch.Covers, ReasonBatchLimit)
			continue
		}
		if totalCost+candidate.batch.Cost > maxCost {
			noteReasons(reasons, candidate.batch.Covers, ReasonBudget)
			continue
		}
		selected = append(selected, candidate.batch)
		totalCost += candidate.batch.Cost
		for _, id := range candidate.batch.Covers {
			covered[id] = true
		}
	}

	openIDs := make([]string, 0, len(open))
	for id := range open {
		openIDs = append(openIDs, id)
	}
	sort.Strings(openIDs)

	uncovered := make([]BlockedFinding, 0)
	for _, id := range openIDs {
		if covered[id] {
			continue
		}
		reason := reasons[id]
		if reason == "" {
			reason = ReasonBudget
		}
		uncovered = append(uncovered, BlockedFinding{ID: id, Reason: reason})
	}
	s.uncovered = uncovered

	return selected, nil
}

// Coverage counts the still open findings removed by the given batches.
func (s *RemediationBatchSelector) Coverage(bs []Batch) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, b := range bs {
		for _, id := range b.Actions {
			if _, exists := s.actions[id]; !exists {
				return 0, ErrUnknownAction
			}
		}
	}

	covered := make(map[string]bool)
	for _, b := range bs {
		for _, id := range b.Actions {
			a := s.actions[id]
			for _, findingID := range a.Fixes {
				f, exists := s.findings[findingID]
				if !exists || s.fixed[findingID] || f.Component != a.Component {
					continue
				}
				covered[findingID] = true
			}
		}
	}
	return len(covered), nil
}

// Conflict reports whether two actions may not share one maintenance window.
func (s *RemediationBatchSelector) Conflict(a, b Action) bool {
	return actionsConflict(a, b)
}

// Apply marks every finding removed by the batch as fixed. Repeating the same
// batch is a no-op.
func (s *RemediationBatchSelector) Apply(batch Batch) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, id := range batch.Actions {
		if _, exists := s.actions[id]; !exists {
			return ErrUnknownAction
		}
	}
	for _, id := range batch.Actions {
		for _, findingID := range s.actions[id].Fixes {
			s.fixed[findingID] = true
		}
	}
	return nil
}

// Uncovered lists the open findings left out by the last Select call together
// with the blocking constraint.
func (s *RemediationBatchSelector) Uncovered() []BlockedFinding {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.uncovered == nil {
		return nil
	}
	out := make([]BlockedFinding, len(s.uncovered))
	copy(out, s.uncovered)
	return out
}

// Findings lists the registered findings ordered by ID.
func (s *RemediationBatchSelector) Findings() []Finding {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]Finding, 0, len(s.findings))
	for _, f := range s.findings {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Actions lists the actions registered for one component ordered by ID.
func (s *RemediationBatchSelector) Actions(component string) []Action {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]Action, 0)
	for _, a := range s.actions {
		if a.Component == component {
			out = append(out, cloneAction(a))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Fixed reports whether a finding was already removed by an applied batch.
func (s *RemediationBatchSelector) Fixed(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fixed[id]
}

type snapshotState struct {
	Version  int       `json:"version"`
	Findings []Finding `json:"findings"`
	Actions  []Action  `json:"actions"`
	Fixed    []string  `json:"fixed"`
}

// Snapshot serialises the findings, actions and applied state.
func (s *RemediationBatchSelector) Snapshot() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state := snapshotState{
		Version:  snapshotVersion,
		Findings: make([]Finding, 0, len(s.findings)),
		Actions:  make([]Action, 0, len(s.actions)),
		Fixed:    make([]string, 0, len(s.fixed)),
	}
	for _, f := range s.findings {
		state.Findings = append(state.Findings, f)
	}
	for _, a := range s.actions {
		state.Actions = append(state.Actions, cloneAction(a))
	}
	for id, isFixed := range s.fixed {
		if isFixed {
			state.Fixed = append(state.Fixed, id)
		}
	}
	sort.Slice(state.Findings, func(i, j int) bool { return state.Findings[i].ID < state.Findings[j].ID })
	sort.Slice(state.Actions, func(i, j int) bool { return state.Actions[i].ID < state.Actions[j].ID })
	sort.Strings(state.Fixed)

	return json.Marshal(state)
}

// Restore replaces the selector state from a snapshot. A corrupt payload
// leaves the selector untouched.
func (s *RemediationBatchSelector) Restore(data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state snapshotState
	if err := decoder.Decode(&state); err != nil {
		return ErrBadSnapshot
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return ErrBadSnapshot
	}
	if state.Version != snapshotVersion {
		return ErrBadSnapshot
	}

	findings := make(map[string]Finding, len(state.Findings))
	for _, f := range state.Findings {
		if strings.TrimSpace(f.ID) == "" || strings.TrimSpace(f.Component) == "" {
			return ErrBadSnapshot
		}
		if _, exists := findings[f.ID]; exists {
			return ErrBadSnapshot
		}
		findings[f.ID] = f
	}

	actions := make(map[string]Action, len(state.Actions))
	for _, a := range state.Actions {
		if validateAction(a) != nil || len(a.Fixes) == 0 {
			return ErrBadSnapshot
		}
		if _, exists := actions[a.ID]; exists {
			return ErrBadSnapshot
		}
		actions[a.ID] = cloneAction(a)
	}

	fixed := make(map[string]bool, len(state.Fixed))
	for _, id := range state.Fixed {
		if strings.TrimSpace(id) == "" {
			return ErrBadSnapshot
		}
		if _, exists := fixed[id]; exists {
			return ErrBadSnapshot
		}
		fixed[id] = true
	}

	order := make([]string, 0, len(findings))
	for id := range findings {
		order = append(order, id)
	}
	sort.Strings(order)

	// Everything validated: replace the live state atomically.
	s.findings = findings
	s.actions = actions
	s.fixed = fixed
	s.order = order
	s.uncovered = nil
	return nil
}

type actionCandidate struct {
	action   Action
	fixes    []string
	earliest time.Time
}

// actionCandidates returns one candidate per registered action that removes at
// least one currently open finding of its own component.
func (s *RemediationBatchSelector) actionCandidates(open map[string]Finding) []actionCandidate {
	candidates := make([]actionCandidate, 0, len(s.actions))
	for _, a := range s.actions {
		fixSet := make(map[string]bool)
		for _, findingID := range a.Fixes {
			f, exists := open[findingID]
			if !exists || f.Component != a.Component {
				continue
			}
			fixSet[findingID] = true
		}
		if len(fixSet) == 0 {
			continue
		}
		fixes := sortedKeys(fixSet)
		earliest := open[fixes[0]].DueAt
		for _, id := range fixes[1:] {
			if open[id].DueAt.Before(earliest) {
				earliest = open[id].DueAt
			}
		}
		candidates = append(candidates, actionCandidate{
			action:   cloneAction(a),
			fixes:    fixes,
			earliest: earliest,
		})
	}
	return candidates
}

type workingBatch struct {
	actions []Action
	fixes   map[string]bool
	cost    int
}

func (w *workingBatch) hasConflict(a Action) bool {
	for _, existing := range w.actions {
		if actionsConflict(existing, a) {
			return true
		}
	}
	return false
}

func (w *workingBatch) add(a Action, fixes []string) {
	w.actions = append(w.actions, a)
	w.cost += a.Cost
	for _, id := range fixes {
		w.fixes[id] = true
	}
}

type batchCandidate struct {
	batch     Batch
	earliest  time.Time
	signature string
}

func actionsConflict(a, b Action) bool {
	if a.Component == b.Component && a.TargetVersion != b.TargetVersion {
		return true
	}
	pa := strings.TrimSpace(a.Prereq)
	pb := strings.TrimSpace(b.Prereq)
	return pa != "" && pa == pb
}

func validateAction(a Action) error {
	if strings.TrimSpace(a.ID) == "" ||
		strings.TrimSpace(a.Component) == "" ||
		strings.TrimSpace(a.TargetVersion) == "" ||
		a.Cost < 0 {
		return ErrBadAction
	}
	return nil
}

func actionDefinitionConflict(id string) error {
	return &definitionConflictError{id: id}
}

type definitionConflictError struct {
	id string
}

func (e *definitionConflictError) Error() string {
	return "action " + e.id + " is already registered with a different definition"
}

// Is lets callers treat a redefinition as an action mismatch without allowing
// the existing definition to be overwritten.
func (e *definitionConflictError) Is(target error) bool {
	return target == ErrActionMismatch
}

func sameAction(a, b Action) bool {
	if a.ID != b.ID ||
		a.Component != b.Component ||
		a.TargetVersion != b.TargetVersion ||
		a.Prereq != b.Prereq ||
		a.Cost != b.Cost {
		return false
	}
	return sameStringSet(a.Fixes, b.Fixes)
}

func sameStringSet(a, b []string) bool {
	setA := make(map[string]bool, len(a))
	for _, value := range a {
		setA[value] = true
	}
	setB := make(map[string]bool, len(b))
	for _, value := range b {
		setB[value] = true
	}
	if len(setA) != len(setB) {
		return false
	}
	for value := range setA {
		if !setB[value] {
			return false
		}
	}
	return true
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func cloneAction(a Action) Action {
	if a.Fixes != nil {
		a.Fixes = append([]string(nil), a.Fixes...)
	}
	return a
}

func sortedKeys[V any](values map[string]V) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func noteReasons(reasons map[string]string, ids []string, reason string) {
	for _, id := range ids {
		current, exists := reasons[id]
		if !exists {
			reasons[id] = reason
			continue
		}
		// Budget is the stronger blocker: more windows would not help.
		if current != ReasonBudget && reason == ReasonBudget {
			reasons[id] = reason
		}
	}
}
