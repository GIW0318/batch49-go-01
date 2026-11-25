# SSO 连接状态迁移说明

## 状态流转

| 当前状态 | 动作 | 目标状态 | 守卫与失败结果 |
| --- | --- | --- | --- |
| （新建） | `Create` | `draft` | 空租户 `ErrBlankTenant`；无域名/空域名 `ErrBlankDomain`；超域名上限 `ErrDomainLimit`；域名被占用 `ErrDomainTaken`；超租户连接上限 `ErrTenantLimit` |
| `draft` | `VerifyDomain` 首次验证成功 | `verify_pending` | 空证明 `ErrBadProof`；连接不存在 `ErrUnknownConnection`；域名不在配置中 `ErrUnknownDomain` |
| `verify_pending` | 继续验证域名 | `verify_pending` | 同一域名重复验证幂等成功，`VerifiedDomains` 不重复 |
| `draft` / `verify_pending` | `Test` 且全部配置域名已验证 | `tested` | 有域名未验证返回 `ErrDomainUnverified`；结果含 `Checked` 与非 nil `Warnings` |
| `tested` | `Enable` | `enabled` | 未验证完整返回 `ErrDomainUnverified`；未通过测试返回 `ErrBadState` |
| `enabled` | `Disable` | `disabled` | 仅启用态可停用；其他非启用状态返回 `ErrBadState`，重复停用幂等 |
| `disabled` | `Enable` | `enabled` | 仅当该连接曾通过测试且域名仍全部验证；配置与验证域名保留 |
| `enabled` / `tested` / `disabled` | 再次 `Test` | 保持当前状态 | 刷新检查结果，但不把已启用连接降级为 `tested` |

`Rollout` 可在任意已存在连接上设置 `0..100` 的百分数；越界返回 `ErrBadRollout` 且不覆盖旧值。灰度只改变登录路由命中比例，不改变 `State`。

## 登录路由与灰度兜底

- 仅 `enabled` 且邮箱域名精确命中 `VerifiedDomains` 的连接可成为候选；草稿、验证中、已测试和已停用连接均不参与。
- 未显式设置灰度时按 `100%` 命中；显式 `100` 全量，显式 `0` 全不命中。
- 中间比例使用 `FNV-1a(tenantID + email) % 100` 做稳定哈希：同一用户结果稳定，样本量足够时比例趋近设定值，不依赖固定邮箱白名单。
- 未命中新连接时，回退到同租户最早创建的其他启用连接（若存在精确域名匹配则优先）；若不存在第二条启用连接，则由当前唯一可用连接兜底，保证登录不直接报错。已停用连接不参与兜底。

## 强制 SSO 与破窗

- `SetEnforceSSO(tenant, true)` 保存租户级强制开关，`Get` 返回的 `Connection.Enforced` 同步反映该值，并进入快照。
- 强制开关不关闭 `BreakGlass`。非空口令视为应急入口可用；每次成功使用写入 `break_glass_used` 告警，可通过 `Alerts(tenantID)` 读取。
- `SsoLimits.MaxBreakGlassPerDay` 按 UTC 自然日限制成功使用次数；超额返回 `ErrBreakGlassDenied` 并记录拒绝告警。

## 并发、配额与快照

- `TenantSsoConnection` 用一把 `sync.RWMutex` 保护全部状态；所有创建、启用、停用、灰度和恢复写入均串行化，因此同租户操作串行、全局域名唯一、并发启停最终只呈现 `enabled` 或 `disabled` 一个状态。
- 租户连接数按该租户全部连接计数；域名数按提交配置长度检查。失败创建不写入连接、不占用域名，也不淘汰既有记录。
- `Snapshot` 输出带版本号的 JSON，包含连接配置、状态、已验证域名、测试标记、灰度值/是否显式设置、强制开关、破窗用量与告警。
- `Restore` 先在锁外完整解码和校验；JSON 损坏、版本不支持、状态非法、灰度越界、验证域名不属于配置或域名重复占用均返回 `ErrBadSnapshot`，且不会替换或修改现有连接。校验通过后在锁内整份替换运行态。
