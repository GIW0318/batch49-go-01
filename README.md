# 调音台母线树的独奏、静音与延迟补偿

## 题面

扩声调音台的母线树改版上线后，返送端频繁出现“点了独奏还是一整片没有声音”和“把带延迟的效果旁路之后，现场口型反而更对不上”两类反馈，录音棚与直播两套场景都能复现。落点 gsb/batch49-go-game-c-11 的 bus_solo_router.go，需要把 BusSoloRouter 补齐。

建树接口：NewBusSoloRouter(maxDepth int) *BusSoloRouter 建出来的路由自带唯一根母线，ID 为 RootBusID，名字 master，深度 0；CreateBus(parentID, name string) (BusID, error) 在指定父母线下面挂一条子母线，ID 依次为 bus-1、bus-2 递增，名字两侧空格要去掉，空名报 ErrBadBusName，父母线不存在报 ErrBusNotFound，同级重名报 ErrDuplicateBusName，超过深度上限报 ErrDepthExceeded；DeleteBus(busID string, policy DeletePolicy) error 删除母线，根母线一律拒绝并报 ErrRootBus；Children(busID string) ([]BusID, error) 按创建顺序给出子母线，Path(busID string) ([]string, error) 给出根到该母线的名字链，Bus(busID string) (BusInfo, bool) 读单条记录，Muted(busID string) (bool, error) 只读该母线自身的静音标记、不折算继承结果，Count() int 给出母线总数。删除策略分三种：DeleteReject 遇到仍有子母线就报 ErrHasChildren，DeleteDetach 把子母线挂到被删母线的父级，DeleteCascade 连子树一起删；无论哪种策略，收尾都不允许留下指向已删母线的父引用。

开关与可听性：SetMute、SetSolo、SetBypass、SetLatency 都按母线 ID 定位，母线不存在统一报 ErrBusNotFound；静音沿子树向下继承，子母线自己的静音标记保持不变；独奏只压制同一父母线下的兄弟母线，不改变兄弟之外任何层级的可听性，同层同时按住两条独奏时这两条都保持可听、其余兄弟继续被压低，独奏复位后所有兄弟立刻恢复；Audible(busID string) (bool, error) 给出最终结果，静音优先于独奏，被独奏母线下属的母线不会因为“独奏生效”而解除自身静音。

延迟补偿：SetLatency(busID string, ms int) error 设置母线自身的处理延迟，允许 0 到 2000 毫秒，越界报 ErrLatencyRange；Latency(busID string) (int, error) 返回对齐补偿量，等于全树最长路径延迟减去该母线到根的路径延迟；路径延迟只累加未被旁路母线的自身延迟；母线被旁路时自身延迟退出路径，解除旁路后重新计入，推子连续改延迟与旁路时补偿查询要跟着最新链路走。旁路一条带延迟的总线之后，补偿量要按剩下的链路重新算过，不能把该效果的延迟直接从原补偿值里减掉。

快照与并发：Snapshot() ([]byte, error) 与 Restore([]byte) error 覆盖整棵树、所有开关、延迟与 ID 水位，快照里带上深度上限；Restore 遇到损坏内容报 ErrBadSnapshot 并且不动现网，恢复出来的结构必须无环、无悬挂父引用，恢复后新建母线不能复用旧 ID，恢复出来的树即使比新实例声明的深度上限更深也要完整可用。开关设置、可听性与补偿查询会被调音台多路推子并发调用，同一母线的并发开关按调用串行生效，Audible 与 Latency 只能看到整份旧状态或整份新状态，Snapshot 也不得拍到半棵树。

验收口径：go test ./... 的 15 条用例全过，测试文件不得修改、跳过或按固定输入写死；并发与恢复自行验证；只做本地实现，交付源码与运行记录。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-c-11`
- 实现文件：`bus_solo_router.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过模块路径导入实现
- 主要类型：`BusSoloRouter`
