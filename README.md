# batch49-go-game-a-06

## 题面

限时闯关活动上线后，玩家在最后一个阶段明明还剩两秒却直接判负，配好的检查点回退也没生效，客服一天收到十几张工单。运维翻看当天的执行记录，发现同一批活动里还有玩家反馈宽限只生效了一次，之后再申请延期没有反应，重试后剩余总时长又被重新灌满；在线人数一多，同一毫秒内的阶段切换还会把上一阶段的判负状态带进新阶段。请把 gsb/batch49-go-game-a-06 的 timed_stage_guard.go 里 TimedStageGuard 的能力补齐，模块内只允许使用标准库。

公开接口：NewTimedStageGuard(Options) *TimedStageGuard、Start(questID, player string, at int64) (Run, error)、EnterStage(runID string, stage int, at int64) error、Extend(runID string, seconds int, at int64) (int64, error)、Tick(runID string, at int64) (StageState, error)、Retry(runID string, at int64) (Run, error)、Close(runID string) error、StageIndex(runID string) (int, error)、Deadline(runID string) (int64, error)、TotalSpent(runID string) (int64, error)、Snapshot() ([]byte, error)、Restore([]byte) error。时间一律由调用方传毫秒。

需要落实的行为：
1. Options 为零值时全局阶段时限 30000 毫秒、宽限 5000 毫秒；StageLimits 按阶段覆盖全局时限，TotalBudgetMs 为零表示不设总预算。
2. Start 生成的 Run.ID 为 任务ID#玩家#序号，序号从 1 递增且同一任务链内不复用；Stage 从 0 起，StartedAt 为传入时刻，Deadline 为起算时刻加该阶段时限。
3. Start 对空任务 ID 或空玩家返回 ErrInvalidIdentity，且不占用序号。
4. EnterStage 只接受恰好下一阶段，跳阶或回退返回 ErrStageOrder 并保持原阶段状态；被拒绝的调用不得改变截止时刻、宽限标记或重试次数。
5. 进入新阶段时截止时刻按该阶段时限重算，宽限标记清零，TotalSpent 累加上一阶段实际停留时长；阶段内 Tick 只读，不重复累加停留时长。
6. Tick 在截止前返回剩余毫秒；正好等于截止时刻仍算准时，RemainingMs 为 0；超过截止一毫秒即视为超时。
7. 超过截止但未超宽限时进入宽限：InGrace 为真，RemainingMs 按宽限终点计算；宽限只发一次，第二次超时不再进入宽限。
8. 宽限耗尽时回退到不高于当前阶段的最近检查点：RolledBack 为真，Stage 改为该检查点，截止时刻按回退时刻重算，整链不判负；检查点表里没有条目时视为不可回退。
9. 没有可用检查点时整链判负：Failed 与 Expired 同时为真，FailedStage 记录失败阶段，后续 Tick 保持判负。
10. Extend 在当前截止时刻上追加 seconds 秒并返回新截止时刻；seconds 非正返回 ErrInvalidExtend；同一阶段多次 Extend 依次累加，但宽限开始后一律拒绝。
11. Retry 把当前阶段截止时刻按传入时刻重算并让 Retries 加一，ID 保持不变；Close 释放该 run，之后查询返回 ErrRunNotFound。
12. Snapshot/Restore 往返保留每个 run 的阶段、起止时刻、宽限使用标记、重试次数与已耗总时长，恢复后 StageIndex 与 Deadline 与快照时一致。

硬约束：同一 run 的 Tick、EnterStage、Extend、Retry 会被多个网络协程并发调用，内部状态只能串行推进，同一毫秒内阶段切换与 Tick 交错时新阶段不得继承上一阶段的判负与宽限状态；快照必须原子生成，不能拍到一半字段；TotalSpentMs 超出 TotalBudgetMs 时按判负处理。

验收：go test ./... 的 15 条全部通过，不得修改测试、跳过用例或按固定输入写死；并发与恢复场景自行验证；只做本地实现。

## 运行方式

在模块根目录执行：

```
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-a-06`
- 包名：`task`
- 主文件：`timed_stage_guard.go`
- 测试目录：`tests/test_timed_stage_guard_test.go`
