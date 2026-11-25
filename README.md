# DDoS 源认证挑战与分区限速

## 题面

周末的流量图上，一个段在四十分钟里轮换了两千个源地址，每次只发一条登录请求；接入层对新源一律直接放行，后端的验证码形同虚设；值班同学手工加黑名单后，攻击方换到同段的另一个地址继续打，限速键也被打散；更麻烦的是，已经通过校验的源在令牌刷新后又被重新发起挑战，正常用户被反复弹验证码。改动落在 gsb/batch49-go-fix-a-06 的 challenge_rate_gate.go，类型 ChallengeRateGate。关键签名保持：NewChallengeRateGate(cfg GateConfig) *ChallengeRateGate、SetNow(now time.Time)、Admit(source string) (Decision, error)、Verify(challengeID, nonce, digest string) (Pass, error)、AllowPrefix(cidr string) error、DenyPrefix(cidr string) error、Challenge(challengeID string) (Challenge, bool)、Bucket(source string) (BucketState, bool)、Sweep() int、Stats() GateStats、Snapshot() ([]byte, error)、Restore(data []byte) error。

要修的点：一、源地址为空报 ErrBlankSource，非法 IP 报 ErrBadSource；IPv6 按配置位宽聚合成前缀，IPv4 映射地址按 IPv4 处理。二、新源第一次 Admit 返回 challenge，难度取 DifficultyBase，挑战号非空；挑战按前缀存放并在 TTL 内复用，同段不同地址拿到同一个挑战。三、挑战 TTL 到期后 Verify 报 ErrChallengeExpired，已解出的挑战再次提交报 ErrReplay。四、解答为挑战号与 nonce 以冒号拼接后的 SHA-256 十六进制串，前 Difficulty 个字符必须为 0；长度不对或不满足前导零报 ErrBadSolution。五、解答成功签发 Pass，前缀与挑战一致，有效期 PassTTL，令牌桶补满到 Burst；持有未过期 Pass 的后续请求按桶放行。六、令牌桶按 RefillPerSecond 补充并以 Burst 封顶，每放行一次扣一个；不足一个时返回 reject，RetryAfter 给出补足一个所需时长，报 ErrRateLimited。七、解答失败计入该前缀，累计到 MaxFailures 后封禁 BlockDuration，期间 Admit 与 Verify 都报 ErrBlocked；封禁到期后允许重新挑战。八、允许名单只免挑战，仍受令牌桶与封禁约束；拒绝名单先于允许名单生效，多个前缀命中取最长匹配，长度相同按拒绝优先。九、重复挑战的难度按失败次数逐级上调并以 DifficultyMax 封顶；安静超过 PenaltyDecay 后惩罚逐级衰减。十、跟踪源数量受 MaxSources 限制，新源到来时淘汰最久未使用且没有有效 Pass、也不在封禁中的源并计数，没有可淘汰项时报 ErrSourceLimit。十一、Sweep 清理过期挑战、过期 Pass 与长期空闲的桶并返回条数；Stats 给出来源数、挑战、Pass、放行、挑战中、限速、封禁、失败与淘汰计数。十二、Snapshot 覆盖前缀桶、挑战、Pass 与全部计数，Restore 整份替换，损坏快照报 ErrBadSnapshot 且不动现网，恢复后 Pass 与计数器继续有效。

硬约束：并发 Admit、Verify 与 Sweep 必须串行裁决，同前缀的挑战与失败计数原子更新；桶容量与源上限只放行配置值，不得越过；快照与在线请求互斥，恢复后计数器与桶状态不回退。验收以 go test ./... 的 15 条用例全绿为准，测试不许改、不许跳过、不许按固定输入写死；并发与重启恢复自行验证；只做本地实现，交付源码、跑测记录和一页以内说明。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-fix-a-06`
- 实现文件：`challenge_rate_gate.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`ChallengeRateGate`
