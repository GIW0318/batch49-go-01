# 邮箱域到租户 SSO 连接路由

## 题面

邮箱域迁移之后，一家集团把同一个域名同时登记给了两个租户，登录页一会儿跳到 A 公司一会儿跳到 B 公司；另一次事故是子域声明还没验完，员工就被送进了父域的 SSO，更麻烦的是域名释放后系统仍按旧归属跳转，员工被送到已经停用的连接，安全团队要求把归属表和路由链一起重做。范围落在 gsb/batch49-go-safety-a-24 的 sso_domain_router.go，类型 SsoDomainRouter，负责登录时按邮箱域选择租户 SSO 连接。公开入口保持：NewSsoDomainRouter(limits DomainRouterLimits) *SsoDomainRouter、Route(email string) (Route, error)、Claim(tenantID, domain string) (Claim, error)、Verify(claimID, proof string) error、Release(tenantID, domain string) error、Invalidate(domain string) error、Snapshot() ([]byte, error)、Restore(data []byte) error、Lookup(domain string) (Route, bool)、Claims(tenantID string) []Claim、Disputes() []Claim、Capacity() (int, int)。

要把下面这些行为做实：1) 声明落在待验证态，标识非空，域名原样保存；2) 空租户报 ErrBlankTenant，空域名报 ErrBlankDomain，邮箱缺少本地部分、域部分或含多个 @ 报 ErrBadEmail；3) 只有验证通过的声明才生效，证明为空报 ErrBadProof，未知声明报 ErrUnknownClaim；4) Route 按邮箱域精确命中生效声明并返回租户、命中域与声明标识，邮箱与域名统一按小写归一后再匹配，命中结果里保留调用方原始邮箱，未命中时返回 Default 为 true、租户为空的默认路由而不是报错；5) 同一域两个租户声明时先验证者生效，后声明者冻结，不得按最近声明覆盖；6) 子域未验证时不得回退到父域连接，只能走默认登录；7) Release 撤回本租户在该域的声明，之后的 Route 不再命中，没有声明时撤回报 ErrNoClaim，释放后允许重新声明但必须重新验证；8) 域名路由表容量按已声明域名计数，待验证与生效都占位，满了报 ErrDomainLimit；9) 单租户声明数按未释放声明计数，超限报 ErrClaimLimit；10) 域名归属、验证时间与冻结态要能整份快照并在新实例恢复，损坏快照报 ErrBadSnapshot；11) 缓存失效前旧路由仍生效，但连接停用后必须立即清缓存；12) Capacity 返回当前占用与上限，占用数包含待验证、生效与冻结声明，Disputes 只列冻结态声明。

硬约束：并发声明、验证与撤回按域名串行，同一域不会同时出现两个生效声明；路由查询与归属更新之间看不到半更新状态，重启后归属与冻结态延续；路由表容量是硬上限，超限拒绝而不是淘汰已有归属，撤回声明要立刻把该域从占用里释放；域名比较统一按小写归一，避免同一域因大小写差异占到两条。

验收：go test ./... 15 条用例全部通过，测试不得修改、跳过或按固定输入写死；并发声明与重启恢复自行验证；只做本地实现，交付时附一页归属与缓存口径说明。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-a-24`
- 实现文件：`sso_domain_router.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`SsoDomainRouter`、`Route`、`Claim`、`DomainRouterLimits`
