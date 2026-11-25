# VPN 隧道重密钥与逐包重放窗口

## 题面

区域办公点的加密隧道每隔几小时整体重连一次，抓包显示对端把同一个序号的数据包连续投递了十几遍，另有一批来自早已下线密钥的报文被静默放行进了内网；网关重启之后，重放拦截看起来完全失忆，昨天拦过的包今天又收下了。改动落在模块 gsb/batch49-go-fix-a-07 的 vpn_rekey_replay_guard.go，类型 RekeyReplayGuard 负责发起侧重密钥的纪元切换与接收侧逐包重放判定。核心签名保持：NewRekeyReplayGuard(cfg GuardConfig) (*RekeyReplayGuard, error)、InstallEpoch(epoch uint64, keyID string) error、ActiveEpoch() uint64、AcceptPacket(pkt Packet) (Verdict, error)、NextCounter(epoch uint64) (uint64, error)、RetireEpoch(epoch uint64) error、WindowTop(epoch uint64) (uint64, bool)、Epochs() []EpochState、ReplayCount(keyID string) uint64、AcceptedCount(epoch uint64) uint64、Snapshot() ([]byte, error)、Restore(data []byte) error。

需要落地的行为如下。

一、InstallEpoch 只接受严格递增的纪元；epoch 为 0 报 ErrBadEpoch，keyID 全空白报 ErrBadKeyID，重复或回退报 ErrEpochStale，被拒的安装不得改动任何状态。

二、同时保持活跃的纪元数受 MaxEpochs 硬顶；表满时最早的活动纪元转为退役墓碑，其报文判 drop 且原因为 epoch_retired，Epochs 里要能看到 Retired 标记。

三、退役墓碑最多保留 MaxEpochs 条，更旧的墓碑连同统计一起直接删除，之后到达的报文按 epoch_unknown 丢弃，墓碑不参与 NextCounter 分配。

四、新纪元最多领先当前活跃纪元 MaxFutureEpochs；越界报 ErrEpochTooFar 且不占用槽位；该值配成 0 表示不限制跳号。

五、AcceptPacket 的 epoch 未安装时返回 drop/epoch_unknown，不返回错误；退役纪元的包返回 drop/epoch_retired。

六、每个纪元维护 64 位滑动窗口：序号大于窗口顶就滑动并接受；落在窗口内且已置位判 replay；落在窗口内未置位接受；落后窗口顶 64 及以上判 drop/counter_too_old，而且这条路径不得移动窗口，也不得把窗口内的位图清零。

七、counter 为 0 的包报 ErrPacketInvalid；Bytes 为负同样报错，且不写窗口。

八、同一纪元累计接受的字节数超过 MaxBytesPerEpoch 之后，新包判 drop/budget_exhausted，窗口顶、接受计数与重放计数都保持不变。

九、NextCounter 按纪元各自从 1 递增，跨纪元互不影响；对未知纪元报 ErrEpochUnknown，对退役纪元报 ErrEpochRetired。

十、RetireEpoch 对已退役或不存在的纪元是幂等空操作；退役当前活跃纪元报 ErrActiveEpoch。

十一、ReplayCount 按 keyID 汇总跨纪元的重放次数，AcceptedCount 按纪元计数，两个统计都不能被拒绝路径污染。

十二、快照覆盖纪元表、每个纪元的窗口位图与计数、发送计数；快照损坏或为空时 Restore 报 ErrBadSnapshot；恢复后窗口不得倒退，旧序号仍然判重放。

硬约束：AcceptPacket 与 InstallEpoch 会被多条收包协程并发调用，同序号只能有一个返回 accept，其余全部 replay；进程重启后凭快照恢复，纪元表、窗口与统计原样接续，恢复后第一次安装的纪元必须仍然严格大于快照里的活跃纪元；活跃纪元数、墓碑数与每纪元字节预算都是硬顶，越线只能拒绝或退役，不能静默扩容。

收尾标准：go test ./... 跑完 15 条用例必须全绿，测试文件不许改动、不许跳过，也不许对固定输入返回常量；同一序号并发到达与进程重启后的窗口续接各自跑一遍留痕；实现只留在本地。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-fix-a-07`
- 实现文件：`vpn_rekey_replay_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`RekeyReplayGuard`
