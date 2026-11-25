# SP 元数据与签名证书更替

## 题面

证书更替那晚值班同事回滚到上一版元数据，结果旧证书早就过期，SP 侧签名校验全挂；另有一次运维把 ACS 地址填错，系统只丢了那个字段继续发布，登录回调被打到了外部域名。审计还要能追到每次发布与激活：哪份元数据在什么时候推上去、什么时候生效、什么时候被回滚。值班同事说，回滚按钮点下去之前没人能看出目标版本用到的证书是不是还活着，也没人能列出当前到底接受哪几张证书。落点 gsb/batch49-go-safety-a-19 的 saml_sp_metadata_rotator.go，类型 SamlSpMetadataRotator，签名保持：NewSamlSpMetadataRotator(opts Options) *SamlSpMetadataRotator、SetAllowlist(tenantID string, entityIDs, acsURLs []string) error、Allowlist(tenantID string) ([]string, []string)、Validate(md Metadata) error、Publish(tenantID string, md Metadata) (Version, error)、Activate(tenantID, version string) error、Rollback(tenantID, version string) error、Certs(tenantID string) []CertRef、Versions(tenantID string) []Version、Active(tenantID string) (Version, bool)、Snapshot() ([]byte, error)、Restore(data []byte) error。

要修的行为：SetAllowlist 对实体标识与 ACS 地址去重并按字典序返回，返回的两个切片都是副本，调用方改动不得影响内部白名单，租户为空报 ErrBlankArgument，两侧都空报 ErrBadAllowlist；Validate 对空实体标识报 ErrBlankEntityID，没有证书报 ErrNoCertificate，任一证书的到期时刻不晚于当前时刻报 ErrCertExpired，元数据里多出未使用的字段不予限制但缺证书仍然拒绝；Publish 校验通过后按 v1、v2 递增生成版本号，状态为 draft 并记录发布时刻与序号，版本号单调递增，Versions 按发布先后升序返回，单租户版本数超过 Options.MaxVersionsPerTenant 报 ErrVersionLimit 且不写入；实体标识与全部 ACS 地址必须都在租户白名单内，否则整份拒绝并报 ErrNotAllowed，ACS 地址不在白名单时整份元数据拒绝，不得只丢掉该字段继续发布；Activate 只能激活已发布的版本，未知版本报 ErrUnknownVersion，激活后排他，原生效版本转 retired；激活时生效证书并集超过 Options.MaxCertsPerTenant 报 ErrCertLimit，激活失败要保持原生效版本不变；新旧签名证书有重叠期，重叠期内 Certs 同时返回旧证书与新证书并按证书标识升序，重叠期结束后旧证书必须硬拒绝，不能因为断言仍带旧证书就放行；Rollback 只能回到曾经生效过的版本，草稿版本与未知版本都报 ErrUnknownVersion，回滚到旧版本时若其证书已过期要拒绝回滚，而不是临时续期；Activate 或 Rollback 成功后 Certs 立即可读，不必等下一次发布；Certs 只返回当前可接受的证书，同一标识只出现一次，Active 返回的版本只带自己的证书。

硬约束：发布、激活、回滚与证书并集计算在同一把锁内完成，激活与回滚并发时读方看到的要么是完整的旧生效版本，要么是完整的新生效版本；快照要带走版本表、激活指针、白名单与证书重叠截止时刻，恢复后重叠截止继续按原时刻判定，序号继续往上走；损坏快照报 ErrBadSnapshot，且不得改动现网版本与证书。

验收：go test ./... 共 15 条全部通过，测试不得修改、跳过或按固定输入写死；重叠期与快照恢复自行验证；只做本地实现，交付源码与跑测记录。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-a-19`
- 实现文件：`saml_sp_metadata_rotator.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`SamlSpMetadataRotator`
