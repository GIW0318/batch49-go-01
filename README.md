# API key 作用域收窄重算

## 题面

权限回收工单处理完一小时，审计仍能抓到用旧 key 读到已交回的资料；运营又发现影响面报告里写着会失去三个作用域，真正执行后只收掉了一个，剩下两个仍在放行。客服复盘时还发现，同一个所有者的 key 分属三个服务，报告只按 key 列表格，值班同学根本看不出哪个服务会断。范围落在 gsb/batch49-go-safety-a-25 的 api_key_scope_reconciler.go，类型 ApiKeyScopeReconciler，负责所有者权限或资源归属变更后对存量 API key 作用域的收窄重算。公开入口保持：NewApiKeyScopeReconciler(limits ScopeLimits) *ApiKeyScopeReconciler、RegisterKey(ownerID, keyID string, scopes []string) error、SetOwnerPermissions(ownerID string, perms []string) error、Impact(ownerID string) (Report, error)、Reconcile(keyID string) (Diff, error)、Apply(diffID string) (Result, error)、Authorize(keyID, resource, action string) (Decision, error)、Priority(keyID string, level int) error、Snapshot() ([]byte, error)、Restore(data []byte) error、KeyScopes(keyID string) ([]string, bool)、Version(keyID string) (uint64, bool)。

要把下面这些行为做实：1) 作用域统一写成资源冒号动作，缺冒号或任一段为空报 ErrBadScope；2) 空 key 标识报 ErrBlankKey，空所有者报 ErrBlankOwner；3) 新登记的 key 版本为一，作用域去重后按字典序保存，KeyScopes 返回副本，调用方改切片不影响内部状态，登记时所有者尚未授权报 ErrUnknownOwner；4) 单所有者 key 数超限报 ErrKeyLimit，单 key 作用域条目超限报 ErrScopeLimit；5) 请求时按 key 的 scope 与所有者当前权限求交，两者都有的资源动作才放行，放行原因码 scope_ok，缺一个返回拒绝并给出原因码 scope_missing；6) 所有者权限被收回后，key 的有效作用域必须随之收窄，不能沿用签发时固化的集合；7) 影响面报告逐 key 列出会失去与仍保留的作用域，但不改动 key、不推进版本，报告只读且可反复生成，同一状态下多次生成内容一致；8) Reconcile 生成的差异带标识、前值、后值与损失列表，Apply 之前不生效；9) Apply 成功后版本加一，作用域换成差异里的后值，未知差异报 ErrUnknownDiff，版本已变化的差异报 ErrDiffStale；10) 收窄后已在途的请求若判定基于旧版本，必须在落地前按新版本复核；11) 影响面报告与真正应用之间所有者权限再次变化时，以应用时刻的最新版本为准，不照搬报告；12) 优先级取零到五，越界报 ErrBadPriority，允许的判定带上当前优先级。

硬约束：重算与请求并发时按版本号取较新结果，读到的版本与作用域始终来自同一份快照，不能一半新一半旧；同一 key 的并发 Apply 只能有一个成功，后到者按版本冲突拒绝；所有者的 key 表、作用域集合与版本号能整份快照并恢复，重启后版本延续；损坏快照报 ErrBadSnapshot 且不动现有 key；单所有者 key 数是硬上限，超限拒绝而不是淘汰旧 key。

验收：go test ./... 15 条用例全部通过，测试不得修改、跳过或按固定输入写死；并发重算与重启恢复自行验证；只做本地实现，交付源码、跑测记录与一页版本与求交口径说明。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-a-25`
- 实现文件：`api_key_scope_reconciler.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`ApiKeyScopeReconciler`、`Report`、`Diff`、`Result`、`Decision`、`ScopeLimits`
