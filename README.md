# 补丁分批推进的服务分层与 canary 门槛

## 题面

月度补丁窗口里，值班同学按平台给的批次表往下推，第一批还没打完，第二排服务的机器先自行开始重启，理由是不清楚哪批该等哪批：平台把依赖关系随手按服务名字典序断开，真正的上下游被打散。canary 环节同样失控：同一批里 canary 机器还没回传结果，其余机器已经全量开打，等到失败率算出来时补丁已经铺满整个机群；被隔离的机器第二天又被当成“已完成”计入成功率，报表显示百分之百，实际还有一半没打。跨零点的维护窗口最容易被算错：二十二点半到次日一点半的窗口被拆成两天各算一次停机预算，机器在窗口外被重启，业务中断记到了错误的日子。落点 gsb/batch49-go-safety-c-04 的 patch_rollout_batches.go，类型 PatchRollout，签名保持：NewPatchRollout(policy RolloutPolicy) *PatchRollout、Plan(hosts []Host, deps []DepEdge) ([]Batch, error)、Batches() []Batch、Batch(batchID string) (Batch, bool)、Start(batchID string) error、BeginHost(hostID string, minuteOfDay int) error、ReportHost(hostID string, result HostResult) error、Gate(batchID string) (GateResult, error)、Abort(batchID, reason string) error、HostStatus(hostID string) (string, bool)、Quarantined() []string、Snapshot() ([]byte, error)、Restore(data []byte) error。

要修：Plan 校验主机编号唯一、服务名非空、依赖两端都存在、维护窗口合法，空计划报 ErrEmptyPlan，其余不合格报 ErrBadPlan；服务按依赖分层，被依赖的先进批次，同层可并行，层内服务与主机按编号升序，批次编号按 batch-1、batch-2 递增，每批记下它直接依赖的上游服务；依赖成环时必须在计划阶段拒绝并指出环上的服务，不得按字典序随便断开一条边。Start 只在上一批 gate 通过后放行，提前开启报 ErrDepsPending，重复开启报 ErrBatchStarted，未排计划报 ErrNoPlan，未知批次报 ErrUnknownBatch，已中止报 ErrBatchAborted。BeginHost 按主机的维护窗口判断，窗口起点含、终点不含，窗口外报 ErrOutsideWindow；窗口跨午夜时按本地时区连续计算，不得拆成两天各算一次预算。ReportHost 只能报活动批次里的主机，批次未开报 ErrBatchNotActive，未知主机报 ErrUnknownHost，重复上报报 ErrHostReported，超出单机停机预算报 ErrOverBudget 且不记录结果；有 canary 的批次里，canary 未全部回传前普通主机上报报 ErrCanaryPending。Gate 先等 canary 回齐，未齐时在结果里给出已报数、总数、canary 等待数与原因；canary 失败率超过策略阈值时判不通过，失败的机器进隔离区，该批不放量，下游批次保持等待；通过后该批完成，下游才能开启。隔离区主机在后续批次中必须跳过但保留占位，不得被当成已完成计入成功率；隔离区列表按编号升序返回，主机状态取 pending、active、ok、failed、quarantined、aborted，gate 原因取 GatePassed、GateWaiting、GateIncomplete、GateFailed。Abort 必须带原因，空原因报 ErrBadReason，中止后该批不可再开、不可再上报。快照要保留计划、各批状态、主机结果与隔离区，恢复后继续推进；损坏快照报 ErrBadSnapshot 且不动现网。

硬约束：同一主机并发上报只能有一份被接受，其余报 ErrHostReported，批次状态推进必须原子；快照要么全量旧状态要么全量新状态；单批放量受 canary 门槛与停机预算双重钳制，不得越线放行。

验收：go test ./... 15 条全过，不得改测试、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-c-04`
- 实现文件：`patch_rollout_batches.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`PatchRollout`
