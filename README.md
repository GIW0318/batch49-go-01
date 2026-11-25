# batch49-go-game-c-08

模块路径 `gsb/batch49-go-game-c-08`，包名 `task`，实现文件 `byte_range_indexer.go`，类型 `ByteRangeIndexer`。

## 题面

点播视频切片搬到新存储后，拖动进度条的体验明显变差：明明定位到 90 秒，实际播放的却是 80 秒那一段，用户要反复拖三次才能找到想看的位置；换用带索引盒的旧片源后整个索引为空，播放器直接报错；分段被判失效后，定位仍然悄悄顺延到下一段，运维在日志里查不到任何异常。请补全模块 gsb/batch49-go-game-c-08 中 byte_range_indexer.go 的 ByteRangeIndexer，由它把视频分片流建成字节范围索引并按时间定位。索引重建期间新到的分段要排队而不是被直接丢掉。

数据格式：每个盒子的头是 4 字节大端长度（含头）加 4 字节类型。无索引盒的分段是一串 moof 与 mdat 交替出现：moof 载荷固定 8 字节，前 4 字节是大端 tick 时长，第 5 字节的最低位表示该子分段是否为 SAP 起点；mdat 载荷任意。带索引盒的分段开头是一个 sidx 盒，载荷为 4 字节大端 timescale、4 字节大端条目数，随后每条 12 字节：tick 时长、字节数、标志（最低位为 SAP），字节偏移从 sidx 盒之后开始累计。

公开接口：NewByteRangeIndexer(cfg IndexConfig) *ByteRangeIndexer、IndexSegment(segID string, data []byte) error、Locate(t time.Duration, requireSAP bool) (ByteRange, error)、Invalidate(segID string) error、Entries() int、RangeAt(segID string, index int) (ByteRange, error)、Segments() []string、TotalBytes() int64、Stats() IndexStats、Reset() error、Snapshot() ([]byte, error)、Restore(data []byte) error。

需要落实的行为：1）每个子分段的偏移与大小严格衔接且严格递增，TotalBytes 返回全部区间大小之和，跨分段也不得回退到前面的字节；2）tick 按 timescale 换算成纳秒，换算余数按累计进位保留，单条时长不得四舍五入成 0；3）每个分段在入库时确定它在全局时间轴上的起点，后续失效不改变其它分段的起点；4）Locate 找到 Start 不晚于 t 且 End 晚于 t 的区间，要求 SAP 时定位到不早于该时刻的第一个 SAP 区间；5）无索引盒分段末尾不足一个 moof 与 mdat 对的数据要归入前一个子分段，不能丢弃；6）盒头截断、长度越界或类型不符返回 ErrMalformedSegment，且不留下半个分段；7）重复入库同一分段返回 ErrDuplicateSegment；8）定位命中已失效区间必须返回 ErrOutOfRange，不能安静地顺延到下一可用区间；9）索引条目总数受 MaxEntries 硬顶，超限返回 ErrIndexFull 且不写入；10）Invalidate 未知分段返回 ErrUnknownSegment，成功后失效计数加一，RangeAt 越界返回 ErrOutOfRange；11）t 为负或超过最后一条区间的 End 返回 ErrOutOfRange。

硬约束：IndexSegment、Locate 与 Invalidate 会被多个会话协程并发调用，索引结构与计数器只能看到完整状态，不能出现条目数已加而区间表未写的中间态；快照要保存分段顺序、每段区间与累计字节数，Restore 之后偏移与定位结果必须与中断前一致，损坏快照返回 ErrBadSnapshot 且不损坏现有索引。并发索引与恢复要自行构造验证。

验收：go test ./... 的 15 条用例全部通过，测试文件不得修改、跳过或按固定输入写死；只做本地实现。交付源码、运行记录和定位口径说明。

## 运行

```bash
cd batch49-go-game-c-08
go test ./...
```
