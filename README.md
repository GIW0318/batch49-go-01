# batch49-go-fix-a-13

模块路径：`gsb/batch49-go-fix-a-13`

包名：`task`

源文件：`acl_escalation_auditor.go`

测试目录：`tests/`

## 题面

上线前的权限巡检报告里，一台部署机的 setuid 备份程序被标成红色：审计员用自己的普通账号就能改写这个程序，改完再等运维触发一次备份，等于直接拿到 root。顺着目录往上翻，问题不止一处，有的目录让整组人可写，有的目录上还挂着继承授权，新建的子文件天然带写权限。安全组要求把这条链路一次性查清，并且只回收必要的那几位权限。改动落在 gsb/batch49-go-fix-a-13 的 acl_escalation_auditor.go，类型 AclEscalationAuditor。关键签名保持：NewAclEscalationAuditor(limits Limits) *AclEscalationAuditor、RegisterPrincipal(p Principal) error、AddNode(n Node) error、EffectiveAccess(principalID, path string) (Access, error)、Scan(principalID string) ([]Finding, error)、PlanRepair() (RepairPlan, error)、Apply(plan RepairPlan) (ApplyResult, error)、Verify() error、Snapshot() ([]byte, error)、Restore([]byte) error。

要处理的行为：
一、主体登记拒绝空白 ID、重复 ID 与超上限，Privileged 主体只作为排除项保留。
二、节点路径必须是绝对路径且已规范化，含双点、相对路径、结尾斜杠一律报 ErrPathInvalid；重复路径报 ErrDuplicateNode；Node 返回副本，调用方改返回值不得影响内部状态。
三、属主命中时取属主位与自己的具名条目；非属主取具名用户、命中 GID 或附加组的组条目、other 位三路并集；mask 条目只收窄具名用户与组条目；未知主体报 ErrUnknownPrincipal，未知路径报 ErrNodeNotFound。
四、属主对自己名下节点的写权限不算 ACL 提权；Privileged 主体不产生任何发现。
五、非属主主体对 setuid、setgid 或高权限可执行文件具备写权限时报 critical 的 direct_write，Chain 只含该文件本身。
六、非属主主体对高权限节点的祖先目录同时具备写与执行时报 high 的 ancestor_write，Chain 从该目录一路列到高权限叶节点。
七、归因优先级为具名用户、组、other：同一主体同一路径只出一条发现，组授权要带 ViaGroup，other 授权单独归因为 other_write；若具名与组两条路径都给出写权限，仍只出一条并按直接授权归因，但修复时两条都要回收。
八、目录上的默认继承授权按未来子项计算，只要该目录位于高权限可执行文件的路径上，默认写授权同样算提权路径。
九、Scan 传空主体表示全量扫描；发现按严重度降序、路径升序、主体升序排列，重复 Scan 的结果与 ID 必须逐条一致。
十、PlanRepair 只回收写位，覆盖该主体在该路径上的所有写来源，不碰属主条目、读位与执行位；Digest 覆盖全部主体与节点，任一节点发生变化都让旧计划报 ErrPlanStale。
十一、Apply 要么全做要么全不做；应用后复查仍有残余时报 ErrResidualEscalation 并把节点集合与授权恢复到调用前；同一计划重复 Apply 必须被拒绝且不重复回收；Verify 全量复查，零发现返回 nil，否则报 ErrResidualEscalation。
十二、Snapshot 覆盖主体、节点与序号，Restore 整份替换；损坏快照报 ErrBadSnapshot 且不动现网；并发 Scan 与 EffectiveAccess 的结果不得看到半应用状态。

硬约束：Apply 与 Scan 并发安全，计划绑定状态摘要，重复提交不产生二次扣减；快照恢复后序号继续递增，恢复前生成的计划一律失效。

验收以 go test ./... 的 15 条用例全绿为准，测试不许改、不许跳过、不许按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行

```bash
go test ./...
```
