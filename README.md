# 账号找回案件

## 题面

一位用户的邮箱被撞库，客服按流程给他开了找回工单；三小时后同一个员工又提了一次，系统又开了一条并行工单，两条审批流各自点了通过，账号在等待期内被改了两次主邮箱。风控复盘时还发现，通过后的恢复会话能直接关掉二次认证，等于把刚补上的门又拆了。改动落在 gsb/batch49-go-safety-a-05 的 account_recovery_casefile.go，类型 AccountRecoveryCasefile。关键签名保持：NewAccountRecoveryCasefile(limits Limits) *AccountRecoveryCasefile、SetNow(now time.Time)、Open(userID string, channel Channel, evidence []Evidence) (Case, error)、Submit(caseID string, e Evidence) error、Contest(caseID, actorID, reason string) error、Approve(caseID, reviewerID string) (Grant, error)、Cancel(caseID string) error、Get(caseID string) (Case, error)、Advance(now time.Time) ([]string, error)、Permitted(grantID string, op Operation) (bool, error)、Grant(grantID string) (Grant, bool)、Snapshot() ([]byte, error)、Restore([]byte) error。

要修的点：案件状态用 CaseEvidencePending、CaseWaiting、CaseReviewPending、CaseApproved、CaseRejected、CaseCancelled 六个常量，案件序号单调递增。一、用户号为空或只有空白报 ErrBlankUser，入口渠道不是 email、sms、support 报 ErrBadChannel，证据一条都没报 ErrNoEvidence，证据 ID 只有空白报 ErrBadEvidence，都在 Open 当场拦下。二、证据已过期报 ErrEvidenceExpired，单案证据数超过 MaxEvidencePerCase 报 ErrEvidenceLimit，Submit 也受同一上限约束。三、开案时证据全部已核验就直接进等待期，WaitUntil 为当前时刻加 WaitPeriod，等待期从证据齐备那一刻起算，不得从开案时刻倒推；还有未核验的停在待证态，待证态收到补齐的核验证据后转等待期，之后 Submit 报 ErrCaseState。四、等待期未满 Approve 报 ErrCaseState，状态不变；Advance 把到期的等待期案件转待裁决并按 ID 升序返回，反复调用不重复放行。五、审核人为空报 ErrBadActor，案件不存在报 ErrCaseNotFound；Approve 只能对待裁决案件签发，签发后案件为 approved。六、Grant 要带案件、用户、签发时刻与冷却截止，冷却截止为签发时刻加 CoolingPeriod；冷却期内只允许改密与重新登记因子，这两类之外一律拒绝。七、等待期内原所有者提出异议即冻结案件转人工，不得按时自动通过。八、重复申请要并入原案件而不是新开，避免同一账号并行两条审批流。九、恢复会话在冷却期内不得关闭二次认证或更换主邮箱，这两类操作要再等一个周期。十、单账号找回次数受 MaxAttemptsPerUser 限制，已取消与已驳回计入次数，次数按案件计、同一案件内的重复提交不额外计数，超出报 ErrAttemptsExceeded，别的账号不受影响。十一、Cancel 未知案件报 ErrCaseNotFound，已通过或已取消的案件再 Cancel 报 ErrCaseState，取消后 Approve 报 ErrCaseState。十二、损坏快照 Restore 报 ErrBadSnapshot 且不动现网。

硬约束：同一案件的并发 Approve 只能签出一份恢复会话，其余请求报 ErrCaseState；单账号同时在途的案件数与单案证据数都是硬上限。Snapshot 要覆盖案件状态、证据、等待期截止、尝试次数与恢复会话，Restore 整份替换，重启后等待期剩余时间延续，同一个 Grant 的冷却截止不因重启而重置；Get 返回的是副本，调用方改返回值不得影响内部状态。验收以 go test ./... 的 15 条用例全绿为准，测试不许改、不许跳过、不许按固定输入写死；并发与重启续等自行验证；只做本地实现，交付源码、跑测记录和一页以内说明。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-a-05`
- 实现文件：`account_recovery_casefile.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`AccountRecoveryCasefile`
