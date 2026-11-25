# 世界任务链阶段推进与参与资格

## 题面

世界任务链第二阶段开了一整晚仍显示零贡献，玩家明明在打怪，进度条却一动不动；换服重登之后，参与名单和阶段进度一起消失，昨天开荒的几百号人全部变成没打过。更糟的是两个服同时上报同一只世界首领，血量被算了两份，阶段提前结束，领奖名单却缺了一半人，运营在后台看不到每个玩家到底算不算这一阶段的奖励对象，只能拉日志逐个对。落点模块 gsb/batch49-go-game-a-04，实现文件 world_chain_coordinator.go，核心类型 WorldChainCoordinator，公开签名保持：NewWorldChainCoordinator(Options) *WorldChainCoordinator、DefineChain(chain Chain) error、Join(chainID string, stage int, player string, at int64) error、Contribute(chainID, eventID, player string, amount int64, at int64) (StageProgress, error)、Advance(chainID string, at int64) (StageState, error)、Eligible(chainID string, stage int, player string) bool、Progress(chainID string) (StageProgress, error)、Participants(chainID string, stage int) []string、State(chainID string) (ChainState, bool)、Awarded(chainID string, stage int) []string、Snapshot() ([]byte, error) 与 Restore([]byte) error。

要修的十二条：一、DefineChain 对空链号、空阶段列表、阶段序号与下标不符、阈值非正、持续时长非正报 ErrBadChain，重复定义报 ErrDuplicateChain；二、定义完成后阶段 0 处于 locked，State 返回全部阶段的顺序视图，链状态为 running；三、Advance 首次调用按 at 开启当前阶段，窗口为 at 到 at 加时长，阶段尚未开启时 Contribute 与 Join 报 ErrWindowClosed；四、Contribute 对空事件号或空玩家报 ErrBlankArg，数量非正报 ErrBadAmount，at 落在窗口之外报 ErrWindowClosed，阶段已结算报 ErrStageSettled；五、贡献累加到当前阶段并返回 StageProgress，Players 是参与人数，同服不同事件分别累计；六、Join 对未知链报 ErrUnknownChain、未知阶段或非当前阶段报 ErrStageOutOfRange、空玩家报 ErrBlankArg，同一玩家重复加入只保留一条；七、Eligible 只对在窗口内加入该阶段的玩家返回 true，未加入或未知阶段返回 false，Participants 按字典序升序；八、贡献达到阈值后 Advance 把阶段置为 completed、记下领奖名单并推进到下一阶段，最后阶段完成后整条链状态变为 completed；九、窗口到期仍不达阈值时 Advance 把阶段置为 failed、贡献清零且不推进，下次 Advance 重新开窗重打；十、阶段失败回退后已发奖励不回滚，重打同一阶段不得再发；十一、两个服上报同一事件号只能计一次，不能按来源各计一份；十二、迟到加入的玩家能看到阶段进度但没有该阶段奖励资格。

硬约束：全服贡献事件按事件号去重，重复上报报 ErrEventConflict 且不累加；阶段切换与贡献上报并发时不得出现阶段已完成仍继续累加；快照保存链定义、阶段状态与参与名单，恢复后进度与资格保持一致，损坏数据报 ErrBadSnapshot；阶段窗口由首次 Advance 决定，重启后不得重新计时；参与名单只增不减，阶段重打时不重复记录同一玩家。

验收：go test ./... 15 条用例全过，不能改测试、不能跳过、不能按固定事件号写死；并发上报与重启恢复自行验证；阶段与参与名单的写入同批生效，不能出现进度加了名单没记的中间态；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-a-04`
- 实现文件：`world_chain_coordinator.go`
- 主要类型：`WorldChainCoordinator`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 测试规模：`tests/test_world_chain_coordinator_test.go` 共 15 条用例
