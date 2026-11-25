# 配方前置链、熟练度升级与首制加成

## 题面

配方书改版后，玩家反馈满足技能等级也学不会新配方，有人连续制作同一件物品却一直拿首制奖励，还有人练到满级后熟练度显示成负数，前置配方下架后整条依赖链突然全部锁死。客服按配方 id 排查时又发现，同一个配方在两名玩家身上的解锁状态对不上，制作次数多的账号反而卡在中级升不上去。落点 gsb/batch49-go-game-a-25 的 recipe_mastery_guard.go，类型 RecipeMasteryGuard。调用面：NewRecipeMasteryGuard(Options) *RecipeMasteryGuard、DefineRecipe(Recipe) error、DeleteRecipe(id string) error、Discover(account, recipeID string) (bool, error)、Craft(account, recipeID string, at int64) (CraftResult, error)、Mastery(account, discipline string) MasteryInfo、Unlocked(account string) []string、IsUnlocked(account, recipeID string) bool、Recipes() []string，以及 Snapshot() ([]byte, error) 与 Restore([]byte) error。

要修的行为：配方 id、所属学科、熟练度增量任一为空或非正，技能门槛为负都拒绝定义；重复定义同一配方报错；定义时检测前置环，自引用与两两互引都要拒绝，允许先定义引用后定义被引用者；发现未知或已删除配方报错，空账号报错；技能等级不够报错且不解锁，前置配方未解锁也报错且不解锁；发现成功后重复发现返回未新增，不报错；未发现的配方不能制作；制作成功按学科累计熟练度并返回当前等级，首次制作同一配方给一次性加成，之后不再给；熟练度只增不减，升级保留溢出点数，等级由累计点数除以每级阈值取整，到上限后不再提升；同一账号的制作时间不得回退，回退报错且不改状态；账号解锁列表与配方列表都按 id 升序，读取返回副本，调用方改动不影响力内部状态；同一账号的制作与发现串行，不同账号互不影响。

硬约束：制作失败不消耗首制加成，之后成功制作仍按首次计算；前置配方被删除后，依赖链下游已经解锁的配方保持可用，新的解锁被阻断；快照保存解锁集合、熟练度与首制标记，恢复后重复制作不再给首制加成；损坏快照恢复报错且保持现网状态；首次制作标记与熟练度要在同一事务内落盘，制作与发现不能读到中间状态；等级上限由配置决定，未配置时使用默认上限；同一账号的读取结果要能复现，不能因为并发制作返回半更新的等级与点数；运营会按学科导出熟练度榜单，导出结果里不允许出现负等级或点数回退。

验收口径：go test ./... 15 条用例全过，测试不得修改、跳过或写死；并发制作与重启恢复自行验证；上线前用一名新账号从零练到满级核对等级、点数与首制奖励只发一次；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-a-25`
- 实现文件：`recipe_mastery_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`RecipeMasteryGuard`
