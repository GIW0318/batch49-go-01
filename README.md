# batch49-go-game-a-09

## 题面

自选卡池上线两周，玩家发现把目标换成另一把武器后水位直接清零，连续出目标的人反而攒出一笔负水位，强制出目标的那一抽还把普通保底一起清了。运营复盘还发现，切换目标时折算把水位直接顶到阈值，下一抽立刻又触发强制；同一玩家连点抽卡时，两个请求同时读到旧水位，结算后少算了一次。请把 gsb/batch49-go-game-a-09 的 target_pity_tracker.go 中 TargetPityTracker 补齐，模块内只允许使用标准库。

公开接口：NewTargetPityTracker(Options) *TargetPityTracker、Threshold() int、Ratio() (int, int)、SelectTarget(player, pool, target string) error、OnRoll(player, pool, hit string) (PityState, error)、SwitchTarget(player, pool, target string) (PityState, error)、Watermark(player, pool string) int、NormalPity(player, pool string) int、Forced(player, pool string) bool、Target(player, pool string) string、Pools(player string) []string、Conversions(player, pool string) int、Snapshot() ([]byte, error)、Restore([]byte) error。

需要落实的行为：
1. Options 为零值时阈值 80、折算比例 1/2；只填部分字段时，未填字段按默认值补齐。
2. SelectTarget 要求玩家、卡池、目标都非空，否则 ErrInvalidTarget；首次选定把水位与普通保底置零，并在内存里建立该玩家该卡池的状态。
3. 同一卡池再次 SelectTarget 只换目标不清水位，等价于不折算的直接替换，也不写折算记录；需要折算时走 SwitchTarget。
4. 未选目标前 OnRoll 返回 ErrNoTarget，不改变任何计数；未知玩家与未知卡池同样返回 ErrNoTarget。
5. 每次未命中目标水位加一、普通保底加一；命中目标时水位与普通保底同时清零；命中判定按传入 hit 与当前目标逐字比较；连续命中目标时水位保持零，不出现负值，也不预支后续未命中次数，清零后再次未命中从 1 开始。
6. 水位达到阈值的那一抽强制出目标：返回的 PityState.Forced 为真、水位清零，本次传入的 hit 不按普通命中处理，返回值里的 Target 仍是当前目标。
7. 强制出目标后普通保底计数保持强制前的数值，强制这一抽既不增加也不清零普通保底；普通保底只由自然未命中与自然命中改变。
8. 强制之后的下一抽从水位零重新累计，Forced 恢复为假；强制标记只反映最近一次结算。
9. SwitchTarget 按 Options 比例把旧水位折算为新水位并向下取整；折算结果不得超过原水位，也不得达到阈值，最高只能到阈值减一；折算后的水位仍是整数。
10. 每次 SwitchTarget 记录一条折算（玩家、卡池、新旧目标、折算前后水位），记录按发生顺序保存，Conversions 返回条数，快照保留这些记录。
11. Snapshot/Restore 往返保留目标、水位、普通保底、折算记录与最近一次强制标记，恢复后 Conversions 与快照前一致。

硬约束：同玩家同池的 OnRoll、SwitchTarget、SelectTarget 会被多协程并发调用，结算必须串行且最终计数与串行执行一致；水位只增不减，只有命中或强制触发才清零；十万玩家规模下单次结算不得遍历全部玩家。

验收：go test ./... 的 15 条全部通过，不得修改测试、跳过用例或按固定输入写死；并发场景自行验证。

## 运行方式

在模块根目录执行：

```
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-a-09`
- 包名：`task`
- 主文件：`target_pity_tracker.go`
- 测试目录：`tests/test_target_pity_tracker_test.go`
