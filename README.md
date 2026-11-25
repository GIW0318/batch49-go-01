# 魔法链接签发与兑换

## 题面

员工点开邮件里的登录链接，页面提示链接已使用，可他其实只是从公司邮件安全网关点开了一下；另一个案例是重发链接之后，早先那封邮件里的旧链接依然能登进去。客服现在只能手工把会话踢下线，用户还会反复收到同一封邮件的多份副本。落点 gsb/batch49-go-safety-a-20 的 magic_link_dispenser.go，类型 MagicLinkDispenser，签名保持：NewMagicLinkDispenser(opts Options) *MagicLinkDispenser、Send(email, returnTo string) (Link, error)、Preview(token string) (Landing, error)、Redeem(token, bindCookie string) (Session, error)、Resend(email string) (Link, error)、Revoke(email string) error、Links(email string) []Link、NormalizeEmail(email string) (string, error)、MaskEmail(email string) string、Snapshot() ([]byte, error)、Restore(data []byte) error。

要修的行为：邮箱先去空白转小写，并要求恰好一个 @、本地部分与域名非空，空邮箱报 ErrBlankEmail，格式不对报 ErrBadEmail；跳转地址只允许站内白名单前缀，必须是单斜杠开头的相对路径，含两点上跳、双斜杠开头或带协议的地址一律报 ErrReturnTo；每次发送生成一次性的不可预测令牌与绑定 Cookie，返回签发时刻、过期时刻与重发次数，同一个邮箱的链接序号递增，在冷却窗口内再发报 ErrCooldown；单邮箱活跃链接数超过 Options.MaxActiveLinksPerEmail 报 ErrActiveLinkLimit 且不签发；Preview 只渲染确认页，返回确认信息、确认提交路径与打码邮箱，不得改变链接状态，未知令牌报 ErrTokenNotFound，对已兑换的令牌报 ErrTokenUsed，对已撤销的令牌报 ErrTokenRevoked；邮件安全扫描器预取确认页不消耗链接，只有确认提交才兑换；Redeem 成功后返回会话并带会话过期时刻，同一令牌第二次兑换报 ErrTokenUsed，会话标识不可预测且不得与令牌相同；浏览器绑定的 Cookie 缺失或不匹配要单独报 ErrBrowserMismatch，不能报成已使用；令牌过期后兑换报 ErrTokenExpired，与未知令牌区分开；Resend 为同一邮箱签发新令牌并让该邮箱此前所有链接立即失效，即便它们尚未到期，没有活跃链接时重发报 ErrNoActiveLink；Revoke 让该邮箱当前所有链接不可用，对没有链接的邮箱按幂等成功处理，Links 按签发先后升序返回，已兑换与已撤销的链接也保留在列表里供排查，返回的是副本；MaskEmail 保留本地部分首字符与末字符、域名原样，单字符本地部分只保留首字符，令牌与绑定 Cookie 一一对应。

硬约束：同一邮箱的签发、重发、兑换与撤销在同一把锁内串行，并发发送只放行配置允许的活跃条数，其余报错，不得先写后删；快照要带走未兑换的令牌、原过期时刻、绑定 Cookie 与序号水位，恢复后未兑换链接仍按原过期时间判定，已兑换的不得复活；损坏快照报 ErrBadSnapshot，且不得改动现网链接。

验收：go test ./... 共 15 条全部通过，测试不得修改、跳过或按固定输入写死；并发发送与快照恢复自行验证；只做本地实现，交付源码与跑测记录。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-a-20`
- 实现文件：`magic_link_dispenser.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`MagicLinkDispenser`
