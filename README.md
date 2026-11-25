# 服务器货币产出预算、份额衰减与日切结转

## 题面

游戏新服开服第二天，运营按服务器核对金币产出，发现同一台服务器一天发出去的金币超过了配置预算，几个大号拿到的量远超单人份额，跨日后余额又没有按比例结转，还有玩家在零点前后各领一次导致同一笔产出被重复计入。财务对账时预算池和实际发放对不上，客服只能按玩家一笔笔翻发放记录。落点是 gsb/batch49-go-game-a-23 的 economy_budget_governor.go，类型 EconomyGovernor。对外调用面：NewEconomyGovernor(Options) *EconomyGovernor、Configure(server string, dailyBudget int64, carryPct int) error、SetRoster(server string, players []string, at int64) error、Grant(server, player string, amount int64, at int64) (GrantResult, error)、Usage(server string, at int64) Usage、Rollover(server string, at int64) (RolloverResult, error)、PlayerSpent(server, player string, at int64) int64、Players(server string, at int64) []string、Remain(server string, at int64) int64，以及 Snapshot() ([]byte, error) 与 Restore([]byte) error。

要修的行为：预算必须为正，结转比例只在 0 到 100 之间，重复配置同一服务器报错；未配置服务器、未设定活跃名单时不能发放；活跃名单不能为空、不能有重复玩家，名单在当天首次发放之后锁定；发放金额必须为正，名单外的玩家拒绝；服务器日界按 UTC 零点切分，归属日由调用方传入的 at 计算，跨日发放只能结转一次，发放跨越日界时先自动完成一次结转再入账；玩家当日份额等于当日预算除以日初活跃人数，份额内全额发放，超出份额的部分按配置比例衰减，衰减量单独记录；服务器总发放不得超过当日预算；日切时未用完的预算按比例结转到次日，成为次日预算的一部分，当日用量清零，名单默认沿用前一天，允许在次日首次发放前重新设定；统计接口给出归属日、预算、已用、剩余与活跃人数，未知服务器返回零值且不创建配置；回退到更早的日期必须报错；记录一旦发出不得回滚或重新计入，每笔发放回执都带归属日；同一服务器的发放串行，并发发放不丢用量，不同服务器互不影响。

硬约束：结转只能发生在日切那一刻一次，日中小规模重试不能触发第二次结转；玩家份额按日初活跃人数固定，日内新增玩家不稀释已有份额，其份额安排到次日名单；快照保存当日预算、已用、分玩家用量、活跃名单与配置，重启后继续按同一日口径累计；损坏快照恢复报错且保持现网状态；快照不得包含半份日切结果，恢复后同一笔发放不能既算在旧日又算在新日；预算与份额按整数取整，除法余数不补发，名单排序按玩家 id 升序以保证导出稳定。

验收口径：go test ./... 15 条用例全过，测试不得修改、跳过、写死；并发发放与跨日重启自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-a-23`
- 实现文件：`economy_budget_governor.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`EconomyGovernor`
