# 流送纹理页暂存与入驻

## 题面

开放世界跑图时，远处地形的贴图偶尔整块变成纯色，过几秒又自己好了；硬盘读得慢的机器上，低清 mip 还没到位就开始画高清层，画面先糊后锐地闪。落点 gsb/batch49-go-game-b-09 的 texture_upload_stager.go，类型 TextureUploadStager，公开签名保持：NewTextureUploadStager(cfg StageConfig) *TextureUploadStager、SubmitPage(req PageRequest) (PageTicket, error)、Cancel(ticket PageTicket) error、UploadStep(bytesBudget int) (UploadReport, error)、Evict(texID string, level int) error、Resident(texID string) ([]int, error)、State(texID string, level int) string、Handle(texID string, level int) (PageHandle, error)、DestroyTexture(texID string) error、StagedBytes() int、Tick(ms int64)、Snapshot() ([]byte, error)、Restore([]byte) error。

要落实的行为：一，提交页请求返回递增票据，页进入排队状态，暂存占用按页字节数累计；纹素名为空报 ErrBlankTexture，层号为负报 ErrInvalidLevel，页字节数非正报 ErrInvalidBytes，同一纹理同一层重复提交报 ErrDuplicatePage。二，上传按字节预算推进，一次 UploadStep 只消耗预算内的字节，报告里给出本次字节数、完成页数、受阻页数与是否部分完成；预算不够一页时该页停在部分上传状态，下一次继续把剩余字节推完。三，同一纹理必须父 mip 先上传，低层号先于高层号完成，即使高层号先提交也要等父层驻留，父层还没提交时子层保持排队并计入受阻。四，排队中的页可以取消，取消后票据失效、暂存占用释放、状态转为已取消；上传中的页不可取消，报 ErrUploadInProgress。五，页上传完成后转入驻留，Resident 按层号升序返回该纹理已驻留的层，未驻留的层不出现在列表里。六，驻留页被驱逐后进入退役窗口，窗口内不再算驻留也不重复占暂存，窗口到期才释放，Tick 负责推进时间。七，退役窗口内重新请求同一页要直接复用并立即回到驻留，不再排队也不再占暂存。八，驱逐未驻留的页报 ErrNotResident，页状态保持原样。九，纹理销毁后其页全部释放，再查 Resident 报 ErrNoTexture。十，父 mip 被驱逐后，其子 mip 必须级联释放，不能继续驻留。十一，退役窗口内被重新请求的页不得重置退役计时起点，否则该页永远等不到释放。十二，上传中的页在帧结束时若目标纹理被销毁，后续句柄读取要报错，不能读到空内容。

硬约束：提交、取消、上传推进与驱逐会被资源线程和渲染线程并发调用，同一页只能有一个终态，暂存字节数在取消与失败路径上必须精确归还，-race 下不得出现负的暂存占用或半更新的页表；Snapshot 要保存排队、上传中、驻留与退役四类页以及票据和计时起点，Restore 后继续上传的结果与中断前一致，损坏负载报 ErrBadSnapshot 且不动现有状态。

验收：go test ./... 的 15 条用例全部通过，测试文件不得修改、跳过或针对固定字节数写死；并发提交与重启续传请自行构造场景验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-b-09`
- 实现文件：`texture_upload_stager.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`TextureUploadStager`、`PageRequest`、`PageTicket`、`UploadReport`、`PageHandle`、`StageConfig`
