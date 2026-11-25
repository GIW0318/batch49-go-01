# 下游应用账号撤销对账

## 题面

季度审计前，安全团队拿 IdP 名单和下游三套系统对了一遍，发现有四十多个已经离职的账号还挂在业务系统里，其中一个上周还有活跃会话；对账脚本跑到一半被重启，第二天重跑又把同一批账号重新算了一遍，宽限期从头发起，运维只能手工拦着不让它直接停号。安全团队把结论写成一句话：上游是准的，下游是滞后的，缺的是一套能断点续跑的对账。改动落在 gsb/batch49-go-safety-a-04 的 scim_deprovision_reconciler.go，类型 ScimDeprovisionReconciler。关键签名保持：NewScimDeprovisionReconciler(limits Limits) *ScimDeprovisionReconciler、SetNow(now time.Time)、SetUpstream(tenantID string, userNames []string) error、SetDownstream(tenantID, appID string, accounts []Account) error、Run(tenantID, appID string) (RunReport, error)、Plan(tenantID, appID string) ([]Action, error)、Execute(runID string) (Result, error)、Orphans(tenantID, appID string) ([]Account, error)、Accounts(tenantID, appID string) ([]Account, error)、Confirm(runID, accountID string) error、Ops(runID string) []string、Snapshot() ([]byte, error)、Restore([]byte) error。

要修的点：一、租户号或应用号为空，SetUpstream、SetDownstream、Run 都要当场报 ErrBlankTenant 或 ErrBlankApp，不产生半个轮次。二、Run 先拍上游快照再比对下游，按用户名判定，两侧名字先去首尾空格；只有下游有、上游没有的账号才进孤儿集，匹配上的账号保持 active、会话数不动，RunReport 带出上游人数、下游人数、孤儿数、轮次 ID 与游标。三、新孤儿进入宽限期，状态为 grace，宽限期截止为当前时刻加 GracePeriod，宽限期长度由 Limits 给出，不得按自然日四舍五入，Orphans 按账号 ID 升序返回，Accounts 也按 ID 升序。四、同一应用同时只允许一个对账在跑，上一轮没走到 Completed 之前再调 Run 报 ErrRunInProgress；不同应用可以并行。五、宽限期内 Confirm 报 ErrGraceNotElapsed 且状态不变；Confirm 未知轮次报 ErrUnknownRun，账号不属于该轮报 ErrUnknownAccount。六、宽限期满且二次确认后才禁用；未确认就 Execute 报 ErrNotConfirmed，不得先禁用后补确认。七、禁用顺序固定：先撤销该账号在各应用的活动会话，再停账号，停用后状态为 disabled、会话数归零，Ops 按发生顺序逐条记成 动作:账号 ID 的形式，动作名取自 Action 常量。八、Plan 只做试算，按账号 ID 升序为每个孤儿给出撤销会话与禁用两条动作，Kind 取 Action 常量，不落库；Execute 每次最多处理 MaxAccountsPerRun 个账号，处理不下的留在游标之后，游标只前进不后退，Result 带出本轮的撤销数、禁用数、游标与是否完成。九、已完成的轮次再 Execute 不重复下发，也不追加操作记录。十、宽限期内账号重新出现在上游名单时直接恢复为正常态，不得继续走向禁用；上游快照之后才新增的账号不得被本轮判为孤儿。十一、下游禁用调用失败的账号保持待处理并继续重试，不能标记为已完成。十二、损坏快照 Restore 报 ErrBadSnapshot 且不动现网。

硬约束：Run 与 Execute 的并发裁决必须串行，同一应用只放行一个轮次，一个账号不能被两个轮次同时处理；单轮处理量是硬上限，先做后判不算达标。Snapshot 要覆盖上游名单、下游账号与状态、轮次游标、确认标记与操作记录，Restore 整份替换，重启后未完成轮次从断点继续，已确认的账号不必再确认。验收以 go test ./... 的 15 条用例全绿为准，测试不许改、不许跳过、不许按固定输入写死；并发与重启续跑自行验证；只做本地实现，交付源码、跑测记录和一页以内说明。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-a-04`
- 实现文件：`scim_deprovision_reconciler.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`ScimDeprovisionReconciler`
