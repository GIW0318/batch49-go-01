# 回溯录制的环形缓冲与片段导出

## 题面

直播切条功能上线后，观众导出的精彩片段要么黑屏半秒，要么起点比拖动条早了七八秒，运营复盘时还发现暂停期间缓冲里多出十几兆没有被淘汰。落点 gsb/batch49-go-game-c-12 的 replay_ring_buffer.go，需要把 ReplayRingBuffer 补齐。

入缓冲：NewReplayRingBuffer(opts Options) *ReplayRingBuffer 用 MaxBytes、MaxDuration、MaxGOPs 三项预算界定缓冲，三项未设置时分别按 4MiB、30 秒、8 组兜底；Push(f EncodedFrame) error 依次接收编码帧，载荷字节非正、时间戳为负或相对上一帧不递增一律报 ErrBadFrame，缓冲为空时首帧必须是关键帧否则报 ErrNoKeyframe，单帧超过 MaxBytes 报 ErrTooLarge 且不入缓冲；每个关键帧开启一个图像组，超预算时按图像组从最旧开始整体丢弃，绝不留下缺头的半组，DroppedGOPs() int 累计丢弃的组数。窗口内只保留连续可播的帧序列，丢弃最旧组之后其余帧的时间戳保持原样，Available() Span 与 FrameCount() int、ByteCount() int 三者必须自洽。

导出：Export(from, to time.Duration) (Clip, error) 的起点要回退到不晚于 from 的最近关键帧，终点取不晚于 to 的最后一帧，Clip 里的 Start 与 End 必须等于实际首末帧的时间戳；from 不小于 to、from 为负报 ErrBadRange，空缓冲报 ErrEmptyBuffer；请求区间超出当前可用范围时截断并把 Truncated 置真，回报的起点仍要落在真实关键帧上。KeyframeAtOrBefore(d time.Duration) (time.Duration, bool) 给出不晚于 d 的最近关键帧，早于缓冲起点时返回 false。导出结果按时间戳升序、保持原有编码顺序，同一区间重复导出要得到同样的帧序列。

暂停与上限：Pause() error 与 Resume() error 只冻结与恢复覆盖，采集继续，Paused() bool 反映当前状态，两个开关可重复调用且状态幂等，冻结只影响覆盖、不影响已有片段导出；冻结期间入缓冲照收，但字节上限照样要卡住，不能因为不再淘汰就放任缓冲超额；恢复后一旦超预算立刻从最旧图像组开始丢弃。导出与覆盖并发时，正在被导出引用的图像组要跳过，其它图像组照常覆盖；一旦所需图像组在导出过程中被丢掉，本次导出报 ErrOverwritten 而不是交出残缺片段。

快照：Snapshot() ([]byte, error) 与 Restore([]byte) error 覆盖帧内容、暂停标记、丢弃计数与三项预算，恢复后按快照里的预算继续淘汰，新到的帧与旧窗口按原预算衔接，快照要能被同一模块的新实例读取且不依赖内存地址；损坏内容报 ErrBadSnapshot 且不动现网。多个采集协程与导出协程会同时打进来，Push 与 Export 内部要串行一致，导出看到的窗口不能是撕裂的。

验收口径：go test ./... 的 15 条用例全过，测试文件不得修改、跳过或按固定输入写死；并发导出与暂停恢复自行验证；只做本地实现，交付源码与运行记录。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-c-12`
- 实现文件：`replay_ring_buffer.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过模块路径导入实现
- 主要类型：`ReplayRingBuffer`
