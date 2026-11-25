# batch49-go-safety-d-15

- 模块路径：`gsb/batch49-go-safety-d-15`
- 包名：`task`
- 实现文件：`shared_ns_refcount.go`
- 测试目录：`tests/`

## 运行方式

在本目录执行：

```bash
go test ./...
```

## 题面

<!--PROMPT-START-->
容器平台把同一个网络命名空间共享给十几个工作负载后，退出流程开始出错：某个成员进程崩溃没走退出流程，引用计数一直挂在一，运维想销毁命名空间却被告知还有成员；另一次两个成员用不同的规格加入同一个命名空间，第二个没有报错，运行一段时间后才发现共享范围比预期大。更隐蔽的一次是销毁刚开始，一个新的工作负载恰好加入，销毁完成后计数为零但成员仍然挂在共享命名空间上。请在 gsb/batch49-go-safety-d-15 的 shared_ns_refcount.go 中实现 SharedNsRefcount，把加入规格校验、引用计数与最后退出后的销毁收紧。

需要实现的接口：NewSharedNsRefcount() *SharedNsRefcount；Create(ns string, kind NsKind) error；Join(ns string, member string, want NsSpec) error；Leave(ns string, member string) error；Refs(ns string) int；Destroy(ns string) error；Members(ns string) []string；Kind(ns string) (NsKind, error)；Spec(ns string) (NsSpec, error)；Namespaces() []string；Reconcile(alive []string) error；Snapshot() ([]byte, error) 与 Restore([]byte) error。

需要覆盖的故障与边界：一、命名空间名为空返回 ErrBadNamespace，未知类型返回 ErrBadKind，重复创建返回 ErrNamespaceExists；二、Join 要求命名空间已存在、成员名非空，否则分别返回 ErrUnknownNamespace 与 ErrBadMember；三、第一个成员加入时确定命名空间规格，后续成员提交的期望规格必须逐字段一致，不一致返回 *SpecConflictError（可用 ErrSpecConflict 匹配）并在 Fields 中指出是 mode、owner 还是 labels，差异字段按固定顺序给出，便于脚本判断；四、标签按集合比较，顺序不同不算差异；五、同一成员重复加入同一命名空间只算一次引用，重复加入但规格不同要报错而不是静默接受；六、Leave 只接受当前成员，非成员返回 ErrNotMember，引用计数不得降到负数；七、最后一个成员退出后命名空间才允许销毁，Destroy 在仍有成员时返回 ErrNamespaceBusy 并把成员留在表里；八、销毁成功后再次加入返回 ErrUnknownNamespace，同名命名空间可以重新创建；九、Members 与 Spec 返回副本并按名称排序，成员列表重复读取结果一致，调用方修改返回的切片不得影响内部状态；十、成员进程已消失但未走退出流程时，计数要靠 Reconcile 按存活名单对账修正，而不是永久悬挂；十一、销毁进行中新成员加入必须被拒绝，否则会出现计数为零但仍有成员；十二、快照保存命名空间、类型、规格与成员集合，坏快照返回 ErrBadSnapshot 且不得改动现有状态。

硬约束：同一命名空间的加入、退出与销毁必须串行化，并发加入不得出现重复计数或丢失成员；读取计数与成员列表不得看到半更新状态。重启恢复后计数与成员集合必须与快照一致。对账期间不得把仍在存活名单里的成员误删。

验收：go test ./... 15 条必须全过，不得修改测试、跳过或按固定输入写死；并发与重启场景自行验证；只做本地实现。
<!--PROMPT-END-->
