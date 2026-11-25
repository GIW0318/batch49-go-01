# rollback_drill_scheduler

模块路径：`gsb/batch49-go-safety-e-09`
包名：`task`
实现文件：`rollback_drill_scheduler.go`
测试目录：`tests/`

## 题面

<!-- PROMPT-START -->
季度回滚演练报告被打回：抽样名单里有三台主机早就换到别的补丁批次，演练却仍记成通过；沙箱模拟跑超时的两次也被算进了通过率；同一个批次在两种镜像上的结果被合并成一条，覆盖率看起来漂亮，真出事时找不到可复现的输入和差异。值班同学按报告安排下一轮演练，结果同一批失败样本被反复重抽，缺口却一直没补上；补丁窗口只有几个小时，演练结论必须能直接回答哪台机器、哪个步骤、为什么没过。演练结论落台账要幂等，同一批次重复演练不能写两条。

请修复 gsb/batch49-go-safety-e-09 下的 rollback_drill_scheduler.go，把 RollbackDrillScheduler 的抽样、模拟比对与台账补完整。

关键接口：
NewRollbackDrillScheduler(opts Options) *RollbackDrillScheduler
Enroll(host string, fleet string) error
RegisterBatch(spec BatchSpec) error
Assign(host string, batch string, image string) error
Sample(fleet string, n int) ([]string, error)
Drill(host string, batch string) (DrillResult, error)
Coverage(fleet string) (Coverage, error)
Failures(fleet string) ([]DrillFailure, error)
RecordSimulation(host string, batch string, steps []string, elapsed time.Duration) error
Hosts(fleet string) []Host

需要落实的行为：
1. 主机、舰队、批次、步骤、镜像为空要拒绝；主机重复登记、批次重复登记要报错。
2. 抽样按批次与镜像分层，每个桶轮流取一台；一次调用内不重复，同一主机在一个周期内只抽中一次，全部抽完才开新周期。分层桶按批次号与镜像名排序，保证同样的名单每次抽样顺序一致。
3. 样本规模必须为正；舰队没有登记主机要返回 unknown fleet。
4. 主机分配到批次时必须校验镜像属于该批次；未知主机、未知批次、未登记镜像分别报错。
5. 模拟执行只写沙箱记录，不得改动主机真实批次与镜像状态；未知主机或未知批次拒绝登记。
6. 演练前必须有该主机该批次的模拟记录，没有就报 no simulation，不能默认通过。
7. 步骤比对区分缺失、顺序不符、结果不符三类；少步骤逐条记 missing，顺序颠倒记 order，内容不同记 result。差异要保留期望步骤与实际步骤，便于原样复跑。
8. 模拟耗时超过预算按未完成计，不能把超时当作通过，也不能吞掉已记录差异。
9. 样本主机已不在目标批次上时演练结论无效，要重新抽样，不能记通过。
10. 同一批次在不同镜像版本上的结果分别记账，不能合并成一条覆盖率结论。
11. 覆盖率返回总主机数、已演练主机数、未覆盖批次与缺口数；缺口按批次与镜像的组合计数，不能只看批次。失败清单按主机排序并带原因类别。
12. 同一主机同一批次重复演练以最后一次结果为准，旧失败不能继续挂在失败清单里。

验收：go test ./... 的 15 条必须全过，不许改测试、跳过或按固定输入写死；抽样周期、超时与跨镜像台账自行验证；同一输入重复演练结果要稳定；只做本地实现。
<!-- PROMPT-END -->

## 运行

```bash
go test ./...
```
