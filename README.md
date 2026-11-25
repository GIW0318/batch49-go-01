# CVE 受影响区间补集与最小升级目标求解

## 题面

漏洞周报要求把 CVE 影响区间换算成"该升到哪个版本"，这周返工了两次。一次把还在受影响区间里的版本当成修复版下发，客户升级完漏洞依旧；一次把从未发布过的版本号写进了升级单，镜像仓库里根本拉不到；还有一次给一个早已打过补丁的组件又开了一张升级单，把业务方从稳定版推到了下一个大版本。

落点在 gsb/batch49-go-safety-e-05 的 upgrade_target_solver.go，类型 UpgradeTargetSolver，签名保持：NewUpgradeTargetSolver() *UpgradeTargetSolver、AddRange(r Range) error、AddReleased(component, version string) error、Solve(installed Installed) (Target, error)、Candidates(installed Installed) []string、NoFix(installed Installed) (Risk, bool)、Compatibility(component, maxVersion string) error、Components() []string、Ranges(component string) []Range、Released(component string) []string、Snapshot() ([]byte, error)、Restore([]byte) error。版本一律三段数字点分，区间用 Range{Component, CVE, Introduced, Fixed}，Introduced 闭、Fixed 开，Fixed 为空表示该组件至今没有修复版本；已安装版本用 Installed{Component, Version}。

求解模型：区间、版本清单、兼容上限三张表都只增量追加，求解本身不写状态；候选集必须先过补集再过滤版本身份，最后按版本序取第一个，因此同一份输入反复求解必然落在同一个版本上。

要修的具体故障与边界：
一、组件名为空、CVE 号为空、区间起止倒置报 ErrBadRange；版本号不是三段数字报 ErrBadVersion，被拒绝的区间与版本都不得入库。
二、已发布清单按组件保存，重复登记同一版本幂等；Released 返回升序去重结果，与登记顺序无关。
三、求解前必须已有该组件的已发布清单与受影响区间，否则报 ErrUnknownComponent；已安装版本为空或非法报 ErrBadInstalled。
四、安全版本集是受影响区间在已发布清单上的补集，目标只能从这个补集里取，未发布的版本号一律不能当目标。
五、目标必须严格高于已安装版本，不得降级；同一主版本内有安全版本时优先选最小的同主版本版本。
六、同一主版本内没有安全版本而相邻主版本有时，是否跨主版本由 Compatibility 声明的上限决定，并在结果的 Reason 与 CrossMajor 里留下说明。
七、已安装版本本身就在安全补集里时返回原地不动，Moved 为 false，Reason 为 already_safe。
八、恰好等于 Introduced 的版本算受影响，恰好等于 Fixed 的版本算安全，端点开闭不能弄反。
九、Candidates 返回已安装版本之上、落在安全补集内、且不超过兼容上限的版本，升序返回，去重。
十、没有任何可用安全版本时 Solve 报 ErrNoFix，NoFix 给出最高可用版本与残留 CVE 清单，而不是返回空结果让上层猜。
十一、同一组区间与版本清单并发求解结果一致：八个 goroutine 同时求解同一已安装版本，得到的目标与理由必须相同。
十二、Snapshot 与 Restore 全量保存区间、版本清单与兼容上限，损坏数据报 ErrBadSnapshot 且不影响现有求解。

硬约束：区间集合、版本清单与兼容上限对求解只读，求解过程中不得把缓存写脏；多区间叠加时按并集判定受影响，结果只由录入内容决定，不依赖 map 遍历顺序，因此同一份输入反复求解得到同一个目标。

验收：go test ./... 15 条必须全过，测试不得修改、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-e-05`
- 实现文件：`upgrade_target_solver.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`UpgradeTargetSolver`
