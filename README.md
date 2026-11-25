# 地形四叉树 LOD 选层

## 题面

游戏里的飞行坐骑掠过山谷时，远处山脊一会粗一会细，靠近镜头的地块偶尔整块不长草，把视距拉远再拉回来，同一片山头挑出的节点又变了。范围在 gsb/batch49-go-game-b-14 的 terrain_quad_selector.go，类型 TerrainQuadSelector 负责按视点与屏幕空间误差在地形四叉树上选出本帧要用的节点集合。四叉树按世界方形逐级四分，层级从 0 开始，误差表由美术按层级给出，选层只读误差表与视点，不改任何持久状态。

公开接口：NewTerrainQuadSelector(opts SelectorOptions) (*TerrainQuadSelector, error)、SetErrorTable(levels []GeomError) error、ErrorAt(level int) (float64, error)、Select(view View, budget int) (Selection, error)、Pin(quad QuadID) error、Unpin(quad QuadID) error、Pinned() []QuadID、LastSelection() Selection、Stats() SelectorStats、Snapshot() ([]byte, error) 与 Restore([]byte) error。结构体包括 Vec2、GeomError（层级与误差）、View（视点、视口高度、视场角、像素容差）、QuadID（层级与行列）、Selection（根、预算、选中块、未覆盖块）、SelectorOptions 与 SelectorStats。

要修好的行为：最大层级非正、世界尺寸非正、视口高度非正、视场角不在 0 到 180 度之间或像素容差非正报 ErrInvalidOptions；误差表为空报 ErrEmptyErrorTable，条目数必须等于最大层级加一且层级从 0 连续编号，否则报 ErrErrorLevelMismatch；误差为负报 ErrNegativeError，随层级下降报 ErrNonMonotonicError；节点预算非正报 ErrInvalidBudget，视场角、视口高度或容差非正报 ErrInvalidView；屏幕空间误差按几何误差乘视口高度再除以距离与半视角正切的两倍，距离取下界一；根节点始终在结果里，父节点被选中为叶时子节点不得单独出现；结果按层级、横坐标、纵坐标升序排列；节点数超过预算时保留前面若干叶并把剩余块逐条列进未覆盖列表，不允许静默丢弃；被选中的叶必须覆盖整块地形，相邻叶的层级差不超过一级；同一视点重复调用结果一致，换误差表后下一次选择立即按新表计算。

硬约束：相邻块层级差限制只约束最终结果，选层中间过程允许临时不一致；视点瞬移导致当帧全量重选时滞回计数要全部重置，不能保留旧计数；被 Pin 的块即使超出误差阈值也要保层，且不占节点预算；Pin 与 Unpin 只改 pin 集合，不立即触发选层；快照要保存误差表、Pin 集合与上一次选择，恢复后 ErrorAt 与 Pinned 的读数与中断前一致，损坏快照报 ErrBadSnapshot。

Selection 的 Root 固定为 0 层、0 行、0 列；未覆盖块只描述被预算挤掉的叶，不含已经覆盖或按 Pin 保层的块。选层不写任何持久状态，LastSelection 与 Stats 只在 Select 成功后更新，失败的调用保留上一次结果。误差表与 pin 集合的修改都按整份替换处理，不接受半份新半份旧。

验收：go test ./... 15 条全部通过，测试文件不得修改、跳过或按固定输入写死；预算裁剪、覆盖完整与恢复自行验证，覆盖面积用叶面积求和核对；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-b-14`
- 实现文件：`terrain_quad_selector.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`TerrainQuadSelector`
