# 跑测记录 — batch49-go-safety-a-19

- 时间：2026-11-26 00:22 (Asia/Shanghai)
- 环境：`go version go1.23.4 linux/amd64`
- 模块：`gsb/batch49-go-safety-a-19`
- 测试文件 `tests/test_saml_sp_metadata_rotator_test.go` 未修改、未跳过任何用例。

## 验收命令

```bash
go test ./...
```

结果：

```text
?       gsb/batch49-go-safety-a-19      [no test files]
ok      gsb/batch49-go-safety-a-19/tests        0.002s
```

`go test -v -count=1 ./...` 共 15 条，全部 PASS：

```text
=== RUN   TestSetAllowlistNormalizesAndRejectsBadInput
--- PASS: TestSetAllowlistNormalizesAndRejectsBadInput (0.00s)
=== RUN   TestPublishCreatesDraftVersion
--- PASS: TestPublishCreatesDraftVersion (0.00s)
=== RUN   TestPublishRejectsBlankTenant
--- PASS: TestPublishRejectsBlankTenant (0.00s)
=== RUN   TestValidateRejectsBlankEntityAndEmptyCerts
--- PASS: TestValidateRejectsBlankEntityAndEmptyCerts (0.00s)
=== RUN   TestValidateRejectsExpiredCertificate
--- PASS: TestValidateRejectsExpiredCertificate (0.00s)
=== RUN   TestPublishEnforcesVersionLimit
--- PASS: TestPublishEnforcesVersionLimit (0.00s)
=== RUN   TestActivateMakesVersionActive
--- PASS: TestActivateMakesVersionActive (0.00s)
=== RUN   TestActivateUnknownVersion
--- PASS: TestActivateUnknownVersion (0.00s)
=== RUN   TestActivateRetiresPreviousVersion
--- PASS: TestActivateRetiresPreviousVersion (0.00s)
=== RUN   TestActivateEnforcesCertLimit
--- PASS: TestActivateEnforcesCertLimit (0.00s)
=== RUN   TestCertsUnionDuringOverlap
--- PASS: TestCertsUnionDuringOverlap (0.00s)
=== RUN   TestRollbackRestoresRetiredVersion
--- PASS: TestRollbackRestoresRetiredVersion (0.00s)
=== RUN   TestRollbackRejectsDraftVersion
--- PASS: TestRollbackRejectsDraftVersion (0.00s)
=== RUN   TestSnapshotRestoreKeepsActiveAndCerts
--- PASS: TestSnapshotRestoreKeepsActiveAndCerts (0.00s)
=== RUN   TestRestoreRejectsCorruptSnapshot
--- PASS: TestRestoreRejectsCorruptSnapshot (0.00s)
PASS
ok      gsb/batch49-go-safety-a-19/tests        0.004s
```

## 竞态检查

```bash
go test -race -count=1 ./...
```

```text
ok      gsb/batch49-go-safety-a-19      1.317s
ok      gsb/batch49-go-safety-a-19/tests        1.014s
```

## 自行补充验证（临时测试，验收后已删除，未改动交付的 15 条）

临时白盒用例（根包，9 条）在 `-race` 下全部通过，覆盖题目点名但可见用例未完全断言的行为：

```text
--- PASS: TestScratchNotAllowedRejectsWholeDocument        # 实体/任一 ACS 不在白名单 -> ErrNotAllowed，整份不写入、不占号
--- PASS: TestScratchReturnedSlicesAreCopies               # Allowlist/Versions/Active/Certs 返回深拷贝，调用方改写不影响内部
--- PASS: TestScratchOverlapHardCutover                    # 重叠截止前一秒仍双证；到达截止时刻旧证硬消失，之后不恢复
--- PASS: TestScratchRollbackExpiredRejected               # 回滚目标证书 NotAfter <= now -> ErrCertExpired，active/Certs 不变
--- PASS: TestScratchRollbackLongAfterOverlapStillWorks    # 重叠窗口结束不影响“曾经生效”资格；有效旧版可回滚并开启新重叠
--- PASS: TestScratchSnapshotSeqAndDeadlineContinue        # 恢复后 seq=2，再发布为 v3/Seq=3；按原绝对截止时刻在 base+40m 硬切换
--- PASS: TestScratchCorruptSnapshotsAtomic                # nil/空串/非 JSON/{}/悬空 active/重复版本 均 ErrBadSnapshot 且现网不变
--- PASS: TestScratchConcurrentSwitchAndRead               # 4 激活/回滚 + 4 读/快照 + 1 恢复 并发 300ms，-race 无报告，状态自洽
--- PASS: TestScratchGlobalSeqAcrossTenants                # 全局序号跨租户 1,2,3；版本号仍按租户 v1,v2
```

## 实现要点

- 全部租户状态在单把 `sync.Mutex` 内读写；发布、激活、回滚、证书并集计算与快照/恢复共用同一把锁，返回切片均为深拷贝，读方只能看到完整的旧版或新版。
- 每租户保存白名单集合、按发布顺序排列的版本表、active 指针、`everActive` 集合与带绝对截止时刻的旧证书重叠窗口。
- `Certs` 取当前 active 自有证书 ∪ 未到期重叠窗口证书，按 ID 去重并升序；证书自身 `NotAfter <= now` 也视为不可接受。
- 激活/回滚先做未知版本、回滚资格（必须曾生效）、证书存活、证书并集上限全部检查，再提交状态；任一失败保持原 active 与证书不变。
- 快照为带 format 标记的 JSON，带走版本表、active 指针、双白名单、everActive、重叠截止时刻与全局 seq；恢复先在锁外完整解码+引用校验，成功后才在锁内整体替换，损坏返回 `ErrBadSnapshot` 且不动现网。
