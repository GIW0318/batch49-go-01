// Package task plans goal oriented action sequences for game agents and
// replays the chosen sequence one step at a time.
package task

import "errors"

// errNotImplemented marks behaviour that still has to be written.
var errNotImplemented = errors.New("not implemented")

// DefaultMaxDepth caps how many actions one plan may contain when the
// options do not ask for a different depth.
const DefaultMaxDepth = 6

// Errors reported by the planner.
var (
	ErrBlankAgent      = errors.New("agent id is required")
	ErrBadAction       = errors.New("action definition is invalid")
	ErrDuplicateAction = errors.New("action id already registered")
	ErrUnknownAction   = errors.New("action id is not registered")
	ErrNoGoal          = errors.New("agent has no goal")
	ErrNoPlan          = errors.New("no action sequence reaches the goal")
	ErrPlanDone        = errors.New("planned sequence is already exhausted")
	ErrBadTime         = errors.New("logical time must not go backwards")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// State is a set of world facts that are true. A fact that is absent or set
// to false is considered false.
type State map[string]bool

// Action is one authored step an agent may take. Pre lists the facts that
// must hold before the action can run, Effect lists the facts it turns true.
type Action struct {
	ID     string
	Cost   int
	Pre    State
	Effect State
}

// Options tunes the planner.
type Options struct {
	// MaxDepth is the longest action sequence the search may return.
	// Values below one fall back to DefaultMaxDepth.
	MaxDepth int
}

// Plan is the ordered sequence chosen for one agent and one goal.
type Plan struct {
	Agent   string
	Goal    string
	Actions []string
	Cost    int
	Steps   int
}

// ActionResult describes the single step that Execute just performed.
type ActionResult struct {
	Agent     string
	Action    string
	Index     int
	Remaining int
	Done      bool
}

// GoapPlanner owns action definitions, per agent goals, cached plans and
// per agent execution cursors.
type GoapPlanner struct {
	opts Options
}

// NewGoapPlanner builds an empty planner.
func NewGoapPlanner(opts Options) *GoapPlanner {
	return &GoapPlanner{opts: opts}
}

// AddAction registers one action definition.
func (p *GoapPlanner) AddAction(a Action) error {
	return errNotImplemented
}

// RemoveAction drops one action definition.
func (p *GoapPlanner) RemoveAction(id string) error {
	return errNotImplemented
}

// Actions returns a copy of the registered actions ordered by id.
func (p *GoapPlanner) Actions() []Action {
	return nil
}

// SetGoal stores the desired facts for one agent.
func (p *GoapPlanner) SetGoal(agent, goal string, desired State) error {
	return errNotImplemented
}

// Goal returns a copy of the stored goal of one agent.
func (p *GoapPlanner) Goal(agent string) (State, bool) {
	return nil, false
}

// Plan searches the cheapest applicable sequence for one agent and world.
func (p *GoapPlanner) Plan(agent string, world State) (Plan, error) {
	return Plan{}, errNotImplemented
}

// Execute performs the next stored step of one agent.
func (p *GoapPlanner) Execute(agent string, at int64) (ActionResult, error) {
	return ActionResult{}, errNotImplemented
}

// Cursor reports how many steps of the stored plan already ran.
func (p *GoapPlanner) Cursor(agent string) int {
	return 0
}

// Invalidate drops the cached and stored plan of one agent.
func (p *GoapPlanner) Invalidate(agent string) error {
	return errNotImplemented
}

// CacheStats reports cache hits and misses since the planner was created.
func (p *GoapPlanner) CacheStats() (hits, misses int) {
	return 0, 0
}

// Snapshot serialises actions, goals, cached plans and cursors.
func (p *GoapPlanner) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole planner state with a snapshot.
func (p *GoapPlanner) Restore(data []byte) error {
	return errNotImplemented
}
