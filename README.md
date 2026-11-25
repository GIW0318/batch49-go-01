# 遮挡体离线烘焙的候选筛选与按块丢弃

## 题面

室外大场景的遮挡数据是离线烘出来的，最近发现同一块区域两次烘焙出来的版本号对不上，客户端缓存整片失效；有的块覆盖率明显不够，进游戏却还拿着上一版的遮挡数据，角色躲在墙后照样被剔掉；美术反馈透明树叶被当成遮挡体，人物跑过去整片消失；跨块的大三角形在相邻两块里各算了一份，接缝处的遮挡结果又对不上；同一个场景拆成两批提交，最终版本还跟一次性提交对不上。落点模块 gsb/batch49-go-game-b-04，实现文件 occluder_bake_planner.go，核心类型 OccluderBakePlanner，公开签名保持：NewOccluderBakePlanner(BakeOptions) *OccluderBakePlanner、Submit(sceneID string, tris []Triangle, mat MaterialInfo) error、BakeCell(cell CellID) (CellData, error)、BakePending(limit int) (BakeResult, error)、InvalidateByMaterial(matID string) error、Cells() []CellID、DirtyCells() []CellID、DiscardedCells() []CellID、Coverage(cell CellID) (float64, error)、VersionHash(cell CellID) (string, error)、CandidateCount(cell CellID) int、Snapshot() ([]byte, error) 与 Restore([]byte) error。

要修的地方：一、Submit 校验，场景 ID 为空报 ErrBlankScene，材质 ID 为空报 ErrBadMaterial，空批次报 ErrEmptyBatch，三角形数超过 MaxTrianglesPerBatch 报 ErrBatchTooLarge，坐标出现 NaN 或无穷报 ErrBadTriangle。二、透明与动态材质的三角形在提交阶段整体剔除，不进入任何候选表。三、三角形按 XY 包围盒落到它覆盖的每一个块，块区间按左闭右开划分；跨块的三角形按块边界裁剪，各块各算一份，相邻块不得共享同一个遮挡体引用。四、覆盖率按块内候选三角形投影面积的并集除以块面积计算，重叠部分只算一次，不是逐个三角形与阈值比较。五、BakeCell 遇到未知块报 ErrUnknownCell，覆盖率低于 MinCoverage 时整块丢弃并返回 Discarded 标记。六、整块因覆盖率不达标被丢弃后，旧版本遮挡数据必须同时作废，VersionHash 不得再返回过期哈希。七、烘焙成功要给出块内三角形数、覆盖率与版本哈希，同一内容重复烘焙哈希一致，内容变化哈希必须变化。八、BakePending 受 MaxCellsPerBake 约束，超出预算的块留到下一批，Skipped 如实报数，不能把没烘的块标成完成。九、InvalidateByMaterial 把使用该材质的块标脏，未知材质报 ErrUnknownMaterial，脏块列表与块列表都按 X 再 Y 升序。十、VersionHash 对未烘焙块报 ErrNotBaked，对已丢弃块报 ErrCellDiscarded，CandidateCount 返回块内候选数。十一、同一块重复提交相同的三角形不得重复计数，去重键是三个顶点的坐标与材质；十二、Snapshot 与 Restore 要保住候选、烘焙结果、丢弃表与脏标记，负载损坏报 ErrBadSnapshot 且不动内存。

硬约束：单批三角形数与单次烘焙块数都是硬上限，先塞后清不算数；块列表、脏块列表与丢弃列表的排序不能依赖 map 遍历顺序，同一输入任意顺序提交结果一致；进程重启后从快照恢复，版本哈希与脏标记原样还原。

验收：go test ./... 的 15 条用例全过，不能改测试、不能跳过、不能按固定哈希写死；重启恢复与并发提交自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-b-04`
- 实现文件：`occluder_bake_planner.go`
- 主要类型：`OccluderBakePlanner`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 测试规模：`tests/test_occluder_bake_planner_test.go` 共 15 条用例
