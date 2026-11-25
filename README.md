# batch49-go-safety-a-14

- 模块路径：`gsb/batch49-go-safety-a-14`
- 包名：`task`
- 主文件：`impersonation_session_lease.go`
- 验收用例：`tests/test_impersonation_session_lease_test.go`

## 运行方式

在包根执行：

```bash
go test ./...
```

## 题面

安全评审记了一条：排障用的代操作入口在租约到期后，管理员仍能用已经打开的页面把表单提交成功，等于凭“页面还在”绕过时限；另一个案例里，一位用户撤销了与某管理员的代操作授权，结果该管理员名下其它用户的租约被一起终止；还有管理员拿着只读租约直接调用写接口，系统只弹了提示却仍然放行。代操作治理逻辑位于模块 gsb/batch49-go-safety-a-14，主文件 impersonation_session_lease.go，入口类型 ImpersonationSessionLease，请把同意、租约、会话与操作信封补齐到可交付状态。

流程是：管理员先发起同意请求，写明管理员、用户、范围与只读或可写模式；用户批准后换取租约；租约绑定管理员、用户和模式，到期硬失效，用户可在任意时刻撤销；每次授权判定记录操作信封，写操作必须逐条留痕。同意与租约都有时限，同意只能使用一次，租约同一时刻只允许一个活动会话；单用户活动租约数与单管理员持有数都有上限。授权判定对同一租约串行化，信封序号按租约严格递增。

需要落到的行为：
1. 发起同意校验空管理员、空用户、空范围和非法模式。
2. 同意记录包含范围、模式、到期时间和待批准状态，可反查。
3. 换发租约校验同意是否存在、是否属于该用户、是否已被使用。
4. 同意超时后不可换发，错误要与未知同意区分开。
5. 租约记录包含到期时间与剩余时长，剩余时长按注入时钟计算；活动租约按编号排序返回。
6. 单用户与单管理员的在租上限分别生效，超限拒绝换发。
7. 会话只能开在活动且未撤销的租约上，同一租约重复开会话报错。
8. 关闭会话后租约可再次开新会话，关闭未知会话报错。
9. 授权校验空动作与未知会话，未知错误必须分别可判别。
10. 写动作在可写租约上放行，在只读租约上拒绝并留痕。
11. 撤销立即对已有会话生效，撤销动作本身进入操作信封并标记为未放行。
12. 操作信封按租约保留最近若干条，快照包含同意、租约、会话与信封，恢复后剩余时长与撤销态延续。

关键接口：
- NewImpersonationSessionLease(limits Limits) *ImpersonationSessionLease
- (m *ImpersonationSessionLease) RequestConsent(adminID, userID, scope string, mode Mode) (Consent, error)
- (m *ImpersonationSessionLease) Grant(consentID, userID string) (Lease, error)
- (m *ImpersonationSessionLease) Begin(leaseID string) (Session, error)
- (m *ImpersonationSessionLease) Authorize(sessionID, action string) error
- (m *ImpersonationSessionLease) Revoke(leaseID, actorID, reason string) error
- (m *ImpersonationSessionLease) Active(userID string) []Lease
- (m *ImpersonationSessionLease) LeaseState(leaseID string) (Lease, error)
- (m *ImpersonationSessionLease) ConsentState(consentID string) (Consent, error)
- (m *ImpersonationSessionLease) Envelopes(leaseID string) []Envelope
- (m *ImpersonationSessionLease) EndSession(sessionID string) error
- (m *ImpersonationSessionLease) Snapshot() ([]byte, error)
- (m *ImpersonationSessionLease) Restore(data []byte) error

硬约束：二十四个并发授权在同一会话上必须全部成功且信封序号恰好覆盖一段连续整数，不能重号或丢号；用户撤销只终止该管理员与其之间的授权关系，不得牵连其它管理员已获同意的租约；租约到期后未提交的动作一律拒绝，不能凭已渲染的表单继续提交；只读租约上的写操作直接拒绝并留痕，不得提示后放行；所有上限取自构造时的 Limits。验收口径是在包根执行 go test ./... 全部通过，不得修改或跳过用例，不得针对固定输入写死结果，并发与重启场景自行验证，只使用标准库并在本地完成。
