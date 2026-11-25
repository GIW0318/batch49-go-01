# RP ID 迁移批次 — 实现说明（一页）

源码：`rp_id_migration_campaign.go`（签名/常量/错误不变）。跑测记录：`artifacts/test-run.log`（15/15 PASS、`-race -count=3`、vet/fmt）；并发与恢复自检：`artifacts/verification-extra.log`。

## 设计

单把 `sync.Mutex` 串行化全部读写，同用户 `Record`、同批次并发 `Start`、租户 active 名额“计数+翻转”均原子，名额不超卖。批次表为 `map[id]*campaignRecord`，用户进度存 `migrated/legacy/seq/needs_reenroll`。时钟用 `SetNow` 固定，零值回退 `time.Now`。

## 十二条落点

1. `Plan`：租户或 from/to 空白（含纯空格）→ `ErrBlankTenant`；`from==to` → `ErrDomainUnchanged`；先校验后建号，无半成品、不占名额。
2. 新批次 `planned`，ID `rp-campaign-N`（计数随快照持久化，同租户唯一、恢复不撞号），`StartedAt=当前时钟`，默认截止 `StartedAt+MaxOverlapHours`；`Campaign(id)` 可取回，缺失 `false`。
3. `Start` 锁内统计同租户 active 数，达 `MaxActiveCampaignsPerTenant` → `ErrActiveCampaignLimit` 且留 `planned`；回滚/Sweep 释放后可启动。
4. 仅 `planned` 可 Start，重复 Start → `ErrCampaignNotActive`；未 active 的 `Record` 同错；批次不存在 → `ErrCampaignNotFound`。
5. `Record` 仅收 `RPID==ToDomain`，否则 `ErrCredentialDomain` 且不建用户不入清单；新域登录绝不接受旧 RP。
6. 同用户凭据 ID 重复 → `ErrDuplicateCredential`，不追加/不改写/`Seq` 不增。
7. `len(Migrated)>=MaxCredentialsPerUser` → `ErrCredentialLimit`，存量不动；并发恰好放行配置条数。
8. `ImportLegacy` 空用户 → `ErrBlankUser`；仅收 `RPID==FromDomain`（任一不符整批不写入）；按 ID 对存量和批内去重、旧凭据不被覆盖；去重后超 `MaxCredentialsPerUser` → `ErrLegacyLimit`，整批不生效。
9. `Status` 给 `Migrated/Legacy/Remaining=max(0,len(Legacy)-len(Migrated))/Deadline/Seq`；无新凭据 `pending`，清零且有新凭据 `migrated`，否则 `partial`，回滚标记者 `needs_reenroll`；`Seq` 仅成功 Record +1。
10. `SetDeadline`：不存在 → `ErrCampaignNotFound`；早于 `StartedAt` → `ErrDeadlineInvalid`；超出 `MaxOverlapHours` → `ErrDeadlineTooLong`（边界相等放行）；未显式设置走上限兜底；`closed/rolled_back` 改期 → `ErrCampaignNotActive`。
11. `Sweep(t)` 把 `active && t.After(Deadline)` 转 `closed` 并计数，其后 Record → `ErrCampaignNotActive`。`Rollback` 仅对 active，置 `rolled_back`，新凭据标 `needs_reenroll`，`Migrated`（含 `PublicKey` 等认证器信息）原样保留、绝不转入 `Legacy`；重复回滚报 `ErrCampaignNotActive`。
12. `LoginPolicy`：旧域+旧 RP 在并存期及回滚后放行 `old_rp/match`；截止后（已 closed，或 active 但时钟过 Deadline）给 `Allowed=false / PathRecover / ReasonCampaignClosed`，不锁死，Sweep 前仍可 Record 重新提交；旧域+新 RP、新域+旧 RP 均拒绝并给 `ReasonRPIDMismatch`。

## 快照与验证

`Snapshot` 为带 `version=1` 的 JSON 信封，覆盖批次字段、截止、每用户 `migrated/legacy/seq/needs_reenroll`、全局 `next_id`；剩余数由两份清单推导。`Restore` 先锁外解析并校验版本/状态/时间/重复 ID，失败返回 wrap 的 `ErrBadSnapshot` 且不碰现网，成功后加锁整份替换；恢复后序号与 ID 继续递增，重复导入仍幂等。

`go test ./...` → 15/15 PASS，未改未跳测试（`git diff -- tests/` 为空）。`-race -count=3` 干净；临时自检另证：64 并发 Start 仅 1 次成功、32 并发同 ID 仅 1 条且 Seq=1、8 用户×12 并发每用户恰 4 条、名额释放可启动、登录矩阵、回滚保留、快照续增、坏快照无损；自检已删除。
