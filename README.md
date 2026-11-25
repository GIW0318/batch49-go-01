# TLS 会话恢复缓存的双轨治理

## 题面

门户做灰度升级后，TLS 会话恢复出现两种相反的投诉：一部分客户端拿着旧 ticket 反复恢复成功，恢复出来的 ALPN 和密码套件却和第一次连接不一致，业务层收到半截请求；另一部分客户端在换了密钥的节点上直接被断开，日志里只有一句解密失败。请落点模块 gsb/batch49-go-safety-b-09，实现文件 tls_resume_cache.go，类型 ResumeCache，把会话 ID 缓存与 ticket 密钥环的双轨管理补齐。

公开签名保持不变：NewResumeCache(cfg Config) *ResumeCache；Store(sess Session) (TicketID, error)；Lookup(id TicketID) (Session, bool)；LookupBySessionID(id string) (Session, bool)；Resume(id TicketID, hello ClientHello) (Session, bool)；Renew(id TicketID) (TicketID, error)；RotateKey() error；InvalidateSNI(sni string) int；PurgeExpired() int；SNIs() []string；KeyGenerations() []int；Stats() Stats；Snapshot() ([]byte, error)；Restore([]byte) error。

要修：

1. Store 同时写入会话 ID 缓存与 ticket 解密记录两轨，返回非空 ticket；SNI、ALPN、密码套件族任一为空都报 ErrBadSession，且两轨都不得留下半条记录。
2. ticket 记录绑定签发时的密钥代；Lookup 命中计 hit，未命中计 miss，按会话 ID 查询与按 ticket 查询共享同一份命中计数。
3. Resume 逐项比对 SNI、ALPN、密码套件族与客户端证书主体，四项全部一致才允许恢复并计 resume。
4. ticket 能解密但上述绑定参数任一项不一致时，必须静默降级为全握手：返回零值与 false，不计 resume，也不返回错误终止连接，只计 degraded。
5. 过期 ticket 直接拒绝并从两轨删除，计 expired；PurgeExpired 批量清理全部过期条目并返回删除条数，活条目不受影响。
6. 密钥环只保留当前与上一代，密钥代从 1 起编号；轮换两次后更早代的 ticket 必须失效并被清理，KeyGenerations 返回仍保留的代并按升序排列。
7. 轮换后旧代 ticket 仍可恢复，但不得据此续签当前代 ticket：Renew 遇到旧代报 ErrStaleTicket，客户端必须重新完成一次全握手才能拿到当前代 ticket。
8. 每 SNI 配额打满时只淘汰该 SNI 最久未使用的条目，不得波及其它 SNI 的命中，也不得按全局顺序误伤别的服务器名。
9. 全局容量按插入顺序淘汰最久条目；淘汰后两轨索引必须同时消失，不能出现按会话 ID 还能查到已删记录的情况。
10. InvalidateSNI 删除该 SNI 的全部会话并返回条数；SNIs 按字典序返回仍有会话的服务器名，空 SNI 删除返回零。
11. 并发 Store 在全局容量上不得超发：容量为五时 60 个来自不同 SNI 的并发写入全部成功，最终恰好保留五条，其余全部计入淘汰。
12. 快照保存两轨索引、密钥环、插入顺序与全部计数；恢复损坏载荷报 ErrBadSnapshot 且现网不变，恢复后 ticket、密钥代与淘汰顺序必须一致；Stats 汇总两轨命中、未命中、恢复、降级、过期、淘汰、轮换次数与当前密钥代。

硬约束：Store 的配额检查、淘汰与双轨写入必须原子，并发下不得超容量；快照与读写互斥，快照只能看到全量旧状态或全量新状态；重启恢复后密钥代不得回退，已失效的 ticket 不得复活。验收口径：go test ./... 的 15 条用例必须全过，不得修改或跳过测试、不得按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-b-09`
- 实现文件：`tls_resume_cache.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`ResumeCache`
