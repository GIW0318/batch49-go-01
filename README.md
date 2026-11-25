# 成就点数账本、分类封顶与历史补算

## 题面

赛季结算后，客服陆续收到反馈：同一名玩家在个人页、公会页和排行榜上看到的成就点数总额对不上，完成了整套系列成就的玩家没有拿到系列奖励，运营补录新的成就定义之后，那些在定义到达之前就触发过解锁的账号点数没有补上，反而有几个账号因为重复点开成就详情被多记了一笔。落点是 gsb/batch49-go-game-a-21 的 achievement_points_ledger.go，账本类型 PointsLedger，对外入口保持 NewPointsLedger(Options) *PointsLedger、DefineAchievement(Achievement) error、Unlock(account, achievementID string, at int64) (PointDelta, error)、Points(account string) PointsBreakdown、Recalc(account string) (PointDelta, error)、GrantedBonus(account string) []string、CategoryPoints(account, category string) int64、Unlocked(account string) []string、UndefineAchievement(id string) error，以及 Snapshot() ([]byte, error) 与 Restore([]byte) error。账本要同时服务结算批处理和在线解锁两条链路，两边读到的总额必须一致。

要修的行为有这些：成就 id、分类、点数任一为空或非正必须拒绝，重复定义同一 id 报错；解锁尚未定义的成就先记入待补算集合，返回零增量且不报错，定义到达后由补算补发，待补算的成就也要出现在解锁列表里；同一账号重复解锁同一成就只算一次，第二次返回零增量而不是报错；点数按分类累计，每个分类有各自硬上限，到顶之后继续解锁不能让该分类继续增长，超出部分单独记账；系列成就全部解锁时只发一次系列加成，加成不计入任何分类，也不会因为重复解锁再发；不同账号的点数与加成互不可见，查询未知账号返回空明细；成就定义到达后补算只补已解锁但尚未计分的差额，重复补算不再加；补算不得因为定义调整削减已发点数，只有成就被删除才允许扣回，扣回一次性完成，后续补算不得重复扣；成就解锁列表按 id 升序返回，同一账号的读取与写入不能互相穿插；写入串行，并发解锁同一账号不丢点数，不同账号互不阻塞。

硬约束：同一账号的解锁与补算串行执行，任意并发下总点数等于逐条累加结果；快照要保存成就定义、每账号解锁集合、分类点数、溢出与系列加成记录，恢复后重复解锁仍然幂等；损坏快照恢复必须报错且保持现网账本不变。运营还要求同一份快照在旧账本上恢复后，历史点数、系列加成和解锁集合逐项对得上，客服能据此解释每一笔点数的来源。结算批处理会在低峰时段串行跑全量补算，调用方可能来自多个协程，账本不能因为一次补算失败就丢掉已经发出的点数；读接口返回的明细也不能暴露内部切片，调用方改动返回值不得影响后续查询。

验收口径：go test ./... 15 条用例全过，测试不得修改、跳过，也不能按固定输入把期望值写死；并发解锁与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-a-21`
- 实现文件：`achievement_points_ledger.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`PointsLedger`
