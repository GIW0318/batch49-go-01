# 出站目的地策略求值与重定向逐跳复核

## 题面

运维在出站代理上收紧目的地白名单后，报表任务访问对象存储被拒，客服工单积压，而几个已经被撤销的临时授权仍在代理缓存里继续签发新连接；更麻烦的是一次跨域重定向把请求从加密入口带到了明文地址，审计只记录到最终目标，看不出中间跳。代理侧既没有逐跳的审计字段，也无法解释某次放行到底命中了精确、通配还是类别规则，安全同学要求把判定依据一并落到返回值里；回滚时还发现旧规则可能被重放，值班只能逐条比对代理日志。落点在 gsb/batch49-go-safety-b-12 的 egress_policy_gate.go，类型 EgressGate，需要把目的地匹配、授权签发与重定向复核串成一条可解释的链路。入口 NewEgressGate(cfg Config) *EgressGate，授权 Authorize(req EgressRequest) (Grant, error)，重定向复核 FollowRedirect(grant Grant, next Target) (Grant, error)，撤销 Revoke(tenant string) int，快照 Snapshot() ([]byte, error) 与 Restore([]byte) error，另外提供 MatchRule(tenant string, target Target) (Rule, MatchKind, bool)、Active(tenant string) []Grant、Grant(id string) (Grant, bool)、ValidateRules() error、Advance(ms int64) 与 Clock() int64。

需要落实的判断与边界：1) 域名匹配按精确、后缀通配、类别规则降序取最高优先级，同一档内 Priority 大的先命中；2) 后缀通配只匹配子域，通配串本身与同级仿冒域名都不算命中；3) 类别规则从配置的成员表展开，成员自身及其子域算命中；4) 规则端口与方法集为空表示不限，MatchRule 只按主机与端口选最强规则、不看方法，签发的授权方法集去重后按字典序排列；5) 授权绑定租户、协议、主机、端口、方法集与到期时刻，逻辑时钟从零开始且 Advance 只前进，到期时刻等于签发时的逻辑时钟加配置 TTL，初始跳数为零，时间达到到期时刻即失效；6) 每一跳重定向都要重新求值主机、端口与方法，授权方法集只能收缩不能放宽，新一跳签发后上一跳立即作废；7) 从 HTTPS 跳到 HTTP 一律拒绝并给出降级原因码，目标在白名单内也不放行；8) 同一条授权链最多跟随配置的跳数，超限报 ErrHopLimit；9) 授权一旦过期，整条链失效，链上后续跳即使在有效期内也不得沿用；10) 撤销某租户要清掉它在途授权并返回条数，之后该租户的新申请报 ErrTenantRevoked；11) 在途授权总数有上限，空租户、空主机、非法端口与不支持的协议在入口就报错，ValidateRules 对合法规则返回 nil；12) 快照保存在途授权、撤销租户与逻辑时钟，损坏快照返回 ErrBadSnapshot 且不动现有状态。

硬约束：并发授权、撤销与重定向复核必须互斥，授权上限只放行配置条数；快照与授权写操作互斥，恢复成功前旧授权继续可用；规则选择与授权列表输出不依赖 map 遍历，重复跑结果一致。

验收口径：go test ./... 15 条用例全过，不得修改测试、跳过用例或按固定输入写死；并发与快照恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-b-12`
- 实现文件：`egress_policy_gate.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`EgressGate`
