# 缓解措施封锁能力边后的剩余链路与最小切割

## 题面

演练复盘会上两份报表对不上：控制台显示“关键链路已被缓解措施完全封死”，同一目标在复测里却还能走通三条彼此独立的通路；值班同学把几条措施手工叠加后，残余链路条数在两次刷新之间来回跳，谁也说不清到底还差哪一刀。请落在 gsb/batch49-go-safety-e-11 的 mitigation_cut_analyzer.go，把 MitigationCutAnalyzer 做成可信的残余通路与最小切割计算器。

分析器维护一张由原子能力边构成的有向图，构造时给出起点、默认目标与全部边，每条边有唯一 ID，一条链路用节点序列与边 ID 序列共同表示。关键签名：NewMitigationCutAnalyzer(source, goal State, edges []Edge) *MitigationCutAnalyzer、NewMitigationCutAnalyzerWithLimit(source, goal State, edges []Edge, maxCandidates int) *MitigationCutAnalyzer、Block(m Mitigation, edges []string) error、Apply(set []Mitigation) error、Paths(from, goal State) ([]Chain, error)、Residual(goal State) ([]Chain, error)、MinCut(from, goal State, candidates []Mitigation) ([]Mitigation, error)、Impact(m Mitigation) int、BlockedEdges() []string、Snapshot() ([]byte, error) 与 Restore([]byte) error。

要修的地方：

一、Paths 只列不重复节点的简单通路，按边 ID 序列的字典序升序返回；起止节点必须存在于图中，否则报 ErrUnknownState；目标不可达时返回空列表而不是报错。

二、Residual 在已施加阻断的图上重算到目标的剩余通路，顺序与 Paths 一致。

三、Block 把一条措施施加到指定边，阻断是叠加的：同一措施重复施加到同一条边不重复计数，施加到不同边按边各计一次；边不在图中报 ErrUnknownEdge，边不在这条措施的覆盖范围报 ErrNotCovered。

四、缓解措施可能只封住某条边的一个前置条件，这类边不算被切断，仍要出现在剩余通路里；只有列在 Covers 里的边才按完全阻断处理。

五、Impact 相对默认目标计算：单独施加该措施后，当前剩余通路里被切断的条数；同一条通路上被同一措施覆盖多条边只算一次。

六、MinCut 只从候选里选，返回能切断起点到目标全部通路的最小措施集合；同等大小取代价更低的一组，大小与代价都相同时按名称升序取第一组，重复求解结果必须一致。

七、候选的覆盖边必须都在图中，候选数超过上限报 ErrCandidateLimit；搜索必须在候选上限内完成，不做指数级穷举。

八、没有任何候选组合能切断全部通路时返回 ErrUncuttable，残留通路仍可通过 Residual 查询，不得静默返回空集合。

九、互相点名冲突的候选不能同时进入切割集；Apply 收到互斥的一组，要在改动任何状态之前报 ErrConflict。

十、Apply 按传入定义整份施加，中途失败不得留下半份阻断；重复施加同一集合结果不变。

十一、Snapshot 与 Restore 要保住已施加的阻断与已登记的措施，恢复后 Residual 与 BlockedEdges 与快照前一致；损坏载荷报 ErrBadSnapshot 且不动现网。

十二、Block、Apply、MinCut 与 Snapshot 之间要串行化：并发施加与求解只能看到完整的旧状态或完整的新状态，不能读到半份阻断。

验收口径：go test ./... 15 条必须全过；不得修改测试、跳过用例或按固定输入写死；并发与恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-e-11`
- 实现文件：`mitigation_cut_analyzer.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`MitigationCutAnalyzer`
