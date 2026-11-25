# 网络策略代际切换

## 题面

策略下发系统切到新版本后，值班同学遇到三件怪事：按发布单全量切换后，部分工作负载的放行结果和旧代完全一样，差分却显示“无变化”；回滚按钮点一次回到上一代，再点一次没有落在更早的稳定代，反而把线上规则弄成两代混合；还有在切换瞬间发起的求值请求，前半个规则来自旧代、后半个来自新代。复盘时还发现，差分报表把选择器文本相同的两代判为无变化，而工作负载标签在前一晚刚被发布系统改过；回滚接口没有记录已经回滚过，连续点击会一路退到更早的代际。相关代码在模块 gsb/batch49-go-safety-b-17 的 netpol_generation_switch.go，类型 PolicyGeneration，签名保持：NewPolicyGeneration(Limits) *PolicyGeneration、SetWorkload(workload string, labels map[string]string) error、Stage(policies []Policy) (Generation, error)、Diff(cur, next Generation) (Diff, error)、Commit(gen Generation) error、Rollback() error、Active() Generation、BeginEvaluation(workload string, port int) (EvalToken, error)、FinishEvaluation(tok EvalToken) (Decision, error)、Snapshot() ([]byte, error)、Restore([]byte) error。

要修的行为：

1. 代际号从 1 起严格递增，Stage 只入历史不改线上；History 按代际升序，已被裁剪的历史代际不得被 Commit。
2. 策略名空白报 ErrBlankPolicyName，动作只允许 allow 与 deny 否则报 ErrBadAction，端口限 1 到 65535 否则报 ErrBadPort。
3. 每次 Stage 按当时的标签表把策略选择器解析成“工作负载到命中策略名集合”的归属并固化；之后标签再变不影响已生成代际，Diff 以各代固化的归属为准。
4. Diff 按工作负载分桶比较两代归属：空到非空记 Added，非空到空记 Removed，非空且命中策略名集合变化记 Changed，三组都按工作负载名升序，两代都为空的工作负载不进入任何一组，并且不得因为策略文本变化但归属相同而虚报变更，差分结果带上起止代际号。
5. Commit 只接受历史中已存在的代际，否则报 ErrUnknownGeneration；重复提交同一代报 ErrAlreadyActive，提交不得改变历史顺序。
6. 提交后保留上一代作为唯一回滚目标；Rollback 只能回上一代，且只允许一次，第二次必须报 ErrNoRollback，不得跳到更早代际。
7. 提交前已经开始、提交后才结束的求值必须整份按开始时的代际规则算完，不得混用两代。
8. 求值按优先级最高者生效，同优先级按策略名升序取第一个；没有命中任何策略时返回 deny 与 RuleDefaultDeny，空端口不得被当成通配；决策回填工作负载、端口、代际、动作与命中策略名。
9. 未知工作负载 BeginEvaluation 报 ErrUnknownWorkload；端口非法报 ErrBadPort。
10. 标签表变更、策略数量与工作负载数量受 Limits 约束，超限报 ErrTooManyPolicies 或 ErrGenerationLimit，拒绝时不得留下半份标签。
11. 并发 Commit 同一代只能有一个成功，其余报 ErrAlreadyActive；快照与状态变更互斥。
12. Snapshot 覆盖标签、历史代际、当前代际与回滚状态；Restore 整份生效或整份拒绝，坏负载报 ErrBadSnapshot 且线上代际不变，未提交的暂存代不得被激活。

硬约束：提交与回滚串行裁决，任意时刻只有一个 active 代；快照只反映全量旧状态或全量新状态；重启恢复后继续沿用代际水位，已用掉的一次回滚机会不能被重置。

验收：go test ./... 15 条全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-b-17`
- 实现文件：`netpol_generation_switch.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`PolicyGeneration`
