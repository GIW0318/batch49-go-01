# 证书透明度多来源分裂视图检测

## 题面

审计方接入两家见证者后，证书透明度监控反而漏报：同一日志在两家见证者那里给出尺寸相同、根哈希不同的签名树头，监控只按最后到达的那份覆盖，事后回查才发现两个互斥的视图都曾被当成正常；另一次是日志被隔离后，补送来的更大树头让隔离自动解除，证据链断了。请落点模块 gsb/batch49-go-safety-b-10，实现文件 ct_split_view_watch.go，类型 SplitViewWatch，把多来源树头对齐、一致性证明校验与隔离证据留存补齐。

公开签名保持不变：NewSplitViewWatch(cfg Config) *SplitViewWatch；Offer(sth SignedTreeHead) (Verdict, error)；VerifyConsistency(from, to uint64, proof [][]byte) (bool, error)；Quarantine(logID, reason string) error；Consensus(logID string) (TreeHead, error)；Quarantined(logID string) bool；Evidence(logID string) []Evidence；Logs() []string；Stats() Stats；Snapshot() ([]byte, error)；Restore([]byte) error。

要修：

1. 每日志按来源记录最新签名树头，来源之间对 tree_size 与根哈希完全一致才算同意；同意来源数达到 Quorum 才更新已接受树头并计 accepted，否则停在 pending。
2. 已经形成共识后，再出现相同 tree_size 但不同根哈希的树头，立即判 split_view 并把该日志隔离，此后 Consensus 返回 ErrQuarantined。
3. 尚未形成共识时，多个来源各说各话只停在 pending，不判分裂视图，也不隔离日志；这些互相矛盾的头作为证据留存。
4. tree_size 小于已接受水位的树头判 stale，不推进水位，也不触发隔离；已接受树头与尺寸到根哈希的索引保持不变。
5. 相同 tree_size 与相同根哈希再次提交按接受处理，用于确认来源一致，不重复累计 accepted，也不改变已接受树头。
6. 树头时间戳回退但 tree_size 与根哈希都未变时属于重放，直接忽略，不得据此判分裂视图，也不得清掉已有证据。
7. 一致性证明按二分哈希链校验：从 from 级根开始，逐个把当前摘要与证明节点按「摘要‖节点」拼接做 SHA-256，最后必须等于 to 级根；路径长度必须是能把 from 覆盖到 to 的最小二分层数，from 与 to 相等时为零个节点，多一个或少一个都判 false。
8. 未记录的 tree_size 参与校验报 ErrUnknownHead；from 为零或 from 大于 to 报 ErrBadRange；哈希链比对失败只返回 false，不返回错误。
9. 隔离必须给出原因，空原因报 ErrBadReason，未知日志报 ErrUnknownLog，重复隔离幂等；被隔离日志不参与共识，但证据必须保留。
10. 隔离期间收到更大 tree_size 的树头只追加到该日志的证据集，不得自动解除隔离，也不得更新已接受树头或参与共识。
11. Logs 按字典序返回所有见过的日志；Stats 汇总 offer、accepted、pending、stale、split_view、当前隔离数与日志数。
12. 快照保存已接受树头、尺寸到根哈希的索引、来源表、证据集与隔离状态；恢复损坏载荷报 ErrBadSnapshot 且现网不变，恢复后共识与一致性校验结果必须一致。

硬约束：同一日志的 Offer 串行裁决，已接受树头、尺寸索引与证据集要在同一临界区内更新，不能出现被两份树头交替覆盖的中间态；快照与裁决互斥，只能看到全量旧状态或全量新状态；重启恢复后隔离状态与证据集不得丢失。验收口径：go test ./... 的 15 条用例必须全过，不得修改或跳过测试、不得按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-b-10`
- 实现文件：`ct_split_view_watch.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`SplitViewWatch`
