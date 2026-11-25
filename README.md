# batch49-go-game-a-19 / rank_tie_resolver

模块路径 `gsb/batch49-go-game-a-19`，包名 `task`，主文件 `rank_tie_resolver.go`，核心类型 `TieRankResolver`。

## 题面

赛季末合并分区榜单时，运营发现同一个一千两百分在三个分区分别显示为第 3、第 4、第 7；榜单页往后翻，前三名里有人出现两次，后面的人反倒漏了。另一个分区更离谱，被淘汰的玩家还挂在百分位分母里，导致头部玩家的百分位只有个位数。相关代码在模块 `gsb/batch49-go-game-a-19` 的 `rank_tie_resolver.go`，核心类型 `TieRankResolver`，需要按下面的规格补齐。

榜单每个分区几万条记录，赛季末还会做一次淘汰，把长期不上线的低分玩家清出去。运营要求翻页在并发写入时也能给出稳定结果，前端只拿着上一页最后一行继续往下翻；百分位则要能直接回答这个分数超过了多少比例的玩家，不能受分页影响。

对外接口至少包括：

- `NewTieRankResolver(opts Options) *TieRankResolver`
- `(r *TieRankResolver) Put(board, player string, score int64) error`
- `(r *TieRankResolver) Rank(board, player string) (RankInfo, error)`
- `(r *TieRankResolver) Top(board string, n int) []RankRow`
- `(r *TieRankResolver) Page(board string, after Cursor, limit int) (Page, error)`
- `(r *TieRankResolver) Percentile(board string, score int64) int`
- `(r *TieRankResolver) Watermark(board string) (int64, bool)`
- `(r *TieRankResolver) Len(board string) int`
- `(r *TieRankResolver) Snapshot() ([]byte, error)` / `(r *TieRankResolver) Restore([]byte) error`

具体规格：

1. `Put` 要求 board 与 player 都非空白，否则 `ErrBlankKey`；同榜同玩家再次写入覆盖旧分，榜内人数不变。写入成功后名次立即反映。
2. 榜单排序固定为分数降序、同分按玩家 ID 升序。`RankRow` 与 `Page` 的行都必须遵守这个顺序。
3. `Rank` 同时给出竞赛名次与密集名次：竞赛名次为 1 加上严格高于自己的玩家数，同分会跳号；密集名次为 1 加上比自己分数更高的不同分数个数，同分不跳号。两种口径不能共用同一套计算。
4. 未上榜的玩家调用 `Rank` 返回 `ErrPlayerNotFound`，不能返回零值名次。
5. `Top` 按排序返回前 n 行；n 小于等于 0 返回空，n 超过榜内人数时返回全部行。
6. `Page` 的游标由分数与玩家 ID 组成，候选行是排序中严格位于游标之后的行；只用分数做游标会在同分处重复返回或跳行。
7. `Page` 的 limit 小于等于 0 返回 `ErrBadLimit`；`Page.Total` 是榜内当前人数，`HasMore` 表示后面还有行，`Next` 是本次返回最后一行的游标。
8. `Percentile` 返回“严格低于该分数的人数占榜内总人数的百分比”，整除取整；分母是榜内总人数，不是某一页或前 N 名可见的行数，被淘汰的玩家不能计入。
9. `Options.MaxEntries` 大于 0 时，榜内条目超过上限就淘汰分数最低者，同分时淘汰玩家 ID 字典序最大者；`Watermark` 报告最近一次淘汰的分数，从未淘汰过时第二个返回值为 false。
10. 被淘汰的玩家视为不在榜上：`Rank` 返回 `ErrPlayerNotFound`，也不能出现在 `Top`、`Page` 与百分位分母里。
11. 同一 board 的 `Put` 与 `Page` 必须串行；并发写入之后翻页看到的是完整一致的快照，不能重复或漏行。
12. `Snapshot` 保存每个榜的分数表与淘汰水位；`Restore` 全量替换，载荷损坏返回 `ErrBadSnapshot`，失败时现有数据不能被改动。

验收方式：在本目录执行 `go test ./...`，全部用例必须通过；不得修改或跳过用例，不得针对固定输入写死结果。并发与重启恢复需要自行验证，只做本地实现，不依赖任何外部服务。

## 运行

```bash
go test ./...
```
