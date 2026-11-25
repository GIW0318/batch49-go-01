# 后处理遮罩合成与羽化边界

## 题面

野外夜间关卡上线后，玩家走进雾区时画面先整块变灰，再沿角色轮廓浮出一圈硬边；把雾和调色叠成两层时，第二层直接把第一层盖掉，远处的岩石还会被涂成同一种颜色。问题落在 gsb/batch49-go-game-b-11 的 postfx_mask_compositor.go，类型 PostFxMaskCompositor 负责把后处理结果按单通道遮罩与原始画面逐像素混合。遮罩来源分深度、材质与美术手绘三类，效果按声明顺序挂到同一张颜色目标上，每帧先取原始画面再逐个效果合成，优先级只看声明先后。对外结构体包括 Image（宽、高、像素数组）、MaskRef（ID、来源、宽高、单通道数据）与 Stats（效果名、参与像素、写入像素、羽化、强度、是否重采样）。

接口保持：NewPostFxMaskCompositor(width, height int) (*PostFxMaskCompositor, error)、SetSource(img Image) error、DeclareEffect(effectID string) error、SubmitRendered(effectID string, img Image) error、BindMask(effectID string, mask MaskRef) error、SetFeather(effectID string, px int) error、SetStrength(effectID string, v float64) error、Composite(effectID string) (Stats, error)、CompositeAll() ([]Stats, error)、EffectOrder() []string、Output() Image、Snapshot() ([]byte, error) 与 Restore([]byte) error。

要修好的行为：目标尺寸非正报 ErrInvalidSize，图像缓冲区长度与宽高不符报 ErrInvalidImage；重复声明同一效果报 ErrDuplicateEffect，未声明的效果参与合成报 ErrUnknownEffect；遮罩 ID 为空或单通道长度与宽高不符报 ErrInvalidMask；羽化像素数越界报 ErrInvalidFeather，强度落在 0 到 1 之外报 ErrInvalidStrength；合成按 out = source*(1-m) + rendered*m 逐像素计算，m 为遮罩采样值与强度之积；遮罩分辨率与颜色目标不一致时按像素中心最近邻重采样，重采样过的效果在统计里标记；羽化只作用在遮罩取值发生变化的边缘像素，遮罩内部与图像边界保持原值；多个效果按声明顺序逐级叠加，后声明的效果在前一层结果上继续合成；统计里给出参与像素数、羽化半径、强度、是否重采样与实际写入像素数；强度为 0 的效果在整条链路里完全跳过，不得先混合再乘零；遮罩整块为 0 的效果不参与合成，但它配置的羽化与强度要留给后续帧继续使用；材质遮罩对应的物体被剔除后，该效果仍按上一帧的遮罩结果混合，不得整块退化成全强度。

硬约束：Composite 与 CompositeAll 会被渲染线程和调试面板同时调用，单次合成只能看到全量旧状态或全量新状态；快照要能描述每个效果的遮罩、羽化、强度与声明顺序，恢复后继续合成与中断前一致；损坏快照必须报 ErrBadSnapshot 且不动现有画面；颜色目标的分辨率与效果数设硬上限，超限在声明阶段就拒绝，避免运行期扩容；统计表按效果数封顶，不允许无限追加。

验收：go test ./... 15 条全部通过，测试文件不得修改、跳过或按固定输入返回常量；并发合成与快照恢复自行验证，重采样与羽化边界在临时程序里各跑一遍；只做本地实现，不动其他模块。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-b-11`
- 实现文件：`postfx_mask_compositor.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`PostFxMaskCompositor`
