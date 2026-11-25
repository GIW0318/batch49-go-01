# 模糊器语料的近重复判定与代表样本管理

## 题面

模糊测试跑了一夜，语料目录从两万条涨到三十万条，其中大半是同一段输入改了几个字节的翻版；去重脚本按整串比较，遇到近似变体一律放行，索引表还把内存吃满，重启后又把已经判过的样本重新灌了一遍。运维每周还要人工抽查桶里的代表样本，判断到底该保留哪一条，现在只能凭入库先后拍脑袋。请落在 gsb/batch49-go-safety-e-12 的 corpus_deduper.go，把 CorpusDeduper 做成能长期运行的近重复判定与代表样本管理器。

每个样本带 ID、内容与入库时间。构造时给出 Config（BandSize、Threshold、MaxPerBucket、TTL），未填字段取 Default* 常量。关键签名：NewCorpusDeduper(cfg Config) *CorpusDeduper、Add(c Sample) (Verdict, string, error)、Similar(a, b Sample) (float64, error)、FingerprintOf(s Sample) (Fingerprint, error)、Representative(bucket string) (string, bool)、Buckets() []string、Evict(now time.Time) int、BucketOf(id string) (string, bool)、Lookup(id string) (Sample, bool)、Snapshot() ([]byte, error) 与 Restore([]byte) error。

要修的地方：

一、指纹按 BandSize 字节切带，最后不足一整带的也单独成带，每带取 FNV-1a 64 位哈希；桶名以 band0- 开头，后接首带哈希的 16 位小写十六进制。

二、Similar 用两样本去重后的带哈希交集除以较长一方的带数，结果落在 0 到 1；完全一致得 1，毫无交集得 0。

三、Add 先算指纹，再在同一桶内逐条比较相似度：达到 Threshold 判为 duplicate，返回该桶当前代表样本 ID，并且不落库；否则判为 new，落库后同样返回该桶当前代表 ID。同桶内按指纹登记顺序逐条比较，最终判定不受遍历顺序影响。

四、同一个 ID 重复灌入按已存在处理，返回 duplicate，样本总数不增加。

五、样本 ID 或内容为空报 ErrEmptySample。

六、桶内代表样本按三档选出：去重带数多的优先，带数相同取内容更长的一条，长度也相同取内容摘要字典序更小的一条，全程不依赖插入顺序；代表样本必须始终是桶内仍持有指纹的成员之一。

七、Buckets 与 Representative 只能看到仍持有指纹的样本；容量按桶内指纹条数计。

八、Evict 一次清理两类指纹：早于 now 减去 TTL 的过期指纹，以及超过 MaxPerBucket 后最旧的指纹，按入库时间升序淘汰，返回本次删掉的指纹条数；时间相同按样本 ID 升序，同一批数据多次清理结果固定。

九、指纹被淘汰的样本仍然留在样本库，Lookup 依然能按 ID 取到，Count 也不减少。

十、Snapshot 与 Restore 要同时保住样本库与指纹索引，恢复后同一批输入再次 Add 得到的判定与快照前一致；样本库与指纹索引任一边不完整都按损坏处理，报 ErrBadSnapshot 且不动现网。

十一、Add、Evict、Snapshot 之间要串行化，并发灌入时同一内容只能有一条被判为 new，不能出现两条重复入库。

十二、索引规模受 MaxPerBucket 与 TTL 双重约束，长期灌入后指纹条数不会随样本数线性膨胀；过期判定以传入的 now 为准，不读系统时钟。

验收口径：go test ./... 15 条必须全过；不得修改测试、跳过用例或按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-e-12`
- 实现文件：`corpus_deduper.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`CorpusDeduper`
