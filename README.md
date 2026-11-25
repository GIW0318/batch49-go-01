# 静态与动态双阴影图集的分区配额与重烘预算

## 题面

换到双阴影图集之后，同一盏平行光的静态瓦片越用越多，动态阴影反而分不到位置；美术调了一次阴影距离档，整张图集全部重烘，帧时间抖了三秒；同一块瓦片在一帧里被标了三次脏，重烘队列里排了三遍，真正要用的那块反而排在后面；玩家切场景之后墙上的阴影还是上一个场景的；同一盏灯下挂了几十个接收器，静态区一轮就被占满，降级标记也没有人看见，大灯反而没有位置。落点模块 gsb/batch49-go-game-b-05，实现文件 static_shadow_cache.go，核心类型 StaticShadowCache，公开签名保持：NewStaticShadowCache(CacheOptions) *StaticShadowCache、Acquire(lightID string, size TileSize, isStatic bool) (TileHandle, error)、Release(h TileHandle) error、Invalidate(h TileHandle) error、MarkLightDirty(lightID, reason string) error、ReadTile(h TileHandle) (TileData, error)、RebakeFrame() RebakeResult、TileBudget() Budget、PendingRebakes() int、Snapshot() ([]byte, error) 与 Restore([]byte) error。

要修的地方：一、Acquire 校验，光源 ID 为空报 ErrBlankLight，宽或高非正报 ErrBadTileSize，边长超过 MaxTileEdge 报 ErrTileTooLarge。二、静态请求优先占用静态区槽位，静态区满时降级到动态区并在句柄上打标记，降级后的句柄 Static 为假、Degraded 为真，两区都满报 ErrNoCapacity，不得静默丢弃。三、动态瓦片只能落在动态区，即使静态区还有空槽也不得写入静态页。四、句柄带唯一 ID 与代际，Release 未知或已释放的句柄报 ErrUnknownHandle，释放后槽位可以复用。五、Invalidate 让该瓦片代际加一并进入待重烘，旧句柄再 ReadTile 报 ErrStaleHandle，已释放的句柄报 ErrUnknownHandle。六、MarkLightDirty 校验光源与原因，光源为空报 ErrBlankLight，原因为空报 ErrBlankReason，没有任何瓦片的光源报 ErrUnknownLight。七、只改阴影距离档时，静态瓦片的失效范围按级联裁剪，只失效受影响的那一级，不是整光源全量失效。八、同一瓦片在一帧内被多次标记失效只重烘一次，超出每帧预算的瓦片排队到下一帧，RebakeFrame 如实返回已烘与排队数。九、RebakeFrame 每帧最多处理 RebuildsPerFrame 个瓦片，已烘瓦片 ReadTile 返回 Baked 为真且像素数等于宽乘高，读到的代际与当前句柄一致。十、两区配额互不侵占，TileBudget 如实给出各自槽位、占用与每帧预算。十一、同一光源多次 Acquire 得到互不相同的瓦片，释放其中一块不影响其余。十二、Snapshot 与 Restore 要保存槽位、句柄代际、待重烘队列与脏光源，负载损坏报 ErrBadSnapshot 且不动内存，恢复后旧句柄仍按代际判定。

硬约束：两区槽位与每帧重烘数都是硬上限，先占后清不算数；进程重启后从快照恢复，配额占用、代际与待重烘队列原样还原；并发 Acquire 与 Release 不得超卖槽位，同一瓦片不能被两个调用方同时持有；重烘顺序按进入队列的先后固定，不能依赖 map 遍历。

验收：go test ./... 的 15 条用例全过，不能改测试、不能跳过、不能按固定编号写死；并发占用与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-b-05`
- 实现文件：`static_shadow_cache.go`
- 主要类型：`StaticShadowCache`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 测试规模：`tests/test_static_shadow_cache_test.go` 共 15 条用例
