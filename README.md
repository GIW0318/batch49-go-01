# batch49-go-game-a-16 / goap_planner

模块路径 `gsb/batch49-go-game-a-16`，包名 `task`，主文件 `goap_planner.go`，核心类型 `GoapPlanner`。

## 题面

竞技场资料片上线后的第一个周末，值班策划发现同一份守卫配置在两台客户端上跑出了不同的行动顺序，其中一台还挑了一条明显更贵的路线。更麻烦的是，服务器换班重启后，正在搬木板的守卫会从第一步重新开始，前面已经完成的两步白做。相关逻辑集中在模块 `gsb/batch49-go-game-a-16` 的 `goap_planner.go` 里，核心类型为 `GoapPlanner`，需要按下面的规格补齐。

这份配置由策划在编辑器里维护，行动规模在几十条量级，世界事实不超过二十个。守卫每一帧都会询问下一步做什么，因此规划必须缓存；换班、掉落和玩家交互都会改动世界状态，缓存要跟着失效。测试同学还提到，两个人同时点开同一个守卫时，步骤会重复执行，执行记录里出现两条相同的第二步。

世界状态用 `State`（事实名到布尔值）表示，行动用 `Action{ID, Cost, Pre, Effect}` 描述。对外接口至少包括：

- `NewGoapPlanner(opts Options) *GoapPlanner`
- `(p *GoapPlanner) AddAction(a Action) error`
- `(p *GoapPlanner) Actions() []Action`
- `(p *GoapPlanner) SetGoal(agent, goal string, desired State) error`
- `(p *GoapPlanner) Plan(agent string, world State) (Plan, error)`
- `(p *GoapPlanner) Execute(agent string, at int64) (ActionResult, error)`
- `(p *GoapPlanner) Cursor(agent string) int`
- `(p *GoapPlanner) CacheStats() (hits, misses int)`
- `(p *GoapPlanner) Snapshot() ([]byte, error)` / `(p *GoapPlanner) Restore([]byte) error`

具体规格：

1. `AddAction` 拒绝空 ID、负代价和没有任何效果的行动，统一返回 `ErrBadAction`；重复 ID 返回 `ErrDuplicateAction`。`Actions` 按 ID 升序返回副本，顺序稳定。
2. `SetGoal` 要求 agent 非空白，否则 `ErrBlankAgent`；目标事实必须拷贝保存。`Goal` 返回副本，无目标时第二个返回值为 false。
3. `Plan` 对没有目标的 agent 返回 `ErrNoGoal`。行动可执行的条件是 `Pre` 中每个事实都为真；执行后把 `Effect` 中的事实置真。
4. 多条路线都能达成目标时，按总代价升序、行动条数升序、行动 ID 序列字典序升序的全序选最优，不能受 map 遍历顺序左右。
5. 目标在当前世界已经满足时返回空计划，代价与条数都是 0，同时把该 agent 的执行游标复位到起点。
6. 搜索深度上限取 `Options.MaxDepth`，小于 1 时按 6 处理；在上限内无解返回 `ErrNoPlan`。
7. `Plan` 成功后把该 agent 的游标清零并保存计划；相同 agent 与相同世界重复规划必须命中缓存，`CacheStats` 如实累计命中与未命中。
8. 缓存键必须覆盖世界状态中与行动相关的全部事实，只在目标上做键会让不同世界错误复用同一份计划。
9. `Execute` 按计划顺序推进一步：`Index` 从 1 开始，`Remaining` 是剩余步数，最后一步 `Done` 为真；无计划返回 `ErrNoPlan`，越界返回 `ErrPlanDone`；`at` 必须单调不减，倒退返回 `ErrBadTime` 且不推进游标。
10. `Invalidate` 丢弃该 agent 的缓存、计划与游标，重复调用不报错。`RemoveAction` 删除未注册的 ID 返回 `ErrUnknownAction`，删除成功后相关缓存全部失效。
11. 同一 agent 的 `Plan` 与 `Execute` 必须串行；三个执行者同时推进一份三步计划时，每一步只能被执行一次，索引不重复、不丢失。
12. `Snapshot` 要保存行动集、目标、缓存和每个 agent 的游标；`Restore` 全量替换状态，载荷损坏返回 `ErrBadSnapshot`，失败时现有状态与游标不能被改动。

验收方式：在本目录执行 `go test ./...`，全部用例必须通过；不得修改或跳过用例，不得针对固定输入写死结果。并发与重启恢复需要自行验证，只做本地实现，不依赖任何外部服务。

## 运行

```bash
go test ./...
```
