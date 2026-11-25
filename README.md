# SCIM 组图同步

## 题面

上游目录连续三次同步之后，权限系统里出现了两件怪事：一个只在子组里的人拿到了父组才有的后台入口，另一个组的成员被同步进程卡在半新半旧的状态，人工比对才发现组套组被写成了环。同步作业本身没有报错，日志里全是成功，只有权限侧的人发现入口不对，客服按人查权限已经查不出来源，只能把整个租户的组结构导出来手工核对。改动落在 gsb/batch49-go-safety-a-03 的 scim_group_graph_sync.go，类型 ScimGroupGraphSync。成员引用统一写成 类型:ID 的形式，只认 user: 与 group: 两种前缀，组引用必须指向已经登记的组。关键签名保持：NewScimGroupGraphSync(limits Limits) *ScimGroupGraphSync、UpsertGroup(tenantID string, g Group) (Group, error)、Group(tenantID, groupID string) (Group, bool)、DeleteGroup(tenantID, groupID string) error、SetMembers(tenantID, groupID string, refs []Ref) (Diff, error)、AddMember(tenantID, groupID, ref string) error、RemoveMember(tenantID, groupID, ref string) error、Members(tenantID, groupID string) ([]Ref, error)、EffectiveMembers(tenantID, userID string) ([]string, error)、Depth(tenantID, groupID string) (int, error)、Snapshot() ([]byte, error)、Restore([]byte) error。

要修的点：一、租户号为空、组 ID 只有空白，UpsertGroup 分别报 ErrBlankTenant 与 ErrBlankGroupID，不落半条记录。二、新建组版本从 1 起，再次 Upsert 同名组版本加一并保留已有成员与差异水位；Group 取不到返回 false，删除不存在的组报 ErrGroupNotFound。三、单租户组数受 MaxGroupsPerTenant 限制，超出报 ErrGroupLimit，别的租户照常可写。四、SetMembers 按集合算差异，Diff 给出 Added、Removed、Kept 与版本号，重复引用只算一次，旧成员没被写到的保持不动，目标组不存在报 ErrGroupNotFound，差异要能直接喂给下游审计，Added 与 Removed 里不得混进没变过的成员。五、单组直接成员数受 MaxMembersPerGroup 限制，上限按直接成员算、嵌套组算一个成员，超出报 ErrMemberLimit，被拒的整批不落到组上，原成员原样保留。六、AddMember 的引用必须是 user: 或 group: 前缀，别的写法报 ErrBadRef；已经存在的成员报 ErrDuplicateMember；组引用指向未登记的组、或目标组本身不存在，报 ErrGroupNotFound。七、RemoveMember 删不存在的成员报 ErrMemberNotFound；Members 按类型再按 ID 升序返回，计数与实际条目一致。八、组套组必须无环，新增边会成环时报 ErrCycle，并且整条写入拒绝、已有边保持不变，不能先加后拆再报错。九、嵌套深度以叶子为 1 计算，组套组不得超过 MaxDepth，超出报 ErrDepthExceeded 并指出超限路径，不得截断到上限继续写入，被拒的边一条都不落。十、删除父组后子组保留，子组的有效成员计算不得把已删父组的成员继续算进来；删除只解除引用，不删除被引用的组。十一、EffectiveMembers 要把嵌套展开，用户直接所在的组与所有祖先组都算进去，结果按组 ID 升序，同一个人通过两条嵌套路径进入同一个祖组只算一次，跨租户不得串。十二、损坏快照 Restore 报 ErrBadSnapshot 且不动现网。

硬约束：同一组的并发写入必须以互斥或 channel 串行化，多条并发加成员不能丢写、不能重复存，读侧不得读到半条边；单组直接成员数、嵌套深度与租户组数都是硬上限，先写后清不算达标。Snapshot 要覆盖组、成员、版本水位与差异水位，Restore 整份替换，恢复后 EffectiveMembers 与 Depth 的结果和中断前一致。验收以 go test ./... 的 15 条用例全绿为准，测试不许改、不许跳过、不许按固定输入写死；并发与重启恢复自行验证；只做本地实现，交付源码、跑测记录和一页以内说明。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-a-03`
- 实现文件：`scim_group_graph_sync.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`ScimGroupGraphSync`
