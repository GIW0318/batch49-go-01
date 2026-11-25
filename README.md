# batch49-go-fix-a-16

模块路径：`gsb/batch49-go-fix-a-16`

包名：`task`

源文件：`namespace_escape_auditor.go`

测试目录：`tests/`

## 题面

红队演练收队时，隔离域里多出三个没人认领的进程：一个把父进程挂到了域外，一个的挂载视图与域基线完全不同，还有一个从普通账号变成了 uid 0。采集器只上报 pid、父 pid、启动时间、六个命名空间句柄和能力集，判断全靠对账。安全组要求把这类逃逸从数据里稳定识别出来，并且能给每个结论定位到具体进程和命名空间。改动落在 gsb/batch49-go-fix-a-16 的 namespace_escape_auditor.go，类型 NamespaceEscapeAuditor。关键签名保持：NewNamespaceEscapeAuditor(limits Limits) *NamespaceEscapeAuditor、RegisterDomain(d Domain) error、SetBaseline(domainID string, ns NamespaceSet) error、Observe(row ProcessRow) error、Rows(domainID string) ([]ProcessRow, error)、Audit(domainID string) ([]EscapeFinding, error)、Findings(domainID string) ([]EscapeFinding, error)、Quarantine(domainID string, pid int) (QuarantineTicket, error)、Release(ticketID string) error、Snapshot() ([]byte, error)、Restore([]byte) error。

验收前必须落实的判定：
第一，域 ID 空白报 ErrBlankDomain，基线六个命名空间句柄任一为零报 ErrBadBaseline，重复域报 ErrDuplicateDomain，超上限报 ErrLimitExceeded。
第二，观察行的 pid 必须为正、启动时间不能为零、DomainID 必须与目标域一致，未知域报 ErrUnknownDomain，其余不合法报 ErrBadRow；同一 pid 同一启动时间重复上报必须幂等，不增加行数。
第三，同一 pid 换成新的启动时间视为新进程：当前行被替换，旧启动时间作为历史证据保留，审计时给出 pid_reuse 并同时带上新旧启动时间。
第四，pid 或 user 命名空间与基线不符报 critical 的 ns_mismatch，Namespace 指明是哪一个；net、uts、ipc 不符报 high；同时命中多类时按类各出一条。
第五，pid 与 user 都对得上、只有 mount 句柄不同时报 mount_drift，且不得再重复报 ns_mismatch；挂载视图被换掉通常意味着有人重新挂载了宿主目录，结论里要带上实际的 mount 句柄值。
第六，非 init 进程的父 pid 为零，或父 pid 既不是 init 也不在当前行集合里，报 high 的 orphan；init 自身父 pid 为零不算异常，父 pid 指向域外 init 同样按孤儿处理。
第七，普通 uid 的进程 EUID 变成 0 报 critical 的 privilege_shift；EUID 非零但仍带着能力集报 high 的 cap_retained。
第八，发现按严重度降序、pid 升序、类型升序排列，ID 必须稳定；同一进程同一类型不得重复出条目，重复 Audit 同一域的结果逐条一致，Findings 与 Count 反映最近一次审计。
第九，只有 critical 或 high 的当前发现才能开隔离票，找不到发现报 ErrNoFinding；同一进程重复开票报 ErrAlreadyQuarantined。
第十，释放票要求票存在，未知票报 ErrUnknownTicket；释放后同一进程的后续观察仍可再次产生发现，不能形成永久豁免。
第十一，SetBaseline 只更新基线；更新后已经存在的发现不会自动消失，必须重新 Audit 才按新基线判定。
第十二，Snapshot 覆盖域、基线、当前行、历史启动时间、发现与隔离票，Restore 整份替换；损坏快照报 ErrBadSnapshot 且不动现网。

硬约束：Observe 要能承受多采集器并发上报，同一 pid 的重复行只能落一份，行数与启动时间证据必须准确；Audit 与 Observe 并发时只看到审计前或审计后的完整行集合，不得读到一半。

验收以 go test ./... 的 15 条用例全绿为准，测试不许改、不许跳过、不许按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行

```bash
go test ./...
```
