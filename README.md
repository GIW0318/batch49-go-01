# batch49-go-game-a-17 / utility_selector

模块路径 `gsb/batch49-go-game-a-17`，包名 `task`，主文件 `utility_selector.go`，核心类型 `UtilitySelector`。

## 题面

近战组把战斗、巡逻、逃跑的决策权交给了一张数值表：每个候选行为读一个世界变量，经过一条曲线折成千分制整数分，再选最高分执行。上线后反馈集中在两件事：AI 在两个差不多好的行为之间来回横跳，分数只差几分就换；同一个行为连续用上三四次之后，分数被扣穿，日志里出现了负分。代码位于模块 `gsb/batch49-go-game-a-17` 的 `utility_selector.go`，核心类型 `UtilitySelector`。

数值组同时定了三条规矩：曲线只吃整数、只吐整数，避免浮点误差在客户端和服务器之间放大；一张行为表要同时服务几十个 AI，读分数不能把帧率拖垮；换班重启后冷却和惩罚必须原样恢复，否则刚被扣分的行为会瞬间回到榜首。值班同学还希望在日志里直接看到每个分数的来源，而不是只看到一个整数。

对外接口至少包括：

- `NewUtilitySelector(opts Options) *UtilitySelector`
- `(s *UtilitySelector) AddBehavior(b Behavior) error`
- `(s *UtilitySelector) Behaviors() []Behavior`
- `(s *UtilitySelector) Score(agent, behavior string, world World) (int, error)`
- `(s *UtilitySelector) Select(agent string, world World, at int64) (Selection, error)`
- `(s *UtilitySelector) Commit(agent, behavior string, at int64) error`
- `(s *UtilitySelector) Penalize(agent, behavior string, amount int) error`
- `(s *UtilitySelector) Current(agent string) (string, bool)`
- `(s *UtilitySelector) CooldownUntil(agent, behavior string) int64`
- `(s *UtilitySelector) Snapshot() ([]byte, error)` / `(s *UtilitySelector) Restore([]byte) error`

具体规格：

1. `AddBehavior` 校验行为 ID 与变量名非空白、曲线只接受 `linear`、`step`、`inverse_s`、`InMax` 必须大于 `InMin`，否则返回 `ErrBadBehavior`；重复 ID 返回 `ErrDuplicateBehavior`；`Behaviors` 按 ID 升序返回副本。
2. 曲线先把输入夹到 `[InMin, InMax]`，再算 `p = (v-InMin)*100/(InMax-InMin)`（整除）。`linear` 得 `p*10`；`step` 在 `v >= InMin` 时得 1000，否则 0；`inverse_s` 得 `(100-|2p-100|)*10`。
3. `Score` 输出必须夹在 0 到 1000 之间，并扣掉该 agent 与该行为累计的惩罚分。agent 空白返回 `ErrBlankAgent`，行为未注册返回 `ErrUnknownBehavior`。
4. `Penalize` 只接受正数 `amount`，否则返回 `ErrBadAmount`；累加后的惩罚不超过 `Options.PenaltyCap` 的硬顶，连续提交同一行为时按次数累加、达到硬顶后不再增加，分数最低只到 0。惩罚按 agent 与行为分别记账，互不串用。
5. `Select` 计算所有可参与行为的当前分，冷却未结束（`cooldownUntil > at`）的行为不参与；冷却结束的那一毫秒起该行为即可参与，不能多等一个时间片。
6. 没有当前行为时选最高分，`Switch` 为真、`Reason` 为 `initial`；同分时按行为 ID 升序取第一个。
7. 有当前行为时，候选分数必须严格超过当前行为分数的 `(100-HysteresisPercent)%` 才允许切换；比较对象是当前行为的分数，不是上一次候选的分数。
8. 当前行为处于冷却期时不得继续保持，必须切换到最高分候选，`Reason` 为 `cooldown`。
9. `Commit` 记录当前行为、写入冷却截止时间并维护连续使用计数；行为未注册返回 `ErrUnknownBehavior`。`Select` 只给出建议，不改动当前行为，当前行为只能由 `Commit` 写入。
10. `Select` 在没有任何行为时返回 `ErrNoBehaviors`，空白 agent 返回 `ErrBlankAgent`，全部行为不可选时返回 `ErrNoCandidate`；同一 agent 的 `Select`、`Commit`、`Penalize` 必须串行。
11. `Snapshot` 保存行为定义、当前行为、冷却、惩罚与连续计数；`Restore` 全量替换，载荷损坏返回 `ErrBadPayload`，失败时现有状态不能被改动。

验收方式：在本目录执行 `go test ./...`，全部用例必须通过；不得修改或跳过用例，不得针对固定输入写死结果。并发和重启恢复需要自行验证，只做本地实现，不依赖任何外部服务。

## 运行

```bash
go test ./...
```
