# 服务账号密钥申请审批与到期治理

## 题面

服务账号密钥的季度审计卡住了几件事：有人提交申请后连点两次审批，两条记录看着像两个人，实际是同一个主管；有张工单在审批列表里躺了半年，补批之后密钥当年就签了出来，有效期还是从签发那一刻重新算；还有管密钥的同事把明文交给下游之后，在后台列表里也能看到完整密钥，谁拷走的说不清。

落点在 gsb/batch49-go-safety-a-10 的 service_account_key_issuer.go，类型 ServiceAccountKeyIssuer，负责服务账号密钥从申请到启用的工单流转。需要保持的公开签名：NewServiceAccountKeyIssuer(Options) *ServiceAccountKeyIssuer、Request(accountID, requesterID string, spec KeySpec) (Request, error)、Approve(requestID, approverID string) (Request, error)、Reject(requestID, approverID, reason string) (Request, error)、Issue(requestID string) (IssuedKey, error)、Activate(keyID string) error、Disable(keyID, reason string) error、List(accountID string) []KeyMeta、Get(requestID string) (Request, bool)、LookupKey(keyID string) (KeyMeta, bool)、Snapshot() ([]byte, error)、Restore([]byte) error。

需要做到：
一、账号、申请人、审批人为空报 ErrBlankID；密钥名为空、作用域为空、有效期不为正报 ErrBadSpec；有效期超过硬上限报 ErrTTLTooLong，正好等于上限要接受。
二、申请初始为待审状态，必填审批人数取 Options，申请号与序号单调递增；同一账号待审申请数设上限，超出报 ErrRequestLimit，已签发或已终结的申请不再占用名额。
三、申请人不能审批自己的申请，报 ErrSelfApproval 且不计票；审批人凑满人数后申请转为已批准，之后再审批报 ErrRequestClosed，不再记票。
四、同一审批人重复审批只计一票，不能靠刷新凑够人数；并发审批按请求号收敛，最终票数正好等于所需人数。
五、只有已批准的申请能签发，待审报 ErrNotApproved，未知申请报 ErrRequestNotFound，重复签发报 ErrAlreadyIssued，签发成功后申请置为已签发。
六、签发返回的明文只出现一次，长度不少于 32 位，同时给出前 8 位前缀；之后 List 与 LookupKey 只暴露前缀，快照里不得出现明文。
七、签发后有一段启用延迟，未到时间 Activate 报 ErrTooEarly；到期之后 Activate 报 ErrKeyExpired，到期自动禁用，密钥到期后再启用必须重新走签发，不得原地延长有效期。
八、Disable 必须给出非空原因，否则报 ErrBadReason；未知密钥报 ErrKeyNotFound；已禁用密钥再 Activate 报 ErrKeyDisabled。
九、单账号密钥数设上限，超出报 ErrKeyLimit；List 只返回本账号密钥并按签发顺序升序，每条带作用域、启用时间与到期时间。
十、审批被拒后申请终结，重新提交要新建申请，不复用原审批票，旧票也不能再签发。
十一、快照要带申请、密钥元数据、启用与到期时间，重启后待激活与到期判断按原时间继续；损坏快照报 ErrBadSnapshot 且现网状态不变。
十二、并发签发同一申请只能成功一次，多出来的返回 ErrAlreadyIssued；快照与写入互斥，读到的要么是全部旧状态要么是全部新状态。

验收：go test ./... 15 条必须全过，不得修改测试、跳过或按固定输入写死；并发与重启场景自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-a-10`
- 实现文件：`service_account_key_issuer.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`ServiceAccountKeyIssuer`
