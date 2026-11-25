# batch49-go-game-a-28

模块路径：`gsb/batch49-go-game-a-28`
源文件：`trade_velocity_guard.go`
包名：`task`
测试目录：`tests/`（十五个用例）

## 题面

游戏跨服交易行做限流演练时，同一玩家账号在一分钟窗口里被放行了十二笔，事后账本只记到八笔；另几个商人被判定为对手方集中度过高，复核通过后账号仍然无法下单，客服只能逐个手工解封。问题落在 gsb/batch49-go-game-a-28 的 trade_velocity_guard.go，请补齐 TradeVelocityGuard 的频次与集中度判定。

接口：NewTradeVelocityGuard(Options) *TradeVelocityGuard；Allow(string, string, int64, int64) (Verdict, error)；Record(string, string, string, int64, int64) error；Suspend(string, string, int64) error；Approve(string) error；Window(string, int64) WindowStat；CounterpartShare(string, string, int64) int；Status(string, int64) AccountStatus；Limits() Options；Events(string) []Event；Snapshot() ([]byte, error)；Restore([]byte) error。

需要落地的规则共十二条：（1）窗口时长缺省六十秒，单窗口笔数上限缺省二十，金额上限缺省十万，集中度上限缺省八百千分比，挂起时长缺省三百秒，Limits 返回生效值。（2）发起方或接收方为空、双方相同、金额非正、时刻非正，一律判为非法交易并报错，不得写入任何记录。（3）窗口只统计发起账号自己发起的交易，时间范围是大于下界且不晚于当前时刻，重置时刻取窗口内最早一条记录的成交时刻加窗口时长，空窗口时各项为零。（4）Allow 只读不写：连续放行不得消耗笔数或金额额度，也不得向窗口塞入记录。（5）放行前先看笔数与金额，本次加上去会超过任一上限就拒绝，返回的判定里要带上当前窗口的笔数、金额与重置时刻。（6）集中度用接收方已记录金额加上本次金额作分子、窗口内已记录金额作分母，换算成千分比，超过上限就拒绝并立刻挂起该账号，原因记为 concentration，解挂时刻为当前时刻加挂起时长。（7）处于挂起期的账号一律拒绝放行，判定码为 suspended，挂起到期后自动失效；复核通过调用 Approve 立刻解除，对未知账号调用不报错。（8）挂起要求账号非空、原因非空、解挂时刻为正，违反任一项都要报错。（9）Record 按交易编号去重，同一编号重复写入直接成功返回且不改变窗口，重复写入不同对手方或不同金额也按首次为准。（10）窗口随时间滑动时，过期记录要持续淘汰，挂起期间也照常淘汰；CounterpartShare 只按窗口内已记录金额计算，空窗口返回零。（11）Events 按成交时刻升序返回副本，调用方修改返回值不得影响内部记录。（12）Snapshot 保存窗口记录、挂起表与交易编号索引，Restore 要么整体替换要么原样不动，非法载荷报错，恢复后重复编号仍要去重。

硬约束：同一账号的 Allow、Record、Window 与挂起变更必须串行，多协程同时记录不能丢笔数、不能出现半更新的窗口；快照与判定并发时只能看到完整窗口，重启后从快照恢复的窗口记录、挂起截止时刻与去重索引要逐字段还原。

验收：go test ./... 十五条全部通过；不得修改测试、跳过用例或按固定账号写死判定；并发与重启后的行为自行补充验证；只在本地实现，不接入外部服务。

## 运行

```bash
go test ./...
```

只需 Go 1.23 与标准库。
