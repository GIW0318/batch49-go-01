# 高色深抖动输出管线

## 题面

黄昏天空的渐变在低端机型上出现一圈圈色阶，补光之后整屏发绿，切换分辨率时还会闪一下白边。范围在 gsb/batch49-go-game-b-15 的 bitdepth_dither_pipeline.go，类型 BitDepthDither 负责把线性高色深图像转换到目标位深的输出缓冲，抑制色带并限制误差扩散。管线每帧接收一张线性浮点图，输出同尺寸的目标位深缓冲，帧序号由渲染循环给出，调用方按序号读取相位。

公开接口：NewBitDepthDither(cfg DitherConfig) (*BitDepthDither, error)、Configure(cfg DitherConfig) error、SetStrength(v float64) error、ProcessFrame(img Frame, frameSeq uint64) (Frame, error)、Stats() DitherStats、CurrentConfig() DitherConfig、PhaseFor(frameSeq uint64) int、Reset() error、Snapshot() ([]byte, error) 与 Restore([]byte) error。结构体包括 DitherConfig（目标位深、块尺寸、强度、扩散通道、误差上限）、Frame（宽高、通道数与数据）、DitherStats（帧数、像素数、块数、相位、最大误差、参与扩散的通道数）。

要修好的行为：目标位深不在 1 到 16 之间报 ErrInvalidBitDepth；抖动块尺寸必须是二的幂且不超过 32，否则报 ErrInvalidTileSize；强度不在 0 到 1 之间或为 NaN 报 ErrInvalidStrength；误差上限为负报 ErrInvalidMaxError；扩散通道号超出帧通道数报 ErrInvalidChannel；帧宽高非正、通道数不是 3 或 4、缓冲区长度与宽高通道不符报 ErrInvalidFrame；量化按目标位深把 0 到 1 的线性值映射到整数级再还原；抖动矩阵按二的幂块平铺，二乘二矩阵取 [0 2; 3 1]，单元阈值按 (m+0.5)/块面积减 0.5 归一，相位随帧序号对块尺寸取模推进；误差只在声明通道上扩散，单像素累计误差不超过上限；多个块并行处理时块边界的扩散误差要串行传给下一个块；统计里给出帧数、像素数、块数、相位、最大误差与参与扩散的通道数；输出缓冲长度必须与输入一致，不能就地改调用方切片。

硬约束：强度为 0 时必须走纯量化直通路径，不得写入误差缓冲，否则后续帧会从被污染的初值起算；分块并行时块边界误差必须串行传递，各块独立清零会在接缝处留下色阶；alpha 通道不参与误差扩散，但仍要接受抖动以保持预乘颜色一致；统计按帧累加，Reset 清空计数与待扩散误差但不改配置；快照要保存配置、计数器与待扩散误差，恢复后同一帧序号的输出与中断前逐值一致，损坏快照报 ErrBadSnapshot。

Configure 在全部校验通过后整体替换配置，校验失败保留旧配置；ProcessFrame 不修改配置，SetStrength 只影响后续帧。处理一帧失败时计数器不前进，待扩散误差也不落账。块尺寸与分辨率上限共同决定单帧内存，超限在 Configure 阶段就拒绝，避免运行期分配失控。

验收：go test ./... 15 条全部通过，测试文件不得修改、跳过或按固定输入写死；分块边界与跨帧恢复自行验证，逐值对比恢复前后的输出；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-b-15`
- 实现文件：`bitdepth_dither_pipeline.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`BitDepthDither`
