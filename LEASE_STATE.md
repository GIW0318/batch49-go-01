# AmbientCapLease 租约状态说明

实现文件：`ambient_cap_lease.go`，包 `task`，类型 `AmbientCapLease`。
本文说明租约在台账中的状态、回收路径、并发原子性与快照恢复语义。

## 1. 状态模型

台账只在 `leases map[string]Lease` 中保存“存活租约”。租约一旦被回收，
就在同一个临界区内 `delete` 并追加一条审计记录，不存在第二个“已失效但
仍可读”的状态表。因此：

- `Active()`、`LeaseByID()`、`Readback()`、`Snapshot()` 全部来自这同一份 map；
- `Readback()` 是 map 中全部存活租约 `Caps` 的并集，按能力名字典序升序去重；
- 回收提交后，所有读路径立刻看不到该租约及其独占能力。

`Lease.State` 在存活期间恒为 `active`（常量 `LeaseStateActive`）。

## 2. 状态转换与审计原因

所有回收都经过同一个内部收敛点 `reclaimLocked`：从 map 删除并恰好写一条
`LeaseAudit`。因此任何租约最多产生一条回收记录。

| 触发路径 | 判定 | 审计 `Reason` | 返回 |
| --- | --- | --- | --- |
| `Acquire` / `AcquireFor` | 校验全部通过 | 无审计（仅建租约） | 新租约，ID 形如 `lease-<单调序号>` |
| `Release(id)` | id 仍在 map 中 | `released` | `nil`；重复归还返回 `ErrUnknownLease` |
| `Sweep(now)` | `!Expires.After(now)`，即到期时刻 `<= now` | `expired` | 本次回收 ID 的升序序列 |
| `Reap(holders)` | 租约 `Holder` 不在“存活持有者名单”中 | `holder_gone` | 本次回收 ID 的升序序列 |
| `SetBounding(caps)` 收窄 | 租约持有新边界之外的能力 | `bounding_shrink` | 集合校验通过后先回收、再替换边界集 |

`Reap(holders []string)` 中的 `holders` 是调用方探测到的“仍存活持有者”
名单（alive roster）：凡 `Holder` 不在名单内的租约一律回收；传 `nil`/空
名单表示没有任何存活持有者，回收全部租约。重复 `Reap` 不会产生重复审计。
`Acquire`（不带 holder）使用内部默认持有者 `"default"`；需要模拟具体持有
者时使用 `AcquireFor(holder, ...)`。

到期与显式归还竞争同一租约时，`Release`、`Sweep`（以及 `Reap`、边界收窄）
都在同一把互斥锁内完成“查找 + 删除 + 写审计”。先拿到锁的路径赢得该租约并
写唯一一条审计，后到的路径在 map 中查不到它：`Release` 返回
`ErrUnknownLease`，`Sweep`/`Reap` 的返回列表与审计中也不会再出现该 ID。

## 3. 申请校验规则

默认值（配置项为零值/未配置时）：

- `MaxTTL = 1h`（`DefaultMaxTTL`）
- `MaxLeases = 16`（`DefaultMaxLeases`）
- `MaxCapsPerLease = 4`（`DefaultMaxCapsPerLease`）

`AcquireFor` 的校验顺序（任一失败都不改动台账，整单拒绝）：

1. `op == ""` → `ErrBadOp`
2. `len(caps) == 0` → `ErrNoCaps`
3. `len(caps) > MaxCapsPerLease` → `ErrTooManyCaps`
4. `ttl <= 0 || ttl > MaxTTL` → `ErrBadTTL`（TTL 恰好等于 `MaxTTL` 允许）
5. 逐个能力先查许可集：不在 `Permitted` → `ErrCapNotPermitted`
6. 再查边界集：不在 `Bounding` → `ErrCapOutOfBounds`
7. `len(leases) >= MaxLeases` → `ErrLeaseLimit`

先查许可集再查边界集，因此同时落在两个集合之外的能力（如测试中的
`fs.read`）报 `ErrCapNotPermitted`。

同一 op、同一组 caps 的重复申请各自分配独立 ID、独立租约，不做合并；
一次 `Release` 只撤销该 ID 自己那一份，其它租约及其能力并集不受影响。

## 4. 两个能力集合（硬边界）

- `Permitted`：许可集。`SetPermitted(caps)` 整体替换；空集合合法，含义是
  后续任何能力申请都以 `ErrCapNotPermitted` 被拒。
- `Bounding`：边界集。`SetBounding(caps)`：
  - 空/nil → `ErrEmptyBounding`；
  - 含当前许可集之外的能力 → `ErrCapNotPermitted`，集合与台账均不变；
  - 合法时在同一临界区内先回收所有持有“被移除能力”的存活租约
    （`bounding_shrink` 审计），再替换边界集。外部观察者不可能看到“边界已
    收窄但旧租约仍握着越界能力”的中间态。

两个集合都只约束新申请（以及边界收窄时的存量租约）；任何不合规申请都是
先完整校验、后一次性入账，不存在“先批后撤”。

## 5. 并发与原子性

`AmbientCapLease` 内只有一把 `sync.Mutex`，所有公开方法都在锁内推进
map、集合、ID 计数器与审计切片。返回的 `Lease`/`LeaseAudit`/`Caps` 切片
均为防御性拷贝，调用方修改返回值不会污染台账，也不会与内部状态产生数据
竞争。关键不变量：

- `Readback() == ⋃ Active()[*].Caps`（同一把锁、同一份 map 派生）；
- 每条存活租约的能力都 ⊆ 当前 `Bounding`；
- `len(Audit()) == 成功入账租约数 - len(leases)`，且每个租约 ID 在审计中
  至多出现一次；
- 租约 ID 由锁内单调计数器生成，快照会持久化计数器并按已恢复 ID 序号
  兜底抬高，恢复后新 ID 不与旧 ID 碰撞。

## 6. 快照与进程重启

`Snapshot()` 在锁内生成 JSON（内部 envelope，`version=1`），内容包括：

- 全部存活租约（含 `ID/Op/Holder/Caps/Expires/State`）；
- `Bounding` 与 `Permitted` 两个集合；
- 审计记录与 ID 计数器（保证审计连续性与 ID 不回退）。

时间字段以 `UnixNano` 持久化，恢复为 `time.Unix(0, n)` 的规范形式，因此
恢复后的 `Active()`/`Audit()` 与快照前可 `reflect.DeepEqual` 一致，到期
时刻也精确保持（绝对时间，跨进程/重启仍可被 `Sweep(now)` 正确判定）。

`Restore(payload)` 先把 payload 完整解码并校验（版本、两个集合存在、租约
ID 非空/不重复、能力非空等），全部合法后才加锁整体替换当前状态；非法
payload（nil/空白、非 JSON、`{}`、版本不符、重复 ID 等）一律返回包装了
`ErrBadSnapshot` 的错误（`errors.Is` 成立），且不会破坏调用前的台账。

已回收（released/expired/holder_gone/bounding_shrink）的租约从不写入快
照的存活列表，因此重启后不会复活。重启后对恢复出的台账继续 `Sweep` 即可
清掉停机期间到期的租约；未到期租约继续持有，`Readback`/`Active` 与快照前
一致。

## 7. 验证记录与复现

- 跑测记录：`artifacts/test-run.log`
  - `gofmt -l` 无输出、`go vet ./...` 通过；
  - `go test -v -count=1 ./...`：给定 15 条用例全部 PASS；
  - `go test -race -count=1 ./...`：无数据竞争；
  - `go test -count=20 ./tests/`：重复运行稳定。
- 额外需求自检（Reap、边界收窄回收、重复申请、Release/Sweep 三千次单租约
  竞争、精确到期点、快照 DeepEqual/非法快照、32 协程 chaos 不变量）：
  `artifacts/selfcheck-run.log`，对应临时代码存档
  `artifacts/selfcheck_test.go.txt`（验证后已从模块移除，不改变
  `go test ./...` 的 15 条用例口径）。
- 真·跨进程重启与 32×200 申请/归还压测：记录同在
  `artifacts/selfcheck-run.log`，程序存档
  `artifacts/restart_selfcheck_main.go.txt`。

本地复现：

```bash
go test -v ./...
go test -race ./...
```
