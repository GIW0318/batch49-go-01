# 依赖锁定图的闭包完整性、升级传播与确定性序列化

## 题面

供应链审计前一晚，锁定文件出了问题：同一张图在两台机器上生成的校验和不同，审计工具因此拒收；有人手工把一个传递依赖剪掉后，另一个直接依赖的下游立刻报版本缺失；还有一条记录只有版本号没有摘要，流水线却照样放行。请落在 gsb/batch49-go-safety-e-13 的 lock_graph_guard.go，把 LockGraphGuard 做成可审计、可复现的锁定图守卫。

守卫保存发布元数据（Release：名字、精确版本、64 位小写十六进制摘要、直接依赖清单）与当前锁定集（Entry：名字、版本、摘要、是否直接锁定）。关键签名：NewLockGraphGuard() *LockGraphGuard、Register(name, version, digest string, requires []Requirement) error、Releases(name string) []string、Pin(name, version, digest string) error、Unpin(name string) error、Locked(name string) (Entry, bool)、Entries() []Entry、Closure(root string) ([]string, error)、Upgrade(name, version string) (Plan, error)、Apply(plan Plan) error、Verify() []Problem、Serialize() ([]byte, error) 与 Restore([]byte) error。

要修的地方：

一、Register 拒绝空名字（ErrBadName）、非三段数字版本（ErrFloatingVersion）与非 64 位小写十六进制摘要（ErrBadDigest）；同一坐标重复登记不同摘要报 ErrDigestConflict。

二、Pin 只能锁已登记的发布，未登记报 ErrUnknownRelease；摘要与登记值不一致报 ErrDigestMismatch；同一模块重复直接锁定报 ErrAlreadyPinned；锁定成功后该条目在 Entries 里标记为直接锁定。

三、Closure 沿已锁定条目的依赖边求可达集合，包含根自身并按名字升序返回；根未锁定报 ErrNotLocked；闭包只沿已锁定条目展开，未锁定的依赖不参与可达集合。

四、Entries 覆盖整个锁定集并按名字升序；Locked 查单个坐标。

五、Serialize 每行写名字、版本、摘要三段，用制表符分隔，按名字升序，无表头；同一张图无论登记与锁定的先后顺序，输出必须逐字节一致，不得依赖映射遍历顺序。

六、Upgrade 只计算不改状态：以目标版本重算闭包，把版本变化或新增的条目写进 Plan.Changes（按名字升序，动作取 add 或 upgrade），并把旧版本、旧摘要、新版本、新摘要都填全；目标发布未登记报 ErrUnknownRelease。

七、升级后仍被其它组件传递依赖的旧版本要保留，只在完全不可达时才剪枝；剪枝后重新引入的可达条目要恢复原摘要，不能按当前最新版本重锁。

八、升级引入的间接依赖若与现有条目同坐标不同摘要，属于冲突，必须报错而不是择一保留。

九、同一组件被两个直接依赖以不同版本要求时，锁定集里只能有一个版本，Verify 要报告不可满足，而不是各锁一份。

十、Apply 提交 Plan 前逐条核对旧版本与旧摘要，与当前锁定集不符报 ErrStalePlan 并且不动任何条目；提交成功后锁定集与 Plan 完全一致。

十一、Verify 按名字与问题类型升序返回，至少覆盖未满足的依赖要求、缺失摘要、不可达条目三类问题，同一状态重复调用结果稳定。

十二、Restore 只恢复锁定条目，格式非法报 ErrBadSnapshot 且不动现网；Pin、Unpin、Upgrade、Apply、Serialize 之间串行化，序列化期间读到的必须是完整锁定集。

验收口径：go test ./... 15 条必须全过；不得修改测试、跳过用例或按固定输入写死；并发与恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-e-13`
- 实现文件：`lock_graph_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`LockGraphGuard`
