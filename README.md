# batch49-go-safety-d-09

模块路径 `gsb/batch49-go-safety-d-09`，源码文件 `capability_demand_solver.go`，包名 `task`，主类型 `CapabilityDemandSolver`。

## 题面

权限收敛项目做了一半卡住了：安全同学拿到的建议能力集比实际需要大一截，另一些路径干脆没被覆盖，工作负载按建议改完上线还是被拒绝。安全同学怀疑建议集合是按访问条数机械堆出来的，看不出哪条访问支撑了哪个能力。落点是 gsb/batch49-go-safety-d-09 的 capability_demand_solver.go，类型为 CapabilityDemandSolver。输入是一段运行期访问轨迹，输出该工作负载所需的最小能力集合与画像建议。

公开签名（不得改动）：
NewCapabilityDemandSolver(cfg SolverConfig) *CapabilityDemandSolver
AddAccess(a Access) error
Solve() (Demand, error)
Diff(demand Demand, current Profile) (Diff, error)
Explain(cap string) []Access
Uncovered() []Access
Snapshot() ([]byte, error)
Restore(payload []byte) error
Count() int
Accesses() []Access
Reset() error

逐条要落地：
1) AddAccess 要求操作与路径都非空，否则报 ErrBadAccess；轨迹条数超过 MaxAccesses 报 ErrAccessLimit，已收下的访问不受影响。
2) open 带读标志映射 cap_read_file，带写标志映射 cap_write_file，读写都带时两个都要出现。
3) stat 映射 cap_stat_file，chmod、chown、exec 分别映射 cap_chmod、cap_chown、cap_exec，彼此不能合并成一条。
4) 读目录元数据与读目录内容需要不同能力，只看打开标志会把两者混为一谈：rdir 打开与 readdir 要落到 cap_read_dir，不能算成 cap_read_file。
5) Solve 返回按名字升序的能力集合，ByCap 里每个能力的路径去重后升序；同一路径多次访问只保留一条证据。
6) 轨迹里出现无法映射的访问时整份建议不可用：Solve 报 ErrUncoveredAccess 且 Caps 为空，Uncovered 按接入顺序列出这些访问。
7) 被拒绝的访问不得据此推导能力，否则会把攻击尝试写成需求；这类访问只进拒绝记录，不参与能力求解。
8) 同一路径先写后改属主时，第二次访问所需集合覆盖第一次，合并要看后置条件而不是简单取并集。
9) Diff 把需求与现有画像分开报 Missing 与 Extra，两边都去重升序；缺的能力不能靠额外能力抵掉。
10) Explain 按接入顺序返回支撑某个能力的访问；未知能力返回空而不是报错；空轨迹时 Solve 报 ErrEmptyTrace。
11) 快照要恢复轨迹与配置，恢复后 Solve 结果与快照前一致；非法快照报 ErrBadSnapshot；Reset 清空轨迹后可重新采集。
12) 增量追加访问后重新求解，结果与一次性喂入同一条轨迹相同，重复求解不改变轨迹。

硬约束：多路采集协程会同时 AddAccess，同时还有协程在调 Solve，读到的要么是追加前的完整轨迹要么是追加后的完整轨迹，不允许看到半条；轨迹条数上限是硬顶，超限拒绝而不是覆盖旧访问；进程重启后凭快照恢复整条轨迹，恢复前后求解结果一致。能力名与路径都按字节序比较，排序结果不随运行环境变化，同一份轨迹在不同进程里求解必须得到同一份建议；轨迹里的序号只用于说明先后，不参与能力判定。

验收：go test ./... 的 15 条用例全过，测试不许改、不许跳过、不许按固定输入写死；并发采集与重启恢复自行验证；只做本地实现，交付源码、跑测记录与能力映射口径说明。

## 运行方式

在模块根目录执行：

```bash
go test ./...
```

测试位于 `tests/test_capability_demand_solver_test.go`，属同一个模块，用例通过 `impl "gsb/batch49-go-safety-d-09"` 引用实现。
评测口径为 15 条用例全部通过，测试文件不得修改、跳过或按固定输入写死。
