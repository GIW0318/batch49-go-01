# 奖励槽位预留与溢出缓冲

## 题面

节日活动发奖一波三折：背包只剩两个空格却要发三件套，发到一半报错，玩家只收到半套，剩下的补偿邮件又撞上背包满全部退信；还有人领奖时连续点确认，同一份奖励被占了两次槽位，事后槽位不够用，整服活动奖励全部卡住；运营侧还有一个长期问题，同一份奖励可能由签到、邮件和活动三条链路同时发放，三边各自算槽位，谁也不知道背包到底被预留了多少。落点模块 gsb/batch49-go-game-a-03，实现文件 reward_slot_reserver.go，核心类型 SlotReserver，公开签名保持：NewSlotReserver(Options) *SlotReserver、Reserve(owner, token string, slots int, ttl int64) (Reservation, error)、Commit(owner, token string) error、Release(owner, token string) error、Available(owner string) int、Used(owner string) int、Tick(owner string, at int64) int、Reservation(owner, token string) (Reservation, bool)、Spill(owner string, reward Reward, at int64) (string, error)、ClaimSpill(owner, spillID string, at int64) (Reward, error)、SpillEntries(owner string) []Spill、Snapshot() ([]byte, error) 与 Restore([]byte) error。

要修的十二条：一、Reserve 空角色或空 token 报 ErrBlankArg，槽位数非正报 ErrBadSlots，ttl 非正报 ErrBadTTL；二、可用槽位等于总容量减去已提交槽位与未回收预留之和，预留在提交前也占额度，超过容量报 ErrCapacityExceeded；三、同一 token 以相同条件重复预留原样返回首次结果、不重复占额度，条件不同报 ErrTokenConflict；四、Commit 只接受 pending 预留，成功后该批槽位计入 Used 且不再回到池里，token 不存在、已释放或已过期报 ErrUnknownToken；五、Release 只接受 pending 预留，释放后槽位归还，二次释放报 ErrUnknownToken；六、Tick 按 at 回收到期的 pending 预留并返回回收条数，提交过的预留永不回收；七、Spill 把整份奖励转入溢出缓冲并返回非空编号，ExpireAt 等于 at 加 SpillTTL，奖励号为空或槽位非正报 ErrBadReward，同一奖励号重复转入报 ErrRewardDuplicated；八、ClaimSpill 在容量足够时把奖励搬进背包并计入 Used、返回奖励内容，编号不存在报 ErrSpillUnknown，已到到期时刻报 ErrSpillExpired 且条目保留；九、已提交的预留再次 Release 必须报错，槽位不得被归还两次；十、溢出条目在领取时槽位仍不足要保留原 TTL，不能续期也不能删除；十一、到期的预留只由 Tick 回收，Available 与 Used 绝不顺手回收；十二、Snapshot 保存全部预留、溢出条目、状态与到期时刻，Restore 整份替换，损坏数据报 ErrBadSnapshot 且不动现有状态，恢复后 Tick 仍按原到期时刻回收。

硬约束：token 是幂等键，同一 token 只能成功预留一次；预留、提交、释放与领取串行，Available 与 Tick 并发时不得看到负数或半份状态；每角色 pending 预留数达到 MaxReservations 报 ErrReservationLimit；溢出缓冲的编号必须按角色唯一且可读，SpillEntries 只列未领取条目；槽位账本不接受负数，回收与领取同时发生时以容量上限为准。

验收：go test ./... 15 条用例全过，不能改测试、不能跳过、不能按固定 token 写死；并发领取与重启回收自行验证；快照恢复后编号不得回退，重复领取同一个溢出编号必须报错；活动奖励一旦进入溢出缓冲，玩家下线再上线仍要能看到原到期时刻与奖励内容；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-a-03`
- 实现文件：`reward_slot_reserver.go`
- 主要类型：`SlotReserver`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 测试规模：`tests/test_reward_slot_reserver_test.go` 共 15 条用例
