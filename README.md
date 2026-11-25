# ADTS 帧头扫描、帧长校验与失步重同步

## 题面

直播收录遇到一段脏数据之后，后面整段音频都没有声音，切到 48k 时帧序号又从 1 开始，剪辑师拿到的录音前后对不上。问题落在 gsb/batch49-go-game-c-04 的 adts_frame_scanner.go，需要把 AdtsFrameScanner 的字流切帧能力补齐。对外签名：NewAdtsFrameScanner() *AdtsFrameScanner；Feed(buf []byte) ([]FrameInfo, error)；Resync() error；SetMaxResync(n int) error；SetMaxFrameBytes(n int) error；Stats() ScanStats；Pending() int；FrameCount() int；Reset() error；Snapshot() ([]byte, error)；Restore([]byte) error。

要修好的行为：同步字必须是 0xFFF，layer 必须是 0，采样率索引为 13 或 14 的字节组不算帧头；保护位为 0 时头部按 9 字节算并跳过 2 字节 CRC，否则按 7 字节算；帧长取头里的 13 位数值，包含头部本身，帧长小于头部长度的视为无效数据丢弃；帧序号从 1 起严格递增，偏移是从流开头算起的字节位置；一次喂入可以产出零到多帧，跨调用的半帧必须缓存，下一次补齐后按原顺序产出；同步字之前的垃圾字节计入跳过字节数并继续向后扫描；连续无法对齐的字节超过 SetMaxResync 设定值时，整段放弃并计一次重同步，然后从剩余缓冲继续重扫；采样率或声道配置发生变化时，在该帧上标记格式变更，帧序号继续单调递增，不能重置为 1；采样率索引为 15 的显式采样率帧要跳过，但只把它自己的字节计入跳过数，不能把整段缓冲一起丢掉；不完整帧重扫时不得把已经消费过的同步字再次当作新帧头，否则同一帧会被报两次；帧长超过 SetMaxFrameBytes 上限的整帧跳过并计入跳过字节；统计里的 Buffered 与 Pending 必须一致，两者都表示还没有被消费的字节数；帧头里的 profile、私有位与缓冲满度不参与合法性判定，但不得影响帧长计算；每个已产出帧的偏移加上它自己的长度必须等于下一帧的偏移，中间不允许出现空洞；重扫描从失步位置之后的第一个候选同步字开始，已经确认过的帧不重复计数；Reset 清空缓冲、序号与统计；快照要带走待处理缓冲、帧序号、格式状态与统计，恢复全有或全无，坏数据报错且不动现有状态。

硬约束：Feed 与 Reset 并发时按同一把锁串行，同一时刻只有一次扫描在推进缓冲，帧序号不允许重复或跳号；缓冲长度始终不超过单帧上限加最大重同步字节数，脏数据再长也不能无界堆积；SetMaxResync 只接受 1 到 1048576，SetMaxFrameBytes 只接受 64 到 65535，越界报错且不改变原值。验收以 go test ./... 的 15 条全部通过为准，测试不得修改、跳过或按固定输入写死；并发与重启恢复自行验证；只做本地实现，交付源码、运行输出与失步处理说明。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-c-04`
- 实现文件：`adts_frame_scanner.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`AdtsFrameScanner`，辅助类型 `FrameInfo`、`ScanStats`
