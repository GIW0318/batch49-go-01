# batch49-go-game-a-18 / npc_schedule_guard

模块路径 `gsb/batch49-go-game-a-18`，包名 `task`，主文件 `npc_schedule_guard.go`，核心类型 `NpcScheduleGuard`。

## 题面

主城换班之后，巡逻卫兵在集市摊位前堵成了一列：明明摊位只站得下一个人，后面几位却都显示"已在集市"，摊位占用数一路涨到五；到了中午换场，有人的旧摊位没释放，新摊位又进不去，卡在中间两头占着。这批逻辑在模块 `gsb/batch49-go-game-a-18` 的 `npc_schedule_guard.go` 里，核心类型是 `NpcScheduleGuard`，需要按下面的规格补齐。

这批 NPC 一共四十多个，日程由关卡策划在表里维护，地点容量随场景配置变化。守卫每帧都会对视野内的 NPC 调一次推进，所以同一毫秒内的重复推进必须无副作用；换班重启后，摊位占用和排队顺序也要能原样接上，否则会出现两个人共用一个摊位。

对外接口至少包括：

- `NewNpcScheduleGuard(opts Options) *NpcScheduleGuard`
- `(g *NpcScheduleGuard) DefineSchedule(npcID string, items []ScheduleItem) error`
- `(g *NpcScheduleGuard) Tick(npcID string, at int64) (ScheduleState, error)`
- `(g *NpcScheduleGuard) Interrupt(npcID, reason string, until int64) error`
- `(g *NpcScheduleGuard) Resume(npcID string, at int64) (ScheduleState, error)`
- `(g *NpcScheduleGuard) Occupancy(placeID string) int`
- `(g *NpcScheduleGuard) Waiting(placeID string) int`
- `(g *NpcScheduleGuard) State(npcID string) (ScheduleState, bool)`
- `(g *NpcScheduleGuard) Release(npcID string) error`
- `(g *NpcScheduleGuard) Snapshot() ([]byte, error)` / `(g *NpcScheduleGuard) Restore([]byte) error`

具体规格：

1. `DefineSchedule` 要求 NPC ID 非空白，否则 `ErrBlankNPC`；条目为空返回 `ErrBadSchedule`；每条 `SlotID`、`Place`、`Behavior` 非空白，`StartMin` 在 0 到 1439、`EndMin` 在 0 到 1440 之间且两者不相等，否则 `ErrBadSchedule`。
2. 同一 NPC 的时段不允许重叠，重叠返回 `ErrOverlap`；合法条目按 `StartMin` 升序保存。重复定义整体替换旧日程，并释放旧日程留下的地点占用。
3. 时段左闭右开：把 `at` 换算成当天分钟（`at/60000` 对 1440 取余）后，落在 `[StartMin, EndMin)` 内才算命中；`EndMin` 小于等于 `StartMin` 的条目表示跨零点，跨零点时段在零点处既不能断成两段，也不能在断点重复进入地点。
4. `Tick` 对没有日程的 NPC 返回 `ErrUnknownNPC`。命中时段的 NPC 占用其 `Place`；同一时段内重复 `Tick` 不重复计数，`Since` 保持首次进入的时刻。
5. 时段切换时先释放旧地点再占用新地点；没有命中任何时段则进入空闲，`SlotID`、`Place`、`Behavior` 清空并释放原地点。
6. 地点容量优先取 `Capacities` 中的配置，没有配置时用 `DefaultPlaceCapacity`；已占用数达到容量时，NPC 进入该地点的等待队列，状态 `Waiting` 为真。
7. 等待期间 NPC 必须保留原地点占用，直到成功离开并进入目标地点；不能先把旧地点释放掉再去排队。
8. `Release` 让 NPC 离开当前地点（包括等待期间保留的旧地点）并退出队列，随后该地点按先来先服务放行第一个等待者，等待者直接落位。
9. `Interrupt` 要求原因非空白，否则 `ErrBadReason`；NPC 未定义返回 `ErrUnknownNPC`。中断立刻释放地点占用，中断期间 `Tick` 不重新占用，只回带 `Interrupted` 标志。
10. `Resume` 在 `until` 之前调用返回 `ErrStillInterrupted`；结束中断时按当前时刻重新计算所属时段并进入，不能沿用中断前那个可能已经过期的时段。NPC 未处于中断时调用返回 `ErrNotInterrupted`。
11. 同一 NPC 的 `Tick`、`Interrupt`、`Resume` 必须串行；八个并发 `Tick` 拍在同一个 NPC 上，地点占用只能记一次，不能算成二。
12. `Snapshot` 保存日程、每个 NPC 的状态、地点占用与等待队列；`Restore` 全量替换，载荷损坏返回 `ErrBadRestore`，失败时现有占用与队列不能被改动。

验收方式：在本目录执行 `go test ./...`，全部用例必须通过；不得修改或跳过用例，不得针对固定输入写死结果。并发与重启恢复需要自行验证，只做本地实现，不依赖任何外部服务。

## 运行

```bash
go test ./...
```
