# 投递排序与抑制期口径

## 候选排序

- 先使用 `DeliveryRequest.PreferOrder`；同一用户后续请求未带偏好时，复用已保存的显式偏好。
- `Preference` 只复制并返回用户显式提交的顺序，不追加默认通道。
- 未显式列出的通道按 `email_link`、`email_code`、`sms_code` 去重后追加。
- 所有新通道的健康分初始为 `70`。实际选择可用通道时按健康分降序稳定排序；分数相同则保持上一步的偏好/默认顺序。
- 因此新通道或健康分相同时，首选顺序仍是用户偏好，缺省首选是 `email_link`；硬失败留下的低健康分会在抑制结束后继续影响后续排序。

## 抑制与恢复

- 抑制判断使用闭区间口径：`now >= SuppressedUntil` 时通道恢复候选；`now < SuppressedUntil` 时跳过。
- 手动 `Suppress` 必须提供非空白原因，且截止时间不能早于路由器已经观察到的逻辑当前时间。
- 路由器不读取系统时钟：`Route` 使用 `DeliveryRequest.Now`，`Report` 使用 `DeliveryReport.At`，`Usage` 使用查询时间。由于 `Suppress` 签名没有时间参数，它使用这些输入推进出的最新逻辑时间校验截止时间。
- `hard_fail` 与 `bounce` 会把健康分减 `25`、增加硬失败计数，并从报告的 `At` 起自动抑制该通道 `30m`。
- `ClearSuppression` 会立即清除已知通道的抑制截止时间和原因。

## 重试、凭证与预算

- `soft_fail` 只减 `5` 分并写入指数退避后的 `NextRetryAt`；`Retry` 仍停留在原通道。
- `hard_fail`、`bounce` 或当前通道在重试裁决时处于抑制期，才沿候选列表降级。
- 同一计划的 `ID` 与 `DeliveryID` 在所有重试中保持不变，`Attempt` 每次实际重试加一。
- 每次重试都会轮换内部凭证版本，并把旧凭证加入该计划的失效列表，避免旧链接继续可用；对外不暴露凭证内容。
- `Attempt >= MaxAttemptsPerPlan` 时，下一次重试返回 `ErrAttemptsSpent` 并把计划置为 `exhausted`；之后重试返回 `ErrAlreadyFinal`。已 `delivered` 的计划同样返回 `ErrAlreadyFinal`。

## 频次、快照与容量

- 滑动窗口为 `(at-Window, at]`：恰好位于 `at-Window` 的一次发送已离开窗口。
- `Route` 和实际 `Retry` 尝试都计入用户发送次数；`Usage` 返回该窗口内的数量。
- 快照包含序列号、逻辑时间、健康分、抑制期、偏好、发送时间戳、计划、候选位置、凭证版本和用户计划索引。恢复后窗口计数不重置。
- 损坏或结构不自洽的快照返回包装后的 `ErrBadSnapshot`；恢复前先完整解析和校验，失败不会替换当前状态。
- `MaxPlansPerUser` 限制单用户保留的计划记录数，`MaxChannels` 限制通道状态表大小；达到硬上限后返回容量错误，不淘汰在用记录。
- `Route` 与 `Report` 通过路由器互斥区串行裁决，因此同一用户的检查窗口、计数和计划推进不会交叉产生两份状态。
