# IDS 会话重组与特征匹配去重

## 题面

边界 IDS 升级特征库之后，日志里同时出现两种极端：明文里明明写着攻击串，却一条告警也没有；同一段流量又被重复报了七八次，安全同事排查时发现告警的偏移量全都对不上原始字节。抓包复核看到握手后的报文经常乱序到达，中间还夹着重叠段，而特征匹配一直是对单段做的。改动落在模块 gsb/batch49-go-fix-a-09 的 ids_signature_reassembler.go，类型 SignatureReassembler 负责按流重组连续字节、在重组结果上跑特征并做告警去重。接口签名保持一致：NewSignatureReassembler(cfg Config) (*SignatureReassembler, error)、AddSignature(sig Signature) (int, error)、Feed(seg Segment) ([]Alert, error)、Stream(flowID string) ([]byte, error)、Pending(flowID string) int、CloseFlow(flowID string) error、AlertCount(sigID int) uint64、FlowCount() int、OverlapConflicts(flowID string) int、EvictIdle(now int64) int、Snapshot() ([]byte, error)、Restore(data []byte) error。

逐条要求如下。

一、配置里的 MaxStreamBytes、MaxSegments、MaxFlows、FlowTTL 四项都必须为正，任一非正报 ErrBadConfig。

二、AddSignature 校验：pattern 为空、Name 全空白、Offset 或 Depth 为负报 ErrInvalidSignature；ID 传 0 时由实现挑一个尚未占用的正整数，显式 ID 重复报 ErrDuplicateSignature。

三、第一条段的 Seq 定为该流基准；后续段按 Seq 拼接，缺口未补齐时 Stream 只返回连续前缀，Pending 报告缺口之后缓存的字节数。

四、完全重复的段是幂等操作，既不改变流，也不产生新的告警。

五、重叠段按先到优先：重叠部分保留先收到的字节，只有越过连续前缀的尾部才追加；重叠且字节不同的部分要计入 OverlapConflicts。

六、缺口补齐后自动把缓存段合并进连续前缀，合并结果与段的到达顺序无关。

七、单流的连续字节加待合并段超过 MaxStreamBytes，或待合并段数超过 MaxSegments，立即淘汰该流并报 ErrStreamTooLarge，之后 Stream 报 ErrUnknownFlow。

八、FlowCount 达到 MaxFlows 后新流报 ErrFlowLimit，已经跟踪的流不受影响。

九、特征匹配在连续字节上做，跨段边界的模式必须命中；Offset 规定最早的起始位置，Depth 为正时只允许在 Offset 到 Offset+Depth 之间起始。

十、NoCase 按 ASCII 忽略大小写；AlertOnce 为真时同一流同一特征只告警一次，否则同一流的同一特征同一偏移只告警一次，重扫不得重复计数。

十一、CloseFlow 之后 Feed 报 ErrFlowClosed，Stream 仍能读到已有的连续字节；重复 CloseFlow 是幂等空操作；EvictIdle 把空闲时长达到 FlowTTL 的流整体移除并返回条数。

十二、快照覆盖流表、缺口缓存、已告警集合与签名表；快照字节解析失败时 Restore 必须报 ErrBadSnapshot；恢复后继续投递的段要接在原来的连续前缀之后，已经告警过的特征不再重复告警。

硬约束：同一流的 Feed 必须串行，不同流之间可以并行，流表、缺口缓存与告警去重集合在并发下不得泄漏或重复计数；进程重启后凭快照继续重组，重放旧段不能复活已去重的告警，也不能把连续前缀截断；流数、单流字节数与待合并段数都是硬顶，越线只能淘汰该流，不能拖垮其它流。

交付门槛：go test ./... 的 15 条用例全部通过，测试不许改、不许跳过、不许针对固定字节写死；多流并发投递与快照续跑各构造一次；只提交本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-fix-a-09`
- 实现文件：`ids_signature_reassembler.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`SignatureReassembler`
