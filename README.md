# 交易确认票据绑定与双人复核治理

## 题面

财务共享中心收到几起重复付款投诉：同一笔大额转账，确认页显示收款人是上海供应商，实际落地却换成了另一个账户，票据照样被兑换；柜员用自己两台手机点确认，按说大额要两个人复核，系统却当成两人；还有人在手机银行确认过，隔十分钟又在网银渠道兑了一次，柜面查不到任何拦截记录。

落点在 gsb/batch49-go-safety-a-08 的 transaction_confirmation_guard.go，类型 TransactionConfirmationGuard，负责高风险交易在认证端的确认闭环。需要保持的公开签名：NewTransactionConfirmationGuard(Options) *TransactionConfirmationGuard、Prepare(opID string, tx Transaction) (Challenge, error)、Confirm(challengeID, principalID string, proof Proof) (Ticket, error)、Redeem(ticketID, opID string) (Decision, error)、RedeemOn(ticketID, opID, channel string) (Decision, error)、Cancel(challengeID string) error、Pending(userID string) []Challenge、LookupChallenge(challengeID string) (Challenge, bool)、LookupTicket(ticketID string) (Ticket, bool)、Snapshot() ([]byte, error)、Restore([]byte) error。

需要做到：
一、操作号或账号为空报 ErrBlankID，金额不为正报 ErrBadAmount，收款人、收款账号、渠道任一为空报 ErrBadTransaction；金额低于小额上限免确认，低于大额上限单人复核，达到大额上限双人复核，档位分别是 exempt、single、dual。
二、交易字段按固定顺序规范化后取摘要，摘要用 SHA-256 十六进制表示、长度 64 位，与操作号一起绑定票据；渠道不参与摘要，但要写成票据的绑定属性。
三、同一操作号重复准备要覆盖旧挑战，旧挑战随后确认报 ErrChallengeStale；已签发票据遇到金额或收款人变化必须作废，不存在容差内放行。
四、小额免确认在准备时直接给出可用票据，该票据同样受票据有效期约束；中额要一名复核人，大额要两名不同的复核人，同一人重复提交不构成第二票。
五、证明缺签名或缺设备号报 ErrBadProof 并累加尝试次数，次数要能在挑战记录上读到；尝试次数用完报 ErrAttemptLimit；未知挑战报 ErrChallengeNotFound，已关闭报 ErrChallengeClosed，过期报 ErrChallengeExpired。
六、票据一次性，重复兑换报 ErrTicketUsed；兑换时操作号不符报 ErrOpMismatch 且不消耗票据；票据过期报 ErrTicketExpired；换一个渠道兑换报 ErrChannelMismatch；成功的兑换返回操作号、票据号与原因。
七、撤销开放挑战成功，重复撤销报 ErrChallengeClosed，撤销未知挑战报 ErrChallengeNotFound；已撤销挑战不能再确认，也不再出现在待确认列表里。
八、单用户待确认挑战数设上限，超出报 ErrChallengeLimit；Pending 只列开放挑战，并按创建顺序返回。
九、快照要带挑战、票据、摘要与原始到期时间，重启后未完成挑战按原时间失效、已兑换票据仍算已兑换；损坏快照报 ErrBadSnapshot，现网状态不能被改动。
十、双人挑战的并发确认只能签发一张票据，多出来的提交返回关闭错误；确认与兑换互斥，同一票据并发兑换只有一次成功。

验收：go test ./... 15 条必须全过，不得修改测试、跳过或按固定输入写死；并发与重启场景自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-a-08`
- 实现文件：`transaction_confirmation_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`TransactionConfirmationGuard`
