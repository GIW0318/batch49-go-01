# batch49-go-safety-d-06

模块路径 `gsb/batch49-go-safety-d-06`，源码文件 `audit_provenance_verifier.go`，包名 `task`，主类型 `AuditProvenanceVerifier`。

## 题面

审计接收端扩容后，值班同学发现两张报表对不上：同一条登录事件在“已接收”里出现两次，另一台采集器整段日志消失三天却没有任何人收到通知，排查时只能看到一句笼统的校验失败。值班同学只能按来源逐个手工对账，缺一条就意味着一整段操作没有留痕，事后无法自证。落点是 gsb/batch49-go-safety-d-06 的 audit_provenance_verifier.go，类型为 AuditProvenanceVerifier。采集器上报的事件带来源、启动标识、序号、时间戳、签名与摘要，接收端要按来源维护高水位，并把重复、乱序、跳号、来源不符、签名不符分开报出来。

公开签名（不得改动）：
NewAuditProvenanceVerifier(cfg VerifierConfig) *AuditProvenanceVerifier
Register(src Source) error
Verify(ev Event) (Verdict, Reason, error)
Watermark(src string) (uint64, error)
Gaps(src string) []Gap
Isolate(src string, reason string) error
Isolated(src string) (bool, string)
Credit(src string) (int, error)
Owner(eventID string) (string, bool)
Snapshot() ([]byte, error)
Restore(payload []byte) error

逐条要落地：
1) 来源标识或启动标识为空时注册报 ErrBadSource，同一标识重复注册报 ErrSourceExists；未注册来源的事件返回 VerdictSourceMismatch 与 ErrUnknownSource，水位不动。
2) 事件签名与注册时登记的密钥不一致，返回 VerdictSignatureMismatch，既不推进水位也不扣信用。
3) 第一条序号为 1 的事件被接受，水位推进到 1，信用保持配置值；配置里信用和跳号窗口留空时分别取 5 与 3。
4) 同来源、同序号、同摘要再次到达判 duplicate，原因记 duplicate_seq，水位与信用都不变。
5) 同序号但摘要不同判 duplicate，原因记 duplicate_conflict，扣一点信用。
6) 序号低于水位判 out_of_order，原因记 below_watermark，水位不动。
7) 跳号在窗口内记 gap_pending，落一条从水位加一到 seq 减一的缺口并推进水位，不扣信用；跳号超出窗口升级为 gap 并扣一点信用。
8) 采集器重启后序号从零或一起算，要凭启动标识切换到新基线，不能把整段新序号判成大规模重复，旧基线的水位与缺口要留着可查。
9) 信用扣到零立即隔离来源，原因记 credit_exhausted，此前缺口证据保留，后续事件返回 VerdictIsolated 与 ErrSourceIsolated；Isolate 可人工隔离并记下原因。
10) 同一事件标识只能有一个登记归属方，Owner 能读回归属；另一个来源再报同一标识要判来源不匹配，且不改动已有归属。
11) 时间戳只作展示参考：跨来源时间偏差超限只影响时间序判定，不能据此否掉序号连续性结论，也不能因此拒收事件。
12) 快照要恢复注册来源、水位、信用、缺口与隔离状态，恢复后继续核验能接着推进；非法快照报 ErrBadSnapshot。

硬约束：同一来源的核验必须串行推进水位，并发重放同一事件只能有一次 accept；快照与核验并发时不得出现半个状态；每个来源的缺口条数按配置上限封顶，超出淘汰最旧。缺口记录与隔离原因要能按来源导出供事后复盘；处理每条事件时水位与信用必须同时前进或同时不动，不允许出现水位已推进而信用没扣的中间态。

验收：go test ./... 的 15 条用例全过，测试不许改、不许跳过、不许按固定输入写死；并发重放与重启恢复自行验证；只做本地实现，交付源码、跑测记录与故障说明。

## 运行方式

在模块根目录执行：

```bash
go test ./...
```

测试位于 `tests/test_audit_provenance_verifier_test.go`，属同一个模块，用例通过 `impl "gsb/batch49-go-safety-d-06"` 引用实现。
评测口径为 15 条用例全部通过，测试文件不得修改、跳过或按固定输入写死。
