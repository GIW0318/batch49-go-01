# 蒙皮网格保守包围盒维护

## 题面

坐骑换姿势后，远处角色的阴影忽大忽小，个别帧整只角色被剔除，地面只剩一块空洞；同一根骨骼一帧内被多处逻辑改写时，包围盒有时停在旧位置。范围在 gsb/batch49-go-game-b-13 的 skinned_bounds_updater.go，类型 SkinnedBoundsUpdater 负责在骨骼动画推进后维护蒙皮网格的保守包围盒，供剔除与阴影使用。动画每帧只把被改动的骨骼标脏，重算时按骨骼归属的网格收集影响盒，因此脏集合与网格表要分开维护。

公开接口：NewSkinnedBoundsUpdater(opts BoundsOptions) (*SkinnedBoundsUpdater, error)、Bind(meshID string, bindBounds AABB, influence InfluenceMap) error、SetPose(skelID, bone string, pose BonePose) error、MarkBoneDirty(skelID, bone string) error、Recompute(skelID string) RecomputeReport、Bounds(meshID string) (AABB, bool)、CullBounds(meshID string) (AABB, bool)、StaleCount() int、BeginFrame()、MeshCount() int、Snapshot() ([]byte, error) 与 Restore([]byte) error。相关结构体为 Vec3、AABB、BonePose（平移加逐轴缩放）、InfluenceBox、InfluenceMap（骨骼集与局部盒表）、BoundsOptions 与 RecomputeReport（脏骨骼、延后骨骼、重算网格、跳过数）。

要修好的行为：帧预算非正、外扩边距为负或网格上限非正报 ErrInvalidOptions；网格 ID 为空报 ErrEmptyMesh；包围盒最小值大于最大值报 ErrInvalidBounds；重复绑定同一网格报 ErrDuplicateMesh；未知网格查询不得返回可用包围盒；包围盒由各骨骼局部盒按当前姿势做平移与逐轴缩放后的并集得到，没有影响盒的网格退化为绑定盒；骨骼姿势按平移加逐轴缩放解释，未设置过的骨骼默认为单位变换；同一骨骼一帧内多次标记只重算一次；每帧预算只按骨骼计数，超出预算的骨骼留在脏集合里并延后，其网格标记为陈旧；下一帧继续处理延后的骨骼，处理完陈旧标记归零；脏骨骼按骨骼名升序处理，同一输入多次重算结果一致；未知骨骼或未知骨骼集报 ErrUnknownBone。

硬约束：陈旧包围用于剔除时可以额外外扩，但外扩值不得写回包围表，否则逐帧累积会让剔除范围越缩越小；骨骼被延后到下一帧期间若再次被改动，脏标记不能被清掉；零缩放或含 NaN 的退化骨骼要整体跳过，不能参与包围合并；重算只读写本骨骼集下的网格，不能顺带刷新别的骨骼集；快照要保存网格、姿势、脏集合与陈旧标记，恢复后继续重算与中断前一致，损坏快照报 ErrBadSnapshot。

姿势未设置过的骨骼保持单位变换，重算不会改写调用方传入的绑定盒；Recompute 的返回值只统计本次处理范围内的骨骼与网格，不把别的骨骼集算进来。同一网格被多根脏骨骼命中时只重算一次，重算后该网格的陈旧标记清掉。绑定时骨骼集为空按退化为绑定盒处理，不允许运行期补绑。

验收：go test ./... 15 条全部通过，测试文件不得修改、跳过或按固定输入写死；预算延后、下一帧续算与并发标记自行验证，逐帧打印脏集合与陈旧数；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-b-13`
- 实现文件：`skinned_bounds_updater.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`SkinnedBoundsUpdater`
