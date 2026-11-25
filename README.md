# batch49-go-game-c-06

模块路径 `gsb/batch49-go-game-c-06`，包名 `task`，实现文件 `live_caption_stabilizer.go`，类型 `LiveCaptionStabilizer`。

## 题面

直播同传上线第二周，字幕组的回放抽查几乎每次都能挑出问题：识别引擎每六十毫秒推一次同一段的部分结果，放映端把这些结果当成新行往下追加，一句话被拆成四五行的碎片；已经定稿的句子还会被迟到的旧结果改回草稿，观众看到的字幕顺序来回跳，后排观众只能放弃读字幕。字幕组每场都要手工删掉重复行，排查时又发现延迟被临时调大，字幕跟不上口型，观众干脆关掉字幕。请在模块 gsb/batch49-go-game-c-06 中补全 live_caption_stabilizer.go 里的 LiveCaptionStabilizer，由它把引擎的部分结果整理成可上屏的滚动字幕。

部分结果由 SegID、序号、文本、起始时刻与消息时刻组成，同一段在识别周期内会被反复推送。公开接口：NewLiveCaptionStabilizer(cfg CaptionConfig) *LiveCaptionStabilizer、Update(p Partial) error、Finalize(segID string) error、ForceFinalize(cutoff time.Duration) error、Rolling() []Line、SetCorrectionWindow(d time.Duration) error、Pending() []Line、Stats() CaptionStats、Reset() error、Snapshot() ([]byte, error)、Restore(data []byte) error。

需要落实的行为：1）部分结果按 SegID 原地替换，同一段只占一行，不得追加新行；2）只有序号严格大于已应用序号的同段结果可以应用，乱序到达的旧序号整条丢弃并计入 DroppedStale；3）段首次上屏的消息时刻记为 Shown，超过 CorrectionWindow 后再来的同段结果不得改动文本，计入 LateEdits 并冻结该行；4）定稿后同段再来的部分结果要丢弃并计入 DroppedFinal，不能当成新段追加；5）修正窗口内替换只改文本，行在 Rolling 里的显示位置不变；6）ForceFinalize(cutoff) 要冻结所有 Shown 不晚于 cutoff 的未定稿段，即使它们仍在修正窗口内也要一并冻结，不能只冻结最老的一段；7）Rolling 按首次上屏顺序返回，最多保留 MaxLines 行，超出淘汰最老的行并在计数里留痕；8）空 SegID 或序号小于 1 的 Partial 返回 ErrBadPartial 且不改动状态；9）Finalize 未知段落返回 ErrUnknownSegment，重复定稿不得重复计数；10）在屏段落总数受 MaxSegments 限制，超限返回 ErrSegmentLimit；11）SetCorrectionWindow 拒绝负数窗口并返回 ErrInvalidWindow，配置里为 0 的字段取包内默认值。

硬约束：Update、Finalize 与 ForceFinalize 会被多路识别协程同时调用，同一段落的替换与定稿必须串行生效，Rolling 与 Stats 只能读到完整状态，不能出现文本已换而序号未换的中间态；快照要覆盖修正窗口、每段序号与冻结标记，Restore 之后继续 Update 时序号与显示顺序必须和中断前衔接，损坏快照返回 ErrBadSnapshot 且不得改动现有状态。并发与恢复需要自行压测，异常路径不得吞错，切换窗口与恢复状态期间不能出现半条字幕。

验收：go test ./... 的 15 条用例全部通过，测试文件不得修改、跳过或按固定输入写死；只做本地实现。交付源码、运行记录与简要说明，并发与恢复要给出可复核的命令与结果。

## 运行

```bash
cd batch49-go-game-c-06
go test ./...
```
