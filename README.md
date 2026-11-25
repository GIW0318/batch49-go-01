# mTLS 客户端证书撤销传播与握手准入

## 题面

双向 TLS 网关做过一次证书吊销演练之后，客户端证书换新也没用：已经吊销的序列号还能完成握手，另一批本该正常的新证书反而被拒；值班日志显示吊销名单只在部分终止节点生效，节点之间对新旧名单的判断互相矛盾，名单更新后既有的长连接也一直挂着不断开。改动落在模块 gsb/batch49-go-fix-a-11 的 mtls_revocation_gate.go，类型 MtlsRevocationGate 负责序列号与签发者两级撤销、吊销纪元向终止节点的传播、握手准入与会话排空。方法签名保持：NewMtlsRevocationGate(cfg Config) (*MtlsRevocationGate, error)、PublishRevocation(rec Revocation) (uint64, error)、RevokeIssuer(issuer string) (uint64, error)、Epoch() uint64、Admit(hs Handshake) (Decision, error)、SyncWorker(workerID string, epoch uint64) error、AppliedEpoch(workerID string) uint64、RegisterSession(s Session) error、SessionsFor(serial string) []string、DrainSessions(serial string) ([]string, error)、Revoked(serial string) bool、Snapshot() ([]byte, error)、Restore(data []byte) error。

具体要求如下。

一、MaxRevoked 与 MaxSessionsPerSerial 都必须为正，任一非正报 ErrBadConfig；MaxLag 允许为 0，表示节点必须紧跟当前纪元。

二、PublishRevocation 的 serial 与 issuer 全空白报 ErrBadRevocation；新增一条真实撤销时纪元加一，重复撤销同一序列号幂等返回当前纪元，不再加一。

三、已撤销序列号总数受 MaxRevoked 硬顶，超限报 ErrRevocationLimit 且不写入、不加纪元；重复撤销已经存在的序列号不受这条上限影响。

四、RevokeIssuer 对同一签发者重复调用幂等，只占一条；签发者撤销不点名具体序列号，Revoked 只回答序列号是否被点名。

五、Admit 先看序列号撤销、再看签发者撤销，命中都返回拒绝且原因为 revoked；证书 NotAfter 不晚于当前时间返回拒绝且原因为 expired。

六、节点视图落后当前纪元超过 MaxLag 时一律拒绝并给出 stale_view，即使证书本身没有被撤销，这是防止旧名单放行的兜底。

七、SyncWorker 只接受 0 到当前纪元的取值，往前报 ErrEpochRegression 且不改动视图，超前报 ErrEpochFuture；workerID 空白或 NotAfter 非正的握手报 ErrBadHandshake。

八、Admit 通过时返回 allow、原因为 ok 并带上当前纪元；被拒的决策同样带上当前纪元，便于节点记账。

九、RegisterSession 校验 sessionID 与 serial，同一 sessionID 重复登记幂等；每序列号的活跃会话数受 MaxSessionsPerSerial 硬顶，超限报 ErrSessionLimit。

十、SessionsFor 按 sessionID 升序返回；DrainSessions 返回并移除该序列号的全部会话，对未被撤销的序列号返回空且不报错，重复排空同样幂等。

十一、签发者级撤销要在快照恢复后继续命中新出现的序列号；快照覆盖撤销集合、签发者集合、纪元、节点视图与会话表，损坏快照报 ErrBadSnapshot。

十二、把纪元更低的快照恢复到已有更高纪元的实例上必须报 ErrBadSnapshot；恢复后继续撤销要接着快照的纪元往上走，不能回到旧值。

硬约束：Admit、SyncWorker 与 PublishRevocation 会被大量握手协程和同步协程并发调用，准入判定只能看到完整的撤销集合与完整的节点视图，不能出现半更新；进程重启后凭快照恢复，已撤销序列号与会话表原样接续，节点视图不回退；撤销集合、每证书会话数与会话表都是硬顶，越线只能拒绝，不能静默扩容。

验收方式：go test ./... 的 15 条用例必须全过，不得改测试、跳过或按固定证书写死；并发准入与重启恢复各跑一次并保留输出；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-fix-a-11`
- 实现文件：`mtls_revocation_gate.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`MtlsRevocationGate`
