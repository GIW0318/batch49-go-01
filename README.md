# 背包碎片堆叠整理与临期批次扣减

## 题面

背包碎片堆叠乱了套：同一件药水散成十几叠，每叠都差一点满，玩家点整理按钮时界面转圈半天，整理到一半切地图回来又从第一叠重排；更气人的是临期批次明明最早过期，扣减时却先动保质期最长的那叠，客服一天收到几十条补偿申请；整理预算和断点这两个概念在客户端和服务端各说各话，运营只能看着玩家反复试。落点模块 gsb/batch49-go-game-a-01，实现文件 bag_stack_defragmenter.go，核心类型 StackDefragmenter，公开签名保持：NewStackDefragmenter(Options) *StackDefragmenter、Put(owner string, lot Lot) error、Plan(owner string) (CompactPlan, error)、Compact(owner string, budget int) (CompactResult, error)、Consume(owner, itemID string, qty int) ([]LotUse, error)、Lots(owner, itemID string) []Lot、Total(owner, itemID string) int、Slots(owner string) int、Items(owner string) []string、Snapshot() ([]byte, error) 与 Restore([]byte) error。

要修的十二条：一、Put 遇空角色报 ErrBlankOwner，批次 UID 为空、物品 ID 为空、数量非正、数量超过 StackSize、绑定既不是 free 也不是 bound 一律报 ErrBadLot；二、写入时从 1 起递增分配 Seq，角色持有批次数达到 MaxSlots 后再写入报 ErrBagFull，已有批次一叠不动；三、Lots 按过期时刻升序、同刻按 Seq 升序返回，Total 汇总数量，Slots 统计批次数，Items 去重后按字典序给出；四、Plan 只读，按物品与绑定分组把未满叠的碎片并成整叠，每个 Merge 用组内最早过期者作 Carrier、被吸收批次按 Seq 升序列入 Absorb、合并后数量写入 Qty，Moves 是被吸收批次数，Fragments 是参与碎片数，调用前后槽位与总量必须一致；五、Compact 按方案执行并返回 Merges、Moves 与释放槽位数 Freed，合并前后总量守恒，整叠批次永不参与整理；六、budget 是本次最多吸收的批次数，小于 0 报 ErrBadBudget，等于 0 时现场一个批次都不许动；七、Consume 按过期时刻从早到晚扣减，逐条返回 LotUse 的批次 UID、本次扣减数量与该批次到期时刻，顺序就是扣减顺序，扣减后仍有剩余的批次保留原 UID 与到期时刻；八、数量不足报 ErrShortage 且一个批次都不扣，qty 非正报 ErrBadQty；九、绑定状态不同的批次任何时候都不得并成一叠，整理前后两类数量分别守恒；十、一次扣减跨过批次边界时，先把当前批次剩余量扣完再顺延到下一批次，不能因为扣不满就整批跳过；十一、budget 用尽后未整理的碎片保持原插入顺序，下次整理从断点继续而不是重排全部；十二、Snapshot 保存全部批次、Seq 计数器与整理断点，Restore 整份替换，损坏数据报 ErrBadSnapshot 且不动内存状态，恢复后新写入的 Seq 必须大于快照里的任何编号。

硬约束：同一角色的 Put、Compact、Consume 串行执行，Plan 与 Compact 并发时只能看到完整旧状态或完整新状态；整理断点与 Seq 计数器跨重启保留，不能回绕重号；MaxSlots 是硬上限，先塞后清不算数。

验收：go test ./... 15 条用例全过，不能改测试、不能跳过、不能按固定批次号写死；整理预算与断点的语义要在服务端一次定清，客户端只负责展示；并发整理与重启续做自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-a-01`
- 实现文件：`bag_stack_defragmenter.go`
- 主要类型：`StackDefragmenter`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 测试规模：`tests/test_bag_stack_defragmenter_test.go` 共 15 条用例
