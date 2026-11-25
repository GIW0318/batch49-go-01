# fuzz 语料库治理：覆盖增量入库、内容去重与贡献度淘汰

## 题面

线上 fuzz 集群的语料目录三个月涨到 40G，/data 分区被写满，新任务全部因为磁盘不足退出；值班同事手工删了一批用例，结果上周刚覆盖到的解析分支又退回未覆盖，同一个输入还被不同任务的脚本反复塞回来，来源记录全乱了。落点 gsb/batch49-go-safety-c-06 的 fuzz_corpus_vault.go，类型 CorpusVault，需要把语料入库、覆盖判定、去重、淘汰和崩溃用例管理做成一套可解释的规则。接口保持：NewCorpusVault(Limits, Reducer) *CorpusVault、Add(input []byte, cov CoverSet, origin string) (Verdict, error)、Minimize(id string) ([]byte, error)、Promote(id string) error、MarkCrash(id string) error、ConfirmCrash(id string) error、Reap() []string、Get(id string) (Entry, bool)、Entries() []Entry、Stats() VaultStats、Snapshot() ([]byte, error)、Restore([]byte) error。

要修：一、入库只认新增覆盖，覆盖集用哈希加排序后的覆盖格表示，只有没见过的格才算新增，同一批里的重复格不重复计数。二、按内容哈希去重，相同内容不新建条目，但要把新来源追加到该条目并累加来源计数，去重优先于覆盖判定。三、内容不同却没有带来未见覆盖的，判为冗余拒收；覆盖集为空的输入不入库，但必须计入无效计数，便于观察冗余率。四、条目编号形如 corpus-0001，按入库顺序递增，序号和快照一起持久化，恢复后不得回退。五、字节配额按全部存活条目累计；超额时用新增覆盖数除以字节数比较贡献度，新条目弱于现有可淘汰条目就拒收并给出配额原因，强于则淘汰最弱的一条。六、贡献度并列时先淘汰较旧的条目；被提升的条目不参与淘汰，重复提升必须幂等。七、崩溃用例单独标记，来源引用保留到人工确认之后才允许进入普通流程，未确认前不得因为配额被淘汰。八、最小化必须仍能复现同一覆盖集合：用注入的 Reducer 逐字节裁掉尾部，返回最短的可复现前缀；如果一个前缀都复现不了，就退回保留原输入。九、Get 与 Entries 返回副本，Entries 按入库序号升序；统计块给出条目数、总字节、无效、重复、冗余、淘汰与崩溃计数。十、快照包含条目、来源、已见覆盖水位与全部计数，恢复要么整体生效要么完全不生效；损坏载荷报 ErrBadSnapshot 且不得动现有数据，恢复后新条目序号必须大于此前任意条目。十一、并发入库串行裁决，条目数上限只放行配置条数，其余以配额原因拒收，不得出现重复编号。

硬约束：Add、Reap 与快照互斥，快照只能看到全量旧状态或全量新状态；条目上限、字节配额和覆盖水位跨重启恢复；并发下不得出现数据竞争，也不得让两个条目抢到同一个编号。

验收口径：go test ./... 15 条全过，测试不得修改、跳过或按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-c-06`
- 实现文件：`fuzz_corpus_vault.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`CorpusVault`
