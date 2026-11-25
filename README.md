# batch49-go-game-a-10

## 题面

团本里治疗量突然翻倍，奶妈一口把满血坦克刷出三层护盾，持续治疗在禁疗结束后一次性补了五跳，战斗日志的数值对不上。复盘还发现，过量治疗折算护盾时用了四舍五入，护盾比配置上限多出一截；同一目标两个治疗法术同时落地，生命值互相覆盖，少加了一大截。请把 gsb/batch49-go-game-a-10 的 healing_settlement.go 中 HealingSettlement 补齐，模块内只允许使用标准库。

公开接口：NewHealingSettlement(Options) *HealingSettlement、OverhealShieldPct() int、MaxShield() int、MaxHoTs() int、SetHealth(target string, hp, maxHP int) error、Health(target string) int、ApplyHeal(req HealRequest) (HealResult, error)、AddHoT(target string, hot HoT) error、HoTs(target string) int、TickHoT(target string, at int64) ([]HealTick, error)、Shield(target string) ShieldState、ExpireShields(target string, at int64) error、SetNoHeal(target string, until int64) error、Snapshot() ([]byte, error)、Restore([]byte) error。

需要落实的行为：
1. Options 为零值时过量转护盾比例 50%、护盾上限 10000、持续治疗条目上限 4、护盾时长 10000 毫秒；只填部分字段时逐项补默认值。
2. SetHealth 校验 hp 在 0 到 maxHP 之间、maxHP 为正，否则 ErrInvalidHealth；Health 返回当前生命；SetHealth 可重复调用，用于重置目标生命与上限。
3. ApplyHeal 先按 ReductionPct 削减：effective = Amount × (100 - ReductionPct) / 100，整数向下取整；ReductionPct 越界或 Amount 为负返回 ErrInvalidHeal。
4. NoHeal 为真时本次有效治疗为 0，不产生过量治疗，也不产生护盾，不消耗生命上限也不动已有护盾。
5. 有效治疗先补生命，超过生命上限的部分为过量治疗；Applied 为实际补进生命的数值，Applied 与 Overheal 之和等于有效治疗量。
6. 过量治疗按比例转护盾并向下取整，护盾总量不得超过 MaxShield；护盾已有余量时先补到上限，超出部分直接丢弃；护盾从本次结算时刻起计时到期。
7. 未知目标 ApplyHeal、AddHoT、TickHoT 一律返回 ErrUnknownTarget，不得隐式建状态，三类入口行为一致。
8. AddHoT 校验每跳数值、跳数与间隔为正，条目数达到上限返回 ErrHoTFull；急速为负按 0 处理，间隔仍不得小于 1 毫秒。
9. 每跳间隔按施加时的急速快照计算：IntervalMs × 100 / (100 + HastePct)，向下取整且最小 1 毫秒；挂上之后急速再变不影响已挂效果，间隔在 AddHoT 时算好并固定下来。
10. TickHoT 按传入时刻一次性结算所有到期的跳数，每跳受生命上限约束并返回实际治疗量；一次调用可跨多个跳点，返回顺序按时间升序；跳数耗尽的持续治疗立即移除。
11. 禁疗窗口内到期的持续治疗照常消耗跳数与时长，但不产生治疗，也不攒到窗口结束后补发；禁疗窗口只影响治疗量，不影响跳数消耗。
12. ExpireShields 在到期时刻清空护盾，未到期保持原值；Snapshot/Restore 往返保留生命、护盾、持续治疗与折算余数，恢复后 TickHoT 的下一跳时刻与快照前一致。

硬约束：同一目标的 ApplyHeal、TickHoT、ExpireShields 并发调用必须串行，护盾与生命不能出现中间态；护盾、持续治疗条目与单次治疗量都有硬上限，超出按上限处理而不是丢弃整次结算；快照必须原子生成。

验收：go test ./... 的 15 条全部通过，不得修改测试、跳过用例或按固定输入写死；并发场景自行验证。

## 运行方式

在模块根目录执行：

```
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-a-10`
- 包名：`task`
- 主文件：`healing_settlement.go`
- 测试目录：`tests/test_healing_settlement_test.go`
