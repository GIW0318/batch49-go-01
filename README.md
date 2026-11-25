# batch49-go-game-a-27

模块路径：`gsb/batch49-go-game-a-27`
源文件：`stall_consignment_guard.go`
包名：`task`
测试目录：`tests/`（十五个用例）

## 题面

周末拍卖活动开始十分钟，主城东区同一件披风被两名玩家同时买走，摊位里少了一件货却多了一笔货款；还有玩家关摊后离线一晚，第二天上线发现卖出的货款既没进背包也没进账本，浏览列表里仍挂着早已卖掉的靴子。问题落在 gsb/batch49-go-game-a-27 的 stall_consignment_guard.go，请补齐 StallGuard 的摆摊、成交与结算行为。

接口：NewStallGuard(Options) *StallGuard；Open(string, int) (Stall, error)；Stall(string) (Stall, bool)；List(string, Item, int64) (Listing, error)；ListingOf(string) (Listing, bool)；Browse(int, int) (BrowsePage, error)；Refresh() error；SnapshotVersion() int64；Buy(string, string, int64) (Purchase, error)；Close(string) ([]Listing, error)；Settle(string, int64) (Settlement, error)；Ledger(string) []LedgerEntry；Deposit(string, int64) error；Balance(string) int64；Snapshot() ([]byte, error)；Restore([]byte) error。

需要落地的规则共十二条：（1）开摊要求摊主标识非空，槽位在一到 MaxSlots 之间，同一摊主重复开摊直接拒绝。（2）上架要求物品编号与名称非空、数量为正、单价为正；一次上架占用一个槽位，槽位占满后拒绝，非法上架不得消耗槽位。（3）挂单编号唯一、序号单调递增，查询单条挂单返回副本，已售与已撤下的挂单仍可按编号查到状态。（4）浏览列表只取最近一次刷新的快照，刷新一次版本号加一；刷新之后新上架的物品在下一次刷新前不得出现。（5）浏览分页要求页码从一开计、每页条数不超过 MaxPageSize，超出末页返回空列表但总数照旧，列表按单价升序、同价按挂单编号升序。（6）购买要求买家标识非空，摊主不能买自己的挂单；余额不足、挂单已售或已撤下时一律失败且不扣款、不改挂单状态。（7）成交时按买家扣款、按费率向下取整计算手续费、净额写入摊主账本，挂单转为已售并释放槽位；同一挂单只能成交一次。（8）快照里的过期条目在购买失败后不得自动从列表消失，也不得因此触发刷新，刷新前浏览版本保持不变。（9）关摊把剩余在售挂单全部转为已撤下并原样返回，槽位清零、摊位不再可查，账本与余额保留，已撤下的挂单不能再被购买。（10）结算只释放未结算且成交时间不晚于入参时刻的账本条目，按成交时间升序、同一秒内按挂单编号升序，总额为净额之和并计入摊主余额，重复结算不得二次入账。（11）充值金额必须为正，余额查询对未知账号返回零。（12）快照保存摊位、挂单、托管物品、账本、余额与浏览版本，恢复要么整体替换要么原样不动，非法载荷报错。

硬约束：同一摊位与同一挂单上的操作必须串行，关摊与购买并发时只能有一方成功，失败方的物品与货款都不能少；成交、关摊与快照并发时只能看到完整状态，重启后从快照恢复的槽位占用、账本结算标记与浏览版本要逐字段还原。

验收：go test ./... 十五条全部通过；不得修改测试、跳过用例或按固定数字写死结果；并发与重启后的行为自行补充验证；只在本地实现，不接入外部服务。

## 运行

```bash
go test ./...
```

只需 Go 1.23 与标准库。
