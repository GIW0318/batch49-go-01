# 防火墙策略冲突体检与默认拒绝求值

## 题面

边界防火墙换了控制器之后，同一段办公网出现了两种相反的告警：一部分本该被拦的端口被放行，另一部分是新增策略明明排在前面却从未命中；运维在控制台上加规则时也看不到任何冲突提示，两条作用域相同、动作相反的策略可以并排存在，最后谁生效全看谁先写进配置。改动落在模块 gsb/batch49-go-fix-a-08 的 fw_policy_conflict_guard.go，类型 PolicyConflictGuard 负责规则冲突体检、按优先级求值与默认拒绝兜底。对外的签名保持：NewPolicyConflictGuard(cfg GuardConfig) (*PolicyConflictGuard, error)、AddRule(rule Rule) (int, error)、RemoveRule(id int) error、Check(rule Rule) (ConflictReport, error)、Evaluate(pkt Packet) (Decision, error)、Candidates(pkt Packet) ([]int, error)、Rules() []Rule、Reorder(ids []int) error、Hits(ruleID int) uint64、DefaultDenied() uint64、ShadowCount() int、Snapshot() ([]byte, error)、Restore(data []byte) error。

要修的行为如下。

一、配置里的默认动作只能是 deny，其它取值或 MaxRules 非正报 ErrBadConfig，不得带着非法配置启动。

二、规则校验：动作只允许 allow 与 deny，协议只允许 tcp、udp、icmp、any，端口必须在 0 到 65535 之间；icmp 规则的端口必须为 0，违反一律报 ErrInvalidRule。

三、CIDR 必须写成网络地址形式，带主机位或前缀长度越界报 ErrInvalidRule；空串表示任意地址。

四、ID 为 0 时按现有最大 ID 递增分配，负数报 ErrInvalidRule，显式指定已存在的 ID 报 ErrDuplicateRuleID。

五、规则数受 MaxRules 硬顶，超限报 ErrRuleLimit 并且不落任何一条。

六、求值顺序为优先级降序、同优先级按加入先后；只看第一条命中的规则，不得继续扫描，也不得让后面的规则覆盖前面的结论。

七、没有任何规则命中时返回动作 deny、RuleID 为 0、原因为 default_deny，并累加 DefaultDenied；Evaluate 先校验报文，协议非法、端口越界或地址解析失败报 ErrBadPacket。

八、Check 要报出被更高优先级规则完全覆盖的候选规则，ShadowedBy 取覆盖它的最小规则 ID；候选规则被多条同动作的高优先级规则并集覆盖时标 Redundant，这种情况 AddRule 仍然接受。

九、作用域完全相同、动作相反且优先级相同的候选规则必须报 Contradiction，AddRule 直接拒绝并报 ErrRuleConflict。

十、动作相反但优先级不同的规则允许共存，此时高优先级规则先命中，不需要报冲突。

十一、Reorder 只接受现有规则 ID 的完整排列，缺项、重复或未知 ID 报 ErrBadOrder 且原顺序不变；重排只影响同优先级内部的先后。

十二、RemoveRule 删除规则与命中计数，删除不存在的 ID 报 ErrRuleNotFound；Hits 只统计真正命中的规则，遍历过但未命中的不计。

快照要覆盖规则集、求值顺序、每规则命中计数与默认拒绝计数；快照损坏时 Restore 报 ErrBadSnapshot，在线规则集与计数保持不变；恢复后顺序与计数原样接续，继续下发规则不得与恢复出来的 ID 冲突。

硬约束：AddRule、RemoveRule、Reorder 与 Evaluate 会被配置下发协程和转发协程并发调用，求值只能看到完整的新规则集或完整的旧规则集，不能看到半套；规则数、每规则命中计数与冗余/遮蔽计数都是硬顶，越线只能拒绝，不能静默扩容。

合格线：go test ./... 的 15 条用例一条都不能红，不得修改或跳过测试，更不能按样例写死返回值；规则下发与转发并发、重启后的顺序与计数各验一次；改动不离开本模块。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-fix-a-08`
- 实现文件：`fw_policy_conflict_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`PolicyConflictGuard`
