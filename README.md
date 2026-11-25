# H.264 参数集解析、激活时序与分辨率变更检测

## 题面

监控预览换上新的编码器之后，画面从 1080p 变成 1920x1088，切分辨率时下游缓存整批失效，重启后又认回旧尺寸。问题落在 gsb/batch49-go-game-c-03 的 h264_paramset_tracker.go，需要把 ParamSetTracker 补齐。对外签名：NewParamSetTracker() *ParamSetTracker；ParseSPS(nal []byte) (SPSInfo, error)；ParsePPS(nal []byte) (PPSInfo, error)；Activate(ppsID int) error；ActiveSPS() (SPSInfo, bool)；ActivePPS() (PPSInfo, bool)；ResolutionChanged() bool；SPSVersion(id int) (int, bool)；PPSVersion(id int) (int, bool)；Dimensions() (int, int)；Reset() error；Snapshot() ([]byte, error)；Restore([]byte) error。

要修好的行为：NAL 头要校验，SPS 只接受类型 7、PPS 只接受类型 8，类型不符或位流截断报错且不落任何状态，输入按裸 NAL 处理，带起始码视为类型不符；位流里的防竞争字节 0x03 要先行去掉再按位解析，id、log2_max_frame_num_minus4、pic_order_cnt_type、max_num_ref_frames 这些字段都按指数哥伦布码读取，pic_order_cnt_type 为 0 时还要补读 log2_max_pic_order_cnt_lsb_minus4；SPS 要解出 profile、level、id、num_ref_frames 与图像尺寸，宽高先按宏块数乘 16，再减去裁剪窗口，4:2:0 逐行画面下裁剪单位横向 2、纵向 2；PPS 要解出 id、引用的 SPS id、熵编码标志与去块滤波标志，redundant_pic_cnt_present_flag 之后的扩展字节一律忽略；同一 ID 的参数集只有内容发生变化才递增版本，逐字节相同的重复下发不得递增版本；frame_mbs_only_flag 为 0 时高度按场数翻倍，裁剪单位随之变成 4，并与 VUI 裁剪同时作用，不能只翻倍不裁；裁剪窗口越界的参数集要整份丢弃，但不影响已经激活的旧参数集；参数集被 slice 引用时才算激活，激活前的解析结果不得当作当前参数；激活的 PPS 若引用了从未解析过的 SPS 要报错；激活后尺寸、当前 SPS 与当前 PPS 都要反映新集合，分辨率与上一次激活不同时把变更标志置真，用同尺寸再次激活时复位；没有激活集合时 Dimensions 返回 0 与 0，每个已存参数集的版本号从 1 起算；SPS 与 PPS 的 id 只接受 0 到 31，越界报错，profile 与 level 原样返回不改写；单边尺寸超过 4096 的参数集整份拒绝；分辨率变更标志在第一次激活时为假，之后只在尺寸真正变化时置真；激活失败不得改变原有的激活集合；Reset 清空全部参数集与激活状态；快照要带走参数集内容、版本号与激活状态，恢复全有或全无，坏数据报错且不动现有状态。

硬约束：激活与解析共用同一把锁，并发解析不同 ID 的参数集互不覆盖，同一 ID 的解析与激活按到达顺序串行定版；参数集表最多保留 32 个 ID，超出时淘汰最久没有被使用且未被激活的那一个，被淘汰的 ID 之后查版本要返回未找到；快照与解析并发时只能看到全量旧状态或全量新状态。验收以 go test ./... 的 15 条全部通过为准，测试不得修改、跳过或按固定输入写死；并发与重启恢复自行验证；只做本地实现，交付源码、运行输出与解析说明。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-c-03`
- 实现文件：`h264_paramset_tracker.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`ParamSetTracker`，辅助类型 `SPSInfo`、`PPSInfo`
