# 规则包分层继承与有效阈值求交

## 题面

安全规则平台把扫描策略拆成产品级、区域级、租户级三层包下发，这周连着出了三起事故。某租户在子包里把扫描线程从四十提到两百，平台一路放行，第二天核心库的连接池被打满；另一家租户声明只允许 fast 模式，真正下发的有效策略里却还留着 full；运维又反馈同一个包连续解析两次，网关八台机器算出的摘要各不相同，灰度窗口里各判各的。

落点在 gsb/batch49-go-safety-e-01 的 rule_scope_resolver.go，类型 RuleScopeResolver，签名保持不变：NewRuleScopeResolver() *RuleScopeResolver、Define(pkg Package) error、Resolve(name string) (Effective, error)、Override(name, key string, v Value) error、Explain(name, key string) []Layer、Digest(name string) (string, error)、Chain(name string) ([]string, error)、Keys(name string) ([]string, error)、Match(name, pattern string) ([]Match, error)、Snapshot() ([]byte, error)、Restore([]byte) error。包用 Package{Name, Parent, Values} 声明，值用 Value{Kind, Num, Bound, Bool, Set} 表达；数值必须带 Bound，max 表示越小越严，min 表示越大越严。

解析模型：每个包只声明自己这一层的约束，未声明的键沿父链向上找最近的声明层；祖先链在 Define 时就要固定下来，之后不再变化。有效值必须能解释来源，Explain 的每一条都要带包名、键、取值和动作，便于值班同学一眼看出是哪一层放的水。

要修的具体故障与边界：
一、包名为空、父包不存在、同一包名重复声明，分别报 ErrBadPackage、ErrUnknownPackage、ErrDuplicatePackage，被拒绝的声明不得留下半条记录。
二、解析沿祖先链从根到叶逐层收窄：数值取更严的一端，枚举取交集，布尔只允许从开到关。
三、子包试图放宽父包约束，必须在 Override 阶段就报 ErrWiden，错误信息里带上被违反的层名。
四、收窄方向的覆盖要成功，并且下一次解析立刻看到新值，不能只改声明不改有效值。
五、父层是数值、子层给布尔这类类型冲突报 ErrKindConflict；覆盖未声明的键报 ErrUnknownKey，覆盖不存在的包报 ErrUnknownPackage。
六、中间层没有声明某个键时，有效值以最近一次声明为准逐层收窄，不能跳过中间层直接继承更上层那个更宽的值。
七、同一键在父子两层都是枚举时，交集为空要按冲突报错，不能悄悄退回父层取值。
八、Chain 返回根到叶的包名链，Keys 返回解析后的全部键并按字典序排列；Explain 按根到叶给出每一层的记录，动作分别是 declare、inherit、narrow。
九、字段路径支持段内通配，Match 结果按键字典序返回并携带每层来源；通配匹配不到任何字段属于配置错误，不能当成无约束放行。
十、Digest 是解析结果的稳定摘要，取 SHA-256 的小写十六进制，同一包连续解析两次逐字节一致。

硬约束：并发解析只读快照，八个 goroutine 同时解析同一包得到的摘要必须一致；Snapshot 与解析互斥，快照里要么全是旧状态要么全是新状态；Restore 遇到损坏数据报 ErrBadSnapshot 且不动当前状态，恢复后解析结果与快照前一致。

验收：go test ./... 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-e-01`
- 实现文件：`rule_scope_resolver.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`RuleScopeResolver`
