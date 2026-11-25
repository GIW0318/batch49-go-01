# batch49-go-safety-d-07

模块路径 `gsb/batch49-go-safety-d-07`，源码文件 `seccomp_profile_lattice.go`，包名 `task`，主类型 `SeccompProfileLattice`。

## 题面

集群上线后同一份运行时策略在两类节点上表现不一致：一类节点容器一启动就收到 SIGSYS，另一类节点的策略被工作负载声明悄悄放宽，放行了本该拦下的挂载调用。两类节点的基线其实一样，差别只在声明的写法与所在架构，重启之后同一份声明又会换一种结果。落点是 gsb/batch49-go-safety-d-07 的 seccomp_profile_lattice.go，类型为 SeccompProfileLattice。基线与工作负载声明都写成画像，两份画像合成的结果才是生效画像，声明只能收窄不能放宽。这次要一次把两份画像的合成与跨架构翻译做到位。

公开签名（不得改动）：
NewSeccompProfileLattice() *SeccompProfileLattice
Compose(base, overlay Profile) (Profile, error)
Narrow(prev, next Profile) (Diff, error)
Allowed(p Profile, name string) (bool, error)
Translate(p Profile, arch string) (Profile, error)
DefaultAction(p Profile) (Action, error)
Validate(p Profile) error
Canonical(p Profile) (string, error)
Names(p Profile) ([]string, error)
Lookup(p Profile, name string) (Rule, bool, error)

逐条要落地：
1) 合成取两侧允许集的交集；声明里出现基线没有的调用名算放宽，报 ErrWidenAttempt 并让整份合成失败。
2) 参数条件按同一参数下标求交：两边同值 eq 保留，eq 与 ne 撞在同一取值上属于冲突，报 ErrArgConflict。
3) 两组参数条件交不出结果时整份合成失败，不允许把该调用退化成无条件放行。
4) 默认动作只能往更严方向走，严格程度是 allow 小于 errno 小于 trap 小于 kill；声明比基线更松时取基线一侧，更严时取声明一侧。
5) 同一份输入无论规则顺序如何重复合成，Canonical 输出逐字节一致，规则按调用名升序拼接，不依赖 map 遍历顺序。
6) Validate 要拦下空允许集 ErrEmptyAllowSet、未知默认动作 ErrBadDefaultAction、重复调用名 ErrDuplicateRule、空调用名 ErrBadRuleName、非法参数操作符 ErrBadArgOp。
7) Allowed 只查允许集，空调用名报 ErrBadRuleName；Names 升序返回；Lookup 返回命中规则与该架构上的编号。
8) Translate 按名称映射到目标架构编号：amd64 上 read、write、openat、personality 分别是 0、1、257、135，arm64 上 read、write、openat 分别是 63、64、56，personality 在 arm64 没有对应编号。
9) 目标架构缺少某个调用名时要报未知调用 ErrUnknownSyscall 并拒绝整份画像，不能静默丢掉那一条。
10) Narrow 只允许收窄：next 少了调用名记 Removed 并返回 nil，next 多了调用名记 Added 并报 ErrWidenAttempt。
11) 默认动作从 errno 改成 kill 属于收窄、反向属于放宽，Diff 要如实给出 DefaultChanged 与新旧动作，判定不能只看允许集大小。
12) 合成与校验只读入参，不写共享状态；画像架构为空时沿用基线架构；Translate 只认 amd64 与 arm64，其余报 ErrUnknownArch。

硬约束：同一份基线加声明三十二路并发合成，Canonical 必须全部一致；合成与 Narrow 是幂等的，同一输入跑两遍输出逐字节相同；单份画像规则条数上限为四千零九十六条，超限按校验失败处理，不允许截断后继续合成。参数过滤器在同一下标上要保持稳定顺序，Canonical 里同一调用的条件按参数下标升序排列；校验失败要给出具体错误值，不能一律返回同一个错误。

验收：go test ./... 的 15 条用例全过，测试不许改、不许跳过、不许按固定输入写死；并发合成与重复合成自行验证；只做本地实现，交付源码、跑测记录与改动说明。

## 运行方式

在模块根目录执行：

```bash
go test ./...
```

测试位于 `tests/test_seccomp_profile_lattice_test.go`，属同一个模块，用例通过 `impl "gsb/batch49-go-safety-d-07"` 引用实现。
评测口径为 15 条用例全部通过，测试文件不得修改、跳过或按固定输入写死。
