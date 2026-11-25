# batch49-go-safety-a-12

- 模块路径：`gsb/batch49-go-safety-a-12`
- 包名：`task`
- 主文件：`identity_replica_stream.go`
- 验收用例：`tests/test_identity_replica_stream_test.go`

## 运行方式

在包根执行：

```bash
go test ./...
```

## 题面

工单回溯：上个月一次跨区域割接后，用户在主区域删除的身份记录在旧区域又被读了出来，客服按旧记录给已注销账号恢复了权限；同一个变更因为网络重投被处理两次，审计里出现两条一模一样的记录；更糟的是一个落后半小时的区域仍然接受本地改名，回切时把新主上的高版本数据覆盖掉了。复制逻辑位于模块 gsb/batch49-go-safety-a-12，主文件 identity_replica_stream.go，入口类型 IdentityReplicaStream，请把增量复制、墓碑与降级行为补齐到可交付状态。

版本由纪元与计数器两段组成，先比纪元再比计数器，两段都必须为正。每个区域登记后才有自己的水位、记录表和审计日志；一批变更必须按版本严格递增，空批次、超限批次、未知操作、空身份、非法版本都要整批拒绝且不留下任何副作用。版本已经处理过的变更重复投递时按幂等跳过，不得再写第二条审计。

删除以墓碑传播：墓碑版本高于该身份现有版本，读侧对落后区域返回已删除视图；墓碑之下的变更一律拒绝，删除后同号重建必须使用更高版本，旧副本不能把记录复活成删除前的样子。区域落后全局水位超过阈值时降级为只读，本地写必须明确报错而不是悄悄排队。本区域写入只影响本区域，且版本必须高于本区域水位。

需要落到的行为：
1. 区域注册校验空号与重复注册，未注册区域的所有操作都报错。
2. 应用批次要返回已应用数、重复数、陈旧数和本次水位。
3. 批内版本必须严格递增，任何回退或相等都整批失败。
4. 空批次、超过批量上限、未知操作、空身份、非法版本分别拒绝。
5. 应用成功后水位推进到批内最大版本，并写入审计。
6. 本区域写入推进本区域水位，来源记为本区域，不影响其它区域。
7. 本区域写入版本低于或等于本区域水位时拒绝。
8. 墓碑建立后读侧返回删除视图，并带墓碑版本。
9. 低于墓碑版本的变更必须被拒绝，墓碑版本本身不能回退。
10. 落后距离按全局水位与本区域水位之差计算，供只读判断使用。
11. 审计按区域保留最近若干条，序号从 1 起严格递增。
12. 检查点与快照可恢复水位、记录和审计，损坏载荷整次失败且不破坏现有状态。

关键接口：
- NewIdentityReplicaStream(limits Limits) *IdentityReplicaStream
- CompareVersion(a, b Version) int
- (s *IdentityReplicaStream) RegisterRegion(regionID string) error
- (s *IdentityReplicaStream) Apply(regionID string, batch []Change) (ApplyResult, error)
- (s *IdentityReplicaStream) Write(regionID, userID, name string, version Version) error
- (s *IdentityReplicaStream) Watermark(regionID string) (Version, error)
- (s *IdentityReplicaStream) Lag(regionID string) (int64, error)
- (s *IdentityReplicaStream) GlobalVersion() Version
- (s *IdentityReplicaStream) Tombstone(id string, version Version) error
- (s *IdentityReplicaStream) Read(regionID, userID string) (Record, error)
- (s *IdentityReplicaStream) ReadOnly(regionID string) (bool, error)
- (s *IdentityReplicaStream) Audit(regionID string) []AuditEntry
- (s *IdentityReplicaStream) Checkpoint(regionID string) ([]byte, error)
- (s *IdentityReplicaStream) Resume(regionID string, cp []byte) error
- (s *IdentityReplicaStream) Snapshot() ([]byte, error)
- (s *IdentityReplicaStream) Restore(data []byte) error

硬约束：并发应用同一区域的不同版本时水位取最大值、审计条数与生效条数一致，不能互相覆盖；重启从检查点续放时不得把已应用的变更再算一次；所有上限取自构造时的 Limits；整批失败必须保持原子。验收口径是在包根执行 go test ./... 全部通过，不得修改或跳过用例，不得针对固定输入写死结果，并发与重启场景自行验证，只使用标准库并在本地完成。
