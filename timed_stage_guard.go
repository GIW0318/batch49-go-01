package task

import (
	"errors"
	"sync"
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

	errNotImplemented = errors.New("timed_stage_guard: not implemented")
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
	return &TimedStageGuard{opts: opts, runs: make(map[string]*runState)}
}

// StageLimitMs 返回全局阶段时限。
func (g *TimedStageGuard) StageLimitMs() int64 {
	return 0
}

// GraceMs 返回阶段宽限。
func (g *TimedStageGuard) GraceMs() int64 {
	return 0
}

// Start 开启一条新的任务链。
func (g *TimedStageGuard) Start(questID, player string, at int64) (Run, error) {
	return Run{}, errNotImplemented
}

// EnterStage 推进到下一个阶段。
func (g *TimedStageGuard) EnterStage(runID string, stage int, at int64) error {
	return errNotImplemented
}

// Extend 延长当前阶段截止时刻。
func (g *TimedStageGuard) Extend(runID string, seconds int, at int64) (int64, error) {
	return 0, errNotImplemented
}

// Tick 按传入时刻推进并返回阶段状态。
func (g *TimedStageGuard) Tick(runID string, at int64) (StageState, error) {
	return StageState{}, errNotImplemented
}

// Retry 重置当前阶段计时。
func (g *TimedStageGuard) Retry(runID string, at int64) (Run, error) {
	return Run{}, errNotImplemented
}

// Close 结束并释放一条任务链。
func (g *TimedStageGuard) Close(runID string) error {
	return errNotImplemented
}

// StageIndex 返回当前阶段号。
func (g *TimedStageGuard) StageIndex(runID string) (int, error) {
	return 0, errNotImplemented
}

// Deadline 返回当前阶段截止时刻。
func (g *TimedStageGuard) Deadline(runID string) (int64, error) {
	return 0, errNotImplemented
}

// TotalSpent 返回该任务链累计消耗毫秒数。
func (g *TimedStageGuard) TotalSpent(runID string) (int64, error) {
	return 0, errNotImplemented
}

// Snapshot 序列化全部任务链状态。
func (g *TimedStageGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore 从快照恢复任务链状态。
func (g *TimedStageGuard) Restore(data []byte) error {
	return errNotImplemented
}
