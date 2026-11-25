# 两遍码率分配口径

实现位于 `two_pass_rate_planner.go`，公开类型为 `TwoPassPlanner`。

## 输入聚合

- 帧 0 是隐含场景起点；`MarkScene` 只接受大于当前最后声明边界且大于 0 的帧。
- 相邻有效边界间距小于 `minScene` 时，后一个边界并入前一个场景；归并基于原始声明动态计算。
- 场景复杂度是该场景全部 `FrameStats.Complexity` 之和。
- 无任何帧统计的场景标记 `Missing`，其权重取所有非空场景复杂度之和的平均值，而不是 0。

## 分配与取整

1. 设场景权重为 `w[i]`，权重总和为 `W`，总预算为 `T`。
2. 每个场景的基础份额为 `floor(T * w[i] / W)`，即向下取整。
3. 取整产生的剩余整数码率按小数余数从大到小逐单位补 1；余数相同按场景序号升序。补 1 时不得超过 `MaxRate`。
4. 若某场景的份额超过 `MaxRate` 或低于 `MinRate`，固定在该边界并标记 `Clamped`，退出后续再平衡。
5. 剩余预算只在未触界场景之间重新按权重分配；重复上述钳制过程，已经触界的场景不会被重新拉开。
6. 当上下限本身使总量无法满足时，按边界优先返回可行分配；`Validate` 会对超出 `Tolerance` 的结果返回 `ErrBudgetMismatch`。

## 场景切换关键帧

每个有效场景切换处收取一次 `KeyframeOverhead`，计入后继场景，因此后继场景份额先减去该开销。为避免总量漏账，预留开销总额在分配器中回补给首个场景；若首个场景触界，则由钳制/再平衡逻辑转给其他未触界场景。归并掉的短边界不产生切换，也不重复收取关键帧开销。

## 并发与快照

- `AddStats`、`Plan`、`SceneBoundaries`、`Stats`、`MarkScene`、`SetSceneMinFrames`、`Reset`、`Snapshot`、`Restore` 均由同一互斥锁保护，单次读取只看到完整状态。
- `Plan` 每次返回全新 `Scenes` 切片；`SceneBoundaries` 返回副本。
- 快照使用带版本号与 CRC32 的二进制载荷，覆盖原始场景边界、帧统计和最小场景长度。`Restore` 先完整解析并校验，成功后原子替换；任何损坏都返回 `ErrBadSnapshot` 且不修改当前状态。
