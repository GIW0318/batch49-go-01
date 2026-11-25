# 通行密钥凭据的跨区复制与撤销传播

## 题面

周三下午的故障复盘会上，身份平台把三条看似无关的事故摆在一起：欧洲区切到法兰克福副本后，一批刚在美西登记的通行密钥全部提示凭据不存在；一名员工的笔记本丢失，主区已经吊销了那把钥匙，新加坡节点却在收到吊销通知之前又放行两次；还有一把钥匙的签名计数器在两个区之间来回跳，克隆器显然已经跑起来了。根因不复杂：通行密钥只在登记区落盘，副本异步补数，撤销又要跨区转发，中间没有任何水位约束。改动落在 gsb/batch49-go-fix-a-01 的 passkey_region_fabric.go，类型 PasskeyRegionFabric。关键签名保持：NewPasskeyRegionFabric(cfg FabricConfig) *PasskeyRegionFabric、Register(homeRegion string, cred Credential) (ReplicaSet, error)、Replicate(fromRegion, toRegion string, limit int) (int, error)、Authenticate(region string, a Assertion) (LoginGrant, error)、Revoke(credentialID, reason string) (RevocationStamp, error)、Propagate(region string, upToEpoch int64) (int, error)、Status(region string) RegionStatus、Credential(credentialID string) (Credential, bool)、Regions() []string、Snapshot() ([]byte, error)、Restore(data []byte) error。

要修的点：一、登记只能发生在主区，homeRegion 与配置不符报 ErrNotHomeRegion；凭据号、用户号、RP ID 任一为空报 ErrBlankField；RP ID 不等于主区 RP 报 ErrRPIDMismatch；被拒请求不得写入记录，也不得消耗纪元。二、同一凭据号重复登记报 ErrDuplicateCredential，已有记录与水位保持原样。三、首次登记分配从 1 起单调递增的纪元，状态为 active，返回的 ReplicaSet 只含主区，AAGUID、计数器与备份资格原样保留。四、Replicate 只允许从主区发出，源不是主区报 ErrNotHomeRegion，目标未配置报 ErrUnknownRegion；按纪元升序复制，limit 小于等于 0 表示全部，返回实际写入条数并把目标水位推进到最后一条。五、重复复制已存在的记录必须跳过并返回 0，不得重排水位，也不得产生重复条目。六、待复制条数超过目标剩余容量时整批拒绝并报 ErrCredentialLimit，不做部分写入。七、Authenticate 的区域未配置报 ErrUnknownRegion，凭据不在主区台账报 ErrCredentialNotFound，断言用户号与归属不符报 ErrUserMismatch，RP ID 不符报 ErrRPIDMismatch；副本水位低于凭据纪元报 ErrRegionLagging，落后超过 AckWindow 的区域为 read_only 并报 ErrRegionReadOnly。八、签名计数器必须严格递增，相等或回退报 ErrCounterReplay；同一凭据反复回退累计到 ReplayTolerance 后转 quarantined，此后即使计数器前进也不得放行，报 ErrCredentialQuarantined。九、Revoke 在主区台账生效，未知凭据报 ErrCredentialNotFound，重复吊销幂等并返回首次的撤销纪元与原因，不新增墓碑；撤销后主区登录立即拒绝。十、Propagate 把不高于 upToEpoch 的墓碑投递给指定区域，超过主区最新纪元按最新纪元截断，返回新增墓碑数，重复调用返回 0；墓碑送达后该区登录必须拒绝。十一、即使副本尚未收到墓碑，主区台账里已经撤销的凭据在任何区域都不得放行。十二、Status 给出水位、记录数、墓碑数与模式，主区与追平副本为 active，落后不超过 AckWindow 为 lagging，超出为 read_only；Snapshot 覆盖凭据表、墓碑表、各区水位、纪元与登录序号，Restore 整份替换，损坏快照报 ErrBadSnapshot 且不动现网，恢复后新纪元必须大于恢复前最大纪元。

硬约束：并发登记、复制与登录必须串行裁决，副本容量只放行配置条数；快照与在线请求互斥，快照只能看到全量旧状态或全量新状态；纪元与登录序号跨重启单调，恢复后不得回退。验收以 go test ./... 的 15 条用例全绿为准，测试不许改、不许跳过、不许按固定输入写死；并发与重启恢复自行验证；只做本地实现，交付源码、跑测记录和一页以内说明。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-fix-a-01`
- 实现文件：`passkey_region_fabric.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`PasskeyRegionFabric`
