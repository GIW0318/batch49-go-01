# 依赖混淆检测：坐标归一化冲突类、作用域前缀剥离与同名异源取证

## 题面

周二凌晨的内部包仓库同步作业把一个公共同名包拉进内网镜像，构建流水线十几分钟内就装上了外部版本；事后审计只能看到包名末段相同的告警，说不清哪一侧的坐标覆盖了另一侧，也无法解释为什么上周的豁免还在生效。落点 gsb/batch49-go-safety-c-07 的 dep_confusion_detector.go，类型 ConfusionDetector，需要把依赖坐标归一化、同名异源判定和豁免治理固定成可复核的规则。接口保持：NewConfusionDetector(Config) *ConfusionDetector、Normalize(name string) string、ClassID(name string) string、Scan(coords []Coordinate) ([]Collision, error)、Allow(collisionID string, until time.Time) error、RevokeAllow(collisionID string) error、ActiveAllows(now time.Time) []AllowRecord、Collisions() []Collision、Explain(collisionID string) (Evidence, error)、Rounds() int、Snapshot() ([]byte, error)、Restore([]byte) error。

要修：一、归一化先去掉首尾空白并转小写，剥离 @scope/ 前缀，去掉 -、_、. 三类分隔符，再把西里尔等同形字母还原成拉丁字母，结果作为唯一比较键。二、冲突类 ID 形如 cc- 加八位十六进制，只由归一化名决定，同名不同版本、不同分隔写法的 ID 必须一致，跨轮次可追踪。三、判定按归一化名分组，不得只比较包名的最后一段；组内同时出现内部源与公共源且发布者不同，判为同名异源并阻断，严重度 high。四、完全相同的坐标重复出现只保留一个成员，去重按来源、发布者、版本三元组。五、只是编辑距离相近但不同名的，判为疑似，严重度 medium，类名取内部源一侧的归一化名，没有内部源时取字典序较小者；默认策略下疑似类不得当作同名异源处理，只有策略显式升级才阻断。六、每轮扫描把轮次加一，冲突类保留首见轮与末见轮，同一类跨轮沿用同一 ID 并更新末见轮。七、豁免必须先有已扫描出的冲突类，未知类报 ErrNotFound；到期时间必须晚于当前时间，否则报 ErrBadUntil。八、豁免记录带到期时间与适用范围，豁免期内该类不再阻断但仍保留成员证据，Collisions 与 Explain 都能看到。九、豁免不得自动续期，到期后同一冲突类重新按策略判定，历史豁免只作为记录保留。十、ActiveAllows 按当前时刻只返回未到期的记录，按冲突类 ID 升序。十一、Explain 返回按来源、名称、发布者升序排列的取证行与最终判定，未知 ID 报 ErrNotFound。十二、快照保存轮次、冲突类、成员与豁免记录；Restore 整体替换，损坏载荷报 ErrBadSnapshot 且不动现有状态。

硬约束：并发 Scan 串行计轮，同一轮不得出现两个不同的类 ID；豁免记录与冲突类必须一起跨重启恢复；扫描、豁免与快照三者的可见状态要么全旧要么全新。

验收口径：go test ./... 15 条全过，测试不得修改、跳过或按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-c-07`
- 实现文件：`dep_confusion_detector.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`ConfusionDetector`
