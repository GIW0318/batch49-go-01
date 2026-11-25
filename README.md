# 不可能旅行检测：机房出口排除、乱序事件重算与常用地白名单

## 题面

早上九点，一个账号先在法兰克福登录，四分钟后从圣保罗登录，两处都成功，只有一封异地登录邮件躺在收件箱里没人看。事后复核还发现，某些跨洋跳变其实是机房出口地址，业务同学把它当成了真实位置；另有一批事件因为消息队列乱序，被算成了一次次瞬移，白白惊动了半个客服组。请在模块 gsb/batch49-go-safety-a-30 的 impossible_travel_detector.go 中补全类型 ImpossibleTravelDetector，签名保持：NewImpossibleTravelDetector(Config) (*ImpossibleTravelDetector, error)、Observe(e LoginEvent) (Verdict, error)、Verdicts(userID string) []Verdict、MarkKnownLocation(userID, place string) error、KnownLocations(userID string) []string、Dismiss(verdictID, actorID, reason string) error、WindowSize(userID string) int、DistanceKm(a, b LoginEvent) (float64, error)、Snapshot() ([]byte, error)、LoadSnapshot([]byte) error。

要落地的行为：

1. 阈值或各项上限不为正时构造失败返回 ErrBadConfig；用户为空、地点为空、纬度超出正负九十、经度超出正负一百八十、来源类型不认识，都返回 ErrBadEvent。
2. 每个用户维护最近若干条事件，窗口长度受 MaxEventsPerUser 硬顶，WindowSize 如实返回当前条数。
3. 首条事件不产生旅行判定，距离与速度都是零，原因记为无异常。
4. 相邻两条事件按大圆距离除以时间差算速度，北京到上海约一千零六十七公里，北京到西安约九百零五公里，允许正负三十公里误差；DistanceKm 单独调用也要给出同一口径的距离，非法坐标返回 ErrBadEvent。
5. 速度超过阈值判不可能旅行，未超过不判；判定记录带起点、终点、距离、速度与自增序号。
6. 每条判定无论是否命中都要留档，单用户判定记录数受 MaxVerdictsPerUser 硬顶，只保留最新的几条。
7. 常用地白名单按用户登记，地点为空返回 ErrBlankPlace，用户为空返回 ErrBadEvent，登记数受 MaxKnownLocations 硬顶，超限返回 ErrLocationLimit，重复登记同一地点按幂等处理。
8. 起点与终点都在常用地里的一对事件不判旅行，原因记为白名单豁免。
9. 人工排除必须带操作人与原因，任一为空返回 ErrBlankField，未知判定返回 ErrUnknownVerdict，重复排除返回 ErrAlreadyDismissed，排除后记录仍要保留。
10. 机房或 VPN 出口事件不参与速度计算，但结果里要标注来源类型，不能静默丢弃。
11. 乱序到达的事件按事件时间重算，不得按到达顺序误判成旅行。
12. 常用地白名单只豁免该地对，不豁免它与第三方地点组成的对。

硬约束：同一用户的事件窗口与判定表在并发写入下必须保持一致，不能出现半条窗口或重复判定；进程重启后事件窗口、常用地、排除标记与序号全部延续，损坏快照返回 ErrBadSnapshot 且不改动现网；事件窗口与判定记录都受上面两条上限硬顶，长期在线用户的驻留内存不随登录次数增长。

验收：go test ./... 的 15 条用例全过，测试不得修改、跳过或按固定输入写死；并发写入与重启恢复自行验证，只做本地实现，交付源码与跑测记录。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-a-30`
- 实现文件：`impossible_travel_detector.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`ImpossibleTravelDetector`
