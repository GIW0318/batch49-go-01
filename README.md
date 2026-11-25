# 成就解锁率重算与展示柜治理

## 题面

公会的成就展示柜上线一周后，运营发现两件事：同一名玩家在好友视角里展示的格子数比他自己看到的少，稀有度百分比在运营调整了活跃人数以后一直不动，重算前后两版数据还互相串过。展示柜原本只读存档里的解锁记录，但排行榜改版后同一个成就在不同分区被重复统计，百分比的分母也跟着变，运营已经不敢按这份数据发赛季奖励。落点是 gsb/batch49-go-game-a-22 的 achievement_showcase_guard.go，类型 ShowcaseGuard。调用面是 NewShowcaseGuard(Options) *ShowcaseGuard、DefineAchievement(id string, hidden bool) error、RecordUnlock(achievementID, account string) error、Rarity(achievementID string) (Rarity, error)、SetActivePlayers(n int) error、SetShowcase(account string, slots []string) error、View(account, viewer string) []ShowcaseEntry、RecalcRarity(at int64) (RecalcResult, error)、RemoveAchievement(id string) error，以及 Snapshot() ([]byte, error) 与 Restore([]byte) error。

需要修好的行为：成就 id 为空或重复定义要拒绝；解锁记录对同一账号同一成就只计一次，重复上报不报错也不加数；未知成就与空账号报错；稀有度在重算前只给出解锁人数，百分比为零，版本号也为零；活跃人数必须为正，非正值报错；重算按解锁人数除以活跃人数算百分比，保留两位小数，每次重算版本号加一，返回本次变化的成就条数；重算时间不得早于上一次重算时间，否则报错；展示柜槽位有上限，超限整次拒绝且旧内容不变；展示柜只能放已解锁成就，未知成就与未解锁成就分别报错；查看自己的展示柜按槽位顺序给出真实成就 id；查看他人时隐藏成就显示占位并保留槽位，其余条目不受影响；同一账号展示柜写入串行，重算与读取并发时只能看到完整旧版本或完整新版本。运营还要能按账号导出展示柜内容做人工核对，导出的顺序必须与玩家自己看到的一致。

硬约束：活跃人数变化后，未重算的稀有度沿用上一版，不能按新分母即时计算；展示柜去重后槽位要压缩，不留下空槽，槽位上限按去重后计算；隐藏成就在他人视角的占位要占一个槽位，不能凭空少一条；快照保存解锁计数、活跃人数、展示柜与稀有度版本，恢复后版本号继续递增；损坏快照恢复报错且不动现网状态。重算失败时版本号不能前进，旧版本继续对外服务；展示柜为空时返回空列表，不能塞入占位条目；查看接口不能修改内部状态，连续两次查看同一账号结果一致。

验收口径：go test ./... 15 条用例全过，测试不得修改、跳过或按固定输入写死；并发解锁、并发展示柜写入与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-a-22`
- 实现文件：`achievement_showcase_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`ShowcaseGuard`
