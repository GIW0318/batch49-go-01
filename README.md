# DNS 缓存投毒防护与 DNSSEC 校验

## 题面

上一轮演练里，解析侧出了一个很难复现的问题：被投毒的应答把一条伪造的 NS 记录塞进权威段，缓存照单全收，半小时内整个办公网都被带偏；另一次，上游明明对不存在的域名给了带 NSEC 的否证，解析器却把它当成普通空应答缓存下来；还有客户抱怨开了 DNSSEC 之后，签名早就过期的应答仍然命中缓存。改动落在 gsb/batch49-go-fix-a-04 的 dnssec_cache_guard.go，类型 DnssecCacheGuard。关键签名保持：NewDnssecCacheGuard(cfg ResolverConfig) *DnssecCacheGuard、SetNow(now time.Time)、SetTrustAnchor(zone string, ds DsRecord) error、AddZoneKey(zone string, key DnskeyRecord) error、LearnDelegation(zone string, ds []DsRecord) error、Resolve(q Query, resp Response) (Answer, error)、CacheGet(q Query) (Answer, bool)、Purge(zone string) int、Stats() CacheStats、Snapshot() ([]byte, error)、Restore(data []byte) error。

要修的点：一、查询名或类型为空报 ErrBlankQuery，名不以点结尾报 ErrNotAbsolute，类不是 IN 报 ErrUnsupportedClass。二、应答事务号必须等于查询事务号，否则报 ErrIDMismatch；查询带 CaseName 时应答问题名必须逐字回显同一大小写，否则报 ErrCaseMismatch。三、应答的 Zone 必须覆盖查询名；回答段任何记录所有者越出应答区报 ErrBailiwickViolation；权威段与附加段的越界记录直接丢弃并计入 Poisoned，不影响已通过校验的回答。四、开启 DO 时，回答链上每个 RRset 都必须有覆盖其类型的签名，缺失报 ErrSignatureMissing。五、签名必须 Valid、签名区已钉扎、KeyTag 在该区注册、Inception 与 Expiration 覆盖当前时钟、Labels 与所有者标签数一致，否则分别报 ErrSignatureInvalid、ErrNoTrustAnchor、ErrKeyUnknown、ErrSignatureExpired。六、锚的 DS 与签名密钥的 KeyTag、Algorithm 必须一致；DS 只能由父区学习，父区不覆盖该子区时 LearnDelegation 报 ErrBadDelegation。七、CNAME 链按记录逐跳跟随，最终 RRset 类型必须等于查询类型；成环或超过 MaxCNAME 报 ErrCnameLoop；目标越出应答区按投毒拒绝。八、非 CNAME 查询遇到空回答时必须有 SOA 与 NSEC 或 NSEC3 否证，缺失或未签名报 ErrDenialInvalid；NSEC3 opt-out 覆盖的委派按不存在处理，但不得当作肯定回答缓存。九、TTL 取链上各 RRset 的最小值，先按 MinTTL 与 MaxTTL 截断，再被最早的签名到期时间截断；缓存到期时间等于当前时钟加最终 TTL。十、缓存键包含规范名、类型、类与 DO 位；CacheGet 只返回未到期条目，过期即删除并计未命中；Purge 按区清掉子树条目并返回条数。十一、缓存容量受 MaxCacheEntries 限制，按最近使用淘汰；Stats 给出条目数、命中、未命中、投毒丢弃与否证次数。十二、Snapshot 覆盖锚、签名密钥、委派 DS、缓存条目与计数，Restore 整份替换，损坏快照报 ErrBadSnapshot 且不动现网，恢复后缓存键与 LRU 次序延续。

硬约束：并发 Resolve、CacheGet 与 Purge 必须串行裁决，缓存、计数与快照的更新原子生效；Verify 路径不得修改调用方传入的应答切片；恢复后 LRU 次序与计数延续，下一次淘汰仍按最近使用判定。验收以 go test ./... 的 15 条用例全绿为准，测试不许改、不许跳过、不许按固定输入写死；并发与重启恢复自行验证；只做本地实现，交付源码、跑测记录和一页以内说明。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-fix-a-04`
- 实现文件：`dnssec_cache_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`DnssecCacheGuard`
