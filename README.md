# 静态网格合批与索引位宽升级

## 题面

静态场景换成合批之后，同一把椅子在材质边界上被画了两遍，远端贴图一直闪；一张地形网格顶点数过万以后索引全花，模型像被人从中间撕开；美术把一件道具从组里删掉，剩下模型的顶点跟着整体错位，批内区间没有收紧；切线空间的法线贴图贴到旋转过的物件上整体发暗，正对光源也不见高光；同一份静态资源在编辑器里预览正常，打进包就整体错位。落点模块 gsb/batch49-go-game-b-02，实现文件 static_batch_merger.go，核心类型 StaticBatchMerger，公开签名保持：NewStaticBatchMerger(MergerOptions) *StaticBatchMerger、AddSubMesh(groupKey string, mesh SubMesh, world Transform) error、Build(groupKey string) (Batch, error)、RemoveSubMesh(groupKey, subMeshID string) error、BatchOf(subMeshID string) (BatchRef, error)、Report() BuildReport、Groups() []string、SubMeshCount(groupKey string) int、TotalVertices(groupKey string) int、IndexWidth(groupKey string) int、Snapshot() ([]byte, error) 与 Restore([]byte) error。

要修的地方：一、AddSubMesh 校验，组名为空报 ErrBlankGroup，子网格 ID 为空报 ErrBadSubMesh，位置、法线、切线长度与顶点数对不上，或索引不是三的倍数、索引越界，一律报 ErrBadGeometry。二、同一个子网格 ID 只能属于一个组，同组重复添加报 ErrDuplicateSubMesh，跨组添加报 ErrSubMeshBound。三、一个组里的子网格材质与顶点布局必须一致，混入不同材质或不同布局报 ErrMixedGroup，被拒绝的子网格不得落表。四、Build 对未知组报 ErrUnknownGroup；顶点按插入顺序拼进批，每条子网格记录给出顶点起点、顶点数、索引起点与索引数，区间连续不重叠。五、世界变换烘焙进顶点，位置先乘左上 3x3 再平移，法线按左上 3x3 的逆转置再归一化，非均匀缩放之后法线仍是单位长度。六、顶点布局带切线时，切线要对法线重正交化，副法线按切线叉乘法线重算，不能只做一次法线归一就算完。七、单批顶点超过 MaxVerticesPerBatch 报 ErrBatchTooLarge，不截断；同一组重复 Build 的结果逐字段一致，不依赖 map 遍历顺序。八、单批顶点超过 65535 时整批切到 32 位索引并重建全部索引，不能把超限索引截断后复用旧缓冲。九、RemoveSubMesh 删除后剩余区间要向前压缩且不越界，BatchOf 回查拿到新区间；未知子网格报 ErrUnknownSubMesh，未知组报 ErrUnknownGroup。十、子网格全部移出后该批要进入退役表，BatchOf 回查到的结果带退役标记，不能从批表直接消失。十一、未 Build 就 BatchOf 报 ErrNotBuilt；Report 汇总组数、批数、子网格数、顶点数与索引数。十二、Snapshot 与 Restore 要保住组表、批表与区间映射，负载损坏报 ErrBadSnapshot 且不动内存。

硬约束：同一组的加删与 Build 串行提交，并发回查不能看到半个批；进程重启后从快照恢复，批号、区间与索引位宽原样还原；单批顶点数与索引位宽都是硬上限，先塞后清不算数。

验收：go test ./... 的 15 条用例全过，不能改测试、不能跳过、不能按固定顶点数写死；并发回查与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-b-02`
- 实现文件：`static_batch_merger.go`
- 主要类型：`StaticBatchMerger`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 测试规模：`tests/test_static_batch_merger_test.go` 共 15 条用例
