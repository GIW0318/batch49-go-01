# batch49-go-game-c-10

模块路径 `gsb/batch49-go-game-c-10`，包名 `task`，实现文件 `two_pass_rate_planner.go`，类型 `TwoPassPlanner`。

## 题面

同一部片子重编码后，前半段的打斗戏糊得看不清，后半段的静态对话又占掉大量码率：第一遍统计里两段复杂度差了三倍，第二遍却按平均分配；场景切换处插入的关键帧又把下一场景的预算挤没，整段码率曲线像被削平；负责调参的同事连续改了四版码率表，峰值码率仍然顶到上限，静态场景却分不到足够码率。请补全模块 gsb/batch49-go-game-c-10 中 two_pass_rate_planner.go 的 TwoPassPlanner，由它把第一遍的帧级复杂度聚合成场景级码率方案，并保证总量、上下限与容差同时成立。

帧统计由帧号、字节数与复杂度组成，场景起点由调用方显式声明，帧 0 是隐含的第一个场景。公开接口：NewTwoPassPlanner() *TwoPassPlanner、MarkScene(frame int64) error、AddStats(s FrameStats) error、Plan(cfg PlanConfig) (Allocation, error)、SceneBoundaries() []int64、Validate(a Allocation) error、SetSceneMinFrames(n int64) error、SceneCount() int、Stats() PlannerStats、Reset() error、Snapshot() ([]byte, error)、Restore(data []byte) error。

需要落实的行为：1）场景边界由 MarkScene 声明，帧 0 是隐含起点，重复或倒序声明返回 ErrBadScene；相邻边界间距小于最小场景长度时并入前一个场景；2）帧统计必须按帧号严格递增入库，重复帧号返回 ErrDuplicateFrame，倒退返回 ErrOutOfOrder，负帧号、负字节数或负复杂度返回 ErrBadStats；3）场景复杂度为该场景所有帧复杂度之和，Plan 按复杂度比例分配总码率，向下取整；4）每个场景的分配受 MinRate 与 MaxRate 钳制，触界场景标记 Clamped；5）再平衡只把剩余码率分给未钳制的场景，已经触界的场景退出再平衡，不得被重新拉开；6）分配总量与 TotalRate 的误差必须落在 Tolerance 内，超出时 Validate 返回 ErrBudgetMismatch；7）场景切换处额外插入的关键帧开销必须从下一场景的预算里扣除，不能重复占用；8）某个场景完全没有帧统计时，按已有场景的平均复杂度补齐并标记 Missing，不能按零复杂度处理；9）SetSceneMinFrames 只接受不小于 1 的值，否则 ErrBadConfig；10）没有任何帧统计时 Plan 返回 ErrNoStats，总码率非正或上下限颠倒返回 ErrBadConfig；11）分配结果携带场景序号、起止帧、码率、钳制与缺失标记，整体带上总预算与容差，Validate 同时检查单项上下限与总量容差。

硬约束：AddStats 与 Plan、SceneBoundaries、Stats 会被采集线程和调度协程并发调用，读方只能看到完整状态，不能读到一半新一半旧；快照要覆盖场景边界、帧统计与最小场景长度，Restore 之后重新出方案必须与中断前一致，损坏快照返回 ErrBadSnapshot 且不改动现有数据。并发出方案与重启恢复需要自行构造验证，每次 Plan 必须返回全新的切片，调用方改动结果不得影响内部状态。

验收：go test ./... 的 15 条用例全部通过，测试文件不得修改、跳过或按固定输入写死；只做本地实现。交付源码、运行记录和码率分配口径说明，取整与进位规则要写清楚。

## 运行

```bash
cd batch49-go-game-c-10
go test ./...
```
