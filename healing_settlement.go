package task

import (
	"encoding/json"
	"errors"
	"math"
	"sort"
	"sync"
)

// 公开错误，供调用方按类别判断失败原因。
var (
	ErrInvalidHeal    = errors.New("healing_settlement: amount, reduction and max hp must be valid")
	ErrInvalidHealth  = errors.New("healing_settlement: hp must stay within 0 and max hp")
	ErrUnknownTarget  = errors.New("healing_settlement: target has no health state")
	ErrInvalidHoT     = errors.New("healing_settlement: hot values must be positive")
	ErrHoTFull        = errors.New("healing_settlement: hot slot limit reached")
	errNotImplemented = errors.New("healing_settlement: not implemented")
)

// Options 是构造治疗结算器的配置。
type Options struct {
	OverhealShieldPct int
	MaxShield         int
	MaxHoTs           int
	ShieldDurationMs  int64
}

// HealRequest 描述一次直接治疗。
type HealRequest struct {
	Target       string
	Amount       int
	At           int64
	ReductionPct int
	NoHeal       bool
}

// HealResult 是一次直接治疗的结算结果。
type HealResult struct {
	Target       string
	Applied      int
	Overheal     int
	ShieldGained int
	ShieldTotal  int
	Health       int
}

// HoT 是施加在目标身上的持续治疗。
type HoT struct {
	ID         string
	PerTick    int
	Ticks      int
	IntervalMs int64
	HastePct   int
	AppliedAt  int64
}

// HealTick 是持续治疗的单跳结算。
type HealTick struct {
	Target    string
	HoTID     string
	At        int64
	Amount    int
	Remaining int
}

// ShieldState 是目标的临时护盾状态。
type ShieldState struct {
	Amount    int
	ExpiresAt int64
}

// HealingSettlement 管理目标生命、护盾与持续治疗。
type HealingSettlement struct {
	mu      sync.Mutex
	opts    Options
	targets map[string]*targetState
}

type targetState struct {
	hp              int
	maxHP           int
	shield          ShieldState
	noHealUntil     int64
	shieldRemainder int
	hots            []*hotState
}

type hotState struct {
	hot       HoT
	interval  int64
	nextAt    int64
	remaining int
}

const (
	defaultOverhealShieldPct = 50
	defaultMaxShield         = 10000
	defaultMaxHoTs           = 4
	defaultShieldDurationMs  = 10000
	maxSingleHeal            = 1<<31 - 1
)

type snapshotData struct {
	Options Options                   `json:"options"`
	Targets map[string]snapshotTarget `json:"targets"`
}

type snapshotTarget struct {
	HP              int           `json:"hp"`
	MaxHP           int           `json:"max_hp"`
	Shield          ShieldState   `json:"shield"`
	NoHealUntil     int64         `json:"no_heal_until"`
	ShieldRemainder int           `json:"shield_remainder"`
	HoTs            []snapshotHoT `json:"hots"`
}

type snapshotHoT struct {
	HoT       HoT   `json:"hot"`
	Interval  int64 `json:"interval"`
	NextAt    int64 `json:"next_at"`
	Remaining int   `json:"remaining"`
}

// NewHealingSettlement 构造治疗结算器。
func NewHealingSettlement(opts Options) *HealingSettlement {
	return &HealingSettlement{opts: normalizeOptions(opts), targets: make(map[string]*targetState)}
}

// OverhealShieldPct 返回过量治疗转护盾比例。
func (s *HealingSettlement) OverhealShieldPct() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.opts.OverhealShieldPct
}

// MaxShield 返回护盾上限。
func (s *HealingSettlement) MaxShield() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.opts.MaxShield
}

// MaxHoTs 返回持续治疗条目上限。
func (s *HealingSettlement) MaxHoTs() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.opts.MaxHoTs
}

// SetHealth 初始化或更新目标生命。
func (s *HealingSettlement) SetHealth(target string, hp, maxHP int) error {
	if maxHP <= 0 || hp < 0 || hp > maxHP {
		return ErrInvalidHealth
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.targets[target]
	if !ok {
		st = &targetState{}
		s.targets[target] = st
	}
	st.hp = hp
	st.maxHP = maxHP
	return nil
}

// Health 返回目标当前生命。
func (s *HealingSettlement) Health(target string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.targets[target]
	if !ok {
		return 0
	}
	return st.hp
}

// ApplyHeal 结算一次直接治疗。
func (s *HealingSettlement) ApplyHeal(req HealRequest) (HealResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	st, ok := s.targets[req.Target]
	if !ok {
		return HealResult{Target: req.Target}, ErrUnknownTarget
	}
	if req.Amount < 0 || req.ReductionPct < 0 || req.ReductionPct > 100 {
		return HealResult{Target: req.Target}, ErrInvalidHeal
	}

	if st.shield.Amount > 0 && st.shield.ExpiresAt <= req.At {
		st.shield = ShieldState{}
	}

	amount := req.Amount
	if amount > maxSingleHeal {
		amount = maxSingleHeal
	}
	effective := int(int64(amount) * int64(100-req.ReductionPct) / 100)

	if req.NoHeal || inNoHealWindow(st.noHealUntil, req.At) {
		return HealResult{
			Target:      req.Target,
			Health:      st.hp,
			ShieldTotal: st.shield.Amount,
		}, nil
	}

	missing := st.maxHP - st.hp
	if missing < 0 {
		missing = 0
	}
	applied := effective
	if applied > missing {
		applied = missing
	}
	st.hp += applied
	overheal := effective - applied

	shieldGained := 0
	if overheal > 0 {
		numerator := int64(overheal)*int64(s.opts.OverhealShieldPct) + int64(st.shieldRemainder)
		converted := int(numerator / 100)
		st.shieldRemainder = int(numerator % 100)

		space := s.opts.MaxShield - st.shield.Amount
		if space < 0 {
			space = 0
		}
		shieldGained = converted
		if shieldGained > space {
			shieldGained = space
		}
		if shieldGained > 0 {
			st.shield.Amount += shieldGained
			st.shield.ExpiresAt = addClamped(req.At, s.opts.ShieldDurationMs)
		}
	}

	return HealResult{
		Target:       req.Target,
		Applied:      applied,
		Overheal:     overheal,
		ShieldGained: shieldGained,
		ShieldTotal:  st.shield.Amount,
		Health:       st.hp,
	}, nil
}

// AddHoT 给目标施加持续治疗。
func (s *HealingSettlement) AddHoT(target string, hot HoT) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	st, ok := s.targets[target]
	if !ok {
		return ErrUnknownTarget
	}
	if hot.PerTick <= 0 || hot.Ticks <= 0 || hot.IntervalMs <= 0 {
		return ErrInvalidHoT
	}
	if len(st.hots) >= s.opts.MaxHoTs {
		return ErrHoTFull
	}

	perTick := hot.PerTick
	if perTick > maxSingleHeal {
		perTick = maxSingleHeal
	}
	hot.PerTick = perTick

	interval := hotInterval(hot.IntervalMs, hot.HastePct)
	st.hots = append(st.hots, &hotState{
		hot:       hot,
		interval:  interval,
		nextAt:    addClamped(hot.AppliedAt, interval),
		remaining: hot.Ticks,
	})
	return nil
}

// HoTs 返回目标当前持续治疗条数。
func (s *HealingSettlement) HoTs(target string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.targets[target]
	if !ok {
		return 0
	}
	return len(st.hots)
}

// TickHoT 结算目标截至传入时刻的持续治疗跳数。
func (s *HealingSettlement) TickHoT(target string, at int64) ([]HealTick, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	st, ok := s.targets[target]
	if !ok {
		return nil, ErrUnknownTarget
	}

	type pendingTick struct {
		hot       *hotState
		hotIndex  int
		at        int64
		remaining int
	}

	pending := make([]pendingTick, 0)
	for index, hot := range st.hots {
		for hot.remaining > 0 && hot.nextAt <= at {
			tickAt := hot.nextAt
			hot.remaining--
			pending = append(pending, pendingTick{
				hot:       hot,
				hotIndex:  index,
				at:        tickAt,
				remaining: hot.remaining,
			})

			next := addClamped(hot.nextAt, hot.interval)
			if next <= tickAt {
				hot.remaining = 0
				break
			}
			hot.nextAt = next
		}
	}

	sort.SliceStable(pending, func(i, j int) bool {
		if pending[i].at != pending[j].at {
			return pending[i].at < pending[j].at
		}
		return pending[i].hotIndex < pending[j].hotIndex
	})

	ticks := make([]HealTick, 0, len(pending))
	for _, tick := range pending {
		amount := 0
		if !inNoHealWindow(st.noHealUntil, tick.at) {
			missing := st.maxHP - st.hp
			if missing > 0 {
				amount = tick.hot.hot.PerTick
				if amount > missing {
					amount = missing
				}
				st.hp += amount
			}
		}
		ticks = append(ticks, HealTick{
			Target:    target,
			HoTID:     tick.hot.hot.ID,
			At:        tick.at,
			Amount:    amount,
			Remaining: tick.remaining,
		})
	}

	kept := make([]*hotState, 0, len(st.hots))
	for _, hot := range st.hots {
		if hot.remaining > 0 {
			kept = append(kept, hot)
		}
	}
	st.hots = kept
	return ticks, nil
}

// Shield 返回目标当前护盾。
func (s *HealingSettlement) Shield(target string) ShieldState {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.targets[target]
	if !ok {
		return ShieldState{}
	}
	return st.shield
}

// ExpireShields 清理到期护盾。
func (s *HealingSettlement) ExpireShields(target string, at int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.targets[target]
	if !ok {
		return ErrUnknownTarget
	}
	if st.shield.Amount > 0 && at >= st.shield.ExpiresAt {
		st.shield = ShieldState{}
	}
	return nil
}

// SetNoHeal 设置目标的禁疗截止时刻。
func (s *HealingSettlement) SetNoHeal(target string, until int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.targets[target]
	if !ok {
		return ErrUnknownTarget
	}
	st.noHealUntil = until
	return nil
}

// Snapshot 序列化全部目标状态。
func (s *HealingSettlement) Snapshot() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data := snapshotData{
		Options: s.opts,
		Targets: make(map[string]snapshotTarget, len(s.targets)),
	}
	for name, st := range s.targets {
		target := snapshotTarget{
			HP:              st.hp,
			MaxHP:           st.maxHP,
			Shield:          st.shield,
			NoHealUntil:     st.noHealUntil,
			ShieldRemainder: st.shieldRemainder,
			HoTs:            make([]snapshotHoT, 0, len(st.hots)),
		}
		for _, hot := range st.hots {
			target.HoTs = append(target.HoTs, snapshotHoT{
				HoT:       hot.hot,
				Interval:  hot.interval,
				NextAt:    hot.nextAt,
				Remaining: hot.remaining,
			})
		}
		data.Targets[name] = target
	}
	return json.Marshal(data)
}

// Restore 从快照恢复目标状态。
func (s *HealingSettlement) Restore(data []byte) error {
	var decoded snapshotData
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}

	targets := make(map[string]*targetState, len(decoded.Targets))
	for name, target := range decoded.Targets {
		st := &targetState{
			hp:              target.HP,
			maxHP:           target.MaxHP,
			shield:          target.Shield,
			noHealUntil:     target.NoHealUntil,
			shieldRemainder: target.ShieldRemainder,
			hots:            make([]*hotState, 0, len(target.HoTs)),
		}
		for _, hot := range target.HoTs {
			st.hots = append(st.hots, &hotState{
				hot:       hot.HoT,
				interval:  hot.Interval,
				nextAt:    hot.NextAt,
				remaining: hot.Remaining,
			})
		}
		targets[name] = st
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.opts = normalizeOptions(decoded.Options)
	s.targets = targets
	return nil
}

func normalizeOptions(opts Options) Options {
	if opts.OverhealShieldPct <= 0 {
		opts.OverhealShieldPct = defaultOverhealShieldPct
	}
	if opts.OverhealShieldPct > 100 {
		opts.OverhealShieldPct = 100
	}
	if opts.MaxShield <= 0 {
		opts.MaxShield = defaultMaxShield
	}
	if opts.MaxHoTs <= 0 {
		opts.MaxHoTs = defaultMaxHoTs
	}
	if opts.ShieldDurationMs <= 0 {
		opts.ShieldDurationMs = defaultShieldDurationMs
	}
	return opts
}

func inNoHealWindow(until, at int64) bool {
	return until != 0 && at < until
}

func addClamped(a, b int64) int64 {
	if b > 0 && a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}

func hotInterval(intervalMs int64, hastePct int) int64 {
	if hastePct < 0 {
		hastePct = 0
	}
	haste := int64(hastePct)
	denominator := int64(100)
	if haste <= math.MaxInt64-100 {
		denominator = haste + 100
	} else {
		denominator = math.MaxInt64
	}
	if intervalMs > math.MaxInt64/100 {
		intervalMs = math.MaxInt64 / 100
	}
	interval := intervalMs * 100 / denominator
	if interval < 1 {
		return 1
	}
	return interval
}
