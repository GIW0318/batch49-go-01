# batch49-go-game-a-08

## 题面

春节期间有人一把开出三件红色装备，日志里基础掉率 1% 的东西结算成了 3%，运营配的幸运、活动与组队加成叠在一起明显偏高。复现时还发现，两个不同类别的来源被放进了同一条递减曲线，第二个类别直接被砍半；活动结束后来源已经过期，快照恢复回来却继续按旧值加概率；封顶时每个分项各自截断再相加，最终数值比硬顶还高，客服只能按日志逐条手工重算。请把 gsb/batch49-go-game-a-08 的 drop_rate_modifier.go 中 DropRateModifier 补齐，模块内只允许使用标准库。

公开接口：NewDropRateModifier(Options) *DropRateModifier、Cap() int、SetBase(itemID string, ppm int) error、Base(itemID string) (int, error)、AddSource(player, itemID, kind, sourceID string, ppm int, until int64) error、RemoveSource(player, sourceID string) error、SourceCount(player string) int、Effective(player, itemID string, at int64) (int, error)、Breakdown(player, itemID string, at int64) []Contribution、Snapshot() ([]byte, error)、Restore([]byte) error。

需要落实的行为：
1. 概率全程用百万分比整数：基础 10000 表示 1%，所有中间结果都用整数保存，禁止浮点。
2. Options.Cap 为零时硬顶取 1000000，Cap 为正时以配置为准，最终值不得超过硬顶。
3. SetBase 拒绝负值（ErrInvalidPPM），同一 itemID 后写覆盖先写；Base 返回最新基础值，未设置时返回 0。
4. AddSource 校验玩家、物品、类别、来源 ID 非空与 ppm 非负，非法分别返回 ErrInvalidSource、ErrInvalidPPM；同玩家同 sourceID 重复写入按更新处理，不新增条目；until 为 0 表示永不过期。
5. 同类来源按 sourceID 升序排队，第 k 个（k 从 0 起）按 ppm/(k+1) 计入，整数除法向下取整；排序只看 sourceID 字符串，与写入先后无关。
6. 不同类别之间先各自求和再相乘：final = base × ∏(1 + 类别和/1000000)，每一步整数除法向下取整；跨类别不得再走递减；没有来源的类别不参与连乘。
7. 到期来源在 at 大于等于 until 时剔除，但记录保留在内部与快照里；过期判定以传入的 at 为准，不读系统时钟。
8. Effective 只看当前有效来源，未知玩家或物品返回基础值，玩家或物品没有任何记录时按 0 基础值返回。
9. Breakdown 按类别名与 sourceID 升序返回全部来源（含已过期），给出原始 ppm、计入值与过期标记；过期来源的计入值必须是 0；明细条数等于该玩家该物品名下的来源总数。
10. RemoveSource 删除该玩家名下全部同名来源，未知来源返回 ErrSourceNotFound；删除后同 sourceID 可以重新加入。
11. 同一玩家的来源写入与读取串行，读取不得看到写了一半的来源集合；Snapshot/Restore 往返保留基础表、来源、到期时刻与硬顶，恢复后过期的来源仍不参与计算，硬顶与到期判定与快照前一致。

硬约束：封顶按最终值截断，不逐项截断后相加；并发读写同一玩家时结果必须落在 0 到 Cap 之间，且不得出现 panic 或部分可见的来源列表；只做本地实现。

验收：go test ./... 的 15 条全部通过，不得修改测试、跳过用例或按固定输入写死；并发场景自行验证。

## 运行方式

在模块根目录执行：

```
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-a-08`
- 包名：`task`
- 主文件：`drop_rate_modifier.go`
- 测试目录：`tests/test_drop_rate_modifier_test.go`
