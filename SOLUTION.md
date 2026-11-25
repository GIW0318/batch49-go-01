# SCIM 组图同步修复说明

实现文件仅 `scim_group_graph_sync.go`；未改动 `tests/` 下任何用例。`go test -count=1 ./...` 为 15/15 PASS，`-race -count=3 ./...` 与并发用例 `-count=50` 均通过（详见 `test-run-record.txt`）。

## 数据模型与并发
- 结构为 `tenants[租户].groups[组]`，每组存 `DisplayName`、成员集合 `map[Ref]struct{}`、`Version` 与最近一次 `Diff`（差异水位，随快照持久化）。成员集合是成员关系与组套组边的唯一事实来源。
- 单个 `sync.RWMutex`：写（Upsert/Delete/Set/Add/Remove/Restore）用 `Lock` 串行化，读用 `RLock`。所有校验先在候选集合/候选邻接表上完成，通过后才一次性替换 map，因此不会出现半条边，并发加成员不丢、不重。

## 十二个问题的落点
- 身份校验：租户号空白 → `ErrBlankTenant`，组 ID 空白 → `ErrBlankGroupID`，均在加锁/建记录前返回，不落半条。
- 版本水位：新建 `Version=1`；再次 Upsert 仅改展示名并 `Version+1`，成员与差异水位原样保留；成员写（Set/Add/Remove）成功后同样 `+1`，`Diff.Version` 为新版本；失败不涨版本。
- 上限：`MaxGroupsPerTenant` 按租户计数（删除即释放名额，别的租户独立）；`MaxMembersPerGroup` 按去重后的直接成员数计（嵌套组算 1 个），SetMembers 整批超限返回 `ErrMemberLimit` 且不替换旧集合。
- 差异：SetMembers 按集合求 Added/Removed/Kept，重复 Ref 去重；Added/Removed 均为确定性排序的非空切片，未变化成员不会混入；目标组不存在 → `ErrGroupNotFound`。
- 引用：字符串引用按第一个冒号解析，仅接受非空 ID 的 `user:`/`group:`，否则 `ErrBadRef`；重复 → `ErrDuplicateMember`；组引用必须在同租户已登记，否则 `ErrGroupNotFound`，且不允许跨租户引用。
- 删除：删除不存在的组 → `ErrGroupNotFound`；删除组时仅从同租户所有父组摘除 `group:<id>` 入边，不级联删任何组；子组保留，基于存活组的反向边展开，已删组及其上游路径不会再进入 EffectiveMembers。
- 环与深度：写入前用候选邻接表做三色 DFS 环检测，成环 → 包装 `ErrCycle`；无环后做最长路径 DP，叶子深度 1，全局最长链超过 `MaxDepth` → 包装 `ErrDepthExceeded`，错误信息含完整超限路径（如 `g-1 -> g-2 -> g-3 -> g-4 is depth 4, limit 3`）。两类拒绝都发生在落图之前，旧边一条不动。
- 查询：`Members` 按 Type 再按 ID 升序返回且计数等于集合大小；`EffectiveMembers` 先取直挂用户的组，再沿反向包含边 BFS 展开全部祖先，visited 去重（双路径/菱形只算一次），结果按组 ID 升序，严格限定本租户。

## 快照与恢复
- `Snapshot` 在 RLock 下输出带格式标记的 JSON，覆盖全部租户的组、成员、版本水位与最近 Diff。
- `Restore` 先反序列化并完整校验（Ref 合法性、组引用存在、无环）到全新 map，再用写锁整体替换 `tenants`；JSON 损坏或语义损坏一律返回包装 `ErrBadSnapshot`，替换前出错故现网不动。恢复后版本、Members、Depth、EffectiveMembers 与中断前一致，后续 Diff 版本号接续。

## 取舍备注
- `Limits` 某一项为 `<=0` 时视为该维度不限制（题面用例均显式给正值）。
- 删组触发父组成员集合变化时，相应父组版本号 `+1` 并留一条 Removed 水位。
- `Members/Depth` 对不存在的组返回 `ErrGroupNotFound`；`EffectiveMembers` 对无数据租户/用户返回空切片与 nil。
