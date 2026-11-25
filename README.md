# batch49-go-safety-d-02

模块路径 `gsb/batch49-go-safety-d-02`，包名 `task`，实现文件 `acl_baseline_reconciler.go`。

## 题面

安全组每月初对受管目录做一次 ACL 基线巡检，上个月一口气报出四千多条偏差，运维按清单改了两天，第二周复查又冒出同一批路径：有的挂载点只读，改不动却被记成修复失败反复重试；有的基线里写了通配主体，展开后的条目数对不上，被误判成缺失；还有临时授权已经到期、工单豁免却还没到期，先被判成过度授权还原，隔天又原样加回来。审计要求下一次巡检必须能说清每条偏差为什么出现、修到了哪一步、哪些根本不该修，而不是再给一张改不完的清单。请把这套对账做成可续跑、可解释的服务：模块路径 gsb/batch49-go-safety-d-02，文件名 acl_baseline_reconciler.go，包名 task，核心类型 AclBaselineReconciler，构造签名为 NewAclBaselineReconciler(tree Tree, l Limits, now func() time.Time)。

对外接口如下，签名固定：
Scan(root string, baseline Baseline) ([]Drift, error)；
ScanShard(root string, baseline Baseline, budget int) (Shard, error)；
Classify(d Drift) DriftKind；
Plan(drifts []Drift) ([]FixStep, error)；
ApplyStep(step FixStep) error；
Exempt(path string, until time.Time, ticket string) error；
Exemptions() []Exemption；
AppliedSteps() int；
Digest() (string, error)；
Generation() uint64；
Stats() ScanStats。

需要落实的行为：

1. Scan 每次只取一份快照并把 generation 固定下来，按路径升序输出偏差，每条偏差都带上本轮快照的 generation；根路径为空报 ErrBlankRoot，基线版本为空报 ErrBadBaseline。
2. 主体权限按集合比较：实际多出的位归 Extra、类别 over_grant；缺少的位归 Missing、类别 missing；同一路径两类同时成立时先报 over_grant 再报 missing。
3. 十万级条目要能分片续跑：ScanShard 每次最多处理 budget 个文件，返回 Scanned、Remaining、Done、Drifts、Generation，Done 为真时 Drifts 是整轮累计结果，budget 非正报 ErrBadBudget。
4. 文件是否变化以内容摘要判断，不看 mtime；内容摘要与上一轮相同且基线版本未变时直接复用上一轮结论，Stats.Cached 记录复用条数。
5. Exempt(path, until, ticket) 必须带工单号和不早于当前时刻的到期时间，缺工单报 ErrBlankTicket、时刻已过报 ErrExemptPast；豁免期内该路径不计入偏差，到期后自动重新计入。
6. 只读挂载上的路径根本无法修复，要归入 DriftUnfixable，不要混进重试队列；Classify 对其余四类原样返回类别。
7. 基线里出现通配主体时，展开后的条目数量与实际不一定相等，必须按集合比较而不是逐条对齐。
8. 临时授权到期与豁免到期落在同一天时以豁免优先，豁免过期后偏差重新出现。
9. Plan 按目录深度升序分批，批次号按深度升序从 0 开始，同一深度共用一个批次号，步骤先 Remove 后 Add，每步带稳定幂等键；偏差数或步数超过上限报 ErrDriftLimit 或 ErrStepLimit，不截断。
10. ApplyStep 幂等：同一步重复执行返回 nil 且 AppliedSteps 不变；步骤的 generation 与最近一次扫描不一致时报 ErrStaleGeneration 且不落地。
11. Digest 返回最近一轮偏差的 SHA-256 小写十六进制，共 64 个字符，与输入顺序和 map 遍历无关。

硬约束：扫描与修复并发时只能读同一份 generation 的快照；每步幂等可重放；分片续跑不重复不遗漏；文件数与偏差数都是硬上限。

验收口径：go test ./... 15 条全部通过，不得修改测试、跳过或用固定输入写死；并发与重启场景自行验证；只做本地实现，不接外部系统。

## 运行

```
go test ./...
```

## 说明

- 仅使用 Go 标准库。
- 测试位于 `tests/test_acl_baseline_reconciler_test.go`。
