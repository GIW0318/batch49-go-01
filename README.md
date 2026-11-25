# 租户登录域切换后的 Passkey RP ID 迁移

## 题面

登录域切换当天，值班台上连着三条投诉：老账号在新域名登录被踢回，重新在新域登记的钥匙进不去旧后台，回滚之后又有一批人彻底登不上。客服把三起工单并成一条主线，结论是凭据的 RP ID 与新老域名对不上，登录链路把两套凭据混着使。租户要把登录域从 login.old.example 迁到 login.new.example，存量 Passkey 凭据得按新的 RP ID 重新登记，并存期结束前还要能随时收手。改动落在 gsb/batch49-go-safety-a-01 的 rp_id_migration_campaign.go，类型 RpIdMigrationCampaign。关键签名保持：NewRpIdMigrationCampaign(limits Limits) *RpIdMigrationCampaign、SetNow(now time.Time)、Plan(tenantID, fromDomain, toDomain string) (Campaign, error)、Start(campaignID string) error、ImportLegacy(campaignID, userID string, creds []Credential) error、Record(campaignID, userID string, cred Credential) (UserProgress, error)、Status(campaignID, userID string) (UserProgress, error)、SetDeadline(campaignID string, at time.Time) error、LoginPolicy(campaignID, userID, domain, rpID string) (LoginVerdict, error)、Rollback(campaignID string) error、Sweep(now time.Time) (int, error)、Snapshot() ([]byte, error)、Restore([]byte) error。

要修的点：一、租户号或任一域为空、新旧域相同都要在 Plan 门口挡住，分别报 ErrBlankTenant 与 ErrDomainUnchanged，不得建出半成品批次，也不得占用活动名额。二、批次初始为 planned，ID 非空，StartedAt 取当前时钟，Campaign 能按 ID 原样取回，取不到返回 false；同一租户的两个批次 ID 不得相同。三、Start 时单租户同时在跑的批次受 MaxActiveCampaignsPerTenant 限制，超出报 ErrActiveCampaignLimit，被拒的批次留在 planned，名额释放后才能再启动。四、只有 planned 能 Start，重复 Start 报 ErrCampaignNotActive；未 Start 就 Record 同样报这个错，Start 不存在的批次报 ErrCampaignNotFound。五、Record 只收 RP ID 等于新域的凭据，旧域凭据报 ErrCredentialDomain 且不入清单；并存期内旧域旧凭据还能登录，但新域绝不能把旧 RP ID 当有效凭据。六、同一凭据 ID 重复登记报 ErrDuplicateCredential，已有清单不得被追加或改写，序号也不得因重复请求跳号。七、单用户新凭据数受 MaxCredentialsPerUser 限制，超出报 ErrCredentialLimit，已存凭据保持原样。八、ImportLegacy 与 Record 遇到空用户号都报 ErrBlankUser；ImportLegacy 只收 RP ID 等于旧域的凭据，按 ID 去重，超过同一上限报 ErrLegacyLimit。九、Status 要给出已迁移凭据、剩余旧凭据与批次截止时间，剩余数按旧凭据数减已迁移数算、最低为零，迁完为 migrated，未迁完为 partial，序号单调递增。十、SetDeadline 早于 StartedAt 报 ErrDeadlineInvalid，超过 MaxOverlapHours 并存窗的报 ErrDeadlineTooLong，批次不存在报 ErrCampaignNotFound，未显式设置时按上限兜底，已关闭的批次不接受改期。十一、Sweep 把过了截止的 active 批次转 closed 并返回条数，之后 Record 报 ErrCampaignNotActive；回滚把批次置为 rolled_back，只能把新域凭据标成待重新登记并保留原认证器信息，不能直接当旧域凭据用。十二、截止后旧域登录要走找回流程，不能把未迁移用户整体锁死，被挡进找回流程的用户仍可重新提交凭据；损坏快照 Restore 报 ErrBadSnapshot 且不动现网。

硬约束：Record 与同一用户的并发登记必须串行，凭据上限只放行配置条数，同一批次并发 Start 只能成功一次；Snapshot 要覆盖批次、每用户进度、剩余旧凭据与截止时间，Restore 整份替换，恢复后序号继续递增，重复导入不改写已有凭据，最新进度以快照为准。验收以 go test ./... 的 15 条用例全绿为准，测试不许改、不许跳过、不许按固定输入写死；并发与重启恢复自行验证；只做本地实现，交付源码、跑测记录和一页以内说明。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-a-01`
- 实现文件：`rp_id_migration_campaign.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`RpIdMigrationCampaign`
