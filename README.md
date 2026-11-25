# NetworkPolicy 选择器求值与允许矩阵编译

## 题面

支付集群上线网络策略后，账单工作负载的出向连接被整段掐断，而未纳入策略的日志采集器反而一直能访问数据库；同一份策略在不同节点上编译出的允许矩阵顺序也不一致，回滚对比时看不出到底改了哪几条。监控面板上入站放行率忽高忽低，值班同学怀疑编译与求值读到了不同版本的规则表，回滚窗口内甚至出现过旧规则短暂复活的告警。落点在 gsb/batch49-go-safety-b-11 的 netpol_compiler.go，类型 NetPolCompiler，需要把策略、工作负载与流量求值这条链路做完整。入口 NewNetPolCompiler() *NetPolCompiler，编译 Compile(policies []Policy, workloads []Workload) (*Matrix, error)，求值 Evaluate(workloadID string, flow Flow) (Decision, Reason)，矩阵差异 Diff(old, next *Matrix) []Change，快照 Snapshot() ([]byte, error) 与 Restore([]byte) error，另外提供 WorkloadIDs() []string、Entries(workloadID string) []Entry、Diagnostics() []Diagnostic、PolicyNames() []string、ValidatePolicies(policies []Policy) error 和 SelectorMatches(selector, labels map[string]string) bool。

需要落实的判断与边界：1) 未被任何策略选中的工作负载保持默认放行，原因码 default_allow，规则命中的原因要带策略名与规则下标；2) 被策略选中后，即使没有任何规则命中也要拒绝，原因码 no_rule，不得回落到默认放行；3) 选择器要同时匹配命名空间标签与工作负载标签，缺一不可，空选择器匹配全部标签集；4) ipBlock 允许网段内的地址，except 子网只从本策略的允许集合里扣除，不得连带扣除其它策略放行的地址；5) 端口区间在编译期归一成起止对，单端口视为起止相同；6) 命名端口按工作负载声明的端口映射解析成数字端口，解析不了的要留下诊断，不能当成零端口放行全部端口；7) 同一工作负载被多条策略选中时规则取并集，但选择关系一旦成立就不再享受默认放行；8) 编译结果的工作负载列表与规则条目必须按稳定键排序，两次编译不因输入顺序不同而变化；9) Diff 按条目比较，新增记 added、消失记 removed，无差异返回空；10) 策略名为空或重复、工作负载标识为空或重复都要报错，错误值可判定，ValidatePolicies 对空名与起止倒置的端口区间分别报 ErrBlankPolicy 与 ErrInvalidPolicy；11) 快照要能恢复编译结果与诊断信息，损坏快照返回 ErrBadSnapshot 且不动现有状态；12) 求值要区分入站与出站，协议不匹配时同端口规则也不得放行，未编译或编译失败时对任何工作负载都返回拒绝，原因码 unknown_workload。

硬约束：并发调用 Compile 与 Evaluate 时，读到的矩阵必须是完整的一份，不能出现半更新的规则表；同一份输入重复编译，矩阵条目逐字段一致，排序不得依赖 map 遍历；快照与编译互斥，恢复成功前旧矩阵继续服务。

验收口径：go test ./... 15 条用例全过，不得修改测试、跳过用例或按固定输入写死；并发一致性与快照恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-b-11`
- 实现文件：`netpol_compiler.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`NetPolCompiler`
