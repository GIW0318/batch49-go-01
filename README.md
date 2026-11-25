# batch49-go-game-a-20 / score_submission_guard

模块路径 `gsb/batch49-go-game-a-20`，包名 `task`，主文件 `score_submission_guard.go`，核心类型 `ScoreGuard`。

## 题面

反作弊在周四的例行巡检里注意到，某个新号在四十秒内把分数从一千推到九千，成绩照常上榜；同一天还有几名正常玩家因为提交太快被误判进隔离区，运营翻遍台账也说不清谁在排队、谁已经放行。相关逻辑在模块 `gsb/batch49-go-game-a-20` 的 `score_submission_guard.go`，核心类型 `ScoreGuard`，需要按下面的规格补齐。

这批成绩来自三个渠道：客户端直传、比赛结算回放和客服补录，字段格式一致但可信度不同。运营要求所有可疑记录都能按提交 ID 追溯到提交时刻、局时长和命中的原因；隔离区不能无限膨胀，满了以后按最旧先出处理，并在台账上留下明确状态。

对外接口至少包括：

- `NewScoreGuard(opts Options) *ScoreGuard`
- `(g *ScoreGuard) Submit(board, player string, score, durationMs int64, at int64) (Verdict, error)`
- `(g *ScoreGuard) Quarantine(board string) []Quarantined`
- `(g *ScoreGuard) Submission(board, submissionID string) (Quarantined, bool)`
- `(g *ScoreGuard) Release(board, submissionID string) error`
- `(g *ScoreGuard) Reject(board, submissionID, reason string) error`
- `(g *ScoreGuard) Effective(board, player string) (int64, bool)`
- `(g *ScoreGuard) EffectiveAll(board string) []EffectiveEntry`
- `(g *ScoreGuard) Watermark(board string) (int64, bool)`
- `(g *ScoreGuard) Stats(board string) Stats`
- `(g *ScoreGuard) Snapshot() ([]byte, error)` / `(g *ScoreGuard) Restore([]byte) error`

具体规格：

1. `Submit` 要求 board 与 player 非空白，否则 `ErrBlankKey`；`durationMs` 必须为正，否则 `ErrBadDuration`；`score` 不得为负，否则 `ErrBadScore`。
2. 提交 ID 由 board、player、score、durationMs、at 推导。完全相同的提交视为重复，返回 `StatusDuplicate`，不新建台账记录，也不重复占用隔离位。
3. 单次增幅检查：本次分数减去该玩家当前有效分超过 `MaxSingleGain` 就记 `single_gain`；该项配置为 0 表示关闭。
4. 滑动窗口检查：窗口为 `[at-WindowMs, at]`，与窗口内该玩家的最高有效分比较，超过 `WindowGain` 就记 `window_gain`；窗口按玩家分别计数，不同玩家绝不能共享同一份计数。
5. 时长合理性：`score*60000` 超过 `MaxScorePerMinute*durationMs` 就记 `duration`；该项配置为 0 表示关闭。
6. 三项检查命中任意一项即进入隔离区，不显示在榜上，`Effective` 对该玩家返回 false；命中多项时 `Reasons` 要全部记录。隔离区按 board 独立、先来先服务，待审数量超过 `QuarantineLimit`（小于 1 时按 16）时丢弃最旧的待审记录并把状态置为 `dropped`。
7. `Release` 要求记录处于待审状态，否则 `ErrBadState`；未知 ID 返回 `ErrUnknownSubmission`。放行后的成绩必须使用提交时的分数与提交时刻入榜，不能用放行时刻的榜内状态重新计算。
8. `Reject` 要求原因非空白，否则 `ErrBadReason`；记录必须处于待审状态，否则 `ErrBadState`。被拒绝的记录永久关闭，不能因为再次提交同一内容而重新进入隔离区。
9. `Effective` 返回该玩家最高的已入榜分（含已放行成绩）；`Watermark` 是该榜最高入榜分；`EffectiveAll` 按分数降序、同分玩家 ID 升序排列。
10. 同一 board 的 `Submit`、`Release`、`Reject` 必须串行；八个并发提交拍在同一玩家上时，最终有效分是其中的最高者，台账计数与提交条数一致。`Stats` 分别统计已提交、已入榜、待审、已放行、已拒绝、已丢弃与重复次数。
11. `Snapshot` 保存台账、隔离队列与榜内成绩；`Restore` 全量替换，载荷损坏返回 `ErrBadPayload`，失败时现有状态不能被改动。

验收方式：在本目录执行 `go test ./...`，全部用例必须通过；不得修改或跳过用例，不得针对固定输入写死结果。并发与重启恢复需要自行验证，只做本地实现，不依赖任何外部服务。

## 运行

```bash
go test ./...
```
