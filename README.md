# batch49-go-safety-d-05

模块路径 `gsb/batch49-go-safety-d-05`，包名 `task`，实现文件 `throttle_event_ledger.go`。

## 题面

上个月有台机器整晚被节流，监控面板却一直显示健康：cpu.stat 里的 nr_throttled 明明在涨，采集脚本把计数器回退当成异常样本丢掉了，中间重启过一次，后面几小时的增量全没算进去；另一个租户把 cpu.max 从两核压到半核，节流计数一点没动，看板照样打绿，直到业务方投诉才有人发现配额根本没写进去。请把节流与内存事件的增量采集收口成一个可查询、可解释的账本：模块路径 gsb/batch49-go-safety-d-05，文件名 throttle_event_ledger.go，包名 task，核心类型 ThrottleEventLedger，构造签名为 NewThrottleEventLedger(l Limits)。

对外接口如下，签名固定：
Observe(id string, s Sample, at time.Time) (Delta, error)；
Window(id string, from, to time.Time) (Usage, error)；
Drift(id string) []Drift；
SetLimit(id string, q Quota) error；
Limit(id string) (Quota, bool)；
Top(n int) []Rank；
Peak(id string) (Usage, error)；
Forget(id string) error；
Samples(id string) int；
Snapshot() ([]byte, error)；
Restore(data []byte) error。

需要落实的行为：

1. Observe 的首个样本只建立基线，返回全零 Delta；同一租户的下一个样本按计数器差给出 Delta，时间戳成为该区间的结束时刻。
2. 计数器回退按重启处理：重设基线，Delta 等于当前的完整计数器值，Restarted 为真，并记录一条 DriftCounterReset。
3. 回退后的下一次增量必须从新基线算起，不能累加出负数，也不能把重启前的值再算一遍。
4. 同一样本重复提交只计一次：时间戳与计数完全相同返回零 Delta 且不新增区间；时间戳相同但计数不同报 ErrOutOfOrder。
5. 时间戳早于上一样本报 ErrOutOfOrder；租户为空报 ErrBlankTenant；租户数超过 MaxTenants 报 ErrTenantLimit。
6. 采样间隔跨越计数器重置点时，区间增量要拆成重置前和重置后两段分别记账，不能整体丢弃。
7. Window(id, from, to) 按区间结束时间落在 [from, to) 求和；from 不早于 to 报 ErrEmptyRange；起点早于保留覆盖起点报 ErrOutOfRange；未知租户报 ErrUnknownTenant。
8. 每个租户保留的区间数不超过 MaxSamplesPerTenant，超出淘汰最旧的区间；Samples 返回该租户当前保留的区间数。
9. SetLimit 只对已存在的租户生效，CPUMax 与 MemMax 都必须为正，否则报 ErrInvalidQuota；Limit 返回已存配额以及是否存在。
10. 限额调低之后节流计数没有上升，要上报 DriftLimitIneffective，不能判为健康。
11. 同一个 cgroup 删除后重建，同名新组不得继承旧组的高水位；Forget 负责清空该租户的样本、区间、漂移与配额。
12. Top 按保留窗口内累计节流时长降序、再按节流次数降序、最后按 id 升序排列，n 非正返回空；Peak 返回单区间各字段的最大值；快照恢复后区间、漂移、配额必须一致，坏快照报 ErrBadSnapshot。

硬约束：采集与查询并发时只能读到完整窗口，不能读到写了一半的区间；保留上限是硬上限，淘汰只淘汰最旧；重复样本幂等；重启或恢复后基线不得回退。

验收口径：go test ./... 15 条全部通过，不得修改测试、跳过或用固定输入写死；并发与重启场景自行验证；只做本地实现，不接外部系统。

## 运行

```
go test ./...
```

## 说明

- 仅使用 Go 标准库。
- 测试位于 `tests/test_throttle_event_ledger_test.go`。
