# batch49-go-safety-d-10

模块路径 `gsb/batch49-go-safety-d-10`，源码文件 `ambient_cap_lease.go`，包名 `task`，主类型 `AmbientCapLease`。

## 题面

临时提权窗口关不掉：一次异常退出之后，几个操作的能力一直挂在那里，读回视图里还能看到它们；另一次是收窄边界之后，仍有租约握着已经被移除的能力，越界权限就这么留在了进程里。值班同学翻审计只看到一堆没有原因的回收记录，分不清是谁归还的、还是超时被扫掉的。落点是 gsb/batch49-go-safety-d-10 的 ambient_cap_lease.go，类型为 AmbientCapLease。按操作申请一段限时的能力提升，操作结束即归还，异常路径也必须回收。

公开签名（不得改动）：
NewAmbientCapLease(cfg LeaseConfig) *AmbientCapLease
Acquire(op string, caps []Cap, ttl time.Duration) (Lease, error)
AcquireFor(holder, op string, caps []Cap, ttl time.Duration) (Lease, error)
Release(id string) error
Active() []Lease
Sweep(now time.Time) []string
Readback() []Cap
SetBounding(caps []Cap) error
SetPermitted(caps []Cap) error
Reap(holders []string) []string
Audit() []LeaseAudit
LeaseByID(id string) (Lease, bool)
Snapshot() ([]byte, error)
Restore(payload []byte) error

逐条要落地：
1) Acquire 的操作名为空报 ErrBadOp，能力为空报 ErrNoCaps，超过单租约能力上限报 ErrTooManyCaps，租约数超过 MaxLeases 报 ErrLeaseLimit。
2) ttl 必须大于零且不超过 MaxTTL，越界报 ErrBadTTL；MaxTTL、MaxLeases、MaxCapsPerLease 未配置时分别取一小时、十六、四。
3) 申请的能力必须同时落在边界集与许可集内，任一不满足整单拒绝：不在许可集报 ErrCapNotPermitted，不在边界集报 ErrCapOutOfBounds。
4) Readback 是全部存活租约能力的并集，按名字升序去重；单个租约归还不能提前撤销其它租约持有的能力。
5) Release 撤销租约并写一条原因为 released 的审计记录，重复归回报 ErrUnknownLease，租约标识不再能查到。
6) Sweep 只回收到期时刻不晚于 now 的租约，返回被回收的租约标识升序；同一时刻重复扫除不再产生新记录。
7) 到期与显式归还竞争同一租约时只留一条回收记录，另一条路径报 ErrUnknownLease。
8) SetBounding 传空集合报 ErrEmptyBounding，集合里有不在许可集的能力报 ErrCapNotPermitted；收窄之后的新申请按新边界判定。
9) 收窄边界集时若仍有租约持有被移除的能力，要先撤销相关租约再改边界集，顺序反了就会留下越界能力。
10) 进程异常退出后租约不会自行消失，需按持有者存活状态清理并同步读回能力，审计里要留下 holder_gone。
11) 同一操作重复申请同一组能力时按新租约计，不能合并成一条后让一次归还有效；一次归还只释放它自己那一份。
12) 同租约的快照要恢复存活租约与两个能力集合，恢复后 Readback、Active 与快照前一致；非法快照报 ErrBadSnapshot。

硬约束：多个协程会同时申请、归还与扫除，台账与审计记录都要原子推进，三十二路并发申请再归还后读回必须为空且审计条数与归还次数相等；进程重启后凭快照继续持有未到期租约，已回收的租约不得复活；两个能力集合都是硬边界，不合规的申请整单拒绝，不允许先批后撤。读回视图与台账必须来自同一份状态，不允许一边已经归还另一边还能读到能力。

验收：go test ./... 的 15 条用例全过，测试不许改、不许跳过、不许按固定输入写死；并发申请与重启恢复自行验证；只做本地实现，交付源码、跑测记录与租约状态说明。

## 运行方式

在模块根目录执行：

```bash
go test ./...
```

测试位于 `tests/test_ambient_cap_lease_test.go`，属同一个模块，用例通过 `impl "gsb/batch49-go-safety-d-10"` 引用实现。
评测口径为 15 条用例全部通过，测试文件不得修改、跳过或按固定输入写死。
