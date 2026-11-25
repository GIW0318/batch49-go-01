# 资源级数据共享同意台账

## 题面

客服在工单里贴出截图：客户上个月撤销了给某家理赔服务商的影像授权，对方今天仍旧能打开同一份保单影像；另一家服务商拿到的授权只写了理赔用途，却拿去批量导出营销名单，系统也照样放行。合规还要按对象导出授权链路：谁在什么时刻授给了谁、依据哪条用途、什么时候撤销，现在一条都答不上来。落点 gsb/batch49-go-safety-a-16 的 data_sharing_consent_ledger.go，类型 DataSharingConsentLedger，对外签名保持：NewDataSharingConsentLedger(limits Limits) *DataSharingConsentLedger、Grant(userID, recipientID, objectID, purpose string, ttl time.Duration) (Grant, error)、Revoke(userID, objectID, recipientID string) error、Check(recipientID, objectID, purpose string) (Decision, error)、ListByObject(userID, objectID string) []Grant、ListByRecipient(recipientID string) []Grant、SweepExpired(now time.Time) (int, error)、Snapshot() ([]byte, error)、Restore(data []byte) error；另有 GrantAt 与 CheckAt 两个带时刻的同义入口，验收时用来固定时钟。

要修的行为：用户、接收方、对象、用途任一项为空或只有空白字符必须拒绝，返回 ErrBlankArgument 且不落账，查询入口参数为空同样报 ErrBlankArgument；ttl 不为正数报 ErrBadTTL，同样不落账；同一用户对同一对象、同一接收方、同一用途重复授权按续期处理，条数不增，截止时间与序号刷新，台账里以最新一次为准；单用户授权条数超过 Limits.MaxGrantsPerUser 报 ErrGrantLimit 且不写入；同一对象上的接收方数量超过 Limits.MaxRecipientsPerObject 报 ErrRecipientLimit 且不写入；命中未撤销且未到期的授权时判定允许，理由为 allowed 并带回截止时间；没有任何匹配授权时判定为不允许、理由 not_granted，这属于正常判定而不是错误；用途与授权里的用途不一致时必须拒绝，理由为 purpose_mismatch；撤销只让指定的那个接收方失效，同一对象上其余接收方的授权不受影响，撤销要保留撤销时刻，撤销已撤销的记录按幂等成功处理，撤销不存在的记录报 ErrUnknownGrant；到期后读取当场拒绝，不必等到清扫任务跑过；SweepExpired 只清理已到期且未撤销的记录并返回清理条数，重复清扫返回零；ListByObject 只返回该用户该对象的记录，ListByRecipient 只返回该接收方名下的记录，两者都按序号升序，返回的是副本，调用方改动不得影响台账。

硬约束：授权判定、上限校验与写入在同一把锁内完成，同一对象上的并发授权只放行配置允许的接收方条数，超出的一律报错，不允许先写后删；读判定与清扫并发时，读方要么看到记录仍在、要么看到已被清走，不得看到半条记录；快照要把到期时间、撤销态与序号水位一并带走，恢复后同一授权仍按原截止时间判定，序号继续往上走；损坏快照报 ErrBadSnapshot，且不得改动现网台账。

验收：go test ./... 共 15 条全部通过，测试不得修改、跳过或按固定输入写死；并发授权与快照恢复自行验证；只做本地实现，交付源码与跑测记录。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-a-16`
- 实现文件：`data_sharing_consent_ledger.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`DataSharingConsentLedger`
