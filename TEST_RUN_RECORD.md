# 跑测记录 — gsb/batch49-go-safety-a-27

- 时间：2026-11-26 04:41 (Asia/Shanghai)
- 环境：Go go1.23.4 linux/amd64，模块 `gsb/batch49-go-safety-a-27`
- 源码：`identity_proofing_case.go`（仅本地实现，未改动 `tests/` 下任何测试）

## 验收：`go test ./... -count=1 -v`

结果：**15/15 PASS**，退出码 0。

```text
?   	gsb/batch49-go-safety-a-27	[no test files]
=== RUN   TestStartBeginsCollectedCase
--- PASS: TestStartBeginsCollectedCase (0.00s)
=== RUN   TestStartRejectsBlankUserAndBadLevel
--- PASS: TestStartRejectsBlankUserAndBadLevel (0.00s)
=== RUN   TestStartEnforcesCaseLimit
--- PASS: TestStartEnforcesCaseLimit (0.00s)
=== RUN   TestSubmitRejectsEmptyInputs
--- PASS: TestSubmitRejectsEmptyInputs (0.00s)
=== RUN   TestSubmitDocumentLocksFirstDocument
--- PASS: TestSubmitDocumentLocksFirstDocument (0.00s)
=== RUN   TestCaseMovesToComparingAfterBothInputs
--- PASS: TestCaseMovesToComparingAfterBothInputs (0.00s)
=== RUN   TestApplyVendorResultRequiresComparing
--- PASS: TestApplyVendorResultRequiresComparing (0.00s)
=== RUN   TestThresholdForLevels
--- PASS: TestThresholdForLevels (0.00s)
=== RUN   TestVendorOutcomeByThreshold
--- PASS: TestVendorOutcomeByThreshold (0.00s)
=== RUN   TestVendorBoundaryAccepted
--- PASS: TestVendorBoundaryAccepted (0.00s)
=== RUN   TestVendorRetryCapEnforced
--- PASS: TestVendorRetryCapEnforced (0.00s)
=== RUN   TestReviewRequiresManualState
--- PASS: TestReviewRequiresManualState (0.00s)
=== RUN   TestReviewApproveAndReject
--- PASS: TestReviewApproveAndReject (0.00s)
=== RUN   TestStatusAndExpire
--- PASS: TestStatusAndExpire (0.00s)
=== RUN   TestSnapshotRoundTrip
--- PASS: TestSnapshotRoundTrip (0.00s)
PASS
ok  	gsb/batch49-go-safety-a-27/tests	0.003s
```

## 静态检查与竞态

- `go build ./...`：通过
- `go vet ./...`：通过
- `gofmt -l .`：无输出（格式干净）
- `go test -race ./... -count=1`：

```text
?   	gsb/batch49-go-safety-a-27	[no test files]
ok  	gsb/batch49-go-safety-a-27/tests	1.011s
```

## 并发与重启自验证

按题目要求自行验证。曾在仓库根目录放置临时 `zz_selfverify_test.go`
（`package task`，可检查内部计数），跑完 `go test -race` 与 `-count=5`
重复压测后**已删除**，因此交付目录中 `go test ./...` 仍是验收的 15 条用例。
临时用例与结果：

```text
--- PASS: TestZZConcurrentRetriesAtomic      # 200 并发重试，MaxRetries=50：恰好 50 次成功且 Retries=1..50 各一次，其余 ErrRetryExhausted，终态计数=50、state=comparing
--- PASS: TestZZConcurrentTimeoutsAtomic     # 100 并发 timeout：与 retry 同语义，恰好 20 次 pending 成功，其余 ErrRetryExhausted
--- PASS: TestZZConcurrentStartCaseLimit     # 120 并发 Start 同一用户，MaxCasesPerUser=20：恰好 20 成功 / 100 ErrCaseLimit，序号水位=20，ID 无重复
--- PASS: TestZZConcurrentDocumentLock       # 100 并发提交两种摘要：首个摘要锁定，同摘要幂等、异摘要 ErrDocumentLocked，摘要不被覆盖
--- PASS: TestZZRestartRecoveryAndReplay     # Snapshot→新实例 LoadSnapshot：状态/Retries/摘要/序号/事件序号/证件绑定全部延续；恢复后第 3 次重试成功、第 4 次 ErrRetryExhausted；下一案件序号继续；回放含证件摘要、供应商分数阈值、复核人；二次快照恢复稳定
--- PASS: TestZZConflictRequiresManual       # A 账号通过后绑定摘要；B 账号同摘要被标记 Conflict/ConflictWith，高分结果不自动通过而转 manual，人工可复核通过
--- PASS: TestZZExpiryNeverFreezesAccount    # 高保障案件过期后 High 操作拒绝、Basic/Standard 仍允许；Status 正常返回 expired 而非报错冻结
--- PASS: TestZZSnapshotKeepsDataOnCorruption # 损坏/非法快照一律 ErrBadSnapshot，加载前后 Snapshot 字节一致，现网状态不变
```

`go test -race -run TestZZ -count=5 .` 连续 5 轮全部 `ok`，无 data race。

## 行为落地对照

1. 空用户 `ErrBlankUser`；非法等级 `ErrBadLevel`；新案 `collected`，全局自增 `Seq`，ID 为 `case-%06d`（不会与占位 `case-404` 相撞）；单用户案件数硬顶 `MaxCasesPerUser`，超出 `ErrCaseLimit`。
2. 摘要空白 `ErrBadDocument`；首份证件锁定，异摘要 `ErrDocumentLocked`，同摘要重复提交幂等且不覆盖。
3. 空/nil 帧 `ErrEmptyFrame`；证件与活体齐备才 `collected → comparing`（材料先后顺序均可），单份材料不跳态。
4. 未知案件 `ErrUnknownCase`；非 `comparing` 提交供应商结果 `ErrNotReady`；未知 `Kind` 返回 `ErrBadVendorResult`。
5. `ThresholdFor`：Basic 0.60 / Standard 0.75 / High 0.90，非法等级 `ErrBadLevel`。
6. `Score >= Threshold` 判通过（等值通过），否则拒绝；`Verdict` 带回实际 `Score` 与所用 `Threshold`，案件同步 `approved/rejected`。
7. `refer` 转 `manual`；空复核人 `ErrBlankReviewer`；非 `manual` 复核 `ErrNotPending`；通过/驳回分别落 `approved/rejected`，复核人写入审计事件。
8. `Status` 返回该用户 `Seq` 最大一案的编号/等级/状态与 `Approved`；无案件 `ErrUnknownUser`。
9. 仅 `approved` 可 `ExpireCase`，否则 `ErrNotApproved`（未知 ID `ErrUnknownCase`）；成功后状态 `expired`。
10. `timeout` 与 `retry` 同处理：`Outcome=pending`、`Pending=true`，案件留在 `comparing` 并占用一次重试额度，不直接判失败、不要求重传。
11. 摘要绑定仅在案件**通过**（供应商自动通过或人工复核通过）后建立到账号；新案件证件命中其它账号绑定时置 `Case.Conflict/ConflictWith`，本会自动通过的结果改转 `manual` 且 `Verdict.Conflict=true`，不能被静默自动通过；人工仍可复核裁决。
    - 兼容说明：现有 15 条用例在同一编排器内跨用户复用同一固定摘要（如 user-9/user-10、user-14/user-15、user-16/user-17），因此不能把“仅提交了相同摘要”直接当成冲突并强制改态，否则阈值拒绝与 `ErrNotPending` 用例会被破坏；以“已通过绑定 + 拦截自动通过”为冲突落地边界。
12. 过期只把案件置 `expired`，不引入任何账号冻结态；附加 `OperationAllowed(user, level)` 策略：过期后仅 `LevelHigh` 操作受限，Basic/Standard 仍放行，`Status` 照常可读。

## 硬约束与回放

- `MaxRetries` 硬顶：锁内先判 `Retries >= MaxRetries` 再自增，第 `MaxRetries+1` 次返回 `ErrRetryExhausted`，计数绝不超过上限；计数与状态变更在同一把 `sync.Mutex` 内原子提交。
- 所有导出方法同一把互斥锁串行化案件读改写；`-race` 压测通过。
- `Snapshot`（JSON，version=1）持久化案件态、重试计数、证件摘要/完整证件、`Seq` 水位、事件序号、冲突标记与绑定标记；`LoadSnapshot` 先反序列化并严格校验、在全新 map 中建好状态，最后才加锁整体替换，坏快照返回 `ErrBadSnapshot` 且不触碰现网数据。
- 回放：每个动作生成有序 `Event`（开案、证件种类/摘要、活体字节数与 SHA-256、供应商/种类/分数/阈值/结论与重试序号、复核人及通过/驳回、过期、冲突扣留），随快照持久化；`Replay(caseID)` 返回单案审计流副本，`Events()` 返回全局流。
