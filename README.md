# 漏洞实例跨扫描身份延续：行号漂移容忍、修复重开状态机与误合并拆分

## 题面

代码扫描平台换了解析器之后，同一批漏洞在两轮报告里换了行号，值班同事按行号对账，把老问题当成新问题、把同一条报了两遍；已经修复的问题下一轮又出现，报表里却仍算在已修复；两个扫描器报了同一处缺陷被合并成一条，想拆回去时历史状态全乱了。落点 gsb/batch49-go-safety-c-10 的 finding_identity_ledger.go，类型 FindingIdentityLedger，需要把跨扫描的身份延续、修复与重开状态机、合并与拆分规则固定下来。接口保持：NewFindingIdentityLedger(Config) *FindingIdentityLedger、Identity(raw RawFinding) string、Resolve(raw RawFinding) (Finding, error)、Ingest(scanID string, raw []RawFinding) ([]Event, error)、Split(findingID string, keep []string) error、Ignore(findingID string) error、Get(id string) (Finding, bool)、Findings() []Finding、Events() []Event、Stats() Stats、Rounds() int、Snapshot() ([]byte, error)、Restore([]byte) error。

要修：一、Identity 把规则标识、路径、行号与语义指纹按这个顺序用竖线连接，作为比较用的原始身份，来源不参与。二、批内任一原始记录缺来源、规则、路径、语义指纹，或行号小于一，整批报 ErrBadRaw 并保持零副作用；空扫描号报 ErrBlankScanID。三、身份解析优先级：先找规则、路径、指纹与行号全同的条目；找不到再找同规则同路径同指纹且行号差不超过 LineDrift 的条目；仍找不到才新建。四、新建条目编号形如 fi- 加八位十六进制，只由原始身份决定；同一份原始记录重复上报必须落到同一条目。五、首次出现记 new，后续轮次记 seen 并把状态从 new 推到 open；本轮没再出现的 open 与 new 条目在整批处理完后改为 fixed 并各写一条 fixed 事件。六、已修复条目在后续轮次以相同语义指纹再次出现时判为 reopened 并累加重开计数，不能当成新条目。七、同一文件内两个相邻但语义不同的缺陷不得因为语义指纹距离接近就并成一条，指纹编辑距离只在同规则同路径且行号接近时用于兜底重绑。八、一条条目记录全部来源，来源按名字升序去重；同一次扫描内不同来源报同一身份时合并到一条并写 merged 事件，来源计数逐条累加。九、Ignore 把条目置为 ignored 并写一条 ignored 事件；已忽略条目在后续扫描中不产生事件、不参与修复判定，重复忽略幂等。十、Split 只能拆出部分来源，保留集必须非空且是原有来源的真子集，否则报 ErrBadSplit，未知条目报 ErrNotFound；拆出的实例编号为原编号加连字符与四位十六进制后缀。十一、拆分后原条目保留剩余来源，拆出的实例按保留来源重新计算身份并保留各自来源，原条目的历史状态事件按来源归属拆散，不得整份复制给每个实例。十二、快照保存轮次、条目、来源与事件序号；Restore 整体替换，损坏载荷报 ErrBadSnapshot 且不动现有状态。

硬约束：并发 Ingest 串行裁决，同一原始记录在任意并发下只能对应一个编号，事件序号不得重复；一批要么整体生效要么完全不生效；身份、状态与序号跨重启恢复。

验收口径：go test ./... 15 条全过，测试不得修改、跳过或按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-c-10`
- 实现文件：`finding_identity_ledger.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`FindingIdentityLedger`
