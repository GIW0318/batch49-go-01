# 漏洞修复批次的动作聚合、预算约束与冲突拆分

## 题面

月度补丁窗口只剩一次，漏洞平台导出的清单里同一个升级动作能同时消掉三四条漏洞，但运维按条目逐条排期，把同一组件的两个不同目标版本排进了同一个窗口，结果后一个把前一个覆盖掉；预算算到最后还超了，也说不清哪几条是被预算挡住、哪几条是被窗口数挡住。请落在 gsb/batch49-go-safety-e-15 的 remediation_batch_selector.go，把 RemediationBatchSelector 做成能聚合、能排期、能交代阻挡原因的批次选择器。

每条 Finding 带 ID、组件、严重度与到期时间；每个 Action 带 ID、组件、目标版本、前置步骤、成本与它消除的漏洞清单；Budget 给出总成本上限与批次数上限。关键签名：NewRemediationBatchSelector() *RemediationBatchSelector、Add(f Finding, actions []Action) error、Group() []FixGroup、Select(budget Budget) ([]Batch, error)、Coverage(bs []Batch) (int, error)、Conflict(a, b Action) bool、Apply(batch Batch) error、Uncovered() []BlockedFinding、Findings() []Finding、Actions(component string) []Action、Fixed(id string) bool、Snapshot() ([]byte, error) 与 Restore([]byte) error。

要修的地方：

一、Add 校验漏洞 ID 与组件非空（ErrBadFinding）、ID 不重复（ErrDuplicateFinding）；动作必须与漏洞同组件且在 Fixes 里点名该漏洞，否则报 ErrActionMismatch。

二、同一动作 ID 以相同定义重复登记视为同一条，定义不同按冲突处理，不得静默覆盖。

三、Group 按组件聚合，组件名、漏洞 ID、动作 ID、目标版本四个列表都升序，目标版本去重。

四、Conflict 的判定：同组件但目标版本不同算冲突，共享同一个非空前置步骤算冲突，其余不冲突。

五、Select 只能使用仍开放漏洞对应的动作，按覆盖漏洞数降序、所覆盖漏洞的最早到期时间升序、动作 ID 升序依次排布。

六、一个批次内同一组件只能有一个目标版本；互不冲突的动作尽量并入已有批次，放不下才开新批次；批次里的动作按 ID 升序写出，Covers 同样升序去重。

七、预算为硬顶：总成本不得超过 MaxCost，批次数不得超过 MaxBatches；放不下的漏洞写进 Uncovered，并标明是被 budget 还是 batch_limit 挡住。

八、一个动作同时修多条漏洞时按漏洞条数计覆盖；其中一条已在别处被修复的，只按仍开放的漏洞计。

九、Coverage 统计给定批次消除的仍开放漏洞条数，同一漏洞被多个动作覆盖只算一次；批次里出现未登记的动作报 ErrUnknownAction。

十、Apply 把批次内动作消除的漏洞标为已修复，重复提交同一批次幂等，Findings 不因修复而消失；Fixed 能查到单条状态；批次里出现未登记的动作报 ErrUnknownAction。

十一、两个批次共用同一组件的不同目标版本属于冲突，必须拆到不同窗口而不是后者覆盖前者；预算不足以覆盖全部时优先选覆盖数高的批次，同覆盖数下按最早到期优先，结果与输入顺序无关。

十二、Snapshot 与 Restore 要保住漏洞、动作与已修复状态，损坏载荷报 ErrBadSnapshot 且不动现网；Add、Select、Apply、Snapshot 之间串行化。

验收口径：go test ./... 15 条必须全过；不得修改测试、跳过用例或按固定输入写死；并发与恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-e-15`
- 实现文件：`remediation_batch_selector.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`RemediationBatchSelector`
