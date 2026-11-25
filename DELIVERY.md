# LiveCaptionStabilizer 交付说明

## 源码

- `live_caption_stabilizer.go`：实现全部公开接口、线程安全状态、统计与快照恢复。
- `live_caption_stabilizer_stress_test.go`：额外并发/恢复压测，带 `//go:build stress` 标签；普通 `go test ./...` 不增加用例数，仍为验收文件中的 15 条。
- `artifacts/verification.log`：以下命令的实际运行记录。

## 实现要点

- 使用 `segments` 映射加 `order` 首次上屏顺序，同 SegID 永远原地更新。
- `Rolling` 原子复制最后 `MaxLines` 行；`MaxSegments` 限制全部受管段落，定稿或滚出 Rolling 的段落仍可正确识别迟到/重复消息。
- 更新优先级为：参数校验、定稿丢弃、旧序号丢弃、修正窗口/冻结丢弃、原子替换文本与序号。
- `Shown` 记录该段第一次上屏时的 `Partial.Now`；窗口边界按 `Now-Shown > CorrectionWindow` 判断，相等时刻仍允许修正。
- `ForceFinalize` 遍历全部 `Shown <= cutoff` 的段落，批量冻结并标记定稿；已因窗口冻结的段落也会补记为定稿，但不重复增加 `Frozen`。
- `CaptionStats.Evicted` 是新增的只读计数字段，记录 Rolling 淘汰的累计行数；不影响原有命名字段。
- 快照为带 magic/version 和 SHA-256 校验的 JSON。恢复前完整解析和校验到局部状态，校验失败返回包装后的 `ErrBadSnapshot`，不会触碰当前状态。
- 一把 `sync.RWMutex` 保护配置、映射、顺序、统计和快照切换；`Rolling`、`Pending`、`Stats` 均返回副本或值。

## 复核命令

```bash
go test -count=1 -v ./...
go test -race -count=1 ./...
go test -tags stress -race -count=50 .
go test -race ./tests \
  -run 'TestConcurrentUpdatesAcrossSegments|TestSnapshotRestoreKeepsLinesAndCounters|TestRestoreRejectsCorruptSnapshot' \
  -count=500
```

最近一次结果：15 条验收用例全部 PASS；race 检查通过；50 轮标签压测通过；500 轮并发/快照关键用例重复执行通过。完整输出见 `artifacts/verification.log`。
