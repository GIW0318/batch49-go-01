# batch49-go-game-c-07

模块路径 `gsb/batch49-go-game-c-07`，包名 `task`，实现文件 `llhls_part_planner.go`，类型 `LlhlsPartPlanner`。

## 题面

晚高峰的体育直播每隔几秒就有一条预加载请求失败：客户端按清单提示去拉下一片，返回的却是 404；分段末尾那片偏短的尾片在下一个分段开始后被重新改写，已经缓存过该片的播放器读到时长对不上，画面卡在最后一帧；服务端明明没有声明等待点，播放器却按阻塞重载去请求，整场直播停在半路；回滚版本后旧清单仍指向已经被清理的片，客户端连拉三次都失败。运维临时把窗口调大到十个分段，问题只是从每几秒一次变成每两分钟一次。请补全模块 gsb/batch49-go-game-c-07 中 llhls_part_planner.go 的 LlhlsPartPlanner，由它维护分段与部分分段的滑动窗口，并对外输出清单与预加载提示。

清单版本固定为 6，目标时长等于当前部分分段目标，起始序号取窗口内最早的分段号。公开接口：NewLlhlsPartPlanner(cfg PlannerConfig) *LlhlsPartPlanner、PublishPart(seg uint64, part PartInfo) error、CompleteSegment(seg uint64) error、Playlist() (Manifest, error)、SetWindow(n int) error、SetPartTarget(d time.Duration) error、Preload() []PreloadHint、RemoveBefore(seg uint64) error、Parts(seg uint64) []PartEntry、Stats() PlannerStats、Snapshot() ([]byte, error)、Restore(data []byte) error。

需要落实的行为：1）部分分段时长不得超过 PartTarget，超过或非正返回 ErrPartDuration；2）偏短的最后一片允许发布，一旦发布即冻结，后续分段到来不得回填改写；3）同一分段内序号必须从 0 起连续递增，重复序号返回 ErrDuplicatePart，跳号返回 ErrPartOrder；4）每段第一片必须声明独立片，否则 ErrNotIndependent，非首片声明独立返回 ErrBadIndependent；5）每个分段的部分数受 MaxPartsPerSeg 硬顶，超限返回 ErrTooManyParts；6）已完整的分段不再接收新片，返回 ErrSegmentClosed，对没有片的分段调用 CompleteSegment 返回 ErrEmptySegment；7）窗口只保留最后 WindowSegments 个分段，过期分段整体移除，被移除的分段不再接受新的部分，起始序号单调不回退；8）预加载提示指向最新分段的下一个部分，类型为 PART，URI 由最新片 URI 的末尾序号加一得到；过期移除时提示要一并清理，不能指向已移除的部分；9）配置声明了 HoldBack 才输出阻塞重载参数，参数包含当前部分分段目标与 HoldBack，没有声明时只能给普通清单；10）SetWindow 只接受 1 到 16，SetPartTarget 只接受正数，否则分别返回 ErrInvalidWindow 与 ErrInvalidPartTarget；11）分段序号只允许前进，迟到且已经滑出窗口的分段返回 ErrOutOfOrder。

硬约束：PublishPart、CompleteSegment 与 Playlist 会被打包线程和多个读协程并发调用，清单必须是某一时刻的完整视图，不能把新旧窗口拼在一起，也不能出现清单里有行而分段表已删的情况；快照要保存窗口、关闭标记和计数器，Restore 之后继续发布与取清单要和中断前衔接，损坏快照返回 ErrBadSnapshot 且不动现有状态。并发和重启恢复自行构造验证，清单渲染期间不得读取到被回收的分段。

验收：go test ./... 的 15 条用例全部通过，测试文件不得修改、跳过或按固定输入写死；只做本地实现。交付源码、运行记录和简要说明，并发与恢复的验证记录要能对上命令和结果。

## 运行

```bash
cd batch49-go-game-c-07
go test ./...
```
