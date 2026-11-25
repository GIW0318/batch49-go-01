# batch49-go-game-a-26

模块路径：`gsb/batch49-go-game-a-26`
源文件：`craft_outcome_roller.go`
包名：`task`
测试目录：`tests/`（十五个用例）

## 题面

跨服锻造活动上线后，同一份配方在帮会驻地和主城锻造台算出的成败对不上，失败返材的账目也乱：有人的材料多返了半份，有人的材料干脆没进背包，重登后品质记录还会整批消失。运营按配方维度对账时，同一玩家连做十次的成功次数与后台统计能差出两三次，客服只能逐单补发。问题落在 gsb/batch49-go-game-a-26 的 craft_outcome_roller.go，请补齐 CraftOutcomeRoller 的结算行为。

接口：NewCraftOutcomeRoller(Options) *CraftOutcomeRoller；SetQualityTiers([]QualityTier) error；QualityTiers() []QualityTier；SetRecipe(RecipeSpec) error；Recipe(string) (RecipeSpec, bool)；Recipes() []RecipeSpec；SuccessPermille(CraftRequest) (int, error)；Roll(CraftRequest, uint64) (Outcome, error)；Batch([]CraftRequest, uint64) ([]Outcome, error)；Refund(Outcome) map[string]int；History() []Outcome；Snapshot() ([]byte, error)；Restore([]byte) error。

需要落地的规则共十一条：（1）品质档位至少三档，最低档下限必须为零，各档下限严格递增、名称非空且互不重复，非法档位表整份拒绝并保留原表。（2）配方标识非空，材料至少一项、编号不重复、数量为正，基础成功率与失败返还比例都落在一千以内，熟练度加成不得为负；非法配方拒绝且不覆盖同名旧配方。（3）单次成功率等于基础值与熟练度乘加成之和，再按 MaxSuccessPermille 封顶，构造时该上限缺省取九百五十。（4）查询未知配方一律报错，配方为空、玩家为空或熟练度为负的请求直接判为非法。（5）同一请求搭配同一 seed，成功标记、两段掷值与品质必须逐字段一致，不受调用次数和历史影响。（6）成功掷值小于本次成功率才算成功；成功率封顶后仍要完成品质掷值，不能当作必成而跳过品质判定。（7）品质掷值落在零到九百九十九，成功后取不大于该掷值的最高档，边界归入高档；失败时品质留空。（8）失败返还按每种材料单独折算并向下取整，余数不累积到下一次也不向上取整。（9）成功与品质两段掷值必须各自派生，不能复用同一段掷值，否则高品质会跟着高成功率走。（10）Batch 按入参顺序结算，任一请求非法则整批报错，不得只落一部分；History 只保留最近若干条并返回副本。（11）Snapshot 保存档位、配方与历史，Restore 要么整体替换要么原样不动，非法载荷报错，快照与返还表都不得与内部状态共享底层数组。

硬约束：Roll、Batch、SetRecipe 与 Snapshot 并发调用时只能看到完整的档位表和配方表，不得出现半更新映射；进程重启后从快照恢复，历史条数、配方字段、档位顺序都要逐字段还原。

验收：go test ./... 十五条全部通过；不得修改测试、跳过用例或按固定请求写死结果；并发与重启后的行为自行补充验证；只在本地实现，不接入外部服务。

## 运行

```bash
go test ./...
```

只需 Go 1.23 与标准库。
