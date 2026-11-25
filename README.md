# 录制中断后的分片扫描与索引重建

## 题面

直播间断流之后，录制目录里留下几段分片，运营点回放只能看到前面几十秒，后面的时长全是黑的，重新点一次修复又从头扫一遍，修复记录还越积越多。落点 gsb/batch49-go-game-c-15 的 recording_recovery.go，需要把 RecordingRecovery 补齐。

扫描：NewRecordingRecovery(cfg Config) *RecordingRecovery 用 MaxSegments 与 MaxFillMs 界定一次修复；Scan(path string) (ScanCursor, error) 在目录里找出所有 .seg 分片，按文件名升序排好，返回扫描数、总字节与最后一段的名字，路径不存在或不是目录报 ErrScanRoot，目录里的非 .seg 文件一律忽略、不计入扫描数与字节数；空游标（Root 为空）交给 Rebuild 或 Resume 一律报 ErrBadCursor。分片是逐行文本，每行三段：pts_ms、dur_ms 与轨道标记（v、a、va），录制进程被杀时最后一行常常没有换行，这种未结束的尾行整行丢弃并计入 DiscardedFrames，分片标记 Truncated。

重建：Rebuild(cur ScanCursor) (Report, error) 从目录头部重新拼出可播索引，分片数超过 MaxSegments 报 ErrTooManySegments，不静默截断；每个分片的时长按末帧 pts 加 dur 减去首帧 pts 计算，TotalMs 等于各分片时长之和加补齐的间隙；相邻分片首帧晚于上一段结束时按上一段结束时间补齐并累加 FilledMs 与 Repaired，间隙超过 MaxFillMs 报 ErrGapTooLarge，TotalMs 与 FilledMs 都以毫秒计、Repaired 统计补齐发生的次数；遇到损坏行立即停止扫描，StopReason 记为 corrupt，已恢复部分照常返回，损坏分片里已经读出的完整帧仍算已恢复部分、损坏点之后的内容一概不再读取，损坏分片之后的分片不得出现在索引里；停止扫描后已恢复索引的时长不得包含损坏分片声明的时长。某一段只有视频帧或只有音频帧时，另一轨仍按已有帧重建并在分片信息里标记缺失轨道，不能把整份修复判失败；轨道标记不合法的行按损坏处理。

校验与恢复：Validate(report Report) error 检查 TotalMs 等于各分片时长之和加 FilledMs、FilledMs 不为负、每个分片至少有一帧，不成立报 ErrBadReport；LastReport、Names、SegmentCount 暴露最近一次结果。Snapshot() ([]byte, error) 与 Restore([]byte) error 存下最近一次报告与游标，恢复后无需重扫即可继续，损坏内容报 ErrBadSnapshot 且不动现网。修复对同一目录重复执行要幂等，两次结果逐字段一致。

并发：多个运维会同时点修复，Rebuild 与 Resume 对同一目录的结果必须一致，Names 与 SegmentCount 只能读到整份旧索引或整份新索引，不能看到半更新的分片表；Resume 从游标记录的上一段之后接着处理，已经修复过的分片不重扫、不重复计入时长，目录里没有更新分片时报 ErrNothingToResume。

验收口径：go test ./... 的 15 条用例全过，测试文件不得修改、跳过或按固定输入写死；并发修复与续跑自行验证；只做本地实现，交付源码与运行记录。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-c-15`
- 实现文件：`recording_recovery.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过模块路径导入实现
- 主要类型：`RecordingRecovery`
