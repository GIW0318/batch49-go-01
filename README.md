# 登录前风险分级：硬规则优先、可信设备仅免中档与挑战矩阵

## 题面

灰度放量后，风控同学发现两件事对不上：一是被标成高风险的登录，用户只收到一条短信就进来了，事后查是那台设备被标记过可信；二是同一条登录事件被网关重投了三次，档位判定一次比一次高，最后把正常用户锁了半小时。请在模块 gsb/batch49-go-safety-a-29 的 login_risk_tier.go 中补全类型 LoginRiskTier，签名保持：NewLoginRiskTier(Limits, func() time.Time) *LoginRiskTier、Classify(e LoginEvent) (Tier, error)、Challenge(tierID, userID string) (Challenge, error)、Verify(tierID string, proof Proof) (Decision, error)、TrustDevice(userID, deviceID string, ttl time.Duration) error、Override(tierID, actorID, reason string) error、Tiers(userID string) []Tier、Trusted(userID, deviceID string) bool、Snapshot() ([]byte, error)、LoadSnapshot([]byte) error。

要修的行为：值班同学现在只能靠肉眼比对日志，既说不清当时为什么定这一档，也没法向被拦的用户解释。

1. 用户为空、时段不在 0 到 23、地区为空都返回 ErrBadEvent，脏事件不进档位表。
2. 分级先看硬规则：禁用地区直接落到严重档，原因记为硬规则命中，不再叠加软信号。
3. 软信号按条数累计：新设备、异常时段、新网络各加一分，零分低档、一分中档、两分高档、三分及以上严重档，原因记为软信号累计。
4. 档位记录带自增序号，单用户记录数受 MaxTiersPerUser 硬顶，只保留最新若干条。
5. 不同档位下发不同挑战：低档不挑战，中档短信，高档动态口令，严重档直接阻断；档位编号未知返回 ErrUnknownTier。
6. 挑战会带一个本地校验码，验证码一致判放行，不一致判拒绝并记下原因与尝试次数。
7. 同一挑战连错三次即锁定，锁定后再用正确校验码也要返回 ErrTooManyAttempts。
8. 可信设备要带过期时间，用户或设备号为空返回 ErrBlankDevice，有效期不为正返回 ErrBadTTL；单用户可信设备数受 MaxDevicesPerUser 硬顶，超限返回 ErrDeviceLimit。
9. 可信设备在有效期内可以免掉中档，过期后重新按原始信号计分，Trusted 如实反映当前是否还在信任窗口内。
10. 人工覆盖必须带操作人与原因，任一为空返回 ErrBlankField，未知档位返回 ErrUnknownTier，重复覆盖返回 ErrAlreadyOverridden；覆盖后档位置为中档并标记已覆盖。
11. 可信设备豁免不覆盖高档与严重档，高档仍必须挑战。
12. 泄露凭据命中是硬规则，其它信号全绿也不能降到低档。
13. 同一事件重复提交幂等，不得按提交次数累加计数抬高或拉低档位。

硬约束：同一档位的挑战与校验在并发下必须串行，尝试次数不能被两个请求同时读到旧值；进程重启后可信设备的到期时刻、档位记录与序号全部延续，损坏快照返回 ErrBadSnapshot 且不改动现网；可信设备表与档位记录表都受上限硬顶，不允许随登录量无限增长。

验收：go test ./... 的 15 条用例全过，测试不得修改、跳过或按固定输入写死；并发校验与重启恢复自行验证，只做本地实现，交付源码与跑测记录。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-a-29`
- 实现文件：`login_risk_tier.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`LoginRiskTier`
