# 场景后处理体积混合

## 题面

雪原场景从洞穴走回白天时，画面先发灰再猛地一下过曝，快速切镜头的时候更明显；同一片区域摆了两个后处理体积，颜色还会随着摆放顺序变来变去。改参数的人在编辑器里反复对照过，单独看每个体积的数值都对，叠在一起就不对，退回单人测试场景又恢复正常。落点 gsb/batch49-go-game-b-10 的 postfx_volume_blender.go，类型 PostFxVolumeBlender，公开签名保持：NewPostFxVolumeBlender(cfg BlendConfig) *PostFxVolumeBlender、AddVolume(v Volume) (VolumeID, error)、UpdateVolume(id VolumeID, v Volume) error、RemoveVolume(id VolumeID) error、SetEnabled(id VolumeID, enabled bool) error、SetCamera(pos Vec3, teleported bool) error、AdvanceFrame() int、Active() PostFxSettings、Weight(id VolumeID) float64、BlendWeight(id VolumeID) float64、VolumeCount() int、Params(id VolumeID) (Volume, bool)、Snapshot() ([]byte, error)、Restore([]byte) error。

要落实的行为：一，注册体积分配递增编号，名字为空、内半径为负、外半径不大于内半径都报 ErrInvalidVolume；更新与移除未注册编号报 ErrVolumeNotFound。二，摄像机在内半径以内权重为一；在外半径以外权重为零；两个半径之间按距离线性衰减，Weight 返回当帧的目标权重。三，参与混合的权重受每帧最大变化速率约束，BlendWeight 返回斜坡推进后的值，每次 AdvanceFrame 最多向目标靠近一个步长并返回新的帧号，斜坡推进与帧号推进必须同步。四，瞬移设置摄像机时权重直接吸附到目标值，不吃斜坡。五，数值参数按混合权重加权平均，没有任何有效体积时返回默认设置。六，布尔参数与枚举参数取混合权重最高的那个体积的值，权重有高下时结果不受注册顺序影响。七，体积被移除后不再参与混合，剩余体积的权重按当前摄像机位置重算，被移除体积的权重缓存一并清掉，编号不会回收给后来注册的体积。八，枚举参数出现同权重冲突时按体积编号小者优先，不能取最后写入的那个。九，体积被禁用的当帧其历史权重必须清零，不能按斜坡慢慢衰减。十，混合结果越出参数声明范围时要钳制，饱和度的范围是零到二；曝光是负三到正三；钳制只作用于输出，不得写回体积自身的参数。十一，同一份体积表与摄像机位置重复取 Active 结果逐字段一致，不依赖 map 遍历顺序。

硬约束：注册、更新、启停与取当帧设置会跨线程并发调用，体积表与权重缓存的读写必须串行且快照一致，-race 下不得读到半份体积表；Snapshot 要保存体积表、启停状态、当帧权重与帧号，Restore 后同一摄像机位置的 Active 结果与恢复前一致，损坏负载报 ErrBadSnapshot 且不动现有体积表。

验收：go test ./... 的 15 条用例全部通过，测试文件不得修改、跳过或按固定数值写死；并发更新与重启恢复请自行构造场景验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-b-10`
- 实现文件：`postfx_volume_blender.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`PostFxVolumeBlender`、`Volume`、`VolumeID`、`Vec3`、`PostFxSettings`、`BlendConfig`
