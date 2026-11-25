# 服务账号短期令牌的受众收窄与代际轮换

## 题面

季度审计里，安全团队把服务账号的令牌问题排成一串：一个只给报表流水线用的账号，手里的令牌却带着对象存储和消息队列两个受众，被下游误用；密钥轮换当天，运维为了不断服把新旧两代令牌一起放行，旧代后来一直没下线；还有一条令牌是从别处转手来的，链路上根本看不出它原本属于谁、被转过几手。改动落在 gsb/batch49-go-fix-a-02 的 sa_token_scope_chain.go，类型 SaTokenScopeChain。关键签名保持：NewSaTokenScopeChain(cfg ScopeConfig) *SaTokenScopeChain、SetNow(now time.Time)、Grant(saID string, audiences []string, ttl time.Duration) (Token, error)、Exchange(parentID string, audiences []string, ttl time.Duration) (Token, error)、Verify(tokenID, audience string) (Principal, error)、Rotate(saID string) (RotationRecord, error)、RevokeGeneration(saID string, generation int) (int, error)、RevokeToken(tokenID string) error、Chain(tokenID string) ([]Token, error)、Status(saID string) ScopeStatus、Sweep() int、Snapshot() ([]byte, error)、Restore(data []byte) error。

要修的点：一、Grant 前先校验，账号为空或受众列表为空、含空白项都报 ErrBlankField；账号未注册报 ErrUnknownServiceAccount；受众不在该账号注册集合内报 ErrUnknownAudience；任何被拒请求都不得留下令牌。二、TTL 必须为正且不超过 MaxTTL，非正报 ErrBadTTL，超上限报 ErrTTLTooLong。三、根令牌分配从 1 起单调递增的签名代，深度 0，父令牌号为空，IssuedAt 取当前时钟，ExpiresAt 等于当前时钟加 TTL；受众去重后按字典升序输出。四、Exchange 的父令牌必须存在、未过期、未撤销；子受众必须是父受众的真子集，相等或越界都报 ErrAudienceWidening；子 TTL 非正报 ErrBadTTL，超过父令牌剩余时间报 ErrTTLTooLong。五、子令牌继承父的账号与签名代，深度加一，父令牌号指向父，到期时间取当前时钟加子 TTL；超过 MaxDepth 报 ErrDepthLimit。六、Verify 对未知令牌报 ErrUnknownToken，过期报 ErrTokenExpired，撤销报 ErrTokenRevoked，受众不在令牌集合内报 ErrAudienceMismatch；通过时返回账号、令牌号、签名代、受众与深度；到期判定可留 ClockSkew 宽限。七、Rotate 把签名代加一，返回新代、被替换的旧代、旧代退役时刻（当前时钟加 OverlapWindow）与旧代在册令牌数；新签发的令牌一律用新代。八、重叠窗内旧代令牌仍可验证，超过退役时刻后旧代令牌报 ErrGenerationRetired，不得继续放行。九、RevokeGeneration 撤销指定代下全部未撤销令牌并返回条数，重复调用返回 0，未知代报 ErrUnknownGeneration；被撤销代的令牌立即失效。十、RevokeToken 撤销单个令牌，重复调用幂等，未知令牌报 ErrUnknownToken；撤销父令牌时其所有后代一并失效。十一、单账号在册令牌数受 MaxTokensPerServiceAccount 限制，超出报 ErrTokenQuotaExceeded；Sweep 清理已过期令牌并返回条数。十二、Status 给出生效代、在册数、已撤销数与已完全退役的代列表；Snapshot 覆盖令牌表、代账本与序号，Restore 整份替换，损坏快照报 ErrBadSnapshot 且不动现网，恢复后新令牌号必须大于恢复前最大号。

硬约束：并发 Grant、Exchange 与 Verify 必须串行裁决，令牌配额只放行配置条数；快照与在线请求互斥，快照只能看到全量旧状态或全量新状态；令牌号与签名代跨重启单调，恢复后不得回退。验收以 go test ./... 的 15 条用例全绿为准，测试不许改、不许跳过、不许按固定输入写死；并发与重启恢复自行验证；只做本地实现，交付源码、跑测记录和一页以内说明。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-fix-a-02`
- 实现文件：`sa_token_scope_chain.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`SaTokenScopeChain`
