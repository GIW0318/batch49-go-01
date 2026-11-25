# 混音块调度、滤波状态连续与欠载补齐

## 题面

录音棚按块渲染时，前一块的尾音被吃掉，切成小块之后参数斜坡忽快忽慢，供给一断整段变成爆音。问题落在 gsb/batch49-go-game-c-02 的 mix_block_scheduler.go，需要把 MixBlockScheduler 这条块处理链补齐。对外签名：NewMixBlockScheduler(sampleRate, blockSize int) (*MixBlockScheduler, error)；SubmitBlock(in Frame) (Frame, error)；SetBlockSize(n int) error；SetSampleRate(hz int) error；SetParam(name string, v float64) error；Param(name string) float64；SetBypass(on bool)；ResetFilter() error；LatencySamples() int；Stats() MixStats；Snapshot() ([]byte, error)；Restore([]byte) error。参数只有 cutoff、gain、mix 三类。

要修好的行为：采样率只接受 8000 到 192000，块大小只接受 1 到 4096，构造与两个 setter 都要校验，越界报错且不改变现有配置；左右声道长度必须相等且非空，长度超过当前块大小直接报错，不许截断；不足一块的输入按静音补到块大小，输出长度恒等于块大小，补零帧数与欠载次数分别累计；正好一块的输入不补零也不增加欠载计数；一阶低通系数按截止频率与当前采样率计算，滤波状态跨块保持，块边界不允许从零重新起算；欠载补出来的静音块也要推进滤波状态，恢复供给后的第一块不能出现阶跃瞬态；采样率变更要按新采样率重算滤波系数与延迟补偿样本数，不能沿用旧值；块大小变更后参数斜坡按样本数换算推进量，不能按块数推进，同一段毫秒数在大块与小块下到达目标的时间必须一致；旁路打开时输出等于补零后的输入，不经过滤波，计数照常累加；cutoff 取值 100 到 20000、gain 取值 0 到 4、mix 取值 0 到 1，未知参数报错，读值返回当前目标；统计要给出块数、输入帧、输出帧、补零帧、欠载次数与当前配置，重置滤波器只清状态不清计数；参数斜坡的起点取当前值，中途再次设值要以当时的值作为新起点，不能跳回上一次的目标；延迟补偿按固定的毫秒预算换算成当前采样率下的样本数并向上取整，结果不小于 1；采样率与块大小变更只影响之后的提交，已经返回的块不做回改；快照带走配置、滤波状态、参数与统计，恢复全有或全无，坏数据报错且不动现有状态。

硬约束：同一时刻只允许一块在处理，并发提交按到达顺序串行，计数不能丢；块大小与采样率有硬上限，任何路径都不得按调用方传入的长度预分配超过上限的缓冲；ResetFilter 与 SubmitBlock 并发时，要么读到全部旧状态，要么读到清零后的全部新状态。验收以 go test ./... 的 15 条全部通过为准，测试不得修改、跳过或按固定输入写死；并发与重启恢复自行验证；只做本地实现，交付源码、运行输出与容量说明。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-c-02`
- 实现文件：`mix_block_scheduler.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`MixBlockScheduler`，辅助类型 `Frame`、`MixStats`
