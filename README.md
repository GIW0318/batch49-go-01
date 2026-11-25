# 任务链分叉汇聚与 quorum 判定

## 题面

新资料片的副本任务链从一条线改成三条并行分支，上线第一天就出两种事故：先做完左分支的玩家，右分支明明没碰却显示已完成；另一个玩家取消了一条支线，整条链的子节点全部变成不可完成，连已经拿到的奖励也一并消失，任务策划在后台看不出某个节点为什么不可完成，只能靠玩家截图猜依赖关系。落点模块 gsb/batch49-go-game-a-05，实现文件 quest_fork_join.go，核心类型 ForkJoinChain，公开签名保持：NewForkJoinChain(Options) *ForkJoinChain、DefineChain(chain Chain) error、Complete(player, chainID, nodeID string, at int64) (NodeState, error)、Cancel(player, chainID, nodeID, reason string) error、Resolve(player, chainID, joinID string) (JoinResult, error)、State(player, chainID string) ChainState、Rewards(player, chainID string) []string、Available(player, chainID string) []string、Snapshot() ([]byte, error) 与 Restore([]byte) error。

要修的十二条：一、DefineChain 对空链号、空节点列表、空节点号、链内重复节点号、Quorum 小于 0 或大于依赖数、有 Quorum 却无依赖一律报 ErrBadChain，重复定义报 ErrDuplicateChain；二、依赖引用不存在的节点报 ErrUnknownNode，定义里出现依赖环报 ErrCycle，节点数超过 MaxNodes 报 ErrNodeLimit；三、定义不创建玩家状态，State 首次求值时根节点为 active、其余为 pending，顺序与定义一致；四、Complete 对空玩家报 ErrBlankArg、未知链报 ErrUnknownChain、未知节点报 ErrUnknownNode，已完成报 ErrAlreadyDone，已取消报 ErrAlreadyCanceled；五、依赖未满足时 Complete 报 ErrNodeNotReady，Quorum 大于 0 的节点按至少 N 个依赖完成判定，Quorum 为 0 要求全部依赖，完成序号按玩家在该链上的完成顺序从 1 递增；六、完成节点时按定义把奖励号记入该玩家的奖励列表，Rewards 按完成顺序返回；七、Available 按定义顺序返回依赖已满足、可立即完成的节点；八、Cancel 对空原因报 ErrBlankArg，取消已完成节点报 ErrAlreadyDone，重复取消报 ErrAlreadyCanceled；九、取消沿依赖边向下传播，全部依赖都必需的节点直接取消，带 Quorum 的节点在剩余未取消依赖数低于 Quorum 时取消，否则保持待完成；十、Quorum 为 N 时，取消一个已完成分支不能让汇聚节点回退到未完成；十一、分支取消只影响本分支，兄弟分支的进度与奖励不受牵连；十二、依赖环包含已被取消的节点时仍要按定义检测，不能因为运行期取消就放行。

硬约束：Resolve 只对带依赖的节点有效，无依赖报 ErrNotJoin，返回生效 Quorum（0 视为依赖数）、已完成依赖数与是否满足；同一玩家同链的完成与取消串行，跨玩家进度互不影响，节点状态只能单向流转；快照保存链定义、每个玩家的节点状态、完成序号与奖励列表，恢复后重放不被重发，损坏数据报 ErrBadSnapshot；定义只做图校验，不改动玩家状态；每次完成只发一次奖励，重复调用不得重复发。

验收：go test ./... 15 条用例全过，不能改测试、不能跳过、不能按固定节点号写死；并发完成与重启恢复自行验证；节点状态与奖励列表同批写入，不能出现节点完成但奖励丢失的中间态；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-a-05`
- 实现文件：`quest_fork_join.go`
- 主要类型：`ForkJoinChain`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 测试规模：`tests/test_quest_fork_join_test.go` 共 15 条用例
