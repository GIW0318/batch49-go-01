# batch49-go-safety-a-11

- 模块路径：`gsb/batch49-go-safety-a-11`
- 包名：`task`
- 主文件：`service_account_key_meter.go`
- 验收用例：`tests/test_service_account_key_meter_test.go`

## 运行方式

在包根执行：

```bash
go test ./...
```

## 题面

客服工单反馈：上周，一个服务账号密钥连续调用了它从未申请过的计费接口，值班第二天早上从账单里发现；另一个账号在密钥轮换后，旧密钥还靠先前取到的允许结论继续放行，月度调用额度被超发，财务对账出现缺口。逻辑位于模块 gsb/batch49-go-safety-a-11，主文件 service_account_key_meter.go，入口类型 ServiceAccountKeyMeter，请把计量、熔断与恢复行为补齐到可交付状态。

密钥有生效、暂停、吊销三种状态。注册时给出密钥号、所属账号和允许的目标列表；目标要去掉首尾空白、去重并按字典序保存，数量超过上限直接拒绝。每次调用都要同时计入密钥级和账号级两层滑动窗口额度，任何一层超限都必须拒绝本次调用并写明原因；计数只能以原子方式累加，并发下不能突破额度。窗口以最近一次记录的时间为水位，区间左开右闭。

访问不在允许列表里的目标时，必须立刻把密钥置为暂停并写入审计，后续调用一律拒绝；暂停必须给原因，恢复必须给审批人，已吊销的密钥不能恢复、不能暂停，重复吊销按幂等处理。已经签发但尚未提交的判定，在密钥暂停生效后不得继续放行，在途请求必须重新校验。判定在签发时带出窗口末尾的到期时刻，只有提交才计入用量且只能提交一次；过期、重复提交、编号不存在都要分别报错。

需要落到的行为：
1. 注册校验空密钥号、空账号、空目标列表、超过单密钥目标上限、重复注册，注册本身不产生调用审计。
2. 未注册或已吊销的密钥在任何操作下都返回对应错误。
3. 允许目标的调用要把放行数、拒绝数和事件序号一起推进。
4. 未授权目标触发即时暂停，状态和原因可在密钥视图里读到。
5. 暂停和恢复的前置条件、幂等边界按上面描述执行，暂停与恢复各留一条审计。
6. 密钥级额度用尽后拒绝，拒绝计数与拒绝审计同步增加；暂停状态下被拒绝的调用同样记一条拒绝审计。
7. 账号级额度用尽时，即使该密钥自己的额度还有剩余，也必须拒绝。
8. 账号额度视图给出已用量、上限、窗口和是否耗尽，已用量达到上限即标记耗尽。
9. 审计事件按账号保留最近若干条，序号全局单调递增。
10. 快照保存密钥状态、窗口用量、判定与事件；恢复后序号继续递增。
11. 重启后已用额度按保守口径恢复，窗口内未落盘的部分不能当作零。
12. 快照损坏时整次恢复失败，现有状态保持可用。

关键接口：
- NewServiceAccountKeyMeter(limits Limits) *ServiceAccountKeyMeter
- (m *ServiceAccountKeyMeter) RegisterKey(keyID, accountID string, targets []string) error
- (m *ServiceAccountKeyMeter) Record(keyID, target string, at time.Time) error
- (m *ServiceAccountKeyMeter) Usage(keyID string, window time.Duration) (UsageStat, error)
- (m *ServiceAccountKeyMeter) Decide(keyID, target string, at time.Time) (Decision, error)
- (m *ServiceAccountKeyMeter) Commit(decisionID string, at time.Time) error
- (m *ServiceAccountKeyMeter) Suspend(keyID, reason string) error
- (m *ServiceAccountKeyMeter) Resume(keyID, approverID string) error
- (m *ServiceAccountKeyMeter) Revoke(keyID, reason string) error
- (m *ServiceAccountKeyMeter) KeyState(keyID string) (KeyStat, error)
- (m *ServiceAccountKeyMeter) Quota(accountID string) (QuotaState, error)
- (m *ServiceAccountKeyMeter) Events(accountID string) []AuditEvent
- (m *ServiceAccountKeyMeter) Snapshot() ([]byte, error)
- (m *ServiceAccountKeyMeter) Restore(data []byte) error

硬约束：四十路并发压同一个密钥时，放行数必须恰好等于剩余额度；同一判定只能提交一次；跨重启恢复不得超发；所有上限取自构造时的 Limits。验收口径是在包根执行 go test ./... 全部通过，不得修改或跳过用例，不得针对固定输入写死结果，并发与重启场景自行验证，只使用标准库并在本地完成。
