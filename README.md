# SAML 属性映射与 NameID 迁移

## 题面

上周把 IdP 的 NameID 格式从持久标识切到邮箱，后台同一批员工冒出两个账号：一个挂在旧的持久标识上，一个用邮箱新建，权限对不上；还有几个员工登录后属性是空的，因为断言里少了一个必需属性，系统没拒绝反而先把账号建了出来。客服只能凭邮箱手工合并账号，审计日志里两个人的操作还混在一条线上。落点 gsb/batch49-go-safety-a-18 的 saml_federation_mapper.go，类型 SamlFederationMapper，签名保持：NewSamlFederationMapper(limits Limits) *SamlFederationMapper、Configure(tenantID, idpID string, rules []AttrRule) error、Attributes(tenantID, idpID string) ([]AttrRule, error)、Bind(tenantID, idpID, nameID, userID string) error、Map(tenantID, idpID string, a Assertion) (Identity, error)、Migrate(tenantID, idpID string, from, to NameIDFormat) (MigrationPlan, error)、PickValue(rule AttrRule, values []string) (string, error)、NormalizeNameID(format NameIDFormat, raw string) (string, error)、Snapshot() ([]byte, error)、Restore(data []byte) error；另有 Unbind、Bindings、Migration、IdPCount 与 MigrateAt 辅助入口。

要修的行为：Configure 按配置顺序保存规则，Attributes 原样返回该顺序，不得按名字重排，并返回副本，调用方改动不得影响已配置规则，规则名重复报 ErrDuplicateRule 且整份不生效，单 IdP 规则数超过 Limits.MaxRulesPerIdP 报 ErrRuleLimit，单租户 IdP 数超过 Limits.MaxIdPsPerTenant 报 ErrIdPLimit，租户或 IdP 为空报 ErrBlankArgument，多值策略只认 first、join、truncate，其它取值报 ErrBadRule，未配置的 IdP 查规则报 ErrUnknownIdP；PickValue 取首值，join 与 truncate 先按 MaxValues 截取再按 Separator 拼接，MaxLen 大于零时按字节截断，取值列表为空报 ErrMissingAttr；NormalizeNameID 对邮箱格式去空白转小写，并要求恰好一个 @、本地部分与域名非空，否则报 ErrBadNameID，持久标识只去首尾空白，瞬态标识不允许绑定、报 ErrBadNameID，未知格式报 ErrBadFormat；Bind 空参数报 ErrBlankArgument、未配置的 IdP 报 ErrUnknownIdP，同一标识绑定同一账号幂等成功且不重复写关联，同一标识指向另一账号报 ErrBindingConflict，单 IdP 绑定数超过 Limits.MaxBindingsPerIdP 报 ErrBindingLimit，Bindings 按绑定先后升序返回，解绑不存在的标识报 ErrBindingNotFound，解绑后空出的标识可以重新绑定，未配置的 IdP 返回空列表而不是报错；Map 先按绑定关系找账号，没有绑定且允许即时建号时按属性建号，同名标识重复登录不得建出第二个账号；同一断言重复调用要返回同一账号与同一组属性，序号单调递增；NameID 从持久标识切到邮箱格式时按既有绑定关联，不得凭邮箱新建第二个账号；必需属性缺失时必须拒绝登录且不创建账号，不能先把账号建出来再等下次补齐；迁移期内两种 NameID 登录都落到同一账号，且不重复写关联记录；Migrate 的源格式与目标格式相同报 ErrSameFormat，任一侧不是受支持格式报 ErrBadFormat，计划要带重叠截止时刻与当前绑定数，同一 IdP 已有未结束迁移时再起一个不同目标报 ErrMigrationActive。

硬约束：绑定表按 IdP 串行写入，并发绑定同一标识只能留一条关联，其余按幂等返回而不是各写一份；快照要带走属性规则、绑定关系、迁移状态与序号水位，恢复后重叠截止时刻与绑定关系延续，序号继续往上走；损坏快照报 ErrBadSnapshot，且不得改动现网数据。

验收：go test ./... 共 15 条全部通过，测试不得修改、跳过或按固定输入写死；并发绑定与快照恢复自行验证；只做本地实现，交付源码与跑测记录。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-a-18`
- 实现文件：`saml_federation_mapper.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`SamlFederationMapper`
