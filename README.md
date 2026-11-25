# 空间哈希宽相索引

## 题面

大世界同屏玩家一多，近战战斗判定开始漏人：两个模型明明贴在一起，服务端却查不到候选对，客户端只能看到对方被击退却没有伤害。把格子调小以后又出现同一个人被反复配对，命中次数翻倍；查询一旦限流，返回的剩余数量也不对，策划据此估算的负载完全失真。跨格的大体型怪物在格子之间来回移动后，旧的格子引用还会残留，明明已经走远却仍被拉进候选。同屏三百人的战场里最严重的一次，整队近战有半秒完全打不中。落点在 gsb/batch49-go-game-a-15 的 broadphase_hash_grid.go，类型 HashGrid，需要在现有签名上把行为补齐。
公开入口：NewHashGrid(cellSize float64) (*HashGrid, error)、Insert(id string, box Box) error、Move(id string, box Box) error、Remove(id string) error、Pairs(budget int) ([]Pair, int, error)、Query(box Box, budget int) ([]string, int, error)、Box(id string) (Box, bool)、Len() int、Cells() int、CellOf(p Vec3) (int64, int64, int64, error)、SetBounds(b Box) error、OverflowCount() int、Snapshot() ([]byte, error)、Restore([]byte) error。
要落到的行为：1) cellSize 必须为正，Insert 校验空 id、非法盒与重复 id；2) 按包围盒跨格登记，盒体覆盖的每个格都留引用，完全在边界内才进正常格，越界对象归入溢出桶；3) Move 先摘旧格再登记新格，旧格引用必须清空，Remove 清掉所有格引用与溢出桶；4) Pairs 返回共享至少一格的对象对，按两个 id 的大小关系归一，不能正反各出一条，同一对对象在多个格子里重复出现时只保留一条；5) 达到预算立即停止，并返回准确的剩余对数，已经处理过的不能算进剩余，剩余只统计尚未返回的候选；6) Query 返回与查询盒共享格子的对象 id，去重并按 id 升序，达到预算截断，查询盒与对象盒只在同一格即算候选，不要求几何相交；7) 溢出桶内的对象两两成对，不与正常格的候选混算，SetBounds 变更后按新边界重新分桶；8) 对象从溢出桶移回正常格时，旧桶引用必须清干净；9) 预算非正报错，对象数与单个盒子的跨格数都有上限，超限拒绝而不是无限登记，单个盒子跨格过多时按上限报错，不允许悄悄裁剪；10) 并发移动与查询只能看到完整的旧索引或完整的新索引，不能看到迁移到一半的状态；11) Pairs 与 Query 只读索引，多次调用的结果稳定且不改变格子占用；12) 快照保存格映射、溢出桶与盒子表，恢复后的查询、配对与 Cells 计数一致。
硬约束：索引由读写锁保护，迁移过程中读方不能看到半份引用；快照是全量状态，恢复要么整体成功要么保持原状；预算、对象数与跨格数都是硬上限，超限当场拒绝；并发移动与查询、溢出桶往返请自行构造验证。
验收：go test ./... 的 15 条用例全过，测试不得修改、跳过或按固定输入写死；只做本地实现，交付源码、测试记录与格子容量口径说明。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-a-15`
- 实现文件：`broadphase_hash_grid.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`HashGrid`
