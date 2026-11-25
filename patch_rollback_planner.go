// Package task plans and executes dependency aware rollbacks of patch batches.
package task

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Rollback result states.
const (
	StatusPlanned      = "planned"
	StatusExecuted     = "executed"
	StatusPartial      = "partial"
	StatusWindowClosed = "window_closed"
)

// Blocker reasons reported by feasibility checks.
const (
	BlockerIrreversible      = "irreversible"
	BlockerMissingDependency = "missing_dependency"
	BlockerCycle             = "cycle"
)

// Errors surfaced by the planner.
var (
	ErrBlankField        = errors.New("batch id, step id and reason are required")
	ErrDuplicateBatch    = errors.New("batch already registered")
	ErrDuplicateStep     = errors.New("step id is repeated inside the batch")
	ErrUnknownBatch      = errors.New("batch is not registered")
	ErrUnknownStep       = errors.New("step is not registered in any batch")
	ErrDependencyMissing = errors.New("step depends on an unknown step")
	ErrDependencyCycle   = errors.New("step dependencies form a cycle")
	ErrBadUpto           = errors.New("rollback count is out of range")
	ErrIrreversible      = errors.New("rollback would cross an irreversible step")
	ErrWindowClosed      = errors.New("rollback window has closed")
	ErrAlreadyExecuted   = errors.New("batch rollback was already started")
	ErrStepFailed        = errors.New("rollback step failed")
)

// Step is one unit of patch application.
type Step struct {
	ID            string   `json:"id"`
	Host          string   `json:"host"`
	DependsOn     []string `json:"depends_on"`
	ApplyOrder    int      `json:"apply_order"`
	Irreversible  bool     `json:"irreversible"`
	ReverseScript string   `json:"reverse_script"`
	FailRollback  bool     `json:"fail_rollback"`
}

// Batch is the applied patch bundle to roll back.
type Batch struct {
	ID        string        `json:"id"`
	Steps     []Step        `json:"steps"`
	AppliedAt time.Time     `json:"applied_at"`
	Window    time.Duration `json:"window"`
}

// Blocker explains why a plan cannot be fully executed.
type Blocker struct {
	Step   string `json:"step"`
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

// RollbackPlan is the reverse ordered rollback schedule.
type RollbackPlan struct {
	Batch      string     `json:"batch"`
	Order      []string   `json:"order"`
	Layers     [][]string `json:"layers"`
	Reversible int        `json:"reversible"`
	Blockers   []Blocker  `json:"blockers"`
	Deadline   time.Time  `json:"deadline"`
}

// Result reports what a rollback execution actually did.
type Result struct {
	Batch     string    `json:"batch"`
	Completed []string  `json:"completed"`
	Failed    string    `json:"failed"`
	Remaining []string  `json:"remaining"`
	Status    string    `json:"status"`
	Deadline  time.Time `json:"deadline,omitempty"`
	Reason    string    `json:"reason,omitempty"`
}

// Options injects the clock used for window checks.
type Options struct {
	Now func() time.Time
}

type runRecord struct {
	result Result
	done   bool
}

// PatchRollbackPlanner owns registered batches and irreversibility flags.
type PatchRollbackPlanner struct {
	mu                  sync.RWMutex
	now                 func() time.Time
	batches             map[string]Batch
	planned             map[string]Batch
	irreversibleReasons map[string]string
	reversibleOverrides map[string]struct{}
	runs                map[string]runRecord
}

// NewPatchRollbackPlanner builds a planner; a nil clock means time.Now.
func NewPatchRollbackPlanner(opts Options) *PatchRollbackPlanner {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &PatchRollbackPlanner{
		now:                 now,
		batches:             make(map[string]Batch),
		planned:             make(map[string]Batch),
		irreversibleReasons: make(map[string]string),
		reversibleOverrides: make(map[string]struct{}),
		runs:                make(map[string]runRecord),
	}
}

// RegisterBatch stores a batch and its rollback window.
func (p *PatchRollbackPlanner) RegisterBatch(b Batch) error {
	if err := validateBatch(b); err != nil {
		return err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if _, exists := p.batches[b.ID]; exists {
		return fmt.Errorf("batch %q: %w", b.ID, ErrDuplicateBatch)
	}
	p.batches[b.ID] = cloneBatch(b)
	return nil
}

// Plan builds the dependency aware reverse order for a batch.
func (p *PatchRollbackPlanner) Plan(batch Batch) (RollbackPlan, error) {
	plan := RollbackPlan{
		Batch:    batch.ID,
		Deadline: batch.AppliedAt.Add(batch.Window),
	}
	if err := validateBatch(batch); err != nil {
		return plan, err
	}

	p.mu.RLock()
	marks := copyReasons(p.irreversibleReasons)
	overrides := copyStringSet(p.reversibleOverrides)
	p.mu.RUnlock()

	byID := make(map[string]Step, len(batch.Steps))
	for _, step := range batch.Steps {
		byID[step.ID] = step
	}

	var graphBlockers []Blocker
	for _, step := range batch.Steps {
		for _, dependencyID := range step.DependsOn {
			if _, ok := byID[dependencyID]; !ok {
				graphBlockers = append(graphBlockers, Blocker{
					Step:   dependencyID,
					Reason: BlockerMissingDependency,
				})
			}
		}
	}
	if len(graphBlockers) > 0 {
		plan.Blockers = dedupeBlockers(graphBlockers)
		p.rememberPlanned(batch)
		return plan, fmt.Errorf("missing dependency in batch %q: %w", batch.ID, ErrDependencyMissing)
	}

	layers, order, err := orderSteps(batch.Steps, byID)
	if err != nil {
		plan.Layers = layers
		plan.Blockers = []Blocker{{Reason: BlockerCycle}}
		p.rememberPlanned(batch)
		return plan, fmt.Errorf("cycle in batch %q: %w", batch.ID, err)
	}

	plan.Layers = layers
	plan.Order = order

	for _, id := range order {
		step := byID[id]
		if isIrreversible(step, marks, overrides) {
			plan.Blockers = append(plan.Blockers, Blocker{
				Step:   id,
				Reason: BlockerIrreversible,
				Detail: irreversibleDetail(id, step, marks),
			})
		}
	}

	plan.Reversible = len(order)
	for i, id := range order {
		if isIrreversible(byID[id], marks, overrides) {
			plan.Reversible = i
			break
		}
	}

	p.rememberPlanned(batch)
	return plan, nil
}

// Feasible tells whether the whole plan can be executed.
func (p *PatchRollbackPlanner) Feasible(plan RollbackPlan) (bool, []Blocker) {
	if len(plan.Blockers) == 0 {
		return true, nil
	}
	return false, append([]Blocker(nil), plan.Blockers...)
}

// Execute rolls back the first upto steps of a plan.
func (p *PatchRollbackPlanner) Execute(plan RollbackPlan, upto int) (Result, error) {
	result := Result{Batch: plan.Batch}
	if upto < 0 || upto > len(plan.Order) {
		return result, fmt.Errorf("upto=%d plan_length=%d: %w", upto, len(plan.Order), ErrBadUpto)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if run, exists := p.runs[plan.Batch]; exists && run.done {
		return cloneResult(run.result), fmt.Errorf("batch %q: %w", plan.Batch, ErrAlreadyExecuted)
	}

	deadline := plan.Deadline
	if registered, ok := p.batches[plan.Batch]; ok {
		deadline = registered.AppliedAt.Add(registered.Window)
	}
	if p.now().After(deadline) {
		result.Status = StatusWindowClosed
		result.Deadline = deadline
		result.Reason = ErrWindowClosed.Error()
		result.Remaining = append([]string(nil), plan.Order...)
		return result, fmt.Errorf("window closed at %s: %w", deadline.Format(time.RFC3339), ErrWindowClosed)
	}

	stepByID := p.executionStepsLocked(plan.Batch)
	blockedInPlan := make(map[string]struct{})
	for _, blocker := range plan.Blockers {
		if blocker.Reason == BlockerIrreversible && blocker.Step != "" {
			blockedInPlan[blocker.Step] = struct{}{}
		}
	}
	for i, id := range plan.Order {
		step := stepByID[id]
		if _, known := stepByID[id]; !known {
			step = Step{ID: id}
		}
		_, planBlocked := blockedInPlan[id]
		currentlyBlocked := isIrreversible(step, p.irreversibleReasons, p.reversibleOverrides)
		if (planBlocked || currentlyBlocked) && upto > i {
			return Result{Batch: plan.Batch}, fmt.Errorf("step %q at rollback position %d: %w", id, i, ErrIrreversible)
		}
	}

	for i := 0; i < upto; i++ {
		id := plan.Order[i]
		step := stepByID[id]
		if step.FailRollback {
			result.Failed = id
			result.Remaining = append([]string(nil), plan.Order[i+1:]...)
			result.Status = StatusPartial
			p.runs[plan.Batch] = runRecord{result: cloneResult(result), done: true}
			return cloneResult(result), fmt.Errorf("step %q: %w", id, ErrStepFailed)
		}
		result.Completed = append(result.Completed, id)
	}

	result.Remaining = append([]string(nil), plan.Order[upto:]...)
	result.Status = StatusExecuted
	p.runs[plan.Batch] = runRecord{result: cloneResult(result), done: true}
	return cloneResult(result), nil
}

// Window returns the rollback deadline of a registered batch.
func (p *PatchRollbackPlanner) Window(batch string) (time.Time, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	b, ok := p.batches[batch]
	if !ok {
		return time.Time{}, fmt.Errorf("unknown batch %q: %w", batch, ErrUnknownBatch)
	}
	return b.AppliedAt.Add(b.Window), nil
}

// MarkIrreversible flags a step as impossible to undo.
func (p *PatchRollbackPlanner) MarkIrreversible(step string, reason string) error {
	if strings.TrimSpace(step) == "" || strings.TrimSpace(reason) == "" {
		return ErrBlankField
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.stepExistsLocked(step) {
		return fmt.Errorf("unknown step %q: %w", step, ErrUnknownStep)
	}
	if _, marked := p.irreversibleReasons[step]; !marked {
		p.irreversibleReasons[step] = reason
	}
	delete(p.reversibleOverrides, step)
	p.setStepIrreversibleLocked(step, true)
	return nil
}

// MarkReversible explicitly allows a normally destructive step to be undone.
func (p *PatchRollbackPlanner) MarkReversible(step string) error {
	if strings.TrimSpace(step) == "" {
		return ErrBlankField
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.stepExistsLocked(step) {
		return fmt.Errorf("unknown step %q: %w", step, ErrUnknownStep)
	}
	p.reversibleOverrides[step] = struct{}{}
	delete(p.irreversibleReasons, step)
	p.setStepIrreversibleLocked(step, false)
	return nil
}

// Steps returns a copy of the registered steps of a batch.
func (p *PatchRollbackPlanner) Steps(batch string) ([]Step, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	b, ok := p.batches[batch]
	if !ok {
		return nil, fmt.Errorf("unknown batch %q: %w", batch, ErrUnknownBatch)
	}
	cloned := cloneBatch(b)
	return cloned.Steps, nil
}

// Status returns the last execution result of a batch.
func (p *PatchRollbackPlanner) Status(batch string) (Result, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	run, ok := p.runs[batch]
	if !ok || !run.done {
		return Result{}, false
	}
	return cloneResult(run.result), true
}

// Reversible tells whether a step may be rolled back at all.
func (p *PatchRollbackPlanner) Reversible(step string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if _, explicit := p.reversibleOverrides[step]; explicit {
		return true
	}
	if _, marked := p.irreversibleReasons[step]; marked {
		return false
	}

	known := false
	for _, batch := range p.batches {
		for _, candidate := range batch.Steps {
			if candidate.ID != step {
				continue
			}
			known = true
			if candidate.Irreversible || isDataMigrationStep(candidate) {
				return false
			}
		}
	}
	return known
}

func validateBatch(b Batch) error {
	if strings.TrimSpace(b.ID) == "" || len(b.Steps) == 0 {
		return ErrBlankField
	}

	seen := make(map[string]struct{}, len(b.Steps))
	for _, step := range b.Steps {
		if strings.TrimSpace(step.ID) == "" {
			return ErrBlankField
		}
		if _, exists := seen[step.ID]; exists {
			return fmt.Errorf("step %q: %w", step.ID, ErrDuplicateStep)
		}
		seen[step.ID] = struct{}{}
	}
	return nil
}

func cloneBatch(b Batch) Batch {
	cloned := b
	cloned.Steps = make([]Step, len(b.Steps))
	for i, step := range b.Steps {
		step.DependsOn = append([]string(nil), step.DependsOn...)
		cloned.Steps[i] = step
	}
	return cloned
}

func cloneResult(result Result) Result {
	result.Completed = append([]string(nil), result.Completed...)
	result.Remaining = append([]string(nil), result.Remaining...)
	return result
}

func copyReasons(src map[string]string) map[string]string {
	dst := make(map[string]string, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func copyStringSet(src map[string]struct{}) map[string]struct{} {
	dst := make(map[string]struct{}, len(src))
	for key := range src {
		dst[key] = struct{}{}
	}
	return dst
}

func dedupeBlockers(blockers []Blocker) []Blocker {
	seen := make(map[Blocker]struct{}, len(blockers))
	result := make([]Blocker, 0, len(blockers))
	for _, blocker := range blockers {
		if _, ok := seen[blocker]; ok {
			continue
		}
		result = append(result, blocker)
	}
	return result
}

func orderSteps(steps []Step, byID map[string]Step) ([][]string, []string, error) {
	dependents := make(map[string][]string, len(steps))
	remainingDependencies := make(map[string]int, len(steps))
	for _, step := range steps {
		dependencies := uniqueDependencies(step.DependsOn)
		remainingDependencies[step.ID] = len(dependencies)
		for _, dependencyID := range dependencies {
			dependents[dependencyID] = append(dependents[dependencyID], step.ID)
		}
	}

	var current []string
	for _, step := range steps {
		if remainingDependencies[step.ID] == 0 {
			current = append(current, step.ID)
		}
	}

	var layers [][]string
	processed := 0
	for len(current) > 0 {
		sortLayerAscending(current, byID)
		layers = append(layers, append([]string(nil), current...))
		processed += len(current)

		var next []string
		for _, id := range current {
			for _, dependentID := range dependents[id] {
				remainingDependencies[dependentID]--
				if remainingDependencies[dependentID] == 0 {
					next = append(next, dependentID)
				}
			}
		}
		current = next
	}

	if processed != len(steps) {
		return layers, nil, ErrDependencyCycle
	}

	var order []string
	for i := len(layers) - 1; i >= 0; i-- {
		layer := append([]string(nil), layers[i]...)
		sortRollbackLayer(layer, byID)
		order = append(order, layer...)
	}
	return layers, order, nil
}

func uniqueDependencies(dependencies []string) []string {
	seen := make(map[string]struct{}, len(dependencies))
	result := make([]string, 0, len(dependencies))
	for _, dependencyID := range dependencies {
		if _, ok := seen[dependencyID]; ok {
			continue
		}
		seen[dependencyID] = struct{}{}
		result = append(result, dependencyID)
	}
	return result
}

func sortLayerAscending(ids []string, byID map[string]Step) {
	sort.Slice(ids, func(i, j int) bool {
		left := byID[ids[i]]
		right := byID[ids[j]]
		if left.ApplyOrder != right.ApplyOrder {
			return left.ApplyOrder < right.ApplyOrder
		}
		return left.ID < right.ID
	})
}

func sortRollbackLayer(ids []string, byID map[string]Step) {
	sort.Slice(ids, func(i, j int) bool {
		left := byID[ids[i]]
		right := byID[ids[j]]
		if left.ApplyOrder != right.ApplyOrder {
			return left.ApplyOrder > right.ApplyOrder
		}
		return left.ID < right.ID
	})
}

func isIrreversible(step Step, marks map[string]string, overrides map[string]struct{}) bool {
	if _, explicitReversible := overrides[step.ID]; explicitReversible {
		return false
	}
	if _, marked := marks[step.ID]; marked {
		return true
	}
	return step.Irreversible || isDataMigrationStep(step)
}

func isDataMigrationStep(step Step) bool {
	id := strings.ToLower(strings.TrimSpace(step.ID))
	return strings.Contains(id, "migrat") || strings.Contains(step.ID, "迁移")
}

func irreversibleDetail(id string, step Step, marks map[string]string) string {
	if reason := strings.TrimSpace(marks[id]); reason != "" {
		return reason
	}
	if isDataMigrationStep(step) {
		return "data migration step"
	}
	return "step is marked irreversible"
}

func (p *PatchRollbackPlanner) rememberPlanned(batch Batch) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.planned[batch.ID] = cloneBatch(batch)
}

func (p *PatchRollbackPlanner) stepExistsLocked(stepID string) bool {
	for _, batch := range p.batches {
		for _, step := range batch.Steps {
			if step.ID == stepID {
				return true
			}
		}
	}
	return false
}

func (p *PatchRollbackPlanner) setStepIrreversibleLocked(stepID string, irreversible bool) {
	for batchID, batch := range p.batches {
		changed := false
		for i := range batch.Steps {
			if batch.Steps[i].ID == stepID {
				batch.Steps[i].Irreversible = irreversible
				changed = true
			}
		}
		if changed {
			p.batches[batchID] = batch
		}
	}
}

func (p *PatchRollbackPlanner) executionStepsLocked(batchID string) map[string]Step {
	batch, ok := p.batches[batchID]
	if !ok {
		batch = p.planned[batchID]
	}
	result := make(map[string]Step, len(batch.Steps))
	for _, step := range batch.Steps {
		result[step.ID] = step
	}
	return result
}
