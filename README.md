# 暴露面清单条目生命周期：归一化身份、证据有效期降级与归属继承覆盖

## 题面

资产组季度巡检时发现暴露面清单越跑越乱：一台边缘代理换了证书主体，旧条目还挂在清单里；一条网段的归属从网络组转到应用组，历史里只剩最后一次归属；还有几台机器端口只探测失败一次就被标成关闭，第二天又自己恢复。落点 gsb/batch49-go-safety-c-09 的 exposure_inventory_ledger.go，类型 ExposureLedger，需要把条目身份、证据有效期、归属继承与合并规则固定下来。接口保持：NewExposureLedger(Limits) *ExposureLedger、Observe(obs Observation) (string, error)、Attribute(itemID string, owner string) error、Inherit(netblock string, owner string) error、Sweep(now time.Time) ([]Change, error)、Merge(ids []string) error、Get(id string) (Item, bool)、Items() []Item、Changes() []Change、Stats() Stats、Snapshot() ([]byte, error)、Restore([]byte) error。

要修：一、条目身份按地址、端口、协议与证书主体归一，地址用规范文本、协议转小写、证书主体去首尾空白，任一字段不同就是不同条目。二、地址必须是合法 IP，端口 1 到 65535，协议只允许 tcp 与 udp，观测状态只允许 open 与 closed，观测时间不能为零，否则报 ErrBadObservation 且不落任何数据。三、观测时间可能乱序到达，首见取最早、末见取最晚，证据按观测次数累加，时间不得回退。四、条目编号形如 exp-0001，按创建顺序递增；清单只列存活条目并按编号升序，历史条目仍可按 ID 查到。五、超过有效期没有新观测的条目在 Sweep 时降级为历史并退出清单，每次降级产出一条 downgraded 变更；Sweep 时间不能为零，重复清理不得重复产出。六、端口由开放转关闭要连续两次关闭观测才确认降级，单次探测失败只算抖动，不写变更。七、归属可继承自网段并按最长前缀匹配；单条 Attribute 覆盖继承值并把来源标为 manual，空归属报 ErrBlankOwner，未知条目报 ErrNotFound。八、网段登记非法报 ErrBadNetblock，同一前缀重复登记以后者为准，继承表要能跨重启恢复。九、合并只接受地址与协议都相同的条目，至少两个 ID；未知 ID 报 ErrNotFound，地址或协议不一致报 ErrMergeMismatch，校验失败时不得改动任何条目。十、合并保留最早首见与最晚末见，证据数相加，被并条目记入 MergedFrom 并从清单移除，归属变更历史按时间顺序完整保留，不能只留最终归属。十一、变更记录按发生顺序追加，序号严格递增，类型为 new、owner_changed、downgraded、merged、closed。十二、同一地址更换证书主体属于新条目，旧条目走超期路径退出，不得把旧条目改写成新证书；快照保存条目、继承表、变更与计数，Restore 整体替换，损坏载荷报 ErrBadSnapshot 且不动现有状态。

硬约束：并发 Observe、Attribute 与 Sweep 串行裁决，编号不得重复，归属覆盖不得丢更新；条目上限只放行配置条数；继承表、变更记录与计数跨重启恢复。

验收口径：go test ./... 15 条全过，测试不得修改、跳过或按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-c-09`
- 实现文件：`exposure_inventory_ledger.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`ExposureLedger`
