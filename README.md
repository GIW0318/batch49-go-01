# 组件坐标与漏洞记录的区间匹配台账

## 题面

漏洞台账每周同步第三方源和发行版公告，最近值班同学发现同一台机器被打上又撤掉同一结论：某个库刚过修复版本，台账却说它仍在受影响区间；另一个库一个月前回移过补丁，台账照样判成受影响，逼得大家手工核对补丁。冲突记录也折磨人：同一漏洞在供应商公告和公共库里给出的修复版本不一样，谁覆盖谁全看加载顺序，覆盖后被遮掉的记录完全消失，事后无从复盘“为何最后采信这一条”。接上 purl 限定符更乱：没有发行版信息的坐标套用了限定到发行版的记录，不是同一个发行版也判成受影响。落点 gsb/batch49-go-safety-c-02 的 cve_match_ledger.go，类型 CveMatchLedger，签名保持：NewCveMatchLedger(priority []string) *CveMatchLedger、Load(records []Record) error、Records() []string、Match(coord Coordinate) ([]Match, error)、Explain(cveID string, coord Coordinate) (Trace, error)、Override(cveID string, coord Coordinate, reason string) error、Withdraw(cveID string) error、Snapshot() ([]byte, error)、Restore(data []byte) error，另有包级函数 CompareVersions(ecosystem, a, b string) (int, error) 与 NormalizeCoordinate(coord Coordinate) (Coordinate, error)。

要修：版本比较按生态分派，语义化版本按三段数字逐段比较，带预发布后缀的低于同版本正式版，构建元数据忽略；deb 与 rpm 先比 epoch 再比上游版本与修订号，波浪号低于任何字符；不认识的生态或畸形版本报 ErrBadVersion。坐标归一化把供应商、包名、生态、发行版去空白转小写，包名或版本为空、生态不认识都报 ErrBadCoordinate。加载校验 CVE 编号、包名、生态、区间上下界与来源唯一，任一条不合格报 ErrBadRecord 且整批不落地，记录列表按编号升序返回。受影响区间左闭右开，恰好等于修复版本不算受影响，低于下界不产生判定；一条记录里有多个区间时任一命中即受影响。坐标按供应商、包名、生态与发行版匹配，不匹配的记录不参与。Match 只为受影响、人工覆盖与回移修复返回条目，版本越过修复上界或低于下界不返回，需要细节走 Explain。多来源冲突时按构造时的来源优先级取一条，优先级相同取更新时间较新的，被遮掉的记录留在解释结果里，解释同时记下采信的来源。Explain 按区间顺序给出上下界比较结果，状态取受影响、不受影响或未适用；版本越过修复上界时是未适用并写明已修复。覆盖按漏洞与归一化坐标精确匹配，原因不能为空，覆盖后匹配与解释都按不受影响返回人工覆盖，未知或未加载的漏洞报 ErrUnknownCVE。撤销从记录集移除，重复撤销报 ErrUnknownCVE。三个易漏边界：坐标同时落入受影响区间与不受影响区间、且不受影响那条是回移修复时，必须判为不受影响；通配上界不等于该包全部版本受影响，仍要尊重记录自带的版本上界与限定符；坐标没有发行版信息时不得套用带发行版限定的记录，只能匹配不带发行版限定的记录。

硬约束：匹配与加载必须互斥，任一次匹配只能看到完整的新旧记录集之一，不得看到半批记录；覆盖表与记录集一起落盘，恢复后判定、解释与撤销接着原状态走；损坏的快照报 ErrBadSnapshot 且不得改动现网状态。

验收：go test ./... 15 条全过，不得改测试、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-c-02`
- 实现文件：`cve_match_ledger.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`CveMatchLedger`
