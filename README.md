# 扫描任务按维护窗口与资产分组的错峰排期

## 题面

扫描排期这周被投诉三次：夜里的全量扫描在维护窗口开始前就派了出去，把生产库带崩；同一台主机上并发跑了七个任务，端口探测全被目标侧限流；被限流的任务退避回来以后插到了队尾，排在三天后才提交的任务后面，跨窗口中断的任务又从零重扫了一遍。

落点在 gsb/batch49-go-safety-e-03 的 scan_window_planner.go，类型 ScanWindowPlanner，签名保持：NewScanWindowPlanner(limits Limits) *ScanWindowPlanner、Declare(group string, w Window) error、Submit(t Task) (string, error)、Next(now time.Time) (Task, bool)、Report(id string, r Outcome) error、InWindow(group string, at time.Time) (bool, error)、Pending(group string) int、Groups() []string、State(id string) (TaskState, error)、Queue(group string) []string、Snapshot() ([]byte, error)、Restore([]byte) error。窗口用 Window{Zone, Days, Start, End} 声明，Zone 是 IANA 时区名，Days 是允许的星期，Start 与 End 是相对当地零点的偏移；任务用 Task{ID, Group, Target, Progress}，回报用 Outcome{Kind, RetryAfter, Progress, At}，At 是执行侧观察到结果的那一刻。

排期模型：每个任务只有一条记录，状态在排队、执行、退避、完成、挂起之间迁移，任何时刻同一任务最多占一个并发位；组与窗口在声明时固定，任务提交之后不再改窗口。派发只发生在 Next 被调用的那一刻，执行侧拿到的任务里带着已经完成的进度，便于续跑。

要修的具体故障与边界：
一、窗口非法（Start 不小于 End、时区名无效、Days 为空）报 ErrBadWindow，同一分组重复声明报 ErrDuplicateGroup。
二、提交到未声明的分组报 ErrUnknownGroup；任务 ID 为空报 ErrBadTask，ID 重复报 ErrDuplicateTask，被拒绝的任务不得入队。
三、窗口外一律不派发，Next 在窗口外返回 false；窗口按声明的时区判定当地日期与时刻，不看服务器本地时间。
四、同一目标同时进行的任务数不得超过配置的每目标并发上限，其它目标不受牵连。
五、同一分组内严格按提交顺序派发，先提交先派发，跨分组不重排。
六、任务回报成功后出队，并发位立即释放，Pending 相应减一。
七、目标返回限流信号时按 RetryAfter 退避，退避期内不派发；退避结束后仍按原队列位置派发，不得插到队尾。
八、连续限流超过上限的任务转人工挂起并释放并发位，Pending 不再计入它。
九、同一资产同时属于两个分组且窗口不重叠时按更严的窗口判定，并在归属解释里说明取的是哪一组。
十、跨窗口中断的任务按最近一次回报的 Progress 续跑，不能从零重来。
十一、退避中的任务再次被回报成功时，以最新一次结果出队，不能按旧的失败结论把它挂在退避里。
十二、Snapshot 与调度互斥，Restore 之后 Pending 与队列顺序必须一致，损坏数据报 ErrBadSnapshot 且不动现网。

硬约束：Next 与 Report 之间对队列、并发闸门、退避时间的读写必须串行裁决，同一时刻不得把同一任务发出去两次；所有判定只依赖传入的时刻，不读墙上时钟，因此同一串调用在不同机器上结果一致。

验收：go test ./... 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-e-03`
- 实现文件：`scan_window_planner.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`ScanWindowPlanner`
