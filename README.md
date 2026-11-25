# 会话级 DNS 钉扎与重绑定复核

## 题面

网关侧的 DNS 重绑定防护上线后，内网巡检工具连续报警：一个外部域名的 A 记录在会话中途换成了 169.254.169.254，网关照样放行，元数据接口被读走；另一批报警来自正常的双活域名，运营商把四条 A 记录收敛成一条，网关却判成重绑定把会话全掐了；运维还反馈 TTL 只有三十秒的域名把钉扎撕得很碎，每次续期都重新首解。值班同学翻日志时还发现，同一个会话的复核在并发请求下会互相覆盖，钉扎集时大时小，谁也说不清那一刻到底钉了什么。

改动落在模块 gsb/batch49-go-safety-b-02 的 dns_rebind_pin.go，类型 RebindPin 负责会话级地址钉扎与复核。核心签名保持：NewRebindPin(cfg PinConfig) *RebindPin、Resolve(sessionID, host string) (Pinned, error)、Verify(sessionID string, addrs []Addr) (Verdict, error)、Release(sessionID string)、PinnedFor(sessionID, host string) (Pinned, bool)、PinCount(sessionID string) int、Generations(sessionID, host string) int64、Classify(ip string) string、Sessions() []string、Snapshot() ([]byte, error)、Restore([]byte) error。

需要落地的行为如下。

一、首解时把解析器返回的地址集整份钉扎，记下首解时刻、生效 TTL、到期时刻与解析代数，代数从 1 起算。

二、空会话号与空域名分别报 ErrBlankSession 与 ErrBlankHost，两种情况都不得落下任何记录。

三、单会话钉扎条目数受配置上限约束，超出报 ErrPinLimit，不得淘汰已有条目；会话之间互不影响。

四、钉扎未到期时同域名再次解析直接复用原记录，不再查询解析器，代数与到期时刻都保持不变。

五、复核得到的地址集与钉扎集求交，交集为空判重绑定，报 ErrRebindDetected，并在结论里置起重绑定标志与对应原因码。

六、交集非空时只允许使用交集成员，结论里列出允许集；复核结论不得改写已经生效的钉扎记录。

七、环回、私网、链路本地与运营商级 NAT 地址一律拒绝，报 ErrBlockedAddr，并按地址族给出分类原因码；被拒绝的地址不得进入钉扎集，也不得出现在允许集里。

八、Classify 对同一批地址返回稳定分类，无法解析的字面量归为 addr_invalid，空串同样按无效处理；172.16 到 172.31 之外的同段地址不算私网。

九、单会话没有任何钉扎时复核报 ErrUnknownSession；空地址集或含无法解析地址的复核报 ErrBadAddress。

十、解析器失败报 ErrLookupFailed，返回空集报 ErrBadAddress，解析到被拒地址族报 ErrBlockedAddr，这三种情况都不得落下钉扎。

十一、Release 清空该会话的全部钉扎，重复释放是空操作，且不影响其它会话的记录。

十二、快照要覆盖钉扎集、首解时刻、到期时刻、生效 TTL 与代数；损坏或空快照报 ErrBadSnapshot，且不影响在线钉扎。

硬约束：同一会话的复核必须串行化，并发复核不得撕裂钉扎记录，也不得凭空新增条目；进程重启后钉扎集、代数与到期时刻要原样恢复，恢复时仍在有效期内的域名不得重新首解，恢复出来的钉扎要能直接用于复核。

验收：go test ./... 的 15 条用例全过，不得改测试、跳过或按固定输入写死；并发复核与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-b-02`
- 实现文件：`dns_rebind_pin.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`RebindPin`
