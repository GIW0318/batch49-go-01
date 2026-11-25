# TLS 证书链校验与吊销状态缓存

## 题面

一次灰度换证把网关的证书校验推上了风口：新签发的中级证书上线后，部分老客户端的链路被判定为不可信；另一头，吊销列表因为拉取超时被整体跳过，网关对一张已经吊销的叶子证书照常放行；运维把状态缓存调大之后，内存涨了上去，被吊销的条目反而先被挤掉了。排障时三个人给出三种说法，监控里只有一条笼统的握手失败；事后复盘发现校验顺序、通配符规则与状态新鲜度三处各有一套实现。改动落在 gsb/batch49-go-fix-a-03 的 chain_revocation_cache.go，类型 ChainRevocationCache。关键签名保持：NewChainRevocationCache(cfg ChainConfig) *ChainRevocationCache、SetNow(now time.Time)、AddTrustAnchor(cert CertInfo) error、Verify(chain []CertInfo, host string) (Verdict, error)、RecordStatus(issuer, serial, status, source string) error、Status(issuer, serial string) (StatusEntry, bool)、Prune() int、Stats() CacheStats、Anchors() []string、Snapshot() ([]byte, error)、Restore(data []byte) error。

要修的点：一、空链报 ErrEmptyChain，长度超过 MaxChainLen 报 ErrChainTooLong；链按叶子在前、根的后继（不含锚）排列。二、叶子 SAN 必须覆盖主机名，精确匹配不区分大小写，通配符只匹配一级标签且不匹配裸后缀，不覆盖报 ErrHostMismatch。三、每张证书按当前时钟校验有效期，早于 NotBefore 报 ErrCertNotYetValid，晚于 NotAfter 报 ErrCertExpired。四、叶子必须不是 CA，否则报 ErrLeafIsCA；叶子必须带 serverAuth 用途，否则报 ErrKeyUsage。五、相邻证书的 Issuer 必须等于下一张的 Subject，否则报 ErrChainBroken；中间证书的 IsCA 必须为真，否则报 ErrNotCA。六、PathLenSet 的 CA 其下允许的中间 CA 数不得超过 PathLen，超出报 ErrPathTooLong，PathLen 只数 CA 证书。七、链尾 Issuer 必须命中已钉扎锚的 Subject，否则报 ErrUntrustedChain；锚必须为 CA 且在有效期内；同一 Subject 重复钉扎报 ErrDuplicateAnchor，空 Subject 报 ErrBlankField。八、锚带 NameConstraints 时，叶子全部 DNS 名必须落在某个后缀内，越界报 ErrNameConstraint。九、吊销状态按签发者与序列号缓存，两者都以字符串原样比较，Verify 只查叶子条目；无条目报 ErrStatusUnknown，已吊销报 ErrRevoked。十、good 条目超过 StatusTTL 后进入 MaxStaleness 宽限，宽限内仍可用但计入 Stale，超过 StatusTTL 加 MaxStaleness 报 ErrStatusStale；撤销条目永久有效，不得被后续 good 覆盖，覆盖尝试报 ErrRevokedPermanent。十一、缓存容量受 MaxCacheEntries 限制，按最近使用淘汰，优先淘汰 good 与 unknown 条目；撤销条目不参与淘汰，缓存全是撤销条目而仍要新增时报 ErrCacheFull；Prune 清理过期条目并返回条数。十二、Stats 给出条目数、命中、未命中、淘汰与陈旧次数，命中与未命中按 Verify 与 Status 的查询记账；Snapshot 覆盖锚、状态条目、LRU 次序与计数，Restore 整份替换，损坏快照报 ErrBadSnapshot 且不动现网。

硬约束：并发 Verify、RecordStatus 与快照必须互斥，统计与缓存更新原子生效；Verify 不得修改调用方传入的证书切片；恢复后 LRU 次序与计数延续，下一次淘汰仍按最近使用判定。验收以 go test ./... 的 15 条用例全绿为准，测试不许改、不许跳过、不许按固定输入写死；并发与重启恢复自行验证；只做本地实现，交付源码、跑测记录和一页以内说明。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-fix-a-03`
- 实现文件：`chain_revocation_cache.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`ChainRevocationCache`
