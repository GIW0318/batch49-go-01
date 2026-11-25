# 能力映射口径

本文档固定 `CapabilityDemandSolver` 对运行期访问轨迹的映射、合并与序列化口径。

## 单条访问映射

| 访问 | 模式/条件 | 能力 |
| --- | --- | --- |
| `open` | `Mode == "rdir"` | `cap_read_dir` |
| `open` | 含读标志（`r`、`rw` 等） | `cap_read_file` |
| `open` | 含写标志（`w`、`rw` 等） | `cap_write_file` |
| `readdir` | 任意已接受模式 | `cap_read_dir` |
| `stat` | 任意已接受模式 | `cap_stat_file` |
| `chmod` | 任意已接受模式 | `cap_chmod` |
| `chown` | 任意已接受模式 | `cap_chown` |
| `exec` | 任意已接受模式 | `cap_exec` |

`open` 的 `rdir` 是目录打开口径，和 `readdir` 一样落到 `cap_read_dir`，不再因为字符串中包含 `r` 而落到 `cap_read_file`。普通 `open` 同时带读、写标志时，`cap_read_file` 与 `cap_write_file` 同时出现。

操作名或路径为空的访问不接收，返回 `ErrBadAccess`。已接收轨迹达到 `MaxAccesses` 后，新访问返回 `ErrAccessLimit`，不会覆盖或淘汰旧访问。

## 拒绝访问与未映射访问

- `Denied == true` 的访问是拒绝记录，保留在原始轨迹、条数和快照中，但不参与能力映射、`Solve`、`Explain` 或 `Uncovered`。
- 非拒绝访问无法映射到任何能力时，按接入顺序进入 `Uncovered()`；只要存在这种访问，`Solve()` 就返回 `ErrUncoveredAccess` 和空的 `Caps`，不输出可用建议。
- 轨迹长度为零时 `Solve()` 返回 `ErrEmptyTrace`。只有拒绝记录时，原始轨迹非空且没有未覆盖的合法访问，因此返回空能力集合而不是 `ErrEmptyTrace`。

## 同路径后置条件覆盖

对同一路径上的未拒绝访问，如果先前的写文件证据之后出现未拒绝的 `chown`，后续 `chown` 按题面的后置条件口径覆盖该写需求：对应的 `cap_write_file` 证据不再进入 `Demand.ByCap` 或 `Explain(cap_write_file)`。其他独立能力仍然保留；`chown` 之后新出现的写访问会重新产生 `cap_write_file`。

该折叠只影响能力推导和证据解释，不删除 `Accesses()` 中的原始记录，也不影响快照中的完整审计轨迹。

## 排序、去重与差异

- `Demand.Caps` 按能力名字节序升序、去重。
- `Demand.ByCap[cap]` 按路径字节序升序、去重；同一路径的多次访问只在路径列表中出现一次。
- `Explain(cap)` 保留全部仍支撑该能力的原始访问，并严格按接入顺序返回；未知能力返回空切片且不报错。
- `Diff` 分别对需求能力和当前画像能力去重，再计算 `Missing = Demand - Profile`、`Extra = Profile - Demand`；两个结果均按字节序升序。额外能力不能抵消缺失能力。
- 排序使用 Go 字符串原生字节比较，不依赖 locale、`Seq` 或 map 遍历顺序。`Seq` 只作为访问字段保留，不参与能力判定。

## 并发、快照与恢复

- 求解器使用 `sync.RWMutex` 保护轨迹和配置。`AddAccess`、`Restore`、`Reset` 串行修改；`Solve`、`Explain`、`Uncovered`、`Snapshot`、`Count`、`Accesses` 读到的要么是追加前的完整轨迹，要么是追加后的完整轨迹。
- `Snapshot` 使用带 `magic/version` 信封的 `gob` 编码，保留 `SolverConfig`、全部 `Access` 字段以及路径中的原始字节。
- `Restore` 先完整解码和校验载荷，再整体替换状态；坏载荷返回可由 `errors.Is(..., ErrBadSnapshot)` 识别的错误，且不破坏调用前状态。
- `Reset` 只清空轨迹，保留同一求解器实例及其配置，之后可以重新采集。
