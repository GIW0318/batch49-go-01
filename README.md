# 渲染子图实例化与跨帧历史代际仲裁

## 题面

新渲染管线切到子图实例化后，远景特效开始成片闪烁：同一盏灯的上一帧阴影在两帧之间来回跳，回放录屏里能清楚看到画面被拉回一帧；美术把同一份配置重复导入一次，整帧凭空多出一批 pass，帧时间直接翻倍；更麻烦的是某台机器展开到一半报参数错误，之后整帧都渲染不出来，只能重启进程。落点模块 gsb/batch49-go-game-b-01，实现文件 render_subgraph_instancer.go，核心类型 SubGraphInstancer，公开签名保持：NewSubGraphInstancer(InstancerOptions) *SubGraphInstancer、RegisterSubGraph(name string, def SubGraphDef) error、Instantiate(graphID, subGraph string, params map[string]string) (InstanceID, error)、BindHistory(instanceID InstanceID, resource string) (HistoryHandle, error)、Expand(graphID string) ([]PassNode, error)、CommitFrame(graphID string) error、RetireGeneration(gen uint64) error、Instances(graphID string) []InstanceID、PassCount(graphID string) int、CurrentGeneration() uint64、HandleAlive(h HistoryHandle) bool、Snapshot() ([]byte, error) 与 Restore([]byte) error。

要修的地方：一、注册校验，名称为空报 ErrBlankName，重名报 ErrDuplicateSubGraph，pass 列表为空报 ErrEmptyDefinition，任一 pass 名为空报 ErrBadPass；实例化时图 ID 为空报 ErrBlankGraph，子图未注册报 ErrUnknownSubGraph，必填参数缺失报 ErrMissingParam，传入未声明的参数报 ErrUnknownParam。二、同一图内参数集合完全一致的请求复用同一个实例，参数值不同则各自成实例；参数显式给出空字符串与整条参数缺省是两个不同的实例，前者按空值参与实例化，后者取声明里的默认值，归一化时不得合并。三、两个实例的参数一模一样，但绑定的历史资源属于不同代际时必须拆成两个实例，不能只按参数键合并。四、实例总数超过 MaxInstances 报 ErrInstanceLimit，已有实例保持原样。五、BindHistory 对未知实例报 ErrUnknownInstance，资源名为空报 ErrBadResource，同一实例重复绑定同一资源返回同一句柄。六、Expand 先按实例创建顺序、再按定义内的 pass 顺序展开，Ordinal 从零连续编号，节点里带实例 ID、子图名与输入输出。七、历史读写的 pass 没有绑定资源时 Expand 报 ErrUnboundHistory，本次展开整体不生效，PassCount 保持上一次成功展开的值。八、展开中途失败必须整帧回滚，已回滚实例绑定的历史句柄要一并释放，不能留下悬挂的代际引用。九、单帧 pass 数超过 MaxPassesPerFrame 报 ErrPassBudget，不允许半份展开。十、CommitFrame 成功后才把当前代际加一，新绑定句柄落在新代，未提交的帧不推进代际，空帧提交报 ErrEmptyFrame。十一、RetireGeneration 退役当前代报 ErrGenerationActive，退役从未出现过的代报 ErrUnknownGeneration，退役后该代句柄 HandleAlive 为假。十二、Snapshot 与 Restore 要保住注册表、实例参数、历史绑定与代际，负载损坏报 ErrBadSnapshot 且不动内存。

硬约束：同一图的实例化与展开串行提交，多协程同时实例化不得出现重复实例或半个实例；进程重启后从快照恢复，代际号、句柄编号与绑定关系原样还原；实例数与单帧 pass 数都是硬上限，先超后清不算数。

验收：go test ./... 的 15 条用例全过，不能改测试、不能跳过、不能按固定参数写死；并发实例化与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-b-01`
- 实现文件：`render_subgraph_instancer.go`
- 主要类型：`SubGraphInstancer`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 测试规模：`tests/test_render_subgraph_instancer_test.go` 共 15 条用例
