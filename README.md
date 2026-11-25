# 扫描目标侧压力自适应与惩罚区恢复

## 题面

一批政务客户这周集中报障。扫描器把一台老防火墙的管理口打到假死，只能重启恢复；同一个目标的阈值降下来以后，旁边几个目标也被一起限速，越限越慢；限速恢复时又在一次成功之后直接冲回满并发，目标第二次被打挂；目标下线后状态一直挂在内存里，跑一天下来表涨到十几万条。

落点在 gsb/batch49-go-safety-e-04 的 target_pressure_governor.go，类型 TargetPressureGovernor，签名保持：NewTargetPressureGovernor(cfg Config) *TargetPressureGovernor、Report(target string, r Outcome) error、Permit(target string) (bool, error)、Release(target string) error、Concurrency(target string) int、Delay(target string) time.Duration、Penalized(target string) bool、Health(target string) int、State(target string) (TargetState, error)、Targets() []string、AddAlias(target, alias string) error、Snapshot() ([]byte, error)、Restore([]byte) error。配置用 Config{MaxConcurrency, BaseDelay, MaxDelay, PenaltyThreshold, RecoveryStreak, MaxTargets}，回报用 Outcome{Kind, Bytes, Duration, At}，Kind 允许 ok、throttled、error、probe 四种。

压力模型：健康分是唯一的状态量，档位、间隔、惩罚都从它派生，不允许旁路修改；每个目标的在飞许可数单独记账，Release 与挂起回收都要把账平掉，不能出现位子被占着没人还的情况。

要修的具体故障与边界：
一、目标名为空报 ErrBadTarget，结果类型不认识报 ErrBadOutcome，被拒绝的回报不得建立目标状态。
二、新目标健康分一百，并发档位等于配置上限，间隔等于 BaseDelay，不在惩罚区。
三、成功加五、限流减二十五、错误减十五，健康分夹在零到一百之间；连续两次限流或健康分不高于 PenaltyThreshold 时进入惩罚区。
四、并发档位按健康分线性折算：配置上限乘健康分再除以一百，向下取整且最小为一；惩罚区内固定为一。
五、请求间隔按健康分分四档：不低于八十为 BaseDelay，六十到七十九为两倍，四十到五十九为四倍，再低为八倍，且一律不超过 MaxDelay。
六、许可发放是原子的：上限为三时五个并发请求只能有三个拿到许可，其余当场拒绝；Release 之后空出的位子必须能再次发放。
七、一个目标降档不影响其它目标的档位与间隔，孤立故障不得扩散。
八、惩罚区内只做小流量探测，探测结果与业务扫描分开计数，业务侧的成功不能顶替探测样本去推动恢复。
九、目标返回成功但内容明显异常（比如零字节）的样本不计入恢复所需的正样本。
十、连续 RecoveryStreak 次合格探测之后才退出惩罚区，退出时健康分只回到惩罚门槛附近，之后逐档爬升，不得一次跳回满档。
十一、同一目标的多个别名地址按归一后的身份共享健康分，不能各算各的。
十二、目标表有上限，超过时回收最久未活动的目标；快照与恢复保留健康分、档位、惩罚状态和别名，损坏数据报 ErrBadSnapshot 且不动现网。

硬约束：Permit、Release、Report 对同一目标的计数必须原子更新，八个 goroutine 同时抢四个许可只能发出四个，不能超发也不能死锁；所有档位与间隔只由健康分和配置推出，不读墙上时钟，同一串回报在任何机器上得到同一组数值。

验收：go test ./... 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-e-04`
- 实现文件：`target_pressure_governor.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`TargetPressureGovernor`
