# batch49-go-game-c-09

模块路径 `gsb/batch49-go-game-c-09`，包名 `task`，实现文件 `present_mode_arbiter.go`，类型 `PresentModeArbiter`。

## 题面

电竞馆那批 240Hz 显示器在比赛开始前集体出现异常：选手把窗口切到后台看数据，再切回来时画面出现横向撕裂；有台机器的帧间隔掉到 7 毫秒，程序却把它当成超限，一路降级回同步呈现，高手立刻抱怨手感发黏；还有机器在两种模式之间每两帧来回跳一次，帧率表像锯齿一样上下抖。测试同学录了三段视频，同一台机器复现率不到一半，说明判定里混进了随机因素；驱动厂商也说不清是哪一版引入的。请补全模块 gsb/batch49-go-game-c-09 中 present_mode_arbiter.go 的 PresentModeArbiter，由它按最近若干帧的耗时在同步、邮箱与立即三种呈现模式之间做选择。

帧报告由帧时长与是否聚焦组成，判定窗口与分位数来自配置，配置里为 0 的字段取包内默认值。公开接口：NewPresentModeArbiter(cfg ArbiterConfig) *PresentModeArbiter、ReportFrame(d FrameTiming) error、RequestMode(m PresentMode) error、Current() PresentMode、Stats() PresentStats、SetVRRRange(lo, hi time.Duration) error、SetHysteresis(n int) error、SupportedModes() []PresentMode、Reset() error、Snapshot() ([]byte, error)、Restore(data []byte) error。

需要落实的行为：1）判定只用最近 Window 帧的滑动分位，按 Percentile 取最近秩，不能看单帧值，窗口不足时按已有样本计算；2）分位超过 VRRMax 才算超出可变刷新率区间，切回同步呈现；3）分位低于 VRRMin 说明帧间隔过短，切到立即呈现，不能当成超限而误降级；4）分位落在区间内时使用邮箱呈现，SupportedModes 按同步、邮箱、立即的固定顺序返回；5）候选模式要连续满足 Hysteresis 帧才切换，连续计数只在候选保持不变时累加，中途出现不匹配的帧要把连续计数清零；6）距上次切换不足 MinResidence 帧时不得再次切换；7）切换当帧不计入 Frames 与 Tears，滞回连续计数从零重算；8）失焦时立即强制同步呈现，该帧不进入窗口也不推进统计；9）非同步模式下帧时长超过 RefreshInterval 记一次撕裂，Stats 同时给出帧数、撕裂数、切换数、窗口分位与滞回计数；10）RequestMode 只接受已知模式，未知模式返回 ErrUnknownMode 并保持当前模式，不被支持的模式同样报错且不得悄悄降级；11）SetVRRRange 拒绝下界为负或下界大于上界的情况并返回 ErrBadVRRRange，SetHysteresis 只接受不小于 1 的值，否则 ErrBadHysteresis。

硬约束：ReportFrame 会被渲染线程与遥测协程并发调用，Current 与 Stats 只能读到完整状态，切换不能出现模式已换而窗口还是旧样本的中间态；快照要覆盖当前模式、最近窗口与计数器，Restore 之后按恢复的窗口继续判定，损坏快照返回 ErrBadSnapshot 且不动现有状态。并发上报与恢复需要自行压测，上报与切换的先后顺序要能对得上。

验收：go test ./... 的 15 条用例全部通过，测试文件不得修改、跳过或按固定输入写死；只做本地实现。交付源码、运行记录和切换策略说明，并发与恢复的验证要有成对的命令和输出。

## 运行

```bash
cd batch49-go-game-c-09
go test ./...
```
