# 会话行为偏离度评分与挑战锁存

## 题面

风控值班发现一类怪现象：同一个账号在正常办公时段被反复要求二次验证，换成凌晨出差登录反而一路放行；另有一次，评分已经回落到阈值以下，页面上的挑战弹窗却怎么也关不掉。值班先怀疑采集端漏事件，核对后才发现四类事件的权重本身有高有低，基线又被同一批数据带着跑。范围落在 gsb/batch49-go-safety-a-22 的 session_risk_scorer.go，类型 SessionRiskScorer，负责已建立会话的行为偏离度评分与挑战决策。公开入口保持：NewSessionRiskScorer(cfg ScorerConfig) *SessionRiskScorer、Observe(sessionID string, e Event) (Score, error)、Score(sessionID string) (Score, error)、Explain(sessionID string) []Reason、Challenge(sessionID, level string) error、Ack(sessionID, challengeID string) error、Snapshot() ([]byte, error)、Restore(data []byte) error、Baseline(userID, eventType string, slot int) (float64, error)、ChallengeState(sessionID string) (ChallengeState, bool)、Sessions(userID string) []string。

要把下面这些行为做实：1) 空会话标识报 ErrBlankSession，未知事件类型报 ErrBadEvent，未建立的会话查询报 ErrUnknownSession；2) 事件类型只认登录地、换设备、换 UA、命令突发这四类；3) 每用户按事件类型维护分时段基线，一天切六段、每段四小时，段号取事件小时数整除四，段号只接受 0 到 5；4) 基线初值 1.0，随后按 alpha 0.2 做指数滑动平均，同一段再来一个权重 5 的事件后基线应为 1.8；5) 偏离度取事件权重与当前基线差值的绝对值除以基线，评分按 beta 0.3 做滑动平均并乘以 10，单个权重 9 的事件应把评分推到 24；6) 评分有饱和上限，上限从 ScorerConfig 读取，单个权重 20 的事件在满分 30 的配置下停在 30 并标记饱和；7) 单次极端偏离不得顶穿饱和上限后继续累加，形成事实上无法解除的拒绝；8) 评分每次更新版本号加一，同一会话的版本号严格递增；9) Explain 至少给出偏离原因码与正增量，评分饱和时附饱和原因；10) Challenge 只能作用于已建立会话，档位只认 step_up 与 block，触发后 ChallengeState 返回锁存标记与非空挑战标识，Ack 只接受本会话签发的挑战标识，外来标识报 ErrBadChallenge；11) 基线在用户跨时区旅行期间要按当地时段平移，不能把正常作息变化判为偏离；12) 分数回落后已触发的挑战仍保持锁存，直到通过才清除；Sessions 按建立顺序返回该用户的会话标识，未知用户返回空列表。

硬约束：评分更新用比较交换保证同一会话不丢更新也不回退，八路并发 Observe 之后版本号与最终分数要对得上；分时段基线、会话评分与挑战锁存要能整份快照并在新实例上恢复，恢复后基线、版本与锁存状态延续，损坏快照报 ErrBadSnapshot；单会话事件队列与单用户会话数设上限，超限分别报 ErrQueueFull 与 ErrSessionLimit，不得静默丢弃已有事件。

验收：go test ./... 15 条用例全部通过，测试不得修改、跳过或按固定输入写死；并发评分与重启恢复自行验证；只做本地实现，交付时附一份评分公式与锁存口径说明。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-a-22`
- 实现文件：`session_risk_scorer.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`SessionRiskScorer`、`Event`、`Score`、`Reason`、`ChallengeState`、`ScorerConfig`
