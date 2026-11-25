# 物品多来源锁定与批量丢弃预演

## 题面

批量丢弃上线后事故不断：有玩家把装备中的武器连着整批杂物一起丢进了商店，找回工单排了一整天；还有人在限时锁到期后重登，发现物品明明该解锁却仍被拦着，客服查记录又找不到到底锁在哪个来源上；更离谱的是活动道具被系统锁住，玩家自己点了解锁按钮，界面显示已解锁，丢弃却依然失败，两边说法对不上。落点模块 gsb/batch49-go-game-a-02，实现文件 item_lock_guard.go，核心类型 ItemLockGuard，公开签名保持：NewItemLockGuard(Options) *ItemLockGuard、AddItem(owner string, item BagItem) error、Items(owner string) []BagItem、Lock(owner, itemUID, reason string, until int64) error、Unlock(owner, itemUID, reason string) error、EffectiveLock(owner, itemUID string, at int64) LockState、Locks(owner, itemUID string) []LockEntry、LockCount(owner string) int、DryRunDiscard(owner string, uids []string) ([]Rejection, error)、Discard(owner string, uids []string, at int64) (DiscardResult, error)、Snapshot() ([]byte, error) 与 Restore([]byte) error。

要修的十二条：一、AddItem 空角色、空物品号报 ErrBlankArg，数量非正或同角色内物品号重复报 ErrBadItem；二、Items 按物品号字典序升序返回该角色背包；三、Lock 对空角色或空物品号报 ErrBlankArg，物品不在背包报 ErrUnknownItem，来源不在 player、timed、equipped、escrow、quest 五种里报 ErrBadReason；四、timed 来源必须带正的到期时刻，其余四种来源的 until 必须为 0，违反报 ErrBadDeadline；五、单件物品记录来源数达到 MaxLocksPerItem 后再加锁报 ErrLockLimit；六、Unlock 只摘指定来源，来源不在记录里报 ErrNotLocked，摘除后无来源则 EffectiveLock 返回未锁定；七、EffectiveLock 按 at 判定，timed 记录在 at 大于等于 until 后不再计入生效来源，Reasons 按字典序升序，未知物品返回未锁定；八、Locks 按写入顺序返回该物品的全部记录，LockCount 统计角色记录条数；九、DryRunDiscard 批量为空、出现重复物品号或超过 MaxBatch 报 ErrBadBatch，物品号不在背包报 ErrUnknownItem，返回按入参顺序排列的拒绝项，全部可丢时返回空切片且不报错；十、Discard 参数校验与 DryRunDiscard 一致，整批全部可丢才删除，Removed 按入参顺序返回；只要有一件被拦，整批不执行、Removed 为空，Rejected 与预演结论逐条一致；十一、系统锁与玩家锁叠加时，玩家侧 Unlock 之后系统来源仍然生效，物品依旧不可丢弃；十二、批量里同时存在可丢与不可丢条目时整批不得执行，可丢条目也不能被先删。

硬约束：限时锁到期后记录仍在，重复上锁同一来源只更新到期时刻、不新增条目；来源记录一旦写入不能被静默覆盖，预演结果只服务于本次确认，不得写入后续可复用的缓存；丢弃按记录保守拦截，即使限时来源已到期也要先 Unlock 才放行；同角色的加锁、解锁、预演与丢弃串行，预演不产生任何写入。

验收：go test ./... 15 条用例全过，不能改测试、不能跳过、不能按固定物品号写死；Snapshot 保存背包、全部锁记录与序号，Restore 整份替换，损坏数据报 ErrBadSnapshot 且不动现有状态；并发批量丢弃自行验证；锁记录与背包的删除同批生效，不能出现物品已删记录还在的中间态；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-a-02`
- 实现文件：`item_lock_guard.go`
- 主要类型：`ItemLockGuard`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 测试规模：`tests/test_item_lock_guard_test.go` 共 15 条用例
