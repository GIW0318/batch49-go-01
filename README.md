# 备用找回通道登记与主通道冷却治理

## 题面

客服转来一批账号找回工单：用户补登的备用邮箱一直停在待验证，凭证发了三次之后再发就提示通道不可用；有人的备用手机开始退信，之后找回页面里这条通道直接消失，运维在后台也查不到它退过信；还有人把主找回通道从邮箱换成手机，切换那一刻旧邮箱就没再收到任何安全通知，事后追责时说不清是谁改的、什么时候改的。

问题落在 gsb/batch49-go-safety-a-06 的 recovery_channel_registry.go，类型 RecoveryChannelRegistry，负责备用邮箱与手机通道的登记、验证和可用性维护。需要保持的公开签名：NewRecoveryChannelRegistry(Options) *RecoveryChannelRegistry、Add(userID string, ch Channel) (Pending, error)、Verify(userID, channelID, proof string) error、Remove(userID, channelID string) error、SetPrimary(userID, channelID string) error、Primary(userID string) (Channel, error)、Usable(userID string) ([]Channel, error)、Channels(userID string) []Channel、MarkBounce(channelID, detail string) error、Notices(userID string) []Notice、Snapshot() ([]byte, error)、Restore([]byte) error。

需要做到：
一、登记成功即进入未验证态，返回的 Pending 必须带通道号、待投递凭证和剩余尝试次数；账号或通道号为空报 ErrBlankIdentity，通道类型不在邮箱、手机之内报 ErrBadChannelKind，通道号全库唯一，重复登记报 ErrChannelExists，单账号通道数超上限报 ErrChannelLimit；登记顺序号单调递增，通道列表按登记先后返回。
二、凭证不符报 ErrBadProof 并累加该通道的尝试次数，尝试次数要能在通道记录上读到；凭证正确才转入生效态；允许次数用完报 ErrVerifyLocked，此后凭证正确也不放行。
三、退信细节为空报 ErrBadDetail，未知通道报 ErrChannelNotFound；退信通道转为退信态、不参与找回选择，但登记信息与退信细节必须留档，不能静默删除；重新验证通过后可直接恢复生效，不必重新登记。
四、把主通道改到未验证通道报 ErrNotVerified，未知通道报 ErrChannelNotFound；主通道变更要经过冷却期，冷却期内旧通道仍接收通知但不再作为找回首选，冷却结束才真正切换；冷却期内重复提交以最后一次为准。
五、账号至少保留一条生效通道，移除最后一条要拒绝并报 ErrLastChannel；移除未知或已移除的通道报 ErrChannelNotFound；移除只改变状态，通道号不复用。
六、Usable 只返回生效通道并按登记先后升序；首个生效通道不会自动成为主通道，未指定时 Primary 报 ErrNoPrimary。
七、通知按发生顺序归档，每条带账号、通道、事由与发生时间；一个账号的通知不能被另一个账号读到。
八、快照要带上通道、尝试次数、待投递凭证和主通道冷却的剩余时间，重启后冷却按剩余时间继续而不是重新计时；损坏快照报 ErrBadSnapshot，现网状态不能被改动。
九、同一通道号并发登记只允许一条成功，其余报 ErrChannelExists；快照与写操作互斥，读到的要么是全部旧状态要么是全部新状态；所有时间判断统一走 Options.Now。
十、每个账号的通道、通知与尝试计数互相隔离，一个账号的退信不影响另一个账号的通道；查一个没有通道的账号要返回空列表而不是报错。

验收：go test ./... 15 条必须全过，不得修改测试、跳过或按固定输入写死；并发与重启场景自行验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-a-06`
- 实现文件：`recovery_channel_registry.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`RecoveryChannelRegistry`
