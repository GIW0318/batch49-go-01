# vex_state_propagator

模块路径：`gsb/batch49-go-safety-e-07`
包名：`task`
实现文件：`vex_state_propagator.go`
测试目录：`tests/`

## 题面

<!-- PROMPT-START -->
上周上游发布了一条 VEX 结论又撤回，下游三个业务线给出的状态却完全不同：有的还按旧结论把组件标成受影响，有的把撤回当成删除，确认过的待办直接消失；两家消费者自己做过分析，结果也被上游结论硬盖掉，审计时说不清依据。值班表上同一条 CVE 在三个清单里挂在不同版本上，客户侧扫描器与我们的结论对不上，回归会议每次都要人工翻发布记录。更麻烦的是审计口径：上游说改过哪几条、谁确认到哪一天、撤回发生在确认之前还是之后，现有记录都对不上，出了事故无法证明当时看到的是哪一版结论。请修复 gsb/batch49-go-safety-e-07 下的 vex_state_propagator.go，把 VexStatePropagator 的传播、定序与确认水位补完整。

关键接口：
NewVexStatePropagator() *VexStatePropagator
RegisterConsumer(consumer string) error
RegisterComponent(component, version string) error
Publish(st Statement, at time.Time) error
StateFor(consumer, component, cve string) (State, error)
Consumers(component, cve string) []string
Ack(consumer string, upto time.Time) error
Retract(st Statement, at time.Time) error
Pending(consumer string) int
History(consumer, component, cve string) []Record
SubmitLocal(f LocalFinding, at time.Time) error

需要落实的行为：
1. 上游、组件、CVE、状态为空或全空白一律拒绝；状态只接受 affected、not_affected、under_investigation；修订号小于一拒绝；重复登记同一消费者保持幂等；判定时未登记的组件或没有结论的键分别报未知组件、未知结论。
2. 同一组件同一 CVE 上，修订号大的结论优先，修订号相同则声明时间晚的优先；同一上游对同一键给出互相矛盾的结论时，以修订号更大者为准，并把冲突留在历史里备查。
3. 结论只在受影响版本区间内生效：当前组件版本低于下界或不低于上界时，对外状态为 not_affected，依据记 outside_range，但上游原始状态仍要能看到。
4. 撤回是一条优先级更高的结论，不是删除；撤回后对外状态为 retracted，依据记 retracted，旧结论仍留在历史里并标记失效。
5. 撤回要幂等，重复撤回不得追加第二条撤回记录。
6. 消费者确认按水位推进，只能前进不能后退；水位之前的结论不再计入待办，水位之后的新结论持续计入。
7. 同一键有多条待确认结论时只算一条待办，不能按修订条数膨胀。
8. 上游结论带过期时间，超过过期时间后对外状态回落到 under_investigation，依据记 expired。
9. 消费者自己做过本地分析时，上游结论只作参考、不强制覆盖；State 要同时保留上游与本地两个来源，并给出裁决依据，不能只留一个合并后的状态。
10. 已经确认过的旧结论被撤回后要标记失效但仍计入历史，不能从待办里直接消失。
11. Consumers 返回按字典序排列的已登记消费者；History 按声明时间升序，同一时刻按修订号升序。
12. 多个 goroutine 同时发布时，修订定序不能丢结论，待办数量必须收敛到一条。

验收：go test ./... 的 15 条必须全过，不许改测试、跳过或按固定输入写死；版本区间外、过期、重复撤回这些边界自行覆盖；并发与重复调用自行验证；只做本地实现。
<!-- PROMPT-END -->

## 运行

```bash
go test ./...
```
