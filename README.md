# 高速移动体扫掠碰撞

## 题面

新赛季把箭矢和冲刺速度整体上调后，薄墙和栅栏开始被直接穿过去：帧率低的时候最明显，明明撞上了墙，角色却出现在墙后面，回放里连一次接触都没有。把大位移拆成小步能挡住，但拆多细、撞上之后停在哪里，客户端和服务端算出来的结果又对不上，复活点偶尔还会把人塞进墙里。更麻烦的是薄栅栏两侧的判定框只有一层，角色贴着边走时偶尔会被弹到另一侧。落点在 gsb/batch49-go-game-a-13 的 swept_collision_solver.go，类型 SweptCollisionSolver，需要在现有签名上把行为补齐。
公开入口：NewSweptCollisionSolver(Options) *SweptCollisionSolver、AddBody(b Body) error、RemoveBody(id string) error、Move(id string, delta Vec3, dtMs int) (MoveResult, error)、Sweep(from, to AABB, dtMs int) (Hit, bool, error)、Substeps(distance, minThickness float64) int、Body(id string) (Body, bool)、Position(id string) (Vec3, bool)、Velocity(id string) (Vec3, bool)、Blocked(id string) bool、Snapshot() ([]byte, error)、Restore([]byte) error。
要落到的行为：1) AddBody 校验空 id、非正半长、重复 id 与容量上限，静态体单独标记；2) Sweep 对盒体求最早接触，返回零到一之间的接触比例、接触体、法线与接触点，没有接触时明确返回未命中，扫掠按整体位移求交，不能只看终点是否重叠；3) AABB 的 min 超过 max 报错，dtMs 非正报错；4) Substeps 的第二个参数是最小厚度，等于 body 三个半长中最小者的两倍，步数按距离除以该厚度的一半向上取整，至少一步，封顶 MaxSubsteps；5) Move 按子步推进，每步做扫掠，命中后停在接触点前 epsilon 处，本帧标记 Blocked，每个子步只处理该步内的最早接触，剩余位移丢弃；6) 无遮挡时一百毫秒一次与两次五十毫秒的终点误差不超过 epsilon；7) 回退到上一合法位置时速度不清零，下一帧仍按原速度尝试；8) 同一时刻有多个接触时按接触法线字典序取第一个，不能依赖遍历顺序；9) 子步上限用尽后的阻断标记只影响本帧，下一次移动重新尝试；10) 静态体不能移动，未知体与非正时间步各自报错；11) 快照保存位置、速度、上一合法位置与阻断标记，恢复后的移动与重启前连续，上一合法位置在每次成功移动后更新；12) 同一 body 的移动串行，不同 body 可以并行，查询与移动不能互相阻塞，body 数量是硬上限。
硬约束：同一 body 的 Move 与快照必须串行，任何时刻都不能看到半移动的位置；快照是全量状态，恢复要么整体成功要么保持原状；子步数与 body 数都是资源上限，超限按上限处理而不是无限细分；碰撞只与静态盒体结算，动态体之间的推挤不在范围内；并发移动、不同帧率与重启恢复请自行构造验证。
验收：go test ./... 的 15 条用例全过，测试不得修改、跳过或按固定输入写死；只做本地实现，交付源码、测试记录与终止位置和法线的取法说明。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-a-13`
- 实现文件：`swept_collision_solver.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`SweptCollisionSolver`
