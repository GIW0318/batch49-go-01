// Package task schedules scan tasks inside declared maintenance windows.
package task

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
	_ "time/tzdata"
)

// Task states tracked by the planner.
const (
	StateQueued  = "queued"
	StateRunning = "running"
	StateBackoff = "backoff"
	StateDone    = "done"
	StateParked  = "parked"
)

// Outcome kinds reported by the executor.
const (
	OutcomeDone      = "done"
	OutcomeThrottled = "throttled"
	OutcomeFailed    = "failed"
)

const snapshotVersion = 1

// Errors returned by the planner.
var (
	ErrBadWindow      = errors.New("maintenance window is invalid")
	ErrDuplicateGroup = errors.New("asset group is already declared")
	ErrUnknownGroup   = errors.New("asset group is not declared")
	ErrBadTask        = errors.New("scan task is invalid")
	ErrDuplicateTask  = errors.New("scan task id is already submitted")
	ErrUnknownTask    = errors.New("scan task is unknown or already settled")
	ErrBadOutcome     = errors.New("scan outcome kind is invalid")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// Window is the maintenance window declared for one asset group. Zone is an
// IANA time zone name, Days lists the allowed weekdays and Start/End are the
// offsets from local midnight.
type Window struct {
	Zone  string         `json:"zone"`
	Days  []time.Weekday `json:"days"`
	Start time.Duration  `json:"start"`
	End   time.Duration  `json:"end"`
}

// Task is one unit of scan work.
type Task struct {
	ID       string `json:"id"`
	Group    string `json:"group"`
	Target   string `json:"target"`
	Progress int    `json:"progress"`
}

// Outcome is one executor report. At is the moment the executor observed the
// outcome, RetryAfter is the delay requested by the target.
type Outcome struct {
	Kind       string        `json:"kind"`
	RetryAfter time.Duration `json:"retry_after"`
	Progress   int           `json:"progress"`
	At         time.Time     `json:"at"`
}

// TaskState is the inspectable state of one submitted task.
type TaskState struct {
	ID       string `json:"id"`
	Group    string `json:"group"`
	Target   string `json:"target"`
	State    string `json:"state"`
	Attempts int    `json:"attempts"`
	Progress int    `json:"progress"`
}

// Limits bounds dispatch.
type Limits struct {
	PerTargetConcurrency int
	MaxThrottles         int
	MaxBackoff           time.Duration
}

// TargetAttribution explains which declared group/window controls a target.
type TargetAttribution struct {
	Target          string   `json:"target"`
	Open            bool     `json:"open"`
	SelectedGroup   string   `json:"selected_group"`
	StrictestGroup  string   `json:"strictest_group"`
	CandidateGroups []string `json:"candidate_groups"`
	Reason          string   `json:"reason"`
}

type groupState struct {
	window Window
	loc    *time.Location
	queue  []string
}

type taskRecord struct {
	seq          uint64
	id           string
	group        string
	target       string
	progress     int
	state        string
	attempts     int
	throttles    int
	backoffUntil time.Time
	lastReportAt time.Time
}

// ScanWindowPlanner owns the window declarations, the queue and the per target
// concurrency gates.
type ScanWindowPlanner struct {
	mu       sync.Mutex
	limits   Limits
	groups   map[string]*groupState
	tasks    map[string]*taskRecord
	inflight map[string]int
	nextSeq  uint64
}

// NewScanWindowPlanner builds a planner with the given caps.
func NewScanWindowPlanner(limits Limits) *ScanWindowPlanner {
	if limits.PerTargetConcurrency == 0 {
		limits.PerTargetConcurrency = 1
	}
	return &ScanWindowPlanner{
		limits:   limits,
		groups:   make(map[string]*groupState),
		tasks:    make(map[string]*taskRecord),
		inflight: make(map[string]int),
	}
}

// Declare registers the maintenance window of one asset group.
func (p *ScanWindowPlanner) Declare(group string, w Window) error {
	loc, err := validateWindow(w)
	if err != nil {
		return err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.initLocked()

	if _, exists := p.groups[group]; exists {
		return ErrDuplicateGroup
	}

	w.Days = append([]time.Weekday(nil), w.Days...)
	p.groups[group] = &groupState{
		window: w,
		loc:    loc,
		queue:  make([]string, 0),
	}
	return nil
}

// Submit enqueues one scan task and returns its id.
func (p *ScanWindowPlanner) Submit(t Task) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.initLocked()

	if t.ID == "" {
		return "", ErrBadTask
	}
	if _, ok := p.groups[t.Group]; !ok {
		return "", ErrUnknownGroup
	}
	if _, exists := p.tasks[t.ID]; exists {
		return "", ErrDuplicateTask
	}

	p.nextSeq++
	p.tasks[t.ID] = &taskRecord{
		seq:      p.nextSeq,
		id:       t.ID,
		group:    t.Group,
		target:   t.Target,
		progress: t.Progress,
		state:    StateQueued,
	}
	p.groups[t.Group].queue = append(p.groups[t.Group].queue, t.ID)
	return t.ID, nil
}

// Next hands out the next dispatchable task whose window is open at now.
func (p *ScanWindowPlanner) Next(now time.Time) (Task, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.initLocked()

	ready := make([]*taskRecord, 0, len(p.tasks))
	for _, record := range p.tasks {
		if record.state == StateQueued || record.state == StateBackoff {
			ready = append(ready, record)
		}
	}
	sort.Slice(ready, func(i, j int) bool {
		return ready[i].seq < ready[j].seq
	})

	for _, record := range ready {
		group, ok := p.groups[record.group]
		if !ok || !windowOpenAt(group.window, group.loc, now) {
			continue
		}
		if !p.targetAttributionOpenLocked(record.target, now) {
			continue
		}
		if record.state == StateBackoff && now.Before(record.backoffUntil) {
			continue
		}
		if p.limits.PerTargetConcurrency > 0 &&
			p.inflight[record.target] >= p.limits.PerTargetConcurrency {
			continue
		}

		record.state = StateRunning
		record.attempts++
		p.inflight[record.target]++
		return record.asTask(), true
	}

	return Task{}, false
}

// Report records the executor outcome of one dispatched task.
func (p *ScanWindowPlanner) Report(id string, r Outcome) error {
	switch r.Kind {
	case OutcomeDone, OutcomeThrottled, OutcomeFailed:
	default:
		return ErrBadOutcome
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.initLocked()

	record, ok := p.tasks[id]
	if !ok || isSettled(record.state) {
		return ErrUnknownTask
	}

	wasRunning := record.state == StateRunning
	record.progress = r.Progress
	record.lastReportAt = r.At

	switch r.Kind {
	case OutcomeDone:
		record.state = StateDone
		record.backoffUntil = time.Time{}
		p.removeFromGroupLocked(record.group, id)
		if wasRunning {
			p.releaseTargetLocked(record.target)
		}

	case OutcomeThrottled:
		delay := r.RetryAfter
		if delay < 0 {
			delay = 0
		}
		if p.limits.MaxBackoff > 0 && delay > p.limits.MaxBackoff {
			delay = p.limits.MaxBackoff
		}
		record.throttles++
		record.backoffUntil = r.At.Add(delay)

		if record.throttles > p.limits.MaxThrottles {
			record.state = StateParked
			record.backoffUntil = time.Time{}
			p.removeFromGroupLocked(record.group, id)
			if wasRunning {
				p.releaseTargetLocked(record.target)
			}
			return nil
		}

		record.state = StateBackoff
		if wasRunning {
			p.releaseTargetLocked(record.target)
		}

	case OutcomeFailed:
		if r.RetryAfter > 0 {
			delay := r.RetryAfter
			if p.limits.MaxBackoff > 0 && delay > p.limits.MaxBackoff {
				delay = p.limits.MaxBackoff
			}
			record.state = StateBackoff
			record.backoffUntil = r.At.Add(delay)
		} else {
			record.state = StateQueued
			record.backoffUntil = time.Time{}
		}
		if wasRunning {
			p.releaseTargetLocked(record.target)
		}
	}

	return nil
}

// InWindow reports whether the group window is open at the given instant.
func (p *ScanWindowPlanner) InWindow(group string, at time.Time) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.initLocked()

	groupState, ok := p.groups[group]
	if !ok {
		return false, ErrUnknownGroup
	}
	return windowOpenAt(groupState.window, groupState.loc, at), nil
}

// Pending counts the unsettled tasks of one group.
func (p *ScanWindowPlanner) Pending(group string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.initLocked()

	groupState, ok := p.groups[group]
	if !ok {
		return 0
	}
	return len(groupState.queue)
}

// Groups lists the declared asset groups in lexicographic order.
func (p *ScanWindowPlanner) Groups() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.initLocked()

	return p.groupNamesLocked()
}

// State returns the current state of one task.
func (p *ScanWindowPlanner) State(id string) (TaskState, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.initLocked()

	record, ok := p.tasks[id]
	if !ok {
		return TaskState{}, ErrUnknownTask
	}
	return TaskState{
		ID:       record.id,
		Group:    record.group,
		Target:   record.target,
		State:    record.state,
		Attempts: record.attempts,
		Progress: record.progress,
	}, nil
}

// Queue lists the unsettled task ids of one group in queue order.
func (p *ScanWindowPlanner) Queue(group string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.initLocked()

	groupState, ok := p.groups[group]
	if !ok {
		return nil
	}
	return append([]string{}, groupState.queue...)
}

// ExplainAttribution identifies the strictest declared group/window controlling
// a target and explains the attribution decision at at. Membership is inferred
// from submitted tasks because Task is the planner's asset/group binding.
func (p *ScanWindowPlanner) ExplainAttribution(target string, at time.Time) (TargetAttribution, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.initLocked()

	groupSet := make(map[string]struct{})
	for _, record := range p.tasks {
		if record.target == target {
			groupSet[record.group] = struct{}{}
		}
	}
	if len(groupSet) == 0 {
		return TargetAttribution{}, fmt.Errorf("%w: target %q", ErrUnknownTask, target)
	}

	names := make([]string, 0, len(groupSet))
	for name := range groupSet {
		names = append(names, name)
	}
	sort.Strings(names)

	attribution := TargetAttribution{
		Target:          target,
		CandidateGroups: append([]string{}, names...),
		Open:            true,
	}

	openByGroup := make(map[string]bool, len(names))
	for _, name := range names {
		groupState := p.groups[name]
		open := windowOpenAt(groupState.window, groupState.loc, at)
		openByGroup[name] = open
		if !open {
			attribution.Open = false
		}
	}

	attribution.StrictestGroup = strictestGroupLocked(p.groups, names)
	if attribution.Open {
		attribution.SelectedGroup = attribution.StrictestGroup
		attribution.Reason = fmt.Sprintf(
			"all attributed groups %v are open at %s; selected the narrowest window of group %q",
			names, at.Format(time.RFC3339), attribution.SelectedGroup,
		)
		return attribution, nil
	}

	for _, name := range names {
		if !openByGroup[name] {
			attribution.SelectedGroup = name
			break
		}
	}
	attribution.Reason = fmt.Sprintf(
		"windows do not all overlap at %s; group %q is closed and its stricter attribution controls dispatch",
		at.Format(time.RFC3339), attribution.SelectedGroup,
	)
	return attribution, nil
}

// Snapshot serialises declarations, queue, gates and counters.
func (p *ScanWindowPlanner) Snapshot() ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.initLocked()

	payload := snapshotData{
		Version:  snapshotVersion,
		NextSeq:  p.nextSeq,
		Groups:   make([]snapshotGroup, 0, len(p.groups)),
		Tasks:    make([]snapshotTask, 0, len(p.tasks)),
		Inflight: copyIntMap(p.inflight),
	}

	for _, name := range p.groupNamesLocked() {
		groupState := p.groups[name]
		payload.Groups = append(payload.Groups, snapshotGroup{
			Name:   name,
			Window: groupState.window,
			Queue:  append([]string{}, groupState.queue...),
		})
	}

	ids := make([]string, 0, len(p.tasks))
	for id := range p.tasks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		record := p.tasks[id]
		payload.Tasks = append(payload.Tasks, snapshotTask{
			Seq:          record.seq,
			ID:           record.id,
			Group:        record.group,
			Target:       record.target,
			Progress:     record.progress,
			State:        record.state,
			Attempts:     record.attempts,
			Throttles:    record.throttles,
			BackoffUntil: record.backoffUntil,
			LastReportAt: record.lastReportAt,
		})
	}

	data, err := json.Marshal(&payload)
	if err != nil {
		return nil, fmt.Errorf("encode planner snapshot: %w", err)
	}
	return data, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (p *ScanWindowPlanner) Restore(data []byte) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return ErrBadSnapshot
	}

	var payload snapshotData
	if err := json.Unmarshal(data, &payload); err != nil {
		return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	if payload.Version != snapshotVersion {
		return fmt.Errorf("%w: unsupported version %d", ErrBadSnapshot, payload.Version)
	}

	groups := make(map[string]*groupState, len(payload.Groups))
	for _, snapshotGroup := range payload.Groups {
		loc, err := validateWindow(snapshotGroup.Window)
		if err != nil {
			return fmt.Errorf("%w: group %q: %v", ErrBadSnapshot, snapshotGroup.Name, err)
		}
		if _, duplicate := groups[snapshotGroup.Name]; duplicate {
			return fmt.Errorf("%w: duplicate group %q", ErrBadSnapshot, snapshotGroup.Name)
		}

		queue := make([]string, 0, len(snapshotGroup.Queue))
		seen := make(map[string]struct{}, len(snapshotGroup.Queue))
		for _, id := range snapshotGroup.Queue {
			if id == "" {
				return fmt.Errorf("%w: empty queue task id in group %q", ErrBadSnapshot, snapshotGroup.Name)
			}
			if _, duplicate := seen[id]; duplicate {
				return fmt.Errorf("%w: duplicate queue entry %q", ErrBadSnapshot, id)
			}
			seen[id] = struct{}{}
			queue = append(queue, id)
		}

		window := snapshotGroup.Window
		window.Days = append([]time.Weekday(nil), window.Days...)
		groups[snapshotGroup.Name] = &groupState{
			window: window,
			loc:    loc,
			queue:  queue,
		}
	}

	tasks := make(map[string]*taskRecord, len(payload.Tasks))
	seenSeq := make(map[uint64]struct{}, len(payload.Tasks))
	var maxSeq uint64
	for _, snapshotTask := range payload.Tasks {
		if snapshotTask.ID == "" {
			return fmt.Errorf("%w: task with empty id", ErrBadSnapshot)
		}
		if _, duplicate := tasks[snapshotTask.ID]; duplicate {
			return fmt.Errorf("%w: duplicate task %q", ErrBadSnapshot, snapshotTask.ID)
		}
		if _, ok := groups[snapshotTask.Group]; !ok {
			return fmt.Errorf("%w: task %q has unknown group %q", ErrBadSnapshot, snapshotTask.ID, snapshotTask.Group)
		}
		if !validState(snapshotTask.State) {
			return fmt.Errorf("%w: task %q has invalid state %q", ErrBadSnapshot, snapshotTask.ID, snapshotTask.State)
		}
		if snapshotTask.Seq == 0 {
			return fmt.Errorf("%w: task %q has zero sequence", ErrBadSnapshot, snapshotTask.ID)
		}
		if _, duplicate := seenSeq[snapshotTask.Seq]; duplicate {
			return fmt.Errorf("%w: duplicate sequence %d", ErrBadSnapshot, snapshotTask.Seq)
		}
		if snapshotTask.Attempts < 0 || snapshotTask.Throttles < 0 {
			return fmt.Errorf("%w: task %q has negative counters", ErrBadSnapshot, snapshotTask.ID)
		}

		seenSeq[snapshotTask.Seq] = struct{}{}
		if snapshotTask.Seq > maxSeq {
			maxSeq = snapshotTask.Seq
		}
		tasks[snapshotTask.ID] = &taskRecord{
			seq:          snapshotTask.Seq,
			id:           snapshotTask.ID,
			group:        snapshotTask.Group,
			target:       snapshotTask.Target,
			progress:     snapshotTask.Progress,
			state:        snapshotTask.State,
			attempts:     snapshotTask.Attempts,
			throttles:    snapshotTask.Throttles,
			backoffUntil: snapshotTask.BackoffUntil,
			lastReportAt: snapshotTask.LastReportAt,
		}
	}

	for name, groupState := range groups {
		var lastSeq uint64
		for _, id := range groupState.queue {
			record, ok := tasks[id]
			if !ok {
				return fmt.Errorf("%w: queue %q references missing task %q", ErrBadSnapshot, name, id)
			}
			if record.group != name {
				return fmt.Errorf("%w: queue entry %q is stored in group %q", ErrBadSnapshot, id, record.group)
			}
			if isSettled(record.state) {
				return fmt.Errorf("%w: settled task %q remains in queue", ErrBadSnapshot, id)
			}
			if record.seq <= lastSeq {
				return fmt.Errorf("%w: queue %q is not in submission order", ErrBadSnapshot, name)
			}
			lastSeq = record.seq
		}
	}

	for id, record := range tasks {
		if isSettled(record.state) {
			continue
		}
		groupState := groups[record.group]
		found := false
		for _, queuedID := range groupState.queue {
			if queuedID == id {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%w: unsettled task %q is missing from queue", ErrBadSnapshot, id)
		}
	}

	inflight := make(map[string]int)
	for _, record := range tasks {
		if record.state == StateRunning {
			inflight[record.target]++
		}
	}
	if payload.Inflight != nil {
		if len(payload.Inflight) != len(inflight) {
			return fmt.Errorf("%w: inflight gates are inconsistent", ErrBadSnapshot)
		}
		for target, count := range payload.Inflight {
			if count != inflight[target] {
				return fmt.Errorf("%w: inflight gate for %q is inconsistent", ErrBadSnapshot, target)
			}
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.groups = groups
	p.tasks = tasks
	p.inflight = inflight
	p.nextSeq = max(payload.NextSeq, maxSeq)
	return nil
}

type snapshotData struct {
	Version  int             `json:"version"`
	NextSeq  uint64          `json:"next_seq"`
	Groups   []snapshotGroup `json:"groups"`
	Tasks    []snapshotTask  `json:"tasks"`
	Inflight map[string]int  `json:"inflight,omitempty"`
}

type snapshotGroup struct {
	Name   string   `json:"name"`
	Window Window   `json:"window"`
	Queue  []string `json:"queue"`
}

type snapshotTask struct {
	Seq          uint64    `json:"seq"`
	ID           string    `json:"id"`
	Group        string    `json:"group"`
	Target       string    `json:"target"`
	Progress     int       `json:"progress"`
	State        string    `json:"state"`
	Attempts     int       `json:"attempts"`
	Throttles    int       `json:"throttles"`
	BackoffUntil time.Time `json:"backoff_until"`
	LastReportAt time.Time `json:"last_report_at"`
}

func (p *ScanWindowPlanner) initLocked() {
	if p.groups == nil {
		p.groups = make(map[string]*groupState)
	}
	if p.tasks == nil {
		p.tasks = make(map[string]*taskRecord)
	}
	if p.inflight == nil {
		p.inflight = make(map[string]int)
	}
}

func (p *ScanWindowPlanner) groupNamesLocked() []string {
	names := make([]string, 0, len(p.groups))
	for name := range p.groups {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (p *ScanWindowPlanner) removeFromGroupLocked(group, id string) {
	groupState, ok := p.groups[group]
	if !ok {
		return
	}
	kept := make([]string, 0, len(groupState.queue))
	for _, queuedID := range groupState.queue {
		if queuedID != id {
			kept = append(kept, queuedID)
		}
	}
	groupState.queue = kept
}

func (p *ScanWindowPlanner) releaseTargetLocked(target string) {
	if p.inflight == nil {
		return
	}
	p.inflight[target]--
	if p.inflight[target] <= 0 {
		delete(p.inflight, target)
	}
}

func (r *taskRecord) asTask() Task {
	return Task{
		ID:       r.id,
		Group:    r.group,
		Target:   r.target,
		Progress: r.progress,
	}
}

func validateWindow(w Window) (*time.Location, error) {
	if w.Zone == "" || w.Zone == "Local" {
		return nil, ErrBadWindow
	}
	loc, err := time.LoadLocation(w.Zone)
	if err != nil {
		return nil, fmt.Errorf("%w: %q", ErrBadWindow, w.Zone)
	}
	if len(w.Days) == 0 || w.Start >= w.End {
		return nil, ErrBadWindow
	}
	return loc, nil
}

func windowOpenAt(w Window, loc *time.Location, at time.Time) bool {
	if loc == nil {
		loc = time.UTC
	}
	local := at.In(loc)

	dayAllowed := false
	for _, day := range w.Days {
		if day == local.Weekday() {
			dayAllowed = true
			break
		}
	}
	if !dayAllowed {
		return false
	}

	offset := time.Duration(local.Hour())*time.Hour +
		time.Duration(local.Minute())*time.Minute +
		time.Duration(local.Second())*time.Second +
		time.Duration(local.Nanosecond())
	return offset >= w.Start && offset < w.End
}

func strictestGroupLocked(groups map[string]*groupState, names []string) string {
	selected := ""
	var shortest time.Duration
	for index, name := range names {
		weekly := weeklyWindowDuration(groups[name].window)
		if index == 0 || weekly < shortest {
			selected = name
			shortest = weekly
		}
	}
	return selected
}

func (p *ScanWindowPlanner) targetAttributionOpenLocked(target string, at time.Time) bool {
	attributedGroups := make(map[string]struct{})
	for _, record := range p.tasks {
		if record.target == target {
			attributedGroups[record.group] = struct{}{}
		}
	}
	for name := range attributedGroups {
		groupState, ok := p.groups[name]
		if !ok || !windowOpenAt(groupState.window, groupState.loc, at) {
			return false
		}
	}
	return true
}

func weeklyWindowDuration(w Window) time.Duration {
	seen := make(map[time.Weekday]struct{}, len(w.Days))
	for _, day := range w.Days {
		seen[day] = struct{}{}
	}
	days := len(seen)
	if days == 0 {
		return 0
	}
	return time.Duration(days) * (w.End - w.Start)
}

func validState(state string) bool {
	switch state {
	case StateQueued, StateRunning, StateBackoff, StateDone, StateParked:
		return true
	default:
		return false
	}
}

func isSettled(state string) bool {
	return state == StateDone || state == StateParked
}

func copyIntMap(source map[string]int) map[string]int {
	if len(source) == 0 {
		return nil
	}
	result := make(map[string]int, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
