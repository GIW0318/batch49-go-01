# 租户出站配额台账

## 题面

出站网关的租户配额台账连续两天对不上：某租户只跑了六十兆，台账却记成一百兆，原因是请求整体失败后预留没有全额退回；新出现的对象存储域名按未知类目走了很小的探索配额，运营把它归入已知类目后，历史探索用量被清零，当月额度凭空多出来；最严重的一次，租户被熔断后健康检查也一起被拒，控制面完全失联。账单侧还发现预留票据在进程重启后全部丢失，已预留的额度没人归还；熔断恢复时间写死成一小时，运营无法按租户调整。相关代码在模块 gsb/batch49-go-safety-b-19 的 egress_quota_ledger.go，类型 EgressQuotaLedger，签名保持：NewEgressQuotaLedger(Limits) *EgressQuotaLedger、Check(tenant string, dest Dest) (Verdict, error)、Reserve(tenant string, est int64) (Ticket, error)、Settle(t Ticket, used int64) error、Release(t Ticket) error、Usage(tenant string) Usage、Breaker(tenant string) BreakerState、Samples(tenant string) []string、Snapshot() ([]byte, error)、Restore([]byte) error。

要修的行为：

1. 租户为空报 ErrBlankTenant，目的地主机为空报 ErrBlankDestination，Kind 只允许 data、management、audit，其它值报 ErrBadCategory。
2. 已知类目走 KnownQuota，未知类目走更小的 ExploreQuota，未知主机要留存样本，样本按首次出现顺序去重，超过 MaxSamples 丢弃最旧记录。
3. Check 是只读预判：返回是否放行、类目、剩余额度与原因；放行时原因为 allowed，熔断时数据面原因为 breaker_open，剩余额度等于对应配额减去已用与预留；额度已被预留占满时返回 quota_exceeded 且剩余为零，Check 不得创建预留或改变已用额度。
4. Reserve 只接受正数估值，否则报 ErrBadEstimate；超出剩余额度报 ErrQuotaExceeded，且不得先占后拒，拒绝时 Reserved 保持不变。
5. 票据编号单调递增，预留计入 Reserved 但不计入已用；票据记录租户、类目与预留额，编号水位跨重启保留；结算前失败整单 Release，必须全额归还。
6. Settle 按实际用量记账：少于预留的差额归还，超出预留的补记；结算或归还后票据作废，再次使用报 ErrUnknownTicket，同一票据不得被结算两次。
7. 实际用量为负报 ErrBadUsage，且不得改动任何计数。
8. 归还和结算都不得把窗口用量减成负数，重复归还要报 ErrUnknownTicket 而不是继续扣减。
9. 连续拒绝达到 RejectThreshold 后开启整租户熔断，当次仍返回配额错误，下一次起数据面请求报 ErrBreakerOpen；Breaker 暴露连续次数、开启时间与恢复时间，恢复后连续次数清零。
10. 熔断期间健康检查、审计上报等管理通道仍然放行，不消耗数据配额，也不计入拒绝次数，但仍要校验租户与主机非空。
11. 未知类目被样本聚类为已知类目后，历史探索用量保留不清零，之后同一主机按已知类目计价，已结算账单不回写。
12. Snapshot 覆盖窗口用量、票据、票据编号水位、样本顺序、类目与熔断状态；Restore 整份生效或整份拒绝，坏负载报 ErrBadSnapshot 且现网台账不变。

硬约束：Check、Reserve、Settle、Release 并发下串行裁决，二十个并发预留不能超发额度；窗口按租户隔离，A 租户的拒绝不能触发 B 租户熔断；快照与变更互斥；重启恢复后窗口用量、预留、票据编号与熔断计数继续沿用。

验收：go test ./... 15 条全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-b-19`
- 实现文件：`egress_quota_ledger.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`EgressQuotaLedger`
