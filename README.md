# 身份证明案件：供应商结果归一、证件冲突转人工与有效期降级

## 题面

开户视频见证上线后，客服每天收到十几条投诉：有人传了身份证和活体视频，页面卡在审核中一夜不动；有人换了张身份证重传，系统直接给他过了，可同一张证件明明还挂在另一个账号上；还有老客户反馈，年度复核过期以后连查余额都被拦，账号像是被冻结。请在模块 gsb/batch49-go-safety-a-27 的 identity_proofing_case.go 中补全类型 IdentityProofingCase，签名保持：NewIdentityProofingCase(Limits) *IdentityProofingCase、Start(userID string, level Level) (Case, error)、SubmitDocument(caseID string, doc Document) error、SubmitLiveness(caseID string, frame []byte) error、ApplyVendorResult(caseID string, r VendorResult) (Verdict, error)、Review(caseID, reviewerID string, approve bool) error、Status(userID string) (ProofState, error)、ExpireCase(caseID string) error、CaseState(caseID string) (string, error)、ThresholdFor(level Level) (float64, error)、Snapshot() ([]byte, error)、LoadSnapshot([]byte) error。

要落地的行为：这批案件还得能回放，谁提交了什么、供应商给了几分、谁复核的，事后都要对得上。

1. 空用户返回 ErrBlankUser，等级超出基础、标准、高保障三档返回 ErrBadLevel；新案件从收件态开始，带自增序号，单用户案件数受 MaxCasesPerUser 硬顶，超限返回 ErrCaseLimit。
2. 证件摘要为空返回 ErrBadDocument；同一案件只接受第一份证件，再传不同摘要返回 ErrDocumentLocked，摘要不得被悄悄覆盖；重复提交同一份摘要按幂等处理。
3. 活体帧为空或长度为零返回 ErrEmptyFrame；证件与活体都齐了，案件才从收件态进入待比对态，只有一份材料时状态不能提前跳。
4. 案件不在待比对态时提交供应商结果返回 ErrNotReady，未知案件返回 ErrUnknownCase，供应商结果种类不认识返回 ErrBadVendorResult。
5. 三档阈值分别是 0.60、0.75、0.90，ThresholdFor 对非法等级返回 ErrBadLevel。
6. 供应商分数达到阈值判通过，低于阈值判拒绝，恰好等于阈值也算通过；判定结果要带回实际分数与所用阈值，案件状态同步落到通过或拒绝。
7. 供应商标记需要人工时案件转待人工；人工复核空复核人返回 ErrBlankReviewer，非待人工态复核返回 ErrNotPending，复核通过进通过态、驳回进拒绝态。
8. Status 按用户读出最近一案的案件编号、等级与状态，未知用户返回 ErrUnknownUser。
9. 只有通过态的案件能因验证到期降级，其它状态调用 ExpireCase 返回 ErrNotApproved，过期后状态是过期态。
10. 供应商超时要按待定进入重试，不能直接判失败要求用户重传。
11. 同一证件摘要已经绑定其它账号时转人工并标记冲突，既不能自动通过也不能直接拒绝。
12. 验证过期只限制高保障操作，账号本身不冻结。

硬约束：重试次数受 MaxRetries 硬顶，超出返回 ErrRetryExhausted；同一案件的重试计数与状态变更必须原子，并发提交不能把计数打乱；进程重启后案件态、重试计数、证件摘要与序号全部延续，损坏快照返回 ErrBadSnapshot 且不改动现网数据。

验收：go test ./... 的 15 条用例全过，测试不得修改、跳过或按固定输入写死；并发提交与重启恢复自行验证，只做本地实现，交付源码与跑测记录。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-a-27`
- 实现文件：`identity_proofing_case.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`IdentityProofingCase`
