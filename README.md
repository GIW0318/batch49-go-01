# WebSocket 鉴权与消息注入防护

## 题面

客服侧的内嵌会话窗口上线后，接入层同时收到两类反馈：一类是页面明明已经登录，消息发出去却一直提示未认证；另一类是伪造的回调地址也能连上来，抓包里还看到消息正文混着换行头、原型污染字段和深到几十层的嵌套 JSON，逐条落成了下游的业务错误。改动落在模块 gsb/batch49-go-fix-a-12 的 ws_auth_inject_guard.go，类型 WsAuthInjectGuard 负责 Origin 校验、一次性票据鉴权、入站消息注入筛查与出站转义。公开接口如下：NewWsAuthInjectGuard(cfg Config) (*WsAuthInjectGuard, error)、Open(connID, origin string) (string, error)、Authenticate(connID string, t Ticket) (Session, error)、Inbound(connID string, seq uint64, payload []byte) (Message, error)、Outbound(connID string, msg Message) ([]byte, error)、Close(connID string, code int) error、State(connID string) (string, error)、RevokeSubject(subject string) int、Stats() GuardStats、UsedTicket(value string) bool、Snapshot() ([]byte, error)、Restore(data []byte) error。

行为清单如下。

一、配置里的消息字节上限、嵌套深度、key 长度、鉴权超时、票据有效期、连接数、速率条数与窗口都必须为正，允许 Origin 列表不能为空，否则 ErrBadConfig。

二、Open 校验 Origin：允许列表与请求都先转小写，http 的 80 与 https 的 443 视为省略端口，末尾斜杠忽略；不匹配报 ErrOriginDenied。

三、Open 的连接号空白报 ErrBadConn，重复打开报 ErrConnOpen，连接数达到 MaxConns 报 ErrConnLimit；成功时返回一次性 nonce，不同连接不得复用同一个 nonce；State 对未知连接报 ErrUnknownConn。

四、Authenticate 的票据必须携带同连接号与同 nonce，否则 ErrTicketMismatch；票据已过期报 ErrTicketExpired；同一票据值第二次出现一律报 ErrTicketReused，无论连接当前处于什么状态。

五、鉴权耗时超过 AuthTimeoutMs 报 ErrAuthTimeout 并把连接置为 closed；票据值或 subject 空白的票据报 ErrBadTicket。

六、未鉴权连接发送业务消息报 ErrNotAuthenticated；已关闭或正在关闭的连接报 ErrConnClosed；subject 被撤销的连接报 ErrRevoked。

七、入站先查字节上限，超过 MaxMessageBytes 报 ErrMessageTooLarge 且连接状态不变；再查裸字节，出现 CR 或 LF 报 ErrHeaderInjection，出现 C0 控制字符（制表符除外）报 ErrControlChar。

八、payload 必须是 JSON 对象，否则 ErrBadPayload；对象嵌套深度超过 MaxDepth 报 ErrTooDeep；任一 key（含嵌套层）超过 MaxKeyLen 报 ErrLongKey；出现 __proto__、constructor、prototype、$where 报 ErrReservedField；通过筛查的消息按原字节填入 Message，Kind 记为 text，Seq 取本次序号。

九、每条连接的入站序号从 1 开始严格递增，跳号或重复报 ErrBadSequence 并把连接置为 closed，已接受的序号不回退。

十、每连接在 RateWindowMs 窗口内的入站条数受 RateLimit 硬顶，超限报 ErrRateLimited，窗口推进后额度恢复。

十一、Outbound 的 text 帧要把 & < > 双引号与单引号转义成实体，已经转义过的文本再次编码不得二次转义；binary 帧走 base64；未鉴权、已关闭或被撤销的连接一律报错。

十二、RevokeSubject 把该 subject 的全部已鉴权连接置为 closing 并返回条数，重复调用返回 0；快照覆盖连接状态、nonce、已用票据与撤销主体，损坏快照报 ErrBadSnapshot，恢复后已用票据不能再用。

硬约束：同一连接的 Inbound 必须串行，序号与速率计数不能被并发写乱；并发 Authenticate 同一个票据只能成功一次，其余全部报 ErrTicketReused；进程重启后凭快照恢复，已用票据集合仍在，不能出现同一票据二次通过鉴权；连接数、每连接速率额度与已用票据集合都是硬顶，越线只能拒绝，不能静默扩容。

验收要求：go test ./... 的 15 条用例全部通过，测试文件不得改动、跳过或按固定 payload 写死；并发鉴权与快照恢复各验一次；只交付本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-fix-a-12`
- 实现文件：`ws_auth_inject_guard.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`WsAuthInjectGuard`
