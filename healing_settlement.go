package task

import (
	"errors"
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
	hp          int
	maxHP       int
	shield      ShieldState
	noHealUntil int64
	hots        []*hotState
}

type hotState struct {
	hot       HoT
	interval  int64
	lastAt    int64
	remaining int
}

// NewHealingSettlement 构造治疗结算器。
func NewHealingSettlement(opts Options) *HealingSettlement {
	return &HealingSettlement{opts: opts, targets: make(map[string]*targetState)}
}

// OverhealShieldPct 返回过量治疗转护盾比例。
func (s *HealingSettlement) OverhealShieldPct() int {
	return 0
}

// MaxShield 返回护盾上限。
func (s *HealingSettlement) MaxShield() int {
	return 0
}

// MaxHoTs 返回持续治疗条目上限。
func (s *HealingSettlement) MaxHoTs() int {
	return 0
}

// SetHealth 初始化或更新目标生命。
func (s *HealingSettlement) SetHealth(target string, hp, maxHP int) error {
	return errNotImplemented
}

// Health 返回目标当前生命。
func (s *HealingSettlement) Health(target string) int {
	return 0
}

// ApplyHeal 结算一次直接治疗。
func (s *HealingSettlement) ApplyHeal(req HealRequest) (HealResult, error) {
	return HealResult{}, errNotImplemented
}

// AddHoT 给目标施加持续治疗。
func (s *HealingSettlement) AddHoT(target string, hot HoT) error {
	return errNotImplemented
}

// HoTs 返回目标当前持续治疗条数。
func (s *HealingSettlement) HoTs(target string) int {
	return 0
}

// TickHoT 结算目标截至传入时刻的持续治疗跳数。
func (s *HealingSettlement) TickHoT(target string, at int64) ([]HealTick, error) {
	return nil, errNotImplemented
}

// Shield 返回目标当前护盾。
func (s *HealingSettlement) Shield(target string) ShieldState {
	return ShieldState{}
}

// ExpireShields 清理到期护盾。
func (s *HealingSettlement) ExpireShields(target string, at int64) error {
	return errNotImplemented
}

// SetNoHeal 设置目标的禁疗截止时刻。
func (s *HealingSettlement) SetNoHeal(target string, until int64) error {
	return errNotImplemented
}

// Snapshot 序列化全部目标状态。
func (s *HealingSettlement) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore 从快照恢复目标状态。
func (s *HealingSettlement) Restore(data []byte) error {
	return errNotImplemented
}
