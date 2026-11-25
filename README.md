# exploit_chain_validator

模块路径：`gsb/batch49-go-safety-e-10`
包名：`task`
实现文件：`exploit_chain_validator.go`
测试目录：`tests/`

## 题面

<!-- PROMPT-START -->
红队复盘时发现攻击链验证器把两条不同的路径判成了同一条：先走消耗大的等价边，后面再想换路已经没预算；目标能力本来就在初始状态里，工具却报不可达；带否定前置的边在拿到某项能力后没有失效，反而被绕过去，报告里出现了不存在的攻击路径。评审会上没人能解释为什么同一份能力图两次跑出的链不一样，也没人能说清搜索到底穷尽了多少节点。更麻烦的是，验证器给出的链无法复现：同一张图在一次评审里被判定可达，换台机器重跑又说不可达，报告里的节点数前后矛盾。请修复 gsb/batch49-go-safety-e-10 下的 exploit_chain_validator.go，把 ExploitChainValidator 的搜索、预算与快照补完整。

关键接口：
NewExploitChainValidator(limits Limits) *ExploitChainValidator
AddEdge(e Edge) error
Reachable(from, goal State, budget Budget) (Chain, error)
Enumerate(from, goal State, limit int) ([]Chain, error)
Explain(chain Chain) []Step
SetBudget(b Budget) error
Snapshot() ([]byte, error)
Restore([]byte) error
Edges() []Edge
Stats() Stats
Truncated() bool

需要落实的行为：
1. 边标识为空或全空白拒绝；标识重复、消耗为负拒绝；边数超过硬顶返回边数超限。
2. 边可触发要求当前能力集合包含 From 与 Requires，且不含 Forbids 中任何一项；触发后把 To 与 Grants 合入当前集合，消耗累计扣减。
3. 一次求解中每条边最多用一次，状态回访不构成通路；成环不能绕过边只能使用一次的限制。
4. 两条边产出相同能力但消耗不同时，先走消耗大的可能堵死后续路径，必须按剩余预算回退搜索，不能贪心定死。
5. 目标能力已在初始状态中时返回空链而不是不可达。
6. 前置条件含否定项（要求某能力不存在）时，一旦该能力已被获得，这条边永久不可用，不能忽略否定项。
7. 预算包含总消耗、链长与搜索节点数三个硬顶；任一超顶都要停下并标注未穷尽，不能静默返回空结论；超顶时要保留已扩展节点数与截断标志。
8. 可达到时返回按边标识字典序最小的链；同一起点同一目标多次求解结果一致。
9. Enumerate 按字典序返回至多 limit 条链，limit 非正拒绝；节点顶到上限时返回已有部分并置 Truncated。
10. Explain 按链中边顺序给出序号、边标识、消耗与获得后的能力集合。
11. 快照要包含边集、硬顶与默认预算，恢复要么全成要么全不改；损坏负载返回快照错误且保持原状态，不能留下半套边集。
12. 多个请求同时读边集与快照时不能看到半截边集或串预算。

验收：go test ./... 的 15 条必须全过，不许改测试、跳过或按固定输入写死；预算回退、空链与否定前置自行验证；并发与重复求解自行验证；只做本地实现。
<!-- PROMPT-END -->

## 运行

```bash
go test ./...
```
