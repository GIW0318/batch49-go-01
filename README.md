# 伤害链接分摊与反伤上限

## 题面

团本跨服战上线后，前排承伤统计和成员实际掉血长期对不上：同一发范围技能，有的成员按比例分到了伤害，有的完全没吃到；把残血成员从链接里撤下后，余数仍然算到了他身上，导致一发技能的分配总额比原始伤害多出一点。有团长反馈同一发技能的分配总量偶尔还会比战斗统计少一截，怀疑有人被重复计入。战斗日志里同一发技能的分配条数也忽多忽少，客服只能手工补账。落点在 gsb/batch49-go-game-a-11 的 damage_link_guard.go，类型 DamageLinkGuard，需要在现有签名上把行为补齐。
公开入口：NewDamageLinkGuard(Options) *DamageLinkGuard、Link(groupID string, members []string, sharePct int) error、Unlink(groupID, member string) error、Settle(hit Hit) ([]Share, error)、Reflect(source, target string, amount int64, at int64) (int64, error)、TickReset(at int64)、GroupMembers(groupID string) []string、SharePct(groupID string) (int, error)、SetHealth(member string, hp int64) error、Health(member string) int64、ReflectedTotal(target string) int64、Snapshot() ([]byte, error)、Restore([]byte) error。
要落到的行为：1) Link 保留成员加入顺序，比例只接受 1 到 100，组数、成员数、重复成员与空 id 都要拒绝；2) Settle 先按比例切出链接额度，余下的直伤只落在被命中的目标身上；3) 链接额度按成员均分，整数除法余数按加入顺序补足，链接份额与直伤合计必须等于原始伤害；4) 输出顺序跟随加入顺序，直伤份额带 Direct 标记排在最后，份额为零的成员不出现在结果里；5) 成员血量随分到的份额扣减，扣到零的成员不再参与后续分摊，未单独设置血量的成员按默认血量处理，血量不低于零；6) 分摊过程中有人血量归零时，剩余额度要在存活成员之间重新归一，不能继续压到已经倒下的人身上；7) 余数只补发给仍在组内的成员，已经离组的人不能分到；8) Unlink 后成员立刻退出，重复移除和未知组各自报错，且不改动其余成员；9) Reflect 只把实际承伤回敬给来源，回敬额度不能超过本次实际承伤，单 tick 上限按来源分别累计，不同来源各算各的；10) 反伤不得再次进入链接，也不得再触发一次反伤，ReflectedTotal 按目标累计；11) TickReset 清空上一 tick 的计数，快照保存组、成员顺序、比例、血量与反伤计数，恢复后行为连续；12) 组数与成员数都是硬上限，超限拒绝而不是静默丢弃，零值配置按默认组数、成员数与 tick 长度处理，成员加入与离组都按调用顺序生效。
硬约束：同一组的 Settle 与 Unlink 必须串行，任何时刻都不能看到半份成员表；快照是全量状态，恢复要么整体成功要么保持原状，恢复后的第一次结算与重启前完全一致；反伤上限是资源上限，不能靠截断总额绕过；并发场景与重启恢复请自行构造验证，覆盖同组结算与离组交错、两个来源同时反伤两种情形。
验收：go test ./... 的 15 条用例全过，测试不得修改、跳过或按固定输入写死；只做本地实现，交付源码与测试记录。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-a-11`
- 实现文件：`damage_link_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`DamageLinkGuard`
