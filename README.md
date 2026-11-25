# 条款升级后的重新同意活动

## 题面

条款版本升到新一版已经两周，运营发现完成率仍按全量用户算，豁免名单里的人被反复催办；宽限期还没结束，没点同意的用户就被挡在登录页外面，一线把投诉转到了合规。合规还要能解释每个租户、每个条款版本的完成率曲线，以及某个人为什么被限制、限制的是哪一类功能。要在 gsb/batch49-go-safety-a-17 的 consent_renewal_campaign.go 把这次重新同意活动做成可复盘、可暂停的流程，类型 ConsentRenewalCampaign，签名保持：NewConsentRenewalCampaign(opts Options) *ConsentRenewalCampaign、Plan(tenantID, fromVersion, toVersion string) (Campaign, error)、Enroll(campaignID string, userIDs []string) (int, error)、Accept(campaignID, userID, version string) (Receipt, error)、Exempt(campaignID, userID, reason string) error、Progress(campaignID string) (Stats, error)、Pause(campaignID string) error、Resume(campaignID string) error、FeatureAllowed(campaignID, userID, feature string, now time.Time) (bool, string)、Snapshot() ([]byte, error)、Restore(data []byte) error；另有 PlanAt、AcceptAt 两个带时刻的同义入口与 Lookup 查询活动。

要修的行为：租户或任一版本为空报 ErrBlankArgument，源版本与目标版本相同报 ErrBadVersion；新建活动状态为 active，宽限截止等于建单时刻加 Options.GracePeriod，同一租户同一目标版本已有进行中活动时报 ErrDuplicateCampaign；Enroll 收到空名单报 ErrEmptyBatch，批内重复先去重，返回值是真正新增的人数，去重后超过 Options.MaxBatch 报 ErrBatchLimit 且整批不写入，累计人数超过 Options.MaxAudience 报 ErrAudienceLimit 且整批不写入；暂停中的活动不接受报名与同意，报 ErrPaused，Pause 与 Resume 各自幂等，暂停不改变宽限截止时刻，恢复后仍按原时刻判定；Accept 对未报名用户报 ErrNotEnrolled，提交版本与活动目标版本不同报 ErrVersionMismatch 且不产生回执，成功时回执带用户、版本、接受时刻与递增序号，同一用户同一活动只留一条回执；重复接受同一版本按幂等处理，不新增回执，也不得改写原回执的接受时间；宽限期内未同意的用户只失去依赖该条款的功能，不得直接拒绝登录，宽限期满后仍未同意的才限制登录后的敏感操作；豁免用户不计入完成率分母，也不因未同意被功能限制，豁免必须由名单内的用户提出并附非空理由，空理由报 ErrBadReason，重复豁免同一理由按幂等成功；Progress 返回已报名、已同意、已豁免与完成率（万分比、向下取整），已报名数包含被豁免的用户，已同意数不超过已报名数，名单为空时完成率为零且不得除零；FeatureAllowed 对已同意的用户一律放行，对豁免用户在宽限期内与宽限期满后都放行。

硬约束：报名、豁免、同意与各自的上限校验在同一把锁内完成，并发报名只放行配置允许的人数，其余报错，不允许先写后删；快照要带走各活动的名单规模、已同意数、已豁免数、状态与宽限截止时刻，恢复后进度与截止时间延续，序号继续往上走；损坏快照报 ErrBadSnapshot，且不得改动现网活动。

验收：go test ./... 共 15 条全部通过，测试不得修改、跳过或按固定输入写死；并发报名与快照恢复自行验证；只做本地实现，交付源码与跑测记录。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-a-17`
- 实现文件：`consent_renewal_campaign.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`ConsentRenewalCampaign`
