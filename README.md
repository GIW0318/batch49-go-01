# batch49-go-safety-a-15

- 模块路径：`gsb/batch49-go-safety-a-15`
- 包名：`task`
- 主文件：`admin_break_glass_ledger.go`
- 验收用例：`tests/test_admin_break_glass_ledger_test.go`

## 运行方式

在包根执行：

```bash
go test ./...
```

## 题面

审计抽查发现：一次 SSO 故障中，值班管理员启用破窗后在复核还没走完的情况下继续持有权限数小时，理由是“还在复核”；另一位管理员被驳回后，同一事件里其他人的破窗记录被合并统计，导致事件额度反复被占用；还有一次驳回之后，该管理员的历史记录在列表里被后来的操作覆盖，无法追溯。破窗治理逻辑位于模块 gsb/batch49-go-safety-a-15，主文件 admin_break_glass_ledger.go，入口类型 AdminBreakGlassLedger，请把紧急启用、使用、复核与冷却补齐到可交付状态。

破窗授权有启用、使用中、待复核、已复核、已驳回五种状态。启用时写入管理员、事件、原因、启用时刻与硬性到期时刻，并立即产生一条通知记录；使用只允许发生在未到期的启用或使用中状态；到期后进入待复核并立刻收权；复核必须在复核窗口内完成，复核人不能是启用人本人。驳回后该管理员进入冷却期，冷却期内不得再发起破窗；同一事件多人破窗按事件合并计数，不按人数重复占用额度。

需要落到的行为：
1. 启用校验空管理员、空事件、空原因，并写明到期时刻与剩余时长。
2. 单管理员对同一事件只能有一个未关闭的授权，重复启用要拒绝。
3. 单管理员可覆盖的不同事件数有上限，额度查询返回剩余可覆盖事件数，超限后拒绝启用。
4. 同一事件可参与的管理员人数有上限，超限后拒绝。
5. 使用校验授权存在与动作非空，启用和使用中状态累计使用记录。
6. 回收后、已驳回、已复核的授权都不再允许使用，错误要能区分。
7. 到期只接受启用或使用中状态，重复到期要拒绝。
8. 复核只接受待复核状态，未知授权、空复核人、自审分别报错。
9. 复核通过写入复核人、结论和时刻，之后不再允许使用，剩余时长归零。
10. 复核驳回写入结论并启动冷却，冷却截止时间可反查。
11. 事件审计按事件保留最近若干条，序号从 1 起严格递增，记录不可改写。
12. 快照恢复后剩余时长、待复核状态、冷却与额度计数延续。

关键接口：
- NewAdminBreakGlassLedger(limits Limits) *AdminBreakGlassLedger
- (l *AdminBreakGlassLedger) Trigger(adminID, incidentID, reason string) (Grant, error)
- (l *AdminBreakGlassLedger) Use(grantID, action string) error
- (l *AdminBreakGlassLedger) Review(grantID, reviewerID string, uphold bool) (Verdict, error)
- (l *AdminBreakGlassLedger) Expire(grantID string) error
- (l *AdminBreakGlassLedger) Quota(adminID string) (int, error)
- (l *AdminBreakGlassLedger) GrantState(grantID string) (Grant, error)
- (l *AdminBreakGlassLedger) Active(adminID string) []Grant
- (l *AdminBreakGlassLedger) Events(incidentID string) []Event
- (l *AdminBreakGlassLedger) CooldownUntil(adminID string) (time.Time, error)
- (l *AdminBreakGlassLedger) Snapshot() ([]byte, error)
- (l *AdminBreakGlassLedger) Restore(data []byte) error

硬约束：十六路并发对同一管理员同一事件发起启用只能有一个成功，其余必须返回重复启用错误，且只能留下一条通知记录；复核未完成不影响权限收回，不能以仍在复核为由继续持有；驳回后冷却只限制该管理员后续发起破窗，不触碰其它管理员，已有审计记录不可修改或删除；同一事件多人破窗合并计一次事件额度，不按人数重复占用；所有上限取自构造时的 Limits。验收口径是在包根执行 go test ./... 全部通过，不得修改或跳过用例，不得针对固定输入写死结果，并发与重启场景自行验证，只使用标准库并在本地完成。
