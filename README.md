# 粒子子发射链与帧内预算

## 题面

重击特效上线后，同屏十几只怪被击杀时碎片会成片消失，个别帧帧率反而更高，战斗策划判断是子发射整批被丢掉；另一处是父特效提前销毁后，本该延迟出现的拖尾整段没了。范围在 gsb/batch49-go-game-b-12 的 subemitter_chain.go，类型 SubEmitterChain 负责在粒子死亡、碰撞或存活到期时触发声明好的子发射器，并把跨发射器的粒子总量控制在预算内。每个发射器带容量、单次爆发数、存活时长、优先级和两组继承系数，子发射关系按触发种类挂在父发射器上，调用方每帧推进一次并读取报告。

公开接口：NewSubEmitterChain(limits ChainLimits) *SubEmitterChain、Register(emitterID string, def EmitterDef) error、Link(parentID, childID, trigger string) error、Links(emitterID string) []LinkDef、Emit(emitterID string, count int) error、Step(dt float64) StepReport、Alive(emitterID string) int、PendingTriggers(emitterID string) int、Frame() int、Capacity(emitterID string) (int, bool)、Snapshot() ([]byte, error) 与 Restore([]byte) error。StepReport 至少给出本帧生成、过期、子发射、合并、丢弃、深度受限与存活总数，性能面板直接读这些数。

要修好的行为：发射器 ID 为空、容量或单次爆发数非正、存活时长非正报 ErrInvalidEmitter；重复注册报 ErrDuplicateEmitter；link 的两端未注册报 ErrUnknownEmitter，触发种类不在 death、collision、expiry 之内报 ErrUnknownTrigger；触发关系成环或自指报 ErrCycle，且失败不得留下边；直接发射超过容量报 ErrOverCapacity 且不部分生成；存活时长耗尽的那一步才计入过期，粒子在寿命边界上按大于等于判定；死亡粒子按声明顺序触发子发射器，子粒子的爆发数按触发次数累加；同一帧内指向同一子发射器的多次触发合并成一次发射，合并次数写进报告；每帧新增粒子总数受上限约束，超出部分计入丢弃而不是驱逐存活粒子；全链粒子总量受预算约束，直接发射越界报 ErrBudgetExceeded；递归深度超过上限的触发被拦下并计入深度限制计数；排放按优先级从高到低处理。

硬约束：同一帧里重复触发必须合并，不能按触发次数逐次生成；被容量上限丢弃的粒子不计入触发次数，否则子发射计数会虚高；父粒子在延迟窗口内被销毁时，已排队的子发射仍要执行，不能连带取消；位置继承比例与速度继承比例是两组独立参数，各按自己的系数传递，不能共用一个系数；快照要保存粒子、队列、帧号与随机序列位置，恢复后继续推进的结果与中断前一致，损坏快照报 ErrBadSnapshot。

容量按发射器单独计算，预算按全链计算，两者同时命中时先报容量错误；注册表与链边表都设数量上限，注册阶段超限直接拒绝。每帧的触发队列按声明顺序结算，报告里的计数必须与队列实际出队条数一致，不能虚报。

验收：go test ./... 15 条全部通过，测试文件不得修改、跳过或按固定输入写死；同帧爆量、延迟队列与恢复自行构造验证，逐条核对报告里的生成与丢弃计数；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-b-12`
- 实现文件：`subemitter_chain.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`SubEmitterChain`
