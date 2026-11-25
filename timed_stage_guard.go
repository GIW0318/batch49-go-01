package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

const (
	defaultStageLimitMs = 30000
	defaultGraceMs      = 5000
)

// 公开错误，供调用方按类别判断失败原因。
var (
	ErrInvalidIdentity = errors.New("timed_stage_guard: quest id and player are required")
	ErrRunNotFound     = errors.New("timed_stage_guard: run not found")
	ErrStageOrder      = errors.New("timed_stage_guard: stage must advance by exactly one")
	ErrInvalidExtend   = errors.New("timed_stage_guard: extend seconds must be positive")
	ErrGraceConsumed   = errors.New("timed_stage_guard: grace already consumed")
	ErrQuestFailed     = errors.New("timed_stage_guard: quest already failed")
	ErrBudgetExceeded  = errors.New("timed_stage_guard: total budget exceeded")
)

// Options 是构造限时阶段守卫的配置。
type Options struct {
	StageLimitMs  int64
	GraceMs       int64
	TotalBudgetMs int64
	StageLimits   map[int]int64
	Checkpoints   map[int]bool
}

// Run 描述一次进行中的任务链。
type Run struct {
	ID             string
	QuestID        string
	Player         string
	Stage          int
	StartedAt      int64
	StageStartedAt int64
	Deadline       int64
	GraceUsed      bool
	Retries        int
	TotalSpentMs   int64
	Failed         bool
}

// StageState 是单次 Tick 的观测结果。
type StageState struct {
	RunID       string
	Stage       int
	RemainingMs int64
	InGrace     bool
	Expired     bool
	RolledBack  bool
	Failed      bool
	FailedStage int
}

// TimedStageGuard 管理多个任务链的阶段计时。
type TimedStageGuard struct {
	mu   sync.Mutex
	opts Options
	runs map[string]*runState
	next int
}

type runState struct {
	run         Run
	failedStage int
}

// NewTimedStageGuard 构造守卫。
func NewTimedStageGuard(opts Options) *TimedStageGuard {
	if opts.StageLimitMs <= 0 {
		opts.StageLimitMs = defaultStageLimitMs
	}
	if opts.GraceMs <= 0 {
		opts.GraceMs = defaultGraceMs
	}
	return &TimedStageGuard{opts: opts, runs: make(map[string]*runState), next: 1}
}

// StageLimitMs 返回全局阶段时限。
func (g *TimedStageGuard) StageLimitMs() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.opts.StageLimitMs
}

// GraceMs 返回阶段宽限。
func (g *TimedStageGuard) GraceMs() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.opts.GraceMs
}

// stageLimit 返回指定阶段的时限，StageLimits 优先于全局配置。
func (g *TimedStageGuard) stageLimit(stage int) int64 {
	if g.opts.StageLimits != nil {
		if limit, ok := g.opts.StageLimits[stage]; ok && limit > 0 {
			return limit
		}
	}
	return g.opts.StageLimitMs
}

// budgetExceeded 判断累计消耗是否超出总预算，预算为零表示不限制。
func (g *TimedStageGuard) budgetExceeded(spent int64) bool {
	return g.opts.TotalBudgetMs > 0 && spent > g.opts.TotalBudgetMs
}

// failRun 将任务链标记为判负。
func (g *TimedStageGuard) failRun(st *runState, stage int) {
	st.run.Failed = true
	st.failedStage = stage
}

// Start 开启一条新的任务链。
func (g *TimedStageGuard) Start(questID, player string, at int64) (Run, error) {
	if questID == "" || player == "" {
		return Run{}, ErrInvalidIdentity
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	run := Run{
		ID:             fmt.Sprintf("%s#%s#%d", questID, player, g.next),
		QuestID:        questID,
		Player:         player,
		Stage:          0,
		StartedAt:      at,
		StageStartedAt: at,
	}
	run.Deadline = at + g.stageLimit(0)
	g.next++
	g.runs[run.ID] = &runState{run: run}
	return run, nil
}

// EnterStage 推进到下一个阶段。
func (g *TimedStageGuard) EnterStage(runID string, stage int, at int64) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	st, ok := g.runs[runID]
	if !ok {
		return ErrRunNotFound
	}
	if st.run.Failed {
		return ErrQuestFailed
	}
	if stage != st.run.Stage+1 {
		return ErrStageOrder
	}
	st.run.TotalSpentMs += at - st.run.StageStartedAt
	st.run.Stage = stage
	st.run.StageStartedAt = at
	st.run.Deadline = at + g.stageLimit(stage)
	st.run.GraceUsed = false
	if g.budgetExceeded(st.run.TotalSpentMs) {
		g.failRun(st, stage)
		return ErrBudgetExceeded
	}
	return nil
}

// Extend 延长当前阶段截止时刻。
func (g *TimedStageGuard) Extend(runID string, seconds int, at int64) (int64, error) {
	if seconds <= 0 {
		return 0, ErrInvalidExtend
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	st, ok := g.runs[runID]
	if !ok {
		return 0, ErrRunNotFound
	}
	if st.run.Failed {
		return 0, ErrQuestFailed
	}
	if st.run.GraceUsed {
		return 0, ErrGraceConsumed
	}
	st.run.Deadline += int64(seconds) * 1000
	return st.run.Deadline, nil
}

// checkpointFor 返回不高于当前阶段的最近检查点，没有则返回 false。
func (g *TimedStageGuard) checkpointFor(stage int) (int, bool) {
	if len(g.opts.Checkpoints) == 0 {
		return 0, false
	}
	best := -1
	for cp, ok := range g.opts.Checkpoints {
		if ok && cp <= stage && cp > best {
			best = cp
		}
	}
	if best < 0 {
		return 0, false
	}
	return best, true
}

// Tick 按传入时刻推进并返回阶段状态。
func (g *TimedStageGuard) Tick(runID string, at int64) (StageState, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	st, ok := g.runs[runID]
	if !ok {
		return StageState{}, ErrRunNotFound
	}
	state := StageState{RunID: runID, Stage: st.run.Stage}
	if st.run.Failed {
		state.Failed = true
		state.Expired = true
		state.FailedStage = st.failedStage
		return state, nil
	}
	if at <= st.run.Deadline {
		state.RemainingMs = st.run.Deadline - at
		return state, nil
	}
	graceEnd := st.run.Deadline + g.opts.GraceMs
	if at <= graceEnd {
		st.run.GraceUsed = true
		state.InGrace = true
		state.RemainingMs = graceEnd - at
		return state, nil
	}
	// 宽限耗尽，累计本阶段停留时长后尝试回退或判负。
	st.run.TotalSpentMs += at - st.run.StageStartedAt
	if g.budgetExceeded(st.run.TotalSpentMs) {
		g.failRun(st, st.run.Stage)
		state.Failed = true
		state.Expired = true
		state.FailedStage = st.failedStage
		return state, nil
	}
	if cp, ok := g.checkpointFor(st.run.Stage); ok {
		st.run.Stage = cp
		st.run.StageStartedAt = at
		st.run.Deadline = at + g.stageLimit(cp)
		st.run.GraceUsed = false
		state.Stage = cp
		state.RolledBack = true
		state.RemainingMs = st.run.Deadline - at
		return state, nil
	}
	g.failRun(st, st.run.Stage)
	state.Failed = true
	state.Expired = true
	state.FailedStage = st.failedStage
	return state, nil
}

// Retry 重置当前阶段计时。
func (g *TimedStageGuard) Retry(runID string, at int64) (Run, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	st, ok := g.runs[runID]
	if !ok {
		return Run{}, ErrRunNotFound
	}
	if st.run.Failed {
		return Run{}, ErrQuestFailed
	}
	st.run.StageStartedAt = at
	st.run.Deadline = at + g.stageLimit(st.run.Stage)
	st.run.GraceUsed = false
	st.run.Retries++
	return st.run, nil
}

// Close 结束并释放一条任务链。
func (g *TimedStageGuard) Close(runID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.runs[runID]; !ok {
		return ErrRunNotFound
	}
	delete(g.runs, runID)
	return nil
}

// StageIndex 返回当前阶段号。
func (g *TimedStageGuard) StageIndex(runID string) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	st, ok := g.runs[runID]
	if !ok {
		return 0, ErrRunNotFound
	}
	return st.run.Stage, nil
}

// Deadline 返回当前阶段截止时刻。
func (g *TimedStageGuard) Deadline(runID string) (int64, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	st, ok := g.runs[runID]
	if !ok {
		return 0, ErrRunNotFound
	}
	return st.run.Deadline, nil
}

// TotalSpent 返回该任务链累计消耗毫秒数。
func (g *TimedStageGuard) TotalSpent(runID string) (int64, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	st, ok := g.runs[runID]
	if !ok {
		return 0, ErrRunNotFound
	}
	return st.run.TotalSpentMs, nil
}

type snapshotRun struct {
	Run         Run  `json:"run"`
	FailedStage int  `json:"failed_stage"`
	Failed      bool `json:"failed"`
}

type snapshot struct {
	Next    int           `json:"next"`
	Options Options       `json:"options"`
	Runs    []snapshotRun `json:"runs"`
}

// Snapshot 序列化全部任务链状态。
func (g *TimedStageGuard) Snapshot() ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	snap := snapshot{
		Next:    g.next,
		Options: g.opts,
		Runs:    make([]snapshotRun, 0, len(g.runs)),
	}
	for _, st := range g.runs {
		snap.Runs = append(snap.Runs, snapshotRun{
			Run:         st.run,
			FailedStage: st.failedStage,
			Failed:      st.run.Failed,
		})
	}
	return json.Marshal(snap)
}

// Restore 从快照恢复任务链状态。
func (g *TimedStageGuard) Restore(data []byte) error {
	var snap snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if snap.Options.StageLimitMs <= 0 {
		snap.Options.StageLimitMs = defaultStageLimitMs
	}
	if snap.Options.GraceMs <= 0 {
		snap.Options.GraceMs = defaultGraceMs
	}
	g.opts = snap.Options
	g.next = snap.Next
	if g.next < 1 {
		g.next = 1
	}
	g.runs = make(map[string]*runState, len(snap.Runs))
	for _, sr := range snap.Runs {
		g.runs[sr.Run.ID] = &runState{run: sr.Run, failedStage: sr.FailedStage}
	}
	return nil
}
