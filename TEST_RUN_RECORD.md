# 测试记录

- 时间：2026-11-26 03:48 CST
- 环境：Go `go1.23.4 linux/amd64`
- 实现文件：`damage_link_guard.go`

## 验收用例

命令：

```bash
go test -v ./...
```

结果：提供的 15 条用例全部 PASS，退出码 0。

- `TestLinkKeepsMemberOrderAndPercent`
- `TestLinkRejectsBlankAndDuplicateMembers`
- `TestLinkEnforcesGroupAndMemberLimits`
- `TestSettleSplitsEvenlyAcrossMembers`
- `TestSettleDistributesRemainderInJoinOrder`
- `TestSettleHonorsSharePercentAndDirectPart`
- `TestSettleRejectsBadInput`
- `TestSettleOutputOrderedByJoinOrder`
- `TestUnlinkRemovesMember`
- `TestReflectCapsPerTick`
- `TestTickResetClearsCap`
- `TestReflectRejectsBadAmountAndBlankIDs`
- `TestReflectDoesNotRouteThroughLink`
- `TestSnapshotRestoreRoundTrip`
- `TestRestoreRejectsBadSnapshot`

最终简要输出：

```text
?       gsb/batch49-go-game-a-11        [no test files]
ok      gsb/batch49-go-game-a-11/tests 0.003s
```

## 额外并发与恢复自检

自检使用临时包内测试完成，结束后已删除，未修改或新增 `tests/` 下的验收用例。

覆盖场景：

- 残血成员在同一次 `Settle` 中扣到零后，其未吸收额度按加入顺序在存活成员间重新均分。
- 已离组成员不参与均份和整数余数补发。
- 100 组随机比例、随机血量和随机伤害下，每条结算总额恒等于原始伤害；快照恢复后的下一次结算与原状态深度一致。
- 同组 200 个 `Settle` 与 7 个成员的并发 `Unlink` 交错，`-race` 下无数据竞争，成功结算均满足总额守恒和加入顺序。
- 两个来源各 25 个并发 `Reflect` 同时申请每 tick 100 上限，最终每个来源恰好累计 100，同一目标的 `ReflectedTotal` 为 200。
- 非法快照恢复后原状态字节级不变。
- 最后一名成员离组后的空组仍可区分“重复移除”和“未知组”，并可通过快照往返。

执行命令与结果：

```bash
go test -race -run 'TestCustom(SettleUnlinkInterleaving|TwoSourceReflectRace)' -count=20 .
# ok  gsb/batch49-go-game-a-11  1.040s

go test -race -count=1 ./...
# ?   gsb/batch49-go-game-a-11         [no test files]
# ok  gsb/batch49-go-game-a-11/tests   1.011s

go test -count=20 ./tests
# ok  gsb/batch49-go-game-a-11/tests  0.008s
```
