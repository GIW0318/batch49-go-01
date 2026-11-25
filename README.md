# 碰撞层掩码与触发器事件

## 题面

副本里的传送门和伤害区域偶尔不触发，玩家站进去没反应；有时候人已经走开半张地图，才补来一条进入事件，离开事件干脆丢了。把碰撞层调过一次之后，原本能碰的箱子也穿模了，触发器还和实体墙混在一起发事件，导致同一个区域反复进出、结算时重复计算。触发器事件里还夹着同名的实体配对，策划按事件数发奖励时多发了两次。落点在 gsb/batch49-go-game-a-14 的 collision_mask_router.go，类型 CollisionMaskRouter，需要在现有签名上把行为补齐。
公开入口：NewCollisionMaskRouter(Options) *CollisionMaskRouter、SetLayer(id string, layer, mask uint32, kind string) error、Pairs(a, b string) bool、Update(step Step) ([]Event, error)、Entered(a, b string) bool、Layer(id string) (LayerSpec, bool)、RemoveObject(id string) error、ActivePairs() []Pair、ObjectIDs() []string、Snapshot() ([]byte, error)、Restore([]byte) error。
要落到的行为：1) SetLayer 校验空 id、kind 只能是 solid 或 trigger、对象数上限，重复设置同一个 id 视为更新而不是新增，层与掩码都按位图处理，零层也允许注册；2) Pairs 要求双方掩码都接受对方的层，只有单边命中不算有效配对；3) Update 接收候选重叠，按两个 id 的大小关系归一后去重，同一个配对在一个 tick 里只处理一次，自配对与未知对象的候选直接判为非法并返回明确错误；4) 双方都是 solid 的配对记碰撞事件，任一方是 trigger 的配对记触发器事件，触发器事件要带 Trigger 标记，碰撞事件不带；5) 每对对象只发一次进入和一次离开，停留每个 tick 汇总成一条 stay 并累加 Ticks，进入事件的 Ticks 从 1 起算；6) 事件按两个 id 的字典序稳定排序，同一 tick 内输出顺序固定，同一配对不允许在同一 tick 同时出现进入和停留；7) 对象销毁补发的离开事件只发一次，之后同一配对不得再出现；8) 掩码动态改变后，原本有效的配对补发离开，新有效的配对补发进入；9) 因事件数上限被截断的条目不能影响配对集合状态，下一 tick 不能重复发进入；10) Entered 与 ActivePairs 反映当前有效集合，删除对象后 Layer 查不到、ObjectIDs 不再包含它，移除对象后在下一个 tick 之前，Entered 对它的旧配对返回 false；11) 快照保存层表、当前配对与停留计数，恢复后下一次 Update 的 Ticks 与重启前连续。
硬约束：层表与配对集合在一次 Update 内必须一致，读方不能看到只更新了一半的集合；对象表与活跃集合同一把锁保护；快照是全量状态，恢复要么整体成功要么保持原状；事件数与对象数都是硬上限，截断只影响本次输出，不反向改动状态；并发 Update 与 SetLayer 请自行构造验证。
验收：go test ./... 的 15 条用例全过，测试不得修改、跳过或按固定输入写死；只做本地实现，交付源码、测试记录与事件排序口径说明。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-a-14`
- 实现文件：`collision_mask_router.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`CollisionMaskRouter`
