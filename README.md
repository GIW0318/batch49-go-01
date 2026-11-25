# batch49-go-safety-d-03

模块路径 `gsb/batch49-go-safety-d-03`，包名 `task`，实现文件 `domain_process_ledger.go`。

## 题面

上周隔离域回收时出了两件麻烦事：域已经销毁，账本里还挂着十二个成员，宿主机上这些 pid 早被别的服务复用，值守同学对着进程表逐个确认花了一夜；另有一批进程是在域销毁的同一秒被拉起来的，既没进账本也没人发现，第二天才从审计日志里翻出来。还有一次对照扫描表时同一个 pid 出现两条记录，被判成了同一个进程，已经退出的那个于是永远显示存活。要求把域成员登记、销毁核对和差量对账收口成一个账本，让值守同学能直接拿差异清单去和进程表对照，而不是再靠人工猜一遍：模块路径 gsb/batch49-go-safety-d-03，文件名 domain_process_ledger.go，包名 task，核心类型 ProcessDomainLedger，构造签名为 NewProcessDomainLedger(l LedgerLimits)。

对外接口如下，签名固定：
Create(domain string) error；
Join(domain string, p ProcessRef) error；
Leave(domain string, pid int) error；
Members(domain string) []ProcessRef；
Destroy(domain string) ([]ProcessRef, error)；
Reconcile(scan []ProcessRef) Reconciliation；
Snapshot() ([]byte, error)；
Restore(data []byte) error；
Domains() []string；
Stats() LedgerStats；
Generation() uint64。

需要落实的行为：

1. 进程身份由 pid 加启动标识共同决定；pid 非正或启动标识为空报 ErrBlankProcess，不得用 pid 单独认进程。
2. Create 的域名为空白报 ErrBlankDomain，重名报 ErrDomainExists，域总数超过 MaxDomains 报 ErrDomainLimit。
3. Join 时域不存在报 ErrUnknownDomain；引用自带的 Domain 非空且与目标域不同报 ErrCrossDomain；同一身份重复登记报 ErrAlreadyMember；成员数超过 MaxMembersPerDomain 报 ErrMemberLimit。
4. 并发登记同一个进程只能成功一次，其余请求返回 ErrAlreadyMember，不能出现两条相同身份。
5. Members 返回按 pid、启动标识升序的副本，未知域返回空且不报错。
6. Leave 按 pid 删除成员，找不到报 ErrUnknownProcess；同一 pid 下有多条不同启动标识的记录时报 ErrAmbiguousPID，不能随手删一条。
7. Destroy 必须先把域置为不可加入再收成员，返回按序成员清单并删除该域；销毁过程中到达的 Join 要被拒绝，不能漏掉刚加入的进程。
8. 容器里的启动标识可能被虚拟化成固定值，遇到无法区分的情况要退回次优标识，并在差异清单里标注该判定的可信度。
9. 扫描间隔内 pid 已经复用、启动标识精度又不足的两条记录，不得被判成同一个进程。
10. Reconcile 把扫描结果分成未登记、已退出未销账、重复登记三类，输出可重放的排序差异清单（按域、pid、启动标识升序），同一身份出现多次只报一条，且不得改动账本与 generation。
11. 快照与恢复后域、成员、generation 必须一致，坏快照报 ErrBadSnapshot；Stats 给出域数、成员数与 generation。

硬约束：并发登记必须原子，不能出现同身份双写；销毁是两阶段操作，先封门再清点；快照恢复后成员身份判定与上限计数都要恢复；域数、成员数、扫描条数都是硬上限，超限先拒绝再报错。

验收口径：go test ./... 15 条全部通过，不得修改测试、跳过或用固定输入写死；并发与重启场景自行验证；只做本地实现，不接外部系统。

## 运行

```
go test ./...
```

## 说明

- 仅使用 Go 标准库。
- 测试位于 `tests/test_domain_process_ledger_test.go`。
