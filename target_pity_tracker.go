package task

import (
	"encoding/json"
	"errors"
	"sort"
	"sync"
)

// 公开错误，供调用方按类别判断失败原因。
var (
	ErrInvalidTarget   = errors.New("target_pity_tracker: player, pool and target are required")
	ErrNoTarget        = errors.New("target_pity_tracker: no target selected for this pool")
	errInvalidSnapshot = errors.New("target_pity_tracker: invalid snapshot")
)

// Options 是构造自选保底追踪器的配置。
type Options struct {
	Threshold  int
	ConvertNum int
	ConvertDen int
}

// PityState 是一次结算后的保底状态。
type PityState struct {
	Player    string
	Pool      string
	Target    string
	Watermark int
	Normal    int
	Forced    bool
}

// ConversionRecord 记录一次目标切换的折算。
type ConversionRecord struct {
	Player string
	Pool   string
	From   string
	To     string
	Before int
	After  int
}

// TargetPityTracker 按玩家与卡池管理自选目标保底。
type TargetPityTracker struct {
	mu    sync.Mutex
	opts  Options
	pools map[string]map[string]*poolState
}

type poolState struct {
	target      string
	watermark   int
	normal      int
	forced      bool
	conversions []ConversionRecord
}

type trackerSnapshot struct {
	Version int                       `json:"version"`
	States  map[string]playerSnapshot `json:"players"`
}

type playerSnapshot struct {
	Pools map[string]poolSnapshot `json:"pools"`
}

type poolSnapshot struct {
	Target      string             `json:"target"`
	Watermark   int                `json:"watermark"`
	Normal      int                `json:"normal"`
	Forced      bool               `json:"forced"`
	Conversions []ConversionRecord `json:"conversions"`
}

// NewTargetPityTracker 构造保底追踪器。
func NewTargetPityTracker(opts Options) *TargetPityTracker {
	if opts.Threshold <= 0 {
		opts.Threshold = 80
	}
	if opts.ConvertNum <= 0 {
		opts.ConvertNum = 1
	}
	if opts.ConvertDen <= 0 {
		opts.ConvertDen = 2
	}
	return &TargetPityTracker{opts: opts, pools: make(map[string]map[string]*poolState)}
}

// Threshold 返回强制出目标的阈值。
func (t *TargetPityTracker) Threshold() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.opts.Threshold
}

// Ratio 返回切换目标的折算比例的分子与分母。
func (t *TargetPityTracker) Ratio() (int, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.opts.ConvertNum, t.opts.ConvertDen
}

// SelectTarget 为玩家在指定卡池选定目标。
func (t *TargetPityTracker) SelectTarget(player, pool, target string) error {
	if player == "" || pool == "" || target == "" {
		return ErrInvalidTarget
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	playerPools := t.pools[player]
	if playerPools == nil {
		playerPools = make(map[string]*poolState)
		t.pools[player] = playerPools
	}
	if state := playerPools[pool]; state != nil {
		state.target = target
		return nil
	}

	playerPools[pool] = &poolState{target: target}
	return nil
}

// OnRoll 结算一次抽取。
func (t *TargetPityTracker) OnRoll(player, pool, hit string) (PityState, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	state := t.getPoolState(player, pool)
	if state == nil || state.target == "" {
		return PityState{}, ErrNoTarget
	}

	if state.watermark+1 >= t.opts.Threshold {
		state.watermark = 0
		state.forced = true
		return t.pityState(player, pool, state, true), nil
	}

	if hit == state.target {
		state.watermark = 0
		state.normal = 0
	} else {
		state.watermark++
		state.normal++
	}
	state.forced = false

	return t.pityState(player, pool, state, false), nil
}

// SwitchTarget 切换目标并按比例折算水位。
func (t *TargetPityTracker) SwitchTarget(player, pool, target string) (PityState, error) {
	if player == "" || pool == "" || target == "" {
		return PityState{}, ErrInvalidTarget
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	state := t.getPoolState(player, pool)
	if state == nil || state.target == "" {
		return PityState{}, ErrNoTarget
	}

	before := state.watermark
	after := before * t.opts.ConvertNum / t.opts.ConvertDen
	if after < 0 {
		after = 0
	}
	if after > before {
		after = before
	}
	if maxWatermark := t.opts.Threshold - 1; after > maxWatermark {
		after = maxWatermark
	}

	record := ConversionRecord{
		Player: player,
		Pool:   pool,
		From:   state.target,
		To:     target,
		Before: before,
		After:  after,
	}
	state.conversions = append(state.conversions, record)
	state.target = target
	state.watermark = after

	return t.pityState(player, pool, state, state.forced), nil
}

// Watermark 返回当前水位。
func (t *TargetPityTracker) Watermark(player, pool string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	state := t.getPoolState(player, pool)
	if state == nil {
		return 0
	}
	return state.watermark
}

// NormalPity 返回普通保底计数。
func (t *TargetPityTracker) NormalPity(player, pool string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	state := t.getPoolState(player, pool)
	if state == nil {
		return 0
	}
	return state.normal
}

// Forced 返回最近一次结算是否强制出目标。
func (t *TargetPityTracker) Forced(player, pool string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	state := t.getPoolState(player, pool)
	return state != nil && state.forced
}

// Target 返回当前选定的目标。
func (t *TargetPityTracker) Target(player, pool string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	state := t.getPoolState(player, pool)
	if state == nil {
		return ""
	}
	return state.target
}

// Pools 返回玩家已选过目标的卡池。
func (t *TargetPityTracker) Pools(player string) []string {
	t.mu.Lock()
	defer t.mu.Unlock()

	playerPools := t.pools[player]
	if len(playerPools) == 0 {
		return nil
	}

	pools := make([]string, 0, len(playerPools))
	for pool := range playerPools {
		pools = append(pools, pool)
	}
	sort.Strings(pools)
	return pools
}

// Conversions 返回该玩家该卡池的折算记录条数。
func (t *TargetPityTracker) Conversions(player, pool string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	state := t.getPoolState(player, pool)
	if state == nil {
		return 0
	}
	return len(state.conversions)
}

// Snapshot 序列化全部保底状态。
func (t *TargetPityTracker) Snapshot() ([]byte, error) {
	t.mu.Lock()
	snapshot := trackerSnapshot{
		Version: 1,
		States:  make(map[string]playerSnapshot, len(t.pools)),
	}
	for player, playerPools := range t.pools {
		playerState := playerSnapshot{Pools: make(map[string]poolSnapshot, len(playerPools))}
		for pool, state := range playerPools {
			conversions := append([]ConversionRecord(nil), state.conversions...)
			playerState.Pools[pool] = poolSnapshot{
				Target:      state.target,
				Watermark:   state.watermark,
				Normal:      state.normal,
				Forced:      state.forced,
				Conversions: conversions,
			}
		}
		snapshot.States[player] = playerState
	}
	t.mu.Unlock()

	return json.Marshal(snapshot)
}

// Restore 从快照恢复保底状态。
func (t *TargetPityTracker) Restore(data []byte) error {
	var snapshot trackerSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return err
	}
	if snapshot.States == nil {
		return errInvalidSnapshot
	}

	pools := make(map[string]map[string]*poolState, len(snapshot.States))
	for player, playerState := range snapshot.States {
		if player == "" || playerState.Pools == nil {
			return errInvalidSnapshot
		}
		playerPools := make(map[string]*poolState, len(playerState.Pools))
		for pool, state := range playerState.Pools {
			if pool == "" || state.Target == "" {
				return errInvalidSnapshot
			}
			conversions := append([]ConversionRecord(nil), state.Conversions...)
			playerPools[pool] = &poolState{
				target:      state.Target,
				watermark:   state.Watermark,
				normal:      state.Normal,
				forced:      state.Forced,
				conversions: conversions,
			}
		}
		pools[player] = playerPools
	}

	t.mu.Lock()
	t.pools = pools
	t.mu.Unlock()
	return nil
}

func (t *TargetPityTracker) getPoolState(player, pool string) *poolState {
	playerPools := t.pools[player]
	if playerPools == nil {
		return nil
	}
	return playerPools[pool]
}

func (t *TargetPityTracker) pityState(player, pool string, state *poolState, forced bool) PityState {
	return PityState{
		Player:    player,
		Pool:      pool,
		Target:    state.target,
		Watermark: state.watermark,
		Normal:    state.normal,
		Forced:    forced,
	}
}
