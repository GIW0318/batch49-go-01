# 账号找回案件 — 交付说明

实现文件：`account_recovery_casefile.go`（包 `task`，签名与常量保持不变）。
验收：`go test ./...` → 15/15 PASS；`go test -race ./...` 干净；并发 Approve 用例 `-count=30` 压测无失败。记录见 `artifacts/test-run.log`，附加自检见 `artifacts/selfcheck-run.log`。

## 模型与并发

- `AccountRecoveryCasefile` 用一把 `sync.Mutex` 保护 `cases / grants / userCases / attempts / seq`；所有读写与快照替换都在锁内，`Approve` 的“检查待裁决→置 approved→签发 Grant”是同一临界区，因此同案并发只有一个赢家，其余得到 `ErrCaseState`。
- 案件 ID 为 `case-<单调 seq>`，`Seq` 全局递增并进入快照；Grant ID 为 `grant-<seq>`，同案至多一份。

## 规则落点（对应题面）

- Open 顺序校验：空白用户 `ErrBlankUser` → 渠道白名单 → 非空证据 → 证据 ID 非空白（先于过期判断）→ 过期 `ErrEvidenceExpired` → 单案上限 `ErrEvidenceLimit`，非法输入不占序号/次数。
- 开案证据全部 `Verified` 才进 `waiting`，`WaitUntil=当前时钟+WaitPeriod`；否则 `evidence_pending` 且 `WaitUntil` 为零。待证态 `Submit` 到一条已核验证据即转 `waiting` 并从该时刻起算等待期（兼容旧证据仍未核验的情形）；此后任何 `Submit` 均 `ErrCaseState`。Submit 同样校验 ID、过期与证据上限。
- `Advance(now)` 只扫 `waiting` 且未冻结、`now>=WaitUntil` 的案件，转 `review_pending`，ID 升序返回；状态离开 waiting 后重复推进不再放行。等待期内 `Approve` 为 `ErrCaseState` 且状态不变。
- Approve/Contest 的空白操作人报 `ErrBadActor`，未知案件 `ErrCaseNotFound`；Approve 仅对未冻结的 `review_pending` 签发，成功后 `approved`。
- Grant 绑定 Case/User，记录 `IssuedAt` 与绝对截止 `CoolingUntil=IssuedAt+CoolingPeriod`。冷却期内仅 `change_password`、`reenroll_factor` 放行；`disable_two_factor`、`change_primary_email` 及其余操作一律 `(false,nil)`，冷却结束后四类操作均放行；未知 Grant 返回 `ErrUnknownGrant`。
- Contest 记录异议并置内部冻结标记：案件保留在 waiting（六态之一），Advance 跳过、Approve 拒绝，转人工处理。
- 同一用户存在在途案件（pending/waiting/review_pending）时再次 Open，证据并入原案、返回原 ID/Seq，不新建、不增次数；并入后证据总数仍受硬上限约束。单账号在途案件因此恒 ≤1。
- 尝试次数等于该用户已创建案件数，取消/驳回均不回退；新建前 `attempts>=MaxAttemptsPerUser` 即 `ErrAttemptsExceeded`，按用户隔离；Submit 与重复 Open 不计数。
- Cancel：未知 `ErrCaseNotFound`；approved/rejected/cancelled 再取消或取消后 Approve 均 `ErrCaseState`。
- `Get` 返回 `Case` 值并复制 `Evidence` 切片，调用方改写不影响内部状态；`Grant` 同样返回值。

## 时钟、快照与重启

- `SetNow` 钉住内部时钟；未设置时取真实时间。`Advance` 使用入参 `now` 做扫描，不改变 `SetNow` 钉住的时钟。
- `Snapshot` 为 JSON，覆盖 seq、全部案件（状态/证据/WaitUntil/异议/授权关联）、用户案件索引、尝试次数与 Grants（含绝对 `CoolingUntil/IssuedAt`）。
- `Restore` 先把字节反序列化到全新 DTO，JSON 损坏或结构缺失直接 `ErrBadSnapshot`，此时不触碰现网；成功后在锁内整份替换。等待截止与冷却截止都是绝对时间，重启后剩余等待/冷却自然延续，seq 也延续。

## 自检补充

临时测试（已删除，未改动 `tests/` 任何文件）额外验证了：重复 Open 合并且不占尝试次数、异议冻结不可推进/批准、冷却期拒绝关 2FA/换邮箱、重启后 Grant 冷却不重置、Get 深拷贝、Seq 单调、多案 Advance 的 ID 升序、16 并发 Open 仍只合并为一单在途案件；均通过 `-race`。
