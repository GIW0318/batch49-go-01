# 无密码登录投递路由

## 题面

短信验证码在晚高峰成片收不到，另一批投诉是同一用户连点重新发送后，邮箱里躺着两封都还能用的登录链接。值班同学临时把短信通道整体下掉才压住第一波投诉，结果邮件链接又开始重复发送，说明候选排序、抑制期与重试三件事没有串在一条链上。问题落在 gsb/batch49-go-safety-a-21 的 passwordless_delivery_router.go，类型 PasswordlessDeliveryRouter，负责把无密码登录凭证按用户可用通道投递与重试。公开入口保持：NewPasswordlessDeliveryRouter(cfg RouterConfig) *PasswordlessDeliveryRouter、Route(userID string, req DeliveryRequest) (Plan, error)、Report(planID string, r DeliveryReport) error、Suppress(channelID, reason string, until time.Time) error、Retry(planID string) (Plan, error)、Health(channelID string) (ChannelHealth, error)、Snapshot() ([]byte, error)、Restore(data []byte) error、Lookup(planID string) (Plan, bool)、Preference(userID string) []string、Usage(userID string, at time.Time) int、ClearSuppression(channelID string) error。

要把下面这些行为做实：1) 候选通道按用户偏好顺序排列，偏好里没写的按邮件链接、邮件验证码、短信验证码兜底，偏好里出现未知通道报 ErrBadChannel，Preference 只返回用户显式给出的偏好、不回填默认通道；2) 空用户标识在任何投递动作前就报 ErrBlankUser；3) 健康分初始 70，投递成功加 10，软失败减 5，硬失败减 25，分数压在全闭区间 0 到 100；4) 抑制期内的通道不进入候选，Suppress 的截止时间早于当前时刻报 ErrBadSuppression，抑制必须带非空原因；5) 硬失败与退信把本次计划降级到下一候选通道，尝试序号加一，计划标识保持不变；6) 首选通道软失败可按退避重试，只有硬失败或处于抑制期才降级到下一通道；7) 同一次请求重试复用原投递标识，避免同一凭证被重复发出；8) 同一请求重试不得让用户同时持有两条可用凭证，旧凭证要同步失效；9) 单请求尝试次数用尽报 ErrAttemptsSpent 并把计划标成 exhausted，已投递或已用尽的计划再重试报 ErrAlreadyFinal；10) 单用户在滑动窗口内的发送次数超上限报 ErrRateLimited，窗口起点与长度取自 RouterConfig；11) 报告里未知计划报 ErrUnknownPlan，未知结果码或报告通道与计划当前通道不一致报 ErrBadReport，退信与硬失败自动抑制该通道三十分钟，Health 返回值带上被查询的通道标识；12) 抑制期结束后通道恢复候选，但历史健康度仍影响排序。所有时间判定一律用请求里的 Now 或报告里的 At，不读系统时钟；构造函数要把没填的上限补成可用默认值。

硬约束：并发 Route 与 Report 按用户串行裁决，同一请求不会被判成两条计划；健康分、抑制期、频次窗口、偏好与计划状态要能整份快照并在新实例上恢复，恢复后窗口内已用次数不重置；损坏快照报 ErrBadSnapshot 且不动现有状态；单用户计划数与通道表都有容量上限，超限直接报错而不是淘汰在用记录，容量上限按 RouterConfig 里的配置生效。

验收：go test ./... 15 条用例全部通过，测试不得修改、跳过或按固定输入写死；并发投递与重启恢复自行验证；只做本地实现，交付时附一份投递排序与抑制期口径说明。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-a-21`
- 实现文件：`passwordless_delivery_router.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`PasswordlessDeliveryRouter`、`Plan`、`DeliveryReport`、`ChannelHealth`、`RouterConfig`
