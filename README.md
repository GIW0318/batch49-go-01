# 材质变体异步编译与帧边界热替换

## 题面

新场景第一次进场时画面会卡住一下，随后材质整体换了一遍，玩家跑图途中看到草皮闪成默认灰；编译慢的机器上还出现过同一个材质被排了两次编译，队列越积越长。复盘时发现夜间雾效那套组合被两拨系统各自请求了一次，占位材质一直顶到最后才换下来。落点 gsb/batch49-go-game-b-08 的 variant_hot_swapper.go，类型 VariantHotSwapper，公开签名保持：NewVariantHotSwapper(cfg SwapConfig) *VariantHotSwapper、Request(key VariantKey) (Future, error)、Query(key VariantKey) (MaterialHandle, State)、SetParam(key VariantKey, name string, value float64) error、Params(key VariantKey) map[string]float64、StartCompile(key VariantKey) error、CompleteCompile(key VariantKey, ok bool) error、Cancel(key VariantKey) error、AdvanceFrame() SwapReport、Generation(key VariantKey) uint64、PendingCompiles() int、Snapshot() ([]byte, error)、Restore([]byte) error。

要落实的行为：一，请求一个变体先排进编译队列并返回共享的 Future，编号首条从一计起且单调递增，Query 此时给出排队状态与代际为一的占位句柄，空变体键报 ErrBlankVariant。二，同键并发请求合并成一次编译，后来的请求拿到同一个 Future 编号，PendingCompiles 不因此增加。三，开始编译后状态转入编译中，对未跟踪的变体调用 StartCompile 报 ErrUnknownVariant。四，编译成功只把状态推进到已编译，真正的句柄替换只在帧边界发生，帧未推进前 Query 仍读旧句柄与旧代际。五，推进一帧时按每帧替换上限处理待替换项，报告里给出本帧替换数、顺延数与失败数，帧号自增。六，替换成功后句柄代际递增，Generation 返回新代际，飞行帧仍持有的旧代际在延迟期内继续可读，旧代际要到下一次帧边界之后才允许回收。七，取消只对尚未开始编译的任务生效，排队中取消成功且状态转为已取消，未知变体取消报 ErrUnknownVariant。八，编译失败在未达上限时重新排回队列并保留失败次数，达到失败上限后状态转为失败，Query 给出回退到默认变体的句柄并停止重排。九，占位期间被修改的参数必须在替换后保留，不能回退到编译发起时抓取的参数快照。十，已开始的编译即使被取消也要占住队列位直到结束，队列位在编译结束后才释放。十一，每帧替换数量超限时顺延到下一帧，顺延不得改变变体之间的相对顺序。

硬约束：同一变体的并发请求只允许产生一次编译，请求、取消与帧推进并发时状态机不能出现两个终态，-race 下不得有共享状态竞争；Snapshot 要包含每个变体的状态、代际、参数与失败次数，Restore 后继续推进帧的行为与中断前一致，损坏负载报 ErrBadSnapshot 且不动现有运行时状态。

验收：go test ./... 的 15 条用例全部通过，测试文件不得修改、跳过或按固定键值写死；同键并发请求与重启后的状态接续请自行构造场景验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-b-08`
- 实现文件：`variant_hot_swapper.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`VariantHotSwapper`、`VariantKey`、`Future`、`MaterialHandle`、`SwapConfig`、`SwapReport`、`State`
