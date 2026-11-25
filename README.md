# batch49-go-fix-a-15

模块路径：`gsb/batch49-go-fix-a-15`

包名：`task`

源文件：`audit_chain_truncation_guard.go`

测试目录：`tests/`

## 题面

季度合规抽检时，审计员把三台采集节点上的登录审计日志并排放在一起，发现其中一台的最后两条记录凭空消失，序号停在 1187，而另外两台已经写到 1189；单看本地文件，哈希链依然首尾相接，看不出任何破绽。安全组要求每条审计记录都进入链式签名，节点之间定期交换签名锚点，一旦出现尾部截断、同号不同摘要或历史回退，必须给出可定位的结论。改动落在 gsb/batch49-go-fix-a-15 的 audit_chain_truncation_guard.go，类型 AuditChainTruncationGuard。关键签名保持：NewAuditChainTruncationGuard(limits Limits, signer Signer) *AuditChainTruncationGuard、RegisterStream(id string) error、Append(stream, payload string) (Record, error)、Anchor(stream string) (Anchor, error)、IngestAnchor(a Anchor) error、Verify(stream string) (Verdict, error)、Gaps(stream string) ([]Gap, error)、RotateKey(next Signer) error、RetireKey(keyID string, afterSeq uint64) error、Snapshot() ([]byte, error)、Restore([]byte) error。

要修的点：
一、流 ID 空白报 ErrBlankStream，重复登记报 ErrDuplicateStream，签名器为空报 ErrNilSigner；未登记的流上做追加、校验或取锚点都报 ErrUnknownStream。
二、序号从 1 开始严格递增，空内容报 ErrEmptyPayload 且不占用序号；摘要只由流 ID、序号、前一条摘要与内容决定，不掺时钟、随机数或签名，两个实例按同样内容追加必须得到逐字节相同的摘要；单条流记录数受 MaxRecords 硬顶，写满报 ErrLimitExceeded 而不是覆盖最旧记录。
三、单条摘要变化必须向后传导：中间某条内容被改写后，该条及其后所有记录的摘要都要不同。
四、Anchor 以当前 head 生成签名锚点，流里没有记录时报 ErrNoRecords；锚点生成后同时进入本地可信锚点表。
五、IngestAnchor 校验字段完整性、密钥是否受信与签名是否有效，分别报 ErrAnchorInvalid、ErrUnknownKey 与 ErrSignatureInvalid；同一锚点重复导入必须幂等。
六、Verify 用可信锚点的最高序号作为 trustedHead：本地 head 小于它时报 truncated 并给出从 head+1 到 trustedHead 的 tail_truncation 缺口；锚点摘要与本地同号记录不一致、或同号出现两个不同摘要时报 fork，冲突位置要定位到具体序号。
七、没有任何锚点时结论只能是 unanchored，不能报 intact；trustedHead 不高于本地 head 且所有锚点都对得上才算 intact。
八、水位只升不降：先导入 seq 5 的锚点再导入 seq 3 的锚点，trustedHead 仍是 5，不能因为后到的低序号锚点把结论改小。
九、Gaps 与 Verify 里的缺口必须一致；缺口只报缺失范围，不把已存在但摘要不符的序号混进去。
十、RotateKey 切换活动密钥但保留旧密钥的信任，旋转后新锚点用新 KeyID，传入空签名器报 ErrNilSigner；RetireKey 给某个密钥设退役水位，未知密钥报 ErrUnknownKey，序号超过水位的锚点报 ErrKeyRetired，水位以内的历史锚点仍按受信处理。
十一、同号冲突的锚点要全部保留在案供事后取证，不能后到覆盖先到；同一锚点重复导入不得产生重复的冲突记录。
十二、Snapshot 覆盖流、记录、锚点表、密钥信任与退役水位，Restore 整份替换，损坏快照报 ErrBadSnapshot 且不动现网。

硬约束：同一流的 Append 必须串行分配序号，几十个并发写入后序号必须是连续且不重复的 1 到 N；Anchor、IngestAnchor 与 Verify 并发时只能看到完整的链或完整的锚点集合，不能读到写了一半的状态。

验收以 go test ./... 的 15 条用例全绿为准，测试不许改、不许跳过、不许按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行

```bash
go test ./...
```
