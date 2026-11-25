# SBOM 依赖图中受影响组件的引入者归属

## 题面

某次供应链应急里，SBOM 平台给出一份受影响组件清单，值班同学按清单去问“这个组件是谁引进来的”，得到的答案却把两个共存版本混成了一个节点：生产依赖拉进来的 1.2.3 和测试脚手架拉进来的 2.0.0 被合并，责任人找错了团队。路径枚举也不稳定：同一个图跑两次，路径顺序变来变去，值班同学截图对不上；路径多的时候直接全量返回，前端一次渲染几万条。打开作用域剪枝后更糟：明明只剩开发依赖能到达某个组件，剪枝之后返回空，但代码却回落到被剪掉的路径上继续报受影响，等于自己骗自己。落点 gsb/batch49-go-safety-c-03 的 sbom_reach_attribution.go，类型 ReachAttributor，签名保持：NewReachAttributor() *ReachAttributor、Load(graph Graph) error、Components() []string、Roots() []string、Node(id string) (Component, bool)、PathsTo(component string, limit int) ([]Path, error)、Introducers(component string) []Owner、Shortest(component string) (Path, error)、SetPolicy(policy PrunePolicy) error、Policy() PrunePolicy、Snapshot() ([]byte, error)、Restore(data []byte) error。

要修：加载时校验组件名与版本非空、节点编号唯一、边两端都存在、根节点存在、禁止自环，任一条不合格报 ErrBadGraph 且整图不替换，坏图不得覆盖现网已加载的图。组件列表与根列表都按节点编号升序返回，节点编号统一为“包名@版本”，Component 的 ID() string 返回该编号，Node 按编号精确查找，找不到返回 false。路径枚举只产生简单路径，经过环时不得重复经过同一个节点；路径包含起点与终点，结果先按边数升序，边数相同再按节点序列的字典序排，同一张图反复枚举必须得到完全一致的顺序。路径上限必须为正，非正报 ErrBadLimit；超过上限时截断，并给出本次结果是否完整的标记，调用方据此决定要不要放宽上限重查。未知组件报 ErrUnknownComponent；已知但没有任何根可达时返回空结果而不是报错，Shortest 在这个情形下报 ErrNoPath，有多条同长最短路径时取字典序最小的那条。Introducers 返回直接声明依赖的去重父节点，按编号升序，根节点与未知组件返回空；同一个包名存在多个版本时，各版本按各自节点分别归属，不能合并成一个节点。剪枝策略按边的作用域生效，开发与可选作用域可分别丢弃；剪枝之后若生产路径不存在，必须明确返回空结果，不得回落到被剪掉的路径上凑数，也不得因此悄悄把生产路径一起剪掉。快照要连同依赖图与当前剪枝策略一起序列化，恢复后枚举、归属与最短路径都接着原状态走；损坏的快照报 ErrBadSnapshot 且不得改动现网。

硬约束：枚举与加载必须互斥，任何一次路径查询只能看到完整的旧图或完整的新图，不得看到只替换了一半的节点表与边表；同一张图重复查询必须逐字节稳定，不得依赖 map 遍历顺序；单次枚举的路径条数受上限硬顶，不得先全量展开再截断导致内存失控。

验收：go test ./... 15 条全过，不得改测试、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-c-03`
- 实现文件：`sbom_reach_attribution.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`ReachAttributor`
