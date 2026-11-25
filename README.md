# batch49-go-game-a-07

## 题面

周末副本活动上线后，玩家在同一只 Boss 的战斗掉落里连拿两把同组武器，首杀奖励在首杀标记清掉后还在发，运营手动补的表根本盖不住基础表。复盘时还发现，同名条目只换掉了权重，数量区间和互斥组还是基础层的老值；某个互斥组被调成全零权重后，系统又从别的层借来条目凑数，掉落件数一下翻倍。请补齐 gsb/batch49-go-game-a-07 的 layered_drop_table.go 中的 LayeredDropTable，模块内只允许使用标准库。

公开接口：NewLayeredDropTable(Options) *LayeredDropTable、AddLayer(id string, cond LayerCond, entries []Entry) error、RemoveLayer(id string) error、LayerIDs() []string、Effective(ctx Context) (Table, error)、EntryCount(ctx Context) (int, error)、TotalWeight(ctx Context) (int, error)、Roll(ctx Context, seed uint64) (Drop, error)、Explain(ctx Context, entryID string) (int, error)、Groups(ctx Context) map[string][]string、Snapshot() ([]byte, error)、Restore([]byte) error。

需要落实的行为：
1. 层按加入顺序叠加；后加入层的同名条目整条覆盖先前的条目，权重、数量区间与互斥组一起替换，位置保持首次出现处；覆盖后有效表里同名条目只出现一次，新条目追加在末尾。
2. 基础层永远生效；难度层只在上下文难度与声明难度一致时生效；首杀层只在首杀标记为真时整层生效，标记转回假时整层立即失效，不能残留任何加成；条件种类只接受 base、difficulty、firstkill 三种。
3. AddLayer 拒绝重复层 ID（ErrLayerExists）、未知条件种类（ErrInvalidCond）、负权重（ErrInvalidWeight）、空条目 ID 或 MaxCount 小于 MinCount（ErrInvalidEntry）。
4. Effective 返回的条目顺序稳定：先按首次出现顺序，再追加后续层的新条目；TotalWeight 为有效条目权重之和；条目按值拷贝，调用方事后改传入切片不影响已装载的层。
5. 权重在装载时归一为非负整数；零权重条目仍保留在有效表里，但抽取时按零权重处理。
6. 掉落按互斥组分区：每个组至多抽一件，未分组条目合成一个默认槽抽一件；默认槽与各互斥组独立抽取，互不影响；互斥组内权重全部为零时整组跳过，不得退回其他层借用权重。
7. 同一上下文与同一种子多次调用 Roll 结果逐字段一致，不依赖 map 遍历顺序；每个槽的抽取把组名与种子一起参与计算，层顺序变化不改变已有层的结果。
8. 抽取结果的 EntryID 必须来自有效表，数量落在条目的 MinCount 到 MaxCount 之间，一次抽取里同一 EntryID 最多出现一次。
9. 每个互斥组在一次 Roll 里最多出现一件，默认槽也最多一件。
10. Explain 返回条目在有效表里的权重，未知条目返回 ErrEntryNotFound；Groups 返回组名到条目 ID 的映射，未分组条目归到空串组。
11. RemoveLayer 删除层后立即反映到有效表，未知层 ID 返回 ErrLayerNotFound；LayerIDs 按加入顺序返回，删除不改变其他层顺序。
12. Snapshot/Restore 往返保留层 ID、条件、条目与加入顺序，恢复后 Roll 的逐字段结果与快照前一致。

硬约束：装载与抽取会并发发生，Roll 只能看到完整的旧表或完整的新表，不得读到只叠了一半的表；任何一次 Roll 的条目数受组数与默认槽限制，不随层数增长而爆炸；只做本地实现。

验收：go test ./... 的 15 条全部通过，不得修改测试、跳过用例或按固定输入写死；装载与抽取并发自行验证。

## 运行方式

在模块根目录执行：

```
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-a-07`
- 包名：`task`
- 主文件：`layered_drop_table.go`
- 测试目录：`tests/test_layered_drop_table_test.go`
