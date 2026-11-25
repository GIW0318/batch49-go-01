# step-up 认证方式阶梯与降级治理

## 题面

风控发现一类绕过：本该强制安全密钥的敏感操作，用户点了两次失败后系统自动改发短信验证码，短信一通过就放行；还有人把安全密钥拔掉再点重试，页面直接从平台认证器跳到短信；客服核查时发现工单里只记着验证失败，看不出到底是方式不可用还是凭证不对。

落点在 gsb/batch49-go-safety-a-09 的 step_up_method_ladder.go，类型 StepUpMethodLadder，负责敏感操作二次认证的方式选择与降级控制。需要保持的公开签名：NewStepUpMethodLadder(Options) *StepUpMethodLadder、Enroll(userID, method string) error、Revoke(userID, method string) error、Disable(userID, method, reason string) error、Enable(userID, method string) error、Available(userID string) []Method、Begin(userID, operation string) (Challenge, error)、Submit(challengeID, method string, proof Proof) (Result, error)、Escalate(challengeID string) (Challenge, error)、Abandon(challengeID string) error、Reuse(userID, operation string) (Result, bool)、LookupChallenge(challengeID string) (Challenge, bool)、Snapshot() ([]byte, error)、Restore([]byte) error。

需要做到：
一、方式按强度排成阶梯：安全密钥 5、平台认证器 4、TOTP 3、推送 2、短信 1；Available 只列已登记且未被停用的方式，并按强度降序返回。
二、重复登记报 ErrMethodEnrolled，未知方式报 ErrUnknownMethod，账号为空报 ErrBlankID；撤销、停用、启用未登记方式都报 ErrMethodNotEnrolled。
三、停用必须给出原因，原因为空报 ErrBadReason；停用只影响可用性，不删除登记；撤销才删除登记。
四、Begin 选当前最强可用方式；账号或操作名为空报 ErrBlankID，一条可用方式都没有报 ErrNoMethod；单账号并发挑战数设上限，超出报 ErrChallengeLimit。
五、只有高一级方式当前不可用才允许降到下一级，每一级跳过都要在挑战上留一条降级记录，写明被跳过的方式、落到的方式与原因，降级原因不能与验证失败混在一起。
六、证明为空报 ErrBadProof 并累加尝试次数，次数按挑战累计、要能在挑战记录上读到；提交的方式不是挑战当前方式报 ErrMethodNotAllowed 且不计尝试次数；未知挑战报 ErrChallengeNotFound，已关闭报 ErrChallengeClosed，过期报 ErrChallengeExpired。
七、验证失败达上限只能关闭本次挑战并报 ErrAttemptLimit，不得自动降到更弱的方式；关闭后的挑战再提交报 ErrChallengeClosed。
八、Escalate 只能往更强的方式走，目标不可用报 ErrNoStrongerMethod，已到顶再升级同样报 ErrNoStrongerMethod；换方式后尝试次数从零计。
九、验证通过要落一条带方式强度的结果，结果在有效期内可按账号与操作复用；过了有效期、换了操作或换了账号都不再复用，弱方式的后续尝试也不得把已完成的强度改低。
十、Abandon 关闭开放挑战并释放一个并发额度，重复撤销报 ErrChallengeClosed，未知挑战报 ErrChallengeNotFound。
十一、快照要带登记、停用标记、挑战、尝试次数、结果与原始到期时间，重启后挑战和结果按原时间失效；损坏快照报 ErrBadSnapshot 且现网状态不变；快照与写入互斥，读到的是完整旧状态或完整新状态。
十二、同一挑战的并发提交只能产生一条验证通过的结果，其余提交返回关闭错误。

验收：go test ./... 15 条必须全过，不得修改测试、跳过或按固定输入写死；并发与重启场景自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-a-09`
- 实现文件：`step_up_method_ladder.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`StepUpMethodLadder`
