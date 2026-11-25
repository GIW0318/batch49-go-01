# 限量商品共享库存、固定补货锚点与撤销回补

## 题面

游戏限时商店开售后不到一分钟，同一件限量商品在全服卖出了超过库存的份数，两个玩家账号同时点购买时都拿到了回执；补货时间也不对，有的商品在售罄后立刻补满，有的到了约定时刻却没有补，撤销回执之后库存又莫名多出一截。运营复盘时发现，补货记录里同一锚点出现了两次，客服按回执退款时库存直接超过了商品上限，购买记录也无法按账号追溯。落点 gsb/batch49-go-game-a-24 的 shop_stock_replenisher.go，类型 StockReplenisher。公开入口：NewStockReplenisher(Options) *StockReplenisher、DefineOffer(Offer) error、Purchase(account, offerID string, qty int, at int64) (Receipt, error)、Stock(offerID string, at int64) int、Restock(offerID string, at int64) (RestockResult, error)、Remaining(account, offerID string) int、Reverse(account, receiptID string, at int64) error、Receipts(account string) []Receipt，以及 Snapshot() ([]byte, error) 与 Restore([]byte) error。

要修的行为：商品 id、库存上限、每账号限购与限购窗口任一非法都要拒绝定义，重复定义同一商品报错，定义里的锚点列表要整体校验；补货锚点必须严格升序，乱序、重复或超过配置数量都报错；购买未知商品、空账号、非正数量都报错；库存不足整次拒绝，不能扣一半；每账号在窗口内的已购数量不得超过限购，超限整次拒绝且不占库存；购买成功扣减全服共享库存并生成可追溯回执，回执按账号可查；补货只在固定锚点发生，按差额补到库存上限，同一锚点只生效一次，锚点到期即算生效，即使库存已满也要在补货结果里列出，补货结果给出本次生效锚点、补充数量与补后库存；查询库存时先结算到期锚点；撤销本人回执恢复库存并标记回执已撤销，重复撤销、撤销他人回执、撤销不存在的回执分别报错；限购剩余量按该商品最近一次交互时刻判定窗口；同一商品的并发购买不能超卖，购买与限购扣减在同一事务完成；不同账号的限购计数互相独立。

硬约束：补货锚点固定，售罄后不能提前补货，也不能因为期间没人购买就跳过锚点；撤销回执恢复的库存不得超过商品上限，超出部分丢弃但要留下记录；限购计数按窗口重置，撤销回执只恢复库存，不回退已用限购次数；快照保存库存、限购计数、已生效锚点与回执，恢复后撤销仍可用；损坏快照恢复报错且不动现网数据；同一商品的补货、撤销与购买必须串行，保证账实一致。

验收口径：go test ./... 15 条用例全过，测试不得修改、跳过或按固定输入写死；并发购买、跨窗口与重启恢复自行验证；上线前用同一商品的两批并发购买核对库存与回执数量一致；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-a-24`
- 实现文件：`shop_stock_replenisher.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`StockReplenisher`
