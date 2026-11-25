# 租户 SSO 连接生命周期

## 题面

一家制造业客户开完 SSO 灰度会后反馈：新连接只放量一成，剩下九成员工登录直接报错；另一家客户则发现域还没验完，管理员就把连接点成了启用，第二天审计问起来没人说得清。灰度只放量一成却把未命中的人挡在门外，这显然不是比例算错，而是未命中分支根本没有兜底。范围落在 gsb/batch49-go-safety-a-23 的 tenant_sso_connection.go，类型 TenantSsoConnection，负责租户级 SSO 连接的配置、验证与启用。公开入口保持：NewTenantSsoConnection(limits SsoLimits) *TenantSsoConnection、Create(tenantID string, cfg ConnectionConfig) (Connection, error)、VerifyDomain(tenantID, connID, domain, proof string) error、Test(tenantID, connID string) (TestResult, error)、Enable(tenantID, connID string) error、Disable(tenantID, connID string) error、Rollout(tenantID, connID string, percent int) error、Snapshot() ([]byte, error)、Restore(data []byte) error、Get(tenantID, connID string) (Connection, bool)、RouteLogin(tenantID, email string) (Connection, error)、SetEnforceSSO(tenantID string, on bool) error、BreakGlass(tenantID, password string) (bool, error)。

要把下面这些行为做实：1) 新建连接落在草稿态，标识非空，域名列表照抄配置里的顺序；2) 空租户报 ErrBlankTenant，配置里没有域名报 ErrBlankDomain，同一域已被别的连接占用报 ErrDomainTaken；3) 单租户连接数到上限报 ErrTenantLimit，上限取自 SsoLimits；4) VerifyDomain 的证明为空报 ErrBadProof，域名不在连接配置里报 ErrUnknownDomain，验证通过后该域进入已验证列表并刷新更新时间，同一域重复验证幂等成功；5) 域名验证未完成的连接不得启用，也不得被登录路由命中；6) 测试只能在域名验证之后进行，测试通过后连接进入已测试态，返回结果带检查项计数与告警列表；7) 只有已测试的连接能启用，启用后登录路由按已验证域匹配到该连接；8) 停用可从启用态直接落地，停用后不再参与路由，配置与已验证域保留；9) 灰度比例以百分数表示，越界报 ErrBadRollout，合法值原样保存，重复设置覆盖旧值，灰度只影响新连接的命中比例，不改写连接本身的启用态；10) 强制 SSO 开启后破窗口令入口仍可用且每次使用要告警，不能一并关闭；11) 灰度期间未命中新连接的用户走旧连接成功登录，不得直接报错；12) 快照恢复后启用态、已验证域与灰度比例延续，损坏快照报 ErrBadSnapshot 且不动现有连接。

硬约束：并发 Create、Enable 与 Rollout 对同一租户串行，同一域不会被两条连接同时占用，同一租户的并发启停只落一个生效状态；连接状态、已验证域、灰度比例与强制开关要能整份快照并恢复；单租户连接数与单连接域名数都有硬上限，超限报错不淘汰在用记录，域名超限时原有域名集合保持不变，租户之间互不影响。

验收：go test ./... 15 条用例全部通过，测试不得修改、跳过或按固定输入写死；并发启用与重启恢复自行验证；只做本地实现，交付源码、跑测记录与一页状态迁移说明。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-a-23`
- 实现文件：`tenant_sso_connection.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`TenantSsoConnection`、`Connection`、`ConnectionConfig`、`TestResult`、`SsoLimits`
