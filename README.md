# 利用验证执行编排的合并、三态判定与一次性环境

## 题面

红队平台把候选漏洞送进一次性环境做利用验证，最近几份报表被质疑得厉害：三种完全不同的运行结论被压成同一档，超时的、环境起不来的、真正打不穿的都记成“未复现”，后续处置策略跟着全走错。同一个漏洞在十分钟里被四个扫描器同时提交，平台老老实实开了四套环境，配额一天就用光，账单是四倍的资源。还有一次环境销毁失败，残留的容器第二天被下一次验证复用，前一次的网络痕迹混进了新结论，评审时谁也说不清那条命中到底属于哪一次执行。落点 gsb/batch49-go-safety-c-05 的 exploit_verify_runner.go，类型 ExploitVerifier，签名保持：NewExploitVerifier(limits Limits) *ExploitVerifier、Submit(vulnID string, spec VerifySpec) (RunID, error)、Poll(runID string) (RunState, error)、Wait(runID string, timeoutMillis int) (RunState, error)、Report(runID string, ev Evidence) error、Abort(runID, reason string) error、Runs() []string、Inflight() int、Snapshot() ([]byte, error)、Restore(data []byte) error。

要修：Submit 校验漏洞编号与规格的必填项，空编号报 ErrBlankVuln，镜像、命令、目标为空或三项预算非正报 ErrBadSpec。同一漏洞、同一规格的提交必须合并为一次执行，返回同一个运行编号，合并计数从 1 起递增，等待者共享同一份结论；规格指纹包含镜像、命令、目标与三项预算，任一不同就是独立执行。在途运行数受上限约束，上限为 0 表示不限制，超限报 ErrTooManyRuns，但同规格合并请求不受上限约束。运行相位取 running 与 done，结论只有已验证、未复现、不可判定三态，原因取 hit、no_hit、inconclusive、aborted、budget_exceeded、destroy_failed：证据命中判已验证，显式声明不可判定判不可判定，其余判未复现，三者在报表与后续处置上不可混用。取证超出输出字节或 CPU 预算时报 ErrBudgetExceeded 并结束该 run，不得留下可继续追加证据的运行。运行进入终态后再上报或中止报 ErrRunClosed，未知运行编号报 ErrUnknownRun。Abort 必须带原因，空原因报 ErrBadReason，中止的运行按不可判定收尾并记录原因。Wait 按调用方自己的超时等待，运行已结束立即返回终态，仍在跑且等到超时报 ErrWaitTimeout，超时为负报 ErrBadTimeout。三个容易踩的边界：执行超时属于不可判定而不是未复现，两者在报表与后续处置上不可混用；合并之后的各等待者超时时间独立计算，先超时者拿不到结果，但不得因此取消共享执行或影响其他等待者；环境销毁失败时该运行永久标记为不可判定并禁止同一规格重新提交，防止残留环境被复用。快照要保留运行表、合并计数与在途上限，恢复后轮询、上报与合并都接着原状态走；损坏的快照报 ErrBadSnapshot 且不得改动现网。

硬约束：并发提交必须原子裁决，同一规格只允许建立一次执行，不得出现两个运行编号；快照要么全量旧状态要么全量新状态；单次执行的 CPU、时长与输出字节预算受硬顶，超限即结束，不得继续占用环境。

验收：go test ./... 15 条全过，不得改测试、跳过、按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-c-05`
- 实现文件：`exploit_verify_runner.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`ExploitVerifier`
