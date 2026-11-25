# batch49-go-safety-d-01

模块路径 `gsb/batch49-go-safety-d-01`，包名 `task`，实现文件 `acl_grant_tx.go`。

## 题面

周三下午批量授权脚本在业务目录上跑到一半被上游超时打断，第二天审计发现该目录多了三条只写进去一半的条目，mask 还停在旧值，一个本该只读的组现在能写文件；回滚脚本又因为条目序号对不上没法执行，只能人工逐条比对。请把这批操作收口成一个事务化执行器：模块路径 gsb/batch49-go-safety-d-01，文件名 acl_grant_tx.go，包名 task，核心类型 AclGrantTx，用 NewAclGrantTx(Limits) 构造。

对外接口如下，签名固定：
AddPath(path string, owner uint32, entries []Entry) error；
Preview(req GrantRequest) (AclDiff, error)；
Apply(req GrantRequest) (ApplyResult, error)；
Revoke(path string, p Principal) (int, error)；
Effective(path string, uid, gid uint32, groups []uint32) (Perm, error)；
Entries(path string) ([]Entry, error)；
Revision(path string) (uint64, error)；
Snapshot() ([]byte, error)；
Restore(data []byte) error；
Audit() []AuditRecord；
Paths() []string。

需要落实的行为：

1. AddPath 注册受管路径，条目里必须恰好有一条 KindOwner（ID 等于 owner）和一条 KindOther，不允许直接传 KindMask；落盘顺序固定为 owner、mask、按 ID 升序的 user、按 ID 升序的 group、other，标了 Default 的条目排在同类之后。
2. mask 由全部 KindUser 与 KindGroup 条目的原始位按位或得到，任何一次成功改动后重算；Effective 对具名 user 与 group 返回条目位与 mask 相与的结果，owner 与 other 不受 mask 约束。
3. Preview 只做校验和差分，返回 Before、After、Added、Removed、前后 mask 与 BeforeRev/AfterRev，其中 AfterRev 恒为 BeforeRev+1；它不得改动条目、不得消耗票号。
4. Apply 在一个不可分割的事务里落盘：先按 kind 加 id 删除（目标不存在报 ErrUnknownEntry），再新增（删除后仍然冲突报 ErrDuplicateEntry），结果条目数超过 MaxEntriesPerPath 报 ErrEntryLimit，任何一步校验失败整批回退且 Revision 不变。
5. 授权者必须在目标路径上持有 PermChangeACL，否则报 ErrNotAllowed；每条新增条目的权限位必须是授权者自身有效权限的子集，否则报 ErrGrantTooBroad。
6. 授权者可能通过所属组间接持有权限，越权判定要按展开后的有效权限计算，不能只看它自己的直接条目。
7. 标了 Default 的目录条目只对此后新建的对象生效，既有子对象不回填。
8. Revoke 删除与该主体直接相关的条目：KindUser 且 ID 等于 p.UID，以及 KindGroup 且 ID 等于 p.GID 或出现在 p.Groups 中；返回删除条数，路径未知报 ErrUnknownPath，一条都没删到报 ErrUnknownEntry。
9. 每次成功 Apply 或 Revoke 之后都要重算 mask；条目上保存的必须是原始位，不得把被 mask 截断后的有效位写回条目。
10. 票号必填，缺失报 ErrBlankTicket，同一路径上重复使用报 ErrTicketReused；每次成功改动 Revision 加一，审计序号全局递增，Audit 按序号升序返回。
11. 受管路径数超过 MaxPaths 报 ErrPathLimit，空路径报 ErrBlankPath；快照是字节串，Restore 之后条目、mask、Revision、已用票号与审计序号必须与快照时一致，坏快照报 ErrBadSnapshot。
12. 同一路径的并发改动只能有一个成功，其余返回 ErrConflict；同一票号的并发请求只能成功一次。

硬约束：单路径写入必须原子，失败不留半套条目；快照恢复后授予水位（Revision）与审计序号不得回退或重用；条目数和路径数都是硬上限，超限必须先拒绝再报错。

验收口径：go test ./... 15 条全部通过，不得修改测试、跳过或用固定输入写死；并发与重启场景自行验证；只做本地实现，不接外部系统。

## 运行

```
go test ./...
```

## 说明

- 仅使用 Go 标准库。
- 测试位于 `tests/test_acl_grant_tx_test.go`。
