// Package task keeps a per account achievement point ledger with per category
// caps, one shot series bonuses and retroactive recalculation.
package task

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
)

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

	defsMu sync.RWMutex
	defs   map[string]Achievement

	accountsMu sync.Mutex
	accounts   map[string]*accountState
}

// award records how one unlocked achievement was paid out so that recalc can
// top up or claw back exactly once.
type award struct {
	Credited int64 `json:"credited"`
	Overflow int64 `json:"overflow"`
	Deducted bool  `json:"deducted"`
}

// accountState is the mutable per account ledger guarded by its own mutex.
type accountState struct {
	mu          sync.Mutex
	unlocked    map[string]int64
	awards      map[string]*award
	categories  map[string]int64
	overflow    int64
	bonuses     map[string]bool
	bonusPoints int64
}

func newAccountState() *accountState {
	return &accountState{
		unlocked:   map[string]int64{},
		awards:     map[string]*award{},
		categories: map[string]int64{},
		bonuses:    map[string]bool{},
	}
}

func (a *accountState) total() int64 {
	var sum int64
	for _, v := range a.categories {
		sum += v
	}
	return sum + a.bonusPoints
}

func (a *accountState) sortedBonuses() []string {
	if len(a.bonuses) == 0 {
		return nil
	}
	out := make([]string, 0, len(a.bonuses))
	for name := range a.bonuses {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// NewPointsLedger builds an empty ledger.
func NewPointsLedger(opts Options) *PointsLedger {
	return &PointsLedger{
		opts:     opts,
		defs:     map[string]Achievement{},
		accounts: map[string]*accountState{},
	}
}

func (l *PointsLedger) getAccount(account string) *accountState {
	l.accountsMu.Lock()
	defer l.accountsMu.Unlock()
	acc, ok := l.accounts[account]
	if !ok {
		acc = newAccountState()
		l.accounts[account] = acc
	}
	return acc
}

func (l *PointsLedger) findAccount(account string) *accountState {
	l.accountsMu.Lock()
	defer l.accountsMu.Unlock()
	return l.accounts[account]
}

func (l *PointsLedger) defsCopy() map[string]Achievement {
	l.defsMu.RLock()
	defer l.defsMu.RUnlock()
	out := make(map[string]Achievement, len(l.defs))
	for id, def := range l.defs {
		out[id] = def
	}
	return out
}

func validateDefinition(a Achievement) bool {
	if strings.TrimSpace(a.ID) == "" || strings.TrimSpace(a.Category) == "" {
		return false
	}
	return a.Points > 0 && a.Points <= MaxDefinitionPoints
}

// DefineAchievement validates and stores one definition.
func (l *PointsLedger) DefineAchievement(a Achievement) error {
	if !validateDefinition(a) {
		return ErrInvalidAchievement
	}
	l.defsMu.Lock()
	defer l.defsMu.Unlock()
	if _, exists := l.defs[a.ID]; exists {
		return ErrDuplicateAchievement
	}
	a.Deleted = false
	l.defs[a.ID] = a
	return nil
}

// UndefineAchievement marks a definition as deleted.
func (l *PointsLedger) UndefineAchievement(id string) error {
	l.defsMu.Lock()
	defer l.defsMu.Unlock()
	def, ok := l.defs[id]
	if !ok {
		return ErrUnknownAchievement
	}
	def.Deleted = true
	l.defs[id] = def
	return nil
}

// applyAward pays the missing share of one definition into the account,
// honouring the category cap. The account mutex must be held.
func (l *PointsLedger) applyAward(acc *accountState, def Achievement, delta *PointDelta) {
	aw, ok := acc.awards[def.ID]
	if !ok {
		aw = &award{}
		acc.awards[def.ID] = aw
	}
	already := aw.Credited + aw.Overflow
	if def.Points <= already {
		return
	}
	diff := def.Points - already
	credited := diff
	if cap, hasCap := l.opts.CategoryCaps[def.Category]; hasCap {
		room := cap - acc.categories[def.Category]
		if room < 0 {
			room = 0
		}
		if credited > room {
			credited = room
		}
	}
	ovf := diff - credited
	if credited > 0 {
		acc.categories[def.Category] += credited
	}
	acc.overflow += ovf
	aw.Credited += credited
	aw.Overflow += ovf
	delta.Added += credited
	delta.Overflow += ovf
}

// clawBack reverts the award of a deleted achievement exactly once. The
// account mutex must be held.
func clawBack(acc *accountState, def Achievement, delta *PointDelta) {
	aw, ok := acc.awards[def.ID]
	if !ok || aw.Deducted {
		return
	}
	aw.Deducted = true
	if aw.Credited > 0 {
		acc.categories[def.Category] -= aw.Credited
		if acc.categories[def.Category] < 0 {
			acc.categories[def.Category] = 0
		}
		delta.Deducted += aw.Credited
	}
	acc.overflow -= aw.Overflow
	if acc.overflow < 0 {
		acc.overflow = 0
	}
}

// checkSeries grants the one shot series bonus for every completed series.
// The account mutex must be held.
func (l *PointsLedger) checkSeries(acc *accountState, defs map[string]Achievement, delta *PointDelta) {
	need := map[string][]string{}
	for _, def := range defs {
		if def.Series == "" || def.Deleted {
			continue
		}
		need[def.Series] = append(need[def.Series], def.ID)
	}
	for series, ids := range need {
		if acc.bonuses[series] {
			continue
		}
		complete := len(ids) > 0
		for _, id := range ids {
			if _, ok := acc.unlocked[id]; !ok {
				complete = false
				break
			}
		}
		if complete {
			acc.bonuses[series] = true
			acc.bonusPoints += l.opts.SeriesBonus
			delta.Bonuses = append(delta.Bonuses, series)
		}
	}
	if len(delta.Bonuses) > 1 {
		sort.Strings(delta.Bonuses)
	}
}

// Unlock records one achievement for an account. Ids without a definition yet
// stay pending until a definition arrives and a recalc pays them out.
func (l *PointsLedger) Unlock(account, achievementID string, at int64) (PointDelta, error) {
	if strings.TrimSpace(account) == "" {
		return PointDelta{}, ErrBlankAccount
	}
	acc := l.getAccount(account)
	acc.mu.Lock()
	defer acc.mu.Unlock()

	delta := PointDelta{Account: account}
	if _, dup := acc.unlocked[achievementID]; dup {
		delta.Total = acc.total()
		return delta, nil
	}
	acc.unlocked[achievementID] = at

	defs := l.defsCopy()
	if def, ok := defs[achievementID]; ok && !def.Deleted {
		l.applyAward(acc, def, &delta)
	}
	l.checkSeries(acc, defs, &delta)
	delta.Total = acc.total()
	return delta, nil
}

// Points returns the current breakdown for an account.
func (l *PointsLedger) Points(account string) PointsBreakdown {
	acc := l.findAccount(account)
	if acc == nil {
		return PointsBreakdown{}
	}
	acc.mu.Lock()
	defer acc.mu.Unlock()
	categories := make(map[string]int64, len(acc.categories))
	for name, pts := range acc.categories {
		categories[name] = pts
	}
	return PointsBreakdown{
		Total:      acc.total(),
		Categories: categories,
		Overflow:   acc.overflow,
		Bonuses:    acc.sortedBonuses(),
	}
}

// Recalc reconciles newly defined and deleted achievements for one account.
func (l *PointsLedger) Recalc(account string) (PointDelta, error) {
	if strings.TrimSpace(account) == "" {
		return PointDelta{}, ErrBlankAccount
	}
	acc := l.getAccount(account)
	acc.mu.Lock()
	defer acc.mu.Unlock()

	defs := l.defsCopy()
	delta := PointDelta{Account: account}
	ids := make([]string, 0, len(acc.unlocked))
	for id := range acc.unlocked {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		def, ok := defs[id]
		if !ok {
			continue
		}
		if def.Deleted {
			clawBack(acc, def, &delta)
			continue
		}
		l.applyAward(acc, def, &delta)
	}
	l.checkSeries(acc, defs, &delta)
	delta.Total = acc.total()
	return delta, nil
}

// GrantedBonus lists the series bonuses already paid to an account.
func (l *PointsLedger) GrantedBonus(account string) []string {
	acc := l.findAccount(account)
	if acc == nil {
		return nil
	}
	acc.mu.Lock()
	defer acc.mu.Unlock()
	return acc.sortedBonuses()
}

// CategoryPoints returns the awarded points of one category.
func (l *PointsLedger) CategoryPoints(account, category string) int64 {
	acc := l.findAccount(account)
	if acc == nil {
		return 0
	}
	acc.mu.Lock()
	defer acc.mu.Unlock()
	return acc.categories[category]
}

// Unlocked lists the unlocked achievement ids of an account in id order.
func (l *PointsLedger) Unlocked(account string) []string {
	acc := l.findAccount(account)
	if acc == nil {
		return nil
	}
	acc.mu.Lock()
	defer acc.mu.Unlock()
	if len(acc.unlocked) == 0 {
		return nil
	}
	ids := make([]string, 0, len(acc.unlocked))
	for id := range acc.unlocked {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// accountSnapshot is the serialisable form of one account.
type accountSnapshot struct {
	ID          string             `json:"id"`
	Unlocked    map[string]int64   `json:"unlocked"`
	Awards      map[string]*award  `json:"awards"`
	Categories  map[string]int64   `json:"categories"`
	Overflow    int64              `json:"overflow"`
	Bonuses     []string           `json:"bonuses"`
	BonusPoints int64              `json:"bonusPoints"`
}

// snapshot is the serialisable form of the whole ledger.
type snapshot struct {
	Definitions []Achievement     `json:"definitions"`
	Accounts    []accountSnapshot `json:"accounts"`
}

// Snapshot serialises definitions, unlock sets, points and bonus records.
func (l *PointsLedger) Snapshot() ([]byte, error) {
	l.accountsMu.Lock()
	defer l.accountsMu.Unlock()

	defs := l.defsCopy()
	snap := snapshot{
		Definitions: make([]Achievement, 0, len(defs)),
		Accounts:    make([]accountSnapshot, 0, len(l.accounts)),
	}
	for _, def := range defs {
		snap.Definitions = append(snap.Definitions, def)
	}
	sort.Slice(snap.Definitions, func(i, j int) bool {
		return snap.Definitions[i].ID < snap.Definitions[j].ID
	})
	for id, acc := range l.accounts {
		acc.mu.Lock()
		entry := accountSnapshot{
			ID:          id,
			Unlocked:    make(map[string]int64, len(acc.unlocked)),
			Awards:      make(map[string]*award, len(acc.awards)),
			Categories:  make(map[string]int64, len(acc.categories)),
			Overflow:    acc.overflow,
			Bonuses:     acc.sortedBonuses(),
			BonusPoints: acc.bonusPoints,
		}
		for achID, at := range acc.unlocked {
			entry.Unlocked[achID] = at
		}
		for achID, aw := range acc.awards {
			cp := *aw
			entry.Awards[achID] = &cp
		}
		for name, pts := range acc.categories {
			entry.Categories[name] = pts
		}
		acc.mu.Unlock()
		snap.Accounts = append(snap.Accounts, entry)
	}
	sort.Slice(snap.Accounts, func(i, j int) bool {
		return snap.Accounts[i].ID < snap.Accounts[j].ID
	})
	return json.Marshal(snap)
}

// Restore replaces the whole ledger with a snapshot, all or nothing.
func (l *PointsLedger) Restore(data []byte) error {
	var snap snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return ErrBadSnapshot
	}

	defs := make(map[string]Achievement, len(snap.Definitions))
	for _, def := range snap.Definitions {
		if !validateDefinition(def) {
			return ErrBadSnapshot
		}
		if _, dup := defs[def.ID]; dup {
			return ErrBadSnapshot
		}
		defs[def.ID] = def
	}
	accounts := make(map[string]*accountState, len(snap.Accounts))
	for _, entry := range snap.Accounts {
		if strings.TrimSpace(entry.ID) == "" || entry.Overflow < 0 || entry.BonusPoints < 0 {
			return ErrBadSnapshot
		}
		if _, dup := accounts[entry.ID]; dup {
			return ErrBadSnapshot
		}
		acc := newAccountState()
		for id, at := range entry.Unlocked {
			acc.unlocked[id] = at
		}
		for id, aw := range entry.Awards {
			if aw == nil || aw.Credited < 0 || aw.Overflow < 0 {
				return ErrBadSnapshot
			}
			cp := *aw
			acc.awards[id] = &cp
		}
		for name, pts := range entry.Categories {
			if pts < 0 {
				return ErrBadSnapshot
			}
			acc.categories[name] = pts
		}
		acc.overflow = entry.Overflow
		acc.bonusPoints = entry.BonusPoints
		for _, name := range entry.Bonuses {
			acc.bonuses[name] = true
		}
		accounts[entry.ID] = acc
	}

	l.accountsMu.Lock()
	defer l.accountsMu.Unlock()
	l.defsMu.Lock()
	defer l.defsMu.Unlock()
	l.defs = defs
	l.accounts = accounts
	return nil
}
