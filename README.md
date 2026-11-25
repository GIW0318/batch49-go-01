# 阴影图集页内碎片整理

## 题面

夜间关卡的阴影图集跑久了会越用越碎，新瓦片明明还有空位却分不到连续区块，画面上一半灯下的物件丢了阴影。美术把图集页截出来数过，被占用的瓦片之间全是一个槽宽的小洞，分配器却只肯往后追加，页尾很快见底。落点 gsb/batch49-go-game-b-06 的 shadow_atlas_defragger.go，类型 ShadowAtlasDefragger，公开签名保持：NewShadowAtlasDefragger(pages, slotsPerPage, movesPerFrame int) *ShadowAtlasDefragger、Alloc(page int) (TileHandle, error)、Free(h TileHandle) error、Pin(h TileHandle) error、Unpin(h TileHandle) error、Slot(h TileHandle) (TileSlot, error)、HandleValid(h TileHandle) bool、Lookup(page, slot int) (TileHandle, bool)、Fragmentation(page int) float64、RequestDefrag(page int) (Plan, error)、ApplyStep(planID string) (Moved, error)、PlanActive(page int) bool、RetirePage(page int) error、Retired(page int) bool、Snapshot() ([]byte, error)、Restore([]byte) error。

要落实的行为：一，分配取该页最低的空槽，句柄代际从 1 起；页号越界报 ErrNoSuchPage，已退役页报 ErrPageRetired，槽满报 ErrPageFull。二，Free 之后旧句柄立即失效，Slot 与 HandleValid 都要反映失效，被释放的槽回到可回填集合，同一槽再次分配时代际加一；拿过期代际调 Free 报 ErrStaleHandle 且不改动槽状态。三，整理计划把未钉住的瓦片按当前槽号升序压向页首，钉住的瓦片留在原位，它占用的槽不参与目标分配，计划里每条迁移的起点与终点都要能逐条读出。四，页已经紧凑时请求整理报 ErrNotFragmented，整理进行中该页拒绝新分配并报 ErrPageBusy，别的页照常分配。五，每次 ApplyStep 最多执行 movesPerFrame 条迁移，返回本次移动数与剩余条数；计划跑完后再次推进报 ErrPlanDone，未知计划号报 ErrUnknownPlan。六，迁移完成后旧句柄失效，Lookup 按页号与槽号能取到代际加一的新句柄，本来就在页首没动的瓦片保持原句柄。七，碎片率按空闲连续区间数除以空闲槽数计算，空闲槽连成一段时只算一个区间，整页填满或空闲槽为零时记 0，整页分散出四段空洞的状态在整理完成后必须明显下降。八，页退役后进入退役表，退役页上的句柄读取按失效处理，重复退役同一页要幂等成功而不是报错。九，迁移的任何一步都要保证句柄与槽位一一对应，不允许两个句柄同时指向同一个槽，计划里的目标槽也不能落在钉住瓦片占用的槽上。十，整理完成后如果碎片率没有严格下降，整次整理要回滚，迁移过的瓦片连同句柄代际还原到整理前。十一，跨帧保留的迁移计划在新的一帧要按当时的钉住状态重新筛选，不能沿用生成计划时算好的可迁移集合。

硬约束：Pin、Unpin、Alloc、Free 与 ApplyStep 会在同一帧里被多个系统并发调用，代际推进和槽位归属必须原子，-race 下不得出现同一槽被两次占用；Snapshot 要完整描述页、槽、句柄代际、钉住标记、进行中的计划与退役表，Restore 遇到损坏负载报 ErrBadSnapshot 并保持现有状态不变。

验收：go test ./... 的 15 条用例全部通过，测试文件不得修改、跳过或按固定输入写死；并发分配与迁移后的快照恢复请自行构造场景验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-b-06`
- 实现文件：`shadow_atlas_defragger.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`ShadowAtlasDefragger`、`TileHandle`、`TileSlot`、`TileMove`、`Plan`、`Moved`
