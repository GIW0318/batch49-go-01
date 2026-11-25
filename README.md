# BGP 前缀劫持检测与路由策略打分

## 题面

路由监测平台换过采集链路之后连续报出误判：一条正常宣告被判成劫持，另一条明显的子前缀劫持反而显示健康；复核发现基线表里同一条前缀被不同 origin 反复覆盖，而大量观测根本没有留下可复盘的记录，值班同学只能翻日志手工比对，谁也说不清哪条宣告该拦、哪条只是路径抖动。改动落在模块 gsb/batch49-go-fix-a-10 的 bgp_prefix_hijack_watch.go，类型 PrefixHijackWatch 负责维护合法前缀基线，对每条宣告按 origin、前缀长度、路径形状与策略违规打分分档。公开签名保持：NewPrefixHijackWatch(cfg Config) (*PrefixHijackWatch, error)、Learn(b Baseline) error、RemoveBaseline(prefix string) error、BaselineFor(prefix string) (Baseline, bool)、Observe(route Route) (Verdict, error)、History(prefix string) []Verdict、Suppress(prefix string, until int64) error、Suppressed(prefix string) bool、PeerStats(peerAS uint32) PeerStat、TopOffenders(n int) []uint32、Snapshot() ([]byte, error)、Restore(data []byte) error。

故障与边界逐条如下。

一、MaxBaselines 与 MaxHistory 都必须为正，任一非正报 ErrBadConfig。

二、基线的前缀必须是规整 CIDR，带主机位或前缀长度越界报 ErrBadPrefix，OriginAS 为 0 同样报 ErrBadPrefix；MaxLength 必须落在前缀长度到地址位宽之间；不同写法的同一网络要归一成同一条基线。

三、同一前缀同一 origin 重复学习是幂等更新，不占新槽位；同一前缀换 origin 报 ErrOriginConflict，除非 AllowMultipleOrigins 打开。

四、基线前缀数受 MaxBaselines 硬顶，超限报 ErrBaselineLimit 且不落库。

五、Observe 校验前缀、OriginAS、PeerAS 与非空 AS 路径，前缀不规整报 ErrBadPrefix，其余非法字段（含空路径与含 0 的路径）报 ErrBadRoute。

六、与基线同前缀但 origin 不在允许集合，记 origin_mismatch，权重 50。

七、宣告前缀长度超过基线 MaxLength 时记 more_specific，权重 30，即使 origin 与基线一致也要记。

八、AS 路径首段不等于 PeerAS 记 path_spoofed 权重 40；路径里出现 64512 到 65534 或 4200000000 到 4294967294 的私有 AS 记 private_as 权重 20；同一 AS 出现两次记 path_loop 权重 25。

九、完全不在任何基线覆盖范围内的宣告记 uncovered_prefix，权重 20。

十、总分达到 70 判 confirmed，达到 30 判 suspected，其余 clean，没有原因码的宣告才算与基线一致；原因码按固定顺序输出且不得重复。

十一、抑制窗口内的前缀等级为 suppressed，仍给出原因码与分数，但不计入 PeerStats 的 Hijacks，也不写入 History；Suppressed 按当前时间判断。

十二、PeerStats 每次观测都累加 Observations；TopOffenders 只列 Hijacks 为正的 peer，按 Hijacks 降序、同分按 AS 号升序，条数不超过 n，n 为 0 或负数返回空；RemoveBaseline 对不存在的基线报 ErrBaselineNotFound，删除只撤基线，History 里的旧记录保留可查。

快照要覆盖基线、抑制窗口、历史与 peer 计数；快照损坏时 Restore 报 ErrBadSnapshot，在线数据保持不变；恢复后抑制窗口按绝对时间继续有效，peer 计数与历史原样接续。

硬约束：Learn、RemoveBaseline 与 Observe 会被采集协程和运维协程并发调用，观测只能看到完整的新基线集或完整的旧基线集，不能看到半条基线；进程重启后凭快照续跑，已抑制的前缀不能因为重启而提前解禁；基线数、每前缀历史条数与 peer 计数表都是硬顶，越线只能拒绝或淘汰最旧记录。

验收口径：go test ./... 的 15 条用例全绿，不许改测试、不许跳过、不许按固定前缀写死；基线并发更新与抑制窗口跨重启各验一遍；实现限定在本地。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-fix-a-10`
- 实现文件：`bgp_prefix_hijack_watch.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`PrefixHijackWatch`
