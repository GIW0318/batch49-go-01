# build_version_attributor

模块路径：`gsb/batch49-go-safety-e-06`
包名：`task`
实现文件：`build_version_attributor.go`
测试目录：`tests/`

## 题面

<!-- PROMPT-START -->
版本归属最近连续出错：同一批夜间构建物里，有的被贴到补丁发布之后的版本上，有的被归到早已下线的旧版本，值班同学拿到的受影响清单要么漏报要么误报；换了两台构建机、时区设置不一致之后，结果还会互相打架。请修复 gsb/batch49-go-safety-e-06 下的 build_version_attributor.go，把 BuildVersionAttributor 的定位与区间判定补完整。

关键接口：
NewBuildVersionAttributor() *BuildVersionAttributor
RegisterRelease(component, version string, at time.Time) error
BindCommit(component, version, commit string) error
Attribute(b BuildRef) (Attribution, error)
Affected(b BuildRef, ranges []Range) (Verdict, error)
Timeline(component string) []Release
Latest(component string) (Release, bool)
Components() []string
Lookup(id string) (Attribution, bool)
Explain(id string) []Step

需要落实的行为：
1. 组件名或版本号为空（含全是空白）一律拒绝；同一组件重复登记同一版本要报错，首次登记的保持不变；判定时未登记组件返回未知组件。
2. 登记顺序不等于发布时间顺序，Timeline 必须按发布时间升序返回；同一时刻登记的多条按登记序号升序排列。
3. 版本号按点分数字段逐段做数值比较；带短横线后缀的算预发布，同一数字版本下预发布小于正式版。同一时刻同时出现预发布标签和正式标签时，只用正式标签定位，但预发布版本仍要参与受影响区间端点比较。
4. 构建时钟可信时，取发布时间不晚于构建时间的最近一条发布作为归属，置信度九十分，依据记为 time。
5. 构建早于组件首次发布时标记为时间线之前，版本留空、置信度零；晚于最新发布时标记为之后，归到最新版本、置信度六十。
6. 构建自带显式版本声明时以声明为准，置信度一百分；但如果按构建时间定位到的版本与声明不一致，必须返回时间线冲突错误，不能悄悄二选一。声明版本没有登记过要报未知版本，不能回退到按时间猜。
7. 时钟不可信或来自无法核对的时区时，必须改用提交标识定位，并在结论里保留 commit 依据；提交标识没有绑定过、或同一提交被绑定到两个版本，都要报错。
8. 受影响区间下界包含、上界不包含，上界留空表示一直受影响；命中记 in_range、未命中记 outside_ranges；端点为空串或写法非法要返回区间错误，不能当成开放区间。
9. 完全无法定位版本的构建按保守口径判定为受影响，置信度十分，并保留它落在时间线之外的依据。
10. 同一构建标识重复判定必须返回完全一致的结果；Explain 依次给出 basis、version、boundary 三步，未知标识返回空。
11. 补发的旧版本只允许影响此后新构建的定位，已经判定过的构建归属不能被改写。
12. 多个 goroutine 同时登记和判定时，序号不能重复、时间线不能丢条目，结论不能串。

验收：go test ./... 的 15 条必须全过，不许改测试、跳过或按固定输入写死；并发与重启后的重复调用自行验证；只做本地实现。
<!-- PROMPT-END -->

## 运行

```bash
go test ./...
```
