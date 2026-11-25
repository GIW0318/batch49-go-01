// Package task keeps a per account achievement point ledger with per category
// caps, one shot series bonuses and retroactive recalculation.
package task

import "errors"

// errNotImplemented marks behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// MaxDefinitionPoints bounds the award of a single achievement definition.
const MaxDefinitionPoints int64 = 1000000

// Errors returned by the ledger.
var (
	ErrInvalidAchievement   = errors.New("achievement definition is invalid")
	ErrDuplicateAchievement = errors.New("achievement already defined")
	ErrUnknownAchievement   = errors.New("unknown achievement")
	ErrBlankAccount         = errors.New("account id is required")
	ErrBadSnapshot          = errors.New("snapshot payload is invalid")
)

// Achievement is one definition in the catalogue. Deleted definitions stay in
// the ledger so that their historical award can be reconciled once.
type Achievement struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Points   int64  `json:"points"`
	Series   string `json:"series"`
	Deleted  bool   `json:"deleted"`
}

// Options configures caps and the one shot series bonus.
type Options struct {
	CategoryCaps map[string]int64
	SeriesBonus  int64
}

// PointDelta is the outcome of one unlock or recalc call.
type PointDelta struct {
	Account  string   `json:"account"`
	Added    int64    `json:"added"`
	Deducted int64    `json:"deducted"`
	Overflow int64    `json:"overflow"`
	Bonuses  []string `json:"bonuses"`
	Total    int64    `json:"total"`
}

// PointsBreakdown is the read model for one account.
type PointsBreakdown struct {
	Total      int64            `json:"total"`
	Categories map[string]int64 `json:"categories"`
	Overflow   int64            `json:"overflow"`
	Bonuses    []string         `json:"bonuses"`
}

// PointsLedger owns definitions, unlock sets and awarded points.
type PointsLedger struct {
	opts Options
}

// NewPointsLedger builds an empty ledger.
func NewPointsLedger(opts Options) *PointsLedger {
	return &PointsLedger{opts: opts}
}

// DefineAchievement validates and stores one definition.
func (l *PointsLedger) DefineAchievement(a Achievement) error {
	return errNotImplemented
}

// UndefineAchievement marks a definition as deleted.
func (l *PointsLedger) UndefineAchievement(id string) error {
	return errNotImplemented
}

// Unlock records one achievement for an account. Ids without a definition yet
// stay pending until a definition arrives and a recalc pays them out.
func (l *PointsLedger) Unlock(account, achievementID string, at int64) (PointDelta, error) {
	return PointDelta{}, errNotImplemented
}

// Points returns the current breakdown for an account.
func (l *PointsLedger) Points(account string) PointsBreakdown {
	return PointsBreakdown{}
}

// Recalc reconciles newly defined and deleted achievements for one account.
func (l *PointsLedger) Recalc(account string) (PointDelta, error) {
	return PointDelta{}, errNotImplemented
}

// GrantedBonus lists the series bonuses already paid to an account.
func (l *PointsLedger) GrantedBonus(account string) []string {
	return nil
}

// CategoryPoints returns the awarded points of one category.
func (l *PointsLedger) CategoryPoints(account, category string) int64 {
	return 0
}

// Unlocked lists the unlocked achievement ids of an account in id order.
func (l *PointsLedger) Unlocked(account string) []string {
	return nil
}

// Snapshot serialises definitions, unlock sets, points and bonus records.
func (l *PointsLedger) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole ledger with a snapshot, all or nothing.
func (l *PointsLedger) Restore(data []byte) error {
	return errNotImplemented
}
