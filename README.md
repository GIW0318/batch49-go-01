# 制品签名验证门禁：多签名者阈值、按时间点吊销与日志包含证明

## 题面

上周制品发布门禁放行了一个本该拦下的构建：三个签名只凑出两个不同签名者，其中一把密钥月初已经吊销，透明日志里查不到包含证明，值班同事全靠截图和口头确认，季度审计要求每个放行决定都能当场复算。落点 gsb/batch49-go-safety-c-08 的 artifact_sig_verifier.go，类型 SigVerifier，需要把多签名者阈值、密钥有效期、吊销时间点与日志包含证明统一成可复算的裁决。接口保持：NewSigVerifier() *SigVerifier、RegisterKey(k Key) error、SetPolicy(p Policy) error、Policy() Policy、Verify(d Digest, sigs []Signature) (Verdict, error)、RevokeKey(keyID string, at time.Time) error、Revocations() []Revocation、Revoked(keyID string) (time.Time, bool)、Keys() []Key、Rounds() int、Snapshot() ([]byte, error)、Restore([]byte) error。

要修：一、摘要只接受 sha256 与 64 位小写十六进制，其余一律报 ErrBadDigest。二、未设置策略时 Verify 报 ErrNoPolicy；阈值小于一、阈值大于授权签名者数、签名者重复、信任根为空都报 ErrBadPolicy。三、阈值按不同签名者计，同一签名者登记多把密钥只算一票，重复投票要留下原因码但不得抬高票数，也不得因为签名先后改变计数。四、签名者必须出现在策略授权名单里，未授权只记原因码、不参与计数；签名者以注册密钥记录为准。五、密钥未注册的签名不计数；密钥在签名时刻必须落在生效与失效之间，越界不计数，注册表里没有的密钥 ID 只记原因码。六、证书链必须回落到策略里的固定信任根，链尾不等于信任根即不可信。七、吊销按时间点生效，早于吊销时刻签发的签名仍然有效，同一把密钥重复吊销幂等并保留最早时刻，零时刻吊销要拒收。八、吊销之后签发的签名一律无效，即使该密钥在吊销前已注册。九、签名时刻取声明时间与日志时间中较早者，日志时间未填时以声明时间为准；声明时间晚于日志时间的视为可疑，不计入阈值，日志序号必须为正。十、策略要求透明日志时，签名必须带包含证明与日志序号；只有日志签名没有包含证明的按策略降级，不得默认通过。十一、Verdict 返回是否通过、已计票数、所需票数、原因码与按签名者名升序的名单，原因码允许重复出现，Rounds 每裁决一次加一。十二、快照保存密钥、策略、吊销记录与计数，恢复后轮次要接着涨；Restore 整体替换，损坏载荷报 ErrBadSnapshot 且不动现有状态。

硬约束：并发 Verify 只能看到完整的一份策略与吊销表，不得出现半新半旧；吊销、策略与计数跨重启恢复；同一份输入重复裁决的结果必须一致，不得因为并发顺序改变票数。

验收口径：go test ./... 15 条全过，测试不得修改、跳过或按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-c-08`
- 实现文件：`artifact_sig_verifier.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`SigVerifier`
