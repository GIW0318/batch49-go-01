// Package task governs per server daily currency budgets, per player shares
// and the carry over of unused budget across UTC day boundaries.
package task

import "errors"

// errNotImplemented marks behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// DaySeconds is the width of one UTC day.
const DaySeconds int64 = 86400

// Options tunes how much of an over share grant is still paid out.
type Options struct {
	OverflowGrantPct int
}

// Errors returned by the governor.
var (
	ErrInvalidServer   = errors.New("server id is required")
	ErrInvalidBudget   = errors.New("daily budget must be positive")
	ErrInvalidCarryPct = errors.New("carry percentage must be between 0 and 100")
	ErrServerExists    = errors.New("server is already configured")
	ErrUnknownServer   = errors.New("unknown server")
	ErrNoRoster        = errors.New("active roster is not set for the day")
	ErrEmptyRoster     = errors.New("active roster must not be empty")
	ErrDuplicatePlayer = errors.New("duplicate player in roster")
	ErrRosterLocked    = errors.New("roster is locked after the first grant of the day")
	ErrUnknownPlayer   = errors.New("player is not in the active roster")
	ErrInvalidAmount   = errors.New("grant amount must be positive")
	ErrDayRegression   = errors.New("timestamp points to a past day")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// GrantResult reports one currency grant.
type GrantResult struct {
	Server    string `json:"server"`
	Player    string `json:"player"`
	Day       int64  `json:"day"`
	Requested int64  `json:"requested"`
	Granted   int64  `json:"granted"`
	Decayed   int64  `json:"decayed"`
	Share     int64  `json:"share"`
}

// Usage is the read model of one server day.
type Usage struct {
	Server  string `json:"server"`
	Day     int64  `json:"day"`
	Budget  int64  `json:"budget"`
	Spent   int64  `json:"spent"`
	Players int    `json:"players"`
	Remain  int64  `json:"remain"`
}

// RolloverResult reports one day boundary crossing.
type RolloverResult struct {
	Server  string `json:"server"`
	FromDay int64  `json:"from_day"`
	ToDay   int64  `json:"to_day"`
	Carried int64  `json:"carried"`
	Budget  int64  `json:"budget"`
	Rolled  bool   `json:"rolled"`
}

// EconomyGovernor owns server configurations, rosters and daily usage.
type EconomyGovernor struct {
	opts Options
}

// NewEconomyGovernor builds an empty governor.
func NewEconomyGovernor(opts Options) *EconomyGovernor {
	return &EconomyGovernor{opts: opts}
}

// Configure registers a server budget and carry percentage.
func (g *EconomyGovernor) Configure(server string, dailyBudget int64, carryPct int) error {
	return errNotImplemented
}

// SetRoster fixes the active players of the current day.
func (g *EconomyGovernor) SetRoster(server string, players []string, at int64) error {
	return errNotImplemented
}

// Grant pays currency to one player of a server.
func (g *EconomyGovernor) Grant(server, player string, amount int64, at int64) (GrantResult, error) {
	return GrantResult{}, errNotImplemented
}

// Usage reads the counters of the day that owns at.
func (g *EconomyGovernor) Usage(server string, at int64) Usage {
	return Usage{}
}

// Rollover crosses one day boundary and carries the unused budget.
func (g *EconomyGovernor) Rollover(server string, at int64) (RolloverResult, error) {
	return RolloverResult{}, errNotImplemented
}

// PlayerSpent returns the amount already paid to one player for the day.
func (g *EconomyGovernor) PlayerSpent(server, player string, at int64) int64 {
	return 0
}

// Players lists the active roster of the day in id order.
func (g *EconomyGovernor) Players(server string, at int64) []string {
	return nil
}

// Remain returns the unspent budget of the day.
func (g *EconomyGovernor) Remain(server string, at int64) int64 {
	return 0
}

// Configured reports the stored configuration of a server.
func (g *EconomyGovernor) Configured(server string) (dailyBudget int64, carryPct int, ok bool) {
	return 0, 0, false
}

// Snapshot serialises configurations, usage and rosters.
func (g *EconomyGovernor) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole governor state with a snapshot, all or nothing.
func (g *EconomyGovernor) Restore(data []byte) error {
	return errNotImplemented
}
