# batch49-go-fix-a-14

模块路径：`gsb/batch49-go-fix-a-14`

包名：`task`

源文件：`seccomp_escape_guard.go`

测试目录：`tests/`

## 题面

容器平台的巡检表上，一批业务镜像的 syscall 放行清单已经三年没动过：运行时真正用到的调用不到清单的三成，剩下七成全是从模板抄来的历史遗留，其中还包括 ptrace、bpf 和 unshare。安全组既担心清单太宽，又担心一刀切下去把业务打死，要求先按真实调用收窄，再在运行期堵住借道逃逸的尝试。改动落在 gsb/batch49-go-fix-a-14 的 seccomp_escape_guard.go，类型 SeccompEscapeGuard。关键签名保持：NewSeccompEscapeGuard(limits Limits) *SeccompEscapeGuard、Enroll(spec WorkloadSpec) error、Observe(workloadID string, c Call) error、Propose(workloadID string) (Draft, error)、Promote(workloadID string) (Profile, error)、Check(workloadID string, c Call) (Decision, error)、Release(workloadID string) error、Snapshot() ([]byte, error)、Restore([]byte) error。

需要盯住的行为：
1、登记要求 ID 非空、允许清单非空、规则名不重复，规则名为空报 ErrInvalidCall；重复容器报 ErrDuplicateWorkload，超上限报 ErrLimitExceeded。
2、容器初始为 audit 状态，规则带可选的 Arg0Mask 与 Arg0Value 参数约束，mask 非零时 Arg0 必须逐位匹配。
3、观察窗口只接受 audit 状态的容器，空调用名报 ErrInvalidCall；同一调用名按首次出现聚合成一条观察并累计次数，超过窗口容量报 ErrLimitExceeded。
4、提议只能在 audit 状态做，没有观察时报 ErrNoObservation；结果只保留当前画像里出现过的规则，未观察到的规则进入 Removed 并按名字升序；草稿代次为当前画像代次加一，摘要非空且重复提议保持一致。
5、收窄只许变窄：提议里的每条规则都必须能在当前画像中找到同样的名字与参数约束，绝不能因为观察到了清单外的调用就把新规则加进去；已生效的参数豁免只能保留或收紧。
6、提升要求存在待生效草稿，没有草稿报 ErrNoDraft；提升后容器进入 enforced，画像代次加一，再提议或再观察都报 ErrNotAuditing。
7、audit 状态下 Check 一律放行并给出 audit_mode，不计 strike；enforced 状态下命中规则放行，未命中返回 deny 并累加 strike。
8、strike 达到上限时返回 kill 并转入 quarantined；bpf、ptrace、unshare、setns、mount、openat2、io_uring_setup 这类高危调用一旦未命中就立即 kill，原因为 escape_dangerous_syscall。
9、隔离中的容器 Check 一律 deny，不再累加 strike；手工隔离要求给出非空原因，空原因报 ErrReasonRequired，重复隔离报 ErrAlreadyQuarantined。
10、释放只能针对隔离中的容器，否则报 ErrNotQuarantined；释放后回到 audit 并把 strike 归零、打开新的观察窗口；释放后的画像保留已经收窄的版本，不得回退到登记时的全量清单。
11、除 exit_group 与 rt_sigreturn 这类进程退出所必需的调用外，未被观察到的规则都要移除；基线必留调用即使窗口内一次都没出现也不能丢。
12、Snapshot 覆盖容器状态、画像、观察窗口、strike 与拦截记录，Restore 整份替换，损坏快照报 ErrBadSnapshot 且不动现网。

硬约束：同一容器的 Observe 要串行累计，几十个并发上报后的计数与首次出现顺序必须准确；Check 与 Promote 并发时只能看到提升前或提升后的完整画像，不能读到半张表。

验收以 go test ./... 的 15 条用例全绿为准，测试不许改、不许跳过、不许按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行

```bash
go test ./...
```
