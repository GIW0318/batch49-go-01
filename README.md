# 签名策略变更的影响面重算与按命名空间分批生效

## 题面

签名策略改版评审卡住了：运维想放宽算法白名单救回一批老制品，安全团队担心这等于给所有历史镜像开口子；上一次改版是直接全量生效的，结果某个命名空间里的在途校验一半按新策略、一半按旧策略，回滚时还漏撤了两个命名空间。请落在 gsb/batch49-go-safety-e-14 的 signing_policy_change_guard.go，把 SigningPolicyChangeGuard 做成先算影响面、再审批、最后按命名空间分批生效的守卫。

守卫持有基线 Policy（版本号、是否强制签名、允许算法列表、最小密钥位数）与全部存量 Artifact（ID、命名空间、是否已签、算法、密钥位数、签名时间、过期时间）。关键签名：NewSigningPolicyChangeGuard(base Policy, artifacts []Artifact) *SigningPolicyChangeGuard、Stage(next Policy) (Impact, error)、Approve(change string, token Approval) error、Activate(change, scope string) error、Rollback(change string) error、Effective(namespace string) (Policy, error)、ImpactOf(change string) (Impact, error)、Evaluate(id string) (Decision, error)、ActiveScopes(change string) []string、Approved(change string) []string、Snapshot() ([]byte, error) 与 Restore([]byte) error。

要修的地方：

一、Stage 的 next.Version 必须是当前最新版本加一，否则报 ErrBadVersion；同一版本不得重复登记（ErrVersionExists）。

二、Stage 用存量签名在旧策略与新策略下各判一次，把由拒转允的制品写进 Relaxations、由允转拒的写进 Restrictions，两个列表都按制品 ID 升序，并回填 FromVersion 与 ToVersion。

三、变更 ID 由版本决定，形如 policy-v2；Stage 只登记不生效，生效范围与当前策略都不动。

四、存量签名已过期或制品已下线时，只按现存有效签名重算影响面，不能把无签名当作由拒转拒。

五、由拒转允属于放宽，Activate 之前必须有审批记录，否则报 ErrNotApproved。

六、Approve 校验令牌与变更绑定：变更不存在报 ErrUnknownChange，令牌指向别的变更或审批人为空报 ErrBadToken，令牌已过期报 ErrTokenExpired；重复提交同一审批人的令牌视为同一条记录。

七、Activate 把变更在一个命名空间内生效，命名空间必须至少有一个存量制品，否则报 ErrUnknownScope；同一命名空间重复激活要保持幂等，不能重复登记生效范围。

八、Effective 返回某命名空间当前生效的策略：未激活的命名空间必须回落到基线策略，已激活的按该变更的版本返回；未知命名空间报 ErrUnknownScope。

九、Rollback 把该变更激活过的命名空间逐条撤回上一版本并清空生效范围，重复回滚报 ErrNotActive；回滚后此前批准的变更记录仍要保留，审批令牌不得复用于新变更。

十、ImpactOf 返回 Stage 时算好的影响面，未知变更报 ErrUnknownChange；Evaluate 按制品所在命名空间的生效策略判定，回落与激活两种情形都要正确。

十一、Evaluate 的判定口径：未签名直接拒；强制签名但制品未签拒；算法不在白名单拒；密钥位数低于下限拒；签名已过期按拒绝处理。

十二、Stage、Approve、Activate、Rollback 之间串行化，激活期间的求值按开始时的策略版本钉扎，不能读到一半旧一半新；Snapshot 与 Restore 要保住策略版本、审批记录与生效范围，损坏载荷报 ErrBadSnapshot 且不动现网。

验收口径：go test ./... 15 条必须全过；不得修改测试、跳过用例或按固定输入写死；并发与恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-e-14`
- 实现文件：`signing_policy_change_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`SigningPolicyChangeGuard`
