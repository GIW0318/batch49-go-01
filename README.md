# API key 泄露响应：证据分级、渐进吊销与替换密钥预置

## 题面

安全邮箱收到一封匿名举报信，附件里贴着我们对外 SDK 的 API key 明文和几段调用记录截图。值班同学按老经验直接把这把 key 从配置中心删掉，结果三个对接业务方同时掉线；更乱的是第二封举报指向同一把 key，值班同学又开了一个事件，交接时谁也说不清哪条证据已经处理过、哪条还没看。请在模块 gsb/batch49-go-safety-a-26 的 api_key_leak_responder.go 中补全类型 ApiKeyLeakResponder，公开签名保持不变：NewApiKeyLeakResponder(Limits) *ApiKeyLeakResponder、Report(keyID, source string, confidence Confidence, evidence string) (Incident, error)、Triage(incidentID string) (Action, error)、ShadowRevoke(incidentID string) error、HardRevoke(incidentID, actorID string) error、Restore(incidentID, actorID string) error、PrepareReplacement(incidentID string) (IssuedKey, error)、IncidentState(incidentID string) (string, error)、Incidents(keyID string) []Incident、Replacements(keyID string) []IssuedKey、KeyUsable(keyID string) bool、Snapshot() ([]byte, error)、LoadSnapshot([]byte) error。

复盘会定了两条底线：证据链不能丢，key 也不能因为一次误报被永久废掉；同时值班同学人力有限，事件表不能无限长。已经查实的偏差有这些：

1. keyID 为空、来源为空或置信度不认识时没有拒绝，脏数据直接进了事件表，这里要分别返回 ErrBlankKeyID 与 ErrBadConfidence。
2. 举报进来后事件要停在疑似态，带自增序号、来源与证据；证据按到达顺序保留，不能只留最后一条。
3. 低置信度只做监视、不惊动所有者；中置信度升级给人工；高置信度直接影子吊销并通知所有者，这三档不能混。
4. 对已关闭事件反复 Triage、或跳过影子吊销直接硬吊销，都返回 ErrInvalidState；影子吊销重复调用应幂等成功。
5. 硬吊销必须带操作人，空操作人返回 ErrBlankActor，并且只能从影子吊销态进入，人工确认这一步不能被程序绕过。
6. 硬吊销前要预置一把不激活的替换密钥，重复预置返回同一把，不能每次生成新的；未吊销时预置返回 ErrInvalidState。
7. 误报恢复要记录操作人并保留全部证据，只能从已吊销态进入，其它状态返回 ErrInvalidState。
8. 受管范围内事件总数与替换密钥总数都是硬上限，超限分别返回 ErrIncidentCap 与 ErrReplacementCap，不许悄悄淘汰旧记录。
9. 同一把 key 的多条举报要合并成一条事件，不能按举报次数重复升级。
10. 影子吊销期间这把 key 的请求一律拒绝，但必须保留恢复入口，不能提前删掉 key 记录。
11. 误报恢复后同一把 key 再被举报，要按新事件重新计数，不能复用已关闭事件的证据。
12. 进程重启后事件态、吊销标记、替换密钥与序号都要延续；损坏快照返回 ErrBadSnapshot 且不动现网数据。

硬约束：并发对同一事件预置替换密钥只能产出一把，多路调用返回同一个编号；快照与判定互斥，读到的只能是全量旧状态或全量新状态；事件表与替换密钥表受上面两条上限硬顶，不允许按举报次数无限增长。

验收：go test ./... 的 15 条用例全过，测试不得修改、跳过或按固定输入写死；并发预置与重启恢复自行验证。只做本地实现，交付源码与跑测记录。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-a-26`
- 实现文件：`api_key_leak_responder.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`ApiKeyLeakResponder`
