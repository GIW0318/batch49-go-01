# 多跳代理链的规划、租约与出口黏性

## 题面

出站代理换成分跳链之后，支付回调开始整片超时：一条链上第二跳挂了，规划器没有重建，后续请求还在往死节点上送；另一批工单是跨域被误判，同一个注册域换了端口就被当成新目标，出口跟着乱跳，对端风控直接封了一批 IP；安全团队还发现，租约释放之后迟到的结果会把新链的配额和成功率一起改掉。更让人担心的是凭据：一条链里前面的跳点没有认证，后面却带着凭据出去，凭据被送到了不该去的地方。

改动落在模块 gsb/batch49-go-safety-b-03 的 proxy_chain_planner.go，类型 ChainPlanner 负责代理链规划与租约执行。核心签名保持：NewChainPlanner(cfg PlannerConfig) *ChainPlanner、RegisterHop(h Hop) error、Hop(id string) (Hop, bool)、Hops() []Hop、Plan(req Request) (Route, error)、Acquire(route Route) (Lease, error)、Report(leaseID string, out Outcome) error、Release(leaseID string) error、RouteFor(domain string) (Route, bool)、Lease(leaseID string) (Lease, bool)、ActiveLeases() []Lease、Stats(domain string) Stats、Snapshot() ([]byte, error)、Restore([]byte) error。

需要落地的行为如下。

一、注册 hop 时空 id 或空地址报 ErrBlankHop，空方案报 ErrBlankScheme，重复 id 报 ErrHopExists；注册表按 id 升序可列出，未知 id 读不到。

二、规划时空域名报 ErrBlankDomain，候选 hop 为空报 ErrNoHops，引用未注册 hop 报 ErrUnknownHop，且这几种拒绝都不得发布路由。

三、规划阶段按规范化后的地址做环检测，同一个地址被两个 hop 复用或同一个 hop 被重复引用都报 ErrHopLoop。

四、hop 条数超过配置上限报 ErrHopLimit，不得截断；正常链保持请求给出的顺序，出口取最后一跳的地址。

五、同一个凭据引用出现在两个不同安全域的 hop 上时报 ErrCredentialReuse；声明了认证方案却没有凭据引用的 hop 报 ErrBlankCredRef；整条链的认证作用域要在规划阶段就判定完，不能拖到运行期才发现。

六、规划成功发布一条带代际的路由，代际从 1 起；同域后续规划复用该路由的代际，不重建。

七、签发租约时路由必须是当前发布的那一条，未知路由报 ErrUnknownRoute，代际落后的路由报 ErrStaleRoute。

八、单个域的活跃租约数受配置上限约束，超出报 ErrLeaseLimit；租约要带上路由 id、域、出口与代际。

九、成功的租约上报计入成功数并关闭租约，重复上报同一租约报 ErrUnknownLease；释放后再上报同样报 ErrUnknownLease。

十、失败的租约上报计入失败数并触发链重建，重建后的路由代际加一并换新 id，旧路由不能再签发租约。

十一、统计按域分别记账，未被规划的域统计全为零；释放租约不改动成功与失败计数。

十二、快照要覆盖 hop 注册表、路由、租约与统计；损坏或空快照报 ErrBadSnapshot，且不影响在线路由与在途租约。

硬约束：同一域的签发与统计记账必须串行，并发签发时活跃租约数不得超过上限，也不得出现两条同号的租约；进程重启后路由代际、在途租约与统计要原样恢复，恢复后继续签发不能复用已发出的租约号。

验收：go test ./... 的 15 条用例全过，不得改测试、跳过或按固定输入写死；并发签发与重启恢复自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-b-03`
- 实现文件：`proxy_chain_planner.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`ChainPlanner`
