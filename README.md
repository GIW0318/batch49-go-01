# 媒体从时钟的偏移伺服与保持模式

## 题面

播出线上的媒体时间最近总在整点前后抖两下，机顶盒画面先慢半拍又突然跳一格，主时钟断掉十几秒之后本地时间干脆停住。落点 gsb/batch49-go-game-c-13 的 media_clock_slave.go，需要把 MediaClockSlave 补齐。

伺服输入：NewMediaClockSlave(cfg Config) *MediaClockSlave 用 WindowSize、MaxRTT、StepThreshold、SlewLimitPPM、HoldoverAfter 界定伺服；PushSample(s SyncSample) error 接收一次往返测量，RTT 为负或超过 MaxRTT、本地接收时刻为负、主时钟时间戳非正都报 ErrBadSample；每个样本按中点法折算偏移，即主时钟时间戳减去本地接收时刻与半个往返之差，偏移为负说明本地时钟快于参考时钟；样本里的 Seq 只用于外部对账，不参与排序。窗口里已有三条以上样本时，RTT 超过窗口中位数三倍的样本判为异常，报 ErrOutlier 且不进窗口；主时钟时间戳相对上一条回退的样本直接丢弃，报 ErrStaleSample，既不进窗口也不参与中位数与计数。

偏移与频偏：窗口内各样本的偏移取中位数作为目标偏移，窗口按 WindowSize 滚动，样本数达到上限后移除最旧的，窗口未满时按现有样本算中位数；目标与当前偏移之差的绝对值不超过 StepThreshold 时走频偏微调，方向朝目标，幅度受 SlewLimitPPM 限制（允许 1 到 2000 ppm，越界报 ErrSlewRange，收紧上限时正在生效的频偏要立刻降到新上限）；超过 StepThreshold 时直接步进，Steps() int 只统计向前的步进，步进之后本地读数不允许变小。时间轴一律不得回退，向后的偏差不能靠步进硬拉，只能挂上负频偏慢慢追，步进或收敛之后频偏要归零。

推进与保持：Advance(d time.Duration) error 推进本地时钟，非正数报 ErrBadAdvance；推进时按当前频偏折算补偿，逼近目标后把偏移钳到目标并清掉频偏。Advance 在保持模式下照常推进。超过 HoldoverAfter 没有收到有效样本就进入保持模式，Holdover() bool 与 Degraded() bool 同时为真，本地时间继续按当前频偏自由推进；一旦有新样本被接受，两个标志立刻恢复。

快照与并发：Snapshot() ([]byte, error) 与 Restore([]byte) error 覆盖本地位置、当前偏移、目标偏移、频偏、步进计数、拒绝计数与样本窗口，快照里带窗口大小、步进阈值与频偏上限，恢复后按快照里的参数继续伺服，恢复瞬间的读数与快照时刻一致，快照要能被同一模块的新实例读取；损坏内容报 ErrBadSnapshot 且不动现网。多路采集协程会同时推进时钟与读取时间，Advance 与 Now 交替执行时读数只能单调不减，快照不能拍到半更新状态。

验收口径：go test ./... 的 15 条用例全过，测试文件不得修改、跳过或按固定输入写死；并发读数与时间轴回退自行验证；只做本地实现，交付源码与运行记录。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-c-13`
- 实现文件：`media_clock_slave.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过模块路径导入实现
- 主要类型：`MediaClockSlave`
