# WebAuthn 注册的认证器证明策略

## 题面

上个月一次租户级策略放开，注册台把一批没有证明链的钥匙当高强度收了进来，随后又因为名单写反，把银行采购的安全钥匙全挡在门外。翻日志能看到同一天里两种相反的判定，同型号认证器在不同租户下拿到完全不同的档位，事后谁也说不清依据。运营要求把 WebAuthn 注册入口的认证器证明策略做出来。改动落在 gsb/batch49-go-safety-a-02 的 authenticator_attestation_policy.go，类型 AuthenticatorAttestationPolicy。关键签名保持：NewAuthenticatorAttestationPolicy(limits Limits) *AuthenticatorAttestationPolicy、SetPolicy(tenantID string, p Policy) (Version, error)、Policy(tenantID string) (Policy, error)、Versions(tenantID string) []Version、Evaluate(tenantID string, reg Registration) (Decision, error)、Decision(decisionID string) (Decision, bool)、Explain(decisionID string) []Reason、Reevaluate(tenantID string) (int, error)、Snapshot() ([]byte, error)、Restore([]byte) error。

要修的点：决策与解释里的原因码统一用包内的 Reason 常量（ReasonAllowlisted、ReasonDenylisted、ReasonNotAllowlisted、ReasonConveyanceNone、ReasonUntrustedChain、ReasonAttestationRequired 等）。一、租户号为空、证明传递方式不是 none/indirect/enterprise、强度档不是 low/medium/high，都要在 SetPolicy 当场拒绝，分别报 ErrBlankTenant、ErrBadConveyance、ErrBadStrength，不得留下半份策略。二、允许名单加拒绝名单的条目数超过 MaxEntriesPerList 报 ErrAAGUIDLimit，信任锚数超过 MaxTrustAnchors 报 ErrTrustAnchorLimit，被拒的写入不得覆盖现有策略。三、同一租户的策略版本数受 MaxVersions 限制，超出报 ErrPolicyLimit，旧版本与当前策略都保持可用。四、每次成功 SetPolicy 生成新的版本号，Version 要带出版本号与名单、锚的条目数，Versions 按从旧到新列出，Policy 返回当前生效的那份。五、Evaluate 遇到未配置策略的租户报 ErrUnknownTenant，Policy 对未配置的租户同样报 ErrUnknownTenant，注册 ID 只有空白报 ErrBlankRegistration，这几种情况都不生成决策。六、传递方式为 none 的注册只能落到低强度档，不得因为其他字段好看就抬到中或高，用户验证通过也不改变这条上限。七、策略要求证明而注册未带证明时报错拒绝，并在决策原因里给出证明缺失码。八、允许名单非空时，AAGUID 不在名单里的注册一律拒绝并给出未列入码，被拒的注册不得进入台账。九、AAGUID 同时出现在允许与拒绝名单时以拒绝为准，不按名单顺序或后写覆盖放行。十、证明链回溯不到配置的信任锚时按未证明处理，只能发低强度档，除非策略明确要求拒绝；链尾落在信任锚上的企业级注册按高强度放行，低于 MinStrength 的不得放行。十一、缓存的评估结论必须随策略版本失效，Reevaluate 返回被重新评估的凭据条数；策略版本变更后的存量重评估只降不升，不能因为新策略更宽松就把既有凭据自动提档。十二、每次 Evaluate 生成新的决策 ID，Explain 按决策 ID 返回与决策码一一对应的原因明细，每条明细带非空说明，查无此决策返回空，决策序号在租户内单调递增；损坏快照 Restore 报 ErrBadSnapshot 且不动现网。

硬约束：并发 SetPolicy 必须串行发号，版本号不得重复、不得跳号，最后一次生效的策略与版本历史一致；Snapshot 要覆盖策略、版本历史、决策与其原因，Restore 整份替换，恢复后决策仍可按 ID 解释，缓存不得跨版本命中。验收以 go test ./... 的 15 条用例全绿为准，测试不许改、不许跳过、不许按固定输入写死；并发与重启恢复自行验证；只做本地实现，交付源码、跑测记录和一页以内说明。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-a-02`
- 实现文件：`authenticator_attestation_policy.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`AuthenticatorAttestationPolicy`
