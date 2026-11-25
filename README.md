# 网格减面 LOD 链的折叠规划与边界锁定

## 题面

远山 LOD 一切换就出洞，走近能看到地形缺口；河岸边的石头在第二级 LOD 里整块消失，贴图边缘还拉出长条；美术在边界上手动刷过一圈锁定标记，进程重启后锁定全部丢失，折叠继续往下跑，把边界吃掉了；同一份资源包在不同机型上表现还不一样，A 机远景完整，B 机近处塌掉一块；规划一张十万面网格要十几秒，编辑器直接卡死。落点模块 gsb/batch49-go-game-b-03，实现文件 mesh_decimation_planner.go，核心类型 DecimationPlanner，公开签名保持：NewDecimationPlanner(PlannerOptions) *DecimationPlanner、Plan(mesh Mesh, levels []LevelSpec) (Plan, error)、Collapse(level int) (LevelResult, error)、LockBoundary(edge EdgeID) error、Report() DecimateReport、Faces(level int) int、PendingLevel() int、LockedEdges() []EdgeID、CollapseOrder(level int) []EdgeID、Snapshot() ([]byte, error) 与 Restore([]byte) error。

要修的地方：一、Plan 校验，空网格报 ErrEmptyMesh，面引用了不存在的顶点或同一面里出现重复顶点报 ErrBadFace，边引用不存在的顶点、首尾相同或编号重复报 ErrBadEdge。二、各级目标面数必须为正、严格递减且不超过上一级面数，空列表或零目标报 ErrBadTarget，顺序不对报 ErrLevelOrder。三、边界、材质缝与 UV 缝上的边自动锁定，LockBoundary 可以追加手动锁定，未知边报 ErrUnknownEdge，还没有方案时调用一律按未知边处理，重复锁定幂等。四、折叠顺序按边代价也就是两端点距离升序，同代价按边 ID 升序，锁定边永远不进入顺序。五、材质缝两侧位置重合的顶点必须当成两个顶点，折叠时不得按坐标去重合并，否则缝合处会漏出背景。六、边界环在任意级别都不得减少顶点数，即使目标面数已经达成。七、每级要消去的面数等于当前面数减目标面数，可用的未锁定边不够时报 ErrInsufficientEdges，不得拿锁定边凑数。八、全部级别的折叠总数超过 MaxCollapseBudget 时报 ErrCollapseBudget，不截断。九、Collapse 必须按级别顺序推进，跳级或重复执行报 ErrLevelOutOfOrder，结果给出该级完成后的面数、折叠数与是否达标。十、同一网格与同一组级别重复 Plan，结果逐字段一致，不依赖 map 遍历顺序。十一、恢复折叠历史后继续时要还原已锁定的边界集合，不能重新从原始网格推导。十二、Snapshot 与 Restore 要保存网格、方案、已执行级别与手动锁定，负载损坏报 ErrBadSnapshot 且不动内存，恢复后从待执行的那一级继续。

硬约束：折叠历史与锁定集合跨重启保留，恢复后不得重放已经执行过的级别；单次规划与累计折叠数受预算硬顶，先超后清不算数；同一网格重复规划的结果必须逐字段一致，排序不能依赖 map 遍历；并发锁定同一批边时只认一次，不得把同一条边算成两份预算。

验收：go test ./... 的 15 条用例全过，不能改测试、不能跳过、不能按固定边号写死；重启续跑与并发锁定自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-b-03`
- 实现文件：`mesh_decimation_planner.go`
- 主要类型：`DecimationPlanner`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 测试规模：`tests/test_mesh_decimation_planner_test.go` 共 15 条用例
