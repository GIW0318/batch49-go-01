# 多信号设备姿态汇总与转移治理

## 题面

周报里出现一类怪现象：同一批笔记本，有的显示合规，隔一小时再查变成降级；整机加密分数很高的设备因为一条补丁信号过期就被打进隔离池，业务方投诉误伤；员工换组之后，新主管的设备视图里带出了前任账号的历史姿态，合规率被算成了负数。

落点在 gsb/batch49-go-safety-a-07 的 device_posture_aggregator.go，类型 DevicePostureAggregator，负责把 EDR 心跳、磁盘加密、补丁级别、越狱迹象几路信号汇总成设备姿态。需要保持的公开签名：NewDevicePostureAggregator(Options) *DevicePostureAggregator、Report(deviceID string, s Signal) error、Posture(deviceID string) (Posture, error)、Evaluate(userID string) ([]Posture, error)、Quarantine(deviceID, reason string) error、Release(deviceID, approverID string) error、Transfer(deviceID, toUserID string) error、Signals(deviceID string) []Signal、Audit(userID string) []AuditEvent、Snapshot() ([]byte, error)、Restore([]byte) error。

需要做到：
一、每次上报带账号，第一次上报把设备绑定到该账号；设备号或账号为空报 ErrBlankID，未知信号类型报 ErrUnknownKind，分值不在 0 到 100 报 ErrBadScore；设备已被其它账号占用报 ErrDeviceOwner，不能静默改绑。
二、同一设备同一类型只保留时间最新的一条，时间相同保留序号大的；更旧、或时间相同而序号不更大的重复上报报 ErrStaleSignal，且不得改动已存条目。
三、权重为 EDR 心跳 40、磁盘加密 25、补丁级别 20、越狱迹象 15，单条贡献按权重乘分值再除 100 取整；四路齐全且全部新鲜时满分 100。
四、每类信号有独立新鲜度窗口；过期信号按四成权重计入，并把类型列进姿态的 Stale，不得因为过期直接隔离，也不得让其它高分信号抵消硬信号。
五、档位按分数判定：达到 80 合规，达到 40 降级，低于 40 隔离；越狱迹象分值大于 0、或 EDR 上报卸载，属于硬信号，立即隔离。
六、隔离分硬信号与人工两种；人工隔离只有 Release 且审批人不为空才解除，审批人为空报 ErrNoApprover，设备未隔离报 ErrNotQuarantined，隔离原因为空报 ErrBadReason。
七、单设备信号条目数与单账号设备数设上限，超出分别报 ErrSignalLimit、ErrDeviceLimit；Evaluate 只返回本账号设备并按设备号升序；Signals 与 Audit 返回副本，调用方改返回值不影响内部状态。
八、设备转移后旧账号不再列出该设备、新账号可以正常评估；姿态历史不迁移到新账号，只在旧账号留一条转移记录；转给同一账号报 ErrSameOwner，未知设备报 ErrDeviceNotFound。
九、快照要带信号时间、隔离态、审计与序号，重启后新鲜度按原始信号时间判断；损坏快照报 ErrBadSnapshot，现网状态不能被改动。
十、同一设备的并发上报按信号时间裁决，最终只留最新一条；审计按发生顺序追加；快照与写入互斥，读到的要么是全部旧状态要么是全部新状态；所有时间判断统一走 Options.Now。

验收：go test ./... 15 条必须全过，不得修改测试、跳过或按固定输入写死；并发与重启场景自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-a-07`
- 实现文件：`device_posture_aggregator.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`DevicePostureAggregator`
