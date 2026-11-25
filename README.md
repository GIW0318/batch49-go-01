# patch_rollback_planner

模块路径：`gsb/batch49-go-safety-e-08`
包名：`task`
实现文件：`patch_rollback_planner.go`
测试目录：`tests/`

## 题面

<!-- PROMPT-START -->
补丁批次回滚演练变成了一次单向操作：某批安全补丁上了十二台机器，回滚脚本从后往前跑，跑到数据迁移步骤卡住，前面的主机已经退不回去，后面还有哪些步骤没退、退到哪一层，没人能说清；另一个批次里两个步骤都依赖同一前置却同时应用，回滚顺序每次都不一样，复核时无法解释。上一次事故复盘时只能从终端日志里拼时间线，谁在哪个窗口执行到第几步全靠口头回忆，审计要求的下钻证据拿不出来。补丁管理平台已经把批次、步骤和窗口都存下来了，缺的是可靠的拆解与执行记录。请修复 gsb/batch49-go-safety-e-08 下的 patch_rollback_planner.go，把 PatchRollbackPlanner 的拆解、可行性与窗口判定补完整。

关键接口：
NewPatchRollbackPlanner(opts Options) *PatchRollbackPlanner
RegisterBatch(b Batch) error
Plan(batch Batch) (RollbackPlan, error)
Feasible(plan RollbackPlan) (bool, []Blocker)
Execute(plan RollbackPlan, upto int) (Result, error)
Window(batch string) (time.Time, error)
MarkIrreversible(step string, reason string) error
Steps(batch string) ([]Step, error)
Status(batch string) (Result, bool)
Reversible(step string) bool

需要落实的行为：
1. 批次号、步骤号、不可逆原因为空要拒绝；同一批次重复登记要报错；批次内步骤号重复也要拒绝。
2. 依赖缺失返回 missing dependency，依赖成环返回 cycle，不能死循环也不能静默丢步骤。
3. 回滚顺序是应用顺序的逆序，跨主机步骤先按依赖分层再逆序，而不是按登记时间或提交时间倒排；同一台主机上的步骤也要先满足依赖，不能因为主机相同就合并。
4. 同一层内按应用序号倒序；两个步骤依赖同一前置且应用时并行、层内次序不唯一，必须先按依赖分层，再在层内定序。
5. 命中不可逆步骤时只能回滚到它之前，Reversible 记录可回滚前缀长度，Blockers 逐条列出不可逆步骤及原因。
6. 数据迁移步骤即使附带反向脚本也按不可逆处理，除非显式登记为可逆，不能凭脚本存在就放行。
7. Feasible 只在没有任何 blocker 时返回真；存在不可逆步骤、缺失依赖或成环都必须返回假。
8. Window 返回批次登记时给出的截止时间；未知批次返回 unknown batch。
9. 截止时间之后只允许出报告不允许执行，Execute 要返回 window closed 且不留下任何执行进度，报告里保留截止时间与拒绝原因。
10. 回滚数量为负或超过计划长度要拒绝；跨过不可逆边界要拒绝；同一批次只能执行一次，重复执行返回 already executed。
11. 中途失败时停止后续步骤，已完成步骤不可再次回滚，失败步骤与剩余步骤状态要逐条列出，不能只报整体失败。
12. 多个请求同时查询窗口与状态时，不能看到半截计划或串批次的结果。

验收：go test ./... 的 15 条必须全过，不许改测试、跳过或按固定输入写死；不可逆前缀与分层顺序要各自验证；窗口、并发与重复执行自行验证；只做本地实现。
<!-- PROMPT-END -->

## 运行

```bash
go test ./...
```
